package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/storage"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open gateway state: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY
);
INSERT OR IGNORE INTO schema_version(version) VALUES (1);
CREATE TABLE IF NOT EXISTS wallets (
  wallet_id TEXT PRIMARY KEY,
  network TEXT NOT NULL,
  birthday_height INTEGER NOT NULL,
  next_backfill_height INTEGER NOT NULL,
  next_address_index INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS addresses (
  wallet_id TEXT NOT NULL REFERENCES wallets(wallet_id) ON DELETE RESTRICT,
  address TEXT NOT NULL,
  diversifier_index INTEGER NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(wallet_id, diversifier_index),
  UNIQUE(wallet_id, address)
);
CREATE INDEX IF NOT EXISTS idx_addresses_lookup ON addresses(wallet_id, address);
CREATE TABLE IF NOT EXISTS idempotency_receipts (
  idempotency_key TEXT PRIMARY KEY,
  payload_digest TEXT NOT NULL,
  expected_txid TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('processing','complete')),
  http_status INTEGER,
  response_json BLOB,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS metadata (
  key TEXT PRIMARY KEY,
  value BLOB NOT NULL
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate gateway state: %w", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("read gateway schema version: %w", err)
	}
	if version > 2 {
		return fmt.Errorf("unsupported gateway schema version %d", version)
	}
	if version == 1 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin gateway schema migration: %w", err)
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `PRAGMA table_info(wallets)`)
		if err != nil {
			return fmt.Errorf("inspect wallets schema: %w", err)
		}
		hasFingerprint := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("inspect wallets column: %w", err)
			}
			if name == "ufvk_fingerprint" {
				hasFingerprint = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("inspect wallets schema: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("inspect wallets schema: %w", err)
		}
		if !hasFingerprint {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE wallets ADD COLUMN ufvk_fingerprint TEXT`); err != nil {
				return fmt.Errorf("add wallet UFVK fingerprint: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_wallets_ufvk_fingerprint ON wallets(network,ufvk_fingerprint) WHERE ufvk_fingerprint IS NOT NULL`); err != nil {
			return fmt.Errorf("index wallet UFVK fingerprints: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (2)`); err != nil {
			return fmt.Errorf("record gateway schema migration: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit gateway schema migration: %w", err)
		}
		version = 2
	}
	if version != 2 {
		return fmt.Errorf("unsupported gateway schema version %d", version)
	}
	return nil
}

