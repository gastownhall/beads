package compact

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	store      compactableStore
	summarizer summarizer
	config     *Config
}

// compactableStore defines the storage interface required for compaction.
// This interface can be implemented by any storage backend
// that wants to support the compaction feature.
type compactableStore interface {
	CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error)
	GetIssue(ctx context.Context, issueID string) (*types.Issue, error)
	UpdateIssue(ctx context.Context, issueID string, updates map[string]interface{}, actor string) error
	ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error
	AddComment(ctx context.Context, issueID, actor, comment string) error
}

type summarizer interface {
	SummarizeTier1(ctx context.Context, issue *types.Issue) (string, error)
}

// New creates a new Compactor instance with the given configuration.
// The store parameter must implement compactableStore interface.
func New(store compactableStore, apiKey string, config *Config) (*Compactor, error) {
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

	var haiClient summarizer
	var err error
	if !config.DryRun {
		haiClient, err = newHaikuClient(config.APIKey)
		if err != nil {
			if errors.Is(err, errAPIKeyRequired) {
				config.DryRun = true
			} else {
				return nil, fmt.Errorf("failed to create Haiku client: %w", err)
			}
		}
	}
	if hc, ok := haiClient.(*haikuClient); ok && hc != nil {
		hc.auditEnabled = config.AuditEnabled
		hc.auditActor = config.Actor
	}

	return &Compactor{
		store:      store,
		summarizer: haiClient,
		config:     config,
	}, nil
}

// CompactTier1 compacts a single issue at Tier 1 (basic summarization).
func (c *Compactor) CompactTier1(ctx context.Context, issueID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Check eligibility before fetching issue (fail fast)
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
		return fmt.Errorf("failed to fetch issue: %w", err)
	}

	// Calculate original size
	originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)

	if c.config.DryRun {
		return fmt.Errorf("dry-run: would compact %s (original size: %d bytes)", issueID, originalSize)
	}

	// Get summary from AI
	summary, err := c.summarizer.SummarizeTier1(ctx, issue)
	if err != nil {
		return fmt.Errorf("failed to summarize: %w", err)
	}

	// Check if compaction would actually reduce size
	compactedSize := len(summary)
	if compactedSize >= originalSize {
		warningMsg := fmt.Sprintf("Tier 1 compaction skipped: summary (%d bytes) not shorter than original (%d bytes)", compactedSize, originalSize)
		if err := c.store.AddComment(ctx, issueID, "compactor", warningMsg); err != nil {
			return fmt.Errorf("failed to record warning: %w", err)
		}
		return fmt.Errorf("compaction would increase size (%d → %d bytes), keeping original", originalSize, compactedSize)
	}

	// Update issue with summarized content
	updates := map[string]interface{}{
		"description":         summary,
		"design":              "",
		"notes":               "",
		"acceptance_criteria": "",
	}
	if err := c.store.UpdateIssue(ctx, issueID, updates, "compactor"); err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	// Record compaction metadata with git commit hash
	commitHash := GetCurrentCommitHash()
	if err := c.store.ApplyCompaction(ctx, issueID, 1, originalSize, compactedSize, commitHash); err != nil {
		return fmt.Errorf("failed to apply compaction metadata: %w", err)
	}

	// Add comment about compaction
	savingBytes := originalSize - compactedSize
	comment := fmt.Sprintf("Tier 1 compaction: %d → %d bytes (saved %d)", originalSize, compactedSize, savingBytes)
	if err := c.store.AddComment(ctx, issueID, "compactor", comment); err != nil {
		return fmt.Errorf("failed to add compaction comment: %w", err)
	}

	return nil
}

// BatchResult holds the result of a single issue compaction in a batch.
type BatchResult struct {
	IssueID       string
	OriginalSize  int
	CompactedSize int
	Err           error
}

// CompactTier1Batch compacts multiple issues at Tier 1 concurrently.
func (c *Compactor) CompactTier1Batch(ctx context.Context, issueIDs []string) ([]BatchResult, error) {
	results := make([]BatchResult, len(issueIDs))
	sem := make(chan struct{}, c.config.Concurrency)
	var wg sync.WaitGroup

	for i, id := range issueIDs {
		wg.Add(1)
		go func(idx int, issueID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Get issue to calculate original size
			issue, err := c.store.GetIssue(ctx, issueID)
			if err != nil {
				results[idx] = BatchResult{IssueID: issueID, Err: err}
				return
			}

			originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)

			err = c.CompactTier1(ctx, issueID)
			if err != nil {
				results[idx] = BatchResult{IssueID: issueID, OriginalSize: originalSize, Err: err}
				return
			}

			// Get updated issue to calculate compacted size
			issueAfter, _ := c.store.GetIssue(ctx, issueID)
			compactedSize := 0
			if issueAfter != nil {
				compactedSize = len(issueAfter.Description)
			}

			results[idx] = BatchResult{
				IssueID:       issueID,
				OriginalSize:  originalSize,
				CompactedSize: compactedSize,
			}
		}(i, id)
	}

	wg.Wait()
	return results, nil
}
