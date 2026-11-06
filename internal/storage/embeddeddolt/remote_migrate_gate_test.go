//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// captureEmbeddedStderr runs fn with os.Stderr redirected and returns what it
// wrote, so a test can assert which warnings an open printed (and which it did
// not get to print).
func captureEmbeddedStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

// TestEmbeddedRemoteMigrateGate_BlocksReopen verifies that the #4259
// remote-migrate gate also protects embedded mode (the mode the original report
// was filed against): reopening an existing, remote-backed embedded database
// that is behind the binary must refuse to auto-migrate.
func TestEmbeddedRemoteMigrateGate_BlocksReopen(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Regress one migration and register a remote on a raw SQL connection so the
	// reopen sees a behind, remote-backed database.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	_ = cleanup()
	store.Close()

	// Reopen must hit the gate.
	reopened, reErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if reErr == nil {
		reopened.Close()
		t.Fatal("Open (reopen) = nil, want *schema.RemoteMigrateGateError for a behind, remote-backed DB")
	}
	if !schema.IsRemoteMigrateGateError(reErr) {
		t.Fatalf("error = %T (%v), want error wrapping *schema.RemoteMigrateGateError", reErr, reErr)
	}
}

// TestEmbeddedOpenReadOnly_SkipsGateAndMigrations covers bd-6dnrw.32: the
// read-only embedded open (used for cross-repo hydration, GH#3231) must not
// run the remote-migrate gate, must not write anything into the target's
// history or working set, and must refuse write transactions — while plain
// Open of the same behind, remote-backed database stays gated.
func TestEmbeddedOpenReadOnly_SkipsGateAndMigrations(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database, with a marker config row.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "ro"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	store.Close()

	snapshot := func() (commits, dirty int) {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commits); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
			t.Fatalf("count dolt_status: %v", err)
		}
		return commits, dirty
	}
	commitsBefore, dirtyBefore := snapshot()

	// Read-only open of an up-to-date database: reads work, writes are refused.
	ro, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if got, err := ro.GetConfig(ctx, "issue_prefix"); err != nil || got != "ro" {
		t.Fatalf("GetConfig = %q, %v; want %q, nil", got, err, "ro")
	}
	if err := ro.SetConfig(ctx, "issue_prefix", "mutated"); err == nil {
		t.Fatal("SetConfig on read-only store = nil error, want refusal")
	}
	if _, err := ro.ApplySchemaMigrations(ctx); err == nil {
		t.Fatal("ApplySchemaMigrations on read-only store = nil error, want refusal")
	}
	// Version-control mutations bypass withConn's commit guard (they run via
	// withDBConn, outside any SQL transaction) and must be refused too
	// (bd-578h9.12).
	if err := ro.Branch(ctx, "ro-test-branch"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Branch on read-only store = %v, want read-only refusal", err)
	}
	if err := ro.Push(ctx); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Push on read-only store = %v, want read-only refusal", err)
	}
	if _, err := ro.Merge(ctx, "main"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Merge on read-only store = %v, want read-only refusal", err)
	}
	ro.Close()

	if commitsAfter, dirtyAfter := snapshot(); commitsAfter != commitsBefore || dirtyAfter != dirtyBefore {
		t.Fatalf("read-only open mutated the database: commits %d -> %d, dirty tables %d -> %d",
			commitsBefore, commitsAfter, dirtyBefore, dirtyAfter)
	}

	// Make the database behind and remote-backed: plain Open must hit the gate,
	// the read-only open must not.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	_ = cleanup()

	if gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main"); gateErr == nil {
		gated.Close()
		t.Fatal("Open of behind, remote-backed DB = nil error, want remote-migrate gate")
	} else if !schema.IsRemoteMigrateGateError(gateErr) {
		t.Fatalf("Open error = %T (%v), want *schema.RemoteMigrateGateError", gateErr, gateErr)
	}

	// The read-only open stays exempt from the remote-migrate gate, but a
	// behind database now fails at open with a clear behind-drift error
	// instead of unknown-column errors at query time (bd-578h9.12)...
	if ro2, roErr := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main"); roErr == nil {
		ro2.Close()
		t.Fatal("OpenReadOnly of behind DB = nil error, want *schema.SchemaBehindError")
	} else if schema.IsRemoteMigrateGateError(roErr) {
		t.Fatalf("OpenReadOnly error = remote-migrate gate (%v); read-only opens must stay exempt from it", roErr)
	} else if !schema.IsSchemaBehindError(roErr) {
		t.Fatalf("OpenReadOnly error = %T (%v), want *schema.SchemaBehindError", roErr, roErr)
	}

	// ...and BD_IGNORE_SCHEMA_SKEW=1 keeps the escape hatch open.
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	ro2, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenReadOnly (behind, skew ignored) = %v, want nil", err)
	}
	if got, err := ro2.GetConfig(ctx, "issue_prefix"); err != nil || got != "ro" {
		t.Fatalf("GetConfig after skew-ignored open = %q, %v; want %q, nil", got, err, "ro")
	}
	ro2.Close()
}

