package doltserver

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSharedProxyRootDir_StableAcrossScopes proves the shared proxy rootDir is
// machine-wide: it takes no scope (beadsDir) input, so every proxied scope that
// opts into the shared backend resolves to the SAME rootDir. That identity is
// exactly what collapses N+1 per-scope db-proxy-children into one — the proxy
// parent's spawn-or-reuse keys on rootDir.
func TestSharedProxyRootDir_StableAcrossScopes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", tmp)
	t.Setenv("BEADS_SHARED_PROXY_ROOT_PATH", "") // no location override for this case

	first, err := SharedProxyRootDir()
	if err != nil {
		t.Fatalf("SharedProxyRootDir (scope A): %v", err)
	}
	second, err := SharedProxyRootDir()
	if err != nil {
		t.Fatalf("SharedProxyRootDir (scope B): %v", err)
	}
	if first != second {
		t.Errorf("shared proxy rootDir not stable across scopes: %q vs %q", first, second)
	}

	want := filepath.Join(tmp, "proxy")
	if first != want {
		t.Errorf("SharedProxyRootDir = %q, want %q (under SharedServerDir)", first, want)
	}
	if info, err := os.Stat(first); err != nil || !info.IsDir() {
		t.Errorf("expected shared proxy rootDir to exist as a directory: %s", first)
	}
}

// TestSharedProxyRootDir_EnvOverride proves BEADS_SHARED_PROXY_ROOT_PATH
// overrides the location, mirroring how BEADS_SHARED_SERVER_DIR overrides
// SharedServerDir.
func TestSharedProxyRootDir_EnvOverride(t *testing.T) {
	tmp := t.TempDir()
	override := filepath.Join(tmp, "custom-proxy-root")
	t.Setenv("BEADS_SHARED_PROXY_ROOT_PATH", override)

	dir, err := SharedProxyRootDir()
	if err != nil {
		t.Fatalf("SharedProxyRootDir: %v", err)
	}
	if dir != override {
		t.Errorf("SharedProxyRootDir = %q, want %q (from BEADS_SHARED_PROXY_ROOT_PATH)", dir, override)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("expected override dir to be created: %s", dir)
	}
}
