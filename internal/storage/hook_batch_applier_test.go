package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// recordingBatchApplyHooks records the id each hook was fired for, which the
// shared recorder above does not: this decorator's whole job is deciding WHICH
// rows fire, so a recorder that only counted firings could not tell the
// de-duplication rule from a wrapper that fired for the wrong issue.
type recordingBatchApplyHooks struct {
	completions []string
}

func (r *recordingBatchApplyHooks) CompleteIssueOperationCreate(_ context.Context, issue *types.Issue, _ []*types.Dependency) {
	r.completions = append(r.completions, "create:"+issueIDOrNil(issue))
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationUpdate(issue *types.Issue) {
	r.completions = append(r.completions, "update:"+issueIDOrNil(issue))
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationClose(issue *types.Issue) {
	r.completions = append(r.completions, "close:"+issueIDOrNil(issue))
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationDependency(_ context.Context, issueID string) {
	r.completions = append(r.completions, "dependency:"+issueID)
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationComment(_ context.Context, issueID string) {
	r.completions = append(r.completions, "comment:"+issueID)
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationMetadata(_ context.Context, issueID string) {
	r.completions = append(r.completions, "metadata:"+issueID)
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationRelease(_ context.Context, issueID string) {
	r.completions = append(r.completions, "release:"+issueID)
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationReclaim(_ context.Context, issueID string) {
	r.completions = append(r.completions, "reclaim:"+issueID)
}
func (r *recordingBatchApplyHooks) CompleteIssueOperationUpdateByID(_ context.Context, issueID string) {
	r.completions = append(r.completions, "update-by-id:"+issueID)
}

func issueIDOrNil(issue *types.Issue) string {
	if issue == nil {
		return "<nil>"
	}
	return issue.ID
}

// fakeBatchApplier answers with a fixed result so the firing rules can be
// checked without a database.
type fakeBatchApplier struct {
	result issueops.ApplyBatchResult
	err    error
}

func (f *fakeBatchApplier) ApplyBatch(context.Context, issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	return f.result, f.err
}

// batchApplierStore is a DoltStorage whose only real method is BatchApplier.
type batchApplierStore struct {
	DoltStorage
	applier issueops.BatchApplier
	err     error
}

func (s batchApplierStore) BatchApplier() (issueops.BatchApplier, error) { return s.applier, s.err }

// TestHookFiringStoreBatchApplierLayersHooksOverInner pins the recursion.
// Delegating to the inner store instead would still compile and still satisfy
// Storage, and every plan a caller applied would silently stop firing all four
// hook vocabularies at once.
func TestHookFiringStoreBatchApplierLayersHooksOverInner(t *testing.T) {
	inner := &fakeBatchApplier{}
	store := &HookFiringStore{inner: batchApplierStore{applier: inner}}

	applier, err := store.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier() error = %v", err)
	}
	hooked, ok := applier.(*hookBatchApplier)
	if !ok {
		t.Fatalf("BatchApplier() = %T, want *hookBatchApplier", applier)
	}
	if hooked.inner != issueops.BatchApplier(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's applier", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreBatchApplierPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: batchApplierStore{err: want}}

	applier, err := store.BatchApplier()
	if !errors.Is(err, want) {
		t.Fatalf("BatchApplier() error = %v, want %v", err, want)
	}
	if applier != nil {
		t.Fatalf("BatchApplier() = %T, want nil", applier)
	}
}

func batchApplyItemResult(kind issueops.ItemKind, id string, changed bool) issueops.ItemResult {
	result := issueops.ItemResult{Kind: kind, IssueID: id, Changed: changed}
	if kind != issueops.ItemDepAdd {
		result.Issue = &types.Issue{ID: id}
	}
	return result
}

// TestHookBatchApplierFiresPerLandedItem pins every rule this decorator makes,
// and each row is a regression someone would otherwise ship green.
func TestHookBatchApplierFiresPerLandedItem(t *testing.T) {
	for _, test := range []struct {
		name    string
		applier *fakeBatchApplier
		want    []string
	}{
		{
			name: "each landed row verb fires its own hook, in request order",
			applier: &fakeBatchApplier{result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
				batchApplyItemResult(issueops.ItemCreate, "bd-1", true),
				batchApplyItemResult(issueops.ItemUpdate, "bd-2", true),
				batchApplyItemResult(issueops.ItemClose, "bd-3", true),
			}}},
			want: []string{"create:bd-1", "update:bd-2", "close:bd-3"},
		},
		{
			// Changed is the fact a script cares about, not "no error".
			// hookBatchCloser tested a nil per-item Err here and ran the
			// workspace's on_close script on every replayed pass (ga-2yaqp.1);
			// its own table pins the same rule now.
			name: "an item that changed nothing fires nothing",
			applier: &fakeBatchApplier{result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
				batchApplyItemResult(issueops.ItemUpdate, "bd-1", false),
				batchApplyItemResult(issueops.ItemClose, "bd-2", false),
				batchApplyItemResult(issueops.ItemDepAdd, "bd-3", false),
			}}},
			want: nil,
		},
		{
			name: "edges fire the update hook once per distinct source, after the row verbs",
			applier: &fakeBatchApplier{result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
				batchApplyItemResult(issueops.ItemDepAdd, "bd-1", true),
				batchApplyItemResult(issueops.ItemUpdate, "bd-9", true),
				batchApplyItemResult(issueops.ItemDepAdd, "bd-2", true),
				batchApplyItemResult(issueops.ItemDepAdd, "bd-1", true),
			}}},
			want: []string{"update:bd-9", "dependency:bd-1", "dependency:bd-2"},
		},
		{
			// A script was handed this row as a create, and the edges are part
			// of the same act. Firing again would have it react to a graph it
			// has already seen.
			name: "an issue this request created does not also fire update for its edges",
			applier: &fakeBatchApplier{result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
				batchApplyItemResult(issueops.ItemCreate, "bd-1", true),
				batchApplyItemResult(issueops.ItemDepAdd, "bd-1", true),
				batchApplyItemResult(issueops.ItemDepAdd, "bd-2", true),
			}}},
			want: []string{"create:bd-1", "dependency:bd-2"},
		},
		{
			name: "a refused request fires nothing",
			applier: &fakeBatchApplier{
				result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
					batchApplyItemResult(issueops.ItemCreate, "bd-1", true),
				}},
				err: errors.New("boom"),
			},
			want: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingBatchApplyHooks{}
			applier := &hookBatchApplier{inner: test.applier, hooks: recorder}

			_, _ = applier.ApplyBatch(context.Background(), issueops.ApplyBatchRequest{Actor: "worker"})

			if !reflect.DeepEqual(recorder.completions, test.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, test.want)
			}
		})
	}
}

