//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// openWaitResult is what a background OpenSQL call returned.
type openWaitResult struct {
	db      *sql.DB
	cleanup func() error
	err     error
	elapsed time.Duration
}

// initOpenWaitDB creates an initialized embedded database "testdb" under a
// fresh directory and returns the directory. Opening an empty directory with
// no database holds no journal lock, so contention needs a real database.
func initOpenWaitDB(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "embeddeddolt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, cleanup, err := OpenSQL(context.Background(), dir, "", "")
	if err != nil {
		t.Fatalf("init OpenSQL: %v", err)
	}
	if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		_ = cleanup()
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("init cleanup: %v", err)
	}
	return dir
}

// holdOpenWaitDB opens testdb under dir and keeps it open, as a concurrent
// bd process would. release closes the holder once; it is also registered as
// a test cleanup.
func holdOpenWaitDB(t *testing.T, dir string) (holder *sql.DB, release func()) {
	t.Helper()
	db, cleanup, err := OpenSQL(context.Background(), dir, "testdb", "")
	if err != nil {
		t.Fatalf("holder OpenSQL: %v", err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		if err := cleanup(); err != nil {
			t.Errorf("holder cleanup: %v", err)
		}
	}
	t.Cleanup(release)
	return db, release
}

// startOpenWait runs OpenSQL on testdb under dir in the background. The
// returned join cancels ctx's cancel func and waits for the call to return,
// closing any handle it opened; it is registered as a test cleanup after the
// holder's, so it runs first.
func startOpenWait(t *testing.T, ctx context.Context, cancel context.CancelFunc, dir string) (<-chan openWaitResult, func()) {
	t.Helper()
	results := make(chan openWaitResult, 1)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		db, cleanup, err := OpenSQL(ctx, dir, "testdb", "")
		results <- openWaitResult{db: db, cleanup: cleanup, err: err, elapsed: time.Since(start)}
	}()
	joined := false
	join := func() {
		if joined {
			return
		}
		joined = true
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("background OpenSQL did not return 30s after cancellation")
			return
		}
		select {
		case r := <-results:
			if r.err == nil && r.cleanup != nil {
				_ = r.cleanup()
			}
		default:
		}
	}
	t.Cleanup(join)
	return results, join
}

