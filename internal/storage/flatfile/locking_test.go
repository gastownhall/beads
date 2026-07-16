package flatfile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the SQL-transaction semantics for concurrent SAME-issue
// mutations: no lost updates, and claim mutual exclusion. Without writeMu
// serializing the read-modify-write cycle, each is a last-writer-wins race.

func TestConcurrentAddLabelSameIssue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "lbl-1", Title: "labels"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := s.AddLabel(ctx, "lbl-1", fmt.Sprintf("l-%d", idx), "tester"); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("AddLabel: %v", err)
	}

	labels, err := s.GetLabels(ctx, "lbl-1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != n {
		t.Errorf("got %d labels, want %d (lost update)", len(labels), n)
	}
}

func TestConcurrentAddDependencySameSource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "dep-src", Title: "source"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	const n = 20
	for i := range n {
		if err := s.CreateIssue(ctx, &types.Issue{ID: fmt.Sprintf("dep-t%d", i), Title: "target"}, "tester"); err != nil {
			t.Fatalf("CreateIssue target: %v", err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			dep := &types.Dependency{
				IssueID:     "dep-src",
				DependsOnID: fmt.Sprintf("dep-t%d", idx),
				Type:        types.DepBlocks,
			}
			if err := s.AddDependency(ctx, dep, "tester"); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("AddDependency: %v", err)
	}

	deps, err := s.GetDependencyRecords(ctx, "dep-src")
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != n {
		t.Errorf("got %d dependency edges, want %d (lost update)", len(deps), n)
	}
}

func TestConcurrentClaimSingleWinner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "claim-1", Title: "claim me"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	var wins atomic.Int64
	winners := make(chan string, n)
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			actor := fmt.Sprintf("agent-%d", idx)
			err := s.ClaimIssue(ctx, "claim-1", actor)
			switch {
			case err == nil:
				wins.Add(1)
				winners <- actor
			case errors.Is(err, storage.ErrAlreadyClaimed), errors.Is(err, storage.ErrNotClaimable):
				// expected loser
			default:
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	close(errs)
	for err := range errs {
		t.Errorf("ClaimIssue unexpected error: %v", err)
	}

	if got := wins.Load(); got != 1 {
		t.Fatalf("%d claims succeeded, want exactly 1 (double-grant)", got)
	}
	winner := <-winners
	issue, err := s.GetIssue(ctx, "claim-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Assignee != winner {
		t.Errorf("assignee = %q, want winner %q", issue.Assignee, winner)
	}
	if issue.Status != types.StatusInProgress {
		t.Errorf("status = %q, want in_progress", issue.Status)
	}
}

func TestConcurrentUpdateDistinctFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateIssue(ctx, &types.Issue{ID: "upd-same", Title: "fields"}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, updates := range []map[string]interface{}{
		{"assignee": "alice"},
		{"notes": "reviewed"},
	} {
		wg.Add(1)
		go func(u map[string]interface{}) {
			defer wg.Done()
			if err := s.UpdateIssue(ctx, "upd-same", u, "tester"); err != nil {
				errs <- err
			}
		}(updates)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("UpdateIssue: %v", err)
	}

	issue, err := s.GetIssue(ctx, "upd-same")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Assignee != "alice" {
		t.Errorf("assignee = %q, want %q (lost update)", issue.Assignee, "alice")
	}
	if issue.Notes != "reviewed" {
		t.Errorf("notes = %q, want %q (lost update)", issue.Notes, "reviewed")
	}
}
