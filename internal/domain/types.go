package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
	Regtest Network = "regtest"
)

func ParseNetwork(s string) (Network, error) {
	switch Network(strings.ToLower(strings.TrimSpace(s))) {
	case Mainnet:
		return Mainnet, nil
	case Testnet:
		return Testnet, nil
	case Regtest:
		return Regtest, nil
	default:
		return "", fmt.Errorf("network must be mainnet, testnet, or regtest")
	}
}

func (n Network) AddressHRP() string {
	switch n {
	case Mainnet:
		return "j"
	case Testnet:
		return "jtest"
	case Regtest:
		return "jregtest"
	default:
		return ""
	}
}

func (n Network) UFVKHRP() string {
	switch n {
	case Mainnet:
		return "jview"
	case Testnet:
		return "jviewtest"
	case Regtest:
		return "jviewregtest"
	default:
		return ""
	}
}

func (n Network) NodeChain() string {
	switch n {
	case Mainnet:
		return "main"
	case Testnet:
		return "test"
	case Regtest:
		return "regtest"
	default:
		return ""
	}
}

type NodeTip struct {
	Network              string  `json:"network"`
	Height               int64   `json:"height"`
	Hash                 string  `json:"hash"`
	BlockTime            int64   `json:"block_time"`
	Headers              int64   `json:"headers"`
	InitialBlockDownload bool    `json:"initial_sync"`
	VerificationProgress float64 `json:"verification_progress"`
}

type ScannerHealth struct {
	Status          string `json:"status"`
	Network         string `json:"network,omitempty"`
	UAHRP           string `json:"ua_hrp,omitempty"`
	Confirmations   *int64 `json:"confirmations,omitempty"`
	EventEpoch      string `json:"event_epoch,omitempty"`
	Ready           *bool  `json:"ready,omitempty"`
	NodeHeight      *int64 `json:"node_height,omitempty"`
	ScannedHeight   *int64 `json:"scanned_height,omitempty"`
	ScannedHash     string `json:"scanned_hash,omitempty"`
	ScannerLag      *int64 `json:"scanner_lag,omitempty"`
	MaxReadyLag     *int64 `json:"max_ready_lag,omitempty"`
	HistoryComplete *bool  `json:"history_complete,omitempty"`
}

type Balance struct {
	WalletID           string `json:"-"`
	RecipientAddress   string `json:"-"`
	AvailableZat       int64  `json:"available_zat"`
	PendingIncomingZat int64  `json:"pending_incoming_zat"`
	PendingOutgoingZat int64  `json:"pending_outgoing_zat"`
	TotalUnspentZat    int64  `json:"total_unspent_zat"`
	MinConfirmations   int64  `json:"min_confirmations"`
	AsOfNodeHeight     int64  `json:"as_of_node_height"`
	AsOfScannerHeight  int64  `json:"as_of_scanner_height"`
	ScannerLag         int64  `json:"scanner_lag"`
}

