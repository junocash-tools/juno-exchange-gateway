package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
	sqlitestore "github.com/junocash-tools/juno-exchange-gateway/internal/storage/sqlite"
)

func TestServiceCreatesReplayableSignedAttemptsWithDisjointReservations(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 1), fmt.Sprintf("%064x:0", 2)}}
	signer := &fakeSigner{}
	service := newCoordinatorTestService(t, cfg, store, planner, signer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	firstRequest := coordinatorRequest("100000")
	first, replayed, err := service.Create(ctx, "exchange", "withdrawal-1-attempt-1", firstRequest)
	if err != nil || replayed {
		t.Fatalf("first=%+v replayed=%v err=%v", first, replayed, err)
	}
	second, replayed, err := service.Create(ctx, "exchange", "withdrawal-2-attempt-1", coordinatorRequest("200000"))
	if err != nil || replayed {
		t.Fatalf("second=%+v replayed=%v err=%v", second, replayed, err)
	}
	first = waitAttemptState(t, service, "exchange", first.AttemptID, "signed")
	second = waitAttemptState(t, service, "exchange", second.AttemptID, "signed")
	if len(first.SelectedNoteIDs) != 1 || len(second.SelectedNoteIDs) != 1 || first.SelectedNoteIDs[0] == second.SelectedNoteIDs[0] {
		t.Fatalf("reservations overlap: first=%v second=%v", first.SelectedNoteIDs, second.SelectedNoteIDs)
	}
	active, err := store.ActiveNoteIDs(ctx, "regtest", "hot")
	if err != nil || len(active) != 2 {
		t.Fatalf("active=%v err=%v", active, err)
	}
	replay, replayed, err := service.Create(ctx, "exchange", "withdrawal-1-attempt-1", firstRequest)
	if err != nil || !replayed || replay.AttemptID != first.AttemptID || replay.RawTxHex != first.RawTxHex {
		t.Fatalf("replay=%+v replayed=%v err=%v", replay, replayed, err)
	}
	if _, _, err := service.Create(ctx, "exchange", "withdrawal-1-attempt-1", coordinatorRequest("100001")); !hasOperationCode(err, "idempotency_conflict") {
		t.Fatalf("conflict err=%v", err)
	}
	if _, err := service.Cancel(ctx, "exchange", first.AttemptID); !hasOperationCode(err, "attempt_not_cancellable") {
		t.Fatalf("signed cancellation err=%v", err)
	}
}

