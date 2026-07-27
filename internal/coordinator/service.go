package coordinator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

const (
	workerCount       = 4
	recoveryBatchSize = 1000
	recoveryInterval  = 5 * time.Second
)

type AddressAllocator func(context.Context, string, string) (storage.Address, error)

type Service struct {
	cfg      config.Config
	store    storage.Store
	node     domain.Node
	scanner  domain.Scanner
	allocate AddressAllocator
	planner  Planner
	signer   Signer
	logger   *slog.Logger
	wallets  map[string]config.Wallet

	queue       chan string
	startOnce   sync.Once
	inflightMu  sync.Mutex
	inflight    map[string]struct{}
	walletMu    sync.Mutex
	walletLocks map[string]*sync.Mutex
}

func NewService(cfg config.Config, store storage.Store, node domain.Node, scanner domain.Scanner, allocate AddressAllocator, planner Planner, signer Signer, logger *slog.Logger) (*Service, error) {
	if store == nil || node == nil || scanner == nil || allocate == nil || planner == nil || signer == nil {
		return nil, errors.New("coordinator dependencies are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	wallets := make(map[string]config.Wallet, len(cfg.Wallets))
	for _, wallet := range cfg.Wallets {
		wallets[wallet.WalletID] = wallet
	}
	return &Service{
		cfg: cfg, store: store, node: node, scanner: scanner, allocate: allocate, planner: planner, signer: signer, logger: logger,
		wallets: wallets, queue: make(chan string, recoveryBatchSize), inflight: make(map[string]struct{}), walletLocks: make(map[string]*sync.Mutex),
	}, nil
}

func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		for range workerCount {
			go s.worker(ctx)
		}
		go s.recoverLoop(ctx)
	})
}

func (s *Service) Create(ctx context.Context, principal, idempotencyKey string, request CreateRequest) (Attempt, bool, error) {
	if !idempotencyRE.MatchString(idempotencyKey) {
		return Attempt{}, false, opError("invalid_request", "a valid Idempotency-Key header is required", false)
	}
	request.WalletID = strings.TrimSpace(request.WalletID)
	wallet, ok := s.wallets[request.WalletID]
	if !ok {
		return Attempt{}, false, opError("not_found", "wallet not found", false)
	}
	normalized, canonical, digest, err := normalizeCreateRequest(request, s.cfg, wallet)
	if err != nil {
		return Attempt{}, false, opError("invalid_request", err.Error(), false)
	}
	attemptID, err := newAttemptID()
	if err != nil {
		return Attempt{}, false, opError("internal", "transaction attempt ID could not be created", false)
	}
	now := time.Now().UTC()
	claim, err := s.store.ClaimAttempt(ctx, storage.TransactionAttempt{
		AttemptID:            attemptID,
		ScopedIdempotencyKey: scopedAttemptKey(principal, idempotencyKey),
		RequestDigest:        digest,
		PrincipalName:        principal,
		WalletID:             normalized.WalletID,
		ApprovalReference:    normalized.ApprovalReference,
		RequestJSON:          canonical,
		CreatedAt:            now,
	})
	if err != nil {
		return Attempt{}, false, opError("internal", "transaction attempt state could not be claimed", false)
	}
	switch claim.State {
	case storage.ClaimConflict:
		return Attempt{}, false, opError("idempotency_conflict", "idempotency key was used with a different payload", false)
	case storage.ClaimAcquired:
		s.enqueue(claim.Attempt.AttemptID)
		return attemptView(claim.Attempt), false, nil
	case storage.ClaimReplay:
		if recoverableState(claim.Attempt.State) {
			s.enqueue(claim.Attempt.AttemptID)
		}
		return attemptView(claim.Attempt), true, nil
	default:
		return Attempt{}, false, opError("internal", "unexpected idempotency state", false)
	}
}

