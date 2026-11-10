//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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

// TestCommitAllPartialPublishesCommittableTablesAndReportsResidual is the
// partial-publication half of GH#5433: ignored modified issues PLUS committable
// config dirt. DOLT_COMMIT('-Am') stages and commits the config change, so the
// commit reports success — but the pattern-covered issues delta stays in the
// working set, and at base CommitAll answered (true, nil) for it: push would
// send the config commit and still never the issues. The committed bool must
// keep DOLT_COMMIT's answer (true: partial publication) alongside the typed
// residual error.
func TestCommitAllPartialPublishesCommittableTablesAndReportsResidual(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "ntp")

	te.exec(t, ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('ntp-issue-1', 'tracked at head', '', '', '', '', 'open', 2, 'task')")
	if committed, err := te.store.CommitAll(ctx, "test: create tracked issue"); err != nil || !committed {
		t.Fatalf("baseline CommitAll: committed=%v err=%v", committed, err)
	}
	te.exec(t, ctx, "INSERT INTO dolt_ignore (pattern, ignored) VALUES ('issues', true)")
	if _, err := te.store.CommitAll(ctx, "test: register ignore pattern"); err != nil {
		t.Fatalf("CommitAll committing the ignore pattern: %v", err)
	}

	// The partial state: the ignored issues delta plus a committable config
	// write, together.
	te.exec(t, ctx, "UPDATE issues SET title = 'stranded by pattern' WHERE id = 'ntp-issue-1'")
	te.exec(t, ctx, "INSERT INTO config (`key`, value) VALUES ('test.nt-partial', 'v1')")

	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}

	committed, err := te.store.CommitAll(ctx, "test: partial publication")
	if err == nil {
		t.Fatalf("CommitAll reported the partial publication clean: committed=%v err=nil, want the typed residual error", committed)
	}
	if !committed {
		t.Fatalf("CommitAll committed = false with the partial case, want true (DOLT_COMMIT reported success)")
	}
	var residual *storage.UncommittedChangesError
	if !errors.As(err, &residual) {
		t.Fatalf("CommitAll error = %v, want *storage.UncommittedChangesError", err)
	}
	if !residual.Committed {
		t.Fatal("residual error says committed=false; the partial case published the config change")
	}
	if len(residual.Tables) != 1 || residual.Tables[0] != "issues" {
		t.Fatalf("residual tables = %v, want exactly [issues]", residual.Tables)
	}

	// The committable change reached HEAD: the config value is committed and
	// config is no longer dirty, and HEAD advanced.
	headAfter, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter == headBefore {
		t.Fatalf("HEAD did not advance across the partial publication (was %s)", headBefore)
	}
	var configValue string
	te.queryScalar(t, ctx, "SELECT value FROM config WHERE `key` = 'test.nt-partial'", nil, &configValue)
	if configValue != "v1" {
		t.Fatalf("config value after the partial publication = %q, want v1", configValue)
	}
	var configDirty int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'", nil, &configDirty)
	if configDirty != 0 {
		t.Fatalf("config still dirty after the partial publication (%d dolt_status rows)", configDirty)
	}

	// The ignored delta stayed in the working set, and it is still there
	// after a reopen: durable working-set state, not an in-memory leftover.
	var issuesRows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'", nil, &issuesRows)
	if issuesRows == 0 {
		t.Fatal("issues no longer listed in dolt_status after the partial publication")
	}
	headAtClose := headAfter
	if err := te.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	beadsDir := strings.TrimSuffix(te.dataDir, "/embeddeddolt")
	reopened, err := embeddeddolt.Open(ctx, beadsDir, te.database, "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if headReopened, err := reopened.GetCurrentCommit(ctx); err != nil {
		t.Fatalf("GetCurrentCommit after reopen: %v", err)
	} else if headReopened != headAtClose {
		t.Fatalf("HEAD changed across reopen: %s -> %s", headAtClose, headReopened)
	}
	// The still-ignored delta keeps refusing a full commit on the reopened
	// store: the stall stays loud, not just for the session that hit it.
	if _, err := reopened.CommitAll(ctx, "test: reopen refusal"); err == nil {
		t.Fatal("CommitAll after reopen on the ignored-delta-only working set = nil error, want the residual refusal")
	} else {
		var reopenResidual *storage.UncommittedChangesError
		if !errors.As(err, &reopenResidual) || len(reopenResidual.Tables) != 1 || reopenResidual.Tables[0] != "issues" {
			t.Fatalf("CommitAll after reopen error = %v, want the residual refusal naming issues", err)
		}
		if reopenResidual.Committed {
			t.Fatal("CommitAll after reopen reported committed=true; only the ignored delta is dirty")
		}
	}
	var reopenedTitle string
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = 'ntp-issue-1'", nil, &reopenedTitle)
	if reopenedTitle != "stranded by pattern" {
		t.Fatalf("working-set row lost across reopen: title = %q", reopenedTitle)
	}
}
