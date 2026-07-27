package installation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const manifestVersion = 2

type WalletIdentity struct {
	WalletID        string
	UFVKFingerprint string
	BirthdayHeight  int64
	Account         uint32
}

type Identity struct {
	Network string
	Wallets []WalletIdentity
}

type WalletState struct {
	UFVKFingerprint       string   `json:"ufvk_fingerprint"`
	BirthdayHeight        int64    `json:"birthday_height"`
	Account               uint32   `json:"account,omitempty"`
	NextAddressIndex      uint64   `json:"next_address_index"`
	SkippedAddressIndices []uint32 `json:"skipped_address_indices,omitempty"`
}

type Manifest struct {
	Version        int                    `json:"version"`
	InstallationID string                 `json:"installation_id"`
	Network        string                 `json:"network"`
	IdentitySHA256 string                 `json:"identity_sha256"`
	Wallets        map[string]WalletState `json:"wallets"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// State is the external installation manifest and address high-water ledger.
// Every read-modify-write takes an operating-system file lock so independent
// gateway processes sharing the state directory cannot reserve the same index.
type State struct {
	path     string
	identity Identity
}

func Exists(path string) (bool, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect installation manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("installation manifest must be a regular file, not a symlink")
	}
	return true, nil
}

func Create(path string, identity Identity) (*State, Manifest, error) {
	state, err := newState(path, identity)
	if err != nil {
		return nil, Manifest{}, err
	}
	if err := prepareDirectory(filepath.Dir(state.path)); err != nil {
		return nil, Manifest{}, err
	}

	var created Manifest
	err = state.withLock(true, func() error {
		if _, err := os.Lstat(state.path); err == nil {
			return errors.New("installation manifest already exists; init is one-time only")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect installation manifest: %w", err)
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		created = Manifest{
			Version:        manifestVersion,
			InstallationID: id,
			Network:        identity.Network,
			IdentitySHA256: identityChecksum(identity),
			Wallets:        make(map[string]WalletState, len(identity.Wallets)),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		for _, wallet := range identity.Wallets {
			created.Wallets[wallet.WalletID] = WalletState{
				UFVKFingerprint: wallet.UFVKFingerprint,
				BirthdayHeight:  wallet.BirthdayHeight,
				Account:         wallet.Account,
			}
		}
		return writeAtomic(state.path, created)
	})
	if err != nil {
		return nil, Manifest{}, err
	}
	return state, created, nil
}

func Open(path string, identity Identity) (*State, Manifest, error) {
	state, err := newState(path, identity)
	if err != nil {
		return nil, Manifest{}, err
	}
	exists, err := Exists(state.path)
	if err != nil {
		return nil, Manifest{}, err
	}
	if !exists {
		return nil, Manifest{}, errors.New("installation manifest is missing; run the explicit init command for a new installation or recover for existing state")
	}
	var manifest Manifest
	err = state.withLock(false, func() error {
		var err error
		manifest, err = state.readAndVerify()
		return err
	})
	if err != nil {
		return nil, Manifest{}, err
	}
	return state, manifest, nil
}

func newState(path string, identity Identity) (*State, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("installation manifest path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("installation manifest path must be absolute")
	}
	cleaned := filepath.Clean(path)
	if filepath.Base(cleaned) == "." || filepath.Base(cleaned) == string(filepath.Separator) {
		return nil, errors.New("installation manifest path must name a file")
	}
	identity = canonicalIdentity(identity)
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	return &State{path: cleaned, identity: identity}, nil
}

func (s *State) Manifest() (Manifest, error) {
	var manifest Manifest
	err := s.withLock(false, func() error {
		var err error
		manifest, err = s.readAndVerify()
		return err
	})
	return manifest, err
}

// ReserveAddressIndex durably advances the external high-water mark before
// returning the reserved index. A later database or derivation failure may
// leave a gap, but the reserved index will never be returned again.
func (s *State) ReserveAddressIndex(walletID string) (uint32, error) {
	var reserved uint32
	err := s.withLock(true, func() error {
		manifest, err := s.readAndVerify()
		if err != nil {
			return err
		}
		wallet, ok := manifest.Wallets[walletID]
		if !ok {
			return fmt.Errorf("wallet %q is not part of this installation", walletID)
		}
		if wallet.NextAddressIndex > math.MaxUint32 {
			return fmt.Errorf("wallet %q address index is exhausted", walletID)
		}
		reserved = uint32(wallet.NextAddressIndex)
		wallet.NextAddressIndex++
		manifest.Wallets[walletID] = wallet
		manifest.UpdatedAt = time.Now().UTC()
		return writeAtomic(s.path, manifest)
	})
	return reserved, err
}

// MarkAddressIndexSkipped records a reservation that did not become a database
// address row. If a process crashes before this mark, startup fails closed and
// requires audited recovery rather than guessing whether the DB commit landed.
func (s *State) MarkAddressIndexSkipped(walletID string, index uint32) error {
	return s.withLock(true, func() error {
		manifest, err := s.readAndVerify()
		if err != nil {
			return err
		}
		wallet, ok := manifest.Wallets[walletID]
		if !ok {
			return fmt.Errorf("wallet %q is not part of this installation", walletID)
		}
		if uint64(index) >= wallet.NextAddressIndex {
			return fmt.Errorf("wallet %q index %d was not reserved", walletID, index)
		}
		position := sort.Search(len(wallet.SkippedAddressIndices), func(i int) bool { return wallet.SkippedAddressIndices[i] >= index })
		if position < len(wallet.SkippedAddressIndices) && wallet.SkippedAddressIndices[position] == index {
			return nil
		}
		wallet.SkippedAddressIndices = append(wallet.SkippedAddressIndices, 0)
		copy(wallet.SkippedAddressIndices[position+1:], wallet.SkippedAddressIndices[position:])
		wallet.SkippedAddressIndices[position] = index
		manifest.Wallets[walletID] = wallet
		manifest.UpdatedAt = time.Now().UTC()
		return writeAtomic(s.path, manifest)
	})
}

func (s *State) ClearSkippedAddressIndices() (Manifest, error) {
	var updated Manifest
	err := s.withLock(true, func() error {
		manifest, err := s.readAndVerify()
		if err != nil {
			return err
		}
		for walletID, wallet := range manifest.Wallets {
			wallet.SkippedAddressIndices = nil
			manifest.Wallets[walletID] = wallet
		}
		manifest.UpdatedAt = time.Now().UTC()
		if err := writeAtomic(s.path, manifest); err != nil {
			return err
		}
		updated = manifest
		return nil
	})
	return updated, err
}

// RaiseHighWater advances recovery targets without ever lowering an existing
// high-water mark. MaxUint32+1 is valid and represents an exhausted wallet.
func (s *State) RaiseHighWater(targets map[string]uint64) (Manifest, error) {
	var updated Manifest
	err := s.withLock(true, func() error {
		manifest, err := s.readAndVerify()
		if err != nil {
			return err
		}
		for walletID, target := range targets {
			wallet, ok := manifest.Wallets[walletID]
			if !ok {
				return fmt.Errorf("wallet %q is not part of this installation", walletID)
			}
			if target > uint64(math.MaxUint32)+1 {
				return fmt.Errorf("wallet %q high-water exceeds the address index space", walletID)
			}
			if target < wallet.NextAddressIndex {
				return fmt.Errorf("wallet %q high-water cannot decrease from %d to %d", walletID, wallet.NextAddressIndex, target)
			}
			wallet.NextAddressIndex = target
			manifest.Wallets[walletID] = wallet
		}
		manifest.UpdatedAt = time.Now().UTC()
		if err := writeAtomic(s.path, manifest); err != nil {
			return err
		}
		updated = manifest
		return nil
	})
	return updated, err
}

func (s *State) readAndVerify() (Manifest, error) {
	info, err := os.Lstat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, errors.New("installation manifest is missing; run the explicit init command for a new installation or recover for existing state")
		}
		return Manifest{}, fmt.Errorf("inspect installation manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Manifest{}, errors.New("installation manifest must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Manifest{}, fmt.Errorf("installation manifest permissions %04o are too broad; require 0600", info.Mode().Perm())
	}
	f, err := os.Open(s.path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open installation manifest: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode installation manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.New("installation manifest contains trailing data")
	}
	if err := verifyManifest(manifest, s.identity); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func verifyManifest(manifest Manifest, expected Identity) error {
	if manifest.Version != 1 && manifest.Version != manifestVersion {
		return fmt.Errorf("unsupported installation manifest version %d", manifest.Version)
	}
	if len(manifest.InstallationID) != 64 {
		return errors.New("installation manifest has an invalid installation_id")
	}
	if decoded, err := hex.DecodeString(manifest.InstallationID); err != nil || len(decoded) != 32 || manifest.InstallationID != strings.ToLower(manifest.InstallationID) {
		return errors.New("installation manifest has an invalid installation_id")
	}
	if manifest.Network != expected.Network {
		return fmt.Errorf("installation network mismatch: manifest=%q configured=%q", manifest.Network, expected.Network)
	}
	wantIdentityChecksum := identityChecksum(expected)
	if manifest.Version == 1 {
		for _, wallet := range expected.Wallets {
			if wallet.Account != 0 {
				return fmt.Errorf("installation manifest version 1 binds wallet %q to account 0; configured account=%d", wallet.WalletID, wallet.Account)
			}
		}
		wantIdentityChecksum = legacyIdentityChecksum(expected)
	}
	if manifest.IdentitySHA256 != wantIdentityChecksum {
		return errors.New("installation identity checksum does not match configured network and wallets")
	}
	if len(manifest.Wallets) != len(expected.Wallets) {
		return errors.New("installation wallet set does not match configured wallets")
	}
	for _, wanted := range expected.Wallets {
		got, ok := manifest.Wallets[wanted.WalletID]
		if !ok {
			return fmt.Errorf("installation manifest is missing configured wallet %q", wanted.WalletID)
		}
		if got.UFVKFingerprint != wanted.UFVKFingerprint {
			return fmt.Errorf("installation wallet %q UFVK fingerprint mismatch", wanted.WalletID)
		}
		if got.BirthdayHeight != wanted.BirthdayHeight {
			return fmt.Errorf("installation wallet %q birthday mismatch: manifest=%d configured=%d", wanted.WalletID, got.BirthdayHeight, wanted.BirthdayHeight)
		}
		if got.Account != wanted.Account {
			return fmt.Errorf("installation wallet %q account mismatch: manifest=%d configured=%d", wanted.WalletID, got.Account, wanted.Account)
		}
		if got.NextAddressIndex > uint64(math.MaxUint32)+1 {
			return fmt.Errorf("installation wallet %q has an invalid address high-water", wanted.WalletID)
		}
		for i, index := range got.SkippedAddressIndices {
			if uint64(index) >= got.NextAddressIndex {
				return fmt.Errorf("installation wallet %q has an unreserved skipped address index %d", wanted.WalletID, index)
			}
			if i > 0 && got.SkippedAddressIndices[i-1] >= index {
				return fmt.Errorf("installation wallet %q skipped address indices are not strictly ordered", wanted.WalletID)
			}
		}
	}
	if manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() || manifest.UpdatedAt.Before(manifest.CreatedAt) {
		return errors.New("installation manifest timestamps are invalid")
	}
	return nil
}

func (s *State) withLock(exclusive bool, fn func() error) error {
	dir := filepath.Dir(s.path)
	if err := validateDirectory(dir); err != nil {
		return err
	}
	lockPath := s.path + ".lock"
	var before os.FileInfo
	before, err := os.Lstat(lockPath)
	if err == nil {
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return errors.New("installation lock must be a regular file, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect installation lock: %w", err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open installation lock: %w", err)
	}
	defer lock.Close()
	opened, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened installation lock: %w", err)
	}
	if !opened.Mode().IsRegular() || (before != nil && !os.SameFile(before, opened)) {
		return errors.New("installation lock changed while opening or is not a regular file")
	}
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("secure installation lock: %w", err)
	}
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(lock.Fd()), operation); err != nil {
		return fmt.Errorf("lock installation state: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

func prepareDirectory(dir string) error {
	_, statErr := os.Lstat(dir)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return fmt.Errorf("inspect installation state directory: %w", statErr)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create installation state directory: %w", err)
	}
	if created {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure installation state directory: %w", err)
		}
	}
	return validateDirectory(dir)
}

func validateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect installation state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("installation state directory must be a directory, not a symlink")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("installation state directory permissions %04o are group/world-writable", info.Mode().Perm())
	}
	return nil
}

func writeAtomic(path string, manifest Manifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode installation manifest: %w", err)
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return fmt.Errorf("create installation manifest temporary file: %w", err)
	}
	tmpName := tmp.Name()
	remove := true
	defer func() {
		_ = tmp.Close()
		if remove {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure installation manifest temporary file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write installation manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync installation manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close installation manifest: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace installation manifest: %w", err)
	}
	remove = false
	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open installation state directory for sync: %w", err)
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync installation state directory: %w", err)
	}
	return nil
}

func randomID() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate installation_id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func canonicalIdentity(identity Identity) Identity {
	identity.Network = strings.TrimSpace(identity.Network)
	identity.Wallets = append([]WalletIdentity(nil), identity.Wallets...)
	for i := range identity.Wallets {
		identity.Wallets[i].WalletID = strings.TrimSpace(identity.Wallets[i].WalletID)
		identity.Wallets[i].UFVKFingerprint = strings.TrimSpace(identity.Wallets[i].UFVKFingerprint)
	}
	sort.Slice(identity.Wallets, func(i, j int) bool { return identity.Wallets[i].WalletID < identity.Wallets[j].WalletID })
	return identity
}

func validateIdentity(identity Identity) error {
	switch identity.Network {
	case "mainnet", "testnet", "regtest":
	default:
		return fmt.Errorf("unsupported installation network %q", identity.Network)
	}
	if len(identity.Wallets) == 0 {
		return errors.New("installation requires at least one wallet")
	}
	seen := make(map[string]struct{}, len(identity.Wallets))
	for _, wallet := range identity.Wallets {
		if wallet.WalletID == "" {
			return errors.New("installation wallet_id is required")
		}
		if _, ok := seen[wallet.WalletID]; ok {
			return fmt.Errorf("duplicate installation wallet_id %q", wallet.WalletID)
		}
		seen[wallet.WalletID] = struct{}{}
		if wallet.BirthdayHeight < 0 {
			return fmt.Errorf("installation wallet %q birthday must be non-negative", wallet.WalletID)
		}
		if wallet.Account >= 1<<31 {
			return fmt.Errorf("installation wallet %q account must be below 2147483648", wallet.WalletID)
		}
		if len(wallet.UFVKFingerprint) != 64 {
			return fmt.Errorf("installation wallet %q has an invalid UFVK fingerprint", wallet.WalletID)
		}
		decoded, err := hex.DecodeString(wallet.UFVKFingerprint)
		if err != nil || len(decoded) != sha256.Size || wallet.UFVKFingerprint != strings.ToLower(wallet.UFVKFingerprint) {
			return fmt.Errorf("installation wallet %q has an invalid UFVK fingerprint", wallet.WalletID)
		}
	}
	return nil
}

func identityChecksum(identity Identity) string {
	identity = canonicalIdentity(identity)
	var data bytes.Buffer
	data.WriteString(identity.Network)
	data.WriteByte('\n')
	for _, wallet := range identity.Wallets {
		fmt.Fprintf(&data, "%s\x00%s\x00%d\x00%d\n", wallet.WalletID, wallet.UFVKFingerprint, wallet.BirthdayHeight, wallet.Account)
	}
	sum := sha256.Sum256(data.Bytes())
	return hex.EncodeToString(sum[:])
}

func legacyIdentityChecksum(identity Identity) string {
	identity = canonicalIdentity(identity)
	var data bytes.Buffer
	data.WriteString(identity.Network)
	data.WriteByte('\n')
	for _, wallet := range identity.Wallets {
		fmt.Fprintf(&data, "%s\x00%s\x00%d\n", wallet.WalletID, wallet.UFVKFingerprint, wallet.BirthdayHeight)
	}
	sum := sha256.Sum256(data.Bytes())
	return hex.EncodeToString(sum[:])
}