// TestEmbeddedOpenReadOnly_FreshDatabaseNoWrites covers the fresh-database
// half of the strict --readonly hermeticity contract (gastownhall/beads#4930
// maintainer review, 2026-07-23): OpenReadOnly against a beadsDir that has no
// embeddeddolt/ data directory yet must fail rather than create one — unlike
// Open/OpenForReadOnlyCommand, which create the data directory and initialize
// the schema on first use.
func TestEmbeddedOpenReadOnly_FreshDatabaseNoWrites(t *testing.T) {
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Fatalf("beadsDir must not pre-exist: stat error = %v", err)
	}

	ro, err := embeddeddolt.OpenReadOnly(ctx, beadsDir, "testdb", "main")
	if err == nil {
		ro.Close()
		t.Fatal("OpenReadOnly on a fresh (nonexistent) database = nil error, want refusal")
	}

	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("OpenReadOnly on a fresh database created %s (stat error: %v)", dataDir, statErr)
	}
	if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
		t.Fatalf("OpenReadOnly on a fresh database created %s (stat error: %v)", beadsDir, statErr)
	}
}

// TestEmbeddedOpenForReadOnlyCommand_LenientGate covers bd-578h9.5: a
// read-only command's open of a behind, remote-backed embedded database must
// not be bricked by the remote-migrate gate. The open succeeds WITHOUT
// migrating (the schema stays behind), while plain Open of the same database
// stays gated.
func TestEmbeddedOpenForReadOnlyCommand_LenientGate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Create and fully migrate the embedded database.
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Regress the cursor to just below 0073 (arrival_seq) and register a
	// remote so the reopen sees a behind, remote-backed database. Not just
	// one migration back: the dirty-table refusal at the end needs 0073,
	// which alters issues, among the pending migrations, and a migration
	// recorded above it would hold the cursor past it.
	const arrivalSeqVersion = 73
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version >= ?", arrivalSeqVersion); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	_ = cleanup()
	store.Close()

	// Plain Open must stay gated.
	if gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main"); gateErr == nil {
		gated.Close()
		t.Fatal("Open (reopen) = nil, want remote-migrate gate refusal")
	} else if !schema.IsRemoteMigrateGateError(gateErr) {
		t.Fatalf("Open error = %T (%v), want *schema.RemoteMigrateGateError", gateErr, gateErr)
	}

	// The read-only-command open must succeed without migrating.
	roStore, roErr := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main")
	if roErr != nil {
		t.Fatalf("OpenForReadOnlyCommand: %v", roErr)
	}
	roStore.Close()

	db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	var count int
	if err := db2.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", schema.LatestVersion()).Scan(&count); err != nil {
		_ = cleanup2()
		t.Fatalf("read schema_migrations: %v", err)
	}
	if count != 0 {
		_ = cleanup2()
		t.Fatalf("lenient-gate open applied the pending migration (version %d present); it must not migrate", schema.LatestVersion())
	}

	// The read-through ends where the reads would fail: a schema that lacks a
	// column this binary's issue reads select (arrival_seq, the shape a
	// database the previous release migrated has) is refused at open instead
	// of warned about and then failed on every query.
	for _, stmt := range []string{
		"ALTER TABLE issues DROP COLUMN arrival_seq",
		"ALTER TABLE wisps DROP COLUMN arrival_seq",
	} {
		if _, err := db2.ExecContext(ctx, stmt); err != nil {
			_ = cleanup2()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = cleanup2()
	if unreadable, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main"); err == nil {
		unreadable.Close()
		t.Fatal("OpenForReadOnlyCommand of a schema without arrival_seq = nil error, want *schema.SchemaBehindError")
	} else if !schema.IsSchemaBehindError(err) {
		t.Fatalf("OpenForReadOnlyCommand error = %T (%v), want *schema.SchemaBehindError", err, err)
	}

	// The same limit holds for the other refusal a read-only command
	// tolerates. With the remote gone and consent given, the gate passes and
	// the migration is refused by the #4566 dirty-table guard instead: the
	// DROP COLUMNs above left issues dirty, and the pending 0073 alters it.
	db3, cleanup3, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (remove remote): %v", err)
	}
	if _, err := db3.ExecContext(ctx, "CALL DOLT_REMOTE('remove', 'origin')"); err != nil {
		_ = cleanup3()
		t.Fatalf("remove remote: %v", err)
	}
	_ = cleanup3()
	t.Setenv(schema.AllowMigrateEnv, "1")
	if unreadable, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main"); err == nil {
		unreadable.Close()
		t.Fatal("OpenForReadOnlyCommand past a dirty-table refusal of a schema without arrival_seq = nil error, want *schema.SchemaBehindError")
	} else if !schema.IsSchemaBehindError(err) {
		t.Fatalf("OpenForReadOnlyCommand past a dirty-table refusal: error = %T (%v), want *schema.SchemaBehindError", err, err)
	}
}

