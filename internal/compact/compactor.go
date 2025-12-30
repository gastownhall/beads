package compact

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

const (
	defaultConcurrency = 5
)

// Config holds configuration for the compaction process.
type Config struct {
	APIKey       string
	Concurrency  int
	DryRun       bool
	AuditEnabled bool
	Actor        string
}

// Compactor handles issue compaction using AI summarization.
type Compactor struct {
	store      issueStore
	summarizer summarizer
	config     *Config
}

type issueStore interface {
	CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error)
	GetIssue(ctx context.Context, issueID string) (*types.Issue, error)
	UpdateIssue(ctx context.Context, issueID string, updates map[string]interface{}, actor string) error
	ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error
	AddComment(ctx context.Context, issueID, actor, comment string) error
	MarkIssueDirty(ctx context.Context, issueID string) error
}

type summarizer interface {
	SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error)
}

// New creates a new Compactor instance with the given configuration.
func New(store *sqlite.SQLiteStorage, apiKey string, config *Config) (*Compactor, error) {
	if config == nil {
		config = &Config{
			Concurrency: defaultConcurrency,
		}
	}
	if config.Concurrency <= 0 {
		config.Concurrency = defaultConcurrency
	}
	if apiKey != "" {
		config.APIKey = apiKey
	}

	var haikuClient summarizer
	var err error
	if !config.DryRun {
		haikuClient, err = NewHaikuClient(config.APIKey)
		if err != nil {
			if errors.Is(err, ErrAPIKeyRequired) {
				config.DryRun = true
			} else {
				return nil, fmt.Errorf("failed to create Haiku client: %w", err)
			}
		}
	}
	if hc, ok := haikuClient.(*HaikuClient); ok {
		hc.auditEnabled = config.AuditEnabled
		hc.auditActor = config.Actor
	}

	return &Compactor{
		store:      store,
		summarizer: haikuClient,
		config:     config,
	}, nil
}

// Result holds the outcome of a compaction operation.
type Result struct {
	IssueID       string
	OriginalSize  int
	CompactedSize int
	Err           error
}

// CompactTier1 performs tier-1 compaction on a single issue using AI summarization.
func (c *Compactor) CompactTier1(ctx context.Context, issueID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	eligible, reason, err := c.store.CheckEligibility(ctx, issueID, 1)
	if err != nil {
		return fmt.Errorf("failed to verify eligibility: %w", err)
	}

	if !eligible {
		if reason != "" {
			return fmt.Errorf("issue %s is not eligible for Tier 1 compaction: %s", issueID, reason)
		}
		return fmt.Errorf("issue %s is not eligible for Tier 1 compaction", issueID)
	}

	issue, err := c.store.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to get issue: %w", err)
	}

	originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)

	if c.config.DryRun {
		return fmt.Errorf("dry-run: would compact %s (original size: %d bytes)", issueID, originalSize)
	}

	if c.summarizer == nil {
		return fmt.Errorf("summarizer not configured")
	}
	summary, err := c.summarizer.SummarizeTier1(ctx, issue)
	if err != nil {
		return fmt.Errorf("failed to summarize with Haiku: %w", err)
	}

	compactedSize := len(summary)

	if compactedSize >= originalSize {
		warningMsg := fmt.Sprintf("Tier 1 compaction skipped: summary (%d bytes) not shorter than original (%d bytes)", compactedSize, originalSize)
		if err := c.store.AddComment(ctx, issueID, "compactor", warningMsg); err != nil {
			return fmt.Errorf("failed to record warning: %w", err)
		}
		return fmt.Errorf("compaction would increase size (%d → %d bytes), keeping original", originalSize, compactedSize)
	}

	updates := map[string]interface{}{
		"description":         summary,
		"design":              "",
		"notes":               "",
		"acceptance_criteria": "",
	}

	if err := c.store.UpdateIssue(ctx, issueID, updates, "compactor"); err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	commitHash := GetCurrentCommitHash()
	if err := c.store.ApplyCompaction(ctx, issueID, 1, originalSize, compactedSize, commitHash); err != nil {
		return fmt.Errorf("failed to set compaction level: %w", err)
	}

	savingBytes := originalSize - compactedSize
	eventData := fmt.Sprintf("Tier 1 compaction: %d → %d bytes (saved %d)", originalSize, compactedSize, savingBytes)
	if err := c.store.AddComment(ctx, issueID, "compactor", eventData); err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	if err := c.store.MarkIssueDirty(ctx, issueID); err != nil {
		return fmt.Errorf("failed to mark dirty: %w", err)
	}

	return nil
}

