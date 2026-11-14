package dolt

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssue_ReplayRemintsAutoMintedID (bd-t4h0n7, PR #6754): a server
// single-create whose first attempt mints an id, then loses its commit to a
// writer that landed a row under that exact id, must re-mint DISTINCTLY on
// the replay — the rolled-back attempt's id is not the caller's, and the
// writer's row is not one it may overwrite or be refused against. Both rows
// survive with their own content and one created event each.
func TestCreateIssue_ReplayRemintsAutoMintedID(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode=counter): %v", err)
	}

	// Pause the first attempt after its body succeeded, before its commit.
	firstAttempt := make(chan struct{})
	release := make(chan struct{})
	var attempts atomic.Int32
	store.createAttemptHook = func() {
		if attempts.Add(1) == 1 {
			close(firstAttempt)
			<-release
		}
	}

	issue := &types.Issue{Title: "replayed create", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.CreateIssue(ctx, issue, "tester")
	}()

	// settle releases the paused attempt (if it is still paused) and waits for
	// the create to return, so an early failure never tears the store down
	// under a live create goroutine.
	settle := func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-errCh:
		case <-time.After(60 * time.Second):
		}
	}

	<-firstAttempt
	minted := issue.ID
	if minted == "" {
		settle()
		t.Fatal("first attempt minted no id before the hook fired")
	}

	// The writer: a row under the minted id plus its created event, committed
	// on the store's other connection while the first attempt sits uncommitted.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, ?, '', '', '')",
		minted, "conflicting writer", "writer row"); err != nil {
		settle()
		t.Fatalf("writer insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO events (id, issue_id, event_type, actor) VALUES (UUID(), ?, 'created', 'writer')", minted); err != nil {
		settle()
		t.Fatalf("writer event: %v", err)
	}

	close(release) // the first attempt commits into the conflict and replays

	err := <-errCh
	if err != nil {
		t.Fatalf("CreateIssue after replay: %v", err)
	}

	if issue.ID == minted {
		t.Fatalf("replayed create kept the rolled-back minted id %q; it must re-mint distinctly", minted)
	}
	if issue.ID != "test-2" {
		t.Fatalf("issue.ID = %q, want test-2, re-minted past the writer's %q", issue.ID, minted)
	}

	// The writer's row kept its content; the replayed create landed its own.
	writer, err := store.GetIssue(ctx, minted)
	if err != nil {
		t.Fatalf("GetIssue(writer %s): %v", minted, err)
	}
	if writer.Title != "conflicting writer" {
		t.Errorf("writer row title = %q, want %q", writer.Title, "conflicting writer")
	}
	replayed, err := store.GetIssue(ctx, "test-2")
	if err != nil {
		t.Fatalf("GetIssue(test-2): %v", err)
	}
	if replayed.Title != "replayed create" {
		t.Errorf("replayed row title = %q, want %q", replayed.Title, "replayed create")
	}

	// One created event each, and one row per id.
	for _, id := range []string{minted, "test-2"} {
		var events int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'created'", id).Scan(&events); err != nil {
			t.Fatalf("count created events for %s: %v", id, err)
		}
		if events != 1 {
			t.Errorf("created events for %s = %d, want 1", id, events)
		}
		var rows int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&rows); err != nil {
			t.Fatalf("count rows for %s: %v", id, err)
		}
		if rows != 1 {
			t.Errorf("issue rows for %s = %d, want 1", id, rows)
		}
	}
}
