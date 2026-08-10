package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeReadyClaimer answers with a fixed claim, or with nothing, or with an
// error, so the firing rules can be checked without a database.
type fakeReadyClaimer struct {
	claimed *types.IssueWithCounts
	err     error
}

func (f *fakeReadyClaimer) ClaimNext(context.Context, issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	return issueops.ClaimNextResult{Claimed: f.claimed}, f.err
}

// readyClaimerStore is a DoltStorage whose only real method is ReadyClaimer.
type readyClaimerStore struct {
	DoltStorage
	claimer issueops.ReadyClaimer
	err     error
}

func (s readyClaimerStore) ReadyClaimer() (issueops.ReadyClaimer, error) { return s.claimer, s.err }

// TestHookFiringStoreReadyClaimerLayersHooksOverInner pins the recursion.
// Delegating to the inner store instead would still compile and still satisfy
// Storage, and every claim would silently stop firing the update hook the
// legacy claim paths fire.
func TestHookFiringStoreReadyClaimerLayersHooksOverInner(t *testing.T) {
	inner := &fakeReadyClaimer{}
	store := &HookFiringStore{inner: readyClaimerStore{claimer: inner}}

	claimer, err := store.ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer() error = %v", err)
	}
	hooked, ok := claimer.(*hookReadyClaimer)
	if !ok {
		t.Fatalf("ReadyClaimer() = %T, want *hookReadyClaimer", claimer)
	}
	if hooked.inner != issueops.ReadyClaimer(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's claimer", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreReadyClaimerPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: readyClaimerStore{err: want}}

	claimer, err := store.ReadyClaimer()
	if !errors.Is(err, want) {
		t.Fatalf("ReadyClaimer() error = %v, want %v", err, want)
	}
	if claimer != nil {
		t.Fatalf("ReadyClaimer() = %T, want nil", claimer)
	}
}

// TestHookReadyClaimerFiresOnlyForAWonRow pins the empty-front rule. An empty
// ready front is the steady state of a drained queue and a polling agent hits
// it constantly; firing a hook for it would run every on_update script on a
// loop for an issue that does not exist.
func TestHookReadyClaimerFiresOnlyForAWonRow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		claimer *fakeReadyClaimer
		want    []string
	}{
		{
			name:    "won row fires update",
			claimer: &fakeReadyClaimer{claimed: &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-1"}}},
			want:    []string{"update"},
		},
		{
			name:    "empty front fires nothing",
			claimer: &fakeReadyClaimer{},
			want:    nil,
		},
		{
			name:    "error fires nothing",
			claimer: &fakeReadyClaimer{claimed: &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-1"}}, err: errors.New("boom")},
			want:    nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			claimer := &hookReadyClaimer{inner: tc.claimer, hooks: recorder}

			_, _ = claimer.ClaimNext(context.Background(), issueops.ClaimNextRequest{Actor: "worker"})

			if !reflect.DeepEqual(recorder.completions, tc.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, tc.want)
			}
		})
	}
}
