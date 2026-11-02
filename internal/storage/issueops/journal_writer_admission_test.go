package issueops

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestJournalWriterWaitSecondsStaysBelowTheReadTimeout pins the GET_LOCK wait
// for one admission attempt (gastownhall/beads#6117): a whole second only when
// it fits inside both the remaining admission time and the pool's read
// deadline with a second of headroom, an immediate try otherwise. A subsecond
// read timeout must never see a one-second wait, or a contended wait would be
// cut off as an i/o timeout and the session discarded.
func TestJournalWriterWaitSecondsStaysBelowTheReadTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		remaining   time.Duration
		readTimeout time.Duration
		want        int
	}{
		{"no read timeout", 10 * time.Second, 0, 1},
		{"subsecond read timeout", 10 * time.Second, 500 * time.Millisecond, 0},
		{"read timeout at the headroom", 10 * time.Second, 2 * time.Second, 0},
		{"read timeout past the headroom", 10 * time.Second, 3 * time.Second, 1},
		{"subsecond remaining", 900 * time.Millisecond, 0, 0},
		{"one second remaining", time.Second, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := journalWriterWaitSeconds(tc.remaining, tc.readTimeout); got != tc.want {
				t.Fatalf("journalWriterWaitSeconds(%s, %s) = %d, want %d", tc.remaining, tc.readTimeout, got, tc.want)
			}
		})
	}
}

// flagCanceledContext is a context whose Err turns Canceled once canceled is
// set, with no Done channel: database/sql and sqlmock never see the
// cancellation, so a query in flight completes and returns its grant, the way a
// GET_LOCK answer can cross a caller's cancellation on the wire.
type flagCanceledContext struct {
	context.Context
	canceled atomic.Bool
}

func (c *flagCanceledContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func newAdmissionMock(t *testing.T) (*sql.Conn, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, db, mock
}

// TestAcquireJournalWriter_LateGrantIsReleasedAndRefused pins the admission
// deadline at the grant (gastownhall/beads#6117): a GET_LOCK grant that comes
// back after AdmitBy is released at once, and the writer is refused busy
// before any transaction begins, its session kept clean.
func TestAcquireJournalWriter_LateGrantIsReleasedAndRefused(t *testing.T) {
	conn, db, mock := newAdmissionMock(t)
	name := JournalWriterLockName("beads")
	mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs(name, 0).
		WillDelayFor(200 * time.Millisecond).
		WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
	mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs(name).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	lock, err := AcquireJournalWriter(context.Background(), conn, JournalWriterAdmission{
		Database: "beads",
		AdmitBy:  time.Now().Add(50 * time.Millisecond),
	})
	if lock != nil {
		t.Fatal("a grant past the admission deadline was handed to the writer")
	}
	var busy *JournalWriterBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("late grant = %v, want a *JournalWriterBusyError", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("want the late grant released: %v", err)
	}
	if open := db.Stats().OpenConnections; open != 1 {
		t.Fatalf("open connections = %d, want the released session kept", open)
	}
}

// TestJournalWriterGrantAfterCancelIsReleased pins both admission helpers
// against a grant that crosses the caller's cancellation
// (gastownhall/beads#6117): the lock is released, the caller gets its
// cancellation (never busy, never a lock), and no body runs.
func TestJournalWriterGrantAfterCancelIsReleased(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, conn *sql.Conn) (*JournalWriterLock, error)
	}{
		{"queued", func(ctx context.Context, conn *sql.Conn) (*JournalWriterLock, error) {
			return AcquireJournalWriter(ctx, conn, JournalWriterAdmission{Database: "beads", AdmitBy: time.Now().Add(time.Minute)})
		}},
		{"advisory", func(ctx context.Context, conn *sql.Conn) (*JournalWriterLock, error) {
			lock, ok, err := TryJournalWriter(ctx, conn, "beads")
			if ok != (lock != nil) {
				t.Fatalf("ok = %v with lock %v", ok, lock)
			}
			return lock, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, db, mock := newAdmissionMock(t)
			name := JournalWriterLockName("beads")
			mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs(name, sqlmock.AnyArg()).
				WillDelayFor(200 * time.Millisecond).
				WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
			mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs(name).
				WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

			ctx := &flagCanceledContext{Context: context.Background()}
			timer := time.AfterFunc(50*time.Millisecond, func() { ctx.canceled.Store(true) })
			defer timer.Stop()
			lock, err := tc.run(ctx, conn)
			if lock != nil {
				t.Fatal("a grant after the caller's cancellation was handed to the writer")
			}
			if !errors.Is(err, context.Canceled) || errors.Is(err, ErrJournalWriterBusy) {
				t.Fatalf("grant after cancel = %v, want the caller's cancellation and not busy", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("want the grant released: %v", err)
			}
			if open := db.Stats().OpenConnections; open != 1 {
				t.Fatalf("open connections = %d, want the released session kept", open)
			}
		})
	}
}

// TestPinJournalWriterSession_SaturatedPoolExpires pins the pool wait against
// the admission deadline (gastownhall/beads#6117): with every pooled session
// busy, a journal writer is refused busy at AdmitBy rather than waiting on the
// pool past it; a caller whose own deadline is the bound gets
// DeadlineExceeded, and a canceled caller its cancellation, never busy.
func TestPinJournalWriterSession_SaturatedPoolExpires(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ctx      func() (context.Context, context.CancelFunc)
		admitBy  time.Duration
		wantBusy bool
		wantErr  error
	}{
		{name: "admission deadline", admitBy: 100 * time.Millisecond, wantBusy: true,
			ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{name: "caller deadline", admitBy: 100 * time.Millisecond, wantErr: context.DeadlineExceeded,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			}},
		{name: "caller cancel", admitBy: time.Minute, wantErr: context.Canceled,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db, _ := newAdmissionMock(t)
			db.SetMaxOpenConns(1)
			ctx, cancel := tc.ctx()
			defer cancel()
			admitBy := time.Now().Add(tc.admitBy)
			if deadline, ok := ctx.Deadline(); ok && deadline.Before(admitBy) {
				admitBy = deadline
			}
			start := time.Now()
			conn, err := PinJournalWriterSession(ctx, db, "beads", admitBy)
			if conn != nil {
				_ = conn.Close()
				t.Fatal("pinned a session from a saturated pool")
			}
			if waited := time.Since(start); waited > 5*time.Second {
				t.Fatalf("pin gave up after %s, want it bounded by the admission deadline", waited)
			}
			if busy := errors.Is(err, ErrJournalWriterBusy); busy != tc.wantBusy {
				t.Fatalf("pin = %v, busy %v, want busy %v", err, busy, tc.wantBusy)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("pin = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
