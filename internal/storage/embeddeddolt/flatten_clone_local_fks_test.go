//go:build cgo

package embeddeddolt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestFlattenKeepsCloneLocalFKs: Flatten ends in a reset of main onto the
// flattened branch, which drops the foreign keys of clone-local
// (dolt_ignored) tables when that reset is DOLT_RESET('--hard') (bd-7bpkd).
// A flatten must leave every clone-local FK it found in place, still
// enforcing and cascading, with the tracked snapshot unchanged and the
// history squashed to the initial commit plus one snapshot commit.
func TestFlattenKeepsCloneLocalFKs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "fk")

	var lastID string
	for _, title := range []string{"one", "two", "three"} {
		issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", title, err)
		}
		lastID = issue.ID
	}
	te.exec(t, ctx, "CALL DOLT_COMMIT('-Am', 'repro: extra commit', '--allow-empty')")

	const countFKs = `SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_TYPE = 'FOREIGN KEY'
		  AND (TABLE_NAME = 'events' OR TABLE_NAME LIKE 'wisp\_%')`
	var before int
	te.queryScalar(t, ctx, countFKs, nil, &before)
	if before != 8 {
		t.Fatalf("clone-local FKs before flatten = %d, want 8", before)
	}

	var preHead string
	te.queryScalar(t, ctx, "SELECT HASHOF('HEAD')", nil, &preHead)

	if err := te.store.Flatten(ctx); err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	var after int
	te.queryScalar(t, ctx, countFKs, nil, &after)
	if after != before {
		t.Errorf("clone-local FKs after flatten = %d, want %d (hard reset dropped them)", after, before)
	}

	// History holds the initial commit plus the one flattened snapshot.
	var commits int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_log", nil, &commits)
	if commits != 2 {
		t.Errorf("commits after flatten = %d, want 2 (initial + flattened snapshot)", commits)
	}

	// The tracked snapshot is unchanged: the flattened HEAD carries the same
	// tree the pre-flatten HEAD did.
	var changed int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_diff_summary(?, 'HEAD')", []any{preHead}, &changed)
	if changed != 0 {
		t.Errorf("flattened HEAD differs from the pre-flatten HEAD in %d tables, want the same tree", changed)
	}

	// The kept FKs still enforce: an events row cannot name a missing issue.
	// id and created_at are listed explicitly: embedded Dolt does not apply
	// the schema's expression defaults.
	te.execExpectError(t, ctx, "foreign key",
		"INSERT INTO events (id, issue_id, event_type, actor, created_at) "+
			"VALUES ('evt-check-1', 'no-such-issue', 'flatten-check', 'tester', NOW())")
	// And still cascade: deleting the issue takes its events with it.
	te.exec(t, ctx,
		"INSERT INTO events (id, issue_id, event_type, actor, created_at) "+
			"VALUES ('evt-check-2', ?, 'flatten-check', 'tester', NOW())", lastID)
	te.exec(t, ctx, "DELETE FROM issues WHERE id = ?", lastID)
	var evicted int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", []any{lastID}, &evicted)
	if evicted != 0 {
		t.Errorf("events for deleted issue %s = %d, want 0 (fk_events_issue must still cascade)", lastID, evicted)
	}
}

// execExpectError runs stmt expecting it to fail with an error whose text
// contains want (case-insensitive), the inverse of testEnv.exec.
func (te *testEnv) execExpectError(t *testing.T, ctx context.Context, want, stmt string, args ...any) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	_, err = db.ExecContext(ctx, stmt, args...)
	if err == nil {
		t.Fatalf("exec %q succeeded, want an error containing %q", stmt, want)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("exec %q error = %v, want it to contain %q", stmt, err, want)
	}
}
