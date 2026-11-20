//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// These tests execute the recovery guide for the accidental v1.2.1 release
// (docs/recovery/accidental-1-2-1-release.md, gastownhall/beads#6488) against
// the real engine. The guide's SQL is read from the page itself, not copied
// here, so the commands a reader runs are the commands under test.
//
// The machines in these tests stand for v1.2.2 (the 1.1 line, schema 53)
// databases that v1.2.1 migrated to 65. The 1.1 line is modelled at the SQL
// level, without main's migrator: an issue write is an issues row plus an
// events row under a random UUIDv7 id, holding the old issue as JSON long
// enough that Dolt stores it outside the row, and a commit is
// DOLT_COMMIT('-Am') (EmbeddedDoltStore.Commit at v1.2.2). Main's migrations
// 54..65 are the files v1.2.1 shipped; they are applied one commit per
// migration, as v1.2.1's production migrator did, so the parent of 0062's
// drop commit holds the events rows under their 1.1-line ids. v1.2.1 then
// gave the rows of the local, now unversioned table content-derived ids, and
// so does migrateLikeV121: no local id matches its row's id in history,
// except for a row the 1.1 line's own re-key had already given its derived
// id. Each event's comment is a tag the assertions read, since the ids differ.
// Like the rest of the events-flip tests they need BEADS_TEST_EMBEDDED_DOLT=1.

const accidentalReleaseGuide = "docs/recovery/accidental-1-2-1-release.md"

// The guide's sections whose commands the tests run. A section ends at the
// next heading of any level, so the numbered procedure under
// guideRequiredSection does not pick up a subsection's commands.
const (
	guideRequiredSection   = "## Recommended fix: roll the schema cursor back"
	guideMissingSection    = "### If `events` is missing or empty"
	guideBeforePullSection = "#### Before a machine pulls the restored table"
	guideOldRetrackSection = "### If you already ran the old re-track command"
)

// handMadeEventsTable is the empty events table the issue report's
// workaround creates by hand on a clone that has none.
const handMadeEventsTable = `
CREATE TABLE events (
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NOT NULL,
    old_value LONGTEXT,
    new_value LONGTEXT,
    comment TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    id CHAR(36) NOT NULL,
    PRIMARY KEY (id)
)`

// guideCursorRollback is the guide's step 4, which is all an earlier version
// of the guide required: a database recovered with it alone keeps events
// out of versioned history.
const guideCursorRollback = "DELETE FROM schema_migrations WHERE version > 53; CALL DOLT_ADD('schema_migrations'); CALL DOLT_COMMIT('-m', 'recovery: roll schema cursor back to v53 (accidental v1.2.1)', '--author', 'bd recovery <recovery@beads.invalid>')"

// guideOldRetrack is the optional step an earlier version of the guide ended
// with. It never stages the dolt_ignore change.
const guideOldRetrack = "DELETE FROM dolt_ignore WHERE pattern = 'events'; CALL DOLT_ADD('-f', 'events'); CALL DOLT_COMMIT('-m', 'recovery: re-track events table', '--author', 'bd recovery <recovery@beads.invalid>')"

func readAccidentalReleaseGuide(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bazeltest.RepoRoot(t), filepath.FromSlash(accidentalReleaseGuide)))
	if err != nil {
		t.Fatalf("read the recovery guide: %v", err)
	}
	guide := string(data)
	if step4 := guideSQLBatches(t, guide, guideRequiredSection)[0]; step4 != guideCursorRollback {
		t.Fatalf("%s step 4 is %q; guideCursorRollback must match it", accidentalReleaseGuide, step4)
	}
	return guide
}

// guideStep5 returns the guide's step 5 batch, the one after step 4.
func guideStep5(t *testing.T, guide string) string {
	t.Helper()
	batches := guideSQLBatches(t, guide, guideRequiredSection)
	if len(batches) < 2 {
		t.Fatalf("%q has %d dolt sql commands, want step 4's and step 5's first", guideRequiredSection, len(batches))
	}
	return batches[1]
}

// guideBeforePull returns the batch a machine runs before it pulls the
// restored table.
func guideBeforePull(t *testing.T, guide string) string {
	t.Helper()
	batches := guideSQLBatches(t, guide, guideBeforePullSection)
	if len(batches) != 1 {
		t.Fatalf("%q has %d dolt sql commands, want one", guideBeforePullSection, len(batches))
	}
	return batches[0]
}

// guideSQLBatches returns, in order, the dolt sql batches in the guide
// section under heading: the body of each `dolt sql <<'SQL'` heredoc and the
// argument of each `dolt sql -q "..."`. Other shell lines (cd, cp) are not
// SQL and are skipped.
func guideSQLBatches(t *testing.T, guide, heading string) []string {
	t.Helper()
	lines := strings.Split(guide, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == heading })
	if start < 0 {
		t.Fatalf("%s has no %q section", accidentalReleaseGuide, heading)
	}
	var batches []string
	fenced := false
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			if strings.HasPrefix(line, "#") {
				break
			}
			continue
		}
		switch {
		case line == "dolt sql <<'SQL'":
			var body []string
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "SQL"; i++ {
				body = append(body, strings.TrimSpace(lines[i]))
			}
			batches = append(batches, strings.Join(body, "\n"))
		case strings.HasPrefix(line, `dolt sql -q "`) && strings.HasSuffix(line, `"`):
			batches = append(batches, strings.TrimSuffix(strings.TrimPrefix(line, `dolt sql -q "`), `"`))
		}
	}
	if len(batches) == 0 {
		t.Fatalf("%s section %q has no dolt sql commands", accidentalReleaseGuide, heading)
	}
	return batches
}

// splitSQLBatch splits a batch into statements as the dolt CLI does: at
// semicolons outside quotes. Whole-line `--` comments are dropped.
func splitSQLBatch(batch string) []string {
	var (
		stmts []string
		cur   strings.Builder
		quote rune
	)
	for _, line := range strings.Split(batch, "\n") {
		if quote == 0 && strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		for _, r := range line + "\n" {
			cur.WriteRune(r)
			switch {
			case quote != 0:
				if r == quote {
					quote = 0
				}
			case r == '\'' || r == '"' || r == '`':
				quote = r
			case r == ';':
				if stmt := strings.TrimSpace(strings.TrimSuffix(cur.String(), ";")); stmt != "" {
					stmts = append(stmts, stmt)
				}
				cur.Reset()
			}
		}
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		stmts = append(stmts, stmt)
	}
	return stmts
}

