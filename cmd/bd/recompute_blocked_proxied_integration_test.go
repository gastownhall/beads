//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// bdRunner runs `bd <args>` in one storage mode's workspace and returns stdout,
// fataling on a nonzero exit. It is what lets the corrupt-then-repair scenario
// below be written ONCE and replayed against classic and proxied, which is the
// only way a parity claim about them can be checked rather than asserted.
type bdRunner struct {
	mode string
	run  func(t *testing.T, args ...string) string
	// setIsBlocked commits a wrong is_blocked value for id. The MECHANISM is
	// necessarily mode-specific (classic embedded refuses `bd sql`), which is
	// fine and in fact the point: what parity is claimed about is the repair's
	// behavior on an equivalently corrupted database, not how it got corrupted.
	setIsBlocked func(t *testing.T, id string, value int)
}

// blockedRepairObservation is everything the scenario can observe about
// `bd recompute-blocked` through the CLI: both rendered branches and the count.
// Every field must match across modes.
type blockedRepairObservation struct {
	repairText string
	repairRows int
	noopText   string
}

// runBlockedRepairScenario commits a database whose is_blocked column disagrees
// with its own dependency graph, then repairs it — the exact shape the command
// exists for (a stale flag left behind by a merge whose scoped recompute never
// ran, bd-6dnrw.37), not a dirty working set.
//
// Both modes COMMIT their corruption (see bdRunner.setIsBlocked), so the graph
// tables are clean when the repair's dirty-graph guard looks at them and the
// scenario exercises the repair rather than the guard.
func runBlockedRepairScenario(t *testing.T, r bdRunner) blockedRepairObservation {
	t.Helper()

	blocker := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Blocker")))
	blocked := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Blocked", "--deps", "blocked-by:"+blocker.ID)))
	free := parseIssueJSON(t, []byte(r.run(t, "create", "--json", "Unblocked")))

	// Corrupt in BOTH directions, so the repair has to mark and unmark. A
	// one-sided fixture passes a repair that only ever sets the flag one way.
	r.setIsBlocked(t, blocked.ID, 0)
	r.setIsBlocked(t, free.ID, 1)

	obs := blockedRepairObservation{}
	obs.repairText = strings.TrimSpace(r.run(t, "recompute-blocked"))

	r.setIsBlocked(t, blocked.ID, 0)
	obs.repairRows = recomputeRowsCorrected(t, r.run(t, "recompute-blocked", "--json"))

	// Idempotency is the property wh-bridge-sync's 3x retry rides on.
	obs.noopText = strings.TrimSpace(r.run(t, "recompute-blocked"))
	return obs
}

func recomputeRowsCorrected(t *testing.T, out string) int {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in recompute-blocked output:\n%s", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &payload); err != nil {
		t.Fatalf("parse recompute-blocked JSON: %v\nraw: %s", err, out[start:])
	}
	n, ok := payload["rows_corrected"].(float64)
	if !ok {
		t.Fatalf("rows_corrected missing or not a number in %v", payload)
	}
	return int(n)
}

func proxiedBDRunner(t *testing.T, bd string, p proxiedProject) bdRunner {
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
		if err != nil {
			t.Fatalf("proxied bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), err, stdout, stderr)
		}
		return stdout
	}
	return bdRunner{
		mode: "proxied",
		run:  run,
		// `bd sql` COMMITS in proxied mode, so the graph tables are clean when
		// the repair's dirty-graph guard looks at them.
		setIsBlocked: func(t *testing.T, id string, value int) {
			t.Helper()
			run(t, "sql", "UPDATE issues SET is_blocked = "+strconv.Itoa(value)+" WHERE id = '"+id+"'")
		},
	}
}

// classicBDRunner drives an embedded-mode workspace. Its corruption goes
// through the dolt CLI because embedded mode refuses `bd sql` outright, and it
// COMMITS the corruption for the same reason the proxied side does: the repair
// under test refuses a dirty graph, so a fixture that left one would be testing
// the guard instead of the repair.
func classicBDRunner(t *testing.T, bd, beadsDir, prefix, doltBin string) bdRunner {
	dbDir := filepath.Join(beadsDir, "embeddeddolt", prefix)
	dir := filepath.Dir(beadsDir)
	return bdRunner{
		mode: "classic",
		run: func(t *testing.T, args ...string) string {
			t.Helper()
			return runClassic(t, bd, dir, args...)
		},
		setIsBlocked: func(t *testing.T, id string, value int) {
			t.Helper()
			runDolt(t, doltBin, dbDir, "sql", "-q",
				"UPDATE issues SET is_blocked = "+strconv.Itoa(value)+" WHERE id = '"+id+"'")
			runDolt(t, doltBin, dbDir, "add", "issues")
			runDolt(t, doltBin, dbDir, "commit", "-m", "test: corrupt is_blocked",
				"--author", "Parity Test <parity@example.com>")
		},
	}
}

