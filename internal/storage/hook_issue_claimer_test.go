package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeIssueClaimer records every call and answers with whatever the test set.
type fakeIssueClaimer struct {
	calls   int
	changed bool
	err     error
}

func (f *fakeIssueClaimer) Claim(context.Context, issueops.ClaimRequest) (issueops.ClaimResult, error) {
	f.calls++
	return issueops.ClaimResult{Issue: &types.Issue{ID: "bd-1"}, Changed: f.changed}, f.err
}

// claimerStore is a DoltStorage whose only real method is IssueClaimer.
type claimerStore struct {
	DoltStorage
	claimer issueops.Claimer
	err     error
}

func (s claimerStore) IssueClaimer() (issueops.Claimer, error) { return s.claimer, s.err }

// TestHookFiringStoreIssueClaimerLayersHooksOverInner pins the recursion. A
// claim is a write, so this accessor answers the way IssueLifecycle does and
// not the way IssueReader does: delegating to the inner store would compile,
// satisfy Storage, and silently stop firing the hook every claim owes.
func TestHookFiringStoreIssueClaimerLayersHooksOverInner(t *testing.T) {
	inner := &fakeIssueClaimer{}
	store := &HookFiringStore{inner: claimerStore{claimer: inner}}

	claimer, err := store.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer() error = %v", err)
	}
	hooked, ok := claimer.(*hookIssueClaimer)
	if !ok {
		t.Fatalf("IssueClaimer() = %T, want *hookIssueClaimer", claimer)
	}
	if hooked.inner != issueops.Claimer(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's claimer", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreIssueClaimerPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: claimerStore{err: want}}

	claimer, err := store.IssueClaimer()
	if !errors.Is(err, want) {
		t.Fatalf("IssueClaimer() error = %v, want %v", err, want)
	}
	if claimer != nil {
		t.Fatalf("IssueClaimer() = %T, want nil", claimer)
	}
}

// TestHookIssueClaimerFiresOnlyOnAPersistedClaim: the idempotent re-claim wrote
// nothing, so it must not fire an update hook — the same no-op suppression
// Reopen already applies, and the reason it matters here is that a polling
// agent re-claiming its own issue would otherwise run the user's hook script
// once per poll.
func TestHookIssueClaimerFiresOnlyOnAPersistedClaim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
		err     error
		want    []string
	}{
		{name: "claim landed", changed: true, want: []string{"update"}},
		{name: "idempotent re-claim", changed: false},
		{name: "refused", changed: true, err: errors.New("refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &fakeIssueClaimer{changed: tc.changed, err: tc.err}
			recorder := &recordingIssueOperationHooks{}
			claimer := &hookIssueClaimer{inner: inner, hooks: recorder}

			_, err := claimer.Claim(context.Background(), issueops.ClaimRequest{Actor: "alice", IssueID: "bd-1"})
			if !errors.Is(err, tc.err) {
				t.Fatalf("Claim() error = %v, want %v", err, tc.err)
			}
			if inner.calls != 1 {
				t.Errorf("inner claimer called %d times, want 1", inner.calls)
			}
			if len(recorder.completions) != len(tc.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, tc.want)
			}
			for i, want := range tc.want {
				if recorder.completions[i] != want {
					t.Errorf("hook %d = %q, want %q", i, recorder.completions[i], want)
				}
			}
		})
	}
}