// runGuideBatch runs one batch the way a `dolt sql` invocation does: in a
// session of its own, statement by statement, stopping at the first error.
func runGuideBatch(t *testing.T, dataDir, batch string) error {
	t.Helper()
	var runErr error
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		for _, stmt := range splitSQLBatch(batch) {
			if err := schema.DrainCall(t.Context(), conn, stmt); err != nil {
				runErr = fmt.Errorf("%s: %w", stmt, err)
				return
			}
		}
	})
	return runErr
}

// runGuideSection runs every batch of a guide section in order and returns
// the first failure. Step 4's "nothing to commit" is not one: the guide says
// it means the rollback was already done.
func runGuideSection(t *testing.T, dataDir, guide, heading string) error {
	t.Helper()
	for _, batch := range guideSQLBatches(t, guide, heading) {
		err := runGuideBatch(t, dataDir, batch)
		if err != nil && batch == guideCursorRollback && isNothingToCommit(err) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// oldLineCreate writes what a 1.1-line bd create does: the issue and its
// audit row.
func oldLineCreate(ctx context.Context, conn *sql.Conn, issueID, tag string) error {
	if _, err := conn.ExecContext(ctx, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES (?, ?, '', '', '', '', 'open', 2, 'task')`, issueID, issueID); err != nil {
		return err
	}
	return oldLineEvent(ctx, conn, issueID, tag)
}

// oldLineEvent writes one audit row the way the 1.1 line does: under a
// random UUIDv7 id (issueops.NewEventID at v1.1.2), with the whole old issue
// as JSON in old_value (issueops.UpdateIssueInTx at v1.1.2). Its description
// is long enough that Dolt keeps the value outside the row, as it does for
// any TEXT value of a few KB.
func oldLineEvent(ctx context.Context, conn *sql.Conn, issueID, tag string) error {
	oldIssue := fmt.Sprintf(`{"id":%q,"description":%q}`, issueID, strings.Repeat("d", 3000))
	_, err := conn.ExecContext(ctx, `
INSERT INTO events (id, issue_id, event_type, actor, old_value, new_value, comment)
VALUES (?, ?, 'updated', 'tester', ?, NULL, ?)`, uuid.Must(uuid.NewV7()).String(), issueID, oldIssue, tag)
	return err
}

// copyEvent writes, under id, another event with the content of the one
// tagged tag: the identical row a second identical write in the same second
// makes.
func copyEvent(t *testing.T, ctx context.Context, conn *sql.Conn, tag, id string) {
	t.Helper()
	mustExecOn(t, ctx, conn, `
INSERT INTO events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
SELECT ?, issue_id, event_type, actor, old_value, new_value, comment, created_at FROM events WHERE comment = ? LIMIT 1`, id, tag)
}

// oldLineCommit is the 1.1 line's CommitPending: DOLT_COMMIT('-Am') with
// "nothing to commit" taken as success.
func oldLineCommit(ctx context.Context, conn *sql.Conn) error {
	err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-Am', 'bd: commit pending changes by tester')")
	if isNothingToCommit(err) {
		return nil
	}
	return err
}

// oldLinePull is the 1.1 line's bd dolt pull: commit pending, then pull.
func oldLinePull(ctx context.Context, conn *sql.Conn) error {
	if err := oldLineCommit(ctx, conn); err != nil {
		return fmt.Errorf("commit pending before pull: %w", err)
	}
	return schema.DrainCall(ctx, conn, "CALL DOLT_PULL('origin', 'main')")
}

func mustOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// migrateLikeV121 applies migrations 54..65 with one commit per migration,
// as v1.2.1's production migrator did, then rekeys the local events table
// as v1.2.1 did next.
func migrateLikeV121(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for v := 54; v <= 65; v++ {
		if _, err := schema.MigrateUpTo(ctx, conn, v); err != nil {
			t.Fatalf("MigrateUpTo(%d): %v", v, err)
		}
		mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustDrain(t, ctx, conn, fmt.Sprintf("CALL DOLT_COMMIT('--skip-empty', '-m', 'schema: apply migration %04d')", v))
	}
	rekeyEvents(t, ctx, conn)
}

// rekeyEvents is what v1.2.1's MigrateUp did to events once the migrations
// were committed: aux row-id rekey pass 2 (bd-ri8bd, ignored marker 0018),
// which runs for a database arriving from main 53, gave each row of the now
// dolt-ignored table the id derived from its content. The 1.1 line's own
// pass 1 (ignored marker 0009) had done the same to the versioned table,
// whose history keeps those ids. Rows of one content take the ids for
// ordinals 0..n-1: a row already holding one keeps it, and the others take
// the rest in id order. liveEvents reads the digest columns v1.2.1 hashed (it
// rendered created_at with CAST AS CHAR, the same text for this DATETIME
// column).
func rekeyEvents(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for digest, ids := range liveEvents(t, ctx, conn) {
		targets := make([]string, len(ids))
		for i := range ids {
			targets[i] = rowid.New("events", i, digest)
		}
		var free, unheld []string
		for _, id := range ids {
			if !slices.Contains(targets, id) {
				free = append(free, id)
			}
		}
		for _, target := range targets {
			if !slices.Contains(ids, target) {
				unheld = append(unheld, target)
			}
		}
		for i, id := range free {
			mustExecOn(t, ctx, conn, "UPDATE events SET id = ? WHERE id = ?", unheld[i], id)
		}
	}
	requireDerivedIDs(t, liveEvents(t, ctx, conn))
}

// seedMigratedMachine builds the machine step 5 is for: 1.1-line history
// with audit rows for two issues, then the v1.2.1 migration, then work after
// the upgrade that adds an audit row ("post-keep") and deletes issue gone-1,
// whose "pre-gone" row cascades out of the local table but stays in history.
func seedMigratedMachine(t *testing.T, ctx context.Context) string {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, 53)
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create keep-1", oldLineCreate(ctx, conn, "keep-1", "pre-keep"))
		mustOK(t, "create gone-1", oldLineCreate(ctx, conn, "gone-1", "pre-gone"))
		mustOK(t, "commit 1.1-line work", oldLineCommit(ctx, conn))
		migrateLikeV121(t, ctx, conn)
		mustOK(t, "post-upgrade event", oldLineEvent(ctx, conn, "keep-1", "post-keep"))
		mustExecOn(t, ctx, conn, "DELETE FROM issues WHERE id = 'gone-1'")
		mustOK(t, "commit post-upgrade work", oldLineCommit(ctx, conn))
	})
	return dataDir
}

// publishToNewRemote adds a fresh file:// remote as origin and pushes main.
func publishToNewRemote(t *testing.T, ctx context.Context, dataDir string) string {
	t.Helper()
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	return remoteURL
}

// eventTags returns the sorted event tags in the working set (asOf "") or
// at a revision (asOf "HEAD").
func eventTags(t *testing.T, ctx context.Context, conn *sql.Conn, asOf string) []string {
	t.Helper()
	query := "SELECT comment FROM events"
	if asOf != "" {
		query += " AS OF '" + asOf + "'"
	}
	rows, err := conn.QueryContext(ctx, query+" ORDER BY comment")
	if err != nil {
		t.Fatalf("read event tags: %v", err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag sql.NullString
		if err := rows.Scan(&tag); err != nil {
			t.Fatalf("scan event tag: %v", err)
		}
		tags = append(tags, tag.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read event tags: %v", err)
	}
	return tags
}

func requireEventTags(t *testing.T, ctx context.Context, conn *sql.Conn, asOf string, want ...string) {
	t.Helper()
	if got := eventTags(t, ctx, conn, asOf); !slices.Equal(got, want) {
		t.Fatalf("events (as of %q) = %v, want %v", asOf, got, want)
	}
}

// eventID returns the id of the event tagged tag in the working set (asOf
// "") or at a revision.
func eventID(t *testing.T, ctx context.Context, conn *sql.Conn, asOf, tag string) string {
	t.Helper()
	query := "SELECT id FROM events"
	if asOf != "" {
		query += " AS OF '" + asOf + "'"
	}
	var id string
	if err := conn.QueryRowContext(ctx, query+" WHERE comment = ?", tag).Scan(&id); err != nil {
		t.Fatalf("read the id of event %q (as of %q): %v", tag, asOf, err)
	}
	return id
}

// eventIDs returns the sorted ids of the events tagged tag in the working set
// (asOf "") or at a revision: one per copy of an identical row.
func eventIDs(t *testing.T, ctx context.Context, conn *sql.Conn, asOf, tag string) []string {
	t.Helper()
	query := "SELECT id FROM events"
	if asOf != "" {
		query += " AS OF '" + asOf + "'"
	}
	rows, err := conn.QueryContext(ctx, query+" WHERE comment = ? ORDER BY id", tag)
	if err != nil {
		t.Fatalf("read the ids of event %q (as of %q): %v", tag, asOf, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan the id of event %q: %v", tag, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the ids of event %q (as of %q): %v", tag, asOf, err)
	}
	return ids
}

// preflipRevision returns the first parent of v1.2.1's drop commit: the
// history step 5 restores events from.
func preflipRevision(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	return scalarString(t, ctx, conn, `
SELECT parent_hash FROM dolt_commit_ancestors WHERE parent_index = 0 AND commit_hash =
  (SELECT commit_hash FROM dolt_log WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)')`)
}

// requireUnchangedSinceStep4 asserts that a refused batch changed nothing on
// a machine step 4 recovered: HEAD is still step 4's commit, and events is
// still the local, unversioned table under its ignore rule.
func requireUnchangedSinceStep4(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	if head := scalarString(t, ctx, conn, "SELECT message FROM dolt_log LIMIT 1"); head != "recovery: roll schema cursor back to v53 (accidental v1.2.1)" {
		t.Fatalf("HEAD is %q, want the cursor rollback: the stopped step committed", head)
	}
	if !eventsTablePresent(t, ctx, conn) || trackedAtHead(t, ctx, conn, "events") {
		t.Fatal("the stopped step changed the local events table")
	}
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events'"); n != 1 {
		t.Fatal("the stopped step removed the events ignore rule")
	}
}

func requireNothingPending(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	pending, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending changes %v, want none", pending)
	}
}

func eventsIgnoreRuleAtHead(t *testing.T, ctx context.Context, conn *sql.Conn) bool {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_ignore AS OF 'HEAD' WHERE pattern = 'events'") > 0
}

// requireCleanMerge asserts a pull left no row or schema conflicts, no open
// merge and nothing pending.
func requireCleanMerge(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_conflicts"); n != 0 {
		t.Fatalf("%d tables in conflict after the pull", n)
	}
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_schema_conflicts"); n != 0 {
		t.Fatalf("%d tables in schema conflict after the pull", n)
	}
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging"); n != 0 {
		t.Fatal("the pull left a merge open")
	}
	requireNothingPending(t, ctx, conn)
}

