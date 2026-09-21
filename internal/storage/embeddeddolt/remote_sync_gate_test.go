//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// buildDataBehindEmbedded leaves the embedded database at beadsDir in the
// #6575 state: level with its own cached remotes/origin/main on SCHEMA (both
// at latest-1, so the smart gate takes its equal-version first-mover path) and
// BEHIND it by one pure DATA commit. extraLocalCommits > 0 additionally gives
// it commits of its own, producing the diverged shape (ahead >= 1, behind >= 1).
//
// The behind-ness is produced the way the field cohort reaches it — a real
// file:// remote that has commits this history does not — rather than by
// stubbing the ancestry fact. A single database can build it because pushing
// C1 and then rewinding local HEAD to C0 leaves the remote-tracking ref ahead
// of the branch, which is exactly what a fetch-without-merge produces on a
// clone; no second engine is needed.
func buildDataBehindEmbedded(t *testing.T, beadsDir string, extraLocalCommits int) {
	t.Helper()
	ctx := t.Context()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (create): %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	mustExec := func(stage, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	// C0: regress the schema cursor one migration below latest — the "old
	// binary" state the 1.3.0 upgrade cohort is in — and publish it.
	mustExec("regress schema cursor", "DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion())
	mustExec("stage C0", "CALL DOLT_ADD('-A')")
	mustExec("commit C0", "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: regress schema cursor (C0)')")

	var c0 string
	if err := db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log LIMIT 1").Scan(&c0); err != nil {
		t.Fatalf("read C0 hash: %v", err)
	}

	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)",
		"file://"+filepath.Join(t.TempDir(), "remote"))
	mustExec("push C0", "CALL DOLT_PUSH('origin', 'main')")

	// C1: a pure DATA commit, published. No schema change, so both sides stay
	// at latest-1 and the gate lands on its equal-version path.
	mustExec("data edit", "REPLACE INTO config (`key`, value) VALUES ('data-behind-marker', 'C1')")
	mustExec("commit C1", "CALL DOLT_COMMIT('-Am', 'test: pure data commit (C1)')")
	mustExec("push C1", "CALL DOLT_PUSH('origin', 'main')")

	// Rewind the branch to C0. remotes/origin/main stays at C1, so this
	// history is now behind its cached remote ref by one data commit.
	mustExec("rewind to C0", "CALL DOLT_RESET('--hard', ?)", c0)

	for i := 0; i < extraLocalCommits; i++ {
		mustExec("local data edit", "REPLACE INTO config (`key`, value) VALUES ('local-marker', ?)", i)
		mustExec("local commit", "CALL DOLT_COMMIT('-Am', 'test: local-only data commit')")
	}

	assertAheadBehind(t, db, extraLocalCommits, 1)
}

