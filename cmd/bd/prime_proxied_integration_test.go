//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestProxiedServerPrime covers the bd-mm8wf seam from lion's #5361 review:
// `bd prime` sits in noDbCommands, so formatMemoriesForPrime lazily
// initialized storage via ensureStoreActiveForPrime — a direct-store open
// (the bd-m7zzd seam class) that in proxied mode the store factory refuses,
// which the silent-skip contract then swallowed: proxied prime never carried
// memories at all. The memory read now rides the proxied plane (one
// read-only UOW, ConfigUseCase().GetAllConfig), so proxied prime has parity
// with classic when the server is reachable and still degrades silently when
// it is not.
func TestProxiedServerPrime(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// A lazy direct open in this workspace would materialize an embedded
	// store directory; the proxied plane never does.
	assertNoEmbeddedStore := func(t *testing.T, p proxiedProject) {
		t.Helper()
		embeddedDir := filepath.Join(p.beadsDir, "embeddeddolt")
		if _, err := os.Stat(embeddedDir); !os.IsNotExist(err) {
			t.Errorf("expected no embedded store at %s after bd prime (direct-store seam), stat err=%v", embeddedDir, err)
		}
	}

	t.Run("memories_only_injects_via_proxied_plane", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ppr1")
		content := "proxied prime memories ride the uow plane"
		out := bdProxiedMem(t, bd, p.dir, "remember", content, "--key", "proxied-prime-seam")
		if !strings.Contains(out, "Remembered [proxied-prime-seam]") {
			t.Fatalf("expected 'Remembered [proxied-prime-seam]', got: %s", out)
		}

		db := openProxiedDB(t, p)
		head := readDoltHead(t, db)

		out = bdProxiedMem(t, bd, p.dir, "prime", "--memories-only")
		if !strings.Contains(out, "## Persistent Memories") {
			t.Errorf("expected Persistent Memories section in proxied prime output, got: %s", out)
		}
		if !strings.Contains(out, "proxied-prime-seam") || !strings.Contains(out, content) {
			t.Errorf("expected memory key and content in proxied prime output, got: %s", out)
		}

		// The read is read-only on the proxied plane: no Dolt commit landed.
		if n := readDoltLogCountSince(t, db, head); n != 0 {
			t.Errorf("expected 0 commits from a prime memory read, got %d", n)
		}
		assertNoEmbeddedStore(t, p)
	})

	t.Run("full_prime_appends_memories_section", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ppr2")
		bdProxiedMem(t, bd, p.dir, "remember", "full prime carries memories in proxied mode", "--key", "full-prime-check")
		out := bdProxiedMem(t, bd, p.dir, "prime", "--full")
		if !strings.Contains(out, "## Persistent Memories") || !strings.Contains(out, "full-prime-check") {
			t.Errorf("expected memories section with key 'full-prime-check' in full prime output, got: %s", out)
		}
		assertNoEmbeddedStore(t, p)
	})

	t.Run("no_memories_yields_stored_none_message", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ppr3")
		out := bdProxiedMem(t, bd, p.dir, "prime", "--memories-only")
		if !strings.Contains(out, "No memories stored") {
			t.Errorf("expected 'No memories stored' for empty proxied memory set, got: %s", out)
		}
	})

	t.Run("degrades_silently_when_plane_unavailable", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ppr4")
		content := "this memory must not surface when the plane is down"
		bdProxiedMem(t, bd, p.dir, "remember", content, "--key", "plane-down")

		// Corrupt the proxied-server sidecar: provider open now fails before
		// touching any server, the deterministic stand-in for an unreachable
		// plane. Prime must still exit 0 (silent-skip contract) with no
		// memories and no direct-store fallback.
		sidecar := configfile.ProxiedServerClientInfoPath(p.beadsDir)
		if err := os.WriteFile(sidecar, []byte("not json{"), 0o644); err != nil {
			t.Fatalf("corrupting sidecar %s: %v", sidecar, err)
		}

		out := bdProxiedMem(t, bd, p.dir, "prime", "--memories-only")
		if strings.Contains(out, content) || strings.Contains(out, "plane-down") {
			t.Errorf("expected memory to be skipped when the proxied plane is unavailable, got: %s", out)
		}
		if !strings.Contains(out, "No memories stored") {
			t.Errorf("expected the empty-memories fallback on silent skip, got: %s", out)
		}
		assertNoEmbeddedStore(t, p)
	})
}
