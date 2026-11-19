package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Tests for GH#7289: a withRetryTx replay of a DoltStore create must re-mint
// the identifiers the rolled-back attempt generated (issue ID, defaulted
// dependency source, content-derived comment ID) instead of reading them as
// caller-supplied and upserting over the concurrent winner's row.
//
// Each test forces the real interleaving rather than injecting a synthetic
// conflict: the losing create's context carries a pre-commit hook
// (preCommitHookKey) that, on the loser's first attempt only, synchronously
// creates the winner on the store's second pooled connection with the same
// hash inputs (title, description, actor, CreatedAt) while the loser's
// transaction is still open. The winner commits the loser's first candidate
// ID; the loser's commit then conflicts and withRetryTx replays its body.
// forcedReplay checks both halves of that premise — the winner really got the
// loser's first ID, and the loser's body really ran again — so a run that did
// not exercise the replay fails instead of passing vacuously.

// replayFixedCreatedAt is shared by loser and winner: CreatedAt is a hash
// input, so equal values (with equal title, description and actor) make both
// transactions generate the same first candidate.
var replayFixedCreatedAt = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)

const (
	replayActor       = "same-actor"
	replayDescription = "same hash inputs"
)

// forcedReplay records one forced loser/winner interleaving.
type forcedReplay struct {
	attempts     int
	loserFirstID string
	winnerID     string
	winnerErr    error
}

// withForcedReplay returns a context for the losing create. loserID reads the
// loser's generated ID as its open transaction left it; winner runs the
// conflicting create on winnerCtx (which carries no hook) and returns the ID
// it committed.
func withForcedReplay(loserCtx, winnerCtx context.Context, loserID func() string, winner func(context.Context) (string, error)) (context.Context, *forcedReplay) {
	f := &forcedReplay{}
	hook := func(context.Context) error {
		f.attempts++
		if f.attempts > 1 {
			return nil
		}
		f.loserFirstID = loserID()
		f.winnerID, f.winnerErr = winner(winnerCtx)
		return nil
	}
	return context.WithValue(loserCtx, preCommitHookKey{}, hook), f
}

// requireReplayed fails the test unless the winner committed the loser's
// first generated ID and the loser's transaction body ran at least twice.
func (f *forcedReplay) requireReplayed(t *testing.T) {
	t.Helper()
	if f.winnerErr != nil {
		t.Fatalf("winner create inside the loser's open transaction: %v", f.winnerErr)
	}
	if f.loserFirstID == "" || f.winnerID != f.loserFirstID {
		t.Fatalf("premise: winner committed %q, loser's first attempt generated %q; want the same ID so the loser's commit conflicts", f.winnerID, f.loserFirstID)
	}
	if f.attempts < 2 {
		t.Fatalf("premise: loser's transaction body ran %d time(s), want >= 2 (the winner's commit must force a replay)", f.attempts)
	}
}

func replayIssue(title, notes string) *types.Issue {
	return &types.Issue{
		Title:       title,
		Description: replayDescription,
		Notes:       notes,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   replayFixedCreatedAt,
	}
}

// replayPlane names the tables of one storage plane.
type replayPlane struct {
	issues, events, comments, deps string
}

var (
	durablePlane = replayPlane{issues: "issues", events: "events", comments: "comments", deps: "dependencies"}
	wispPlane    = replayPlane{issues: "wisps", events: "wisp_events", comments: "wisp_comments", deps: "wisp_dependencies"}
)

func replayQueryInt(t *testing.T, ctx context.Context, store *DoltStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s %v: %v", query, args, err)
	}
	return n
}

// requireReplayRow asserts id is a row of plane p holding notes, with
// exactly one created event.
func requireReplayRow(t *testing.T, ctx context.Context, store *DoltStore, p replayPlane, id, wantNotes string) {
	t.Helper()
	var notes string
	//nolint:gosec // G201: table is a test-chosen constant
	err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT notes FROM %s WHERE id = ?", p.issues), id).Scan(&notes)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("%s row %s missing", p.issues, id)
	}
	if err != nil {
		t.Fatalf("read %s row %s: %v", p.issues, id, err)
	}
	if notes != wantNotes {
		t.Fatalf("%s row %s notes = %q, want %q (the other writer's content overwrote it)", p.issues, id, notes, wantNotes)
	}
	//nolint:gosec // G201: table is a test-chosen constant
	if n := replayQueryInt(t, ctx, store, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ? AND event_type = ?", p.events), id, string(types.EventCreated)); n != 1 {
		t.Fatalf("%s created events for %s = %d, want 1", p.events, id, n)
	}
}

type replayComment struct{ id, author, text string }