// requireWritableLikeOldLine performs an issue-plus-event write and the 1.1
// line's commit, and asserts the write landed in HEAD with nothing left
// pending.
func requireWritableLikeOldLine(t *testing.T, ctx context.Context, conn *sql.Conn, issueID, tag string) {
	t.Helper()
	mustOK(t, "issue write "+issueID, oldLineCreate(ctx, conn, issueID, tag))
	mustOK(t, "commit "+issueID, oldLineCommit(ctx, conn))
	requireNothingPending(t, ctx, conn)
	if !slices.Contains(eventTags(t, ctx, conn, "HEAD"), tag) {
		t.Fatalf("event %q is not in HEAD after the commit", tag)
	}
}

// TestEmbeddedAccidentalReleaseRecoveryRestoresEvents is AC-1: the guide's
// required steps restore the historical events table, keep this machine's
// audit rows, matched to history by content since v1.2.1 rekeyed them and
// counted where rows are identical, give a local table the historical rows
// it lacks, and leave nothing pending; and step 5's checks stop an empty
// local table and other staged work before anything changes.
func TestEmbeddedAccidentalReleaseRecoveryRestoresEvents(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)

	t.Run("migration-carried history", func(t *testing.T) {
		dataDir := seedMigratedMachine(t, ctx)
		var historicalPreKeep, localPostKeep string
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			historicalPreKeep = eventID(t, ctx, conn, preflipRevision(t, ctx, conn), "pre-keep")
			if eventID(t, ctx, conn, "", "pre-keep") == historicalPreKeep {
				t.Fatal("pre-keep has its historical id in the local table; the v1.2.1 rekey is not reproduced")
			}
			localPostKeep = eventID(t, ctx, conn, "", "post-keep")
		})
		mustOK(t, "the guide's required steps", runGuideSection(t, dataDir, guide, guideRequiredSection))

		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			if v := currentMainVersion(t, ctx, conn); v != 53 {
				t.Fatalf("schema cursor = %d, want 53", v)
			}
			if !trackedAtHead(t, ctx, conn, "events") {
				t.Fatal("events is not in HEAD after recovery")
			}
			// The historical table, not the one 0062 recreated: id leads.
			if pos := scalarInt(t, ctx, conn, `
SELECT ORDINAL_POSITION FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'events' AND COLUMN_NAME = 'id'`); pos != 1 {
				t.Fatalf("events.id is column %d, want 1 (the pre-flip schema)", pos)
			}
			requireEventTags(t, ctx, conn, "HEAD", "post-keep", "pre-keep")
			// A row the history has keeps its historical id, the one every
			// peer has for it; a row written since the upgrade keeps its own.
			if got := eventID(t, ctx, conn, "HEAD", "pre-keep"); got != historicalPreKeep {
				t.Fatalf("pre-keep id = %s after recovery, want its historical id %s", got, historicalPreKeep)
			}
			if got := eventID(t, ctx, conn, "HEAD", "post-keep"); got != localPostKeep {
				t.Fatalf("post-keep id = %s after recovery, want its local id %s", got, localPostKeep)
			}
			if eventsIgnoreRuleAtHead(t, ctx, conn) {
				t.Fatal("the events ignore rule is still in HEAD")
			}
			requireNothingPending(t, ctx, conn)
		})
	})

	t.Run("empty local table stops before any change", func(t *testing.T) {
		dataDir := seedMigratedMachine(t, ctx)
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, "DELETE FROM events")
		})
		err := runGuideSection(t, dataDir, guide, guideRequiredSection)
		if err == nil || !strings.Contains(err.Error(), "local_events_not_empty") {
			t.Fatalf("required steps on an empty local events table: err = %v, want the local_events_not_empty check", err)
		}

		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			requireUnchangedSinceStep4(t, ctx, conn)
			requireNothingPending(t, ctx, conn)
		})
	})

	t.Run("local table missing a historical row gets it back", func(t *testing.T) {
		dataDir := seedMigratedMachine(t, ctx)
		var historicalPreKeep string
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			historicalPreKeep = eventID(t, ctx, conn, preflipRevision(t, ctx, conn), "pre-keep")
			mustExecOn(t, ctx, conn, "DELETE FROM events WHERE comment = 'pre-keep'")
		})
		mustOK(t, "the guide's required steps", runGuideSection(t, dataDir, guide, guideRequiredSection))

		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			requireEventTags(t, ctx, conn, "HEAD", "post-keep", "pre-keep")
			if got := eventID(t, ctx, conn, "HEAD", "pre-keep"); got != historicalPreKeep {
				t.Fatalf("pre-keep id = %s after recovery, want its historical id %s", got, historicalPreKeep)
			}
			requireNothingPending(t, ctx, conn)
		})
	})

	t.Run("identical rows are counted", func(t *testing.T) {
		// History holds one "held" row, which the 1.1 line's own re-key gave
		// its derived id, two identical "missing" rows and one "surplus" row.
		// Since the upgrade this machine lost a "missing" copy and gained a
		// "surplus" copy and a "held" copy, the last under an id that sorts
		// before the held one.
		const sortsFirst = "00000000-0000-7000-8000-000000000000"
		dataDir := seedMainSchemaAt(t, ctx, 53)
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			mustOK(t, "create dup-1", oldLineCreate(ctx, conn, "dup-1", "held"))
			rekeyEvents(t, ctx, conn)
			mustOK(t, "missing event", oldLineEvent(ctx, conn, "dup-1", "missing"))
			copyEvent(t, ctx, conn, "missing", uuid.Must(uuid.NewV7()).String())
			mustOK(t, "surplus event", oldLineEvent(ctx, conn, "dup-1", "surplus"))
			mustOK(t, "commit 1.1-line work", oldLineCommit(ctx, conn))
			migrateLikeV121(t, ctx, conn)
			mustExecOn(t, ctx, conn, "DELETE FROM events WHERE comment = 'missing' LIMIT 1")
			copyEvent(t, ctx, conn, "surplus", uuid.Must(uuid.NewV7()).String())
			copyEvent(t, ctx, conn, "held", sortsFirst)
			mustOK(t, "commit post-upgrade work", oldLineCommit(ctx, conn))
		})
		var history, local map[string][]string
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			preflip := preflipRevision(t, ctx, conn)
			history, local = map[string][]string{}, map[string][]string{}
			for _, tag := range []string{"held", "missing", "surplus"} {
				history[tag] = eventIDs(t, ctx, conn, preflip, tag)
				local[tag] = eventIDs(t, ctx, conn, "", tag)
			}
		})
		if !slices.Contains(local["held"], history["held"][0]) {
			t.Fatalf("local held ids %v lack the historical id %v; the 1.1-line re-key is not reproduced", local["held"], history["held"])
		}
		mustOK(t, "the guide's required steps", runGuideSection(t, dataDir, guide, guideRequiredSection))

		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			// Each row comes back as many times as either side has it: the
			// history's copies under their ids, this machine's extra ones
			// under its own.
			requireEventTags(t, ctx, conn, "HEAD", "held", "held", "missing", "missing", "surplus", "surplus")
			if got, want := eventIDs(t, ctx, conn, "HEAD", "held"), []string{sortsFirst, history["held"][0]}; !slices.Equal(got, want) {
				t.Fatalf("held ids = %v after recovery, want the historical id and the extra copy's %v", got, want)
			}
			if got := eventIDs(t, ctx, conn, "HEAD", "missing"); !slices.Equal(got, history["missing"]) {
				t.Fatalf("missing ids = %v after recovery, want the historical ids %v", got, history["missing"])
			}
			surplus := eventIDs(t, ctx, conn, "HEAD", "surplus")
			extra := slices.DeleteFunc(slices.Clone(surplus), func(id string) bool { return id == history["surplus"][0] })
			if len(extra) != 1 || !slices.Contains(local["surplus"], extra[0]) {
				t.Fatalf("surplus ids = %v after recovery, want the historical id %v and one of the local ids %v", surplus, history["surplus"], local["surplus"])
			}
			requireNothingPending(t, ctx, conn)
		})
	})

	t.Run("other staged work stops before any change", func(t *testing.T) {
		dataDir := seedMigratedMachine(t, ctx)
		mustOK(t, "step 4", runGuideBatch(t, dataDir, guideCursorRollback))
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			mustOK(t, "issue write", oldLineCreate(ctx, conn, "staged-1", "staged"))
			mustDrain(t, ctx, conn, "CALL DOLT_ADD('issues')")
		})
		err := runGuideBatch(t, dataDir, guideStep5(t, guide))
		if err == nil || !strings.Contains(err.Error(), "nothing_else_staged") {
			t.Fatalf("step 5 with issues staged: err = %v, want the nothing_else_staged check", err)
		}

		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			requireUnchangedSinceStep4(t, ctx, conn)
			if staged := scalarString(t, ctx, conn, "SELECT GROUP_CONCAT(table_name) FROM dolt_status WHERE staged = 1"); staged != "issues" {
				t.Fatalf("staged tables = %q after the stopped step, want the issues write alone", staged)
			}
		})
	})
}

