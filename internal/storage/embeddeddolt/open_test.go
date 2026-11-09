//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
)

func TestBuildDSN_SpacesInPath(t *testing.T) {
	// Regression test for #2920: paths with spaces must not be
	// percent-encoded (%20) because the Dolt driver's ParseDataSource
	// uses the path as a literal filesystem path.
	dir := "/Users/bbrenner/Documents/Scripting Projects/beads/.beads/embeddeddolt"
	dsn := buildDSN(dir, "beads")

	if !strings.HasPrefix(dsn, "file://") {
		t.Fatalf("DSN should start with file://, got %q", dsn)
	}

	if strings.Contains(dsn, "%20") {
		t.Errorf("DSN must not percent-encode spaces; got %q", dsn)
	}

	if !strings.Contains(dsn, "Scripting Projects") {
		t.Errorf("DSN must preserve literal spaces in path; got %q", dsn)
	}

	// Verify the path portion is between "file://" and "?"
	afterScheme := strings.TrimPrefix(dsn, "file://")
	qIdx := strings.Index(afterScheme, "?")
	if qIdx == -1 {
		t.Fatalf("DSN missing query parameters: %q", dsn)
	}
	pathPortion := afterScheme[:qIdx]
	if pathPortion != dir {
		t.Errorf("path portion = %q, want %q", pathPortion, dir)
	}
}

func TestBuildDSN_NoDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "")
	if strings.Contains(dsn, "database=") {
		t.Errorf("DSN should not contain database param when empty; got %q", dsn)
	}
}

func TestBuildDSN_WithDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "mydb")
	if !strings.Contains(dsn, "database=mydb") {
		t.Errorf("DSN should contain database=mydb; got %q", dsn)
	}
}

// bd-2nijqd (PR #4284): an embedded open blocked behind another handle on
// the same directory must give up on its own when the caller passed no
// deadline, instead of waiting in the driver's backoff forever. It reports
// ErrOpenTimeout naming the directory and the override knob, the holder
// keeps working, and the database reopens once the holder is gone.
func TestOpenSQL_BlockedOpenWithoutDeadlineIsBounded(t *testing.T) {
	// No t.Parallel: t.Setenv.
	t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "1s")

	dir := t.TempDir()
	initWaitBudgetDatabase(t, dir)

	// Hold the database open, as a concurrent bd process would, keeping
	// the handle queryable.
	holderDB, holdCleanup, err := OpenSQL(context.Background(), dir, waitBudgetDatabase, "")
	if err != nil {
		t.Fatalf("first OpenSQL: %v", err)
	}
	defer func() { _ = holdCleanup() }()

	type result struct {
		cleanup func() error
		err     error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, cleanup, err := OpenSQL(context.Background(), dir, waitBudgetDatabase, "")
		done <- result{cleanup, err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			_ = r.cleanup()
			t.Fatalf("second OpenSQL succeeded after %v while the first handle was held; expected a bounded failure", time.Since(start))
		}
		if !errors.Is(r.err, ErrOpenTimeout) {
			t.Fatalf("second OpenSQL failed after %v with %v; want ErrOpenTimeout", time.Since(start), r.err)
		}
		msg := r.err.Error()
		if !strings.Contains(msg, dir) {
			t.Errorf("ErrOpenTimeout should name the database directory %q: %v", dir, msg)
		}
		if !strings.Contains(msg, "BEADS_EMBEDDED_OPEN_TIMEOUT") {
			t.Errorf("ErrOpenTimeout should name the override knob: %v", msg)
		}
		t.Logf("second OpenSQL failed after %v: %v", time.Since(start), r.err)
	case <-time.After(10 * time.Second):
		t.Fatalf("second OpenSQL with context.Background() still blocked after %v: the 1s open budget did not bound it", time.Since(start))
	}

	// The holder was never touched by the waiter's attempts: it stays
	// queryable.
	if err := holderDB.PingContext(context.Background()); err != nil {
		t.Fatalf("holder lost its handle while the waiter gave up: %v", err)
	}

	// Once the holder is gone the same directory opens again on the same
	// small budget: an uncontended open succeeds on its first attempt.
	if err := holdCleanup(); err != nil {
		t.Fatalf("holder cleanup: %v", err)
	}
	if _, reopenCleanup, err := OpenSQL(context.Background(), dir, waitBudgetDatabase, ""); err != nil {
		t.Fatalf("reopen after holder cleanup: %v", err)
	} else if err := reopenCleanup(); err != nil {
		t.Fatalf("reopen cleanup: %v", err)
	}
}