func (s *Service) Attempt(ctx context.Context, principal, attemptID string) (Attempt, error) {
	if !attemptIDRE.MatchString(attemptID) {
		return Attempt{}, opError("invalid_request", "attempt_id is invalid", false)
	}
	value, found, err := s.store.Attempt(ctx, attemptID)
	if err != nil {
		return Attempt{}, opError("internal", "transaction attempt state could not be read", false)
	}
	if !found {
		return Attempt{}, opError("not_found", "transaction attempt not found", false)
	}
	if value.PrincipalName != principal {
		return Attempt{}, opError("forbidden", "credential does not own this transaction attempt", false)
	}
	return attemptView(value), nil
}

func (s *Service) Cancel(ctx context.Context, principal, attemptID string) (Attempt, error) {
	if !attemptIDRE.MatchString(attemptID) {
		return Attempt{}, opError("invalid_request", "attempt_id is invalid", false)
	}
	current, found, err := s.store.Attempt(ctx, attemptID)
	if err != nil {
		return Attempt{}, opError("internal", "transaction attempt state could not be read", false)
	}
	if !found {
		return Attempt{}, opError("not_found", "transaction attempt not found", false)
	}
	if current.PrincipalName != principal {
		return Attempt{}, opError("forbidden", "credential does not own this transaction attempt", false)
	}
	cancelled, err := s.store.CancelAttempt(ctx, attemptID, time.Now().UTC())
	if errors.Is(err, storage.ErrAttemptStateConflict) {
		return Attempt{}, opError("attempt_not_cancellable", "only a provably unsigned planning or reserved attempt can be cancelled", false)
	}
	if err != nil {
		return Attempt{}, opError("internal", "transaction attempt could not be cancelled", false)
	}
	return attemptView(cancelled), nil
}

func (s *Service) Ready(ctx context.Context) error {
	if err := s.store.Ping(ctx); err != nil {
		return errors.New("coordinator state is unavailable")
	}
	info, err := os.Stat(s.cfg.CoordinatorTxbuildPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("transaction planner is unavailable")
	}
	if err := s.signer.Health(ctx); err != nil {
		return errors.New("transaction signer is unavailable")
	}
	return nil
}

func (s *Service) recoverLoop(ctx context.Context) {
	s.scheduleRecoverable(ctx)
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scheduleRecoverable(ctx)
		}
	}
}

func (s *Service) scheduleRecoverable(ctx context.Context) {
	values, err := s.store.RecoverableAttempts(ctx, recoveryBatchSize)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("coordinator_recovery_scan_failed", "error", err.Error())
		}
		return
	}
	for _, value := range values {
		s.enqueue(value.AttemptID)
	}
}

func (s *Service) enqueue(attemptID string) {
	select {
	case s.queue <- attemptID:
	default:
		s.logger.Warn("coordinator_queue_full", "attempt_id", attemptID)
	}
}

func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case attemptID := <-s.queue:
			if !s.beginAttempt(attemptID) {
				continue
			}
			s.process(ctx, attemptID)
			s.endAttempt(attemptID)
		}
	}
}

func (s *Service) beginAttempt(attemptID string) bool {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if _, exists := s.inflight[attemptID]; exists {
		return false
	}
	s.inflight[attemptID] = struct{}{}
	return true
}

func (s *Service) endAttempt(attemptID string) {
	s.inflightMu.Lock()
	delete(s.inflight, attemptID)
	s.inflightMu.Unlock()
}

func (s *Service) process(ctx context.Context, attemptID string) {
	for transitions := 0; transitions < 3 && ctx.Err() == nil; transitions++ {
		attempt, found, err := s.store.Attempt(ctx, attemptID)
		if err != nil || !found {
			if err != nil {
				s.logger.Error("coordinator_attempt_read_failed", "attempt_id", attemptID, "error", err.Error())
			}
			return
		}
		switch attempt.State {
		case "planning":
			if !s.planAttempt(ctx, attempt) {
				return
			}
		case "reserved", "signing", "signing_unknown":
			if !s.signAttempt(ctx, attempt) {
				return
			}
		case "signed", "broadcast", "mined", "expired_pending_reconciliation", "orphaned":
			s.reconcileAttempt(ctx, attempt)
			return
		default:
			return
		}
	}
}

