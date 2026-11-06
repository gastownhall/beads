//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// These tests execute the recovery guide itself — docs/recovery/
// accidental-1-2-1-release.md — against real embedded Dolt stores
// (gastownhall/beads#6488). The guide's fenced `dolt sql -q` blocks are
// extracted from the markdown and run verbatim, so a prose promise and the
// command a user actually copies can never drift apart: if the guide says
// recovery is complete when it is not, or a block leaves a store whose
// clones cannot write, the test fails on the doc's own bytes.
//
// The fixture mirrors the v1.2.1 timeline on main's frozen migrations: a
// v53 (v1.2.2) database runs the 1.2.x series through the events flip
// (main migration 0062 drops the versioned events table, commits the drop
// under its frozen message, and re-creates events as a dolt_ignored local
// table), keeps writing local audit rows, then follows the guide.

const (
	// rgPreFlip* seed the shared pre-flip history: two issues, three
	// committed versioned events rows.
	rgIssueDoomed = "rg-1" // peer A deletes it; its events must not survive
	rgIssueKept   = "rg-keep"
	rgEvtDoomedA  = "rg-evt-1a"
	rgEvtDoomedB  = "rg-evt-1b"
	rgEvtKept     = "rg-evt-keep"
	// rgEvtLocal is a post-flip local (unversioned) audit row.
	rgEvtLocal = "rg-evt-local"

	// Anchors that locate a guide block by content its SQL must carry.
	rgAnchorRollback     = "roll schema cursor back to v53"
	rgAnchorRestore      = "recovery: restore versioned events table"
	rgAnchorVerify       = "events_ignore_rules"
	rgAnchorHistoryOnly  = "recovery: restore events table from history"
	rgAnchorFinishRetrk  = "recovery: finish re-tracking events"
	rgEventsInsertPrefix = "INSERT INTO events (issue_id, event_type, actor, created_at, id)"
)

// rgStore is one machine's workspace: a beads dir with an embeddeddolt
// data dir holding the single test database.
type rgStore struct {
	beadsDir string
	dataDir  string
}

func (s *rgStore) withConn(t *testing.T, fn func(conn *sql.Conn)) {
	t.Helper()
	withRawConn(t, s.dataDir, "testdb", fn)
}

// rgSeedPreFlip builds the shared origin state: a v53 database (what
// v1.2.2 speaks) with committed versioned events rows, pushed nowhere yet.
func rgSeedPreFlip(t *testing.T) *rgStore {
	t.Helper()
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 53)
	s := &rgStore{beadsDir: filepath.Dir(dataDir), dataDir: dataDir}
	s.withConn(t, func(conn *sql.Conn) {
		execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('`+rgIssueDoomed+`', 'doomed seed', '', '', '', '', 'open', 2, 'task'),
       ('`+rgIssueKept+`', 'kept seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('`+rgIssueDoomed+`', 'created', 'tester', '2026-07-30 00:00:00', '`+rgEvtDoomedA+`'),
       ('`+rgIssueDoomed+`', 'updated', 'tester', '2026-07-30 00:00:01', '`+rgEvtDoomedB+`'),
       ('`+rgIssueKept+`', 'created', 'tester', '2026-07-30 00:00:02', '`+rgEvtKept+`');
`)
		mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed pre-flip history')")
	})
	return s
}

// rgFlip migrates the store through 0062, as running any v1.2.x binary
// did, and records one post-flip local audit row under the given id.
func rgFlip(t *testing.T, s *rgStore, localEvtID string) {
	t.Helper()
	ctx := t.Context()
	s.withConn(t, func(conn *sql.Conn) {
		if _, err := schema.MigrateUpTo(ctx, conn, 62); err != nil {
			t.Fatalf("MigrateUpTo(62): %v", err)
		}
		mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'schema: apply migrations')")
		execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('`+rgIssueKept+`', 'updated', 'laptop', '2026-07-30 00:00:09', '`+localEvtID+`');
`)
	})
}

