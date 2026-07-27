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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

const (
	defaultJSONBodyBytes      = int64(1 << 20)
	defaultBroadcastBodyBytes = int64(4 << 20)
	maxConfirmationsLimit     = int64(10000)
	minInternalSecretLength   = 24
	maxUFVKBytes              = 4096
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Wallet struct {
	WalletID       string `json:"wallet_id"`
	UFVK           string `json:"ufvk"`
	BirthdayHeight int64  `json:"birthday_height"`
	Account        uint32 `json:"account,omitempty"`
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
	Network               domain.Network
	ListenAddress         string
	StateDSN              string
	InstallationStatePath string
	NodeRPCURL            string
	NodeRPCUser           string
	NodeRPCPassword       string
	ScannerURL            string
	ScannerToken          string
	AddrgenPath           string
	Wallets               []Wallet
	Credentials           []Credential

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
	HTTPReadHeaderTimeout  time.Duration
	HTTPReadTimeout        time.Duration
	HTTPWriteTimeout       time.Duration
	HTTPIdleTimeout        time.Duration
	ReadRate               RateLimit
	BroadcastRate          RateLimit
	TrustProxyHeaders      bool
	IdempotencyLease       time.Duration
	BackfillBatchSize      int64
	BackfillYield          time.Duration
	BackfillTimeout        time.Duration
	WalletEffectsMaxEvents int
	NoteSummaryMaxNotes    int

	CoordinatorEnabled       bool
	CoordinatorListenAddress string
	CoordinatorTxbuildPath   string
	CoordinatorSignerSocket  string
	CoordinatorWorkDir       string
	CoordinatorPlanTimeout   time.Duration
	CoordinatorSignTimeout   time.Duration
	CoordinatorMaxBodyBytes  int64
	CoordinatorMaxOutputs    int
	CoordinatorMaxAmountZat  int64
	CoordinatorExpiryOffset  int64
	CoordinatorFeeMultiplier int64
	CoordinatorFeeAddZat     int64
	CoordinatorMinNoteZat    int64
	CoordinatorMinChangeZat  int64
	CoordinatorMaxReplans    int
	CoordinatorRate          RateLimit
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
		Network:                  network,
		ListenAddress:            env("JUNO_GATEWAY_LISTEN", ":8080"),
		StateDSN:                 env("JUNO_GATEWAY_STATE_DSN", "file:/var/lib/juno-gateway/gateway.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"),
		InstallationStatePath:    env("JUNO_GATEWAY_INSTALLATION_STATE", "/var/lib/juno-installation/manifest.json"),
		NodeRPCURL:               env("JUNO_GATEWAY_NODE_RPC_URL", "http://junocashd:8232"),
		NodeRPCUser:              os.Getenv("JUNO_GATEWAY_NODE_RPC_USER"),
		NodeRPCPassword:          os.Getenv("JUNO_GATEWAY_NODE_RPC_PASSWORD"),
		ScannerURL:               env("JUNO_GATEWAY_SCANNER_URL", "http://juno-scan:8080"),
		ScannerToken:             os.Getenv("JUNO_GATEWAY_SCANNER_TOKEN"),
		AddrgenPath:              env("JUNO_GATEWAY_ADDRGEN_PATH", "/usr/local/bin/juno-addrgen"),
		DefaultConfirmations:     envInt64("JUNO_GATEWAY_DEFAULT_CONFIRMATIONS", 100),
		MaxConfirmations:         envInt64("JUNO_GATEWAY_MAX_CONFIRMATIONS", 10000),
		MaxScannerLag:            envInt64("JUNO_GATEWAY_MAX_SCANNER_LAG", 2),
		RequireCompleteHistory:   envBool("JUNO_GATEWAY_REQUIRE_COMPLETE_HISTORY", true),
		JSONBodyBytes:            envInt64("JUNO_GATEWAY_MAX_JSON_BODY_BYTES", defaultJSONBodyBytes),
		BroadcastBodyBytes:       envInt64("JUNO_GATEWAY_MAX_BROADCAST_BODY_BYTES", defaultBroadcastBodyBytes),
		ReadTimeout:              envDuration("JUNO_GATEWAY_READ_TIMEOUT", 15*time.Second),
		BroadcastTimeout:         envDuration("JUNO_GATEWAY_BROADCAST_TIMEOUT", 30*time.Second),
		UpstreamTimeout:          envDuration("JUNO_GATEWAY_UPSTREAM_TIMEOUT", 10*time.Second),
		ShutdownTimeout:          envDuration("JUNO_GATEWAY_SHUTDOWN_TIMEOUT", 15*time.Second),
		HTTPReadHeaderTimeout:    envDuration("JUNO_GATEWAY_HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		HTTPReadTimeout:          envDuration("JUNO_GATEWAY_HTTP_READ_TIMEOUT", 30*time.Second),
		HTTPWriteTimeout:         envDuration("JUNO_GATEWAY_HTTP_WRITE_TIMEOUT", 45*time.Second),
		HTTPIdleTimeout:          envDuration("JUNO_GATEWAY_HTTP_IDLE_TIMEOUT", 60*time.Second),
		ReadRate:                 RateLimit{RPS: envFloat("JUNO_GATEWAY_READ_RATE_RPS", 50), Burst: envInt("JUNO_GATEWAY_READ_RATE_BURST", 100)},
		BroadcastRate:            RateLimit{RPS: envFloat("JUNO_GATEWAY_BROADCAST_RATE_RPS", 2), Burst: envInt("JUNO_GATEWAY_BROADCAST_RATE_BURST", 5)},
		TrustProxyHeaders:        envBool("JUNO_GATEWAY_TRUST_PROXY_HEADERS", false),
		IdempotencyLease:         envDuration("JUNO_GATEWAY_IDEMPOTENCY_LEASE", 30*time.Second),
		BackfillBatchSize:        envInt64("JUNO_GATEWAY_BACKFILL_BATCH_SIZE", 10000),
		BackfillYield:            envDuration("JUNO_GATEWAY_BACKFILL_YIELD", 250*time.Millisecond),
		BackfillTimeout:          envDuration("JUNO_GATEWAY_BACKFILL_TIMEOUT", 10*time.Minute),
		WalletEffectsMaxEvents:   envInt("JUNO_GATEWAY_WALLET_EFFECTS_MAX_EVENTS", 10000),
		NoteSummaryMaxNotes:      envInt("JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES", 100000),
		CoordinatorEnabled:       envBool("JUNO_COORDINATOR_ENABLED", false),
		CoordinatorListenAddress: env("JUNO_COORDINATOR_LISTEN", "127.0.0.1:8081"),
		CoordinatorTxbuildPath:   env("JUNO_COORDINATOR_TXBUILD_PATH", "/usr/local/bin/juno-txbuild"),
		CoordinatorSignerSocket:  env("JUNO_COORDINATOR_SIGNER_SOCKET", "/run/juno-signer/signer.sock"),
		CoordinatorWorkDir:       env("JUNO_COORDINATOR_WORK_DIR", "/var/lib/juno-gateway/coordinator-work"),
		CoordinatorPlanTimeout:   envDuration("JUNO_COORDINATOR_PLAN_TIMEOUT", 2*time.Minute),
		CoordinatorSignTimeout:   envDuration("JUNO_COORDINATOR_SIGN_TIMEOUT", 10*time.Minute),
		CoordinatorMaxBodyBytes:  envInt64("JUNO_COORDINATOR_MAX_BODY_BYTES", defaultJSONBodyBytes),
		CoordinatorMaxOutputs:    envInt("JUNO_COORDINATOR_MAX_OUTPUTS", 199),
		CoordinatorMaxAmountZat:  envInt64("JUNO_COORDINATOR_MAX_AMOUNT_ZAT", 2100000000000000),
		CoordinatorExpiryOffset:  envInt64("JUNO_COORDINATOR_EXPIRY_OFFSET", 40),
		CoordinatorFeeMultiplier: envInt64("JUNO_COORDINATOR_FEE_MULTIPLIER", 20),
		CoordinatorFeeAddZat:     envInt64("JUNO_COORDINATOR_FEE_ADD_ZAT", 0),
		CoordinatorMinNoteZat:    envInt64("JUNO_COORDINATOR_MIN_NOTE_ZAT", 0),
		CoordinatorMinChangeZat:  envInt64("JUNO_COORDINATOR_MIN_CHANGE_ZAT", 0),
		CoordinatorMaxReplans:    envInt("JUNO_COORDINATOR_MAX_REPLANS", 3),
		CoordinatorRate:          RateLimit{RPS: envFloat("JUNO_COORDINATOR_RATE_RPS", 5), Burst: envInt("JUNO_COORDINATOR_RATE_BURST", 10)},
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
	if strings.TrimSpace(c.InstallationStatePath) == "" || !filepath.IsAbs(c.InstallationStatePath) {
		return errors.New("installation state path must be an absolute file path")
	}
	for name, raw := range map[string]string{"node RPC URL": c.NodeRPCURL, "scanner URL": c.ScannerURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be an http(s) URL", name)
		}
	}
	if c.DefaultConfirmations < 1 || c.MaxConfirmations < 1 || c.MaxConfirmations > maxConfirmationsLimit || c.DefaultConfirmations > c.MaxConfirmations {
		return errors.New("confirmation bounds are invalid")
	}
	if c.MaxScannerLag < 0 || c.JSONBodyBytes < 1024 || c.BroadcastBodyBytes < 1024 {
		return errors.New("lag and body limits must be positive")
	}
	if c.ReadTimeout <= 0 || c.BroadcastTimeout <= 0 || c.UpstreamTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return errors.New("timeouts must be positive")
	}
	if c.IdempotencyLease < time.Second {
		return errors.New("idempotency lease must be at least 1s")
	}
	if c.HTTPReadHeaderTimeout <= 0 || c.HTTPReadTimeout <= 0 || c.HTTPWriteTimeout <= 0 || c.HTTPIdleTimeout <= 0 {
		return errors.New("HTTP server timeouts must be positive")
	}
	if c.HTTPReadTimeout < c.HTTPReadHeaderTimeout {
		return errors.New("HTTP read timeout must not be shorter than the read-header timeout")
	}
	if c.HTTPWriteTimeout <= c.BroadcastTimeout {
		return errors.New("HTTP write timeout must be longer than the broadcast request timeout")
	}
	if c.BackfillBatchSize < 1 || c.BackfillBatchSize > 100000 || c.BackfillYield <= 0 || c.BackfillTimeout <= 0 {
		return errors.New("backfill batch size, yield, and timeout are invalid")
	}
	if c.WalletEffectsMaxEvents < 1 || c.WalletEffectsMaxEvents > 100000 {
		return errors.New("wallet effects event cap must be between 1 and 100000")
	}
	if c.NoteSummaryMaxNotes < 1 || c.NoteSummaryMaxNotes > 1000000 {
		return errors.New("note summary cap must be between 1 and 1000000")
	}
	if c.CoordinatorEnabled {
		if strings.TrimSpace(c.CoordinatorListenAddress) == "" || c.CoordinatorListenAddress == c.ListenAddress {
			return errors.New("coordinator listen address must be set and distinct from the public gateway listener")
		}
		for name, path := range map[string]string{
			"coordinator txbuild path":   c.CoordinatorTxbuildPath,
			"coordinator signer socket":  c.CoordinatorSignerSocket,
			"coordinator work directory": c.CoordinatorWorkDir,
		} {
			if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
				return fmt.Errorf("%s must be an absolute path", name)
			}
		}
		if c.CoordinatorPlanTimeout <= 0 || c.CoordinatorSignTimeout <= 0 {
			return errors.New("coordinator timeouts must be positive")
		}
		if c.CoordinatorMaxBodyBytes < 1024 || c.CoordinatorMaxBodyBytes > 8<<20 {
			return errors.New("coordinator body limit must be between 1 KiB and 8 MiB")
		}
		if c.CoordinatorMaxOutputs < 1 || c.CoordinatorMaxOutputs > 199 {
			return errors.New("coordinator max outputs must be between 1 and 199")
		}
		if c.CoordinatorMaxAmountZat < 1 || c.CoordinatorExpiryOffset < 4 || c.CoordinatorExpiryOffset > int64(^uint32(0)) ||
			c.CoordinatorFeeMultiplier < 1 || c.CoordinatorFeeAddZat < 0 || c.CoordinatorMinNoteZat < 0 || c.CoordinatorMinChangeZat < 0 {
			return errors.New("coordinator amount, expiry, and fee policy is invalid")
		}
		if c.CoordinatorMaxReplans < 1 || c.CoordinatorMaxReplans > 20 || c.CoordinatorRate.RPS <= 0 || c.CoordinatorRate.Burst <= 0 {
			return errors.New("coordinator replan and rate limits are invalid")
		}
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
		if len(w.UFVK) > maxUFVKBytes {
			return fmt.Errorf("wallet %q UFVK exceeds %d bytes", w.WalletID, maxUFVKBytes)
		}
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
		if w.Account >= 1<<31 {
			return fmt.Errorf("wallet %q account must be below 2147483648", w.WalletID)
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
	if c.Network != domain.Regtest {
		rpcSecret := strings.TrimSpace(c.NodeRPCPassword)
		scannerSecret := strings.TrimSpace(c.ScannerToken)
		if strings.HasPrefix(strings.ToLower(rpcSecret), "replace-this-") || strings.HasPrefix(strings.ToLower(scannerSecret), "replace-this-") {
			return errors.New("example RPC and scanner credentials are forbidden outside regtest")
		}
		if len(rpcSecret) < minInternalSecretLength || len(scannerSecret) < minInternalSecretLength {
			return fmt.Errorf("RPC and scanner credentials must each be at least %d characters outside regtest", minInternalSecretLength)
		}
		if rpcSecret == scannerSecret {
			return errors.New("RPC and scanner credentials must be distinct outside regtest")
		}
		if isEphemeralStateDSN(c.StateDSN) {
			return errors.New("gateway state must use persistent storage outside regtest")
		}
		if isEphemeralStateDSN(c.InstallationStatePath) {
			return errors.New("installation state must use persistent storage outside regtest")
		}
	}
	seenNames := make(map[string]struct{}, len(c.Credentials))
	seenTokenHashes := make(map[[sha256.Size]byte]string, len(c.Credentials))
	hasCoordinatorCredential := false
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
			if c.Network != domain.Regtest && isExampleTokenHash(cr.TokenSHA256) {
				return fmt.Errorf("credential %q uses the non-authenticating example token hash", cr.Name)
			}
			copy(cr.TokenHash[:], b)
		}
		if otherName, ok := seenTokenHashes[cr.TokenHash]; ok {
			return fmt.Errorf("credential %q reuses the bearer token configured for credential %q", cr.Name, otherName)
		}
		seenTokenHashes[cr.TokenHash] = cr.Name
		if len(cr.Scopes) == 0 {
			return fmt.Errorf("credential %q requires at least one scope", cr.Name)
		}
		for _, scope := range cr.Scopes {
			switch scope {
			case "read", "address", "broadcast", "raw", "treasury", "withdrawal", "plan", "admin":
				if scope == "plan" || scope == "admin" {
					hasCoordinatorCredential = true
				}
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
	if c.CoordinatorEnabled && !hasCoordinatorCredential {
		return errors.New("coordinator requires at least one credential with plan or admin scope")
	}
	return nil
}

func isExampleTokenHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	return strings.Trim(value, value[:1]) == ""
}

func readJSONFile(path string, out any) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("file must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("file permissions %04o are too broad; require 0600", info.Mode().Perm())
	}
	if info.Size() > 1<<20 {
		return errors.New("file exceeds the 1 MiB limit")
	}
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
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
	for _, key := range []string{"JUNO_GATEWAY_DEFAULT_CONFIRMATIONS", "JUNO_GATEWAY_MAX_CONFIRMATIONS", "JUNO_GATEWAY_MAX_SCANNER_LAG", "JUNO_GATEWAY_MAX_JSON_BODY_BYTES", "JUNO_GATEWAY_MAX_BROADCAST_BODY_BYTES", "JUNO_GATEWAY_READ_RATE_BURST", "JUNO_GATEWAY_BROADCAST_RATE_BURST", "JUNO_GATEWAY_BACKFILL_BATCH_SIZE", "JUNO_GATEWAY_WALLET_EFFECTS_MAX_EVENTS", "JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES", "JUNO_COORDINATOR_MAX_BODY_BYTES", "JUNO_COORDINATOR_MAX_OUTPUTS", "JUNO_COORDINATOR_MAX_AMOUNT_ZAT", "JUNO_COORDINATOR_EXPIRY_OFFSET", "JUNO_COORDINATOR_FEE_MULTIPLIER", "JUNO_COORDINATOR_FEE_ADD_ZAT", "JUNO_COORDINATOR_MIN_NOTE_ZAT", "JUNO_COORDINATOR_MIN_CHANGE_ZAT", "JUNO_COORDINATOR_MAX_REPLANS", "JUNO_COORDINATOR_RATE_BURST"} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return fmt.Errorf("%s must be an integer", key)
			}
		}
	}
	for _, key := range []string{"JUNO_GATEWAY_READ_RATE_RPS", "JUNO_GATEWAY_BROADCAST_RATE_RPS", "JUNO_COORDINATOR_RATE_RPS"} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return fmt.Errorf("%s must be a number", key)
			}
		}
	}
	for _, key := range []string{"JUNO_GATEWAY_REQUIRE_COMPLETE_HISTORY", "JUNO_GATEWAY_TRUST_PROXY_HEADERS", "JUNO_COORDINATOR_ENABLED"} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseBool(value); err != nil {
				return fmt.Errorf("%s must be true or false", key)
			}
		}
	}
	for _, key := range []string{"JUNO_GATEWAY_READ_TIMEOUT", "JUNO_GATEWAY_BROADCAST_TIMEOUT", "JUNO_GATEWAY_UPSTREAM_TIMEOUT", "JUNO_GATEWAY_SHUTDOWN_TIMEOUT", "JUNO_GATEWAY_HTTP_READ_HEADER_TIMEOUT", "JUNO_GATEWAY_HTTP_READ_TIMEOUT", "JUNO_GATEWAY_HTTP_WRITE_TIMEOUT", "JUNO_GATEWAY_HTTP_IDLE_TIMEOUT", "JUNO_GATEWAY_IDEMPOTENCY_LEASE", "JUNO_GATEWAY_BACKFILL_YIELD", "JUNO_GATEWAY_BACKFILL_TIMEOUT", "JUNO_COORDINATOR_PLAN_TIMEOUT", "JUNO_COORDINATOR_SIGN_TIMEOUT"} {
		if value := os.Getenv(key); value != "" {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("%s must be a duration", key)
			}
		}
	}
	return nil
}

func isEphemeralStateDSN(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, ":memory:") || strings.Contains(lower, "mode=memory") || strings.Contains(lower, "vfs=memdb") {
		return true
	}

	path := trimmed
	if parsed, err := url.Parse(trimmed); err == nil && strings.EqualFold(parsed.Scheme, "file") {
		path = parsed.Path
		if parsed.Opaque != "" {
			path = parsed.Opaque
		}
	}
	if query := strings.IndexAny(path, "?#"); query >= 0 {
		path = path[:query]
	}
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	path = strings.TrimSpace(path)
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		path = path[len("file:"):]
	}
	for strings.HasPrefix(path, "//") {
		path = path[1:]
	}
	if path == "" || strings.EqualFold(path, ":memory:") {
		return true
	}
	path = strings.TrimRight(path, "/")
	for _, root := range []string{"/tmp", "/var/tmp", "/dev/shm", "/private/tmp", "/var/folders"} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
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