// waitBudgetDatabase is the database the open-budget tests bootstrap and
// contend on.
const waitBudgetDatabase = "repro"

// initWaitBudgetDatabase bootstraps a real database in dir, the fixture
// shape of TestConcurrencyMultiProcess (concurrency_test.go): opening an
// empty directory may hold no journal lock, so a contention test needs an
// initialized database to hold.
func initWaitBudgetDatabase(t *testing.T, dir string) {
	t.Helper()
	db, cleanup, err := OpenSQL(context.Background(), dir, "", "")
	if err != nil {
		t.Fatalf("init OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	if _, err := db.Exec("CREATE DATABASE " + waitBudgetDatabase); err != nil {
		t.Fatalf("create database: %v", err)
	}
}

// manualBackOff is a scriptable policy for the exhaustedBackOff tests: it
// hands out nexts left to right and Stop once they run out.
type manualBackOff struct {
	nexts      []time.Duration
	resetCalls int
}

func (b *manualBackOff) NextBackOff() time.Duration {
	if len(b.nexts) == 0 {
		return backoff.Stop
	}
	d := b.nexts[0]
	b.nexts = b.nexts[1:]
	return d
}

func (b *manualBackOff) Reset() {
	b.resetCalls++
}

// fakeClock advances by hand so the exponential policy's elapsed-time
// checks run deterministically.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func (c *fakeClock) Now() time.Time { return c.now }

// TestOpenWaitRecordsExhaustion covers the exhaustion record deterministically,
// without an engine: Stop records, Reset clears, caller cancellation takes
// precedence over exhaustion, a permanent error never exhausts, the budget
// stops before an interval that would overshoot it, and successful work is
// unaffected by a clock already past the budget.
func TestOpenWaitRecordsExhaustion(t *testing.T) {
	t.Run("stop_records_and_reset_clears", func(t *testing.T) {
		inner := &manualBackOff{nexts: []time.Duration{time.Second, backoff.Stop}}
		p := &exhaustedBackOff{BackOff: inner}
		if p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("a fresh policy must not report exhaustion")
		}
		if got := p.NextBackOff(); got != time.Second {
			t.Fatalf("NextBackOff = %s, want 1s", got)
		}
		if p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("a pending wait is not exhaustion")
		}
		if got := p.NextBackOff(); got != backoff.Stop {
			t.Fatalf("NextBackOff = %s, want Stop", got)
		}
		if !p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("Stop must record exhaustion")
		}
		p.Reset()
		if p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("Reset must clear exhaustion")
		}
		if inner.resetCalls != 1 {
			t.Fatalf("Reset delegated %d times, want 1", inner.resetCalls)
		}
	})

	t.Run("caller_cancellation_precedence", func(t *testing.T) {
		p := &exhaustedBackOff{BackOff: &manualBackOff{}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := p.NextBackOff(); got != backoff.Stop {
			t.Fatalf("NextBackOff = %s, want Stop", got)
		}
		if p.gaveUpWithoutCancellation(ctx) {
			t.Fatal("observed caller cancellation must take precedence over exhaustion")
		}
		if !p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("a live caller must still see the exhaustion")
		}
	})

	t.Run("permanent_error_is_not_exhaustion", func(t *testing.T) {
		p := &exhaustedBackOff{BackOff: &manualBackOff{nexts: []time.Duration{time.Hour}}}
		wantErr := errors.New("non-retryable")
		if err := backoff.Retry(func() error { return backoff.Permanent(wantErr) }, p); !errors.Is(err, wantErr) {
			t.Fatalf("Retry returned %v, want the permanent error", err)
		}
		if p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("a permanent error ended the retries without the policy giving up; it must not be diagnosed as exhaustion")
		}
	})

	t.Run("budget_stops_before_overshooting_interval", func(t *testing.T) {
		clk := &fakeClock{now: time.Unix(0, 0)}
		bo := backoff.NewExponentialBackOff()
		bo.InitialInterval = time.Second
		bo.RandomizationFactor = 0
		bo.Multiplier = 2
		bo.MaxInterval = 5 * time.Second
		bo.MaxElapsedTime = 5 * time.Second
		bo.Clock = clk
		p := &exhaustedBackOff{BackOff: bo}
		p.Reset() // as the driver does before use

		// Attempt 1 failed; the next wait (1s) fits the 5s budget.
		if got := p.NextBackOff(); got != time.Second {
			t.Fatalf("NextBackOff = %s, want 1s", got)
		}
		clk.advance(time.Second)
		// Attempt 2 failed; elapsed 1s + next 2s still fits.
		if got := p.NextBackOff(); got != 2*time.Second {
			t.Fatalf("NextBackOff = %s, want 2s", got)
		}
		clk.advance(2 * time.Second)
		// Attempt 3 failed; elapsed 3s + next 4s overshoots the 5s
		// budget, so the policy stops now, with budget left — the open
		// never waits the full nominal budget.
		if got := p.NextBackOff(); got != backoff.Stop {
			t.Fatalf("NextBackOff = %s, want Stop before the 4s overshooting wait", got)
		}
		if !p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("the early Stop must record exhaustion")
		}
		if got := bo.GetElapsedTime(); got != 3*time.Second {
			t.Fatalf("elapsed = %s, want 3s of the 5s budget", got)
		}
	})

	t.Run("successful_work_survives_past_budget", func(t *testing.T) {
		clk := &fakeClock{now: time.Unix(0, 0)}
		bo := backoff.NewExponentialBackOff()
		bo.MaxElapsedTime = 5 * time.Second
		bo.Clock = clk
		p := &exhaustedBackOff{BackOff: bo}
		p.Reset()
		clk.advance(10 * time.Second)
		// The open succeeded without the policy being asked for another
		// wait; the clock is long past the budget, but exhaustion is only
		// ever recorded when the policy itself says Stop. (Retry resets
		// the policy before use, as the driver does.)
		if err := backoff.Retry(func() error { return nil }, p); err != nil {
			t.Fatalf("Retry of successful work: %v", err)
		}
		if p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("successful work must not be diagnosed as exhaustion")
		}
		// A failure consulted after the budget is spent does see Stop.
		clk.advance(10 * time.Second)
		if got := p.NextBackOff(); got != backoff.Stop {
			t.Fatalf("NextBackOff = %s, want Stop with the budget spent", got)
		}
		if !p.gaveUpWithoutCancellation(context.Background()) {
			t.Fatal("the policy's Stop must record exhaustion")
		}
	})
}

