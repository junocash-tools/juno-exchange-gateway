package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math"
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
	storagepkg "github.com/junocash-tools/juno-exchange-gateway/internal/storage/sqlite"
)

type fakeNode struct {
	mu             sync.Mutex
	tip            domain.NodeTip
	tipErr         error
	tipCalls       int
	blockHashes    map[int64]string
	blockHashErr   error
	blockHashCalls int
	decodedTxID    string
	decodeErr      error
	decodeCalls    int
	transactions   map[string]domain.Transaction
	transactionErr error
	broadcastTxID  string
	broadcastErr   error
	broadcastCalls int
}

func (f *fakeNode) Tip(context.Context) (domain.NodeTip, error) {
	f.tipCalls++
	return f.tip, f.tipErr
}
func (f *fakeNode) BlockHash(_ context.Context, height int64) (string, error) {
	f.blockHashCalls++
	if f.blockHashErr != nil {
		return "", f.blockHashErr
	}
	if hash, ok := f.blockHashes[height]; ok {
		return hash, nil
	}
	if height == f.tip.Height {
		return f.tip.Hash, nil
	}
	return strings.Repeat("d", 64), nil
}
func (f *fakeNode) DecodeRawTransaction(context.Context, string) (string, error) {
	f.decodeCalls++
	return f.decodedTxID, f.decodeErr
}
func (f *fakeNode) Transaction(_ context.Context, txid string, _ bool) (domain.Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transactionErr != nil {
		return domain.Transaction{}, false, f.transactionErr
	}
	tx, ok := f.transactions[txid]
	return tx, ok, nil
}
func (f *fakeNode) Broadcast(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcastCalls++
	return f.broadcastTxID, f.broadcastErr
}

type fakeScanner struct {
	health            domain.ScannerHealth
	balance           domain.Balance
	balanceFound      bool
	balanceCalls      int
	lastConfirmations int64
	events            domain.EventsPage
	eventPages        map[int64]domain.EventsPage
	lastCursor        int64
	lastEventLimit    int
	backfillFrom      int64
	backfillTo        int64
	backfillBatch     int64
	backfillWallet    string
	backfillCalls     int
	backfillStatuses  map[string]domain.BackfillStatus
}

func (f *fakeScanner) Health(context.Context) (domain.ScannerHealth, error) { return f.health, nil }
func (f *fakeScanner) UpsertWallet(_ context.Context, walletID, ufvk string, birthday int64) error {
	if f.backfillStatuses == nil {
		f.backfillStatuses = map[string]domain.BackfillStatus{}
	}
	if _, ok := f.backfillStatuses[walletID]; !ok {
		sum := sha256.Sum256([]byte(strings.TrimSpace(ufvk)))
		f.backfillStatuses[walletID] = domain.BackfillStatus{WalletID: walletID, UFVKFingerprint: hex.EncodeToString(sum[:]), BirthdayHeight: birthday, NextHeight: birthday, State: "pending"}
	}
	return nil
}
func (f *fakeScanner) BackfillStatus(_ context.Context, walletID string) (domain.BackfillStatus, bool, error) {
	status, ok := f.backfillStatuses[walletID]
	return status, ok, nil
}
func (f *fakeScanner) Backfill(_ context.Context, walletID string, toHeight, batchSize int64) (int64, error) {
	f.backfillCalls++
	f.backfillWallet = walletID
	status := f.backfillStatuses[walletID]
	f.backfillFrom = status.NextHeight
	f.backfillTo = toHeight
	f.backfillBatch = batchSize
	status.NextHeight = toHeight + 1
	status.TargetHeight = toHeight
	status.State = "complete"
	f.backfillStatuses[walletID] = status
	return toHeight + 1, nil
}
func (f *fakeScanner) Balance(_ context.Context, _, _ string, confirmations, _ int64) (domain.Balance, bool, error) {
	f.balanceCalls++
	f.lastConfirmations = confirmations
	return f.balance, f.balanceFound, nil
}
func (f *fakeScanner) Events(_ context.Context, _ string, cursor int64, limit int, _ domain.EventFilter) (domain.EventsPage, error) {
	f.lastCursor = cursor
	f.lastEventLimit = limit
	if page, ok := f.eventPages[cursor]; ok {
		return page, nil
	}
	return f.events, nil
}

type fakeDeriver struct{ network domain.Network }

func (f fakeDeriver) Derive(_ context.Context, _ string, index uint32) (string, error) {
	return f.network.AddressHRP() + "1allocated" + string(rune('a'+index)), nil
}

type legacyWalletStore struct{ *storagepkg.Store }

type renewRecordingStore struct {
	storage.Store
	renewed chan struct{}
}

func (s renewRecordingStore) RenewReceipt(ctx context.Context, key, digest string, generation int64, now time.Time) error {
	if err := s.Store.RenewReceipt(ctx, key, digest, generation, now); err != nil {
		return err
	}
	select {
	case s.renewed <- struct{}{}:
	default:
	}
	return nil
}

