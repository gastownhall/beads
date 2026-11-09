package workapi

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestBuildIssueDetails_UnresolvableDependencyEdges pins the raw-edge half of
// the unresolvable disclosure (bd-hkpas7, #5272): where the count-only
// UnresolvableDependencies said HOW MANY edges the hydrated listing dropped,
// UnresolvedDepEdges names them. The member is the stored record, sorted by
// target then type, gated on the same successful count/list reads as the
// count, and silent on a failed raw read.
func TestBuildIssueDetails_UnresolvableDependencyEdges(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	boom := errors.New("backend down")

	depEdge := func(target string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: "rp-1", DependsOnID: target, Type: typ}
	}

	t.Run("all edges unresolvable, sorted by target then type", func(t *testing.T) {
		raw := []*types.Dependency{
			depEdge("rp-x", types.DepBlocks),
			depEdge("other-y", types.DepRelated),
			depEdge("other-y", types.DepBlocks),
			depEdge("external:liveop:liveop-kmf", types.DepTracks),
		}
		src := skewSource{issue: issue, rawDeps: raw, depCount: 4}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if len(details.UnresolvableDependencyEdges) != 4 {
			t.Fatalf("UnresolvableDependencyEdges = %v, want 4 rows", details.UnresolvableDependencyEdges)
		}
		want := []struct {
			target string
			typ    types.DependencyType
		}{
			{"external:liveop:liveop-kmf", types.DepTracks},
			{"other-y", types.DepBlocks},
			{"other-y", types.DepRelated},
			{"rp-x", types.DepBlocks},
		}
		for i, w := range want {
			got := details.UnresolvableDependencyEdges[i]
			if got.DependsOnID != w.target || got.Type != w.typ {
				t.Errorf("edge[%d] = %s/%s, want %s/%s", i, got.DependsOnID, got.Type, w.target, w.typ)
			}
		}
		// The input slice is the caller's and must survive unsorted.
		if raw[0].DependsOnID != "rp-x" || raw[1].DependsOnID != "other-y" {
			t.Errorf("BuildIssueDetails mutated the raw record slice: %v", raw)
		}
	})

	t.Run("hydrated local rows stay in Dependencies and out of the member", func(t *testing.T) {
		src := skewSource{
			issue: issue,
			deps:  []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
			rawDeps: []*types.Dependency{
				depEdge("rp-2", types.DepBlocks),
				depEdge("other-x", types.DepBlocks),
			},
			depCount: 2,
		}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if len(details.Dependencies) != 1 || details.Dependencies[0].ID != "rp-2" {
			t.Errorf("Dependencies = %v, want the one hydrated local row", details.Dependencies)
		}
		if len(details.UnresolvableDependencyEdges) != 1 ||
			details.UnresolvableDependencyEdges[0].DependsOnID != "other-x" {
			t.Errorf("UnresolvableDependencyEdges = %v, want only the other-x edge",
				details.UnresolvableDependencyEdges)
		}
		if details.UnresolvableDependencies == nil || *details.UnresolvableDependencies != 1 {
			t.Errorf("UnresolvableDependencies = %v, want 1", details.UnresolvableDependencies)
		}
	})

	t.Run("failed raw read keeps the count alone", func(t *testing.T) {
		src := skewSource{issue: issue, rawDepsErr: boom, depCount: 2}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.UnresolvableDependencies == nil || *details.UnresolvableDependencies != 2 {
			t.Errorf("UnresolvableDependencies = %v, want the count to survive a failed raw read", details.UnresolvableDependencies)
		}
		if details.UnresolvableDependencyEdges != nil {
			t.Errorf("UnresolvableDependencyEdges = %v on a failed raw read, want unset", details.UnresolvableDependencyEdges)
		}
	})

	// No-delta issues must not pay the extra read at all (design: "Fully
	// local/no-edge cases incur no extra record read"). countingSource wraps a
	// source and counts DependencyRecords calls.
	t.Run("no raw read without a positive delta", func(t *testing.T) {
		local := skewSource{issue: issue, deps: []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)}, depCount: 1}
		counting := &countingSource{src: local}
		if _, err := BuildIssueDetails(ctx, counting, issue, false, DetailOptions{}); err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if counting.calls != 0 {
			t.Errorf("DependencyRecords called %d times on a fully local issue, want 0", counting.calls)
		}

		// A failed count read never establishes a gap, and neither does a
		// failed listing: both must stay off the raw path for the same reason
		// they leave the count unset.
		failedCount := &countingSource{src: skewSource{issue: issue, depCountErr: boom, depCount: 3}}
		if _, err := BuildIssueDetails(ctx, failedCount, issue, false, DetailOptions{}); err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if failedCount.calls != 0 {
			t.Errorf("DependencyRecords called %d times beside a failed count read, want 0", failedCount.calls)
		}
		failedList := &countingSource{src: skewSource{issue: issue, depsErr: boom, depCount: 3}}
		if _, err := BuildIssueDetails(ctx, failedList, issue, false, DetailOptions{}); err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if failedList.calls != 0 {
			t.Errorf("DependencyRecords called %d times beside a failed listing, want 0", failedList.calls)
		}
	})

	t.Run("brief deps trims the hydrated rows, not the raw edges", func(t *testing.T) {
		heavy := types.Issue{ID: "rp-2", Title: "Local", Description: "long body"}
		src := skewSource{
			issue: issue,
			deps:  []*types.IssueWithDependencyMetadata{{Issue: heavy, DependencyType: types.DepBlocks}},
			rawDeps: []*types.Dependency{
				depEdge("rp-2", types.DepBlocks),
				depEdge("external:liveop:liveop-kmf", types.DepTracks),
			},
			depCount: 2,
		}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{BriefDeps: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if len(details.Dependencies) != 1 || details.Dependencies[0].Description != "" {
			t.Errorf("BriefDeps left free-form text on the hydrated row: %+v", details.Dependencies)
		}
		if len(details.UnresolvableDependencyEdges) != 1 ||
			details.UnresolvableDependencyEdges[0].DependsOnID != "external:liveop:liveop-kmf" {
			t.Errorf("BriefDeps dropped the raw unresolvable edge: %v", details.UnresolvableDependencyEdges)
		}
	})

	// The UOW adapter routes the raw read by the SAME plane flag the rest of
	// the source does. A plane mix-up here would publish one plane's edges
	// under another's detail view, which no count would ever surface.
	t.Run("uow adapter routes the raw read by plane", func(t *testing.T) {
		raw := []*types.Dependency{depEdge("external:liveop:liveop-kmf", types.DepTracks)}
		for _, wisp := range []bool{false, true} {
			deps := &planeDepUC{wisp: wisp, raw: raw}
			src := newUseCaseDetailSource(
				staticIssueUC{issue: issue, isWisp: wisp},
				staticLabelUC{},
				deps,
				staticCommentUC{},
			)
			details, err := BuildIssueDetails(ctx, src, issue, wisp, DetailOptions{})
			if err != nil {
				t.Fatalf("BuildIssueDetails(wisp=%t): %v", wisp, err)
			}
			if deps.issueCalls+deps.wispCalls != 1 {
				t.Errorf("wisp=%t: issue-plane calls=%d, wisp-plane calls=%d, want exactly one read on the named plane",
					wisp, deps.issueCalls, deps.wispCalls)
			}
			if (wisp && deps.issueCalls != 0) || (!wisp && deps.wispCalls != 0) {
				t.Errorf("wisp=%t: raw read went to the wrong plane (issue=%d wisp=%d)", wisp, deps.issueCalls, deps.wispCalls)
			}
			if len(details.UnresolvableDependencyEdges) != 1 {
				t.Errorf("wisp=%t: UnresolvableDependencyEdges = %v, want the seeded edge", wisp, details.UnresolvableDependencyEdges)
			}
		}
	})
}

