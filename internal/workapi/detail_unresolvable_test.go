package workapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// skewSource is a DetailSource whose COUNTS and ROWS are set independently.
//
// The shared fakeStoreReader derives both from one slice, so it can only ever
// produce a store where they agree — which is exactly the state this test has
// to move away from. A real backend derives them from two different queries:
// the counts are a plain COUNT(*) over the dependency tables, while the rows
// are the issues on the far end and silently omit any id with no row in this
// database (issueops.GetDependenciesWithMetadataInTx's `continue`). A
// cross-repo or `external:` target hits that gap by construction, since
// depends_on_external is the one target column carrying no foreign key into
// issues.
type skewSource struct {
	issue *types.Issue

	deps    []*types.IssueWithDependencyMetadata
	depsErr error

	dependents []*types.IssueWithDependencyMetadata

	depCount     int64
	depCountErr  error
	rdepCount    int64
	rdepCountErr error
}

func (s skewSource) GetIssue(_ context.Context, _ string) (*types.Issue, error) { return s.issue, nil }
func (s skewSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return nil, notFound(id)
}
func (s skewSource) Labels(_ context.Context, _ string, _ bool) ([]string, error) { return nil, nil }
func (s skewSource) Dependencies(_ context.Context, _ string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps, s.depsErr
}
func (s skewSource) CountDependencies(_ context.Context, _ string, _ bool) (int64, error) {
	return s.depCount, s.depCountErr
}
func (s skewSource) CountDependents(_ context.Context, _ string, _ bool) (int64, error) {
	return s.rdepCount, s.rdepCountErr
}
func (s skewSource) CountComments(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (s skewSource) IterDependents(_ context.Context, _ string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter(s.dependents), nil
}
func (s skewSource) IterComments(_ context.Context, _ string, _ bool) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter[types.Comment](nil), nil
}

func depRow(id string, t types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{Issue: types.Issue{ID: id}, DependencyType: t}
}

// TestBuildIssueDetails_UnresolvableDependencies pins be-lpi: a dependency
// edge whose target has no row in this database counts toward
// dependency_count and cannot appear in the dependencies slice, and that
// difference must be published rather than left for the caller to infer.
//
// The convoy case that produced the report is the one-edge, zero-row row:
// hq-cv-ftcra carries a single `tracks` edge to
// external:liveop:liveop-kmf, so bd show --json answered
// dependency_count 1 beside dependencies:null and every enumeration path
// agreed the edge did not exist.
func TestBuildIssueDetails_UnresolvableDependencies(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}

	tests := []struct {
		name  string
		src   skewSource
		want  *int64
		count int64
	}{
		{
			name:  "all edges unresolvable",
			src:   skewSource{issue: issue, deps: nil, depCount: 1},
			want:  ptr64(1),
			count: 1,
		},
		{
			name: "one of two edges unresolvable",
			src: skewSource{
				issue:    issue,
				deps:     []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount: 2,
			},
			want:  ptr64(1),
			count: 2,
		},
		{
			// The negative control. A fully local store must be
			// byte-identical to before the fix, or every caller that
			// checks the field starts seeing it on healthy rows.
			name: "fully local, nothing unresolvable",
			src: skewSource{
				issue:    issue,
				deps:     []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCount: 1,
			},
			want:  nil,
			count: 1,
		},
		{
			name:  "no dependencies at all",
			src:   skewSource{issue: issue, depCount: 0},
			want:  nil,
			count: 0,
		},
		{
			// A FAILED read and a SHORT read both leave the slice
			// empty. Only the first must stay silent: reporting it
			// would announce a data property on the strength of a
			// query that never ran.
			name:  "dependency read failed, not merely short",
			src:   skewSource{issue: issue, depsErr: errors.New("backend down"), depCount: 3},
			want:  nil,
			count: 3,
		},
		{
			// Symmetric: a failed COUNT yields 0, and 0 minus a
			// populated slice is negative, but say so explicitly
			// rather than relying on the sign.
			name: "count read failed",
			src: skewSource{
				issue:       issue,
				deps:        []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				depCountErr: errors.New("backend down"),
			},
			want:  nil,
			count: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details, err := BuildIssueDetails(ctx, tc.src, issue, false, DetailOptions{})
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			if details.DependencyCount == nil || *details.DependencyCount != tc.count {
				t.Errorf("DependencyCount = %v, want %d", details.DependencyCount, tc.count)
			}
			switch {
			case tc.want == nil && details.UnresolvableDependencies != nil:
				t.Errorf("UnresolvableDependencies = %d, want unset", *details.UnresolvableDependencies)
			case tc.want != nil && details.UnresolvableDependencies == nil:
				t.Errorf("UnresolvableDependencies unset, want %d", *tc.want)
			case tc.want != nil && *details.UnresolvableDependencies != *tc.want:
				t.Errorf("UnresolvableDependencies = %d, want %d", *details.UnresolvableDependencies, *tc.want)
			}
		})
	}
}

