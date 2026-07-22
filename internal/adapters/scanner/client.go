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

	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

type Client struct {
	baseURL      string
	token        string
	http         *http.Client
	backfillHTTP *http.Client
}

func New(baseURL, token string, timeout, backfillTimeout time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token), http: directHTTPClient(timeout), backfillHTTP: directHTTPClient(backfillTimeout)}
}

func directHTTPClient(timeout time.Duration) *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		transport = transport.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.Proxy = nil
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
	if response.WalletID != walletID || len(response.UFVKFingerprint) != 64 || response.UFVKFingerprint != strings.ToLower(response.UFVKFingerprint) || response.BirthdayHeight < 0 || response.NextHeight < response.BirthdayHeight {
		return domain.BackfillStatus{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid backfill status")}
	}
	for _, character := range response.UFVKFingerprint {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return domain.BackfillStatus{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid backfill status")}
		}
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
	var raw struct {
		domain.Balance
		WalletID         string `json:"wallet_id"`
		RecipientAddress string `json:"recipient_address"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			return domain.Balance{}, false, nil
		}
		return domain.Balance{}, false, err
	}
	out := raw.Balance
	out.WalletID = raw.WalletID
	out.RecipientAddress = raw.RecipientAddress
	if !out.ValidFor(walletID, address, confirmations) {
		return domain.Balance{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid address balance")}
	}
	return out, true, nil
}

type requiredNullableInt64 struct {
	set   bool
	value *int64
}

func (v *requiredNullableInt64) UnmarshalJSON(raw []byte) error {
	v.set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		v.value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	v.value = &value
	return nil
}

type rawNoteValueSummary struct {
	NoteCount *int64 `json:"note_count"`
	ValueZat  *int64 `json:"value_zat"`
}

type rawSpendableNoteSummary struct {
	NoteCount       *int64                `json:"note_count"`
	ValueZat        *int64                `json:"value_zat"`
	SmallestNoteZat requiredNullableInt64 `json:"smallest_note_zat"`
	LargestNoteZat  requiredNullableInt64 `json:"largest_note_zat"`
}

type rawPendingSpendNoteSummary struct {
	NoteCount        *int64                `json:"note_count"`
	ValueZat         *int64                `json:"value_zat"`
	KnownExpiryCount *int64                `json:"known_expiry_count"`
	NextExpiryHeight requiredNullableInt64 `json:"next_expiry_height"`
	LastExpiryHeight requiredNullableInt64 `json:"last_expiry_height"`
}

type rawWalletNoteSummary struct {
	WalletID           *string                     `json:"wallet_id"`
	MinConfirmations   *int64                      `json:"min_confirmations"`
	MinNoteZat         *int64                      `json:"min_note_zat"`
	AsOfScannerHeight  *int64                      `json:"as_of_scanner_height"`
	AsOfScannerHash    *string                     `json:"as_of_scanner_hash"`
	TotalUnspent       *rawNoteValueSummary        `json:"total_unspent"`
	Spendable          *rawSpendableNoteSummary    `json:"spendable"`
	Immature           *rawNoteValueSummary        `json:"immature"`
	PendingSpend       *rawPendingSpendNoteSummary `json:"pending_spend"`
	BelowMinNote       *rawNoteValueSummary        `json:"below_min_note"`
	WitnessUnavailable *rawNoteValueSummary        `json:"witness_unavailable"`
}

func (c *Client) NoteSummary(ctx context.Context, walletID string, minConfirmations, minNoteZat int64, maxNotes int) (domain.WalletNoteSummary, bool, error) {
	q := url.Values{}
	q.Set("min_confirmations", strconv.FormatInt(minConfirmations, 10))
	q.Set("min_note_zat", strconv.FormatInt(minNoteZat, 10))
	q.Set("max_notes", strconv.Itoa(maxNotes))
	path := "/v1/wallets/" + url.PathEscape(walletID) + "/notes/summary?" + q.Encode()
	var raw *rawWalletNoteSummary
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			return domain.WalletNoteSummary{}, false, nil
		}
		if errors.As(err, &he) && he.status == http.StatusUnprocessableEntity {
			return domain.WalletNoteSummary{}, false, domain.ErrNoteSummaryLimitExceeded
		}
		return domain.WalletNoteSummary{}, false, err
	}
	if raw == nil || raw.WalletID == nil || raw.MinConfirmations == nil || raw.MinNoteZat == nil || raw.AsOfScannerHeight == nil || raw.AsOfScannerHash == nil ||
		raw.TotalUnspent == nil || raw.Spendable == nil || raw.Immature == nil || raw.PendingSpend == nil || raw.BelowMinNote == nil || raw.WitnessUnavailable == nil ||
		!completeRawNoteValue(raw.TotalUnspent) || !completeRawNoteValue(raw.Immature) || !completeRawNoteValue(raw.BelowMinNote) || !completeRawNoteValue(raw.WitnessUnavailable) ||
		raw.Spendable.NoteCount == nil || raw.Spendable.ValueZat == nil || !raw.Spendable.SmallestNoteZat.set || !raw.Spendable.LargestNoteZat.set ||
		raw.PendingSpend.NoteCount == nil || raw.PendingSpend.ValueZat == nil || raw.PendingSpend.KnownExpiryCount == nil || !raw.PendingSpend.NextExpiryHeight.set || !raw.PendingSpend.LastExpiryHeight.set {
		return domain.WalletNoteSummary{}, false, invalidNoteSummaryResponse()
	}
	out := domain.WalletNoteSummary{
		WalletID:          strings.TrimSpace(*raw.WalletID),
		MinConfirmations:  *raw.MinConfirmations,
		MinNoteZat:        *raw.MinNoteZat,
		AsOfScannerHeight: *raw.AsOfScannerHeight,
		AsOfScannerHash:   strings.TrimSpace(*raw.AsOfScannerHash),
		TotalUnspent:      noteValueSummary(raw.TotalUnspent),
		Spendable: domain.SpendableNoteSummary{
			NoteValueSummary: domain.NoteValueSummary{NoteCount: *raw.Spendable.NoteCount, ValueZat: *raw.Spendable.ValueZat},
			SmallestNoteZat:  raw.Spendable.SmallestNoteZat.value,
			LargestNoteZat:   raw.Spendable.LargestNoteZat.value,
		},
		Immature: noteValueSummary(raw.Immature),
		PendingSpend: domain.PendingSpendNoteSummary{
			NoteValueSummary: domain.NoteValueSummary{NoteCount: *raw.PendingSpend.NoteCount, ValueZat: *raw.PendingSpend.ValueZat},
			KnownExpiryCount: *raw.PendingSpend.KnownExpiryCount,
			NextExpiryHeight: raw.PendingSpend.NextExpiryHeight.value,
			LastExpiryHeight: raw.PendingSpend.LastExpiryHeight.value,
		},
		BelowMinNote:       noteValueSummary(raw.BelowMinNote),
		WitnessUnavailable: noteValueSummary(raw.WitnessUnavailable),
	}
	if !out.ValidFor(walletID, minConfirmations, minNoteZat, maxNotes) {
		return domain.WalletNoteSummary{}, false, invalidNoteSummaryResponse()
	}
	return out, true, nil
}

func completeRawNoteValue(value *rawNoteValueSummary) bool {
	return value != nil && value.NoteCount != nil && value.ValueZat != nil
}

func noteValueSummary(value *rawNoteValueSummary) domain.NoteValueSummary {
	return domain.NoteValueSummary{NoteCount: *value.NoteCount, ValueZat: *value.ValueZat}
}

func invalidNoteSummaryResponse() error {
	return &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("scanner returned invalid note summary")}
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
		EventEpoch string                `json:"event_epoch"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return domain.EventsPage{}, err
	}
	return domain.EventsPage{Events: raw.Events, NextCursor: raw.NextCursor, EventEpoch: strings.TrimSpace(raw.EventEpoch)}, nil
}
