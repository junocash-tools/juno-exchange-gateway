package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

var (
	idempotencyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	attemptIDRE   = regexp.MustCompile(`^txn_[0-9a-f]{32}$`)
	txIDRE        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	noteIDRE      = regexp.MustCompile(`^[0-9a-f]{64}:(0|[1-9][0-9]*)$`)
)

type Output struct {
	ToAddress string `json:"to_address"`
	AmountZat string `json:"amount_zat"`
	MemoHex   string `json:"memo_hex,omitempty"`
}

type CreateRequest struct {
	WalletID          string   `json:"wallet_id"`
	ApprovalReference string   `json:"approval_reference"`
	Outputs           []Output `json:"outputs"`
}

type Attempt struct {
	AttemptID                  string    `json:"attempt_id"`
	State                      string    `json:"state"`
	WalletID                   string    `json:"wallet_id"`
	ApprovalReference          string    `json:"approval_reference"`
	ChangeAddress              string    `json:"change_address,omitempty"`
	PlanDigest                 string    `json:"plan_digest,omitempty"`
	FeeZat                     string    `json:"fee_zat,omitempty"`
	ExpiryHeight               int64     `json:"expiry_height,omitempty"`
	SelectedNoteIDs            []string  `json:"selected_note_ids,omitempty"`
	TxID                       string    `json:"txid,omitempty"`
	RawTxHex                   string    `json:"raw_tx_hex,omitempty"`
	OrchardOutputActionIndices []uint32  `json:"orchard_output_action_indices,omitempty"`
	OrchardChangeActionIndex   *uint32   `json:"orchard_change_action_index,omitempty"`
	Error                      *APIError `json:"error,omitempty"`
	CreatedAt                  string    `json:"created_at"`
	UpdatedAt                  string    `json:"updated_at"`
}

type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

type txPlan struct {
	Version       string     `json:"version"`
	Kind          string     `json:"kind"`
	WalletID      string     `json:"wallet_id"`
	CoinType      uint32     `json:"coin_type"`
	Account       uint32     `json:"account"`
	Chain         string     `json:"chain"`
	ExpiryHeight  uint32     `json:"expiry_height"`
	Outputs       []Output   `json:"outputs"`
	ChangeAddress string     `json:"change_address"`
	FeeZat        string     `json:"fee_zat"`
	Notes         []planNote `json:"notes"`
}

type planNote struct {
	NoteID string `json:"note_id"`
}

type planResult struct {
	Bytes           []byte
	Digest          string
	FeeZat          string
	ExpiryHeight    int64
	SelectedNoteIDs []string
}

type signerResult struct {
	TxID                       string
	RawTxHex                   string
	FeeZat                     string
	OrchardOutputActionIndices []uint32
	OrchardChangeActionIndex   *uint32
	Replay                     bool
}

func normalizeCreateRequest(req CreateRequest, cfg config.Config, wallet config.Wallet) (CreateRequest, []byte, string, error) {
	req.WalletID = strings.TrimSpace(req.WalletID)
	req.ApprovalReference = strings.TrimSpace(req.ApprovalReference)
	if req.WalletID != wallet.WalletID {
		return CreateRequest{}, nil, "", errors.New("wallet_id is invalid")
	}
	if req.ApprovalReference == "" || len(req.ApprovalReference) > 128 {
		return CreateRequest{}, nil, "", errors.New("approval_reference must contain 1 to 128 characters")
	}
	if len(req.Outputs) < 1 || len(req.Outputs) > cfg.CoordinatorMaxOutputs {
		return CreateRequest{}, nil, "", fmt.Errorf("outputs must contain between 1 and %d entries", cfg.CoordinatorMaxOutputs)
	}
	var total uint64
	for i := range req.Outputs {
		out := &req.Outputs[i]
		out.ToAddress = strings.TrimSpace(out.ToAddress)
		out.AmountZat = strings.TrimSpace(out.AmountZat)
		memoHex := strings.TrimSpace(out.MemoHex)
		if memoHex != strings.ToLower(memoHex) {
			return CreateRequest{}, nil, "", fmt.Errorf("outputs[%d].memo_hex must be lowercase hexadecimal", i)
		}
		out.MemoHex = memoHex
		if out.ToAddress == "" || !strings.HasPrefix(out.ToAddress, cfg.Network.AddressHRP()+"1") {
			return CreateRequest{}, nil, "", fmt.Errorf("outputs[%d].to_address does not match %s", i, cfg.Network)
		}
		amount, err := parseDecimalZat(out.AmountZat)
		if err != nil || amount == 0 {
			return CreateRequest{}, nil, "", fmt.Errorf("outputs[%d].amount_zat must be a positive base-10 integer", i)
		}
		if amount > uint64(cfg.CoordinatorMaxAmountZat) || total > uint64(cfg.CoordinatorMaxAmountZat)-amount {
			return CreateRequest{}, nil, "", errors.New("outputs exceed the configured maximum amount")
		}
		total += amount
		if len(out.MemoHex) > 1024 || len(out.MemoHex)%2 != 0 {
			return CreateRequest{}, nil, "", fmt.Errorf("outputs[%d].memo_hex must encode at most 512 bytes", i)
		}
		if out.MemoHex != "" {
			if _, err := hex.DecodeString(out.MemoHex); err != nil {
				return CreateRequest{}, nil, "", fmt.Errorf("outputs[%d].memo_hex must be lowercase hexadecimal", i)
			}
		}
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return CreateRequest{}, nil, "", errors.New("encode transaction request")
	}
	sum := sha256.Sum256(canonical)
	return req, canonical, hex.EncodeToString(sum[:]), nil
}

