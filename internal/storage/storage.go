package storage

import (
	"context"
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

type Store interface {
	EnsureWallet(context.Context, string, string, string, int64) error
	Wallet(context.Context, string) (Wallet, bool, error)
	AdvanceBackfill(context.Context, string, int64, int64) error
	SetBackfillProgress(context.Context, string, int64) error
	AllocateAddress(context.Context, string, string, DeriveFunc) (Address, error)
	Address(context.Context, string, string) (Address, bool, error)
	ClaimReceipt(context.Context, string, string, string, time.Time, time.Duration) (ClaimResult, error)
	CompleteReceipt(context.Context, string, string, int, []byte, time.Time) error
	CursorKey(context.Context) ([]byte, error)
	Ping(context.Context) error
	Close() error
}
