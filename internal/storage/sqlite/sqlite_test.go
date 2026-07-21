package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "state.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAllocateAddressIsAtomicAndPersistent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.EnsureWallet(ctx, "hot", "regtest", fingerprint("hot-ufvk"), 0); err != nil {
		t.Fatal(err)
	}
	const count = 32
	var wg sync.WaitGroup
	var calls atomic.Int32
	results := make(chan storage.Address, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			address, err := s.AllocateAddress(ctx, "hot", "customer", func(index uint32) (string, error) {
				calls.Add(1)
				return fmt.Sprintf("jregtest1address%d", index), nil
			})
			if err != nil {
				errs <- err
				return
			}
			results <- address
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[uint32]bool{}
	for address := range results {
		if seen[address.DiversifierIndex] {
			t.Fatalf("duplicate index %d", address.DiversifierIndex)
		}
		seen[address.DiversifierIndex] = true
	}
	if len(seen) != count || calls.Load() != count {
		t.Fatalf("allocated=%d derive_calls=%d", len(seen), calls.Load())
	}
	for i := uint32(0); i < count; i++ {
		if !seen[i] {
			t.Fatalf("missing index %d", i)
		}
	}
	got, ok, err := s.Address(ctx, "hot", "jregtest1address17")
	if err != nil || !ok || got.DiversifierIndex != 17 {
		t.Fatalf("lookup=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestExactAddressAllocationAdvancesPastGaps(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.EnsureWallet(ctx, "hot", "regtest", fingerprint("hot-ufvk"), 0); err != nil {
		t.Fatal(err)
	}
	derive := func(index uint32) (string, error) { return fmt.Sprintf("jregtest1exact%d", index), nil }
	got, err := s.AllocateAddressAt(ctx, "hot", 5, "recovered", derive)
	if err != nil || got.DiversifierIndex != 5 {
		t.Fatalf("exact allocation=%+v err=%v", got, err)
	}
	got, err = s.AllocateAddressAt(ctx, "hot", 5, "ignored-on-replay", derive)
	if err != nil || got.Label != "recovered" {
		t.Fatalf("exact replay=%+v err=%v", got, err)
	}
	got, err = s.AllocateAddress(ctx, "hot", "next", derive)
	if err != nil || got.DiversifierIndex != 6 {
		t.Fatalf("allocation after gap=%+v err=%v", got, err)
	}
	if _, err := s.AllocateAddressAt(ctx, "hot", 5, "", func(uint32) (string, error) { return "jregtest1different", nil }); err == nil {
		t.Fatal("expected conflicting exact mapping rejection")
	}
}

func TestInstallationBindingAndInitializableGuard(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.AssertInitializable(ctx); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%064x", 1)
	if err := s.BeginInstallationRecovery(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.BindInstallation(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.InstallationID(ctx)
	if err != nil || !ok || got != id {
		t.Fatalf("installation binding=%q ok=%v err=%v", got, ok, err)
	}
	if err := s.BindInstallation(ctx, id); err != nil {
		t.Fatalf("same binding should be idempotent: %v", err)
	}
	if err := s.BindInstallation(ctx, fmt.Sprintf("%064x", 2)); err == nil {
		t.Fatal("expected installation binding mismatch")
	}
	if err := s.AssertInitializable(ctx); err == nil {
		t.Fatal("bound database must not be initializable")
	}
}

func TestRecoveryRejectsUnmarkedNonemptyDatabase(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.EnsureWallet(ctx, "hot", "regtest", fingerprint("legacy"), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginInstallationRecovery(ctx, fmt.Sprintf("%064x", 1)); err == nil {
		t.Fatal("expected unmarked nonempty recovery rejection")
	}
}

func TestReceiptClaimReplayConflictAndLease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	claim, err := s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now, time.Minute)
	if err != nil || claim.State != storage.ClaimAcquired || claim.Receipt.Generation != 1 {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	firstGeneration := claim.Receipt.Generation
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now.Add(time.Second), time.Minute)
	if claim.State != storage.ClaimInProgress {
		t.Fatalf("state=%s", claim.State)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-b", "txid", now.Add(time.Second), time.Minute)
	if claim.State != storage.ClaimConflict {
		t.Fatalf("state=%s", claim.State)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now.Add(2*time.Minute), time.Minute)
	if claim.State != storage.ClaimAcquired || claim.Receipt.Generation != firstGeneration+1 {
		t.Fatalf("expired lease state=%s", claim.State)
	}
	secondGeneration := claim.Receipt.Generation
	if err := s.CompleteReceipt(ctx, "withdrawal-1", "digest-a", firstGeneration, 202, []byte(`{"txid":"stale"}`), now); err == nil {
		t.Fatal("stale claimant completed a reclaimed receipt")
	}
	if err := s.RenewReceipt(ctx, "withdrawal-1", "digest-a", firstGeneration, now); err == nil {
		t.Fatal("stale claimant renewed a reclaimed receipt")
	}
	if err := s.AbandonReceipt(ctx, "withdrawal-1", "digest-a", firstGeneration, now); err == nil {
		t.Fatal("stale claimant abandoned a reclaimed receipt")
	}
	if err := s.RenewReceipt(ctx, "withdrawal-1", "digest-a", secondGeneration, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReceipt(ctx, "withdrawal-1", "digest-a", secondGeneration, 202, []byte(`{"txid":"a"}`), now); err != nil {
		t.Fatal(err)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now, time.Minute)
	if claim.State != storage.ClaimReplay || claim.Receipt.HTTPStatus != 202 || string(claim.Receipt.ResponseJSON) != `{"txid":"a"}` {
		t.Fatalf("replay=%+v", claim)
	}

	abandoned, err := s.ClaimReceipt(ctx, "withdrawal-2", "digest-c", "txid", now, time.Minute)
	if err != nil || abandoned.State != storage.ClaimAcquired {
		t.Fatalf("abandoned claim=%+v err=%v", abandoned, err)
	}
	if err := s.AbandonReceipt(ctx, "withdrawal-2", "digest-c", abandoned.Receipt.Generation, now); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimReceipt(ctx, "withdrawal-2", "digest-c", "txid", now.Add(time.Second), time.Minute)
	if err != nil || reclaimed.State != storage.ClaimAcquired || reclaimed.Receipt.Generation <= abandoned.Receipt.Generation {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
}

func TestCursorKeyPersists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first, err := s.CursorKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CursorKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || len(first) != 32 {
		t.Fatal("cursor key did not persist")
	}
}

func TestEarlierBirthdayRewindsBackfillSafely(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.EnsureWallet(ctx, "hot", "mainnet", fingerprint("ufvk-a"), 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceBackfill(ctx, "hot", 50, 101); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWallet(ctx, "hot", "mainnet", fingerprint("ufvk-a"), 20); err != nil {
		t.Fatal(err)
	}
	wallet, ok, err := s.Wallet(ctx, "hot")
	if err != nil || !ok || wallet.BirthdayHeight != 20 || wallet.NextBackfillHeight != 20 {
		t.Fatalf("wallet=%+v ok=%v err=%v", wallet, ok, err)
	}
	if err := s.EnsureWallet(ctx, "hot", "testnet", fingerprint("ufvk-a"), 20); err == nil {
		t.Fatal("expected network mismatch")
	}
	if err := s.EnsureWallet(ctx, "hot", "mainnet", fingerprint("ufvk-a"), 80); err == nil {
		t.Fatal("expected unsafe birthday increase rejection")
	}
	if err := s.EnsureWallet(ctx, "hot", "mainnet", fingerprint("ufvk-b"), 20); err == nil {
		t.Fatal("expected UFVK fingerprint change rejection")
	}
}

func TestVersionOneWalletMigrationBindsFingerprintWithoutLosingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
INSERT INTO schema_version(version) VALUES (1);
CREATE TABLE wallets (
  wallet_id TEXT PRIMARY KEY,
  network TEXT NOT NULL,
  birthday_height INTEGER NOT NULL,
  next_backfill_height INTEGER NOT NULL,
  next_address_index INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
INSERT INTO wallets(wallet_id,network,birthday_height,next_backfill_height,next_address_index,created_at)
VALUES ('hot','mainnet',50,101,27,'2026-07-21T00:00:00Z');
CREATE TABLE addresses (wallet_id TEXT NOT NULL, address TEXT NOT NULL, diversifier_index INTEGER NOT NULL, label TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, PRIMARY KEY(wallet_id,diversifier_index), UNIQUE(wallet_id,address));
CREATE TABLE idempotency_receipts (idempotency_key TEXT PRIMARY KEY,payload_digest TEXT NOT NULL,expected_txid TEXT NOT NULL,state TEXT NOT NULL,http_status INTEGER,response_json BLOB,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE metadata (key TEXT PRIMARY KEY,value BLOB NOT NULL);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	wallet, found, err := s.Wallet(ctx, "hot")
	if err != nil || !found || wallet.NextBackfillHeight != 101 || wallet.UFVKFingerprint != "" {
		t.Fatalf("wallet=%+v found=%v err=%v", wallet, found, err)
	}
	fp := fingerprint("legacy-ufvk")
	if err := s.EnsureWallet(ctx, "hot", "mainnet", fp, 50); err != nil {
		t.Fatal(err)
	}
	wallet, found, err = s.Wallet(ctx, "hot")
	if err != nil || !found || wallet.NextBackfillHeight != 101 || wallet.UFVKFingerprint != fp {
		t.Fatalf("bound wallet=%+v found=%v err=%v", wallet, found, err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var generation int64
	if err := s.db.QueryRow(`SELECT claim_generation FROM idempotency_receipts LIMIT 1`).Scan(&generation); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("receipt generation migration: %v", err)
	}
}

func TestUFVKFingerprintCannotBindTwoWalletIDs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fp := fingerprint("shared-ufvk")
	if err := s.EnsureWallet(ctx, "hot", "mainnet", fp, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWallet(ctx, "cold", "mainnet", fp, 0); err == nil {
		t.Fatal("expected duplicate fingerprint rejection")
	}
}