// assertAheadBehind pins the fixture's measured position against
// remotes/origin/main, so a refusal below can never be attributed to a fixture
// that failed to produce the state it claims.
func assertAheadBehind(t *testing.T, db *sql.DB, wantAhead, wantBehind int) {
	t.Helper()
	var ahead, behind int
	if err := db.QueryRowContext(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log AS OF 'remotes/origin/main')) AS ahead,
			(SELECT COUNT(*) FROM dolt_log AS OF 'remotes/origin/main' WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log)) AS behind
	`).Scan(&ahead, &behind); err != nil {
		t.Fatalf("measure ahead/behind: %v", err)
	}
	if ahead != wantAhead || behind != wantBehind {
		t.Fatalf("fixture position = ahead %d, behind %d; want ahead %d, behind %d",
			ahead, behind, wantAhead, wantBehind)
	}
}

// pendingMigrationStillPending fails unless the binary's latest migration is
// still unapplied — i.e. the open under test did NOT migrate.
func pendingMigrationStillPending(t *testing.T, beadsDir string) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	defer func() { _ = cleanup() }()
	var count int
	if err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", schema.LatestVersion()).Scan(&count); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("the open applied the pending migration (version %d recorded); a data-behind clone must not migrate",
			schema.LatestVersion())
	}
}

// TestEmbeddedOpenForRemoteSync_DataBehind is the F1 regression test for
// gastownhall/beads#6575: the refusal must not block its own prescribed
// remedy.
//
// The data-behind stop tells the operator to run `bd dolt pull`. That command
// opens the store, and a strict open re-hits the refusal that prescribed it, so
// before this the refused clone had no in-band exit at all — on embedded there
// is no external `dolt` binary either, leaving only
// BD_ALLOW_REMOTE_MIGRATE=1, i.e. performing the migration the refusal exists
// to prevent. This is the same deadlock #4566 broke for `bd dolt commit`, and
// the remote-sync open is its narrow twin.
//
// Both shapes are covered: strict-ancestor (ahead 0) and diverged (ahead 1).
func TestEmbeddedOpenForRemoteSync_DataBehind(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	for _, tc := range []struct {
		name              string
		extraLocalCommits int
		wantDiverged      bool
	}{
		{name: "strict ancestor (ahead 0, behind 1)", extraLocalCommits: 0},
		{name: "diverged (ahead 1, behind 1)", extraLocalCommits: 1, wantDiverged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(schema.AllowRemoteMigrateEnv, "0")
			t.Setenv(schema.SmartGateEnv, "1")
			schema.SetForceAllowRemoteMigrate(false)

			ctx := t.Context()
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			buildDataBehindEmbedded(t, beadsDir, tc.extraLocalCommits)

			// A strict open still refuses, and refuses for the data-behind
			// reason (not some other fixture artifact).
			gated, gateErr := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			if gateErr == nil {
				gated.Close()
				t.Fatal("Open of a data-behind, remote-backed DB = nil error, want the #6575 refusal")
			}
			var rmErr *schema.RemoteMigrateGateError
			if !errors.As(gateErr, &rmErr) {
				t.Fatalf("Open error = %T (%v), want *schema.RemoteMigrateGateError", gateErr, gateErr)
			}
			if !rmErr.IsDataBehind() {
				t.Fatalf("FallbackReason = %q, want the data-behind stop", rmErr.FallbackReason)
			}
			if rmErr.DataDiverged != tc.wantDiverged {
				t.Errorf("DataDiverged = %v, want %v", rmErr.DataDiverged, tc.wantDiverged)
			}

			// ...and the remote-sync open — what `bd dolt pull` uses — gets
			// through it, without migrating.
			syncStore, syncErr := embeddeddolt.OpenForRemoteSync(ctx, beadsDir, "testdb", "main")
			if syncErr != nil {
				t.Fatalf("OpenForRemoteSync of a data-behind DB = %v; the refusal must not block the pull it prescribes (#6575)", syncErr)
			}
			syncStore.Close()
			pendingMigrationStillPending(t, beadsDir)
		})
	}
}

// TestEmbeddedOpenForRemoteSync_OtherRefusalsStayFatal pins the narrowness of
// the exemption above, which is the whole reason it is keyed on the gate's
// REASON rather than being another blanket lenient open: a pull cannot resolve
// a fork skew, a below-floor database or an opted-out blunt block, so it must
// not be waved through them. Here BD_SMART_GATE=0 produces the blunt
// opted-out stop on the very same fixture that the data-behind case above
// opens successfully.
func TestEmbeddedOpenForRemoteSync_OtherRefusalsStayFatal(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")
	schema.SetForceAllowRemoteMigrate(false)

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	// Build the fixture with the smart gate ON (the fixture helper's own
	// Open must not be the thing that is opted out), then opt out for the
	// probe below.
	t.Setenv(schema.SmartGateEnv, "1")
	buildDataBehindEmbedded(t, beadsDir, 0)
	t.Setenv(schema.SmartGateEnv, "0")

	store, err := embeddeddolt.OpenForRemoteSync(ctx, beadsDir, "testdb", "main")
	if err == nil {
		store.Close()
		t.Fatal("OpenForRemoteSync = nil error with BD_SMART_GATE=0; the exemption must apply to the data-behind reason only")
	}
	var rmErr *schema.RemoteMigrateGateError
	if !errors.As(err, &rmErr) {
		t.Fatalf("error = %T (%v), want *schema.RemoteMigrateGateError", err, err)
	}
	if rmErr.IsDataBehind() {
		t.Fatalf("FallbackReason = %q; with the smart gate off this must be the blunt opted-out stop", rmErr.FallbackReason)
	}
}
