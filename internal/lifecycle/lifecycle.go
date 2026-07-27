package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/installation"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage/guarded"
)

const (
	InitAcknowledgement    = "I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION"
	RecoverAcknowledgement = "I_UNDERSTAND_RECOVERY_REBUILDS_JUNO_ADDRESS_STATE"
)

type InstallationStore interface {
	storage.Store
	AssertInitializable(context.Context) error
	InstallationID(context.Context) (string, bool, error)
	BeginInstallationRecovery(context.Context, string) error
	BindInstallation(context.Context, string) error
	AllocateAddressAt(context.Context, string, uint32, string, storage.DeriveFunc) (storage.Address, error)
	AddressIndices(context.Context, string) ([]uint32, error)
}

type Deriver interface {
	Derive(context.Context, string, uint32) (string, error)
}

type batchDeriver interface {
	DeriveBatch(context.Context, string, uint32, uint32) ([]string, error)
}

func Identity(cfg config.Config) installation.Identity {
	wallets := make([]installation.WalletIdentity, 0, len(cfg.Wallets))
	for _, wallet := range cfg.Wallets {
		wallets = append(wallets, installation.WalletIdentity{
			WalletID:        wallet.WalletID,
			UFVKFingerprint: wallet.UFVKFingerprint(),
			BirthdayHeight:  wallet.BirthdayHeight,
			Account:         wallet.Account,
		})
	}
	return installation.Identity{Network: string(cfg.Network), Wallets: wallets}
}

func Initialize(ctx context.Context, cfg config.Config, store InstallationStore, deriver Deriver, acknowledgement string) (installation.Manifest, error) {
	if acknowledgement != InitAcknowledgement {
		return installation.Manifest{}, fmt.Errorf("init requires exact acknowledgement %q", InitAcknowledgement)
	}
	if exists, err := installation.Exists(cfg.InstallationStatePath); err != nil {
		return installation.Manifest{}, err
	} else if exists {
		return installation.Manifest{}, errors.New("installation manifest already exists; init is one-time only")
	}
	if err := validateConfiguredWallets(ctx, cfg, deriver); err != nil {
		return installation.Manifest{}, err
	}
	if err := store.AssertInitializable(ctx); err != nil {
		return installation.Manifest{}, err
	}
	_, manifest, err := installation.Create(cfg.InstallationStatePath, Identity(cfg))
	if err != nil {
		return installation.Manifest{}, err
	}
	if err := store.BeginInstallationRecovery(ctx, manifest.InstallationID); err != nil {
		return installation.Manifest{}, fmt.Errorf("prepare initialized database binding: %w; the manifest now exists, so use recover rather than init after correcting the cause", err)
	}
	for _, wallet := range cfg.Wallets {
		if err := store.EnsureWallet(ctx, wallet.WalletID, string(cfg.Network), wallet.UFVKFingerprint(), wallet.BirthdayHeight); err != nil {
			return installation.Manifest{}, fmt.Errorf("initialize wallet %q: %w; the manifest now exists, so use recover rather than init after correcting the cause", wallet.WalletID, err)
		}
	}
	if err := store.BindInstallation(ctx, manifest.InstallationID); err != nil {
		return installation.Manifest{}, fmt.Errorf("bind initialized database: %w; the manifest now exists, so use recover rather than init after correcting the cause", err)
	}
	return manifest, nil
}

func OpenForServe(ctx context.Context, cfg config.Config, store InstallationStore) (*guarded.Store, installation.Manifest, error) {
	state, manifest, err := installation.Open(cfg.InstallationStatePath, Identity(cfg))
	if err != nil {
		return nil, installation.Manifest{}, err
	}
	boundID, ok, err := store.InstallationID(ctx)
	if err != nil {
		return nil, installation.Manifest{}, err
	}
	if !ok {
		return nil, installation.Manifest{}, errors.New("gateway database is not bound to this installation; normal serve will not rebuild empty state, run the audited recover command")
	}
	if boundID != manifest.InstallationID {
		return nil, installation.Manifest{}, fmt.Errorf("gateway database installation mismatch: database=%q manifest=%q", boundID, manifest.InstallationID)
	}
	for walletID, wallet := range manifest.Wallets {
		indices, err := store.AddressIndices(ctx, walletID)
		if err != nil {
			return nil, installation.Manifest{}, err
		}
		if err := verifyAddressCoverage(walletID, wallet.NextAddressIndex, wallet.SkippedAddressIndices, indices); err != nil {
			return nil, installation.Manifest{}, err
		}
	}
	wrapped, err := guarded.New(store, state)
	if err != nil {
		return nil, installation.Manifest{}, err
	}
	return wrapped, manifest, nil
}