func validatePlan(raw []byte, request CreateRequest, wallet config.Wallet, network domain.Network, changeAddress string) (planResult, error) {
	var plan txPlan
	if len(raw) == 0 || json.Unmarshal(raw, &plan) != nil {
		return planResult{}, errors.New("planner returned invalid TxPlan JSON")
	}
	if plan.Version != "v0" || plan.Kind != "withdrawal" || plan.WalletID != wallet.WalletID || plan.Account != wallet.Account || plan.Chain != network.NodeChain() {
		return planResult{}, errors.New("planner returned a TxPlan with mismatched wallet, account, or network")
	}
	wantCoinType := map[domain.Network]uint32{domain.Mainnet: 8133, domain.Testnet: 8134, domain.Regtest: 8135}[network]
	if plan.CoinType != wantCoinType || plan.ChangeAddress != changeAddress || len(plan.Outputs) != len(request.Outputs) {
		return planResult{}, errors.New("planner returned a TxPlan with mismatched coin type, change, or outputs")
	}
	for i := range request.Outputs {
		if plan.Outputs[i] != request.Outputs[i] {
			return planResult{}, errors.New("planner returned output data different from the approved request")
		}
	}
	if _, err := parseDecimalZat(plan.FeeZat); err != nil || plan.ExpiryHeight == 0 || len(plan.Notes) == 0 {
		return planResult{}, errors.New("planner returned invalid fee, expiry, or selected notes")
	}
	noteIDs := make([]string, 0, len(plan.Notes))
	seen := make(map[string]struct{}, len(plan.Notes))
	for i, note := range plan.Notes {
		if !noteIDRE.MatchString(note.NoteID) {
			return planResult{}, fmt.Errorf("planner returned invalid notes[%d].note_id", i)
		}
		parts := strings.Split(note.NoteID, ":")
		if _, err := strconv.ParseUint(parts[1], 10, 32); err != nil {
			return planResult{}, fmt.Errorf("planner returned out-of-range notes[%d].note_id", i)
		}
		if _, duplicate := seen[note.NoteID]; duplicate {
			return planResult{}, errors.New("planner returned duplicate note IDs")
		}
		seen[note.NoteID] = struct{}{}
		noteIDs = append(noteIDs, note.NoteID)
	}
	sum := sha256.Sum256(raw)
	return planResult{
		Bytes:           append([]byte(nil), raw...),
		Digest:          "sha256:" + hex.EncodeToString(sum[:]),
		FeeZat:          plan.FeeZat,
		ExpiryHeight:    int64(plan.ExpiryHeight),
		SelectedNoteIDs: noteIDs,
	}, nil
}

func attemptView(value storage.TransactionAttempt) Attempt {
	out := Attempt{
		AttemptID:                  value.AttemptID,
		State:                      value.State,
		WalletID:                   value.WalletID,
		ApprovalReference:          value.ApprovalReference,
		ChangeAddress:              value.ChangeAddress,
		PlanDigest:                 value.PlanDigest,
		FeeZat:                     value.FeeZat,
		ExpiryHeight:               value.ExpiryHeight,
		SelectedNoteIDs:            append([]string(nil), value.SelectedNoteIDs...),
		TxID:                       value.TxID,
		RawTxHex:                   value.RawTxHex,
		OrchardOutputActionIndices: append([]uint32(nil), value.OrchardOutputActionIndices...),
		OrchardChangeActionIndex:   value.OrchardChangeActionIndex,
		CreatedAt:                  value.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:                  value.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if value.ErrorCode != "" {
		out.Error = &APIError{Code: value.ErrorCode, Message: value.ErrorMessage, Retryable: value.ErrorRetryable}
	}
	return out
}

func parseDecimalZat(raw string) (uint64, error) {
	if raw == "" || (len(raw) > 1 && raw[0] == '0') {
		return 0, errors.New("invalid decimal amount")
	}
	return strconv.ParseUint(raw, 10, 64)
}
