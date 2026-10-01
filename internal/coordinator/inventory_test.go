package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

func TestReservedNotesKeepAttemptPlanningUntilReleased(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	note := fmt.Sprintf("%064x:0", 1)
	planner := &fakePlanner{notes: []string{note}}
	service := newCoordinatorTestService(t, cfg, store, planner, &fakeSigner{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	first, _, err := service.Create(ctx, "exchange", "withdrawal-1-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	first = waitAttemptState(t, service, "exchange", first.AttemptID, "signed")
	second, _, err := service.Create(ctx, "exchange", "withdrawal-2-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	blocked := waitAttemptError(t, service, "exchange", second.AttemptID, "notes_reserved")
	if blocked.State != "planning" || !blocked.Error.Retryable {
		t.Fatalf("blocked=%+v", blocked)
	}

	// A wallet that is genuinely short stays a hard failure.
	planner.setNotes()
	third, _, err := service.Create(ctx, "exchange", "withdrawal-3-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	failed := waitAttemptState(t, service, "exchange", third.AttemptID, "failed_unsigned")
	if failed.Error == nil || failed.Error.Code != "insufficient_balance" || failed.Error.Retryable {
		t.Fatalf("failed=%+v", failed)
	}

	// Once the first attempt's note is released, the parked attempt plans
	// with it on the next pass.
	planner.setNotes(note)
	if err := store.MarkAttemptState(ctx, first.AttemptID, "released", "", "", false, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Queued work inside the backoff window is skipped; once the window has
	// passed, the parked attempt plans again.
	service.enqueue(second.AttemptID)
	time.Sleep(100 * time.Millisecond)
	if parked, _, _ := store.Attempt(ctx, second.AttemptID); parked.State != "planning" {
		t.Fatalf("parked attempt advanced inside backoff: %+v", parked)
	}
	if err := store.MarkAttemptState(ctx, second.AttemptID, "planning", "notes_reserved", blocked.Error.Message, true, false, time.Now().Add(-time.Minute).UTC()); err != nil {
		t.Fatal(err)
	}
	service.enqueue(second.AttemptID)
	signed := waitAttemptState(t, service, "exchange", second.AttemptID, "signed")
	if len(signed.SelectedNoteIDs) != 1 || signed.SelectedNoteIDs[0] != note || signed.Error != nil {
		t.Fatalf("signed=%+v", signed)
	}
}

func TestWithdrawalChangeIsSplitBelowTargetInventory(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	cfg.CoordinatorTargetNotes = 5
	cfg.CoordinatorChangeSplitMax = 3
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 1)}}
	scanner := &fakeCoordinatorScanner{spendable: 1, unspentNotes: true}
	node := &fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: fmt.Sprintf("%064x", 100)}}
	service := newCoordinatorTestServiceWithChain(t, cfg, store, planner, &fakeSigner{}, node, scanner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	attempt, _, err := service.Create(ctx, "exchange", "withdrawal-1-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	signed := waitAttemptState(t, service, "exchange", attempt.AttemptID, "signed")
	options := planner.recordedOptions()
	if len(options) != 1 || options[0].SplitChange != 3 {
		t.Fatalf("options=%+v", options)
	}
	// Only the approved output is mapped back to the exchange; split change
	// outputs stay internal.
	if len(signed.OrchardOutputActionIndices) != 1 || signed.OrchardOutputActionIndices[0] != 0 {
		t.Fatalf("signed=%+v", signed)
	}
}

func TestMultiNoteWithdrawalSplitsChangeForEverySpentNote(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	cfg.CoordinatorTargetNotes = 3
	cfg.CoordinatorChangeSplitMax = 8
	planner := &fakePlanner{spends: 3, notes: []string{fmt.Sprintf("%064x:0", 1), fmt.Sprintf("%064x:0", 2), fmt.Sprintf("%064x:0", 3)}}
	// Inventory is at target, so a one-note withdrawal would not split. This
	// plan spends three notes, so its change must replace all three.
	scanner := &fakeCoordinatorScanner{spendable: 3, unspentNotes: true}
	node := &fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: fmt.Sprintf("%064x", 100)}}
	service := newCoordinatorTestServiceWithChain(t, cfg, store, planner, &fakeSigner{}, node, scanner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	attempt, _, err := service.Create(ctx, "exchange", "withdrawal-1-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	signed := waitAttemptState(t, service, "exchange", attempt.AttemptID, "signed")
	options := planner.recordedOptions()
	if len(options) != 2 || options[0].SplitChange != 0 || options[1].SplitChange != 3 {
		t.Fatalf("options=%+v", options)
	}
	if len(signed.SelectedNoteIDs) != 3 || len(signed.OrchardOutputActionIndices) != 1 {
		t.Fatalf("signed=%+v", signed)
	}
	// Widening raises the fee, so the wider plan can select more notes; the
	// split keeps growing until it covers the final selection.
	planner.setNotes(fmt.Sprintf("%064x:0", 4), fmt.Sprintf("%064x:0", 5), fmt.Sprintf("%064x:0", 6), fmt.Sprintf("%064x:0", 7), fmt.Sprintf("%064x:0", 8))
	// Three notes stay reserved by the first attempt; six spendable leaves the
	// wallet exactly at target, so the first plan does not split.
	scanner.mu.Lock()
	scanner.spendable = 6
	scanner.mu.Unlock()
	planner.mu.Lock()
	planner.spends, planner.growWithSplit = 2, true
	planner.options = nil
	planner.mu.Unlock()
	grown, _, err := service.Create(ctx, "exchange", "withdrawal-2-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	grownSigned := waitAttemptState(t, service, "exchange", grown.AttemptID, "signed")
	options = planner.recordedOptions()
	if len(options) != 3 || options[0].SplitChange != 0 || options[1].SplitChange != 2 || options[2].SplitChange != 3 || len(grownSigned.SelectedNoteIDs) != 3 {
		t.Fatalf("grown options=%+v signed=%+v", options, grownSigned)
	}
	if got := (changeSplitPolicy{deficit: -2, max: 8}).options(1); got.SplitChange != 0 {
		t.Fatalf("surplus split=%+v", got)
	}
	if got := (changeSplitPolicy{deficit: 1, max: 4}).options(9); got.SplitChange != 4 {
		t.Fatalf("capped split=%+v", got)
	}
}

func TestReplayDoesNotBypassReservedNoteBackoff(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 1)}}
	service := newCoordinatorTestService(t, cfg, store, planner, &fakeSigner{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	first, _, err := service.Create(ctx, "exchange", "withdrawal-1-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	waitAttemptState(t, service, "exchange", first.AttemptID, "signed")
	second, _, err := service.Create(ctx, "exchange", "withdrawal-2-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	waitAttemptError(t, service, "exchange", second.AttemptID, "notes_reserved")
	calls := planner.calls()
	for range 5 {
		replay, replayed, err := service.Create(ctx, "exchange", "withdrawal-2-create", coordinatorRequest("100000"))
		if err != nil || !replayed || replay.State != "planning" {
			t.Fatalf("replay=%+v replayed=%v err=%v", replay, replayed, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := planner.calls(); got != calls {
		t.Fatalf("planner calls after replays = %d, want %d", got, calls)
	}
}

func TestSplitOptionsStayOffWhenInventoryIsHealthyOrUnknown(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	cfg.CoordinatorTargetNotes = 3
	cfg.CoordinatorChangeSplitMax = 8
	scanner := &fakeCoordinatorScanner{spendable: 5, unspentNotes: true}
	node := &fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: fmt.Sprintf("%064x", 100)}}
	service := newCoordinatorTestServiceWithChain(t, cfg, store, &fakePlanner{}, &fakeSigner{}, node, scanner)
	ctx := context.Background()
	if got := service.splitPolicy(ctx, coordinatorRequest("1"), "hot").options(1); got.SplitChange != 0 {
		t.Fatalf("healthy split=%+v", got)
	}
	scanner.mu.Lock()
	scanner.spendable = 0
	scanner.mu.Unlock()
	if got := service.splitPolicy(ctx, coordinatorRequest("1"), "hot").options(1); got.SplitChange != 0 {
		t.Fatalf("unknown inventory split=%+v", got)
	}
	split := CreateRequest{WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 4, NoteZat: "1000"}}
	scanner.mu.Lock()
	scanner.spendable = 1
	scanner.mu.Unlock()
	if got := service.splitPolicy(ctx, split, "hot").options(1); got.SplitChange != 0 {
		t.Fatalf("split request split=%+v", got)
	}
	if got := service.splitPolicy(ctx, coordinatorRequest("1"), "hot").options(1); got.SplitChange != 3 {
		t.Fatalf("low inventory split=%+v", got)
	}
}

func TestNoteSplitAttemptPlansRebalanceToChangeAddress(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 1)}}
	service := newCoordinatorTestService(t, cfg, store, planner, &fakeSigner{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	request := CreateRequest{WalletID: "hot", ApprovalReference: "split-1", Split: &NoteSplit{NoteCount: 4, NoteZat: "25000"}}
	attempt, _, err := service.Create(ctx, "exchange", "split-1-create", request)
	if err != nil {
		t.Fatal(err)
	}
	signed := waitAttemptState(t, service, "exchange", attempt.AttemptID, "signed")
	stored, found, err := store.Attempt(ctx, attempt.AttemptID)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	var plan txPlan
	if err := json.Unmarshal(stored.PlanJSON, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Kind != "rebalance" || len(plan.Outputs) != 4 {
		t.Fatalf("plan=%+v", plan)
	}
	for _, output := range plan.Outputs {
		if output.ToAddress != signed.ChangeAddress || output.AmountZat != "25000" {
			t.Fatalf("plan=%+v change=%s", plan, signed.ChangeAddress)
		}
	}
	if len(signed.OrchardOutputActionIndices) != 4 {
		t.Fatalf("signed=%+v", signed)
	}
	if options := planner.recordedOptions(); len(options) != 1 || options[0].SplitChange != 0 {
		t.Fatalf("options=%+v", options)
	}
}

func TestNormalizeNoteSplitRequest(t *testing.T) {
	cfg, _ := coordinatorTestConfig(t)
	cfg.CoordinatorMinNoteZat = 1000
	wallet := cfg.Wallets[0]
	valid := CreateRequest{WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 2, NoteZat: "1000"}}
	if _, _, _, err := normalizeCreateRequest(valid, cfg, wallet); err != nil {
		t.Fatalf("valid split err=%v", err)
	}
	for name, request := range map[string]CreateRequest{
		"one note":       {WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 1, NoteZat: "1000"}},
		"too many notes": {WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 200, NoteZat: "1000"}},
		"below minimum":  {WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 2, NoteZat: "999"}},
		"zero value":     {WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 2, NoteZat: "0"}},
		"with outputs": {WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 2, NoteZat: "1000"},
			Outputs: []Output{{ToAddress: "jregtest1destination", AmountZat: "1"}}},
	} {
		if _, _, _, err := normalizeCreateRequest(request, cfg, wallet); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestValidatePlanOnlyAcceptsTrailingChangeOutputsForWithdrawals(t *testing.T) {
	wallet := config.Wallet{WalletID: "hot", Account: 0}
	request := coordinatorRequest("100000")
	change := "jregtest1change"
	note := []planNote{{NoteID: fmt.Sprintf("%064x:0", 1)}}
	build := func(kind string, outputs []Output) []byte {
		raw, _ := json.Marshal(txPlan{Version: "v0", Kind: kind, WalletID: "hot", CoinType: 8135, Chain: "regtest", ExpiryHeight: 140,
			Outputs: outputs, ChangeAddress: change, FeeZat: "200000", Notes: note})
		return raw
	}
	approved := request.Outputs[0]
	ok, err := validatePlan(build("withdrawal", []Output{approved, {ToAddress: change, AmountZat: "5000"}}), request, wallet, domain.Regtest, change)
	if err != nil || ok.OutputCount != 2 {
		t.Fatalf("split change result=%+v err=%v", ok, err)
	}
	for name, outputs := range map[string][]Output{
		"extra to stranger":   {approved, {ToAddress: "jregtest1other", AmountZat: "5000"}},
		"extra with memo":     {approved, {ToAddress: change, AmountZat: "5000", MemoHex: "00"}},
		"extra zero":          {approved, {ToAddress: change, AmountZat: "0"}},
		"change before payee": {{ToAddress: change, AmountZat: "5000"}, approved},
		"missing payee":       {},
	} {
		if _, err := validatePlan(build("withdrawal", outputs), request, wallet, domain.Regtest, change); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	if _, err := validatePlan(build("rebalance", []Output{approved}), request, wallet, domain.Regtest, change); err == nil {
		t.Fatal("withdrawal request accepted a rebalance plan")
	}
	split := CreateRequest{WalletID: "hot", ApprovalReference: "split", Split: &NoteSplit{NoteCount: 2, NoteZat: "1000"}}
	splitOutputs := split.planOutputs(change)
	if _, err := validatePlan(build("rebalance", splitOutputs), split, wallet, domain.Regtest, change); err != nil {
		t.Fatalf("split plan err=%v", err)
	}
	if _, err := validatePlan(build("rebalance", append(splitOutputs, Output{ToAddress: change, AmountZat: "1000"})), split, wallet, domain.Regtest, change); err == nil {
		t.Fatal("split plan accepted extra outputs")
	}
}

func TestNoteInventoryReportsReservationsAcrossCredentials(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	cfg.CoordinatorTargetNotes = 4
	ownerToken, otherToken := "owner-token-123456789", "other-token-123456789"
	cfg.Credentials = []config.Credential{
		{Name: "exchange", TokenHash: sha256.Sum256([]byte(ownerToken)), Scopes: []string{"plan"}, Wallets: []string{"hot"}},
		{Name: "other", TokenHash: sha256.Sum256([]byte(otherToken)), Scopes: []string{"plan"}, Wallets: []string{"hot"}},
	}
	scanner := &fakeCoordinatorScanner{spendable: 3, unspentNotes: true}
	node := &fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: fmt.Sprintf("%064x", 100)}}
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 1)}}
	service := newCoordinatorTestServiceWithChain(t, cfg, store, planner, &fakeSigner{}, node, scanner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)
	attempt, _, err := service.Create(ctx, "exchange", "withdrawal-1-create", coordinatorRequest("100000"))
	if err != nil {
		t.Fatal(err)
	}
	waitAttemptState(t, service, "exchange", attempt.AttemptID, "signed")

	handler, err := NewHandler(cfg, service)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/wallets/hot/note-inventory", nil)
	r.Header.Set("Authorization", "Bearer "+otherToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "raw_tx_hex") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data NoteInventory `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	inventory := envelope.Data
	if inventory.Spendable.NoteCount != 3 || inventory.Reserved.NoteCount != 1 || inventory.Unreserved.NoteCount != 2 ||
		!inventory.LowNoteInventory || inventory.TargetNotes != 4 || !inventory.ReservationsComplete ||
		len(inventory.ReservedByAttempts) != 1 || inventory.ReservedByAttempts[0].AttemptID != attempt.AttemptID ||
		inventory.ReservedByAttempts[0].AttemptState != "signed" || inventory.ReservedByAttempts[0].ExpiryHeight != 140 {
		t.Fatalf("inventory=%+v", inventory)
	}

	r = httptest.NewRequest(http.MethodGet, "/v1/wallets/cold/note-inventory", nil)
	r.Header.Set("Authorization", "Bearer "+ownerToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong wallet status=%d body=%s", w.Code, w.Body.String())
	}

	scanner.mu.Lock()
	scanner.spendable = 0
	scanner.mu.Unlock()
	if _, err := service.NoteInventory(ctx, "hot"); !hasOperationCode(err, "not_found") {
		t.Fatalf("missing scanner wallet err=%v", err)
	}
}

func waitAttemptError(t *testing.T, service *Service, principal, attemptID, code string) Attempt {
	t.Helper()
	for range 400 {
		attempt, err := service.Attempt(context.Background(), principal, attemptID)
		if err == nil && attempt.Error != nil && attempt.Error.Code == code {
			return attempt
		}
		time.Sleep(10 * time.Millisecond)
	}
	attempt, err := service.Attempt(context.Background(), principal, attemptID)
	t.Fatalf("attempt did not report %s: attempt=%+v err=%v", code, attempt, err)
	return Attempt{}
}
