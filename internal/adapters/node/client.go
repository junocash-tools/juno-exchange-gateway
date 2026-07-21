package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
)

type Client struct {
	url      string
	user     string
	password string
	http     *http.Client
	nextID   atomic.Uint64
}

func New(rawURL, user, password string, timeout time.Duration) *Client {
	return &Client{url: strings.TrimRight(rawURL, "/"), user: user, password: password, http: &http.Client{Timeout: timeout}}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	ID     uint64          `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("node RPC error %d", e.Code) }

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: c.nextID.Add(1), Method: method, Params: params})
	if err != nil {
		return errors.New("encode node RPC request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("build node RPC request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "juno-exchange-gateway/1")
	if c.user != "" || c.password != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &domain.UpstreamError{Kind: "unavailable", Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &domain.UpstreamError{Kind: "unavailable", Err: errors.New("read node RPC response")}
	}
	var decoded rpcResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("invalid node RPC response")}
	}
	if decoded.Error != nil {
		return decoded.Error
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &domain.UpstreamError{Kind: "unavailable", Err: fmt.Errorf("node RPC HTTP %d", resp.StatusCode)}
	}
	if out != nil && len(decoded.Result) > 0 && !bytes.Equal(decoded.Result, []byte("null")) {
		if err := json.Unmarshal(decoded.Result, out); err != nil {
			return &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("invalid node RPC result")}
		}
	}
	return nil
}

func (c *Client) Tip(ctx context.Context) (domain.NodeTip, error) {
	var info struct {
		Chain                string  `json:"chain"`
		Blocks               int64   `json:"blocks"`
		Headers              int64   `json:"headers"`
		BestBlockHash        string  `json:"bestblockhash"`
		InitialBlockDownload bool    `json:"initialblockdownload"`
		VerificationProgress float64 `json:"verificationprogress"`
	}
	if err := c.call(ctx, "getblockchaininfo", nil, &info); err != nil {
		return domain.NodeTip{}, err
	}
	var header struct {
		Time int64 `json:"time"`
	}
	if info.BestBlockHash != "" {
		if err := c.call(ctx, "getblockheader", []any{info.BestBlockHash, true}, &header); err != nil {
			return domain.NodeTip{}, err
		}
	}
	return domain.NodeTip{
		Network: info.Chain, Height: info.Blocks, Hash: info.BestBlockHash, BlockTime: header.Time,
		Headers: info.Headers, InitialBlockDownload: info.InitialBlockDownload, VerificationProgress: info.VerificationProgress,
	}, nil
}

func (c *Client) BlockHash(ctx context.Context, height int64) (string, error) {
	if height < 0 {
		return "", errors.New("block height must be non-negative")
	}
	var hash string
	if err := c.call(ctx, "getblockhash", []any{height}, &hash); err != nil {
		return "", err
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 64 {
		return "", &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("node returned invalid block hash")}
	}
	for _, character := range hash {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return "", &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("node returned invalid block hash")}
		}
	}
	return hash, nil
}

func (c *Client) DecodeRawTransaction(ctx context.Context, rawTxHex string) (string, error) {
	var decoded struct {
		TxID string `json:"txid"`
	}
	if err := c.call(ctx, "decoderawtransaction", []any{rawTxHex}, &decoded); err != nil {
		return "", err
	}
	txid := strings.ToLower(strings.TrimSpace(decoded.TxID))
	if len(txid) != 64 {
		return "", &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("node decoder returned invalid transaction ID")}
	}
	for _, character := range txid {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return "", &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("node decoder returned invalid transaction ID")}
		}
	}
	return txid, nil
}

func (c *Client) Transaction(ctx context.Context, txid string, includeRaw bool) (domain.Transaction, bool, error) {
	var raw struct {
		Hex           string `json:"hex"`
		TxID          string `json:"txid"`
		Size          int64  `json:"size"`
		ExpiryHeight  int64  `json:"expiryheight"`
		BlockHash     string `json:"blockhash"`
		Confirmations int64  `json:"confirmations"`
		Time          int64  `json:"time"`
		BlockTime     int64  `json:"blocktime"`
		Orchard       struct {
			Actions []json.RawMessage `json:"actions"`
		} `json:"orchard"`
		VActionsOrchard []json.RawMessage `json:"vActionsOrchard"`
	}
	err := c.call(ctx, "getrawtransaction", []any{txid, 1}, &raw)
	if err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) && (rpcErr.Code == -5 || strings.Contains(strings.ToLower(rpcErr.Message), "not found") || strings.Contains(strings.ToLower(rpcErr.Message), "no such")) {
			return domain.Transaction{}, false, nil
		}
		return domain.Transaction{}, false, err
	}
	out := domain.Transaction{TxID: strings.ToLower(strings.TrimSpace(raw.TxID)), Confirmations: raw.Confirmations, BlockHash: raw.BlockHash}
	if out.TxID == "" {
		out.TxID = txid
	}
	if out.TxID != txid {
		return domain.Transaction{}, false, &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("node returned mismatched transaction ID")}
	}
	if raw.BlockHash != "" {
		if raw.Confirmations > 0 {
			out.State = "confirmed"
		} else {
			out.State = "orphaned"
		}
		var header struct {
			Height int64 `json:"height"`
			Time   int64 `json:"time"`
		}
		if err := c.call(ctx, "getblockheader", []any{raw.BlockHash, true}, &header); err != nil {
			return domain.Transaction{}, false, err
		}
		out.BlockHeight = &header.Height
		out.BlockTime = &header.Time
	} else {
		out.State = "mempool"
	}
	if raw.Size > 0 {
		out.SerializedSize = &raw.Size
	}
	if raw.ExpiryHeight > 0 {
		out.ExpiryHeight = &raw.ExpiryHeight
	}
	actions := int64(len(raw.Orchard.Actions))
	if actions == 0 {
		actions = int64(len(raw.VActionsOrchard))
	}
	out.OrchardActions = &actions
	if includeRaw {
		out.RawTxHex = raw.Hex
	}
	return out, true, nil
}

func (c *Client) Broadcast(ctx context.Context, rawTxHex string) (string, error) {
	var txid string
	err := c.call(ctx, "sendrawtransaction", []any{rawTxHex}, &txid)
	if err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) {
			message := strings.ToLower(rpcErr.Message)
			if rpcErr.Code == -28 || strings.Contains(message, "warming up") || strings.Contains(message, "loading block") {
				return "", &domain.UpstreamError{Kind: "unavailable", Err: errors.New("node is not ready")}
			}
			return "", &domain.UpstreamError{Kind: "rejected", Err: errors.New("node rejected transaction")}
		}
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(txid)), nil
}