func skipUnlessEmbeddedOpenWait(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

// TestOpenSQLWaitBudget pins how long OpenSQL waits for a database another
// handle holds (bd-2nijqd, PR #4284).
func TestOpenSQLWaitBudget(t *testing.T) {
	skipUnlessEmbeddedOpenWait(t)

	t.Run("held_database_times_out", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "1s")
		dir := initOpenWaitDB(t)
		holder, release := holdOpenWaitDB(t, dir)

		ctx, cancel := context.WithCancel(context.Background())
		results, join := startOpenWait(t, ctx, cancel, dir)

		var r openWaitResult
		select {
		case r = <-results:
		case <-time.After(10 * time.Second):
			join()
			t.Fatalf("OpenSQL without a deadline still waiting on a held database after 10s")
		}
		if r.err == nil {
			_ = r.cleanup()
			t.Fatalf("OpenSQL succeeded after %v while the database was held", r.elapsed)
		}
		t.Logf("OpenSQL failed after %v: %v", r.elapsed, r.err)
		if !errors.Is(r.err, ErrOpenTimeout) {
			t.Fatalf("OpenSQL error = %v, want ErrOpenTimeout", r.err)
		}
		for _, want := range []string{dir, "BEADS_EMBEDDED_OPEN_TIMEOUT"} {
			if !strings.Contains(r.err.Error(), want) {
				t.Errorf("OpenSQL error %q does not name %q", r.err, want)
			}
		}

		var n int
		if err := holder.QueryRow("SELECT 1").Scan(&n); err != nil {
			t.Fatalf("holder query after the waiter gave up: %v", err)
		}
		release()

		db, cleanup, err := OpenSQL(context.Background(), dir, "testdb", "")
		if err != nil {
			t.Fatalf("reopen after holder cleanup: %v", err)
		}
		defer func() { _ = cleanup() }()
		if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
			t.Fatalf("query after reopen: %v", err)
		}
	})

	t.Run("parent_deadline_takes_precedence", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "1s")
		dir := initOpenWaitDB(t)
		holdOpenWaitDB(t, dir)

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		results, join := startOpenWait(t, ctx, cancel, dir)

		select {
		case r := <-results:
			if r.err == nil {
				_ = r.cleanup()
			}
			t.Fatalf("OpenSQL with a 4s caller deadline returned after %v (err %v); want it to wait past the 1s budget for the deadline", r.elapsed, r.err)
		case <-time.After(3 * time.Second):
		}

		var r openWaitResult
		select {
		case r = <-results:
		case <-time.After(30 * time.Second):
			join()
			t.Fatalf("OpenSQL still waiting 30s after its 4s caller deadline")
		}
		if r.err == nil {
			_ = r.cleanup()
			t.Fatalf("OpenSQL succeeded after %v while the database was held", r.elapsed)
		}
		t.Logf("OpenSQL failed after %v: %v", r.elapsed, r.err)
		if errors.Is(r.err, ErrOpenTimeout) {
			t.Errorf("OpenSQL ended by its caller's deadline returned ErrOpenTimeout: %v", r.err)
		}
	})

	t.Run("caller_cancellation_is_not_a_timeout", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "20s")
		dir := initOpenWaitDB(t)
		holdOpenWaitDB(t, dir)

		ctx, cancel := context.WithCancel(context.Background())
		results, join := startOpenWait(t, ctx, cancel, dir)
		time.Sleep(500 * time.Millisecond)
		cancel()

		var r openWaitResult
		select {
		case r = <-results:
		case <-time.After(30 * time.Second):
			join()
			t.Fatalf("OpenSQL still waiting 30s after its caller canceled")
		}
		if r.err == nil {
			_ = r.cleanup()
			t.Fatalf("OpenSQL succeeded after %v while the database was held", r.elapsed)
		}
		if errors.Is(r.err, ErrOpenTimeout) {
			t.Errorf("canceled OpenSQL returned ErrOpenTimeout: %v", r.err)
		}
	})

	t.Run("already_canceled_caller_is_not_a_timeout", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "1ms")
		dir := initOpenWaitDB(t)
		holdOpenWaitDB(t, dir)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		db, cleanup, err := OpenSQL(ctx, dir, "testdb", "")
		if err == nil {
			_ = cleanup()
			t.Fatalf("OpenSQL with a canceled context succeeded on a held database (db %v)", db)
		}
		if errors.Is(err, ErrOpenTimeout) {
			t.Errorf("OpenSQL with a canceled context returned ErrOpenTimeout: %v", err)
		}
	})

	t.Run("non_retryable_error_is_not_a_timeout", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "1ms")
		missing := filepath.Join(t.TempDir(), "missing")
		db, cleanup, err := OpenSQL(context.Background(), missing, "testdb", "")
		if err == nil {
			_ = cleanup()
			t.Fatalf("OpenSQL on a missing directory succeeded (db %v)", db)
		}
		if errors.Is(err, ErrOpenTimeout) {
			t.Errorf("OpenSQL on a missing directory returned ErrOpenTimeout: %v", err)
		}
	})
}

