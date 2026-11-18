//go:build cgo

package embeddeddolt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// flattenCloneLocalFKs are the foreign keys a migrated store puts on its
// clone-local (dolt_ignored) tables, as table.constraint. The doctor's
// CloneLocalFKs spec lists the same eight and guards them against drift.
var flattenCloneLocalFKs = []string{
	"events.fk_events_issue",
	"wisp_child_counters.fk_wisp_child_counters_parent",
	"wisp_comments.fk_wisp_comments_issue",
	"wisp_dependencies.fk_wisp_dep_issue",
	"wisp_dependencies.fk_wisp_dep_issue_target",
	"wisp_dependencies.fk_wisp_dep_wisp_target",
	"wisp_events.fk_wisp_events_issue",
	"wisp_labels.fk_wisp_labels_issue",
}

// cloneLocalFKNames returns the foreign keys on clone-local tables, sorted.
func (te *testEnv) cloneLocalFKNames(t *testing.T) string {
	t.Helper()
	var names string
	te.queryScalar(t, t.Context(), `SELECT COALESCE(GROUP_CONCAT(CONCAT(TABLE_NAME, '.', CONSTRAINT_NAME) ORDER BY TABLE_NAME, CONSTRAINT_NAME SEPARATOR ','), '')
		FROM information_schema.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_TYPE = 'FOREIGN KEY'
		  AND (TABLE_NAME = 'events' OR TABLE_NAME LIKE 'wisp\_%')`, nil, &names)
	return names
}

// execErr runs stmt on a short-lived raw connection and returns its error.
func (te *testEnv) execErr(ctx context.Context, stmt string) error {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = db.ExecContext(ctx, stmt)
	return err
}

// TestFlattenKeepsCloneLocalFKs: Flatten used to end in DOLT_RESET('--hard'),
// which dropped the foreign keys of clone-local (dolt_ignored) tables
// (bd-7bpkd). A clean flatten keeps the tracked snapshot, leaves the initial
// commit plus one snapshot commit, and every clone-local FK still enforces
// and cascades.
func TestFlattenKeepsCloneLocalFKs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "fk")

	for _, title := range []string{"one", "two", "three"} {
		issue := &types.Issue{ID: "fk-" + title, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", title, err)
		}
	}
	wisp := &types.Issue{ID: "fk-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := te.store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	te.exec(t, ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES ('fk-wisp', 'keep')")
	te.exec(t, ctx, "CALL DOLT_COMMIT('-Am', 'test: extra commit', '--allow-empty')")

	want := strings.Join(flattenCloneLocalFKs, ",")
	if got := te.cloneLocalFKNames(t); got != want {
		t.Fatalf("clone-local FKs before flatten = %q, want %q", got, want)
	}
	var head string
	te.queryScalar(t, ctx, "SELECT HASHOF('HEAD')", nil, &head)

	if err := te.store.Flatten(ctx); err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	if got := te.cloneLocalFKNames(t); got != want {
		t.Errorf("clone-local FKs after flatten = %q, want %q", got, want)
	}

	var commits int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_log", nil, &commits)
	var top string
	te.queryScalar(t, ctx, "SELECT message FROM dolt_log LIMIT 1", nil, &top)
	if commits != 2 || top != "flatten: squash all history into single commit" {
		t.Errorf("history after flatten = %d commits topped by %q, want the initial commit plus the flattened snapshot", commits, top)
	}
	var changed int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_diff_summary(?, 'HEAD')", []any{head}, &changed)
	if changed != 0 {
		t.Errorf("flattened HEAD differs from the pre-flatten HEAD in %d table(s), want the same snapshot", changed)
	}

	// Enforcing: a child row naming no parent is refused.
	for _, insert := range []string{
		"INSERT INTO events (id, issue_id, event_type, actor) VALUES ('11111111-1111-1111-1111-111111111111', 'fk-no-such-issue', 'created', 'tester')",
		"INSERT INTO wisp_labels (issue_id, label) VALUES ('fk-no-such-wisp', 'orphan')",
	} {
		if err := te.execErr(ctx, insert); err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
			t.Errorf("%s after flatten: err = %v, want a foreign key violation", insert, err)
		}
	}

	// Cascading: deleting a parent removes its clone-local children.
	var labels, events int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'fk-wisp'", nil, &labels)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM events WHERE issue_id = 'fk-one'", nil, &events)
	if labels == 0 || events == 0 {
		t.Fatalf("before the cascade check: %d wisp_labels and %d events rows, want both present", labels, events)
	}
	te.exec(t, ctx, "DELETE FROM wisps WHERE id = 'fk-wisp'")
	te.exec(t, ctx, "DELETE FROM issues WHERE id = 'fk-one'")
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'fk-wisp'", nil, &labels)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM events WHERE issue_id = 'fk-one'", nil, &events)
	if labels != 0 || events != 0 {
		t.Errorf("after deleting the parents: %d wisp_labels and %d events rows left, want the cascade to remove them", labels, events)
	}
}
