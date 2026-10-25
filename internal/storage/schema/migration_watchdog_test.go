package schema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/term"
)

// TestRunMigrationWithWatchdog_NoWarningUnderThreshold covers be-yyzzs: a
// migration that finishes well inside the threshold must produce zero
// watchdog output — the warning is a "still working" signal, not a
// per-migration progress line (that already exists via runMigrations'
// "Applying migration..."/"done" prints).
func TestRunMigrationWithWatchdog_NoWarningUnderThreshold(t *testing.T) {
	var buf bytes.Buffer
	err := runMigrationWithWatchdog(context.Background(), &buf, 42, "add_date_indexes", 100*time.Millisecond,
		func(ctx context.Context) error { return nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("want no watchdog output for a fast migration, got %q", got)
	}
}

// TestRunMigrationWithWatchdog_WarnsOnThresholdExceeded covers be-yyzzs: once
// a migration runs past the threshold, a WARN line naming the version, the
// human migration name, and the elapsed time must appear on the writer that
// runMigrations already uses for progress output.
func TestRunMigrationWithWatchdog_WarnsOnThresholdExceeded(t *testing.T) {
	var buf bytes.Buffer
	err := runMigrationWithWatchdog(context.Background(), &buf, 42, "add_date_indexes", 15*time.Millisecond,
		func(ctx context.Context) error {
			time.Sleep(40 * time.Millisecond)
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "WARN") {
		t.Errorf("want a WARN-level line, got %q", got)
	}
	if !strings.Contains(got, fmt.Sprintf("%04d", 42)) {
		t.Errorf("want the migration version in the warning, got %q", got)
	}
	if !strings.Contains(got, "add_date_indexes") {
		t.Errorf("want the migration name in the warning, got %q", got)
	}
	if !strings.Contains(got, "still running") {
		t.Errorf("want an elapsed-time/still-running marker, got %q", got)
	}
}

// TestRunMigrationWithWatchdog_RepeatsWarningEveryInterval covers be-yyzzs:
// a migration that keeps running past multiple threshold intervals must get
// a fresh warning each interval, not just once — so an operator watching
// logs can distinguish "still working" from "stopped emitting anything".
func TestRunMigrationWithWatchdog_RepeatsWarningEveryInterval(t *testing.T) {
	var buf bytes.Buffer
	err := runMigrationWithWatchdog(context.Background(), &buf, 7, "backfill", 10*time.Millisecond,
		func(ctx context.Context) error {
			time.Sleep(55 * time.Millisecond)
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count := strings.Count(buf.String(), "WARN")
	if count < 3 {
		t.Errorf("want at least 3 repeated warnings over ~5.5 intervals, got %d in %q", count, buf.String())
	}
}

// TestRunMigrationWithWatchdog_ContextUnaffected covers be-yyzzs: the
// watchdog is observability only. It must hand fn the exact ctx it was
// given — no wrapping with a new timeout/cancel — and that ctx must still be
// live (Err() == nil) even after the watchdog has fired multiple warnings.
func TestRunMigrationWithWatchdog_ContextUnaffected(t *testing.T) {
	type sentinelKey struct{}
	ctx := context.WithValue(context.Background(), sentinelKey{}, "sentinel-value")

	var sawValue any
	var errDuringRun error
	var buf bytes.Buffer

	err := runMigrationWithWatchdog(ctx, &buf, 1, "initial", 10*time.Millisecond,
		func(fnCtx context.Context) error {
			time.Sleep(35 * time.Millisecond)
			sawValue = fnCtx.Value(sentinelKey{})
			errDuringRun = fnCtx.Err()
			return nil
		})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawValue != "sentinel-value" {
		t.Errorf("fn did not receive the original ctx (missing sentinel value); got %v", sawValue)
	}
	if errDuringRun != nil {
		t.Errorf("ctx passed to fn was canceled/expired by the watchdog: %v", errDuringRun)
	}
	if ctx.Err() != nil {
		t.Errorf("caller's ctx was canceled/expired by the watchdog: %v", ctx.Err())
	}
}

// TestRunMigrationWithWatchdog_ErrorPassthrough covers be-yyzzs: the
// watchdog must never alter the migration's outcome. A migration that runs
// long AND ultimately fails must still surface the exact original error —
// the warning is purely additive, not a substitute for or wrapper around it.
func TestRunMigrationWithWatchdog_ErrorPassthrough(t *testing.T) {
	sentinel := errors.New("boom: migration 0007 failed")
	var buf bytes.Buffer

	err := runMigrationWithWatchdog(context.Background(), &buf, 7, "backfill", 10*time.Millisecond,
		func(ctx context.Context) error {
			time.Sleep(25 * time.Millisecond)
			return sentinel
		})

	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error passed through unchanged, got %v", err)
	}
	if !strings.Contains(buf.String(), "WARN") {
		t.Errorf("want the watchdog to still have warned before the failure, got %q", buf.String())
	}
}

// syncWriter serializes writes so the panic test below can read what the
// watchdog has emitted so far without racing it. The other tests in this file
// use a bare bytes.Buffer because the watchdog goroutine is always joined
// before they read; the panic case reads at a point where a leaked goroutine
// would still be writing, which is the leak it exists to catch.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestRunMigrationWithWatchdog_StopsWarnerWhenFnPanics pins the deferred
// close(done)/wg.Wait(): Dolt's embedded engine can panic out of DDL, and a
// caller that recovers instead of dying (internal/httpapi/server.go's
// middleware recovers everything except http.ErrAbortHandler and keeps
// serving) leaves the process alive afterwards. Stopping the warner on the
// straight-line path after fn returns is skipped by a panic, which orphans the
// ticker goroutine into warning about a migration that is no longer running —
// on the deliberately un-gated watchdogStderr — for the rest of that process's
// lifetime.
func TestRunMigrationWithWatchdog_StopsWarnerWhenFnPanics(t *testing.T) {
	const interval = 10 * time.Millisecond
	out := &syncWriter{}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("want fn's panic to propagate to the caller unchanged, got none")
			}
		}()
		_ = runMigrationWithWatchdog(context.Background(), out, 47, "recompute_mixed_is_blocked", interval,
			func(ctx context.Context) error {
				time.Sleep(35 * time.Millisecond)
				panic("dolt engine panicked out of DDL")
			})
	}()

	// The warner must already be joined by the time the panic reaches here, so
	// the line count cannot grow any further.
	settled := strings.Count(out.String(), "WARN")
	if settled == 0 {
		t.Fatalf("want at least one WARN before the panic, otherwise the check below is vacuous; got %q", out.String())
	}
	time.Sleep(5 * interval)
	if after := strings.Count(out.String(), "WARN"); after != settled {
		t.Errorf("watchdog goroutine outlived the panicking migration: %d WARN lines when the panic unwound, %d after a further %s", settled, after, 5*interval)
	}
}