func (b Balance) ValidFor(walletID, recipientAddress string, minConfirmations int64) bool {
	if b.WalletID != walletID || b.RecipientAddress != recipientAddress || b.MinConfirmations != minConfirmations ||
		b.AvailableZat < 0 || b.PendingIncomingZat < 0 || b.PendingOutgoingZat < 0 || b.TotalUnspentZat < 0 ||
		b.MinConfirmations < 0 || b.AsOfNodeHeight < 0 || b.AsOfScannerHeight < 0 || b.ScannerLag < 0 ||
		b.AsOfScannerHeight > b.AsOfNodeHeight || b.ScannerLag != b.AsOfNodeHeight-b.AsOfScannerHeight ||
		b.AvailableZat > b.TotalUnspentZat || b.PendingIncomingZat > b.TotalUnspentZat || b.PendingOutgoingZat > b.TotalUnspentZat {
		return false
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if b.AvailableZat > maxInt64-b.PendingIncomingZat || b.AvailableZat+b.PendingIncomingZat > maxInt64-b.PendingOutgoingZat {
		return false
	}
	return b.AvailableZat+b.PendingIncomingZat+b.PendingOutgoingZat == b.TotalUnspentZat
}

type ScannerEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Height    int64           `json:"height"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type EventsPage struct {
	Events     []ScannerEvent
	NextCursor int64
	EventEpoch string
}

type NoteValueSummary struct {
	NoteCount int64 `json:"note_count"`
	ValueZat  int64 `json:"value_zat"`
}

type SpendableNoteSummary struct {
	NoteValueSummary
	SmallestNoteZat *int64 `json:"smallest_note_zat,omitempty"`
	LargestNoteZat  *int64 `json:"largest_note_zat,omitempty"`
}

type PendingSpendNoteSummary struct {
	NoteValueSummary
	KnownExpiryCount int64  `json:"known_expiry_count"`
	NextExpiryHeight *int64 `json:"next_expiry_height,omitempty"`
	LastExpiryHeight *int64 `json:"last_expiry_height,omitempty"`
}

type WalletNoteSummary struct {
	WalletID           string
	MinConfirmations   int64
	MinNoteZat         int64
	AsOfScannerHeight  int64
	AsOfScannerHash    string
	TotalUnspent       NoteValueSummary
	Spendable          SpendableNoteSummary
	Immature           NoteValueSummary
	PendingSpend       PendingSpendNoteSummary
	BelowMinNote       NoteValueSummary
	WitnessUnavailable NoteValueSummary
}

var ErrNoteSummaryLimitExceeded = errors.New("note summary limit exceeded")

func (s WalletNoteSummary) ValidFor(walletID string, minConfirmations, minNoteZat int64, maxNotes int) bool {
	if s.WalletID != walletID || s.MinConfirmations != minConfirmations || s.MinNoteZat != minNoteZat ||
		minConfirmations < 0 || minNoteZat < 0 || s.AsOfScannerHeight < 0 || !isLowerHex64(s.AsOfScannerHash) || maxNotes < 1 {
		return false
	}
	buckets := []NoteValueSummary{s.Spendable.NoteValueSummary, s.Immature, s.PendingSpend.NoteValueSummary, s.BelowMinNote, s.WitnessUnavailable}
	var count, value int64
	const maxInt64 = int64(^uint64(0) >> 1)
	for _, bucket := range buckets {
		if bucket.NoteCount < 0 || bucket.ValueZat < 0 || (bucket.NoteCount == 0 && bucket.ValueZat != 0) ||
			count > maxInt64-bucket.NoteCount || value > maxInt64-bucket.ValueZat {
			return false
		}
		count += bucket.NoteCount
		value += bucket.ValueZat
	}
	if s.TotalUnspent.NoteCount < 0 || s.TotalUnspent.ValueZat < 0 || s.TotalUnspent.NoteCount > int64(maxNotes) ||
		count != s.TotalUnspent.NoteCount || value != s.TotalUnspent.ValueZat {
		return false
	}
	if s.Spendable.NoteCount == 0 {
		if s.Spendable.SmallestNoteZat != nil || s.Spendable.LargestNoteZat != nil {
			return false
		}
	} else if s.Spendable.SmallestNoteZat == nil || s.Spendable.LargestNoteZat == nil ||
		*s.Spendable.SmallestNoteZat <= 0 || *s.Spendable.SmallestNoteZat < minNoteZat ||
		*s.Spendable.SmallestNoteZat > *s.Spendable.LargestNoteZat || *s.Spendable.LargestNoteZat > s.Spendable.ValueZat ||
		*s.Spendable.SmallestNoteZat > maxInt64/s.Spendable.NoteCount ||
		*s.Spendable.SmallestNoteZat*s.Spendable.NoteCount > s.Spendable.ValueZat ||
		(*s.Spendable.LargestNoteZat <= maxInt64/s.Spendable.NoteCount && *s.Spendable.LargestNoteZat*s.Spendable.NoteCount < s.Spendable.ValueZat) {
		return false
	}
	if s.PendingSpend.KnownExpiryCount < 0 || s.PendingSpend.KnownExpiryCount > s.PendingSpend.NoteCount {
		return false
	}
	if s.PendingSpend.KnownExpiryCount == 0 {
		if s.PendingSpend.NextExpiryHeight != nil || s.PendingSpend.LastExpiryHeight != nil {
			return false
		}
	} else if s.PendingSpend.NextExpiryHeight == nil || s.PendingSpend.LastExpiryHeight == nil ||
		*s.PendingSpend.NextExpiryHeight < s.AsOfScannerHeight || *s.PendingSpend.NextExpiryHeight > *s.PendingSpend.LastExpiryHeight {
		return false
	}
	if s.BelowMinNote.NoteCount == 0 {
		return s.BelowMinNote.ValueZat == 0
	}
	if minNoteZat == 0 {
		return s.BelowMinNote.ValueZat == 0
	}
	maxBelowValuePerNote := minNoteZat - 1
	if maxBelowValuePerNote == 0 {
		return s.BelowMinNote.ValueZat == 0
	}
	return s.BelowMinNote.NoteCount > maxInt64/maxBelowValuePerNote ||
		s.BelowMinNote.ValueZat <= s.BelowMinNote.NoteCount*maxBelowValuePerNote
}

func isLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

type BackfillStatus struct {
	WalletID        string    `json:"wallet_id"`
	UFVKFingerprint string    `json:"ufvk_fingerprint"`
	BirthdayHeight  int64     `json:"birthday_height"`
	NextHeight      int64     `json:"next_height"`
	TargetHeight    int64     `json:"target_height"`
	State           string    `json:"state"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type EventFilter struct {
	Kinds []string
	TxID  string
}

type Transaction struct {
	TxID           string `json:"txid"`
	State          string `json:"state"`
	Confirmations  int64  `json:"confirmations"`
	BlockHash      string `json:"block_hash,omitempty"`
	BlockHeight    *int64 `json:"block_height,omitempty"`
	BlockTime      *int64 `json:"block_time,omitempty"`
	ExpiryHeight   *int64 `json:"expiry_height,omitempty"`
	SerializedSize *int64 `json:"serialized_size,omitempty"`
	OrchardActions *int64 `json:"orchard_action_count,omitempty"`
	RawTxHex       string `json:"raw_tx_hex,omitempty"`
}

type Node interface {
	Tip(context.Context) (NodeTip, error)
	BlockHash(context.Context, int64) (string, error)
	DecodeRawTransaction(context.Context, string) (string, error)
	Transaction(context.Context, string, bool) (Transaction, bool, error)
	Broadcast(context.Context, string) (string, error)
}

type Scanner interface {
	Health(context.Context) (ScannerHealth, error)
	UpsertWallet(context.Context, string, string, int64) error
	BackfillStatus(context.Context, string) (BackfillStatus, bool, error)
	Backfill(context.Context, string, int64, int64) (int64, error)
	Balance(context.Context, string, string, int64, int64) (Balance, bool, error)
	NoteSummary(context.Context, string, int64, int64, int) (WalletNoteSummary, bool, error)
	Events(context.Context, string, int64, int, EventFilter) (EventsPage, error)
}

type Deriver interface {
	Derive(context.Context, string, uint32) (string, error)
}

type UpstreamError struct {
	Kind string
	Err  error
}

func (e *UpstreamError) Error() string {
	if e == nil || e.Err == nil {
		return "upstream error"
	}
	return e.Err.Error()
}

func (e *UpstreamError) Unwrap() error { return e.Err }

func IsUpstreamKind(err error, kind string) bool {
	var target *UpstreamError
	return errors.As(err, &target) && target.Kind == kind
}
