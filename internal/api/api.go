package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

var (
	txIDPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	walletIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	eventEpochPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	requestIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type BuildInfo struct {
	Version    string            `json:"version"`
	Revision   string            `json:"revision"`
	BuildTime  string            `json:"build_time,omitempty"`
	APIVersion string            `json:"api_version"`
	Components map[string]string `json:"components,omitempty"`
}

type API struct {
	cfg            config.Config
	store          storage.Store
	node           domain.Node
	scanner        domain.Scanner
	registry       *walletRegistry
	codec          cursorCodec
	auth           authenticator
	readLimit      *limiter
	broadcastLimit *limiter
	logger         *slog.Logger
	build          BuildInfo
}

func New(cfg config.Config, store storage.Store, node domain.Node, scanner domain.Scanner, deriver domain.Deriver, logger *slog.Logger, build BuildInfo) (*API, error) {
	if store == nil || node == nil || scanner == nil || deriver == nil {
		return nil, errors.New("api dependencies are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	registry, err := newWalletRegistry(cfg, store, scanner, deriver)
	if err != nil {
		return nil, err
	}
	key, err := store.CursorKey(context.Background())
	if err != nil {
		return nil, err
	}
	if build.Version == "" {
		build.Version = "dev"
	}
	if build.Revision == "" {
		build.Revision = "unknown"
	}
	if build.APIVersion == "" {
		build.APIVersion = "v1"
	}
	return &API{
		cfg: cfg, store: store, node: node, scanner: scanner, registry: registry, codec: cursorCodec{key: key},
		auth:      authenticator{credentials: cfg.Credentials, allowAnonymous: cfg.Network == domain.Regtest},
		readLimit: newLimiter(cfg.ReadRate.RPS, cfg.ReadRate.Burst), broadcastLimit: newLimiter(cfg.BroadcastRate.RPS, cfg.BroadcastRate.Burst),
		logger: logger, build: build,
	}, nil
}

func (a *API) ReconcileWallets(ctx context.Context) {
	nextSync := time.Time{}
	for {
		now := time.Now()
		if !now.Before(nextSync) {
			syncCtx, cancel := context.WithTimeout(ctx, a.cfg.UpstreamTimeout)
			if err := a.registry.sync(syncCtx); err != nil {
				a.logger.Warn("wallet_reconcile_failed", "error", err.Error())
			}
			cancel()
			nextSync = now.Add(30 * time.Second)
		}
		healthCtx, cancel := context.WithTimeout(ctx, a.cfg.UpstreamTimeout)
		health, err := a.scanner.Health(healthCtx)
		cancel()
		worked := false
		if err == nil && health.ScannedHeight != nil && health.Network == string(a.cfg.Network) && health.UAHRP == a.cfg.Network.AddressHRP() {
			backfillCtx, backfillCancel := context.WithTimeout(ctx, a.cfg.BackfillTimeout)
			worked, err = a.registry.backfillOne(backfillCtx, *health.ScannedHeight, a.cfg.BackfillBatchSize)
			backfillCancel()
		}
		if err != nil {
			a.logger.Warn("wallet_backfill_failed", "error", err.Error())
		}
		delay := 2 * time.Second
		if worked {
			delay = a.cfg.BackfillYield
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// AllocateInternalAddress gives the private coordinator the same guarded,
// crash-safe allocator used by the public address API without exposing another
// public route. The label must identify an internal exchange purpose.
func (a *API) AllocateInternalAddress(ctx context.Context, walletID, label string) (storage.Address, error) {
	if !strings.HasPrefix(label, "internal_") {
		return storage.Address{}, errors.New("internal address label must start with internal_")
	}
	if len(label) > 128 {
		return storage.Address{}, errors.New("internal address label is too long")
	}
	if _, code, err := a.checkReady(ctx); err != nil {
		return storage.Address{}, fmt.Errorf("%s: %w", code, err)
	}
	return a.registry.allocate(ctx, walletID, label)
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/live", a.handleLive)
	mux.HandleFunc("GET /v1/version", a.protected("read", false, false, a.handleVersion))
	mux.HandleFunc("GET /v1/health/ready", a.protected("read", false, false, a.handleReady))
	mux.HandleFunc("GET /v1/network/tip", a.protected("read", false, false, a.handleTip))
	mux.HandleFunc("POST /v1/wallets/{wallet_id}/addresses", a.protected("address", true, false, a.handleAllocateAddress))
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/addresses/{address}/balance", a.protected("read", true, false, a.handleBalance))
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/notes/summary", a.protected("treasury", true, false, a.handleNoteSummary))
	mux.HandleFunc("POST /v1/wallets/{wallet_id}/notes/status", a.protected("treasury", true, false, a.handleNoteStatuses))
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/deposits", a.protected("read", true, false, a.handleDeposits))
	mux.HandleFunc("GET /v1/transactions/{txid}", a.protected("read", false, false, a.handleTransaction))
	mux.HandleFunc("POST /v1/transactions/broadcast", a.protected("broadcast", false, true, a.handleBroadcast))
	mux.HandleFunc("/", a.protected("read", false, false, func(w http.ResponseWriter, r *http.Request) {
		a.writeError(w, r, http.StatusNotFound, "not_found", "route not found", false, nil)
	}))
	methodNotAllowed := a.authenticated(func(w http.ResponseWriter, r *http.Request) {
		allowed, _ := knownRouteMethod(r.URL.Path)
		w.Header().Set("Allow", allowed)
		a.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", false, nil)
	})
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed, known := knownRouteMethod(r.URL.Path); known && r.Method != allowed && !(allowed == http.MethodGet && r.Method == http.MethodHead) {
			methodNotAllowed(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return a.requestMiddleware(a.recoverMiddleware(router))
}

func knownRouteMethod(path string) (string, bool) {
	switch path {
	case "/v1/health/live", "/v1/version", "/v1/health/ready", "/v1/network/tip":
		return http.MethodGet, true
	case "/v1/transactions/broadcast":
		return http.MethodPost, true
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "transactions" && segments[2] != "" {
		return http.MethodGet, true
	}
	if len(segments) >= 4 && segments[0] == "v1" && segments[1] == "wallets" && segments[2] != "" {
		switch {
		case len(segments) == 4 && segments[3] == "addresses":
			return http.MethodPost, true
		case len(segments) == 4 && segments[3] == "deposits":
			return http.MethodGet, true
		case len(segments) == 5 && segments[3] == "notes" && segments[4] == "summary":
			return http.MethodGet, true
		case len(segments) == 5 && segments[3] == "notes" && segments[4] == "status":
			return http.MethodPost, true
		case len(segments) == 6 && segments[3] == "addresses" && segments[4] != "" && segments[5] == "balance":
			return http.MethodGet, true
		}
	}
	return "", false
}

func (a *API) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.auth.authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			a.writeError(w, r, http.StatusUnauthorized, "unauthorized", "a valid bearer credential is required", false, nil)
			return
		}
		*r = *r.WithContext(withPrincipal(r.Context(), p))
		if metadata, ok := r.Context().Value(requestMetadataKey{}).(*requestMetadata); ok {
			metadata.principal = p.name
		}
		next(w, r)
	}
}

func (a *API) protected(scope string, walletScoped, broadcast bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.auth.authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			a.writeError(w, r, http.StatusUnauthorized, "unauthorized", "a valid bearer credential is required", false, nil)
			return
		}
		if !p.hasScope(scope) {
			a.writeError(w, r, http.StatusForbidden, "forbidden", "credential lacks the required scope", false, nil)
			return
		}
		walletID := r.PathValue("wallet_id")
		if walletScoped && !p.hasWallet(walletID) {
			a.writeError(w, r, http.StatusForbidden, "forbidden", "credential is not authorized for this wallet", false, nil)
			return
		}
		key := p.name + ":" + a.remoteIP(r)
		selected := a.readLimit
		if broadcast {
			selected = a.broadcastLimit
		}
		if !selected.allow(key, time.Now()) {
			w.Header().Set("Retry-After", "1")
			a.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded", true, nil)
			return
		}
		timeout := a.cfg.ReadTimeout
		if broadcast {
			timeout = a.cfg.BroadcastTimeout
		}
		ctx, cancel := context.WithTimeout(withPrincipal(r.Context(), p), timeout)
		defer cancel()
		if metadata, ok := r.Context().Value(requestMetadataKey{}).(*requestMetadata); ok {
			metadata.principal = p.name
		}
		next(w, r.WithContext(ctx))
	}
}

type requestIDKey struct{}
type requestMetadataKey struct{}
type requestMetadata struct{ principal string }

func requestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }

func (a *API) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		metadata := &requestMetadata{}
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = context.WithValue(ctx, requestMetadataKey{}, metadata)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(recorder, r)
		pattern := r.Pattern
		if pattern == "" {
			pattern = "unmatched"
		}
		a.logger.Info("http_request", "request_id", id, "method", r.Method, "route", pattern, "status", recorder.status, "bytes", recorder.bytes, "duration_ms", time.Since(start).Milliseconds(), "principal", metadata.principal, "remote_ip", a.remoteIP(r))
	})
}

