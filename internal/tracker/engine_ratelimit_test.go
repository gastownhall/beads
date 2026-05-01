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

// fakeRateLimitError satisfies the RateLimitedError interface used by the
// push loop to detect provider-side rate limiting. We define it here (rather
// than importing internal/github) to keep the tracker package free of any
// dependency on a specific provider.
type fakeRateLimitError struct {
	retryAfter time.Duration
	msg        string
}

func (e *fakeRateLimitError) Error() string { return e.msg }
func (e *fakeRateLimitError) RateLimitRetryAfter() time.Duration {
	return e.retryAfter
}

// countingMockTracker wraps mockTracker to count CreateIssue invocations
// (including failures, which mockTracker.created does not capture).
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

// TestEnginePushAbortsLoopOnRateLimit asserts that when the underlying tracker
// returns a RateLimitedError from CreateIssue, the push loop stops processing
// the remaining queue instead of cascading the same failure across every
// pending issue. This is the engine-side counterpart to the client fix:
// without this, fixing the per-request retry math just produces a wall of
// well-formatted rate-limit warnings instead of a wall of generic ones.
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

	mock := newMockTracker("test")
	tracker := &countingMockTracker{
		mockTracker: mock,
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

	attempts := atomic.LoadInt32(&tracker.createAttempts)
	if attempts != 1 {
		t.Errorf("expected push loop to abort after first rate-limit error: got %d CreateIssue attempts (want 1)", attempts)
	}

	// One create error is expected (the one we hit). The remaining N-1 should
	// be reflected as skipped or simply not-attempted, NOT counted as errors —
	// they were not actually rejected by the server.
	if result.Stats.Errors > 1 {
		t.Errorf("expected at most 1 error in stats, got %d", result.Stats.Errors)
	}

	// Should have emitted at least one warning that names the rate limit so the
	// user understands why the push stopped early.
	foundRateLimitWarning := false
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), "rate limit") {
			foundRateLimitWarning = true
			break
		}
	}
	if !foundRateLimitWarning {
		t.Errorf("expected a warning mentioning the rate limit, got: %v", warnings)
	}
}
