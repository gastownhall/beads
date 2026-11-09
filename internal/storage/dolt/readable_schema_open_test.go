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

// TestDoltNew_CloneLocalReadableSchema is the gastownhall/beads#7038
// regression: the documented consumer-clone recipe — `bd dolt pull` on the old
// binary, then upgrade — brings the replicated main-series schema but never
// the clone-local (ignored) one, so the clone sits at main-latest with an
// outdated lease schema. A read-only open that asks for a readable schema
// must refuse such a database with the typed clone-local refusal — naming the
// missing lease column and the per-database migration remedy — instead of
// opening fine and failing every issue read on a raw unknown-column error.
//
// The fixture deliberately reproduces both a stale cursor (ignored rows
// regressed to 13) and a missing read column (granted_node, ignored/0016):
// the sentinel probe clamps the contradicted cursor to its replay floor, so
// the refusal's effective version is asserted against CurrentIgnoredVersion,
// not against the raw 13.
func TestDoltNew_CloneLocalReadableSchema(t *testing.T) {
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
	ignoredCursor := func() int {
		var v int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read ignored cursor: %v", err)
		}
		return v
	}
	headCommit := func() string {
		var hash string
		if err := store.db.QueryRowContext(ctx,
			"SELECT COMMIT_HASH FROM dolt_log LIMIT 1").Scan(&hash); err != nil {
			t.Fatalf("read HEAD: %v", err)
		}
		return hash
	}

	// The #7038 consumer-clone shape: main current, clone-local series behind
	// with the granted_node column ignored/0016 adds missing. Both objects
	// are dolt-ignored, so the fixture leaves nothing in the working set.
	for _, stmt := range []string{
		"ALTER TABLE leases DROP COLUMN granted_node",
		"DELETE FROM ignored_schema_migrations WHERE version > 13",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rawCursor, headBefore := ignoredCursor(), headCommit()
	if rawCursor != 13 {
		t.Fatalf("fixture: ignored cursor = %d, want 13", rawCursor)
	}

	// The refused open changes nothing: not the raw cursor, not HEAD.
	defer func() {
		if got := ignoredCursor(); got != rawCursor {
			t.Errorf("a refused read-only open moved the ignored cursor from %d to %d", rawCursor, got)
		}
		if got := headCommit(); got != headBefore {
			t.Errorf("a refused read-only open moved HEAD from %s to %s", headBefore, got)
		}
	}()

	t.Run("read-only open refuses with the typed clone-local refusal", func(t *testing.T) {
		err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true})
		var e *schema.CloneLocalBehindError
		if !errors.As(err, &e) {
			t.Fatalf("New (read-only, clone-local behind, granted_node missing) = %T (%v), want *schema.CloneLocalBehindError, not an open whose issue reads fail on the raw column", err, err)
		}
		// The effective cursor is sentinel-clamped (the raw 13 is contradicted
		// by the missing column), so it is asserted against the same
		// sentinel-aware read the check itself uses, never against the raw 13.
		effective, effErr := schema.CurrentIgnoredVersion(ctx, store.db)
		if effErr != nil {
			t.Fatalf("CurrentIgnoredVersion: %v", effErr)
		}
		if e.DBIgnoredVersion != effective || e.DBIgnoredVersion >= e.LatestIgnoredVersion {
			t.Errorf("CloneLocalBehindError local versions = %d of %d, want the effective cursor %d behind the latest", e.DBIgnoredVersion, e.LatestIgnoredVersion, effective)
		}
		if e.MainVersion != schema.LatestVersion() || e.LatestMainVersion != schema.LatestVersion() {
			t.Errorf("CloneLocalBehindError claims main %d of %d, want a current main series", e.MainVersion, e.LatestMainVersion)
		}
		if len(e.MissingColumns) != 1 || e.MissingColumns[0] != "granted_node" {
			t.Errorf("MissingColumns = %v, want [granted_node]", e.MissingColumns)
		}
		msg := err.Error()
		if !strings.Contains(msg, "granted_node") || !strings.Contains(msg, "bd migrate schema") {
			t.Errorf("refusal %q does not name the missing column and the per-database remedy", msg)
		}
		if !strings.Contains(msg, "never arrive with `bd dolt pull`") {
			t.Errorf("refusal %q does not explain why the pull could not bring this schema", msg)
		}
	})

	t.Run("an open that does not ask still opens", func(t *testing.T) {
		// bd doctor's diagnostic opens must reach the database to report it.
		if err := openWith(&Config{ReadOnly: true}); err != nil {
			t.Fatalf("New (read-only, no RequireReadableSchema) = %v, want success", err)
		}
	})

	t.Run("gateway open names the server administrator", func(t *testing.T) {
		err := openWith(&Config{Gateway: true, RequireReadableSchema: true})
		var e *schema.CloneLocalBehindError
		if !errors.As(err, &e) || !e.Gateway {
			t.Fatalf("New (gateway, clone-local behind) = %T (%v), want a gateway *schema.CloneLocalBehindError", err, err)
		}
		if msg := err.Error(); strings.Contains(msg, "bd migrate schema") {
			t.Errorf("gateway refusal %q prescribes `bd migrate schema`", msg)
		} else if !strings.Contains(msg, "server administrator") {
			t.Errorf("gateway refusal %q does not direct the operator to the server administrator", msg)
		}
	})

	t.Run("the escape hatch opens, and the raw failure is what reads see", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		ro, err := New(ctx, &Config{
			Path:                  tmpDir,
			CommitterName:         "test",
			CommitterEmail:        "test@example.com",
			Database:              dbName,
			ReadOnly:              true,
			RequireReadableSchema: true,
		})
		if err != nil {
			t.Fatalf("New (read-only, BD_IGNORE_SCHEMA_SKEW=1) = %v, want success", err)
		}
		defer ro.Close()
		// The hatch only skips the refusal; it cannot make the reads work.
		if _, err := ro.SearchIssues(ctx, "", types.IssueFilter{}); err == nil {
			t.Errorf("SearchIssues on a schema missing granted_node = nil, want the unknown-column failure the hatch accepted")
		}
	})

	t.Run("behind with every projected lease column stays readable", func(t *testing.T) {
		// ignored/0016's guarded ALTER, applied out of band: the cursor stays
		// behind, but every column the issue projection selects is present,
		// so reads keep working through the window (#7038 AC-5).
		if _, err := store.db.ExecContext(ctx,
			"ALTER TABLE leases ADD COLUMN granted_node VARCHAR(255) NOT NULL DEFAULT ''"); err != nil {
			t.Fatalf("re-add granted_node: %v", err)
		}
		ro, err := New(ctx, &Config{
			Path:                  tmpDir,
			CommitterName:         "test",
			CommitterEmail:        "test@example.com",
			Database:              dbName,
			ReadOnly:              true,
			RequireReadableSchema: true,
		})
		if err != nil {
			t.Fatalf("New (read-only, clone-local behind, columns present) = %v, want success", err)
		}
		defer ro.Close()
		if _, err := ro.CountIssues(ctx, "", types.IssueFilter{}); err != nil {
			t.Errorf("CountIssues (clone-local behind, columns present) = %v, want success", err)
		}
		if _, err := ro.SearchIssues(ctx, "", types.IssueFilter{}); err != nil {
			t.Errorf("SearchIssues (clone-local behind, columns present) = %v, want success", err)
		}
	})

	t.Run("main-series refusal keeps precedence", func(t *testing.T) {
		// Both lanes behind, and the main one is what the reads cannot
		// query: the main refusal fires, not the clone-local one.
		if _, err := store.db.ExecContext(ctx,
			"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
			t.Fatalf("regress schema_migrations: %v", err)
		}
		runMigrationSQL(t, ctx, store, "../schema/migrations/0073_add_arrival_seq.down.sql")
		err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true})
		var behind *schema.SchemaBehindError
		if !errors.As(err, &behind) {
			t.Fatalf("New (both lanes behind) = %T (%v), want the main-series *schema.SchemaBehindError", err, err)
		}
		if schema.IsCloneLocalBehindError(err) {
			t.Errorf("both-lanes refusal %v is also a clone-local refusal; the main error must keep precedence", err)
		}
	})

	t.Run("main behind with a complete projection gets the clone-local refusal", func(t *testing.T) {
		// Main behind by its cursor alone (0073's DDL restored, its cursor
		// row still regressed, so the issues projection is complete),
		// clone-local behind for real: the clone-local refusal must not
		// claim the shared schema is current.
		runMigrationSQL(t, ctx, store, "../schema/migrations/0073_add_arrival_seq.up.sql")
		if _, err := store.db.ExecContext(ctx,
			"ALTER TABLE leases DROP COLUMN granted_node"); err != nil {
			t.Fatalf("drop granted_node again: %v", err)
		}
		err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true})
		var e *schema.CloneLocalBehindError
		if !errors.As(err, &e) {
			t.Fatalf("New (main cursor-behind, clone-local behind) = %T (%v), want *schema.CloneLocalBehindError", err, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "designated-migrator/pull procedure") {
			t.Errorf("refusal on a main-behind database %q does not send the operator to the migrator procedure first", msg)
		}
		if strings.Contains(msg, "no further shared-schema migration consent") {
			t.Errorf("refusal on a main-behind database %q claims the shared schema is current", msg)
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

// TestDoltNew_CloneLocalBehindOpensNeverMigrate covers the opens with no
// authority to apply the clone-local series against the incompatible
// clone-local fixture (#7038 AC-4), snapshotting the raw cursor, the lease
// column and HEAD around each open. None of them migrates: a gateway open
// that asks for a readable schema gets the typed refusal, naming the server
// administrator rather than a command bd would refuse to run there, while a preview's and a diagnostic probe's opens, which do not ask,
// keep opening as before (bd doctor and the version-bump probe must reach
// the database to report on it).
func TestDoltNew_CloneLocalBehindOpensNeverMigrate(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	open, store := cloneLocalBehindFixture(t, ctx, false)
	before := cloneLocalState(t, ctx, store)
	unchanged := func(t *testing.T) {
		t.Helper()
		if after := cloneLocalState(t, ctx, store); after != before {
			t.Errorf("open changed the database: before %s, after %s", before, after)
		}
	}

	t.Run("gateway open names the server administrator", func(t *testing.T) {
		s, err := open(&Config{Gateway: true, RequireReadableSchema: true})
		if err == nil {
			s.Close()
		}
		var behind *schema.CloneLocalBehindError
		if !errors.As(err, &behind) || !behind.Gateway {
			t.Fatalf("New (gateway, granted_node missing) = %T (%v), want a gateway *schema.CloneLocalBehindError", err, err)
		}
		if !strings.Contains(err.Error(), "server administrator") || strings.Contains(err.Error(), "bd migrate schema") {
			t.Errorf("gateway refusal %q must send the operator to the server administrator, not to `bd migrate schema`", err)
		}
		unchanged(t)
	})

	for _, tc := range []struct {
		name string
		cfg  *Config
	}{
		{"preview open keeps its own policy", &Config{ReadOnly: true, Preview: true}},
		{"diagnostic probe open still opens", &Config{ReadOnly: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := open(tc.cfg)
			if err != nil {
				t.Fatalf("New (%+v) = %v, want success: an open that does not ask for a readable schema must still reach the database", *tc.cfg, err)
			}
			s.Close()
			unchanged(t)
		})
	}

	t.Run("the escape hatch reads anyway", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		s, err := open(&Config{ReadOnly: true, RequireReadableSchema: true})
		if err != nil {
			t.Fatalf("New (read-only, BD_IGNORE_SCHEMA_SKEW=1) = %v, want success", err)
		}
		s.Close()
		unchanged(t)
	})
}

// TestDoltNew_CloneLocalBehindCompleteProjectionNoMigration is #7038's AC-5:
// the upgrade window's other half. The pull-then-upgrade recipe leaves the
// clone-local cursor behind, and a behind cursor is not by itself a problem:
// the refusal exists to stop reads that cannot work, not to force
// convergence. With every lease column the issue projection selects present
// (the fixture regresses the cursor alone), a read-only open that asks for a
// readable schema keeps opening and answering reads, twice over (the window
// stays open for the whole upgrade, not just the first command), and applies
// nothing: not the cursor, not the lease schema, not HEAD.
//
// A guard, green against a base with no clone-local refusal at all: it holds
// the window open against an over-firing fix, while the missing-column tests
// above hold the refusal shut.
func TestDoltNew_CloneLocalBehindCompleteProjectionNoMigration(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	open, store := cloneLocalBehindFixture(t, ctx, true)
	if v, err := schema.CurrentIgnoredVersion(ctx, store.db); err != nil || v >= schema.LatestIgnoredVersion() {
		t.Fatalf("precondition: effective clone-local cursor = %d (%v), want behind v%d", v, err, schema.LatestIgnoredVersion())
	}
	before := cloneLocalState(t, ctx, store)

	for i := range 2 {
		ro, err := open(&Config{ReadOnly: true, RequireReadableSchema: true})
		if err != nil {
			t.Fatalf("open %d: New (read-only, clone-local cursor behind, lease columns present) = %v, want success: reads keep working through the window", i+1, err)
		}
		issues, err := ro.SearchIssues(ctx, "", types.IssueFilter{})
		ro.Close()
		if err != nil || len(issues) != 1 || issues[0].ID != "cl-1" {
			t.Fatalf("open %d: SearchIssues = %d issues, %v; want cl-1", i+1, len(issues), err)
		}
		if after := cloneLocalState(t, ctx, store); after != before {
			t.Errorf("open %d: read-only open changed the database: before %s, after %s", i+1, before, after)
		}
	}
}
