//go:build cgo

package main

import (
	"bytes"
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

// compactStraddleFixture is the #6645 shape on an embedded workspace: a
// preserved (recent-dated) commit "edit A" whose old-dated child undoes the
// edit, on top of enough ordinary history that compaction is actionable. The
// embedded handle is released before the fixture returns so bd subprocesses
// can open the database.
type compactStraddleFixture struct {
	bd, dir, beadsDir string
	issueID           string
	// rootHash is the lowest-commit_order commit; headBefore and
	// logCountBefore are main's tip and log size after the undo commit.
	rootHash, headBefore string
	logCountBefore       int
	// recent is the future-dated edit commit, old its undoing child, with
	// the dates dolt stored for them.
	recent, old storage.CommitInfo
}

func buildCompactStraddle(t *testing.T, prefix string) compactStraddleFixture {
	t.Helper()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", prefix)
	issue := bdCreate(t, bd, dir, "ORIGINAL", "--type", "task")
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
	// "edit A" is dated in the future (preserved by --days 0); its child
	// undoes the title and is dated now, so it becomes the old boundary.
	edits := []struct {
		title string
		date  string // "--date" argument; "" commits undated
	}{
		{"edit A", time.Now().Add(40 * time.Minute).Format(time.RFC3339)},
		{"ORIGINAL", ""},
	}
	messages := []string{"straddle: edit A", "straddle: revert to ORIGINAL"}
	for i, e := range edits {
		if _, err := conn.ExecContext(t.Context(), "UPDATE issues SET title = ? WHERE id = ?", e.title, issue.ID); err != nil {
			t.Fatalf("update title %q: %v", e.title, err)
		}
		if e.date != "" {
			if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", messages[i], e.date); err != nil {
				t.Fatalf("dolt commit %q: %v", messages[i], err)
			}
		} else if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?)", messages[i]); err != nil {
			t.Fatalf("dolt commit %q: %v", messages[i], err)
		}
	}

	f := compactStraddleFixture{bd: bd, dir: dir, beadsDir: beadsDir, issueID: issue.ID}
	if err := conn.QueryRowContext(t.Context(),
		"SELECT commit_hash, date FROM dolt_log WHERE message = ? LIMIT 1", messages[0]).Scan(&f.recent.Hash, &f.recent.Date); err != nil {
		t.Fatalf("read edit A commit: %v", err)
	}
	if err := conn.QueryRowContext(t.Context(),
		"SELECT commit_hash, date FROM dolt_log WHERE message = ? LIMIT 1", messages[1]).Scan(&f.old.Hash, &f.old.Date); err != nil {
		t.Fatalf("read revert commit: %v", err)
	}
	if err := conn.QueryRowContext(t.Context(),
		"SELECT commit_hash FROM dolt_log ORDER BY commit_order ASC LIMIT 1").Scan(&f.rootHash); err != nil {
		t.Fatalf("read root commit: %v", err)
	}
	if err := conn.QueryRowContext(t.Context(),
		"SELECT commit_hash FROM dolt_log ORDER BY commit_order DESC LIMIT 1").Scan(&f.headBefore); err != nil {
		t.Fatalf("read head commit: %v", err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&f.logCountBefore); err != nil {
		t.Fatalf("count log: %v", err)
	}
	conn.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	return f
}

// withReopened runs fn on a fresh SQL connection to the fixture's embedded
// database — any bd subprocess has exited by then, so the store lock is free.
func (f compactStraddleFixture) withReopened(t *testing.T, fn func(conn *sql.Conn)) {
	t.Helper()
	cfg, _ := configfile.Load(f.beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(f.beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		cleanup()
		t.Fatalf("db.Conn: %v", err)
	}
	defer func() {
		conn.Close()
		cleanup()
	}()
	fn(conn)
}

// assertMainUntouched fails when main moved at all since the fixture was
// built: hash, log size, the issue's title, and the branch list (no leftover
// compact-tmp).
func (f compactStraddleFixture) assertMainUntouched(t *testing.T, conn *sql.Conn) {
	t.Helper()
	var head string
	if err := conn.QueryRowContext(t.Context(),
		"SELECT commit_hash FROM dolt_log ORDER BY commit_order DESC LIMIT 1").Scan(&head); err != nil {
		t.Fatalf("read head after: %v", err)
	}
	if head != f.headBefore {
		t.Errorf("main head = %s, want unchanged %s", head, f.headBefore)
	}
	var logCount int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&logCount); err != nil {
		t.Fatalf("count log after: %v", err)
	}
	if logCount != f.logCountBefore {
		t.Errorf("log count = %d, want unchanged %d", logCount, f.logCountBefore)
	}
	var title string
	if err := conn.QueryRowContext(t.Context(),
		"SELECT title FROM issues WHERE id = ?", f.issueID).Scan(&title); err != nil {
		t.Fatalf("read title after: %v", err)
	}
	if title != "ORIGINAL" {
		t.Errorf("title = %q, want %q", title, "ORIGINAL")
	}
	var tmpBranches int
	if err := conn.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM dolt_branches WHERE name = 'compact-tmp'").Scan(&tmpBranches); err != nil {
		t.Fatalf("count compact-tmp branches: %v", err)
	}
	if tmpBranches != 0 {
		t.Errorf("compact-tmp branch left behind (%d)", tmpBranches)
	}
}

