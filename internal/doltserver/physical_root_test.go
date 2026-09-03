package doltserver

import (
	"os"
	"path/filepath"
	"testing"
)

// resetPhysicalRootEnv neutralizes every env var ResolvePhysicalRoots (and
// the helpers under it) consults, so tests describe the mode themselves
// rather than inheriting the developer machine's beads setup. The central
// config is pointed at a nonexistent file for the same reason — a real
// ~/.config/beads/server.json on the host must not leak modes into tests.
func resetPhysicalRootEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_DATA_DIR",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_PROXIED_SERVER_ROOT_PATH",
		"BEADS_SHARED_SERVER_DIR",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "no-such-central.json"))
}

func writeBeadsMetadata(t *testing.T, beadsDir, body string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestBeadsDir(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

func assertSingleRoot(t *testing.T, pr PhysicalRoots, wantMode, wantRoot string) {
	t.Helper()
	if pr.Mode != wantMode {
		t.Errorf("Mode = %q, want %q (provenance: %s)", pr.Mode, wantMode, pr.Provenance)
	}
	if len(pr.Roots) != 1 || pr.Roots[0] != filepath.Clean(wantRoot) {
		t.Errorf("Roots = %v, want [%s] (provenance: %s)", pr.Roots, wantRoot, pr.Provenance)
	}
	if pr.RemoteBackend {
		t.Errorf("RemoteBackend = true, want false (provenance: %s)", pr.Provenance)
	}
	if pr.Provenance == "" {
		t.Error("Provenance is empty; every decision must explain itself")
	}
}

func TestResolvePhysicalRootsEmbeddedMetadata(t *testing.T) {
	resetPhysicalRootEnv(t)
	beadsDir := newTestBeadsDir(t)
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"embedded"}`)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	// The embedded engine hardcodes embeddeddolt — NOT the dolt/ dir that
	// ResolveDoltDir reports for the same workspace. This divergence is the
	// original review blocker the resolver exists to fix.
	assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
}

func TestResolvePhysicalRootsServerMetadata(t *testing.T) {
	resetPhysicalRootEnv(t)
	beadsDir := newTestBeadsDir(t)
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server"}`)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "dolt"))
}

func TestResolvePhysicalRootsServerDataDirConfig(t *testing.T) {
	resetPhysicalRootEnv(t)
	absData := filepath.Join(t.TempDir(), "fast-disk")

	t.Run("relative", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server","dolt_data_dir":"mydata"}`)
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "mydata"))
	})

	t.Run("absolute", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server","dolt_data_dir":"`+jsonEscapePath(absData)+`"}`)
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "server", absData)
	})
}

func TestResolvePhysicalRootsServerDataDirEnv(t *testing.T) {
	resetPhysicalRootEnv(t)

	t.Run("relative", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server"}`)
		t.Setenv("BEADS_DOLT_DATA_DIR", "envdata")
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "envdata"))
	})

	t.Run("absolute", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server"}`)
		absData := filepath.Join(t.TempDir(), "env-abs")
		t.Setenv("BEADS_DOLT_DATA_DIR", absData)
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "server", absData)
	})
}

func TestResolvePhysicalRootsServerModeEnvOverridesEmbeddedMetadata(t *testing.T) {
	resetPhysicalRootEnv(t)
	beadsDir := newTestBeadsDir(t)
	// Runtime env wins over stale persisted mode (GH#2949 semantics).
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"embedded"}`)
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "dolt"))
}

func TestResolvePhysicalRootsRemoteServer(t *testing.T) {
	resetPhysicalRootEnv(t)
	beadsDir := newTestBeadsDir(t)
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server","dolt_server_host":"db.example.com"}`)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Mode != "server" {
		t.Errorf("Mode = %q, want server", pr.Mode)
	}
	if !pr.RemoteBackend {
		t.Error("RemoteBackend = false, want true for non-local dolt_server_host")
	}
	if len(pr.Roots) != 0 {
		t.Errorf("Roots = %v, want none for a remote backend", pr.Roots)
	}
}

