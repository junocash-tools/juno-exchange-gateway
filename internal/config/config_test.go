package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

func TestReadJSONFileRequiresOwnerOnlyRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.json")
	if err := os.WriteFile(path, []byte(`{"wallets":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out WalletFile
	if err := readJSONFile(path, &out); err == nil {
		t.Fatal("expected broad file mode rejection")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(path, &out); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "wallets-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(link, &out); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func validConfig(network domain.Network) Config {
	ufvk := map[domain.Network]string{domain.Mainnet: "jview1example", domain.Testnet: "jviewtest1example", domain.Regtest: "jviewregtest1example"}[network]
	return Config{
		Network: network, ListenAddress: ":8080", StateDSN: "file:test.db", InstallationStatePath: "/var/lib/juno-installation/manifest.json", NodeRPCURL: "http://node:8232", NodeRPCUser: "rpc", NodeRPCPassword: "0123456789abcdef01234567", ScannerURL: "http://scanner:8080", ScannerToken: "76543210fedcba9876543210",
		Wallets:              []Wallet{{WalletID: "hot", UFVK: ufvk}},
		Credentials:          []Credential{{Name: "exchange", Token: "012345678901234567890123", Scopes: []string{"read"}, Wallets: []string{"hot"}}},
		DefaultConfirmations: 100, MaxConfirmations: 10000, MaxScannerLag: 2, JSONBodyBytes: 1 << 20, BroadcastBodyBytes: 4 << 20,
		ReadTimeout: time.Second, BroadcastTimeout: time.Second, UpstreamTimeout: time.Second, ShutdownTimeout: time.Second, IdempotencyLease: time.Second,
		HTTPReadHeaderTimeout: time.Second, HTTPReadTimeout: 2 * time.Second, HTTPWriteTimeout: 2 * time.Second, HTTPIdleTimeout: time.Second,
		BackfillBatchSize: 10000, BackfillYield: time.Millisecond, BackfillTimeout: time.Second,
		WalletEffectsMaxEvents: 10000,
		NoteSummaryMaxNotes:    100000,
		ReadRate:               RateLimit{RPS: 1, Burst: 1}, BroadcastRate: RateLimit{RPS: 1, Burst: 1},
	}
}

func TestValidateRejectsDuplicateUFVKs(t *testing.T) {
	cfg := validConfig(domain.Mainnet)
	cfg.Wallets = append(cfg.Wallets, Wallet{WalletID: "cold", UFVK: cfg.Wallets[0].UFVK})
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate UFVK rejection")
	}
}

func TestValidateRejectsDuplicateCredentialTokenHashes(t *testing.T) {
	for _, useHash := range []bool{false, true} {
		name := "plaintext"
		if useHash {
			name = "mixed plaintext and hash"
		}
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(domain.Mainnet)
			sharedToken := cfg.Credentials[0].Token
			duplicate := Credential{Name: "second", Token: sharedToken, Scopes: []string{"broadcast"}, Wallets: []string{"hot"}}
			if useHash {
				sum := sha256.Sum256([]byte(sharedToken))
				duplicate.Token = ""
				duplicate.TokenSHA256 = hex.EncodeToString(sum[:])
			}
			cfg.Credentials = append(cfg.Credentials, duplicate)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reuses the bearer token") {
				t.Fatalf("duplicate token error=%v", err)
			}
		})
	}
}

func TestValidateRejectsOversizedUFVK(t *testing.T) {
	cfg := validConfig(domain.Regtest)
	cfg.Credentials = nil
	cfg.Wallets[0].UFVK = "jviewregtest1" + strings.Repeat("a", maxUFVKBytes)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized UFVK error=%v", err)
	}
}

func TestValidateSupportsAllNetworks(t *testing.T) {
	for _, network := range []domain.Network{domain.Mainnet, domain.Testnet, domain.Regtest} {
		t.Run(string(network), func(t *testing.T) {
			cfg := validConfig(network)
			if network == domain.Regtest {
				cfg.Credentials = nil
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateCoordinatorSupportsAllNetworksAndRequiresPlanScope(t *testing.T) {
	for _, network := range []domain.Network{domain.Mainnet, domain.Testnet, domain.Regtest} {
		t.Run(string(network), func(t *testing.T) {
			cfg := validConfig(network)
			cfg.CoordinatorEnabled = true
			cfg.CoordinatorListenAddress = "127.0.0.1:8081"
			cfg.CoordinatorTxbuildPath = "/usr/local/bin/juno-txbuild"
			cfg.CoordinatorSignerSocket = "/run/juno-signer/signer.sock"
			cfg.CoordinatorWorkDir = "/var/lib/juno-gateway/coordinator-work"
			cfg.CoordinatorPlanTimeout = 2 * time.Minute
			cfg.CoordinatorSignTimeout = 10 * time.Minute
			cfg.CoordinatorMaxBodyBytes = 1 << 20
			cfg.CoordinatorMaxOutputs = 199
			cfg.CoordinatorMaxAmountZat = 2_100_000_000_000_000
			cfg.CoordinatorExpiryOffset = 40
			cfg.CoordinatorFeeMultiplier = 20
			cfg.CoordinatorMaxReplans = 3
			cfg.CoordinatorRate = RateLimit{RPS: 5, Burst: 10}
			cfg.Credentials[0].Scopes = []string{"plan"}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}

	cfg := validConfig(domain.Regtest)
	cfg.CoordinatorEnabled = true
	cfg.CoordinatorListenAddress = "127.0.0.1:8081"
	cfg.CoordinatorTxbuildPath = "/usr/local/bin/juno-txbuild"
	cfg.CoordinatorSignerSocket = "/run/juno-signer/signer.sock"
	cfg.CoordinatorWorkDir = "/var/lib/juno-gateway/coordinator-work"
	cfg.CoordinatorPlanTimeout = time.Minute
	cfg.CoordinatorSignTimeout = time.Minute
	cfg.CoordinatorMaxBodyBytes = 1 << 20
	cfg.CoordinatorMaxOutputs = 1
	cfg.CoordinatorMaxAmountZat = 1
	cfg.CoordinatorExpiryOffset = 40
	cfg.CoordinatorFeeMultiplier = 20
	cfg.CoordinatorMaxReplans = 1
	cfg.CoordinatorRate = RateLimit{RPS: 1, Burst: 1}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "plan or admin scope") {
		t.Fatalf("missing coordinator credential error=%v", err)
	}
}

func TestValidateEnvironmentRejectsInvalidCoordinatorValues(t *testing.T) {
	for key, value := range map[string]string{
		"JUNO_COORDINATOR_ENABLED":      "sometimes",
		"JUNO_COORDINATOR_PLAN_TIMEOUT": "tomorrow",
		"JUNO_COORDINATOR_RATE_RPS":     "fast",
		"JUNO_COORDINATOR_MAX_REPLANS":  "many",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if err := validateEnvironment(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("validation error=%v", err)
			}
		})
	}
}

func TestValidateAcceptsWithdrawalScopeAndCapsConfirmationOverride(t *testing.T) {
	cfg := validConfig(domain.Mainnet)
	cfg.Credentials[0].Scopes = []string{"read", "withdrawal"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	cfg = validConfig(domain.Mainnet)
	cfg.MaxConfirmations = maxConfirmationsLimit + 1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "confirmation bounds") {
		t.Fatalf("confirmation ceiling error=%v", err)
	}
}

func TestValidateEnvironmentRejectsInvalidNoteSummaryCap(t *testing.T) {
	t.Setenv("JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES", "not-an-integer")
	if err := validateEnvironment(); err == nil || !strings.Contains(err.Error(), "JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES") {
		t.Fatalf("note summary cap error=%v", err)
	}
}

func TestValidateRejectsNetworkMismatchAndMissingProductionAuth(t *testing.T) {
	cfg := validConfig(domain.Testnet)
	cfg.Wallets[0].UFVK = "jview1wrong"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected UFVK network mismatch")
	}
	cfg = validConfig(domain.Mainnet)
	cfg.Credentials = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected authentication requirement")
	}
}

func TestValidateRejectsExampleProductionCredentials(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"node RPC": func(cfg *Config) { cfg.NodeRPCPassword = "replace-this-regtest-rpc-password" },
		"scanner":  func(cfg *Config) { cfg.ScannerToken = "replace-this-internal-scanner-token" },
		"gateway":  func(cfg *Config) { cfg.Credentials[0].Token = "replace-this-example-token-1234567890" },
		"gateway hash": func(cfg *Config) {
			cfg.Credentials[0].Token = ""
			cfg.Credentials[0].TokenSHA256 = strings.Repeat("0", sha256.Size*2)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(domain.Mainnet)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected example credential rejection")
			}
		})
	}

	cfg := validConfig(domain.Regtest)
	cfg.NodeRPCPassword = "replace-this-regtest-rpc-password"
	cfg.ScannerToken = "replace-this-internal-scanner-token"
	cfg.Credentials = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("isolated regtest defaults should remain usable: %v", err)
	}
}

func TestValidateRejectsRepeatedDigitExampleTokenHashes(t *testing.T) {
	cfg := validConfig(domain.Mainnet)
	cfg.Credentials[0].Token = ""
	cfg.Credentials[0].TokenSHA256 = strings.Repeat("1", sha256.Size*2)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "example token hash") {
		t.Fatalf("example hash error=%v", err)
	}
}

func TestValidateRejectsWeakOrSharedInternalSecrets(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"weak RPC secret":     func(cfg *Config) { cfg.NodeRPCPassword = "short" },
		"weak scanner secret": func(cfg *Config) { cfg.ScannerToken = "short" },
		"shared secret":       func(cfg *Config) { cfg.ScannerToken = cfg.NodeRPCPassword },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(domain.Mainnet)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected internal credential rejection")
			}
		})
	}
}

func TestValidateRejectsEphemeralProductionState(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:?cache=shared", "file:gateway?vfs=memdb", "file:/tmp/gateway.db", "file:/dev/shm/gateway.db"} {
		t.Run(dsn, func(t *testing.T) {
			cfg := validConfig(domain.Mainnet)
			cfg.StateDSN = dsn
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected ephemeral state rejection")
			}
		})
	}

	cfg := validConfig(domain.Regtest)
	cfg.StateDSN = ":memory:"
	cfg.Credentials = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("regtest should permit ephemeral state: %v", err)
	}
}

func TestValidateRejectsUnsafeHTTPTimeouts(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"zero read header": func(cfg *Config) { cfg.HTTPReadHeaderTimeout = 0 },
		"short read":       func(cfg *Config) { cfg.HTTPReadTimeout = cfg.HTTPReadHeaderTimeout - time.Nanosecond },
		"short write":      func(cfg *Config) { cfg.HTTPWriteTimeout = cfg.BroadcastTimeout },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(domain.Mainnet)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected HTTP timeout rejection")
			}
		})
	}
}

func TestValidateRejectsSubsecondIdempotencyLease(t *testing.T) {
	cfg := validConfig(domain.Mainnet)
	cfg.IdempotencyLease = time.Second - time.Nanosecond
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least 1s") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateRequiresAbsoluteInstallationStatePath(t *testing.T) {
	cfg := validConfig(domain.Regtest)
	cfg.InstallationStatePath = "installation/manifest.json"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected relative installation path rejection")
	}
}

func TestValidateRejectsEphemeralProductionInstallationState(t *testing.T) {
	cfg := validConfig(domain.Mainnet)
	cfg.InstallationStatePath = "/tmp/juno-installation/manifest.json"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected ephemeral installation-state rejection")
	}
}

func TestValidateHashesPlaintextAndClearsIt(t *testing.T) {
	cfg := validConfig(domain.Mainnet)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Credentials[0].Token != "" {
		t.Fatal("plaintext token retained")
	}
	if cfg.Credentials[0].TokenHash != sha256.Sum256([]byte("012345678901234567890123")) {
		t.Fatal("wrong token hash")
	}
}