// TestOpenWaitBudget pins how BEADS_EMBEDDED_OPEN_TIMEOUT resolves: positive
// Go durations or bare seconds, and the 30s default for anything else.
func TestOpenWaitBudget(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "")
		if err := os.Unsetenv("BEADS_EMBEDDED_OPEN_TIMEOUT"); err != nil {
			t.Fatalf("unsetenv: %v", err)
		}
		if got := openWaitBudget(); got != 30*time.Second {
			t.Errorf("openWaitBudget() with the variable unset = %v, want 30s", got)
		}
	})

	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", 30 * time.Second},
		{"   ", 30 * time.Second},
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"1ms", time.Millisecond},
		{"90", 90 * time.Second},
		{" 45s ", 45 * time.Second},
		{"\t90\n", 90 * time.Second},
		{"0", 30 * time.Second},
		{"0s", 30 * time.Second},
		{"-5s", 30 * time.Second},
		{"-5", 30 * time.Second},
		{"soon", 30 * time.Second},
		{"5 s", 30 * time.Second},
		{"99999999999999999999", 30 * time.Second},
		{"99999999999999999999h", 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.raw, " ", "_"), func(t *testing.T) {
			t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", tc.raw)
			if got := openWaitBudget(); got != tc.want {
				t.Errorf("openWaitBudget() with %q = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// openWaitClock is a manual backoff.Clock.
type openWaitClock struct{ now time.Time }

func (c *openWaitClock) Now() time.Time { return c.now }

// openWaitTimer is a backoff.Timer that fires at once, advancing clock by
// the interval it was started with instead of sleeping.
type openWaitTimer struct {
	clock *openWaitClock
	c     chan time.Time
}

func (t *openWaitTimer) Start(d time.Duration) {
	t.clock.now = t.clock.now.Add(d)
	t.c = make(chan time.Time, 1)
	t.c <- t.clock.now
}

func (t *openWaitTimer) Stop() {}

func (t *openWaitTimer) C() <-chan time.Time { return t.c }

// retryOpenWait runs backoff.Retry's loop on clock, without real sleeps.
func retryOpenWait(clock *openWaitClock, op backoff.Operation, b backoff.BackOff) error {
	return backoff.RetryNotifyWithTimer(op, b, nil, &openWaitTimer{clock: clock})
}

// newOpenWaitTestBackOff returns an openWaitBackOff over a deterministic
// exponential policy: 1s initial interval doubling, no randomization, and
// the given budget, read from clock.
func newOpenWaitTestBackOff(clock *openWaitClock, budget time.Duration) *openWaitBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = time.Second
	bo.RandomizationFactor = 0
	bo.Multiplier = 2
	bo.MaxInterval = 5 * time.Second
	bo.MaxElapsedTime = budget
	bo.Clock = clock
	wait := &openWaitBackOff{BackOff: bo}
	wait.Reset()
	return wait
}

// TestOpenWaitRecordsExhaustion pins the retry-policy wrapper OpenSQL hands
// the driver and how its record classifies an open error.
func TestOpenWaitRecordsExhaustion(t *testing.T) {
	errLocked := errors.New("the database is locked by another dolt process")

	t.Run("stop_is_recorded_and_reset_clears_it", func(t *testing.T) {
		clock := &openWaitClock{now: time.Unix(0, 0)}
		wait := newOpenWaitTestBackOff(clock, 10*time.Second)

		if next := wait.NextBackOff(); next != time.Second {
			t.Fatalf("first NextBackOff = %v, want 1s", next)
		}
		if wait.exhausted.Load() {
			t.Fatalf("exhaustion recorded within the budget")
		}
		clock.now = clock.now.Add(11 * time.Second)
		if next := wait.NextBackOff(); next != backoff.Stop {
			t.Fatalf("NextBackOff past the budget = %v, want Stop", next)
		}
		if !wait.exhausted.Load() {
			t.Fatalf("Stop not recorded as exhaustion")
		}
		wait.Reset()
		if wait.exhausted.Load() {
			t.Fatalf("Reset did not clear the recorded exhaustion")
		}
		if next := wait.NextBackOff(); next != time.Second {
			t.Fatalf("NextBackOff after Reset = %v, want 1s", next)
		}
	})

	t.Run("stops_before_an_interval_that_overshoots_the_budget", func(t *testing.T) {
		clock := &openWaitClock{now: time.Unix(0, 0)}
		wait := newOpenWaitTestBackOff(clock, 2500*time.Millisecond)

		if next := wait.NextBackOff(); next != time.Second { // elapsed 0 + 1s <= 2.5s
			t.Fatalf("first NextBackOff = %v, want 1s", next)
		}
		clock.now = clock.now.Add(time.Second)
		// elapsed 1s + next 2s overshoots 2.5s: the policy stops early.
		if next := wait.NextBackOff(); next != backoff.Stop {
			t.Fatalf("NextBackOff whose interval overshoots the budget = %v, want Stop", next)
		}
		if !wait.exhausted.Load() {
			t.Fatalf("early Stop not recorded as exhaustion")
		}
	})

	t.Run("caller_cancellation_records_nothing", func(t *testing.T) {
		clock := &openWaitClock{now: time.Unix(0, 0)}
		wait := newOpenWaitTestBackOff(clock, 10*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		err := retryOpenWait(clock, func() error {
			attempts++
			cancel()
			clock.now = clock.now.Add(time.Minute)
			return errLocked
		}, backoff.WithContext(wait, ctx))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Retry after cancellation = %v, want context.Canceled", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
		if wait.exhausted.Load() {
			t.Fatalf("exhaustion recorded for a canceled caller")
		}
		if got := classifyOpenErr(ctx, wait, "/db", 10*time.Second, errLocked); got != errLocked {
			t.Fatalf("classifyOpenErr for a canceled caller = %v, want the error unchanged", got)
		}
	})

	t.Run("permanent_error_records_nothing", func(t *testing.T) {
		clock := &openWaitClock{now: time.Unix(0, 0)}
		wait := newOpenWaitTestBackOff(clock, time.Nanosecond)
		errBad := errors.New("not a dolt database")
		err := retryOpenWait(clock, func() error {
			clock.now = clock.now.Add(time.Minute)
			return backoff.Permanent(errBad)
		}, wait)
		if !errors.Is(err, errBad) {
			t.Fatalf("Retry with a permanent error = %v, want it", err)
		}
		if wait.exhausted.Load() {
			t.Fatalf("exhaustion recorded for a permanent error")
		}
		if got := classifyOpenErr(context.Background(), wait, "/db", time.Nanosecond, errBad); got != errBad {
			t.Fatalf("classifyOpenErr for a permanent error = %v, want the error unchanged", got)
		}
	})

	t.Run("success_after_the_budget_stays_successful", func(t *testing.T) {
		clock := &openWaitClock{now: time.Unix(0, 0)}
		wait := newOpenWaitTestBackOff(clock, time.Second)
		err := retryOpenWait(clock, func() error {
			clock.now = clock.now.Add(time.Minute)
			return nil
		}, wait)
		if err != nil {
			t.Fatalf("Retry of a successful attempt that outlasted the budget = %v, want nil", err)
		}
		if wait.exhausted.Load() {
			t.Fatalf("exhaustion recorded for a successful attempt")
		}
	})

	t.Run("exhaustion_with_a_live_caller_is_a_timeout", func(t *testing.T) {
		clock := &openWaitClock{now: time.Unix(0, 0)}
		wait := newOpenWaitTestBackOff(clock, 2*time.Second)
		err := retryOpenWait(clock, func() error {
			clock.now = clock.now.Add(time.Second)
			return errLocked
		}, wait)
		if !errors.Is(err, errLocked) {
			t.Fatalf("Retry past the budget = %v, want the last attempt's error", err)
		}
		if !wait.exhausted.Load() {
			t.Fatalf("exhaustion not recorded")
		}

		got := classifyOpenErr(context.Background(), wait, "/data/db", 2*time.Second, errLocked)
		if !errors.Is(got, ErrOpenTimeout) || !errors.Is(got, errLocked) {
			t.Fatalf("classifyOpenErr = %v, want ErrOpenTimeout wrapping the driver error", got)
		}
		for _, want := range []string{"budget 2s", "/data/db", "BEADS_EMBEDDED_OPEN_TIMEOUT", errLocked.Error()} {
			if !strings.Contains(got.Error(), want) {
				t.Errorf("classifyOpenErr message %q does not contain %q", got, want)
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := classifyOpenErr(ctx, wait, "/data/db", 2*time.Second, errLocked); got != errLocked {
			t.Errorf("classifyOpenErr with a canceled caller = %v, want the error unchanged: cancellation takes precedence", got)
		}
	})
}