func (s *Store) EnsureWallet(ctx context.Context, walletID, network, ufvkFingerprint string, birthdayHeight int64) error {
	if len(ufvkFingerprint) != 64 {
		return errors.New("UFVK fingerprint must be 64 lowercase hex characters")
	}
	if decoded, err := hex.DecodeString(ufvkFingerprint); err != nil || len(decoded) != 32 || ufvkFingerprint != strings.ToLower(ufvkFingerprint) {
		return errors.New("UFVK fingerprint must be 64 lowercase hex characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin wallet registration: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO wallets(wallet_id,network,ufvk_fingerprint,birthday_height,next_backfill_height,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(wallet_id) DO NOTHING`, walletID, network, ufvkFingerprint, birthdayHeight, birthdayHeight, now)
	if err != nil {
		return fmt.Errorf("ensure wallet: %w", err)
	}
	var got string
	var storedFingerprint sql.NullString
	var storedBirthday int64
	if err := tx.QueryRowContext(ctx, `SELECT network,ufvk_fingerprint,birthday_height FROM wallets WHERE wallet_id=?`, walletID).Scan(&got, &storedFingerprint, &storedBirthday); err != nil {
		return fmt.Errorf("verify wallet: %w", err)
	}
	if got != network {
		return fmt.Errorf("wallet %q is registered for network %q", walletID, got)
	}
	if storedFingerprint.Valid && storedFingerprint.String != "" && storedFingerprint.String != ufvkFingerprint {
		return fmt.Errorf("wallet %q is bound to a different UFVK fingerprint", walletID)
	}
	if birthdayHeight > storedBirthday {
		return fmt.Errorf("wallet %q birthday_height cannot increase from %d to %d", walletID, storedBirthday, birthdayHeight)
	}
	if !storedFingerprint.Valid || storedFingerprint.String == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE wallets SET ufvk_fingerprint=? WHERE wallet_id=? AND (ufvk_fingerprint IS NULL OR ufvk_fingerprint='')`, ufvkFingerprint, walletID); err != nil {
			return fmt.Errorf("bind wallet UFVK fingerprint: %w", err)
		}
	}
	if birthdayHeight < storedBirthday {
		if _, err := tx.ExecContext(ctx, `UPDATE wallets SET birthday_height=?,next_backfill_height=? WHERE wallet_id=?`, birthdayHeight, birthdayHeight, walletID); err != nil {
			return fmt.Errorf("rewind wallet birthday: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit wallet registration: %w", err)
	}
	return nil
}

func (s *Store) Wallet(ctx context.Context, walletID string) (storage.Wallet, bool, error) {
	var w storage.Wallet
	err := s.db.QueryRowContext(ctx, `SELECT wallet_id,network,COALESCE(ufvk_fingerprint,''),birthday_height,next_backfill_height FROM wallets WHERE wallet_id=?`, walletID).Scan(&w.WalletID, &w.Network, &w.UFVKFingerprint, &w.BirthdayHeight, &w.NextBackfillHeight)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Wallet{}, false, nil
	}
	if err != nil {
		return storage.Wallet{}, false, fmt.Errorf("lookup wallet: %w", err)
	}
	return w, true, nil
}

func (s *Store) AdvanceBackfill(ctx context.Context, walletID string, expectedFrom, next int64) error {
	if next <= expectedFrom {
		return errors.New("backfill did not advance")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE wallets SET next_backfill_height=? WHERE wallet_id=? AND next_backfill_height=?`, next, walletID, expectedFrom)
	if err != nil {
		return fmt.Errorf("advance wallet backfill: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows != 1 {
		return errors.New("wallet backfill progress changed concurrently")
	}
	return nil
}

func (s *Store) SetBackfillProgress(ctx context.Context, walletID string, next int64) error {
	var birthday int64
	if err := s.db.QueryRowContext(ctx, `SELECT birthday_height FROM wallets WHERE wallet_id=?`, walletID).Scan(&birthday); err != nil {
		return fmt.Errorf("read wallet birthday: %w", err)
	}
	if next < birthday {
		return errors.New("backfill progress precedes wallet birthday")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE wallets SET next_backfill_height=? WHERE wallet_id=?`, next, walletID); err != nil {
		return fmt.Errorf("mirror wallet backfill progress: %w", err)
	}
	return nil
}

func (s *Store) AllocateAddress(ctx context.Context, walletID, label string, derive storage.DeriveFunc) (storage.Address, error) {
	if derive == nil {
		return storage.Address{}, errors.New("derive callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Address{}, fmt.Errorf("begin address allocation: %w", err)
	}
	defer tx.Rollback()

	var next int64
	if err := tx.QueryRowContext(ctx, `SELECT next_address_index FROM wallets WHERE wallet_id=?`, walletID).Scan(&next); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.Address{}, sql.ErrNoRows
		}
		return storage.Address{}, fmt.Errorf("read address index: %w", err)
	}
	if next < 0 || next > math.MaxUint32 {
		return storage.Address{}, errors.New("address index exhausted")
	}
	address, err := derive(uint32(next))
	if err != nil {
		return storage.Address{}, err
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return storage.Address{}, errors.New("deriver returned an empty address")
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO addresses(wallet_id,address,diversifier_index,label,created_at) VALUES(?,?,?,?,?)`, walletID, address, next, label, now.Format(time.RFC3339Nano)); err != nil {
		return storage.Address{}, fmt.Errorf("insert address: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallets SET next_address_index=? WHERE wallet_id=?`, next+1, walletID); err != nil {
		return storage.Address{}, fmt.Errorf("advance address index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Address{}, fmt.Errorf("commit address allocation: %w", err)
	}
	return storage.Address{WalletID: walletID, Address: address, DiversifierIndex: uint32(next), Label: label, CreatedAt: now}, nil
}

func (s *Store) Address(ctx context.Context, walletID, address string) (storage.Address, bool, error) {
	var out storage.Address
	var idx int64
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT wallet_id,address,diversifier_index,label,created_at FROM addresses WHERE wallet_id=? AND address=?`, walletID, address).Scan(&out.WalletID, &out.Address, &idx, &out.Label, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Address{}, false, nil
	}
	if err != nil {
		return storage.Address{}, false, fmt.Errorf("lookup address: %w", err)
	}
	if idx < 0 || idx > math.MaxUint32 {
		return storage.Address{}, false, errors.New("stored address index is invalid")
	}
	out.DiversifierIndex = uint32(idx)
	out.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return storage.Address{}, false, errors.New("stored address timestamp is invalid")
	}
	return out, true, nil
}

func (s *Store) ClaimReceipt(ctx context.Context, key, digest, expectedTxID string, now time.Time, lease time.Duration) (storage.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.ClaimResult{}, fmt.Errorf("begin receipt claim: %w", err)
	}
	defer tx.Rollback()
	stamp := now.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `INSERT INTO idempotency_receipts(idempotency_key,payload_digest,expected_txid,state,created_at,updated_at) VALUES(?,?,?,'processing',?,?) ON CONFLICT(idempotency_key) DO NOTHING`, key, digest, expectedTxID, stamp, stamp)
	if err != nil {
		return storage.ClaimResult{}, fmt.Errorf("insert receipt claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if err := tx.Commit(); err != nil {
			return storage.ClaimResult{}, fmt.Errorf("commit receipt claim: %w", err)
		}
		return storage.ClaimResult{State: storage.ClaimAcquired}, nil
	}

	receipt, err := scanReceipt(tx.QueryRowContext(ctx, `SELECT idempotency_key,payload_digest,expected_txid,state,http_status,response_json,updated_at FROM idempotency_receipts WHERE idempotency_key=?`, key))
	if err != nil {
		return storage.ClaimResult{}, err
	}
	if receipt.PayloadDigest != digest {
		return storage.ClaimResult{State: storage.ClaimConflict, Receipt: receipt}, nil
	}
	if receipt.State == "complete" {
		return storage.ClaimResult{State: storage.ClaimReplay, Receipt: receipt}, nil
	}
	if now.Sub(receipt.UpdatedAt) < lease {
		return storage.ClaimResult{State: storage.ClaimInProgress, Receipt: receipt}, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE idempotency_receipts SET expected_txid=?,updated_at=? WHERE idempotency_key=?`, expectedTxID, stamp, key); err != nil {
		return storage.ClaimResult{}, fmt.Errorf("reclaim receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.ClaimResult{}, fmt.Errorf("commit receipt reclaim: %w", err)
	}
	return storage.ClaimResult{State: storage.ClaimAcquired, Receipt: receipt}, nil
}

type rowScanner interface{ Scan(...any) error }

func scanReceipt(row rowScanner) (storage.Receipt, error) {
	var out storage.Receipt
	var httpStatus sql.NullInt64
	var response []byte
	var updated string
	if err := row.Scan(&out.Key, &out.PayloadDigest, &out.ExpectedTxID, &out.State, &httpStatus, &response, &updated); err != nil {
		return storage.Receipt{}, fmt.Errorf("read receipt: %w", err)
	}
	if httpStatus.Valid {
		out.HTTPStatus = int(httpStatus.Int64)
	}
	out.ResponseJSON = append([]byte(nil), response...)
	var err error
	out.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return storage.Receipt{}, errors.New("stored receipt timestamp is invalid")
	}
	return out, nil
}

func (s *Store) CompleteReceipt(ctx context.Context, key, digest string, status int, response []byte, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE idempotency_receipts SET state='complete',http_status=?,response_json=?,updated_at=? WHERE idempotency_key=? AND payload_digest=?`, status, response, now.UTC().Format(time.RFC3339Nano), key, digest)
	if err != nil {
		return fmt.Errorf("complete receipt: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("receipt claim no longer exists")
	}
	return nil
}

func (s *Store) CursorKey(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var key []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='cursor_hmac_key'`).Scan(&key)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("stored cursor key is invalid")
		}
		return append([]byte(nil), key...), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read cursor key: %w", err)
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate cursor key: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('cursor_hmac_key',?)`, key); err != nil {
		return nil, fmt.Errorf("store cursor key: %w", err)
	}
	return append([]byte(nil), key...), nil
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) Close() error                   { return s.db.Close() }
