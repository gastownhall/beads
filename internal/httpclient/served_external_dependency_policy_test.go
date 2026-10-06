//go:build cgo

package httpclient

import (
	"testing"

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
}
