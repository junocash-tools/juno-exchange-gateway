package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBalanceUsesBearerAndAddressRoute(t *testing.T) {
	address := "jregtest1example"
	client := New("http://scanner.invalid", "scanner-secret", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer scanner-secret" {
			t.Errorf("missing bearer")
		}
		if !strings.Contains(r.URL.EscapedPath(), "/v1/wallets/hot/addresses/"+address+"/balance") {
			t.Errorf("path=%s", r.URL.EscapedPath())
		}
		if r.URL.Query().Get("min_confirmations") != "100" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"wallet_id":"hot","recipient_address":"jregtest1example","available_zat":10,"pending_incoming_zat":1,"pending_outgoing_zat":2,"total_unspent_zat":13,"min_confirmations":100,"as_of_node_height":10,"as_of_scanner_height":10,"scanner_lag":0}`))}, nil
	})
	balance, found, err := client.Balance(context.Background(), "hot", address, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !found || balance.AvailableZat != 10 || balance.PendingOutgoingZat != 2 || balance.WalletID != "hot" || balance.RecipientAddress != address {
		t.Fatalf("balance=%+v found=%v", balance, found)
	}
}

func TestBalanceRejectsWrongIdentityAndBrokenAccounting(t *testing.T) {
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	response := `{"wallet_id":"other","recipient_address":"jregtest1example","available_zat":10,"pending_incoming_zat":1,"pending_outgoing_zat":2,"total_unspent_zat":12,"min_confirmations":0,"as_of_node_height":10,"as_of_scanner_height":10,"scanner_lag":0}`
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	if _, _, err := client.Balance(context.Background(), "hot", "jregtest1example", 0, 10); err == nil {
		t.Fatal("expected invalid balance rejection")
	}
}

func TestEventsCarriesScannerEpoch(t *testing.T) {
	epoch := strings.Repeat("a", 64)
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"events":[],"next_cursor":7,"event_epoch":"` + epoch + `"}`))}, nil
	})
	page, err := client.Events(context.Background(), "hot", 7, 100, domain.EventFilter{})
	if err != nil || page.EventEpoch != epoch || page.NextCursor != 7 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestNoteSummaryUsesAtomicAggregateRoute(t *testing.T) {
	snapshotHash := strings.Repeat("a", 64)
	client := New("http://scanner.invalid", "scanner-secret", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/wallets/hot/notes/summary" || r.URL.Query().Get("min_confirmations") != "100" || r.URL.Query().Get("min_note_zat") != "100001" || r.URL.Query().Get("max_notes") != "100000" {
			t.Fatalf("request=%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"wallet_id":"hot","min_confirmations":100,"min_note_zat":100001,"as_of_scanner_height":10,"as_of_scanner_hash":"` + snapshotHash + `","total_unspent":{"note_count":2,"value_zat":300000},"spendable":{"note_count":1,"value_zat":200000,"smallest_note_zat":200000,"largest_note_zat":200000},"immature":{"note_count":0,"value_zat":0},"pending_spend":{"note_count":0,"value_zat":0,"known_expiry_count":0,"next_expiry_height":null,"last_expiry_height":null},"below_min_note":{"note_count":1,"value_zat":100000},"witness_unavailable":{"note_count":0,"value_zat":0}}`))}, nil
	})
	summary, found, err := client.NoteSummary(context.Background(), "hot", 100, 100001, 100000)
	if err != nil || !found || summary.AsOfScannerHash != snapshotHash || summary.TotalUnspent.NoteCount != 2 || summary.Spendable.ValueZat != 200000 || summary.BelowMinNote.NoteCount != 1 {
		t.Fatalf("summary=%+v found=%v err=%v", summary, found, err)
	}
}

