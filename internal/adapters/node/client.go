package node

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

type Client struct {
	url      string
	user     string
	password string
	http     *http.Client
	nextID   atomic.Uint64
}

func New(rawURL, user, password string, timeout time.Duration) *Client {
	return &Client{url: strings.TrimRight(rawURL, "/"), user: user, password: password, http: directHTTPClient(timeout)}
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
	requestID := c.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: requestID, Method: method, Params: params})
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
	if decoded.ID != requestID {
		return &domain.UpstreamError{Kind: "invalid_response", Err: errors.New("node RPC response ID does not match request")}
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
	return normalizeHexIdentifier(hash, "block hash")
}

func (c *Client) DecodeRawTransaction(ctx context.Context, rawTxHex string) (string, error) {
	var decoded struct {
		TxID string `json:"txid"`
	}
	if err := c.call(ctx, "decoderawtransaction", []any{rawTxHex}, &decoded); err != nil {
		return "", err
	}
	return normalizeHexIdentifier(decoded.TxID, "transaction ID")
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
	if raw.Size < 0 || raw.ExpiryHeight < 0 {
		return domain.Transaction{}, false, invalidNodeResponse("node returned negative transaction metadata")
	}
	out := domain.Transaction{TxID: strings.ToLower(strings.TrimSpace(raw.TxID)), Confirmations: raw.Confirmations}
	if out.TxID == "" {
		out.TxID = txid
	}
	normalizedTxID, err := normalizeHexIdentifier(out.TxID, "transaction ID")
	if err != nil {
		return domain.Transaction{}, false, err
	}
	if normalizedTxID != txid {
		return domain.Transaction{}, false, invalidNodeResponse("node returned mismatched transaction ID")
	}
	out.TxID = normalizedTxID
	if out.Confirmations < 0 {
		out.Confirmations = 0
	}
	if raw.BlockHash != "" {
		blockHash, err := normalizeHexIdentifier(raw.BlockHash, "block hash")
		if err != nil {
			return domain.Transaction{}, false, err
		}
		out.BlockHash = blockHash
		if raw.Confirmations > 0 {
			out.State = "confirmed"
		} else {
			out.State = "orphaned"
		}
		var header struct {
			Height int64 `json:"height"`
			Time   int64 `json:"time"`
		}
		if err := c.call(ctx, "getblockheader", []any{blockHash, true}, &header); err != nil {
			return domain.Transaction{}, false, err
		}
		if header.Height < 0 || header.Time < 0 {
			return domain.Transaction{}, false, invalidNodeResponse("node returned negative block metadata")
		}
		out.BlockHeight = &header.Height
		out.BlockTime = &header.Time
	} else {
		if raw.Confirmations > 0 {
			return domain.Transaction{}, false, invalidNodeResponse("confirmed transaction is missing its block hash")
		}
		if raw.Confirmations < 0 {
			out.State = "orphaned"
		} else {
			out.State = "mempool"
		}
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
		rawHex := strings.TrimSpace(raw.Hex)
		if rawHex == "" || len(rawHex)%2 != 0 {
			return domain.Transaction{}, false, invalidNodeResponse("node returned invalid raw transaction hex")
		}
		if _, err := hex.DecodeString(rawHex); err != nil {
			return domain.Transaction{}, false, invalidNodeResponse("node returned invalid raw transaction hex")
		}
		out.RawTxHex = strings.ToLower(rawHex)
	}
	return out, true, nil
}

func normalizeHexIdentifier(value, label string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return "", invalidNodeResponse("node returned invalid " + label)
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return "", invalidNodeResponse("node returned invalid " + label)
		}
	}
	return value, nil
}

func invalidNodeResponse(message string) error {
	return &domain.UpstreamError{Kind: "invalid_response", Err: errors.New(message)}
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
			if rpcErr.Code == -27 || isAlreadyKnownMessage(message) {
				return "", &domain.UpstreamError{Kind: "already_known", Err: errors.New("node already knows transaction")}
			}
			if rpcErr.Code == -22 || isDefinitiveConsensusRejection(rpcErr.Code, message) {
				return "", &domain.UpstreamError{Kind: "rejected", Err: errors.New("node definitively rejected transaction")}
			}
			return "", &domain.UpstreamError{Kind: "uncertain", Err: errors.New("node did not return a definitive broadcast outcome")}
		}
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(txid)), nil
}

func isAlreadyKnownMessage(message string) bool {
	for _, marker := range []string{"txn-already-in-mempool", "txn-already-known", "already in block chain", "already in blockchain", "already known", "already have transaction"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isDefinitiveConsensusRejection(code int, message string) bool {
	if code != -26 {
		return false
	}
	prefix, _, found := strings.Cut(strings.TrimSpace(message), ":")
	if !found {
		return false
	}
	rejectCode, err := strconv.Atoi(strings.TrimSpace(prefix))
	return err == nil && rejectCode == 0x10
}