// TestBuildIssueDetails_UnresolvableDependents pins the inbound half, and in
// particular that it stays silent in count-only mode. Dependents is nil by
// design without IncludeDependents (be-ijck6q), so a delta taken against it
// there would report every dependent an issue has as unresolvable.
func TestBuildIssueDetails_UnresolvableDependents(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	src := skewSource{issue: issue, dependents: nil, rdepCount: 2}

	countOnly, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails count-only: %v", err)
	}
	if countOnly.UnresolvableDependents != nil {
		t.Errorf("count-only UnresolvableDependents = %d, want unset — the rows were never fetched",
			*countOnly.UnresolvableDependents)
	}

	included, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{IncludeDependents: true})
	if err != nil {
		t.Fatalf("BuildIssueDetails include-dependents: %v", err)
	}
	if included.UnresolvableDependents == nil || *included.UnresolvableDependents != 2 {
		t.Errorf("UnresolvableDependents = %v, want 2", included.UnresolvableDependents)
	}

	// Negative control on the same path: every dependent resolvable.
	local := skewSource{
		issue:      issue,
		dependents: []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
		rdepCount:  1,
	}
	got, err := BuildIssueDetails(ctx, local, issue, false, DetailOptions{IncludeDependents: true})
	if err != nil {
		t.Fatalf("BuildIssueDetails local: %v", err)
	}
	if got.UnresolvableDependents != nil {
		t.Errorf("UnresolvableDependents = %d on a fully local store, want unset", *got.UnresolvableDependents)
	}
}

// mutatingSource models a write landing BETWEEN the two reads
// BuildIssueDetails makes: whichever of Dependencies/CountDependencies runs
// first observes one edge, and whichever runs second observes two.
//
// This is what makes the ordering testable at all. A source returning fixed
// values cannot distinguish the two orders — the first version of this test
// used one, and reverting the fix under it still passed, which is a test that
// pins arithmetic while claiming to pin an order.
type mutatingSource struct {
	issue  *types.Issue
	edges  int
	before int
}

func (m *mutatingSource) observe() int {
	if m.before == 0 {
		m.before = 1
		return m.edges // the first reader sees the pre-write state
	}
	return m.edges + 1 // by the second read, an edge has been added
}

func (m *mutatingSource) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return m.issue, nil
}
func (m *mutatingSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return nil, notFound(id)
}
func (m *mutatingSource) Labels(_ context.Context, _ string, _ bool) ([]string, error) {
	return nil, nil
}
func (m *mutatingSource) Dependencies(_ context.Context, _ string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	n := m.observe()
	out := make([]*types.IssueWithDependencyMetadata, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, depRow(fmt.Sprintf("rp-%d", i+2), types.DepBlocks))
	}
	return out, nil
}
func (m *mutatingSource) CountDependencies(_ context.Context, _ string, _ bool) (int64, error) {
	return int64(m.observe()), nil
}
func (m *mutatingSource) CountDependents(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (m *mutatingSource) CountComments(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (m *mutatingSource) IterDependents(_ context.Context, _ string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter[types.IssueWithDependencyMetadata](nil), nil
}
func (m *mutatingSource) IterComments(_ context.Context, _ string, _ bool) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter[types.Comment](nil), nil
}

// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable pins the
// ORDER of the two reads, which is the only defence the store-backed source
// has against a write landing between them. (The UOW source runs the whole
// build inside one read transaction and needs none.)
//
// The count is read FIRST, so an edge added in the window leaves it
// stale-LOW: the difference goes negative and nothing is reported. Read
// afterwards it would be stale-HIGH, and a freshly added, perfectly ordinary
// local edge would be published as unresolvable — a false claim about the
// data, which is the expensive direction. Found by a codex review of the
// first cut of this change.
//
// The source above mutates between calls, so swapping the two reads in
// BuildIssueDetails turns this red. That is the control; without it the case
// passes under either order.
func TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	src := &mutatingSource{issue: issue, edges: 1}

	details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}
	if details.UnresolvableDependencies != nil {
		t.Errorf("UnresolvableDependencies = %d, want unset — a count read before a concurrent add is stale-low, not evidence of an unrenderable edge; are the two reads still in count-then-listing order?",
			*details.UnresolvableDependencies)
	}
}

func ptr64(v int64) *int64 { return &v }
