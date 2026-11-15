package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// sqlStateError is a driver error carrying a SQLSTATE, the shape pgconn.PgError
// and every other driver error uow.IsSerializationError reads.
type sqlStateError struct{ state string }

func (e *sqlStateError) Error() string {
	return "ERROR: could not serialize access (SQLSTATE " + e.state + ")"
}
func (e *sqlStateError) SQLState() string { return e.state }

// contendedGateRoles fails the first len(errs) updates with errs, then
// behaves as fakeGateRoles. A failed update writes nothing, except failed
// update number landed (1-based): it lands first and is answered with its
// error anyway, as a gateway's 503 can answer a write the server committed.
// Once every one of errs is spent, a read answers readErr if it is set.
type contendedGateRoles struct {
	*fakeGateRoles
	errs    []error
	landed  int
	failed  int
	readErr error
}

func (c *contendedGateRoles) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	if c.readErr != nil && len(c.errs) == 0 {
		return nil, c.readErr
	}
	return c.fakeGateRoles.Get(ctx, req)
}

func (c *contendedGateRoles) Update(ctx context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		c.failed++
		if c.failed == c.landed {
			if _, applyErr := c.fakeGateRoles.Update(ctx, req); applyErr != nil {
				return issueops.UpdateResult{}, applyErr
			}
			return issueops.UpdateResult{}, err
		}
		c.updates = append(c.updates, req)
		return issueops.UpdateResult{}, err
	}
	return c.fakeGateRoles.Update(ctx, req)
}

