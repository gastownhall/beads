//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
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

// GH#6066: a blocks edge between two LOCAL issues whose ids carry foreign
// prefixes (created with --id --force) must gate ready like any local edge.
func TestForeignPrefixLocalBlockerGatesEmbeddedReady(t *testing.T) {
	store := openExternalDependencyStore(t, "demo")
	ctx := t.Context()

	blocker := &types.Issue{ID: "treehouse-lease-t4", Title: "the blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	dependent := &types.Issue{ID: "firstmate-lease-adopt", Title: "the dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{blocker, dependent} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     dependent.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if got := embeddedIssueIDs(ready); !slices.Equal(got, []string{blocker.ID}) {
		t.Fatalf("ready IDs = %v, want only %s: open local blocker %s must gate %s", got, blocker.ID, blocker.ID, dependent.ID)
	}
}

// foreignPrefixEnv is a "demo" database holding issues whose explicit ids
// carry other prefixes (GH#6066).
type foreignPrefixEnv struct {
	*testEnv
	t *testing.T
}

func newForeignPrefixEnv(t *testing.T) foreignPrefixEnv {
	t.Helper()
	return foreignPrefixEnv{testEnv: newTestEnv(t, "demo"), t: t}
}

func (e foreignPrefixEnv) create(issues ...*types.Issue) {
	e.t.Helper()
	for _, issue := range issues {
		if issue.Status == "" {
			issue.Status = types.StatusOpen
		}
		if issue.IssueType == "" {
			issue.IssueType = types.TypeTask
		}
		if issue.Title == "" {
			issue.Title = issue.ID
		}
		issue.Priority = 2
		if err := e.store.CreateIssue(e.t.Context(), issue, "tester"); err != nil {
			e.t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
}

// targetColumn names the one typed target column that holds the edge.
func (e foreignPrefixEnv) targetColumn(table, source, target string) string {
	e.t.Helper()
	var issueCol, wispCol, externalCol int
	e.queryScalar(e.t, e.t.Context(), "SELECT "+
		"COALESCE(SUM(depends_on_issue_id = ?), 0), COALESCE(SUM(depends_on_wisp_id = ?), 0), COALESCE(SUM(depends_on_external = ?), 0) "+
		"FROM "+table+" WHERE issue_id = ?", []any{target, target, target, source}, &issueCol, &wispCol, &externalCol)
	var cols []string
	if issueCol > 0 {
		cols = append(cols, "depends_on_issue_id")
	}
	if wispCol > 0 {
		cols = append(cols, "depends_on_wisp_id")
	}
	if externalCol > 0 {
		cols = append(cols, "depends_on_external")
	}
	if len(cols) != 1 {
		e.t.Fatalf("%s edge %s -> %s stored in %v, want exactly one target column", table, source, target, cols)
	}
	return cols[0]
}

func (e foreignPrefixEnv) readyIDs() []string {
	e.t.Helper()
	ready, err := e.store.GetReadyWork(e.t.Context(), types.WorkFilter{})
	if err != nil {
		e.t.Fatalf("GetReadyWork: %v", err)
	}
	ids := embeddedIssueIDs(ready)
	slices.Sort(ids)
	return ids
}

func (e foreignPrefixEnv) blockedBy(id string) []string {
	e.t.Helper()
	blocked, err := e.store.GetBlockedIssues(e.t.Context(), types.WorkFilter{})
	if err != nil {
		e.t.Fatalf("GetBlockedIssues: %v", err)
	}
	for _, b := range blocked {
		if b.ID == id {
			return b.BlockedBy
		}
	}
	return nil
}

// The edge is local through both dependency editors (the store verb and
// the transaction verb): it lands in depends_on_issue_id, withholds the
// dependent from ready, and blocked names the blocker.
func TestForeignPrefixLocalBlockerIsALocalEdge(t *testing.T) {
	for _, editor := range []string{"store", "transaction"} {
		t.Run(editor, func(t *testing.T) {
			e := newForeignPrefixEnv(t)
			ctx := t.Context()
			const blocker, dependent = "treehouse-lease-t4", "firstmate-lease-adopt"
			e.create(&types.Issue{ID: blocker}, &types.Issue{ID: dependent})
			dep := &types.Dependency{IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks}
			var err error
			if editor == "store" {
				err = e.store.AddDependency(ctx, dep, "tester")
			} else {
				err = e.store.RunInTransaction(ctx, "test: add dep", func(tx storage.Transaction) error {
					return tx.AddDependency(ctx, dep, "tester")
				})
			}
			if err != nil {
				t.Fatalf("AddDependency: %v", err)
			}
			if got := e.targetColumn("dependencies", dependent, blocker); got != "depends_on_issue_id" {
				t.Fatalf("edge stored in %s, want depends_on_issue_id: %s is a local issue", got, blocker)
			}
			if got := e.readyIDs(); !slices.Equal(got, []string{blocker}) {
				t.Fatalf("ready = %v, want only %s", got, blocker)
			}
			if got := e.blockedBy(dependent); !slices.Equal(got, []string{blocker}) {
				t.Fatalf("blocked: %s blocked by %v, want [%s]", dependent, got, blocker)
			}
		})
	}
}

// Closing the local foreign-prefix blocker readies the dependent.
func TestForeignPrefixLocalBlockerCloseReadiesDependent(t *testing.T) {
	e := newForeignPrefixEnv(t)
	ctx := t.Context()
	const blocker, dependent = "treehouse-lease-t4", "firstmate-lease-adopt"
	e.create(&types.Issue{ID: blocker}, &types.Issue{ID: dependent})
	if err := e.store.AddDependency(ctx, &types.Dependency{IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if got := e.readyIDs(); slices.Contains(got, dependent) {
		t.Fatalf("ready = %v before close, want %s withheld", got, dependent)
	}
	if err := e.store.CloseIssue(ctx, blocker, "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if got := e.readyIDs(); !slices.Equal(got, []string{dependent}) {
		t.Fatalf("ready = %v after closing %s, want [%s]", got, blocker, dependent)
	}
	if got := e.blockedBy(dependent); len(got) != 0 {
		t.Fatalf("blocked: %s still blocked by %v after its blocker closed", dependent, got)
	}
}

// Create-with-dependencies stores local target columns for held
// foreign-prefix targets on the single-edge path (one dependency) and on the
// batch path (several, answered from batch reads).
func TestForeignPrefixCreateDependenciesAreLocal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []string
	}{
		{"single_edge", []string{"treehouse-a"}},
		{"batch", []string{"treehouse-a", "treehouse-b", "outpost-c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newForeignPrefixEnv(t)
			ctx := t.Context()
			for _, id := range tc.targets {
				e.create(&types.Issue{ID: id})
			}
			dependent := &types.Issue{ID: "firstmate-dependent", Title: "dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			for _, id := range tc.targets {
				dependent.Dependencies = append(dependent.Dependencies, &types.Dependency{DependsOnID: id, Type: types.DepBlocks})
			}
			if err := e.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{dependent}, "tester", storage.BatchCreateOptions{
				SkipPrefixValidation: true,
			}); err != nil {
				t.Fatalf("CreateIssuesWithFullOptions: %v", err)
			}
			for _, id := range tc.targets {
				if got := e.targetColumn("dependencies", dependent.ID, id); got != "depends_on_issue_id" {
					t.Fatalf("created edge -> %s stored in %s, want depends_on_issue_id", id, got)
				}
			}
			want := slices.Clone(tc.targets)
			slices.Sort(want)
			if got := e.readyIDs(); !slices.Equal(got, want) {
				t.Fatalf("ready = %v, want %v (%s withheld by its open local blockers)", got, want, dependent.ID)
			}
		})
	}
}

// A cross-prefix target this database does not hold, and an explicit
// external: ref, keep their external storage and leave raw ready alone; a
// same-prefix target that is missing is still refused.
func TestForeignPrefixAbsentTargetStaysExternal(t *testing.T) {
	e := newForeignPrefixEnv(t)
	ctx := t.Context()
	const dependent = "demo-dependent"
	e.create(&types.Issue{ID: dependent})
	for _, target := range []string{"otherrig-abc", "external:otherrig:payments"} {
		if err := e.store.AddDependency(ctx, &types.Dependency{IssueID: dependent, DependsOnID: target, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s): %v", target, err)
		}
		if got := e.targetColumn("dependencies", dependent, target); got != "depends_on_external" {
			t.Fatalf("edge -> %s stored in %s, want depends_on_external", target, got)
		}
	}
	if got := e.readyIDs(); !slices.Equal(got, []string{dependent}) {
		t.Fatalf("raw ready = %v, want [%s]: external edges are the decorator's policy", got, dependent)
	}
	if err := e.store.AddDependency(ctx, &types.Dependency{IssueID: dependent, DependsOnID: "demo-missing", Type: types.DepBlocks}, "tester"); err == nil {
		t.Fatal("AddDependency to a missing same-prefix target succeeded, want a missing-target refusal")
	}
}

// A held foreign-prefix ancestor is a local ancestor, so a blocks edge
// from its descendant is refused by hierarchy validation and writes nothing.
// The ancestor is a grandparent: a child's own parent already holds the
// (child, parent) row.
func TestForeignPrefixAncestorBlockIsRefused(t *testing.T) {
	e := newForeignPrefixEnv(t)
	ctx := t.Context()
	const grandparent, parent, child = "treehouse-epic", "firstmate-parent", "firstmate-child"
	e.create(&types.Issue{ID: grandparent, IssueType: types.TypeEpic}, &types.Issue{ID: parent, IssueType: types.TypeEpic}, &types.Issue{ID: child})
	for _, edge := range [][2]string{{parent, grandparent}, {child, parent}} {
		if err := e.store.AddDependency(ctx, &types.Dependency{IssueID: edge[0], DependsOnID: edge[1], Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency(parent-child %s -> %s): %v", edge[0], edge[1], err)
		}
	}
	err := e.store.AddDependency(ctx, &types.Dependency{IssueID: child, DependsOnID: grandparent, Type: types.DepBlocks}, "tester")
	var conflict *domain.DependencyHierarchyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("AddDependency(%s blocks on ancestor %s) = %v, want *DependencyHierarchyConflictError", child, grandparent, err)
	}
	var edges int
	e.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = 'blocks'", []any{child}, &edges)
	if edges != 0 {
		t.Fatalf("refused hierarchy edge left %d blocks rows for %s", edges, child)
	}
}
