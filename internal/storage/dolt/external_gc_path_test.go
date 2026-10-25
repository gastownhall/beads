package dolt

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestDoltStoreExternalGCPathKeepsInstanceAuthority(t *testing.T) {
	clearSizingModeEnv(t)
	beadsDir := t.TempDir()
	cfg := &Config{Path: filepath.Join(beadsDir, "dolt"), BeadsDir: beadsDir, Database: "active", ServerHost: "localhost", AutoStart: true}
	owned := &DoltStore{localActiveDatabaseDir: resolveLocalActiveDatabaseDir(cfg)}
	wantOwned := filepath.Join(cfg.Path, "active")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "44001")
	external := &DoltStore{dbPath: cfg.Path, database: cfg.Database, localActiveDatabaseDir: resolveLocalActiveDatabaseDir(cfg)}
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	sharedRoot := t.TempDir()
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedRoot)
	shared := &DoltStore{localActiveDatabaseDir: resolveLocalActiveDatabaseDir(cfg)}
	wantShared := filepath.Join(sharedRoot, "dolt", "active")
	for _, mode := range []string{"", "1"} {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", mode)
		t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
		for _, tc := range []struct {
			store *DoltStore
			want  string
		}{{owned, wantOwned}, {shared, wantShared}} {
			if got, err := tc.store.ExternalGCPath(t.Context()); err != nil || got != tc.want {
				t.Fatalf("ExternalGCPath = %q, %v; want captured %q", got, err, tc.want)
			}
		}
		got, err := external.ExternalGCPath(t.Context())
		var unsupported *storage.ErrUnsupported
		if got != "" || !errors.As(err, &unsupported) {
			t.Fatalf("external instance gained local GC authority: %q, %v", got, err)
		}
	}
}
