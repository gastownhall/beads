//go:build cgo

package main

import (
	"fmt"
	"os"
	"path/filepath"
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

// TestProxiedServerBatchDefersThenDoltCommitAdvancesHeadOnce pins every half of
// GH#4995 on the proxied route: dolt.auto-commit=batch must leave policy writes
// in the working set (no Dolt commit per write), `bd dolt commit` must mint
// exactly one commit for the whole batch and attribute it to the actor, and the
// explicit commit points must keep minting their own commit with the caller's
// message despite the route-wide deferral.
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

	headCommit := func(t *testing.T) map[string]interface{} {
		t.Helper()
		rows := bdProxiedSQLJSON(t, bd, p.dir,
			"SELECT message, committer, email FROM dolt_log ORDER BY date DESC LIMIT 1")
		if len(rows) != 1 {
			t.Fatalf("expected one dolt_log row for HEAD, got %d: %v", len(rows), rows)
		}
		return rows[0]
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
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
		"--actor", "flush-actor", "dolt", "commit", "-m", "batch flush")
	if err != nil {
		t.Fatalf("bd dolt commit failed in proxied mode: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Committed.") {
		t.Fatalf("bd dolt commit did not report a commit:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got, want := doltLogCount(t), head0+1; got != want {
		t.Fatalf("bd dolt commit did not advance HEAD exactly once: dolt_log %v -> %v (want %v)", head0, got, want)
	}

	// 3b. The flush is attributed to the bd actor, not the SQL session user.
	// Under batch mode this is the only commit a whole batch of writes leaves
	// behind, so `dolt log` has to be able to say whose batch it was — the
	// '--author' DoltStorage.CommitAll passes on the direct route.
	head := headCommit(t)
	if got := head["message"]; got != "batch flush" {
		t.Fatalf("flush commit message = %v, want %q (row: %v)", got, "batch flush", head)
	}
	if got := head["committer"]; got != "flush-actor" {
		t.Fatalf("flush commit is not attributed to the actor: committer = %v, want %q (row: %v)", got, "flush-actor", head)
	}
	if got := head["email"]; got != "test@test.com" {
		t.Fatalf("flush commit email = %v, want the git identity %q (row: %v)", got, "test@test.com", head)
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

	// 6. The deferral is per commit CLASS, not per route: `bd batch -m` is an
	// explicit commit point (its direct-route twin calls transact, which mints
	// regardless of policy), so under batch it must still commit, carrying the
	// caller's message. A plain create in the same policy is the control.
	before = doltLogCount(t)
	stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir,
		"--dolt-auto-commit", "batch", "create", "deferred control write", "-p", "1")
	if err != nil {
		t.Fatalf("control create under batch failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := doltLogCount(t); got != before {
		t.Fatalf("control create minted a commit under batch: dolt_log %v -> %v (want unchanged)", before, got)
	}

	script := filepath.Join(p.dir, "explicit-batch.txt")
	if err := os.WriteFile(script, []byte("create task 1 explicit batch write\n"), 0o600); err != nil {
		t.Fatalf("write batch script: %v", err)
	}
	stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir,
		"--dolt-auto-commit", "batch", "batch", "-f", script, "-m", "explicit batch commit")
	if err != nil {
		t.Fatalf("bd batch under batch policy failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got, want := doltLogCount(t), before+1; got != want {
		t.Fatalf("explicit batch commit did not mint exactly one commit under batch: dolt_log %v -> %v (want %v)", before, got, want)
	}
	// The message is the half a route-wide deferral silently drops: blanking it
	// selects the plain-COMMIT form, which persists the rows and records nothing.
	if got := headCommit(t)["message"]; got != "explicit batch commit" {
		t.Fatalf("explicit batch commit lost the caller's message: %v (want %q)", got, "explicit batch commit")
	}

	// 7. That commit swept the deferred control write with it (DOLT_COMMIT
	// '-Am'), so the working set is clean and a flush now has nothing to do —
	// the exemption does not strand the writes the policy deferred.
	stdout, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "dolt", "commit", "-m", "post-batch flush")
	if err != nil {
		t.Fatalf("flush after explicit batch commit failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Nothing to commit.") {
		t.Fatalf("flush after explicit batch commit found pending work:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}
