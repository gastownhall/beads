//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// statusDriftCount pulls the single count out of
// 'bd recompute-blocked --status [--fix] --json'. The key differs between the
// two branches on purpose — a report and a repair that both said
// "status_blocked_drift: 1" would be indistinguishable to a script — so the
// caller names which one it expects, and a response carrying the other key
// fails instead of silently reading as zero.
func statusDriftCount(t *testing.T, out, wantKey string) int {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in recompute-blocked --status output:\n%s", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &payload); err != nil {
		t.Fatalf("parse recompute-blocked --status JSON: %v\nraw: %s", err, out[start:])
	}
	n, ok := payload[wantKey].(float64)
	if !ok {
		t.Fatalf("key %q missing or not a number in %v", wantKey, payload)
	}
	return int(n)
}

// TestEmbeddedRecomputeBlockedStatusFlag covers 'bd recompute-blocked --status'
// and '--status --fix' end to end in embedded mode — the only mode in which
// the be-ntbxt repair is reachable at all for most users, since 'bd doctor' is
// server-mode only.
//
// The fixture deliberately holds three status='blocked' beads of which exactly
// ONE is drift, so the assertions pin the predicate and not just the plumbing:
//
//   - stranded: no dependency edge ever recorded (15 of the 29 real victims).
//   - legit:    a still-open 'blocks' blocker.
//   - child:    no blocking edge of its own, blocked only by inheriting its
//     parent's block through parent-child. A predicate that counts
//     'blocks' edges alone misses this one and --fix force-opens a
//     bead the graph still holds blocked (gastownhall/beads#6565).
func TestEmbeddedRecomputeBlockedStatusFlag(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rbs")
	run := func(args ...string) string { return runClassic(t, bd, dir, args...) }

	blocker := parseIssueJSON(t, []byte(run("create", "--json", "Open blocker")))
	stranded := parseIssueJSON(t, []byte(run("create", "--json", "Stranded, no edge")))
	legit := parseIssueJSON(t, []byte(run("create", "--json", "Blocked by an open blocker",
		"--deps", "blocked-by:"+blocker.ID)))
	parent := parseIssueJSON(t, []byte(run("create", "--json", "Blocked parent",
		"--deps", "blocked-by:"+blocker.ID)))
	child := parseIssueJSON(t, []byte(run("create", "--json", "Child of a blocked parent")))
	run("dep", "add", child.ID, parent.ID, "-t", "parent-child")

	// The manual enum: what nothing in bd ever clears on its own.
	for _, id := range []string{stranded.ID, legit.ID, child.ID} {
		run("update", id, "--status", "blocked")
		if got := getIssueStatus(t, bd, dir, id); got != "blocked" {
			t.Fatalf("fixture: %s status = %q, want blocked", id, got)
		}
	}

	// --status alone: reports, never mutates.
	if n := statusDriftCount(t, run("recompute-blocked", "--status", "--json"), "status_blocked_drift"); n != 1 {
		t.Fatalf("report: want 1 drifted row (only %s), got %d", stranded.ID, n)
	}
	if text := run("recompute-blocked", "--status"); !strings.Contains(text, "Run with --fix to repair") {
		t.Errorf("report-only text must point at --fix, got:\n%s", text)
	}
	for _, id := range []string{stranded.ID, legit.ID, child.ID} {
		if got := getIssueStatus(t, bd, dir, id); got != "blocked" {
			t.Fatalf("--status without --fix mutated %s: status = %q, want blocked", id, got)
		}
	}

	// --status --fix: repairs exactly the drifted bead.
	if n := statusDriftCount(t, run("recompute-blocked", "--status", "--fix", "--json"), "status_blocked_fixed"); n != 1 {
		t.Fatalf("fix: want 1 row corrected, got %d", n)
	}
	if got := getIssueStatus(t, bd, dir, stranded.ID); got != "open" {
		t.Errorf("after fix: %s (no edge recorded) status = %q, want open", stranded.ID, got)
	}
	if got := getIssueStatus(t, bd, dir, legit.ID); got != "blocked" {
		t.Errorf("after fix: %s (open 'blocks' blocker) status = %q, want still blocked", legit.ID, got)
	}
	if got := getIssueStatus(t, bd, dir, child.ID); got != "blocked" {
		t.Errorf("after fix: %s (inherits its parent's block) status = %q, want still blocked", child.ID, got)
	}

	// Converged, and idempotent — the property any repair loop relies on.
	if n := statusDriftCount(t, run("recompute-blocked", "--status", "--json"), "status_blocked_drift"); n != 0 {
		t.Errorf("after fix: want 0 drifted rows, got %d", n)
	}
	if n := statusDriftCount(t, run("recompute-blocked", "--status", "--fix", "--json"), "status_blocked_fixed"); n != 0 {
		t.Errorf("second fix must be a no-op: want 0 corrected, got %d", n)
	}
	if text := run("recompute-blocked", "--status"); !strings.Contains(text, "No status=blocked drift") {
		t.Errorf("converged text: got:\n%s", text)
	}
}

// TestEmbeddedRecomputeBlockedStatusFixIgnoresStaleIsBlocked pins
// gastownhall/beads#6565 review R2: the drift test's parent-child leg reads the
// parent's stored is_blocked column, so a repair that trusted a stale column
// would force-open a child the graph still holds blocked. The parent here is
// genuinely blocked by an open blocker, but its is_blocked column has been
// zeroed by hand; '--status --fix' must recompute is_blocked before it repairs
// and leave the child blocked.
func TestEmbeddedRecomputeBlockedStatusFixIgnoresStaleIsBlocked(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rbx")
	run := func(args ...string) string { return runClassic(t, bd, dir, args...) }

	blocker := parseIssueJSON(t, []byte(run("create", "--json", "Open blocker")))
	parent := parseIssueJSON(t, []byte(run("create", "--json", "Blocked parent",
		"--deps", "blocked-by:"+blocker.ID)))
	child := parseIssueJSON(t, []byte(run("create", "--json", "Child of a blocked parent")))
	run("dep", "add", child.ID, parent.ID, "-t", "parent-child")
	run("update", child.ID, "--status", "blocked")

	// Make the parent's derived column stale: the graph still holds it blocked.
	// 'bd sql' is not supported in embedded mode, so write it directly.
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load config from %s: %v", beadsDir, err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), cfg.GetDoltDatabase(), "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	res, err := db.ExecContext(t.Context(), "UPDATE issues SET is_blocked = 0 WHERE id = ?", parent.ID)
	if err != nil {
		cleanup()
		t.Fatalf("zero parent is_blocked: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		cleanup()
		t.Fatalf("fixture: zeroing %s is_blocked affected %d rows, want 1 (was it not blocked?)", parent.ID, n)
	}
	if _, err := db.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', 'test: stale parent is_blocked')"); err != nil {
		cleanup()
		t.Fatalf("commit stale is_blocked: %v", err)
	}
	cleanup()

	if n := statusDriftCount(t, run("recompute-blocked", "--status", "--fix", "--json"), "status_blocked_fixed"); n != 0 {
		t.Errorf("fix against a stale parent is_blocked: want 0 rows corrected, got %d", n)
	}
	if got := getIssueStatus(t, bd, dir, child.ID); got != "blocked" {
		t.Errorf("after fix: %s (child of a parent the graph holds blocked) status = %q, want still blocked", child.ID, got)
	}
}
