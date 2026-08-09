//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestFreshBootstrapHealIncarnation exercises the destructive bootstrap-heal
// authorization against a real Dolt sql-server. In particular, the
// drop/recreate subtest is the regression for stale creator authority crossing
// a database-name ABA boundary and deleting another incarnation's work.
func TestFreshBootstrapHealIncarnation(t *testing.T) {
	integration.RequireDolt(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adminDSN := doltutil.ServerDSN{
		Host: "127.0.0.1",
		Port: state.Port,
		User: "root",
	}.String()
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	t.Run("exactly one concurrent creator receives a capability", func(t *testing.T) {
		const workers = 16
		type result struct {
			hasCapability bool
			err           error
		}

		ready := make(chan struct{})
		results := make(chan result, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-ready
				cfg := freshBootstrapIntegrationConfig(
					beadsDir, state.Port, "heal_creator_race", fmt.Sprintf("creator-%02d", i),
				)
				db, _, facts, err := openServerConnection(ctx, cfg)
				if db != nil {
					_ = db.Close()
				}
				results <- result{hasCapability: facts.bootstrapHeal != nil, err: err}
			}(i)
		}
		close(ready)
		wg.Wait()
		close(results)

		capabilities := 0
		for got := range results {
			if got.err != nil {
				t.Errorf("concurrent open: %v", got.err)
			}
			if got.hasCapability {
				capabilities++
			}
		}
		if capabilities != 1 {
			t.Fatalf("creator capability grants = %d, want exactly 1", capabilities)
		}
	})

	t.Run("exact creator heals its interrupted bootstrap", func(t *testing.T) {
		cfg := freshBootstrapIntegrationConfig(beadsDir, state.Port, "heal_exact_creator", "exact-creator")
		db, _, facts, err := openServerConnection(ctx, cfg)
		if err != nil {
			t.Fatalf("creator open: %v", err)
		}
		defer db.Close()
		if facts.bootstrapHeal == nil {
			t.Fatal("exact bare CREATE did not return a capability")
		}

		prepareFreshBootstrapV51Dirty(t, ctx, db, "creator_debris")
		if _, err := initSchemaOnDBWithBootstrapHeal(
			ctx, db, facts.bootstrapHeal, serverEndpointIdentity(cfg),
		); err != nil {
			t.Fatalf("creator-authorized migration: %v", err)
		}
		assertFreshBootstrapColumnCount(t, ctx, db, cfg.Database, "creator_debris", 0)
		assertFreshBootstrapConverged(t, ctx, db)
	})

	t.Run("fresh store initializes and commits cleanly", func(t *testing.T) {
		cfg := freshBootstrapIntegrationConfig(beadsDir, state.Port, "heal_fresh_store", "fresh-store")
		cfg.CommitterName = "Beads Test"
		cfg.CommitterEmail = "beads@example.com"
		store, err := New(ctx, cfg)
		if err != nil {
			t.Fatalf("open fresh store: %v", err)
		}
		defer store.Close()
		if err := store.SetConfig(ctx, "issue_prefix", "heal"); err != nil {
			t.Fatalf("set intentional init config: %v", err)
		}
		if err := store.CommitWithConfig(ctx, "bd init"); err != nil {
			t.Fatalf("config-inclusive init commit: %v", err)
		}
		assertFreshBootstrapConverged(t, ctx, store.db)
		var prefix string
		if err := store.db.QueryRowContext(ctx,
			"SELECT value FROM config AS OF 'HEAD' WHERE `key` = 'issue_prefix'",
		).Scan(&prefix); err != nil {
			t.Fatalf("read committed issue_prefix: %v", err)
		}
		if prefix != "heal" {
			t.Fatalf("committed issue_prefix = %q, want heal", prefix)
		}
	})

	t.Run("drop and recreate revokes creator capability", func(t *testing.T) {
		cfg := freshBootstrapIntegrationConfig(beadsDir, state.Port, "heal_creator_aba", "creator-aba")
		creatorDB, _, facts, err := openServerConnection(ctx, cfg)
		if err != nil {
			t.Fatalf("creator open: %v", err)
		}
		defer creatorDB.Close()
		if facts.bootstrapHeal == nil {
			t.Fatal("exact bare CREATE did not return a capability")
		}

		if _, err := admin.ExecContext(ctx, "DROP DATABASE `heal_creator_aba`"); err != nil {
			t.Fatalf("drop creator database: %v", err)
		}
		if _, err := admin.ExecContext(ctx, "CREATE DATABASE `heal_creator_aba`"); err != nil {
			t.Fatalf("create replacement database: %v", err)
		}
		replacement := openFreshBootstrapIntegrationDB(t, state.Port, cfg.Database)
		defer replacement.Close()
		prepareFreshBootstrapV51Dirty(t, ctx, replacement, "foreign_debris")

		_, migrateErr := initSchemaOnDBWithRetryAndGateBootstrapHeal(
			ctx, creatorDB, nil, facts.bootstrapHeal, serverEndpointIdentity(cfg),
		)
		var dirtyErr *schema.DirtyTablesError
		if !errors.As(migrateErr, &dirtyErr) {
			t.Fatalf("replacement migration error = %v, want DirtyTablesError", migrateErr)
		}
		assertFreshBootstrapColumnCount(t, ctx, replacement, cfg.Database, "foreign_debris", 1)
		if got := freshBootstrapSchemaVersion(t, ctx, replacement); got != 51 {
			t.Fatalf("replacement schema version = %d, want preserved v51", got)
		}
		if got := freshBootstrapDoltStatusCount(t, ctx, replacement); got == 0 {
			t.Fatal("replacement working set was cleared; wanted fail-closed preservation")
		}
	})

	t.Run("preexisting dirty database has no reset authority", func(t *testing.T) {
		const database = "heal_preexisting_dirty"
		if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+database+"`"); err != nil {
			t.Fatalf("create preexisting database: %v", err)
		}
		db := openFreshBootstrapIntegrationDB(t, state.Port, database)
		defer db.Close()
		prepareFreshBootstrapV51Dirty(t, ctx, db, "preexisting_debris")

		_, migrateErr := initSchemaOnDB(ctx, db)
		var dirtyErr *schema.DirtyTablesError
		if !errors.As(migrateErr, &dirtyErr) {
			t.Fatalf("preexisting migration error = %v, want DirtyTablesError", migrateErr)
		}
		assertFreshBootstrapColumnCount(t, ctx, db, database, "preexisting_debris", 1)
		if got := freshBootstrapDoltStatusCount(t, ctx, db); got == 0 {
			t.Fatal("preexisting working set was cleared; wanted fail-closed preservation")
		}
	})
}

