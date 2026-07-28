//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerMolProgress(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("explicit_id_reports_totals", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mpg")
		root := bdProxiedCreate(t, bd, p.dir, "Progress root", "--type", "epic")
		step1 := bdProxiedCreate(t, bd, p.dir, "Step 1", "--type", "task", "--parent", root.ID)
		bdProxiedCreate(t, bd, p.dir, "Step 2", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "close", step1.ID); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "progress", root.ID, "--json")
		if err != nil {
			t.Fatalf("bd mol progress --json: %v\n%s", err, out)
		}
		var got struct {
			MoleculeID string `json:"molecule_id"`
			Total      int    `json:"total"`
			Completed  int    `json:"completed"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.MoleculeID != root.ID {
			t.Errorf("molecule_id = %s, want %s", got.MoleculeID, root.ID)
		}
		if got.Total != 2 || got.Completed != 1 {
			t.Errorf("total/completed = %d/%d, want 2/1", got.Total, got.Completed)
		}
	})

	t.Run("infers_current_from_in_progress", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mpgi")
		root := bdProxiedCreate(t, bd, p.dir, "Inferred progress root", "--type", "epic")
		step := bdProxiedCreate(t, bd, p.dir, "In progress step", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "update", step.ID, "--claim"); err != nil {
			t.Fatalf("bd update --claim: %v\n%s", err, out)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "progress", "--json")
		if err != nil {
			t.Fatalf("bd mol progress --json: %v\n%s", err, out)
		}
		var got struct {
			MoleculeID string `json:"molecule_id"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.MoleculeID != root.ID {
			t.Errorf("molecule_id = %s, want %s", got.MoleculeID, root.ID)
		}
	})

	t.Run("no_molecules_in_progress_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mpgn")
		out, err := bdProxiedRun(t, bd, p.dir, "mol", "progress", "--json")
		if err != nil {
			t.Fatalf("bd mol progress --json: %v\n%s", err, out)
		}
		if strings.TrimSpace(string(out)) != "[]" {
			t.Errorf("expected empty JSON array, got: %s", out)
		}
	})
}