func TestServiceRecoversUnknownSigningOutcomeWithoutReplanning(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 3)}}
	signer := &fakeSigner{unknownOnce: true}
	service := newCoordinatorTestService(t, cfg, store, planner, signer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	attempt, _, err := service.Create(ctx, "exchange", "withdrawal-3-attempt-1", coordinatorRequest("300000"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := waitAttemptState(t, service, "exchange", attempt.AttemptID, "signing_unknown")
	if unknown.Error == nil || !unknown.Error.Retryable {
		t.Fatalf("unknown attempt=%+v", unknown)
	}
	active, err := store.ActiveNoteIDs(ctx, "regtest", "hot")
	if err != nil || len(active) != 1 {
		t.Fatalf("unknown reservations=%v err=%v", active, err)
	}
	service.enqueue(attempt.AttemptID)
	signed := waitAttemptState(t, service, "exchange", attempt.AttemptID, "signed")
	if signed.RawTxHex == "" || planner.calls() != 1 || signer.calls() != 2 {
		t.Fatalf("signed=%+v planner_calls=%d signer_calls=%d", signed, planner.calls(), signer.calls())
	}
}

func TestServiceKeepsPriorSigningUncertaintySticky(t *testing.T) {
	t.Run("unknown retry returns busy", func(t *testing.T) {
		cfg, store := coordinatorTestConfig(t)
		noteID := fmt.Sprintf("%064x:0", 31)
		signer := &fakeSigner{signErrors: []error{
			&operationError{Code: "signing_outcome_unknown", Message: "first response was lost", Retryable: true, OutcomeUnknown: true},
			&operationError{Code: "signer_busy", Message: "journal is busy", Retryable: true},
		}}
		service := newCoordinatorTestService(t, cfg, store, &fakePlanner{notes: []string{noteID}}, signer)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service.Start(ctx)

		created, _, err := service.Create(ctx, "exchange", "withdrawal-31-attempt-1", coordinatorRequest("310000"))
		if err != nil {
			t.Fatal(err)
		}
		waitAttemptState(t, service, "exchange", created.AttemptID, "signing_unknown")
		service.enqueue(created.AttemptID)
		waitSignerCallCount(t, signer, 2)
		unknown := waitAttemptState(t, service, "exchange", created.AttemptID, "signing_unknown")
		if unknown.Error == nil || unknown.Error.Code != "signer_busy" {
			t.Fatalf("unknown attempt=%+v", unknown)
		}
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 1 || active[0] != noteID {
			t.Fatalf("unknown reservations=%v", active)
		}
	})

	t.Run("recovered signing returns plan rejection", func(t *testing.T) {
		cfg, store := coordinatorTestConfig(t)
		noteID := fmt.Sprintf("%064x:0", 32)
		signer := &fakeSigner{signErrors: []error{
			&operationError{Code: "plan_not_allowed", Message: "current retry rejected the plan", Retryable: false},
		}}
		service := newCoordinatorTestService(t, cfg, store, &fakePlanner{notes: []string{noteID}}, signer)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		created, _, err := service.Create(ctx, "exchange", "withdrawal-32-attempt-1", coordinatorRequest("320000"))
		if err != nil {
			t.Fatal(err)
		}
		stored, found, err := store.Attempt(ctx, created.AttemptID)
		if err != nil || !found || !service.planAttempt(ctx, stored) {
			t.Fatalf("planning attempt=%+v found=%v err=%v", stored, found, err)
		}
		if _, err := store.BeginAttemptSigning(ctx, created.AttemptID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		service.Start(ctx)
		waitSignerCallCount(t, signer, 1)
		unknown := waitAttemptState(t, service, "exchange", created.AttemptID, "signing_unknown")
		if unknown.Error == nil || unknown.Error.Code != "plan_not_allowed" {
			t.Fatalf("unknown attempt=%+v", unknown)
		}
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 1 || active[0] != noteID {
			t.Fatalf("unknown reservations=%v", active)
		}
	})
}

func TestServiceReleasesReservationsOnlyAfterFinalityOrExpiryProof(t *testing.T) {
	t.Run("confirmed finality", func(t *testing.T) {
		cfg, store := coordinatorTestConfig(t)
		noteID := fmt.Sprintf("%064x:0", 5)
		hash := fmt.Sprintf("%064x", 100)
		node := &fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: hash}}
		scanner := &fakeCoordinatorScanner{}
		service := newCoordinatorTestServiceWithChain(t, cfg, store, &fakePlanner{notes: []string{noteID}}, &fakeSigner{}, node, scanner)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service.Start(ctx)
		created, _, err := service.Create(ctx, "exchange", "withdrawal-5-attempt-1", coordinatorRequest("500000"))
		if err != nil {
			t.Fatal(err)
		}
		signed := waitAttemptState(t, service, "exchange", created.AttemptID, "signed")
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 1 {
			t.Fatalf("signed reservations=%v", active)
		}
		node.setTransaction(domain.Transaction{TxID: signed.TxID, State: "confirmed", Confirmations: 100}, true)
		service.enqueue(signed.AttemptID)
		time.Sleep(50 * time.Millisecond)
		if current, err := service.Attempt(ctx, "exchange", signed.AttemptID); err != nil || current.State != "signed" {
			t.Fatalf("reservation released before scanner proof: attempt=%+v err=%v", current, err)
		}
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 1 {
			t.Fatalf("pre-proof reservations=%v", active)
		}
		ready, complete, pendingReady := true, true, true
		confirmations := int64(100)
		height, sourceHeight, value, spentHeight, spentConfirmedHeight := int64(100), int64(1), int64(700000), int64(1), int64(100)
		txid := signed.TxID
		scanner.setProof(domain.ScannerHealth{Status: "ok", Network: "regtest", UAHRP: "jregtest", Confirmations: &confirmations, Ready: &ready, HistoryComplete: &complete,
			PendingSpendsReady: &pendingReady, ScannedHeight: &height, ScannedHash: hash}, domain.WalletNoteStatuses{
			WalletID: "hot", EventEpoch: fmt.Sprintf("%064x", 1), AsOfScannerHeight: height, AsOfScannerHash: hash,
			Statuses: []domain.NoteStatus{{NoteID: noteID, State: "spent", SourceHeight: &sourceHeight, ValueZat: &value,
				SpentTxID: &txid, SpentHeight: &spentHeight, SpentConfirmedHeight: &spentConfirmedHeight}},
		})
		service.enqueue(signed.AttemptID)
		waitAttemptState(t, service, "exchange", signed.AttemptID, "final")
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 0 {
			t.Fatalf("final reservations=%v", active)
		}
	})

	t.Run("expired unspent proof", func(t *testing.T) {
		cfg, store := coordinatorTestConfig(t)
		noteID := fmt.Sprintf("%064x:0", 6)
		node := &fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: fmt.Sprintf("%064x", 100)}}
		scanner := &fakeCoordinatorScanner{}
		service := newCoordinatorTestServiceWithChain(t, cfg, store, &fakePlanner{notes: []string{noteID}}, &fakeSigner{}, node, scanner)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service.Start(ctx)
		created, _, err := service.Create(ctx, "exchange", "withdrawal-6-attempt-1", coordinatorRequest("600000"))
		if err != nil {
			t.Fatal(err)
		}
		signed := waitAttemptState(t, service, "exchange", created.AttemptID, "signed")
		boundaryHash := fmt.Sprintf("%064x", 140)
		node.setTip(domain.NodeTip{Network: "regtest", Height: 140, Hash: boundaryHash})
		service.enqueue(signed.AttemptID)
		time.Sleep(50 * time.Millisecond)
		if boundary, err := service.Attempt(ctx, "exchange", signed.AttemptID); err != nil || boundary.State != "signed" {
			t.Fatalf("attempt expired at its valid boundary: attempt=%+v err=%v", boundary, err)
		}
		expiredHash := fmt.Sprintf("%064x", 141)
		node.setTip(domain.NodeTip{Network: "regtest", Height: 141, Hash: expiredHash})
		service.enqueue(signed.AttemptID)
		waitAttemptState(t, service, "exchange", signed.AttemptID, "expired_pending_reconciliation")
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 1 {
			t.Fatalf("expired-pending reservations=%v", active)
		}

		hash := fmt.Sprintf("%064x", 239)
		node.setTip(domain.NodeTip{Network: "regtest", Height: 239, Hash: hash})
		ready, complete, pendingReady := true, true, true
		confirmations := int64(100)
		height := int64(239)
		sourceHeight, value := int64(1), int64(700000)
		scanner.setProof(domain.ScannerHealth{Status: "ok", Network: "regtest", UAHRP: "jregtest", Confirmations: &confirmations, Ready: &ready, HistoryComplete: &complete,
			PendingSpendsReady: &pendingReady, ScannedHeight: &height, ScannedHash: hash}, domain.WalletNoteStatuses{
			WalletID: "hot", EventEpoch: fmt.Sprintf("%064x", 1), AsOfScannerHeight: height, AsOfScannerHash: hash,
			Statuses: []domain.NoteStatus{{NoteID: noteID, State: "unspent", SourceHeight: &sourceHeight, ValueZat: &value}},
		})
		service.enqueue(signed.AttemptID)
		time.Sleep(50 * time.Millisecond)
		if pending, err := service.Attempt(ctx, "exchange", signed.AttemptID); err != nil || pending.State != "expired_pending_reconciliation" {
			t.Fatalf("attempt released before expiry finality: attempt=%+v err=%v", pending, err)
		}
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 1 {
			t.Fatalf("pre-release reservations=%v", active)
		}

		hash = fmt.Sprintf("%064x", 240)
		height = 240
		node.setTip(domain.NodeTip{Network: "regtest", Height: height, Hash: hash})
		scanner.setProof(domain.ScannerHealth{Status: "ok", Network: "regtest", UAHRP: "jregtest", Confirmations: &confirmations, Ready: &ready, HistoryComplete: &complete,
			PendingSpendsReady: &pendingReady, ScannedHeight: &height, ScannedHash: hash}, domain.WalletNoteStatuses{
			WalletID: "hot", EventEpoch: fmt.Sprintf("%064x", 1), AsOfScannerHeight: height, AsOfScannerHash: hash,
			Statuses: []domain.NoteStatus{{NoteID: noteID, State: "unspent", SourceHeight: &sourceHeight, ValueZat: &value}},
		})
		service.enqueue(signed.AttemptID)
		waitAttemptState(t, service, "exchange", signed.AttemptID, "released")
		if active, _ := store.ActiveNoteIDs(ctx, "regtest", "hot"); len(active) != 0 {
			t.Fatalf("released reservations=%v", active)
		}
	})
}

