package guarded_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junocash-tools/juno-exchange-gateway/internal/installation"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage/guarded"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage/sqlite"
)

func TestDerivationFailureSkipsReservedIndex(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := sqlite.Open(ctx, "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprint := strings.Repeat("a", 64)
	if err := store.EnsureWallet(ctx, "hot", "regtest", fingerprint, 0); err != nil {
		t.Fatal(err)
	}
	state, _, err := installation.Create(filepath.Join(t.TempDir(), "manifest.json"), installation.Identity{
		Network: "regtest",
		Wallets: []installation.WalletIdentity{{WalletID: "hot", UFVKFingerprint: fingerprint}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := guarded.New(store, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.AllocateAddress(ctx, "hot", "failed", func(index uint32) (string, error) {
		if index != 0 {
			t.Fatalf("first reservation=%d", index)
		}
		return "", errors.New("derivation failed")
	}); err == nil {
		t.Fatal("expected derivation error")
	}
	got, err := wrapped.AllocateAddress(ctx, "hot", "success", func(index uint32) (string, error) {
		return fmt.Sprintf("jregtest1address%d", index), nil
	})
	if err != nil || got.DiversifierIndex != 1 {
		t.Fatalf("allocation after failure=%+v err=%v", got, err)
	}
}
