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

type stubSummarizer struct {
	summary string
	err     error
	calls   int
	mu      sync.Mutex
}

func (s *stubSummarizer) SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.summary, s.err
}

func (s *stubSummarizer) getCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
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
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	if err := c.CompactTier1(context.Background(), "bd-123"); err != nil {
		t.Fatalf("CompactTier1 unexpected error: %v", err)
	}
	if summary.calls != 1 {
		t.Fatalf("expected summarizer used once, got %d", summary.calls)
	}
	if !updateCalled || !applyCalled {
		t.Fatalf("expected update/apply to be called")
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

// --- New constructor tests ---

func TestNew_NilConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := &stubStore{}
	c, err := New(store, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.config.Concurrency != defaultConcurrency {
		t.Errorf("expected default concurrency %d, got %d", defaultConcurrency, c.config.Concurrency)
	}
	if !c.config.DryRun {
		t.Error("expected DryRun to be set when no API key")
	}
}

func TestNew_DryRunExplicit(t *testing.T) {
	store := &stubStore{}
	c, err := New(store, "", &Config{DryRun: true, Concurrency: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.config.Concurrency != 3 {
		t.Errorf("expected concurrency 3, got %d", c.config.Concurrency)
	}
	if c.summarizer != nil {
		t.Error("expected nil summarizer in dry run")
	}
}

func TestNew_NoAPIKeyFallsToDryRun(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := &stubStore{}
	c, err := New(store, "", &Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.config.DryRun {
		t.Error("expected DryRun to be set when no API key")
	}
}

func TestNew_WithAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := &stubStore{}
	c, err := New(store, "test-key-123", &Config{Concurrency: 2, AuditEnabled: true, Actor: "testbot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.config.Concurrency != 2 {
		t.Errorf("expected concurrency 2, got %d", c.config.Concurrency)
	}
	if c.summarizer == nil {
		t.Error("expected non-nil summarizer with API key")
	}
	if c.config.APIKey != "test-key-123" {
		t.Errorf("expected API key to be set, got %q", c.config.APIKey)
	}
}

func TestNew_ZeroConcurrency(t *testing.T) {
	store := &stubStore{}
	c, err := New(store, "", &Config{DryRun: true, Concurrency: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.config.Concurrency != defaultConcurrency {
		t.Errorf("expected default concurrency %d for zero value, got %d", defaultConcurrency, c.config.Concurrency)
	}
}

func TestNew_NegativeConcurrency(t *testing.T) {
	store := &stubStore{}
	c, err := New(store, "", &Config{DryRun: true, Concurrency: -1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.config.Concurrency != defaultConcurrency {
		t.Errorf("expected default concurrency %d for negative value, got %d", defaultConcurrency, c.config.Concurrency)
	}
}

func TestNew_EnvKeyOverridesParam(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	store := &stubStore{}
	c, err := New(store, "param-key", &Config{Concurrency: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.summarizer == nil {
		t.Error("expected non-nil summarizer when env key set")
	}
}

// --- CompactTier1 additional error path tests ---

func TestCompactTier1_CancelledContext(t *testing.T) {
	c := &Compactor{store: &stubStore{}, config: &Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.CompactTier1(ctx, "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestCompactTier1_EligibilityCheckError(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) {
			return false, "", errors.New("db error")
		},
	}
	c := &Compactor{store: store, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to verify eligibility") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCompactTier1_IneligibleNoReason(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return false, "", nil },
	}
	c := &Compactor{store: store, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	expected := "issue bd-123 is not eligible for Tier 1 compaction"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestCompactTier1_GetIssueFetchError(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn: func(context.Context, string) (*types.Issue, error) {
			return nil, errors.New("fetch error")
		},
	}
	c := &Compactor{store: store, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to fetch issue") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCompactTier1_SummarizerError(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
	}
	summary := &stubSummarizer{err: errors.New("API error")}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to summarize") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCompactTier1_ApplyCompactionError(t *testing.T) {
	cleanup := withGitHash(t, "abc\n")
	t.Cleanup(cleanup)

	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn:      func(context.Context, string, map[string]interface{}, string) error { return nil },
		applyCompactionFn:  func(context.Context, string, int, int, int, string) error { return errors.New("apply failed") },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to apply compaction metadata") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCompactTier1_AddCommentError(t *testing.T) {
	cleanup := withGitHash(t, "abc\n")
	t.Cleanup(cleanup)

	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		updateIssueFn:      func(context.Context, string, map[string]interface{}, string) error { return nil },
		applyCompactionFn:  func(context.Context, string, int, int, int, string) error { return nil },
		addCommentFn:       func(context.Context, string, string, string) error { return errors.New("comment failed") },
	}
	summary := &stubSummarizer{summary: "short"}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to add compaction comment") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCompactTier1_SummaryNotSmaller_CommentError(t *testing.T) {
	store := &stubStore{
		checkEligibilityFn: func(context.Context, string, int) (bool, string, error) { return true, "", nil },
		getIssueFn:         func(context.Context, string) (*types.Issue, error) { return stubIssue(), nil },
		addCommentFn:       func(context.Context, string, string, string) error { return errors.New("comment failed") },
	}
	summary := &stubSummarizer{summary: strings.Repeat("X", 40)}
	c := &Compactor{store: store, summarizer: summary, config: &Config{}}

	err := c.CompactTier1(context.Background(), "bd-123")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to record warning") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- CompactTier1Batch additional tests ---

func TestCompactTier1Batch_GetIssueError(t *testing.T) {
	store := &stubStore{
		getIssueFn: func(ctx context.Context, id string) (*types.Issue, error) {
			return nil, errors.New("not found")
		},
	}
	c := &Compactor{store: store, config: &Config{Concurrency: 1}}

	results, err := c.CompactTier1Batch(context.Background(), []string{"bd-1"})
	if err != nil {
		t.Fatalf("batch should not return top-level error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err == nil {
		t.Error("expected error in result")
	}
}

func TestCompactTier1Batch_Empty(t *testing.T) {
	c := &Compactor{store: &stubStore{}, config: &Config{Concurrency: 1}}

	results, err := c.CompactTier1Batch(context.Background(), []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestCompactTier1Batch_MixedResults(t *testing.T) {
	cleanup := withGitHash(t, "cafebabe\n")
	t.Cleanup(cleanup)

	var mu sync.Mutex
	updated := make(map[string]int)
	applied := make(map[string]int)
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
	resMap := map[string]*BatchResult{}
	for _, r := range results {
		resMap[r.IssueID] = &r
	}

	if res := resMap["bd-1"]; res == nil || res.Err != nil || res.CompactedSize == 0 {
		t.Fatalf("expected success result for bd-1, got %+v", res)
	}
	if res := resMap["bd-2"]; res == nil || res.Err == nil || !strings.Contains(res.Err.Error(), "not eligible") {
		t.Fatalf("expected ineligible error for bd-2, got %+v", res)
	}
	if updated["bd-1"] != 1 || applied["bd-1"] != 1 {
		t.Fatalf("expected store operations for bd-1 exactly once")
	}
	if updated["bd-2"] != 0 || applied["bd-2"] != 0 {
		t.Fatalf("bd-2 should not be processed")
	}
	if summary.calls != 1 {
		t.Fatalf("summarizer should run once; got %d", summary.calls)
	}
}
