//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

func TestProxiedServerMolCurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("explicit_id_shows_step_statuses", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mcu")
		root := bdProxiedCreate(t, bd, p.dir, "Current root", "--type", "epic")
		done := bdProxiedCreate(t, bd, p.dir, "Done step", "--type", "task", "--parent", root.ID)
		current := bdProxiedCreate(t, bd, p.dir, "Current step", "--type", "task", "--parent", root.ID)
		bdProxiedCreate(t, bd, p.dir, "Ready step", "--type", "task", "--parent", root.ID)

		if out, err := bdProxiedRun(t, bd, p.dir, "close", done.ID); err != nil {
			t.Fatalf("bd close %s: %v\n%s", done.ID, err, out)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "update", current.ID, "--status", "in_progress"); err != nil {
			t.Fatalf("bd update %s: %v\n%s", current.ID, err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "current", root.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol current --json: %v\n%s", err, out)
		}
		var got []struct {
			MoleculeID string `json:"molecule_id"`
			Completed  int    `json:"completed"`
			Total      int    `json:"total"`
			Steps      []struct {
				Status string `json:"status"`
				Issue  struct {
					ID string `json:"id"`
				} `json:"issue"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 molecule, got %d", len(got))
		}
		if got[0].Completed != 1 || got[0].Total != 3 {
			t.Errorf("Completed/Total = %d/%d, want 1/3", got[0].Completed, got[0].Total)
		}
		statusByID := map[string]string{}
		for _, s := range got[0].Steps {
			statusByID[s.Issue.ID] = s.Status
		}
		if statusByID[done.ID] != "done" {
			t.Errorf("done step status = %s, want done", statusByID[done.ID])
		}
		if statusByID[current.ID] != "current" {
			t.Errorf("current step status = %s, want current", statusByID[current.ID])
		}
	})

	t.Run("infers_from_in_progress_assignee", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mcia")
		root := bdProxiedCreate(t, bd, p.dir, "Inferred root", "--type", "epic")
		step := bdProxiedCreate(t, bd, p.dir, "Inferred step", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "update", step.ID, "--status", "in_progress", "--assignee", "tester"); err != nil {
			t.Fatalf("bd update: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "current", "--for", "tester", "--json")
		if err != nil {
			t.Fatalf("bd mol current --for tester --json: %v\n%s", err, out)
		}
		var got []struct {
			MoleculeID string `json:"molecule_id"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(got) != 1 || got[0].MoleculeID != root.ID {
			t.Fatalf("expected inferred molecule %s, got %+v", root.ID, got)
		}
	})

	t.Run("no_molecules_in_progress_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mcne")
		out, err := bdProxiedRun(t, bd, p.dir, "mol", "current", "--for", "nobody", "--json")
		if err != nil {
			t.Fatalf("bd mol current --json: %v\n%s", err, out)
		}
		if strings.TrimSpace(string(out)) != "[]" {
			t.Errorf("expected empty JSON array, got: %s", out)
		}
	})

	t.Run("limit_and_range_truncate_steps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mclr")
		root := bdProxiedCreate(t, bd, p.dir, "Range root", "--type", "epic")
		for i := 0; i < 5; i++ {
			bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Step %d", i), "--type", "task", "--parent", root.ID)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "current", root.ID, "--limit", "2", "--json")
		if err != nil {
			t.Fatalf("bd mol current --limit 2 --json: %v\n%s", err, out)
		}
		var got []struct {
			Steps []struct{} `json:"steps"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(got) != 1 || len(got[0].Steps) != 2 {
			t.Fatalf("expected 2 steps with --limit 2, got %+v", got)
		}
	})

	t.Run("large_molecule_shows_summary", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mclg")

		steps := make([]*formula.Step, 0, 101)
		for i := 0; i < 101; i++ {
			steps = append(steps, &formula.Step{
				ID:    fmt.Sprintf("step%d", i),
				Title: fmt.Sprintf("Step %d", i),
				Type:  "task",
			})
		}
		f := &formula.Formula{
			Formula: "big",
			Version: 1,
			Type:    formula.TypeWorkflow,
			Steps:   steps,
		}
		writeFormulaFixture(t, p, f)

		pourOut, err := bdProxiedRun(t, bd, p.dir, "mol", "pour", "big", "--json")
		if err != nil {
			t.Fatalf("bd mol pour big --json: %v\n%s", err, pourOut)
		}
		var pourResult struct {
			NewEpicID string `json:"new_epic_id"`
			Created   int    `json:"created"`
		}
		if err := json.Unmarshal(pourOut, &pourResult); err != nil {
			t.Fatalf("unmarshal pour result: %v\n%s", err, pourOut)
		}
		if pourResult.Created != 102 {
			t.Fatalf("expected 102 issues created (root+101 steps), got %d", pourResult.Created)
		}

		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "current", pourResult.NewEpicID)
		if err != nil {
			t.Fatalf("bd mol current: %v\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "has 101 steps") {
			t.Errorf("expected large-molecule summary message, got: %s", stdout)
		}

		stdoutFull, _, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "current", pourResult.NewEpicID, "--limit", "50")
		if err != nil {
			t.Fatalf("bd mol current --limit 50: %v\n%s", err, stdoutFull)
		}
		if strings.Contains(stdoutFull, "has 101 steps") {
			t.Errorf("--limit 50 should print full step list, not the summary: %s", stdoutFull)
		}
	})
}
