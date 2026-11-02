package main

// The label reads of uowMolReader.GetIssuesByIDs hydrate the rows the wisp
// GC's cascade expansion guards on (bd-961adp): a label the sweep cannot
// read must not read as an unlabeled wisp, so the port propagates the
// failure instead of returning partially hydrated rows — the caller then
// aborts the expansion rather than risk deleting a protected dependent.
//
// The stubs follow proxied_lookup_failure_test.go: a UnitOfWork whose use
// cases answer only the calls GetIssuesByIDs makes; any other method is a
// nil call and panics, which is the assertion that this path stops there.

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// stubCascadeRowsUC answers the two row reads GetIssuesByIDs makes.
type stubCascadeRowsUC struct {
	domain.IssueUseCase
	issues []*types.Issue
	wisps  []*types.Issue
}

func (s stubCascadeRowsUC) GetIssuesByIDs(context.Context, []string) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s stubCascadeRowsUC) GetWispsByIDs(context.Context, []string) ([]*types.Issue, error) {
	return s.wisps, nil
}

// stubHydrationLabelsUC answers the two label reads, failing where a test
// says so. nil error with a nil map means "no labels on record", which the
// port must not conflate with a failed read.
type stubHydrationLabelsUC struct {
	domain.LabelUseCase
	forIssuesErr error
	forWispsErr  error
}

func (s stubHydrationLabelsUC) GetLabelsForIssues(context.Context, []string) (map[string][]string, error) {
	return nil, s.forIssuesErr
}

func (s stubHydrationLabelsUC) GetLabelsForWisps(context.Context, []string) (map[string][]string, error) {
	return nil, s.forWispsErr
}

// hydrationUOW serves the issue and label use cases; the other three are nil
// because a unit of work hands all of them over at once, and this path must
// not reach any of them.
type hydrationUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
	labels domain.LabelUseCase
}

func (u hydrationUOW) Close(context.Context)                       {}
func (u hydrationUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u hydrationUOW) LabelUseCase() domain.LabelUseCase           { return u.labels }
func (u hydrationUOW) DependencyUseCase() domain.DependencyUseCase { return nil }
func (u hydrationUOW) CommentUseCase() domain.CommentUseCase       { return nil }

// TestUOWMolReaderGetIssuesByIDsPropagatesLabelErrors pins "no partial
// rows": when either label read fails, GetIssuesByIDs returns the error and
// no rows — never label-less rows a guard would treat as unprotected. With
// both reads healthy, the rows come back hydrated.
func TestUOWMolReaderGetIssuesByIDsPropagatesLabelErrors(t *testing.T) {
	rows := []*types.Issue{{ID: "gc-parent"}}
	for _, tc := range []struct {
		name   string
		wisps  []*types.Issue
		labels stubHydrationLabelsUC
	}{
		{name: "issue label read fails", labels: stubHydrationLabelsUC{forIssuesErr: errors.New("hydrate labels: connection refused")}},
		{name: "wisp label read fails", wisps: []*types.Issue{{ID: "gc-wisp"}}, labels: stubHydrationLabelsUC{forWispsErr: errors.New("hydrate wisp labels: connection refused")}},
		{name: "healthy reads", labels: stubHydrationLabelsUC{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := uowMolReader{uw: hydrationUOW{
				issues: stubCascadeRowsUC{issues: rows, wisps: tc.wisps},
				labels: tc.labels,
			}}
			got, err := r.GetIssuesByIDs(context.Background(), []string{"gc-parent"})
			if tc.name == "healthy reads" {
				if err != nil {
					t.Fatalf("GetIssuesByIDs: %v", err)
				}
				if len(got) != len(rows)+len(tc.wisps) {
					t.Errorf("got %d rows, want %d (healthy reads hydrate every row)", len(got), len(rows)+len(tc.wisps))
				}
				return
			}
			if err == nil {
				t.Errorf("GetIssuesByIDs swallowed a failed label read and returned %d rows; want no partial rows", len(got))
			}
			if len(got) != 0 {
				t.Errorf("partially hydrated rows came back over a failed label read (got %d); want none", len(got))
			}
		})
	}
}
