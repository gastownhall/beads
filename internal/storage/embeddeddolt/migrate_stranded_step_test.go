//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover the stranded-migration-step recovery (bd-0eetsy): a pass
// killed after a numbered migration's cursor row was recorded but before its
// per-step commit leaves that one step uncommitted in the working set. The
// next MigrateUp recognizes exactly that shape and finishes the step through
// commitMigrationStep under the step's own message, before the dirty-table
// guard runs — where the pre-fix behavior refused every such retry with
// *DirtyTablesError, or (when no pending migration touches the stranded
// step's tables) silently let a later step's commit carry the stranded cursor
// row while the step's own change stayed uncommitted.
//
// The strand itself is fabricated with schema.MigrateUpTo(at) on a store
// already committed at at-1: MigrateUpTo's test-support path (upTo>0) skips
// the per-step commits the production pass makes (commitEachStep is gated on
// upTo==0), so the store is left with the step's SQL and its cursor row
// sitting uncommitted — the faithful working set of a SIGKILL between the
// step's SQL and its commitMigrationStep.

// strandMainAt fabricates the bd-0eetsy strand: main migration step at, and
// its cursor row, sitting uncommitted on a store committed at at-1.
func strandMainAt(t *testing.T, ctx context.Context, dataDir string, at int) {
	t.Helper()
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	applied, err := schema.MigrateUpTo(ctx, conn, at)
	if err != nil {
		t.Fatalf("MigrateUpTo(%d): %v", at, err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUpTo(%d) applied %d steps from the seed, want 1", at, applied)
	}
	if v := currentMainVersion(t, ctx, conn); v != at {
		t.Fatalf("working cursor after strand = %d, want %d", v, at)
	}
}

// canonicalDoltIgnoreSeedPatterns mirrors schema's canonical dolt_ignore
// pattern list (doltIgnorePatterns in internal/storage/schema/schema.go).
// Every dolt_ignore write in the shipped migration series is REPLACE INTO, so
// pre-seeding these ahead of a raw MigrateUpTo seed run is idempotent. If
// schema's list grows, the 0062 row's only-one-apply-commit-touches-
// dolt_ignore assertion fails loudly: update this list with it.
var canonicalDoltIgnoreSeedPatterns = []string{
	"__temp__%",
	"bd_events_journal",
	"bd_events_seq",
	"ignored_schema_migrations",
	"leases",
	"local_metadata",
	"repo_mtimes",
	"wisp_%",
	"wisps",
}

// strandSeedMainSchemaAt is seedMainSchemaAt with the canonical dolt_ignore
// patterns asserted before the migration series runs — the shape production
// opens have, where seedDoltIgnorePatterns runs at the pass front ahead of
// everything. It matters for the tables the series itself creates: leases
// (main 0055) chief among them is born dolt_ignored in production and never
// reaches HEAD as a tracked table. Seeding with plain MigrateUpTo instead
// commits leases TRACKED at the baseline commit (no pattern existed when it
// was created, and DOLT_ADD('-A') sweeps it in), and no later pattern can
// untrack a committed table — the ignored series' 0016 ALTER then leaves
// leases visibly dirty after every pass, drowning the real assertions.
//
// It also serves the 0062 row directly: a strand of 0062 dirties dolt_ignore
// itself (its ('events', true) row), and with the full canonical list
// committed at seed time the retry's seedDoltIgnorePatterns finds nothing
// missing, so commitSeededDoltIgnore cannot sweep the strand's row into a
// seed commit before the finish could stage it.
func strandSeedMainSchemaAt(t *testing.T, ctx context.Context, at int) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	for _, pattern := range canonicalDoltIgnoreSeedPatterns {
		if _, err := conn.ExecContext(ctx, "INSERT IGNORE INTO dolt_ignore VALUES (?, true)", pattern); err != nil {
			t.Fatalf("pre-seed dolt_ignore pattern %q: %v", pattern, err)
		}
	}
	if _, err := schema.MigrateUpTo(ctx, conn, at); err != nil {
		t.Fatalf("MigrateUpTo(%d): %v", at, err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD -A: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'test: seed baseline schema')"); err != nil {
		t.Fatalf("DOLT_COMMIT baseline: %v", err)
	}
	return dataDir
}

