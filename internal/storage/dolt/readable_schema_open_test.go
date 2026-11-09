package dolt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoltNew_RequireReadableSchema covers server mode's refusal of a schema
// too old for this binary's issue reads. A read-only open never migrates, so
// after a bd upgrade it is routinely one migration behind; where that
// migration added a column the shared projection selects, every list, show and
// ready would fail on the unknown column. An open that sets
// RequireReadableSchema refuses such a database with SchemaBehindError, while a
// behind database that still has every projected column keeps opening, so
// reads keep working through the upgrade window (bd-578h9.5, #5920).
//
// One CREATE/DROP DATABASE cycle covers the matrix, as in
// TestDoltNew_SharedStoreMigrateGate_NoRemote, and no step opens it writable:
// a writable open would migrate it back to the latest schema.
func TestDoltNew_RequireReadableSchema(t *testing.T) {
	skipIfNoDolt(t)

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
	behind := schema.LatestVersion() - 1

	// Behind by the cursor alone: the schema change itself stays applied, the
	// state a co-resident client sees while its operator decides to migrate.
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	t.Run("behind with every projected column opens", func(t *testing.T) {
		if err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true}); err != nil {
			t.Fatalf("New (read-only, cursor behind, columns present) = %v, want success: reads keep working on the old schema", err)
		}
	})

	// Behind for real: 0073's down takes arrival_seq off both planes, the
	// shape of a database the previous release migrated.
	runMigrationSQL(t, ctx, store, "../schema/migrations/0073_add_arrival_seq.down.sql")

	t.Run("read-only open refuses a schema its reads cannot query", func(t *testing.T) {
		err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true})
		var behindErr *schema.SchemaBehindError
		if !errors.As(err, &behindErr) {
			t.Fatalf("New (read-only, missing arrival_seq) = %T (%v), want *schema.SchemaBehindError, not an open that fails each read later", err, err)
		}
		if behindErr.DBVersion != behind || behindErr.BinaryVersion != schema.LatestVersion() || behindErr.Gateway {
			t.Errorf("SchemaBehindError = %+v, want v%d behind v%d, not a gateway's", *behindErr, behind, schema.LatestVersion())
		}
		if !strings.Contains(err.Error(), "bd migrate schema") {
			t.Errorf("refusal %q does not name `bd migrate schema`", err)
		}
	})

	t.Run("an open that does not ask still opens", func(t *testing.T) {
		// bd doctor's opens: they must reach a behind database to report it.
		if err := openWith(&Config{ReadOnly: true}); err != nil {
			t.Fatalf("New (read-only, no RequireReadableSchema) = %v, want success", err)
		}
	})

	t.Run("gateway open names the server administrator", func(t *testing.T) {
		// A gateway never migrates, writable or not, and bd cannot migrate
		// the schema it owns.
		err := openWith(&Config{Gateway: true, RequireReadableSchema: true})
		var behindErr *schema.SchemaBehindError
		if !errors.As(err, &behindErr) || !behindErr.Gateway {
			t.Fatalf("New (gateway, missing arrival_seq) = %T (%v), want a gateway *schema.SchemaBehindError", err, err)
		}
		if strings.Contains(err.Error(), "bd migrate schema") {
			t.Errorf("gateway refusal %q prescribes `bd migrate schema`", err)
		}
	})

	t.Run("the escape hatch reads anyway", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		if err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true}); err != nil {
			t.Fatalf("New (read-only, BD_IGNORE_SCHEMA_SKEW=1) = %v, want success", err)
		}
	})
}

// cloneLocalBehindFixture creates a database at the latest main schema, with
// one issue, and puts its clone-local (dolt-ignored) series where a clone
// that followed the "pull on the old binary, then upgrade" recipe leaves it
// (gastownhall/beads#7038): ignored/0016's leases.granted_node, which every
// issue read selects, never applied, and the ignored cursor at 13, the
// highest any pre-1.3.0 binary recorded. `bd dolt pull` brings the main
// series but never this one, so nothing else moves it.
//
// With keepColumn the cursor alone is behind, granted_node stays: the shape a
// read still survives. It returns the open function for further stores on the
// same database and the writable store whose pool the probes below read
// through. No step may open the database writable again: that would migrate
// it back.
func cloneLocalBehindFixture(t *testing.T, ctx context.Context, keepColumn bool) (func(*Config) (*DoltStore, error), *DoltStore) {
	t.Helper()
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
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	})
	if err := store.SetConfig(ctx, "issue_prefix", "cl"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	now := time.Now()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "cl-1", Title: "readable once the clone-local series applies",
		Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask,
		CreatedAt: now, UpdatedAt: now,
	}, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	stmts := []string{"DELETE FROM ignored_schema_migrations WHERE version > 13"}
	if !keepColumn {
		stmts = append(stmts, "ALTER TABLE leases DROP COLUMN granted_node")
	}
	for _, stmt := range stmts {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if v, err := schema.CurrentVersion(ctx, store.db); err != nil || v != schema.LatestVersion() {
		t.Fatalf("precondition: main cursor = %d (%v), want the latest v%d", v, err, schema.LatestVersion())
	}
	open := func(cfg *Config) (*DoltStore, error) {
		cfg.Path = tmpDir
		cfg.CommitterName = "test"
		cfg.CommitterEmail = "test@example.com"
		cfg.Database = dbName
		return New(ctx, cfg)
	}
	return open, store
}

