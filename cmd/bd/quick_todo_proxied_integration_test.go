//go:build cgo

package main

import (
	"strings"
	"testing"
)

func TestProxiedServerQuick(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("q_creates_and_outputs_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "qk")
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "q", "Quick captured task")
		if err != nil {
			t.Fatalf("q failed: %v\nstderr:\n%s", err, stderr)
		}
		id := strings.TrimSpace(stdout)
		if id == "" {
			t.Fatal("q produced no ID")
		}
		got := bdProxiedShow(t, bd, p.dir, id)
		if got.Title != "Quick captured task" {
			t.Errorf("title = %q, want 'Quick captured task'", got.Title)
		}
	})

	t.Run("q_with_parent_creates_child", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "qp")
		parent := bdProxiedCreate(t, bd, p.dir, "Quick parent", "--type", "epic")
		bdProxiedLabel(t, bd, p.dir, "add", parent.ID, "inherited")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "q", "Quick child", "--parent", parent.ID)
		if err != nil {
			t.Fatalf("q --parent failed: %v\nstderr:\n%s", err, stderr)
		}
		childID := strings.TrimSpace(stdout)
		if !strings.HasPrefix(childID, parent.ID+".") {
			t.Errorf("child ID %q should be hierarchical under %s", childID, parent.ID)
		}

		db := openProxiedDB(t, p)
		assertProxiedDepExists(t, db, childID, parent.ID)

		labels := getProxiedLabels(t, db, childID)
		found := false
		for _, l := range labels {
			if l == "inherited" {
				found = true
			}
		}
		if !found {
			t.Errorf("child labels = %v, want inherited label from parent", labels)
		}
	})
}

func TestProxiedServerTodo(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("add_list_done", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "td")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "todo", "add", "Write the tests")
		if err != nil {
			t.Fatalf("todo add failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(stdout, "Write the tests") {
			t.Errorf("expected created title in output, got:\n%s", stdout)
		}

		listOut, _, err := bdProxiedRunBuffers(t, bd, p.dir, "todo", "list")
		if err != nil {
			t.Fatalf("todo list failed: %v", err)
		}
		if !strings.Contains(listOut, "Write the tests") {
			t.Errorf("expected todo in list, got:\n%s", listOut)
		}
	})

	t.Run("add_json_and_done", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "tj")
		created := bdProxiedCreate(t, bd, p.dir, "Task to finish", "--type", "task")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "todo", "done", created.ID)
		if err != nil {
			t.Fatalf("todo done failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Closed") {
			t.Errorf("expected 'Closed' output, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, created.ID); got.Status != "closed" {
			t.Errorf("status = %q, want closed", got.Status)
		}
	})

	t.Run("bare_todo_lists_open_tasks", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "tb")
		open := bdProxiedCreate(t, bd, p.dir, "Open todo", "--type", "task")
		done := bdProxiedCreate(t, bd, p.dir, "Done todo", "--type", "task")
		if _, _, err := bdProxiedRunBuffers(t, bd, p.dir, "todo", "done", done.ID); err != nil {
			t.Fatalf("todo done failed: %v", err)
		}

		out, _, err := bdProxiedRunBuffers(t, bd, p.dir, "todo")
		if err != nil {
			t.Fatalf("bare todo failed: %v", err)
		}
		if !strings.Contains(out, open.ID) {
			t.Errorf("expected open todo %s in list, got:\n%s", open.ID, out)
		}
		if strings.Contains(out, done.ID) {
			t.Errorf("closed todo %s should not appear in default list:\n%s", done.ID, out)
		}
	})
}
