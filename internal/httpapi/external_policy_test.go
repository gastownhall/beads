package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// externalEdgesProvider is the claim fake with an `external:` dependency plane,
// so the external-capability policy has something to hold an issue back with.
type externalEdgesProvider struct {
	*fakeProvider
	edges map[string][]*types.Dependency
	// reads counts reads of the whole workspace's external edges; ownReads,
	// when set, reads of named issues' own records, which is all a guard on
	// one issue makes.
	reads    *int
	ownReads *int
	// ready, when set, answers the ready-shaped use-case methods, so the
	// served ready list, count and claim-next have a front to read.
	ready *readyIssues
}

func (p externalEdgesProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	u, err := p.fakeProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return externalEdgesUOW{UnitOfWork: u, p: p}, nil
}

type externalEdgesUOW struct {
	uow.UnitOfWork
	p externalEdgesProvider
}

func (u externalEdgesUOW) IssueUseCase() domain.IssueUseCase {
	if u.p.ready != nil {
		return u.p.ready
	}
	return u.UnitOfWork.IssueUseCase()
}

func (u externalEdgesUOW) CommentUseCase() domain.CommentUseCase { return noComments{} }

type noComments struct{ domain.CommentUseCase }

func (noComments) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (d externalEdgesDeps) CountsByIssueIDs(context.Context, []string) (map[string]*types.DependencyCounts, error) {
	return map[string]*types.DependencyCounts{}, nil
}

func (d externalEdgesDeps) GetForIssueIDs(context.Context, []string) (map[string][]*types.Dependency, error) {
	return map[string][]*types.Dependency{}, nil
}

// readyIssues is a tiny ready front: every row is ready unless a filter's
// ExcludeIDs names it, which is the only way the policy narrows one.
type readyIssues struct {
	*fakeIssues
	rows []*types.Issue
}

func (u *readyIssues) front(filter types.WorkFilter) []*types.Issue {
	var out []*types.Issue
	for _, row := range u.rows {
		if !slices.Contains(filter.ExcludeIDs, row.ID) && row.Assignee == "" {
			out = append(out, row)
		}
	}
	return out
}

func (u *readyIssues) WakeExpiredDefers(context.Context) (int, int, error) { return 0, 0, nil }

func (u *readyIssues) GetReadyWorkWithCounts(_ context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	page := domain.SearchCountsPage{Items: []*types.IssueWithCounts{}}
	for _, row := range u.front(filter) {
		if filter.Limit > 0 && len(page.Items) == filter.Limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, &types.IssueWithCounts{Issue: row})
	}
	return page, nil
}

func (u *readyIssues) ClaimReadyIssue(_ context.Context, filter types.WorkFilter, actor string) (domain.ClaimReadyResult, error) {
	for _, row := range u.front(filter) {
		row.Assignee, row.Status = actor, types.StatusInProgress
		return domain.ClaimReadyResult{Issue: row, Claimed: true}, nil
	}
	return domain.ClaimReadyResult{}, nil
}

func (u externalEdgesUOW) DependencyUseCase() domain.DependencyUseCase {
	return externalEdgesDeps{DependencyUseCase: u.UnitOfWork.DependencyUseCase(), p: u.p}
}

type externalEdgesDeps struct {
	domain.DependencyUseCase
	p externalEdgesProvider
}

func (d externalEdgesDeps) GetExternalBlockingDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	*d.p.reads++
	return d.p.edges, nil
}

// GetIssueDependencyRecords answers ids' own records, as the real query does:
// on this fake an issue's only records are its `external:` edges.
func (d externalEdgesDeps) GetIssueDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	if d.p.ownReads != nil {
		*d.p.ownReads++
	}
	out := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		out[id] = d.p.edges[id]
	}
	return out, nil
}

// servedPolicyProvider wraps the fake in the external-capability policy exactly
// as `bd serve` wraps its provider (wireExternalDependencyUOWProvider). The
// locator resolves no project, so every `external:` blocker is unsatisfied.
func servedPolicyProvider(issues *fakeIssues, edges map[string][]*types.Dependency, reads, ownReads *int) uow.UnitOfWorkProvider {
	inner := externalEdgesProvider{fakeProvider: &fakeProvider{issues: issues}, edges: edges, reads: reads, ownReads: ownReads}
	return externaldeps.WrapUOWProvider(inner, func(externaldeps.ProjectName) (string, bool) { return "", false }, nil)
}

