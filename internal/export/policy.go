package export

import (
	"context"
	"fmt"
	"time"
)

// ErrorPolicy defines how export operations handle errors
type ErrorPolicy string

const (
	// PolicyStrict fails fast on any error (default for user-initiated exports)
	PolicyStrict ErrorPolicy = "strict"

	// PolicyBestEffort skips failed operations with warnings (good for auto-export)
	PolicyBestEffort ErrorPolicy = "best-effort"

	// PolicyPartial retries transient failures, skips persistent ones with manifest
	PolicyPartial ErrorPolicy = "partial"

	// PolicyRequiredCore fails on core data (issues/deps), skips enrichments (labels/comments)
	PolicyRequiredCore ErrorPolicy = "required-core"
)

// Config keys for export error handling
const (
	ConfigKeyErrorPolicy        = "export.error_policy"
	ConfigKeyRetryAttempts      = "export.retry_attempts"
	ConfigKeyRetryBackoffMS     = "export.retry_backoff_ms"
	ConfigKeySkipEncodingErrors = "export.skip_encoding_errors"
	ConfigKeyWriteManifest      = "export.write_manifest"
	ConfigKeyAutoExportPolicy   = "auto_export.error_policy"
)

// Default values
const (
	DefaultErrorPolicy        = PolicyStrict
	DefaultRetryAttempts      = 3
	DefaultRetryBackoffMS     = 100
	DefaultSkipEncodingErrors = false
	DefaultWriteManifest      = false
	DefaultAutoExportPolicy   = PolicyBestEffort
)

// Config holds export error handling configuration
type Config struct {
	Policy             ErrorPolicy
	RetryAttempts      int
	RetryBackoffMS     int
	SkipEncodingErrors bool
	WriteManifest      bool
	IsAutoExport       bool // If true, may use different policy
}

// Manifest tracks export completeness and failures
type Manifest struct {
	ExportedCount int           `json:"exported_count"`
	FailedIssues  []FailedIssue `json:"failed_issues,omitempty"`
	PartialData   []string      `json:"partial_data,omitempty"` // e.g., ["labels", "comments"]
	Warnings      []string      `json:"warnings,omitempty"`
	Complete      bool          `json:"complete"`
	ExportedAt    time.Time     `json:"exported_at"`
	ErrorPolicy   string        `json:"error_policy"`
}

// FailedIssue tracks a single issue that failed to export
type FailedIssue struct {
	IssueID     string   `json:"issue_id"`
	Reason      string   `json:"reason"`
	MissingData []string `json:"missing_data,omitempty"` // e.g., ["labels", "comments"]
}

// RetryWithBackoff wraps a function with retry logic
func RetryWithBackoff(ctx context.Context, attempts int, initialBackoffMS int, desc string, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	backoff := time.Duration(initialBackoffMS) * time.Millisecond

	for attempt := 1; attempt <= attempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Don't wait after last attempt
		if attempt == attempts {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= 2 // Exponential backoff
		}
	}

	if attempts > 1 {
		return fmt.Errorf("%s failed after %d attempts: %w", desc, attempts, lastErr)
	}
	return lastErr
}

// IsValid checks if the policy is a valid value
func (p ErrorPolicy) IsValid() bool {
	switch p {
	case PolicyStrict, PolicyBestEffort, PolicyPartial, PolicyRequiredCore:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer
func (p ErrorPolicy) String() string {
	return string(p)
}
