//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// bdCompact runs "bd compact" with the given args and returns stdout.
func bdCompact(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"compact"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd compact %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdCompactFail runs "bd compact" expecting failure.
func bdCompactFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"compact"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd compact %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedCompact(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Dry Run =====

	t.Run("compact_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cd")
		bdCreate(t, bd, dir, "Compact dry-run issue", "--type", "task")

		out := bdCompact(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "DRY RUN") && !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") {
			t.Errorf("expected dry-run or nothing-to-compact output: %s", out)
		}
	})

	// ===== Nothing to Compact (1 commit) =====

	t.Run("compact_nothing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cn")
		out := bdCompact(t, bd, dir, "--force")
		if !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") && !strings.Contains(out, "Only") {
			t.Errorf("expected nothing-to-compact message: %s", out)
		}
	})

	// ===== No --force Errors =====

	t.Run("compact_no_force", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cf")
		// Create issues to generate commits
		bdCreate(t, bd, dir, "Compact no-force 1", "--type", "task")
		bdCreate(t, bd, dir, "Compact no-force 2", "--type", "task")

		// With --days 0, all commits are "old"
		// May fail with "use --force" hint or succeed with "nothing to compact"
		cmd := exec.Command(bd, "compact", "--days", "0")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			// Succeeded — either nothing to compact or only 1 old commit
			_ = out
		} else {
			// Should contain --force hint
			if !strings.Contains(string(out), "--force") {
				t.Errorf("expected --force hint in error: %s", out)
			}
		}
	})

	// ===== Force with --days 0 =====

	t.Run("compact_force", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cx")
		// Create issues + config changes to build commit history
		bdCreate(t, bd, dir, "Compact force 1", "--type", "task")
		cmd := exec.Command(bd, "config", "set", "compact.test1", "v1")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput()
		cmd = exec.Command(bd, "dolt", "commit", "-m", "config commit 1")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput()
		bdCreate(t, bd, dir, "Compact force 2", "--type", "task")

		// Try compacting with --days 0
		out := bdCompact(t, bd, dir, "--force", "--days", "0")
		// Either compacts or reports nothing to compact — both OK
		if !strings.Contains(out, "Compacted") && !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") && !strings.Contains(out, "Only") {
			t.Errorf("expected compact result: %s", out)
		}
	})

	// ===== --days Flag =====

	t.Run("compact_days_flag", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dy")
		bdCreate(t, bd, dir, "Compact days issue", "--type", "task")

		out := bdCompact(t, bd, dir, "--dry-run", "--days", "7")
		if !strings.Contains(out, "7 days") && !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") {
			t.Errorf("expected days reference in output: %s", out)
		}
	})

	// ===== JSON Output =====

	t.Run("compact_dry_run_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cj")
		bdCreate(t, bd, dir, "Compact JSON issue", "--type", "task")

		cmd := exec.Command(bd, "--json", "compact", "--dry-run")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd --json compact --dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		// Should produce some output without crashing
		_ = stdout.String()
	})
}

// TestEmbeddedCompactConcurrent exercises compact --dry-run concurrently.
func TestEmbeddedCompactConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cc")

	bdCreate(t, bd, dir, "Compact concurrent issue", "--type", "task")

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			cmd := exec.Command(bd, "compact", "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("compact --dry-run (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}

// TestEmbeddedCompactMisdatedChild reproduces #6516: two commits editing the
// same row where the child is dated before its parent. Compact must replay the
// preserved commits in graph order (dolt_log.commit_order), not date order,
// or the cherry-pick of the child conflicts.
func TestEmbeddedCompactMisdatedChild(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "md")
	issue := bdCreate(t, bd, dir, "Misdated bead", "--type", "task")
	bdCreate(t, bd, dir, "Filler bead", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	// Both commits are dated in the future so --days 0 preserves them; the
	// child (edit B) is dated 20 minutes before its parent (edit A).
	now := time.Now()
	edits := []struct {
		title string
		date  time.Time
	}{
		{"edit A", now.Add(40 * time.Minute)},
		{"edit B", now.Add(20 * time.Minute)},
	}
	for _, e := range edits {
		if _, err := conn.ExecContext(t.Context(), "UPDATE issues SET title = ? WHERE id = ?", e.title, issue.ID); err != nil {
			t.Fatalf("update title %q: %v", e.title, err)
		}
		if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?, '--date', ?)",
			"repro: "+e.title, e.date.Format(time.RFC3339)); err != nil {
			t.Fatalf("dolt commit %q: %v", e.title, err)
		}
	}
	conn.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	bdCompact(t, bd, dir, "--force", "--days", "0")

	if got := bdShow(t, bd, dir, issue.ID).Title; got != "edit B" {
		t.Errorf("title after compact = %q, want %q", got, "edit B")
	}
}

