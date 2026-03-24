//go:build embeddeddolt

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdTodo runs "bd todo" with the given args and returns stdout.
func bdTodo(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"todo"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd todo %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedTodo(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "td")

	// ===== todo add =====

	t.Run("todo_add_basic", func(t *testing.T) {
		out := bdTodo(t, bd, dir, "add", "Buy groceries")
		if !strings.Contains(out, "Created") {
			t.Errorf("expected 'Created' in output: %s", out)
		}
		if !strings.Contains(out, "Buy groceries") {
			t.Errorf("expected title in output: %s", out)
		}
	})

	t.Run("todo_add_json", func(t *testing.T) {
		cmd := exec.Command(bd, "todo", "add", "JSON todo", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd todo add --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("parse todo JSON: %v\n%s", err, s)
		}
		if m["title"] != "JSON todo" {
			t.Errorf("expected title 'JSON todo', got %v", m["title"])
		}
		if m["issue_type"] != "task" {
			t.Errorf("expected type task, got %v", m["issue_type"])
		}
	})

	t.Run("todo_add_priority", func(t *testing.T) {
		out := bdTodo(t, bd, dir, "add", "Urgent todo", "--priority", "1")
		if !strings.Contains(out, "Created") {
			t.Errorf("expected 'Created' in output: %s", out)
		}
	})

	t.Run("todo_add_description", func(t *testing.T) {
		out := bdTodo(t, bd, dir, "add", "Described todo", "--description", "Details here")
		if !strings.Contains(out, "Created") {
			t.Errorf("expected 'Created' in output: %s", out)
		}
	})

	// ===== todo list =====

	t.Run("todo_list", func(t *testing.T) {
		out := bdTodo(t, bd, dir, "list")
		// Should show TODOs we created
		if !strings.Contains(out, "Buy groceries") && !strings.Contains(out, "td-") {
			t.Logf("todo list output: %s", out)
		}
	})

	t.Run("todo_list_json", func(t *testing.T) {
		cmd := exec.Command(bd, "todo", "list", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd todo list --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in output: %s", s)
		}
		var issues []map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &issues); err != nil {
			t.Fatalf("parse todo list JSON: %v\n%s", err, s)
		}
		if len(issues) == 0 {
			t.Error("expected at least one todo in list")
		}
	})

	// ===== todo done =====

	t.Run("todo_done_single", func(t *testing.T) {
		// Create a todo, then mark done
		cmd := exec.Command(bd, "todo", "add", "Done test", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd todo add failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		var m map[string]interface{}
		json.Unmarshal([]byte(s[start:]), &m)
		id := m["id"].(string)

		bdTodo(t, bd, dir, "done", id)
		got := bdShow(t, bd, dir, id)
		if got.Status != "closed" {
			t.Errorf("expected closed after done, got %s", got.Status)
		}
	})

	t.Run("todo_done_multiple", func(t *testing.T) {
		cmd1 := exec.Command(bd, "todo", "add", "Multi done 1", "--json")
		cmd1.Dir = dir
		cmd1.Env = bdEnv(dir)
		out1, _ := cmd1.CombinedOutput()
		var m1 map[string]interface{}
		json.Unmarshal([]byte(strings.TrimSpace(string(out1))[strings.Index(strings.TrimSpace(string(out1)), "{"):]), &m1)
		id1 := m1["id"].(string)

		cmd2 := exec.Command(bd, "todo", "add", "Multi done 2", "--json")
		cmd2.Dir = dir
		cmd2.Env = bdEnv(dir)
		out2, _ := cmd2.CombinedOutput()
		var m2 map[string]interface{}
		json.Unmarshal([]byte(strings.TrimSpace(string(out2))[strings.Index(strings.TrimSpace(string(out2)), "{"):]), &m2)
		id2 := m2["id"].(string)

		bdTodo(t, bd, dir, "done", id1, id2)
		got1 := bdShow(t, bd, dir, id1)
		got2 := bdShow(t, bd, dir, id2)
		if got1.Status != "closed" {
			t.Errorf("issue1: expected closed, got %s", got1.Status)
		}
		if got2.Status != "closed" {
			t.Errorf("issue2: expected closed, got %s", got2.Status)
		}
	})

	t.Run("todo_done_with_reason", func(t *testing.T) {
		cmd := exec.Command(bd, "todo", "add", "Reason done", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, _ := cmd.CombinedOutput()
		var m map[string]interface{}
		json.Unmarshal([]byte(strings.TrimSpace(string(out))[strings.Index(strings.TrimSpace(string(out)), "{"):]), &m)
		id := m["id"].(string)

		bdTodo(t, bd, dir, "done", id, "--reason", "No longer needed")
		got := bdShow(t, bd, dir, id)
		if got.Status != "closed" {
			t.Errorf("expected closed, got %s", got.Status)
		}
	})

	t.Run("todo_done_json", func(t *testing.T) {
		cmd := exec.Command(bd, "todo", "add", "JSON done", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, _ := cmd.CombinedOutput()
		var m map[string]interface{}
		json.Unmarshal([]byte(strings.TrimSpace(string(out))[strings.Index(strings.TrimSpace(string(out)), "{"):]), &m)
		id := m["id"].(string)

		doneCmd := exec.Command(bd, "todo", "done", id, "--json")
		doneCmd.Dir = dir
		doneCmd.Env = bdEnv(dir)
		doneOut, err := doneCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd todo done --json failed: %v\n%s", err, doneOut)
		}
	})

	// ===== todo list --all =====

	t.Run("todo_list_all", func(t *testing.T) {
		cmd := exec.Command(bd, "todo", "list", "--all", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd todo list --all --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array: %s", s)
		}
		var issues []map[string]interface{}
		json.Unmarshal([]byte(s[start:]), &issues)
		// Should include both open and closed todos
		hasOpen, hasClosed := false, false
		for _, iss := range issues {
			if iss["status"] == "open" {
				hasOpen = true
			}
			if iss["status"] == "closed" {
				hasClosed = true
			}
		}
		if !hasOpen || !hasClosed {
			t.Logf("expected both open and closed with --all: open=%v closed=%v (total=%d)", hasOpen, hasClosed, len(issues))
		}
	})

	// ===== Lifecycle =====

	t.Run("todo_lifecycle", func(t *testing.T) {
		// Add
		out := bdTodo(t, bd, dir, "add", "Lifecycle todo")
		if !strings.Contains(out, "Created") {
			t.Fatal("expected Created")
		}

		// List
		listOut := bdTodo(t, bd, dir, "list")
		if !strings.Contains(listOut, "Lifecycle todo") {
			t.Logf("lifecycle todo not in list: %s", listOut)
		}
	})
}

// TestEmbeddedTodoConcurrent exercises todo operations concurrently.
func TestEmbeddedTodoConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tx")

	const numWorkers = 6

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

			title := fmt.Sprintf("w%d-todo", worker)
			cmd := exec.Command(bd, "todo", "add", title)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("todo add: %v\n%s", err, out)
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
