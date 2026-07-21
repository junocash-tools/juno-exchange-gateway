package scanner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"available_zat":12,"pending_incoming_zat":1,"pending_outgoing_zat":2,"total_unspent_zat":13,"min_confirmations":100,"as_of_node_height":10,"as_of_scanner_height":10,"scanner_lag":0}`))}, nil
	})
	balance, found, err := client.Balance(context.Background(), "hot", address, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !found || balance.AvailableZat != 12 || balance.PendingOutgoingZat != 2 {
		t.Fatalf("balance=%+v found=%v", balance, found)
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
	client := New("http://scanner.invalid", "", time.Second, time.Minute)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"wallet_id":"hot","birthday_height":50,"next_height":75,"target_height":100,"state":"running","updated_at":"2026-07-21T12:00:00Z"}`))}, nil
	})
	status, found, err := client.BackfillStatus(context.Background(), "hot")
	if err != nil || !found || status.NextHeight != 75 || status.State != "running" {
		t.Fatalf("status=%+v found=%v err=%v", status, found, err)
	}
}