// CompactTier1Batch performs tier-1 compaction on multiple issues in a single batch.
func (c *Compactor) CompactTier1Batch(ctx context.Context, issueIDs []string) ([]*Result, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	eligibleIDs := make([]string, 0, len(issueIDs))
	results := make([]*Result, 0, len(issueIDs))

	for _, id := range issueIDs {
		eligible, reason, err := c.store.CheckEligibility(ctx, id, 1)
		if err != nil {
			results = append(results, &Result{
				IssueID: id,
				Err:     fmt.Errorf("failed to verify eligibility: %w", err),
			})
			continue
		}
		if !eligible {
			results = append(results, &Result{
				IssueID: id,
				Err:     fmt.Errorf("not eligible for Tier 1 compaction: %s", reason),
			})
		} else {
			eligibleIDs = append(eligibleIDs, id)
		}
	}

	if len(eligibleIDs) == 0 {
		return results, nil
	}

	if c.config.DryRun {
		for _, id := range eligibleIDs {
			issue, err := c.store.GetIssue(ctx, id)
			if err != nil {
				results = append(results, &Result{
					IssueID: id,
					Err:     fmt.Errorf("failed to get issue: %w", err),
				})
				continue
			}
			originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
			results = append(results, &Result{
				IssueID:      id,
				OriginalSize: originalSize,
				Err:          nil,
			})
		}
		return results, nil
	}

	workCh := make(chan string, len(eligibleIDs))
	resultCh := make(chan *Result, len(eligibleIDs))

	var wg sync.WaitGroup
	for i := 0; i < c.config.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for issueID := range workCh {
				result := &Result{IssueID: issueID}

				if err := c.compactSingleWithResult(ctx, issueID, result); err != nil {
					result.Err = err
				}

				resultCh <- result
			}
		}()
	}

	for _, id := range eligibleIDs {
		workCh <- id
	}
	close(workCh)

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for result := range resultCh {
		results = append(results, result)
	}

	return results, nil
}

func (c *Compactor) compactSingleWithResult(ctx context.Context, issueID string, result *Result) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	issue, err := c.store.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to get issue: %w", err)
	}

	result.OriginalSize = len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)

	if c.summarizer == nil {
		return fmt.Errorf("summarizer not configured")
	}
	summary, err := c.summarizer.SummarizeTier1(ctx, issue)
	if err != nil {
		return fmt.Errorf("failed to summarize with Haiku: %w", err)
	}

	result.CompactedSize = len(summary)

	if result.CompactedSize >= result.OriginalSize {
		warningMsg := fmt.Sprintf("Tier 1 compaction skipped: summary (%d bytes) not shorter than original (%d bytes)", result.CompactedSize, result.OriginalSize)
		if err := c.store.AddComment(ctx, issueID, "compactor", warningMsg); err != nil {
			return fmt.Errorf("failed to record warning: %w", err)
		}
		return fmt.Errorf("compaction would increase size (%d → %d bytes), keeping original", result.OriginalSize, result.CompactedSize)
	}

	updates := map[string]interface{}{
		"description":         summary,
		"design":              "",
		"notes":               "",
		"acceptance_criteria": "",
	}

	if err := c.store.UpdateIssue(ctx, issueID, updates, "compactor"); err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	commitHash := GetCurrentCommitHash()
	if err := c.store.ApplyCompaction(ctx, issueID, 1, result.OriginalSize, result.CompactedSize, commitHash); err != nil {
		return fmt.Errorf("failed to set compaction level: %w", err)
	}

	savingBytes := result.OriginalSize - result.CompactedSize
	eventData := fmt.Sprintf("Tier 1 compaction: %d → %d bytes (saved %d)", result.OriginalSize, result.CompactedSize, savingBytes)
	if err := c.store.AddComment(ctx, issueID, "compactor", eventData); err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	if err := c.store.MarkIssueDirty(ctx, issueID); err != nil {
		return fmt.Errorf("failed to mark dirty: %w", err)
	}

	return nil
}
