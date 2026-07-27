package storage

import (
	"context"
	"errors"
	"time"
)

type Wallet struct {
	WalletID           string
	Network            string
	UFVKFingerprint    string
	BirthdayHeight     int64
	NextBackfillHeight int64
}

type Address struct {
	WalletID         string    `json:"wallet_id"`
	Address          string    `json:"address"`
	DiversifierIndex uint32    `json:"diversifier_index"`
	Label            string    `json:"label,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type DeriveFunc func(index uint32) (string, error)

type Receipt struct {
	Key           string
	PayloadDigest string
	ExpectedTxID  string
	Generation    int64
	State         string
	ResponseJSON  []byte
	HTTPStatus    int
	UpdatedAt     time.Time
}

type ClaimState string

const (
	ClaimAcquired   ClaimState = "acquired"
	ClaimInProgress ClaimState = "in_progress"
	ClaimReplay     ClaimState = "replay"
	ClaimConflict   ClaimState = "conflict"
)

type ClaimResult struct {
	State   ClaimState
	Receipt Receipt
}

var (
	ErrAttemptStateConflict = errors.New("transaction attempt state conflict")
	ErrNoteReservation      = errors.New("note reservation conflict")
)

type TransactionAttempt struct {
	AttemptID                  string
	ScopedIdempotencyKey       string
	RequestDigest              string
	PrincipalName              string
	WalletID                   string
	ApprovalReference          string
	RequestJSON                []byte
	State                      string
	ChangeAddress              string
	PlanJSON                   []byte
	PlanDigest                 string
	FeeZat                     string
	ExpiryHeight               int64
	SelectedNoteIDs            []string
	TxID                       string
	RawTxHex                   string
	OrchardOutputActionIndices []uint32
	OrchardChangeActionIndex   *uint32
	ErrorCode                  string
	ErrorMessage               string
	ErrorRetryable             bool
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type AttemptClaimResult struct {
	State   ClaimState
	Attempt TransactionAttempt
}

type Store interface {
	EnsureWallet(context.Context, string, string, string, int64) error
	Wallet(context.Context, string) (Wallet, bool, error)
	AdvanceBackfill(context.Context, string, int64, int64) error
	SetBackfillProgress(context.Context, string, int64) error
	AllocateAddress(context.Context, string, string, DeriveFunc) (Address, error)
	Address(context.Context, string, string) (Address, bool, error)
	ClaimReceipt(context.Context, string, string, string, time.Time, time.Duration) (ClaimResult, error)
	RenewReceipt(context.Context, string, string, int64, time.Time) error
	CompleteReceipt(context.Context, string, string, int64, int, []byte, time.Time) error
	AbandonReceipt(context.Context, string, string, int64, time.Time) error
	ClaimAttempt(context.Context, TransactionAttempt) (AttemptClaimResult, error)
	Attempt(context.Context, string) (TransactionAttempt, bool, error)
	RecoverableAttempts(context.Context, string, int) ([]TransactionAttempt, error)
	SetAttemptChangeAddress(context.Context, string, string, time.Time) error
	ActiveNoteIDs(context.Context, string, string) ([]string, error)
	ReserveAttemptPlan(context.Context, string, string, []byte, string, string, int64, []string, time.Time) error
	BeginAttemptSigning(context.Context, string, time.Time) (TransactionAttempt, error)
	CompleteAttemptSigning(context.Context, string, string, string, string, []uint32, *uint32, time.Time) error
	MarkAttemptState(context.Context, string, string, string, string, bool, bool, time.Time) error
	CancelAttempt(context.Context, string, time.Time) (TransactionAttempt, error)
	CursorKey(context.Context) ([]byte, error)
	Ping(context.Context) error
	Close() error
}
