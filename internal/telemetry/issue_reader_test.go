package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

type fakeIssueReader struct {
	calls int
	err   error
}

func (f *fakeIssueReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	f.calls++
	return issueops.IssuePage{}, f.err
}
func (f *fakeIssueReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	f.calls++
	return issueops.IssuePage{}, f.err
}
func (f *fakeIssueReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	f.calls++
	return nil, f.err
}

// readerDoltStore is a DoltStorage whose only real method is IssueReader, so a
// test can see which reader the instrumented layer recursed into.
type readerDoltStore struct {
	storage.DoltStorage
	reader issueops.Reader
	err    error
}

func (s *readerDoltStore) IssueReader() (issueops.Reader, error) { return s.reader, s.err }

// TestInstrumentedStorageIssueReaderInstrumentsInner pins the recursion, the
// same way TestInstrumentedStorageIssueLifecycleInstrumentsInner pins the write
// side. `return s.Unwrap().IssueReader()` is a one-line edit that compiles,
// satisfies Storage, and passes every other test in the tree — and every read
// goes unspanned while writes stay instrumented.
func TestInstrumentedStorageIssueReaderInstrumentsInner(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	inner := &fakeIssueReader{}
	wrapped := WrapStorage(&readerDoltStore{reader: inner}).(*InstrumentedStorage)

	reader, err := wrapped.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader() error = %v", err)
	}
	instrumented, ok := reader.(*instrumentedIssueReader)
	if !ok {
		t.Fatalf("IssueReader() = %T, want *instrumentedIssueReader", reader)
	}
	if instrumented.inner != issueops.Reader(inner) {
		t.Fatalf("telemetry layer wraps %#v, want the inner store's reader", instrumented.inner)
	}
}

func TestInstrumentedStorageIssueReaderPropagatesInnerError(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	want := errors.New("inner refused")
	wrapped := WrapStorage(&readerDoltStore{err: want}).(*InstrumentedStorage)

	reader, err := wrapped.IssueReader()
	if !errors.Is(err, want) {
		t.Fatalf("IssueReader() error = %v, want %v", err, want)
	}
	if reader != nil {
		t.Fatalf("IssueReader() = %T, want nil", reader)
	}
}

// TestInstrumentedIssueReaderForwardsEveryCallOnce: the span wrapper must not
// swallow, duplicate or reorder a call, on either outcome.
func TestInstrumentedIssueReaderForwardsEveryCallOnce(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	base := WrapStorage(&fakeDoltStore{}).(*InstrumentedStorage)
	for _, test := range []struct {
		name string
		call func(issueops.Reader) error
	}{
		{"ready", func(r issueops.Reader) error {
			_, e := r.Ready(context.Background(), issueops.ReadyRequest{})
			return e
		}},
		{"list", func(r issueops.Reader) error {
			_, e := r.List(context.Background(), issueops.ListRequest{})
			return e
		}},
		{"get", func(r issueops.Reader) error {
			_, e := r.Get(context.Background(), issueops.GetRequest{})
			return e
		}},
	} {
		t.Run(test.name+" success", func(t *testing.T) {
			fake := &fakeIssueReader{}
			if err := test.call(base.WrapIssueReader(fake)); err != nil || fake.calls != 1 {
				t.Fatalf("err=%v calls=%d", err, fake.calls)
			}
		})
		t.Run(test.name+" failure", func(t *testing.T) {
			want := errors.New("inner refused")
			fake := &fakeIssueReader{err: want}
			if err := test.call(base.WrapIssueReader(fake)); !errors.Is(err, want) || fake.calls != 1 {
				t.Fatalf("err=%v calls=%d", err, fake.calls)
			}
		})
	}
}