// TestEmbeddedAccidentalReleaseRecoveredCloneWrites is AC-2, the reported
// bug: a fresh clone of a database recovered by the guide takes an
// issue-plus-event write and the 1.1 line's commit, with no migrator run.
func TestEmbeddedAccidentalReleaseRecoveredCloneWrites(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)

	source := seedMigratedMachine(t, ctx)
	mustOK(t, "the guide's required steps", runGuideSection(t, source, guide, guideRequiredSection))
	remoteURL := publishToNewRemote(t, ctx, source)
	_, clone := cloneLineage(t, ctx, remoteURL)

	withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
		requireWritableLikeOldLine(t, ctx, conn, "clone-1", "clone-create")
		requireEventTags(t, ctx, conn, "HEAD", "clone-create", "post-keep", "pre-keep")
	})
}

// TestEmbeddedAccidentalReleaseRecoveryDivergentPeers is AC-3: peers that
// diverged before the flip, two of which ran v1.2.1, follow the guide's
// synchronization order. Both merges complete cleanly and hold the surviving
// common events plus each peer's own, without the deleted issue's.
func TestEmbeddedAccidentalReleaseRecoveryDivergentPeers(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)

	first := seedMainSchemaAt(t, ctx, 53)
	withRawConn(t, first, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create common-keep", oldLineCreate(ctx, conn, "common-keep", "common-keep"))
		mustOK(t, "create common-gone", oldLineCreate(ctx, conn, "common-gone", "common-gone"))
		mustOK(t, "commit common work", oldLineCommit(ctx, conn))
	})
	remoteURL := publishToNewRemote(t, ctx, first)
	_, second := cloneLineage(t, ctx, remoteURL)
	_, neverMigrated := cloneLineage(t, ctx, remoteURL)

	// Divergent 1.1-line work, then v1.2.1 on two of the peers.
	withRawConn(t, second, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create second-1", oldLineCreate(ctx, conn, "second-1", "second-pre"))
		mustOK(t, "commit", oldLineCommit(ctx, conn))
		migrateLikeV121(t, ctx, conn)
		mustOK(t, "post-upgrade event", oldLineEvent(ctx, conn, "second-1", "second-post"))
		mustOK(t, "commit", oldLineCommit(ctx, conn))
	})
	withRawConn(t, neverMigrated, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create never-1", oldLineCreate(ctx, conn, "never-1", "never-pre"))
		mustOK(t, "commit", oldLineCommit(ctx, conn))
	})
	withRawConn(t, first, preflipDatabase, func(conn *sql.Conn) {
		migrateLikeV121(t, ctx, conn)
		mustOK(t, "post-upgrade event", oldLineEvent(ctx, conn, "common-keep", "first-post"))
		mustExecOn(t, ctx, conn, "DELETE FROM issues WHERE id = 'common-gone'")
		mustOK(t, "commit", oldLineCommit(ctx, conn))
	})

	// The machine holding post-upgrade rows recovers and pushes first.
	mustOK(t, "first: the guide's required steps", runGuideSection(t, first, guide, guideRequiredSection))
	withRawConn(t, first, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	// Another migrated machine recovers on its own before it pulls.
	mustOK(t, "second: the guide's required steps", runGuideSection(t, second, guide, guideRequiredSection))
	withRawConn(t, second, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "second: pull", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		requireEventTags(t, ctx, conn, "HEAD", "common-keep", "first-post", "second-post", "second-pre")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	// A machine that never ran v1.2.1 just pulls.
	withRawConn(t, neverMigrated, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "never-migrated: pull", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		requireEventTags(t, ctx, conn, "HEAD", "common-keep", "first-post", "never-pre", "second-post", "second-pre")
		if v := currentMainVersion(t, ctx, conn); v != 53 {
			t.Fatalf("schema cursor = %d after the pull, want 53", v)
		}
	})
}