func (s legacyWalletStore) Wallet(ctx context.Context, walletID string) (storage.Wallet, bool, error) {
	wallet, found, err := s.Store.Wallet(ctx, walletID)
	wallet.UFVKFingerprint = ""
	return wallet, found, err
}

func testConfig(network domain.Network) config.Config {
	ready := true
	scanned := int64(100)
	lag := int64(0)
	_ = ready
	_ = scanned
	_ = lag
	ufvk := map[domain.Network]string{domain.Regtest: "jviewregtest1example", domain.Testnet: "jviewtest1example", domain.Mainnet: "jview1example"}[network]
	return config.Config{Network: network, ListenAddress: ":0", StateDSN: "unused", NodeRPCURL: "http://node", ScannerURL: "http://scanner", AddrgenPath: "addrgen", Wallets: []config.Wallet{{WalletID: "hot", UFVK: ufvk}}, DefaultConfirmations: 100, MaxConfirmations: 10000, MaxScannerLag: 2, RequireCompleteHistory: true, JSONBodyBytes: 1 << 20, BroadcastBodyBytes: 4 << 20, ReadTimeout: time.Second, BroadcastTimeout: time.Second, UpstreamTimeout: time.Second, ShutdownTimeout: time.Second, IdempotencyLease: time.Minute, WalletEffectsMaxEvents: 10000, ReadRate: config.RateLimit{RPS: 1000, Burst: 1000}, BroadcastRate: config.RateLimit{RPS: 1000, Burst: 1000}}
}