// TestServedClaimRefusesExternallyBlockedWork pins the claim endpoint on serve's
// PROVIDER arm: an issue held back by an unsatisfied `external:` blocker is
// refused before the compare-and-set runs, as the store arm and `bd update
// --claim` refuse it. The policy reads the claimed issue's own edges TWICE for
// the request — once before the claim's write transaction, to resolve the
// foreign projects with no transaction open, and once inside it, so an edge
// committed in between is still seen (externaldeps.preResolved) — and never
// the whole workspace's.
func TestServedClaimRefusesExternallyBlockedWork(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "", types.StatusOpen)}
	reads, ownReads := 0, 0
	provider := servedPolicyProvider(issues, map[string][]*types.Dependency{
		"bd-1": {{IssueID: "bd-1", DependsOnID: "external:remote:payments", Type: types.DepBlocks}},
	}, &reads, &ownReads)
	ts := newTestServer(t, Config{Provider: provider})

	resp := ts.claim(t, claimPath, `{"actor":"alice"}`)
	body := readAll(t, resp)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("served claim of an externally blocked issue succeeded: %s", body)
	}
	if n := len(issues.claimed()); n != 0 {
		t.Fatalf("the compare-and-set ran %d times for an externally blocked issue, want 0", n)
	}
	// A claim refusal, not a close one: 409 not_claimable with a detail about
	// the blocker, never not_closable's "close with force" advice.
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"code":"not_claimable"`) ||
		!strings.Contains(body, "blocked by an unsatisfied dependency") || strings.Contains(body, "force") {
		t.Errorf("served refusal = %d %s, want 409 not_claimable naming the blocker", resp.StatusCode, body)
	}
	if ownReads != 2 || reads != 0 {
		t.Errorf("the request read the claimed issue's edges %d times and the workspace's %d times, want exactly 2 (pre-resolution, then in the claim's transaction) and 0", ownReads, reads)
	}

	// The same server claims the issue once the blocker is gone, so the
	// refusal above is the policy's and not the fake's.
	unblocked := &fakeIssues{issue: seededIssue("bd-1", "", types.StatusOpen)}
	ts = newTestServer(t, Config{Provider: servedPolicyProvider(unblocked, nil, &reads, nil)})
	if resp := ts.claim(t, claimPath, `{"actor":"alice"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("served claim of an unblocked issue: status %d: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestServedReadyRolesApplyThePolicyOncePerRequest pins serve's PROVIDER arm to
// the same role-level policy the CLI uses: the ready list, the ready count and
// claim-next each exclude the externally blocked issue and read the external
// edges exactly ONCE per request — the role wrapper reads them, and the role it
// delegates to runs beneath the policy's use-case overrides rather than
// through them, so nothing applies the policy a second time.
func TestServedReadyRolesApplyThePolicyOncePerRequest(t *testing.T) {
	reads := 0
	held := seededIssue("bd-held", "", types.StatusOpen)
	free := seededIssue("bd-free", "", types.StatusOpen)
	ready := &readyIssues{fakeIssues: &fakeIssues{issue: free}, rows: []*types.Issue{held, free}}
	inner := externalEdgesProvider{
		fakeProvider: &fakeProvider{issues: ready.fakeIssues},
		edges: map[string][]*types.Dependency{
			held.ID: {{IssueID: held.ID, DependsOnID: "external:remote:payments", Type: types.DepBlocks}},
		},
		reads: &reads,
		ready: ready,
	}
	provider := externaldeps.WrapUOWProvider(inner, func(externaldeps.ProjectName) (string, bool) { return "", false }, nil)
	ts := newTestServer(t, Config{Provider: provider})

	for _, tc := range []struct {
		name string
		do   func() *http.Response
	}{
		{"ready list", func() *http.Response { return ts.get(t, "/v0/beads/ready") }},
		{"ready count", func() *http.Response { return ts.get(t, "/v0/beads/ready:count") }},
		{"claim next", func() *http.Response { return ts.claimNext(t, claimNextPath, `{"actor":"poller"}`) }},
	} {
		reads = 0
		resp := tc.do()
		body := readAll(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.name, resp.StatusCode, body)
		}
		if strings.Contains(body, held.ID) {
			t.Errorf("%s answered with the externally blocked %s: %s", tc.name, held.ID, body)
		}
		if reads != 1 {
			t.Errorf("%s read the external edges %d times, want exactly 1", tc.name, reads)
		}
	}
	if held.Assignee != "" {
		t.Errorf("claim-next claimed the externally blocked %s", held.ID)
	}
	if free.Assignee != "poller" {
		t.Errorf("claim-next left %s assigned to %q, want poller", free.ID, free.Assignee)
	}
}

// rewrappingProvider is a decorating provider whose ReadyClaimer accessor hands
// back its OWN role, and which records the inner provider every Rewrap put
// beneath it.
type rewrappingProvider struct {
	uow.UnitOfWorkProvider
	claimer  *roleReadyClaimer
	rewraps  *[]uow.UnitOfWorkProvider
	accessed *int
}

func (p rewrappingProvider) Unwrap() uow.UnitOfWorkProvider { return p.UnitOfWorkProvider }

func (p rewrappingProvider) Rewrap(inner uow.UnitOfWorkProvider) uow.UnitOfWorkProvider {
	*p.rewraps = append(*p.rewraps, inner)
	p.UnitOfWorkProvider = inner
	return p
}

func (p rewrappingProvider) ReadyClaimer() (issueops.ReadyClaimer, error) {
	*p.accessed++
	return p.claimer, nil
}

// TestProviderArmReachesRolesThroughTheDecoratorsAccessors pins how serve's
// provider arm builds a role now: through the configured provider's OWN
// accessor — so a decorator such as the external-dependency policy applies its
// role-level layer, as it does for the CLI — with the per-request timing
// provider slid BENEATH that decorator, so the units of work the role opens
// are still timed into the request's log line.
func TestProviderArmReachesRolesThroughTheDecoratorsAccessors(t *testing.T) {
	var rewraps []uow.UnitOfWorkProvider
	accessed := 0
	claimer := &roleReadyClaimer{}
	provider := rewrappingProvider{UnitOfWorkProvider: &fakeProvider{}, claimer: claimer, rewraps: &rewraps, accessed: &accessed}
	ts := newTestServer(t, Config{Provider: provider})

	if resp := ts.claimNext(t, claimNextPath, `{"actor":"poller"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if accessed != 1 || len(claimer.claimNextRequests()) != 1 {
		t.Fatalf("decorator accessor called %d times, its role %d times; want the request served by the decorator's own role",
			accessed, len(claimer.claimNextRequests()))
	}
	if len(rewraps) != 1 {
		t.Fatalf("Rewrap called %d times, want once per request", len(rewraps))
	}
	if _, ok := rewraps[0].(timedProvider); !ok {
		t.Fatalf("the provider beneath the decorator is %T, want the request's timedProvider", rewraps[0])
	}
}

// TestExternalDependencyCapabilityFollowsTheConfig pins U7's wire half: GET
// /v0/beads/context advertises policy.external_dependencies exactly when the
// Config says the served roles carry the policy, and a server built without it
// — the zero Config every embedder starts from — does not. The rest of the list
// is the build-level Capabilities() either way.
func TestExternalDependencyCapabilityFollowsTheConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		on   bool
	}{
		{"composed", true},
		{"not composed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, Config{ExternalDependencyPolicy: tc.on})
			raw, _ := decodeBody(t, ts.get(t, "/v0/beads/context"))["capabilities"].([]any)
			var got []string
			for _, c := range raw {
				got = append(got, c.(string))
			}
			if has := slices.Contains(got, "policy.external_dependencies"); has != tc.on {
				t.Errorf("advertises policy.external_dependencies = %v, want %v (capabilities %v)", has, tc.on, got)
			}
			want := slices.Clone(Capabilities())
			if tc.on {
				want = append(want, "policy.external_dependencies")
				slices.Sort(want)
			}
			if !slices.Equal(got, want) {
				t.Errorf("capabilities = %v, want %v", got, want)
			}
			if !slices.IsSorted(got) {
				t.Errorf("capabilities = %v, want them sorted", got)
			}
		})
	}
}
