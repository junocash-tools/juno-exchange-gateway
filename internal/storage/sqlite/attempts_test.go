package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

func TestTransactionAttemptClaimReplayAndConflict(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ensureAttemptWallet(t, store)
	now := time.Now().UTC()
	candidate := testAttempt(1, "scope-a", "digest-a", now)

	claim, err := store.ClaimAttempt(ctx, candidate)
	if err != nil || claim.State != storage.ClaimAcquired || claim.Attempt.State != "planning" {
		t.Fatalf("acquire=%+v err=%v", claim, err)
	}
	replay, err := store.ClaimAttempt(ctx, testAttempt(99, "scope-a", "digest-a", now.Add(time.Minute)))
	if err != nil || replay.State != storage.ClaimReplay || replay.Attempt.AttemptID != candidate.AttemptID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflict, err := store.ClaimAttempt(ctx, testAttempt(2, "scope-a", "digest-b", now))
	if err != nil || conflict.State != storage.ClaimConflict || conflict.Attempt.AttemptID != candidate.AttemptID {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestRecoverableAttemptsUsesStableAttemptIDCursor(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ensureAttemptWallet(t, store)
	now := time.Now().UTC()
	for number := 1; number <= 1001; number++ {
		if claim, err := store.ClaimAttempt(ctx, testAttempt(number, fmt.Sprintf("scope-%d", number), fmt.Sprintf("digest-%d", number), now)); err != nil || claim.State != storage.ClaimAcquired {
			t.Fatalf("claim %d=%+v err=%v", number, claim, err)
		}
	}
	first, err := store.RecoverableAttempts(ctx, "", 1000)
	if err != nil || len(first) != 1000 {
		t.Fatalf("first page count=%d err=%v", len(first), err)
	}
	if first[0].AttemptID != testAttempt(1, "", "", now).AttemptID || first[999].AttemptID != testAttempt(1000, "", "", now).AttemptID {
		t.Fatalf("first page first=%v last=%v", first[0].AttemptID, first[999].AttemptID)
	}
	second, err := store.RecoverableAttempts(ctx, first[999].AttemptID, 1000)
	if err != nil || len(second) != 1 || second[0].AttemptID != testAttempt(1001, "", "", now).AttemptID {
		t.Fatalf("second page=%v err=%v", attemptIDs(second), err)
	}
}

func TestSigningUnknownRecoveryRefreshDoesNotGrowEvents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ensureAttemptWallet(t, store)
	now := time.Now().UTC()
	attempt := testAttempt(88, "scope-unknown", "digest-unknown", now)
	if _, err := store.ClaimAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	noteID := fmt.Sprintf("%064x:0", 88)
	if err := store.ReserveAttemptPlan(ctx, attempt.AttemptID, "regtest", []byte(`{"plan":88}`), "sha256:unknown", "200000", 140, []string{noteID}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAttemptSigning(ctx, attempt.AttemptID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAttemptState(ctx, attempt.AttemptID, "signing_unknown", "signer_busy", "journal is busy", true, false, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transaction_attempt_events WHERE attempt_id=?`, attempt.AttemptID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if replay, err := store.BeginAttemptSigning(ctx, attempt.AttemptID, now.Add(3*time.Second)); err != nil || replay.State != "signing_unknown" {
		t.Fatalf("unknown replay=%+v err=%v", replay, err)
	}
	refreshAt := now.Add(time.Minute)
	if err := store.MarkAttemptState(ctx, attempt.AttemptID, "signing_unknown", "signer_busy", "journal is busy", true, false, refreshAt); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transaction_attempt_events WHERE attempt_id=?`, attempt.AttemptID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.Attempt(ctx, attempt.AttemptID)
	if err != nil || !found || !stored.UpdatedAt.Equal(refreshAt) {
		t.Fatalf("stored=%+v found=%v err=%v", stored, found, err)
	}
	if after != before {
		t.Fatalf("identical recovery grew events: before=%d after=%d", before, after)
	}
}

func attemptIDs(attempts []storage.TransactionAttempt) []string {
	out := make([]string, len(attempts))
	for index := range attempts {
		out[index] = attempts[index].AttemptID
	}
	return out
}

func TestTransactionAttemptReservationsAreAtomicAndSurviveSigning(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ensureAttemptWallet(t, store)
	now := time.Now().UTC()
	first := testAttempt(1, "scope-a", "digest-a", now)
	second := testAttempt(2, "scope-b", "digest-b", now)
	for _, candidate := range []storage.TransactionAttempt{first, second} {
		if claim, err := store.ClaimAttempt(ctx, candidate); err != nil || claim.State != storage.ClaimAcquired {
			t.Fatalf("claim=%+v err=%v", claim, err)
		}
	}
	noteA := fmt.Sprintf("%064x:0", 10)
	noteB := fmt.Sprintf("%064x:1", 11)
	noteC := fmt.Sprintf("%064x:2", 12)
	if err := store.ReserveAttemptPlan(ctx, first.AttemptID, "regtest", []byte(`{"plan":1}`), "sha256:first", "200000", 140, []string{noteB, noteA}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAttemptState(ctx, first.AttemptID, "final", "", "", false, true, now); !errors.Is(err, storage.ErrAttemptStateConflict) {
		t.Fatalf("reserved attempt skipped signing: %v", err)
	}
	if err := store.ReserveAttemptPlan(ctx, second.AttemptID, "regtest", []byte(`{"plan":2}`), "sha256:second", "200000", 140, []string{noteC, noteA}, now); !errors.Is(err, storage.ErrNoteReservation) {
		t.Fatalf("reservation conflict=%v", err)
	}
	active, err := store.ActiveNoteIDs(ctx, "regtest", "hot")
	if err != nil || len(active) != 2 || active[0] != noteA || active[1] != noteB {
		t.Fatalf("active=%v err=%v", active, err)
	}
	secondValue, found, err := store.Attempt(ctx, second.AttemptID)
	if err != nil || !found || secondValue.State != "planning" || len(secondValue.PlanJSON) != 0 {
		t.Fatalf("second=%+v found=%v err=%v", secondValue, found, err)
	}

	signing, err := store.BeginAttemptSigning(ctx, first.AttemptID, now.Add(time.Second))
	if err != nil || signing.State != "signing" || signing.PlanDigest != "sha256:first" {
		t.Fatalf("signing=%+v err=%v", signing, err)
	}
	replay, err := store.BeginAttemptSigning(ctx, first.AttemptID, now.Add(2*time.Second))
	if err != nil || replay.State != "signing" {
		t.Fatalf("signing replay=%+v err=%v", replay, err)
	}
	change := uint32(2)
	if err := store.CompleteAttemptSigning(ctx, first.AttemptID, fmt.Sprintf("%064x", 20), "00", "200000", []uint32{1}, &change, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	active, err = store.ActiveNoteIDs(ctx, "regtest", "hot")
	if err != nil || len(active) != 2 {
		t.Fatalf("signed attempt released reservations early: active=%v err=%v", active, err)
	}
	if err := store.MarkAttemptState(ctx, first.AttemptID, "final", "", "", false, true, now.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	active, err = store.ActiveNoteIDs(ctx, "regtest", "hot")
	if err != nil || len(active) != 0 {
		t.Fatalf("final reservations=%v err=%v", active, err)
	}
}

func TestTransactionAttemptCancellationIsFailClosed(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	ensureAttemptWallet(t, store)
	now := time.Now().UTC()
	planning := testAttempt(1, "scope-a", "digest-a", now)
	if _, err := store.ClaimAttempt(ctx, planning); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.CancelAttempt(ctx, planning.AttemptID, now.Add(time.Second))
	if err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	if _, err := store.CancelAttempt(ctx, planning.AttemptID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("cancel replay: %v", err)
	}

	signing := testAttempt(2, "scope-b", "digest-b", now)
	if _, err := store.ClaimAttempt(ctx, signing); err != nil {
		t.Fatal(err)
	}
	note := fmt.Sprintf("%064x:0", 30)
	if err := store.ReserveAttemptPlan(ctx, signing.AttemptID, "regtest", []byte(`{"plan":2}`), "sha256:second", "200000", 140, []string{note}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAttemptSigning(ctx, signing.AttemptID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelAttempt(ctx, signing.AttemptID, now.Add(2*time.Second)); !errors.Is(err, storage.ErrAttemptStateConflict) {
		t.Fatalf("signing cancellation=%v", err)
	}
	active, err := store.ActiveNoteIDs(ctx, "regtest", "hot")
	if err != nil || len(active) != 1 || active[0] != note {
		t.Fatalf("signing reservations=%v err=%v", active, err)
	}
}

func testAttempt(number int, scope, digest string, now time.Time) storage.TransactionAttempt {
	return storage.TransactionAttempt{
		AttemptID:            fmt.Sprintf("txn_%032x", number),
		ScopedIdempotencyKey: scope,
		RequestDigest:        digest,
		PrincipalName:        "exchange",
		WalletID:             "hot",
		ApprovalReference:    fmt.Sprintf("withdrawal-%d", number),
		RequestJSON:          []byte(fmt.Sprintf(`{"wallet_id":"hot","approval_reference":"withdrawal-%d","outputs":[]}`, number)),
		CreatedAt:            now,
	}
}

func ensureAttemptWallet(t *testing.T, store *Store) {
	t.Helper()
	if err := store.EnsureWallet(context.Background(), "hot", "regtest", fingerprint("attempt-hot-ufvk"), 0); err != nil {
		t.Fatal(err)
	}
}
