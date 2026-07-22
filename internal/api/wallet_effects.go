package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

type walletEffectSourceNote struct {
	TxID        string `json:"txid"`
	ActionIndex uint32 `json:"action_index"`
	BlockHeight int64  `json:"block_height"`
}

type walletEffect struct {
	EventID                 int64                   `json:"event_id"`
	Kind                    string                  `json:"kind"`
	ObservedHeight          int64                   `json:"observed_height"`
	ObservedAt              time.Time               `json:"observed_at"`
	WalletID                string                  `json:"wallet_id"`
	TxID                    string                  `json:"txid"`
	State                   string                  `json:"state"`
	BlockHeight             *int64                  `json:"block_height,omitempty"`
	Confirmations           *int64                  `json:"confirmations,omitempty"`
	RequiredConfirmations   *int64                  `json:"required_confirmations,omitempty"`
	ConfirmedHeight         *int64                  `json:"confirmed_height,omitempty"`
	RollbackHeight          *int64                  `json:"rollback_height,omitempty"`
	PreviousConfirmedHeight *int64                  `json:"previous_confirmed_height,omitempty"`
	OrphanedAtHeight        *int64                  `json:"orphaned_at_height,omitempty"`
	ExpiryHeight            *int64                  `json:"expiry_height,omitempty"`
	ActionIndex             *uint32                 `json:"action_index,omitempty"`
	AmountZat               *int64                  `json:"amount_zat,omitempty"`
	Address                 string                  `json:"address,omitempty"`
	DiversifierIndex        *uint32                 `json:"diversifier_index,omitempty"`
	MemoHex                 string                  `json:"memo_hex,omitempty"`
	OVKScope                string                  `json:"ovk_scope,omitempty"`
	RecipientScope          string                  `json:"recipient_scope,omitempty"`
	SourceNote              *walletEffectSourceNote `json:"source_note,omitempty"`
}

type rawWalletEffectStatus struct {
	State         *string `json:"state"`
	Height        *int64  `json:"height,omitempty"`
	Confirmations *int64  `json:"confirmations,omitempty"`
}

type rawWalletEffectPayload struct {
	Version          *string                `json:"version"`
	WalletID         *string                `json:"wallet_id"`
	AccountID        *string                `json:"account_id,omitempty"`
	DiversifierIndex *uint32                `json:"diversifier_index,omitempty"`
	TxID             *string                `json:"txid"`
	Height           *int64                 `json:"height,omitempty"`
	ActionIndex      *uint32                `json:"action_index,omitempty"`
	AmountZatoshis   *uint64                `json:"amount_zatoshis,omitempty"`
	MemoHex          *string                `json:"memo_hex,omitempty"`
	Status           *rawWalletEffectStatus `json:"status"`

	Origin           *string `json:"origin,omitempty"`
	RecipientAddress *string `json:"recipient_address,omitempty"`
	NoteNullifier    *string `json:"note_nullifier,omitempty"`

	NoteTxID        *string `json:"note_txid,omitempty"`
	NoteActionIndex *uint32 `json:"note_action_index,omitempty"`
	NoteHeight      *int64  `json:"note_height,omitempty"`

	ExpiryHeight   *int64  `json:"expiry_height,omitempty"`
	OVKScope       *string `json:"ovk_scope,omitempty"`
	RecipientScope *string `json:"recipient_scope,omitempty"`

	ConfirmedHeight         *int64 `json:"confirmed_height,omitempty"`
	RequiredConfirmations   *int64 `json:"required_confirmations,omitempty"`
	OrphanedAtHeight        *int64 `json:"orphaned_at_height,omitempty"`
	RollbackHeight          *int64 `json:"rollback_height,omitempty"`
	PreviousConfirmedHeight *int64 `json:"previous_confirmed_height,omitempty"`
}

