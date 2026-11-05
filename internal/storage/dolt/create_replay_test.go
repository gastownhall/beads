package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/types"
)

// A create that loses Dolt's commit-time merge to a concurrent writer is
// rolled back by the server and replayed by withRetryTx. These tests prove the
// replay starts from the caller's request: it mints its own ID and derives its
// inline edges and comments from that ID, rather than re-using what the
// rolled-back attempt wrote back into the caller's issue.

// TestConcurrentCounterModeCreatesNeverShareAnID: in counter mode two writers
// that overlap mint the same number, so one of them loses at commit and is
// replayed. A replay that re-used the rolled-back attempt's ID took the
// explicit-ID upsert path and overwrote the winner's issue, and both callers
// were told the same ID.
func TestConcurrentCounterModeCreatesNeverShareAnID(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := base.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set counter mode: %v", err)
	}
	const writers, rounds = 4, 3
	stores := []*DoltStore{base}
	for len(stores) < writers {
		stores = append(stores, openPeerStore(t, ctx, base))
	}

	var mu sync.Mutex
	titleByID := map[string]string{}
	errs := make(chan error, writers*rounds*3)
	record := func(issue *types.Issue) {
		mu.Lock()
		defer mu.Unlock()
		if prev, dup := titleByID[issue.ID]; dup {
			errs <- fmt.Errorf("id %s handed to both %q and %q", issue.ID, prev, issue.Title)
			return
		}
		titleByID[issue.ID] = issue.Title
	}
	newIssue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for r := range rounds {
				single := newIssue(fmt.Sprintf("single %d-%d", w, r))
				if err := s.CreateIssue(ctx, single, "writer"); err != nil {
					errs <- fmt.Errorf("create %q: %w", single.Title, err)
				} else {
					record(single)
				}
				batch := []*types.Issue{newIssue(fmt.Sprintf("batch %d-%d-a", w, r)), newIssue(fmt.Sprintf("batch %d-%d-b", w, r))}
				if err := s.CreateIssues(ctx, batch, "writer"); err != nil {
					errs <- fmt.Errorf("create batch %d-%d: %w", w, r, err)
				} else {
					record(batch[0])
					record(batch[1])
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for id, title := range titleByID {
		got, err := base.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("read %s: %v", id, err)
			continue
		}
		if got.Title != title {
			t.Errorf("issue %s holds %q, want %q: a replayed create overwrote it", id, got.Title, title)
		}
	}
	if want := writers * rounds * 3; len(titleByID) != want && !t.Failed() {
		t.Fatalf("distinct issues = %d, want %d", len(titleByID), want)
	}
}

// replayIDModes are the two ID modes a replayed create must restart in: in
// counter mode the competitor mints the very number the loser's rolled-back
// attempt had; in hash mode the replay mints a fresh ID, and the events journal
// makes the two overlapping writers collide.
var replayIDModes = []struct {
	name        string
	counterMode bool
}{
	{name: "counter mode", counterMode: true},
	{name: "hash mode", counterMode: false},
}

func setUpReplayIDMode(t *testing.T, ctx context.Context, base *DoltStore, counterMode bool) {
	t.Helper()
	if counterMode {
		if err := base.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("set counter mode: %v", err)
		}
		return
	}
	base.SetEventsJournalEnabled(true)
}

