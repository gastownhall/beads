package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// labelFailIssueUC answers the two ID lookups uowMolReader.GetIssuesByIDs
// makes; every other method is a nil call, which is the assertion that this
// path touches nothing else.
type labelFailIssueUC struct {
	domain.IssueUseCase
	issues []*types.Issue
	wisps  []*types.Issue
}

func (s labelFailIssueUC) GetIssuesByIDs(context.Context, []string) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s labelFailIssueUC) GetWispsByIDs(context.Context, []string) ([]*types.Issue, error) {
	return s.wisps, nil
}

// labelFailLabelUC fails whichever of the two label reads is selected.
type labelFailLabelUC struct {
	domain.LabelUseCase
	issueErr error
	wispErr  error
}

func (s labelFailLabelUC) GetLabelsForIssues(context.Context, []string) (map[string][]string, error) {
	if s.issueErr != nil {
		return nil, s.issueErr
	}
	return map[string][]string{}, nil
}

func (s labelFailLabelUC) GetLabelsForWisps(context.Context, []string) (map[string][]string, error) {
	if s.wispErr != nil {
		return nil, s.wispErr
	}
	return map[string][]string{}, nil
}

type labelFailUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
	labels domain.LabelUseCase
}

func (u labelFailUOW) Close(context.Context)             {}
func (u labelFailUOW) IssueUseCase() domain.IssueUseCase { return u.issues }
func (u labelFailUOW) LabelUseCase() domain.LabelUseCase { return u.labels }

// TestUOWMolReaderGetIssuesByIDsPropagatesLabelErrors pins the fail-closed
// contract the wisp GC guard depends on.
//
// This port used to swallow both label reads (`if labelMap, err := …; err ==
// nil`), which left every returned issue with nil Labels. For a renderer that
// is a cosmetic defect. For isProtectedWisp it is a licence to delete: an
// unlabeled issue reads as "carries no protected label", so a transient label
// read failure during cascade expansion would have made `bd mol wisp gc` reclaim
// exactly the records wisp.protected_labels promises it never will — silently,
// with no error anywhere for an operator to notice.
func TestUOWMolReaderGetIssuesByIDsPropagatesLabelErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("label table read failed")

	for _, tc := range []struct {
		name   string
		reader uowMolReader
	}{
		{
			name: "issue label read failure",
			reader: uowMolReader{uw: labelFailUOW{
				issues: labelFailIssueUC{issues: []*types.Issue{{ID: "bd-1"}}},
				labels: labelFailLabelUC{issueErr: boom},
			}},
		},
		{
			name: "wisp label read failure",
			reader: uowMolReader{uw: labelFailUOW{
				issues: labelFailIssueUC{wisps: []*types.Issue{{ID: "bd-wisp-1"}}},
				labels: labelFailLabelUC{wispErr: boom},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.reader.GetIssuesByIDs(t.Context(), []string{"bd-1", "bd-wisp-1"})
			if err == nil {
				t.Fatalf("a failed label read must surface as an error, got issues=%v and nil error", got)
			}
			if !errors.Is(err, boom) {
				t.Errorf("error should wrap the underlying failure; got %v", err)
			}
			if got != nil {
				t.Errorf("no partial result may escape a failed label read; got %v", got)
			}
		})
	}
}
