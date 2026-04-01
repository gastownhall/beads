//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// bdComments runs "bd comments" with the given args and returns stdout.
func bdComments(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"comments"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd comments %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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
		cmd := exec.Command(bd, "comments", "add", issue.ID, "JSON comment text", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd comments add --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
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
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd comments list --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
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
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd comments list --local-time failed: %v\n%s", err, out)
		}
		_ = out
	})

	// ===== Round-trip =====

	t.Run("comments_add_then_list_round_trip", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Round trip", "--type", "task")
		bdComments(t, bd, dir, "add", issue.ID, "Round trip comment")

		cmd := exec.Command(bd, "comments", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("list failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
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
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
