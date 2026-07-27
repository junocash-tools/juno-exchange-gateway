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

	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
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
  claim_generation INTEGER NOT NULL DEFAULT 1,
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
	if version > 4 {
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
	if version == 2 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin receipt fencing migration: %w", err)
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `PRAGMA table_info(idempotency_receipts)`)
		if err != nil {
			return fmt.Errorf("inspect receipt schema: %w", err)
		}
		hasGeneration := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("inspect receipt column: %w", err)
			}
			if name == "claim_generation" {
				hasGeneration = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("inspect receipt schema: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("inspect receipt schema: %w", err)
		}
		if !hasGeneration {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE idempotency_receipts ADD COLUMN claim_generation INTEGER NOT NULL DEFAULT 1`); err != nil {
				return fmt.Errorf("add receipt claim generation: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (3)`); err != nil {
			return fmt.Errorf("record receipt fencing migration: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit receipt fencing migration: %w", err)
		}
		version = 3
	}
	if version == 3 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin coordinator state migration: %w", err)
		}
		defer tx.Rollback()
		const coordinatorSchema = `
CREATE TABLE IF NOT EXISTS transaction_attempts (
  attempt_id TEXT PRIMARY KEY,
  scoped_idempotency_key TEXT NOT NULL UNIQUE,
  request_digest TEXT NOT NULL,
  principal_name TEXT NOT NULL,
  wallet_id TEXT NOT NULL REFERENCES wallets(wallet_id) ON DELETE RESTRICT,
  approval_reference TEXT NOT NULL,
  request_json BLOB NOT NULL,
  state TEXT NOT NULL,
  change_address TEXT,
  plan_json BLOB,
  plan_digest TEXT,
  fee_zat TEXT,
  expiry_height INTEGER,
  selected_note_ids_json BLOB,
  txid TEXT,
  raw_tx_hex TEXT,
  output_action_indices_json BLOB,
  change_action_index INTEGER,
  error_code TEXT,
  error_message TEXT,
  error_retryable INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_transaction_attempts_wallet_state ON transaction_attempts(wallet_id,state,updated_at);
CREATE INDEX IF NOT EXISTS idx_transaction_attempts_txid ON transaction_attempts(txid) WHERE txid IS NOT NULL;
CREATE TABLE IF NOT EXISTS active_note_reservations (
  network TEXT NOT NULL,
  wallet_id TEXT NOT NULL REFERENCES wallets(wallet_id) ON DELETE RESTRICT,
  note_id TEXT NOT NULL,
  attempt_id TEXT NOT NULL REFERENCES transaction_attempts(attempt_id) ON DELETE RESTRICT,
  plan_digest TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(network,wallet_id,note_id)
);
CREATE INDEX IF NOT EXISTS idx_active_note_reservations_attempt ON active_note_reservations(attempt_id);
CREATE TABLE IF NOT EXISTS transaction_attempt_events (
  event_id INTEGER PRIMARY KEY AUTOINCREMENT,
  attempt_id TEXT NOT NULL REFERENCES transaction_attempts(attempt_id) ON DELETE RESTRICT,
  state TEXT NOT NULL,
  detail_json BLOB,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_transaction_attempt_events_attempt ON transaction_attempt_events(attempt_id,event_id);`
		if _, err := tx.ExecContext(ctx, coordinatorSchema); err != nil {
			return fmt.Errorf("create coordinator state: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (4)`); err != nil {
			return fmt.Errorf("record coordinator state migration: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit coordinator state migration: %w", err)
		}
		version = 4
	}
	if version != 4 {
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

// AllocateAddressAt records an externally reserved diversifier index. It is
// idempotent only when an existing row has the same deterministic address.
func (s *Store) AllocateAddressAt(ctx context.Context, walletID string, index uint32, label string, derive storage.DeriveFunc) (storage.Address, error) {
	if derive == nil {
		return storage.Address{}, errors.New("derive callback is required")
	}
	address, err := derive(index)
	if err != nil {
		return storage.Address{}, err
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return storage.Address{}, errors.New("deriver returned an empty address")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Address{}, fmt.Errorf("begin exact address allocation: %w", err)
	}
	defer tx.Rollback()

	var ignored int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM wallets WHERE wallet_id=?`, walletID).Scan(&ignored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.Address{}, sql.ErrNoRows
		}
		return storage.Address{}, fmt.Errorf("verify exact address wallet: %w", err)
	}

	var existingAddress, existingLabel, createdRaw string
	err = tx.QueryRowContext(ctx, `SELECT address,label,created_at FROM addresses WHERE wallet_id=? AND diversifier_index=?`, walletID, int64(index)).Scan(&existingAddress, &existingLabel, &createdRaw)
	if err == nil {
		if existingAddress != address {
			return storage.Address{}, fmt.Errorf("wallet %q index %d is already bound to a different address", walletID, index)
		}
		created, err := time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return storage.Address{}, errors.New("stored address timestamp is invalid")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE wallets SET next_address_index=CASE WHEN next_address_index < ? THEN ? ELSE next_address_index END WHERE wallet_id=?`, uint64(index)+1, uint64(index)+1, walletID); err != nil {
			return storage.Address{}, fmt.Errorf("advance exact address index: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return storage.Address{}, fmt.Errorf("commit existing exact address allocation: %w", err)
		}
		return storage.Address{WalletID: walletID, Address: existingAddress, DiversifierIndex: index, Label: existingLabel, CreatedAt: created}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.Address{}, fmt.Errorf("lookup exact address index: %w", err)
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO addresses(wallet_id,address,diversifier_index,label,created_at) VALUES(?,?,?,?,?)`, walletID, address, int64(index), label, now.Format(time.RFC3339Nano)); err != nil {
		return storage.Address{}, fmt.Errorf("insert exact address: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallets SET next_address_index=CASE WHEN next_address_index < ? THEN ? ELSE next_address_index END WHERE wallet_id=?`, uint64(index)+1, uint64(index)+1, walletID); err != nil {
		return storage.Address{}, fmt.Errorf("advance exact address index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Address{}, fmt.Errorf("commit exact address allocation: %w", err)
	}
	return storage.Address{WalletID: walletID, Address: address, DiversifierIndex: index, Label: label, CreatedAt: now}, nil
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

func (s *Store) AddressIndices(ctx context.Context, walletID string) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT diversifier_index FROM addresses WHERE wallet_id=? ORDER BY diversifier_index`, walletID)
	if err != nil {
		return nil, fmt.Errorf("list wallet address indices: %w", err)
	}
	defer rows.Close()
	var indices []uint32
	for rows.Next() {
		var index int64
		if err := rows.Scan(&index); err != nil {
			return nil, fmt.Errorf("read wallet address index: %w", err)
		}
		if index < 0 || index > math.MaxUint32 {
			return nil, errors.New("stored address index is invalid")
		}
		indices = append(indices, uint32(index))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list wallet address indices: %w", err)
	}
	return indices, nil
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
	res, err := tx.ExecContext(ctx, `INSERT INTO idempotency_receipts(idempotency_key,payload_digest,expected_txid,claim_generation,state,created_at,updated_at) VALUES(?,?,?,1,'processing',?,?) ON CONFLICT(idempotency_key) DO NOTHING`, key, digest, expectedTxID, stamp, stamp)
	if err != nil {
		return storage.ClaimResult{}, fmt.Errorf("insert receipt claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if err := tx.Commit(); err != nil {
			return storage.ClaimResult{}, fmt.Errorf("commit receipt claim: %w", err)
		}
		return storage.ClaimResult{State: storage.ClaimAcquired, Receipt: storage.Receipt{Key: key, PayloadDigest: digest, ExpectedTxID: expectedTxID, Generation: 1, State: "processing", UpdatedAt: now.UTC()}}, nil
	}

	receipt, err := scanReceipt(tx.QueryRowContext(ctx, `SELECT idempotency_key,payload_digest,expected_txid,claim_generation,state,http_status,response_json,updated_at FROM idempotency_receipts WHERE idempotency_key=?`, key))
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
	res, err = tx.ExecContext(ctx, `UPDATE idempotency_receipts SET expected_txid=?,claim_generation=claim_generation+1,updated_at=? WHERE idempotency_key=? AND payload_digest=? AND state='processing' AND claim_generation=?`, expectedTxID, stamp, key, digest, receipt.Generation)
	if err != nil {
		return storage.ClaimResult{}, fmt.Errorf("reclaim receipt: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return storage.ClaimResult{}, errors.New("receipt claim changed while reclaiming")
	}
	if err := tx.Commit(); err != nil {
		return storage.ClaimResult{}, fmt.Errorf("commit receipt reclaim: %w", err)
	}
	receipt.Generation++
	receipt.ExpectedTxID = expectedTxID
	receipt.UpdatedAt = now.UTC()
	return storage.ClaimResult{State: storage.ClaimAcquired, Receipt: receipt}, nil
}

type rowScanner interface{ Scan(...any) error }

func scanReceipt(row rowScanner) (storage.Receipt, error) {
	var out storage.Receipt
	var httpStatus sql.NullInt64
	var response []byte
	var updated string
	if err := row.Scan(&out.Key, &out.PayloadDigest, &out.ExpectedTxID, &out.Generation, &out.State, &httpStatus, &response, &updated); err != nil {
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

func (s *Store) RenewReceipt(ctx context.Context, key, digest string, generation int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE idempotency_receipts SET updated_at=? WHERE idempotency_key=? AND payload_digest=? AND claim_generation=? AND state='processing'`, now.UTC().Format(time.RFC3339Nano), key, digest, generation)
	if err != nil {
		return fmt.Errorf("renew receipt claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("receipt claim is no longer owned")
	}
	return nil
}

func (s *Store) CompleteReceipt(ctx context.Context, key, digest string, generation int64, status int, response []byte, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE idempotency_receipts SET state='complete',http_status=?,response_json=?,updated_at=? WHERE idempotency_key=? AND payload_digest=? AND claim_generation=? AND state='processing'`, status, response, now.UTC().Format(time.RFC3339Nano), key, digest, generation)
	if err != nil {
		return fmt.Errorf("complete receipt: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("receipt claim is no longer owned")
	}
	return nil
}

func (s *Store) AbandonReceipt(ctx context.Context, key, digest string, generation int64, _ time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE idempotency_receipts SET claim_generation=claim_generation+1,updated_at=? WHERE idempotency_key=? AND payload_digest=? AND claim_generation=? AND state='processing'`, time.Unix(0, 0).UTC().Format(time.RFC3339Nano), key, digest, generation)
	if err != nil {
		return fmt.Errorf("abandon receipt claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("receipt claim is no longer owned")
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

func (s *Store) AssertInitializable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var wallets, addresses, receipts, attempts, reservations, events, metadata int64
	if err := s.db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM wallets),
  (SELECT COUNT(*) FROM addresses),
  (SELECT COUNT(*) FROM idempotency_receipts),
	(SELECT COUNT(*) FROM transaction_attempts),
	(SELECT COUNT(*) FROM active_note_reservations),
	(SELECT COUNT(*) FROM transaction_attempt_events),
	  (SELECT COUNT(*) FROM metadata)`).Scan(&wallets, &addresses, &receipts, &attempts, &reservations, &events, &metadata); err != nil {
		return fmt.Errorf("inspect gateway database before init: %w", err)
	}
	if wallets != 0 || addresses != 0 || receipts != 0 || attempts != 0 || reservations != 0 || events != 0 || metadata != 0 {
		return fmt.Errorf("gateway database is not empty (wallets=%d addresses=%d receipts=%d attempts=%d reservations=%d events=%d metadata=%d); init refuses existing state", wallets, addresses, receipts, attempts, reservations, events, metadata)
	}
	return nil
}

func (s *Store) InstallationID(ctx context.Context) (string, bool, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='installation_id'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read gateway installation binding: %w", err)
	}
	id := string(raw)
	if len(id) != 64 {
		return "", false, errors.New("gateway database installation binding is invalid")
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 32 || id != strings.ToLower(id) {
		return "", false, errors.New("gateway database installation binding is invalid")
	}
	return id, true, nil
}

func (s *Store) BeginInstallationRecovery(ctx context.Context, installationID string) error {
	if err := validateInstallationID(installationID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin installation recovery marker: %w", err)
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='recovery_installation_id'`).Scan(&existing)
	if err == nil {
		if string(existing) != installationID {
			return fmt.Errorf("gateway database recovery belongs to installation %q", string(existing))
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read installation recovery marker: %w", err)
	}
	var wallets, addresses, receipts, attempts, reservations, events, metadata int64
	if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM wallets),
  (SELECT COUNT(*) FROM addresses),
  (SELECT COUNT(*) FROM idempotency_receipts),
	(SELECT COUNT(*) FROM transaction_attempts),
	(SELECT COUNT(*) FROM active_note_reservations),
	(SELECT COUNT(*) FROM transaction_attempt_events),
	  (SELECT COUNT(*) FROM metadata)`).Scan(&wallets, &addresses, &receipts, &attempts, &reservations, &events, &metadata); err != nil {
		return fmt.Errorf("inspect gateway database before recovery: %w", err)
	}
	if wallets != 0 || addresses != 0 || receipts != 0 || attempts != 0 || reservations != 0 || events != 0 || metadata != 0 {
		return fmt.Errorf("unbound gateway database is not empty and has no matching recovery marker (wallets=%d addresses=%d receipts=%d attempts=%d reservations=%d events=%d metadata=%d)", wallets, addresses, receipts, attempts, reservations, events, metadata)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('recovery_installation_id',?)`, []byte(installationID)); err != nil {
		return fmt.Errorf("store installation recovery marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit installation recovery marker: %w", err)
	}
	return nil
}

