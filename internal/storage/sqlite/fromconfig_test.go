package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// writeSQLiteMetadata persists a new-era SQLite workspace config (backend +
// sqlite_path, the shape runInitSQLite always writes).
func writeSQLiteMetadata(t *testing.T, beadsDir, sqlitePath string) {
	t.Helper()
	cfg := &configfile.Config{
		Backend:    configfile.BackendSQLite,
		SQLitePath: sqlitePath,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
}

// fakeDoltDB plants a Dolt database marker (<root>/<db>/.dolt) under beadsDir.
func fakeDoltDB(t *testing.T, beadsDir, root, db string) string {
	t.Helper()
	dir := filepath.Join(beadsDir, root, db)
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatalf("mkdir fake dolt db: %v", err)
	}
	return dir
}

// TestNewFromConfig_RefusesFreshProvisionOverDoltData is the bd-oyvc2.7 guard:
// when metadata.json selects SQLite but the database file does not exist and the
// workspace already contains a Dolt database, NewFromConfig must refuse loudly
// rather than silently provisioning a fresh empty SQLite database (false-empty).
func TestNewFromConfig_RefusesFreshProvisionOverDoltData(t *testing.T) {
	for _, layout := range []string{"embeddeddolt", "dolt"} {
		t.Run(layout+" layout", func(t *testing.T) {
			beadsDir := t.TempDir()
			writeSQLiteMetadata(t, beadsDir, "beads.db")
			fakeDoltDB(t, beadsDir, layout, "beads")

			store, err := NewFromConfig(context.Background(), beadsDir)
			if err == nil {
				_ = store.Close()
				t.Fatal("expected refusal, got a store")
			}
			if !strings.Contains(err.Error(), "refusing to create a fresh empty SQLite database") {
				t.Errorf("error should explain the refusal, got: %v", err)
			}
			if !strings.Contains(err.Error(), "bd init --backend=sqlite") {
				t.Errorf("error should give actionable guidance, got: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(beadsDir, "beads.db")); !os.IsNotExist(statErr) {
				t.Error("refusal must not leave a freshly created beads.db behind")
			}
		})
	}
}

// TestNewFromConfig_OpensExistingSQLiteNextToDoltData verifies that a genuine
// SQLite workspace (database file present) keeps opening even when Dolt artifacts
// also exist on disk — the guard only blocks fresh provisioning, not existing data.
func TestNewFromConfig_OpensExistingSQLiteNextToDoltData(t *testing.T) {
	ctx := context.Background()
	beadsDir := t.TempDir()
	writeSQLiteMetadata(t, beadsDir, "beads.db")

	// Provision first (as bd init --backend=sqlite does), then plant Dolt leftovers.
	st, err := Provision(ctx, filepath.Join(beadsDir, "beads.db"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	_ = st.Close()
	fakeDoltDB(t, beadsDir, "embeddeddolt", "beads")

	store, err := NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("NewFromConfig should open the existing SQLite database: %v", err)
	}
	_ = store.Close()
}

// TestNewFromConfig_FreshProvisionWithoutDoltData verifies the fresh-clone path:
// SQLite metadata, no database file yet, no Dolt data — provisioning is normal.
func TestNewFromConfig_FreshProvisionWithoutDoltData(t *testing.T) {
	beadsDir := t.TempDir()
	writeSQLiteMetadata(t, beadsDir, "beads.db")

	store, err := NewFromConfig(context.Background(), beadsDir)
	if err != nil {
		t.Fatalf("NewFromConfig should provision on a fresh clone: %v", err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(beadsDir, "beads.db")); err != nil {
		t.Errorf("expected beads.db to be provisioned: %v", err)
	}
}

// TestNewFromConfig_HonorsCustomDoltDataDir verifies the guard also sees a Dolt
// database living in a configured dolt_data_dir rather than the default layouts.
func TestNewFromConfig_HonorsCustomDoltDataDir(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{
		Backend:     configfile.BackendSQLite,
		SQLitePath:  "beads.db",
		DoltDataDir: "customdolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	fakeDoltDB(t, beadsDir, "customdolt", "beads")

	store, err := NewFromConfig(context.Background(), beadsDir)
	if err == nil {
		_ = store.Close()
		t.Fatal("expected refusal with Dolt data in custom dolt_data_dir, got a store")
	}
	if !strings.Contains(err.Error(), "refusing to create a fresh empty SQLite database") {
		t.Errorf("error should explain the refusal, got: %v", err)
	}
}
