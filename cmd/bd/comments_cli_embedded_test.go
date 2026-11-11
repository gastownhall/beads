//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdComments runs "bd comments" with the given args and returns stdout.
func bdComments(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"comments"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd comments %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedCommentsCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cc")

	// ===== comments add =====

	t.Run("comments_add", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Comment target", "--type", "task")
		out := bdComments(t, bd, dir, "add", issue.ID, "Hello world")
		if !strings.Contains(out, "Comment added") {
			t.Errorf("expected 'Comment added' in output: %s", out)
		}
	})

	t.Run("comments_add_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON comment", "--type", "task")
		cmd := exec.Command(bd, "comments", "add", issue.ID, "JSON", "comment", "text", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd comments add --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("parse comment JSON: %v\n%s", err, s)
		}
		if m["text"] != "JSON comment text" {
			t.Errorf("expected text 'JSON comment text', got %v", m["text"])
		}
	})

	t.Run("comments_add_from_file", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "File comment", "--type", "task")

		// Write comment to a file
		tmpFile := filepath.Join(t.TempDir(), "comment.txt")
		os.WriteFile(tmpFile, []byte("Comment from file"), 0644)

		out := bdComments(t, bd, dir, "add", issue.ID, "--file", tmpFile)
		if !strings.Contains(out, "Comment added") {
			t.Errorf("expected 'Comment added' in output: %s", out)
		}
	})

	t.Run("comments_add_nonexistent_issue", func(t *testing.T) {
		cmd := exec.Command(bd, "comments", "add", "cc-nonexistent999", "nope")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected failure for nonexistent issue, got: %s", out)
		}
	})

	// Swapped-order add (`bd comments <id> add <text>`) must be rejected, not
	// silently dropped with exit 0 (GH#4642).
	t.Run("comments_swapped_add_rejected", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Swapped add target", "--type", "task")

		cmd := exec.Command(bd, "comments", issue.ID, "add", "should not be stored")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected non-zero exit for swapped-order add, got success:\n%s", out)
		}
		if !strings.Contains(string(out), "bd comments add") {
			t.Errorf("expected hint pointing to `bd comments add`, got:\n%s", out)
		}

		// The stray "add"/text must NOT have been written as a comment.
		list := bdComments(t, bd, dir, issue.ID)
		if strings.Contains(list, "should not be stored") {
			t.Errorf("swapped-order add silently stored a comment:\n%s", list)
		}
	})

	// Any other stray trailing arg on the list form is also rejected.
	t.Run("comments_extra_arg_rejected", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Extra arg target", "--type", "task")
		cmd := exec.Command(bd, "comments", issue.ID, "stray")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected non-zero exit for stray trailing arg, got success:\n%s", out)
		}
	})

	// ===== comments list =====

	t.Run("comments_list", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "List comments", "--type", "task")
		bdComments(t, bd, dir, "add", issue.ID, "First comment")
		bdComments(t, bd, dir, "add", issue.ID, "Second comment")

		out := bdComments(t, bd, dir, issue.ID)
		if !strings.Contains(out, "First comment") {
			t.Errorf("expected 'First comment' in list: %s", out)
		}
		if !strings.Contains(out, "Second comment") {
			t.Errorf("expected 'Second comment' in list: %s", out)
		}
	})

	t.Run("comments_list_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON list comments", "--type", "task")
		bdComments(t, bd, dir, "add", issue.ID, "A comment")

		cmd := exec.Command(bd, "comments", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd comments list --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "[")
		if start >= 0 {
			var comments []map[string]interface{}
			if err := json.Unmarshal([]byte(s[start:]), &comments); err != nil {
				t.Fatalf("parse comments JSON: %v\n%s", err, s)
			}
			if len(comments) == 0 {
				t.Error("expected at least one comment")
			}
		}
	})

	t.Run("comments_list_empty", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No comments", "--type", "task")
		out := bdComments(t, bd, dir, issue.ID)
		// Should not error, may show "no comments"
		_ = out
	})

	t.Run("comments_list_local_time", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Local time comments", "--type", "task")
		bdComments(t, bd, dir, "add", issue.ID, "Timed comment")

		cmd := exec.Command(bd, "comments", issue.ID, "--local-time")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd comments list --local-time failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		_ = stdout.String()
	})

	// ===== Round-trip =====

	t.Run("comments_add_then_list_round_trip", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Round trip", "--type", "task")
		bdComments(t, bd, dir, "add", issue.ID, "Round trip comment")

		cmd := exec.Command(bd, "comments", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("list failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "[")
		if start >= 0 {
			var comments []map[string]interface{}
			json.Unmarshal([]byte(s[start:]), &comments)
			found := false
			for _, c := range comments {
				if c["text"] == "Round trip comment" {
					found = true
				}
			}
			if !found {
				t.Error("expected 'Round trip comment' in listed comments")
			}
		}
	})

	// ===== Attribution =====

	// Who `bd comments add` attributes a comment to, today: --author when it is
	// given, else the actor (--actor > BEADS_ACTOR > BD_ACTOR > git user.name).
	// The two flags are independent: --actor never overrides --author, and two
	// different names are not refused. The Dolt commit each comment makes is
	// the store's own (author "beads <beads@local>", message "bd: comment
	// <id>"), whoever the author is. These are pins, not a ruling: native fix
	// C2 (--author an alias of --actor; two different values refused before
	// any write) flips the two-names row, and its PR changes that row and
	// cites its decision record.
	t.Run("comments_add_author_actor_precedence", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Attribution target", "--type", "task")
		rows := []struct {
			name     string
			env      []string // appended to bdEnv, which strips every BEADS_ and BD_ variable
			global   []string // flags before "comments"
			flags    []string // flags after the text
			wantExit int
			want     string // the stored comment's author; "" when nothing is written
		}{
			{name: "git user.name when nothing names anyone", want: "Test"},
			{name: "--author", flags: []string{"--author", "kim"}, want: "kim"},
			{name: "--actor", global: []string{"--actor", "sam"}, want: "sam"},
			{name: "--actor and --author name two people", global: []string{"--actor", "sam"}, flags: []string{"--author", "kim"}, want: "kim"},
			{name: "--actor and --author name one person", global: []string{"--actor", "sam"}, flags: []string{"--author", "sam"}, want: "sam"},
			{name: "BD_ACTOR", env: []string{"BD_ACTOR=env-bd"}, want: "env-bd"},
			{name: "BEADS_ACTOR", env: []string{"BEADS_ACTOR=env-beads"}, want: "env-beads"},
			{name: "BEADS_ACTOR over BD_ACTOR", env: []string{"BEADS_ACTOR=env-beads", "BD_ACTOR=env-bd"}, want: "env-beads"},
			{name: "-a over BD_ACTOR", env: []string{"BD_ACTOR=env-bd"}, flags: []string{"-a", "kim"}, want: "kim"},
			{name: "--actor over BD_ACTOR", env: []string{"BD_ACTOR=env-bd"}, global: []string{"--actor", "sam"}, want: "sam"},
		}

		// dolt_log, read in-process between commands (no bd process holds the store).
		readLog := func(limit int) (int, [][3]string) {
			t.Helper()
			db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(dir, ".beads", "embeddeddolt"), "cc", "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer cleanup()
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&count); err != nil {
				t.Fatalf("count dolt_log: %v", err)
			}
			rows, err := db.QueryContext(t.Context(), "SELECT author, author_email, message FROM dolt_log ORDER BY commit_order DESC LIMIT ?", limit)
			if err != nil {
				t.Fatalf("read dolt_log: %v", err)
			}
			defer rows.Close()
			var heads [][3]string
			for rows.Next() {
				var h [3]string
				if err := rows.Scan(&h[0], &h[1], &h[2]); err != nil {
					t.Fatalf("scan dolt_log: %v", err)
				}
				heads = append(heads, h)
			}
			return count, heads
		}

		before, _ := readLog(0)
		written := 0
		for i, row := range rows {
			args := append(append(slices.Clone(row.global), "comments", "add", issue.ID, fmt.Sprintf("attribution %d", i)), row.flags...)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = append(bdEnv(dir), row.env...)
			out, err := cmd.CombinedOutput()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("%s: bd %s: %v", row.name, strings.Join(args, " "), err)
			}
			if code != row.wantExit {
				t.Errorf("%s: bd %s: exit %d, want %d\n%s", row.name, strings.Join(args, " "), code, row.wantExit, out)
			}
			if row.wantExit == 0 {
				written++
			}
		}

		var comments []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		}
		listed := bdComments(t, bd, dir, issue.ID, "--json")
		if err := json.Unmarshal([]byte(listed[strings.Index(listed, "["):]), &comments); err != nil {
			t.Fatalf("parse comments: %v\n%s", err, listed)
		}
		authors := make(map[string]string, len(comments))
		for _, c := range comments {
			authors[c.Text] = c.Author
		}
		for i, row := range rows {
			if got := authors[fmt.Sprintf("attribution %d", i)]; got != row.want {
				t.Errorf("%s: author = %q, want %q", row.name, got, row.want)
			}
		}

		after, heads := readLog(written)
		if after-before != written {
			t.Errorf("dolt_log grew by %d over %d written comments, want one commit per comment", after-before, written)
		}
		for _, h := range heads {
			if want := [3]string{"beads", "beads@local", "bd: comment " + issue.ID}; h != want {
				t.Errorf("comment commit (author, email, message) = %q, want %q: the commit is the store's, whoever the comment's author is", h, want)
			}
		}
	})
}

// TestEmbeddedCommentsCLIConcurrent exercises comments concurrently.
func TestEmbeddedCommentsCLIConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cy")

	issue := bdCreate(t, bd, dir, "Concurrent comments target", "--type", "task")

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

			text := fmt.Sprintf("Comment from worker %d", worker)
			cmd := exec.Command(bd, "comments", "add", issue.ID, text)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("add comment: %v\n%s", err, out)
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