func (s *Service) planAttempt(ctx context.Context, initial storage.TransactionAttempt) bool {
	lock := s.walletLock(initial.WalletID)
	lock.Lock()
	defer lock.Unlock()

	attempt, found, err := s.store.Attempt(ctx, initial.AttemptID)
	if err != nil || !found || attempt.State != "planning" {
		return false
	}
	request, wallet, err := s.decodeRequest(attempt)
	if err != nil {
		s.failUnsigned(ctx, attempt.AttemptID, "stored_request_invalid", err.Error())
		return false
	}
	changeAddress := attempt.ChangeAddress
	if changeAddress == "" {
		allocationCtx, cancel := context.WithTimeout(ctx, s.cfg.CoordinatorPlanTimeout)
		address, allocateErr := s.allocate(allocationCtx, attempt.WalletID, "internal_change_"+attempt.AttemptID)
		cancel()
		if allocateErr != nil {
			s.recordPlanningError(ctx, attempt.AttemptID, &operationError{Code: "change_address_unavailable", Message: "registered change address could not be allocated", Retryable: true})
			return false
		}
		if err := s.store.SetAttemptChangeAddress(ctx, attempt.AttemptID, address.Address, time.Now().UTC()); err != nil {
			return false
		}
		changeAddress = address.Address
	}

	for replan := 0; replan < s.cfg.CoordinatorMaxReplans; replan++ {
		excluded, err := s.store.ActiveNoteIDs(ctx, string(s.cfg.Network), attempt.WalletID)
		if err != nil {
			s.recordPlanningError(ctx, attempt.AttemptID, &operationError{Code: "reservation_state_unavailable", Message: "active note reservations could not be read", Retryable: true})
			return false
		}
		planCtx, cancel := context.WithTimeout(ctx, s.cfg.CoordinatorPlanTimeout)
		result, planErr := s.planner.Plan(planCtx, request, wallet, changeAddress, excluded)
		cancel()
		if planErr != nil {
			var operation *operationError
			if !errors.As(planErr, &operation) {
				operation = &operationError{Code: "planner_unavailable", Message: "transaction planner failed", Retryable: true}
			}
			if operation.Retryable {
				s.recordPlanningError(ctx, attempt.AttemptID, operation)
			} else {
				s.failUnsigned(ctx, attempt.AttemptID, operation.Code, operation.Message)
			}
			return false
		}
		err = s.store.ReserveAttemptPlan(ctx, attempt.AttemptID, string(s.cfg.Network), result.Bytes, result.Digest, result.FeeZat, result.ExpiryHeight, result.SelectedNoteIDs, time.Now().UTC())
		if err == nil {
			return true
		}
		if !errors.Is(err, storage.ErrNoteReservation) {
			return false
		}
	}
	s.recordPlanningError(ctx, attempt.AttemptID, &operationError{Code: "reservation_conflict", Message: "selected notes raced with another transaction; planning will retry", Retryable: true})
	return false
}

