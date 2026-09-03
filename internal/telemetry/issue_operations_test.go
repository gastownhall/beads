package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

type fakeIssueOperations struct {
	calls int
	err   error
}

func (f *fakeIssueOperations) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	f.calls++
	return issueops.CreateResult{}, f.err
}
func (f *fakeIssueOperations) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	f.calls++
	return issueops.UpdateResult{}, f.err
}
func (f *fakeIssueOperations) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	f.calls++
	return issueops.CloseResult{}, f.err
}
func (f *fakeIssueOperations) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	f.calls++
	return issueops.ReopenResult{}, f.err
}

// lifecycleDoltStore is a DoltStorage whose only real method is IssueLifecycle,
// so a test can see which lifecycle the instrumented layer recursed into.
type lifecycleDoltStore struct {
	storage.DoltStorage
	lifecycle issueops.Lifecycle
	err       error
}

func (s *lifecycleDoltStore) IssueLifecycle() (issueops.Lifecycle, error) {
	return s.lifecycle, s.err
}

// TestInstrumentedStorageIssueLifecycleInstrumentsInner pins the recursion:
// delegating to the inner store would compile and return an unspanned,
// untimed lifecycle.
func TestInstrumentedStorageIssueLifecycleInstrumentsInner(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	inner := &fakeIssueOperations{}
	wrapped := WrapStorage(&lifecycleDoltStore{lifecycle: inner}).(*InstrumentedStorage)

	lifecycle, err := wrapped.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() error = %v", err)
	}
	instrumented, ok := lifecycle.(*instrumentedIssueOperations)
	if !ok {
		t.Fatalf("IssueLifecycle() = %T, want *instrumentedIssueOperations", lifecycle)
	}
	if instrumented.inner != issueops.Lifecycle(inner) {
		t.Fatalf("telemetry layer wraps %#v, want the inner store's lifecycle", instrumented.inner)
	}
}

func TestInstrumentedStorageIssueLifecyclePropagatesInnerError(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	want := errors.New("inner refused")
	wrapped := WrapStorage(&lifecycleDoltStore{err: want}).(*InstrumentedStorage)

	lifecycle, err := wrapped.IssueLifecycle()
	if !errors.Is(err, want) {
		t.Fatalf("IssueLifecycle() error = %v, want %v", err, want)
	}
	if lifecycle != nil {
		t.Fatalf("IssueLifecycle() = %T, want nil", lifecycle)
	}
}

func TestInstrumentedIssueOperationsForwardsEveryAttemptOnce(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	base := WrapStorage(&fakeDoltStore{}).(*InstrumentedStorage)
	for _, test := range []struct {
		name string
		call func(issueops.Lifecycle) error
	}{
		{"create", func(o issueops.Lifecycle) error {
			_, e := o.Create(context.Background(), issueops.CreateRequest{})
			return e
		}},
		{"update", func(o issueops.Lifecycle) error {
			_, e := o.Update(context.Background(), issueops.UpdateRequest{})
			return e
		}},
		{"close", func(o issueops.Lifecycle) error {
			_, e := o.Close(context.Background(), issueops.CloseRequest{})
			return e
		}},
		{"reopen", func(o issueops.Lifecycle) error {
			_, e := o.Reopen(context.Background(), issueops.ReopenRequest{})
			return e
		}},
	} {
		t.Run(test.name+" success", func(t *testing.T) {
			fake := &fakeIssueOperations{}
			if err := test.call(base.WrapIssueOperations(fake)); err != nil || fake.calls != 1 {
				t.Fatalf("err=%v calls=%d", err, fake.calls)
			}
		})
		t.Run(test.name+" error", func(t *testing.T) {
			want := errors.New("underlying")
			fake := &fakeIssueOperations{err: want}
			if err := test.call(base.WrapIssueOperations(fake)); !errors.Is(err, want) || fake.calls != 1 {
				t.Fatalf("err=%v calls=%d", err, fake.calls)
			}
		})
	}
}