// TestOpenSQLWaitBudget exercises the open retry budget against a real
// embedded engine held by a second handle: release before exhaustion,
// parent-deadline precedence, caller cancellation, non-retryable open
// failures and uncontended opens must all come out classified correctly.
// Gated like TestConcurrencyMultiProcess (concurrency_test.go): these boot
// real engines and do not run in the short PR lane.
func TestOpenSQLWaitBudget(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt open-budget tests")
	}

	// openWaitResult carries a synchronous OpenSQL outcome out of a
	// goroutine so a stuck open fails the watchdog instead of hanging the
	// test (a cleanup-only cancel cannot rescue a call that never
	// returns).
	type openWaitResult struct {
		db      *sql.DB
		cleanup func() error
		err     error
	}

	// holdWaitBudgetDatabase opens a second handle on the initialized
	// database in dir and holds it for the subtest's duration.
	holdWaitBudgetDatabase := func(t *testing.T, dir string) func() error {
		t.Helper()
		_, hold, err := OpenSQL(context.Background(), dir, waitBudgetDatabase, "")
		if err != nil {
			t.Fatalf("holder OpenSQL: %v", err)
		}
		return hold
	}

	t.Run("release_before_exhaustion", func(t *testing.T) {
		// No t.Parallel: t.Setenv.
		t.Setenv(openWaitBudgetEnv, "20s")
		dir := t.TempDir()
		initWaitBudgetDatabase(t, dir)

		hold := holdWaitBudgetDatabase(t, dir)
		release := sync.OnceFunc(func() { _ = hold() })
		defer release()

		// The holder goes away well before the 20s budget runs out, so
		// the waiter's retries must let it through, not time it out.
		go func() {
			time.Sleep(2 * time.Second)
			release()
		}()

		done := make(chan openWaitResult, 1)
		go func() {
			db, cleanup, err := OpenSQL(context.Background(), dir, waitBudgetDatabase, "")
			done <- openWaitResult{db, cleanup, err}
		}()

		var r openWaitResult
		select {
		case r = <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("waiter still blocked after 30s; release before exhaustion must let it open")
		}
		if r.err != nil {
			t.Fatalf("waiter failed after holder release: %v", r.err)
		}
		defer func() { _ = r.cleanup() }()

		// It did not just open: the handle answers queries.
		var one int
		if err := r.db.QueryRow("SELECT 1").Scan(&one); err != nil {
			t.Fatalf("waiter query after open: %v", err)
		}
		if one != 1 {
			t.Fatalf("SELECT 1 = %d, want 1", one)
		}
	})

	t.Run("parent_deadline_precedence", func(t *testing.T) {
		// No t.Parallel: t.Setenv.
		t.Setenv(openWaitBudgetEnv, "1s")
		dir := t.TempDir()
		initWaitBudgetDatabase(t, dir)

		hold := holdWaitBudgetDatabase(t, dir)
		defer func() { _ = hold() }()

		// The caller's 4s deadline must win over the 1s environment
		// budget: the open waits past the budget and ends on the
		// caller's cancellation, never as ErrOpenTimeout.
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		start := time.Now()
		done := make(chan error, 1)
		go func() {
			_, cleanup, err := OpenSQL(ctx, dir, waitBudgetDatabase, "")
			if err == nil {
				_ = cleanup()
				err = errors.New("open succeeded while the holder was held")
			}
			done <- err
		}()

		select {
		case err := <-done:
			if errors.Is(err, ErrOpenTimeout) {
				t.Fatalf("the parent deadline must take precedence over the environment budget; got ErrOpenTimeout: %v", err)
			}
			if elapsed := time.Since(start); elapsed < 3*time.Second {
				t.Fatalf("contended open returned after %s, before the 4s parent deadline: the 1s environment budget was applied", elapsed)
			}
			t.Logf("contended open ended on the caller's deadline after %s: %v", time.Since(start), err)
		case <-time.After(15 * time.Second):
			t.Fatalf("contended open did not end on the caller's 4s deadline within 15s")
		}
	})

	t.Run("cancelled_parent_context", func(t *testing.T) {
		// No t.Parallel: t.Setenv.
		t.Setenv(openWaitBudgetEnv, "1s")
		dir := t.TempDir()
		initWaitBudgetDatabase(t, dir)

		hold := holdWaitBudgetDatabase(t, dir)
		defer func() { _ = hold() }()

		// A caller that already cancelled must see its own cancellation,
		// not a budget diagnosis, and must not hang.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)
		go func() {
			_, cleanup, err := OpenSQL(ctx, dir, waitBudgetDatabase, "")
			if err == nil {
				_ = cleanup()
				err = errors.New("open succeeded while the holder was held")
			}
			done <- err
		}()

		select {
		case err := <-done:
			if errors.Is(err, ErrOpenTimeout) {
				t.Fatalf("a cancelled caller must not be diagnosed as ErrOpenTimeout: %v", err)
			}
			t.Logf("open with a pre-cancelled context returned %v", err)
		case <-time.After(15 * time.Second):
			t.Fatalf("open with a pre-cancelled context did not return within 15s")
		}
	})
}
