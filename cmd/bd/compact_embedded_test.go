//go:build cgo

package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

// compactLogRow is one dolt_log row as TestEmbeddedCompactKeepsCommitDates
// compares it across a compaction.
type compactLogRow struct {
	committer, email, message string
	date                      time.Time
}

// TestEmbeddedCompactKeepsCommitDates pins #7294 end to end: compaction keeps
// each replayed commit's date (to the millisecond, so two commits in one
// second keep their order), committer, email and message, dates the squashed
// base with the boundary commit, and so leaves AS OF reads and the tracker's
// previous-external_ref lookup between two kept commits unchanged.
func TestEmbeddedCompactKeepsCommitDates(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "kd")
	issue := bdCreate(t, bd, dir, "Dated bead", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	openConn := func() (*sql.Conn, func()) {
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatalf("db.Conn: %v", err)
		}
		if _, err := conn.ExecContext(t.Context(), "SET time_zone = '+00:00'"); err != nil {
			t.Fatalf("set time_zone: %v", err)
		}
		return conn, func() {
			conn.Close()
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		}
	}
	// readLog returns dolt_log in graph order, oldest first.
	readLog := func(conn *sql.Conn) []compactLogRow {
		rows, err := conn.QueryContext(t.Context(), "SELECT committer, email, message, date FROM dolt_log ORDER BY commit_order")
		if err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		defer rows.Close()
		var out []compactLogRow
		for rows.Next() {
			var r compactLogRow
			if err := rows.Scan(&r.committer, &r.email, &r.message, &r.date); err != nil {
				t.Fatalf("scan dolt_log: %v", err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read dolt_log: %v", err)
		}
		return out
	}

	// Dated in the future so --days 0 keeps all three commits in the replay
	// window; the first two are 333 ms apart within one second.
	s := time.Now().UTC().Truncate(time.Second)
	const author = "Original Author <orig@example.com>"
	commits := []struct {
		title, ref string // empty: an --allow-empty commit
		message    string
		date       time.Time
	}{
		{"edit A", "ext-A", "keep: edit A", s.Add(20*time.Minute + 123*time.Millisecond)},
		{"", "", "keep: empty", s.Add(20*time.Minute + 456*time.Millisecond)},
		{"edit B", "ext-B", "keep: edit B", s.Add(40 * time.Minute)},
	}
	conn, closeConn := openConn()
	for _, c := range commits {
		date := c.date.Format("2006-01-02T15:04:05.000Z07:00")
		if c.title == "" {
			if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('--allow-empty', '-m', ?, '--date', ?, '--author', ?)", c.message, date, author); err != nil {
				t.Fatalf("dolt commit %q: %v", c.message, err)
			}
			continue
		}
		if _, err := conn.ExecContext(t.Context(), "UPDATE issues SET title = ?, external_ref = ? WHERE id = ?", c.title, c.ref, issue.ID); err != nil {
			t.Fatalf("update %q: %v", c.title, err)
		}
		if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?, '--date', ?, '--author', ?)", c.message, date, author); err != nil {
			t.Fatalf("dolt commit %q: %v", c.message, err)
		}
	}
	before := readLog(conn)
	kept := before[len(before)-len(commits):]
	for i, c := range commits {
		if kept[i].message != c.message || !kept[i].date.Equal(c.date) {
			t.Fatalf("setup: commit %d is %q at %s, want %q at %s", i, kept[i].message, kept[i].date, c.message, c.date)
		}
	}
	boundary := before[len(before)-len(commits)-1]
	closeConn()

	bdCompact(t, bd, dir, "--force", "--days", "0")

	conn, closeConn = openConn()
	defer closeConn()
	after := readLog(conn)
	if len(after) != len(commits)+2 {
		t.Fatalf("dolt_log after compact has %d commits, want initial + base + %d kept: %+v", len(after), len(commits), after)
	}
	for i, want := range kept {
		got := after[len(after)-len(commits)+i]
		if got.message != want.message || !got.date.Equal(want.date) || got.committer != want.committer || got.email != want.email {
			t.Errorf("replayed commit %d = %q by %s <%s> at %s, want %q by %s <%s> at %s",
				i, got.message, got.committer, got.email, got.date.Format(time.RFC3339Nano),
				want.message, want.committer, want.email, want.date.Format(time.RFC3339Nano))
		}
	}
	if base := after[1]; !strings.HasPrefix(base.message, "compact: squash") || !base.date.Equal(boundary.date) {
		t.Errorf("squashed base = %q at %s, want a compact: squash commit at the boundary's date %s",
			base.message, base.date.Format(time.RFC3339Nano), boundary.date.Format(time.RFC3339Nano))
	}

	// Between edit A and edit B. A time literal: AS OF reads a bare string as a ref.
	between := s.Add(30 * time.Minute)
	var title string
	if err := conn.QueryRowContext(t.Context(),
		fmt.Sprintf("SELECT title FROM issues AS OF TIMESTAMP('%s') WHERE id = ?", between.Format("2006-01-02 15:04:05")),
		issue.ID).Scan(&title); err != nil {
		t.Fatalf("AS OF read: %v", err)
	}
	if title != "edit A" {
		t.Errorf("title AS OF %s = %q, want %q", between.Format(time.RFC3339), title, "edit A")
	}
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	ref, found, err := issueops.PreviousExternalRefInTx(t.Context(), tx, issue.ID, between)
	if err != nil {
		t.Fatalf("PreviousExternalRefInTx: %v", err)
	}
	if !found || ref != "ext-A" {
		t.Errorf("PreviousExternalRefInTx(%s) = %q (found %v), want %q", between.Format(time.RFC3339), ref, found, "ext-A")
	}
}