func (a *API) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("http_panic", "request_id", requestID(r.Context()), "panic", true, "stack", string(debug.Stack()))
				a.writeError(w, r, http.StatusInternalServerError, "internal", "an internal error occurred", false, nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "req_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "req_" + hex.EncodeToString(b)
}

func (a *API) remoteIP(r *http.Request) string {
	if a.cfg.TrustProxyHeaders {
		if raw := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); raw != "" {
			return raw
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *API) handleLive(w http.ResponseWriter, r *http.Request) {
	a.writeData(w, r, http.StatusOK, map[string]string{"state": "live"})
}
func (a *API) handleVersion(w http.ResponseWriter, r *http.Request) {
	a.writeData(w, r, http.StatusOK, map[string]any{"build": a.build, "network": a.cfg.Network})
}

type readiness struct {
	Network       domain.Network       `json:"network"`
	Node          domain.NodeTip       `json:"node"`
	Scanner       domain.ScannerHealth `json:"scanner"`
	ScannerLag    int64                `json:"scanner_lag"`
	MaxScannerLag int64                `json:"max_scanner_lag"`
}

func (a *API) checkReady(ctx context.Context) (readiness, string, error) {
	if err := a.store.Ping(ctx); err != nil {
		return readiness{}, "gateway_state_not_ready", err
	}
	tip, err := a.readyNode(ctx)
	if err != nil {
		return readiness{}, "node_not_ready", err
	}
	health, err := a.scanner.Health(ctx)
	if err != nil {
		return readiness{}, "scanner_not_ready", err
	}
	if !strings.EqualFold(health.Status, "ok") || health.ScannedHeight == nil {
		return readiness{}, "scanner_not_ready", errors.New("scanner has no indexed tip")
	}
	if health.Network == "" || health.Network != string(a.cfg.Network) || health.UAHRP == "" || health.UAHRP != a.cfg.Network.AddressHRP() {
		return readiness{}, "scanner_not_ready", errors.New("scanner network does not match gateway configuration")
	}
	if health.Confirmations == nil || *health.Confirmations <= 0 || *health.Confirmations != a.cfg.DefaultConfirmations {
		return readiness{}, "scanner_not_ready", errors.New("scanner confirmation policy does not match gateway configuration")
	}
	if *health.ScannedHeight < 0 || *health.ScannedHeight > tip.Height || !txIDPattern.MatchString(health.ScannedHash) {
		return readiness{}, "scanner_not_ready", errors.New("scanner indexed tip is invalid")
	}
	if !eventEpochPattern.MatchString(health.EventEpoch) {
		return readiness{}, "scanner_not_ready", errors.New("scanner event epoch is invalid")
	}
	nodeScannedHash, err := a.node.BlockHash(ctx, *health.ScannedHeight)
	if err != nil {
		return readiness{}, "node_not_ready", err
	}
	if health.ScannedHash != nodeScannedHash {
		return readiness{}, "scanner_not_ready", errors.New("scanner indexed tip does not match node chain")
	}
	lag := tip.Height - *health.ScannedHeight
	if health.Ready == nil || !*health.Ready {
		return readiness{}, "scanner_not_ready", errors.New("scanner reports not ready")
	}
	if health.PendingSpendsReady == nil {
		return readiness{}, "scanner_not_ready", errors.New("scanner cannot attest pending spend reconciliation")
	}
	if !*health.PendingSpendsReady {
		return readiness{}, "scanner_not_ready", errors.New("scanner pending spend reconciliation is incomplete")
	}
	if a.cfg.RequireCompleteHistory {
		if health.HistoryComplete == nil {
			return readiness{}, "scanner_not_ready", errors.New("scanner cannot attest complete history")
		}
		if !*health.HistoryComplete {
			return readiness{}, "scanner_not_ready", errors.New("scanner history is incomplete")
		}
	}
	if lag > a.cfg.MaxScannerLag {
		return readiness{}, "scanner_not_ready", errors.New("scanner lag exceeds configured maximum")
	}
	if health.ScannerLag == nil || *health.ScannerLag < 0 || *health.ScannerLag != lag {
		return readiness{}, "scanner_not_ready", errors.New("scanner lag does not match node-derived lag")
	}
	complete, err := a.registry.completeThrough(ctx, *health.ScannedHeight)
	if err != nil {
		if domain.IsUpstreamKind(err, "unavailable") || domain.IsUpstreamKind(err, "invalid_response") {
			return readiness{}, "scanner_not_ready", err
		}
		return readiness{}, "gateway_state_not_ready", err
	}
	if !complete {
		return readiness{}, "scanner_not_ready", errors.New("registered wallet history is incomplete")
	}
	return readiness{Network: a.cfg.Network, Node: tip, Scanner: health, ScannerLag: lag, MaxScannerLag: a.cfg.MaxScannerLag}, "", nil
}

func (a *API) readyNode(ctx context.Context) (domain.NodeTip, error) {
	tip, err := a.node.Tip(ctx)
	if err != nil {
		return domain.NodeTip{}, err
	}
	if a.validateNodeTip(tip) != nil || tip.InitialBlockDownload {
		return domain.NodeTip{}, errors.New("node is not ready for configured network")
	}
	return tip, nil
}

func (a *API) validateNodeTip(tip domain.NodeTip) error {
	if tip.Network != a.cfg.Network.NodeChain() || tip.Height < 0 ||
		!txIDPattern.MatchString(tip.Hash) || tip.Headers < tip.Height ||
		math.IsNaN(tip.VerificationProgress) || math.IsInf(tip.VerificationProgress, 0) ||
		tip.VerificationProgress < 0 || tip.VerificationProgress > 1 {
		return errors.New("node returned an invalid tip for the configured network")
	}
	return nil
}

func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	ready, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, code, "financial reads are not ready", true, nil)
		return
	}
	a.writeData(w, r, http.StatusOK, ready)
}