// cloneLocalState is what an open that must not migrate must leave alone: the
// raw ignored cursor, whether leases.granted_node exists, and HEAD.
func cloneLocalState(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	var cursor, columns int
	var head string
	if err := store.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&cursor); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'leases' AND column_name = 'granted_node'").Scan(&columns); err != nil {
		t.Fatalf("probe leases.granted_node: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return fmt.Sprintf("ignored cursor %d, granted_node columns %d, HEAD %s", cursor, columns, head)
}

// TestDoltNew_CloneLocalReadableSchema covers gastownhall/beads#7038: after
// the documented old-binary pull and upgrade, a database has the latest main
// schema but an older clone-local series, and leases.granted_node, which the
// shared issue projection selects, is missing. A read-only open never
// migrates (bd-6dnrw.32), and the main-series check passes, so the open used
// to succeed and every list, count and search then failed on the unknown
// column. An open that asks for a readable schema refuses it instead, with a
// typed error naming the column and the per-database remedy, and changes
// nothing.
func TestDoltNew_CloneLocalReadableSchema(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	open, store := cloneLocalBehindFixture(t, ctx, false)
	before := cloneLocalState(t, ctx, store)

	ro, err := open(&Config{ReadOnly: true, RequireReadableSchema: true})
	if err == nil {
		_, readErr := ro.SearchIssues(ctx, "", types.IssueFilter{})
		ro.Close()
		t.Fatalf("New (read-only, clone-local series behind, granted_node missing) succeeded; its first issue read returned %v", readErr)
	}
	var behind *schema.CloneLocalBehindError
	if !errors.As(err, &behind) {
		t.Fatalf("New (read-only, granted_node missing) = %T (%v), want *schema.CloneLocalBehindError", err, err)
	}
	if behind.Gateway || behind.MainVersion != schema.LatestVersion() ||
		behind.LatestIgnoredVersion != schema.LatestIgnoredVersion() ||
		behind.IgnoredVersion >= schema.LatestIgnoredVersion() {
		t.Errorf("CloneLocalBehindError = %+v, want a direct (non-gateway) refusal at the latest main version with the clone-local series behind", *behind)
	}
	if len(behind.MissingColumns) != 1 || behind.MissingColumns[0] != "granted_node" || behind.Table != "leases" {
		t.Errorf("CloneLocalBehindError names %s.%v, want leases.[granted_node]", behind.Table, behind.MissingColumns)
	}
	msg := err.Error()
	for _, want := range []string{"granted_node", "bd dolt pull", "bd migrate schema"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
	for _, never := range []string{"--force", "re-clone"} {
		if strings.Contains(msg, never) {
			t.Errorf("refusal %q prescribes %q", msg, never)
		}
	}
	if after := cloneLocalState(t, ctx, store); after != before {
		t.Errorf("refused open changed the database: before %s, after %s", before, after)
	}
}

// TestDoltNew_CloneLocalBehindWithLeaseColumnsStaysReadable is the control
// for TestDoltNew_CloneLocalReadableSchema: a clone-local series behind by its
// cursor alone, with every lease column the issue reads select present, keeps
// opening read-only and answering reads, and the open applies nothing. Only a
// missing column makes the refusal worth its cost, as with the main series
// (bd-578h9.5).
func TestDoltNew_CloneLocalBehindWithLeaseColumnsStaysReadable(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	open, store := cloneLocalBehindFixture(t, ctx, true)
	if v, err := schema.CurrentIgnoredVersion(ctx, store.db); err != nil || v >= schema.LatestIgnoredVersion() {
		t.Fatalf("precondition: clone-local cursor = %d (%v), want behind v%d", v, err, schema.LatestIgnoredVersion())
	}
	before := cloneLocalState(t, ctx, store)

	ro, err := open(&Config{ReadOnly: true, RequireReadableSchema: true})
	if err != nil {
		t.Fatalf("New (read-only, clone-local cursor behind, lease columns present) = %v, want success", err)
	}
	issues, err := ro.SearchIssues(ctx, "", types.IssueFilter{})
	ro.Close()
	if err != nil || len(issues) != 1 {
		t.Fatalf("SearchIssues = %d issues, %v; want cl-1", len(issues), err)
	}
	if after := cloneLocalState(t, ctx, store); after != before {
		t.Errorf("read-only open changed the database: before %s, after %s", before, after)
	}
}
