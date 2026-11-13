package dolt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestDoltNew_SharedStoreMigrateGate_NoRemote is the server-mode half of
// gastownhall/beads#5920: a dolt sql-server database with NO remote at all.
//
// Before this gate, the first upgraded client to open such a database writably
// migrated it silently, promoting the schema cursor for every co-resident bd
// client on the same server — which then refused the database until each was
// upgraded too (~14 minutes of a locked-out team, in the incident). A
// sql-server store serves co-resident clients by construction, so the refusal
// does not depend on a remote existing.
//
// One CREATE/DROP DATABASE cycle covers the whole matrix: consecutive cycles
// destabilize the test dolt server, so the states are walked in order on a
// single database.
func TestDoltNew_SharedStoreMigrateGate_NoRemote(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	t.Setenv(schema.SmartGateEnv, "0")
	t.Cleanup(func() {
		schema.SetSharedMigrateConsent(false)
		schema.SetForceAllowRemoteMigrate(false)
	})

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// Precondition: no remote anywhere. This is the state the ordinary #4259
	// gate allows straight through.
	if remotes, err := store.ListRemotes(ctx); err != nil {
		t.Fatalf("ListRemotes: %v", err)
	} else if len(remotes) != 0 {
		t.Fatalf("precondition failed: %d remote(s) registered; this test covers the no-remote case", len(remotes))
	}

	// Put the database one migration behind the binary by regressing only the
	// recorded cursor, exactly as the #4259 integration test does.
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}

	openWith := func(cfg *Config) error {
		cfg.Path = tmpDir
		cfg.CommitterName = "test"
		cfg.CommitterEmail = "test@example.com"
		cfg.Database = dbName
		s, err := New(ctx, cfg)
		if err == nil {
			s.Close()
		}
		return err
	}
	cursor := func() int {
		var v int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read schema cursor: %v", err)
		}
		return v
	}

	behind := cursor()
	if behind != schema.LatestVersion()-1 {
		t.Fatalf("precondition failed: cursor = %d, want %d", behind, schema.LatestVersion()-1)
	}

	t.Run("writable open refuses without consent", func(t *testing.T) {
		err := openWith(&Config{})
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("New (writable, behind, no remote) = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if gateErr.Decision != "shared-no-remote" {
			t.Errorf("Decision = %q, want %q", gateErr.Decision, "shared-no-remote")
		}
		if got := cursor(); got != behind {
			t.Fatalf("the refused open promoted the cursor to %d (was %d) — this is the #5920 regression", got, behind)
		}
	})

	// The bd-578h9.5 contract: reads must keep working on the old schema while
	// the operator decides. Read-only opens never touch the schema at all.
	t.Run("read-only open still succeeds", func(t *testing.T) {
		if err := openWith(&Config{ReadOnly: true}); err != nil {
			t.Fatalf("New (read-only, behind, no remote) = %v, want success", err)
		}
		if got := cursor(); got != behind {
			t.Fatalf("read-only open promoted the cursor to %d (was %d)", got, behind)
		}
	})

	// openConsented opens the database writably under the verb's migrate
	// consent and keeps the store open, as `bd migrate schema` does: its root
	// pre-run opens the store, then the verb calls ApplySchemaMigrations on
	// that same store.
	openConsented := func(t *testing.T) *DoltStore {
		t.Helper()
		schema.SetSharedMigrateConsent(true)
		defer schema.SetSharedMigrateConsent(false)
		s, err := New(ctx, &Config{
			Path:           tmpDir,
			CommitterName:  "test",
			CommitterEmail: "test@example.com",
			Database:       dbName,
		})
		if err != nil {
			t.Fatalf("New (writable, consented) = %v, want success", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	wantApplied := func(t *testing.T, s *DoltStore, want int, why string) {
		t.Helper()
		applied, err := s.ApplySchemaMigrations(ctx)
		if err != nil {
			t.Fatalf("ApplySchemaMigrations (%s): %v", why, err)
		}
		if applied != want {
			t.Fatalf("ApplySchemaMigrations (%s) = %d, want %d", why, applied, want)
		}
	}

	// bd-pgsagt (gastownhall/beads#7380): the consented open applies the
	// pending migration itself, so the verb's explicit pass finds nothing
	// left. It must still report the open's migration, once, or `bd migrate
	// schema` prints "already at vN" and skips the version stamp.
	t.Run("verb consent migrates", func(t *testing.T) {
		s := openConsented(t)
		if got := cursor(); got != schema.LatestVersion() {
			t.Fatalf("cursor = %d after a consented open, want %d", got, schema.LatestVersion())
		}
		wantApplied(t, s, 1, "first call after a migrating open")
		wantApplied(t, s, 0, "second call on the same store")
	})

	// A fresh open of the now-current database applies nothing, so it has
	// nothing to report: the count above came from that open, not from a
	// value every store carries.
	t.Run("current open reports nothing", func(t *testing.T) {
		s, err := New(ctx, &Config{
			Path:           tmpDir,
			CommitterName:  "test",
			CommitterEmail: "test@example.com",
			Database:       dbName,
		})
		if err != nil {
			t.Fatalf("New (writable, current) = %v, want success", err)
		}
		defer s.Close()
		wantApplied(t, s, 0, "fresh open of a current database")
	})

	// The verb returns on a migration error without looking at the count, so
	// a failed explicit pass must not consume the open's migration: a retry
	// on the same store still reports it, once.
	t.Run("failed explicit pass keeps the open's count", func(t *testing.T) {
		if _, err := store.db.ExecContext(ctx,
			"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
			t.Fatalf("regress schema_migrations: %v", err)
		}
		s := openConsented(t)
		if got := cursor(); got != schema.LatestVersion() {
			t.Fatalf("cursor = %d after a consented open, want %d", got, schema.LatestVersion())
		}
		// An unparseable DSN makes openMigrationDB fail deterministically.
		goodConnStr := s.connStr
		s.connStr = "invalid"
		applied, err := s.ApplySchemaMigrations(ctx)
		s.connStr = goodConnStr
		if err == nil || !strings.Contains(err.Error(), "failed to parse DSN for migration connection") {
			t.Fatalf("ApplySchemaMigrations with a broken DSN = (%d, %v), want the DSN parse error", applied, err)
		}
		wantApplied(t, s, 1, "retry after a failed call")
		wantApplied(t, s, 0, "call after the retry reported it")
	})
}
