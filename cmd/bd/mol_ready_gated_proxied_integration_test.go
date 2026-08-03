//go:build cgo

package main

import (
	"encoding/json"
	"testing"
)

func TestProxiedServerMolReadyGated(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	setupGateType := func(t *testing.T, bd, dir string) {
		t.Helper()
		if out, err := bdProxiedRun(t, bd, dir, "config", "set", "types.custom", "gate"); err != nil {
			t.Fatalf("bd config set types.custom gate: %v\n%s", err, out)
		}
	}

	molReadyGated := func(t *testing.T, bd, dir string) []struct {
		MoleculeID string `json:"molecule_id"`
	} {
		t.Helper()
		out, err := bdProxiedRun(t, bd, dir, "mol", "ready", "--gated", "--json")
		if err != nil {
			t.Fatalf("bd mol ready --gated --json: %v\n%s", err, out)
		}
		var got struct {
			Molecules []struct {
				MoleculeID string `json:"molecule_id"`
			} `json:"molecules"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return got.Molecules
	}

	t.Run("no_gates_returns_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mrgn")
		root := bdProxiedCreate(t, bd, p.dir, "No gates root", "--type", "epic")
		bdProxiedCreate(t, bd, p.dir, "Plain step", "--type", "task", "--parent", root.ID)

		molecules := molReadyGated(t, bd, p.dir)
		if len(molecules) != 0 {
			t.Errorf("expected 0 gate-ready molecules, got %d: %+v", len(molecules), molecules)
		}
	})

	t.Run("closed_gate_makes_molecule_ready", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mrgc")
		setupGateType(t, bd, p.dir)

		root := bdProxiedCreate(t, bd, p.dir, "Gated root", "--type", "epic")
		gate := bdProxiedCreate(t, bd, p.dir, "Gate: ci", "--type", "gate", "--parent", root.ID)
		step := bdProxiedCreate(t, bd, p.dir, "Deploy after CI", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", step.ID, gate.ID); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "close", gate.ID); err != nil {
			t.Fatalf("bd close gate: %v\n%s", err, out)
		}

		molecules := molReadyGated(t, bd, p.dir)
		if len(molecules) != 1 || molecules[0].MoleculeID != root.ID {
			t.Fatalf("expected 1 gate-ready molecule %s, got %+v", root.ID, molecules)
		}
	})

	t.Run("open_gate_not_ready", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mrgo")
		setupGateType(t, bd, p.dir)

		root := bdProxiedCreate(t, bd, p.dir, "Open gate root", "--type", "epic")
		gate := bdProxiedCreate(t, bd, p.dir, "Gate: pending", "--type", "gate", "--parent", root.ID)
		step := bdProxiedCreate(t, bd, p.dir, "Blocked step", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", step.ID, gate.ID); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}

		molecules := molReadyGated(t, bd, p.dir)
		if len(molecules) != 0 {
			t.Errorf("expected 0 gate-ready molecules with an open gate, got %d: %+v", len(molecules), molecules)
		}
	})

	t.Run("hooked_molecule_excluded", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mrgh")
		setupGateType(t, bd, p.dir)

		root := bdProxiedCreate(t, bd, p.dir, "Hooked root", "--type", "epic")
		gate := bdProxiedCreate(t, bd, p.dir, "Gate: hooked", "--type", "gate", "--parent", root.ID)
		step := bdProxiedCreate(t, bd, p.dir, "Ready step under hooked mol", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", step.ID, gate.ID); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "close", gate.ID); err != nil {
			t.Fatalf("bd close gate: %v\n%s", err, out)
		}

		db := openProxiedDB(t, p)
		if _, err := db.Exec("UPDATE issues SET status = 'hooked' WHERE id = ?", root.ID); err != nil {
			t.Fatalf("mark root hooked: %v", err)
		}

		molecules := molReadyGated(t, bd, p.dir)
		for _, m := range molecules {
			if m.MoleculeID == root.ID {
				t.Errorf("hooked molecule %s should be excluded from gate-ready results", root.ID)
			}
		}
	})

	t.Run("multiple_gates_multiple_molecules", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mrgm")
		setupGateType(t, bd, p.dir)

		var roots []string
		for i := 0; i < 3; i++ {
			root := bdProxiedCreate(t, bd, p.dir, "Multi gate root", "--type", "epic")
			gate := bdProxiedCreate(t, bd, p.dir, "Gate", "--type", "gate", "--parent", root.ID)
			step := bdProxiedCreate(t, bd, p.dir, "Step", "--type", "task", "--parent", root.ID)
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", step.ID, gate.ID); err != nil {
				t.Fatalf("bd dep add: %v\n%s", err, out)
			}
			if out, err := bdProxiedRun(t, bd, p.dir, "close", gate.ID); err != nil {
				t.Fatalf("bd close gate: %v\n%s", err, out)
			}
			roots = append(roots, root.ID)
		}

		molecules := molReadyGated(t, bd, p.dir)
		if len(molecules) != 3 {
			t.Fatalf("expected 3 gate-ready molecules, got %d: %+v", len(molecules), molecules)
		}
		got := map[string]bool{}
		for _, m := range molecules {
			got[m.MoleculeID] = true
		}
		for _, r := range roots {
			if !got[r] {
				t.Errorf("expected root %s in gate-ready results, got %+v", r, molecules)
			}
		}
	})

	t.Run("bd_ready_gated_matches_bd_mol_ready_gated", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mrgeq")
		setupGateType(t, bd, p.dir)

		root := bdProxiedCreate(t, bd, p.dir, "Equivalence root", "--type", "epic")
		gate := bdProxiedCreate(t, bd, p.dir, "Gate: eq", "--type", "gate", "--parent", root.ID)
		step := bdProxiedCreate(t, bd, p.dir, "Eq step", "--type", "task", "--parent", root.ID)
		if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", step.ID, gate.ID); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "close", gate.ID); err != nil {
			t.Fatalf("bd close gate: %v\n%s", err, out)
		}

		viaMol := molReadyGated(t, bd, p.dir)

		out, err := bdProxiedRun(t, bd, p.dir, "ready", "--gated", "--json")
		if err != nil {
			t.Fatalf("bd ready --gated --json: %v\n%s", err, out)
		}
		var viaReady struct {
			Molecules []struct {
				MoleculeID string `json:"molecule_id"`
			} `json:"molecules"`
		}
		if err := json.Unmarshal(out, &viaReady); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}

		if len(viaMol) != len(viaReady.Molecules) {
			t.Fatalf("bd mol ready --gated found %d molecules, bd ready --gated found %d", len(viaMol), len(viaReady.Molecules))
		}
		if len(viaMol) != 1 || viaMol[0].MoleculeID != root.ID {
			t.Fatalf("expected 1 gate-ready molecule %s, got %+v", root.ID, viaMol)
		}
	})
}
