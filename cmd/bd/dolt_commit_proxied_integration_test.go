//go:build cgo

package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// sqlCountValue extracts a COUNT(*) cell from bdProxiedSQLJSON. sqlValueEquals
// only answers "is it this number"; the batch-flush assertions need the number
// itself so a failure can report how far HEAD actually moved.
func sqlCountValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// TestProxiedServerBatchDefersThenDoltCommitAdvancesHeadOnce pins both halves
// of GH#4995 on the proxied route: dolt.auto-commit=batch must leave writes in
// the working set (no Dolt commit per write), and `bd dolt commit` must mint
// exactly one commit for the whole batch.
//
// Before the proxied flush point existed, step 3 below failed with
// "no store available": proxied mode returns from the root pre-run before
// newDoltStore runs, so getStore() is nil and `bd dolt commit` had nothing to
// commit through. batch/off therefore meant "never commit" on this route.
func TestProxiedServerBatchDefersThenDoltCommitAdvancesHeadOnce(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "batchflush")

	doltLogCount := func(t *testing.T) float64 {
		t.Helper()
		rows := bdProxiedSQLJSON(t, bd, p.dir, "SELECT COUNT(*) AS count FROM dolt_log")
		if len(rows) != 1 {
			t.Fatalf("expected one row from dolt_log count, got %d: %v", len(rows), rows)
		}
		n, ok := sqlCountValue(rows[0]["count"])
		if !ok {
			t.Fatalf("dolt_log count is not numeric: %#v", rows[0]["count"])
		}
		return n
	}

	head0 := doltLogCount(t)

	// 1. Three writes under the batch policy must not mint any Dolt commit.
	for i := 1; i <= 3; i++ {
		title := fmt.Sprintf("batch write %d", i)
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
			"--dolt-auto-commit", "batch", "create", title, "-p", "1")
		if err != nil {
			t.Fatalf("bd create %q failed: %v\nstdout:\n%s\nstderr:\n%s", title, err, stdout, stderr)
		}
	}

	if got := doltLogCount(t); got != head0 {
		t.Fatalf("batch writes minted Dolt commits: dolt_log went %v -> %v (want unchanged)", head0, got)
	}

	// 2. The writes are in the working set, not rolled back.
	listOut, listErr, err := bdProxiedRunBuffers(t, bd, p.dir, "list", "--json")
	if err != nil {
		t.Fatalf("bd list --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, listOut, listErr)
	}
	for i := 1; i <= 3; i++ {
		title := fmt.Sprintf("batch write %d", i)
		if !strings.Contains(listOut, title) {
			t.Fatalf("deferred write %q is not readable after batch create:\n%s", title, listOut)
		}
	}

	// 3. The explicit flush point mints exactly one commit for the batch.
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dolt", "commit", "-m", "batch flush")
	if err != nil {
		t.Fatalf("bd dolt commit failed in proxied mode: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Committed.") {
		t.Fatalf("bd dolt commit did not report a commit:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got, want := doltLogCount(t), head0+1; got != want {
		t.Fatalf("bd dolt commit did not advance HEAD exactly once: dolt_log %v -> %v (want %v)", head0, got, want)
	}

	// 4. A second flush with nothing pending is a no-op, not a second commit.
	stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "dolt", "commit", "-m", "second flush")
	if err != nil {
		t.Fatalf("second bd dolt commit failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Nothing to commit.") {
		t.Fatalf("second bd dolt commit did not report an empty working set:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got, want := doltLogCount(t), head0+1; got != want {
		t.Fatalf("second bd dolt commit advanced HEAD: dolt_log = %v (want %v)", got, want)
	}

	// 5. The deferral is policy-driven, not unconditional: with the default
	// "on" policy a single create advances dolt_log by one on its own.
	before := doltLogCount(t)
	stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir,
		"--dolt-auto-commit", "on", "create", "immediate write", "-p", "1")
	if err != nil {
		t.Fatalf("bd create with --dolt-auto-commit on failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got, want := doltLogCount(t), before+1; got != want {
		t.Fatalf("auto-commit=on did not commit per write: dolt_log %v -> %v (want %v)", before, got, want)
	}
}
