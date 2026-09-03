//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

func twoStepFormula(name, stepTitle string) *formula.Formula {
	return &formula.Formula{
		Formula: name,
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps: []*formula.Step{
			{ID: "step1", Title: stepTitle, Type: "task"},
		},
	}
}

func TestProxiedServerMolBond(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("label_only_protos_bond_as_compound_molecule_not_proto", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbpp")
		protoA := bdProxiedCreate(t, bd, p.dir, "Proto A", "--type", "epic", "--label", "template")
		protoB := bdProxiedCreate(t, bd, p.dir, "Proto B", "--type", "epic", "--label", "template")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "bond", protoA.ID, protoB.ID, "--type", "sequential", "--json")
		if err != nil {
			t.Fatalf("bd mol bond --json: %v\n%s", err, out)
		}
		var got struct {
			ResultID   string `json:"result_id"`
			ResultType string `json:"result_type"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.ResultType != "compound_molecule" {
			t.Errorf("result_type = %s, want compound_molecule (parity guard: bond dispatch does not detect label-only DB issues as protos; see isProtoIssue in wisp.go)", got.ResultType)
		}
		if got.ResultID != protoA.ID {
			t.Errorf("result_id = %s, want %s (mol+mol bonds attach B to A)", got.ResultID, protoA.ID)
		}

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, protoB.ID, protoA.ID, "blocks")
	})

	t.Run("formula_plus_formula_currently_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbppf")
		writeFormulaFixture(t, p, twoStepFormula("proto-a", "Step of proto A"))
		writeFormulaFixture(t, p, twoStepFormula("proto-b", "Step of proto B"))

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "bond", "proto-a", "proto-b", "--type", "sequential")
		if err == nil {
			t.Fatalf("expected bonding two cooked-but-unpersisted formulas to error (pre-existing limitation); got success: %s", stdout)
		}
		if !strings.Contains(stdout+stderr, "proto-a") {
			t.Errorf("expected error to reference proto-a, got stdout:%s stderr:%s", stdout, stderr)
		}
	})

	t.Run("proto_plus_mol_spawns_and_attaches", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbpm")
		writeFormulaFixture(t, p, twoStepFormula("diagnostic", "Diagnostic step"))
		mol := bdProxiedCreate(t, bd, p.dir, "Live molecule", "--type", "epic")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "bond", "diagnostic", mol.ID, "--type", "sequential", "--json")
		if err != nil {
			t.Fatalf("bd mol bond --json: %v\n%s", err, out)
		}
		var got struct {
			ResultID   string `json:"result_id"`
			ResultType string `json:"result_type"`
			Spawned    int    `json:"spawned"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.ResultType != "compound_molecule" || got.ResultID != mol.ID {
			t.Fatalf("expected compound_molecule attached to %s, got %+v", mol.ID, got)
		}
		if got.Spawned != 2 {
			t.Fatalf("spawned = %d, want 2 (proto root + step)", got.Spawned)
		}

		show := bdProxiedShow(t, bd, p.dir, mol.ID)
		if show.ID != mol.ID {
			t.Fatalf("expected molecule %s to still exist after bond", mol.ID)
		}
	})

	t.Run("mol_plus_mol_adds_dependency_and_rejects_cycle", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbmm")
		molA := bdProxiedCreate(t, bd, p.dir, "Mol A", "--type", "epic")
		molB := bdProxiedCreate(t, bd, p.dir, "Mol B", "--type", "epic")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "bond", molA.ID, molB.ID, "--type", "sequential", "--json")
		if err != nil {
			t.Fatalf("bd mol bond --json: %v\n%s", err, out)
		}
		var got struct {
			ResultType string `json:"result_type"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.ResultType != "compound_molecule" {
			t.Fatalf("result_type = %s, want compound_molecule", got.ResultType)
		}

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, molB.ID, molA.ID, "blocks")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "bond", molB.ID, molA.ID, "--type", "sequential")
		if err == nil {
			t.Fatalf("expected cycle rejection, got success: %s", stdout)
		}
		if !strings.Contains(stdout+stderr, "cycle") {
			t.Errorf("expected 'cycle' in error, got stdout:%s stderr:%s", stdout, stderr)
		}
	})

	t.Run("ref_dynamic_bonding_generates_readable_ids", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbref")
		writeFormulaFixture(t, p, twoStepFormula("arm", "Capture {{worker_name}}"))
		patrol := bdProxiedCreate(t, bd, p.dir, "Patrol", "--type", "epic")

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "bond", "arm", patrol.ID,
			"--ref", "arm-{{worker_name}}", "--var", "worker_name=ace", "--json")
		if err != nil {
			t.Fatalf("bd mol bond --ref --json: %v\n%s", err, out)
		}
		var got struct {
			IDMapping map[string]string `json:"id_mapping"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		expectedRootID := patrol.ID + ".arm-ace"
		found := false
		for _, v := range got.IDMapping {
			if v == expectedRootID {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected id_mapping to contain root ID %q, got %+v", expectedRootID, got.IDMapping)
		}

		substituted := false
		for _, newID := range got.IDMapping {
			if newID == expectedRootID {
				continue
			}
			issue := bdProxiedShow(t, bd, p.dir, newID)
			if strings.Contains(issue.Title, "ace") {
				substituted = true
			}
		}
		if !substituted {
			t.Errorf("expected a spawned step title to contain 'ace' after {{worker_name}} substitution, id_mapping: %+v", got.IDMapping)
		}
	})

	t.Run("dry_run_creates_nothing", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbdr")
		writeFormulaFixture(t, p, twoStepFormula("dry-proto-a", "Dry proto A step"))
		writeFormulaFixture(t, p, twoStepFormula("dry-proto-b", "Dry proto B step"))

		before := bdProxiedListJSON(t, bd, p, "--all")
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "bond", "dry-proto-a", "dry-proto-b", "--dry-run")
		if err != nil {
			t.Fatalf("bd mol bond --dry-run: %v\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "Dry run") {
			t.Errorf("expected dry-run preview, got: %s", stdout)
		}
		after := bdProxiedListJSON(t, bd, p, "--all")
		if len(after) != len(before) {
			t.Errorf("expected no new issues from --dry-run: before=%d after=%d", len(before), len(after))
		}
	})
}
