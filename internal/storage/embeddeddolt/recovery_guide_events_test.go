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
// events row, and a commit is DOLT_COMMIT('-Am') (EmbeddedDoltStore.Commit at
// v1.2.2). Main's migrations 54..65 are the files v1.2.1 shipped; they are
// applied one commit per migration, as v1.2.1's production migrator did, so
// the parent of 0062's drop commit holds the events rows 0061 rekeyed. Each
// event's comment is a tag the assertions read, since 0061 rekeys ids.
// Like the rest of the events-flip tests they need BEADS_TEST_EMBEDDED_DOLT=1.

const accidentalReleaseGuide = "docs/recovery/accidental-1-2-1-release.md"

// The guide's sections whose commands the tests run. A section ends at the
// next heading of any level, so the numbered procedure under
// guideRequiredSection does not pick up a subsection's commands.
const guideRequiredSection = "## Recommended fix: roll the schema cursor back"

// guideCursorRollback is the guide's step 4, which is all an earlier version
// of the guide required: a database recovered with it alone keeps events
// out of versioned history.
const guideCursorRollback = "DELETE FROM schema_migrations WHERE version > 53; CALL DOLT_ADD('schema_migrations'); CALL DOLT_COMMIT('-m', 'recovery: roll schema cursor back to v53 (accidental v1.2.1)', '--author', 'bd recovery <recovery@beads.invalid>')"

func readAccidentalReleaseGuide(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bazeltest.RepoRoot(t), filepath.FromSlash(accidentalReleaseGuide)))
	if err != nil {
		t.Fatalf("read the recovery guide: %v", err)
	}
	return string(data)
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

// oldLineEvent writes one audit row the way the 1.1 line does, id included.
func oldLineEvent(ctx context.Context, conn *sql.Conn, issueID, tag string) error {
	_, err := conn.ExecContext(ctx, `
INSERT INTO events (id, issue_id, event_type, actor, old_value, new_value, comment)
VALUES (?, ?, 'updated', 'tester', NULL, NULL, ?)`, tag, issueID, tag)
	return err
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
// as v1.2.1's production migrator did.
func migrateLikeV121(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for v := 54; v <= 65; v++ {
		if _, err := schema.MigrateUpTo(ctx, conn, v); err != nil {
			t.Fatalf("MigrateUpTo(%d): %v", v, err)
		}
		mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustDrain(t, ctx, conn, fmt.Sprintf("CALL DOLT_COMMIT('--skip-empty', '-m', 'schema: apply migration %04d')", v))
	}
}

// seedMigratedMachine builds the machine step 5 is for: 1.1-line history
// with audit rows for two issues, then the v1.2.1 migration, then work under
// v1.2.1 that adds an audit row ("post-keep") and deletes issue gone-1, whose
// "pre-gone" row cascades out of the local table but stays in history.
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
// audit rows, and leave nothing pending; and its row-loss check stops an
// empty local table before anything changes.
func TestEmbeddedAccidentalReleaseRecoveryRestoresEvents(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	guide := readAccidentalReleaseGuide(t)

	t.Run("migration-carried history", func(t *testing.T) {
		dataDir := seedMigratedMachine(t, ctx)
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
			if head := scalarString(t, ctx, conn, "SELECT message FROM dolt_log LIMIT 1"); head != "recovery: roll schema cursor back to v53 (accidental v1.2.1)" {
				t.Fatalf("HEAD is %q, want the cursor rollback: the stopped step committed", head)
			}
			if !eventsTablePresent(t, ctx, conn) || trackedAtHead(t, ctx, conn, "events") {
				t.Fatal("the stopped step changed the local events table")
			}
			if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_ignore WHERE pattern = 'events'"); n != 1 {
				t.Fatal("the stopped step removed the events ignore rule")
			}
			requireNothingPending(t, ctx, conn)
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
