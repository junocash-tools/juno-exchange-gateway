package installation

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func testIdentity() Identity {
	return Identity{
		Network: "regtest",
		Wallets: []WalletIdentity{
			{WalletID: "cold", UFVKFingerprint: strings.Repeat("b", 64), BirthdayHeight: 7},
			{WalletID: "hot", UFVKFingerprint: strings.Repeat("a", 64), BirthdayHeight: 3},
		},
	}
}

func TestCreateIsOneTimeAndContainsNoUFVK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installation", "manifest.json")
	state, manifest, err := Create(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || manifest.InstallationID == "" {
		t.Fatal("expected initialized installation state")
	}
	if _, _, err := Create(path, testIdentity()); err == nil || !strings.Contains(err.Error(), "one-time") {
		t.Fatalf("expected repeated init rejection, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ufvk") && strings.Contains(string(raw), "jview") {
		t.Fatal("manifest must not contain a raw UFVK")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest permissions = %04o, want 0600", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("created directory permissions = %04o, want 0700", info.Mode().Perm())
	}
}

func TestCreateRejectsWritableDirectoryAndUnsafeLock(t *testing.T) {
	t.Run("writable directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "installation")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Create(filepath.Join(dir, "manifest.json"), testIdentity()); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("expected writable directory rejection, got %v", err)
		}
	})

	t.Run("symlink lock", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "installation")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "manifest.json")
		if err := os.Symlink(target, path+".lock"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Create(path, testIdentity()); err == nil || !strings.Contains(err.Error(), "lock must be a regular file") {
			t.Fatalf("expected symlink lock rejection, got %v", err)
		}
	})
}

func TestReservePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	state, _, err := Create(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	for want := uint32(0); want < 2; want++ {
		got, err := state.ReserveAddressIndex("hot")
		if err != nil || got != want {
			t.Fatalf("reserve = %d, %v; want %d", got, err, want)
		}
	}
	reopened, _, err := Open(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.ReserveAddressIndex("hot"); err != nil || got != 2 {
		t.Fatalf("post-restart reserve = %d, %v; want 2", got, err)
	}
}

func TestRaiseHighWaterNeverDecreases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	state, _, err := Create(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RaiseHighWater(map[string]uint64{"hot": 9}); err != nil {
		t.Fatal(err)
	}
	if got, err := state.ReserveAddressIndex("hot"); err != nil || got != 9 {
		t.Fatalf("reserve = %d, %v; want 9", got, err)
	}
	if _, err := state.RaiseHighWater(map[string]uint64{"hot": 4}); err == nil || !strings.Contains(err.Error(), "cannot decrease") {
		t.Fatalf("expected decreasing high-water rejection, got %v", err)
	}
}

func TestSkippedReservationPersistsAndRecoveryClearsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	state, _, err := Create(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	index, err := state.ReserveAddressIndex("hot")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.MarkAddressIndexSkipped("hot", index); err != nil {
		t.Fatal(err)
	}
	_, manifest, err := Open(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	skipped := manifest.Wallets["hot"].SkippedAddressIndices
	if len(skipped) != 1 || skipped[0] != 0 {
		t.Fatalf("skipped indices=%v", skipped)
	}
	manifest, err = state.ClearSkippedAddressIndices()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Wallets["hot"].SkippedAddressIndices) != 0 {
		t.Fatal("recovery did not clear skipped indices")
	}
}

func TestOpenRejectsIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if _, _, err := Create(path, testIdentity()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Identity){
		"network":     func(id *Identity) { id.Network = "testnet" },
		"wallet set":  func(id *Identity) { id.Wallets = id.Wallets[:1] },
		"fingerprint": func(id *Identity) { id.Wallets[0].UFVKFingerprint = strings.Repeat("c", 64) },
		"birthday":    func(id *Identity) { id.Wallets[0].BirthdayHeight++ },
		"account":     func(id *Identity) { id.Wallets[0].Account++ },
	} {
		t.Run(name, func(t *testing.T) {
			identity := testIdentity()
			mutate(&identity)
			if _, _, err := Open(path, identity); err == nil {
				t.Fatal("expected installation identity mismatch")
			}
		})
	}
}

func TestManifestBindsAccountAndAcceptsLegacyAccountZero(t *testing.T) {
	t.Run("new manifest", func(t *testing.T) {
		identity := testIdentity()
		identity.Wallets[0].Account = 7
		path := filepath.Join(t.TempDir(), "manifest.json")
		_, manifest, err := Create(path, identity)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Version != 2 || manifest.Wallets["cold"].Account != 7 {
			t.Fatalf("manifest=%+v", manifest)
		}
		changed := identity
		changed.Wallets = append([]WalletIdentity(nil), identity.Wallets...)
		changed.Wallets[0].Account = 8
		if _, _, err := Open(path, changed); err == nil || !strings.Contains(err.Error(), "identity checksum") {
			t.Fatalf("account drift error=%v", err)
		}
	})

	t.Run("legacy account zero", func(t *testing.T) {
		identity := testIdentity()
		path := filepath.Join(t.TempDir(), "manifest.json")
		_, manifest, err := Create(path, identity)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Version = 1
		manifest.IdentitySHA256 = legacyIdentityChecksum(identity)
		if err := writeAtomic(path, manifest); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Open(path, identity); err != nil {
			t.Fatalf("legacy account zero rejected: %v", err)
		}
		changed := identity
		changed.Wallets = append([]WalletIdentity(nil), identity.Wallets...)
		changed.Wallets[0].Account = 1
		if _, _, err := Open(path, changed); err == nil || !strings.Contains(err.Error(), "account 0") {
			t.Fatalf("legacy account drift error=%v", err)
		}
	})
}

func TestConcurrentReservationsAreUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	state, _, err := Create(path, testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	const count = 50
	got := make([]int, 0, count)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			index, err := state.ReserveAddressIndex("hot")
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			got = append(got, int(index))
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	sort.Ints(got)
	for i, index := range got {
		if index != i {
			t.Fatalf("reservation[%d] = %d", i, index)
		}
	}
}

func TestMappingChecksumIsStableAndBoundToInstallation(t *testing.T) {
	first := NewMappingChecksum("regtest", strings.Repeat("a", 64))
	first.Add("hot", 0, "jregtest1address")
	second := NewMappingChecksum("regtest", strings.Repeat("a", 64))
	second.Add("hot", 0, "jregtest1address")
	if first.Sum() != second.Sum() {
		t.Fatal("same mapping produced different checksums")
	}
	changed := NewMappingChecksum("regtest", strings.Repeat("b", 64))
	changed.Add("hot", 0, "jregtest1address")
	if first.Sum() == changed.Sum() {
		t.Fatal("installation ID must bind the mapping checksum")
	}
	if len(first.Sum()) != 64 {
		t.Fatalf("checksum = %q", first.Sum())
	}
}
