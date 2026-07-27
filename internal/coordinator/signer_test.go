package coordinator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixSignerUsesExactDigestBoundContract(t *testing.T) {
	planBytes := []byte(`{"version":"v0"}`)
	plan := planResult{Bytes: planBytes, Digest: "sha256:" + fmt.Sprintf("%064x", 1), FeeZat: "200000"}
	attemptID := "txn_00000000000000000000000000000001"
	socket := shortTestSocket(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "v1", "status": "ok"})
		case "/v1/sign":
			var request struct {
				Version      string `json:"version"`
				AttemptID    string `json:"attempt_id"`
				PlanDigest   string `json:"plan_digest"`
				TxPlanBase64 string `json:"txplan_base64"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Version != "v1" || request.AttemptID != attemptID || request.PlanDigest != plan.Digest || request.TxPlanBase64 != base64.StdEncoding.EncodeToString(planBytes) {
				t.Errorf("request=%+v err=%v", request, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "v1", "status": "ok", "data": map[string]any{
					"attempt_id": attemptID, "plan_digest": plan.Digest, "replayed": true,
					"txid": fmt.Sprintf("%064x", 2), "raw_tx_hex": "00", "fee_zat": plan.FeeZat,
					"orchard_output_action_indices": []uint32{0}, "orchard_change_action_index": uint32(1),
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	})

	client := NewUnixSigner(socket, time.Second)
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := client.Sign(context.Background(), attemptID, plan)
	if err != nil || result.TxID != fmt.Sprintf("%064x", 2) || result.RawTxHex != "00" || !result.Replay || result.OrchardChangeActionIndex == nil || *result.OrchardChangeActionIndex != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestUnixSignerKeepsReservationsOnUnknownOutcome(t *testing.T) {
	socket := shortTestSocket(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "v1", "status": "err", "error": map[string]any{"code": "attempt_outcome_unknown", "message": "operator recovery required"},
		})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	_, err = NewUnixSigner(socket, time.Second).Sign(context.Background(), "txn_00000000000000000000000000000001", planResult{
		Bytes: []byte(`{}`), Digest: "sha256:" + fmt.Sprintf("%064x", 1), FeeZat: "200000",
	})
	var operation *operationError
	if !errors.As(err, &operation) || !operation.OutcomeUnknown || operation.Code != "attempt_outcome_unknown" {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

func shortTestSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(os.TempDir(), fmt.Sprintf("juno-coord-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}
