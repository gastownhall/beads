package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeBatchCloser answers with a fixed result so the per-item firing rules can
// be checked without a database.
type fakeBatchCloser struct {
	result issueops.CloseBatchResult
	err    error
}

func (f *fakeBatchCloser) CloseBatch(context.Context, issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	return f.result, f.err
}

// batchCloserStore is a DoltStorage whose only real method is BatchCloser.
type batchCloserStore struct {
	DoltStorage
	closer issueops.BatchCloser
	err    error
}

func (s batchCloserStore) BatchCloser() (issueops.BatchCloser, error) { return s.closer, s.err }

// TestHookFiringStoreBatchCloserLayersHooksOverInner pins the recursion, for
// the reason its Lifecycle sibling pins it: delegating would compile, satisfy
// Storage, and silently stop running every on_close script.
func TestHookFiringStoreBatchCloserLayersHooksOverInner(t *testing.T) {
	inner := &fakeBatchCloser{}
	store := &HookFiringStore{inner: batchCloserStore{closer: inner}}

	closer, err := store.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser() error = %v", err)
	}
	hooked, ok := closer.(*hookBatchCloser)
	if !ok {
		t.Fatalf("BatchCloser() = %T, want *hookBatchCloser", closer)
	}
	if hooked.inner != issueops.BatchCloser(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's closer", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreBatchCloserPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: batchCloserStore{err: want}}

	closer, err := store.BatchCloser()
	if !errors.Is(err, want) {
		t.Fatalf("BatchCloser() error = %v, want %v", err, want)
	}
	if closer != nil {
		t.Fatalf("BatchCloser() = %T, want nil", closer)
	}
}

// TestHookBatchCloserFiresOncePerLandedItem pins the rule that makes a batch
// close indistinguishable from N single closes as far as a hook script can
// tell: one firing per item that LANDED, in request order, and none for an
// item that refused or changed nothing. Collapsing the batch into one firing
// would silently stop reporting every close but one.
//
// It records the ID each hook was fired for, using the sibling applier's
// recorder, because half these rows are about WHICH item fires: a recorder
// that only counted firings would pass a wrapper that announced the re-close
// and dropped the real one.
func TestHookBatchCloserFiresOncePerLandedItem(t *testing.T) {
	issue := func(id string) *types.Issue { return &types.Issue{ID: id} }
	for _, test := range []struct {
		name  string
		inner *fakeBatchCloser
		want  []string
	}{
		{
			// The claim's update fires last, matching the order the
			// transaction applied them in: the closes, then the claim.
			name: "each landed item fires its own hook, in request order, and the claim's update fires last",
			inner: &fakeBatchCloser{result: issueops.CloseBatchResult{
				Outcomes: []issueops.CloseOutcome{
					{IssueID: "bd-1", Issue: issue("bd-1"), Changed: true},
					{IssueID: "bd-2", Err: errors.New("refused")},
					{IssueID: "bd-3", Issue: issue("bd-3"), Changed: true},
				},
				ClaimedNext: &types.IssueWithCounts{Issue: issue("bd-4")},
			}},
			want: []string{"close:bd-1", "close:bd-3", "update:bd-4"},
		},
		{
			// CHANGED IS THE TEST, not a nil per-item Err: an idempotent
			// re-close is a per-item SUCCESS that wrote nothing
			// (issueops.CloseOutcome.Changed), so a teardown replayed against
			// an already-closed convoy would otherwise run the workspace's
			// on_close script on every pass. The batch also earns no claim,
			// for the reason issueops.CloseBatchRequest.ClaimNext gives.
			name: "an idempotent re-close fires nothing",
			inner: &fakeBatchCloser{result: issueops.CloseBatchResult{
				Outcomes: []issueops.CloseOutcome{
					{IssueID: "bd-1", Issue: issue("bd-1")},
					{IssueID: "bd-2", Issue: issue("bd-2")},
				},
			}},
			want: nil,
		},
		{
			// The discriminating row: a replay where one item was still open.
			// Only that item is a script's business.
			name: "a re-close beside a real close announces only the close that landed",
			inner: &fakeBatchCloser{result: issueops.CloseBatchResult{
				Outcomes: []issueops.CloseOutcome{
					{IssueID: "bd-1", Issue: issue("bd-1")},
					{IssueID: "bd-2", Issue: issue("bd-2"), Changed: true},
				},
			}},
			want: []string{"close:bd-2"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingBatchApplyHooks{}
			closer := &hookBatchCloser{inner: test.inner, hooks: recorder}

			if _, err := closer.CloseBatch(context.Background(), issueops.CloseBatchRequest{Actor: "worker"}); err != nil {
				t.Fatalf("CloseBatch() error = %v", err)
			}
			if !reflect.DeepEqual(recorder.completions, test.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, test.want)
			}
		})
	}
}

// TestHookBatchCloserFiresNothingOnRequestFailure pins that a batch that never
// ran reports nothing: a method error means validation, cancellation or
// infrastructure, so there is no landed item to announce.
func TestHookBatchCloserFiresNothingOnRequestFailure(t *testing.T) {
	inner := &fakeBatchCloser{
		result: issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{{IssueID: "bd-1", Issue: &types.Issue{ID: "bd-1"}}}},
		err:    errors.New("boom"),
	}
	recorder := &recordingIssueOperationHooks{}
	closer := &hookBatchCloser{inner: inner, hooks: recorder}

	if _, err := closer.CloseBatch(context.Background(), issueops.CloseBatchRequest{Actor: "worker"}); err == nil {
		t.Fatal("CloseBatch() error = nil, want the inner failure")
	}
	if recorder.completions != nil {
		t.Fatalf("hooks fired = %v, want none", recorder.completions)
	}
}