func TestCoordinatorHTTPRequiresPlanCredentialAndUsesStableEnvelope(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	token := "regtest-coordinator-token-123456"
	cfg.Credentials = []config.Credential{{Name: "exchange", TokenHash: sha256.Sum256([]byte(token)), Scopes: []string{"plan"}, Wallets: []string{"hot"}}}
	service := newCoordinatorTestService(t, cfg, store, &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 4)}}, &fakeSigner{})
	handler, err := NewHandler(cfg, service)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"wallet_id":"hot","approval_reference":"withdrawal-4","outputs":[{"to_address":"jregtest1destination","amount_zat":"400000"}]}`
	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/transaction-attempts", strings.NewReader(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorized.Header.Set("Idempotency-Key", "withdrawal-4-attempt-1")
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorizedRecorder.Code, unauthorizedRecorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/transaction-attempts", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "withdrawal-4-attempt-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var envelope struct {
		Status    string  `json:"status"`
		Data      Attempt `json:"data"`
		RequestID string  `json:"request_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Status != "ok" || envelope.Data.State != "planning" || envelope.RequestID == "" {
		t.Fatalf("envelope=%+v err=%v", envelope, err)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/transaction-attempts/"+envelope.Data.AttemptID, nil)
	get.Header.Set("Authorization", "Bearer "+token)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK || !strings.Contains(getRecorder.Body.String(), envelope.Data.AttemptID) {
		t.Fatalf("get status=%d body=%s", getRecorder.Code, getRecorder.Body.String())
	}
}