// TestEmbeddedAccidentalReleaseRecoveryV121Clone covers a machine that got
// the migration by cloning with v1.2.1, as v1.2.1's bd bootstrap does:
// ignored 0019 gave it an empty, unversioned events table of its own, which
// then took its audit rows. Step 5 restores the history's rows beside its
// own, and it then pulls the recovered source cleanly.
func TestEmbeddedAccidentalReleaseRecoveryV121Clone(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)
	createSQL, err := schema.IgnoredMigrationSQL("0019_create_events.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0019 migration: %v", err)
	}

	source := seedMigratedMachine(t, ctx)
	remoteURL := publishToNewRemote(t, ctx, source)
	_, clone := cloneLineage(t, ctx, remoteURL)
	var historicalPreKeep string
	withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
		execFrozenGuard(t, ctx, conn, createSQL)
		// v1.2.1 writes a row under its content-derived id.
		mustOK(t, "clone: post-clone event", oldLineEvent(ctx, conn, "keep-1", "clone-post"))
		rekeyEvents(t, ctx, conn)
		requireNothingPending(t, ctx, conn)
		historicalPreKeep = eventID(t, ctx, conn, preflipRevision(t, ctx, conn), "pre-keep")
	})

	// The source recovers and pushes; the clone recovers on its own before
	// it pulls.
	mustOK(t, "source: the guide's required steps", runGuideSection(t, source, guide, guideRequiredSection))
	withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	mustOK(t, "clone: the guide's required steps", runGuideSection(t, clone, guide, guideRequiredSection))
	withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
		requireEventTags(t, ctx, conn, "HEAD", "clone-post", "pre-keep")
		if got := eventID(t, ctx, conn, "HEAD", "pre-keep"); got != historicalPreKeep {
			t.Fatalf("pre-keep id = %s after recovery, want its historical id %s", got, historicalPreKeep)
		}
		mustOK(t, "clone: pull", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		requireEventTags(t, ctx, conn, "HEAD", "clone-post", "post-keep", "pre-keep")
		requireWritableLikeOldLine(t, ctx, conn, "clone-1", "clone-create")
	})
}