// TestProxiedServerRecomputeBlocked covers the proxied-server port of
// `bd recompute-blocked` (bd-04vav).
//
// The downstream consumer is wh-bridge-sync, which runs this after a
// cross-machine merge under a timeout cap and retries up to three times on the
// strength of the command being IDEMPOTENT and its exit code being HONEST. Both
// of those are asserted here directly, alongside the repair itself, the
// mode-independent commit it lands, and the dirty-graph refusal that had to
// survive the port — a repair that committed flags derived from someone else's
// uncommitted dependency edits would be worse than no repair at all.
func TestProxiedServerRecomputeBlocked(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("cross_mode_parity", func(t *testing.T) {
		t.Parallel()
		doltBin, err := exec.LookPath("dolt")
		if err != nil {
			t.Skip("dolt binary not on PATH: the classic half of the oracle cannot corrupt its fixture")
		}
		p := newSharedProxiedProject(t, bd, "rbp")
		proxied := runBlockedRepairScenario(t, proxiedBDRunner(t, bd, p))

		_, classicBeads, _ := bdInit(t, bd, "--prefix", "rbc")
		classic := runBlockedRepairScenario(t, classicBDRunner(t, bd, classicBeads, "rbc", doltBin))

		if proxied != classic {
			t.Errorf("recompute-blocked differs across modes:\n  proxied: %#v\n  classic: %#v",
				proxied, classic)
		}
		// Pin the shared values too, so a symmetric regression in both modes
		// (the failure an equality-only oracle is blind to) still fails.
		if proxied.repairRows != 1 {
			t.Errorf("rows_corrected: got %d, want 1", proxied.repairRows)
		}
		if !strings.Contains(proxied.repairText, "Recomputed is_blocked: 2 row(s) corrected") {
			t.Errorf("repair line: got %q", proxied.repairText)
		}
		if !strings.Contains(proxied.noopText, "is_blocked already consistent") {
			t.Errorf("no-op line: got %q", proxied.noopText)
		}
	})

	t.Run("commits_only_when_it_repairs", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rbc2")
		run := proxiedBDRunner(t, bd, p)
		blocker := parseIssueJSON(t, []byte(run.run(t, "create", "--json", "Blocker")))
		blocked := parseIssueJSON(t, []byte(run.run(t, "create", "--json", "Blocked",
			"--deps", "blocked-by:"+blocker.ID)))

		db := openProxiedDB(t, p)
		if !readIsBlocked(t, db, blocked.ID) {
			t.Fatalf("fixture: %s should start blocked", blocked.ID)
		}

		// A no-op repair must mint NO commit: the bridge runs this on every tick
		// that owes a recompute, and a commit per tick is history every clone
		// then has to pull.
		before := readDoltHead(t, db)
		run.run(t, "recompute-blocked")
		if got := readDoltLogCountSince(t, db, before); got != 0 {
			t.Errorf("no-op recompute created %d dolt commit(s), want 0", got)
		}

		run.run(t, "sql", "UPDATE issues SET is_blocked = 0 WHERE id = '"+blocked.ID+"'")
		before = readDoltHead(t, db)
		run.run(t, "recompute-blocked")
		if got := readDoltLogCountSince(t, db, before); got != 1 {
			t.Errorf("repair created %d dolt commit(s), want exactly 1", got)
		}
		if msg := readDoltLogTopMessage(t, db); msg != "bd: recompute is_blocked (full)" {
			t.Errorf("repair commit message: got %q, want the mode-independent message", msg)
		}
		if !readIsBlocked(t, db, blocked.ID) {
			t.Errorf("%s: is_blocked still 0 after repair", blocked.ID)
		}
	})

	t.Run("refuses_dirty_graph", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rbd")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker")
		bdProxiedCreate(t, bd, p.dir, "Blocked", "--deps", "blocked-by:"+blocker.ID)

		// Dirty `issues` OUTSIDE bd, so nothing commits it: a direct write on
		// the server's own connection is the hand-resolved-merge state the guard
		// exists for.
		db := openProxiedDB(t, p)
		if _, err := db.ExecContext(context.Background(),
			"UPDATE issues SET title = 'dirtied' WHERE id = ?", blocker.ID); err != nil {
			t.Fatalf("dirty the working set: %v", err)
		}

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "recompute-blocked")
		if err == nil {
			t.Fatalf("recompute-blocked must refuse a dirty graph; it succeeded:\nstdout:\n%s\nstderr:\n%s",
				stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "needs a clean working set") {
			t.Errorf("expected the dirty-graph refusal, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
	})
}
