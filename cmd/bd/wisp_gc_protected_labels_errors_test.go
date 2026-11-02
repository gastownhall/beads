package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// failingGCReader is a cascadeAgeReader whose setting read and initial search
// can be made to fail. value is what GetConfig answers when configErr is nil.
type failingGCReader struct {
	*cascadeAgeReader
	value     string
	configErr error
	searchErr error
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