// RecoveryTargets applies optional per-wallet next-index overrides. Overrides
// may advance a manifest mark, but can never lower it.
func RecoveryTargets(manifest installation.Manifest, overrides map[string]uint64) (map[string]uint64, error) {
	targets := make(map[string]uint64, len(manifest.Wallets))
	for walletID, wallet := range manifest.Wallets {
		targets[walletID] = wallet.NextAddressIndex
	}
	for walletID, target := range overrides {
		current, ok := targets[walletID]
		if !ok {
			return nil, fmt.Errorf("recovery override references unknown wallet %q", walletID)
		}
		if target < current {
			return nil, fmt.Errorf("wallet %q recovery high-water cannot decrease from %d to %d", walletID, current, target)
		}
		if target > uint64(math.MaxUint32)+1 {
			return nil, fmt.Errorf("wallet %q recovery high-water exceeds the address index space", walletID)
		}
		targets[walletID] = target
	}
	return targets, nil
}

func RecoveryChecksum(ctx context.Context, cfg config.Config, manifest installation.Manifest, targets map[string]uint64, deriver Deriver) (string, error) {
	if deriver == nil {
		return "", errors.New("address deriver is required")
	}
	wallets := append([]config.Wallet(nil), cfg.Wallets...)
	sort.Slice(wallets, func(i, j int) bool { return wallets[i].WalletID < wallets[j].WalletID })
	checksum := installation.NewMappingChecksum(string(cfg.Network), manifest.InstallationID)
	for _, wallet := range wallets {
		target, ok := targets[wallet.WalletID]
		if !ok {
			return "", fmt.Errorf("recovery target is missing wallet %q", wallet.WalletID)
		}
		if err := forEachDerivedAddress(ctx, deriver, cfg.Network.AddressHRP()+"1", wallet.UFVK, target, func(index uint32, address string) error {
			checksum.Add(wallet.WalletID, index, strings.TrimSpace(address))
			return nil
		}); err != nil {
			return "", fmt.Errorf("derive wallet %q recovery mapping: %w", wallet.WalletID, err)
		}
	}
	return checksum.Sum(), nil
}

func Recover(ctx context.Context, cfg config.Config, store InstallationStore, deriver Deriver, acknowledgement, expectedChecksum string, overrides map[string]uint64) (installation.Manifest, error) {
	if acknowledgement != RecoverAcknowledgement {
		return installation.Manifest{}, fmt.Errorf("recover requires exact acknowledgement %q", RecoverAcknowledgement)
	}
	state, manifest, err := installation.Open(cfg.InstallationStatePath, Identity(cfg))
	if err != nil {
		return installation.Manifest{}, err
	}
	if existing, ok, err := store.InstallationID(ctx); err != nil {
		return installation.Manifest{}, err
	} else if ok {
		if existing != manifest.InstallationID {
			return installation.Manifest{}, fmt.Errorf("gateway database belongs to installation %q, not %q", existing, manifest.InstallationID)
		}
		return installation.Manifest{}, errors.New("gateway database is already bound to this installation; recovery is not required")
	}
	targets, err := RecoveryTargets(manifest, overrides)
	if err != nil {
		return installation.Manifest{}, err
	}
	expectedChecksum = strings.TrimSpace(expectedChecksum)
	decoded, decodeErr := hex.DecodeString(expectedChecksum)
	if decodeErr != nil || len(decoded) != sha256.Size || expectedChecksum != strings.ToLower(expectedChecksum) {
		return installation.Manifest{}, errors.New("recover requires a 64-character lowercase --mapping-sha256 from recovery-checksum")
	}
	actualChecksum, err := RecoveryChecksum(ctx, cfg, manifest, targets, deriver)
	if err != nil {
		return installation.Manifest{}, err
	}
	if actualChecksum != expectedChecksum {
		return installation.Manifest{}, fmt.Errorf("recovery mapping checksum mismatch: calculated=%s", actualChecksum)
	}
	if err := store.BeginInstallationRecovery(ctx, manifest.InstallationID); err != nil {
		return installation.Manifest{}, err
	}
	manifest, err = state.RaiseHighWater(targets)
	if err != nil {
		return installation.Manifest{}, err
	}

	wallets := append([]config.Wallet(nil), cfg.Wallets...)
	sort.Slice(wallets, func(i, j int) bool { return wallets[i].WalletID < wallets[j].WalletID })
	for _, wallet := range wallets {
		if err := store.EnsureWallet(ctx, wallet.WalletID, string(cfg.Network), wallet.UFVKFingerprint(), wallet.BirthdayHeight); err != nil {
			return installation.Manifest{}, fmt.Errorf("recover wallet %q: %w", wallet.WalletID, err)
		}
		if err := forEachDerivedAddress(ctx, deriver, cfg.Network.AddressHRP()+"1", wallet.UFVK, targets[wallet.WalletID], func(index uint32, address string) error {
			_, err := store.AllocateAddressAt(ctx, wallet.WalletID, index, "", func(requested uint32) (string, error) {
				if requested != index {
					return "", errors.New("storage requested a different recovery index")
				}
				return address, nil
			})
			return err
		}); err != nil {
			return installation.Manifest{}, fmt.Errorf("recover wallet %q registry: %w", wallet.WalletID, err)
		}
	}
	manifest, err = state.ClearSkippedAddressIndices()
	if err != nil {
		return installation.Manifest{}, fmt.Errorf("complete recovered address ledger: %w", err)
	}
	for walletID, wallet := range manifest.Wallets {
		indices, err := store.AddressIndices(ctx, walletID)
		if err != nil {
			return installation.Manifest{}, err
		}
		if err := verifyAddressCoverage(walletID, wallet.NextAddressIndex, nil, indices); err != nil {
			return installation.Manifest{}, err
		}
	}
	if err := store.BindInstallation(ctx, manifest.InstallationID); err != nil {
		return installation.Manifest{}, fmt.Errorf("complete recovered database binding: %w", err)
	}
	return manifest, nil
}

