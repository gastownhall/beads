package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddLabel adds a label to an issue.
// Wisp labels live in dolt_ignore'd tables and skip DOLT_COMMIT; permanent
// issue labels create a Dolt commit so the change reaches push targets and
// does not leave the working set dirty in server mode (GH server-mode flush).
func (s *DoltStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.AddLabelInTx(ctx, tx, "", "", issueID, label, actor)
	}); err != nil {
		return err
	}
	if s.isActiveWisp(ctx, issueID) {
		return nil
	}
	return s.doltAddAndCommit(ctx, []string{"labels", "events"}, fmt.Sprintf("bd: label add %s %s", issueID, label))
}

// RemoveLabel removes a label from an issue.
// Delegates SQL work to issueops.RemoveLabelInTx which handles wisp routing.
// See AddLabel for the DOLT_COMMIT contract.
func (s *DoltStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.RemoveLabelInTx(ctx, tx, "", "", issueID, label, actor)
	}); err != nil {
		return err
	}
	if s.isActiveWisp(ctx, issueID) {
		return nil
	}
	return s.doltAddAndCommit(ctx, []string{"labels", "events"}, fmt.Sprintf("bd: label remove %s %s", issueID, label))
}

// GetLabels retrieves all labels for an issue
func (s *DoltStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var labels []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		labels, err = issueops.GetLabelsInTx(ctx, tx, "", issueID)
		return err
	})
	return labels, err
}

// GetLabelsForIssues retrieves labels for multiple issues.
// Delegates to issueops.GetLabelsForIssuesInTx for shared query logic.
func (s *DoltStore) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	var result map[string][]string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetLabelsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

// GetIssuesByLabel retrieves all issues with a specific label
func (s *DoltStore) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	var ids []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		ids, err = issueops.GetIssuesByLabelInTx(ctx, tx, label)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssuesByIDs(ctx, ids)
}