// TestEmbeddedAccidentalReleaseRecoveryTwoFlips covers peers that both ran
// v1.2.1 and synced before recovering: the second pulled the first's
// migration while still on v1.2.1, so its history holds both removal
// commits. Each recovers on its own, and the second then pulls the first's
// recovered table cleanly, with every peer's audit rows.
func TestEmbeddedAccidentalReleaseRecoveryTwoFlips(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)

	origin := seedMainSchemaAt(t, ctx, 53)
	withRawConn(t, origin, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create common-1", oldLineCreate(ctx, conn, "common-1", "common"))
		mustOK(t, "commit common work", oldLineCommit(ctx, conn))
	})
	remoteURL := publishToNewRemote(t, ctx, origin)
	_, first := cloneLineage(t, ctx, remoteURL)
	_, second := cloneLineage(t, ctx, remoteURL)

	withRawConn(t, first, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create first-1", oldLineCreate(ctx, conn, "first-1", "first-pre"))
		mustOK(t, "commit", oldLineCommit(ctx, conn))
		migrateLikeV121(t, ctx, conn)
		mustOK(t, "post-upgrade event", oldLineEvent(ctx, conn, "first-1", "first-post"))
		mustOK(t, "commit", oldLineCommit(ctx, conn))
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	withRawConn(t, second, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "create second-1", oldLineCreate(ctx, conn, "second-1", "second-pre"))
		mustOK(t, "commit", oldLineCommit(ctx, conn))
		migrateLikeV121(t, ctx, conn)
		mustOK(t, "pull while on v1.2.1", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)'"); n != 2 {
			t.Fatalf("the second peer's history holds %d removal commits, want 2: the shape is not reproduced", n)
		}
	})

	mustOK(t, "first: the guide's required steps", runGuideSection(t, first, guide, guideRequiredSection))
	withRawConn(t, first, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	mustOK(t, "second: the guide's required steps", runGuideSection(t, second, guide, guideRequiredSection))
	withRawConn(t, second, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "second: pull", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		requireEventTags(t, ctx, conn, "HEAD", "common", "first-post", "first-pre", "second-pre")
		requireWritableLikeOldLine(t, ctx, conn, "second-2", "second-after")
	})
}

