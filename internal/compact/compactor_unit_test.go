package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

type stubStore struct {
	checkEligibilityFn func(context.Context, string, int) (bool, string, error)
	getIssueFn         func(context.Context, string) (*types.Issue, error)
	updateIssueFn      func(context.Context, string, map[string]interface{}, string) error
	applyCompactionFn  func(context.Context, string, int, int, int, string) error
	addCommentFn       func(context.Context, string, string, string) error
	markDirtyFn        func(context.Context, string) error
}

func (s *stubStore) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	if s.checkEligibilityFn != nil {
		return s.checkEligibilityFn(ctx, issueID, tier)
	}
	return false, "", nil
}

func (s *stubStore) GetIssue(ctx context.Context, issueID string) (*types.Issue, error) {
	if s.getIssueFn != nil {
		return s.getIssueFn(ctx, issueID)
	}
	return nil, fmt.Errorf("GetIssue not stubbed")
}

func (s *stubStore) UpdateIssue(ctx context.Context, issueID string, updates map[string]interface{}, actor string) error {
	if s.updateIssueFn != nil {
		return s.updateIssueFn(ctx, issueID, updates, actor)
	}
	return nil
}

func (s *stubStore) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error {
	if s.applyCompactionFn != nil {
		return s.applyCompactionFn(ctx, issueID, tier, originalSize, compactedSize, commitHash)
	}
	return nil
}

func (s *stubStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	if s.addCommentFn != nil {
		return s.addCommentFn(ctx, issueID, actor, comment)
	}
	return nil
}

func (s *stubStore) MarkIssueDirty(ctx context.Context, issueID string) error {
	if s.markDirtyFn != nil {
		return s.markDirtyFn(ctx, issueID)
	}
	return nil
}

type stubSummarizer struct {
	summary string
	err     error
	calls   int
}

func (s *stubSummarizer) SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error) {
	s.calls++
	return s.summary, s.err
}

func stubIssue() *types.Issue {
	return &types.Issue{
		ID:                 "bd-123",
		Title:              "Fix login",
		Description:        strings.Repeat("A", 20),
		Design:             strings.Repeat("B", 10),
		Notes:              strings.Repeat("C", 5),
		AcceptanceCriteria: "done",
		Status:             types.StatusClosed,
	}
}

func withGitHash(t *testing.T, hash string) func() {
	orig := gitExec
	gitExec = func(string, ...string) ([]byte, error) {
		return []byte(hash), nil
	}
	return func() { gitExec = orig }
}

func TestCompactTier1_Success(t *testing.T) {
	cleanup := withGitHash(t, "deadbeef\n")
	t.Cleanup(cleanup)

	updateCalled := false
	applyCalled := false
	markCalled := false
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn: func(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
			updateCalled = true
			if updates["description"].(string) != "short" {
				t.Fatalf("expected summarized description")
			}
			if updates["design"].(string) != "" {
				t.Fatalf("design should be cleared")
			}
			return nil
		},
		applyCompactionFn: func(ctx context.Context, id string, tier, original, compacted int, hash string) error {
			applyCalled = true
			if hash != "deadbeef" {
				t.Fatalf("unexpected hash %q", hash)
			}
			return nil
		},
		addCommentFn: func(ctx context.Context, id, actor, comment string) error {
			if !strings.Contains(comment, "saved") {
				t.Fatalf("unexpected comment %q", comment)
			}
			return nil
		},
		markDirtyFn: func(context.Context, string) error {
			markCalled = true
			return nil
		},
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	if err := c.CompactTier1(context.Background(), "bd-123"); err != nil {
		t.Fatalf("CompactTier1 unexpected error: %v", err)
	}
	if summary.calls != 1 {
		t.Fatalf("expected summarizer used once, got %d", summary.calls)
	}
	if !updateCalled || !applyCalled || !markCalled {
		t.Fatalf("expected update/apply/mark to be called")
	}
}

