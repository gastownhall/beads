package storereadylister

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// recordingPager is the ONLY seam the body can reach — ReadyPager has one
// method — and it records every call, so the tests below pin the role-level
// budget: exactly one pass per listing, whatever the page shape.
type recordingPager struct {
	ready   []string
	calls   []types.WorkFilter
	failure error
}

func (p *recordingPager) GetReadyWorkWithCountsAndTotal(_ context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	p.calls = append(p.calls, filter)
	if p.failure != nil {
		return nil, 0, p.failure
	}
	var out []*types.IssueWithCounts
	for _, id := range p.ready {
		if slices.Contains(filter.ExcludeIDs, id) {
			continue
		}
		out = append(out, &types.IssueWithCounts{Issue: &types.Issue{ID: id}})
	}
	total := len(out)
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	if err := storageops.EnforceMaxRowsCap(len(out), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func ids(items []*types.IssueWithCounts) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func intp(v int) *int { return &v }

// TestListReadyIsOnePass is the role-level statement budget: every listing —
// unlimited, truncated, offset, past the end — is ONE call to the single-pass
// seam, which is what keeps `bd ready --limit N`'s "of M" free. A second call
// (a separate count, a probe row re-query) fails here before it reaches the
// statement-level budget in embeddeddolt.
func TestListReadyIsOnePass(t *testing.T) {
	cases := []struct {
		name      string
		limit     *int
		offset    int
		wantIDs   []string
		wantMore  bool
		wantTotal int64
		wantLimit int
	}{
		{"default limit", nil, 0, []string{"a", "b", "c", "d", "e"}, false, 5, 100},
		{"unlimited", intp(0), 0, []string{"a", "b", "c", "d", "e"}, false, 5, 0},
		{"truncated", intp(2), 0, []string{"a", "b"}, true, 5, 2},
		{"exact", intp(5), 0, []string{"a", "b", "c", "d", "e"}, false, 5, 5},
		{"offset page", intp(2), 2, []string{"c", "d"}, true, 5, 4},
		{"last page", intp(2), 4, []string{"e"}, false, 5, 6},
		{"past the end", intp(2), 9, []string{}, false, 5, 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pager := &recordingPager{ready: []string{"a", "b", "c", "d", "e"}}
			lister, err := New(pager)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lister.ListReady(t.Context(), issueops.ReadyListRequest{
				ReadyRequest: issueops.ReadyRequest{Sort: "priority", Limit: tc.limit, Offset: tc.offset},
			})
			if err != nil {
				t.Fatalf("ListReady: %v", err)
			}
			if len(pager.calls) != 1 {
				t.Fatalf("ListReady made %d seam calls, want exactly 1", len(pager.calls))
			}
			if pager.calls[0].Limit != tc.wantLimit || pager.calls[0].Offset != 0 {
				t.Errorf("seam filter limit/offset = %d/%d, want %d/0 (the page widened past the skipped rows, the skip in Go)",
					pager.calls[0].Limit, pager.calls[0].Offset, tc.wantLimit)
			}
			if !slices.Equal(ids(got.Items), tc.wantIDs) || got.HasMore != tc.wantMore || got.Total != tc.wantTotal {
				t.Errorf("ListReady = %v more=%v total=%d, want %v more=%v total=%d",
					ids(got.Items), got.HasMore, got.Total, tc.wantIDs, tc.wantMore, tc.wantTotal)
			}
		})
	}
}

// TestListReadyCarriesTheRequestOntoTheFilter pins the pass-through: the ready
// question goes through the shared builder and the client cap rides beside it,
// with its attribution, so a refusal reads back the knob that set it.
func TestListReadyCarriesTheRequestOntoTheFilter(t *testing.T) {
	pager := &recordingPager{ready: []string{"a", "b", "c"}}
	lister, _ := New(pager)
	_, err := lister.ListReady(t.Context(), issueops.ReadyListRequest{
		ReadyRequest:  issueops.ReadyRequest{Sort: "oldest", Limit: intp(0), Labels: []string{" x "}, ExcludeIDs: []string{"b"}, IncludeEphemeral: true, Brief: true},
		MaxRows:       1,
		MaxRowsSource: "--max-rows",
	})
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) {
		t.Fatalf("ListReady past the cap: err = %v, want *ErrTooManyRows", err)
	}
	f := pager.calls[0]
	if f.MaxRows != 1 || f.MaxRowsSource != "--max-rows" || f.SortPolicy != types.SortPolicyOldest ||
		!slices.Equal(f.Labels, []string{"x"}) || !slices.Equal(f.ExcludeIDs, []string{"b"}) || !f.IncludeEphemeral || !f.Lite ||
		f.Status != types.StatusOpen {
		t.Errorf("seam filter = %+v, want the built ready filter with the cap attached", f)
	}
}

func TestListReadyRefusesBeforeReading(t *testing.T) {
	pager := &recordingPager{}
	lister, _ := New(pager)
	if _, err := lister.ListReady(t.Context(), issueops.ReadyListRequest{ReadyRequest: issueops.ReadyRequest{Sort: "bogus"}}); !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if len(pager.calls) != 0 {
		t.Fatalf("a refused request reached the seam %d times", len(pager.calls))
	}
	boom := fmt.Errorf("boom")
	pager.failure = boom
	if _, err := lister.ListReady(t.Context(), issueops.ReadyListRequest{ReadyRequest: issueops.ReadyRequest{Sort: "priority"}}); !errors.Is(err, boom) {
		t.Fatalf("seam failure: err = %v, want it passed through", err)
	}
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) = nil error")
	}
}
