package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testRPCResponse(t *testing.T, result any, rpcErr any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"result": result, "error": rpcErr, "id": 1})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func TestTipAndTransactionUseTypedRPC(t *testing.T) {
	txid := strings.Repeat("a", 64)
	blockHash := strings.Repeat("b", 64)
	client := New("http://node.invalid", "", "", time.Second)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "getblockchaininfo":
			return testRPCResponse(t, map[string]any{"chain": "regtest", "blocks": 20, "headers": 20, "bestblockhash": blockHash, "initialblockdownload": false, "verificationprogress": 1}, nil), nil
		case "getblockheader":
			return testRPCResponse(t, map[string]any{"height": 20, "time": 1234}, nil), nil
		case "getrawtransaction":
			return testRPCResponse(t, map[string]any{"txid": txid, "hex": "00", "size": 1, "expiryheight": 30, "blockhash": blockHash, "confirmations": 2, "orchard": map[string]any{"actions": []any{map[string]any{}}}}, nil), nil
		default:
			t.Fatalf("unexpected method %s", req.Method)
			return nil, nil
		}
	})
	tip, err := client.Tip(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tip.Network != "regtest" || tip.Height != 20 || tip.BlockTime != 1234 {
		t.Fatalf("tip=%+v", tip)
	}
	tx, found, err := client.Transaction(context.Background(), txid, true)
	if err != nil {
		t.Fatal(err)
	}
	if !found || tx.State != "confirmed" || tx.BlockHeight == nil || *tx.BlockHeight != 20 || tx.RawTxHex != "00" || tx.OrchardActions == nil || *tx.OrchardActions != 1 {
		t.Fatalf("tx=%+v", tx)
	}
}

func TestBroadcastClassifiesRejectedAndWarmingUp(t *testing.T) {
	code := -26
	message := "mandatory-script-verify-flag-failed"
	client := New("http://node.invalid", "", "", time.Second)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return testRPCResponse(t, nil, map[string]any{"code": code, "message": message}), nil
	})
	if _, err := client.Broadcast(context.Background(), "00"); !domain.IsUpstreamKind(err, "rejected") {
		t.Fatalf("err=%v", err)
	}
	code = -28
	message = "Loading block index"
	if _, err := client.Broadcast(context.Background(), "00"); !domain.IsUpstreamKind(err, "unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestBlockHashAndDecodeRawTransactionAreTyped(t *testing.T) {
	hash := strings.Repeat("b", 64)
	txid := strings.Repeat("c", 64)
	client := New("http://node.invalid", "", "", time.Second)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "getblockhash":
			if len(req.Params) != 1 || req.Params[0].(float64) != 12 {
				t.Fatalf("params=%v", req.Params)
			}
			return testRPCResponse(t, hash, nil), nil
		case "decoderawtransaction":
			return testRPCResponse(t, map[string]any{"txid": txid}, nil), nil
		default:
			t.Fatalf("method=%s", req.Method)
			return nil, nil
		}
	})
	gotHash, err := client.BlockHash(context.Background(), 12)
	if err != nil || gotHash != hash {
		t.Fatalf("hash=%q err=%v", gotHash, err)
	}
	gotTxID, err := client.DecodeRawTransaction(context.Background(), "00")
	if err != nil || gotTxID != txid {
		t.Fatalf("txid=%q err=%v", gotTxID, err)
	}
}

func TestTransactionWithStaleBlockHashIsOrphaned(t *testing.T) {
	for _, confirmations := range []int64{0, -2} {
		t.Run(fmt.Sprintf("confirmations_%d", confirmations), func(t *testing.T) {
			txid := strings.Repeat("a", 64)
			blockHash := strings.Repeat("b", 64)
			client := New("http://node.invalid", "", "", time.Second)
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Method {
				case "getrawtransaction":
					return testRPCResponse(t, map[string]any{"txid": txid, "blockhash": blockHash, "confirmations": confirmations}, nil), nil
				case "getblockheader":
					return testRPCResponse(t, map[string]any{"height": 7, "time": 1234}, nil), nil
				default:
					t.Fatalf("method=%s", req.Method)
					return nil, nil
				}
			})
			tx, found, err := client.Transaction(context.Background(), txid, false)
			if err != nil || !found || tx.State != "orphaned" || tx.BlockHeight == nil || *tx.BlockHeight != 7 {
				t.Fatalf("tx=%+v found=%v err=%v", tx, found, err)
			}
		})
	}
}
