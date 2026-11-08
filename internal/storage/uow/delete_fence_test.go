package uow

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	publicops "github.com/steveyegge/beads/issueops"
)

// opaqueWrapper stands in for a per-request provider wrapper such as bd serve's
// timing wrapper (internal/httpapi's timedProvider): roles are built over IT,
// and it neither unwraps nor answers FenceProvider, which is what that wrapper
// was before it answered FenceProvider. Its NewUOW records the call, runs onOpen
// when one is set, and answers err, so a test sees which provider a write went
// through, and when, without a database behind it.
type opaqueWrapper struct {
	inner  UnitOfWorkProvider
	err    error
	onOpen func(context.Context) error
	opened int
}

func (w *opaqueWrapper) NewUOW(ctx context.Context) (UnitOfWork, error) {
	w.opened++
	if w.onOpen != nil {
		if err := w.onOpen(ctx); err != nil {
			return nil, err
		}
	}
	return nil, w.err
}

func (w *opaqueWrapper) Close(context.Context) error { return nil }

// fencedWrapper is opaqueWrapper answering FenceProvider, the shape bd serve's
// timing wrapper has now: still no Unwrap.
type fencedWrapper struct{ opaqueWrapper }

func (w *fencedWrapper) FenceProvider() UnitOfWorkProvider { return w.inner }

// TestGuardedDeleteTakesTheFenceBeneathAPerRequestWrapper pins the two halves
// of a guarded delete built over a per-request wrapper, the way bd serve builds
// every role: the GET_LOCK fence is taken on the pool of the provider beneath
// the wrapper (found through FenceProvider and then Unwrap), and the write
// still opens its unit of work through the wrapper itself, inside the lock, so
// the request's timing and the policy layers above the bare provider see it.
//
// The hook layer stands in for the external-dependency policy bd serve has
// beneath its wrapper; both peel with Unwrap.
func TestGuardedDeleteTakesTheFenceBeneathAPerRequestWrapper(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	// In order, which sqlmock enforces: the lock session's database, the lock,
	// the write (a marker the wrapper sends when it opens a unit of work), and
	// the release. A write outside the lock breaks the order.
	lock := deleteFenceLockName("beads", "bd-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lock, deleteFenceLockTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"GET_LOCK"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 'write'")).
		WillReturnRows(sqlmock.NewRows([]string{"write"}).AddRow("write"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lock).
		WillReturnRows(sqlmock.NewRows([]string{"RELEASE_LOCK"}).AddRow(1))

	errWrite := errors.New("the write opened its unit of work through the wrapper")
	beneath := NewNotifyingProvider(&doltSQLProvider{db: db}, Sinks{Hook: &notifyRunner{}})
	wrapper := &fencedWrapper{opaqueWrapper{inner: beneath, err: errWrite, onOpen: func(ctx context.Context) error {
		var marker string
		return db.QueryRowContext(ctx, "SELECT 'write'").Scan(&marker)
	}}}
	deleter, err := NewDeleter(wrapper)
	if err != nil {
		t.Fatalf("NewDeleter: %v", err)
	}

	version := int64(41)
	_, err = deleter.Delete(context.Background(), publicops.DeleteRequest{
		IDs: []string{"bd-1"}, ExpectedVersion: &version, Force: true,
	})
	if !errors.Is(err, errWrite) {
		t.Fatalf("Delete = %v, want the wrapper's own answer: the write must open its unit of work "+
			"through the wrapper the role was built over, inside the fence, never through the provider "+
			"holding the fence (a sqlmock error here means the write ran outside the lock)", err)
	}
	if wrapper.opened != 1 {
		t.Errorf("the wrapper opened %d units of work, want 1", wrapper.opened)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the write did not run inside a fence taken and released on the pool beneath the wrapper: %v", err)
	}
}

// TestGuardedDeleteRefusesAProviderChainThatHidesTheFence: a guarded delete
// whose provider chain hides the fence is refused before it reads or writes
// anything, instead of running unfenced. That silent fallback is how bd serve's
// v0 route answered every same-token racer 200 (GH#7243).
//
// The refusal belongs to the guard alone: through the same chain an unguarded
// delete and a guarded dry run, neither of which takes the fence, still go
// through to the wrapper.
func TestGuardedDeleteRefusesAProviderChainThatHidesTheFence(t *testing.T) {
	errWrite := errors.New("the request reached the wrapper")
	wrapper := &opaqueWrapper{inner: &doltSQLProvider{}, err: errWrite}
	deleter, err := NewDeleter(wrapper)
	if err != nil {
		t.Fatalf("NewDeleter: %v", err)
	}
	ctx := context.Background()
	version := int64(41)

	_, err = deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{"bd-1"}, ExpectedVersion: &version, Force: true})
	if !errors.Is(err, errDeleteFenceUnreachable) {
		t.Fatalf("guarded delete through a wrapper hiding the fence = %v, want errDeleteFenceUnreachable", err)
	}
	if wrapper.opened != 0 {
		t.Errorf("the refused delete opened %d units of work, want 0", wrapper.opened)
	}

	for _, tc := range []struct {
		name string
		req  publicops.DeleteRequest
	}{
		{"unguarded", publicops.DeleteRequest{IDs: []string{"bd-1"}, Force: true}},
		{"guarded dry run", publicops.DeleteRequest{IDs: []string{"bd-1"}, ExpectedVersion: &version, DryRun: true}},
	} {
		wrapper.opened = 0
		if _, err := deleter.Delete(ctx, tc.req); !errors.Is(err, errWrite) {
			t.Errorf("%s delete = %v, want it to reach the wrapper", tc.name, err)
		}
		if wrapper.opened != 1 {
			t.Errorf("%s delete opened %d units of work through the wrapper, want 1", tc.name, wrapper.opened)
		}
	}
}
