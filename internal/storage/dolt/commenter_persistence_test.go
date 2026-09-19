package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file is the persistence tier of the Commenter role: what the shared
// contract in package conformance deliberately leaves out because it is a
// property of the Dolt working set rather than a promised result of the
// operation. It is pinned at this backend only.
//
//   - The commit-message spelling is single-sourced in
//     storageissueops.AddCommentCommitMessage, so one backend pinning it is
//     enough and re-pinning it three times would be duplication.
//   - The staging assertions need a planted dirty working set and a dolt_status
//     read. The working set is not a caller-visible thing on the unit-of-work
//     route, which is where the same line was already drawn for Lifecycle in
//     conformance/issue_operations_staging.go.
//
// Everything else the old commenter_test.go asserted — verbatim text, the
// result mirroring the row, wisp-thread routing, ErrNotFound — is now
// conformance.RunCommenter*, wired at all three backends.

// TestDoltStoreCommenterNamesTheIssueInHistoryAndCommitsTheTable pins the two
// persistence halves of a durable comment: the history entry is named
// "bd: comment <id>", and the comments table is committed rather than left
// dirty in the working set.
func TestDoltStoreCommenterNamesTheIssueInHistoryAndCommitsTheTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-comment-role")

	commenter, err := store.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	if _, err := commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: "test-comment-role",
		Text:    "a durable comment",
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	if !doltHasCommitMessage(ctx, t, store, "bd: comment test-comment-role") {
		t.Error("the comment did not name its issue in history")
	}
	requireCleanTables(ctx, t, store, "comments")
}

// TestDoltStoreCommenterOnAWispSweepsNoPendingRow pins the staging half of the
// ephemeral case, which no data read can see.
//
// It needs a pending row in the durable comments table to be visible at all.
// The wisp tables are dolt-ignored, so a commit that named one is swallowed
// and a commit count alone cannot tell a correctly-staged wisp write from one
// that reached for `comments` — which is exactly what a wrongly resolved plane
// would do while the thread read still passed. A comment on a wisp must
// therefore leave `comments` dirty: it neither wrote to it nor swept someone
// else's pending row into a commit.
func TestDoltStoreCommenterOnAWispSweepsNoPendingRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-comment-neighbor")
	wisp := &types.Issue{ID: "test-comment-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "seed"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, NOW())`,
		"00000000-0000-4000-8000-00000000c0de", "test-comment-neighbor", "someone-else", "pending",
	); err != nil {
		t.Fatalf("stage a pending comment row: %v", err)
	}

	commenter, err := store.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	if _, err := commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "author",
		IssueID: wisp.ID,
		Text:    "on the ephemeral plane",
	}); err != nil {
		t.Fatalf("AddComment on a wisp: %v", err)
	}

	var dirty int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'comments'").Scan(&dirty); err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	if dirty == 0 {
		t.Fatal("comments is clean: the wisp comment staged the durable table and carried a pending row with it")
	}
}
