//go:build cgo

package httpclient

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// TestServedReadyExcludesUnsatisfiedExternalDependencyWithoutServerCapability
// reproduces the S6 review's HIGH-1 regression: cmd/bd's
// wireExternalDependencyPolicy used to skip wrapping ANY registered remote
// backend outright (storage.RemoteBackendStore), so an issue blocked by an
// external:<project>:<capability> dependency was listed by `bd ready` over
// http even though the identical workspace against a local backend would
// exclude it — the policy was silently skipped, not deferred to a server that
// claimed to enforce it (design 3.6).
//
// This pins the fix at the layer design 3.6 actually gates on: the decorator
// must still apply client-side enforcement whenever the server's handshake
// does not advertise wire.CapExternalDependencies. Today's OSS httpapi never
// advertises it (confirmed below), so this also exercises the http backend's
// GetAllDependencyRecords fallback (loadBlockingState's compatibility path)
// against a real served server, end to end.
func TestServedReadyExcludesUnsatisfiedExternalDependencyWithoutServerCapability(t *testing.T) {
	env := newServedEnv(t, "hixd")
	ctx := t.Context()

	blockedID := env.prefix + "-blocked"
	freeID := env.prefix + "-free"
	if err := env.createIssue(ctx, &types.Issue{
		ID: blockedID, Title: "blocked by an external capability",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("seed blocked issue: %v", err)
	}
	if err := env.createIssue(ctx, &types.Issue{
		ID: freeID, Title: "not blocked by anything",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("seed free issue: %v", err)
	}
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID:     blockedID,
		DependsOnID: "external:otherproj:cap",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed external dependency: %v", err)
	}

	// The premise this test pins: the server this harness serves does not
	// advertise wire.CapExternalDependencies. If a future change makes OSS
	// httpapi advertise it, this test's exclusion no longer proves the
	// client-side path and must be re-pointed at a server that genuinely
	// withholds the capability.
	if enforced, err := env.subject.ServerEnforcesExternalDependencyPolicy(ctx); err != nil {
		t.Fatalf("ServerEnforcesExternalDependencyPolicy: %v", err)
	} else if enforced {
		t.Fatal("served test server unexpectedly advertises wire.CapExternalDependencies; this test's no-server-enforcement premise no longer holds")
	}

	// externaldeps.New with nil locate/open funcs is the same fail-closed
	// shape cmd/bd's wireExternalDependencyPolicy composes for an
	// unconfigured external project: a reference to a project this workspace
	// has not configured resolves to "unsatisfied", so the dependency keeps
	// blocking rather than silently passing.
	decorated := externaldeps.New(env.subject, nil, nil)

	filter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	rows, err := decorated.GetReadyWorkWithCounts(ctx, filter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts over http: %v", err)
	}
	var ids []string
	for _, row := range rows {
		if row != nil && row.Issue != nil {
			ids = append(ids, row.ID)
		}
	}
	for _, id := range ids {
		if id == blockedID {
			t.Errorf("ready over http = %v, want %q excluded by its unsatisfied external:otherproj:cap dependency", ids, blockedID)
		}
	}
	var sawFree bool
	for _, id := range ids {
		if id == freeID {
			sawFree = true
		}
	}
	if !sawFree {
		t.Errorf("ready over http = %v, want unrelated issue %q present", ids, freeID)
	}

	// The total `bd ready --json` prints beside the page, and the count
	// text-mode `bd ready` prints, must exclude the same issue. This store
	// cannot express ExcludeIDs, so both come from the decorator's client-side
	// drop, which is where the exclusion has to be subtracted.
	_, total, err := decorated.GetReadyWorkWithCountsAndTotal(ctx, filter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsAndTotal over http: %v", err)
	}
	count, err := decorated.CountReadyWork(ctx, filter)
	if err != nil {
		t.Fatalf("CountReadyWork over http: %v", err)
	}
	if total != len(ids) || count != len(ids) {
		t.Errorf("ready over http lists %v, but total = %d and CountReadyWork = %d; want both %d (%q excluded)", ids, total, count, len(ids), blockedID)
	}
}

// TestServedReadyCountExcludesAnExternallyBlockedIssuePastTheDefaultPage pins,
// against a real served server, the shape the decorator once misread: an
// unlimited ready listing over http comes back as the server's default page
// (the wire omits a zero limit) while its total counts the whole ready set.
// Text-mode `bd ready` counts through CountReadyWork, which always fetches
// unlimited, so an externally blocked issue sorted past that page stayed in
// its count when the decorator took the zero limit for a window holding every
// row.
func TestServedReadyCountExcludesAnExternallyBlockedIssuePastTheDefaultPage(t *testing.T) {
	env := newServedEnv(t, "hixp")
	ctx := t.Context()

	free := make([]*types.Issue, 0, workapi.DefaultReadyLimit+1)
	for i := range workapi.DefaultReadyLimit + 1 {
		free = append(free, &types.Issue{
			ID: fmt.Sprintf("%s-free%03d", env.prefix, i), Title: "not blocked by anything",
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		})
	}
	if err := env.reference.CreateIssues(ctx, free, "tester"); err != nil {
		t.Fatalf("seed free issues: %v", err)
	}
	// Priority 4 sorts the blocked issue after every free one, past the
	// default page.
	blockedID := env.prefix + "-blocked"
	if err := env.createIssue(ctx, &types.Issue{
		ID: blockedID, Title: "blocked by an external capability",
		Status: types.StatusOpen, Priority: 4, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("seed blocked issue: %v", err)
	}
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID:     blockedID,
		DependsOnID: "external:otherproj:cap",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed external dependency: %v", err)
	}

	countFilter, err := workapi.BuildReadyCountFilter(issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("BuildReadyCountFilter: %v", err)
	}
	// The premise this test pins: undecorated, the unlimited listing is the
	// default page, the blocked issue is not on it, and the total counts it.
	rows, total, err := env.subject.GetReadyWorkWithCountsAndTotal(ctx, countFilter)
	if err != nil {
		t.Fatalf("undecorated GetReadyWorkWithCountsAndTotal over http: %v", err)
	}
	if len(rows) != workapi.DefaultReadyLimit || total != workapi.DefaultReadyLimit+2 {
		t.Fatalf("undecorated unlimited listing over http = %d rows, total %d; want the %d-row default page, total %d; this test's premise no longer holds",
			len(rows), total, workapi.DefaultReadyLimit, workapi.DefaultReadyLimit+2)
	}
	for _, row := range rows {
		if row != nil && row.Issue != nil && row.ID == blockedID {
			t.Fatalf("%q is on the default page; this test needs it past the page", blockedID)
		}
	}

	decorated := externaldeps.New(env.subject, nil, nil)
	const want = workapi.DefaultReadyLimit + 1 // every free issue
	count, err := decorated.CountReadyWork(ctx, countFilter)
	if err != nil {
		t.Fatalf("CountReadyWork over http: %v", err)
	}
	pageFilter, err := workapi.BuildReadyFilter(issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	pageRows, pageTotal, err := decorated.GetReadyWorkWithCountsAndTotal(ctx, pageFilter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsAndTotal over http: %v", err)
	}
	if count != want || pageTotal != want || len(pageRows) != workapi.DefaultReadyLimit {
		t.Errorf("over http: CountReadyWork = %d; default page = %d rows, total %d; want count and total %d (%q excluded), %d rows",
			count, len(pageRows), pageTotal, want, blockedID, workapi.DefaultReadyLimit)
	}
}

// TestServedRolesKeepTheExternalDependencyPolicy pins, against a real served
// server, the roles the decorator composes over a remote store. Each one
// answers through httpclient.Store's served roles: the base accessors built
// them over legacy methods that store refuses, which failed `bd list`,
// `bd show`, `bd dep tree`, `bd ready --claim` and `bd close` even for an issue
// nothing blocks. And each still holds the externally blocked issue out of
// ready work, claims and closes. The served handler here binds its roles to
// the raw reference store, which knows nothing of the policy, so every
// exclusion below is the client's.
func TestServedRolesKeepTheExternalDependencyPolicy(t *testing.T) {
	env := newServedEnv(t, "hixr")
	ctx := t.Context()

	const externalRef = "external:otherproj:cap"
	blockedID := env.prefix + "-blocked"
	freeID := env.prefix + "-free"
	// Priority 1 sorts the blocked issue first, where a claim blind to the
	// policy would take it.
	for _, seed := range []*types.Issue{
		{ID: blockedID, Title: "blocked by an external capability", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: freeID, Title: "not blocked by anything", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := env.createIssue(ctx, seed, "tester"); err != nil {
			t.Fatalf("seed %s: %v", seed.ID, err)
		}
	}
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID:     blockedID,
		DependsOnID: externalRef,
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed external dependency: %v", err)
	}
	decorated := externaldeps.New(env.subject, nil, nil)

	reader, err := decorated.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader: %v", err)
	}
	listed, err := reader.List(ctx, issueops.ListRequest{})
	if err != nil {
		t.Fatalf("List over http: %v", err)
	}
	if ids := pageIDs(listed); !slices.Contains(ids, blockedID) || !slices.Contains(ids, freeID) {
		t.Errorf("List over http = %v, want both issues: a plain listing claims nothing about readiness", ids)
	}
	readyListed, err := reader.List(ctx, issueops.ListRequest{ReadyFlag: true})
	if err != nil {
		t.Fatalf("List --ready over http: %v", err)
	}
	if ids := pageIDs(readyListed); !slices.Equal(ids, []string{freeID}) {
		t.Errorf("List --ready over http = %v, want [%s]", ids, freeID)
	}
	ready, err := reader.Ready(ctx, issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("Ready over http: %v", err)
	}
	if ids := pageIDs(ready); !slices.Equal(ids, []string{freeID}) {
		t.Errorf("Ready over http = %v, want [%s]", ids, freeID)
	}
	details, err := reader.Get(ctx, issueops.GetRequest{ID: blockedID})
	if err != nil || details == nil || details.ID != blockedID {
		t.Fatalf("Get %s over http = %v, %v", blockedID, details, err)
	}

	annotator, err := decorated.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}
	blocking, err := annotator.AnnotateBlocking(ctx, issueops.BlockingRequest{IDs: []string{blockedID, freeID}})
	if err != nil {
		t.Fatalf("AnnotateBlocking over http: %v", err)
	}
	for _, item := range blocking.Items {
		if blockedBy := slices.Contains(item.BlockedBy, externalRef); blockedBy != (item.ID == blockedID) {
			t.Errorf("AnnotateBlocking over http: %s blocked by %v", item.ID, item.BlockedBy)
		}
	}

	walker, err := decorated.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker: %v", err)
	}
	tree, err := walker.WalkTree(ctx, issueops.WalkTreeRequest{RootID: blockedID, MaxDepth: 50})
	if err != nil {
		t.Fatalf("WalkTree over http: %v", err)
	}
	var leaves int
	for _, node := range tree.Nodes {
		if node.ID == externalRef {
			leaves++
		}
	}
	if leaves != 1 {
		t.Errorf("WalkTree over http shows %d %s leaves, want one", leaves, externalRef)
	}

	claimer, err := decorated.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer: %v", err)
	}
	if _, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "worker", IssueID: blockedID}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("Claim %s over http = %v, want ErrCloseBlocked", blockedID, err)
	}
	readyClaimer, err := decorated.ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer: %v", err)
	}
	next, err := readyClaimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "worker"})
	if err != nil || next.Claimed == nil || next.Claimed.ID != freeID {
		t.Fatalf("ClaimNext over http = %+v, %v; want %s", next.Claimed, err, freeID)
	}

	closer, err := decorated.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser: %v", err)
	}
	closed, err := closer.CloseBatch(ctx, issueops.CloseBatchRequest{
		Actor: "worker",
		Items: []issueops.BatchCloseItem{{IssueID: blockedID}, {IssueID: freeID}},
	})
	if err != nil || len(closed.Outcomes) != 2 {
		t.Fatalf("CloseBatch over http = %+v, %v; want two outcomes", closed.Outcomes, err)
	}
	if !errors.Is(closed.Outcomes[0].Err, storage.ErrCloseBlocked) || closed.Outcomes[1].Err != nil {
		t.Fatalf("CloseBatch over http: %s = %v, %s = %v; want the first refused, the second closed",
			blockedID, closed.Outcomes[0].Err, freeID, closed.Outcomes[1].Err)
	}
	assertServedStatus(t, env, blockedID, types.StatusOpen)
	assertServedStatus(t, env, freeID, types.StatusClosed)
	forced, err := closer.CloseBatch(ctx, issueops.CloseBatchRequest{
		Actor: "worker", Force: true,
		Items: []issueops.BatchCloseItem{{IssueID: blockedID}},
	})
	if err != nil || len(forced.Outcomes) != 1 || forced.Outcomes[0].Err != nil {
		t.Fatalf("CloseBatch --force over http = %+v, %v; want it closed", forced.Outcomes, err)
	}
	assertServedStatus(t, env, blockedID, types.StatusClosed)
}