func (a *API) handleTip(w http.ResponseWriter, r *http.Request) {
	tip, err := a.node.Tip(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "node request failed", true, nil)
		return
	}
	if err := a.validateNodeTip(tip); err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "node_not_ready", "node tip is invalid for the configured network", true, nil)
		return
	}
	health, err := a.scanner.Health(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner request failed", true, nil)
		return
	}
	if health.Network == "" || health.Network != string(a.cfg.Network) || health.UAHRP == "" || health.UAHRP != a.cfg.Network.AddressHRP() {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner network does not match gateway configuration", true, nil)
		return
	}
	lag := int64(0)
	if health.ScannedHeight != nil {
		lag = tip.Height - *health.ScannedHeight
		if lag < 0 {
			lag = 0
		}
	}
	a.writeData(w, r, http.StatusOK, map[string]any{"network": a.cfg.Network, "height": tip.Height, "hash": tip.Hash, "block_time": tip.BlockTime, "headers": tip.Headers, "initial_sync": tip.InitialBlockDownload, "verification_progress": tip.VerificationProgress, "scanner_height": health.ScannedHeight, "scanner_lag": lag})
}

func (a *API) handleAllocateAddress(w http.ResponseWriter, r *http.Request) {
	walletID := r.PathValue("wallet_id")
	if _, ok := a.registry.known(walletID); !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if err := decodeJSON(w, r, a.cfg.JSONBodyBytes, &req); err != nil {
		a.writeDecodeError(w, r, err)
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if len(req.Label) > 128 {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "label must be at most 128 characters", false, nil)
		return
	}
	if _, code, err := a.checkReady(r.Context()); err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, code, "address allocation is not ready", true, nil)
		return
	}
	address, err := a.registry.allocate(r.Context(), walletID, req.Label)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "address allocation failed", false, nil)
		return
	}
	a.writeData(w, r, http.StatusCreated, address)
}

func (a *API) handleBalance(w http.ResponseWriter, r *http.Request) {
	walletID, address := r.PathValue("wallet_id"), r.PathValue("address")
	if _, ok := a.registry.known(walletID); !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
		return
	}
	if !strings.HasPrefix(address, a.cfg.Network.AddressHRP()+"1") {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "address does not match configured network", false, nil)
		return
	}
	if _, ok, err := a.store.Address(r.Context(), walletID, address); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "state lookup failed", false, nil)
		return
	} else if !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "address is not registered to this wallet", false, nil)
		return
	}
	confirmations, err := a.confirmations(r)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error(), false, nil)
		return
	}
	ready, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, code, "financial reads are not ready", true, nil)
		return
	}
	balance, found, err := a.scanner.Balance(r.Context(), walletID, address, confirmations, ready.Node.Height)
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner balance request failed", true, nil)
		return
	}
	if !found {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found in scanner", false, nil)
		return
	}
	if !balance.ValidFor(walletID, address, confirmations) {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner returned an invalid address balance", true, nil)
		return
	}
	a.writeData(w, r, http.StatusOK, map[string]any{"wallet_id": walletID, "address": address, "balance": balance})
}

func (a *API) confirmations(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("min_confirmations"))
	if raw == "" {
		return a.cfg.DefaultConfirmations, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 || n > a.cfg.MaxConfirmations {
		return 0, fmt.Errorf("min_confirmations must be between 0 and %d", a.cfg.MaxConfirmations)
	}
	return n, nil
}

type noteSummary struct {
	WalletID           string                         `json:"wallet_id"`
	MinConfirmations   int64                          `json:"min_confirmations"`
	MinNoteZat         int64                          `json:"min_note_zat"`
	AsOfNodeHeight     int64                          `json:"as_of_node_height"`
	AsOfScannerHeight  int64                          `json:"as_of_scanner_height"`
	AsOfScannerHash    string                         `json:"as_of_scanner_hash"`
	ScannerLag         int64                          `json:"scanner_lag"`
	TotalUnspent       domain.NoteValueSummary        `json:"total_unspent"`
	Spendable          domain.SpendableNoteSummary    `json:"spendable"`
	Immature           domain.NoteValueSummary        `json:"immature"`
	PendingSpend       domain.PendingSpendNoteSummary `json:"pending_spend"`
	BelowMinNote       domain.NoteValueSummary        `json:"below_min_note"`
	WitnessUnavailable domain.NoteValueSummary        `json:"witness_unavailable"`
}

