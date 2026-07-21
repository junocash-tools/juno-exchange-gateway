package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/storage"
)

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
	if err := s.EnsureWallet(ctx, "hot", "regtest", 0); err != nil {
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

func TestReceiptClaimReplayConflictAndLease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	claim, err := s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now, time.Minute)
	if err != nil || claim.State != storage.ClaimAcquired {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now.Add(time.Second), time.Minute)
	if claim.State != storage.ClaimInProgress {
		t.Fatalf("state=%s", claim.State)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-b", "txid", now.Add(time.Second), time.Minute)
	if claim.State != storage.ClaimConflict {
		t.Fatalf("state=%s", claim.State)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now.Add(2*time.Minute), time.Minute)
	if claim.State != storage.ClaimAcquired {
		t.Fatalf("expired lease state=%s", claim.State)
	}
	if err := s.CompleteReceipt(ctx, "withdrawal-1", "digest-a", 202, []byte(`{"txid":"a"}`), now); err != nil {
		t.Fatal(err)
	}
	claim, _ = s.ClaimReceipt(ctx, "withdrawal-1", "digest-a", "txid", now, time.Minute)
	if claim.State != storage.ClaimReplay || claim.Receipt.HTTPStatus != 202 || string(claim.Receipt.ResponseJSON) != `{"txid":"a"}` {
		t.Fatalf("replay=%+v", claim)
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
	if err := s.EnsureWallet(ctx, "hot", "mainnet", 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceBackfill(ctx, "hot", 50, 101); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWallet(ctx, "hot", "mainnet", 20); err != nil {
		t.Fatal(err)
	}
	wallet, ok, err := s.Wallet(ctx, "hot")
	if err != nil || !ok || wallet.BirthdayHeight != 20 || wallet.NextBackfillHeight != 20 {
		t.Fatalf("wallet=%+v ok=%v err=%v", wallet, ok, err)
	}
	if err := s.EnsureWallet(ctx, "hot", "testnet", 20); err == nil {
		t.Fatal("expected network mismatch")
	}
	if err := s.EnsureWallet(ctx, "hot", "mainnet", 80); err != nil {
		t.Fatal(err)
	}
	wallet, ok, err = s.Wallet(ctx, "hot")
	if err != nil || !ok || wallet.BirthdayHeight != 80 || wallet.NextBackfillHeight != 80 {
		t.Fatalf("raised wallet=%+v ok=%v err=%v", wallet, ok, err)
	}
}
