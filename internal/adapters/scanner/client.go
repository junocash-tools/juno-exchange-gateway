package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
)

type Client struct {
	baseURL      string
	token        string
	http         *http.Client
	backfillHTTP *http.Client
}

func New(baseURL, token string, timeout, backfillTimeout time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token), http: &http.Client{Timeout: timeout}, backfillHTTP: &http.Client{Timeout: backfillTimeout}}
}

type httpError struct{ status int }

func (e *httpError) Error() string { return fmt.Sprintf("scanner HTTP %d", e.status) }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return c.doWithClient(ctx, c.http, method, path, in, out)
}

func (c *Client) doWithClient(ctx context.Context, httpClient *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return errors.New("encode scanner request")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return errors.New("build scanner request")
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return &domain.UpstreamError{Kind: "unavailable", Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &domain.UpstreamError{Kind: "unavailable", Err: errors.New("read scanner response")}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpError{status: resp.StatusCode}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("invalid scanner response")}
		}
	}
	return nil
}

func (c *Client) BackfillStatus(ctx context.Context, walletID string) (domain.BackfillStatus, bool, error) {
	path := "/v1/wallets/" + url.PathEscape(walletID) + "/backfill"
	var response domain.BackfillStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			return domain.BackfillStatus{}, false, nil
		}
		return domain.BackfillStatus{}, false, &domain.UpstreamError{Kind: "unavailable", Err: err}
	}
	if response.WalletID != walletID || response.BirthdayHeight < 0 || response.NextHeight < response.BirthdayHeight {
		return domain.BackfillStatus{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid backfill status")}
	}
	switch response.State {
	case "pending", "running", "complete", "error":
	default:
		return domain.BackfillStatus{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid backfill state")}
	}
	return response, true, nil
}

func (c *Client) Backfill(ctx context.Context, walletID string, toHeight, batchSize int64) (int64, error) {
	path := "/v1/wallets/" + url.PathEscape(walletID) + "/backfill"
	request := map[string]int64{"to_height": toHeight, "batch_size": batchSize}
	var response struct {
		NextHeight int64 `json:"next_height"`
	}
	if err := c.doWithClient(ctx, c.backfillHTTP, http.MethodPost, path, request, &response); err != nil {
		return 0, err
	}
	if response.NextHeight < 0 || response.NextHeight > toHeight+1 {
		return 0, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid backfill progress")}
	}
	return response.NextHeight, nil
}

func (c *Client) Health(ctx context.Context) (domain.ScannerHealth, error) {
	var out domain.ScannerHealth
	if err := c.do(ctx, http.MethodGet, "/v1/health", nil, &out); err != nil {
		return domain.ScannerHealth{}, err
	}
	return out, nil
}

func (c *Client) UpsertWallet(ctx context.Context, walletID, ufvk string, birthdayHeight int64) error {
	return c.do(ctx, http.MethodPost, "/v1/wallets", map[string]any{"wallet_id": walletID, "ufvk": ufvk, "birthday_height": birthdayHeight}, &struct {
		Status string `json:"status"`
	}{})
}

func (c *Client) Balance(ctx context.Context, walletID, address string, confirmations, _ int64) (domain.Balance, bool, error) {
	q := url.Values{}
	q.Set("min_confirmations", strconv.FormatInt(confirmations, 10))
	path := "/v1/wallets/" + url.PathEscape(walletID) + "/addresses/" + url.PathEscape(address) + "/balance?" + q.Encode()
	var out domain.Balance
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			return domain.Balance{}, false, nil
		}
		return domain.Balance{}, false, err
	}
	return out, true, nil
}

func (c *Client) Events(ctx context.Context, walletID string, cursor int64, limit int, filter domain.EventFilter) (domain.EventsPage, error) {
	q := url.Values{}
	q.Set("cursor", strconv.FormatInt(cursor, 10))
	q.Set("limit", strconv.Itoa(limit))
	for _, kind := range filter.Kinds {
		q.Add("kind", kind)
	}
	if filter.TxID != "" {
		q.Set("txid", filter.TxID)
	}
	path := "/v1/wallets/" + url.PathEscape(walletID) + "/events?" + q.Encode()
	var raw struct {
		Events     []domain.ScannerEvent `json:"events"`
		NextCursor int64                 `json:"next_cursor"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return domain.EventsPage{}, err
	}
	return domain.EventsPage{Events: raw.Events, NextCursor: raw.NextCursor}, nil
}