// countingSource wraps a DetailSource and counts DependencyRecords calls, the
// way a spy double does: the wrapped source answers everything, the wrapper
// only observes the one read this test cares about.
type countingSource struct {
	src   DetailSource
	calls int
}

func (c *countingSource) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return c.src.GetIssue(ctx, id)
}
func (c *countingSource) GetWisp(ctx context.Context, id string) (*types.Issue, error) {
	return c.src.GetWisp(ctx, id)
}
func (c *countingSource) Labels(ctx context.Context, id string, isWisp bool) ([]string, error) {
	return c.src.Labels(ctx, id, isWisp)
}
func (c *countingSource) Dependencies(ctx context.Context, id string, isWisp bool) ([]*types.IssueWithDependencyMetadata, error) {
	return c.src.Dependencies(ctx, id, isWisp)
}
func (c *countingSource) DependencyRecords(ctx context.Context, id string, isWisp bool) ([]*types.Dependency, error) {
	c.calls++
	return c.src.DependencyRecords(ctx, id, isWisp)
}
func (c *countingSource) CountDependencies(ctx context.Context, id string, isWisp bool) (int64, error) {
	return c.src.CountDependencies(ctx, id, isWisp)
}
func (c *countingSource) CountDependents(ctx context.Context, id string, isWisp bool) (int64, error) {
	return c.src.CountDependents(ctx, id, isWisp)
}
func (c *countingSource) CountComments(ctx context.Context, id string, isWisp bool) (int64, error) {
	return c.src.CountComments(ctx, id, isWisp)
}
func (c *countingSource) IterDependents(ctx context.Context, id string, isWisp bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return c.src.IterDependents(ctx, id, isWisp)
}
func (c *countingSource) IterComments(ctx context.Context, id string, isWisp bool) (storage.Iter[types.Comment], error) {
	return c.src.IterComments(ctx, id, isWisp)
}