func validateConfiguredWallets(ctx context.Context, cfg config.Config, deriver Deriver) error {
	if deriver == nil {
		return errors.New("address deriver is required")
	}
	prefix := cfg.Network.AddressHRP() + "1"
	seen := make(map[string]string, len(cfg.Wallets))
	for _, wallet := range cfg.Wallets {
		first, err := deriver.Derive(ctx, wallet.UFVK, 0)
		if err != nil {
			return fmt.Errorf("validate wallet %q UFVK: %w", wallet.WalletID, err)
		}
		first, err = validateDerivedAddress(first, prefix)
		if err != nil {
			return fmt.Errorf("validate wallet %q UFVK: %w", wallet.WalletID, err)
		}
		second, err := deriver.Derive(ctx, wallet.UFVK, 0)
		if err != nil {
			return fmt.Errorf("repeat wallet %q UFVK validation: %w", wallet.WalletID, err)
		}
		second, err = validateDerivedAddress(second, prefix)
		if err != nil {
			return fmt.Errorf("repeat wallet %q UFVK validation: %w", wallet.WalletID, err)
		}
		if first != second {
			return fmt.Errorf("wallet %q address derivation is not deterministic", wallet.WalletID)
		}
		if other, ok := seen[first]; ok {
			return fmt.Errorf("wallet %q derives the same first address as wallet %q", wallet.WalletID, other)
		}
		seen[first] = wallet.WalletID
	}
	return nil
}

func forEachDerivedAddress(ctx context.Context, deriver Deriver, addressPrefix, ufvk string, target uint64, visit func(uint32, string) error) error {
	const batchSize = uint64(10000)
	seen := make(map[string]uint32)
	validatedVisit := func(index uint32, raw string) error {
		address, err := validateDerivedAddress(raw, addressPrefix)
		if err != nil {
			return fmt.Errorf("index %d: %w", index, err)
		}
		if previous, ok := seen[address]; ok {
			return fmt.Errorf("index %d duplicates address derived at index %d", index, previous)
		}
		seen[address] = index
		return visit(index, address)
	}
	if batched, ok := deriver.(batchDeriver); ok {
		for start := uint64(0); start < target; {
			if err := ctx.Err(); err != nil {
				return err
			}
			count := target - start
			if count > batchSize {
				count = batchSize
			}
			addresses, err := batched.DeriveBatch(ctx, ufvk, uint32(start), uint32(count))
			if err != nil {
				return fmt.Errorf("batch %d..%d: %w", start, start+count-1, err)
			}
			if len(addresses) != int(count) {
				return fmt.Errorf("batch %d returned %d addresses, want %d", start, len(addresses), count)
			}
			for offset, address := range addresses {
				if err := validatedVisit(uint32(start+uint64(offset)), address); err != nil {
					return err
				}
			}
			start += count
		}
		return nil
	}
	for index := uint64(0); index < target; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		address, err := deriver.Derive(ctx, ufvk, uint32(index))
		if err != nil {
			return fmt.Errorf("index %d: %w", index, err)
		}
		if err := validatedVisit(uint32(index), address); err != nil {
			return err
		}
	}
	return nil
}

func validateDerivedAddress(raw, expectedPrefix string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", errors.New("address deriver returned an empty address")
	}
	if expectedPrefix == "" || !strings.HasPrefix(address, expectedPrefix) {
		return "", fmt.Errorf("derived address does not match expected network prefix %q", expectedPrefix)
	}
	return address, nil
}

func verifyAddressCoverage(walletID string, next uint64, skipped []uint32, present []uint32) error {
	covered := uint64(0)
	i, j := 0, 0
	for i < len(present) || j < len(skipped) {
		var index uint32
		switch {
		case i >= len(present):
			index = skipped[j]
			j++
		case j >= len(skipped):
			index = present[i]
			i++
		case present[i] < skipped[j]:
			index = present[i]
			i++
		case skipped[j] < present[i]:
			index = skipped[j]
			j++
		default:
			index = present[i]
			i++
			j++
		}
		if uint64(index) != covered {
			return fmt.Errorf("gateway address registry for wallet %q is incomplete at index %d; run audited recovery", walletID, covered)
		}
		covered++
	}
	if covered != next {
		return fmt.Errorf("gateway address registry for wallet %q covers %d indices but installation high-water is %d; run audited recovery", walletID, covered, next)
	}
	return nil
}