func TestRecoveredDatabaseSealBlocksCoordinatorReadinessAndCreation(t *testing.T) {
	cfg, baseStore := coordinatorTestConfig(t)
	token := "regtest-recovery-seal-token-123456"
	cfg.Credentials = []config.Credential{{Name: "exchange", TokenHash: sha256.Sum256([]byte(token)), Scopes: []string{"plan"}, Wallets: []string{"hot"}}}
	planner := &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 45)}}
	signer := &fakeSigner{}
	store := recoveryGateStore{Store: baseStore, sealed: true}
	service := newCoordinatorTestService(t, cfg, store, planner, signer)

	if err := service.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "sealed after gateway database recovery") {
		t.Fatalf("ready error=%v", err)
	}
	if _, _, err := service.Create(context.Background(), "exchange", "withdrawal-sealed-attempt-1", coordinatorRequest("450000")); !hasOperationCode(err, "coordinator_recovery_sealed") {
		t.Fatalf("create error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	if planner.calls() != 0 || signer.calls() != 0 {
		t.Fatalf("sealed coordinator planner_calls=%d signer_calls=%d", planner.calls(), signer.calls())
	}
	attempts, err := baseStore.RecoverableAttempts(context.Background(), "", 10)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("sealed coordinator attempts=%v err=%v", attempts, err)
	}

	handler, err := NewHandler(cfg, service)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"wallet_id":"hot","approval_reference":"withdrawal:sealed","outputs":[{"to_address":"jregtest1destination","amount_zat":"450000"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/transaction-attempts", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "withdrawal-sealed-attempt-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"coordinator_recovery_sealed"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	unavailable := newCoordinatorTestService(t, cfg, recoveryGateStore{Store: baseStore, err: errors.New("corrupt gate")}, planner, signer)
	if _, _, err := unavailable.Create(context.Background(), "exchange", "withdrawal-gate-unavailable-1", coordinatorRequest("450000")); !hasOperationCode(err, "recovery_gate_unavailable") {
		t.Fatalf("unavailable gate create error=%v", err)
	}
}

func TestCoordinatorHTTPForbiddenCancelDoesNotReleaseReservation(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	token := "regtest-revoked-wallet-token-123456"
	cfg.Credentials = []config.Credential{{Name: "exchange", TokenHash: sha256.Sum256([]byte(token)), Scopes: []string{"plan"}, Wallets: []string{"cold"}}}
	service := newCoordinatorTestService(t, cfg, store, &fakePlanner{}, &fakeSigner{})
	handler, err := NewHandler(cfg, service)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	attemptID := "txn_0123456789abcdef0123456789abcdef"
	claim, err := store.ClaimAttempt(context.Background(), storage.TransactionAttempt{
		AttemptID:            attemptID,
		ScopedIdempotencyKey: "exchange\x00withdrawal-revoked-wallet-1",
		RequestDigest:        "sha256:request",
		PrincipalName:        "exchange",
		WalletID:             "hot",
		ApprovalReference:    "withdrawal:revoked-wallet",
		RequestJSON:          []byte(`{"wallet_id":"hot"}`),
		CreatedAt:            now,
	})
	if err != nil || claim.State != storage.ClaimAcquired {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	noteID := fmt.Sprintf("%064x:0", 44)
	if err := store.ReserveAttemptPlan(context.Background(), attemptID, "regtest", []byte(`{"version":"v0"}`), "sha256:plan", "200000", 140, []string{noteID}, now); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/transaction-attempts/"+attemptID+"/cancel", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	stored, found, err := store.Attempt(context.Background(), attemptID)
	if err != nil || !found || stored.State != "reserved" {
		t.Fatalf("attempt=%+v found=%v err=%v", stored, found, err)
	}
	active, err := store.ActiveNoteIDs(context.Background(), "regtest", "hot")
	if err != nil || len(active) != 1 || active[0] != noteID {
		t.Fatalf("active reservations=%v err=%v", active, err)
	}
}

func TestCreateRejectsNonCanonicalFinancialInputsBeforeClaim(t *testing.T) {
	cfg, store := coordinatorTestConfig(t)
	service := newCoordinatorTestService(t, cfg, store, &fakePlanner{notes: []string{fmt.Sprintf("%064x:0", 7)}}, &fakeSigner{})
	for name, mutate := range map[string]func(*CreateRequest){
		"floating amount": func(request *CreateRequest) { request.Outputs[0].AmountZat = "1.5" },
		"leading zero":    func(request *CreateRequest) { request.Outputs[0].AmountZat = "010" },
		"uppercase memo":  func(request *CreateRequest) { request.Outputs[0].MemoHex = "AA" },
		"wrong network":   func(request *CreateRequest) { request.Outputs[0].ToAddress = "j1mainnet" },
	} {
		t.Run(name, func(t *testing.T) {
			request := coordinatorRequest("100")
			mutate(&request)
			if _, _, err := service.Create(context.Background(), "exchange", "invalid-"+strings.ReplaceAll(name, " ", "-"), request); !hasOperationCode(err, "invalid_request") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestValidateSignerResultRejectsActionIndicesOutsideOrchardBundle(t *testing.T) {
	request := coordinatorRequest("100")
	for name, result := range map[string]signerResult{
		"requested output": {OrchardOutputActionIndices: []uint32{200}},
		"change output": {
			OrchardOutputActionIndices: []uint32{0},
			OrchardChangeActionIndex:   pointerTo(uint32(200)),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSignerResult(result, request); err == nil || !strings.Contains(err.Error(), "above 199") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func pointerTo[T any](value T) *T { return &value }

type recoveryGateStore struct {
	storage.Store
	sealed bool
	err    error
}

func (s recoveryGateStore) CoordinatorRecoverySealed(context.Context) (bool, error) {
	return s.sealed, s.err
}

func coordinatorTestConfig(t *testing.T) (config.Config, *sqlitestore.Store) {
	t.Helper()
	cfg := config.Config{
		Network: domain.Regtest, Wallets: []config.Wallet{{WalletID: "hot", UFVK: "uviewregtest1placeholder", Account: 0}},
		DefaultConfirmations: 100, CoordinatorMaxOutputs: 199, CoordinatorMaxAmountZat: 2_100_000_000_000_000,
		CoordinatorPlanTimeout: time.Second, CoordinatorSignTimeout: time.Second, CoordinatorMaxReplans: 3,
		CoordinatorMaxBodyBytes: 1 << 20, CoordinatorRate: config.RateLimit{RPS: 100, Burst: 100},
		ReadTimeout: time.Second, UpstreamTimeout: time.Second, CoordinatorTxbuildPath: "/bin/true",
		CoordinatorFeeMultiplier: 20, CoordinatorExpiryOffset: 40, CoordinatorListenAddress: "127.0.0.1:8081",
		CoordinatorSignerSocket: "/tmp/test-signer.sock", CoordinatorWorkDir: t.TempDir(),
	}
	dsn := "file:" + filepath.Join(t.TempDir(), "coordinator.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	store, err := sqlitestore.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureWallet(context.Background(), "hot", "regtest", fmt.Sprintf("%064x", 9), 0); err != nil {
		t.Fatal(err)
	}
	return cfg, store
}

func newCoordinatorTestService(t *testing.T, cfg config.Config, store storage.Store, planner Planner, signer Signer) *Service {
	t.Helper()
	return newCoordinatorTestServiceWithChain(t, cfg, store, planner, signer,
		&fakeCoordinatorNode{tip: domain.NodeTip{Network: "regtest", Height: 100, Hash: fmt.Sprintf("%064x", 100)}},
		&fakeCoordinatorScanner{})
}

func newCoordinatorTestServiceWithChain(t *testing.T, cfg config.Config, store storage.Store, planner Planner, signer Signer, node domain.Node, scanner domain.Scanner) *Service {
	t.Helper()
	var allocationMu sync.Mutex
	allocation := 0
	service, err := NewService(cfg, store, node, scanner,
		func(context.Context, string, string) (storage.Address, error) {
			allocationMu.Lock()
			defer allocationMu.Unlock()
			allocation++
			return storage.Address{WalletID: "hot", Address: fmt.Sprintf("jregtest1change%d", allocation), DiversifierIndex: uint32(allocation)}, nil
		}, planner, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func coordinatorRequest(amount string) CreateRequest {
	return CreateRequest{WalletID: "hot", ApprovalReference: "withdrawal-approval", Outputs: []Output{{ToAddress: "jregtest1destination", AmountZat: amount}}}
}

func waitAttemptState(t *testing.T, service *Service, principal, attemptID, state string) Attempt {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		attempt, err := service.Attempt(context.Background(), principal, attemptID)
		if err == nil && attempt.State == state {
			return attempt
		}
		time.Sleep(10 * time.Millisecond)
	}
	attempt, err := service.Attempt(context.Background(), principal, attemptID)
	t.Fatalf("attempt did not reach %s: attempt=%+v err=%v", state, attempt, err)
	return Attempt{}
}

func hasOperationCode(err error, code string) bool {
	var operation *operationError
	return errors.As(err, &operation) && operation.Code == code
}

type fakePlanner struct {
	mu        sync.Mutex
	notes     []string
	callCount int
}

func (p *fakePlanner) Plan(_ context.Context, request CreateRequest, wallet config.Wallet, change string, excluded []string) (planResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCount++
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, note := range excluded {
		excludedSet[note] = struct{}{}
	}
	selected := ""
	for _, candidate := range p.notes {
		if _, skip := excludedSet[candidate]; !skip {
			selected = candidate
			break
		}
	}
	if selected == "" {
		return planResult{}, opError("insufficient_balance", "no unreserved test note", false)
	}
	raw, _ := json.Marshal(txPlan{Version: "v0", Kind: "withdrawal", WalletID: wallet.WalletID, CoinType: 8135, Account: wallet.Account,
		Chain: "regtest", ExpiryHeight: 140, Outputs: request.Outputs, ChangeAddress: change, FeeZat: "200000", Notes: []planNote{{NoteID: selected}}})
	return validatePlan(raw, request, wallet, domain.Regtest, change)
}

func (p *fakePlanner) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

type fakeSigner struct {
	mu          sync.Mutex
	callCount   int
	unknownOnce bool
	signErrors  []error
}

func (s *fakeSigner) Sign(_ context.Context, _ string, plan planResult) (signerResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callCount++
	if len(s.signErrors) > 0 {
		err := s.signErrors[0]
		s.signErrors = s.signErrors[1:]
		return signerResult{}, err
	}
	if s.unknownOnce {
		s.unknownOnce = false
		return signerResult{}, &operationError{Code: "signing_outcome_unknown", Message: "test outcome unknown", Retryable: true, OutcomeUnknown: true}
	}
	return signerResult{TxID: fmt.Sprintf("%064x", s.callCount+100), RawTxHex: "00", FeeZat: plan.FeeZat,
		OrchardOutputActionIndices: []uint32{0}, OrchardChangeActionIndex: uint32Pointer(1)}, nil
}

func (s *fakeSigner) Health(context.Context) error { return nil }
func (s *fakeSigner) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

func waitSignerCallCount(t *testing.T, signer *fakeSigner, expected int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if signer.calls() >= expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("signer calls=%d, expected at least %d", signer.calls(), expected)
}
func uint32Pointer(value uint32) *uint32 { return &value }

type fakeCoordinatorNode struct {
	mu    sync.Mutex
	tip   domain.NodeTip
	tx    domain.Transaction
	found bool
}

func (n *fakeCoordinatorNode) Tip(context.Context) (domain.NodeTip, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tip, nil
}
func (n *fakeCoordinatorNode) BlockHash(context.Context, int64) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tip.Hash, nil
}
func (*fakeCoordinatorNode) DecodeRawTransaction(context.Context, string) (string, error) {
	return "", errors.New("not implemented")
}
func (n *fakeCoordinatorNode) Transaction(context.Context, string, bool) (domain.Transaction, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tx, n.found, nil
}
func (*fakeCoordinatorNode) Broadcast(context.Context, string) (string, error) {
	return "", errors.New("not implemented")
}

func (n *fakeCoordinatorNode) setTransaction(tx domain.Transaction, found bool) {
	n.mu.Lock()
	n.tx, n.found = tx, found
	n.mu.Unlock()
}

func (n *fakeCoordinatorNode) setTip(tip domain.NodeTip) {
	n.mu.Lock()
	n.tip = tip
	n.mu.Unlock()
}

type fakeCoordinatorScanner struct {
	mu       sync.Mutex
	health   domain.ScannerHealth
	statuses domain.WalletNoteStatuses
}

func (s *fakeCoordinatorScanner) Health(context.Context) (domain.ScannerHealth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health, nil
}
func (*fakeCoordinatorScanner) UpsertWallet(context.Context, string, string, int64) error { return nil }
func (*fakeCoordinatorScanner) BackfillStatus(context.Context, string) (domain.BackfillStatus, bool, error) {
	return domain.BackfillStatus{}, false, nil
}
func (*fakeCoordinatorScanner) Backfill(context.Context, string, int64, int64) (int64, error) {
	return 0, nil
}
func (*fakeCoordinatorScanner) Balance(context.Context, string, string, int64, int64) (domain.Balance, bool, error) {
	return domain.Balance{}, false, nil
}
func (*fakeCoordinatorScanner) NoteSummary(context.Context, string, int64, int64, int) (domain.WalletNoteSummary, bool, error) {
	return domain.WalletNoteSummary{}, false, nil
}
func (s *fakeCoordinatorScanner) NoteStatuses(context.Context, string, []string) (domain.WalletNoteStatuses, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statuses, len(s.statuses.Statuses) > 0, nil
}
func (*fakeCoordinatorScanner) Events(context.Context, string, int64, int, domain.EventFilter) (domain.EventsPage, error) {
	return domain.EventsPage{}, nil
}

func (s *fakeCoordinatorScanner) setProof(health domain.ScannerHealth, statuses domain.WalletNoteStatuses) {
	s.mu.Lock()
	s.health, s.statuses = health, statuses
	s.mu.Unlock()
}