// TestEmbeddedCompactKeepsPendingWrite: a write that dolt.auto-commit=off
// left in main's working set survives bd compact through the embedded front
// door and stays pending. The hard reset compaction used to end with deleted
// it after its writer had been told it succeeded.
func TestEmbeddedCompactKeepsPendingWrite(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "kp")
	bdCreate(t, bd, dir, "Committed one", "--type", "task")
	bdCreate(t, bd, dir, "Committed two", "--type", "task")
	pending := bdCreate(t, bd, dir, "Pending write", "--type", "task", "--dolt-auto-commit", "off")

	if out := bdCompact(t, bd, dir, "--force", "--days", "0"); !strings.Contains(out, "Compacted") {
		t.Fatalf("expected a compaction, got:\n%s", out)
	}

	if got := bdShow(t, bd, dir, pending.ID).Title; got != "Pending write" {
		t.Errorf("pending issue title after compact = %q, want %q", got, "Pending write")
	}
	withEmbeddedConn(t, beadsDir, func(conn *sql.Conn) {
		var inWorking, inHead int
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM issues WHERE id = ?", pending.ID).Scan(&inWorking); err != nil {
			t.Fatalf("count pending issue: %v", err)
		}
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = ?", pending.ID).Scan(&inHead); err != nil {
			t.Fatalf("count pending issue in HEAD: %v", err)
		}
		if inWorking != 1 || inHead != 0 {
			t.Errorf("pending issue after compact: in working set %d, in HEAD %d; want 1 and 0 (kept, still uncommitted)", inWorking, inHead)
		}
	})
}

// withEmbeddedConn opens the workspace's embedded database on main, runs fn on
// a pinned connection, and releases the engine before any bd subprocess runs.
func withEmbeddedConn(t *testing.T, beadsDir string, fn func(conn *sql.Conn)) {
	t.Helper()
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	fn(conn)
	conn.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// embeddedCommit applies an UPDATE and commits it with the given date, or
// with the current time when date is zero.
func embeddedCommit(t *testing.T, conn *sql.Conn, query string, args []any, msg string, date time.Time) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
	commit, commitArgs := "CALL DOLT_COMMIT('-Am', ?)", []any{msg}
	if !date.IsZero() {
		commit, commitArgs = "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", []any{msg, date.Format(time.RFC3339)}
	}
	if _, err := conn.ExecContext(t.Context(), commit, commitArgs...); err != nil {
		t.Fatalf("dolt commit %q: %v", msg, err)
	}
}

// compactRefState is the part of main's state compaction must not touch when
// it refuses or aborts.
type compactRefState struct {
	head      string
	logCount  int
	tmpBranch int
}

func readCompactRefState(t *testing.T, beadsDir string) compactRefState {
	t.Helper()
	var s compactRefState
	withEmbeddedConn(t, beadsDir, func(conn *sql.Conn) {
		if err := conn.QueryRowContext(t.Context(), "SELECT HASHOF('main')").Scan(&s.head); err != nil {
			t.Fatalf("HASHOF(main): %v", err)
		}
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&s.logCount); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_branches WHERE name = 'compact-tmp'").Scan(&s.tmpBranch); err != nil {
			t.Fatalf("count compact-tmp: %v", err)
		}
	})
	return s
}

