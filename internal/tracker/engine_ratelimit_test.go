package tracker

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

type fakeRateLimitError struct {
	retryAfter time.Duration
	msg        string
}

func (e *fakeRateLimitError) Error() string                      { return e.msg }
func (e *fakeRateLimitError) RateLimitRetryAfter() time.Duration { return e.retryAfter }

// countingMockTracker counts CreateIssue invocations including failures
// (mockTracker.created only records successes).
type countingMockTracker struct {
	*mockTracker
	createAttempts int32
	failWith       error
}

func (m *countingMockTracker) CreateIssue(ctx context.Context, issue *types.Issue) (*TrackerIssue, error) {
	atomic.AddInt32(&m.createAttempts, 1)
	if m.failWith != nil {
		return nil, m.failWith
	}
	return m.mockTracker.CreateIssue(ctx, issue)
}

// On a provider rate limit, the push loop must stop after the first failure
// rather than re-running the same doomed request for every remaining issue.
func TestEnginePushAbortsLoopOnRateLimit(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	const numIssues = 5
	for i := 0; i < numIssues; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("bd-rl%d", i),
			Title:     fmt.Sprintf("Rate-limit issue %d", i),
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			Priority:  2,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("seed CreateIssue: %v", err)
		}
	}

	tracker := &countingMockTracker{
		mockTracker: newMockTracker("test"),
		failWith: &fakeRateLimitError{
			retryAfter: 60 * time.Second,
			msg:        "github secondary rate limit",
		},
	}
	engine := NewEngine(tracker, store, "test-actor")

	var warnings []string
	engine.OnWarning = func(msg string) { warnings = append(warnings, msg) }

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	if got := atomic.LoadInt32(&tracker.createAttempts); got != 1 {
		t.Errorf("expected 1 CreateIssue attempt before abort, got %d", got)
	}
	if result.Stats.Errors > 1 {
		t.Errorf("expected at most 1 error in stats, got %d", result.Stats.Errors)
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), "rate limit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning mentioning the rate limit, got: %v", warnings)
	}
}