func (s *Service) signAttempt(ctx context.Context, initial storage.TransactionAttempt) bool {
	request, wallet, err := s.decodeRequest(initial)
	if err != nil {
		s.markSigningUnknown(ctx, initial.AttemptID, "stored_request_invalid", err.Error(), false)
		return false
	}
	validated, err := validatePlan(initial.PlanJSON, request, wallet, s.cfg.Network, initial.ChangeAddress)
	if err != nil || validated.Digest != initial.PlanDigest || validated.FeeZat != initial.FeeZat || validated.ExpiryHeight != initial.ExpiryHeight || !sameStringSet(validated.SelectedNoteIDs, initial.SelectedNoteIDs) {
		if initial.State == "reserved" {
			s.failUnsigned(ctx, initial.AttemptID, "stored_plan_invalid", "reserved TxPlan failed integrity validation")
		} else {
			s.markSigningUnknown(ctx, initial.AttemptID, "stored_plan_invalid", "TxPlan integrity failed after signing may have begun", false)
		}
		return false
	}
	signing, err := s.store.BeginAttemptSigning(ctx, initial.AttemptID, time.Now().UTC())
	if err != nil {
		return false
	}
	validated.Bytes = signing.PlanJSON
	signCtx, cancel := context.WithTimeout(ctx, s.cfg.CoordinatorSignTimeout)
	result, signErr := s.signer.Sign(signCtx, signing.AttemptID, validated)
	cancel()
	if signErr != nil {
		var operation *operationError
		if !errors.As(signErr, &operation) {
			operation = &operationError{Code: "signer_unavailable", Message: "signer result is unknown; notes remain reserved", Retryable: true, OutcomeUnknown: true}
		}
		if operation.OutcomeUnknown {
			s.markSigningUnknown(ctx, signing.AttemptID, operation.Code, operation.Message, operation.Retryable)
			return false
		}
		if operation.Retryable {
			_ = s.store.MarkAttemptState(ctx, signing.AttemptID, "reserved", operation.Code, operation.Message, true, false, time.Now().UTC())
			return false
		}
		_ = s.store.MarkAttemptState(ctx, signing.AttemptID, "failed_unsigned", operation.Code, operation.Message, false, true, time.Now().UTC())
		return false
	}
	if err := validateSignerResult(result, request); err != nil {
		s.markSigningUnknown(ctx, signing.AttemptID, "signer_invalid_response", err.Error(), true)
		return false
	}
	if err := s.store.CompleteAttemptSigning(ctx, signing.AttemptID, result.TxID, result.RawTxHex, result.FeeZat, result.OrchardOutputActionIndices, result.OrchardChangeActionIndex, time.Now().UTC()); err != nil {
		s.logger.Error("coordinator_signed_result_store_failed", "attempt_id", signing.AttemptID, "error", err.Error())
		return false
	}
	return true
}

func (s *Service) reconcileAttempt(ctx context.Context, attempt storage.TransactionAttempt) {
	lookupCtx, cancel := context.WithTimeout(ctx, s.cfg.UpstreamTimeout)
	tx, found, err := s.node.Transaction(lookupCtx, attempt.TxID, false)
	cancel()
	if err != nil {
		return
	}
	if found {
		switch tx.State {
		case "mempool":
			s.transition(ctx, attempt, "broadcast", false)
			return
		case "confirmed":
			if tx.Confirmations >= s.cfg.DefaultConfirmations {
				s.transition(ctx, attempt, "final", true)
			} else {
				s.transition(ctx, attempt, "mined", false)
			}
			return
		case "orphaned":
			s.transition(ctx, attempt, "orphaned", false)
			attempt.State = "orphaned"
		case "expired":
			s.transition(ctx, attempt, "expired_pending_reconciliation", false)
			attempt.State = "expired_pending_reconciliation"
		default:
			return
		}
	}
	if attempt.State == "signed" || attempt.State == "broadcast" || attempt.State == "mined" {
		// Absence alone never releases notes. It only becomes actionable after
		// the exact expiry window and finality depth have passed.
	}
	s.reconcileExpired(ctx, attempt)
}

func (s *Service) reconcileExpired(ctx context.Context, attempt storage.TransactionAttempt) {
	if attempt.ExpiryHeight <= 0 || attempt.ExpiryHeight > int64(^uint32(0)) {
		return
	}
	proofCtx, cancel := context.WithTimeout(ctx, s.cfg.UpstreamTimeout)
	defer cancel()
	tip, err := s.node.Tip(proofCtx)
	if err != nil || tip.Network != s.cfg.Network.NodeChain() || tip.InitialBlockDownload || tip.Height < attempt.ExpiryHeight+s.cfg.DefaultConfirmations {
		return
	}
	health, err := s.scanner.Health(proofCtx)
	if err != nil || health.Status != "ok" || health.Network != string(s.cfg.Network) || health.UAHRP != s.cfg.Network.AddressHRP() || health.Ready == nil || !*health.Ready ||
		health.HistoryComplete == nil || !*health.HistoryComplete || health.PendingSpendsReady == nil || !*health.PendingSpendsReady || health.ScannedHeight == nil || *health.ScannedHeight != tip.Height || health.ScannedHash != tip.Hash {
		return
	}
	statuses, found, err := s.scanner.NoteStatuses(proofCtx, attempt.WalletID, attempt.SelectedNoteIDs)
	if err != nil || !found || !statuses.ValidFor(attempt.WalletID, attempt.SelectedNoteIDs) || statuses.AsOfScannerHeight != tip.Height || statuses.AsOfScannerHash != tip.Hash {
		return
	}
	for _, status := range statuses.Statuses {
		if status.State != "unspent" {
			return
		}
	}
	s.transition(ctx, attempt, "released", true)
}