// straddledCutoff is the #6645 history: recent-dated A changes a title to
// "edit A" and its old-dated child B restores "ORIGINAL".
type straddledCutoff struct {
	bd, dir, beadsDir, issueID string
	a, b                       storage.CommitInfo
}

func setupStraddledCutoff(t *testing.T, prefix string) straddledCutoff {
	t.Helper()
	f := straddledCutoff{bd: buildEmbeddedBD(t)}
	f.dir, f.beadsDir, _ = bdInit(t, f.bd, "--prefix", prefix)
	f.issueID = bdCreate(t, f.bd, f.dir, "ORIGINAL", "--type", "task").ID
	bdCreate(t, f.bd, f.dir, "Filler bead", "--type", "task")

	now := time.Now()
	withEmbeddedConn(t, f.beadsDir, func(conn *sql.Conn) {
		embeddedCommit(t, conn, "UPDATE issues SET title = ? WHERE id = ?", []any{"edit A", f.issueID},
			"repro: edit A", now.Add(40*time.Minute))
		// B is undated, as in the report: it is the newest old commit by
		// the time compact runs.
		embeddedCommit(t, conn, "UPDATE issues SET title = ? WHERE id = ?", []any{"ORIGINAL", f.issueID},
			"repro: revert to ORIGINAL", time.Time{})
		rows, err := conn.QueryContext(t.Context(), "SELECT commit_hash, date FROM dolt_log ORDER BY commit_order DESC LIMIT 2")
		if err != nil {
			t.Fatalf("read A/B: %v", err)
		}
		defer rows.Close()
		for _, c := range []*storage.CommitInfo{&f.b, &f.a} {
			if !rows.Next() {
				t.Fatalf("dolt_log has fewer than 2 commits: %v", rows.Err())
			}
			if err := rows.Scan(&c.Hash, &c.Date); err != nil {
				t.Fatalf("scan A/B: %v", err)
			}
		}
	})

	if got := bdShow(t, f.bd, f.dir, f.issueID).Title; got != "ORIGINAL" {
		t.Fatalf("title before compact = %q, want %q", got, "ORIGINAL")
	}
	return f
}