func newTestAPI(t *testing.T, cfg config.Config) (*API, *fakeNode, *fakeScanner) {
	t.Helper()
	store, err := storagepkg.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "state.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := &fakeNode{tip: domain.NodeTip{Network: cfg.Network.NodeChain(), Height: 100, Hash: strings.Repeat("b", 64), Headers: 100, VerificationProgress: 1}, blockHashes: map[int64]string{}, transactions: map[string]domain.Transaction{}}
	ready := true
	scanned := int64(100)
	lag := int64(0)
	confirmations := cfg.DefaultConfirmations
	scanner := &fakeScanner{health: domain.ScannerHealth{Status: "ok", Network: string(cfg.Network), UAHRP: cfg.Network.AddressHRP(), Confirmations: &confirmations, EventEpoch: strings.Repeat("e", 64), Ready: &ready, ScannedHeight: &scanned, ScannedHash: node.tip.Hash, ScannerLag: &lag}, balanceFound: true, backfillStatuses: map[string]domain.BackfillStatus{}}
	service, err := New(cfg, store, node, scanner, fakeDeriver{network: cfg.Network}, slog.New(slog.NewTextHandler(io.Discard, nil)), BuildInfo{Version: "test", Revision: "abc", APIVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.registry.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, configuredWallet := range cfg.Wallets {
		scanner.backfillStatuses[configuredWallet.WalletID] = domain.BackfillStatus{WalletID: configuredWallet.WalletID, UFVKFingerprint: configuredWallet.UFVKFingerprint(), BirthdayHeight: configuredWallet.BirthdayHeight, NextHeight: 101, TargetHeight: 100, State: "complete"}
	}
	for _, configuredWallet := range cfg.Wallets {
		wallet, ok, err := store.Wallet(context.Background(), configuredWallet.WalletID)
		if err != nil || !ok {
			t.Fatalf("wallet state: ok=%v err=%v", ok, err)
		}
		if wallet.NextBackfillHeight <= 100 {
			if err := store.AdvanceBackfill(context.Background(), configuredWallet.WalletID, wallet.NextBackfillHeight, 101); err != nil {
				t.Fatal(err)
			}
		}
	}
	return service, node, scanner
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func allocate(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := request(t, handler, http.MethodPost, "/v1/wallets/hot/addresses", `{"label":"customer-1"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("allocate status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Address string `json:"address"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Data.Address
}

func walletEffectPayload(walletID, txid string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"wallet_id": walletID, "txid": txid})
	return raw
}

func TestBalanceRequiresAllocatedAddressAndUsesDefaultConfirmations(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	address := allocate(t, handler)
	scanner.balance = domain.Balance{WalletID: "hot", RecipientAddress: address, AvailableZat: 42, TotalUnspentZat: 42, MinConfirmations: 100, AsOfNodeHeight: 100, AsOfScannerHeight: 100}
	rec := request(t, handler, http.MethodGet, "/v1/wallets/hot/addresses/"+address+"/balance", ``, nil)
	if rec.Code != http.StatusOK || scanner.lastConfirmations != 100 {
		t.Fatalf("status=%d confirmations=%d body=%s", rec.Code, scanner.lastConfirmations, rec.Body.String())
	}
	scanner.balance.MinConfirmations = 0
	rec = request(t, handler, http.MethodGet, "/v1/wallets/hot/addresses/"+address+"/balance?min_confirmations=0", ``, nil)
	if rec.Code != http.StatusOK || scanner.lastConfirmations != 0 {
		t.Fatalf("zero-confirmation status=%d confirmations=%d body=%s", rec.Code, scanner.lastConfirmations, rec.Body.String())
	}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/hot/addresses/jregtest1notallocated/balance", ``, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if scanner.balanceCalls != 2 {
		t.Fatalf("unowned address reached scanner")
	}
}

func TestDepositsUseOpaqueWalletBoundCursor(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	address := allocate(t, handler)
	txid := strings.Repeat("a", 64)
	payload, _ := json.Marshal(depositPayload{WalletID: "hot", TxID: txid, Origin: "external", Height: 90, ActionIndex: 2, AmountZatoshis: 99, RecipientAddress: address})
	scanner.events = domain.EventsPage{Events: []domain.ScannerEvent{{ID: 7, Kind: "DepositEvent", Height: 90, Payload: payload, CreatedAt: time.Unix(1, 0).UTC()}}, NextCursor: 7, EventEpoch: scanner.health.EventEpoch}
	rec := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?limit=1", ``, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			NextCursor string    `json:"next_cursor"`
			Deposits   []deposit `json:"deposits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Deposits) != 1 || out.Data.Deposits[0].DepositID != "hot:"+txid+":2" || out.Data.Deposits[0].EventID != scanner.health.EventEpoch+":7" || !strings.Contains(out.Data.NextCursor, ".") {
		t.Fatalf("response=%+v", out.Data)
	}
	scanner.events = domain.EventsPage{NextCursor: 7, EventEpoch: scanner.health.EventEpoch}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?cursor="+out.Data.NextCursor, ``, nil)
	if rec.Code != http.StatusOK || scanner.lastCursor != 7 {
		t.Fatalf("status=%d cursor=%d body=%s", rec.Code, scanner.lastCursor, rec.Body.String())
	}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/other/deposits?cursor="+out.Data.NextCursor, ``, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestDepositCursorResetIsExplicitAcrossScannerEpochs(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	scanner.events = domain.EventsPage{NextCursor: 0, EventEpoch: scanner.health.EventEpoch}
	first := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits", ``, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Data struct {
			NextCursor string `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	newEpoch := strings.Repeat("f", 64)
	scanner.health.EventEpoch = newEpoch
	scanner.events.EventEpoch = newEpoch
	reset := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?cursor="+firstBody.Data.NextCursor, ``, nil)
	if reset.Code != http.StatusConflict || !strings.Contains(reset.Body.String(), `"code":"cursor_reset_required"`) || !strings.Contains(reset.Body.String(), `"action":"restart_without_cursor"`) || !strings.Contains(reset.Body.String(), newEpoch) {
		t.Fatalf("status=%d body=%s", reset.Code, reset.Body.String())
	}
}

func TestDepositCursorIsBoundToCanonicalFilters(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	scanner.events = domain.EventsPage{NextCursor: 0, EventEpoch: scanner.health.EventEpoch}
	first := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?status=confirmed", ``, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var body struct {
		Data struct {
			NextCursor string `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	mismatch := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?status=detected&cursor="+body.Data.NextCursor, ``, nil)
	if mismatch.Code != http.StatusConflict || !strings.Contains(mismatch.Body.String(), "cursor_filter_mismatch") {
		t.Fatalf("status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	same := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?status=confirmed&cursor="+body.Data.NextCursor, ``, nil)
	if same.Code != http.StatusOK {
		t.Fatalf("same-filter status=%d body=%s", same.Code, same.Body.String())
	}
}

func TestDepositsOmitAddressesOutsideGatewayRegistry(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	_ = allocate(t, handler) // A scanner-suppressed change event must never be fabricated by the gateway.
	txid := strings.Repeat("a", 64)
	payload, _ := json.Marshal(depositPayload{WalletID: "hot", TxID: txid, Origin: "external", Height: 90, ActionIndex: 1, AmountZatoshis: 55, RecipientAddress: "jregtest1derivedoutsidegateway"})
	scanner.events = domain.EventsPage{Events: []domain.ScannerEvent{{ID: 9, Kind: "DepositEvent", Height: 90, Payload: payload, CreatedAt: time.Unix(1, 0).UTC()}}, NextCursor: 9, EventEpoch: scanner.health.EventEpoch}
	rec := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits", ``, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Deposits   []deposit `json:"deposits"`
			NextCursor string    `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Deposits) != 0 {
		t.Fatalf("unregistered event escaped: %+v", out.Data.Deposits)
	}
	position, err := service.codec.decode(out.Data.NextCursor, "hot", scanner.health.EventEpoch, depositCursorFilter("", "", ""))
	if err != nil || position != 9 {
		t.Fatalf("position=%d err=%v", position, err)
	}
	scanner.events = domain.EventsPage{NextCursor: 9, EventEpoch: scanner.health.EventEpoch}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?cursor="+out.Data.NextCursor, ``, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty scanner-suppressed page status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDepositsFailClosedOnMissingOrInternalOrigin(t *testing.T) {
	for _, origin := range []string{"", "internal"} {
		name := origin
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			handler := service.Handler()
			address := allocate(t, handler)
			txid := strings.Repeat("a", 64)
			payload := map[string]any{"wallet_id": "hot", "txid": txid, "height": 90, "action_index": 1, "amount_zatoshis": 55, "recipient_address": address, "diversifier_index": 0}
			if origin != "" {
				payload["origin"] = origin
			}
			raw, _ := json.Marshal(payload)
			scanner.events = domain.EventsPage{Events: []domain.ScannerEvent{{ID: 1, Kind: "DepositEvent", Height: 90, Payload: raw, CreatedAt: time.Unix(1, 0).UTC()}}, NextCursor: 1, EventEpoch: scanner.health.EventEpoch}
			rec := request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits", ``, nil)
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDepositsRejectBackwardEmptyPageCursor(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	scanner.events = domain.EventsPage{NextCursor: -1, EventEpoch: scanner.health.EventEpoch}
	rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/deposits", ``, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBroadcastIsSignedRawOnlyAndIdempotent(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("c", 64)
	node.broadcastTxID = txid
	node.decodedTxID = txid
	body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-1"}
	rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if rec.Code != http.StatusOK || node.broadcastCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"accepted":false`) || !strings.Contains(rec.Body.String(), `"already_known":true`) {
		t.Fatalf("replay body=%s", rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", `{"raw_tx_hex":"01","expected_txid":"`+txid+`"}`, headers)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", `{"raw_tx_hex":"00","expected_txid":"`+txid+`","seed":"secret"}`, map[string]string{"Idempotency-Key": "withdrawal-2"})
	if rec.Code != http.StatusBadRequest || node.broadcastCalls != 1 {
		t.Fatalf("secret field accepted status=%d", rec.Code)
	}
}

func TestCompletedBroadcastReplaysWhileNodeIsUnavailable(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("c", 64)
	node.broadcastTxID = txid
	node.decodedTxID = txid
	body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-replay-outage"}
	if rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers); rec.Code != http.StatusAccepted {
		t.Fatalf("initial status=%d body=%s", rec.Code, rec.Body.String())
	}
	tipCalls, decodeCalls := node.tipCalls, node.decodeCalls
	node.tipErr = &domain.UpstreamError{Kind: "unavailable", Err: io.EOF}
	node.decodeErr = &domain.UpstreamError{Kind: "unavailable", Err: io.EOF}
	replay := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if replay.Code != http.StatusOK || node.tipCalls != tipCalls || node.decodeCalls != decodeCalls || node.broadcastCalls != 1 {
		t.Fatalf("replay status=%d tips=%d decodes=%d broadcasts=%d body=%s", replay.Code, node.tipCalls, node.decodeCalls, node.broadcastCalls, replay.Body.String())
	}
}

func TestReceiptLeaseRenewsDuringLongNodeOperation(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, _ := newTestAPI(t, cfg)
	now := time.Now()
	claim, err := service.store.ClaimReceipt(context.Background(), "lease-key", "lease-digest", strings.Repeat("a", 64), now, 30*time.Millisecond)
	if err != nil || claim.State != storage.ClaimAcquired {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	recorded := renewRecordingStore{Store: service.store, renewed: make(chan struct{}, 1)}
	lease := newReceiptLease(context.Background(), recorded, "lease-key", "lease-digest", claim.Receipt.Generation, 30*time.Millisecond)
	select {
	case <-recorded.renewed:
	case <-time.After(time.Second):
		lease.Abandon()
		t.Fatal("receipt lease was not renewed")
	}
	if err := lease.Complete(http.StatusAccepted, []byte(`{"data":{"txid":"ok"}}`)); err != nil {
		t.Fatal(err)
	}
	replay, err := service.store.ClaimReceipt(context.Background(), "lease-key", "lease-digest", strings.Repeat("a", 64), time.Now(), 30*time.Millisecond)
	if err != nil || replay.State != storage.ClaimReplay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestBroadcastRejectsJSONLikeContentTypes(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	txid := strings.Repeat("c", 64)
	body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, map[string]string{
		"Content-Type":    "application/jsonp",
		"Idempotency-Key": "wrong-media-type",
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Content-Type must be application/json") || node.decodeCalls != 0 || node.broadcastCalls != 0 {
		t.Fatalf("status=%d decodes=%d broadcasts=%d body=%s", rec.Code, node.decodeCalls, node.broadcastCalls, rec.Body.String())
	}
}

func TestBroadcastRejectsExpectedTxIDMismatchBeforeStateOrBroadcast(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	expected := strings.Repeat("c", 64)
	node.decodedTxID = strings.Repeat("d", 64)
	node.broadcastTxID = expected
	node.transactions[expected] = domain.Transaction{TxID: expected, State: "mempool"}
	body := `{"raw_tx_hex":"00","expected_txid":"` + expected + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-mismatch"}
	first := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if first.Code != http.StatusUnprocessableEntity || !strings.Contains(first.Body.String(), "expected_txid_mismatch") || node.broadcastCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", first.Code, node.broadcastCalls, first.Body.String())
	}
	delete(node.transactions, expected)
	node.decodedTxID = expected
	second := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if second.Code != http.StatusAccepted || node.broadcastCalls != 1 || node.decodeCalls != 2 {
		t.Fatalf("status=%d broadcasts=%d decodes=%d body=%s", second.Code, node.broadcastCalls, node.decodeCalls, second.Body.String())
	}
}

func TestBroadcastIdempotencyIsScopedToPrincipal(t *testing.T) {
	cfg := testConfig(domain.Mainnet)
	tokenA := strings.Repeat("a", 24)
	tokenB := strings.Repeat("b", 24)
	hashA := sha256.Sum256([]byte(tokenA))
	hashB := sha256.Sum256([]byte(tokenB))
	cfg.Credentials = []config.Credential{
		{Name: "broadcaster-a", TokenHash: hashA, Scopes: []string{"broadcast"}, Wallets: []string{"hot"}},
		{Name: "broadcaster-b", TokenHash: hashB, Scopes: []string{"broadcast"}, Wallets: []string{"hot"}},
	}
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	key := "shared-upstream-withdrawal-id"
	txidA := strings.Repeat("a", 64)
	txidB := strings.Repeat("b", 64)

	node.decodedTxID, node.broadcastTxID = txidA, txidA
	bodyA := `{"raw_tx_hex":"00","expected_txid":"` + txidA + `"}`
	headersA := map[string]string{"Authorization": "Bearer " + tokenA, "Idempotency-Key": key}
	if rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", bodyA, headersA); rec.Code != http.StatusAccepted {
		t.Fatalf("principal A status=%d body=%s", rec.Code, rec.Body.String())
	}

	node.decodedTxID, node.broadcastTxID = txidB, txidB
	bodyB := `{"raw_tx_hex":"01","expected_txid":"` + txidB + `"}`
	headersB := map[string]string{"Authorization": "Bearer " + tokenB, "Idempotency-Key": key}
	if rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", bodyB, headersB); rec.Code != http.StatusAccepted {
		t.Fatalf("principal B status=%d body=%s", rec.Code, rec.Body.String())
	}
	if node.broadcastCalls != 2 {
		t.Fatalf("broadcast calls=%d", node.broadcastCalls)
	}
	if rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", bodyB, headersA); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Fatalf("same-principal conflict status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWalletTransactionEffectsPaginateAndEnforceCap(t *testing.T) {
	txid := strings.Repeat("a", 64)
	t.Run("all pages", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		service, node, scanner := newTestAPI(t, cfg)
		node.transactions[txid] = domain.Transaction{TxID: txid, State: "confirmed", Confirmations: 1}
		first := make([]domain.ScannerEvent, 1000)
		for i := range first {
			first[i] = domain.ScannerEvent{ID: int64(i + 1), Kind: "DepositEvent", Payload: walletEffectPayload("hot", txid)}
		}
		scanner.eventPages = map[int64]domain.EventsPage{
			0:    {Events: first, NextCursor: 1000, EventEpoch: scanner.health.EventEpoch},
			1000: {Events: []domain.ScannerEvent{{ID: 1001, Kind: "DepositConfirmed", Payload: walletEffectPayload("hot", txid)}}, NextCursor: 1001, EventEpoch: scanner.health.EventEpoch},
		}
		rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				WalletEffects []domain.ScannerEvent `json:"wallet_effects"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Data.WalletEffects) != 1001 {
			t.Fatalf("effects=%d err=%v", len(out.Data.WalletEffects), err)
		}
	})
	t.Run("configured cap", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		cfg.WalletEffectsMaxEvents = 2
		service, node, scanner := newTestAPI(t, cfg)
		node.transactions[txid] = domain.Transaction{TxID: txid, State: "confirmed", Confirmations: 1}
		payload := walletEffectPayload("hot", txid)
		scanner.eventPages = map[int64]domain.EventsPage{0: {Events: []domain.ScannerEvent{{ID: 1, Payload: payload}, {ID: 2, Payload: payload}, {ID: 3, Payload: payload}}, NextCursor: 3, EventEpoch: scanner.health.EventEpoch}}
		rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "wallet_effects_limit_exceeded") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestWalletTransactionEffectsRejectMismatchedIdentity(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for name, payload := range map[string]json.RawMessage{
		"wallet": walletEffectPayload("other", txid),
		"txid":   walletEffectPayload("hot", strings.Repeat("b", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, node, scanner := newTestAPI(t, cfg)
			node.transactions[txid] = domain.Transaction{TxID: txid, State: "confirmed", Confirmations: 1}
			scanner.eventPages = map[int64]domain.EventsPage{0: {Events: []domain.ScannerEvent{{ID: 1, Kind: "DepositEvent", Payload: payload}}, NextCursor: 1, EventEpoch: scanner.health.EventEpoch}}
			rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWalletTransactionUsesLatestTerminalScannerState(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for name, test := range map[string]struct {
		kind       string
		payload    string
		state      string
		extraCheck string
	}{
		"orphaned": {
			kind:    "DepositOrphaned",
			payload: `{"wallet_id":"hot","txid":"` + txid + `","height":90,"status":{"state":"orphaned"}}`,
			state:   "orphaned",
		},
		"expired": {
			kind:       "OutgoingOutputExpired",
			payload:    `{"wallet_id":"hot","txid":"` + txid + `","expiry_height":90,"status":{"state":"expired"}}`,
			state:      "expired",
			extraCheck: `"expiry_height":90`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			scanner.events = domain.EventsPage{
				Events:     []domain.ScannerEvent{{ID: 1, Kind: test.kind, Payload: json.RawMessage(test.payload)}},
				NextCursor: 1,
				EventEpoch: scanner.health.EventEpoch,
			}
			rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"`+test.state+`"`) || (test.extraCheck != "" && !strings.Contains(rec.Body.String(), test.extraCheck)) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWalletTransactionTerminalFallbackRequiresFinalValidLifecycleEvent(t *testing.T) {
	txid := strings.Repeat("a", 64)
	expired := json.RawMessage(`{"wallet_id":"hot","txid":"` + txid + `","expiry_height":90,"status":{"state":"expired"}}`)
	t.Run("later nonterminal event wins", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		service, _, scanner := newTestAPI(t, cfg)
		scanner.events = domain.EventsPage{
			Events: []domain.ScannerEvent{
				{ID: 1, Kind: "OutgoingOutputExpired", Payload: expired},
				{ID: 2, Kind: "OutgoingOutputEvent", Payload: json.RawMessage(`{"wallet_id":"hot","txid":"` + txid + `","status":{"state":"mempool"}}`)},
			},
			NextCursor: 2,
			EventEpoch: scanner.health.EventEpoch,
		}
		rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("terminal payload must agree", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		service, _, scanner := newTestAPI(t, cfg)
		scanner.events = domain.EventsPage{
			Events:     []domain.ScannerEvent{{ID: 1, Kind: "OutgoingOutputExpired", Payload: json.RawMessage(`{"wallet_id":"hot","txid":"` + txid + `","expiry_height":90,"status":{"state":"mempool"}}`)}},
			NextCursor: 1,
			EventEpoch: scanner.health.EventEpoch,
		}
		rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestRejectedBroadcastIsPersistedForIdempotentReplay(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("d", 64)
	node.broadcastErr = &domain.UpstreamError{Kind: "rejected", Err: io.EOF}
	node.decodedTxID = txid
	body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-rejected"}
	first := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	second := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if first.Code != http.StatusUnprocessableEntity || second.Code != http.StatusUnprocessableEntity || node.broadcastCalls != 1 {
		t.Fatalf("first=%d second=%d calls=%d", first.Code, second.Code, node.broadcastCalls)
	}
}

func TestAlreadyKnownBroadcastIsSuccessfulAndPersisted(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("e", 64)
	node.broadcastErr = &domain.UpstreamError{Kind: "already_known", Err: io.EOF}
	node.decodedTxID = txid
	body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-already-known"}
	first := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	second := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || node.broadcastCalls != 1 {
		t.Fatalf("first=%d second=%d calls=%d first_body=%s", first.Code, second.Code, node.broadcastCalls, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"state":"known"`) || !strings.Contains(first.Body.String(), `"accepted":false`) || !strings.Contains(first.Body.String(), `"already_known":true`) {
		t.Fatalf("body=%s", first.Body.String())
	}
}

func TestAmbiguousBroadcastFailuresRemainRetryable(t *testing.T) {
	for _, kind := range []string{"uncertain", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			cfg.IdempotencyLease = 0
			service, node, _ := newTestAPI(t, cfg)
			txid := strings.Repeat("f", 64)
			node.decodedTxID = txid
			node.transactionErr = &domain.UpstreamError{Kind: "unavailable", Err: io.EOF}
			node.broadcastErr = &domain.UpstreamError{Kind: kind, Err: io.EOF}
			body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
			headers := map[string]string{"Idempotency-Key": "withdrawal-retryable-" + kind}
			for attempt := 0; attempt < 2; attempt++ {
				rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, headers)
				if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"retryable":true`) || strings.Contains(rec.Body.String(), "transaction_rejected") {
					t.Fatalf("attempt=%d status=%d body=%s", attempt, rec.Code, rec.Body.String())
				}
			}
			if node.broadcastCalls != 2 {
				t.Fatalf("broadcast calls=%d", node.broadcastCalls)
			}
		})
	}
}

func TestOversizedBroadcastRejectedBeforeDecode(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	cfg.BroadcastBodyBytes = 16
	service, node, _ := newTestAPI(t, cfg)
	rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", `{"raw_tx_hex":"0011223344556677"}`, map[string]string{"Idempotency-Key": "large"})
	if rec.Code != http.StatusRequestEntityTooLarge || node.broadcastCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
	}
}

func TestProductionAuthProtectsVersionAndWalletAuthorization(t *testing.T) {
	cfg := testConfig(domain.Mainnet)
	cfg.Wallets = append(cfg.Wallets, config.Wallet{WalletID: "cold", UFVK: "jview1cold"})
	token := "012345678901234567890123"
	hash := sha256.Sum256([]byte(token))
	cfg.Credentials = []config.Credential{{Name: "exchange", TokenHash: hash, Scopes: []string{"read"}, Wallets: []string{"hot"}}}
	service, _, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	if rec := request(t, handler, http.MethodGet, "/v1/health/live", ``, nil); rec.Code != http.StatusOK {
		t.Fatalf("liveness=%d", rec.Code)
	}
	if rec := request(t, handler, http.MethodGet, "/v1/version", ``, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("version without auth=%d", rec.Code)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	if rec := request(t, handler, http.MethodGet, "/v1/version", ``, headers); rec.Code != http.StatusOK {
		t.Fatalf("version auth=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(t, handler, http.MethodGet, "/v1/wallets/cold/deposits", ``, headers); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-wallet=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(t, handler, http.MethodPost, "/v1/wallets/hot/addresses", `{"label":"x"}`, headers); rec.Code != http.StatusForbidden {
		t.Fatalf("read token allocated address status=%d", rec.Code)
	}
}

func TestWrongMethodUsesAuthenticatedJSONEnvelope(t *testing.T) {
	cfg := testConfig(domain.Mainnet)
	token := strings.Repeat("a", 24)
	hash := sha256.Sum256([]byte(token))
	cfg.Credentials = []config.Credential{{Name: "exchange", TokenHash: hash, Scopes: []string{"read"}, Wallets: []string{"hot"}}}
	service, _, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	if rec := request(t, handler, http.MethodPost, "/v1/network/tip", ``, nil); rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unauthenticated status=%d content_type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec := request(t, handler, http.MethodPost, "/v1/network/tip", ``, map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(rec.Body.String(), `"code":"method_not_allowed"`) || rec.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status=%d content_type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestReadinessFailsClosedOnNetworkAndLag(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	scanner.health.UAHRP = "j"
	rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	scanner.health.UAHRP = "jregtest"
	lag := int64(3)
	scanner.health.ScannerLag = &lag
	scanned := int64(97)
	scanner.health.ScannedHeight = &scanned
	scanner.health.ScannedHash = strings.Repeat("d", 64)
	rec = request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("lag status=%d", rec.Code)
	}
}

func TestReadinessFailsClosedOnScannerConfirmationPolicy(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()

	scanner.health.Confirmations = nil
	if rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
		t.Fatalf("missing policy status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, value := range []int64{0, cfg.DefaultConfirmations + 1} {
		scanner.health.Confirmations = &value
		if rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
			t.Fatalf("policy=%d status=%d body=%s", value, rec.Code, rec.Body.String())
		}
	}
}

func TestReadyNodeRejectsInvalidTipInvariants(t *testing.T) {
	cases := map[string]func(*domain.NodeTip){
		"uppercase hash":        func(tip *domain.NodeTip) { tip.Hash = strings.Repeat("A", 64) },
		"short hash":            func(tip *domain.NodeTip) { tip.Hash = "abc" },
		"headers behind blocks": func(tip *domain.NodeTip) { tip.Headers = tip.Height - 1 },
		"negative progress":     func(tip *domain.NodeTip) { tip.VerificationProgress = -0.01 },
		"excess progress":       func(tip *domain.NodeTip) { tip.VerificationProgress = 1.01 },
		"nan progress":          func(tip *domain.NodeTip) { tip.VerificationProgress = math.NaN() },
		"infinite progress":     func(tip *domain.NodeTip) { tip.VerificationProgress = math.Inf(1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, node, _ := newTestAPI(t, cfg)
			mutate(&node.tip)
			if _, err := service.readyNode(context.Background()); err == nil {
				t.Fatal("expected invalid node tip rejection")
			}
		})
	}
}

func TestNetworkTipRejectsInvalidStructureButReportsInitialSync(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	node.tip.Headers = node.tip.Height - 1
	if rec := request(t, handler, http.MethodGet, "/v1/network/tip", ``, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "node_not_ready") {
		t.Fatalf("invalid tip status=%d body=%s", rec.Code, rec.Body.String())
	}
	node.tip.Headers = node.tip.Height
	node.tip.InitialBlockDownload = true
	if rec := request(t, handler, http.MethodGet, "/v1/network/tip", ``, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"initial_sync":true`) {
		t.Fatalf("syncing tip status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReadinessUsesNodeLagAndMatchesScannedBlockHash(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	bogusLag := int64(999999)
	scanner.health.ScannerLag = &bogusLag
	if rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil); rec.Code != http.StatusOK {
		t.Fatalf("scanner-reported lag affected readiness: status=%d body=%s", rec.Code, rec.Body.String())
	}
	scanner.health.ScannedHash = strings.Repeat("a", 64)
	if rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
		t.Fatalf("hash mismatch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if node.blockHashCalls < 2 {
		t.Fatalf("getblockhash calls=%d", node.blockHashCalls)
	}
}

func TestInvalidScannerBalanceFailsClosed(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	address := allocate(t, handler)
	scanner.balance = domain.Balance{WalletID: "other", RecipientAddress: address, AvailableZat: 42, TotalUnspentZat: 41, MinConfirmations: 100, AsOfNodeHeight: 100, AsOfScannerHeight: 100}
	if rec := request(t, handler, http.MethodGet, "/v1/wallets/hot/addresses/"+address+"/balance", ``, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayStateLossWithRetainedScannerFailsStartup(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	store, err := storagepkg.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "fresh-gateway.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &fakeScanner{backfillStatuses: map[string]domain.BackfillStatus{"hot": {WalletID: "hot", BirthdayHeight: 0, NextHeight: 101, State: "complete"}}}
	node := &fakeNode{}
	_, err = New(cfg, store, node, scanner, fakeDeriver{network: domain.Regtest}, slog.New(slog.NewTextHandler(io.Discard, nil)), BuildInfo{})
	if err == nil || !strings.Contains(err.Error(), "restore the gateway backup") {
		t.Fatalf("err=%v", err)
	}
	if _, found, lookupErr := store.Wallet(context.Background(), "hot"); lookupErr != nil || found {
		t.Fatalf("wallet was recreated: found=%v err=%v", found, lookupErr)
	}
}

func TestLegacyGatewayWalletMigrationRequiresMatchingScannerFingerprint(t *testing.T) {
	for _, match := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching", false: "mismatched"}[match], func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			store, err := storagepkg.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "legacy-gateway.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			wallet := cfg.Wallets[0]
			if err := store.EnsureWallet(context.Background(), wallet.WalletID, string(cfg.Network), wallet.UFVKFingerprint(), wallet.BirthdayHeight); err != nil {
				t.Fatal(err)
			}
			fingerprint := wallet.UFVKFingerprint()
			if !match {
				fingerprint = strings.Repeat("f", 64)
			}
			scanner := &fakeScanner{backfillStatuses: map[string]domain.BackfillStatus{"hot": {WalletID: "hot", UFVKFingerprint: fingerprint, BirthdayHeight: 0, NextHeight: 1, State: "complete"}}}
			_, err = New(cfg, legacyWalletStore{Store: store}, &fakeNode{}, scanner, fakeDeriver{network: domain.Regtest}, slog.New(slog.NewTextHandler(io.Discard, nil)), BuildInfo{})
			if match && err != nil {
				t.Fatal(err)
			}
			if !match && err == nil {
				t.Fatal("expected mismatched legacy binding rejection")
			}
		})
	}
}

func TestBirthdayBackfillProgressIsRequiredAndPersisted(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	cfg.Wallets[0].BirthdayHeight = 50
	store, err := storagepkg.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "backfill.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &fakeScanner{}
	registry, err := newWalletRegistry(cfg, store, scanner, fakeDeriver{network: domain.Regtest})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBackfillProgress(context.Background(), "hot", 101); err != nil {
		t.Fatal(err)
	}
	complete, err := registry.completeThrough(context.Background(), 100)
	if err != nil || complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	rewound, ok, err := store.Wallet(context.Background(), "hot")
	if err != nil || !ok || rewound.NextBackfillHeight != 50 {
		t.Fatalf("rewound=%+v ok=%v err=%v", rewound, ok, err)
	}
	worked, err := registry.backfillOne(context.Background(), 100, 10000)
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if scanner.backfillFrom != 50 || scanner.backfillTo != 100 || scanner.backfillBatch != 51 {
		t.Fatalf("backfill from=%d to=%d batch=%d", scanner.backfillFrom, scanner.backfillTo, scanner.backfillBatch)
	}
	complete, err = registry.completeThrough(context.Background(), 100)
	if err != nil || !complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	wallet, ok, err := store.Wallet(context.Background(), "hot")
	if err != nil || !ok || wallet.NextBackfillHeight != 101 {
		t.Fatalf("wallet=%+v ok=%v err=%v", wallet, ok, err)
	}
}

func TestBackfillChoosesDeepestLagDeterministicallyAndCapsBatch(t *testing.T) {
	for name, nextHeights := range map[string]map[string]int64{
		"deepest": {"hot": 90, "cold": 10},
		"tie":     {"hot": 10, "cold": 10},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			cfg.Wallets = append(cfg.Wallets, config.Wallet{WalletID: "cold", UFVK: "jviewregtest1cold", BirthdayHeight: 0})
			store, err := storagepkg.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "adaptive-backfill.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			scanner := &fakeScanner{backfillStatuses: map[string]domain.BackfillStatus{}}
			for _, wallet := range cfg.Wallets {
				if err := store.EnsureWallet(context.Background(), wallet.WalletID, string(cfg.Network), wallet.UFVKFingerprint(), wallet.BirthdayHeight); err != nil {
					t.Fatal(err)
				}
				scanner.backfillStatuses[wallet.WalletID] = domain.BackfillStatus{WalletID: wallet.WalletID, UFVKFingerprint: wallet.UFVKFingerprint(), BirthdayHeight: wallet.BirthdayHeight, NextHeight: nextHeights[wallet.WalletID], State: "running"}
			}
			registry, err := newWalletRegistry(cfg, store, scanner, fakeDeriver{network: cfg.Network})
			if err != nil {
				t.Fatal(err)
			}
			worked, err := registry.backfillOne(context.Background(), 100, 10000)
			if err != nil || !worked {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			if scanner.backfillWallet != "cold" || scanner.backfillBatch != 91 {
				t.Fatalf("wallet=%s batch=%d", scanner.backfillWallet, scanner.backfillBatch)
			}
		})
	}
}