// TestServedReadyTotalIgnoresAnExternallyBlockedIssueOutsideTheFilter pins the
// count an assignee-filtered `bd ready` prints over http. Past the server's
// default page the decorator used to subtract every externally blocked issue
// in the workspace from the filtered total, so bob's blocked issue, which the
// filter never counted, took one off alice's.
func TestServedReadyTotalIgnoresAnExternallyBlockedIssueOutsideTheFilter(t *testing.T) {
	env := newServedEnv(t, "hixa")
	ctx := t.Context()

	mine := make([]*types.Issue, 0, workapi.DefaultReadyLimit+1)
	for i := range workapi.DefaultReadyLimit + 1 {
		mine = append(mine, &types.Issue{
			ID: fmt.Sprintf("%s-alice%03d", env.prefix, i), Title: "alice's ready work",
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Assignee: "alice",
		})
	}
	if err := env.reference.CreateIssues(ctx, mine, "tester"); err != nil {
		t.Fatalf("seed alice's issues: %v", err)
	}
	blockedID := env.prefix + "-bob"
	if err := env.createIssue(ctx, &types.Issue{
		ID: blockedID, Title: "bob's externally blocked work",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Assignee: "bob",
	}, "tester"); err != nil {
		t.Fatalf("seed blocked issue: %v", err)
	}
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID:     blockedID,
		DependsOnID: "external:otherproj:cap",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed external dependency: %v", err)
	}

	decorated := externaldeps.New(env.subject, nil, nil)
	const want = workapi.DefaultReadyLimit + 1 // every one of alice's issues
	request := issueops.ReadyRequest{Assignee: "alice"}
	countFilter, err := workapi.BuildReadyCountFilter(request)
	if err != nil {
		t.Fatalf("BuildReadyCountFilter: %v", err)
	}
	count, err := decorated.CountReadyWork(ctx, countFilter)
	if err != nil {
		t.Fatalf("CountReadyWork over http: %v", err)
	}
	pageFilter, err := workapi.BuildReadyFilter(request)
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	rows, total, err := decorated.GetReadyWorkWithCountsAndTotal(ctx, pageFilter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsAndTotal over http: %v", err)
	}
	if count != want || total != want || len(rows) != workapi.DefaultReadyLimit {
		t.Errorf("over http, alice's ready work: CountReadyWork = %d; page = %d rows, total %d; want count and total %d, %d rows",
			count, len(rows), total, want, workapi.DefaultReadyLimit)
	}
}

func assertServedStatus(t *testing.T, env *servedEnv, id string, want types.Status) {
	t.Helper()
	issue, err := env.getIssue(t.Context(), id)
	if err != nil || issue == nil {
		t.Fatalf("read %s back: %v, %v", id, issue, err)
	}
	if issue.Status != want {
		t.Errorf("%s status = %s, want %s", id, issue.Status, want)
	}
}