// TestMigrationWatchdogIntervalDuration covers be-yyzzs: the threshold must
// default to 5 minutes and be overridable via BEADS_MIGRATION_WATCHDOG_INTERVAL,
// following this codebase's existing timeoutFromEnv/BEADS_*_TIMEOUT convention
// (internal/storage/dolt/store.go's cliExecTimeoutEnv/timeoutFromEnv): unset,
// empty, or unparsable values fall back to the default.
func TestMigrationWatchdogIntervalDuration(t *testing.T) {
	cases := []struct {
		name   string
		envVal string
		setEnv bool
		want   time.Duration
	}{
		{name: "unset_defaults_to_5m", setEnv: false, want: 5 * time.Minute},
		{name: "empty_defaults_to_5m", setEnv: true, envVal: "", want: 5 * time.Minute},
		{name: "valid_duration_string_overrides", setEnv: true, envVal: "10s", want: 10 * time.Second},
		{name: "invalid_value_falls_back_to_default", setEnv: true, envVal: "not-a-duration", want: 5 * time.Minute},
		// internal/storage/dolt/store.go's parseTimeout — the convention this
		// function's doc comment claims to follow — documents "bare numbers
		// treated as seconds (e.g. \"90\")". Without this, a plausible
		// BEADS_MIGRATION_WATCHDOG_INTERVAL=300 silently means 5m rather than
		// the 5m the operator was trying to change.
		// NB: not "300" — 300s IS the 5m default, so that value cannot tell
		// "parsed as seconds" from "fell back".
		{name: "bare_seconds_treated_as_seconds", setEnv: true, envVal: "90", want: 90 * time.Second},
		{name: "bare_seconds_larger_than_default", setEnv: true, envVal: "600", want: 10 * time.Minute},
		{name: "bare_zero_falls_back_to_default", setEnv: true, envVal: "0", want: 5 * time.Minute},
		{name: "negative_falls_back_to_default", setEnv: true, envVal: "-5s", want: 5 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv(migrationWatchdogIntervalEnv, tc.envVal)
			} else {
				// The "unset" arm has to actually unset: t.Setenv registers the
				// restore of whatever the ambient value was, then os.Unsetenv
				// removes it for the duration of the subtest. Without this,
				// a shell exporting BEADS_MIGRATION_WATCHDOG_INTERVAL (this
				// repo has a history of ambient BEADS_* reddening tests) flows
				// straight into the call and the compiled-in default this
				// subtest exists to pin is never exercised.
				t.Setenv(migrationWatchdogIntervalEnv, "")
				os.Unsetenv(migrationWatchdogIntervalEnv)
			}
			if got := migrationWatchdogIntervalDuration(); got != tc.want {
				t.Errorf("migrationWatchdogIntervalDuration() = %v; want %v", got, tc.want)
			}
		})
	}
}