func (s *Store) BindInstallation(ctx context.Context, installationID string) error {
	return s.bindInstallation(ctx, installationID, false)
}

func (s *Store) BindRecoveredInstallation(ctx context.Context, installationID string) error {
	return s.bindInstallation(ctx, installationID, true)
}

func (s *Store) bindInstallation(ctx context.Context, installationID string, sealCoordinator bool) error {
	if err := validateInstallationID(installationID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin gateway installation binding: %w", err)
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='installation_id'`).Scan(&existing)
	if err == nil {
		if string(existing) != installationID {
			return fmt.Errorf("gateway database is already bound to installation %q", string(existing))
		}
		if sealCoordinator {
			if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('coordinator_recovery_seal',?) ON CONFLICT(key) DO NOTHING`, []byte(installationID)); err != nil {
				return fmt.Errorf("seal already-bound coordinator after gateway recovery: %w", err)
			}
			var seal []byte
			if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='coordinator_recovery_seal'`).Scan(&seal); err != nil {
				return fmt.Errorf("verify already-bound coordinator recovery seal: %w", err)
			}
			if string(seal) != installationID {
				return errors.New("coordinator recovery seal does not match the gateway installation")
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit already-bound coordinator recovery seal: %w", err)
			}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read gateway installation binding: %w", err)
	}
	var recoveryID []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='recovery_installation_id'`).Scan(&recoveryID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("gateway database has no installation recovery marker")
		}
		return fmt.Errorf("read installation recovery marker: %w", err)
	}
	if string(recoveryID) != installationID {
		return fmt.Errorf("gateway database recovery belongs to installation %q", string(recoveryID))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('installation_id',?)`, []byte(installationID)); err != nil {
		return fmt.Errorf("store gateway installation binding: %w", err)
	}
	if sealCoordinator {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('coordinator_recovery_seal',?)`, []byte(installationID)); err != nil {
			return fmt.Errorf("seal coordinator after gateway recovery: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metadata WHERE key='recovery_installation_id'`); err != nil {
		return fmt.Errorf("clear installation recovery marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit gateway installation binding: %w", err)
	}
	return nil
}

