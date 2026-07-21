package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/config"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
	storagepkg "github.com/Abdullah1738/juno-exchange-gateway/internal/storage/sqlite"
)

type fakeNode struct {
	mu             sync.Mutex
	tip            domain.NodeTip
	transactions   map[string]domain.Transaction
	broadcastTxID  string
	broadcastErr   error
	broadcastCalls int
}

func (f *fakeNode) Tip(context.Context) (domain.NodeTip, error) { return f.tip, nil }
func (f *fakeNode) Transaction(_ context.Context, txid string, _ bool) (domain.Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	lastCursor        int64
	backfillFrom      int64
	backfillTo        int64
	backfillBatch     int64
	backfillCalls     int
	backfillStatuses  map[string]domain.BackfillStatus
}

func (f *fakeScanner) Health(context.Context) (domain.ScannerHealth, error) { return f.health, nil }
func (f *fakeScanner) UpsertWallet(_ context.Context, walletID, _ string, birthday int64) error {
	if f.backfillStatuses == nil {
		f.backfillStatuses = map[string]domain.BackfillStatus{}
	}
	if _, ok := f.backfillStatuses[walletID]; !ok {
		f.backfillStatuses[walletID] = domain.BackfillStatus{WalletID: walletID, BirthdayHeight: birthday, NextHeight: birthday, State: "pending"}
	}
	return nil
}
func (f *fakeScanner) BackfillStatus(_ context.Context, walletID string) (domain.BackfillStatus, bool, error) {
	status, ok := f.backfillStatuses[walletID]
	return status, ok, nil
}
func (f *fakeScanner) Backfill(_ context.Context, walletID string, toHeight, batchSize int64) (int64, error) {
	f.backfillCalls++
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
func (f *fakeScanner) Events(_ context.Context, _ string, cursor int64, _ int, _ domain.EventFilter) (domain.EventsPage, error) {
	f.lastCursor = cursor
	return f.events, nil
}

type fakeDeriver struct{ network domain.Network }

func (f fakeDeriver) Derive(_ context.Context, _ string, index uint32) (string, error) {
	return f.network.AddressHRP() + "1allocated" + string(rune('a'+index)), nil
}

func testConfig(network domain.Network) config.Config {
	ready := true
	scanned := int64(100)
	lag := int64(0)
	_ = ready
	_ = scanned
	_ = lag
	ufvk := map[domain.Network]string{domain.Regtest: "jviewregtest1example", domain.Testnet: "jviewtest1example", domain.Mainnet: "jview1example"}[network]
	return config.Config{Network: network, ListenAddress: ":0", StateDSN: "unused", NodeRPCURL: "http://node", ScannerURL: "http://scanner", AddrgenPath: "addrgen", Wallets: []config.Wallet{{WalletID: "hot", UFVK: ufvk}}, DefaultConfirmations: 100, MaxConfirmations: 10000, MaxScannerLag: 2, RequireCompleteHistory: true, JSONBodyBytes: 1 << 20, BroadcastBodyBytes: 4 << 20, ReadTimeout: time.Second, BroadcastTimeout: time.Second, UpstreamTimeout: time.Second, ShutdownTimeout: time.Second, IdempotencyLease: time.Minute, ReadRate: config.RateLimit{RPS: 1000, Burst: 1000}, BroadcastRate: config.RateLimit{RPS: 1000, Burst: 1000}}
}

func newTestAPI(t *testing.T, cfg config.Config) (*API, *fakeNode, *fakeScanner) {
	t.Helper()
	store, err := storagepkg.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "state.db")+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := &fakeNode{tip: domain.NodeTip{Network: cfg.Network.NodeChain(), Height: 100, Hash: strings.Repeat("b", 64), Headers: 100, VerificationProgress: 1}, transactions: map[string]domain.Transaction{}}
	ready := true
	scanned := int64(100)
	lag := int64(0)
	scanner := &fakeScanner{health: domain.ScannerHealth{Status: "ok", Network: string(cfg.Network), UAHRP: cfg.Network.AddressHRP(), Ready: &ready, ScannedHeight: &scanned, ScannerLag: &lag}, balanceFound: true, backfillStatuses: map[string]domain.BackfillStatus{}}
	service, err := New(cfg, store, node, scanner, fakeDeriver{network: cfg.Network}, slog.New(slog.NewTextHandler(io.Discard, nil)), BuildInfo{Version: "test", Revision: "abc", APIVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.registry.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, configuredWallet := range cfg.Wallets {
		scanner.backfillStatuses[configuredWallet.WalletID] = domain.BackfillStatus{WalletID: configuredWallet.WalletID, BirthdayHeight: configuredWallet.BirthdayHeight, NextHeight: 101, TargetHeight: 100, State: "complete"}
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

func TestBalanceRequiresAllocatedAddressAndUsesDefaultConfirmations(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	address := allocate(t, handler)
	scanner.balance = domain.Balance{AvailableZat: 42, TotalUnspentZat: 42, MinConfirmations: 100, AsOfNodeHeight: 100, AsOfScannerHeight: 100}
	rec := request(t, handler, http.MethodGet, "/v1/wallets/hot/addresses/"+address+"/balance", ``, nil)
	if rec.Code != http.StatusOK || scanner.lastConfirmations != 100 {
		t.Fatalf("status=%d confirmations=%d body=%s", rec.Code, scanner.lastConfirmations, rec.Body.String())
	}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/hot/addresses/jregtest1notallocated/balance", ``, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if scanner.balanceCalls != 1 {
		t.Fatalf("unowned address reached scanner")
	}
}

func TestDepositsUseOpaqueWalletBoundCursor(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("a", 64)
	payload, _ := json.Marshal(depositPayload{WalletID: "hot", TxID: txid, Height: 90, ActionIndex: 2, AmountZatoshis: 99, RecipientAddress: "jregtest1recipient"})
	scanner.events = domain.EventsPage{Events: []domain.ScannerEvent{{ID: 7, Kind: "DepositEvent", Height: 90, Payload: payload, CreatedAt: time.Unix(1, 0).UTC()}}, NextCursor: 7}
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
	if len(out.Data.Deposits) != 1 || out.Data.Deposits[0].DepositID != "hot:"+txid+":2" || !strings.Contains(out.Data.NextCursor, ".") {
		t.Fatalf("response=%+v", out.Data)
	}
	scanner.events = domain.EventsPage{NextCursor: 7}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/hot/deposits?cursor="+out.Data.NextCursor, ``, nil)
	if rec.Code != http.StatusOK || scanner.lastCursor != 7 {
		t.Fatalf("status=%d cursor=%d body=%s", rec.Code, scanner.lastCursor, rec.Body.String())
	}
	rec = request(t, handler, http.MethodGet, "/v1/wallets/other/deposits?cursor="+out.Data.NextCursor, ``, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestBroadcastIsSignedRawOnlyAndIdempotent(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("c", 64)
	node.broadcastTxID = txid
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
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", `{"raw_tx_hex":"01","expected_txid":"`+txid+`"}`, headers)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", `{"raw_tx_hex":"00","expected_txid":"`+txid+`","seed":"secret"}`, map[string]string{"Idempotency-Key": "withdrawal-2"})
	if rec.Code != http.StatusBadRequest || node.broadcastCalls != 1 {
		t.Fatalf("secret field accepted status=%d", rec.Code)
	}
}

func TestRejectedBroadcastIsPersistedForIdempotentReplay(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("d", 64)
	node.broadcastErr = &domain.UpstreamError{Kind: "rejected", Err: io.EOF}
	body := `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-rejected"}
	first := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	second := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if first.Code != http.StatusUnprocessableEntity || second.Code != http.StatusUnprocessableEntity || node.broadcastCalls != 1 {
		t.Fatalf("first=%d second=%d calls=%d", first.Code, second.Code, node.broadcastCalls)
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
	rec = request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("lag status=%d", rec.Code)
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
	if scanner.backfillFrom != 50 || scanner.backfillTo != 100 || scanner.backfillBatch != 10000 {
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
