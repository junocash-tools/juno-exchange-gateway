package coordinator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
)

type Handler struct {
	cfg     config.Config
	service *Service
	limit   *coordinatorLimiter
}

type coordinatorPrincipal struct {
	name    string
	scopes  map[string]struct{}
	wallets map[string]struct{}
}

func NewHandler(cfg config.Config, service *Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("coordinator service is required")
	}
	return &Handler{cfg: cfg, service: service, limit: newCoordinatorLimiter(cfg.CoordinatorRate)}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r.Header.Get("X-Request-ID"))
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")

	if r.URL.Path == "/v1/health/live" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			h.writeError(w, http.StatusMethodNotAllowed, requestID, "method_not_allowed", "method not allowed", false)
			return
		}
		h.writeData(w, http.StatusOK, requestID, map[string]any{"service": "transaction-coordinator", "status": "live"})
		return
	}

	principal, ok := h.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		h.writeError(w, http.StatusUnauthorized, requestID, "unauthorized", "a valid bearer credential is required", false)
		return
	}
	if !principal.hasScope("plan") {
		h.writeError(w, http.StatusForbidden, requestID, "forbidden", "credential lacks the plan scope", false)
		return
	}
	if !h.limit.allow(principal.name, time.Now()) {
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusTooManyRequests, requestID, "rate_limited", "request rate limit exceeded", true)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.ReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	switch {
	case r.URL.Path == "/v1/health/ready":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			h.writeError(w, http.StatusMethodNotAllowed, requestID, "method_not_allowed", "method not allowed", false)
			return
		}
		if err := h.service.Ready(r.Context()); err != nil {
			h.writeError(w, http.StatusServiceUnavailable, requestID, "coordinator_not_ready", err.Error(), true)
			return
		}
		h.writeData(w, http.StatusOK, requestID, map[string]any{"service": "transaction-coordinator", "status": "ready"})
	case r.URL.Path == "/v1/transaction-attempts":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			h.writeError(w, http.StatusMethodNotAllowed, requestID, "method_not_allowed", "method not allowed", false)
			return
		}
		h.handleCreate(w, r, requestID, principal)
	default:
		attemptID, action, matched := attemptPath(r.URL.Path)
		if !matched {
			h.writeError(w, http.StatusNotFound, requestID, "not_found", "route not found", false)
			return
		}
		if action == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.handleGet(w, r, requestID, principal, attemptID)
			return
		}
		if action == "cancel" && r.Method == http.MethodPost {
			h.handleCancel(w, r, requestID, principal, attemptID)
			return
		}
		allowed := http.MethodGet
		if action == "cancel" {
			allowed = http.MethodPost
		}
		w.Header().Set("Allow", allowed)
		h.writeError(w, http.StatusMethodNotAllowed, requestID, "method_not_allowed", "method not allowed", false)
	}
}

func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request, requestID string, principal coordinatorPrincipal) {
	if !jsonContentType(r.Header.Get("Content-Type")) {
		h.writeError(w, http.StatusUnsupportedMediaType, requestID, "unsupported_media_type", "Content-Type must be application/json", false)
		return
	}
	var request CreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.cfg.CoordinatorMaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.writeError(w, http.StatusRequestEntityTooLarge, requestID, "invalid_request", "request body exceeds the configured limit", false)
			return
		}
		h.writeError(w, http.StatusBadRequest, requestID, "invalid_request", "invalid request JSON", false)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		h.writeError(w, http.StatusBadRequest, requestID, "invalid_request", "request must contain one JSON object", false)
		return
	}
	if !principal.hasWallet(strings.TrimSpace(request.WalletID)) {
		h.writeError(w, http.StatusForbidden, requestID, "forbidden", "credential is not authorized for this wallet", false)
		return
	}
	attempt, replayed, err := h.service.Create(r.Context(), principal.name, strings.TrimSpace(r.Header.Get("Idempotency-Key")), request)
	if err != nil {
		h.writeOperationError(w, requestID, err)
		return
	}
	status := http.StatusAccepted
	if replayed && !recoverableState(attempt.State) {
		status = http.StatusOK
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	h.writeData(w, status, requestID, attempt)
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, requestID string, principal coordinatorPrincipal, attemptID string) {
	attempt, err := h.service.Attempt(r.Context(), principal.name, attemptID)
	if err != nil {
		h.writeOperationError(w, requestID, err)
		return
	}
	if !principal.hasWallet(attempt.WalletID) {
		h.writeError(w, http.StatusForbidden, requestID, "forbidden", "credential is not authorized for this wallet", false)
		return
	}
	h.writeData(w, http.StatusOK, requestID, attempt)
}

