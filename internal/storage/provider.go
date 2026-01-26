// Package storage defines the interface for issue storage backends.
package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// StorageProvider wraps a Storage interface to provide IssueProvider functionality.
// This adapts the full Storage interface to the minimal IssueProvider interface
// needed for orphan detection.
type StorageProvider struct {
	storage Storage
	prefix  string // Cached prefix (empty = not cached yet)
}

// NewStorageProvider creates an IssueProvider backed by a Storage instance.
func NewStorageProvider(s Storage) *StorageProvider {
	return &StorageProvider{storage: s}
}

// GetOpenIssues returns issues that are open or in_progress.
func (p *StorageProvider) GetOpenIssues(ctx context.Context) ([]*types.Issue, error) {
	// Use SearchIssues with empty query and status filter
	// We need to search for both "open" and "in_progress" issues
	openStatus := types.StatusOpen
	openIssues, err := p.storage.SearchIssues(ctx, "", types.IssueFilter{Status: &openStatus})
	if err != nil {
		return nil, err
	}

	inProgressStatus := types.StatusInProgress
	inProgressIssues, err := p.storage.SearchIssues(ctx, "", types.IssueFilter{Status: &inProgressStatus})
	if err != nil {
		return nil, err
	}

	// Combine results
	return append(openIssues, inProgressIssues...), nil
}

// GetIssuePrefix returns the configured issue prefix.
func (p *StorageProvider) GetIssuePrefix() string {
	// Cache the prefix on first access
	if p.prefix == "" {
		ctx := context.Background()
		prefix, err := p.storage.GetConfig(ctx, "issue_prefix")
		if err != nil || prefix == "" {
			p.prefix = "bd" // default
		} else {
			p.prefix = prefix
		}
	}
	return p.prefix
}

// Ensure StorageProvider implements types.IssueProvider
var _ types.IssueProvider = (*StorageProvider)(nil)
