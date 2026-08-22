package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv(migrationWatchdogIntervalEnv, tc.envVal)
			}
			if got := migrationWatchdogIntervalDuration(); got != tc.want {
				t.Errorf("migrationWatchdogIntervalDuration() = %v; want %v", got, tc.want)
			}
		})
	}
}
