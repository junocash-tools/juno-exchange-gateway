package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/config"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/domain"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/storage"
)

type walletRegistry struct {
	wallets    map[string]config.Wallet
	store      storage.Store
	scanner    domain.Scanner
	deriver    domain.Deriver
	network    domain.Network
	mu         sync.RWMutex
	registered map[string]bool
}

func newWalletRegistry(cfg config.Config, st storage.Store, scanner domain.Scanner, deriver domain.Deriver) (*walletRegistry, error) {
	r := &walletRegistry{wallets: make(map[string]config.Wallet, len(cfg.Wallets)), store: st, scanner: scanner, deriver: deriver, network: cfg.Network, registered: make(map[string]bool, len(cfg.Wallets))}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.UpstreamTimeout)
	defer cancel()
	for _, wallet := range cfg.Wallets {
		r.wallets[wallet.WalletID] = wallet
		stored, exists, err := st.Wallet(ctx, wallet.WalletID)
		if err != nil {
			return nil, err
		}
		if !exists {
			if _, scannerHasWallet, err := scanner.BackfillStatus(ctx, wallet.WalletID); err != nil {
				return nil, fmt.Errorf("verify fresh gateway wallet %s against scanner: %w", wallet.WalletID, err)
			} else if scannerHasWallet {
				return nil, fmt.Errorf("gateway state is missing wallet %q while scanner state retains it; restore the gateway backup to prevent address reuse", wallet.WalletID)
			}
		} else if stored.UFVKFingerprint == "" {
			status, scannerHasWallet, err := scanner.BackfillStatus(ctx, wallet.WalletID)
			if err != nil {
				return nil, fmt.Errorf("verify legacy wallet %s UFVK binding: %w", wallet.WalletID, err)
			}
			if !scannerHasWallet || status.UFVKFingerprint != wallet.UFVKFingerprint() {
				return nil, fmt.Errorf("legacy gateway wallet %q UFVK binding cannot be verified; restore matching scanner state before migration", wallet.WalletID)
			}
		}
		if err := st.EnsureWallet(ctx, wallet.WalletID, string(cfg.Network), wallet.UFVKFingerprint(), wallet.BirthdayHeight); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *walletRegistry) known(walletID string) (config.Wallet, bool) {
	wallet, ok := r.wallets[walletID]
	return wallet, ok
}

func (r *walletRegistry) sync(ctx context.Context) error {
	var errs []error
	for _, wallet := range r.wallets {
		if err := r.scanner.UpsertWallet(ctx, wallet.WalletID, wallet.UFVK, wallet.BirthdayHeight); err != nil {
			r.mu.Lock()
			r.registered[wallet.WalletID] = false
			r.mu.Unlock()
			errs = append(errs, fmt.Errorf("wallet %s: %w", wallet.WalletID, err))
			continue
		}
		r.mu.Lock()
		r.registered[wallet.WalletID] = true
		r.mu.Unlock()
	}
	return errors.Join(errs...)
}

func (r *walletRegistry) completeThrough(ctx context.Context, height int64) (bool, error) {
	r.mu.RLock()
	for walletID := range r.wallets {
		if !r.registered[walletID] {
			r.mu.RUnlock()
			return false, nil
		}
	}
	r.mu.RUnlock()
	for walletID, configured := range r.wallets {
		status, ok, err := r.scanner.BackfillStatus(ctx, walletID)
		if err != nil {
			return false, err
		}
		if !ok || status.UFVKFingerprint != configured.UFVKFingerprint() || status.BirthdayHeight != configured.BirthdayHeight {
			return false, nil
		}
		if err := r.store.SetBackfillProgress(ctx, walletID, status.NextHeight); err != nil {
			return false, err
		}
		if status.State != "complete" || status.NextHeight <= height {
			return false, nil
		}
	}
	return true, nil
}

func (r *walletRegistry) backfillOne(ctx context.Context, tipHeight, batchSize int64) (bool, error) {
	for walletID, configured := range r.wallets {
		status, ok, err := r.scanner.BackfillStatus(ctx, walletID)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.New("registered wallet missing from scanner state")
		}
		if status.UFVKFingerprint != configured.UFVKFingerprint() || status.BirthdayHeight != configured.BirthdayHeight {
			return false, errors.New("scanner wallet identity does not match gateway configuration")
		}
		if err := r.store.SetBackfillProgress(ctx, walletID, status.NextHeight); err != nil {
			return false, err
		}
		if status.State == "complete" && status.NextHeight > tipHeight {
			continue
		}
		next, err := r.scanner.Backfill(ctx, walletID, tipHeight, batchSize)
		if err != nil {
			return false, fmt.Errorf("wallet %s backfill: %w", walletID, err)
		}
		if status.NextHeight <= tipHeight && next <= status.NextHeight {
			return false, errors.New("scanner wallet backfill did not advance")
		}
		if err := r.store.SetBackfillProgress(ctx, walletID, next); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (r *walletRegistry) allocate(ctx context.Context, walletID, label string) (storage.Address, error) {
	wallet, ok := r.known(walletID)
	if !ok {
		return storage.Address{}, errors.New("unknown wallet")
	}
	return r.store.AllocateAddress(ctx, walletID, label, func(index uint32) (string, error) {
		address, err := r.deriver.Derive(ctx, wallet.UFVK, index)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(address, r.network.AddressHRP()+"1") {
			return "", errors.New("derived address has wrong network")
		}
		return address, nil
	})
}
