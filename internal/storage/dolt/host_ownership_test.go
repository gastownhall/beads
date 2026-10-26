package dolt

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// storeHostSpellings covers each host kind in the spellings that used to get
// different answers from the store's private host list and from configfile.
var storeHostSpellings = []string{
	"", "127.0.0.1", "localhost", "LOCALHOST", " localhost ", "0.0.0.0", "[127.0.0.1]", "::ffff:127.0.0.1",
	"::1", "[::1]", "0:0:0:0:0:0:0:1", "::",
	"127.0.0.2",
	"db.example.com", "10.0.0.5", "2001:db8::1",
}

// TestStoreHostOwnershipDecisionsAgree pins every store decision about
// whether bd runs the Dolt server at a host to configfile.BdRunsServerAt:
// auto-start, the local data directory, the shared-database consent gate,
// the port file, and the advice that names `bd dolt start`. They must agree
// for every spelling; before one predicate decided them all, LOCALHOST and
// 0.0.0.0 got bd-start advice but no auto-start, and ::1 the reverse in
// places.
func TestStoreHostOwnershipDecisionsAgree(t *testing.T) {
	originalEnsure := ensureResolvedPortFile
	t.Cleanup(func() { ensureResolvedPortFile = originalEnsure })

	for _, host := range storeHostSpellings {
		t.Run("host="+host, func(t *testing.T) {
			clearPredicateEnv(t)
			clearSizingModeEnv(t)
			bd := configfile.BdRunsServerAt(host)

			if got := isExternalServerHost(host); got == bd {
				t.Errorf("isExternalServerHost(%q) = %v, want %v", host, got, !bd)
			}
			if got := doltserver.UnresolvedPortAdvice(host) == ""; got != bd {
				t.Errorf("UnresolvedPortAdvice(%q) empty = %v, want %v", host, got, bd)
			}
			if got := serverOpenCanAutoStart(&Config{AutoStart: true, Path: "/unused", ServerHost: host}); got != bd {
				t.Errorf("serverOpenCanAutoStart(host %q) = %v, want %v", host, got, bd)
			}

			beadsDir := t.TempDir()
			root := filepath.Join(beadsDir, "dolt")
			dataDir := resolveLocalActiveDatabaseDir(&Config{
				Path: root, BeadsDir: beadsDir, Database: "active", ServerHost: host, AutoStart: true,
			})
			if got := dataDir != ""; got != bd {
				t.Errorf("resolveLocalActiveDatabaseDir(host %q) = %q; a local data dir must be counted only for a server bd runs (want %v)", host, dataDir, bd)
			}

			cfg := &Config{BeadsDir: managedWorkspace(t, 3307), ServerHost: host, ServerPort: 3307}
			if got := !sharedServerDatabase(cfg); got != bd {
				t.Errorf("sharedServerDatabase(host %q) exempts the consent gate = %v, want %v", host, got, bd)
			}

			calls := 0
			ensureResolvedPortFile = func(string, int) error {
				calls++
				return nil
			}
			if err := persistResolvedPortFile(&Config{ServerHost: host, ServerPort: 3307}, t.TempDir()); err != nil {
				t.Fatalf("persistResolvedPortFile(host %q): %v", host, err)
			}
			if got := calls > 0; got != bd {
				t.Errorf("persistResolvedPortFile(host %q) wrote bd's port file = %v, want %v", host, got, bd)
			}
		})
	}
}