// TestHookBatchApplierHandsTheHookTheResultSnapshot pins WHY ItemResult carries
// an Issue at all. Every completion hook is written against the row it is being
// told about, and a wrapper that had to re-read each one would be N reads
// outside the transaction that wrote them — so the snapshot the body hydrated
// in-transaction is what a script sees.
func TestHookBatchApplierHandsTheHookTheResultSnapshot(t *testing.T) {
	landed := &types.Issue{ID: "bd-1", Title: "after the update"}
	recorder := &recordingBatchApplyIssues{}
	applier := &hookBatchApplier{
		inner: &fakeBatchApplier{result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
			{Kind: issueops.ItemUpdate, IssueID: "bd-1", Changed: true, Issue: landed},
		}}},
		hooks: recorder,
	}

	if _, err := applier.ApplyBatch(context.Background(), issueops.ApplyBatchRequest{Actor: "worker"}); err != nil {
		t.Fatalf("ApplyBatch() error = %v", err)
	}
	if len(recorder.issues) != 1 || recorder.issues[0] != landed {
		t.Fatalf("the update hook was handed %v, want the result's own post-item snapshot", recorder.issues)
	}
}

// recordingBatchApplyIssues keeps the ISSUE VALUES the hooks were handed, which
// is the fact the id-recording twin above cannot check.
type recordingBatchApplyIssues struct {
	issues []*types.Issue
}

func (r *recordingBatchApplyIssues) CompleteIssueOperationCreate(_ context.Context, issue *types.Issue, _ []*types.Dependency) {
	r.issues = append(r.issues, issue)
}
func (r *recordingBatchApplyIssues) CompleteIssueOperationUpdate(issue *types.Issue) {
	r.issues = append(r.issues, issue)
}
func (r *recordingBatchApplyIssues) CompleteIssueOperationClose(issue *types.Issue) {
	r.issues = append(r.issues, issue)
}
func (r *recordingBatchApplyIssues) CompleteIssueOperationDependency(context.Context, string) {}
func (r *recordingBatchApplyIssues) CompleteIssueOperationComment(context.Context, string)    {}
func (r *recordingBatchApplyIssues) CompleteIssueOperationMetadata(context.Context, string)   {}
func (r *recordingBatchApplyIssues) CompleteIssueOperationRelease(context.Context, string)    {}
func (r *recordingBatchApplyIssues) CompleteIssueOperationReclaim(context.Context, string)    {}
func (r *recordingBatchApplyIssues) CompleteIssueOperationUpdateByID(context.Context, string) {}