func TestNoteSummaryRejectsMissingOrNullRequiredFields(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"wallet_id": "hot", "min_confirmations": 100, "min_note_zat": 100, "as_of_scanner_height": 10,
			"as_of_scanner_hash": strings.Repeat("a", 64),
			"total_unspent":      map[string]any{"note_count": 0, "value_zat": 0},
			"spendable":          map[string]any{"note_count": 0, "value_zat": 0, "smallest_note_zat": nil, "largest_note_zat": nil},
			"immature":           map[string]any{"note_count": 0, "value_zat": 0},
			"pending_spend":      map[string]any{"note_count": 0, "value_zat": 0, "known_expiry_count": 0, "next_expiry_height": nil, "last_expiry_height": nil},
			"below_min_note":     map[string]any{"note_count": 0, "value_zat": 0},
			"witness_unavailable": map[string]any{
				"note_count": 0, "value_zat": 0,
			},
		}
	}
	tests := map[string]func() string{
		"empty body":   func() string { return "" },
		"empty object": func() string { return `{}` },
		"null bucket": func() string {
			payload := valid()
			payload["total_unspent"] = nil
			return mustJSON(payload)
		},
		"missing numeric": func() string {
			payload := valid()
			delete(payload["total_unspent"].(map[string]any), "value_zat")
			return mustJSON(payload)
		},
		"omitted nullable": func() string {
			payload := valid()
			delete(payload["spendable"].(map[string]any), "smallest_note_zat")
			return mustJSON(payload)
		},
		"value without notes": func() string {
			payload := valid()
			payload["total_unspent"].(map[string]any)["value_zat"] = 1
			return mustJSON(payload)
		},
		"expired pending marker": func() string {
			payload := valid()
			pending := payload["pending_spend"].(map[string]any)
			pending["note_count"], pending["value_zat"], pending["known_expiry_count"] = 1, 1, 1
			pending["next_expiry_height"], pending["last_expiry_height"] = 9, 9
			payload["total_unspent"] = map[string]any{"note_count": 1, "value_zat": 1}
			return mustJSON(payload)
		},
		"below-min aggregate above strict floor": func() string {
			payload := valid()
			payload["below_min_note"] = map[string]any{"note_count": 1, "value_zat": 100}
			payload["total_unspent"] = map[string]any{"note_count": 1, "value_zat": 100}
			return mustJSON(payload)
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			client := New("http://scanner.invalid", "", time.Second, time.Minute)
			client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response()))}, nil
			})
			if _, _, err := client.NoteSummary(context.Background(), "hot", 100, 100, 100000); err == nil || !domain.IsUpstreamKind(err, "invalid_response") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestNoteSummaryLimitIsTyped(t *testing.T) {
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnprocessableEntity, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("limit"))}, nil
	})
	if _, _, err := client.NoteSummary(context.Background(), "hot", 100, 0, 1); !errors.Is(err, domain.ErrNoteSummaryLimitExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestNoteStatusesUsesBatchRouteAndPreservesOrder(t *testing.T) {
	noteIDs := []string{strings.Repeat("a", 64) + ":0", strings.Repeat("b", 64) + ":4294967295"}
	epoch, snapshotHash := strings.Repeat("c", 64), strings.Repeat("d", 64)
	client := New("http://scanner.invalid", "scanner-secret", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/wallets/hot/notes/status" || r.Header.Get("Authorization") != "Bearer scanner-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request=%s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request struct {
			NoteIDs []string `json:"note_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.NoteIDs) != 2 || request.NoteIDs[0] != noteIDs[0] || request.NoteIDs[1] != noteIDs[1] {
			t.Fatalf("request=%+v err=%v", request, err)
		}
		response := `{"wallet_id":"hot","event_epoch":"` + epoch + `","as_of_scanner_height":100,"as_of_scanner_hash":"` + snapshotHash + `","statuses":[` +
			`{"note_id":"` + noteIDs[0] + `","state":"unknown"},` +
			`{"note_id":"` + noteIDs[1] + `","state":"pending","source_height":10,"value_zat":25,"pending_spent_txid":"` + strings.Repeat("e", 64) + `","pending_spent_at":"2026-07-22T12:00:00Z","pending_spent_expiry_height":140}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	statuses, found, err := client.NoteStatuses(context.Background(), "hot", noteIDs)
	if err != nil || !found || statuses.EventEpoch != epoch || statuses.AsOfScannerHash != snapshotHash || len(statuses.Statuses) != 2 || statuses.Statuses[0].State != "unknown" || statuses.Statuses[1].PendingSpentExpiryHeight == nil || *statuses.Statuses[1].PendingSpentExpiryHeight != 140 {
		t.Fatalf("statuses=%+v found=%v err=%v", statuses, found, err)
	}
}

func TestNoteStatusesAcceptsUnspentAndSpentFields(t *testing.T) {
	noteIDs := []string{strings.Repeat("a", 64) + ":0", strings.Repeat("b", 64) + ":1"}
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := `{"wallet_id":"hot","event_epoch":"` + strings.Repeat("c", 64) + `","as_of_scanner_height":100,"as_of_scanner_hash":"` + strings.Repeat("d", 64) + `","statuses":[` +
			`{"note_id":"` + noteIDs[0] + `","state":"unspent","source_height":10,"value_zat":25},` +
			`{"note_id":"` + noteIDs[1] + `","state":"spent","source_height":20,"value_zat":30,"spent_txid":"` + strings.Repeat("e", 64) + `","spent_height":50,"spent_confirmed_height":100}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	statuses, found, err := client.NoteStatuses(context.Background(), "hot", noteIDs)
	if err != nil || !found || len(statuses.Statuses) != 2 || statuses.Statuses[0].ValueZat == nil || *statuses.Statuses[0].ValueZat != 25 || statuses.Statuses[1].SpentTxID == nil || statuses.Statuses[1].SpentConfirmedHeight == nil || *statuses.Statuses[1].SpentConfirmedHeight != 100 {
		t.Fatalf("statuses=%+v found=%v err=%v", statuses, found, err)
	}
}

func TestNoteStatusesRejectsMalformedScannerResponses(t *testing.T) {
	noteID := strings.Repeat("a", 64) + ":0"
	valid := func() map[string]any {
		return map[string]any{
			"wallet_id": "hot", "event_epoch": strings.Repeat("b", 64), "as_of_scanner_height": 100,
			"as_of_scanner_hash": strings.Repeat("c", 64),
			"statuses":           []any{map[string]any{"note_id": noteID, "state": "unspent", "source_height": 10, "value_zat": 25}},
		}
	}
	tests := map[string]func() string{
		"empty": func() string { return "" },
		"missing snapshot": func() string {
			payload := valid()
			delete(payload, "event_epoch")
			return mustJSON(payload)
		},
		"extra top-level field": func() string {
			payload := valid()
			payload["safe_to_release"] = true
			return mustJSON(payload)
		},
		"wrong wallet": func() string {
			payload := valid()
			payload["wallet_id"] = "cold"
			return mustJSON(payload)
		},
		"wrong order identity": func() string {
			payload := valid()
			payload["statuses"].([]any)[0].(map[string]any)["note_id"] = strings.Repeat("d", 64) + ":0"
			return mustJSON(payload)
		},
		"unknown with source": func() string {
			payload := valid()
			status := payload["statuses"].([]any)[0].(map[string]any)
			status["state"] = "unknown"
			return mustJSON(payload)
		},
		"unspent missing value": func() string {
			payload := valid()
			delete(payload["statuses"].([]any)[0].(map[string]any), "value_zat")
			return mustJSON(payload)
		},
		"pending missing observed time": func() string {
			payload := valid()
			status := payload["statuses"].([]any)[0].(map[string]any)
			status["state"] = "pending"
			status["pending_spent_txid"] = strings.Repeat("e", 64)
			return mustJSON(payload)
		},
		"pending after expiry": func() string {
			payload := valid()
			status := payload["statuses"].([]any)[0].(map[string]any)
			status["state"] = "pending"
			status["pending_spent_txid"] = strings.Repeat("e", 64)
			status["pending_spent_at"] = "2026-07-22T12:00:00Z"
			status["pending_spent_expiry_height"] = 99
			return mustJSON(payload)
		},
		"pending null optional expiry": func() string {
			payload := valid()
			status := payload["statuses"].([]any)[0].(map[string]any)
			status["state"] = "pending"
			status["pending_spent_txid"] = strings.Repeat("e", 64)
			status["pending_spent_at"] = "2026-07-22T12:00:00Z"
			status["pending_spent_expiry_height"] = nil
			return mustJSON(payload)
		},
		"spent confirmation before spend": func() string {
			payload := valid()
			status := payload["statuses"].([]any)[0].(map[string]any)
			status["state"] = "spent"
			status["spent_txid"] = strings.Repeat("f", 64)
			status["spent_height"] = 50
			status["spent_confirmed_height"] = 49
			return mustJSON(payload)
		},
		"extra item field": func() string {
			payload := valid()
			payload["statuses"].([]any)[0].(map[string]any)["note_nullifier"] = strings.Repeat("f", 64)
			return mustJSON(payload)
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			client := New("http://scanner.invalid", "", time.Second, time.Minute)
			client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response()))}, nil
			})
			if _, _, err := client.NoteStatuses(context.Background(), "hot", []string{noteID}); err == nil || !domain.IsUpstreamKind(err, "invalid_response") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestNoteStatusesMapsMissingAndUnavailableScanner(t *testing.T) {
	noteID := strings.Repeat("a", 64) + ":0"
	for _, test := range []struct {
		name        string
		status      int
		found       bool
		kind        string
		snapshotErr bool
	}{
		{name: "missing", status: http.StatusNotFound, found: false},
		{name: "snapshot changed", status: http.StatusConflict, snapshotErr: true},
		{name: "invalid request response", status: http.StatusBadRequest, kind: "invalid_response"},
		{name: "unexpected body rejection", status: http.StatusRequestEntityTooLarge, kind: "invalid_response"},
		{name: "unavailable", status: http.StatusServiceUnavailable, kind: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New("http://scanner.invalid", "", time.Second, time.Minute)
			client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("error"))}, nil
			})
			_, found, err := client.NoteStatuses(context.Background(), "hot", []string{noteID})
			if found != test.found || (test.snapshotErr && !errors.Is(err, domain.ErrScannerSnapshotChanged)) ||
				(!test.snapshotErr && test.kind == "" && err != nil) || (test.kind != "" && !domain.IsUpstreamKind(err, test.kind)) {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestHealthParsesConfirmationPolicy(t *testing.T) {
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"ok","confirmations":100,"pending_spends_ready":true}`))}, nil
	})
	health, err := client.Health(context.Background())
	if err != nil || health.Confirmations == nil || *health.Confirmations != 100 || health.PendingSpendsReady == nil || !*health.PendingSpendsReady {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestBalanceNotFoundIsTyped(t *testing.T) {
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
	})
	_, found, err := client.Balance(context.Background(), "hot", "jregtest1example", 100, 10)
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestBackfillUsesDedicatedClientAndValidatesProgress(t *testing.T) {
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.backfillHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/wallets/hot/backfill" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"next_height":101}`))}, nil
	})
	next, err := client.Backfill(context.Background(), "hot", 100, 10000)
	if err != nil || next != 101 {
		t.Fatalf("next=%d err=%v", next, err)
	}
}

