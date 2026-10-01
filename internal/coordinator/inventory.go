package coordinator

import (
	"context"
	"errors"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

// NoteInventory describes how many notes the coordinator can plan with right
// now. Spendable comes from the scanner; reserved notes are held by active
// attempts from any credential and are not available to new attempts.
type NoteInventory struct {
	WalletID             string                  `json:"wallet_id"`
	MinConfirmations     int64                   `json:"min_confirmations"`
	MinNoteZat           int64                   `json:"min_note_zat"`
	AsOfScannerHeight    int64                   `json:"as_of_scanner_height"`
	Spendable            domain.NoteValueSummary `json:"spendable"`
	Reserved             domain.NoteValueSummary `json:"reserved_spendable"`
	Unreserved           domain.NoteValueSummary `json:"unreserved_spendable"`
	TargetNotes          int                     `json:"target_notes"`
	LowNoteInventory     bool                    `json:"low_note_inventory"`
	ChangeSplitMax       int                     `json:"change_split_max"`
	ReservedByAttempts   []NoteReservationView   `json:"reservations"`
	ReservationsComplete bool                    `json:"reservations_complete"`
}

type NoteReservationView struct {
	NoteID       string `json:"note_id"`
	AttemptID    string `json:"attempt_id"`
	AttemptState string `json:"attempt_state"`
	ExpiryHeight int64  `json:"expiry_height,omitempty"`
	NoteState    string `json:"note_state"`
	ValueZat     *int64 `json:"value_zat,omitempty"`
	ReservedAt   string `json:"reserved_at"`
}

// NoteInventory reports spendable, reserved and unreserved note counts for a
// wallet across every credential.
func (s *Service) NoteInventory(ctx context.Context, walletID string) (NoteInventory, error) {
	if _, ok := s.wallets[walletID]; !ok {
		return NoteInventory{}, opError("not_found", "wallet not found", false)
	}
	summary, err := s.spendableSummary(ctx, walletID)
	if err != nil {
		return NoteInventory{}, err
	}
	reservations, statuses, err := s.reservedNotes(ctx, walletID)
	if err != nil {
		return NoteInventory{}, err
	}
	out := NoteInventory{
		WalletID:             walletID,
		MinConfirmations:     summary.MinConfirmations,
		MinNoteZat:           summary.MinNoteZat,
		AsOfScannerHeight:    summary.AsOfScannerHeight,
		Spendable:            summary.Spendable.NoteValueSummary,
		TargetNotes:          s.cfg.CoordinatorTargetNotes,
		ChangeSplitMax:       s.cfg.CoordinatorChangeSplitMax,
		ReservedByAttempts:   make([]NoteReservationView, 0, len(reservations)),
		ReservationsComplete: true,
	}
	for _, reservation := range reservations {
		view := NoteReservationView{
			NoteID:       reservation.NoteID,
			AttemptID:    reservation.AttemptID,
			AttemptState: reservation.State,
			ExpiryHeight: reservation.ExpiryHeight,
			NoteState:    "unknown",
			ReservedAt:   reservation.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		status, known := statuses[reservation.NoteID]
		if known {
			view.NoteState = status.State
			view.ValueZat = status.ValueZat
			if status.State == "unspent" {
				out.Reserved.NoteCount++
				if status.ValueZat != nil {
					out.Reserved.ValueZat += *status.ValueZat
				}
			}
		} else {
			out.ReservationsComplete = false
		}
		out.ReservedByAttempts = append(out.ReservedByAttempts, view)
	}
	out.Unreserved = domain.NoteValueSummary{
		NoteCount: max(out.Spendable.NoteCount-out.Reserved.NoteCount, 0),
		ValueZat:  max(out.Spendable.ValueZat-out.Reserved.ValueZat, 0),
	}
	if out.TargetNotes > 0 {
		out.LowNoteInventory = out.Unreserved.NoteCount < int64(out.TargetNotes)
	}
	return out, nil
}

func (s *Service) spendableSummary(ctx context.Context, walletID string) (domain.WalletNoteSummary, error) {
	scanCtx, cancel := context.WithTimeout(ctx, s.cfg.UpstreamTimeout)
	defer cancel()
	summary, found, err := s.scanner.NoteSummary(scanCtx, walletID, s.cfg.DefaultConfirmations, s.cfg.CoordinatorMinNoteZat, s.cfg.NoteSummaryMaxNotes)
	if errors.Is(err, domain.ErrNoteSummaryLimitExceeded) {
		return domain.WalletNoteSummary{}, opError("note_summary_limit_exceeded", "wallet has more unspent notes than the configured summary limit", false)
	}
	if err != nil {
		return domain.WalletNoteSummary{}, opError("scanner_unavailable", "scanner note summary is unavailable", true)
	}
	if !found {
		return domain.WalletNoteSummary{}, opError("not_found", "wallet is not known to the scanner", false)
	}
	if !summary.ValidFor(walletID, s.cfg.DefaultConfirmations, s.cfg.CoordinatorMinNoteZat, s.cfg.NoteSummaryMaxNotes) {
		return domain.WalletNoteSummary{}, opError("scanner_unavailable", "scanner returned an invalid note summary", true)
	}
	return summary, nil
}

// reservedNotes returns every active reservation for the wallet and the
// scanner's view of each reserved note, keyed by note ID.
func (s *Service) reservedNotes(ctx context.Context, walletID string) ([]storage.NoteReservation, map[string]domain.NoteStatus, error) {
	reservations, err := s.store.NoteReservations(ctx, string(s.cfg.Network), walletID)
	if err != nil {
		return nil, nil, opError("reservation_state_unavailable", "active note reservations could not be read", true)
	}
	if len(reservations) > maxInventoryReservations {
		return nil, nil, opError("attempt_list_limit_exceeded", "too many active note reservations to report", false)
	}
	statuses := make(map[string]domain.NoteStatus, len(reservations))
	for start := 0; start < len(reservations); start += 200 {
		end := min(start+200, len(reservations))
		ids := make([]string, 0, end-start)
		for _, reservation := range reservations[start:end] {
			ids = append(ids, reservation.NoteID)
		}
		scanCtx, cancel := context.WithTimeout(ctx, s.cfg.UpstreamTimeout)
		batch, found, err := s.scanner.NoteStatuses(scanCtx, walletID, ids)
		cancel()
		if err != nil {
			return nil, nil, opError("scanner_unavailable", "scanner note status is unavailable", true)
		}
		if !found || !batch.ValidFor(walletID, ids) {
			continue
		}
		for _, status := range batch.Statuses {
			statuses[status.NoteID] = status
		}
	}
	return reservations, statuses, nil
}

// changeSplitPolicy describes how far the wallet is below
// CoordinatorTargetNotes. A zero value disables change splitting.
type changeSplitPolicy struct {
	deficit int64
	max     int
}

// options returns the change split for a plan that spends the given number
// of unreserved notes; the change should replace every spent note and make up
// the existing deficit once the transaction is mined.
func (p changeSplitPolicy) options(spends int) planOptions {
	if p.max < 2 {
		return planOptions{}
	}
	want := p.deficit + int64(max(spends, 1))
	if want < 2 {
		return planOptions{}
	}
	return planOptions{SplitChange: int(min(want, int64(p.max)))}
}

// splitPolicy decides how many change notes withdrawals should create so the
// wallet keeps CoordinatorTargetNotes unreserved spendable notes. Any error
// leaves change unsplit; splitting is an optimisation, not a gate.
func (s *Service) splitPolicy(ctx context.Context, request CreateRequest, walletID string) changeSplitPolicy {
	if s.cfg.CoordinatorTargetNotes < 1 || s.cfg.CoordinatorChangeSplitMax < 2 || request.isSplit() {
		return changeSplitPolicy{}
	}
	summary, err := s.spendableSummary(ctx, walletID)
	if err != nil {
		return changeSplitPolicy{}
	}
	reservations, statuses, err := s.reservedNotes(ctx, walletID)
	if err != nil {
		return changeSplitPolicy{}
	}
	reserved := int64(0)
	for _, reservation := range reservations {
		if status, ok := statuses[reservation.NoteID]; ok && status.State == "unspent" {
			reserved++
		}
	}
	unreserved := max(summary.Spendable.NoteCount-reserved, 0)
	return changeSplitPolicy{deficit: int64(s.cfg.CoordinatorTargetNotes) - unreserved, max: s.cfg.CoordinatorChangeSplitMax}
}

// fundableWithoutReservations reports whether the wallet could fund the
// request if no notes were reserved. It plans without reserving anything.
func (s *Service) fundableWithoutReservations(ctx context.Context, request CreateRequest, wallet config.Wallet, changeAddress string) bool {
	planCtx, cancel := context.WithTimeout(ctx, s.cfg.CoordinatorPlanTimeout)
	defer cancel()
	_, err := s.planner.Plan(planCtx, request, wallet, changeAddress, nil, planOptions{})
	return err == nil
}
