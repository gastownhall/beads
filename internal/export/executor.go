package export

import (
	"context"
	"fmt"
	"os"
)

// DataType represents a type of data being fetched
type DataType string

const (
	DataTypeCore     DataType = "core"     // Issues and dependencies
	DataTypeLabels   DataType = "labels"   // Issue labels
	DataTypeComments DataType = "comments" // Issue comments
)

// FetchResult holds the result of a data fetch operation
type FetchResult struct {
	Success  bool
	Err      error
	Warnings []string
}

// FetchWithPolicy executes a fetch operation with the configured error policy
func FetchWithPolicy(ctx context.Context, cfg *Config, dataType DataType, desc string, fn func() error) FetchResult {
	var result FetchResult

	// Determine if this is core data
	isCore := dataType == DataTypeCore

	// Execute based on policy
	switch cfg.Policy {
	case PolicyStrict:
		// Fail-fast on any error
		err := RetryWithBackoff(ctx, cfg.RetryAttempts, cfg.RetryBackoffMS, desc, fn)
		if err != nil {
			result.Err = err
			return result
		}
		result.Success = true

	case PolicyBestEffort:
		// Skip errors with warnings
		err := RetryWithBackoff(ctx, cfg.RetryAttempts, cfg.RetryBackoffMS, desc, fn)
		if err != nil {
			warning := fmt.Sprintf("Warning: %s failed, skipping: %v", desc, err)
			fmt.Fprintf(os.Stderr, "%s\n", warning)
			result.Warnings = append(result.Warnings, warning)
			result.Success = false // Data is missing
			return result
		}
		result.Success = true

	case PolicyPartial:
		// Retry with backoff, then skip with manifest entry
		err := RetryWithBackoff(ctx, cfg.RetryAttempts, cfg.RetryBackoffMS, desc, fn)
		if err != nil {
			warning := fmt.Sprintf("Warning: %s failed after retries, skipping: %v", desc, err)
			fmt.Fprintf(os.Stderr, "%s\n", warning)
			result.Warnings = append(result.Warnings, warning)
			result.Success = false
			return result
		}
		result.Success = true

	case PolicyRequiredCore:
		// Fail on core data, skip enrichments
		if isCore {
			err := RetryWithBackoff(ctx, cfg.RetryAttempts, cfg.RetryBackoffMS, desc, fn)
			if err != nil {
				result.Err = err
				return result
			}
			result.Success = true
		} else {
			// Best-effort for enrichments
			err := RetryWithBackoff(ctx, cfg.RetryAttempts, cfg.RetryBackoffMS, desc, fn)
			if err != nil {
				warning := fmt.Sprintf("Warning: %s (enrichment) failed, skipping: %v", desc, err)
				fmt.Fprintf(os.Stderr, "%s\n", warning)
				result.Warnings = append(result.Warnings, warning)
				result.Success = false
				return result
			}
			result.Success = true
		}

	default:
		// Unknown policy, fail-fast as safest option
		result.Err = fmt.Errorf("unknown error policy: %s", cfg.Policy)
		return result
	}

	return result
}