// slowMigrationDB makes the first migration body take long enough for a
// short-interval watchdog to fire, so the wiring between runMigrations and
// runMigrationWithWatchdog can be exercised end to end rather than asserted
// on a package variable.
type slowMigrationDB struct {
	mockDB
	once sync.Once
	d    time.Duration
}

func (s *slowMigrationDB) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	s.once.Do(func() { time.Sleep(s.d) })
	return s.mockDB.ExecContext(ctx, q, a...)
}

// TestRunMigrationsWatchdogSurvivesNonTTY covers the review point on #5997:
// the package's progress writer is io.Discard whenever os.Stderr is not a
// terminal, so wiring the watchdog to it makes the warning silent under
// bd serve, systemd, CI, and any piped invocation — precisely the situations
// where a migration that has been running for twenty minutes needs to be
// visible. On a tty it worked fine, which is why this went unnoticed.
func TestRunMigrationsWatchdogSurvivesNonTTY(t *testing.T) {
	if term.IsTerminal(int(os.Stderr.Fd())) {
		t.Skip("stderr is a terminal here; this test asserts the non-tty path")
	}

	// Put the progress writer in the state a non-tty caller really sees.
	origStderr := stderr
	stderr = defaultStderr()
	defer func() { stderr = origStderr }()
	if stderr != io.Discard {
		t.Fatalf("precondition: progress writer off a tty = %T, want io.Discard", stderr)
	}

	var watchdogBuf bytes.Buffer
	origWatchdog := watchdogStderr
	watchdogStderr = &watchdogBuf
	defer func() { watchdogStderr = origWatchdog }()

	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	defer func() { issueRowCounter = origCounter }()

	t.Setenv(migrationWatchdogIntervalEnv, "10ms")

	db := &slowMigrationDB{d: 80 * time.Millisecond}
	if _, err := runMigrations(context.Background(), db, mainSource, 0, 39, false); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	if got := watchdogBuf.String(); !strings.Contains(got, "WARN") {
		t.Errorf("no watchdog WARN reached the non-tty writer: the warning is discarded exactly where operators need it. got %q", got)
	}
}