// TestEmbeddedAccidentalReleaseMissingEventsClone is AC-4: a clone of a
// database published after the cursor rollback alone has no events table.
// Its first issue write fails with the reported error; either documented
// repair, pulling a recovered source or the history-only batch, makes its
// next issue-plus-event write commit cleanly, also on a machine with an
// empty events table once the batch before the pull has dropped it; a
// hand-made table that already took writes recovers with step 5; and the
// history-only batch and the batch before the pull stop before any change
// when they do not apply.
func TestEmbeddedAccidentalReleaseMissingEventsClone(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)
	beforePull := guideBeforePull(t, guide)

	cloneWithoutEvents := func(t *testing.T, remoteURL string) string {
		t.Helper()
		_, clone := cloneLineage(t, ctx, remoteURL)
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			if eventsTablePresent(t, ctx, conn) {
				t.Fatal("the clone has an events table; the reported shape is not reproduced")
			}
		})
		return clone
	}
	publishedWithoutEvents := func(t *testing.T) (source, clone, remoteURL string) {
		t.Helper()
		source = seedMigratedMachine(t, ctx)
		mustOK(t, "cursor rollback alone", runGuideBatch(t, source, guideCursorRollback))
		remoteURL = publishToNewRemote(t, ctx, source)
		return source, cloneWithoutEvents(t, remoteURL), remoteURL
	}
	recoverAndPush := func(t *testing.T, source string) {
		t.Helper()
		mustOK(t, "source: the guide's required steps", runGuideSection(t, source, guide, guideRequiredSection))
		withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
		})
	}

	t.Run("first issue write fails before the repair", func(t *testing.T) {
		_, clone, _ := publishedWithoutEvents(t)
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			err := oldLineEvent(ctx, conn, "keep-1", "clone-before-repair")
			if err == nil || !strings.Contains(err.Error(), "table not found: events") {
				t.Fatalf("the clone's event write: err = %v, want the reported table not found: events", err)
			}
		})
	})

	t.Run("pull from a recovered source", func(t *testing.T) {
		source, clone, _ := publishedWithoutEvents(t)
		// Step 5 on the clone stops early: there is no local table.
		if err := runGuideSection(t, clone, guide, guideRequiredSection); err == nil || !strings.Contains(err.Error(), "table not found") {
			t.Fatalf("required steps on the clone: err = %v, want table not found", err)
		}
		recoverAndPush(t, source)
		// With no events table here, the batch before the pull changes nothing.
		mustOK(t, "clone: the batch before the pull", runGuideBatch(t, clone, beforePull))
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			requireNothingPending(t, ctx, conn)
			mustOK(t, "clone: pull", oldLinePull(ctx, conn))
			requireCleanMerge(t, ctx, conn)
			requireWritableLikeOldLine(t, ctx, conn, "clone-1", "clone-create")
			requireEventTags(t, ctx, conn, "HEAD", "clone-create", "post-keep", "pre-keep")
		})
	})

	t.Run("pull from a recovered source over an empty hand-made table", func(t *testing.T) {
		source, clone, _ := publishedWithoutEvents(t)
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, handMadeEventsTable)
		})
		recoverAndPush(t, source)

		// The empty table blocks the pull, commit or not, until the batch
		// drops it.
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			err := oldLinePull(ctx, conn)
			if err == nil || !strings.Contains(err.Error(), "local changes would be stomped by merge") || !strings.Contains(err.Error(), "events") {
				t.Fatalf("clone: pull over the empty table: err = %v, want the stomped-by-merge refusal naming events that the guide quotes", err)
			}
			if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging"); n != 0 {
				t.Fatal("the refused pull left a merge open")
			}
		})
		mustOK(t, "clone: the batch before the pull", runGuideBatch(t, clone, beforePull))
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			if eventsTablePresent(t, ctx, conn) {
				t.Fatal("the batch before the pull left the empty events table")
			}
			requireNothingPending(t, ctx, conn)
			mustOK(t, "clone: pull", oldLinePull(ctx, conn))
			requireCleanMerge(t, ctx, conn)
			requireWritableLikeOldLine(t, ctx, conn, "clone-1", "clone-create")
			requireEventTags(t, ctx, conn, "HEAD", "clone-create", "post-keep", "pre-keep")
		})

		// Run again, the batch never drops the versioned table, even an
		// empty one.
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, "DELETE FROM events")
			mustOK(t, "commit the emptied table", oldLineCommit(ctx, conn))
		})
		if err := runGuideBatch(t, clone, beforePull); err == nil || !strings.Contains(err.Error(), "events_history_found") {
			t.Fatalf("the batch before the pull, after the pull: err = %v, want the events_history_found check", err)
		}
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			if !eventsTablePresent(t, ctx, conn) || !trackedAtHead(t, ctx, conn, "events") {
				t.Fatal("the batch before the pull dropped the versioned events table")
			}
			requireNothingPending(t, ctx, conn)
		})
	})

	t.Run("step 5 over a hand-made table with rows", func(t *testing.T) {
		source, clone, _ := publishedWithoutEvents(t)
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, handMadeEventsTable)
			mustOK(t, "clone write", oldLineCreate(ctx, conn, "clone-1", "clone-create"))
			// The hand-made table has no foreign key, so a deleted issue's
			// row stays in it.
			mustOK(t, "clone write", oldLineCreate(ctx, conn, "clone-2", "clone-gone"))
			mustExecOn(t, ctx, conn, "DELETE FROM issues WHERE id = 'clone-2'")
			mustOK(t, "clone commit", oldLineCommit(ctx, conn))
		})
		mustOK(t, "clone: the guide's required steps", runGuideSection(t, clone, guide, guideRequiredSection))
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			requireEventTags(t, ctx, conn, "HEAD", "clone-create", "pre-keep")
		})
		recoverAndPush(t, source)
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			mustOK(t, "clone: pull", oldLinePull(ctx, conn))
			requireCleanMerge(t, ctx, conn)
			requireEventTags(t, ctx, conn, "HEAD", "clone-create", "post-keep", "pre-keep")
		})
	})

	t.Run("the batch before the pull stops at a table with rows", func(t *testing.T) {
		source, _, _ := publishedWithoutEvents(t)
		err := runGuideBatch(t, source, beforePull)
		if err == nil || !strings.Contains(err.Error(), "local_events_absent_or_empty") {
			t.Fatalf("the batch before the pull on a table with rows: err = %v, want the local_events_absent_or_empty check", err)
		}
		withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
			requireUnchangedSinceStep4(t, ctx, conn)
			requireEventTags(t, ctx, conn, "", "post-keep", "pre-keep")
			requireNothingPending(t, ctx, conn)
		})
	})

	t.Run("history-only batch", func(t *testing.T) {
		_, clone, _ := publishedWithoutEvents(t)
		mustOK(t, "the history-only batch", runGuideSection(t, clone, guide, guideMissingSection))
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			// Pre-upgrade rows of surviving issues; post-keep never reached
			// history and gone-1 was deleted.
			requireEventTags(t, ctx, conn, "HEAD", "pre-keep")
			if eventsIgnoreRuleAtHead(t, ctx, conn) {
				t.Fatal("the events ignore rule is still in HEAD")
			}
			requireWritableLikeOldLine(t, ctx, conn, "clone-1", "clone-create")
		})
	})

	t.Run("history-only batch over an empty hand-made table", func(t *testing.T) {
		_, clone, _ := publishedWithoutEvents(t)
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, handMadeEventsTable)
		})
		mustOK(t, "the history-only batch", runGuideSection(t, clone, guide, guideMissingSection))
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			requireEventTags(t, ctx, conn, "HEAD", "pre-keep")
			requireWritableLikeOldLine(t, ctx, conn, "clone-1", "clone-create")
		})
	})

	t.Run("history-only batch, then a machine with an empty hand-made table pulls", func(t *testing.T) {
		_, clone, remoteURL := publishedWithoutEvents(t)
		other := cloneWithoutEvents(t, remoteURL)
		withRawConn(t, other, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, handMadeEventsTable)
		})
		mustOK(t, "the history-only batch", runGuideSection(t, clone, guide, guideMissingSection))
		withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
		})

		mustOK(t, "other: the batch before the pull", runGuideBatch(t, other, beforePull))
		withRawConn(t, other, preflipDatabase, func(conn *sql.Conn) {
			mustOK(t, "other: pull", oldLinePull(ctx, conn))
			requireCleanMerge(t, ctx, conn)
			if eventsIgnoreRuleAtHead(t, ctx, conn) {
				t.Fatal("the events ignore rule is still in HEAD after the pull")
			}
			requireWritableLikeOldLine(t, ctx, conn, "other-1", "other-create")
			requireEventTags(t, ctx, conn, "HEAD", "other-create", "pre-keep")
		})
	})

	t.Run("history-only batch without a usable history source stops before any change", func(t *testing.T) {
		// A drop commit whose first parent has no events table, under a
		// hand-made empty local table.
		dataDir := seedMainSchemaAt(t, ctx, 53)
		var head string
		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			mustExecOn(t, ctx, conn, "DROP TABLE events")
			mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
			mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: history without events')")
			mustExecOn(t, ctx, conn, "INSERT INTO dolt_ignore VALUES ('events', 1)")
			mustDrain(t, ctx, conn, "CALL DOLT_ADD('dolt_ignore')")
			mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'schema: drop versioned events table for ignored-plane flip (bd-red8u)')")
			mustExecOn(t, ctx, conn, handMadeEventsTable)
			requireNothingPending(t, ctx, conn)
			head = scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
		})

		err := runGuideSection(t, dataDir, guide, guideMissingSection)
		if err == nil || !strings.Contains(err.Error(), "events AS OF @preflip") {
			t.Fatalf("history-only batch without a history source: err = %v, want the history probe to stop it", err)
		}

		withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
			if got := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); got != head {
				t.Fatalf("HEAD moved to %s, want %s: the stopped batch committed", got, head)
			}
			if !eventsTablePresent(t, ctx, conn) || trackedAtHead(t, ctx, conn, "events") {
				t.Fatal("the stopped batch changed the local events table")
			}
			if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events'"); n != 1 || !eventsIgnoreRuleAtHead(t, ctx, conn) {
				t.Fatal("the stopped batch removed the events ignore rule")
			}
			requireNothingPending(t, ctx, conn)
		})
	})
}