// planeDepUC answers only the three calls a positive-delta build makes on the
// dependency plane, and records which plane each went to. Anything else is a
// test bug: this fake exists to pin routing, not to fake data.
type planeDepUC struct {
	wisp       bool
	raw        []*types.Dependency
	issueCalls int
	wispCalls  int
}

func (p *planeDepUC) ListWithIssueMetadata(context.Context, string, domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (p *planeDepUC) ListWispWithIssueMetadata(context.Context, string, domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (p *planeDepUC) IterWithIssueMetadata(context.Context, string, domain.DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter[types.IssueWithDependencyMetadata](nil), nil
}
func (p *planeDepUC) IterWispWithIssueMetadata(context.Context, string, domain.DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter[types.IssueWithDependencyMetadata](nil), nil
}
func (p *planeDepUC) CountByIssueID(context.Context, string, domain.DepListFilter) (int64, error) {
	if !p.wisp {
		return int64(len(p.raw)), nil
	}
	return 0, nil
}
func (p *planeDepUC) CountByWispID(context.Context, string, domain.DepListFilter) (int64, error) {
	if p.wisp {
		return int64(len(p.raw)), nil
	}
	return 0, nil
}
func (p *planeDepUC) GetIssueDependencyRecords(_ context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	p.issueCalls++
	return map[string][]*types.Dependency{issueIDs[0]: p.raw}, nil
}
func (p *planeDepUC) GetWispDependencyRecords(_ context.Context, wispIDs []string) (map[string][]*types.Dependency, error) {
	p.wispCalls++
	return map[string][]*types.Dependency{wispIDs[0]: p.raw}, nil
}

// staticIssueUC, staticLabelUC and staticCommentUC are the minimal answers the
// plane-routing build needs from the other three use cases: everything the
// detail view reads, and nothing it does not.
type staticIssueUC struct {
	issue  *types.Issue
	isWisp bool
}

func (s staticIssueUC) GetIssue(context.Context, string) (*types.Issue, error) { return s.issue, nil }
func (s staticIssueUC) GetWisp(context.Context, string) (*types.Issue, error) {
	return s.issue, nil
}

type staticLabelUC struct{}

func (staticLabelUC) GetLabels(context.Context, string) ([]string, error) { return nil, nil }
func (staticLabelUC) GetWispLabels(context.Context, string) ([]string, error) {
	return nil, nil
}

type staticCommentUC struct{}

func (staticCommentUC) CountCommentsForIssue(context.Context, string) (int64, error) {
	return 0, nil
}
func (staticCommentUC) CountCommentsForWisp(context.Context, string) (int64, error) {
	return 0, nil
}
func (staticCommentUC) IterCommentsForIssue(context.Context, string) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter[types.Comment](nil), nil
}
func (staticCommentUC) IterCommentsForWisp(context.Context, string) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter[types.Comment](nil), nil
}
