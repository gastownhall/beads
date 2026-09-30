package dolt

import (
	"context"
	"errors"
	"fmt"
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

	t.Run("verb consent migrates", func(t *testing.T) {
		schema.SetSharedMigrateConsent(true)
		defer schema.SetSharedMigrateConsent(false)
		if err := openWith(&Config{}); err != nil {
			t.Fatalf("New (writable, consented) = %v, want success", err)
		}
		if got := cursor(); got != schema.LatestVersion() {
			t.Fatalf("cursor = %d after a consented open, want %d", got, schema.LatestVersion())
		}
	})
}
