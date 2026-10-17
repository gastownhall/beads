package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournaledWispWritesSurviveConcurrentWriters drives every wisp write path
// from several stores at once with the events journal on. Each of those writes
// bumps the one bd_events_seq row inside its own transaction, so two writers
// whose transactions overlap collide at commit and the later one is rolled back
// with MySQL 1213 ("serialization failure ... try restarting transaction").
// Every path must replay its transaction instead of surfacing that conflict.
func TestJournaledWispWritesSurviveConcurrentWriters(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const writers, rounds = 4, 3
	stores := []*DoltStore{base}
	for len(stores) < writers {
		stores = append(stores, openPeerStore(t, ctx, base))
	}

	type target struct{ wisp, anchor, doomed, swept string }
	targets := make([][]target, writers)
	for w := range writers {
		for r := range rounds {
			tg := target{
				wisp:   fmt.Sprintf("test-wisp-%d-%d", w, r),
				anchor: fmt.Sprintf("test-anchor-%d-%d", w, r),
				doomed: fmt.Sprintf("test-doomed-%d-%d", w, r),
				swept:  fmt.Sprintf("test-swept-%d-%d", w, r),
			}
			createWisp(t, ctx, base, tg.wisp)
			createPerm(t, ctx, base, tg.anchor)
			createWisp(t, ctx, base, tg.doomed)
			createWisp(t, ctx, base, tg.swept)
			targets[w] = append(targets[w], tg)
		}
	}
	for _, s := range stores {
		s.SetEventsJournalEnabled(true)
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds*11)
	start := make(chan struct{})
	for w, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			actor := fmt.Sprintf("writer-%d", w)
			try := func(op, id string, err error) {
				if err != nil {
					errs <- fmt.Errorf("%s %s %s: %w", actor, op, id, err)
				}
			}
			for r, tg := range targets[w] {
				try("update", tg.wisp, s.UpdateIssue(ctx, tg.wisp, map[string]interface{}{"notes": "noted by " + actor}, actor))
				try("checked update", tg.wisp, s.UpdateIssueChecked(ctx, tg.wisp, map[string]interface{}{"priority": 1}, actor,
					storage.UpdateIssueOptions{ExpectedStatus: strPtr(string(types.StatusOpen))}))
				try("claim", tg.wisp, s.ClaimIssue(ctx, tg.wisp, actor))
				try("link", tg.wisp, s.AddDependency(ctx, &types.Dependency{IssueID: tg.wisp, DependsOnID: tg.anchor, Type: types.DepRelated}, actor))
				try("merge metadata", tg.wisp, s.MergeMetadata(ctx, tg.wisp, "owner", json.RawMessage(`"`+actor+`"`), actor))
				try("clear metadata", tg.wisp, s.SlotClear(ctx, tg.wisp, "owner", actor))
				try("unlink", tg.wisp, s.RemoveDependency(ctx, tg.wisp, tg.anchor, actor))
				if r%2 == 0 {
					try("close", tg.wisp, s.CloseIssue(ctx, tg.wisp, "done", actor, ""))
				} else {
					_, err := s.CloseIssueChecked(ctx, tg.wisp, actor, storage.CloseIssueOptions{Reason: "done"})
					try("checked close", tg.wisp, err)
				}
				try("delete", tg.doomed, s.DeleteIssue(ctx, tg.doomed))
				_, err := s.DeleteIssues(ctx, []string{tg.swept}, false, true, false)
				try("batch delete", tg.swept, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	for w := range writers {
		actor := fmt.Sprintf("writer-%d", w)
		for _, tg := range targets[w] {
			got, err := base.GetIssue(ctx, tg.wisp)
			if err != nil {
				t.Fatalf("read %s: %v", tg.wisp, err)
			}
			if got.Status != types.StatusClosed || got.Assignee != actor || got.Notes != "noted by "+actor || got.Priority != 1 {
				t.Errorf("%s = status %s, assignee %q, notes %q, priority %d; want every write of %s applied",
					tg.wisp, got.Status, got.Assignee, got.Notes, got.Priority, actor)
			}
			if len(got.Metadata) > 0 && string(got.Metadata) != "{}" {
				t.Errorf("%s metadata = %s, want the merged key cleared", tg.wisp, got.Metadata)
			}
			deps, err := base.GetDependencyRecords(ctx, tg.wisp)
			if err != nil {
				t.Fatalf("read edges of %s: %v", tg.wisp, err)
			}
			if len(deps) != 0 {
				t.Errorf("%s keeps %d edge(s), want the link removed", tg.wisp, len(deps))
			}
			for _, gone := range []string{tg.doomed, tg.swept} {
				if _, err := base.GetIssue(ctx, gone); !errors.Is(err, storage.ErrNotFound) {
					t.Errorf("read %s after its delete = %v, want ErrNotFound", gone, err)
				}
			}
		}
	}
}