// snapshotFiring is one hook firing with the issue value it was handed, which
// is the fact the id-only recorder above cannot check.
type snapshotFiring struct {
	verb  string
	id    string
	issue *types.Issue
}

// snapshotRecordingHooks records every firing's verb, id and issue value.
type snapshotRecordingHooks struct {
	fired []snapshotFiring
}

func (r *snapshotRecordingHooks) CompleteIssueOperationCreate(_ context.Context, issue *types.Issue, _ []*types.Dependency) {
	r.fired = append(r.fired, snapshotFiring{"create", issueIDOrNil(issue), issue})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationUpdate(issue *types.Issue) {
	r.fired = append(r.fired, snapshotFiring{"update", issueIDOrNil(issue), issue})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationClose(issue *types.Issue) {
	r.fired = append(r.fired, snapshotFiring{"close", issueIDOrNil(issue), issue})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationDependency(_ context.Context, issueID string) {
	r.fired = append(r.fired, snapshotFiring{"dependency", issueID, nil})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationComment(_ context.Context, issueID string) {
	r.fired = append(r.fired, snapshotFiring{"comment", issueID, nil})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationMetadata(_ context.Context, issueID string) {
	r.fired = append(r.fired, snapshotFiring{"metadata", issueID, nil})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationRelease(_ context.Context, issueID string) {
	r.fired = append(r.fired, snapshotFiring{"release", issueID, nil})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationReclaim(_ context.Context, issueID string) {
	r.fired = append(r.fired, snapshotFiring{"reclaim", issueID, nil})
}
func (r *snapshotRecordingHooks) CompleteIssueOperationUpdateByID(_ context.Context, issueID string) {
	r.fired = append(r.fired, snapshotFiring{"update-by-id", issueID, nil})
}

// TestHookBatchApplierHandsEveryRowHookTheSharedSnapshot (bd-xx3ma9): a mixed
// batch whose row-verb entries share ONE snapshot — one per row, the state it
// finished the batch in — fires each row verb's own hook, in request order,
// under the same count and gating rules as before, and hands EVERY row hook
// the shared snapshot the entries carry, not a re-read of the row. A script
// reacting to the create and to the update of the same row sees the row's
// final state both times, which is also all it can act on: the batch commits
// or rolls back together.
func TestHookBatchApplierHandsEveryRowHookTheSharedSnapshot(t *testing.T) {
	shared := &types.Issue{ID: "bd-1", Title: "after the batch"}
	other := &types.Issue{ID: "bd-2", Title: "the edge's other end"}
	recorder := &snapshotRecordingHooks{}
	applier := &hookBatchApplier{
		inner: &fakeBatchApplier{result: issueops.ApplyBatchResult{Items: []issueops.ItemResult{
			{Kind: issueops.ItemCreate, IssueID: "bd-1", Changed: true, Issue: shared},
			{Kind: issueops.ItemUpdate, IssueID: "bd-1", Changed: true, Issue: shared},
			{Kind: issueops.ItemUpdate, IssueID: "bd-1", Changed: false, Issue: shared},
			{Kind: issueops.ItemCreate, IssueID: "bd-2", Changed: true, Issue: other},
			{Kind: issueops.ItemDepAdd, IssueID: "bd-1", DependsOnID: "bd-2", Changed: true},
		}}},
		hooks: recorder,
	}

	if _, err := applier.ApplyBatch(context.Background(), issueops.ApplyBatchRequest{Actor: "worker"}); err != nil {
		t.Fatalf("ApplyBatch() error = %v", err)
	}

	want := []snapshotFiring{
		{"create", "bd-1", shared},
		{"update", "bd-1", shared},
		{"create", "bd-2", other},
	}
	if !reflect.DeepEqual(recorder.fired, want) {
		t.Fatalf("hooks fired = %v, want %v (count, order and gating unchanged; every row hook handed the shared snapshot)", recorder.fired, want)
	}
	for _, firing := range recorder.fired {
		if firing.verb == "create" || firing.verb == "update" || firing.verb == "close" {
			if firing.issue == nil || (firing.id == "bd-1" && firing.issue != shared) {
				t.Errorf("the %s hook for %s was not handed the row's shared snapshot", firing.verb, firing.id)
			}
		}
	}
}