// assertRefusalNames checks that out names both straddling commits with their
// dates and carries the remediation hint.
func assertRefusalNames(t *testing.T, f straddledCutoff, out string) {
	t.Helper()
	for _, want := range []string{
		"cannot compact: commit dates are out of history order",
		"recent commit " + f.a.Hash[:8] + " (dated " + f.a.Date.UTC().Format(time.RFC3339) + ")",
		"old commit " + f.b.Hash[:8] + " (dated " + f.b.Date.UTC().Format(time.RFC3339) + ")",
		"No compaction changes were made.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestEmbeddedCompactRecentAncestorOfBoundary reproduces #6645: a preserved
// (recent-dated) commit A is an ancestor of the old-dated boundary B, and B
// reverts A's edit. The squashed base already reflects B, so cherry-picking A
// on top would resurrect A's edit. Compact must refuse up front, naming both
// commits, and leave main untouched; --force does not override the refusal.
func TestEmbeddedCompactRecentAncestorOfBoundary(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	f := setupStraddledCutoff(t, "rb")
	before := readCompactRefState(t, f.beadsDir)

	for _, args := range [][]string{
		{"compact", "--force", "--days", "0"},
		{"compact", "--days", "0"},
	} {
		cmd := exec.Command(f.bd, args...)
		cmd.Dir = f.dir
		cmd.Env = bdEnv(f.dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatalf("bd %s succeeded, want refusal\nstdout:\n%s", strings.Join(args, " "), stdout.String())
		}
		assertRefusalNames(t, f, stderr.String())
		if strings.Contains(stdout.String()+stderr.String(), "Use --force") {
			t.Errorf("bd %s recommends --force on a refused plan:\n%s%s", strings.Join(args, " "), stdout.String(), stderr.String())
		}
	}

	cmd := exec.Command(f.bd, "--json", "compact", "--force", "--days", "0")
	cmd.Dir = f.dir
	cmd.Env = bdEnv(f.dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		t.Fatalf("bd --json compact --force succeeded, want refusal\nstdout:\n%s", stdout.String())
	}
	var jsonErr struct {
		Error string `json:"error"`
		Hint  string `json:"hint"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &jsonErr); err != nil {
		t.Fatalf("parse JSON error: %v\nstderr:\n%s", err, stderr.String())
	}
	assertRefusalNames(t, f, jsonErr.Error+"\n"+jsonErr.Hint)

	if got := bdShow(t, f.bd, f.dir, f.issueID).Title; got != "ORIGINAL" {
		t.Errorf("title after compact = %q, want %q", got, "ORIGINAL")
	}
	if after := readCompactRefState(t, f.beadsDir); after != before {
		t.Errorf("main state after refused compact = %+v, want unchanged %+v", after, before)
	}
}

// TestEmbeddedCompactRefusedDryRun covers the #6645 preview: --dry-run on a
// refused history exits 0 without changing main, reports the refusal and its
// hint instead of recommending --force, and the JSON preview keeps its fields
// and adds refusal.error and refusal.hint.
func TestEmbeddedCompactRefusedDryRun(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	f := setupStraddledCutoff(t, "rd")
	before := readCompactRefState(t, f.beadsDir)

	out := bdCompact(t, f.bd, f.dir, "--dry-run", "--days", "0")
	if !strings.Contains(out, "DRY RUN") || !strings.Contains(out, "Refused: ") || !strings.Contains(out, "Hint: ") {
		t.Errorf("dry-run output lacks the refusal preview:\n%s", out)
	}
	assertRefusalNames(t, f, out)
	if strings.Contains(out, "--force to proceed") || strings.Contains(out, "Result: ") {
		t.Errorf("dry-run forecasts a forced run on a refused plan:\n%s", out)
	}

	cmd := exec.Command(f.bd, "--json", "compact", "--dry-run", "--days", "0")
	cmd.Dir = f.dir
	cmd.Env = bdEnv(f.dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd --json compact --dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var preview map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil {
		t.Fatalf("parse JSON preview: %v\nstdout:\n%s", err, stdout.String())
	}
	if preview["dry_run"] != true {
		t.Errorf("JSON preview dry_run = %v, want true", preview["dry_run"])
	}
	for _, key := range []string{"dry_run", "total_commits", "old_commits", "recent_commits", "cutoff_days", "cutoff_date", "initial_hash", "boundary_hash"} {
		if _, ok := preview[key]; !ok {
			t.Errorf("JSON preview lacks %q: %v", key, preview)
		}
	}
	refusal, _ := preview["refusal"].(map[string]any)
	errText, _ := refusal["error"].(string)
	hint, _ := refusal["hint"].(string)
	if errText == "" || hint == "" {
		t.Fatalf("JSON preview refusal = %v, want error and hint", preview["refusal"])
	}
	assertRefusalNames(t, f, errText+"\n"+hint)

	if got := bdShow(t, f.bd, f.dir, f.issueID).Title; got != "ORIGINAL" {
		t.Errorf("title after dry-run = %q, want %q", got, "ORIGINAL")
	}
	if after := readCompactRefState(t, f.beadsDir); after != before {
		t.Errorf("main state after dry-run = %+v, want unchanged %+v", after, before)
	}
}

// TestEmbeddedCompactMisdatedOldWindow covers a contiguous partition whose old
// commits are dated out of history order: C (now-1m), then E1 (now-5h) and E2
// (now-4h) editing one title, then a recent edit R to another issue. The
// squashed base must end at E2, not the newest-dated old commit, so both
// final values survive and the superseded old commits leave main's history.
func TestEmbeddedCompactMisdatedOldWindow(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mo")
	edited := bdCreate(t, bd, dir, "X0", "--type", "task")
	other := bdCreate(t, bd, dir, "Z0", "--type", "task")

	now := time.Now()
	var c, e1, e2 string
	withEmbeddedConn(t, beadsDir, func(conn *sql.Conn) {
		head := func() string {
			var h string
			if err := conn.QueryRowContext(t.Context(), "SELECT HASHOF('main')").Scan(&h); err != nil {
				t.Fatalf("HASHOF(main): %v", err)
			}
			return h
		}
		update := "UPDATE issues SET title = ? WHERE id = ?"
		embeddedCommit(t, conn, update, []any{"C", edited.ID}, "old: C", now.Add(-time.Minute))
		c = head()
		embeddedCommit(t, conn, update, []any{"Y1", edited.ID}, "old: E1", now.Add(-5*time.Hour))
		e1 = head()
		embeddedCommit(t, conn, update, []any{"Y2", edited.ID}, "old: E2", now.Add(-4*time.Hour))
		e2 = head()
		embeddedCommit(t, conn, update, []any{"R", other.ID}, "recent: R", now.Add(time.Hour))
	})

	bdCompact(t, bd, dir, "--force", "--days", "0")

	if got := bdShow(t, bd, dir, edited.ID).Title; got != "Y2" {
		t.Errorf("old-window title after compact = %q, want %q", got, "Y2")
	}
	if got := bdShow(t, bd, dir, other.ID).Title; got != "R" {
		t.Errorf("recent title after compact = %q, want %q", got, "R")
	}
	withEmbeddedConn(t, beadsDir, func(conn *sql.Conn) {
		for name, hash := range map[string]string{"C": c, "E1": e1, "E2": e2} {
			var n int
			if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", hash).Scan(&n); err != nil {
				t.Fatalf("look up %s in dolt_log: %v", name, err)
			}
			if n != 0 {
				t.Errorf("superseded old commit %s (%s) is still reachable from main", name, hash)
			}
		}
		var replays int
		if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log WHERE message = ?", "recent: R").Scan(&replays); err != nil {
			t.Fatalf("count recent: R in dolt_log: %v", err)
		}
		if replays != 1 {
			t.Errorf("recent commit R is reachable %d times from main, want exactly 1", replays)
		}
	})
}

// TestEmbeddedCompactAbortsDivergentReplay drives the executor directly with
// the #6645 plan the planner now refuses (squash to B, replay A). The replay
// resurrects "edit A", so Compact must notice the committed data differs from
// main before the swap, leave main untouched, and remove its temp branch.
func TestEmbeddedCompactAbortsDivergentReplay(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	f := setupStraddledCutoff(t, "dv")
	before := readCompactRefState(t, f.beadsDir)

	var compactErr error
	withEmbeddedConn(t, f.beadsDir, func(conn *sql.Conn) {
		var root string
		if err := conn.QueryRowContext(t.Context(), "SELECT commit_hash FROM dolt_log ORDER BY commit_order ASC LIMIT 1").Scan(&root); err != nil {
			t.Fatalf("read root commit: %v", err)
		}
		// The plan was read at main's current HEAD, B: that is the tree
		// the replay must reproduce.
		compactErr = versioncontrolops.Compact(t.Context(), conn, root, f.b.Hash, before.head, before.logCount-1, []string{f.a.Hash})
	})
	if compactErr == nil || !strings.Contains(compactErr.Error(), "replayed history differs from main") {
		t.Errorf("Compact error = %v, want the replay divergence abort", compactErr)
	}

	if got := bdShow(t, f.bd, f.dir, f.issueID).Title; got != "ORIGINAL" {
		t.Errorf("title after aborted compact = %q, want %q", got, "ORIGINAL")
	}
	if after := readCompactRefState(t, f.beadsDir); after != before {
		t.Errorf("main state after aborted compact = %+v, want unchanged %+v", after, before)
	}
}
