//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestCommitAllOutOfBandChangeAndHonestNoOp covers the embedded half of the
// explicit-commit fix. Embedded Commit already stages everything via
// DOLT_COMMIT('-Am'), so the staging gap is server-mode-only; what CommitAll
// adds here is the honest report: (true, nil) exactly when a commit was
// created, (false, nil) when there was nothing to commit — so `bd vc commit`
// can print "Nothing to commit" instead of "Created commit <existing HEAD>".
func TestCommitAllOutOfBandChangeAndHonestNoOp(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "cat")

	// Flush anything setup left uncommitted, then pin the honest no-op.
	if _, err := te.store.CommitAll(ctx, "test: flush baseline"); err != nil {
		t.Fatalf("baseline CommitAll: %v", err)
	}
	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	committed, err := te.store.CommitAll(ctx, "test: nothing to commit")
	if err != nil {
		t.Fatalf("CommitAll on clean working set: %v", err)
	}
	if committed {
		t.Fatalf("CommitAll reported a commit on a clean working set")
	}
	headAfter, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("CommitAll reported committed=false but HEAD advanced: %s -> %s", headBefore, headAfter)
	}

	// Out-of-band write: a config row inserted through a raw SQL connection,
	// bypassing the store's transaction tracking entirely.
	te.exec(t, ctx, "INSERT INTO config (`key`, value) VALUES ('test.oob-commit-all', 'v1')")

	committed, err = te.store.CommitAll(ctx, "test: sweep out-of-band config")
	if err != nil {
		t.Fatalf("CommitAll with out-of-band config change: %v", err)
	}
	if !committed {
		t.Fatalf("CommitAll reported nothing to commit while config was dirty")
	}
	var dirty int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'", nil, &dirty)
	if dirty != 0 {
		t.Fatalf("CommitAll left config dirty (%d dolt_status rows)", dirty)
	}
	headSwept, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headSwept == headAfter {
		t.Fatalf("CommitAll reported committed=true but HEAD did not advance from %s", headAfter)
	}
}

// TestCommitAllReportsTrackedTableSkippedByIgnorePattern is the GH#5433
// regression test. An enabled dolt_ignore pattern covering a TRACKED table
// (here a user's 'issues' row) makes Dolt's '-Am' staging skip that table
// while its working-set delta stays: dolt_status still lists it as modified,
// the commit answers "nothing to commit" or commits only the other dirty
// tables, and at base CommitAll answered (false, nil) for the whole state —
// the silent stall the reporter hit three times, with push still reporting
// success. The store must refuse that answer with an error naming the table.
func TestCommitAllReportsTrackedTableSkippedByIgnorePattern(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "ntc")

	// A tracked issue at HEAD, so the table's later delta is a modification
	// of a tracked table, not a new table Dolt would hide from dolt_status.
	te.exec(t, ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('ntc-issue-1', 'tracked at head', '', '', '', '', 'open', 2, 'task')")
	if committed, err := te.store.CommitAll(ctx, "test: create tracked issue"); err != nil || !committed {
		t.Fatalf("baseline CommitAll: committed=%v err=%v", committed, err)
	}

	// The stall fixture: an operator's ignore pattern covering the tracked
	// table, itself committed so it is active for staging.
	te.exec(t, ctx, "INSERT INTO dolt_ignore (pattern, ignored) VALUES ('issues', true)")
	if _, err := te.store.CommitAll(ctx, "test: register ignore pattern"); err != nil {
		t.Fatalf("CommitAll committing the ignore pattern: %v", err)
	}

	// A write that lands in the working set but, covered by the pattern,
	// never stages. It must be visible in dolt_status: the fixture must be
	// the reported stall before the assertion on it means anything.
	te.exec(t, ctx, "UPDATE issues SET title = 'modified after pattern' WHERE id = 'ntc-issue-1'")
	var modified int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'", nil, &modified)
	if modified == 0 {
		t.Fatalf("fixture did not reproduce the stall: dolt_status does not list issues as changed (%d rows)", modified)
	}

	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}

	committed, err := te.store.CommitAll(ctx, "test: commit with ignored tracked change")
	if err == nil {
		t.Fatalf("CommitAll accepted the stall: committed=%v err=nil, want an error reporting the skipped tracked change", committed)
	}
	// The refusal is the typed residual finding: nothing landed (DOLT_COMMIT
	// reported nothing to commit), the tracked table is named, and the
	// operator's pattern is offered as a suggested cause.
	var residual *storage.UncommittedChangesError
	if !errors.As(err, &residual) {
		t.Fatalf("CommitAll error = %v, want *storage.UncommittedChangesError", err)
	}
	if residual.Committed {
		t.Fatal("residual error says committed=true; DOLT_COMMIT reported nothing to commit for the pattern-covered store")
	}
	if len(residual.Tables) != 1 || residual.Tables[0] != "issues" {
		t.Fatalf("residual tables = %v, want exactly [issues]", residual.Tables)
	}
	if len(residual.Patterns) != 1 || residual.Patterns[0] != "issues" {
		t.Fatalf("residual patterns = %v, want exactly the noncanonical pattern [issues]", residual.Patterns)
	}
	if msg := residual.Error(); !strings.Contains(msg, "issues") ||
		!strings.Contains(msg, "these changes are not in HEAD and push will not send them") {
		t.Fatalf("residual error message lacks the table or the push warning: %q", msg)
	}

	// The refusal preserves the working set and HEAD: the write stays saved,
	// and nothing was published.
	headAfter, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("HEAD moved across the refused commit: %s -> %s", headBefore, headAfter)
	}
	var title string
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = 'ntc-issue-1'", nil, &title)
	if title != "modified after pattern" {
		t.Fatalf("working-set row lost across the refused commit: title = %q", title)
	}
}
