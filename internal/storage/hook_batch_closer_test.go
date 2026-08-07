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
// tell: one firing per item that landed, in request order, and none for an
// item that refused. Collapsing the batch into one firing would silently stop
// reporting every close but one.
func TestHookBatchCloserFiresOncePerLandedItem(t *testing.T) {
	issue := func(id string) *types.Issue { return &types.Issue{ID: id} }
	inner := &fakeBatchCloser{result: issueops.CloseBatchResult{
		Outcomes: []issueops.CloseOutcome{
			{IssueID: "bd-1", Issue: issue("bd-1")},
			{IssueID: "bd-2", Err: errors.New("refused")},
			{IssueID: "bd-3", Issue: issue("bd-3")},
		},
		ClaimedNext: &types.IssueWithCounts{Issue: issue("bd-4")},
	}}
	recorder := &recordingIssueOperationHooks{}
	closer := &hookBatchCloser{inner: inner, hooks: recorder}

	if _, err := closer.CloseBatch(context.Background(), issueops.CloseBatchRequest{Actor: "worker"}); err != nil {
		t.Fatalf("CloseBatch() error = %v", err)
	}

	// The claim's update fires last, matching the order the transaction
	// applied them in: the closes, then the claim.
	want := []string{"close", "close", "update"}
	if !reflect.DeepEqual(recorder.completions, want) {
		t.Fatalf("hooks fired = %v, want %v", recorder.completions, want)
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
