//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func bdProxiedReopen(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"reopen"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd reopen %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedReopenFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"reopen"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd reopen %s to fail, got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedReopenJSON(t *testing.T, bd, dir string, args ...string) []*types.Issue {
	t.Helper()
	fullArgs := append([]string{"reopen", "--json"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd reopen --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array in reopen output:\n%s", stdout)
	}
	var issues []*types.Issue
	if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
		t.Fatalf("parse reopen JSON: %v\nraw: %s", err, stdout[start:])
	}
	return issues
}

func readReopenComment(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var got sql.NullString
	err := db.QueryRowContext(context.Background(),
		"SELECT comment FROM events WHERE issue_id = ? AND event_type = ? ORDER BY created_at DESC LIMIT 1",
		id, string(types.EventCommented)).Scan(&got)
	if err == sql.ErrNoRows {
		if err := db.QueryRowContext(context.Background(),
			"SELECT comment FROM wisp_events WHERE issue_id = ? AND event_type = ? ORDER BY created_at DESC LIMIT 1",
			id, string(types.EventCommented)).Scan(&got); err != nil {
			if err == sql.ErrNoRows {
				return ""
			}
			t.Fatalf("read reopen comment for %s: %v", id, err)
		}
	} else if err != nil {
		t.Fatalf("read reopen comment for %s: %v", id, err)
	}
	if !got.Valid {
		return ""
	}
	return got.String
}

