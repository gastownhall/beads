//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestServerModeLenientOpenDirtyTableGate covers gastownhall/beads#5781, the
// server-mode counterpart of #4566, which was fixed for embedded only. A pending
// schema migration that alters a table carrying uncommitted rows must refuse a
// plain writable open, but the working-set-reconcile open behind "bd dolt
// commit" / "bd vc commit" (Config.LenientOpen) must still succeed, because
// that commit is the refusal's own documented recovery. Against an external
// Dolt server there is no equivalent of deleting the local database, so
// without this the operator's only escape is hand-issuing DOLT_ADD/DOLT_COMMIT
// through the raw dolt client.
func TestServerModeLenientOpenDirtyTableGate(t *testing.T) {
	integration.RequireDolt(t)
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PASSWORD", "")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("start local Dolt server: %v", err)
	}
	t.Cleanup(func() {
		current, stateErr := doltserver.IsRunning(beadsDir)
		if stateErr != nil {
			t.Errorf("check local Dolt server before stop: %v", stateErr)
			return
		}
		if current == nil || !current.Running {
			return
		}
		if err := doltserver.Stop(beadsDir); err != nil {
			t.Errorf("stop local Dolt server: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Run("dirty database deadlocks a plain open and recovers through a lenient one", func(t *testing.T) {
		const database = "lenient_dirty"
		cfg := lenientOpenConfig(beadsDir, state.Port, database)
		admin := prepareLenientOpenDatabaseAtV51(t, ctx, state.Port, database)
		defer admin.Close()

		if _, err := admin.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, '', '', '', '')",
			database+"-1", "uncommitted issue",
		); err != nil {
			t.Fatalf("dirty issues table: %v", err)
		}

		gated, gateErr := New(ctx, cfg)
		if gateErr == nil {
			gated.Close()
			t.Fatal("New (plain writable open) = nil, want *schema.DirtyTablesError")
		}
		var dirtyErr *schema.DirtyTablesError
		if !errors.As(gateErr, &dirtyErr) {
			t.Fatalf("New error = %T (%v), want error wrapping *schema.DirtyTablesError", gateErr, gateErr)
		}
		if !containsTable(dirtyErr.Tables, "issues") {
			t.Fatalf("DirtyTablesError.Tables = %v, want to include %q", dirtyErr.Tables, "issues")
		}

		lenientCfg := lenientOpenConfig(beadsDir, state.Port, database)
		lenientCfg.LenientOpen = true
		store, err := New(ctx, lenientCfg)
		if err != nil {
			t.Fatalf("New (LenientOpen) = %v, want a usable store", err)
		}
		defer store.Close()

		if got := lenientOpenSchemaVersion(t, ctx, store.db); got != 51 {
			t.Fatalf("schema version after lenient open = %d, want 51 (the migration must stay skipped)", got)
		}

		if err := store.Commit(ctx, "checkpoint before migration"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := lenientOpenDirtyCount(t, ctx, store.db); got != 0 {
			t.Fatalf("dolt_status rows after commit = %d, want 0", got)
		}
		store.Close()

		migrated, err := New(ctx, lenientOpenConfig(beadsDir, state.Port, database))
		if err != nil {
			t.Fatalf("New (post-commit plain open): %v", err)
		}
		defer migrated.Close()
		if got := lenientOpenSchemaVersion(t, ctx, migrated.db); got != schema.LatestVersion() {
			t.Fatalf("schema version after post-commit open = %d, want latest %d", got, schema.LatestVersion())
		}
	})

	// The migration-consent refusal fires before the dirty guard, so on a
	// database that is both behind and dirty the unconsented commit the
	// guard prescribes would rebuild this deadlock one check earlier.
	t.Run("behind and dirty database without consent recovers through a lenient open", func(t *testing.T) {
		const database = "lenient_consent"
		admin := prepareLenientOpenDatabaseAtV51(t, ctx, state.Port, database)
		defer admin.Close()

		if _, err := admin.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, '', '', '', '')",
			database+"-1", "uncommitted issue",
		); err != nil {
			t.Fatalf("dirty issues table: %v", err)
		}

		schema.SetLocalMigrateConsent(false)
		t.Cleanup(func() { schema.SetLocalMigrateConsent(true) })
		t.Setenv(schema.AllowMigrateEnv, "")
		t.Setenv(schema.AllowRemoteMigrateEnv, "")

		refuses := func(name string, cfg *Config) {
			t.Helper()
			s, err := New(ctx, cfg)
			if err == nil {
				s.Close()
				t.Fatalf("New (%s) = nil, want *schema.MigrateConsentError", name)
			}
			if !schema.IsMigrateConsentError(err) {
				t.Fatalf("New (%s) error = %T (%v), want *schema.MigrateConsentError", name, err, err)
			}
		}
		refuses("plain writable open", lenientOpenConfig(beadsDir, state.Port, database))
		syncCfg := lenientOpenConfig(beadsDir, state.Port, database)
		syncCfg.RemoteSyncOpen = true
		refuses("RemoteSyncOpen", syncCfg)

		lenientCfg := lenientOpenConfig(beadsDir, state.Port, database)
		lenientCfg.LenientOpen = true
		store, err := New(ctx, lenientCfg)
		if err != nil {
			t.Fatalf("New (LenientOpen) = %v, want it to continue past the consent refusal", err)
		}
		defer store.Close()
		if got := lenientOpenSchemaVersion(t, ctx, store.db); got != 51 {
			t.Fatalf("schema version after lenient open = %d, want 51 (no consent, so no migration)", got)
		}
		if err := store.Commit(ctx, "checkpoint before migration"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := lenientOpenDirtyCount(t, ctx, store.db); got != 0 {
			t.Fatalf("dolt_status rows after commit = %d, want 0", got)
		}
		store.Close()

		schema.SetLocalMigrateConsent(true)
		migrated, err := New(ctx, lenientOpenConfig(beadsDir, state.Port, database))
		if err != nil {
			t.Fatalf("New (consented, post-commit): %v", err)
		}
		defer migrated.Close()
		if got := lenientOpenSchemaVersion(t, ctx, migrated.db); got != schema.LatestVersion() {
			t.Fatalf("schema version after consented open = %d, want latest %d", got, schema.LatestVersion())
		}
	})

	// gastownhall/beads#5816: a pending ignored migration (0002) that touches
	// dirty committable data refuses with the same typed error before any
	// numbered migration runs, so the lenient open recovers it too, while an
	// unrelated dirty wisp stays local throughout.
	t.Run("ignored migration over dirty committable data recovers through a lenient open", func(t *testing.T) {
		const (
			database = "lenient_ignored_committable"
			parentID = database + "-parent"
			wispID   = database + "-wisp"
		)
		admin := prepareLenientOpenDatabaseAtV51(t, ctx, state.Port, database)
		defer admin.Close()
		seeded, err := New(ctx, lenientOpenConfig(beadsDir, state.Port, database))
		if err != nil {
			t.Fatalf("New (seed to latest): %v", err)
		}
		seeded.Close()

		for _, q := range []string{
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES ('" + parentID + "', 'durable parent', '', '', '', '')",
			"CALL DOLT_COMMIT('-Am', 'test: durable parent', '--author', 'Beads Test <beads@example.com>')",
			"INSERT INTO child_counters (parent_id, last_child) VALUES ('" + parentID + "', 7)",
			"INSERT INTO wisps (id, title) VALUES ('" + wispID + "', 'unrelated wisp')",
			"DELETE FROM ignored_schema_migrations WHERE version > 1",
		} {
			if _, err := admin.ExecContext(ctx, q); err != nil {
				t.Fatalf("fixture %q: %v", q, err)
			}
		}

		gated, gateErr := New(ctx, lenientOpenConfig(beadsDir, state.Port, database))
		if gateErr == nil {
			gated.Close()
			t.Fatal("New (plain writable open) = nil, want *schema.DirtyTablesError")
		}
		var dirtyErr *schema.DirtyTablesError
		if !errors.As(gateErr, &dirtyErr) {
			t.Fatalf("New error = %T (%v), want error wrapping *schema.DirtyTablesError", gateErr, gateErr)
		}
		if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != "child_counters" {
			t.Fatalf("DirtyTablesError.Tables = %v, want [child_counters]", dirtyErr.Tables)
		}

		lenientCfg := lenientOpenConfig(beadsDir, state.Port, database)
		lenientCfg.LenientOpen = true
		var (
			store   *DoltStore
			openErr error
		)
		warning := captureOpenStderr(t, func() { store, openErr = New(ctx, lenientCfg) })
		if openErr != nil {
			t.Fatalf("New (LenientOpen) = %v, want a usable store", openErr)
		}
		if !strings.Contains(warning, "Warning:") || !strings.Contains(warning, "child_counters") {
			store.Close()
			t.Fatalf("lenient open stderr = %q, want a warning naming child_counters", warning)
		}
		committed, err := store.CommitAll(ctx, "reconcile working set")
		store.Close()
		if err != nil {
			t.Fatalf("CommitAll: %v", err)
		}
		if !committed {
			t.Fatal("CommitAll committed = false, want the dirty child_counters row committed")
		}

		migrated, err := New(ctx, lenientOpenConfig(beadsDir, state.Port, database))
		if err != nil {
			t.Fatalf("New (post-commit plain open) = %v, want migrations to converge", err)
		}
		defer migrated.Close()
		if got := lenientOpenSchemaVersion(t, ctx, migrated.db); got != schema.LatestVersion() {
			t.Fatalf("schema version = %d, want latest %d", got, schema.LatestVersion())
		}
		var ignoredVersion int
		if err := migrated.db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		).Scan(&ignoredVersion); err != nil {
			t.Fatalf("read ignored cursor: %v", err)
		}
		if ignoredVersion != schema.LatestIgnoredVersion() {
			t.Fatalf("ignored cursor = %d, want %d", ignoredVersion, schema.LatestIgnoredVersion())
		}
		for _, q := range []string{
			"SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = '" + parentID + "'",
			"SELECT last_child FROM child_counters WHERE parent_id = '" + parentID + "'",
		} {
			var lastChild int
			if err := migrated.db.QueryRowContext(ctx, q).Scan(&lastChild); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if lastChild != 7 {
				t.Fatalf("%s = %d, want 7", q, lastChild)
			}
		}
		var working, atHead int
		if err := migrated.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM wisps WHERE id = '"+wispID+"'").Scan(&working); err != nil {
			t.Fatalf("count working wisp: %v", err)
		}
		// An untracked wisps table is absent from HEAD altogether, which also
		// keeps the row out of it.
		if err := migrated.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM wisps AS OF 'HEAD' WHERE id = '"+wispID+"'").Scan(&atHead); err != nil &&
			!strings.Contains(err.Error(), "table not found") {
			t.Fatalf("count committed wisp: %v", err)
		}
		if working != 1 || atHead != 0 {
			t.Fatalf("unrelated wisp: working = %d, at HEAD = %d; want 1 and 0 (it must stay local)", working, atHead)
		}
	})

	t.Run("clean database still migrates through a lenient open", func(t *testing.T) {
		const database = "lenient_clean"
		admin := prepareLenientOpenDatabaseAtV51(t, ctx, state.Port, database)
		defer admin.Close()

		cfg := lenientOpenConfig(beadsDir, state.Port, database)
		cfg.LenientOpen = true
		store, err := New(ctx, cfg)
		if err != nil {
			t.Fatalf("New (LenientOpen, clean database): %v", err)
		}
		defer store.Close()
		if got := lenientOpenSchemaVersion(t, ctx, store.db); got != schema.LatestVersion() {
			t.Fatalf("schema version = %d, want latest %d (a lenient open must not skip migrations wholesale)",
				got, schema.LatestVersion())
		}
	})
}