func (s *Store) CoordinatorRecoverySealed(ctx context.Context) (bool, error) {
	var seal []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='coordinator_recovery_seal'`).Scan(&seal)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read coordinator recovery seal: %w", err)
	}
	var installationID []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='installation_id'`).Scan(&installationID); err != nil {
		return false, fmt.Errorf("read installation binding for coordinator recovery seal: %w", err)
	}
	if string(seal) != string(installationID) {
		return false, errors.New("coordinator recovery seal does not match the gateway installation")
	}
	return true, nil
}

func (s *Store) UnsealCoordinatorRecovery(ctx context.Context, installationID, reconciliationReference string, at time.Time) (bool, error) {
	if err := validateInstallationID(installationID); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin coordinator recovery unseal: %w", err)
	}
	defer tx.Rollback()
	var bound []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='installation_id'`).Scan(&bound); err != nil {
		return false, fmt.Errorf("read gateway installation binding: %w", err)
	}
	if string(bound) != installationID {
		return false, fmt.Errorf("gateway database belongs to installation %q, not %q", string(bound), installationID)
	}
	var seal []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='coordinator_recovery_seal'`).Scan(&seal)
	if errors.Is(err, sql.ErrNoRows) {
		var prior []byte
		if auditErr := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='coordinator_recovery_reconciliation_reference'`).Scan(&prior); auditErr != nil {
			if errors.Is(auditErr, sql.ErrNoRows) {
				return false, errors.New("coordinator automation is not sealed by a database recovery")
			}
			return false, fmt.Errorf("read coordinator recovery unseal audit: %w", auditErr)
		}
		if string(prior) != reconciliationReference {
			return false, fmt.Errorf("coordinator recovery was already unsealed with reconciliation reference %q", string(prior))
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read coordinator recovery seal: %w", err)
	}
	if string(seal) != installationID {
		return false, errors.New("coordinator recovery seal does not match the gateway installation")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metadata WHERE key='coordinator_recovery_seal'`); err != nil {
		return false, fmt.Errorf("clear coordinator recovery seal: %w", err)
	}
	for key, value := range map[string][]byte{
		"coordinator_recovery_reconciliation_reference": []byte(reconciliationReference),
		"coordinator_recovery_unsealed_at":              []byte(at.UTC().Format(time.RFC3339Nano)),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return false, fmt.Errorf("store coordinator recovery unseal audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit coordinator recovery unseal: %w", err)
	}
	return true, nil
}

func validateInstallationID(installationID string) error {
	if len(installationID) != 64 {
		return errors.New("installation_id must be 64 lowercase hex characters")
	}
	decoded, err := hex.DecodeString(installationID)
	if err != nil || len(decoded) != 32 || installationID != strings.ToLower(installationID) {
		return errors.New("installation_id must be 64 lowercase hex characters")
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) Close() error                   { return s.db.Close() }