// schemaApplyCommits returns the commit hashes of every
// `schema: apply migration <file>` commit whose message carries versionPrefix
// (e.g. "0049_"). Migration files are named <version>_<slug>.up.sql with
// unique four-digit versions, so the prefix cannot over-match.
func schemaApplyCommits(t *testing.T, ctx context.Context, conn *sql.Conn, versionPrefix string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	var hashes []string
	prefix := "schema: apply migration " + versionPrefix
	for rows.Next() {
		var hash, message string
		if err := rows.Scan(&hash, &message); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		if strings.HasPrefix(message, prefix) {
			hashes = append(hashes, hash)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	return hashes
}

// oneApplyCommit asserts exactly one apply-migration commit for versionPrefix
// and returns its hash.
func oneApplyCommit(t *testing.T, ctx context.Context, conn *sql.Conn, versionPrefix string) string {
	t.Helper()
	hashes := schemaApplyCommits(t, ctx, conn, versionPrefix)
	if len(hashes) != 1 {
		t.Fatalf("found %d commits for `schema: apply migration %s*`, want exactly 1", len(hashes), versionPrefix)
	}
	return hashes[0]
}

// commitChangedTables returns the sorted tables a commit changed.
func commitChangedTables(t *testing.T, ctx context.Context, conn *sql.Conn, hash string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT DISTINCT table_name FROM dolt_diff WHERE commit_hash = ?", hash)
	if err != nil {
		t.Fatalf("read dolt_diff for commit %s: %v", hash, err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan dolt_diff: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_diff: %v", err)
	}
	sort.Strings(tables)
	return tables
}

// cursorRowCommit is one dolt_history_schema_migrations entry for a cursor
// row: a commit that carried the row, by hash and message.
type cursorRowCommit struct {
	hash    string
	message string
}

// cursorRowCommitHistory returns every commit that carried cursor row
// version, newest first. dolt_history_schema_migrations lists the row at
// every commit from its first appearance to HEAD — not only the commit that
// introduced it — and names each commit only by commit_hash (its extra
// columns are commit_hash, committer and commit_date — no message), so the
// hashes are resolved to messages through dolt_log.
func cursorRowCommitHistory(t *testing.T, ctx context.Context, conn *sql.Conn, version int) []cursorRowCommit {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT commit_hash FROM dolt_history_schema_migrations WHERE version = ?", version)
	if err != nil {
		t.Fatalf("read dolt_history_schema_migrations for version %d: %v", version, err)
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan dolt_history_schema_migrations: %v", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_history_schema_migrations: %v", err)
	}

	messagesByHash := make(map[string]string, len(hashes))
	logs, err := conn.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer logs.Close()
	for logs.Next() {
		var hash, message string
		if err := logs.Scan(&hash, &message); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		messagesByHash[hash] = message
	}
	if err := logs.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}

	entries := make([]cursorRowCommit, 0, len(hashes))
	for _, hash := range hashes {
		entries = append(entries, cursorRowCommit{hash: hash, message: messagesByHash[hash]})
	}
	return entries
}

// applyMigrationVersionOf extracts the four-digit version an apply-migration
// commit message carries ("schema: apply migration 0062_events_dolt_ignore.up.sql"
// → 62, true). Other commit messages — the open's dolt_ignore seed commit,
// the pass's terminal "schema: apply migrations" commit — report false.
func applyMigrationVersionOf(message string) (int, bool) {
	const prefix = "schema: apply migration "
	rest, ok := strings.CutPrefix(message, prefix)
	if !ok || len(rest) < 5 || rest[4] != '_' {
		return 0, false
	}
	v, err := strconv.Atoi(rest[:4])
	if err != nil {
		return 0, false
	}
	return v, true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEmbeddedMigrateFinishesStrandedOwnStep is the core AC set: for each
// strand version, a plain retry finishes the step under its own message and
// converges to a clean latest schema (AC-1, AC-2), the finished step counts
// as applied (AC-7), and the finish logs exactly one line naming the step and
// the tables it staged (AC-7).
//
// AC-1 rows (46, 49, 54 — a later pending migration touches the stranded
// step's tables) additionally compare the finished commit's changed tables
// against the same step's commit in an uninterrupted MigrateUp from the same
// seed, and check the applied count equals that uninterrupted pass's (the
// finish must count as applied, not silently drop from the total).
//
// AC-2 rows (62, 73 — no later migration touches the stranded step's tables)
// pin where the step's change and its cursor row landed: in the step's own
// commit, with the next step's commit carrying only its own cursor row.
//
// PRE-fix, rows 46/49/54 fail with *DirtyTablesError [issues] at the retry,
// row 62 leaves the ('events', true) dolt_ignore row uncommitted after a
// nil-error pass, and row 73 leaves the strand dirty with 0074's commit
// carrying cursor row 73.
func TestEmbeddedMigrateFinishesStrandedOwnStep(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	rows := []struct {
		version    int
		versionPfx string
		// wantCommitTables is what the step's apply commit must change,
		// beside the cursor table. The dolt-ignored tables some of these
		// migrations also write (wisps, recreated events) never show in
		// dolt_status, so they are never staged and never listed.
		wantCommitTables []string
		twinCompare      bool
	}{
		{46, "0046_", []string{"issues"}, true},
		{49, "0049_", []string{"comments", "issues"}, true},
		{54, "0054_", []string{"issues"}, true},
		{62, "0062_", []string{"dolt_ignore"}, false},
		{73, "0073_", []string{"issues"}, false},
	}

	for _, row := range rows {
		t.Run(fmt.Sprintf("strand_%d", row.version), func(t *testing.T) {
			dataDirA := strandSeedMainSchemaAt(t, ctx, row.version-1)
			strandMainAt(t, ctx, dataDirA, row.version)

			var errA error
			func() {
				connA, closeA := openPinnedConn(t, ctx, dataDirA)
				defer closeA()
				_, errA = schema.MigrateUp(ctx, connA)
			}()
			if errA != nil {
				t.Fatalf("retry MigrateUp after strand at %d: %v", row.version, errA)
			}

			connA, closeA := openPinnedConn(t, ctx, dataDirA)
			defer closeA()
			if v := currentMainVersion(t, ctx, connA); v != schema.LatestVersion() {
				t.Fatalf("main version after retry = %d, want latest %d", v, schema.LatestVersion())
			}
			if dirty := dirtyTableNames(t, ctx, connA); len(dirty) != 0 {
				t.Fatalf("working set after retry is dirty: %v, want clean", dirty)
			}

			hashA := oneApplyCommit(t, ctx, connA, row.versionPfx)
			got := commitChangedTables(t, ctx, connA, hashA)
			want := append([]string{"schema_migrations"}, row.wantCommitTables...)
			sort.Strings(want)
			if !equalStrings(got, want) {
				t.Fatalf("apply commit for step %d changed tables = %v, want %v", row.version, got, want)
			}

			if row.twinCompare {
				// AC-1: the finish's commit must match the uninterrupted
				// pass's commit for the same step, table for table — and
				// AC-7: the strand store's applied count must equal the
				// uninterrupted pass's (the finished step counted).
				dataDirB := strandSeedMainSchemaAt(t, ctx, row.version-1)
				connB, closeB := openPinnedConn(t, ctx, dataDirB)
				defer closeB()
				_, errB := schema.MigrateUp(ctx, connB)
				if errB != nil {
					t.Fatalf("uninterrupted MigrateUp from seed %d: %v", row.version-1, errB)
				}
				hashB := oneApplyCommit(t, ctx, connB, row.versionPfx)
				wantB := commitChangedTables(t, ctx, connB, hashB)
				if !equalStrings(got, wantB) {
					t.Fatalf("finished step %d changed tables = %v, uninterrupted pass's step commit changed %v", row.version, got, wantB)
				}
			} else {
				// AC-2 rows: pin where the change and the cursor rows landed.
				switch row.version {
				case 62:
					// 0062's ('events', true) row is in the step's own commit:
					// the row is present at HEAD and the only apply-migration
					// commit that ever touches dolt_ignore is 0062's (the
					// fixture's pre-seed commit is not an apply commit). If
					// seedDoltIgnorePatterns ever changes dolt_ignore at this
					// open, its seed commit sweeps the strand row first and the
					// finishing commit below loses the diff — that drift fails
					// here loudly.
					var eventsRows int
					if err := connA.QueryRowContext(ctx,
						"SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events' AND ignored = true").Scan(&eventsRows); err != nil {
						t.Fatalf("read dolt_ignore at HEAD: %v", err)
					}
					if eventsRows != 1 {
						t.Fatalf("dolt_ignore ('events', true) rows at HEAD = %d, want 1", eventsRows)
					}
					ignoreCommits := schemaApplyCommits(t, ctx, connA, "")
					touching := 0
					for _, hash := range ignoreCommits {
						if contains(commitChangedTables(t, ctx, connA, hash), "dolt_ignore") {
							touching++
						}
					}
					if touching != 1 {
						t.Fatalf("%d apply-migration commits touched dolt_ignore, want exactly 1 (0062's own): a seed commit swept the strand row before the finish could stage it", touching)
					}
					assertCursorRowInOwnCommit(t, ctx, connA, 62, "0062_")
					assertCursorRowInOwnCommit(t, ctx, connA, 63, "0063_")
				case 73:
					// issues.arrival_seq landed with the step. QueryRowContext
					// Scan closes its rows, so the connection's close mutex is
					// released before the subtest's deferred conn.Close — a bare
					// QueryContext here leaves the rows open and deadlocks the
					// deferred close for the rest of the shard.
					var arrivalSeq any
					if err := connA.QueryRowContext(ctx, "SELECT arrival_seq FROM issues LIMIT 1").Scan(&arrivalSeq); err != nil && err != sql.ErrNoRows {
						t.Fatalf("issues.arrival_seq missing after retry: %v", err)
					}
					// ...and 0074's commit carries only its own cursor row and
					// table, none of 0073's.
					assertCursorRowInOwnCommit(t, ctx, connA, 73, "0073_")
					assertCursorRowInOwnCommit(t, ctx, connA, 74, "0074_")
					hash74 := oneApplyCommit(t, ctx, connA, "0074_")
					got74 := commitChangedTables(t, ctx, connA, hash74)
					want74 := []string{"id_tombstones", "schema_migrations"}
					if !equalStrings(got74, want74) {
						t.Fatalf("0074's commit changed tables = %v, want %v", got74, want74)
					}
				default:
					t.Fatalf("unhandled AC-2 row %d", row.version)
				}
			}
		})
	}
}

// assertCursorRowInOwnCommit pins that cursor row version was carried by its
// own apply-migration commit and by nothing before it — the row-level half of
// "step N+1's commit adds no schema_migrations row other than N+1's" (AC-2).
// dolt_history lists the row at every commit from its first appearance to
// HEAD, so the entry set must reach back to exactly the step's own apply
// commit: an entry naming an apply commit for a version below N means the row
// left the stranded working set in some earlier commit — the seed's or a
// step's — before the finish could stage it, and a set that does not reach
// the step's own commit at all means a later commit swept the row instead
// (that shape also fails oneApplyCommit, which finds no apply commit for N).
func assertCursorRowInOwnCommit(t *testing.T, ctx context.Context, conn *sql.Conn, version int, versionPfx string) {
	t.Helper()
	entries := cursorRowCommitHistory(t, ctx, conn, version)
	want := oneApplyCommit(t, ctx, conn, versionPfx)
	found := false
	messages := make([]string, 0, len(entries))
	for _, e := range entries {
		messages = append(messages, e.message)
		if e.hash == want {
			found = true
			continue
		}
		if v, ok := applyMigrationVersionOf(e.message); ok && v < version {
			t.Fatalf("cursor row %d's commit history includes %q for version %d: the row left the stranded working set before its own step committed it", version, e.message, v)
		}
	}
	if !found {
		t.Fatalf("cursor row %d's commit history does not reach its own apply commit (%s): entries %v", version, versionPfx, messages)
	}
}