// TestEmbeddedOpenForReadOnlyCommand_LenientGateRefusesUnreadableOverlay is
// the #6572 regression for the guard's remaining gap. The lenient gate
// promises "writes blocked, reads fine", and requireReadableSchema makes that
// promise true for the issues projection (#7912) — but a hydrated issue read
// also joins the clone-local leases overlay and falls back to wisps, so a
// behind, remote-backed database whose issues rows are intact but whose
// overlay is absent or short a column still cannot answer a single read. The
// open must refuse that database up front, naming what is missing, with both
// the gate and the schema-behind classification reachable from the error —
// not print the continuation warning and fail every query after it.
//
// Each case regresses ONLY the main migration cursor (the ignored series,
// which owns leases and the wisp plane, stays at its latest), registers a
// file remote so the gate fires, and breaks one overlay relation while the
// issues rows stay complete.
func TestEmbeddedOpenForReadOnlyCommand_LenientGateRefusesUnreadableOverlay(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	// Blunt-gate coverage: pin the (default-on) smart router off.
	t.Setenv(schema.SmartGateEnv, "0")
	// The skew escape hatch must not mute the refusal under test.
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	for _, tc := range []struct {
		name         string
		breakOverlay []string
		missing      []string
	}{
		{
			// A fresh clone materializes leases only through the ignored
			// series' replay, which a read-only open never runs (0055):
			// behind on main and never written to, it has no leases table.
			name:         "no leases table",
			breakOverlay: []string{"DROP TABLE leases"},
			missing:      []string{"leases"},
		},
		{
			name:         "leases missing granted_node",
			breakOverlay: []string{"ALTER TABLE leases DROP COLUMN granted_node"},
			missing:      []string{"leases.granted_node"},
		},
		{
			name:         "wisps missing storage_class",
			breakOverlay: []string{"ALTER TABLE wisps DROP COLUMN storage_class"},
			missing:      []string{"wisps.storage_class"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			dataDir := filepath.Join(beadsDir, "embeddeddolt")

			// A fully migrated store with one seeded issue.
			store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := store.SetConfig(ctx, "issue_prefix", "lgr"); err != nil {
				store.Close()
				t.Fatalf("SetConfig: %v", err)
			}
			if err := store.CreateIssue(ctx, &types.Issue{
				ID: "probe1", Title: "probe", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			}, "tester"); err != nil {
				store.Close()
				t.Fatalf("CreateIssue: %v", err)
			}
			store.Close()

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					_ = cleanup()
					t.Fatalf("%s: %v", query, err)
				}
			}
			exec(fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", schema.LatestVersion()))
			exec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
			for _, stmt := range tc.breakOverlay {
				exec(stmt)
			}
			_ = cleanup()

			var openErr error
			stderr := captureEmbeddedStderr(t, func() {
				var ro *embeddeddolt.EmbeddedDoltStore
				ro, openErr = embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main")
				if openErr == nil {
					ro.Close()
				}
			})
			if openErr == nil {
				t.Fatal("OpenForReadOnlyCommand = nil error, want refusal: the lenient gate's continuation promise is false for a database whose hydration overlay is unreadable")
			}
			if !schema.IsRemoteMigrateGateError(openErr) {
				t.Fatalf("OpenForReadOnlyCommand error = %T (%v), want it to keep the *schema.RemoteMigrateGateError classification", openErr, openErr)
			}
			if !schema.IsSchemaBehindError(openErr) {
				t.Fatalf("OpenForReadOnlyCommand error = %T (%v), want a *schema.SchemaBehindError reachable through the gate error", openErr, openErr)
			}
			var behind *schema.SchemaBehindError
			if errors.As(openErr, &behind) {
				if got, want := strings.Join(behind.Missing, ", "), strings.Join(tc.missing, ", "); got != want {
					t.Errorf("refusal names %q, want %q", got, want)
				}
			}
			if strings.Contains(stderr, "Read-only command: continuing") {
				t.Errorf("the refusal still printed the continuation warning first:\n%s", stderr)
			}

			// The refusal must not have healed anything: the cursor stays
			// behind and the broken relation stays broken.
			db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL (verify): %v", err)
			}
			defer func() { _ = cleanup2() }()
			var applied int
			if err := db2.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", schema.LatestVersion()).Scan(&applied); err != nil {
				t.Fatalf("read schema_migrations: %v", err)
			}
			if applied != 0 {
				t.Fatalf("the refused open applied migration %d; a read-only command must not migrate", schema.LatestVersion())
			}
			if tc.name == "no leases table" {
				var leases int
				if err := db2.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'leases'").Scan(&leases); err != nil {
					t.Fatalf("probe leases: %v", err)
				}
				if leases != 0 {
					t.Fatal("the refused open re-created the leases table; a read-only command must not write schema")
				}
			}
		})
	}
}

