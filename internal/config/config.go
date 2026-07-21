package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
)

const (
	defaultJSONBodyBytes      = int64(1 << 20)
	defaultBroadcastBodyBytes = int64(4 << 20)
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Wallet struct {
	WalletID       string `json:"wallet_id"`
	UFVK           string `json:"ufvk"`
	BirthdayHeight int64  `json:"birthday_height"`
}

func (w Wallet) UFVKFingerprint() string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(w.UFVK)))
	return hex.EncodeToString(sum[:])
}

type CredentialFile struct {
	Credentials []Credential `json:"credentials"`
}

type Credential struct {
	Name        string   `json:"name"`
	Token       string   `json:"token,omitempty"`
	TokenSHA256 string   `json:"token_sha256,omitempty"`
	Scopes      []string `json:"scopes"`
	Wallets     []string `json:"wallets"`
	TokenHash   [32]byte `json:"-"`
}

type WalletFile struct {
	Wallets []Wallet `json:"wallets"`
}

type RateLimit struct {
	RPS   float64
	Burst int
}

type Config struct {
	Network         domain.Network
	ListenAddress   string
	StateDSN        string
	NodeRPCURL      string
	NodeRPCUser     string
	NodeRPCPassword string
	ScannerURL      string
	ScannerToken    string
	AddrgenPath     string
	Wallets         []Wallet
	Credentials     []Credential

	DefaultConfirmations   int64
	MaxConfirmations       int64
	MaxScannerLag          int64
	RequireCompleteHistory bool
	JSONBodyBytes          int64
	BroadcastBodyBytes     int64
	ReadTimeout            time.Duration
	BroadcastTimeout       time.Duration
	UpstreamTimeout        time.Duration
	ShutdownTimeout        time.Duration
	ReadRate               RateLimit
	BroadcastRate          RateLimit
	TrustProxyHeaders      bool
	IdempotencyLease       time.Duration
	BackfillBatchSize      int64
	BackfillYield          time.Duration
	BackfillTimeout        time.Duration
	WalletEffectsMaxEvents int
}

