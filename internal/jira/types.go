// Package jira provides client, types, and utilities for Jira integration.
package jira

import "time"

// API configuration constants.
const (
	// MaxRetries is the maximum number of retries for transient failures.
	MaxRetries = 3

	// RetryDelay is the base delay between retries (exponential backoff).
	RetryDelay = time.Second

	// MaxResponseSize is the maximum response body size (50 MB).
	MaxResponseSize = 50 * 1024 * 1024
)