func TestProxiedServerReopen(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("no_ids_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ron")
		out := bdProxiedReopenFail(t, bd, p.dir)
		if !strings.Contains(out, "requires at least 1 arg") {
			t.Errorf("expected cobra arg-count error, got: %s", out)
		}
	})

	t.Run("reopens_closed_issue", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rorc")
		issue := bdProxiedCreate(t, bd, p.dir, "Reopen target")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		out := bdProxiedReopen(t, bd, p.dir, issue.ID)
		if !strings.Contains(out, "Reopened") {
			t.Errorf("expected 'Reopened' in stdout, got: %s", out)
		}
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, issue.ID); got != types.StatusOpen {
			t.Errorf("status: got %q, want open", got)
		}
		if got := readCloseReason(t, db, issue.ID); got != "" {
			t.Errorf("close_reason should be cleared, got %q", got)
		}
		var closedAt sql.NullTime
		if err := db.QueryRowContext(context.Background(),
			"SELECT closed_at FROM issues WHERE id = ?", issue.ID).Scan(&closedAt); err != nil {
			t.Fatalf("read closed_at: %v", err)
		}
		if closedAt.Valid {
			t.Errorf("closed_at should be NULL after reopen, got %v", closedAt.Time)
		}
	})

	t.Run("reason_recorded_as_comment", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rorr")
		issue := bdProxiedCreate(t, bd, p.dir, "Reason target")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		bdProxiedReopen(t, bd, p.dir, issue.ID, "--reason", "regression in QA")
		db := openProxiedDB(t, p)
		if got := readReopenComment(t, db, issue.ID); got != "regression in QA" {
			t.Errorf("reopen comment: got %q, want %q", got, "regression in QA")
		}
	})

	t.Run("already_open_prints_warning", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "roao")
		issue := bdProxiedCreate(t, bd, p.dir, "Already open")
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "reopen", issue.ID)
		if err != nil {
			t.Fatalf("already-open reopen should exit 0, got: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stderr, "already open") {
			t.Errorf("expected 'already open' on stderr, got stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("missing_id_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rom")
		out := bdProxiedReopenFail(t, bd, p.dir, "rom-does-not-exist")
		if !strings.Contains(out, "not found") {
			t.Errorf("expected 'not found' error, got: %s", out)
		}
	})

	t.Run("reopens_wisp", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rorw")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp reopen", "--ephemeral")
		bdProxiedClose(t, bd, p.dir, wisp.ID)
		bdProxiedReopen(t, bd, p.dir, wisp.ID)
		db := openProxiedDB(t, p)
		var status string
		if err := db.QueryRowContext(context.Background(),
			"SELECT status FROM wisps WHERE id = ?", wisp.ID).Scan(&status); err != nil {
			t.Fatalf("read wisp status: %v", err)
		}
		if types.Status(status) != types.StatusOpen {
			t.Errorf("wisp status: got %q, want open", status)
		}
		var closedAt sql.NullTime
		if err := db.QueryRowContext(context.Background(),
			"SELECT closed_at FROM wisps WHERE id = ?", wisp.ID).Scan(&closedAt); err != nil {
			t.Fatalf("read wisp closed_at: %v", err)
		}
		if closedAt.Valid {
			t.Errorf("wisp closed_at should be NULL after reopen, got %v", closedAt.Time)
		}
		var evtCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
			wisp.ID, string(types.EventReopened)).Scan(&evtCount); err != nil {
			t.Fatalf("count wisp reopened events: %v", err)
		}
		if evtCount != 1 {
			t.Errorf("wisp_events EventReopened count: got %d, want 1", evtCount)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "roj")
		issue := bdProxiedCreate(t, bd, p.dir, "JSON reopen")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		issues := bdProxiedReopenJSON(t, bd, p.dir, issue.ID)
		if len(issues) != 1 || issues[0].ID != issue.ID {
			t.Errorf("reopen JSON: got %+v, want [%s]", issues, issue.ID)
		}
		if issues[0].Status != types.StatusOpen {
			t.Errorf("returned issue status: got %q, want open", issues[0].Status)
		}
	})

	t.Run("batch_single_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rosd")
		a := bdProxiedCreate(t, bd, p.dir, "Batch A")
		b := bdProxiedCreate(t, bd, p.dir, "Batch B")
		bdProxiedClose(t, bd, p.dir, a.ID, b.ID)
		db := openProxiedDB(t, p)
		before := readDoltHead(t, db)
		bdProxiedReopen(t, bd, p.dir, a.ID, b.ID)
		count := readDoltLogCountSince(t, db, before)
		if count != 1 {
			t.Errorf("expected exactly 1 new dolt commit for batch reopen, got %d", count)
		}
		msg := readDoltLogTopMessage(t, db)
		if !strings.HasPrefix(msg, "bd: reopen ") {
			t.Errorf("commit message should begin with 'bd: reopen ', got: %q", msg)
		}
		for _, id := range []string{a.ID, b.ID} {
			if !strings.Contains(msg, id) {
				t.Errorf("commit message %q should contain id %s", msg, id)
			}
		}
	})

	t.Run("reblocks_dependent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rorb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Reblock blocker")
		blocked := bdProxiedCreate(t, bd, p.dir, "Reblock blocked", "--deps", "depends-on:"+blocker.ID)
		bdProxiedClose(t, bd, p.dir, blocker.ID)
		db := openProxiedDB(t, p)
		if readIsBlocked(t, db, blocked.ID) {
			t.Fatal("dependent should be unblocked while blocker is closed")
		}
		bdProxiedReopen(t, bd, p.dir, blocker.ID)
		if !readIsBlocked(t, db, blocked.ID) {
			t.Error("dependent should be re-blocked after blocker reopens")
		}
	})

	t.Run("reopen_with_reason_short", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rorrs")
		issue := bdProxiedCreate(t, bd, p.dir, "Short reason target")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		bdProxiedReopen(t, bd, p.dir, issue.ID, "-r", "needs more work")
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, issue.ID); got != types.StatusOpen {
			t.Errorf("status: got %q, want open", got)
		}
		if got := readReopenComment(t, db, issue.ID); got != "needs more work" {
			t.Errorf("reopen comment via -r: got %q, want %q", got, "needs more work")
		}
	})

	t.Run("reopen_clears_defer_until", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rocd")
		issue := bdProxiedCreate(t, bd, p.dir, "Deferred reopen", "--defer", "+8760h")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		bdProxiedReopen(t, bd, p.dir, issue.ID)
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, issue.ID); got != types.StatusOpen {
			t.Errorf("status: got %q, want open", got)
		}
		var deferUntil sql.NullTime
		if err := db.QueryRowContext(context.Background(),
			"SELECT defer_until FROM issues WHERE id = ?", issue.ID).Scan(&deferUntil); err != nil {
			t.Fatalf("read defer_until: %v", err)
		}
		if deferUntil.Valid {
			t.Errorf("defer_until should be NULL after reopen, got %v", deferUntil.Time)
		}
	})

	t.Run("last_touched_not_supported", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rolt")
		_ = bdProxiedCreate(t, bd, p.dir, "Recent create")
		out := bdProxiedReopenFail(t, bd, p.dir)
		if !strings.Contains(out, "requires at least 1 arg") {
			t.Errorf("proxied reopen must not fall back to last-touched; got: %s", out)
		}
	})

	t.Run("hooks_fire_on_update", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("hook script form is POSIX shell")
		}
		marker := filepath.Join(t.TempDir(), "on_update_marker")
		script := "#!/bin/sh\nprintf '%s\\n' \"$1\" > " + shellQuote(marker) + "\n"
		p := newSharedProxiedProjectWithHooks(t, bd, "rofh", map[string]string{"on_update": script})
		issue := bdProxiedCreate(t, bd, p.dir, "Hook reopen")
		bdProxiedClose(t, bd, p.dir, issue.ID)
		_ = os.Remove(marker)
		bdProxiedReopen(t, bd, p.dir, issue.ID)
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("on_update hook marker not written after reopen: %v", err)
		}
		if !strings.Contains(string(data), issue.ID) {
			t.Errorf("hook marker missing issue ID; got: %q", string(data))
		}
	})

	t.Run("wisp_reopen_clears_defer_until", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rowd")
		wisp := bdProxiedCreate(t, bd, p.dir, "Deferred wisp", "--ephemeral", "--defer", "+8760h")
		bdProxiedClose(t, bd, p.dir, wisp.ID)
		bdProxiedReopen(t, bd, p.dir, wisp.ID)
		db := openProxiedDB(t, p)
		var deferUntil sql.NullTime
		if err := db.QueryRowContext(context.Background(),
			"SELECT defer_until FROM wisps WHERE id = ?", wisp.ID).Scan(&deferUntil); err != nil {
			t.Fatalf("read wisp defer_until: %v", err)
		}
		if deferUntil.Valid {
			t.Errorf("wisp defer_until should be NULL after reopen, got %v", deferUntil.Time)
		}
	})

	t.Run("wisp_reason_recorded_as_comment", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rowr")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp reason", "--ephemeral")
		bdProxiedClose(t, bd, p.dir, wisp.ID)
		bdProxiedReopen(t, bd, p.dir, wisp.ID, "--reason", "wisp regression")
		db := openProxiedDB(t, p)
		if got := readReopenComment(t, db, wisp.ID); got != "wisp regression" {
			t.Errorf("wisp reopen comment: got %q, want %q", got, "wisp regression")
		}
	})

	t.Run("wisp_reopen_with_reason_short", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rowrs")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp short reason", "--ephemeral")
		bdProxiedClose(t, bd, p.dir, wisp.ID)
		bdProxiedReopen(t, bd, p.dir, wisp.ID, "-r", "still flaky")
		db := openProxiedDB(t, p)
		var status string
		if err := db.QueryRowContext(context.Background(),
			"SELECT status FROM wisps WHERE id = ?", wisp.ID).Scan(&status); err != nil {
			t.Fatalf("read wisp status: %v", err)
		}
		if types.Status(status) != types.StatusOpen {
			t.Errorf("wisp status: got %q, want open", status)
		}
		if got := readReopenComment(t, db, wisp.ID); got != "still flaky" {
			t.Errorf("wisp reopen comment via -r: got %q, want %q", got, "still flaky")
		}
	})
}

func TestProxiedServerReopenConcurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "rxc")

	const (
		numWorkers      = 10
		issuesPerWorker = 5
	)

	type prep struct {
		ids []string
	}
	preps := make([]prep, numWorkers)
	for w := 0; w < numWorkers; w++ {
		for i := 0; i < issuesPerWorker; i++ {
			title := fmt.Sprintf("concurrent-reopen-%d-%d", w, i)
			issue := bdProxiedCreate(t, bd, p.dir, title)
			bdProxiedClose(t, bd, p.dir, issue.ID)
			preps[w].ids = append(preps[w].ids, issue.ID)
		}
	}

	type ws struct {
		reopened int
		errs     []string
	}
	results := make([]ws, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		w := w
		go func() {
			defer wg.Done()
			r := &results[w]
			for _, id := range preps[w].ids {
				cmd := exec.Command(bd, "reopen", id)
				cmd.Dir = p.dir
				cmd.Env = bdProxiedEnv(p.dir)
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					r.errs = append(r.errs, fmt.Sprintf("reopen %s: %v\n%s", id, err, stderr.String()))
					continue
				}
				r.reopened++
			}
		}()
	}
	wg.Wait()

	totalReopened := 0
	for w, r := range results {
		totalReopened += r.reopened
		for _, e := range r.errs {
			t.Errorf("worker %d: %s", w, e)
		}
	}
	want := numWorkers * issuesPerWorker
	if totalReopened != want {
		t.Errorf("reopened count: got %d, want %d", totalReopened, want)
	}

	db := openProxiedDB(t, p)
	var closedCount int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM issues WHERE status = 'closed'").Scan(&closedCount); err != nil {
		t.Fatalf("query closed count: %v", err)
	}
	if closedCount != 0 {
		t.Errorf("closed issues remain after concurrent reopen: %d", closedCount)
	}
}
