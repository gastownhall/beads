//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/dolt"
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

// serverModeRigOnSharedServer provisions a server-mode rig workspace whose
// database lives on the shared dolt server the proxied launcher's served
// database is on — the kind of target a prefix route from a proxied launcher
// can open (a proxied-server target is refused, GH#5861). The database is
// bootstrapped in-process and rigIDs are seeded by explicit ID. It returns the
// rig's root directory and a root-connection handle onto its database.
func serverModeRigOnSharedServer(t *testing.T, port int, rigIDs ...string) (string, *sql.DB) {
	t.Helper()

	database := uniqueProxiedDatabase()
	rigDir := filepath.Join(t.TempDir(), "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}
	cfg := &configfile.Config{
		Database:       "dolt",
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltDatabase:   database,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
	}
	if err := cfg.Save(rigBeadsDir); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testStoreOpenTimeout)
	defer cancel()
	doltNewMutex.Lock()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(rigBeadsDir, "dolt"),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        database,
		CreateIfMissing: true,
	})
	doltNewMutex.Unlock()
	if err != nil {
		t.Fatalf("open rig store on the shared server: %v", err)
	}
	// The rig mints its own IDs under the routed prefix; CreateIssue refuses
	// a database whose config table has no issue_prefix.
	if err := store.SetConfig(ctx, "issue_prefix", "mbrr"); err != nil {
		store.Close()
		t.Fatalf("set rig issue_prefix: %v", err)
	}
	for _, id := range rigIDs {
		seedIssue(t, ctx, store, id)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?multiStatements=true&parseTime=true", port, database))
	if err != nil {
		t.Fatalf("open rig database %s: %v", database, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping rig database %s: %v", database, err)
	}
	return rigDir, db
}

// writeProxiedRoute points prefix at rigDir from the launcher workspace.
func writeProxiedRoute(t *testing.T, town proxiedProject, prefix, rigDir string) {
	t.Helper()
	rel, err := filepath.Rel(town.dir, rigDir)
	if err != nil {
		t.Fatalf("relative rig path: %v", err)
	}
	route := `{"prefix":` + strconv.Quote(prefix) + `,"path":` + strconv.Quote(rel) + "}\n"
	if err := os.WriteFile(filepath.Join(town.beadsDir, "routes.jsonl"), []byte(route), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
}

// assertSharedServerCount fails the test unless query returns want rows.
func assertSharedServerCount(t *testing.T, db *sql.DB, ctx context.Context, query string, want int, what string, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("query %s: %v", what, err)
	}
	if got != want {
		t.Errorf("%s: got %d, want %d", what, got, want)
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

	// bd-tqdsvo (GH#5732): the direct-store route resolves a bond operand
	// that lives only in a prefix-routed rig; the proxied-server route must
	// find it too instead of resolving both operands in the served UOW only,
	// and execute the bond in that rig. The rig is a server-mode workspace on
	// the same shared dolt server the launcher's served database is on — what
	// a prefix route from a proxied launcher can open (a proxied-server
	// target is refused, GH#5861).
	t.Run("formula_plus_prefix_routed_issue_resolves", func(t *testing.T) {
		t.Parallel()
		port := requireSharedProxiedServer(t)
		town := newSharedProxiedProject(t, bd, "mbrt")
		writeFormulaFixture(t, town, twoStepFormula("routed-proto", "Routed proto step"))
		rigDir, rigDB := serverModeRigOnSharedServer(t, port, "mbrr-bondmol")
		writeProxiedRoute(t, town, "mbrr-", rigDir)

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, town.dir, "mol", "bond", "routed-proto", "mbrr-bondmol", "--type", "sequential", "--dry-run")
		if err != nil {
			t.Fatalf("bd mol bond <formula> <prefix-routed issue> --dry-run from the town: %v\nstdout:%s\nstderr:%s", err, stdout, stderr)
		}
		for _, want := range []string{"Dry run", "routed-proto", "mbrr-bondmol"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("dry-run preview does not name %q: %s", want, stdout)
			}
		}

		townIssuesBefore := len(bdProxiedListJSON(t, bd, town, "--all"))
		stdout, stderr, err = bdProxiedRunBuffers(t, bd, town.dir, "mol", "bond", "routed-proto", "mbrr-bondmol", "--type", "sequential")
		if err != nil {
			t.Fatalf("bd mol bond <formula> <prefix-routed issue> from the town: %v\nstdout:%s\nstderr:%s", err, stdout, stderr)
		}

		// The spawn and the attachment committed in the rig database: one
		// dependency row onto the rig molecule, and the two spawned issues.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assertSharedServerCount(t, rigDB, ctx,
			"SELECT COUNT(*) FROM dependencies WHERE depends_on_issue_id = ?",
			1, "attachment rows onto mbrr-bondmol in the rig", "mbrr-bondmol")
		assertSharedServerCount(t, rigDB, ctx,
			"SELECT COUNT(*) FROM issues",
			3, "issues in the rig (seeded mol + spawned proto root + step)")

		if got := len(bdProxiedListJSON(t, bd, town, "--all")); got != townIssuesBefore {
			t.Errorf("the launcher's served database changed: %d issues before the bond, %d after", townIssuesBefore, got)
		}
	})

	// bd-tqdsvo (GH#5732), AC-2's proxied leg: two molecules that live in one
	// routed rig bond there from a proxied launcher — B depends on A in the
	// rig database, the served database never sees a write.
	t.Run("mol_plus_mol_prefix_routed_bonds_in_rig", func(t *testing.T) {
		t.Parallel()
		port := requireSharedProxiedServer(t)
		town := newSharedProxiedProject(t, bd, "mbrm")
		rigDir, rigDB := serverModeRigOnSharedServer(t, port, "mbrr-bola", "mbrr-bolb")
		writeProxiedRoute(t, town, "mbrr-", rigDir)

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, town.dir, "mol", "bond", "mbrr-bola", "mbrr-bolb", "--type", "sequential")
		if err != nil {
			t.Fatalf("bd mol bond <prefix-routed mol> <prefix-routed mol> from the town: %v\nstdout:%s\nstderr:%s", err, stdout, stderr)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assertSharedServerCount(t, rigDB, ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			1, "the blocks row mbrr-bolb -> mbrr-bola in the rig", "mbrr-bolb", "mbrr-bola")
		assertSharedServerCount(t, rigDB, ctx,
			"SELECT COUNT(*) FROM issues",
			2, "issues in the rig (a routed mol+mol bond spawns nothing)")
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