func lenientOpenConfig(beadsDir string, port int, database string) *Config {
	return &Config{
		Path:           filepath.Join(beadsDir, database),
		BeadsDir:       beadsDir,
		ServerHost:     "127.0.0.1",
		ServerPort:     port,
		ServerUser:     "root",
		Database:       database,
		CommitterName:  "Beads Test",
		CommitterEmail: "beads@example.com",
		MaxOpenConns:   1,
	}
}

// prepareLenientOpenDatabaseAtV51 creates database on the running server,
// migrates it to schema v51, and commits that baseline so the working set
// starts clean. v51 leaves every later migration pending, including several
// that alter `issues`. Returns an open connection to the prepared database.
func prepareLenientOpenDatabaseAtV51(t *testing.T, ctx context.Context, port int, database string) *sql.DB {
	t.Helper()
	adminDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String()
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+database+"`"); err != nil {
		admin.Close()
		t.Fatalf("create database %s: %v", database, err)
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("close admin connection: %v", err)
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: database}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", database, err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		t.Fatalf("pin %s: %v", database, err)
	}
	defer conn.Close()
	if _, err := schema.MigrateUpTo(ctx, conn, 51); err != nil {
		db.Close()
		t.Fatalf("migrate %s to v51: %v", database, err)
	}
	if _, err := conn.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'test: v51 baseline', '--author', 'Beads Test <beads@example.com>')",
	); err != nil {
		db.Close()
		t.Fatalf("commit v51 baseline: %v", err)
	}
	if got := lenientOpenSchemaVersion(t, ctx, conn); got != 51 {
		db.Close()
		t.Fatalf("baseline schema version = %d, want 51", got)
	}
	return db
}

type lenientOpenQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lenientOpenSchemaVersion(t *testing.T, ctx context.Context, db lenientOpenQueryer) int {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

func lenientOpenDirtyCount(t *testing.T, ctx context.Context, db lenientOpenQueryer) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&count); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	return count
}

func containsTable(tables []string, want string) bool {
	for _, table := range tables {
		if table == want {
			return true
		}
	}
	return false
}