func Load() (Config, error) {
	if err := validateEnvironment(); err != nil {
		return Config{}, err
	}
	network, err := domain.ParseNetwork(env("JUNO_GATEWAY_NETWORK", "regtest"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Network:                network,
		ListenAddress:          env("JUNO_GATEWAY_LISTEN", ":8080"),
		StateDSN:               env("JUNO_GATEWAY_STATE_DSN", "file:/var/lib/juno-gateway/gateway.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"),
		NodeRPCURL:             env("JUNO_GATEWAY_NODE_RPC_URL", "http://junocashd:8232"),
		NodeRPCUser:            os.Getenv("JUNO_GATEWAY_NODE_RPC_USER"),
		NodeRPCPassword:        os.Getenv("JUNO_GATEWAY_NODE_RPC_PASSWORD"),
		ScannerURL:             env("JUNO_GATEWAY_SCANNER_URL", "http://juno-scan:8080"),
		ScannerToken:           os.Getenv("JUNO_GATEWAY_SCANNER_TOKEN"),
		AddrgenPath:            env("JUNO_GATEWAY_ADDRGEN_PATH", "/usr/local/bin/juno-addrgen"),
		DefaultConfirmations:   envInt64("JUNO_GATEWAY_DEFAULT_CONFIRMATIONS", 100),
		MaxConfirmations:       envInt64("JUNO_GATEWAY_MAX_CONFIRMATIONS", 10000),
		MaxScannerLag:          envInt64("JUNO_GATEWAY_MAX_SCANNER_LAG", 2),
		RequireCompleteHistory: envBool("JUNO_GATEWAY_REQUIRE_COMPLETE_HISTORY", true),
		JSONBodyBytes:          envInt64("JUNO_GATEWAY_MAX_JSON_BODY_BYTES", defaultJSONBodyBytes),
		BroadcastBodyBytes:     envInt64("JUNO_GATEWAY_MAX_BROADCAST_BODY_BYTES", defaultBroadcastBodyBytes),
		ReadTimeout:            envDuration("JUNO_GATEWAY_READ_TIMEOUT", 15*time.Second),
		BroadcastTimeout:       envDuration("JUNO_GATEWAY_BROADCAST_TIMEOUT", 30*time.Second),
		UpstreamTimeout:        envDuration("JUNO_GATEWAY_UPSTREAM_TIMEOUT", 10*time.Second),
		ShutdownTimeout:        envDuration("JUNO_GATEWAY_SHUTDOWN_TIMEOUT", 15*time.Second),
		ReadRate:               RateLimit{RPS: envFloat("JUNO_GATEWAY_READ_RATE_RPS", 50), Burst: envInt("JUNO_GATEWAY_READ_RATE_BURST", 100)},
		BroadcastRate:          RateLimit{RPS: envFloat("JUNO_GATEWAY_BROADCAST_RATE_RPS", 2), Burst: envInt("JUNO_GATEWAY_BROADCAST_RATE_BURST", 5)},
		TrustProxyHeaders:      envBool("JUNO_GATEWAY_TRUST_PROXY_HEADERS", false),
		IdempotencyLease:       envDuration("JUNO_GATEWAY_IDEMPOTENCY_LEASE", 30*time.Second),
		BackfillBatchSize:      envInt64("JUNO_GATEWAY_BACKFILL_BATCH_SIZE", 10000),
		BackfillYield:          envDuration("JUNO_GATEWAY_BACKFILL_YIELD", 250*time.Millisecond),
		BackfillTimeout:        envDuration("JUNO_GATEWAY_BACKFILL_TIMEOUT", 10*time.Minute),
		WalletEffectsMaxEvents: envInt("JUNO_GATEWAY_WALLET_EFFECTS_MAX_EVENTS", 10000),
	}

	if path := strings.TrimSpace(os.Getenv("JUNO_GATEWAY_WALLETS_FILE")); path != "" {
		var f WalletFile
		if err := readJSONFile(path, &f); err != nil {
			return Config{}, fmt.Errorf("wallets file: %w", err)
		}
		cfg.Wallets = f.Wallets
	}
	if path := strings.TrimSpace(os.Getenv("JUNO_GATEWAY_AUTH_FILE")); path != "" {
		var f CredentialFile
		if err := readJSONFile(path, &f); err != nil {
			return Config{}, fmt.Errorf("auth file: %w", err)
		}
		cfg.Credentials = f.Credentials
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Network.AddressHRP() == "" {
		return errors.New("unsupported network")
	}
	if strings.TrimSpace(c.ListenAddress) == "" || strings.TrimSpace(c.StateDSN) == "" {
		return errors.New("listen address and state DSN are required")
	}
	for name, raw := range map[string]string{"node RPC URL": c.NodeRPCURL, "scanner URL": c.ScannerURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be an http(s) URL", name)
		}
	}
	if c.DefaultConfirmations < 0 || c.MaxConfirmations < 1 || c.DefaultConfirmations > c.MaxConfirmations {
		return errors.New("confirmation bounds are invalid")
	}
	if c.MaxScannerLag < 0 || c.JSONBodyBytes < 1024 || c.BroadcastBodyBytes < 1024 {
		return errors.New("lag and body limits must be positive")
	}
	if c.ReadTimeout <= 0 || c.BroadcastTimeout <= 0 || c.UpstreamTimeout <= 0 || c.ShutdownTimeout <= 0 || c.IdempotencyLease <= 0 {
		return errors.New("timeouts and idempotency lease must be positive")
	}
	if c.BackfillBatchSize < 1 || c.BackfillBatchSize > 100000 || c.BackfillYield <= 0 || c.BackfillTimeout <= 0 {
		return errors.New("backfill batch size, yield, and timeout are invalid")
	}
	if c.WalletEffectsMaxEvents < 1 || c.WalletEffectsMaxEvents > 100000 {
		return errors.New("wallet effects event cap must be between 1 and 100000")
	}
	if c.ReadRate.RPS <= 0 || c.ReadRate.Burst <= 0 || c.BroadcastRate.RPS <= 0 || c.BroadcastRate.Burst <= 0 {
		return errors.New("rate limits must be positive")
	}
	if len(c.Wallets) == 0 {
		return errors.New("at least one registered wallet is required")
	}
	seenWallets := make(map[string]struct{}, len(c.Wallets))
	seenUFVKs := make(map[string]string, len(c.Wallets))
	ufvkPrefix := c.Network.UFVKHRP() + "1"
	for i := range c.Wallets {
		w := &c.Wallets[i]
		w.WalletID = strings.TrimSpace(w.WalletID)
		w.UFVK = strings.TrimSpace(w.UFVK)
		if !safeID.MatchString(w.WalletID) {
			return fmt.Errorf("wallet %d has invalid wallet_id", i)
		}
		if _, exists := seenWallets[w.WalletID]; exists {
			return fmt.Errorf("duplicate wallet_id %q", w.WalletID)
		}
		seenWallets[w.WalletID] = struct{}{}
		if !strings.HasPrefix(w.UFVK, ufvkPrefix) {
			return fmt.Errorf("wallet %q UFVK does not match %s", w.WalletID, c.Network)
		}
		fingerprint := w.UFVKFingerprint()
		if otherWalletID, exists := seenUFVKs[fingerprint]; exists {
			return fmt.Errorf("wallet %q duplicates the UFVK configured for wallet %q", w.WalletID, otherWalletID)
		}
		seenUFVKs[fingerprint] = w.WalletID
		if w.BirthdayHeight < 0 {
			return fmt.Errorf("wallet %q birthday_height must be non-negative", w.WalletID)
		}
	}
	if c.Network != domain.Regtest && len(c.Credentials) == 0 {
		return errors.New("authentication is required outside regtest")
	}
	if c.Network != domain.Regtest && (strings.TrimSpace(c.NodeRPCUser) == "" || strings.TrimSpace(c.NodeRPCPassword) == "") {
		return errors.New("authenticated node RPC is required outside regtest")
	}
	if c.Network != domain.Regtest && strings.TrimSpace(c.ScannerToken) == "" {
		return errors.New("scanner authentication is required outside regtest")
	}
	if c.Network != domain.Regtest && (strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.NodeRPCPassword)), "replace-this-") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.ScannerToken)), "replace-this-")) {
		return errors.New("example RPC and scanner credentials are forbidden outside regtest")
	}
	seenNames := make(map[string]struct{}, len(c.Credentials))
	for i := range c.Credentials {
		cr := &c.Credentials[i]
		cr.Name = strings.TrimSpace(cr.Name)
		if !safeID.MatchString(cr.Name) {
			return fmt.Errorf("credential %d has invalid name", i)
		}
		if _, ok := seenNames[cr.Name]; ok {
			return fmt.Errorf("duplicate credential name %q", cr.Name)
		}
		seenNames[cr.Name] = struct{}{}
		hasToken := strings.TrimSpace(cr.Token) != ""
		hasHash := strings.TrimSpace(cr.TokenSHA256) != ""
		if hasToken == hasHash {
			return fmt.Errorf("credential %q must set exactly one token or token_sha256", cr.Name)
		}
		if hasToken {
			if len(cr.Token) < 24 {
				return fmt.Errorf("credential %q token must be at least 24 characters", cr.Name)
			}
			if c.Network != domain.Regtest && strings.HasPrefix(strings.ToLower(strings.TrimSpace(cr.Token)), "replace-this-") {
				return fmt.Errorf("credential %q uses an example token", cr.Name)
			}
			cr.TokenHash = sha256.Sum256([]byte(cr.Token))
			cr.Token = ""
		} else {
			b, err := hex.DecodeString(cr.TokenSHA256)
			if err != nil || len(b) != sha256.Size {
				return fmt.Errorf("credential %q token_sha256 must be 64 lowercase hex characters", cr.Name)
			}
			if cr.TokenSHA256 != strings.ToLower(cr.TokenSHA256) {
				return fmt.Errorf("credential %q token_sha256 must be lowercase", cr.Name)
			}
			if c.Network != domain.Regtest && cr.TokenSHA256 == strings.Repeat("0", sha256.Size*2) {
				return fmt.Errorf("credential %q uses the non-authenticating example token hash", cr.Name)
			}
			copy(cr.TokenHash[:], b)
		}
		if len(cr.Scopes) == 0 {
			return fmt.Errorf("credential %q requires at least one scope", cr.Name)
		}
		for _, scope := range cr.Scopes {
			switch scope {
			case "read", "address", "broadcast", "raw", "admin":
			default:
				return fmt.Errorf("credential %q has invalid scope %q", cr.Name, scope)
			}
		}
		if len(cr.Wallets) == 0 {
			return fmt.Errorf("credential %q requires wallet authorization", cr.Name)
		}
		for _, walletID := range cr.Wallets {
			if walletID == "*" {
				continue
			}
			if _, ok := seenWallets[walletID]; !ok {
				return fmt.Errorf("credential %q references unknown wallet %q", cr.Name, walletID)
			}
		}
	}
	return nil
}

