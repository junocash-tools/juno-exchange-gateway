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
	health             domain.ScannerHealth
	healthSequence     []domain.ScannerHealth
	healthCalls        int
	balance            domain.Balance
	balanceFound       bool
	balanceCalls       int
	lastConfirmations  int64
	events             domain.EventsPage
	eventPages         map[int64]domain.EventsPage
	noteSummary        domain.WalletNoteSummary
	noteSummaryFound   bool
	noteSummaryErr     error
	noteSummaryMinConf int64
	noteSummaryMinZat  int64
	noteSummaryMax     int
	lastCursor         int64
	lastEventLimit     int
	backfillFrom       int64
	backfillTo         int64
	backfillBatch      int64
	backfillWallet     string
	backfillCalls      int
	backfillStatuses   map[string]domain.BackfillStatus
}

func (f *fakeScanner) Health(context.Context) (domain.ScannerHealth, error) {
	if len(f.healthSequence) == 0 {
		return f.health, nil
	}
	index := f.healthCalls
	if index >= len(f.healthSequence) {
		index = len(f.healthSequence) - 1
	}
	f.healthCalls++
	return f.healthSequence[index], nil
}
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
func (f *fakeScanner) NoteSummary(_ context.Context, walletID string, minConf, minNoteZat int64, maxNotes int) (domain.WalletNoteSummary, bool, error) {
	f.noteSummaryMinConf = minConf
	f.noteSummaryMinZat = minNoteZat
	f.noteSummaryMax = maxNotes
	if f.noteSummaryErr != nil {
		return domain.WalletNoteSummary{}, false, f.noteSummaryErr
	}
	out := f.noteSummary
	if out.WalletID == "" {
		out.WalletID = walletID
		out.MinConfirmations = minConf
		out.MinNoteZat = minNoteZat
		if f.health.ScannedHeight != nil {
			out.AsOfScannerHeight = *f.health.ScannedHeight
		}
		out.AsOfScannerHash = f.health.ScannedHash
	}
	return out, f.noteSummaryFound, nil
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
	return config.Config{Network: network, ListenAddress: ":0", StateDSN: "unused", NodeRPCURL: "http://node", ScannerURL: "http://scanner", AddrgenPath: "addrgen", Wallets: []config.Wallet{{WalletID: "hot", UFVK: ufvk}}, DefaultConfirmations: 100, MaxConfirmations: 10000, MaxScannerLag: 2, RequireCompleteHistory: true, JSONBodyBytes: 1 << 20, BroadcastBodyBytes: 4 << 20, ReadTimeout: time.Second, BroadcastTimeout: time.Second, UpstreamTimeout: time.Second, ShutdownTimeout: time.Second, IdempotencyLease: time.Minute, WalletEffectsMaxEvents: 10000, NoteSummaryMaxNotes: 100000, ReadRate: config.RateLimit{RPS: 1000, Burst: 1000}, BroadcastRate: config.RateLimit{RPS: 1000, Burst: 1000}}
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
	historyComplete := true
	scanned := int64(100)
	lag := int64(0)
	confirmations := cfg.DefaultConfirmations
	scanner := &fakeScanner{health: domain.ScannerHealth{Status: "ok", Network: string(cfg.Network), UAHRP: cfg.Network.AddressHRP(), Confirmations: &confirmations, EventEpoch: strings.Repeat("e", 64), Ready: &ready, ScannedHeight: &scanned, ScannedHash: node.tip.Hash, ScannerLag: &lag, HistoryComplete: &historyComplete}, balanceFound: true, noteSummaryFound: true, backfillStatuses: map[string]domain.BackfillStatus{}}
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

func depositEffectPayload(kind, walletID, txid, address string, eventHeight int64, actionIndex uint32, amount uint64) json.RawMessage {
	blockHeight := eventHeight
	confirmations := int64(1)
	payload := map[string]any{
		"version": "v1", "wallet_id": walletID, "txid": txid, "origin": "external",
		"height": blockHeight, "action_index": actionIndex, "amount_zatoshis": amount,
		"recipient_address": address, "diversifier_index": uint32(0),
		"status": map[string]any{"state": "confirmed", "height": blockHeight, "confirmations": confirmations},
	}
	switch kind {
	case "DepositConfirmed":
		blockHeight = eventHeight - 99
		payload["height"] = blockHeight
		payload["confirmed_height"] = eventHeight
		payload["required_confirmations"] = int64(100)
		payload["status"] = map[string]any{"state": "confirmed", "height": blockHeight, "confirmations": int64(100)}
	case "DepositUnconfirmed":
		blockHeight = eventHeight - 98
		payload["height"] = blockHeight
		payload["rollback_height"] = eventHeight
		payload["previous_confirmed_height"] = eventHeight + 1
		payload["status"] = map[string]any{"state": "confirmed", "height": blockHeight, "confirmations": int64(99)}
	case "DepositOrphaned":
		blockHeight = eventHeight + 1
		payload["height"] = blockHeight
		payload["orphaned_at_height"] = eventHeight
		payload["status"] = map[string]any{"state": "orphaned", "height": blockHeight, "confirmations": int64(0)}
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func walletEffectPayload(kind, walletID, txid string, eventHeight int64) json.RawMessage {
	if strings.HasPrefix(kind, "Deposit") {
		return depositEffectPayload(kind, walletID, txid, "jregtest1effect", eventHeight, 1, 10)
	}
	payload := map[string]any{
		"version": "v1", "wallet_id": walletID, "txid": txid,
		"action_index": uint32(1), "amount_zatoshis": uint64(10),
		"recipient_address": "jregtest1recipient", "ovk_scope": "external",
		"status": map[string]any{"state": "mempool"},
	}
	if strings.HasPrefix(kind, "Spend") {
		payload = map[string]any{
			"version": "v1", "wallet_id": walletID, "txid": txid, "height": eventHeight,
			"note_txid": strings.Repeat("d", 64), "note_action_index": uint32(0), "note_height": int64(0),
			"amount_zatoshis": uint64(10), "note_nullifier": strings.Repeat("f", 64),
			"status": map[string]any{"state": "confirmed", "height": eventHeight, "confirmations": int64(1)},
		}
	}
	switch kind {
	case "SpendConfirmed", "OutgoingOutputConfirmed":
		blockHeight := eventHeight - 99
		payload["height"] = blockHeight
		payload["confirmed_height"] = eventHeight
		payload["required_confirmations"] = int64(100)
		payload["status"] = map[string]any{"state": "confirmed", "height": blockHeight, "confirmations": int64(100)}
	case "SpendUnconfirmed", "OutgoingOutputUnconfirmed":
		blockHeight := eventHeight - 98
		payload["height"] = blockHeight
		payload["rollback_height"] = eventHeight
		payload["previous_confirmed_height"] = eventHeight + 1
		payload["status"] = map[string]any{"state": "confirmed", "height": blockHeight, "confirmations": int64(99)}
	case "SpendOrphaned", "OutgoingOutputOrphaned":
		blockHeight := eventHeight + 1
		payload["height"] = blockHeight
		payload["orphaned_at_height"] = eventHeight
		payload["status"] = map[string]any{"state": "orphaned", "height": blockHeight, "confirmations": int64(0)}
	case "OutgoingOutputExpired":
		payload["expiry_height"] = eventHeight - 1
		payload["status"] = map[string]any{"state": "expired"}
	}
	raw, _ := json.Marshal(payload)
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

func TestNoteSummaryReturnsAtomicScannerAggregate(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	expiry := int64(140)
	smallest, largest := int64(500), int64(500)
	scanner.noteSummary = domain.WalletNoteSummary{
		WalletID: "hot", MinConfirmations: 10, MinNoteZat: 100, AsOfScannerHeight: 100,
		AsOfScannerHash:    strings.Repeat("b", 64),
		TotalUnspent:       domain.NoteValueSummary{NoteCount: 5, ValueZat: 960},
		Spendable:          domain.SpendableNoteSummary{NoteValueSummary: domain.NoteValueSummary{NoteCount: 1, ValueZat: 500}, SmallestNoteZat: &smallest, LargestNoteZat: &largest},
		Immature:           domain.NoteValueSummary{NoteCount: 1, ValueZat: 100},
		PendingSpend:       domain.PendingSpendNoteSummary{NoteValueSummary: domain.NoteValueSummary{NoteCount: 1, ValueZat: 300}, KnownExpiryCount: 1, NextExpiryHeight: &expiry, LastExpiryHeight: &expiry},
		BelowMinNote:       domain.NoteValueSummary{NoteCount: 1, ValueZat: 50},
		WitnessUnavailable: domain.NoteValueSummary{NoteCount: 1, ValueZat: 10},
	}
	rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/notes/summary?min_confirmations=10&min_note_zat=100", ``, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data noteSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.Data
	if got.TotalUnspent.NoteCount != 5 || got.TotalUnspent.ValueZat != 960 ||
		got.Spendable.NoteCount != 1 || got.Spendable.ValueZat != 500 || got.Spendable.SmallestNoteZat == nil || *got.Spendable.SmallestNoteZat != 500 ||
		got.BelowMinNote.NoteCount != 1 || got.Immature.NoteCount != 1 || got.PendingSpend.NoteCount != 1 || got.PendingSpend.NextExpiryHeight == nil || *got.PendingSpend.NextExpiryHeight != 140 ||
		got.WitnessUnavailable.NoteCount != 1 || got.MinConfirmations != 10 || got.MinNoteZat != 100 || got.AsOfScannerHeight != 100 || got.AsOfScannerHash != strings.Repeat("b", 64) {
		t.Fatalf("summary=%+v", got)
	}
	if scanner.noteSummaryMinConf != 10 || scanner.noteSummaryMinZat != 100 || scanner.noteSummaryMax != cfg.NoteSummaryMaxNotes {
		t.Fatalf("summary request minconf=%d minzat=%d max=%d", scanner.noteSummaryMinConf, scanner.noteSummaryMinZat, scanner.noteSummaryMax)
	}
}

func TestNoteSummaryRequiresTreasuryScopeAndFailsClosedAtCap(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	readToken := "read-token-that-is-at-least-24-bytes"
	treasuryToken := "treasury-token-at-least-24-bytes"
	cfg.Credentials = []config.Credential{
		{Name: "reader", TokenHash: sha256.Sum256([]byte(readToken)), Scopes: []string{"read"}, Wallets: []string{"hot"}},
		{Name: "treasury", TokenHash: sha256.Sum256([]byte(treasuryToken)), Scopes: []string{"treasury"}, Wallets: []string{"hot"}},
	}
	cfg.NoteSummaryMaxNotes = 1
	service, _, scanner := newTestAPI(t, cfg)
	scanner.noteSummaryErr = domain.ErrNoteSummaryLimitExceeded
	rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/notes/summary", ``, map[string]string{"Authorization": "Bearer " + readToken})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/notes/summary", ``, map[string]string{"Authorization": "Bearer " + treasuryToken})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "note_summary_limit_exceeded") {
		t.Fatalf("cap status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNoteSummaryRejectsDifferentScannerSnapshot(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	scanner.noteSummary = domain.WalletNoteSummary{
		WalletID: "hot", MinConfirmations: 100, AsOfScannerHeight: 99,
		AsOfScannerHash: strings.Repeat("b", 64),
	}
	rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/notes/summary", ``, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "scanner_snapshot_changed") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNoteSummaryRejectsInvalidAggregateFromScanner(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	scanner.noteSummary = domain.WalletNoteSummary{
		WalletID: "hot", MinConfirmations: 100, AsOfScannerHeight: 100,
		AsOfScannerHash: strings.Repeat("b", 64),
		TotalUnspent:    domain.NoteValueSummary{NoteCount: 0, ValueZat: 1},
	}
	rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/notes/summary", ``, nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNoteSummaryRejectsSnapshotMutationDuringRequest(t *testing.T) {
	for _, mutation := range []string{"height", "hash", "epoch"} {
		t.Run(mutation, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			before := scanner.health
			after := before
			switch mutation {
			case "height":
				changed := *after.ScannedHeight + 1
				after.ScannedHeight = &changed
			case "hash":
				after.ScannedHash = strings.Repeat("c", 64)
			case "epoch":
				after.EventEpoch = strings.Repeat("f", 64)
			}
			scanner.noteSummary = domain.WalletNoteSummary{
				WalletID: "hot", MinConfirmations: 100, AsOfScannerHeight: *before.ScannedHeight,
				AsOfScannerHash: before.ScannedHash,
			}
			scanner.healthSequence = []domain.ScannerHealth{before, after}
			rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/notes/summary", ``, nil)
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "scanner_snapshot_changed") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDepositsUseOpaqueWalletBoundCursor(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	address := allocate(t, handler)
	txid := strings.Repeat("a", 64)
	payload := depositEffectPayload("DepositEvent", "hot", txid, address, 90, 2, 99)
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
	payload := depositEffectPayload("DepositEvent", "hot", txid, "jregtest1derivedoutsidegateway", 90, 1, 55)
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
			payload := map[string]any{
				"version": "v1", "wallet_id": "hot", "txid": txid, "height": 90, "action_index": 1,
				"amount_zatoshis": 55, "recipient_address": address, "diversifier_index": 0,
				"status": map[string]any{"state": "confirmed", "height": 90, "confirmations": 1},
			}
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

func TestDepositsFailClosedOnInconsistentLifecycle(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   string
		mutate func(map[string]any)
	}{
		{name: "detected event height", kind: "DepositEvent", mutate: func(payload map[string]any) { payload["height"] = float64(89) }},
		{name: "missing zero diversifier index", kind: "DepositEvent", mutate: func(payload map[string]any) { delete(payload, "diversifier_index") }},
		{name: "confirmed marker", kind: "DepositConfirmed", mutate: func(payload map[string]any) { payload["confirmed_height"] = float64(99) }},
		{name: "unconfirmed previous marker", kind: "DepositUnconfirmed", mutate: func(payload map[string]any) { delete(payload, "previous_confirmed_height") }},
		{name: "unconfirmed without prior finality", kind: "DepositUnconfirmed", mutate: func(payload map[string]any) {
			payload["height"] = float64(99)
			payload["previous_confirmed_height"] = float64(101)
			payload["status"] = map[string]any{"state": "confirmed", "height": float64(99), "confirmations": float64(2)}
		}},
		{name: "orphaned state", kind: "DepositOrphaned", mutate: func(payload map[string]any) { payload["status"].(map[string]any)["state"] = "confirmed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			address := allocate(t, service.Handler())
			txid := strings.Repeat("a", 64)
			eventHeight := int64(100)
			if test.kind == "DepositEvent" {
				eventHeight = 90
			}
			var payload map[string]any
			if err := json.Unmarshal(depositEffectPayload(test.kind, "hot", txid, address, eventHeight, 1, 55), &payload); err != nil {
				t.Fatal(err)
			}
			test.mutate(payload)
			raw, _ := json.Marshal(payload)
			scanner.events = domain.EventsPage{Events: []domain.ScannerEvent{{ID: 1, Kind: test.kind, Height: eventHeight, Payload: raw, CreatedAt: time.Unix(1, 0).UTC()}}, NextCursor: 1, EventEpoch: scanner.health.EventEpoch}
			rec := request(t, service.Handler(), http.MethodGet, "/v1/wallets/hot/deposits", ``, nil)
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
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	headers := map[string]string{"Idempotency-Key": "withdrawal-1"}
	rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"wallet_id":"hot"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", body, headers)
	if rec.Code != http.StatusOK || node.broadcastCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"accepted":false`) || !strings.Contains(rec.Body.String(), `"already_known":true`) {
		t.Fatalf("replay body=%s", rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", `{"wallet_id":"hot","raw_tx_hex":"01","expected_txid":"`+txid+`"}`, headers)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = request(t, handler, http.MethodPost, "/v1/transactions/broadcast", `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"`+txid+`","seed":"secret"}`, map[string]string{"Idempotency-Key": "withdrawal-2"})
	if rec.Code != http.StatusBadRequest || node.broadcastCalls != 1 {
		t.Fatalf("secret field accepted status=%d", rec.Code)
	}
}

func TestBroadcastIsBoundToAuthorizedRegisteredWallet(t *testing.T) {
	cfg := testConfig(domain.Mainnet)
	cfg.Wallets = append(cfg.Wallets, config.Wallet{WalletID: "cold", UFVK: "jview1cold"})
	token := strings.Repeat("a", 24)
	cfg.Credentials = []config.Credential{{Name: "hot-broadcaster", TokenHash: sha256.Sum256([]byte(token)), Scopes: []string{"broadcast"}, Wallets: []string{"hot"}}}
	service, node, _ := newTestAPI(t, cfg)
	txid := strings.Repeat("c", 64)
	node.decodedTxID, node.broadcastTxID = txid, txid
	headers := map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "wallet-bound"}

	for name, test := range map[string]struct {
		body string
		want int
	}{
		"missing":      {body: `{"raw_tx_hex":"00","expected_txid":"` + txid + `"}`, want: http.StatusBadRequest},
		"unauthorized": {body: `{"wallet_id":"cold","raw_tx_hex":"00","expected_txid":"` + txid + `"}`, want: http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", test.body, headers)
			if rec.Code != test.want || node.broadcastCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
			}
		})
	}

	regtest, regNode, _ := newTestAPI(t, testConfig(domain.Regtest))
	rec := request(t, regtest.Handler(), http.MethodPost, "/v1/transactions/broadcast", `{"wallet_id":"unknown","raw_tx_hex":"00","expected_txid":"`+txid+`"}`, map[string]string{"Idempotency-Key": "unknown-wallet"})
	if rec.Code != http.StatusNotFound || regNode.broadcastCalls != 0 {
		t.Fatalf("unknown status=%d calls=%d body=%s", rec.Code, regNode.broadcastCalls, rec.Body.String())
	}
}

func TestBroadcastIdempotencyPayloadIncludesWallet(t *testing.T) {
	cfg := testConfig(domain.Mainnet)
	cfg.Wallets = append(cfg.Wallets, config.Wallet{WalletID: "cold", UFVK: "jview1cold"})
	token := strings.Repeat("a", 24)
	cfg.Credentials = []config.Credential{{Name: "broadcaster", TokenHash: sha256.Sum256([]byte(token)), Scopes: []string{"broadcast"}, Wallets: []string{"hot", "cold"}}}
	service, node, _ := newTestAPI(t, cfg)
	txid := strings.Repeat("c", 64)
	node.decodedTxID, node.broadcastTxID = txid, txid
	headers := map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "same-attempt"}
	hotBody := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	if rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", hotBody, headers); rec.Code != http.StatusAccepted {
		t.Fatalf("hot status=%d body=%s", rec.Code, rec.Body.String())
	}
	coldBody := `{"wallet_id":"cold","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
	if rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", coldBody, headers); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "idempotency_conflict") || node.broadcastCalls != 1 {
		t.Fatalf("cold status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
	}
}

func TestBroadcastInProgressReturnsRetryHint(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, _, _ := newTestAPI(t, cfg)
	txid := strings.Repeat("a", 64)
	rawTx := "00"
	key := "withdrawal-processing"
	digestBytes := sha256.Sum256([]byte("hot\x00" + rawTx + "\x00" + txid))
	digest := hex.EncodeToString(digestBytes[:])
	if _, err := service.store.ClaimReceipt(context.Background(), scopedIdempotencyKey("regtest-anonymous", key), digest, txid, time.Now(), cfg.IdempotencyLease); err != nil {
		t.Fatal(err)
	}
	rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", `{"wallet_id":"hot","raw_tx_hex":"`+rawTx+`","expected_txid":"`+txid+`"}`, map[string]string{"Idempotency-Key": key})
	if rec.Code != http.StatusConflict || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"retry_after_seconds"`) {
		t.Fatalf("status=%d retry-after=%q body=%s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestCompletedBroadcastReplaysWhileNodeIsUnavailable(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	handler := service.Handler()
	txid := strings.Repeat("c", 64)
	node.broadcastTxID = txid
	node.decodedTxID = txid
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
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
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
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
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + expected + `"}`
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

func TestBroadcastOnlyShortCircuitsCanonicalNodeStates(t *testing.T) {
	for _, state := range []string{"mempool", "confirmed"} {
		t.Run(state, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, node, _ := newTestAPI(t, cfg)
			txid := strings.Repeat("c", 64)
			node.decodedTxID = txid
			node.transactions[txid] = domain.Transaction{TxID: txid, State: state}
			body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
			rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, map[string]string{"Idempotency-Key": "already-" + state})
			if rec.Code != http.StatusOK || node.broadcastCalls != 0 || !strings.Contains(rec.Body.String(), `"state":"`+state+`"`) {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
			}
		})
	}
}

func TestBroadcastRebroadcastsOrphanedTransaction(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		service, node, _ := newTestAPI(t, cfg)
		txid := strings.Repeat("d", 64)
		node.decodedTxID, node.broadcastTxID = txid, txid
		node.transactions[txid] = domain.Transaction{TxID: txid, State: "orphaned"}
		body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
		rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, map[string]string{"Idempotency-Key": "rebroadcast-orphaned"})
		if rec.Code != http.StatusAccepted || node.broadcastCalls != 1 || !strings.Contains(rec.Body.String(), `"state":"mempool"`) {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
		}
	})

	t.Run("uncertain result remains retryable", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		service, node, _ := newTestAPI(t, cfg)
		txid := strings.Repeat("e", 64)
		node.decodedTxID = txid
		node.transactions[txid] = domain.Transaction{TxID: txid, State: "orphaned"}
		node.broadcastErr = &domain.UpstreamError{Kind: "unavailable", Err: io.EOF}
		body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
		rec := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, map[string]string{"Idempotency-Key": "rebroadcast-orphaned-uncertain"})
		if rec.Code != http.StatusBadGateway || node.broadcastCalls != 1 || !strings.Contains(rec.Body.String(), `"retryable":true`) || strings.Contains(rec.Body.String(), `"state":"orphaned"`) {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, node.broadcastCalls, rec.Body.String())
		}
	})
}