func (h *Handler) handleCancel(w http.ResponseWriter, r *http.Request, requestID string, principal coordinatorPrincipal, attemptID string) {
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "" && !jsonContentType(contentType) {
		h.writeError(w, http.StatusUnsupportedMediaType, requestID, "unsupported_media_type", "Content-Type must be application/json when a body is sent", false)
		return
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 17))
		if err != nil || (len(strings.TrimSpace(string(body))) != 0 && strings.TrimSpace(string(body)) != "{}") {
			h.writeError(w, http.StatusBadRequest, requestID, "invalid_request", "cancel accepts an empty body or one empty JSON object", false)
			return
		}
	}
	attempt, err := h.service.Attempt(r.Context(), principal.name, attemptID)
	if err != nil {
		h.writeOperationError(w, requestID, err)
		return
	}
	if !principal.hasWallet(attempt.WalletID) {
		h.writeError(w, http.StatusForbidden, requestID, "forbidden", "credential is not authorized for this wallet", false)
		return
	}
	attempt, err = h.service.Cancel(r.Context(), principal.name, attemptID)
	if err != nil {
		h.writeOperationError(w, requestID, err)
		return
	}
	h.writeData(w, http.StatusOK, requestID, attempt)
}

func (h *Handler) authenticate(r *http.Request) (coordinatorPrincipal, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return coordinatorPrincipal{}, false
	}
	token := strings.TrimSpace(header[7:])
	if token == "" {
		return coordinatorPrincipal{}, false
	}
	hash := sha256.Sum256([]byte(token))
	for _, credential := range h.cfg.Credentials {
		if subtle.ConstantTimeCompare(hash[:], credential.TokenHash[:]) != 1 {
			continue
		}
		principal := coordinatorPrincipal{name: credential.Name, scopes: make(map[string]struct{}), wallets: make(map[string]struct{})}
		for _, scope := range credential.Scopes {
			principal.scopes[scope] = struct{}{}
		}
		for _, wallet := range credential.Wallets {
			principal.wallets[wallet] = struct{}{}
		}
		return principal, true
	}
	return coordinatorPrincipal{}, false
}

func (p coordinatorPrincipal) hasScope(scope string) bool {
	_, admin := p.scopes["admin"]
	_, allowed := p.scopes[scope]
	return admin || allowed
}

func (p coordinatorPrincipal) hasWallet(wallet string) bool {
	_, all := p.wallets["*"]
	_, allowed := p.wallets[wallet]
	return all || allowed
}

func (h *Handler) writeOperationError(w http.ResponseWriter, requestID string, err error) {
	var operation *operationError
	if !errors.As(err, &operation) {
		h.writeError(w, http.StatusInternalServerError, requestID, "internal", "internal coordinator error", false)
		return
	}
	status := http.StatusInternalServerError
	switch operation.Code {
	case "invalid_request":
		status = http.StatusBadRequest
	case "unauthorized":
		status = http.StatusUnauthorized
	case "forbidden":
		status = http.StatusForbidden
	case "not_found":
		status = http.StatusNotFound
	case "idempotency_conflict", "attempt_not_cancellable":
		status = http.StatusConflict
	case "coordinator_recovery_sealed", "recovery_gate_unavailable", "expiry_status_unavailable":
		status = http.StatusServiceUnavailable
	case "rate_limited":
		status = http.StatusTooManyRequests
	}
	h.writeError(w, status, requestID, operation.Code, operation.Message, operation.Retryable)
}

func (h *Handler) writeData(w http.ResponseWriter, status int, requestID string, data any) {
	h.writeJSON(w, status, map[string]any{
		"status":     "ok",
		"data":       data,
		"request_id": requestID,
	})
}

func (h *Handler) writeError(w http.ResponseWriter, status int, requestID, code, message string, retryable bool) {
	h.writeJSON(w, status, map[string]any{
		"status":     "error",
		"error":      APIError{Code: code, Message: message, Retryable: retryable},
		"request_id": requestID,
	})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func attemptPath(path string) (string, string, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 3 || len(segments) > 4 || segments[0] != "v1" || segments[1] != "transaction-attempts" || segments[2] == "" {
		return "", "", false
	}
	if len(segments) == 3 {
		return segments[2], "", true
	}
	if segments[3] == "cancel" {
		return segments[2], "cancel", true
	}
	return "", "", false
}

func jsonContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func requestID(supplied string) string {
	supplied = strings.TrimSpace(supplied)
	if len(supplied) > 0 && len(supplied) <= 128 {
		valid := true
		for _, char := range supplied {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:-", char)) {
				valid = false
				break
			}
		}
		if valid {
			return supplied
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return hex.EncodeToString(random)
	}
	sum := sha256.Sum256([]byte(time.Now().UTC().String()))
	return hex.EncodeToString(sum[:16])
}

type coordinatorBucket struct {
	tokens float64
	last   time.Time
}

type coordinatorLimiter struct {
	mu      sync.Mutex
	rps     float64
	burst   float64
	buckets map[string]coordinatorBucket
}

func newCoordinatorLimiter(limit config.RateLimit) *coordinatorLimiter {
	return &coordinatorLimiter{rps: limit.RPS, burst: float64(limit.Burst), buckets: make(map[string]coordinatorBucket)}
}

func (l *coordinatorLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, exists := l.buckets[key]
	if !exists {
		bucket = coordinatorBucket{tokens: l.burst, last: now}
	}
	elapsed := now.Sub(bucket.last).Seconds()
	bucket.tokens += elapsed * l.rps
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	bucket.last = now
	if bucket.tokens < 1 {
		l.buckets[key] = bucket
		return false
	}
	bucket.tokens--
	l.buckets[key] = bucket
	return true
}