func (s *Service) transition(ctx context.Context, attempt storage.TransactionAttempt, state string, release bool) {
	if attempt.State == state {
		return
	}
	if err := s.store.MarkAttemptState(ctx, attempt.AttemptID, state, "", "", false, release, time.Now().UTC()); err != nil && !errors.Is(err, storage.ErrAttemptStateConflict) {
		s.logger.Error("coordinator_attempt_transition_failed", "attempt_id", attempt.AttemptID, "state", state, "error", err.Error())
	}
}

func (s *Service) decodeRequest(attempt storage.TransactionAttempt) (CreateRequest, config.Wallet, error) {
	wallet, ok := s.wallets[attempt.WalletID]
	if !ok {
		return CreateRequest{}, config.Wallet{}, errors.New("stored wallet is not configured")
	}
	var request CreateRequest
	if json.Unmarshal(attempt.RequestJSON, &request) != nil {
		return CreateRequest{}, config.Wallet{}, errors.New("stored request JSON is invalid")
	}
	normalized, canonical, digest, err := normalizeCreateRequest(request, s.cfg, wallet)
	if err != nil || digest != attempt.RequestDigest || string(canonical) != string(attempt.RequestJSON) {
		return CreateRequest{}, config.Wallet{}, errors.New("stored request integrity validation failed")
	}
	return normalized, wallet, nil
}

func (s *Service) failUnsigned(ctx context.Context, attemptID, code, message string) {
	_ = s.store.MarkAttemptState(ctx, attemptID, "failed_unsigned", code, message, false, true, time.Now().UTC())
}

func (s *Service) recordPlanningError(ctx context.Context, attemptID string, operation *operationError) {
	_ = s.store.MarkAttemptState(ctx, attemptID, "planning", operation.Code, operation.Message, operation.Retryable, false, time.Now().UTC())
}

func (s *Service) markSigningUnknown(ctx context.Context, attemptID, code, message string, retryable bool) {
	_ = s.store.MarkAttemptState(ctx, attemptID, "signing_unknown", code, message, retryable, false, time.Now().UTC())
}

func (s *Service) walletLock(walletID string) *sync.Mutex {
	s.walletMu.Lock()
	defer s.walletMu.Unlock()
	lock := s.walletLocks[walletID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.walletLocks[walletID] = lock
	}
	return lock
}

func newAttemptID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "txn_" + hex.EncodeToString(random), nil
}

func scopedAttemptKey(principal, key string) string {
	sum := sha256.Sum256([]byte("v1\x00" + principal + "\x00" + key))
	return "v1:" + hex.EncodeToString(sum[:])
}

func recoverableState(state string) bool {
	switch state {
	case "planning", "reserved", "signing", "signing_unknown", "signed", "broadcast", "mined", "expired_pending_reconciliation", "orphaned":
		return true
	default:
		return false
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a := append([]string(nil), left...)
	b := append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateSignerResult(result signerResult, request CreateRequest) error {
	if len(result.OrchardOutputActionIndices) != len(request.Outputs) {
		return fmt.Errorf("signer output mapping has %d entries for %d requested outputs", len(result.OrchardOutputActionIndices), len(request.Outputs))
	}
	seen := make(map[uint32]struct{}, len(result.OrchardOutputActionIndices)+1)
	for _, index := range result.OrchardOutputActionIndices {
		if _, duplicate := seen[index]; duplicate {
			return errors.New("signer output mapping contains duplicate action indices")
		}
		seen[index] = struct{}{}
	}
	if result.OrchardChangeActionIndex != nil {
		if _, duplicate := seen[*result.OrchardChangeActionIndex]; duplicate {
			return errors.New("signer change action overlaps a requested output")
		}
	}
	return nil
}
