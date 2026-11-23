package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// dryrunProbeProvider hands the proxied planner one empty read unit of work
// per operand: the served database answers "no such issue", so discovery falls
// through to the formula registry, which is the only branch these cases
// exercise. The UOW embeds the interface without implementing it, so anything
// the planner's read phase needs beyond the use cases below panics and names
// itself, rather than being silently satisfied.
type dryrunProbeProvider struct{}

func (dryrunProbeProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return dryrunProbeUOW{}, nil
}
func (dryrunProbeProvider) Close(context.Context) error { return nil }

type dryrunProbeUOW struct {
	uow.UnitOfWork
}

func (dryrunProbeUOW) Close(context.Context)                       {}
func (dryrunProbeUOW) ConfigUseCase() domain.ConfigUseCase         { return dryrunProbeConfig{} }
func (dryrunProbeUOW) IssueUseCase() domain.IssueUseCase           { return dryrunProbeIssues{} }
func (dryrunProbeUOW) LabelUseCase() domain.LabelUseCase           { return exemptionProbeLabels{} }
func (dryrunProbeUOW) DependencyUseCase() domain.DependencyUseCase { return exemptionProbeDeps{} }
func (dryrunProbeUOW) CommentUseCase() domain.CommentUseCase       { return nil }

type dryrunProbeIssues struct {
	domain.IssueUseCase
}

func (dryrunProbeIssues) SearchIssues(context.Context, string, types.IssueFilter) (domain.SearchPage, error) {
	return domain.SearchPage{}, nil
}

func (dryrunProbeIssues) SearchIssueIDs(context.Context, string, types.IssueFilter) ([]string, error) {
	return nil, nil
}

type dryrunProbeConfig struct {
	domain.ConfigUseCase
}

func (dryrunProbeConfig) GetConfig(context.Context, string) (string, error) { return "", nil }

// installDryrunProbe points the package's proxied provider at the empty
// probe for the duration of one test, with no workspace dbPath so prefix
// routing is unavailable and discovery keeps the served miss as its
// issue-side error.
func installDryrunProbe(t *testing.T) {
	t.Helper()
	prevProvider, prevDbPath := uowProvider, dbPath
	uowProvider = dryrunProbeProvider{}
	dbPath = ""
	t.Cleanup(func() { uowProvider, dbPath = prevProvider, prevDbPath })
}

// writeSearchableFormula puts a formula where formula.DefaultSearchPaths will
// find it, via the GT_ROOT arm - the one search path a test can set without
// touching HOME or the working directory.
func writeSearchableFormula(t *testing.T, name, body string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".formula.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	t.Setenv("GT_ROOT", root)
}

// A waits_for gate with no needs: nothing to infer a spawner from, so the
// cooked gate would carry its gate:<value> label and no dependency edge.
const invalidGateFormula = `formula = "dryrun-invalid-gate"
version = 1
type = "workflow"

[[steps]]
id = "fanout"
title = "Fan out"
type = "task"

[[steps]]
id = "gate"
title = "Wait for the children"
type = "task"
waits_for = "all-children"
`

// A `bd mol bond --dry-run` must fail on exactly what the real bond fails on.
//
// The planning tail (discoverMolBondFormula, shared by the direct and the
// proxied planners) resolves a formula with parser.Resolve before validating
// --var values, exactly like the real cook (resolveAndCookFormulaWithVars,
// reached through materializeServedMolBondOperand on the served route). A
// planning tail built on parser.LoadByName alone never calls Formula.Validate
// and previewed bonds the real bond rejects, including the
// waits_for-without-a-spawner rule.
//
// The two routes are asserted against EACH OTHER rather than against a literal
// message: that is what stops a validation rule added later from re-opening the
// same gap.
func TestBondDryRunRejectsWhatTheRealBondRejects(t *testing.T) {
	writeSearchableFormula(t, "dryrun-invalid-gate", invalidGateFormula)
	installDryrunProbe(t)

	// The control, and the reason the bug existed: LoadByName alone accepts
	// this formula. If this ever starts failing, the two assertions below stop
	// distinguishing the routes and this test is no longer about anything.
	if _, err := formula.NewParser().LoadByName("dryrun-invalid-gate"); err != nil {
		t.Fatalf("LoadByName should still accept an invalid formula (it does not validate): %v", err)
	}

	ctx := context.Background()

	_, dryErr := discoverMolBondOperandProxied(ctx, "dryrun-invalid-gate", nil)
	_, realErr := materializeServedMolBondOperand(ctx, nil, &molBondDiscovery{operand: "dryrun-invalid-gate"}, nil)

	if realErr == nil {
		t.Fatal("the real bond accepted an invalid formula, so this test is no longer testing the gap")
	}
	if dryErr == nil {
		t.Fatalf("dry-run previewed a bond the real bond rejects (real bond said: %v)", realErr)
	}

	// Both routes must name the actual problem, not "not found as issue or
	// formula" - the formula resolved fine, it just does not validate.
	for name, err := range map[string]error{"dry-run": dryErr, "real bond": realErr} {
		if !strings.Contains(err.Error(), "waits_for") {
			t.Errorf("%s error does not name the failing field: %v", name, err)
		}
		if strings.Contains(err.Error(), "not found as issue or formula") {
			t.Errorf("%s reported an invalid formula as not found: %v", name, err)
		}
	}
}

// Resolving before validating --var values also closes a second-order gap: the
// real path validates against the MERGED formula, so a var declared only in a
// parent was unchecked by a dry-run that validated the unmerged one.
func TestBondDryRunValidatesVarsAgainstTheMergedFormula(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+".formula.toml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("dryrun-parent", `formula = "dryrun-parent"
version = 1
type = "workflow"

[vars.env]
enum = ["dev", "prod"]

[[steps]]
id = "deploy"
title = "Deploy to {{env}}"
type = "task"
`)
	write("dryrun-child", `formula = "dryrun-child"
version = 1
type = "workflow"
extends = ["dryrun-parent"]
`)
	t.Setenv("GT_ROOT", root)
	installDryrunProbe(t)

	ctx := context.Background()
	vars := map[string]string{"env": "staging"} // not in the parent's enum

	_, dryErr := discoverMolBondOperandProxied(ctx, "dryrun-child", vars)
	_, realErr := materializeServedMolBondOperand(ctx, nil, &molBondDiscovery{operand: "dryrun-child"}, vars)

	if realErr == nil {
		t.Fatal("the real bond accepted a var value outside the inherited enum")
	}
	if dryErr == nil {
		t.Fatalf("dry-run accepted a var value the real bond rejects; the enum is declared only in the parent (real bond said: %v)", realErr)
	}
}
