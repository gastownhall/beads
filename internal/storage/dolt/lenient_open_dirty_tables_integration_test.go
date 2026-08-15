//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