// TestEmbeddedOpenForReadOnlyCommand_LenientGateReadsIntactOverlay is the
// positive control for the #6572 guard's fixture family: the same behind,
// remote-backed database with every hydration relation intact must keep
// serving the read-through the gate promises. The refusal the guard adds for
// unreadable overlays must not widen into refusing the degraded window the
// gate exists to serve (bd-578h9.5, #5920), and the open must not quietly
// migrate or heal anything to get there.
func TestEmbeddedOpenForReadOnlyCommand_LenientGateReadsIntactOverlay(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	t.Setenv(schema.SmartGateEnv, "0")
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "lgi"); err != nil {
		store.Close()
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "probe1", Title: "probe", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		store.Close()
		t.Fatalf("CreateIssue: %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	for _, stmt := range []string{
		fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", schema.LatestVersion()),
		fmt.Sprintf("CALL DOLT_REMOTE('add', 'origin', 'file://%s')", filepath.Join(t.TempDir(), "remote")),
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = cleanup()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = cleanup()

	var ro *embeddeddolt.EmbeddedDoltStore
	var openErr error
	stderr := captureEmbeddedStderr(t, func() {
		ro, openErr = embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, "testdb", "main")
	})
	if openErr != nil {
		t.Fatalf("OpenForReadOnlyCommand = %v, want the promised read-through on an intact behind schema", openErr)
	}
	defer ro.Close()
	for _, want := range []string{"Read-only command: continuing on schema v", "without migrating"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("continuation warning missing %q:\n%s", want, stderr)
		}
	}

	// The read commands the gate's promise covers, against the seeded issue.
	issues, err := ro.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "probe1" {
		t.Fatalf("SearchIssues = %d issues (first %v), want the seeded probe1", len(issues), firstID(issues))
	}
	count, err := ro.CountIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountIssues = %d, want 1", count)
	}
	ready, err := ro.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "probe1" {
		t.Fatalf("GetReadyWork = %d issues (first %v), want the seeded probe1", len(ready), firstID(ready))
	}
	got, err := ro.GetIssue(ctx, "probe1")
	if err != nil || got == nil || got.Title != "probe" {
		t.Fatalf("GetIssue(probe1) = %v, %v; want the seeded issue", got, err)
	}
	// An id absent from issues exercises the wisp fallback's projection too:
	// on the intact schema it is a clean not-found, not a query failure.
	if missing, err := ro.GetIssue(ctx, "no-such-id"); err == nil || missing != nil {
		t.Fatalf("GetIssue(no-such-id) = %v, %v; want a wrapped not-found", missing, err)
	}

	// The read-through migrated and healed nothing.
	db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	defer func() { _ = cleanup2() }()
	var applied int
	if err := db2.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", schema.LatestVersion()).Scan(&applied); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if applied != 0 {
		t.Fatalf("the read-through applied migration %d; a read-only command must not migrate", schema.LatestVersion())
	}
	var leases int
	if err := db2.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'leases'").Scan(&leases); err != nil {
		t.Fatalf("probe leases: %v", err)
	}
	if leases != 1 {
		t.Fatal("the leases overlay did not survive the read-through intact")
	}
}

// firstID returns the first issue's id, or "" for an empty slice, for
// failure messages that stay readable.
func firstID(issues []*types.Issue) string {
	if len(issues) == 0 {
		return ""
	}
	return issues[0].ID
}