func (a *API) handleNoteSummary(w http.ResponseWriter, r *http.Request) {
	walletID := r.PathValue("wallet_id")
	if _, ok := a.registry.known(walletID); !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
		return
	}
	confirmations, err := a.confirmations(r)
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error(), false, nil)
		return
	}
	minNoteZat := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("min_note_zat")); raw != "" {
		minNoteZat, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || minNoteZat < 0 {
			a.writeError(w, r, http.StatusBadRequest, "invalid_request", "min_note_zat must be a non-negative integer", false, nil)
			return
		}
	}
	before, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, code, "financial reads are not ready", true, nil)
		return
	}
	if before.Scanner.ScannedHeight == nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner height is unavailable", true, nil)
		return
	}
	atomicSummary, found, err := a.scanner.NoteSummary(r.Context(), walletID, confirmations, minNoteZat, a.cfg.NoteSummaryMaxNotes)
	if err != nil {
		if errors.Is(err, domain.ErrNoteSummaryLimitExceeded) {
			a.writeError(w, r, http.StatusUnprocessableEntity, "note_summary_limit_exceeded", "wallet note inventory exceeds the configured summary cap", false, map[string]any{"max_notes": a.cfg.NoteSummaryMaxNotes})
			return
		}
		if domain.IsUpstreamKind(err, "invalid_response") {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an invalid note summary", true, nil)
			return
		}
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner note summary request failed", true, nil)
		return
	}
	if !found {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner wallet is missing", true, nil)
		return
	}
	if !atomicSummary.ValidFor(walletID, confirmations, minNoteZat, a.cfg.NoteSummaryMaxNotes) {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an invalid note summary", true, nil)
		return
	}
	after, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusConflict, "scanner_snapshot_changed", "readiness changed while summarizing notes; retry", true, map[string]any{"readiness_code": code})
		return
	}
	if after.Scanner.ScannedHeight == nil || atomicSummary.AsOfScannerHeight != *before.Scanner.ScannedHeight ||
		atomicSummary.AsOfScannerHash != before.Scanner.ScannedHash ||
		*after.Scanner.ScannedHeight != *before.Scanner.ScannedHeight ||
		after.Scanner.ScannedHash != before.Scanner.ScannedHash || after.Scanner.EventEpoch != before.Scanner.EventEpoch {
		a.writeError(w, r, http.StatusConflict, "scanner_snapshot_changed", "scanner snapshot changed while summarizing notes; retry", true, nil)
		return
	}
	summary := noteSummary{
		WalletID: walletID, MinConfirmations: confirmations, MinNoteZat: minNoteZat,
		AsOfNodeHeight: before.Node.Height, AsOfScannerHeight: atomicSummary.AsOfScannerHeight, AsOfScannerHash: atomicSummary.AsOfScannerHash, ScannerLag: before.ScannerLag,
		TotalUnspent: atomicSummary.TotalUnspent, Spendable: atomicSummary.Spendable, Immature: atomicSummary.Immature,
		PendingSpend: atomicSummary.PendingSpend, BelowMinNote: atomicSummary.BelowMinNote, WitnessUnavailable: atomicSummary.WitnessUnavailable,
	}
	a.writeData(w, r, http.StatusOK, summary)
}

type noteStatusesRequest struct {
	NoteIDs []string `json:"note_ids"`
}

type noteStatusesResponse struct {
	WalletID          string              `json:"wallet_id"`
	AsOfNodeHeight    int64               `json:"as_of_node_height"`
	AsOfScannerHeight int64               `json:"as_of_scanner_height"`
	AsOfScannerHash   string              `json:"as_of_scanner_hash"`
	ScannerLag        int64               `json:"scanner_lag"`
	EventEpoch        string              `json:"event_epoch"`
	Statuses          []domain.NoteStatus `json:"statuses"`
}

func (a *API) handleNoteStatuses(w http.ResponseWriter, r *http.Request) {
	walletID := r.PathValue("wallet_id")
	if _, ok := a.registry.known(walletID); !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
		return
	}
	var req noteStatusesRequest
	if err := decodeJSON(w, r, a.cfg.JSONBodyBytes, &req); err != nil {
		a.writeDecodeError(w, r, err)
		return
	}
	if len(req.NoteIDs) < 1 || len(req.NoteIDs) > 200 {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "note_ids must contain between 1 and 200 entries", false, nil)
		return
	}
	seen := make(map[string]struct{}, len(req.NoteIDs))
	for _, noteID := range req.NoteIDs {
		if !domain.ValidNoteID(noteID) {
			a.writeError(w, r, http.StatusBadRequest, "invalid_request", "note_ids must use canonical txid:action_index form", false, nil)
			return
		}
		if _, duplicate := seen[noteID]; duplicate {
			a.writeError(w, r, http.StatusBadRequest, "invalid_request", "note_ids must be unique", false, nil)
			return
		}
		seen[noteID] = struct{}{}
	}
	before, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, code, "financial reads are not ready", true, nil)
		return
	}
	if before.Scanner.ScannedHeight == nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner height is unavailable", true, nil)
		return
	}
	snapshot, found, err := a.scanner.NoteStatuses(r.Context(), walletID, req.NoteIDs)
	if err != nil {
		if errors.Is(err, domain.ErrScannerSnapshotChanged) {
			a.writeError(w, r, http.StatusConflict, "scanner_snapshot_changed", "scanner snapshot changed while reading note statuses; retry", true, nil)
			return
		}
		if domain.IsUpstreamKind(err, "invalid_response") {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned invalid note statuses", true, nil)
			return
		}
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner note status request failed", true, nil)
		return
	}
	if !found {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner wallet is missing", true, nil)
		return
	}
	if !snapshot.ValidFor(walletID, req.NoteIDs) {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned invalid note statuses", true, nil)
		return
	}
	after, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusConflict, "scanner_snapshot_changed", "readiness changed while reading note statuses; retry", true, map[string]any{"readiness_code": code})
		return
	}
	if after.Scanner.ScannedHeight == nil || snapshot.AsOfScannerHeight != *before.Scanner.ScannedHeight ||
		snapshot.AsOfScannerHash != before.Scanner.ScannedHash || snapshot.EventEpoch != before.Scanner.EventEpoch ||
		*after.Scanner.ScannedHeight != *before.Scanner.ScannedHeight || after.Scanner.ScannedHash != before.Scanner.ScannedHash ||
		after.Scanner.EventEpoch != before.Scanner.EventEpoch {
		a.writeError(w, r, http.StatusConflict, "scanner_snapshot_changed", "scanner snapshot changed while reading note statuses; retry", true, nil)
		return
	}
	a.writeData(w, r, http.StatusOK, noteStatusesResponse{
		WalletID: walletID, AsOfNodeHeight: before.Node.Height, AsOfScannerHeight: snapshot.AsOfScannerHeight,
		AsOfScannerHash: snapshot.AsOfScannerHash, ScannerLag: before.ScannerLag, EventEpoch: snapshot.EventEpoch, Statuses: snapshot.Statuses,
	})
}

