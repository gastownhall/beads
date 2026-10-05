//go:build !cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// nocgoSharedServerPort is a distinctive port nothing listens on, pinned through
// BEADS_DOLT_SERVER_PORT (the highest-priority port source in
// internal/doltserver) and reused by the assertion so the two cannot drift
// apart. Distinct from the cgo half's readOnlySharedServerPort and the prime
// test's sharedServerPrimePort only so a failure names which test dialed.
//
// Below 32768, i.e. outside the kernel's ephemeral range (32768-60999 here and
// on the GitHub runners). That matters now that the assertion means "we dialed
// THIS port and were refused": a process that bound :0 and happened to land
// here would answer the dial, turning the expected ECONNREFUSED into a
// handshake error and reddening a correct implementation.
const nocgoSharedServerPort = "19997"

// isolateSharedServerSignals makes a test's shared-server answer come only from
// what the test itself sets up.
//
// The two *FromConfig tests below assert the ABSENCE of server mode, and both
// now resolve it through effectiveServerMode, whose last fallback is
// doltserver.IsSharedServerMode() — the process-global env var plus the
// config.yaml layer on the config singleton. Without this, an empty beadsDir
// inherits the answer from whatever machine the suite runs on, and on any
// developer box or agent rig with dolt.shared-server enabled the two tests red
// while reporting a connection error instead of the flag-suggestion string they
// are about. Same env-bleed class, and same recipe, as TestEffectiveServerMode
// in the cgo half of this package.
func isolateSharedServerSignals(t *testing.T) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
}

// TestNocgoNewDoltStore_ErrorSuggestsCorrectFlag verifies that newDoltStore
// suggests "bd init --server" (not the old "--mode server") when ServerMode
// is false.
func TestNocgoNewDoltStore_ErrorSuggestsCorrectFlag(t *testing.T) {
	cfg := &dolt.Config{ServerMode: false}
	_, err := newDoltStore(t.Context(), cfg)
	if err == nil {
		t.Fatal("expected error when ServerMode is false")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bd init --server") {
		t.Errorf("error should suggest 'bd init --server', got: %s", msg)
	}
	if strings.Contains(msg, "--mode") {
		t.Errorf("error should NOT contain '--mode' (old flag), got: %s", msg)
	}
}

// TestNocgoNewDoltStoreFromConfig_ErrorSuggestsCorrectFlag verifies that
// newDoltStoreFromConfig suggests "bd init --server" when no server-mode
// config exists.
func TestNocgoNewDoltStoreFromConfig_ErrorSuggestsCorrectFlag(t *testing.T) {
	isolateSharedServerSignals(t)
	beadsDir := t.TempDir() // empty dir — no config.json
	_, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err == nil {
		t.Fatal("expected error for empty beads dir without server config")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bd init --server") {
		t.Errorf("error should suggest 'bd init --server', got: %s", msg)
	}
	if strings.Contains(msg, "--mode") {
		t.Errorf("error should NOT contain '--mode' (old flag), got: %s", msg)
	}
}

// TestNocgoNewReadOnlyStoreFromConfig_ErrorSuggestsCorrectFlag verifies that
// newReadOnlyStoreFromConfig suggests "bd init --server" when no server-mode
// config exists.
func TestNocgoNewReadOnlyStoreFromConfig_ErrorSuggestsCorrectFlag(t *testing.T) {
	isolateSharedServerSignals(t)
	beadsDir := t.TempDir() // empty dir — no config.json
	_, err := newReadOnlyStoreFromConfig(t.Context(), beadsDir)
	if err == nil {
		t.Fatal("expected error for empty beads dir without server config")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bd init --server") {
		t.Errorf("error should suggest 'bd init --server', got: %s", msg)
	}
	if strings.Contains(msg, "--mode") {
		t.Errorf("error should NOT contain '--mode' (old flag), got: %s", msg)
	}
}