func replayComments(t *testing.T, ctx context.Context, store *DoltStore, p replayPlane, issueID string) []replayComment {
	t.Helper()
	//nolint:gosec // G201: table is a test-chosen constant
	rows, err := store.db.QueryContext(ctx, fmt.Sprintf("SELECT id, author, text FROM %s WHERE issue_id = ? ORDER BY id", p.comments), issueID)
	if err != nil {
		t.Fatalf("read %s for %s: %v", p.comments, issueID, err)
	}
	defer rows.Close()
	var out []replayComment
	for rows.Next() {
		var c replayComment
		if err := rows.Scan(&c.id, &c.author, &c.text); err != nil {
			t.Fatalf("scan %s: %v", p.comments, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", p.comments, err)
	}
	return out
}

// requireOnlyComment asserts issueID has exactly one comment in plane p, with
// the given text and (when wantID is set) that id.
func requireOnlyComment(t *testing.T, ctx context.Context, store *DoltStore, p replayPlane, issueID, wantID, wantText string) {
	t.Helper()
	got := replayComments(t, ctx, store, p, issueID)
	if len(got) != 1 {
		t.Fatalf("%s for %s = %+v, want exactly one comment %q", p.comments, issueID, got, wantText)
	}
	if got[0].text != wantText {
		t.Fatalf("%s for %s text = %q, want %q", p.comments, issueID, got[0].text, wantText)
	}
	if wantID != "" && got[0].id != wantID {
		t.Fatalf("%s for %s id = %q, want %q (the caller's struct names a comment the issue does not own)", p.comments, issueID, got[0].id, wantID)
	}
}

// replayDepTargets lists the targets of issueID's edges in p, whichever of
// the split target columns (issue, wisp, external) holds each one.
func replayDepTargets(t *testing.T, ctx context.Context, store *DoltStore, p replayPlane, issueID string) []string {
	t.Helper()
	//nolint:gosec // G201: table is a test-chosen constant
	rows, err := store.db.QueryContext(ctx, fmt.Sprintf("SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS target FROM %s WHERE issue_id = ? ORDER BY target", p.deps), issueID)
	if err != nil {
		t.Fatalf("read %s for %s: %v", p.deps, issueID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatalf("scan %s: %v", p.deps, err)
		}
		out = append(out, target)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", p.deps, err)
	}
	return out
}

func requireDepTargets(t *testing.T, ctx context.Context, store *DoltStore, p replayPlane, issueID string, want ...string) {
	t.Helper()
	got := replayDepTargets(t, ctx, store, p, issueID)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s edges from %s = %v, want %v", p.deps, issueID, got, want)
	}
}

// createReplayWinner creates the conflicting winner: an ID-less issue with
// the shared hash inputs, its own notes and one comment of its own.
func createReplayWinner(store *DoltStore, title string) (*types.Issue, func(context.Context) (string, error)) {
	w := replayIssue(title, "winner")
	w.Comments = []*types.Comment{{Author: "winner", Text: "winner comment", CreatedAt: replayFixedCreatedAt}}
	return w, func(ctx context.Context) (string, error) {
		err := store.CreateIssue(ctx, w, replayActor)
		return w.ID, err
	}
}

// TestCreateIssueReplayRemintsGeneratedIdentifiers (AC-1): a single
// CreateIssue whose generated ID a concurrent winner commits first must, on
// replay, mint a fresh ID; both rows keep their own content and each has one
// created event.
func TestCreateIssueReplayRemintsGeneratedIdentifiers(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	run := func(t *testing.T, title string, withComments bool) {
		t.Helper()
		loser := replayIssue(title, "loser")
		winner := replayIssue(title, "winner")
		if withComments {
			// Equal author/text/timestamp: the comment's content-derived id
			// differs only by its owning issue, so a replay that kept the
			// first attempt's comment id would name the winner's comment.
			loser.Comments = []*types.Comment{{Author: "same", Text: "same comment", CreatedAt: replayFixedCreatedAt}}
			winner.Comments = []*types.Comment{{Author: "same", Text: "same comment", CreatedAt: replayFixedCreatedAt}}
		}
		loserCtx, f := withForcedReplay(ctx, ctx, func() string { return loser.ID }, func(ctx context.Context) (string, error) {
			err := store.CreateIssue(ctx, winner, replayActor)
			return winner.ID, err
		})
		if err := store.CreateIssue(loserCtx, loser, replayActor); err != nil {
			t.Fatalf("loser CreateIssue: %v", err)
		}
		f.requireReplayed(t)

		if loser.ID == winner.ID {
			t.Fatalf("both creates returned nil with the same ID %q: the replay reused the rolled-back attempt's ID and upserted over the winner", loser.ID)
		}
		requireReplayRow(t, ctx, store, durablePlane, winner.ID, "winner")
		requireReplayRow(t, ctx, store, durablePlane, loser.ID, "loser")
		if n := replayQueryInt(t, ctx, store, "SELECT COUNT(*) FROM issues WHERE title = ?", title); n != 2 {
			t.Fatalf("issues titled %q = %d, want 2", title, n)
		}
		if withComments {
			requireOnlyComment(t, ctx, store, durablePlane, winner.ID, winner.Comments[0].ID, "same comment")
			requireOnlyComment(t, ctx, store, durablePlane, loser.ID, loser.Comments[0].ID, "same comment")
			if loser.Comments[0].ID == winner.Comments[0].ID {
				t.Fatalf("loser's comment ID %q equals the winner's: the replay kept the first attempt's derived comment ID", loser.Comments[0].ID)
			}
		}
	}

	t.Run("hash", func(t *testing.T) {
		run(t, "replay-single-hash", false)
	})
	t.Run("comment", func(t *testing.T) {
		run(t, "replay-single-comment", true)
	})
	t.Run("serial_control", func(t *testing.T) {
		// Without an overlap, equal hash inputs already get distinct IDs: the
		// second create's generation sees the first's committed row.
		a := replayIssue("replay-serial", "a")
		b := replayIssue("replay-serial", "b")
		if err := store.CreateIssue(ctx, a, replayActor); err != nil {
			t.Fatalf("first serial create: %v", err)
		}
		if err := store.CreateIssue(ctx, b, replayActor); err != nil {
			t.Fatalf("second serial create: %v", err)
		}
		if a.ID == b.ID {
			t.Fatalf("serial creates with equal hash inputs share ID %q", a.ID)
		}
	})
	// Last: it leaves the store in counter mode.
	t.Run("counter", func(t *testing.T) {
		if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("set issue_id_mode: %v", err)
		}
		seed := replayIssue("replay-counter-seed", "seed")
		if err := store.CreateIssue(ctx, seed, replayActor); err != nil {
			t.Fatalf("seed create: %v", err)
		}
		run(t, "replay-single-counter", false)
	})
}

