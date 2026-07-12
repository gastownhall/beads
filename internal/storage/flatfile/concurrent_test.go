package flatfile

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestConcurrentCreateDifferentIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const n = 50

	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			issue := &types.Issue{
				ID:    fmt.Sprintf("par-%d", idx),
				Title: fmt.Sprintf("Parallel issue %d", idx),
			}
			if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
				errs <- fmt.Errorf("create par-%d: %w", idx, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// Verify all created
	all, err := s.loadAllIssues()
	if err != nil {
		t.Fatalf("loadAllIssues: %v", err)
	}
	if len(all) != n {
		t.Errorf("got %d issues, want %d", len(all), n)
	}
}

func TestConcurrentUpdateDifferentIssues(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const n = 30

	// Create issues first
	for i := range n {
		s.CreateIssue(ctx, &types.Issue{
			ID:    fmt.Sprintf("upd-%d", i),
			Title: fmt.Sprintf("Issue %d", i),
		}, "tester")
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := s.UpdateIssue(ctx, fmt.Sprintf("upd-%d", idx), map[string]interface{}{
				"title": fmt.Sprintf("Updated %d", idx),
			}, "updater")
			if err != nil {
				errs <- fmt.Errorf("update upd-%d: %w", idx, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// Verify all updated
	for i := range n {
		issue, err := s.GetIssue(ctx, fmt.Sprintf("upd-%d", i))
		if err != nil {
			t.Errorf("GetIssue upd-%d: %v", i, err)
			continue
		}
		expected := fmt.Sprintf("Updated %d", i)
		if issue.Title != expected {
			t.Errorf("upd-%d title = %q, want %q", i, issue.Title, expected)
		}
	}
}

func TestConcurrentSearchDuringWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Pre-populate some issues
	for i := range 20 {
		s.CreateIssue(ctx, &types.Issue{
			ID:    fmt.Sprintf("search-%d", i),
			Title: fmt.Sprintf("Searchable %d", i),
		}, "tester")
	}

	var wg sync.WaitGroup

	// Writers
	for i := 20; i < 40; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.CreateIssue(ctx, &types.Issue{
				ID:    fmt.Sprintf("search-%d", idx),
				Title: fmt.Sprintf("Searchable %d", idx),
			}, "writer")
		}(i)
	}

	// Readers — should not panic or error
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				t.Errorf("SearchIssues during concurrent writes: %v", err)
			}
		}()
	}

	wg.Wait()
}

func TestConcurrentCounterAllocation(t *testing.T) {
	s := newTestStore(t)
	const n = 100

	var wg sync.WaitGroup
	ids := make(chan string, n)

	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.nextSequentialID("test")
			if err != nil {
				t.Errorf("nextSequentialID: %v", err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		seen[id] = true
	}

	if len(seen) != n {
		t.Errorf("got %d unique IDs, want %d", len(seen), n)
	}
}