func TestBackfillStatusIsTyped(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"wallet_id":"hot","ufvk_fingerprint":"` + fingerprint + `","birthday_height":50,"next_height":75,"target_height":100,"state":"running","updated_at":"2026-07-21T12:00:00Z"}`))}, nil
	})
	status, found, err := client.BackfillStatus(context.Background(), "hot")
	if err != nil || !found || status.UFVKFingerprint != fingerprint || status.NextHeight != 75 || status.State != "running" {
		t.Fatalf("status=%+v found=%v err=%v", status, found, err)
	}
}

func TestPrivateScannerClientsIgnoreProxyAndRejectRedirects(t *testing.T) {
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	for name, httpClient := range map[string]*http.Client{"foreground": client.http, "backfill": client.backfillHTTP} {
		transport, ok := httpClient.Transport.(*http.Transport)
		if !ok || transport.Proxy != nil {
			t.Fatalf("%s transport=%T", name, httpClient.Transport)
		}
	}

	redirectTargetHit := false
	client = New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "redirect.invalid" {
			redirectTargetHit = true
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://redirect.invalid/"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})
	if _, err := client.Health(context.Background()); err == nil {
		t.Fatal("expected redirect rejection")
	}
	if redirectTargetHit {
		t.Fatal("scanner client followed redirect")
	}
}