func freshBootstrapIntegrationConfig(beadsDir string, port int, database, pathSuffix string) *Config {
	return &Config{
		Path:            filepath.Join(beadsDir, pathSuffix),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		ServerUser:      "root",
		Database:        database,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	}
}

func prepareFreshBootstrapV51Dirty(t *testing.T, ctx context.Context, db *sql.DB, debrisColumn string) {
	t.Helper()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin database: %v", err)
	}
	defer conn.Close()
	if _, err := schema.MigrateUpTo(ctx, conn, 51); err != nil {
		t.Fatalf("migrate database to v51: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'test: v51 baseline', '--author', 'Beads Test <beads@example.com>')",
	); err != nil {
		t.Fatalf("commit v51 baseline: %v", err)
	}
	if got := freshBootstrapSchemaVersion(t, ctx, conn); got != 51 {
		t.Fatalf("baseline schema version = %d, want 51", got)
	}
	if _, err := conn.ExecContext(ctx, "ALTER TABLE issues ADD COLUMN "+debrisColumn+" INT"); err != nil {
		t.Fatalf("dirty bootstrap database: %v", err)
	}
}

func openFreshBootstrapIntegrationDB(t *testing.T, port int, database string) *sql.DB {
	t.Helper()
	dsn := doltutil.ServerDSN{
		Host:     "127.0.0.1",
		Port:     port,
		User:     "root",
		Database: database,
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", database, err)
	}
	return db
}

type freshBootstrapQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func freshBootstrapSchemaVersion(t *testing.T, ctx context.Context, db freshBootstrapQueryer) int {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

func freshBootstrapDoltStatusCount(t *testing.T, ctx context.Context, db freshBootstrapQueryer) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&count); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	return count
}

func assertFreshBootstrapColumnCount(
	t *testing.T,
	ctx context.Context,
	db freshBootstrapQueryer,
	database string,
	column string,
	want int,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = ? AND table_name = 'issues' AND column_name = ?",
		database, column,
	).Scan(&count); err != nil {
		t.Fatalf("count %s.%s: %v", database, column, err)
	}
	if count != want {
		t.Fatalf("column %s.%s count = %d, want %d", database, column, count, want)
	}
}

func assertFreshBootstrapConverged(t *testing.T, ctx context.Context, db freshBootstrapQueryer) {
	t.Helper()
	want := schema.LatestVersion()
	if got := freshBootstrapSchemaVersion(t, ctx, db); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	if got := freshBootstrapDoltStatusCount(t, ctx, db); got != 0 {
		t.Fatalf("dolt_status rows after heal = %d, want 0", got)
	}
}
