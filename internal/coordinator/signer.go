package coordinator

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Signer interface {
	Sign(context.Context, string, planResult) (signerResult, error)
	Health(context.Context) error
}

type UnixSigner struct {
	socket string
	client *http.Client
}

func NewUnixSigner(socket string, timeout time.Duration) *UnixSigner {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression:    true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &UnixSigner{socket: socket, client: &http.Client{Transport: transport}}
}

func (s *UnixSigner) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("signer health returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *UnixSigner) Sign(ctx context.Context, attemptID string, plan planResult) (signerResult, error) {
	payload, err := json.Marshal(map[string]string{
		"version":       "v1",
		"attempt_id":    attemptID,
		"plan_digest":   plan.Digest,
		"txplan_base64": base64.StdEncoding.EncodeToString(plan.Bytes),
	})
	if err != nil {
		return signerResult{}, opError("signer_request_invalid", "signer request could not be encoded", false)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/sign", strings.NewReader(string(payload)))
	if err != nil {
		return signerResult{}, opError("signer_request_invalid", "signer request could not be created", false)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return signerResult{}, wrapOpError("signer_unavailable", "signer response is unknown; notes remain reserved", true, true, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return signerResult{}, wrapOpError("signer_unavailable", "signer response could not be read; notes remain reserved", true, true, err)
	}
	if len(body) > 8<<20 {
		return signerResult{}, opError("signer_invalid_response", "signer response exceeds the 8 MiB limit", true)
	}
	var envelope struct {
		Version string `json:"version"`
		Status  string `json:"status"`
		Data    struct {
			AttemptID                  string   `json:"attempt_id"`
			PlanDigest                 string   `json:"plan_digest"`
			Replayed                   bool     `json:"replayed"`
			TxID                       string   `json:"txid"`
			RawTxHex                   string   `json:"raw_tx_hex"`
			FeeZat                     string   `json:"fee_zat"`
			OrchardOutputActionIndices []uint32 `json:"orchard_output_action_indices"`
			OrchardChangeActionIndex   *uint32  `json:"orchard_change_action_index"`
		} `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Version != "v1" {
		return signerResult{}, opError("signer_invalid_response", "signer returned invalid JSON", true)
	}
	if resp.StatusCode != http.StatusOK || envelope.Status != "ok" {
		return signerResult{}, signerHTTPError(resp.StatusCode, envelope.Error.Code, envelope.Error.Message)
	}
	data := envelope.Data
	if data.AttemptID != attemptID || data.PlanDigest != plan.Digest || !txIDRE.MatchString(data.TxID) || data.RawTxHex == "" || data.RawTxHex != strings.ToLower(data.RawTxHex) || len(data.RawTxHex)%2 != 0 || data.FeeZat != plan.FeeZat {
		return signerResult{}, opError("signer_invalid_response", "signer returned data that does not match the reserved plan", true)
	}
	if _, err := hex.DecodeString(data.RawTxHex); err != nil {
		return signerResult{}, opError("signer_invalid_response", "signer returned invalid raw transaction hex", true)
	}
	return signerResult{
		TxID:                       data.TxID,
		RawTxHex:                   data.RawTxHex,
		FeeZat:                     data.FeeZat,
		OrchardOutputActionIndices: append([]uint32(nil), data.OrchardOutputActionIndices...),
		OrchardChangeActionIndex:   data.OrchardChangeActionIndex,
		Replay:                     data.Replayed,
	}, nil
}

func signerHTTPError(status int, code, message string) error {
	if code == "" {
		code = "signer_unavailable"
	}
	if message == "" {
		message = fmt.Sprintf("signer returned HTTP %d", status)
	}
	switch code {
	case "invalid_request", "plan_not_allowed":
		return &operationError{Code: code, Message: message, Retryable: false, OutcomeUnknown: false}
	case "signer_busy":
		return &operationError{Code: code, Message: message, Retryable: true, OutcomeUnknown: false}
	case "attempt_outcome_unknown", "signing_outcome_unknown", "attempt_digest_conflict", "journal_unsafe", "journal_unavailable", "journal_write_failed":
		return &operationError{Code: code, Message: message, Retryable: code != "attempt_digest_conflict", OutcomeUnknown: true}
	default:
		return &operationError{Code: code, Message: message, Retryable: status >= 500 || status == http.StatusTooManyRequests, OutcomeUnknown: status >= 500}
	}
}
