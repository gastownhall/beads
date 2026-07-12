package flatfile

import (
	"context"
	"errors"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddLabel adds a label to an issue. Mirrors issueops.AddLabelInTx: the label
// row is idempotent (INSERT IGNORE) but the label_added event is recorded
// unconditionally.
func (s *FlatFileStore) AddLabel(_ context.Context, issueID, label, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()

	issue, err := s.readIssue(issueID)
	if err != nil {
		return err
	}

	present := false
	for _, l := range issue.Labels {
		if l == label {
			present = true
			break
		}
	}
	if !present {
		// No UpdatedAt bump: label ops never touch issues.updated_at on
		// the SQL backends (AddLabelInTx only inserts the label row and
		// event), so bumping here would create per-backend drift in
		// updated-since filters and sync diffing.
		issue.Labels = append(issue.Labels, label)
		if err := s.writeIssue(issue); err != nil {
			return err
		}
	}
	return s.recordCommentEvent(issueID, issueops.IsWisp(issue), types.EventLabelAdded, actor, "Added label: "+label)
}

// RemoveLabel removes a label from an issue. Mirrors issueops.RemoveLabelInTx:
// removing an absent label is not an error, but the label_removed event is
// recorded unconditionally.
func (s *FlatFileStore) RemoveLabel(_ context.Context, issueID, label, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()

	issue, err := s.readIssue(issueID)
	if err != nil {
		return err
	}

	filtered := make([]string, 0, len(issue.Labels))
	removed := false
	for _, l := range issue.Labels {
		if l == label {
			removed = true
			continue
		}
		filtered = append(filtered, l)
	}
	if removed {
		// No UpdatedAt bump — see AddLabel.
		issue.Labels = filtered
		if err := s.writeIssue(issue); err != nil {
			return err
		}
	}
	return s.recordCommentEvent(issueID, issueops.IsWisp(issue), types.EventLabelRemoved, actor, "Removed label: "+label)
}

// GetLabels returns the labels for an issue. A missing issue yields an empty
// list, not an error: issueops.GetLabelsInTx is a bare SELECT on the labels
// table, so SQL backends return zero rows for unknown IDs.
func (s *FlatFileStore) GetLabels(_ context.Context, issueID string) ([]string, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	issue, err := s.readIssue(issueID)
	if errors.Is(err, storage.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}

	if issue.Labels == nil {
		return []string{}, nil
	}
	// Mirror issueops' bare `ORDER BY label`: binary/code-point ordering.
	labels := append([]string(nil), issue.Labels...)
	sort.Strings(labels)
	return labels, nil
}

// GetIssuesByLabel returns all issues that have the given label.
func (s *FlatFileStore) GetIssuesByLabel(_ context.Context, label string) ([]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	var result []*types.Issue
	for _, issue := range all {
		for _, l := range issue.Labels {
			if l == label {
				result = append(result, issue)
				break
			}
		}
	}
	return result, nil
}