func sanitizeWalletEffect(event domain.ScannerEvent, walletID, expectedTxID, addressHRP string, requiredConfirmations int64) (walletEffect, error) {
	var payload rawWalletEffectPayload
	decoder := json.NewDecoder(bytes.NewReader(event.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return walletEffect{}, invalidWalletEffect("payload is not valid v1 JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return walletEffect{}, invalidWalletEffect("payload contains more than one JSON value")
	}
	if event.ID <= 0 || event.Height < 0 || payload.Version == nil || *payload.Version != "v1" ||
		payload.WalletID == nil || *payload.WalletID != walletID || payload.TxID == nil || !txIDPattern.MatchString(*payload.TxID) ||
		(expectedTxID != "" && *payload.TxID != expectedTxID) || payload.Status == nil || payload.Status.State == nil {
		return walletEffect{}, invalidWalletEffect("identity, version, or status is invalid")
	}
	if payload.Height != nil && *payload.Height < 0 || payload.ExpiryHeight != nil && *payload.ExpiryHeight < 0 {
		return walletEffect{}, invalidWalletEffect("height is negative")
	}
	if payload.AmountZatoshis != nil && *payload.AmountZatoshis > uint64(^uint64(0)>>1) {
		return walletEffect{}, invalidWalletEffect("amount exceeds signed 64-bit range")
	}
	if payload.Status.Height != nil && *payload.Status.Height < 0 || payload.Status.Confirmations != nil && *payload.Status.Confirmations < 0 {
		return walletEffect{}, invalidWalletEffect("status height or confirmations is negative")
	}

	out := walletEffect{
		EventID: event.ID, Kind: event.Kind, ObservedHeight: event.Height, ObservedAt: event.CreatedAt,
		WalletID: walletID, TxID: *payload.TxID, State: *payload.Status.State,
		BlockHeight: payload.Height, Confirmations: payload.Status.Confirmations,
		RequiredConfirmations: payload.RequiredConfirmations, ConfirmedHeight: payload.ConfirmedHeight,
		RollbackHeight: payload.RollbackHeight, PreviousConfirmedHeight: payload.PreviousConfirmedHeight,
		OrphanedAtHeight: payload.OrphanedAtHeight, ExpiryHeight: payload.ExpiryHeight,
	}
	if payload.AmountZatoshis != nil {
		amount := int64(*payload.AmountZatoshis)
		out.AmountZat = &amount
	}

	switch event.Kind {
	case "DepositEvent", "DepositConfirmed", "DepositUnconfirmed", "DepositOrphaned":
		if err := validateDepositEffect(event, &payload, addressHRP, requiredConfirmations); err != nil {
			return walletEffect{}, err
		}
		index := *payload.DiversifierIndex
		out.ActionIndex = payload.ActionIndex
		out.DiversifierIndex = &index
		out.Address = strings.TrimSpace(*payload.RecipientAddress)
	case "SpendEvent", "SpendConfirmed", "SpendUnconfirmed", "SpendOrphaned":
		if err := validateSpendEffect(event, &payload, addressHRP, requiredConfirmations); err != nil {
			return walletEffect{}, err
		}
		out.SourceNote = &walletEffectSourceNote{TxID: *payload.NoteTxID, ActionIndex: *payload.NoteActionIndex, BlockHeight: *payload.NoteHeight}
	case "OutgoingOutputEvent", "OutgoingOutputConfirmed", "OutgoingOutputUnconfirmed", "OutgoingOutputOrphaned", "OutgoingOutputExpired":
		if err := validateOutgoingEffect(event, &payload, addressHRP, requiredConfirmations); err != nil {
			return walletEffect{}, err
		}
		out.ActionIndex = payload.ActionIndex
		out.Address = strings.TrimSpace(*payload.RecipientAddress)
		out.OVKScope = strings.TrimSpace(*payload.OVKScope)
		if payload.RecipientScope != nil {
			out.RecipientScope = strings.TrimSpace(*payload.RecipientScope)
		}
		if payload.MemoHex != nil {
			out.MemoHex = strings.TrimSpace(*payload.MemoHex)
		}
	default:
		return walletEffect{}, invalidWalletEffect("kind is not supported")
	}
	return out, nil
}

func validateDepositEffect(event domain.ScannerEvent, payload *rawWalletEffectPayload, addressHRP string, requiredConfirmations int64) error {
	if payload.Origin == nil || *payload.Origin != "external" || payload.Height == nil || payload.ActionIndex == nil || payload.AmountZatoshis == nil ||
		payload.RecipientAddress == nil || payload.DiversifierIndex == nil ||
		!strings.HasPrefix(strings.TrimSpace(*payload.RecipientAddress), addressHRP+"1") {
		return invalidWalletEffect("deposit identity or value fields are incomplete")
	}
	if payload.Status.Height == nil || *payload.Status.Height != *payload.Height {
		return invalidWalletEffect("deposit status height does not match its block")
	}
	return validateLifecycle(event, payload, requiredConfirmations, "Deposit")
}

func validateSpendEffect(event domain.ScannerEvent, payload *rawWalletEffectPayload, addressHRP string, requiredConfirmations int64) error {
	if payload.Height == nil || payload.NoteTxID == nil || !txIDPattern.MatchString(*payload.NoteTxID) || payload.NoteActionIndex == nil ||
		payload.NoteHeight == nil || *payload.NoteHeight < 0 || *payload.NoteHeight > *payload.Height || payload.AmountZatoshis == nil {
		return invalidWalletEffect("spend source-note fields are incomplete or invalid")
	}
	if payload.RecipientAddress != nil && strings.TrimSpace(*payload.RecipientAddress) != "" && !strings.HasPrefix(strings.TrimSpace(*payload.RecipientAddress), addressHRP+"1") {
		return invalidWalletEffect("spend source address is on another network")
	}
	if payload.Status.Height == nil || *payload.Status.Height != *payload.Height {
		return invalidWalletEffect("spend status height does not match its block")
	}
	return validateLifecycle(event, payload, requiredConfirmations, "Spend")
}

func validateOutgoingEffect(event domain.ScannerEvent, payload *rawWalletEffectPayload, addressHRP string, requiredConfirmations int64) error {
	if payload.ActionIndex == nil || payload.AmountZatoshis == nil || payload.RecipientAddress == nil ||
		!strings.HasPrefix(strings.TrimSpace(*payload.RecipientAddress), addressHRP+"1") || payload.OVKScope == nil ||
		(*payload.OVKScope != "external" && *payload.OVKScope != "internal") {
		return invalidWalletEffect("outgoing output fields are incomplete or invalid")
	}
	if payload.RecipientScope != nil && *payload.RecipientScope != "" && *payload.RecipientScope != "external" && *payload.RecipientScope != "internal" {
		return invalidWalletEffect("outgoing recipient scope is invalid")
	}
	if payload.MemoHex != nil {
		memo := strings.TrimSpace(*payload.MemoHex)
		if len(memo) > 1024 || len(memo)%2 != 0 || memo != strings.ToLower(memo) {
			return invalidWalletEffect("outgoing memo is invalid")
		}
		if _, err := hex.DecodeString(memo); err != nil {
			return invalidWalletEffect("outgoing memo is invalid")
		}
	}
	state := *payload.Status.State
	if event.Kind == "OutgoingOutputEvent" {
		switch state {
		case "mempool":
			if event.Height != 0 || payload.Height != nil || nonzero(payload.Status.Confirmations) || payload.Status.Height != nil {
				return invalidWalletEffect("mempool output has mined fields")
			}
			return requireOnlyLifecycleFields(payload, "event")
		case "confirmed":
			if payload.Height == nil || event.Height != *payload.Height || payload.Status.Height == nil || *payload.Status.Height != *payload.Height || valueOrZero(payload.Status.Confirmations) != 1 {
				return invalidWalletEffect("mined output fields are inconsistent")
			}
			return requireOnlyLifecycleFields(payload, "event")
		default:
			return invalidWalletEffect("outgoing output state is invalid")
		}
	}
	if event.Kind == "OutgoingOutputExpired" {
		if state != "expired" || payload.Height != nil || payload.ExpiryHeight == nil || event.Height <= *payload.ExpiryHeight || payload.Status.Height != nil || nonzero(payload.Status.Confirmations) {
			return invalidWalletEffect("expired output fields are inconsistent")
		}
		return requireOnlyLifecycleFields(payload, "expired")
	}
	if payload.Height == nil || payload.Status.Height == nil || *payload.Status.Height != *payload.Height {
		return invalidWalletEffect("outgoing lifecycle block height is missing")
	}
	return validateLifecycle(event, payload, requiredConfirmations, "OutgoingOutput")
}

func validateLifecycle(event domain.ScannerEvent, payload *rawWalletEffectPayload, requiredConfirmations int64, prefix string) error {
	state := *payload.Status.State
	switch event.Kind {
	case prefix + "Event":
		if state != "confirmed" || payload.Height == nil || event.Height != *payload.Height || valueOrZero(payload.Status.Confirmations) != 1 {
			return invalidWalletEffect("detected lifecycle fields are inconsistent")
		}
		return requireOnlyLifecycleFields(payload, "event")
	case prefix + "Confirmed":
		if state != "confirmed" || payload.Height == nil || payload.ConfirmedHeight == nil || payload.RequiredConfirmations == nil ||
			*payload.RequiredConfirmations != requiredConfirmations || *payload.ConfirmedHeight != event.Height || event.Height < *payload.Height ||
			valueOrZero(payload.Status.Confirmations) != event.Height-*payload.Height+1 || valueOrZero(payload.Status.Confirmations) < requiredConfirmations {
			return invalidWalletEffect("confirmed lifecycle fields are inconsistent")
		}
		return requireOnlyLifecycleFields(payload, "confirmed")
	case prefix + "Unconfirmed":
		expected := int64(0)
		if payload.Height != nil && event.Height >= *payload.Height {
			expected = event.Height - *payload.Height + 1
		}
		if state != "confirmed" || payload.Height == nil || payload.RollbackHeight == nil || *payload.RollbackHeight != event.Height ||
			event.Height < *payload.Height ||
			payload.PreviousConfirmedHeight == nil || *payload.PreviousConfirmedHeight <= event.Height ||
			!reachedRequiredConfirmations(*payload.Height, *payload.PreviousConfirmedHeight, requiredConfirmations) ||
			(payload.RequiredConfirmations != nil && *payload.RequiredConfirmations != requiredConfirmations) ||
			valueOrZero(payload.Status.Confirmations) != expected || expected >= requiredConfirmations {
			return invalidWalletEffect("unconfirmed lifecycle fields are inconsistent")
		}
		return requireOnlyLifecycleFields(payload, "unconfirmed")
	case prefix + "Orphaned":
		if state != "orphaned" || payload.Height == nil || event.Height >= *payload.Height || payload.OrphanedAtHeight == nil || *payload.OrphanedAtHeight != event.Height || nonzero(payload.Status.Confirmations) {
			return invalidWalletEffect("orphaned lifecycle fields are inconsistent")
		}
		return requireOnlyLifecycleFields(payload, "orphaned")
	default:
		return invalidWalletEffect("lifecycle kind is invalid")
	}
}

func reachedRequiredConfirmations(blockHeight, confirmedHeight, requiredConfirmations int64) bool {
	if blockHeight < 0 || confirmedHeight < blockHeight || requiredConfirmations < 1 {
		return false
	}
	return confirmedHeight-blockHeight >= requiredConfirmations-1
}

func requireOnlyLifecycleFields(payload *rawWalletEffectPayload, lifecycle string) error {
	if lifecycle != "confirmed" && (payload.ConfirmedHeight != nil || payload.RequiredConfirmations != nil) ||
		lifecycle != "unconfirmed" && (payload.RollbackHeight != nil || payload.PreviousConfirmedHeight != nil) ||
		lifecycle != "orphaned" && payload.OrphanedAtHeight != nil {
		return invalidWalletEffect("payload mixes lifecycle-specific fields")
	}
	return nil
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func nonzero(value *int64) bool { return value != nil && *value != 0 }

func invalidWalletEffect(reason string) error {
	return &domain.UpstreamError{Kind: "invalid_response", Err: fmt.Errorf("scanner returned an invalid wallet effect: %s", reason)}
}
