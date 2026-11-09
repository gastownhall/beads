package doctor

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionCandidateFilter pins the load filter both pollution checks
// share: open, non-epic, Ephemeral=false issues, with no row cap of its own.
func TestPollutionCandidateFilter(t *testing.T) {
	f := PollutionCandidateFilter()
	if f.Status == nil || *f.Status != types.StatusOpen {
		t.Errorf("Status = %v, want open", f.Status)
	}
	if !slices.Equal(f.ExcludeTypes, []types.IssueType{types.TypeEpic}) {
		t.Errorf("ExcludeTypes = %v, want [epic]", f.ExcludeTypes)
	}
	if f.Ephemeral == nil || *f.Ephemeral {
		t.Errorf("Ephemeral = %v, want false (NoHistory rows stay eligible)", f.Ephemeral)
	}
	if f.MaxRows != 0 {
		t.Errorf("MaxRows = %d, want 0: the gate counts every candidate", f.MaxRows)
	}
}

// fakePollutionSearcher serves canned issues, or an error, and records the
// filter it was asked for.
type fakePollutionSearcher struct {
	issues []*types.Issue
	err    error
	filter *types.IssueFilter
}

func (f *fakePollutionSearcher) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.filter = &filter
	return f.issues, f.err
}

// TestCheckTestPollutionWithStore pins the validate gate on the shared
// scorer: it loads the shared candidate filter with no row cap, counts
// exactly DetectTestPollution's hits (re-applying eligibility to a superset),
// and keeps its zero-hit, warning and query-failure shapes.
func TestCheckTestPollutionWithStore(t *testing.T) {
	ctx := context.Background()
	open := func(id, title, desc string) *types.Issue {
		return &types.Issue{ID: id, Title: title, Description: desc, Status: types.StatusOpen, IssueType: types.TypeTask}
	}
	closed := open("bd-closed", "Test issue", "")
	closed.Status = types.StatusClosed

	t.Run("counts the scorer's hits", func(t *testing.T) {
		issues := []*types.Issue{
			open("bd-1", "Test issue", ""),
			open("bd-2", "TEST-fixture data", ""),
			open("bd-3", "test-runner hangs on sync", "The runner stalls after the third push."),
			closed,
		}
		f := &fakePollutionSearcher{issues: issues}
		check := checkTestPollutionWithStore(ctx, f)
		if want := len(DetectTestPollution(issues)); want != 2 {
			t.Fatalf("scorer hits = %d, want 2", want)
		}
		if check.Status != StatusWarning || check.Message != "2 potential test issue(s) detected" {
			t.Errorf("check = %q %q, want warning counting 2", check.Status, check.Message)
		}
		if check.Fix == "" || check.Detail == "" {
			t.Errorf("warning lost its Detail/Fix: %+v", check)
		}
		if f.filter == nil || !reflect.DeepEqual(*f.filter, PollutionCandidateFilter()) {
			t.Errorf("searched with %+v, want PollutionCandidateFilter() unchanged (no row cap)", f.filter)
		}
	})

	t.Run("zero hits", func(t *testing.T) {
		check := checkTestPollutionWithStore(ctx, &fakePollutionSearcher{issues: []*types.Issue{closed, open("bd-4", "Fix login", "")}})
		if check.Status != StatusOK || check.Message != "No test pollution detected" {
			t.Errorf("check = %q %q, want ok", check.Status, check.Message)
		}
	})

	t.Run("query failure", func(t *testing.T) {
		check := checkTestPollutionWithStore(ctx, &fakePollutionSearcher{err: errors.New("table not found")})
		if check.Status != StatusWarning || check.Message != "N/A (query failed)" {
			t.Errorf("check = %q %q, want warning N/A (query failed)", check.Status, check.Message)
		}
	})
}
