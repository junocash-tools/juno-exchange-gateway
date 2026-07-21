package config

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
)

func validConfig(network domain.Network) Config {
	ufvk := map[domain.Network]string{domain.Mainnet: "jview1example", domain.Testnet: "jviewtest1example", domain.Regtest: "jviewregtest1example"}[network]
	return Config{
		Network: network, ListenAddress: ":8080", StateDSN: "file:test.db", NodeRPCURL: "http://node:8232", NodeRPCUser: "rpc", NodeRPCPassword: "secret", ScannerURL: "http://scanner:8080", ScannerToken: "scanner-secret",
		Wallets:              []Wallet{{WalletID: "hot", UFVK: ufvk}},
		Credentials:          []Credential{{Name: "exchange", Token: "012345678901234567890123", Scopes: []string{"read"}, Wallets: []string{"hot"}}},
		DefaultConfirmations: 100, MaxConfirmations: 10000, MaxScannerLag: 2, JSONBodyBytes: 1 << 20, BroadcastBodyBytes: 4 << 20,
		ReadTimeout: time.Second, BroadcastTimeout: time.Second, UpstreamTimeout: time.Second, ShutdownTimeout: time.Second, IdempotencyLease: time.Second,
		BackfillBatchSize: 10000, BackfillYield: time.Millisecond, BackfillTimeout: time.Second,
		WalletEffectsMaxEvents: 10000,
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