// TestEmbeddedAccidentalReleaseFinishOldRetrack is AC-5: a source and a
// clone left tracked-but-ignored by the earlier guide's optional command run
// the documented finish command and synchronize; afterwards writes leave
// nothing pending and merges complete in both directions. Step 5 refuses
// such a history, and the clone cannot pull until it has finished too.
func TestEmbeddedAccidentalReleaseFinishOldRetrack(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)

	source := seedMigratedMachine(t, ctx)
	mustOK(t, "cursor rollback", runGuideBatch(t, source, guideCursorRollback))
	mustOK(t, "the old re-track command", runGuideBatch(t, source, guideOldRetrack))

	// Step 5 refuses this history and leaves events as it was.
	var before []string
	withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
		before = eventTags(t, ctx, conn, "")
	})
	if err := runGuideSection(t, source, guide, guideRequiredSection); err == nil || !strings.Contains(err.Error(), "events_history_found") {
		t.Fatalf("required steps after the old re-track: err = %v, want the events_history_found check", err)
	}
	withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
		if head := scalarString(t, ctx, conn, "SELECT message FROM dolt_log LIMIT 1"); head != "recovery: re-track events table" {
			t.Fatalf("HEAD is %q, want the old re-track: the refused step committed", head)
		}
		requireEventTags(t, ctx, conn, "", before...)
	})

	remoteURL := publishToNewRemote(t, ctx, source)
	_, clone := cloneLineage(t, ctx, remoteURL)

	withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "clone write", oldLineCreate(ctx, conn, "clone-1", "clone-before"))
		mustOK(t, "clone commit", oldLineCommit(ctx, conn))
		pending, err := statusTables(ctx, conn)
		mustOK(t, "read dolt_status", err)
		if !slices.Contains(pending, "events") {
			t.Fatalf("pending %v after a clone write, want events: the tracked-but-ignored shape is not reproduced", pending)
		}
	})
	// A source write left pending too; the finish command commits it, so
	// origin gets an events change for the clone's pull to meet.
	withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
		mustOK(t, "source write", oldLineCreate(ctx, conn, "source-0", "source-before"))
		mustOK(t, "source commit", oldLineCommit(ctx, conn))
	})

	finish := guideSQLBatches(t, guide, guideOldRetrackSection)
	if len(finish) != 1 {
		t.Fatalf("%q has %d dolt sql commands, want the one finish command", guideOldRetrackSection, len(finish))
	}
	mustOK(t, "source: finish command", runGuideBatch(t, source, finish[0]))
	withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
		requireNothingPending(t, ctx, conn)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	// Until the clone finishes too, its pending events change blocks the pull.
	withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
		if err := oldLinePull(ctx, conn); err == nil {
			t.Fatal("the clone pulled with its events change still pending; the tracked-but-ignored shape is not reproduced")
		}
		if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging"); n != 0 {
			t.Fatal("the refused pull left a merge open")
		}
	})

	mustOK(t, "clone: finish command", runGuideBatch(t, clone, finish[0]))
	withRawConn(t, clone, preflipDatabase, func(conn *sql.Conn) {
		requireNothingPending(t, ctx, conn)
		mustOK(t, "clone: pull", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		if eventsIgnoreRuleAtHead(t, ctx, conn) {
			t.Fatal("the events ignore rule is still in the clone's HEAD")
		}
		requireWritableLikeOldLine(t, ctx, conn, "clone-2", "clone-after")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	withRawConn(t, source, preflipDatabase, func(conn *sql.Conn) {
		if eventsIgnoreRuleAtHead(t, ctx, conn) {
			t.Fatal("the events ignore rule is still in the source's HEAD")
		}
		requireWritableLikeOldLine(t, ctx, conn, "source-1", "source-after")
		mustOK(t, "source: pull", oldLinePull(ctx, conn))
		requireCleanMerge(t, ctx, conn)
		requireEventTags(t, ctx, conn, "HEAD", "clone-after", "clone-before", "post-keep", "pre-keep", "source-after", "source-before")
	})
}
