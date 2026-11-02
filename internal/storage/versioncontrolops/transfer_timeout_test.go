package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// transferEngineSentinel stands in for the engine's own failure observed
// after a canceled transfer statement (GH#7989): the timeout error must
// preserve it, not replace it.
var transferEngineSentinel = errors.New("dolt engine: upload interrupted")

// fakeTransferDB is a DBConn that records the context every ExecContext ran
// under and routes DOLT_PUSH / DOLT_FETCH statements to a per-test network
// function. Everything else succeeds immediately.
type fakeTransferDB struct {
	mu     sync.Mutex
	stmts  []recordedTransferStmt
	network func(ctx context.Context, query string) error
}

type recordedTransferStmt struct {
	query string
	ctx   context.Context
}

func (f *fakeTransferDB) ExecContext(ctx context.Context, query string, _ ...any) (sql.Result, error) {
	f.mu.Lock()
	f.stmts = append(f.stmts, recordedTransferStmt{query: query, ctx: ctx})
	f.mu.Unlock()
	if strings.Contains(query, "DOLT_PUSH") || strings.Contains(query, "DOLT_FETCH") {
		if f.network != nil {
			return nil, f.network(ctx, query)
		}
		return nil, nil
	}
	return nil, nil
}

func (f *fakeTransferDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected query: " + query)
}

func (f *fakeTransferDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return nil
}

// blockUntilCancelled is the "blocked transport" transfer: the statement
// observes cancellation and returns the engine failure plus the context
// error it observed.
func blockUntilCancelled(ctx context.Context, _ string) error {
	<-ctx.Done()
	return errors.Join(transferEngineSentinel, ctx.Err())
}

// watchdogCtx gives every blocked case an independent generous deadline that
// only a regression trips; the assertion after it fails names the real
// expectation.
func watchdogCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// wantTransferTimeout asserts the AC-1 contract on err: DeadlineExceeded
// preserved, engine error preserved, and the operation, configured duration
// and configuration key identified (credentials never).
func wantTransferTimeout(t *testing.T, err error, op string, timeout time.Duration, creds string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error does not preserve context.DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, transferEngineSentinel) {
		t.Fatalf("timeout error does not preserve the engine error: %v", err)
	}
	var tte *TransferTimeoutError
	if !errors.As(err, &tte) {
		t.Fatalf("expected *TransferTimeoutError, got %T: %v", err, err)
	}
	if tte.Op != op {
		t.Fatalf("timeout error names operation %q, want %q", tte.Op, op)
	}
	if tte.Timeout != timeout {
		t.Fatalf("timeout error reports duration %s, want %s", tte.Timeout, timeout)
	}
	msg := err.Error()
	for _, want := range []string{remoteTimeoutConfigKey, remoteTimeoutEnvKey, timeout.String()} {
		if !strings.Contains(msg, want) {
			t.Fatalf("timeout error does not identify %q: %v", want, err)
		}
	}
	if creds != "" && strings.Contains(msg, creds) {
		t.Fatalf("timeout error leaks credentials: %v", err)
	}
}

// wantNoTransferTimeout asserts err is NOT blamed on the remote timeout
// configuration: whatever cancellation or failure produced it keeps its
// identity.
func wantNoTransferTimeout(t *testing.T, err error) {
	t.Helper()
	var tte *TransferTimeoutError
	if errors.As(err, &tte) {
		t.Fatalf("error must not be blamed on %s: %v", remoteTimeoutConfigKey, err)
	}
	if strings.Contains(err.Error(), remoteTimeoutConfigKey) {
		t.Fatalf("error message must not name %s: %v", remoteTimeoutConfigKey, err)
	}
}

const testTransferTimeout = 100 * time.Millisecond

// TestRemoteTransferDeadline pins GH#7989 AC-1: an embedded push or fetch
// statement that stays blocked observes the configured transfer deadline and
// returns a timeout error preserving context.DeadlineExceeded and the
// underlying failure, with the operation, duration and configuration key
// identified. Cases also cover the boundaries the AC pins: fast success,
// error before the deadline, an already-canceled caller, an earlier caller
// deadline, credentials kept out of the message, and no opt-in timeout for
// callers that did not attach one.
func TestRemoteTransferDeadline(t *testing.T) {
	t.Run("push statement site", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := Push(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "main", "")
		if err == nil {
			t.Fatal("push to a blocked transport returned nil")
		}
		wantTransferTimeout(t, err, "push", testTransferTimeout, "")
	})
	t.Run("forced push statement site", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := ForcePush(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "main", "")
		wantTransferTimeout(t, err, "force push", testTransferTimeout, "")
	})
	t.Run("fetch statement site", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := Fetch(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "")
		wantTransferTimeout(t, err, "fetch", testTransferTimeout, "")
	})
	t.Run("pull fetch statement site", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := PullWithStrategy(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "main", "", "")
		wantTransferTimeout(t, err, "pull fetch", testTransferTimeout, "")
	})
	t.Run("explicit credentials stay out of the message", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := Push(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "main", "sekrit-user-4f2a")
		wantTransferTimeout(t, err, "push", testTransferTimeout, "sekrit-user-4f2a")
	})
	t.Run("fast success is not a timeout", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		fake := &fakeTransferDB{}
		if err := Push(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "main", ""); err != nil {
			t.Fatalf("fast push failed: %v", err)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.stmts) != 1 {
			t.Fatalf("expected one recorded statement, got %d", len(fake.stmts))
		}
	})
	t.Run("error before the deadline keeps its identity", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		rejected := errors.Join(transferEngineSentinel, errors.New("remote rejected the ref"))
		fake := &fakeTransferDB{network: func(context.Context, string) error { return rejected }}
		err := Push(WithRemoteTransferTimeout(ctx, 10*time.Minute), fake, "origin", "main", "")
		if !errors.Is(err, transferEngineSentinel) {
			t.Fatalf("error does not preserve the engine failure: %v", err)
		}
		wantNoTransferTimeout(t, err)
	})
	t.Run("already-canceled caller keeps its identity", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		cancel() // before the statement runs
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := Push(WithRemoteTransferTimeout(ctx, testTransferTimeout), fake, "origin", "main", "")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error does not preserve caller cancellation: %v", err)
		}
		wantNoTransferTimeout(t, err)
	})
	t.Run("earlier caller deadline wins", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := Push(WithRemoteTransferTimeout(ctx, 10*time.Minute), fake, "origin", "main", "")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error does not preserve the caller deadline: %v", err)
		}
		wantNoTransferTimeout(t, err)
	})
	t.Run("no attached timeout means no statement deadline", func(t *testing.T) {
		ctx, cancel := watchdogCtx(t)
		defer cancel()
		attachCtx := WithRemoteTransferTimeout(ctx, 0)
		fake := &fakeTransferDB{network: blockUntilCancelled}
		err := Push(attachCtx, fake, "origin", "main", "")
		if !errors.Is(err, transferEngineSentinel) {
			t.Fatalf("error does not preserve the engine failure: %v", err)
		}
		wantNoTransferTimeout(t, err)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.stmts) != 1 {
			t.Fatalf("expected one recorded statement, got %d", len(fake.stmts))
		}
		// The statement ran on the caller's context: the watchdog is still
		// the only deadline it observed.
		stmtCtx := fake.stmts[0].ctx
		stmtDeadline, stmtHas := stmtCtx.Deadline()
		wdDeadline, wdHas := ctx.Deadline()
		if !stmtHas || !wdHas || !stmtDeadline.Equal(wdDeadline) {
			t.Fatalf("statement context %v does not match the caller context %v", stmtCtx, ctx)
		}
	})
}