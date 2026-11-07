package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// failingGCReader is a cascadeAgeReader whose setting read, initial search
// and cascade fetch can be made to fail. value is what GetConfig answers when
// configErr is nil.
type failingGCReader struct {
	*cascadeAgeReader
	value     string
	configErr error
	searchErr error
	expandErr error
	fetchErr  error
}

func (r failingGCReader) GetConfig(context.Context, string) (string, error) {
	return r.value, r.configErr
}

func (r failingGCReader) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	if r.searchErr != nil {
		return nil, r.searchErr
	}
	return r.cascadeAgeReader.SearchIssues(ctx, query, filter)
}

func (r failingGCReader) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	children, err := r.cascadeAgeReader.FindWispDependentsRecursive(ctx, ids)
	if r.expandErr != nil {
		return children, r.expandErr
	}
	return children, err
}

func (r failingGCReader) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if r.fetchErr != nil {
		return nil, r.fetchErr
	}
	return r.cascadeAgeReader.GetIssuesByIDs(ctx, ids)
}

// wispGCGuardFailures are the reads wisp gc cannot do without: the setting
// itself, its parse, and the initial search, whose label hydration is what
// the guard matches against (a label read that fails there fails the search).
func wispGCGuardFailures(issues []*types.Issue) map[string]failingGCReader {
	base := func() *cascadeAgeReader { return &cascadeAgeReader{issues: issues} }
	return map[string]failingGCReader{
		"setting unreadable":      {cascadeAgeReader: base(), configErr: errors.New("config table unavailable")},
		"setting malformed":       {cascadeAgeReader: base(), value: `["keep:mail"`},
		"setting names no label":  {cascadeAgeReader: base(), value: ","},
		"setting has a null name": {cascadeAgeReader: base(), value: `[null,"keep:mail"]`},
		"label hydration fails":   {cascadeAgeReader: base(), value: "keep:mail", searchErr: errors.New("hydrate labels: connection reset")},
	}
}

// TestWispGCAgeAbortsWhenProtectedLabelsUnreadable pins that age GC fails
// closed: a guard it cannot read is an error with no candidates, never an
// empty guard that lets a protected record be deleted.
func TestWispGCAgeAbortsWhenProtectedLabelsUnreadable(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	issues := []*types.Issue{
		{ID: "gc-mail", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}},
		{ID: "gc-plain", Status: types.StatusClosed, UpdatedAt: stale},
	}
	for name, r := range wispGCGuardFailures(issues) {
		for _, cleanAll := range []bool{false, true} {
			got, err := findAbandonedWisps(context.Background(), r, cleanAll, time.Hour, nil)
			if err == nil || len(got) != 0 {
				t.Errorf("%s, --all=%v: findAbandonedWisps = (%d candidates, %v), want an error and no candidates",
					name, cleanAll, len(got), err)
			}
		}
	}
}

// TestWispGCClosedPurgeAbortsWhenProtectedLabelsUnreadable pins the same for
// the closed purge, which both routes share.
func TestWispGCClosedPurgeAbortsWhenProtectedLabelsUnreadable(t *testing.T) {
	issues := []*types.Issue{
		{ID: "gc-mail", Status: types.StatusClosed, Labels: []string{"keep:mail"}},
		{ID: "gc-plain", Status: types.StatusClosed},
	}
	for name, r := range wispGCGuardFailures(issues) {
		got, _, err := selectClosedPurgeCandidates(context.Background(), r, nil, nil)
		if err == nil || len(got) != 0 {
			t.Errorf("%s: selectClosedPurgeCandidates = (%d candidates, %v), want an error and no candidates", name, len(got), err)
		}
	}
}

// TestWispGCCascadeFetchFailureIsReported pins the cascade's failure mode: when
// the dependents cannot be fetched (on the proxied route, a failed label read
// now fails the fetch rather than returning unlabeled rows), the abandoned
// parents are still returned, none of the dependents that fetch would have
// produced are, and the error comes back so the command warns that cascade
// expansion is incomplete instead of reporting a clean run.
func TestWispGCCascadeFetchFailureIsReported(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	parent := &types.Issue{ID: "gc-parent", Status: types.StatusOpen, UpdatedAt: stale}
	step := &types.Issue{ID: "gc-step", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	boom := errors.New("loading labels for 1 wisp(s): label table read failed")

	r := failingGCReader{
		cascadeAgeReader: &cascadeAgeReader{
			issues:   []*types.Issue{parent, step},
			children: map[string]bool{step.ID: true},
		},
		value:    "keep:mail",
		fetchErr: boom,
	}
	got, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
	if !errors.Is(err, boom) {
		t.Errorf("findAbandonedWisps error = %v, want the cascade fetch failure", err)
	}
	if len(got) != 1 || got[0].ID != parent.ID {
		t.Errorf("selected %d wisp(s), want only the abandoned parent %s (nothing that depends only on the failed fetch)", len(got), parent.ID)
	}
}

// TestWispGCCascadeFetchFailureKeepsIndependentParents pins the edges of that
// failure mode. A wisp the sweep selected on its own stays selected even when
// its ID is also among the dependents whose fetch failed, and when the
// recursive expansion and the fetch both fail, both errors reach the caller.
func TestWispGCCascadeFetchFailureKeepsIndependentParents(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	parent := &types.Issue{ID: "gc-parent", Status: types.StatusOpen, UpdatedAt: stale}
	// gc-sub is old and open, so the sweep selects it itself, and it also
	// depends on gc-parent, so it is in the failed cascade fetch.
	sub := &types.Issue{ID: "gc-sub", Status: types.StatusOpen, UpdatedAt: stale}
	step := &types.Issue{ID: "gc-step", Status: types.StatusClosed, UpdatedAt: stale}
	expandBoom := errors.New("walking wisp dependents: connection reset")
	fetchBoom := errors.New("loading labels for 2 wisp(s): label table read failed")

	r := failingGCReader{
		cascadeAgeReader: &cascadeAgeReader{
			issues:   []*types.Issue{parent, sub, step},
			children: map[string]bool{sub.ID: true, step.ID: true},
		},
		value:     "keep:mail",
		expandErr: expandBoom,
		fetchErr:  fetchBoom,
	}
	got, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
	if !errors.Is(err, expandBoom) || !errors.Is(err, fetchBoom) {
		t.Errorf("findAbandonedWisps error = %v, want both the expansion and the fetch failure", err)
	}
	ids := map[string]bool{}
	for _, issue := range got {
		ids[issue.ID] = true
	}
	if len(got) != 2 || !ids[parent.ID] || !ids[sub.ID] {
		t.Errorf("selected %v, want exactly the independently selected %s and %s (not %s, reachable only through the failed fetch)",
			ids, parent.ID, sub.ID, step.ID)
	}
}
