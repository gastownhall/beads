package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

type fakeIssueClaimer struct {
	calls int
	err   error
}

func (f *fakeIssueClaimer) Claim(context.Context, issueops.ClaimRequest) (issueops.ClaimResult, error) {
	f.calls++
	return issueops.ClaimResult{}, f.err
}

// claimerDoltStore is a DoltStorage whose only real method is IssueClaimer, so
// a test can see which claimer the instrumented layer recursed into.
type claimerDoltStore struct {
	storage.DoltStorage
	claimer issueops.Claimer
	err     error
}

func (s *claimerDoltStore) IssueClaimer() (issueops.Claimer, error) { return s.claimer, s.err }

// TestInstrumentedStorageIssueClaimerInstrumentsInner pins the recursion, the
// same way its lifecycle and reader siblings do: `return
// s.Unwrap().IssueClaimer()` compiles, satisfies Storage, passes every other
// test, and leaves the one write on this role unspanned forever.
func TestInstrumentedStorageIssueClaimerInstrumentsInner(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	inner := &fakeIssueClaimer{}
	wrapped := WrapStorage(&claimerDoltStore{claimer: inner}).(*InstrumentedStorage)

	claimer, err := wrapped.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer() error = %v", err)
	}
	instrumented, ok := claimer.(*instrumentedIssueClaimer)
	if !ok {
		t.Fatalf("IssueClaimer() = %T, want *instrumentedIssueClaimer", claimer)
	}
	if instrumented.inner != issueops.Claimer(inner) {
		t.Fatalf("telemetry layer wraps %#v, want the inner store's claimer", instrumented.inner)
	}
}

func TestInstrumentedStorageIssueClaimerPropagatesInnerError(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	want := errors.New("inner refused")
	wrapped := WrapStorage(&claimerDoltStore{err: want}).(*InstrumentedStorage)

	claimer, err := wrapped.IssueClaimer()
	if !errors.Is(err, want) {
		t.Fatalf("IssueClaimer() error = %v, want %v", err, want)
	}
	if claimer != nil {
		t.Fatalf("IssueClaimer() = %T, want nil", claimer)
	}
}

// TestInstrumentedIssueClaimerForwardsEveryCallOnce: the span wrapper must not
// swallow, duplicate or reorder the claim, on either outcome.
func TestInstrumentedIssueClaimerForwardsEveryCallOnce(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	base := WrapStorage(&fakeDoltStore{}).(*InstrumentedStorage)

	t.Run("success", func(t *testing.T) {
		fake := &fakeIssueClaimer{}
		if _, err := base.WrapIssueClaimer(fake).Claim(context.Background(), issueops.ClaimRequest{}); err != nil || fake.calls != 1 {
			t.Fatalf("err=%v calls=%d", err, fake.calls)
		}
	})
	t.Run("failure", func(t *testing.T) {
		want := errors.New("inner refused")
		fake := &fakeIssueClaimer{err: want}
		if _, err := base.WrapIssueClaimer(fake).Claim(context.Background(), issueops.ClaimRequest{}); !errors.Is(err, want) || fake.calls != 1 {
			t.Fatalf("err=%v calls=%d", err, fake.calls)
		}
	})
}