// TestAddGateWaiterRetriesTransientContention: contention the store gave up
// replaying is one more lost round, not a failure, but a costly one. The
// store's transaction envelope replays a serialization failure or deadlock
// (Dolt and MySQL 1213/1205, SQLSTATE 40001/40P01) until its budget is spent
// and then returns it; over HTTP bd serve answers that spent budget, or a
// refused admission, as 503 busy (wire.ErrBusy). The loop retries them under
// the same backoff as a version miss, but only gateAddWaiterContentionRetries
// times, because each such write has already spent a whole replay budget.
// What makes a retry safe is the round itself (re-read, deduplicate,
// version-guarded whole-list write), which the busy-after-commit case pins: a
// 503 is not proof that nothing was written.
func TestAddGateWaiterRetriesTransientContention(t *testing.T) {
	setGateTestActor(t)
	origSleep := gateAddWaiterSleep
	t.Cleanup(func() { gateAddWaiterSleep = origSleep })
	var sleeps []time.Duration
	gateAddWaiterSleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	gate := func() *types.Issue {
		return &types.Issue{ID: "bd-g", IssueType: "gate", Waiters: []string{"a"}, RowVersion: 7}
	}
	doltSerialization := fmt.Errorf("update: %w", &mysql.MySQLError{Number: 1213, Message: "serialization failure: this transaction conflicts with a committed transaction from another client, try restarting transaction"})

	for _, tt := range []struct {
		name string
		err  error
	}{
		{"serialization failure", fmt.Errorf("update: %w", &sqlStateError{state: "40001"})},
		{"deadlock", fmt.Errorf("update: %w", &sqlStateError{state: "40P01"})},
		{"Dolt serialization failure (1213)", doltSerialization},
		{"lock wait timeout (1205)", fmt.Errorf("update: %w", &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded; try restarting transaction"})},
		{"bd serve busy", fmt.Errorf("updateIssue: %w", wire.ErrBusy)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sleeps = nil
			roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: []error{tt.err, tt.err}}
			added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
			if err != nil || !added {
				t.Fatalf("addGateWaiter = %v, %v; want added after two contended rounds", added, err)
			}
			if len(roles.updates) != 3 || len(sleeps) != 2 {
				t.Fatalf("updates = %d, sleeps = %d; want 3 writes with a backoff before each retry", len(roles.updates), len(sleeps))
			}
			if got := roles.issues["bd-g"].Waiters; !reflect.DeepEqual(got, []string{"a", "b"}) {
				t.Errorf("waiters = %v, want [a b]", got)
			}
		})
	}

	for _, tt := range []struct {
		name string
		err  error
	}{
		{"bd serve busy", wire.ErrBusy},
		{"Dolt serialization failure", doltSerialization},
	} {
		t.Run(tt.name+" gets only the contention retries", func(t *testing.T) {
			sleeps = nil
			roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: slices.Repeat([]error{tt.err}, gateAddWaiterContentionRetries+2)}
			_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
			if !errors.Is(err, tt.err) {
				t.Fatalf("addGateWaiter error = %v, want the last contended write's %v", err, tt.err)
			}
			if want := gateAddWaiterContentionRetries + 1; len(roles.updates) != want {
				t.Fatalf("updates = %d, want %d: each contended write already spent a whole replay budget", len(roles.updates), want)
			}
			if len(sleeps) != gateAddWaiterContentionRetries {
				t.Errorf("sleeps = %v, want one per retry and none after the last contended write", sleeps)
			}
		})
	}

	t.Run("version misses do not spend the contention retries", func(t *testing.T) {
		roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: []error{
			wire.ErrBusy, wire.ErrBusy, issueops.ErrVersionMismatch, issueops.ErrVersionMismatch,
		}}
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil || !added || len(roles.updates) != 5 {
			t.Fatalf("addGateWaiter = %v, %v after %d updates; want added on the fifth write", added, err, len(roles.updates))
		}
	})

	for _, tt := range []struct {
		name string
		errs []error
		want string
		is   error
	}{
		{
			"a gate that changed under every write",
			slices.Repeat([]error{issueops.ErrVersionMismatch}, gateAddWaiterMaxAttempts),
			"updating gate: bd-g changed on every one of 20 attempts: ",
			issueops.ErrVersionMismatch,
		},
		{
			"writes lost to contention",
			slices.Repeat([]error{wire.ErrBusy}, gateAddWaiterContentionRetries+1),
			"updating gate: no write to bd-g was confirmed in 3 attempts (3 answered busy or lost to write contention): ",
			wire.ErrBusy,
		},
		{
			"one contended write among version misses",
			append([]error{wire.ErrBusy}, slices.Repeat([]error{issueops.ErrVersionMismatch}, gateAddWaiterMaxAttempts-1)...),
			"updating gate: no write to bd-g was confirmed in 20 attempts (1 answered busy or lost to write contention): ",
			issueops.ErrVersionMismatch,
		},
	} {
		t.Run("the exhaustion error after "+tt.name, func(t *testing.T) {
			roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: tt.errs}
			_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
			if !errors.Is(err, tt.is) {
				t.Fatalf("addGateWaiter error = %v, want it to wrap %v", err, tt.is)
			}
			if got := err.Error(); got != tt.want+tt.is.Error() {
				t.Errorf("addGateWaiter error = %q, want %q", got, tt.want+tt.is.Error())
			}
		})
	}

	t.Run("a busy answer to a write that landed adds the waiter once", func(t *testing.T) {
		roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: []error{wire.ErrBusy}, landed: 1}
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil {
			t.Fatalf("addGateWaiter error = %v, want the retry's re-read to find the landed waiter", err)
		}
		if added {
			t.Errorf("addGateWaiter reported a second add; the re-read must find b already registered")
		}
		if len(roles.updates) != 1 {
			t.Errorf("updates = %d, want only the write that landed", len(roles.updates))
		}
		if got := roles.issues["bd-g"].Waiters; !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Errorf("waiters = %v, want [a b] with b exactly once", got)
		}
	})

	for _, tt := range []struct {
		name string
		errs []error
	}{
		{"the contention bound", slices.Repeat([]error{wire.ErrBusy}, gateAddWaiterContentionRetries+1)},
		{"the attempt bound", append(slices.Repeat([]error{issueops.ErrVersionMismatch}, gateAddWaiterMaxAttempts-1), wire.ErrBusy)},
	} {
		t.Run("a busy answer to the last write, at "+tt.name+", that landed is found by one more read", func(t *testing.T) {
			sleeps = nil
			roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: tt.errs, landed: len(tt.errs)}
			added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
			if err != nil || added {
				t.Fatalf("addGateWaiter = %v, %v; want no error and no second add once the last read finds b", added, err)
			}
			if len(roles.updates) != len(tt.errs) || len(sleeps) != len(tt.errs)-1 {
				t.Fatalf("updates = %d, sleeps = %d; want %d writes and no wait before the last read", len(roles.updates), len(sleeps), len(tt.errs))
			}
			if got := roles.issues["bd-g"].Waiters; !reflect.DeepEqual(got, []string{"a", "b"}) {
				t.Errorf("waiters = %v, want [a b] with b exactly once", got)
			}
		})
	}

	t.Run("a failed read after the last busy write confirms nothing", func(t *testing.T) {
		errs := slices.Repeat([]error{wire.ErrBusy}, gateAddWaiterContentionRetries+1)
		roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: errs, landed: len(errs), readErr: fmt.Errorf("getIssue: %w", wire.ErrBusy)}
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		want := "updating gate: no write to bd-g was confirmed in 3 attempts (3 answered busy or lost to write contention): " + wire.ErrBusy.Error()
		if err == nil || err.Error() != want {
			t.Fatalf("addGateWaiter error = %v, want %q", err, want)
		}
	})

	for _, tt := range []struct {
		name string
		err  error
	}{
		{"a server fault (500, an indeterminate commit)", fmt.Errorf("updateIssue: %w", wire.ErrServerFault)},
		{"an unreachable database", fmt.Errorf("updateIssue: %w", wire.ErrDBUnavailable)},
	} {
		t.Run(tt.name+" is not retried", func(t *testing.T) {
			roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: []error{tt.err}}
			_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
			if !errors.Is(err, tt.err) || len(roles.updates) != 1 {
				t.Fatalf("addGateWaiter = %v after %d updates, want %v after one write", err, len(roles.updates), tt.err)
			}
		})
	}

	t.Run("a busy answer's Retry-After is waited out whole, plus the jitter", func(t *testing.T) {
		busy := &wire.ProblemError{Status: 503, Code: "busy", RetryAfter: 5 * time.Second, Err: wire.ErrBusy}
		sleeps = nil
		roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: []error{fmt.Errorf("updateIssue: %w", busy), wire.ErrBusy}}
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil || !added {
			t.Fatalf("addGateWaiter = %v, %v; want added after two busy rounds", added, err)
		}
		if len(sleeps) != 2 {
			t.Fatalf("sleeps = %v, want one per retry", sleeps)
		}
		if lo, hi := busy.RetryAfter+gateAddWaiterBackoffBase/2, busy.RetryAfter+gateAddWaiterBackoffBase; sleeps[0] < lo || sleeps[0] >= hi {
			t.Errorf("wait after Retry-After 5s = %s, want the whole 5s plus the first backoff, in [%s, %s)", sleeps[0], lo, hi)
		}
		if lo, hi := gateAddWaiterBackoffBase, 2*gateAddWaiterBackoffBase; sleeps[1] < lo || sleeps[1] >= hi {
			t.Errorf("wait after a busy answer with no Retry-After = %s, want the plain second backoff, in [%s, %s)", sleeps[1], lo, hi)
		}
	})

	t.Run("another SQLSTATE is not retried", func(t *testing.T) {
		roles := &contendedGateRoles{fakeGateRoles: newFakeGateRoles(gate()), errs: []error{&sqlStateError{state: "23505"}}}
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err == nil || len(roles.updates) != 1 {
			t.Fatalf("addGateWaiter = %v after %d updates, want one failed write", err, len(roles.updates))
		}
	})
}