func TestCompletedBroadcastUsesFreshOperationKeyForReconciledOrphan(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, _ := newTestAPI(t, cfg)
	txid := strings.Repeat("d", 64)
	node.decodedTxID, node.broadcastTxID = txid, txid
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`

	firstKey := map[string]string{"Idempotency-Key": "withdrawal-orphan-attempt-1"}
	first := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, firstKey)
	if first.Code != http.StatusAccepted || node.broadcastCalls != 1 {
		t.Fatalf("initial status=%d calls=%d body=%s", first.Code, node.broadcastCalls, first.Body.String())
	}

	node.transactions[txid] = domain.Transaction{TxID: txid, State: "orphaned"}
	replay := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, firstKey)
	if replay.Code != http.StatusOK || node.broadcastCalls != 1 || !strings.Contains(replay.Body.String(), `"already_known":true`) {
		t.Fatalf("receipt replay status=%d calls=%d body=%s", replay.Code, node.broadcastCalls, replay.Body.String())
	}

	rebroadcast := request(t, service.Handler(), http.MethodPost, "/v1/transactions/broadcast", body, map[string]string{"Idempotency-Key": "withdrawal-orphan-attempt-1-rebroadcast-1"})
	if rebroadcast.Code != http.StatusAccepted || node.broadcastCalls != 2 || !strings.Contains(rebroadcast.Body.String(), `"accepted":true`) {
		t.Fatalf("rebroadcast status=%d calls=%d body=%s", rebroadcast.Code, node.broadcastCalls, rebroadcast.Body.String())
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
	bodyA := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txidA + `"}`
	headersA := map[string]string{"Authorization": "Bearer " + tokenA, "Idempotency-Key": key}
	if rec := request(t, handler, http.MethodPost, "/v1/transactions/broadcast", bodyA, headersA); rec.Code != http.StatusAccepted {
		t.Fatalf("principal A status=%d body=%s", rec.Code, rec.Body.String())
	}

	node.decodedTxID, node.broadcastTxID = txidB, txidB
	bodyB := `{"wallet_id":"hot","raw_tx_hex":"01","expected_txid":"` + txidB + `"}`
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
			first[i] = domain.ScannerEvent{ID: int64(i + 1), Kind: "DepositEvent", Payload: walletEffectPayload("DepositEvent", "hot", txid, 0)}
		}
		scanner.eventPages = map[int64]domain.EventsPage{
			0:    {Events: first, NextCursor: 1000, EventEpoch: scanner.health.EventEpoch},
			1000: {Events: []domain.ScannerEvent{{ID: 1001, Kind: "DepositConfirmed", Height: 100, Payload: walletEffectPayload("DepositConfirmed", "hot", txid, 100)}}, NextCursor: 1001, EventEpoch: scanner.health.EventEpoch},
		}
		rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				WalletEffects []walletEffect `json:"wallet_effects"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Data.WalletEffects) != 1001 {
			t.Fatalf("effects=%d err=%v", len(out.Data.WalletEffects), err)
		}
		if strings.Contains(rec.Body.String(), `"payload"`) || strings.Contains(rec.Body.String(), "note_nullifier") || out.Data.WalletEffects[0].TxID != txid {
			t.Fatalf("wallet effects were not sanitized: %s", rec.Body.String())
		}
	})
	t.Run("configured cap", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		cfg.WalletEffectsMaxEvents = 2
		service, node, scanner := newTestAPI(t, cfg)
		node.transactions[txid] = domain.Transaction{TxID: txid, State: "confirmed", Confirmations: 1}
		payload := walletEffectPayload("DepositEvent", "hot", txid, 0)
		scanner.eventPages = map[int64]domain.EventsPage{0: {Events: []domain.ScannerEvent{{ID: 1, Kind: "DepositEvent", Payload: payload}, {ID: 2, Kind: "DepositEvent", Payload: payload}, {ID: 3, Kind: "DepositEvent", Payload: payload}}, NextCursor: 3, EventEpoch: scanner.health.EventEpoch}}
		rec := request(t, service.Handler(), http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, nil)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "wallet_effects_limit_exceeded") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestWalletEffectSanitizerAcceptsEveryScannerLifecycleAndRemovesSecrets(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for _, kind := range []string{
		"DepositEvent", "DepositConfirmed", "DepositUnconfirmed", "DepositOrphaned",
		"SpendEvent", "SpendConfirmed", "SpendUnconfirmed", "SpendOrphaned",
		"OutgoingOutputEvent", "OutgoingOutputConfirmed", "OutgoingOutputUnconfirmed", "OutgoingOutputOrphaned", "OutgoingOutputExpired",
	} {
		t.Run(kind, func(t *testing.T) {
			height := int64(100)
			if kind == "DepositEvent" {
				height = 90
			}
			if kind == "SpendEvent" || kind == "OutgoingOutputEvent" {
				height = 0
			}
			event := domain.ScannerEvent{ID: 1, Kind: kind, Height: height, Payload: walletEffectPayload(kind, "hot", txid, height), CreatedAt: time.Unix(1, 0).UTC()}
			effect, err := sanitizeWalletEffect(event, "hot", txid, "jregtest", 100)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(effect)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("note_nullifier")) || bytes.Contains(raw, []byte(`"payload"`)) || effect.Kind != kind || effect.TxID != txid {
				t.Fatalf("effect=%s", raw)
			}
			if strings.HasPrefix(kind, "Spend") && effect.SourceNote == nil {
				t.Fatalf("spend source note was omitted: %s", raw)
			}
		})
	}
}

func TestWalletEffectSanitizerRejectsUnconfirmedWithoutPriorFinality(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for _, kind := range []string{"DepositUnconfirmed", "SpendUnconfirmed", "OutgoingOutputUnconfirmed"} {
		t.Run(kind, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal(walletEffectPayload(kind, "hot", txid, 100), &payload); err != nil {
				t.Fatal(err)
			}
			payload["height"] = float64(99)
			payload["previous_confirmed_height"] = float64(101)
			payload["status"] = map[string]any{"state": "confirmed", "height": float64(99), "confirmations": float64(2)}
			raw, _ := json.Marshal(payload)
			_, err := sanitizeWalletEffect(domain.ScannerEvent{ID: 1, Kind: kind, Height: 100, Payload: raw}, "hot", txid, "jregtest", 100)
			if err == nil || !domain.IsUpstreamKind(err, "invalid_response") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestWalletEffectSanitizerRejectsLifecycleFieldsOnOutgoingEvent(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for _, test := range []struct {
		name        string
		eventHeight int64
		mutate      func(map[string]any)
	}{
		{
			name:        "mempool event with confirmation marker",
			eventHeight: 0,
			mutate: func(payload map[string]any) {
				payload["confirmed_height"] = float64(100)
				payload["required_confirmations"] = float64(100)
			},
		},
		{
			name:        "mined event with rollback marker",
			eventHeight: 90,
			mutate: func(payload map[string]any) {
				payload["height"] = float64(90)
				payload["rollback_height"] = float64(90)
				payload["previous_confirmed_height"] = float64(100)
				payload["status"] = map[string]any{"state": "confirmed", "height": float64(90), "confirmations": float64(1)}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal(walletEffectPayload("OutgoingOutputEvent", "hot", txid, 0), &payload); err != nil {
				t.Fatal(err)
			}
			test.mutate(payload)
			raw, _ := json.Marshal(payload)
			_, err := sanitizeWalletEffect(domain.ScannerEvent{ID: 1, Kind: "OutgoingOutputEvent", Height: test.eventHeight, Payload: raw}, "hot", txid, "jregtest", 100)
			if err == nil || !domain.IsUpstreamKind(err, "invalid_response") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestWalletEffectSanitizerRejectsUnknownPayloadFields(t *testing.T) {
	txid := strings.Repeat("a", 64)
	var payload map[string]any
	if err := json.Unmarshal(walletEffectPayload("SpendEvent", "hot", txid, 0), &payload); err != nil {
		t.Fatal(err)
	}
	payload["seed"] = "must-not-pass"
	raw, _ := json.Marshal(payload)
	_, err := sanitizeWalletEffect(domain.ScannerEvent{ID: 1, Kind: "SpendEvent", Payload: raw}, "hot", txid, "jregtest", 100)
	if err == nil || !domain.IsUpstreamKind(err, "invalid_response") {
		t.Fatalf("error=%v", err)
	}
}

func TestWalletTransactionEffectsRejectMismatchedIdentity(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for name, payload := range map[string]json.RawMessage{
		"wallet": walletEffectPayload("DepositEvent", "other", txid, 0),
		"txid":   walletEffectPayload("DepositEvent", "hot", strings.Repeat("b", 64), 0),
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
		state      string
		extraCheck string
	}{
		"orphaned": {
			kind:  "DepositOrphaned",
			state: "orphaned",
		},
		"expired": {
			kind:       "OutgoingOutputExpired",
			state:      "expired",
			extraCheck: `"expiry_height":99`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			scanner.events = domain.EventsPage{
				Events:     []domain.ScannerEvent{{ID: 1, Kind: test.kind, Height: 100, Payload: walletEffectPayload(test.kind, "hot", txid, 100)}},
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
	expired := walletEffectPayload("OutgoingOutputExpired", "hot", txid, 100)
	t.Run("later nonterminal event wins", func(t *testing.T) {
		cfg := testConfig(domain.Regtest)
		service, _, scanner := newTestAPI(t, cfg)
		scanner.events = domain.EventsPage{
			Events: []domain.ScannerEvent{
				{ID: 1, Kind: "OutgoingOutputExpired", Height: 100, Payload: expired},
				{ID: 2, Kind: "OutgoingOutputEvent", Payload: walletEffectPayload("OutgoingOutputEvent", "hot", txid, 0)},
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
			Events:     []domain.ScannerEvent{{ID: 1, Kind: "OutgoingOutputExpired", Height: 100, Payload: json.RawMessage(`{"version":"v1","wallet_id":"hot","txid":"` + txid + `","action_index":1,"amount_zatoshis":10,"recipient_address":"jregtest1recipient","ovk_scope":"external","expiry_height":99,"status":{"state":"mempool"}}`)}},
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
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
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
	body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
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
			body := `{"wallet_id":"hot","raw_tx_hex":"00","expected_txid":"` + txid + `"}`
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
	withdrawalToken := "withdrawal-reader-token-24"
	hash := sha256.Sum256([]byte(token))
	withdrawalHash := sha256.Sum256([]byte(withdrawalToken))
	cfg.Credentials = []config.Credential{
		{Name: "exchange", TokenHash: hash, Scopes: []string{"read"}, Wallets: []string{"hot"}},
		{Name: "withdrawal-reader", TokenHash: withdrawalHash, Scopes: []string{"read", "withdrawal"}, Wallets: []string{"hot"}},
	}
	service, node, scanner := newTestAPI(t, cfg)
	scanner.events.EventEpoch = scanner.health.EventEpoch
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
	txid := strings.Repeat("a", 64)
	node.transactions[txid] = domain.Transaction{TxID: txid, State: "mempool"}
	if rec := request(t, handler, http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, headers); rec.Code != http.StatusForbidden {
		t.Fatalf("read token enriched transaction status=%d body=%s", rec.Code, rec.Body.String())
	}
	withdrawalHeaders := map[string]string{"Authorization": "Bearer " + withdrawalToken}
	if rec := request(t, handler, http.MethodGet, "/v1/transactions/"+txid+"?wallet_id=hot", ``, withdrawalHeaders); rec.Code != http.StatusOK {
		t.Fatalf("withdrawal token status=%d body=%s", rec.Code, rec.Body.String())
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

func TestReadinessRequiresScannerLagMatchAndScannedBlockHash(t *testing.T) {
	cfg := testConfig(domain.Regtest)
	service, node, scanner := newTestAPI(t, cfg)
	handler := service.Handler()
	bogusLag := int64(999999)
	scanner.health.ScannerLag = &bogusLag
	if rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
		t.Fatalf("scanner lag mismatch status=%d body=%s", rec.Code, rec.Body.String())
	}
	validLag := int64(0)
	scanner.health.ScannerLag = &validLag
	scanner.health.ScannedHash = strings.Repeat("a", 64)
	if rec := request(t, handler, http.MethodGet, "/v1/health/ready", ``, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
		t.Fatalf("hash mismatch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if node.blockHashCalls < 2 {
		t.Fatalf("getblockhash calls=%d", node.blockHashCalls)
	}
}

func TestReadinessRequiresScannerReadyAndLagFields(t *testing.T) {
	for name, mutate := range map[string]func(*domain.ScannerHealth){
		"missing ready": func(health *domain.ScannerHealth) { health.Ready = nil },
		"false ready": func(health *domain.ScannerHealth) {
			ready := false
			health.Ready = &ready
		},
		"missing lag":  func(health *domain.ScannerHealth) { health.ScannerLag = nil },
		"negative lag": func(health *domain.ScannerHealth) { lag := int64(-1); health.ScannerLag = &lag },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			mutate(&scanner.health)
			rec := request(t, service.Handler(), http.MethodGet, "/v1/health/ready", ``, nil)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestReadinessRequiresCompleteHistoryAttestation(t *testing.T) {
	for name, mutate := range map[string]func(*domain.ScannerHealth){
		"missing history attestation": func(health *domain.ScannerHealth) { health.HistoryComplete = nil },
		"incomplete history": func(health *domain.ScannerHealth) {
			complete := false
			health.HistoryComplete = &complete
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(domain.Regtest)
			service, _, scanner := newTestAPI(t, cfg)
			mutate(&scanner.health)
			rec := request(t, service.Handler(), http.MethodGet, "/v1/health/ready", ``, nil)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "scanner_not_ready") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
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
