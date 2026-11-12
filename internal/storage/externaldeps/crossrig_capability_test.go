package externaldeps

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestOldTokenServerKeepsCrossRigEnforcement pins GH#5784 AC-8: a server of
// the previous build advertises the capability-reference half of the
// external-dependency policy (wire.CapExternalDependencies) but not the bare
// cross-prefix half this build added (wire.CapCrossRigDependencies), and the
// old token must not be read as the whole policy. Ready, blocked-list,
// is-blocked and tree queries against such a server keep their
// CLIENT-composed cross-rig enforcement — the open bare target withholds the
// source from ready, lists it blocked with the target among its blockers,
// answers is-blocked true, and hangs the foreign tree leaf — each query
// succeeding with its ordinary output contract: no blanket refusal, and no
// bypass. A server that advertises the new token has folded both halves in
// and every answer passes through as the server gave it; a server that
// advertises neither token gets the ordinary client-side enforcement for
// both halves, as before.
func TestOldTokenServerKeepsCrossRigEnforcement(t *testing.T) {
	for _, server := range []struct {
		name         string
		enforced     bool
		crossRig     bool
		wantReady    []string
		wantBlocked  bool
		wantTreeLeaf bool
	}{
		{name: "old token", enforced: true, crossRig: false, wantReady: []string{"be-free"}, wantBlocked: true, wantTreeLeaf: true},
		{name: "new token", enforced: true, crossRig: true, wantReady: []string{"be-src", "be-free"}, wantBlocked: false, wantTreeLeaf: false},
		{name: "no tokens", enforced: false, crossRig: false, wantReady: []string{"be-free"}, wantBlocked: true, wantTreeLeaf: true},
	} {
		t.Run(server.name, func(t *testing.T) {
			src, free := issue("be-src"), issue("be-free")
			raw := newRemoteStore(src, free)
			raw.enforced = server.enforced
			raw.crossRigEnforced = server.crossRig
			raw.deps[src.ID] = []*types.Dependency{externalDep(src.ID, xrigTarget, types.DepBlocks)}
			raw.tree = []*types.TreeNode{{Issue: *src}}
			resolver := &fakeResolver{results: map[string]*ForeignIssueResult{
				xrigTarget: {Issue: &types.Issue{ID: xrigTarget, Title: "foreign blocker", Status: types.StatusOpen}},
			}}
			store := crossRigStore(raw, resolver)

			// Ready: the client-composed exclusion withholds the source
			// unless the server's own answer already applied the policy.
			got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(got); !slices.Equal(ids, server.wantReady) {
				t.Fatalf("ready IDs = %v, want %v", ids, server.wantReady)
			}

			// Blocked list: the source is listed with the foreign target
			// among its blockers, or stays out of a listing the server
			// already composed.
			blocked, err := store.GetBlockedIssues(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetBlockedIssues: %v", err)
			}
			if server.wantBlocked {
				var row *types.BlockedIssue
				for _, item := range blocked {
					if item != nil && item.ID == src.ID {
						row = item
					}
				}
				if row == nil {
					t.Fatalf("blocked listing = %v, want %s listed with its foreign blocker", blockedIssueIDs(blocked), src.ID)
				}
				if !slices.Contains(row.BlockedBy, xrigTarget) {
					t.Fatalf("%s blocked by %v, want %s among the blockers", src.ID, row.BlockedBy, xrigTarget)
				}
			} else if ids := blockedIssueIDs(blocked); !slices.Equal(ids, nil) {
				t.Fatalf("blocked listing = %v, want the server's answer as it came", ids)
			}

			// Is-blocked: the batch flag reports the source blocked, and a
			// free row stays free, whichever server answers the base flag.
			flags, err := store.IsBlockedBatch(t.Context(), []string{src.ID, free.ID})
			if err != nil {
				t.Fatalf("IsBlockedBatch: %v", err)
			}
			if flags[src.ID] != server.wantBlocked {
				t.Fatalf("IsBlockedBatch[%s] = %v, want %v", src.ID, flags[src.ID], server.wantBlocked)
			}
			if flags[free.ID] {
				t.Fatalf("IsBlockedBatch[%s] = true, want the free row free", free.ID)
			}

			// Tree: the down walk hangs the foreign leaf the server left
			// out, marked external; a server that enforces the policy keeps
			// its own tree untouched.
			walker, err := store.TreeWalker()
			if err != nil {
				t.Fatalf("TreeWalker: %v", err)
			}
			result, err := walker.WalkTree(t.Context(), publicops.WalkTreeRequest{RootID: src.ID, MaxDepth: 50})
			if err != nil {
				t.Fatalf("WalkTree: %v", err)
			}
			ids := make([]string, 0, len(result.Nodes))
			for _, node := range result.Nodes {
				ids = append(ids, node.ID)
			}
			if !slices.Equal(ids, []string{src.ID, xrigTarget}) && server.wantTreeLeaf {
				t.Fatalf("tree = %v, want the foreign leaf %s under the root", ids, xrigTarget)
			}
			if !server.wantTreeLeaf && !slices.Equal(ids, []string{src.ID}) {
				t.Fatalf("tree = %v, want the server's walk as it came", ids)
			}
			if server.wantTreeLeaf {
				leaf := result.Nodes[len(result.Nodes)-1]
				if leaf.ID != xrigTarget || !leaf.External || leaf.Unresolved {
					t.Fatalf("leaf = %+v, want the resolved external leaf for %s", leaf, xrigTarget)
				}
			}
		})
	}
}