// TestCreateBatchReplayRemintsGeneratedIdentifiers (AC-2): a replayed batch
// re-mints its issue ID and re-derives the dependency source and comment ID
// from it, leaving the winner's row and records unchanged.
func TestCreateBatchReplayRemintsGeneratedIdentifiers(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	target := &types.Issue{Title: "replay-batch-target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, target, "tester"); err != nil {
		t.Fatalf("create dependency target: %v", err)
	}

	cases := []struct {
		name   string
		create func(ctx context.Context, issues []*types.Issue) error
		extra  bool // add a second, non-colliding generated issue (exercises the batch cache)
	}{
		{name: "CreateIssues", create: func(ctx context.Context, issues []*types.Issue) error {
			return store.CreateIssues(ctx, issues, replayActor)
		}},
		{name: "CreateIssuesWithFullOptions", create: func(ctx context.Context, issues []*types.Issue) error {
			return store.CreateIssuesWithFullOptions(ctx, issues, replayActor, storage.BatchCreateOptions{})
		}},
		{name: "cached_batch", extra: true, create: func(ctx context.Context, issues []*types.Issue) error {
			return store.CreateIssues(ctx, issues, replayActor)
		}},
		{name: "CreateOnly", create: func(ctx context.Context, issues []*types.Issue) error {
			return store.CreateIssuesWithFullOptions(ctx, issues, replayActor, storage.BatchCreateOptions{CreateOnly: true})
		}},
		{name: "ConflictSkip", create: func(ctx context.Context, issues []*types.Issue) error {
			return store.CreateIssuesWithFullOptions(ctx, issues, replayActor, storage.BatchCreateOptions{ConflictSkip: true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title := "replay-batch-" + tc.name
			loser := replayIssue(title, "loser")
			loser.Dependencies = []*types.Dependency{{DependsOnID: target.ID, Type: types.DepBlocks}}
			loser.Comments = []*types.Comment{{Author: "loser", Text: "loser comment", CreatedAt: replayFixedCreatedAt}}
			batch := []*types.Issue{loser}
			var other *types.Issue
			if tc.extra {
				other = replayIssue(title+"-other", "other")
				batch = append(batch, other)
			}
			winner, runWinner := createReplayWinner(store, title)

			loserCtx, f := withForcedReplay(ctx, ctx, func() string { return loser.ID }, runWinner)
			if err := tc.create(loserCtx, batch); err != nil {
				t.Fatalf("loser batch create: %v", err)
			}
			f.requireReplayed(t)

			if loser.ID == winner.ID {
				t.Fatalf("batch replay reused the winner's ID %q", loser.ID)
			}
			requireReplayRow(t, ctx, store, durablePlane, loser.ID, "loser")
			if got := loser.Dependencies[0].IssueID; got != loser.ID {
				t.Fatalf("loser dependency IssueID = %q, want the fresh issue ID %q", got, loser.ID)
			}
			requireDepTargets(t, ctx, store, durablePlane, loser.ID, target.ID)
			requireOnlyComment(t, ctx, store, durablePlane, loser.ID, loser.Comments[0].ID, "loser comment")

			requireReplayRow(t, ctx, store, durablePlane, winner.ID, "winner")
			requireDepTargets(t, ctx, store, durablePlane, winner.ID)
			requireOnlyComment(t, ctx, store, durablePlane, winner.ID, winner.Comments[0].ID, "winner comment")

			if other != nil {
				if other.ID == "" || other.ID == loser.ID || other.ID == winner.ID {
					t.Fatalf("second batch issue ID = %q (loser %q, winner %q), want its own", other.ID, loser.ID, winner.ID)
				}
				requireReplayRow(t, ctx, store, durablePlane, other.ID, "other")
			}
		})
	}
}
