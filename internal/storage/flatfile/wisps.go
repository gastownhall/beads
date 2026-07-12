package flatfile

import (
	"context"
	"sort"

	"github.com/steveyegge/beads/internal/types"
)

// ListWisps returns ephemeral issues matching the filter, sorted by
// priority ascending then created_at descending (matching Dolt behavior).
func (s *FlatFileStore) ListWisps(_ context.Context, filter types.WispFilter) ([]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	var result []*types.Issue
	for _, issue := range all {
		if !issue.Ephemeral {
			continue
		}
		if !matchesWispFilter(issue, filter) {
			continue
		}
		result = append(result, issue)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func matchesWispFilter(issue *types.Issue, f types.WispFilter) bool {
	if f.Type != nil && issue.IssueType != *f.Type {
		return false
	}
	if f.Status != nil {
		if issue.Status != *f.Status {
			return false
		}
	} else if !f.IncludeClosed {
		// Default: non-closed only
		if issue.Status == types.StatusClosed {
			return false
		}
	}
	if f.UpdatedAfter != nil && !issue.UpdatedAt.After(*f.UpdatedAfter) {
		return false
	}
	if f.UpdatedBefore != nil && !issue.UpdatedAt.Before(*f.UpdatedBefore) {
		return false
	}
	return true
}