// rgRunGuide executes the guide's required recovery: the cursor rollback
// (step 4) then the versioned-events restoration (step 5), verbatim.
func rgRunGuide(t *testing.T, s *rgStore) {
	t.Helper()
	ctx := t.Context()
	s.withConn(t, func(conn *sql.Conn) {
		if err := rgExecGuideSQL(t, ctx, conn, rgAnchorRollback); err != nil {
			t.Fatalf("guide step 4 (cursor rollback) failed: %v", err)
		}
		if err := rgExecGuideSQL(t, ctx, conn, rgAnchorRestore); err != nil {
			t.Fatalf("guide step 5 (restore versioned events) failed: %v", err)
		}
	})
}

// rgRemoteDir creates an empty file:// remote and returns its URL.
func rgRemoteDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	return "file://" + dir
}

// rgClone clones the remote into a fresh machine's data dir.
func rgClone(t *testing.T, remoteURL string) *rgStore {
	t.Helper()
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create clone data dir: %v", err)
	}
	withRawConn(t, dataDir, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, "testdb")
	})
	return &rgStore{beadsDir: beadsDir, dataDir: dataDir}
}

func (s *rgStore) rgAddOriginPush(t *testing.T, remoteURL string) {
	t.Helper()
	s.withConn(t, func(conn *sql.Conn) {
		ctx := t.Context()
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_remotes WHERE name = 'origin'").Scan(&n); err != nil {
			t.Fatalf("read dolt_remotes: %v", err)
		}
		if n == 0 { // a clone already carries its origin
			mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
}

// rgIssueWrite performs an issue-plus-event write and the ordinary commit
// operation the 1.1 line runs after one, without any migrator involvement.
// It fails the test when the write itself cannot be recorded.
func rgIssueWrite(t *testing.T, s *rgStore, issue, event string) {
	t.Helper()
	ctx := t.Context()
	s.withConn(t, func(conn *sql.Conn) {
		execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('`+issue+`', 'clone work', '', '', '', '', 'open', 2, 'task');
`+rgEventsInsertPrefix+`
VALUES ('`+issue+`', 'created', 'clone', '2026-07-30 00:01:00', '`+event+`');
`)
		if err := versioncontrolops.StageAndCommit(ctx, conn,
			map[string]bool{"issues": true, "events": true}, "test: issue write", ""); err != nil {
			t.Fatalf("ordinary commit after the issue write: %v", err)
		}
	})
}

// --- guide extraction: run the markdown's own command blocks ---

// rgGuidePath is the guide these tests execute, relative to the repo root.
var rgGuidePath = filepath.Join("docs", "recovery", "accidental-1-2-1-release.md")

// rgRecoveryGuideSQL returns the `dolt sql -q "..."` payload from the
// guide whose SQL contains anchor. Exactly one block must match: the guide
// is the product under test, so ambiguity is a failure, not a search.
func rgRecoveryGuideSQL(t *testing.T, anchor string) string {
	t.Helper()
	root := rgRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, rgGuidePath))
	if err != nil {
		t.Fatalf("reading the recovery guide: %v", err)
	}
	md := string(raw)
	var found []string
	for _, payload := range rgDoltSQLPayloads(md) {
		if strings.Contains(payload, anchor) {
			found = append(found, payload)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("the recovery guide has no dolt sql block containing %q — the step these tests pin is missing from %s", anchor, rgGuidePath)
	default:
		t.Fatalf("the recovery guide has %d dolt sql blocks containing %q; the anchor is ambiguous", len(found), anchor)
	}
	return ""
}

// rgDoltSQLPayloads returns every `dolt sql -q "..."` payload in the
// markdown, fenced blocks only. Guide SQL never contains a double quote,
// so the payload runs from the quote after -q to the next quote.
func rgDoltSQLPayloads(md string) []string {
	var payloads []string
	fenced := false
	var block []string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if fenced {
				payloads = append(payloads, rgPayloadsFromBlock(strings.Join(block, "\n"))...)
				block = nil
			}
			fenced = !fenced
			continue
		}
		if fenced {
			block = append(block, line)
		}
	}
	return payloads
}

func rgPayloadsFromBlock(block string) []string {
	var payloads []string
	for {
		i := strings.Index(block, `dolt sql -q "`)
		if i < 0 {
			return payloads
		}
		rest := block[i+len(`dolt sql -q "`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return payloads
		}
		payloads = append(payloads, rest[:end])
		block = rest[end+1:]
	}
}

// rgExecGuideSQL runs one guide payload as a single multi-statement batch,
// exactly as `dolt sql -q` executes it: statements run in order and the
// batch stops at the first error, which it returns (guards in the guide
// abort by failing a statement).
func rgExecGuideSQL(t *testing.T, ctx context.Context, conn *sql.Conn, anchor string) error {
	t.Helper()
	payload := rgRecoveryGuideSQL(t, anchor)
	_, err := conn.ExecContext(ctx, payload)
	return err
}

// --- assertions ---

func rgRequireCursor(t *testing.T, ctx context.Context, conn *sql.Conn, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&got); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	if got != want {
		t.Fatalf("schema cursor = %d, want %d", got, want)
	}
}

// rgEventIDs returns the events rows' ids from fromClause ("events" or
// "events AS OF 'HEAD'"), ordered by id.
func rgEventIDs(t *testing.T, ctx context.Context, conn *sql.Conn, fromClause string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT id FROM "+fromClause+" ORDER BY id")
	if err != nil {
		t.Fatalf("read events from %s: %v", fromClause, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan events id: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events: %v", err)
	}
	return out
}

func rgRequireEventIDs(t *testing.T, ctx context.Context, conn *sql.Conn, fromClause string, want ...string) {
	t.Helper()
	got := rgEventIDs(t, ctx, conn, fromClause)
	if len(got) != len(want) {
		t.Fatalf("events rows from %s = %v, want exactly %v", fromClause, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events rows from %s = %v, want exactly %v", fromClause, got, want)
		}
	}
}

// rgEventColumns returns the ordered column descriptors of the working
// events table, or of the table as it exists at ref, so the restored
// identity can be compared against its historical source.
func rgEventColumns(t *testing.T, ctx context.Context, conn *sql.Conn, ref string) []string {
	t.Helper()
	query := "SHOW COLUMNS FROM events"
	if ref != "" {
		query += " AS OF '" + ref + "'"
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("read events columns (ref %q): %v", ref, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("column names: %v", err)
	}
	dests := make([]any, len(cols))
	raw := make([]sql.NullString, len(cols))
	fields := make([]string, len(cols))
	for i := range dests {
		dests[i] = &raw[i]
	}
	var out []string
	for rows.Next() {
		if err := rows.Scan(dests...); err != nil {
			t.Fatalf("scan events column: %v", err)
		}
		for i := range raw {
			fields[i] = raw[i].String
		}
		out = append(out, strings.Join(fields, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events columns: %v", err)
	}
	return out
}

// rgFlipParent returns the first parent of the flip commit in this
// database's history: the last commit that still holds the original
// versioned events table.
func rgFlipParent(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	var parent string
	err := conn.QueryRowContext(ctx, `
SELECT parent_hash FROM dolt_commit_ancestors
WHERE parent_index = 0 AND commit_hash = (
  SELECT commit_hash FROM dolt_log
  WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)'
  ORDER BY date LIMIT 1)`).Scan(&parent)
	if err != nil {
		t.Fatalf("resolve the flip commit's parent: %v", err)
	}
	return parent
}

func rgRequireCleanStatus(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	dirty, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("working set is dirty: %v, want clean (recovery must leave no pending changes)", dirty)
	}
}

func rgRequireNoEventsIgnoreRule(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events'").Scan(&n); err != nil {
		t.Fatalf("read dolt_ignore: %v", err)
	}
	if n != 0 {
		t.Fatalf("dolt_ignore still holds %d events pattern(s), want 0", n)
	}
}

// rgRequireNoOpenMerge asserts a pull's merge settled: no merge in
// progress and no row or schema conflicts left behind.
func rgRequireNoOpenMerge(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for _, q := range []string{
		"SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging",
		"SELECT COUNT(*) FROM dolt_conflicts",
		"SELECT COUNT(*) FROM dolt_schema_conflicts",
	} {
		var n int
		if err := conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d after the pull, want 0 (the merge must settle cleanly)", q, n)
		}
	}
}

// rgRepoRoot resolves the repository root under Bazel (runfiles, which
// hold exactly the data the test declares) and plain go test.
func rgRepoRoot(t *testing.T) string {
	t.Helper()
	return bazeltest.RepoRoot(t)
}

// --- the acceptance criteria ---

// TestRecoveryGuideRestoresVersionedEventsOnMigratedStore is AC-1: a
// migrated store with migration-carried pre-flip history and local
// post-flip audit rows runs the guide's required recovery, and ends with
// the cursor at 53, the historical events schema at HEAD carrying the
// local rows, the ignore rule gone, and no pending changes.
func TestRecoveryGuideRestoresVersionedEventsOnMigratedStore(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	s := rgSeedPreFlip(t)
	rgFlip(t, s, rgEvtLocal)
	s.withConn(t, func(conn *sql.Conn) {
		if trackedAtHead(t, ctx, conn, "events") {
			t.Fatal("fixture: events is already tracked at HEAD before the flip took effect")
		}
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events'").Scan(&n); err != nil || n != 1 {
			t.Fatalf("fixture: events ignore rule rows = %d (err %v), want 1 after the flip", n, err)
		}
	})

	rgRunGuide(t, s)

	s.withConn(t, func(conn *sql.Conn) {
		rgRequireCursor(t, ctx, conn, 53)
		if !trackedAtHead(t, ctx, conn, "events") {
			t.Fatal("events is not tracked at HEAD after the guide's restoration step")
		}
		// The local audit rows — carried pre-flip history plus the
		// post-flip row — are what HEAD must now carry.
		rgRequireEventIDs(t, ctx, conn, "events AS OF 'HEAD'", rgEvtDoomedA, rgEvtDoomedB, rgEvtKept, rgEvtLocal)
		rgRequireEventIDs(t, ctx, conn, "events", rgEvtDoomedA, rgEvtDoomedB, rgEvtKept, rgEvtLocal)
		// The restored table is the historical one, column for column,
		// not the migration's re-created shape (whose column order
		// differs and cannot merge with pre-flip history).
		parent := rgFlipParent(t, ctx, conn)
		want := rgEventColumns(t, ctx, conn, parent)
		got := rgEventColumns(t, ctx, conn, "")
		if len(want) == 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("restored events columns:\n%v\nwant the historical identity from %s:\n%v", got, parent, want)
		}
		if len(got) == 0 || !strings.HasPrefix(got[0], "id|") {
			t.Fatalf("restored events table's first column = %q, want the historical id-first order", firstField(got))
		}
		rgRequireNoEventsIgnoreRule(t, ctx, conn)
		rgRequireCleanStatus(t, ctx, conn)
	})

	// The guide's own verification queries must agree (step 6's block).
	s.withConn(t, func(conn *sql.Conn) {
		if err := rgExecGuideSQL(t, ctx, conn, rgAnchorVerify); err != nil {
			// The verify block is four separate one-line commands; run
			// each so a failing check names itself.
			for _, payload := range rgVerifyPayloads(t) {
				if _, err := conn.ExecContext(ctx, payload); err != nil {
					t.Fatalf("guide step 6 verification query failed: %q: %v", payload, err)
				}
			}
		}
	})
}

// rgVerifyPayloads returns the step-6 verification queries individually.
func rgVerifyPayloads(t *testing.T) []string {
	t.Helper()
	root := rgRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, rgGuidePath))
	if err != nil {
		t.Fatalf("reading the recovery guide: %v", err)
	}
	var out []string
	for _, payload := range rgDoltSQLPayloads(string(raw)) {
		if strings.Contains(payload, rgAnchorVerify) {
			out = append(out, payload)
		}
	}
	if len(out) == 0 {
		t.Fatalf("guide has no verification block containing %q", rgAnchorVerify)
	}
	return out
}

func firstField(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	return strings.SplitN(cols[0], "|", 2)[0]
}