func TestResolvePhysicalRootsSharedServerWinsOverMetadata(t *testing.T) {
	resetPhysicalRootEnv(t)
	beadsDir := newTestBeadsDir(t)
	// Stale dolt_mode=embedded must NOT override active shared intent
	// (mirrors ResolveDoltDir and the main.go no-config rescue, GH#3817).
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"embedded"}`)

	sharedParent := filepath.Join(t.TempDir(), "shared-server")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedParent)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleRoot(t, pr, "shared-server", filepath.Join(sharedParent, "dolt"))

	// Side-effect freedom: resolution must not have created the shared
	// tree (SharedServerDir/SharedDoltDir mkdir on first use; the resolver
	// must use the Path variants that do not).
	if _, statErr := os.Stat(sharedParent); !os.IsNotExist(statErr) {
		t.Errorf("shared server dir %s was created by pure resolution (stat err = %v)", sharedParent, statErr)
	}
}

func TestResolvePhysicalRootsProxied(t *testing.T) {
	resetPhysicalRootEnv(t)
	meta := `{"database":"beads.db","dolt_mode":"proxied-server"}`

	t.Run("env absolute", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, meta)
		absRoot := filepath.Join(t.TempDir(), "proxy-root")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", absRoot)
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "proxied-server", absRoot)
	})

	t.Run("env relative", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, meta)
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "relroot")
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "proxied-server", filepath.Join(beadsDir, "relroot"))
	})

	t.Run("client info sidecar", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, meta)
		sidecarRoot := filepath.Join(t.TempDir(), "sidecar-root")
		body := `{"root_path":"` + jsonEscapePath(sidecarRoot) + `"}`
		if err := os.WriteFile(filepath.Join(beadsDir, "proxied_server_client_info.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "proxied-server", sidecarRoot)
	})

	t.Run("fallback default data dir", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, meta)
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "proxied-server", filepath.Join(beadsDir, "dolt"))
	})
}

func TestResolvePhysicalRootsIgnoresCentralConfig(t *testing.T) {
	resetPhysicalRootEnv(t)

	// CLI parity: cmd/bd/main.go's hand-built store selection never applies
	// central-config defaults, so the resolver must not either — a central
	// dolt_mode=server must NOT promote a mode-less project workspace. The
	// LIBRARY path is broader; that side is covered by LibraryOpenRootPath.
	beadsDir := newTestBeadsDir(t)
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db"}`)
	centralPath := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(centralPath, []byte(`{"dolt_mode":"server"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
}

func TestResolvePhysicalRootsPortDoesNotPromoteEmbedded(t *testing.T) {
	resetPhysicalRootEnv(t)

	// `bd init --server-port N` without --server writes dolt_mode=embedded
	// AND a port; the CLI opens embeddeddolt, and ResolveServerMode gives
	// an explicit dolt_mode=embedded precedence over the port heuristic.
	// The resolver must agree: no port>0 promotion.
	beadsDir := newTestBeadsDir(t)
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"embedded","dolt_server_port":3307}`)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
}

