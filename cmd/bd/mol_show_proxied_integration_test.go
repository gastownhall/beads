//go:build cgo

package main

import (
	"encoding/json"
	"testing"
)

func TestProxiedServerMolShow(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("shows_root_children_and_deps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "msh")
		root := bdProxiedCreate(t, bd, p.dir, "Molecule root", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Step one", "--type", "task", "--parent", root.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "show", root.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol show --json: %v\n%s", err, out)
		}
		var got struct {
			Root struct {
				ID string `json:"id"`
			} `json:"root"`
			Issues []struct {
				ID string `json:"id"`
			} `json:"issues"`
			Dependencies []struct {
				IssueID     string `json:"issue_id"`
				DependsOnID string `json:"depends_on_id"`
			} `json:"dependencies"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.Root.ID != root.ID {
			t.Errorf("root.id = %s, want %s", got.Root.ID, root.ID)
		}
		if len(got.Issues) != 2 {
			t.Fatalf("expected 2 issues (root+child), got %d: %+v", len(got.Issues), got.Issues)
		}
		foundDep := false
		for _, d := range got.Dependencies {
			if d.IssueID == child.ID && d.DependsOnID == root.ID {
				foundDep = true
			}
		}
		if !foundDep {
			t.Errorf("expected parent-child dependency %s -> %s, got %+v", child.ID, root.ID, got.Dependencies)
		}
	})

	t.Run("parallel_marks_ready_and_blocked_steps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mshp")
		root := bdProxiedCreate(t, bd, p.dir, "Parallel root", "--type", "epic")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker step", "--type", "task", "--parent", root.ID)
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked step", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", blocked.ID, blocker.ID); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "show", root.ID, "--parallel", "--json")
		if err != nil {
			t.Fatalf("bd mol show --parallel --json: %v\n%s", err, out)
		}
		var got struct {
			Parallel struct {
				Steps map[string]struct {
					IsReady   bool     `json:"is_ready"`
					BlockedBy []string `json:"blocked_by"`
				} `json:"steps"`
			} `json:"parallel"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if !got.Parallel.Steps[blocker.ID].IsReady {
			t.Errorf("blocker %s should be ready: %+v", blocker.ID, got.Parallel.Steps[blocker.ID])
		}
		if got.Parallel.Steps[blocked.ID].IsReady {
			t.Errorf("blocked step %s should not be ready", blocked.ID)
		}
	})

	t.Run("unknown_id_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mshu")
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "show", "bd-doesnotexist")
		if err == nil {
			t.Fatalf("expected error, got stdout:%s stderr:%s", stdout, stderr)
		}
	})
}