// TestNocgoFactoriesHonorSharedServer is the !cgo half of the GH#6551 fix.
//
// store_factory_nocgo.go defines its OWN copies of newDoltStoreFromConfig and
// newReadOnlyStoreFromConfig, so compensation added only to the cgo factories
// did not reach the CGO_ENABLED=0 binaries .goreleaser.yml ships — the build
// where a shared server is the only usable backend. There the embedded
// fallthrough is a refusal rather than a phantom database, so the symptom is
// different and worse: the user is told to reinstall with embedded support
// (nocgoEmbeddedErrMsg) even though they already configured a mode this binary
// can serve.
//
// Every test covering the cgo factories carries //go:build cgo and therefore
// cannot observe this, and before this test the pure-Go CI job only proved the
// package compiles. This test is the missing behavioral half, and it runs in CI
// only because it is allowlisted BY NAME in the "Run pure-Go cmd/bd test subset
// (CGO_ENABLED=0)" step of both .github/workflows/pr.yml and
// .github/workflows/main.yml. The test name and those two allowlists must move
// together: renaming this function without editing both files, or trimming the
// name out of them, retires this coverage silently and restores the
// compile-only lane. Locally:
// `CGO_ENABLED=0 go test ./cmd/bd -run TestNocgoFactoriesHonorSharedServer`.
// It is the exact inverse of the two flag-suggestion tests above: same two
// factories, same empty-metadata.json workspace, opposite expectation once the
// workspace's config.yaml asks for a shared server.
func TestNocgoFactoriesHonorSharedServer(t *testing.T) {
	isolateSharedServerSignals(t)

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The GH#6551 shape: config.yaml present (tracked), metadata.json absent
	// (gitignored machine-local state), so the workspace's only statement of
	// shared-server mode is in the file configfile.IsDoltServerMode does not read.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n  auto-start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DIR", beadsDir)
	// BEADS_DOLT_SHARED_SERVER stays empty (isolateSharedServerSignals): the env
	// arm was never the broken one, and configfile.IsDoltServerMode honors it
	// already. The config.yaml layer is what the !cgo twins still ignored.
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", nocgoSharedServerPort)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if !effectiveServerMode(beadsDir, nil) {
		t.Fatal("test setup: config.yaml did not enable shared-server mode")
	}

	factories := map[string]func(context.Context, string) (interface{ Close() error }, error){
		"newDoltStoreFromConfig": func(ctx context.Context, dir string) (interface{ Close() error }, error) {
			return newDoltStoreFromConfig(ctx, dir)
		},
		"newReadOnlyStoreFromConfig": func(ctx context.Context, dir string) (interface{ Close() error }, error) {
			return newReadOnlyStoreFromConfig(ctx, dir)
		},
	}

	for name, open := range factories {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			store, err := open(ctx, beadsDir)
			if err == nil {
				if store != nil {
					_ = store.Close()
				}
				t.Fatalf("%s unexpectedly succeeded without a server", name)
			}
			// Negative half: the "reinstall with CGO" refusal is what the
			// unfixed twin produces, so its absence is what the GH#6551 fix
			// buys here.
			if strings.Contains(err.Error(), "requires a CGO build") {
				t.Fatalf("%s fell through to the embedded refusal with shared-server enabled (GH#6551, !cgo twin); got: %v", name, err)
			}
			// Positive half: absence alone is one-sided — any future pre-gate
			// refusal (say a new validation that errors on a metadata-less dir)
			// would satisfy it without the shared-server arm ever running. Only
			// that arm dials, so a refused connection to the port this test
			// pinned is proof it was reached. Both discriminators are owned
			// outside internal/storage/dolt — a syscall sentinel and this test's
			// own distinctive port — so a reworded connection error in a package
			// this PR does not own cannot turn a correct implementation red. The
			// two sibling fixes in this commit added the same half for the same
			// reason (prime_shared_server_test.go, store_factory_test.go).
			if !errors.Is(err, syscall.ECONNREFUSED) && !strings.Contains(err.Error(), nocgoSharedServerPort) {
				t.Fatalf("%s did not dial the shared server at the pinned port %s; got: %v", name, nocgoSharedServerPort, err)
			}
		})
	}
}