func TestResolvePhysicalRootsBracketedIPv6LoopbackIsLocal(t *testing.T) {
	resetPhysicalRootEnv(t)

	// "[::1]" is what a host:port split leaves behind for IPv6 loopback;
	// treating it as remote would silently drop the physical gate. The
	// local set mirrors the storage layer's isLocalHost.
	beadsDir := newTestBeadsDir(t)
	writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"server","dolt_server_host":"[::1]"}`)

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "dolt"))
}

func TestLibraryOpenRootPath(t *testing.T) {
	resetPhysicalRootEnv(t)

	t.Run("embedded metadata still yields DatabasePath root", func(t *testing.T) {
		// The library open path (dolt.NewFromConfigWithOptions) is
		// server-only and derives its root from DatabasePath even for
		// embedded-metadata workspaces — the exact under-gating the union
		// in beads.OpenGated exists to cover.
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_mode":"embedded"}`)
		if got, want := LibraryOpenRootPath(beadsDir), filepath.Join(beadsDir, "dolt"); got != want {
			t.Errorf("LibraryOpenRootPath = %q, want %q", got, want)
		}
	})

	t.Run("absent metadata uses DefaultConfig without migration", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		legacyPath := filepath.Join(beadsDir, "config.json")
		if err := os.WriteFile(legacyPath, []byte(`{"database":"beads.db"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, want := LibraryOpenRootPath(beadsDir), filepath.Join(beadsDir, "dolt"); got != want {
			t.Errorf("LibraryOpenRootPath = %q, want %q", got, want)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
			t.Errorf("metadata.json was created by pure resolution (stat err = %v)", statErr)
		}
		if _, statErr := os.Stat(legacyPath); statErr != nil {
			t.Errorf("legacy config.json was removed by pure resolution: %v", statErr)
		}
	})

	t.Run("data dir config honored", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		writeBeadsMetadata(t, beadsDir, `{"database":"beads.db","dolt_data_dir":"fast"}`)
		if got, want := LibraryOpenRootPath(beadsDir), filepath.Join(beadsDir, "fast"); got != want {
			t.Errorf("LibraryOpenRootPath = %q, want %q", got, want)
		}
	})
}

func TestResolvePhysicalRootsNoMetadataFallbacks(t *testing.T) {
	resetPhysicalRootEnv(t)

	t.Run("embeddeddolt dir present", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
	})

	t.Run("dolt server-layout dir present", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "server", filepath.Join(beadsDir, "dolt"))
	})

	t.Run("nothing on disk defaults to embedded", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		// The root may not exist yet; gating a not-yet-existing root is
		// fine (the gate file lives beside it in .beads).
		assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
	})

	t.Run("env server mode without metadata stays embedded", func(t *testing.T) {
		// CLI parity: the nil-config branch in cmd/bd/main.go rescues ONLY
		// shared-server mode; BEADS_DOLT_SERVER_MODE without metadata does
		// not change what the CLI opens, so it must not change the gate.
		beadsDir := newTestBeadsDir(t)
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		pr, err := ResolvePhysicalRoots(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
	})
}

func TestResolvePhysicalRootsSideEffectFree(t *testing.T) {
	resetPhysicalRootEnv(t)
	beadsDir := newTestBeadsDir(t)
	// Legacy layout: config.json present, metadata.json absent. An
	// unguarded configfile.Load would MIGRATE (write metadata.json, delete
	// config.json); pure resolution must not.
	legacyPath := filepath.Join(beadsDir, "config.json")
	if err := os.WriteFile(legacyPath, []byte(`{"database":"beads.db","dolt_mode":"server"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	pr, err := ResolvePhysicalRoots(beadsDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(statErr) {
		t.Errorf("metadata.json was created by pure resolution (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(legacyPath); statErr != nil {
		t.Errorf("legacy config.json was removed by pure resolution: %v", statErr)
	}
	// With the migration correctly NOT triggered, the legacy config is
	// invisible and the no-metadata stat fallback applies.
	assertSingleRoot(t, pr, "embedded", filepath.Join(beadsDir, "embeddeddolt"))
}

func TestResolveProxiedServerRootPath(t *testing.T) {
	resetPhysicalRootEnv(t)

	t.Run("env absolute wins", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		absRoot := filepath.Join(t.TempDir(), "abs-root")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", absRoot)
		got, err := ResolveProxiedServerRootPath(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != absRoot {
			t.Errorf("root = %q, want %q", got, absRoot)
		}
	})

	t.Run("env relative joins beadsDir", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "rel-root")
		got, err := ResolveProxiedServerRootPath(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(beadsDir, "rel-root"); got != want {
			t.Errorf("root = %q, want %q", got, want)
		}
	})

	t.Run("sidecar then default", func(t *testing.T) {
		beadsDir := newTestBeadsDir(t)
		got, err := ResolveProxiedServerRootPath(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(beadsDir, "dolt"); got != want {
			t.Errorf("default root = %q, want %q", got, want)
		}

		sidecarRoot := filepath.Join(t.TempDir(), "side-root")
		body := `{"root_path":"` + jsonEscapePath(sidecarRoot) + `"}`
		if err := os.WriteFile(filepath.Join(beadsDir, "proxied_server_client_info.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err = ResolveProxiedServerRootPath(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != sidecarRoot {
			t.Errorf("sidecar root = %q, want %q", got, sidecarRoot)
		}
	})
}

// jsonEscapePath escapes a filesystem path for embedding in a hand-written
// JSON string literal (Windows backslashes would otherwise be parsed as
// escape sequences).
func jsonEscapePath(p string) string {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' {
			out = append(out, '\\', '\\')
		} else {
			out = append(out, p[i])
		}
	}
	return string(out)
}
