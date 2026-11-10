//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
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

	// AC-5 (bd-lrkpd3): the proxied route orders a sequential --ref arm
	// after the same terminal steps the direct route does
	// (TestBondProtoMolRefArmWaitsForTerminalSteps): S2 and S3, not S1, which
	// S2 waits on, and not the patrol, which would deadlock.
	t.Run("ref_arm_waits_for_terminal_steps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbterm")
		writeFormulaFixture(t, p, twoStepFormula("arm", "Capture {{worker_name}}"))
		patrol, s1, s2, s3 := seedProxiedRefArmParent(t, bd, p)

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "bond", "arm", patrol,
			"--ref", "arm-{{worker_name}}", "--var", "worker_name=ace", "--json")
		if err != nil {
			t.Fatalf("bd mol bond --ref --json: %v\n%s", err, out)
		}
		arm := patrol + ".arm-ace"
		db := openProxiedDB(t, p)
		if got, want := proxiedOutgoingDeps(t, db, arm), wantDeps("blocks:"+s2, "blocks:"+s3); !slices.Equal(got, want) {
			t.Fatalf("arm %s deps = %v, want %v (no edge onto S1 %s or the patrol %s)", arm, got, want, s1, patrol)
		}
	})

	// AC-5's failure leg on the proxied route: bondProtoMolAttachInto runs in
	// the caller's unit of work, so an ordering edge that fails after another
	// was written rolls the whole arm back with it.
	t.Run("ref_arm_terminal_edge_failure_rolls_back", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mbroll")
		patrol, _, _, _ := seedProxiedRefArmParent(t, bd, p)

		ctx := context.Background()
		provider, err := newProxiedServerUOWProvider(ctx, p.beadsDir, "")
		if err != nil {
			t.Fatalf("open proxied UOW provider: %v", err)
		}
		t.Cleanup(func() { _ = provider.Close(context.Background()) })
		sub, err := cookFormulaToSubgraph(twoStepFormula("arm", "Capture {{worker_name}}"), "arm")
		if err != nil {
			t.Fatalf("cook arm: %v", err)
		}
		arm := patrol + ".arm-ace"
		injected := errors.New("injected ordering failure")
		var w *secondOrderingEdgeFails
		_, err = uow.RunTxResult(ctx, provider, func(ctx context.Context, uw uow.UnitOfWork) (*BondResult, string, error) {
			mol, err := newUOWMolWriter(uw).GetIssue(ctx, patrol)
			if err != nil {
				return nil, "", err
			}
			w = &secondOrderingEdgeFails{molWriter: newUOWMolWriter(uw), armID: arm, err: injected}
			r, err := bondProtoMolAttachInto(ctx, w, uow.MoleculeReaderInUOW(uw), sub, sub.Root, mol, types.BondTypeSequential,
				map[string]string{"worker_name": "ace"}, "arm-{{worker_name}}", "test", false, false)
			return r, "bd: mol bond rollback test", err
		})
		if !errors.Is(err, injected) {
			t.Fatalf("bond error = %v, want the injected ordering failure", err)
		}
		if w == nil || w.written != 1 {
			t.Fatalf("ordering edges written before the failure = %v, want 1 (the failure must follow a write)", w)
		}

		db := openProxiedDB(t, p)
		var issues, deps int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ? OR id LIKE ?", arm, arm+".%").Scan(&issues); err != nil {
			t.Fatalf("count arm issues: %v", err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? OR issue_id LIKE ?", arm, arm+".%").Scan(&deps); err != nil {
			t.Fatalf("count arm edges: %v", err)
		}
		if issues != 0 || deps != 0 {
			t.Fatalf("after the failed bond, %d arm issues and %d arm edges persisted, want none", issues, deps)
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

// seedProxiedRefArmParent creates the AC-1 parent graph through the proxied
// bd: a patrol epic with direct steps S1, S2 and S3, S2 blocked by S1.
func seedProxiedRefArmParent(t *testing.T, bd string, p proxiedProject) (patrol, s1, s2, s3 string) {
	t.Helper()
	patrol = bdProxiedCreate(t, bd, p.dir, "Patrol", "--type", "epic").ID
	s1 = bdProxiedCreate(t, bd, p.dir, "S1", "--parent", patrol).ID
	s2 = bdProxiedCreate(t, bd, p.dir, "S2", "--parent", patrol).ID
	s3 = bdProxiedCreate(t, bd, p.dir, "S3", "--parent", patrol).ID
	if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", s2, s1); err != nil {
		t.Fatalf("bd dep add %s %s: %v\n%s", s2, s1, err, out)
	}
	return patrol, s1, s2, s3
}

// proxiedOutgoingDeps answers id's stored edges as sorted "type:target".
func proxiedOutgoingDeps(t *testing.T, db *sql.DB, id string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT type, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) FROM dependencies WHERE issue_id = ?", id)
	if err != nil {
		t.Fatalf("query deps of %s: %v", id, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var typ, target string
		if err := rows.Scan(&typ, &target); err != nil {
			t.Fatalf("scan dep of %s: %v", id, err)
		}
		out = append(out, typ+":"+target)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("dep rows of %s: %v", id, err)
	}
	slices.Sort(out)
	return out
}

// secondOrderingEdgeFails passes the first ordering edge from armID through
// and fails the second with err, so a test sees a failure after a write.
type secondOrderingEdgeFails struct {
	molWriter
	armID   string
	err     error
	written int
}

func (w *secondOrderingEdgeFails) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if dep.IssueID == w.armID && dep.Type == types.DepBlocks {
		if w.written > 0 {
			return w.err
		}
		w.written++
	}
	return w.molWriter.AddDependency(ctx, dep, actor)
}
