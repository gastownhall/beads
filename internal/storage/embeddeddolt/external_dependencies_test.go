//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
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

// TestExternalCapabilityGuardsEmbeddedBatchClose pins the BatchCloser role on
// the store decorator: the direct `bd close` route and `bd serve`'s store arm
// both close through it. An unsatisfied external blocker refuses the item
// (unless forced) while the survivors commit, and the claim a batch earns
// never selects an externally blocked issue.
func TestExternalCapabilityGuardsEmbeddedBatchClose(t *testing.T) {
	local := openExternalDependencyStore(t, "local")
	ctx := t.Context()
	create := func(id string, priority int) {
		t.Helper()
		if err := local.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: priority, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	create("local-held", 0)
	create("local-free", 2)
	create("local-done", 3)
	create("local-other", 3)
	if err := local.AddDependency(ctx, &types.Dependency{
		IssueID: "local-held", DependsOnID: "external:remote:payments", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	wrapped := externaldeps.New(local, func(externaldeps.ProjectName) (string, bool) { return "", false }, nil)
	closer, err := wrapped.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser: %v", err)
	}

	// The claim a batch earns: local-held heads the priority order and is
	// externally blocked, so it must be walked past.
	claimed, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor:     "tester",
		Items:     []publicops.BatchCloseItem{{IssueID: "local-done"}},
		ClaimNext: &publicops.ReadyRequest{Sort: "priority"},
	})
	if err != nil {
		t.Fatalf("CloseBatch with ClaimNext: %v", err)
	}
	if claimed.ClaimedNext == nil || claimed.ClaimedNext.ID != "local-free" {
		t.Errorf("ClaimedNext = %v, want local-free (local-held is externally blocked)", claimed.ClaimedNext)
	}

	// The close guard: the blocked item refuses, the survivor commits.
	result, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "tester",
		Items: []publicops.BatchCloseItem{{IssueID: "local-held"}, {IssueID: "local-other"}},
	})
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	if len(result.Outcomes) != 2 || !errors.Is(result.Outcomes[0].Err, storage.ErrCloseBlocked) {
		t.Fatalf("outcomes = %+v, want ErrCloseBlocked for the externally blocked item", result.Outcomes)
	}
	if result.Outcomes[1].Err != nil || !result.Outcomes[1].Changed {
		t.Fatalf("outcome[1] = %+v, want the unblocked survivor to close", result.Outcomes[1])
	}
	held, err := local.GetIssue(ctx, "local-held")
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != types.StatusOpen || held.Assignee != "" {
		t.Fatalf("local-held = %s/%q, want open and unassigned", held.Status, held.Assignee)
	}

	forced, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "tester", Force: true, Items: []publicops.BatchCloseItem{{IssueID: "local-held"}},
	})
	if err != nil || forced.Outcomes[0].Err != nil || !forced.Outcomes[0].Changed {
		t.Fatalf("forced close = %+v, %v; want --force to bypass the external guard", forced.Outcomes, err)
	}
}

// TestExternalCapabilityReCloseIsANoOpOnTheStoreArm pins the idempotent
// re-close (ga-ktn9pe.4.8) against the external close guard on every close
// path the store decorator hands out: an issue that is ALREADY closed and
// carries an unsatisfied `external:` blocker re-closes as a no-op through the
// BatchCloser, Lifecycle.Close and a Lifecycle.Update to closed, while an OPEN
// issue with the same blocker is still refused.
func TestExternalCapabilityReCloseIsANoOpOnTheStoreArm(t *testing.T) {
	local := openExternalDependencyStore(t, "local")
	ctx := t.Context()
	for _, id := range []string{"local-done", "local-open"} {
		if err := local.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	if err := local.CloseIssue(ctx, "local-done", "shipped", "tester", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	for _, id := range []string{"local-done", "local-open"} {
		if err := local.AddDependency(ctx, &types.Dependency{
			IssueID: id, DependsOnID: "external:remote:payments", Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s): %v", id, err)
		}
	}
	wrapped := externaldeps.New(local, func(externaldeps.ProjectName) (string, bool) { return "", false }, nil)

	closer, err := wrapped.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser: %v", err)
	}
	result, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{
		Actor: "tester",
		Items: []publicops.BatchCloseItem{{IssueID: "local-done"}, {IssueID: "local-open"}},
	})
	if err != nil {
		t.Fatalf("CloseBatch: %v", err)
	}
	if got := result.Outcomes[0]; got.Err != nil || got.Changed {
		t.Errorf("batch re-close of closed local-done = %+v, want the no-op (no error, not changed)", got)
	}
	if got := result.Outcomes[1]; !errors.Is(got.Err, storage.ErrCloseBlocked) {
		t.Errorf("batch close of open local-open = %+v, want ErrCloseBlocked", got)
	}

	lifecycle, err := wrapped.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "tester", IssueID: "local-done"}); err != nil {
		t.Errorf("Lifecycle.Close re-close of local-done: %v, want the no-op", err)
	}
	if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "tester", IssueID: "local-open"}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("Lifecycle.Close of open local-open: %v, want ErrCloseBlocked", err)
	}
	closed := publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: publicops.Status(types.StatusClosed)}}
	if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "tester", IssueID: "local-done", Patch: closed}); errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("Lifecycle.Update to closed on closed local-done: %v, want no external refusal", err)
	}
	if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "tester", IssueID: "local-open", Patch: closed}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("Lifecycle.Update to closed on open local-open: %v, want ErrCloseBlocked", err)
	}
	if got, _ := local.GetIssue(ctx, "local-open"); got == nil || got.Status != types.StatusOpen {
		t.Errorf("local-open after refused closes = %v, want open", got)
	}
}
