//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestOpenSQLFailureNamesLocationElapsedAndCallerDeadline pins the GH#7989
// open-failure contract: when the database cannot be opened — here because an
// in-process holder keeps its lock, the shape a blocked OpenSQL takes — the
// failure says what was being done (opening the database at its location) and
// how long the open ran, and preserves the engine's own error and the
// caller's expiry. It deliberately asserts nothing about a holder: the
// engine does not name one, and an unnamed guess would be worse than silence.
func TestOpenSQLFailureNamesLocationElapsedAndCallerDeadline(t *testing.T) {
	// Cannot be parallel: a second in-process engine holds the database lock
	// for the duration.
	ctx := t.Context()
	dir := t.TempDir()

	// Bootstrap: create the database once (with no USE attached, so the
	// bootstrap engine opens cleanly), then release the directory.
	bootstrap, bootstrapCleanup, err := OpenSQL(ctx, dir, "", "")
	if err != nil {
		t.Fatalf("bootstrap OpenSQL: %v", err)
	}
	if _, err := bootstrap.ExecContext(ctx, "CREATE DATABASE `beads`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	_ = bootstrap.Close()
	if err := bootstrapCleanup(); err != nil {
		t.Fatalf("bootstrap cleanup: %v", err)
	}

	first, firstCleanup, err := OpenSQL(ctx, dir, "beads", "main")
	if err != nil {
		t.Fatalf("first OpenSQL: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	t.Cleanup(func() { _ = firstCleanup() })

	blockedCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err = OpenSQL(blockedCtx, dir, "beads", "main")
	if err == nil {
		t.Fatal("second OpenSQL succeeded although the first holder kept the database open")
	}
	msg := err.Error()

	if !strings.Contains(msg, dir) {
		t.Errorf("open failure does not name the database location %q: %s", dir, msg)
	}
	// "failed after <duration>" with a lived-in elapsed value, not a constant.
	elapsedMarker := regexp.MustCompile(`failed after (?:\d+(?:\.\d+)?(?:ns|µs|ms)|\d+(?:\.\d+)?(?:m|h)?s)+`)
	if !elapsedMarker.MatchString(msg) {
		t.Errorf("open failure does not say how long it ran: %s", msg)
	}
	if want := time.Since(start); want < 2*time.Second {
		// The blocked open must have actually lived through the 3s deadline,
		// not failed for some unrelated fast reason.
		t.Fatalf("blocked OpenSQL returned in %s, want it held out against the 3s deadline for another holder", want)
	}
	if !strings.Contains(msg, "locked") {
		t.Errorf("open failure lost the engine's own locked-database error: %s", msg)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("open failure = %v, want it to preserve the caller's deadline (context.DeadlineExceeded)", err)
	}

	// A caller that cancels keeps its cancellation in the chain the same way.
	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, _, err = OpenSQL(canceledCtx, dir, "beads", "main"); err == nil {
		t.Fatal("OpenSQL succeeded on an already-canceled context")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled OpenSQL = %v, want the caller's cancellation preserved (context.Canceled)", err)
	}
}
