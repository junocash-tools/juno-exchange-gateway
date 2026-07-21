package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/installation"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage/sqlite"
)

type testDeriver struct{}

func (testDeriver) Derive(_ context.Context, _ string, index uint32) (string, error) {
	return fmt.Sprintf("jregtest1recovered%d", index), nil
}

type testBatchDeriver struct {
	batches int
	singles int
}

type fixedDeriver struct{ address string }

func (d fixedDeriver) Derive(context.Context, string, uint32) (string, error) {
	return d.address, nil
}

type malformedBatchDeriver struct{ addresses []string }

func (d malformedBatchDeriver) Derive(context.Context, string, uint32) (string, error) {
	return "jregtest1single", nil
}

func (d malformedBatchDeriver) DeriveBatch(context.Context, string, uint32, uint32) ([]string, error) {
	return append([]string(nil), d.addresses...), nil
}

func (d *testBatchDeriver) Derive(_ context.Context, _ string, index uint32) (string, error) {
	d.singles++
	return fmt.Sprintf("jregtest1single%d", index), nil
}

func (d *testBatchDeriver) DeriveBatch(_ context.Context, _ string, start, count uint32) ([]string, error) {
	d.batches++
	addresses := make([]string, count)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("jregtest1batch%d", uint64(start)+uint64(i))
	}
	return addresses, nil
}

func lifecycleConfig(root string) config.Config {
	return config.Config{
		Network:               domain.Regtest,
		StateDSN:              "file:" + filepath.Join(root, "gateway.db"),
		InstallationStatePath: filepath.Join(root, "installation", "manifest.json"),
		Wallets: []config.Wallet{{
			WalletID: "hot", UFVK: "jviewregtest1test", BirthdayHeight: 0,
		}},
	}
}

func TestInitServeLossAndAuditedRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := lifecycleConfig(root)
	store, err := sqlite.Open(ctx, cfg.StateDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenForServe(ctx, cfg, store); err == nil || !strings.Contains(err.Error(), "manifest is missing") {
		t.Fatalf("serve before init error=%v", err)
	}
	_, err = Initialize(ctx, cfg, store, testDeriver{}, InitAcknowledgement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(ctx, cfg, store, testDeriver{}, InitAcknowledgement); err == nil || !strings.Contains(err.Error(), "one-time") {
		t.Fatalf("repeat init error=%v", err)
	}
	guardedStore, _, err := OpenForServe(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	for want := uint32(0); want < 2; want++ {
		got, err := guardedStore.AllocateAddress(ctx, "hot", "", func(index uint32) (string, error) {
			return testDeriver{}.Derive(ctx, cfg.Wallets[0].UFVK, index)
		})
		if err != nil || got.DiversifierIndex != want {
			t.Fatalf("allocation=%+v err=%v want=%d", got, err, want)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(root, "gateway.db") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	store, err = sqlite.Open(ctx, cfg.StateDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := OpenForServe(ctx, cfg, store); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("serve after DB loss error=%v", err)
	}
	_, manifest, err := installation.Open(cfg.InstallationStatePath, Identity(cfg))
	if err != nil {
		t.Fatal(err)
	}
	targets, err := RecoveryTargets(manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := RecoveryChecksum(ctx, cfg, manifest, targets, testDeriver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(ctx, cfg, store, testDeriver{}, RecoverAcknowledgement, checksum, nil); err != nil {
		t.Fatal(err)
	}
	guardedStore, _, err = OpenForServe(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := guardedStore.AllocateAddress(ctx, "hot", "", func(index uint32) (string, error) {
		return testDeriver{}.Derive(ctx, cfg.Wallets[0].UFVK, index)
	})
	if err != nil || got.DiversifierIndex != 2 {
		t.Fatalf("post-recovery allocation=%+v err=%v", got, err)
	}
}

func TestInitializeValidatesUFVKBeforeStateMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := lifecycleConfig(root)
	store, err := sqlite.Open(ctx, cfg.StateDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := Initialize(ctx, cfg, store, fixedDeriver{address: "jtest1wrongnetwork"}, InitAcknowledgement); err == nil || !strings.Contains(err.Error(), "network prefix") {
		t.Fatalf("invalid UFVK validation error=%v", err)
	}
	exists, err := installation.Exists(cfg.InstallationStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("invalid UFVK created an installation manifest")
	}
	if _, ok, err := store.InstallationID(ctx); err != nil || ok {
		t.Fatalf("invalid UFVK bound database: ok=%v err=%v", ok, err)
	}
	if err := store.AssertInitializable(ctx); err != nil {
		t.Fatalf("database is no longer initializable: %v", err)
	}
}

func TestExplicitRecoveryHighWaterAndIdentityMismatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := lifecycleConfig(root)
	store, err := sqlite.Open(ctx, cfg.StateDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, manifest, err := installation.Create(cfg.InstallationStatePath, Identity(cfg))
	if err != nil {
		t.Fatal(err)
	}
	targets, err := RecoveryTargets(manifest, map[string]uint64{"hot": 9})
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := RecoveryChecksum(ctx, cfg, manifest, targets, testDeriver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(ctx, cfg, store, testDeriver{}, RecoverAcknowledgement, checksum, map[string]uint64{"hot": 9}); err != nil {
		t.Fatal(err)
	}
	guardedStore, _, err := OpenForServe(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := guardedStore.AllocateAddress(ctx, "hot", "", func(index uint32) (string, error) {
		return testDeriver{}.Derive(ctx, cfg.Wallets[0].UFVK, index)
	})
	if err != nil || got.DiversifierIndex != 9 {
		t.Fatalf("explicit high-water allocation=%+v err=%v", got, err)
	}

	mismatch := cfg
	mismatch.Network = domain.Testnet
	if _, _, err := OpenForServe(ctx, mismatch, store); err == nil || !strings.Contains(err.Error(), "network mismatch") {
		t.Fatalf("network mismatch error=%v", err)
	}
}

func TestAddressCoverageAllowsRecordedSkipsAndRejectsStaleRegistry(t *testing.T) {
	if err := verifyAddressCoverage("hot", 3, []uint32{1}, []uint32{0, 2}); err != nil {
		t.Fatalf("recorded skip should cover the registry: %v", err)
	}
	for name, tc := range map[string]struct {
		next    uint64
		skipped []uint32
		present []uint32
	}{
		"missing suffix":   {next: 3, present: []uint32{0, 1}},
		"missing interior": {next: 3, present: []uint32{0, 2}},
		"manifest stale":   {next: 1, present: []uint32{0, 1}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyAddressCoverage("hot", tc.next, tc.skipped, tc.present); err == nil {
				t.Fatal("expected incomplete/stale registry rejection")
			}
		})
	}
}

func TestRecoveryDerivationUsesBoundedBatches(t *testing.T) {
	deriver := &testBatchDeriver{}
	var visited uint64
	if err := forEachDerivedAddress(context.Background(), deriver, "jregtest1", "ufvk", 20001, func(index uint32, address string) error {
		if uint64(index) != visited || address == "" {
			t.Fatalf("index=%d visited=%d address=%q", index, visited, address)
		}
		visited++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if visited != 20001 || deriver.batches != 3 || deriver.singles != 0 {
		t.Fatalf("visited=%d batches=%d singles=%d", visited, deriver.batches, deriver.singles)
	}
}

func TestRecoveryRejectsMalformedDerivationBeforeBinding(t *testing.T) {
	for name, addresses := range map[string][]string{
		"wrong network": {"jtest1one", "jtest1two"},
		"empty":         {"jregtest1one", ""},
		"duplicate":     {"jregtest1same", "jregtest1same"},
		"short batch":   {"jregtest1one"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			cfg := lifecycleConfig(root)
			store, err := sqlite.Open(ctx, cfg.StateDSN)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, _, err := installation.Create(cfg.InstallationStatePath, Identity(cfg)); err != nil {
				t.Fatal(err)
			}
			deriver := malformedBatchDeriver{addresses: addresses}
			if _, err := Recover(ctx, cfg, store, deriver, RecoverAcknowledgement, strings.Repeat("0", 64), map[string]uint64{"hot": 2}); err == nil {
				t.Fatal("expected malformed recovery derivation rejection")
			}
			if _, ok, err := store.InstallationID(ctx); err != nil || ok {
				t.Fatalf("malformed recovery bound database: ok=%v err=%v", ok, err)
			}
		})
	}

	t.Run("single wrong network", func(t *testing.T) {
		cfg := lifecycleConfig(t.TempDir())
		_, manifest, err := installation.Create(cfg.InstallationStatePath, Identity(cfg))
		if err != nil {
			t.Fatal(err)
		}
		_, err = RecoveryChecksum(context.Background(), cfg, manifest, map[string]uint64{"hot": 1}, fixedDeriver{address: "jtest1wrongnetwork"})
		if err == nil || !strings.Contains(err.Error(), "network prefix") {
			t.Fatalf("single derivation error=%v", err)
		}
	})
}