// TestEmbeddedCompactRefusesStraddledCutoff covers #6645 (AC-1): a preserved
// (recent-dated) commit that is an ANCESTOR of the date-picked boundary, whose
// edit the boundary undid. Compaction must refuse — naming both commits and
// the remediation — and leave main byte-identical, not silently resurrect the
// edit. Today compact --force succeeds and rewrites the title to "edit A".
func TestEmbeddedCompactRefusesStraddledCutoff(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	f := buildCompactStraddle(t, "sr")

	short := func(hash string) string {
		if len(hash) > 8 {
			return hash[:8]
		}
		return hash
	}

	t.Run("force_refuses", func(t *testing.T) {
		cmd := exec.Command(f.bd, "compact", "--force", "--days", "0")
		cmd.Dir = f.dir
		cmd.Env = bdEnv(f.dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected compact --force to refuse, got success:\n%s", out)
		}
		for _, want := range []string{
			"cannot compact",
			short(f.recent.Hash),
			f.recent.Date.UTC().Format(time.RFC3339),
			short(f.old.Hash),
			f.old.Date.UTC().Format(time.RFC3339),
			"No compaction changes were made",
		} {
			if !strings.Contains(string(out), want) {
				t.Errorf("refusal output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("without_force_refuses_too", func(t *testing.T) {
		cmd := exec.Command(f.bd, "compact", "--days", "0")
		cmd.Dir = f.dir
		cmd.Env = bdEnv(f.dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected compact without --force to refuse, got success:\n%s", out)
		}
		if !strings.Contains(string(out), "cannot compact") {
			t.Errorf("expected refusal message, got:\n%s", out)
		}
	})

	t.Run("json_error", func(t *testing.T) {
		cmd := exec.Command(f.bd, "--json", "compact", "--force", "--days", "0")
		cmd.Dir = f.dir
		cmd.Env = bdEnv(f.dir)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			t.Fatalf("expected json compact to refuse, got success:\nstdout:\n%s", stdout.String())
		}
		start := strings.Index(stderr.String(), "{")
		if start < 0 {
			t.Fatalf("no JSON error object on stderr:\n%s", stderr.String())
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(stderr.String()[start:]), &payload); err != nil {
			t.Fatalf("parse JSON error: %v\nraw: %s", err, stderr.String())
		}
		if msg, _ := payload["error"].(string); !strings.Contains(msg, "cannot compact") {
			t.Errorf("JSON error = %q, want the refusal message", msg)
		}
		if hint, _ := payload["hint"].(string); !strings.Contains(hint, "No compaction changes were made") {
			t.Errorf("JSON hint = %q, want the remediation hint", hint)
		}
	})

	f.withReopened(t, func(conn *sql.Conn) {
		f.assertMainUntouched(t, conn)
	})
}

// TestEmbeddedCompactDryRunRefusedPreview covers #6645 (AC-2): previewing the
// straddled history must exit 0 and change nothing, report the refusal and
// the hint instead of recommending --force, and keep the existing preview
// fields with refusal.error/refusal.hint added in JSON.
func TestEmbeddedCompactDryRunRefusedPreview(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	f := buildCompactStraddle(t, "dp")

	short := func(hash string) string {
		if len(hash) > 8 {
			return hash[:8]
		}
		return hash
	}

	t.Run("text", func(t *testing.T) {
		cmd := exec.Command(f.bd, "compact", "--dry-run", "--days", "0")
		cmd.Dir = f.dir
		cmd.Env = bdEnv(f.dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dry-run must exit 0 even when refused: %v\n%s", err, out)
		}
		for _, want := range []string{
			"DRY RUN",
			"Refused: cannot compact",
			short(f.recent.Hash),
			short(f.old.Hash),
			"Hint: No compaction changes were made",
		} {
			if !strings.Contains(string(out), want) {
				t.Errorf("dry-run output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(string(out), "Run with --force") {
			t.Errorf("refused preview must not recommend --force:\n%s", out)
		}
		if strings.Contains(string(out), "Result:") {
			t.Errorf("refused preview must not print a success forecast:\n%s", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		cmd := exec.Command(f.bd, "--json", "compact", "--dry-run", "--days", "0")
		cmd.Dir = f.dir
		cmd.Env = bdEnv(f.dir)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("json dry-run must exit 0 even when refused: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		start := strings.Index(stdout.String(), "{")
		if start < 0 {
			t.Fatalf("no JSON object in dry-run output:\n%s", stdout.String())
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(stdout.String()[start:]), &m); err != nil {
			t.Fatalf("parse dry-run JSON: %v\nraw: %s", err, stdout.String())
		}
		// The existing preview fields all survive.
		if m["dry_run"] != true {
			t.Errorf("dry_run = %v, want true", m["dry_run"])
		}
		for _, field := range []string{"total_commits", "old_commits", "recent_commits", "cutoff_days", "cutoff_date", "initial_hash", "boundary_hash"} {
			if _, ok := m[field]; !ok {
				t.Errorf("dry-run JSON lost field %q: %v", field, m)
			}
		}
		refusal, ok := m["refusal"].(map[string]interface{})
		if !ok {
			t.Fatalf("dry-run JSON has no refusal object: %v", m)
		}
		if msg, _ := refusal["error"].(string); !strings.Contains(msg, "cannot compact") || !strings.Contains(msg, short(f.recent.Hash)) {
			t.Errorf("refusal.error = %q, want the refusal naming the recent commit", msg)
		}
		if hint, _ := refusal["hint"].(string); !strings.Contains(hint, "No compaction changes were made") {
			t.Errorf("refusal.hint = %q, want the remediation hint", hint)
		}
	})

	f.withReopened(t, func(conn *sql.Conn) {
		f.assertMainUntouched(t, conn)
	})
}

// TestEmbeddedCompactMisdatedOldWindow covers #6645's sibling defect: OLD
// commits dated out of history order (a clock that jumped back mid-window).
// Date-picked selection squashed from an obsolete boundary and kept the old
// chain reachable; commit-order selection must squash the whole old window,
// preserve the old window's final value (Y2) and the recent edit (R), and
// leave the superseded old commits unreachable.
func TestEmbeddedCompactMisdatedOldWindow(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ow")
	window := bdCreate(t, bd, dir, "ORIGINAL", "--type", "task")
	other := bdCreate(t, bd, dir, "Other bead", "--type", "task")

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
	// Ancestry: ... creates -> C (undated, old) -> E1 (Y1, -5h) -> E2 (Y2,
	// -4h) -> R (future, edits the OTHER issue). The old window's dates run
	// backwards relative to its own history.
	now := time.Now()
	steps := []struct {
		issueID, title, msg string
		date                string
	}{
		{window.ID, "C1", "oldwindow: C", ""},
		{window.ID, "Y1", "oldwindow: E1", now.Add(-5 * time.Hour).Format(time.RFC3339)},
		{window.ID, "Y2", "oldwindow: E2", now.Add(-4 * time.Hour).Format(time.RFC3339)},
		{other.ID, "R edit", "oldwindow: R", now.Add(time.Hour).Format(time.RFC3339)},
	}
	for _, s := range steps {
		if _, err := conn.ExecContext(t.Context(), "UPDATE issues SET title = ? WHERE id = ?", s.title, s.issueID); err != nil {
			t.Fatalf("update title %q: %v", s.title, err)
		}
		if s.date != "" {
			if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", s.msg, s.date); err != nil {
				t.Fatalf("dolt commit %q: %v", s.msg, err)
			}
		} else if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?)", s.msg); err != nil {
			t.Fatalf("dolt commit %q: %v", s.msg, err)
		}
	}
	conn.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	bdCompact(t, bd, dir, "--force", "--days", "0")

	if got := bdShow(t, bd, dir, window.ID).Title; got != "Y2" {
		t.Errorf("old-window issue title after compact = %q, want %q", got, "Y2")
	}
	if got := bdShow(t, bd, dir, other.ID).Title; got != "R edit" {
		t.Errorf("recent-edit issue title after compact = %q, want %q", got, "R edit")
	}

	compactStraddleFixture{bd: bd, beadsDir: beadsDir}.withReopened(t, func(conn *sql.Conn) {
		for _, msg := range []string{"oldwindow: C", "oldwindow: E1", "oldwindow: E2"} {
			var count int
			if err := conn.QueryRowContext(t.Context(),
				"SELECT COUNT(*) FROM dolt_log WHERE message = ?", msg).Scan(&count); err != nil {
				t.Fatalf("count %q: %v", msg, err)
			}
			if count != 0 {
				t.Errorf("superseded commit %q still reachable on main (%d rows)", msg, count)
			}
		}
		var replays int
		if err := conn.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM dolt_log WHERE message = ?", "oldwindow: R").Scan(&replays); err != nil {
			t.Fatalf("count oldwindow: R: %v", err)
		}
		if replays != 1 {
			t.Errorf("recent commit replayed %d times, want exactly 1", replays)
		}
	})
}