func readJSONFile(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("file must contain one JSON object")
	}
	return nil
}

func validateEnvironment() error {
	for _, key := range []string{"JUNO_GATEWAY_DEFAULT_CONFIRMATIONS", "JUNO_GATEWAY_MAX_CONFIRMATIONS", "JUNO_GATEWAY_MAX_SCANNER_LAG", "JUNO_GATEWAY_MAX_JSON_BODY_BYTES", "JUNO_GATEWAY_MAX_BROADCAST_BODY_BYTES", "JUNO_GATEWAY_READ_RATE_BURST", "JUNO_GATEWAY_BROADCAST_RATE_BURST", "JUNO_GATEWAY_BACKFILL_BATCH_SIZE", "JUNO_GATEWAY_WALLET_EFFECTS_MAX_EVENTS"} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
		}
	}
	for _, key := range []string{"JUNO_GATEWAY_READ_RATE_RPS", "JUNO_GATEWAY_BROADCAST_RATE_RPS"} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return fmt.Errorf("%s must be a number", key)
			}
		}
	}
	for _, key := range []string{"JUNO_GATEWAY_REQUIRE_COMPLETE_HISTORY", "JUNO_GATEWAY_TRUST_PROXY_HEADERS"} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseBool(value); err != nil {
				return fmt.Errorf("%s must be true or false", key)
			}
		}
	}
	for _, key := range []string{"JUNO_GATEWAY_READ_TIMEOUT", "JUNO_GATEWAY_BROADCAST_TIMEOUT", "JUNO_GATEWAY_UPSTREAM_TIMEOUT", "JUNO_GATEWAY_SHUTDOWN_TIMEOUT", "JUNO_GATEWAY_IDEMPOTENCY_LEASE", "JUNO_GATEWAY_BACKFILL_YIELD", "JUNO_GATEWAY_BACKFILL_TIMEOUT"} {
		if value := os.Getenv(key); value != "" {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("%s must be a duration", key)
			}
		}
	}
	return nil
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
func envInt64(k string, d int64) int64 {
	if v := os.Getenv(k); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil {
			return n
		}
	}
	return d
}
func envInt(k string, d int) int { return int(envInt64(k, int64(d))) }
func envFloat(k string, d float64) float64 {
	if v := os.Getenv(k); v != "" {
		if n, e := strconv.ParseFloat(v, 64); e == nil {
			return n
		}
	}
	return d
}
func envBool(k string, d bool) bool {
	if v := os.Getenv(k); v != "" {
		if n, e := strconv.ParseBool(v); e == nil {
			return n
		}
	}
	return d
}
func envDuration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if n, e := time.ParseDuration(v); e == nil {
			return n
		}
	}
	return d
}