// TestReplayedBatchCreateKeepsInlineEdgeOnItsOwnIssue holds a batch create
// open (through the skipped-edge callback it runs inside its transaction) until
// a competitor has committed, so the batch loses at commit and is replayed. Its
// inline edge named no source, so the create filled the source in from the
// minted ID: the replay must fill it in again from its own ID.
func TestReplayedBatchCreateKeepsInlineEdgeOnItsOwnIssue(t *testing.T) {
	for _, mode := range replayIDModes {
		t.Run(mode.name, func(t *testing.T) {
			base, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := concurrentTestContext(t)
			defer cancel()
			setUpReplayIDMode(t, ctx, base, mode.counterMode)
			peer := openPeerStore(t, ctx, base)
			peer.SetEventsJournalEnabled(!mode.counterMode)
			if err := base.CreateIssue(ctx, &types.Issue{ID: "test-anchor", Title: "anchor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
				t.Fatalf("seed anchor: %v", err)
			}

			prepared, release := make(chan struct{}), make(chan struct{})
			var attempts atomic.Int32
			opts := storage.BatchCreateOptions{
				// Runs inside the create transaction, after the anchor edge is
				// written: hold the first attempt open until the competitor
				// has committed.
				OnSkippedDependency: func(_, _, _ string) {
					if attempts.Add(1) == 1 {
						close(prepared)
						<-release
					}
				},
			}
			loser := &types.Issue{
				Title: "loser", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				Dependencies: []*types.Dependency{
					{DependsOnID: "test-anchor", Type: types.DepBlocks},
					{DependsOnID: "test-missing", Type: types.DepRelated},
				},
			}
			done := make(chan error, 1)
			go func() { done <- base.CreateIssuesWithFullOptions(ctx, []*types.Issue{loser}, "loser", opts) }()
			waitForTransactionPrepared(t, ctx, prepared, "loser")
			competitor := &types.Issue{Title: "competitor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := peer.CreateIssue(ctx, competitor, "competitor"); err != nil {
				close(release)
				t.Fatalf("competitor create: %v", err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("loser create was not replayed to success: %v", err)
			}
			if n := attempts.Load(); n < 2 {
				t.Fatalf("loser create ran %d attempt(s), want a replay: the competitor's commit did not conflict", n)
			}
			assertCompetitorIntact(t, ctx, base, loser, competitor)
			if got := edgeSourcesInto(t, ctx, base, "test-anchor"); len(got) != 1 || got[0] != loser.ID {
				t.Fatalf("edges into test-anchor come from %v, want exactly [%s] (the loser's own issue); competitor is %s", got, loser.ID, competitor.ID)
			}
		})
	}
}

// TestReplayedCreateDerivesInlineCommentIDFromItsOwnIssue holds a single
// create's COMMIT at a proxy until a competitor has committed, so the create
// loses and is replayed. Its inline comment carried no ID, so the create
// derived one from the minted issue ID: the replay must derive it again from
// its own.
func TestReplayedCreateDerivesInlineCommentIDFromItsOwnIssue(t *testing.T) {
	for _, mode := range replayIDModes {
		t.Run(mode.name, func(t *testing.T) {
			base, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := concurrentTestContext(t)
			defer cancel()
			setUpReplayIDMode(t, ctx, base, mode.counterMode)
			proxy := startSQLWireProxy(t)
			via := openStoreOnPort(t, ctx, base, proxy.port())
			via.SetEventsJournalEnabled(!mode.counterMode)

			loser := &types.Issue{
				Title: "loser", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				Comments: []*types.Comment{{Author: "loser", Text: "filed with the issue"}},
			}
			mark := proxy.mark()
			held, release := proxy.holdNext(isCommitStatement)
			done := make(chan error, 1)
			go func() { done <- via.CreateIssue(ctx, loser, "loser") }()
			select {
			case <-held:
			case err := <-done:
				t.Fatalf("loser create finished before its COMMIT was held: %v", err)
			case <-ctx.Done():
				t.Fatalf("loser create never reached COMMIT: %v", ctx.Err())
			}
			competitor := &types.Issue{Title: "competitor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := base.CreateIssue(ctx, competitor, "competitor"); err != nil {
				release()
				t.Fatalf("competitor create: %v", err)
			}
			release()
			if err := <-done; err != nil {
				t.Fatalf("loser create was not replayed to success: %v", err)
			}
			insertsComment := func(q string) bool { return strings.HasPrefix(q, "INSERT INTO comments") }
			if n := proxy.statementsSince(mark, insertsComment); n != 2 {
				t.Fatalf("loser create wrote its comment in %d attempt(s), want 2 (one lost to the competitor, one replay)", n)
			}
			assertCompetitorIntact(t, ctx, base, loser, competitor)
			assertCommentDerivedFromOwnIssue(t, ctx, base, loser.ID)
		})
	}
}

func assertCompetitorIntact(t *testing.T, ctx context.Context, base *DoltStore, loser, competitor *types.Issue) {
	t.Helper()
	if loser.ID == competitor.ID {
		t.Fatalf("loser and competitor were both handed %s", loser.ID)
	}
	got, err := base.GetIssue(ctx, competitor.ID)
	if err != nil {
		t.Fatalf("read competitor %s: %v", competitor.ID, err)
	}
	if got.Title != "competitor" {
		t.Fatalf("competitor %s holds %q: the replayed create overwrote it", competitor.ID, got.Title)
	}
}

func edgeSourcesInto(t *testing.T, ctx context.Context, s *DoltStore, target string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, "SELECT issue_id FROM dependencies WHERE depends_on_issue_id = ?", target)
	if err != nil {
		t.Fatalf("query edges into %s: %v", target, err)
	}
	defer rows.Close()
	var sources []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan edge source: %v", err)
		}
		sources = append(sources, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read edges into %s: %v", target, err)
	}
	sort.Strings(sources)
	return sources
}

// assertCommentDerivedFromOwnIssue checks issueID's one comment carries the ID
// derived from issueID itself, not one left over from a rolled-back attempt
// that derived it from that attempt's issue ID.
func assertCommentDerivedFromOwnIssue(t *testing.T, ctx context.Context, s *DoltStore, issueID string) {
	t.Helper()
	comments, err := s.GetIssueComments(ctx, issueID)
	if err != nil {
		t.Fatalf("read comments of %s: %v", issueID, err)
	}
	if len(comments) != 1 {
		t.Fatalf("%s has %d comments, want 1", issueID, len(comments))
	}
	c := comments[0]
	want := rowid.New("comments", 0, rowid.Digest([]sql.NullString{
		{String: issueID, Valid: true}, {String: c.Author, Valid: true},
		{String: c.Text, Valid: true}, {String: issueops.FormatAuxTime(c.CreatedAt), Valid: true},
	}))
	if c.ID != want {
		t.Fatalf("comment on %s has id %s, want %s (derived from its own issue): the replay re-used the rolled-back attempt's comment id", issueID, c.ID, want)
	}
}
