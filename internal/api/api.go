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
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/config"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/storage"
)

var (
	txIDPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
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

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/live", a.handleLive)
	mux.HandleFunc("GET /v1/version", a.protected("read", false, false, a.handleVersion))
	mux.HandleFunc("GET /v1/health/ready", a.protected("read", false, false, a.handleReady))
	mux.HandleFunc("GET /v1/network/tip", a.protected("read", false, false, a.handleTip))
	mux.HandleFunc("POST /v1/wallets/{wallet_id}/addresses", a.protected("address", true, false, a.handleAllocateAddress))
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/addresses/{address}/balance", a.protected("read", true, false, a.handleBalance))
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/deposits", a.protected("read", true, false, a.handleDeposits))
	mux.HandleFunc("GET /v1/transactions/{txid}", a.protected("read", false, false, a.handleTransaction))
	mux.HandleFunc("POST /v1/transactions/broadcast", a.protected("broadcast", false, true, a.handleBroadcast))
	mux.HandleFunc("/", a.protected("read", false, false, func(w http.ResponseWriter, r *http.Request) {
		a.writeError(w, r, http.StatusNotFound, "not_found", "route not found", false, nil)
	}))
	return a.requestMiddleware(a.recoverMiddleware(mux))
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
	if health.Ready != nil && !*health.Ready {
		return readiness{}, "scanner_not_ready", errors.New("scanner reports not ready")
	}
	if a.cfg.RequireCompleteHistory {
		if health.HistoryComplete != nil && !*health.HistoryComplete {
			return readiness{}, "scanner_not_ready", errors.New("scanner history is incomplete")
		}
		if health.HistoryComplete == nil && health.Ready == nil {
			return readiness{}, "scanner_not_ready", errors.New("scanner cannot attest complete history")
		}
	}
	if lag > a.cfg.MaxScannerLag {
		return readiness{}, "scanner_not_ready", errors.New("scanner lag exceeds configured maximum")
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
	if tip.Network != a.cfg.Network.NodeChain() || tip.InitialBlockDownload || tip.Height < 0 || tip.Hash == "" {
		return domain.NodeTip{}, errors.New("node is not ready for configured network")
	}
	return tip, nil
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
	if tip.Network != a.cfg.Network.NodeChain() {
		a.writeError(w, r, http.StatusServiceUnavailable, "node_not_ready", "node network does not match gateway configuration", true, nil)
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

type depositPayload struct {
	WalletID         string `json:"wallet_id"`
	TxID             string `json:"txid"`
	Origin           string `json:"origin"`
	Height           int64  `json:"height"`
	ActionIndex      uint32 `json:"action_index"`
	AmountZatoshis   uint64 `json:"amount_zatoshis"`
	RecipientAddress string `json:"recipient_address"`
	DiversifierIndex uint32 `json:"diversifier_index"`
	ConfirmedHeight  *int64 `json:"confirmed_height,omitempty"`
	OrphanedAtHeight *int64 `json:"orphaned_at_height,omitempty"`
	RollbackHeight   *int64 `json:"rollback_height,omitempty"`
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
		var payload depositPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || !txIDPattern.MatchString(payload.TxID) || payload.WalletID != walletID || payload.Origin != "external" || payload.Height < 0 || !strings.HasPrefix(payload.RecipientAddress, a.cfg.Network.AddressHRP()+"1") {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an invalid deposit event", true, nil)
			return
		}
		publicStatus, ok := depositStatus(event.Kind)
		if !ok {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned an unexpected deposit event kind", true, nil)
			return
		}
		next = event.ID
		if allocated, registered, err := a.store.Address(r.Context(), walletID, payload.RecipientAddress); err != nil {
			a.writeError(w, r, http.StatusInternalServerError, "internal", "state lookup failed", false, nil)
			return
		} else if !registered {
			continue
		} else if allocated.DiversifierIndex != payload.DiversifierIndex {
			a.writeError(w, r, http.StatusBadGateway, "scanner_not_ready", "scanner returned a deposit with mismatched address identity", true, nil)
			return
		}
		if status != "" && publicStatus != status {
			continue
		}
		if address != "" && payload.RecipientAddress != address {
			continue
		}
		items = append(items, deposit{DepositID: walletID + ":" + payload.TxID + ":" + strconv.FormatUint(uint64(payload.ActionIndex), 10), EventID: page.EventEpoch + ":" + strconv.FormatInt(event.ID, 10), WalletID: walletID, TxID: payload.TxID, ActionIndex: payload.ActionIndex, Address: payload.RecipientAddress, DiversifierIndex: payload.DiversifierIndex, AmountZat: payload.AmountZatoshis, Status: publicStatus, BlockHeight: payload.Height, ConfirmedHeight: payload.ConfirmedHeight, OrphanedAtHeight: payload.OrphanedAtHeight, RollbackHeight: payload.RollbackHeight, ObservedAt: event.CreatedAt})
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
	tx, found, err := a.node.Transaction(r.Context(), txid, includeRaw)
	if err != nil {
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "transaction lookup failed", true, nil)
		return
	}
	if !found {
		a.writeError(w, r, http.StatusNotFound, "not_found", "transaction not found", false, nil)
		return
	}
	data := map[string]any{"transaction": tx}
	if walletID != "" {
		effects, err := a.walletEffects(r.Context(), walletID, txid, ready.Scanner.EventEpoch)
		if err != nil {
			if errors.Is(err, errCursorResetRequired) {
				a.writeError(w, r, http.StatusConflict, "event_history_reset", "scanner event history changed during transaction lookup; retry the request", true, map[string]any{"event_epoch": ready.Scanner.EventEpoch})
				return
			}
			if errors.Is(err, errWalletEffectsLimit) {
				a.writeError(w, r, http.StatusUnprocessableEntity, "wallet_effects_limit_exceeded", "wallet transaction effects exceed the configured safety cap", false, map[string]any{"max_events": a.cfg.WalletEffectsMaxEvents})
				return
			}
			a.writeError(w, r, http.StatusServiceUnavailable, "scanner_not_ready", "wallet transaction lookup failed", true, nil)
			return
		}
		data["wallet_id"], data["wallet_effects"] = walletID, effects
	}
	a.writeData(w, r, http.StatusOK, data)
}

var errWalletEffectsLimit = errors.New("wallet effects limit exceeded")

func (a *API) walletEffects(ctx context.Context, walletID, txid, eventEpoch string) ([]domain.ScannerEvent, error) {
	effects := make([]domain.ScannerEvent, 0)
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
			position = event.ID
			effects = append(effects, event)
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
	if _, err := a.readyNode(r.Context()); err != nil {
		a.writeError(w, r, http.StatusServiceUnavailable, "node_not_ready", "node is not ready", true, nil)
		return
	}
	decodedTxID, err := a.node.DecodeRawTransaction(r.Context(), req.RawTxHex)
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
	digestBytes := sha256.Sum256([]byte(req.RawTxHex + "\x00" + req.ExpectedTxID))
	digest := hex.EncodeToString(digestBytes[:])
	claim, err := a.store.ClaimReceipt(r.Context(), key, digest, req.ExpectedTxID, time.Now(), a.cfg.IdempotencyLease)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "idempotency state failed", false, nil)
		return
	}
	switch claim.State {
	case storage.ClaimConflict:
		a.writeError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key was used with a different payload", false, nil)
		return
	case storage.ClaimInProgress:
		a.writeError(w, r, http.StatusConflict, "idempotency_in_progress", "an identical broadcast is still being resolved", true, nil)
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
		var data any
		if err := json.Unmarshal(stored.Data, &data); err != nil {
			a.writeError(w, r, http.StatusInternalServerError, "internal", "stored idempotency response is invalid", false, nil)
			return
		}
		a.writeData(w, r, http.StatusOK, data)
		return
	}
	if existing, found, err := a.node.Transaction(r.Context(), req.ExpectedTxID, false); err == nil && found {
		data := map[string]any{"txid": req.ExpectedTxID, "state": existing.State, "accepted": false, "already_known": true}
		a.completeAndWrite(w, r, key, digest, http.StatusOK, data)
		return
	}
	txid, err := a.node.Broadcast(r.Context(), req.RawTxHex)
	if err != nil {
		if existing, found, lookupErr := a.node.Transaction(r.Context(), req.ExpectedTxID, false); lookupErr == nil && found {
			data := map[string]any{"txid": req.ExpectedTxID, "state": existing.State, "accepted": false, "already_known": true}
			a.completeAndWrite(w, r, key, digest, http.StatusOK, data)
			return
		}
		if domain.IsUpstreamKind(err, "rejected") {
			a.completeErrorAndWrite(w, r, key, digest, http.StatusUnprocessableEntity, "transaction_rejected", "node rejected the signed transaction", false)
			return
		}
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "broadcast result is uncertain; retry with the same idempotency key", true, nil)
		return
	}
	if txid != req.ExpectedTxID {
		a.writeError(w, r, http.StatusBadGateway, "node_rpc_error", "node returned a transaction ID different from expected_txid", true, nil)
		return
	}
	data := map[string]any{"txid": txid, "state": "mempool", "accepted": true, "already_known": false}
	a.completeAndWrite(w, r, key, digest, http.StatusAccepted, data)
}

func (a *API) completeAndWrite(w http.ResponseWriter, r *http.Request, key, digest string, status int, data any) {
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
	if err := a.store.CompleteReceipt(r.Context(), key, digest, status, raw, time.Now()); err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "idempotency state failed", false, nil)
		return
	}
	a.writeData(w, r, status, data)
}

type storedReceipt struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error *errorBody      `json:"error,omitempty"`
}

func (a *API) completeErrorAndWrite(w http.ResponseWriter, r *http.Request, key, digest string, status int, code, message string, retryable bool) {
	body := &errorBody{Code: code, Message: message, Retryable: retryable, Details: map[string]any{}}
	raw, err := json.Marshal(storedReceipt{Error: body})
	if err != nil || a.store.CompleteReceipt(r.Context(), key, digest, status, raw, time.Now()) != nil {
		a.writeError(w, r, http.StatusInternalServerError, "internal", "idempotency state failed", false, nil)
		return
	}
	a.writeError(w, r, status, code, message, retryable, nil)
}

var errBodyTooLarge = errors.New("request body exceeds the configured limit")

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
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
