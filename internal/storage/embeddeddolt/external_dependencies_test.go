//go:build cgo

package embeddeddolt_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
)

type nonClosingStore struct {
	storage.DoltStorage
}

func (nonClosingStore) Close() error { return nil }

func TestExternalCapabilityBlocksEmbeddedReadyAndAppearsInTree(t *testing.T) {
	local := openExternalDependencyStore(t, "local")
	remote := openExternalDependencyStore(t, "remote")
	ctx := t.Context()

	source := &types.Issue{
		ID:        "local-source",
		Title:     "Ship checkout",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := local.CreateIssue(ctx, source, "tester"); err != nil {
		t.Fatalf("CreateIssue(local): %v", err)
	}
	const externalRef = "external:remote:payments"
	if err := local.AddDependency(ctx, &types.Dependency{
		IssueID:     source.ID,
		DependsOnID: externalRef,
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	wrapped := externaldeps.New(
		local,
		func(project externaldeps.ProjectName) (string, bool) {
			return string(project), project == "remote"
		},
		func(_ context.Context, _ string) (storage.DoltStorage, error) {
			return nonClosingStore{DoltStorage: remote}, nil
		},
	)

	rawReady, err := local.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("raw GetReadyWork: %v", err)
	}
	if got := embeddedIssueIDs(rawReady); !slices.Equal(got, []string{source.ID}) {
		t.Fatalf("raw ready IDs = %v, want stored external edge to require decorator policy", got)
	}

	ready, err := wrapped.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("wrapped GetReadyWork: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("wrapped ready IDs = %v, want external blocker enforced", embeddedIssueIDs(ready))
	}

	blocked, err := wrapped.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if len(blocked) != 1 || !slices.Equal(blocked[0].BlockedBy, []string{externalRef}) {
		t.Fatalf("blocked = %+v, want external blocker", blocked)
	}

	tree, err := wrapped.GetDependencyTree(ctx, source.ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree: %v", err)
	}
	if len(tree) != 2 || tree[1].ID != externalRef || tree[1].Status != types.StatusOpen {
		t.Fatalf("tree = %+v, want open synthetic external leaf", tree)
	}

	provider := &types.Issue{
		ID:        "remote-provider",
		Title:     "Provide payments",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := remote.CreateIssue(ctx, provider, "tester"); err != nil {
		t.Fatalf("CreateIssue(remote): %v", err)
	}
	if err := remote.AddLabel(ctx, provider.ID, "provides:payments", "tester"); err != nil {
		t.Fatalf("AddLabel(provides): %v", err)
	}
	if err := remote.CloseIssue(ctx, provider.ID, "shipped", "tester", ""); err != nil {
		t.Fatalf("CloseIssue(provider): %v", err)
	}

	claimed, err := wrapped.ClaimReadyIssue(ctx, types.WorkFilter{}, "worker")
	if err != nil {
		t.Fatalf("ClaimReadyIssue after ship: %v", err)
	}
	if claimed == nil || claimed.ID != source.ID {
		t.Fatalf("claimed = %+v, want %s", claimed, source.ID)
	}
}

func openExternalDependencyStore(t *testing.T, prefix string) *embeddeddolt.EmbeddedDoltStore {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), filepath.Join(t.TempDir(), ".beads"), prefix, "main")
	if err != nil {
		t.Fatalf("Open(%s): %v", prefix, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(t.Context(), "issue_prefix", prefix); err != nil {
		t.Fatalf("SetConfig(%s): %v", prefix, err)
	}
	if err := store.Commit(t.Context(), "bd init"); err != nil {
		t.Fatalf("Commit(%s): %v", prefix, err)
	}
	return store
}

func embeddedIssueIDs(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}
