package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
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
	if batchSize < 1 {
		return false, errors.New("wallet backfill batch size must be positive")
	}
	type candidate struct {
		walletID  string
		status    domain.BackfillStatus
		remaining int64
	}
	walletIDs := make([]string, 0, len(r.wallets))
	for walletID := range r.wallets {
		walletIDs = append(walletIDs, walletID)
	}
	sort.Strings(walletIDs)
	candidates := make([]candidate, 0, len(walletIDs))
	for _, walletID := range walletIDs {
		configured := r.wallets[walletID]
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
		remaining := tipHeight - status.NextHeight + 1
		if remaining < 1 {
			remaining = 1
		}
		candidates = append(candidates, candidate{walletID: walletID, status: status, remaining: remaining})
	}
	if len(candidates) == 0 {
		return false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].remaining != candidates[j].remaining {
			return candidates[i].remaining > candidates[j].remaining
		}
		return candidates[i].walletID < candidates[j].walletID
	})
	selected := candidates[0]
	effectiveBatch := batchSize
	if effectiveBatch > selected.remaining {
		effectiveBatch = selected.remaining
	}
	next, err := r.scanner.Backfill(ctx, selected.walletID, tipHeight, effectiveBatch)
	if err != nil {
		return false, fmt.Errorf("wallet %s backfill: %w", selected.walletID, err)
	}
	if selected.status.NextHeight <= tipHeight && next <= selected.status.NextHeight {
		return false, errors.New("scanner wallet backfill did not advance")
	}
	if err := r.store.SetBackfillProgress(ctx, selected.walletID, next); err != nil {
		return false, err
	}
	return true, nil
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