type deposit struct {
	DepositID        string    `json:"deposit_id"`
	EventID          string    `json:"event_id"`
	WalletID         string    `json:"wallet_id"`
	TxID             string    `json:"txid"`
	ActionIndex      uint32    `json:"action_index"`
	Address          string    `json:"address"`
	DiversifierIndex uint32    `json:"diversifier_index"`
	AmountZat        uint64    `json:"amount_zat"`
	Status           string    `json:"status"`
	BlockHeight      int64     `json:"block_height"`
	ConfirmedHeight  *int64    `json:"confirmed_height,omitempty"`
	OrphanedAtHeight *int64    `json:"orphaned_at_height,omitempty"`
	RollbackHeight   *int64    `json:"rollback_height,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
}

var depositKinds = []string{"DepositEvent", "DepositConfirmed", "DepositUnconfirmed", "DepositOrphaned"}

func (a *API) handleDeposits(w http.ResponseWriter, r *http.Request) {
	walletID := r.PathValue("wallet_id")
	if _, ok := a.registry.known(walletID); !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 1000 {
			a.writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 1000", false, nil)
			return
		}
		limit = n
	}
	filter := domain.EventFilter{Kinds: append([]string(nil), depositKinds...)}
	if txid := strings.TrimSpace(r.URL.Query().Get("txid")); txid != "" {
		if !txIDPattern.MatchString(txid) {
			a.writeError(w, r, http.StatusBadRequest, "invalid_request", "txid must be 64 lowercase hex characters", false, nil)
			return
		}
		filter.TxID = txid
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "detected" && status != "confirmed" && status != "unconfirmed" && status != "orphaned" {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid deposit status", false, nil)
		return
	}
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if address != "" {
		if _, ok, err := a.store.Address(r.Context(), walletID, address); err != nil {
			a.writeError(w, r, http.StatusInternalServerError, "internal", "state lookup failed", false, nil)
			return
		} else if !ok {
			a.writeError(w, r, http.StatusNotFound, "not_found", "address is not registered to this wallet", false, nil)
			return
		}
	}
	cursorFilter := depositCursorFilter(status, address, filter.TxID)
	ready, code, err := a.checkReady(r.Context())
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, code, "financial reads are not ready", true, nil)
		return
	}
	position, err := a.codec.decode(strings.TrimSpace(r.URL.Query().Get("cursor")), walletID, ready.Scanner.EventEpoch, cursorFilter)
	if errors.Is(err, errCursorResetRequired) {
		a.writeCursorReset(w, r, ready.Scanner.EventEpoch)
		return
	}
	if errors.Is(err, errCursorFilterMismatch) {
		a.writeError(w, r, http.StatusConflict, "cursor_filter_mismatch", "cursor was created for different deposit filters", false, map[string]any{"action": "restart_without_cursor_or_restore_filters"})
		return
	}
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "cursor is invalid for this wallet", false, nil)
		return
	}
	page, err := a.scanner.Events(r.Context(), walletID, position, 1000, filter)
	if err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "scanner deposit request failed", true, nil)
		return
	}
	if page.EventEpoch != ready.Scanner.EventEpoch {
		a.writeCursorReset(w, r, ready.Scanner.EventEpoch)
		return
	}
	if page.NextCursor < position {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned a backward deposit cursor", true, nil)
		return
	}
	if (len(page.Events) == 0 && page.NextCursor != position) || (len(page.Events) > 0 && page.NextCursor != page.Events[len(page.Events)-1].ID) {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an inconsistent deposit cursor", true, nil)
		return
	}
	items := make([]deposit, 0, limit)
	next := position
	for _, event := range page.Events {
		if event.ID <= next {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned a non-forward deposit cursor", true, nil)
			return
		}
		effect, err := sanitizeWalletEffect(event, walletID, "", a.cfg.Network.AddressHRP(), a.cfg.DefaultConfirmations)
		if err != nil {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an invalid deposit event", true, nil)
			return
		}
		publicStatus, ok := depositStatus(event.Kind)
		if !ok {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an unexpected deposit event kind", true, nil)
			return
		}
		next = event.ID
		registered, err := a.bindRegisteredDepositIdentity(r.Context(), walletID, &effect)
		if err != nil {
			if domain.IsUpstreamKind(err, "invalid_response") {
				a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned a deposit with mismatched address identity", true, nil)
				return
			}
			a.writeError(w, r, http.StatusInternalServerError, "internal", "state lookup failed", false, nil)
			return
		}
		if !registered {
			continue
		}
		if status != "" && publicStatus != status {
			continue
		}
		if address != "" && effect.Address != address {
			continue
		}
		if effect.ActionIndex == nil || effect.AmountZat == nil || effect.BlockHeight == nil || effect.DiversifierIndex == nil {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an incomplete deposit event", true, nil)
			return
		}
		items = append(items, deposit{DepositID: walletID + ":" + effect.TxID + ":" + strconv.FormatUint(uint64(*effect.ActionIndex), 10), EventID: page.EventEpoch + ":" + strconv.FormatInt(event.ID, 10), WalletID: walletID, TxID: effect.TxID, ActionIndex: *effect.ActionIndex, Address: effect.Address, DiversifierIndex: *effect.DiversifierIndex, AmountZat: uint64(*effect.AmountZat), Status: publicStatus, BlockHeight: *effect.BlockHeight, ConfirmedHeight: effect.ConfirmedHeight, OrphanedAtHeight: effect.OrphanedAtHeight, RollbackHeight: effect.RollbackHeight, ObservedAt: event.CreatedAt})
		if len(items) == limit {
			break
		}
	}
	if len(items) < limit && len(page.Events) == 0 {
		next = page.NextCursor
	}
	if len(items) < limit && len(page.Events) > 0 && next == page.Events[len(page.Events)-1].ID {
		next = page.NextCursor
	}
	if page.NextCursor < next {
		a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned a backward deposit cursor", true, nil)
		return
	}
	a.writeData(w, r, http.StatusOK, map[string]any{"deposits": items, "next_cursor": a.codec.encode(walletID, page.EventEpoch, cursorFilter, next), "delivery": "at_least_once", "event_epoch": page.EventEpoch})
}

func (a *API) bindRegisteredDepositIdentity(ctx context.Context, walletID string, effect *walletEffect) (bool, error) {
	allocated, registered, err := a.store.Address(ctx, walletID, effect.Address)
	if err != nil || !registered {
		return registered, err
	}
	if effect.DiversifierIndex == nil {
		if allocated.DiversifierIndex != 0 {
			return true, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner omitted a nonzero deposit diversifier index")}
		}
		index := uint32(0)
		effect.DiversifierIndex = &index
	}
	if allocated.DiversifierIndex != *effect.DiversifierIndex {
		return true, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner deposit address identity does not match the allocation ledger")}
	}
	return true, nil
}

func depositCursorFilter(status, address, txid string) string {
	sum := sha256.Sum256([]byte(status + "\x00" + address + "\x00" + txid))
	return hex.EncodeToString(sum[:])
}

func (a *API) writeCursorReset(w http.ResponseWriter, r *http.Request, eventEpoch string) {
	a.writeError(w, r, http.StatusConflict, "cursor_reset_required", "scanner event history was reset; restart without a cursor and idempotently replay deposits", false, map[string]any{
		"action":      "restart_without_cursor",
		"event_epoch": eventEpoch,
	})
}

func depositStatus(kind string) (string, bool) {
	switch kind {
	case "DepositEvent":
		return "detected", true
	case "DepositConfirmed":
		return "confirmed", true
	case "DepositUnconfirmed":
		return "unconfirmed", true
	case "DepositOrphaned":
		return "orphaned", true
	default:
		return "", false
	}
}

func (a *API) handleTransaction(w http.ResponseWriter, r *http.Request) {
	txid := r.PathValue("txid")
	if !txIDPattern.MatchString(txid) {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "txid must be 64 lowercase hex characters", false, nil)
		return
	}
	includeRaw := false
	if raw := strings.TrimSpace(r.URL.Query().Get("include_raw")); raw != "" {
		var err error
		includeRaw, err = strconv.ParseBool(raw)
		if err != nil {
			a.writeError(w, r, http.StatusBadRequest, "invalid_request", "include_raw must be true or false", false, nil)
			return
		}
	}
	p := principalFrom(r.Context())
	if includeRaw && !p.hasScope("raw") {
		a.writeError(w, r, http.StatusForbidden, "forbidden", "credential lacks raw transaction scope", false, nil)
		return
	}
	walletID := strings.TrimSpace(r.URL.Query().Get("wallet_id"))
	var ready readiness
	if walletID != "" {
		if !p.hasScope("withdrawal") {
			a.writeError(w, r, http.StatusForbidden, "forbidden", "credential lacks wallet transaction scope", false, nil)
			return
		}
		if !p.hasWallet(walletID) {
			a.writeError(w, r, http.StatusForbidden, "forbidden", "credential is not authorized for this wallet", false, nil)
			return
		}
		if _, ok := a.registry.known(walletID); !ok {
			a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
			return
		}
		var code string
		var err error
		ready, code, err = a.checkReady(r.Context())
		if err != nil {
			a.writeError(w, r, http.StatusServiceUnavailable, code, "wallet transaction enrichment is not ready", true, nil)
			return
		}
	}
	if _, err := a.readyNode(r.Context()); err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "node_not_ready", "node is not ready", true, nil)
		return
	}
	var effects []walletEffect
	if walletID != "" {
		var err error
		effects, err = a.walletEffects(r.Context(), walletID, txid, ready.Scanner.EventEpoch)
		if err != nil {
			if errors.Is(err, errCursorResetRequired) {
				a.writeError(w, r, http.StatusConflict, "event_history_reset", "scanner event history changed during transaction lookup; retry the request", true, map[string]any{"event_epoch": ready.Scanner.EventEpoch})
				return
			}
			if errors.Is(err, errWalletEffectsLimit) {
				a.writeError(w, r, http.StatusUnprocessableEntity, "wallet_effects_limit_exceeded", "wallet transaction effects exceed the configured safety cap", false, map[string]any{"max_events": a.cfg.WalletEffectsMaxEvents})
				return
			}
			if errors.Is(err, errWalletEffectsStateLookup) {
				a.writeError(w, r, http.StatusInternalServerError, "internal", "state lookup failed", false, nil)
				return
			}
			a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "wallet transaction lookup failed", true, nil)
			return
		}
	}
	tx, found, err := a.node.Transaction(r.Context(), txid, includeRaw)
	if err != nil {
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "transaction lookup failed", true, nil)
		return
	}
	if !found {
		if walletID != "" && !includeRaw {
			tx, found, err = terminalScannerTransaction(txid, effects, ready.Node.Height)
			if err != nil {
				a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "wallet transaction terminal state is invalid", true, nil)
				return
			}
		}
		if !found {
			a.writeError(w, r, http.StatusNotFound, "not_found", "transaction not found", false, nil)
			return
		}
	}
	data := map[string]any{"transaction": tx}
	if walletID != "" {
		data["wallet_id"], data["wallet_effects"] = walletID, effects
	}
	a.writeData(w, r, http.StatusOK, data)
}

func terminalScannerTransaction(txid string, effects []walletEffect, nodeHeight int64) (domain.Transaction, bool, error) {
	var latest *walletEffect
	state := ""
	for index := range effects {
		effect := &effects[index]
		switch effect.Kind {
		case "DepositEvent", "DepositConfirmed", "DepositUnconfirmed",
			"SpendEvent", "SpendConfirmed", "SpendUnconfirmed",
			"OutgoingOutputEvent", "OutgoingOutputConfirmed", "OutgoingOutputUnconfirmed":
			latest, state = effect, ""
		case "DepositOrphaned", "SpendOrphaned", "OutgoingOutputOrphaned":
			latest, state = effect, "orphaned"
		case "OutgoingOutputExpired":
			latest, state = effect, "expired"
		}
	}
	if latest == nil || state == "" {
		return domain.Transaction{}, false, nil
	}
	if latest.TxID != txid || latest.State != state {
		return domain.Transaction{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned an invalid terminal transaction event")}
	}
	tx := domain.Transaction{TxID: txid, State: state, Confirmations: 0, BlockHeight: latest.BlockHeight}
	if state == "expired" {
		if latest.ExpiryHeight == nil || nodeHeight <= *latest.ExpiryHeight {
			return domain.Transaction{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned an invalid expired transaction event")}
		}
		tx.ExpiryHeight = latest.ExpiryHeight
	}
	return tx, true, nil
}

var (
	errWalletEffectsLimit       = errors.New("wallet effects limit exceeded")
	errWalletEffectsStateLookup = errors.New("wallet effects state lookup failed")
)

func (a *API) walletEffects(ctx context.Context, walletID, txid, eventEpoch string) ([]walletEffect, error) {
	effects := make([]walletEffect, 0)
	position := int64(0)
	for {
		remainingWithProbe := a.cfg.WalletEffectsMaxEvents + 1 - len(effects)
		pageLimit := 1000
		if remainingWithProbe < pageLimit {
			pageLimit = remainingWithProbe
		}
		page, err := a.scanner.Events(ctx, walletID, position, pageLimit, domain.EventFilter{TxID: txid})
		if err != nil {
			return nil, err
		}
		if page.EventEpoch != eventEpoch {
			return nil, errCursorResetRequired
		}
		if (len(page.Events) == 0 && page.NextCursor != position) || (len(page.Events) > 0 && page.NextCursor != page.Events[len(page.Events)-1].ID) {
			return nil, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned an inconsistent event cursor")}
		}
		for _, event := range page.Events {
			if event.ID <= position {
				return nil, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned non-forward transaction effects")}
			}
			effect, err := sanitizeWalletEffect(event, walletID, txid, a.cfg.Network.AddressHRP(), a.cfg.DefaultConfirmations)
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(event.Kind, "Deposit") {
				registered, err := a.bindRegisteredDepositIdentity(ctx, walletID, &effect)
				if err != nil {
					if !domain.IsUpstreamKind(err, "invalid_response") {
						return nil, fmt.Errorf("%w: %v", errWalletEffectsStateLookup, err)
					}
					return nil, err
				}
				if !registered && effect.DiversifierIndex == nil {
					return nil, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner omitted an unregistered deposit diversifier index")}
				}
			}
			position = event.ID
			effects = append(effects, effect)
			if len(effects) > a.cfg.WalletEffectsMaxEvents {
				return nil, errWalletEffectsLimit
			}
		}
		if page.NextCursor != position {
			return nil, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned an inconsistent event cursor")}
		}
		if len(page.Events) < pageLimit {
			return effects, nil
		}
	}
}

type broadcastRequest struct {
	WalletID     string `json:"wallet_id"`
	RawTxHex     string `json:"raw_tx_hex"`
	ExpectedTxID string `json:"expected_txid"`
}

func (a *API) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !idempotencyPattern.MatchString(key) {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "a valid Idempotency-Key header is required", false, nil)
		return
	}
	var req broadcastRequest
	if err := decodeJSON(w, r, a.cfg.BroadcastBodyBytes, &req); err != nil {
		a.writeDecodeError(w, r, err)
		return
	}
	if !walletIDPattern.MatchString(req.WalletID) {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "wallet_id is invalid", false, nil)
		return
	}
	p := principalFrom(r.Context())
	if !p.hasWallet(req.WalletID) {
		a.writeError(w, r, http.StatusForbidden, "forbidden", "credential is not authorized for this wallet", false, nil)
		return
	}
	if _, ok := a.registry.known(req.WalletID); !ok {
		a.writeError(w, r, http.StatusNotFound, "not_found", "wallet not found", false, nil)
		return
	}
	if req.RawTxHex == "" || req.RawTxHex != strings.ToLower(req.RawTxHex) || len(req.RawTxHex)%2 != 0 {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "raw_tx_hex must be non-empty, even-length lowercase hex", false, nil)
		return
	}
	if _, err := hex.DecodeString(req.RawTxHex); err != nil {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "raw_tx_hex must be valid lowercase hex", false, nil)
		return
	}
	if !txIDPattern.MatchString(req.ExpectedTxID) {
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "expected_txid must be 64 lowercase hex characters", false, nil)
		return
	}
	digestBytes := sha256.Sum256([]byte(req.WalletID + "\x00" + req.RawTxHex + "\x00" + req.ExpectedTxID))
	digest := hex.EncodeToString(digestBytes[:])
	receiptKey := scopedIdempotencyKey(p.name, key)
	claim, err := a.store.ClaimReceipt(r.Context(), receiptKey, digest, req.ExpectedTxID, time.Now(), a.cfg.IdempotencyLease)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "idempotency state failed", false, nil)
		return
	}
	switch claim.State {
	case storage.ClaimConflict:
		a.writeError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key was used with a different payload", false, nil)
		return
	case storage.ClaimInProgress:
		retryAfter := retryAfterSeconds(claim.Receipt.UpdatedAt, a.cfg.IdempotencyLease, time.Now())
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
		a.writeError(w, r, http.StatusConflict, "idempotency_in_progress", "an identical broadcast is still being resolved", true, map[string]any{"retry_after_seconds": retryAfter})
		return
	case storage.ClaimReplay:
		var stored storedReceipt
		if err := json.Unmarshal(claim.Receipt.ResponseJSON, &stored); err != nil {
			a.writeError(w, r, http.StatusInternalServerError, "internal", "stored idempotency response is invalid", false, nil)
			return
		}
		if stored.Error != nil {
			a.writeError(w, r, claim.Receipt.HTTPStatus, stored.Error.Code, stored.Error.Message, stored.Error.Retryable, stored.Error.Details)
			return
		}
		var data map[string]any
		if err := json.Unmarshal(stored.Data, &data); err != nil || data == nil {
			a.writeError(w, r, http.StatusInternalServerError, "internal", "stored idempotency response is invalid", false, nil)
			return
		}
		data["accepted"] = false
		data["already_known"] = true
		a.writeData(w, r, http.StatusOK, data)
		return
	}
	lease := newReceiptLease(r.Context(), a.store, receiptKey, digest, claim.Receipt.Generation, a.cfg.IdempotencyLease)
	defer lease.Abandon()
	operationCtx := lease.Context()
	if _, err := a.readyNode(operationCtx); err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "node_not_ready", "node is not ready", true, nil)
		return
	}
	decodedTxID, err := a.node.DecodeRawTransaction(operationCtx, req.RawTxHex)
	if err != nil {
		if domain.IsUpstreamKind(err, "unavailable") || domain.IsUpstreamKind(err, "invalid_response") {
			a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "transaction decoding failed", true, nil)
			return
		}
		a.writeError(w, r, http.StatusBadRequest, "invalid_request", "raw_tx_hex is not a valid transaction", false, nil)
		return
	}
	if decodedTxID != req.ExpectedTxID {
		a.writeError(w, r, http.StatusUnprocessableEntity, "expected_txid_mismatch", "expected_txid does not match raw_tx_hex", false, nil)
		return
	}
	if existing, found, err := a.node.Transaction(operationCtx, req.ExpectedTxID, false); err == nil && broadcastAlreadyKnown(existing, found) {
		data := map[string]any{"wallet_id": req.WalletID, "txid": req.ExpectedTxID, "state": existing.State, "accepted": false, "already_known": true}
		a.completeAndWrite(w, r, lease, http.StatusOK, data)
		return
	}
	txid, err := a.node.Broadcast(operationCtx, req.RawTxHex)
	if err != nil {
		if existing, found, lookupErr := a.node.Transaction(operationCtx, req.ExpectedTxID, false); lookupErr == nil && broadcastAlreadyKnown(existing, found) {
			data := map[string]any{"wallet_id": req.WalletID, "txid": req.ExpectedTxID, "state": existing.State, "accepted": false, "already_known": true}
			a.completeAndWrite(w, r, lease, http.StatusOK, data)
			return
		}
		if domain.IsUpstreamKind(err, "rejected") {
			a.completeErrorAndWrite(w, r, lease, http.StatusUnprocessableEntity, "transaction_rejected", "node rejected the signed transaction", false)
			return
		}
		if domain.IsUpstreamKind(err, "already_known") {
			data := map[string]any{"wallet_id": req.WalletID, "txid": req.ExpectedTxID, "state": "known", "accepted": false, "already_known": true}
			a.completeAndWrite(w, r, lease, http.StatusOK, data)
			return
		}
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "broadcast result is uncertain; retry with the same idempotency key", true, nil)
		return
	}
	if txid != req.ExpectedTxID {
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "node returned a transaction ID different from expected_txid", true, nil)
		return
	}
	data := map[string]any{"wallet_id": req.WalletID, "txid": txid, "state": "mempool", "accepted": true, "already_known": false}
	a.completeAndWrite(w, r, lease, http.StatusAccepted, data)
}

func broadcastAlreadyKnown(tx domain.Transaction, found bool) bool {
	if !found {
		return false
	}
	return tx.State == "mempool" || tx.State == "confirmed"
}

func retryAfterSeconds(updatedAt time.Time, lease time.Duration, now time.Time) int64 {
	remaining := updatedAt.Add(lease).Sub(now)
	seconds := int64(math.Ceil(remaining.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

func scopedIdempotencyKey(principalName, key string) string {
	sum := sha256.Sum256([]byte("v1\x00" + principalName + "\x00" + key))
	return "v1:" + hex.EncodeToString(sum[:])
}

type receiptLease struct {
	ctx             context.Context
	cancelOperation context.CancelFunc
	cancelHeartbeat context.CancelFunc
	done            chan error
	store           storage.Store
	key             string
	digest          string
	generation      int64
	stopped         bool
	stopErr         error
	finished        bool
}

func newReceiptLease(parent context.Context, store storage.Store, key, digest string, generation int64, duration time.Duration) *receiptLease {
	operationCtx, cancelOperation := context.WithCancel(parent)
	stopHeartbeat := make(chan struct{})
	cancelHeartbeat := func() { close(stopHeartbeat) }
	l := &receiptLease{ctx: operationCtx, cancelOperation: cancelOperation, cancelHeartbeat: cancelHeartbeat, done: make(chan error, 1), store: store, key: key, digest: digest, generation: generation}
	interval := duration / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				l.done <- nil
				return
			default:
			}
			select {
			case <-stopHeartbeat:
				l.done <- nil
				return
			case <-parent.Done():
				cancelOperation()
				l.done <- parent.Err()
				return
			case now := <-ticker.C:
				if err := store.RenewReceipt(parent, key, digest, generation, now); err != nil {
					cancelOperation()
					l.done <- err
					return
				}
			}
		}
	}()
	return l
}

func (l *receiptLease) Context() context.Context { return l.ctx }

func (l *receiptLease) stop() error {
	if l.stopped {
		return l.stopErr
	}
	l.cancelHeartbeat()
	l.stopErr = <-l.done
	l.stopped = true
	return l.stopErr
}

func (l *receiptLease) Complete(status int, response []byte) error {
	if l.finished {
		return errors.New("receipt lease is already finished")
	}
	if err := l.stop(); err != nil {
		l.finished = true
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := l.store.CompleteReceipt(ctx, l.key, l.digest, l.generation, status, response, time.Now())
	if err != nil {
		_ = l.store.AbandonReceipt(ctx, l.key, l.digest, l.generation, time.Now())
	}
	l.finished = true
	l.cancelOperation()
	return err
}

func (l *receiptLease) Abandon() {
	if l.finished {
		return
	}
	if err := l.stop(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = l.store.AbandonReceipt(ctx, l.key, l.digest, l.generation, time.Now())
		cancel()
	}
	l.finished = true
	l.cancelOperation()
}

func (a *API) completeAndWrite(w http.ResponseWriter, r *http.Request, lease *receiptLease, status int, data any) {
	dataRaw, err := json.Marshal(data)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "response encoding failed", false, nil)
		return
	}
	raw, err := json.Marshal(storedReceipt{Data: dataRaw})
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "response encoding failed", false, nil)
		return
	}
	if err := lease.Complete(status, raw); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "idempotency state failed", false, nil)
		return
	}
	a.writeData(w, r, status, data)
}

type storedReceipt struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error *errorBody      `json:"error,omitempty"`
}

func (a *API) completeErrorAndWrite(w http.ResponseWriter, r *http.Request, lease *receiptLease, status int, code, message string, retryable bool) {
	body := &errorBody{Code: code, Message: message, Retryable: retryable, Details: map[string]any{}}
	raw, err := json.Marshal(storedReceipt{Error: body})
	if err != nil || lease.Complete(status, raw) != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "idempotency state failed", false, nil)
		return
	}
	a.writeError(w, r, status, code, message, retryable, nil)
}

var errBodyTooLarge = errors.New("request body exceeds the configured limit")

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	if r.ContentLength > limit {
		return errBodyTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errBodyTooLarge
		}
		return errors.New("request body must be valid JSON with only documented fields")
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (a *API) writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errBodyTooLarge) {
		a.writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", err.Error(), false, nil)
		return
	}
	a.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error(), false, nil)
}

type envelope struct {
	Status    string     `json:"status"`
	Data      any        `json:"data,omitempty"`
	Error     *errorBody `json:"error,omitempty"`
	RequestID string     `json:"request_id"`
}
type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

func (a *API) writeData(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeJSON(w, status, envelope{Status: "ok", Data: data, RequestID: requestID(r.Context())})
}
func (a *API) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, envelope{Status: "error", Error: &errorBody{Code: code, Message: message, Retryable: retryable, Details: details}, RequestID: requestID(r.Context())})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