func TestCompactTier1_DryRun(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{DryRun: true}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil || !strings.Contains(err.Error(), "dry-run") {
		t.Fatalf("expected dry-run error, got %v", err)
	}
	if summary.calls != 0 {
		t.Fatalf("summarizer should not be used in dry run")
	}
}

func TestCompactTier1_Ineligible(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return false, "recently compacted", nil },
	}
	c := &Compactor{store: store, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil || !strings.Contains(err.Error(), "recently compacted") {
		t.Fatalf("expected ineligible error, got %v", err)
	}
}

func TestCompactTier1_SummaryNotSmaller(t *testing.T) {
	commentCalled := false
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		addCommentFn: func(ctx context.Context, id, actor, comment string) error {
			commentCalled = true
			if !strings.Contains(comment, "Tier 1 compaction skipped") {
				t.Fatalf("unexpected comment %q", comment)
			}
			return nil
		},
	}
	summary := &stubSummarizer{summary: strings.Repeat("X", 40)}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil || !strings.Contains(err.Error(), "compaction would increase size") {
		t.Fatalf("expected size error, got %v", err)
	}
	if !commentCalled {
		t.Fatalf("expected warning comment to be recorded")
	}
}

func TestCompactTier1_UpdateError(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn:      func(context.Context, string, map[string]interface{}, string) error { return errors.New("boom") },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil || !strings.Contains(err.Error(), "failed to update issue") {
		t.Fatalf("expected update error, got %v", err)
	}
}

func TestCompactTier1Batch_MixedResults(t *testing.T) {
	cleanup := withGitHash(t, "cafebabe\n")
	t.Cleanup(cleanup)

	var mu sync.Mutex
	updated := make(map[string]int)
	applied := make(map[string]int)
	marked := make(map[string]int)
	store := &stubStore{
		checkEligibilityFn: func(ctx context.Context, id string, tier int) (bool, string, error) {
			switch id {
			case "bd-1":
				return true, "", nil
			case "bd-2":
				return false, "not eligible", nil
			default:
				return false, "", fmt.Errorf("unexpected id %s", id)
			}
		},
		getIssueFn: func(ctx context.Context, id string) (*types.Issue, error) {
			issue := stubIssue()
			issue.ID = id
			return issue, nil
		},
		updateIssueFn: func(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
			mu.Lock()
			updated[id]++
			mu.Unlock()
			return nil
		},
		applyCompactionFn: func(ctx context.Context, id string, tier, original, compacted int, hash string) error {
			mu.Lock()
			applied[id]++
			mu.Unlock()
			return nil
		},
		addCommentFn: func(context.Context, string, string, string) error { return nil },
		markDirtyFn: func(ctx context.Context, id string) error {
			mu.Lock()
			marked[id]++
			mu.Unlock()
			return nil
		},
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{Concurrency: 2}}

	results, err := c.CompactTier1Batch(context.Background(), []string{"bd-1", "bd-2"})
	if err != nil {
		t.Fatalf("CompactTier1Batch unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	resMap := map[string]*Result{}
	for _, r := range results {
		resMap[r.IssueID] = r
	}

	if res := resMap["bd-1"]; res == nil || res.Err != nil || res.CompactedSize == 0 {
		t.Fatalf("expected success result for bd-1, got %+v", res)
	}
	if res := resMap["bd-2"]; res == nil || res.Err == nil || !strings.Contains(res.Err.Error(), "not eligible") {
		t.Fatalf("expected ineligible error for bd-2, got %+v", res)
	}
	if updated["bd-1"] != 1 || applied["bd-1"] != 1 || marked["bd-1"] != 1 {
		t.Fatalf("expected store operations for bd-1 exactly once")
	}
	if updated["bd-2"] != 0 || applied["bd-2"] != 0 {
		t.Fatalf("bd-2 should not be processed")
	}
	if summary.calls != 1 {
		t.Fatalf("summarizer should run once; got %d", summary.calls)
	}
}
