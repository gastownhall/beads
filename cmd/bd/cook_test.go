package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// Cook Tests (gt-8tmz.23: Compile-time vs Runtime Cooking)
// =============================================================================

func TestRunCookRejectsInvalidEnumVariable(t *testing.T) {
	formulaDir := t.TempDir()
	formulaPath := filepath.Join(formulaDir, "enum-validation.formula.toml")
	formulaTOML := `formula = "enum-validation"
version = 1
type = "workflow"

[vars.policy]
required = true
enum = ["merge-completes", "tracking-only"]

[[steps]]
id = "publish"
title = "Publish with {{policy}}"
`
	if err := os.WriteFile(formulaPath, []byte(formulaTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		formulaArg  string
		searchPaths []string
	}{
		{
			name:       "exact path",
			formulaArg: formulaPath,
		},
		{
			name:        "registry name",
			formulaArg:  "enum-validation",
			searchPaths: []string{formulaDir},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newCookValidationTestCommand(tt.searchPaths, "policy=merge-comtes")
			stdout, stderr, err := runCookCapturingOutput(t, cmd, tt.formulaArg)
			if err == nil {
				t.Fatal("runCook accepted a value outside the declared enum")
			}
			if stdout != "" {
				t.Fatalf("runCook stdout = %q, want no dry-run output after validation failure", stdout)
			}
			if !strings.Contains(stderr, `variable "policy": value "merge-comtes" not in allowed values [merge-completes tracking-only]`) {
				t.Fatalf("runCook stderr = %q", stderr)
			}

			cmd = newCookValidationTestCommand(tt.searchPaths, "policy=merge-completes")
			stdout, stderr, err = runCookCapturingOutput(t, cmd, tt.formulaArg)
			if err != nil {
				t.Fatalf("runCook rejected a declared enum value: %v; stderr = %q", err, stderr)
			}
			if stderr != "" {
				t.Fatalf("runCook stderr = %q, want no error output for a declared enum value", stderr)
			}
			if !strings.Contains(stdout, "Dry run: would cook formula enum-validation") {
				t.Fatalf("runCook stdout = %q, want the captured dry-run preview", stdout)
			}
		})
	}
}

func runCookCapturingOutput(t *testing.T, cmd *cobra.Command, formulaArg string) (string, string, error) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer stdoutReader.Close()

	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		t.Fatalf("create stderr pipe: %v", err)
	}
	defer stderrReader.Close()

	type captureResult struct {
		output string
		err    error
	}
	drain := func(reader *os.File) <-chan captureResult {
		done := make(chan captureResult, 1)
		go func() {
			output, readErr := io.ReadAll(reader)
			done <- captureResult{output: string(output), err: readErr}
		}()
		return done
	}
	stdoutDone := drain(stdoutReader)
	stderrDone := drain(stderrReader)

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter

	runErr := func() error {
		defer func() {
			os.Stdout = oldStdout
			os.Stderr = oldStderr
			_ = stdoutWriter.Close()
			_ = stderrWriter.Close()
		}()
		return runCook(cmd, []string{formulaArg})
	}()

	stdout := <-stdoutDone
	stderr := <-stderrDone
	if stdout.err != nil {
		t.Fatalf("read stdout: %v", stdout.err)
	}
	if stderr.err != nil {
		t.Fatalf("read stderr: %v", stderr.err)
	}

	return stdout.output, stderr.output, runErr
}

func newCookValidationTestCommand(searchPaths []string, variable string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", true, "")
	cmd.Flags().Bool("persist", false, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().StringSlice("search-path", searchPaths, "")
	cmd.Flags().String("prefix", "", "")
	cmd.Flags().StringArray("var", []string{variable}, "")
	cmd.Flags().String("mode", "", "")
	return cmd
}

// TestSubstituteFormulaVars tests variable substitution in formulas
func TestSubstituteFormulaVars(t *testing.T) {
	tests := []struct {
		name          string
		formula       *formula.Formula
		vars          map[string]string
		wantDesc      string
		wantStepTitle string
	}{
		{
			name: "substitute single variable in description",
			formula: &formula.Formula{
				Description: "Build {{feature}} feature",
				Steps:       []*formula.Step{},
			},
			vars:     map[string]string{"feature": "auth"},
			wantDesc: "Build auth feature",
		},
		{
			name: "substitute variable in step title",
			formula: &formula.Formula{
				Description: "Feature work",
				Steps: []*formula.Step{
					{Title: "Implement {{name}}"},
				},
			},
			vars:          map[string]string{"name": "login"},
			wantDesc:      "Feature work",
			wantStepTitle: "Implement login",
		},
		{
			name: "substitute multiple variables",
			formula: &formula.Formula{
				Description: "Release {{version}} on {{date}}",
				Steps: []*formula.Step{
					{Title: "Tag {{version}}"},
					{Title: "Deploy to {{env}}"},
				},
			},
			vars: map[string]string{
				"version": "1.0.0",
				"date":    "2024-01-15",
				"env":     "production",
			},
			wantDesc:      "Release 1.0.0 on 2024-01-15",
			wantStepTitle: "Tag 1.0.0",
		},
		{
			name: "nested children substitution",
			formula: &formula.Formula{
				Description: "Epic for {{project}}",
				Steps: []*formula.Step{
					{
						Title: "Phase 1: {{project}} design",
						Children: []*formula.Step{
							{Title: "Design {{component}}"},
						},
					},
				},
			},
			vars: map[string]string{
				"project":   "checkout",
				"component": "cart",
			},
			wantDesc:      "Epic for checkout",
			wantStepTitle: "Phase 1: checkout design",
		},
		{
			name: "unsubstituted variable left as-is",
			formula: &formula.Formula{
				Description: "Build {{feature}} with {{extra}}",
				Steps:       []*formula.Step{},
			},
			vars:     map[string]string{"feature": "auth"},
			wantDesc: "Build auth with {{extra}}", // {{extra}} unchanged
		},
		{
			name: "empty vars map",
			formula: &formula.Formula{
				Description: "Keep {{placeholder}} intact",
				Steps:       []*formula.Step{},
			},
			vars:     map[string]string{},
			wantDesc: "Keep {{placeholder}} intact",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			substituteFormulaVars(tt.formula, tt.vars)

			if tt.formula.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", tt.formula.Description, tt.wantDesc)
			}

			if tt.wantStepTitle != "" && len(tt.formula.Steps) > 0 {
				if tt.formula.Steps[0].Title != tt.wantStepTitle {
					t.Errorf("Steps[0].Title = %q, want %q", tt.formula.Steps[0].Title, tt.wantStepTitle)
				}
			}
		})
	}
}

func TestSubstituteFormulaVars_GateFields(t *testing.T) {
	f := &formula.Formula{
		Steps: []*formula.Step{
			{
				ID: "wait-for-pr",
				Gate: &formula.Gate{
					Type:    "gh:{{kind}}",
					ID:      "{{legacy_id}}",
					AwaitID: "{{pr}}",
					Timeout: "{{timeout}}",
					Repo:    "{{repo}}",
				},
			},
		},
	}

	substituteFormulaVars(f, map[string]string{
		"kind":      "pr",
		"legacy_id": "legacy-42",
		"pr":        "https://github.com/org/repo/pull/123",
		"timeout":   "1h",
		"repo":      "srobroek/agentic-packages",
	})

	gate := f.Steps[0].Gate
	if gate.Type != "gh:pr" {
		t.Errorf("Gate.Type = %q, want gh:pr", gate.Type)
	}
	if gate.ID != "legacy-42" {
		t.Errorf("Gate.ID = %q, want legacy-42", gate.ID)
	}
	if gate.AwaitID != "https://github.com/org/repo/pull/123" {
		t.Errorf("Gate.AwaitID = %q, want expanded PR URL", gate.AwaitID)
	}
	if gate.Timeout != "1h" {
		t.Errorf("Gate.Timeout = %q, want 1h", gate.Timeout)
	}
	if gate.Repo != "srobroek/agentic-packages" {
		t.Errorf("Gate.Repo = %q, want srobroek/agentic-packages", gate.Repo)
	}
}

// TestSubstituteStepVarsRecursive tests deep nesting works correctly
func TestSubstituteStepVarsRecursive(t *testing.T) {
	steps := []*formula.Step{
		{
			Title: "Root: {{name}}",
			Children: []*formula.Step{
				{
					Title: "Level 1: {{name}}",
					Children: []*formula.Step{
						{
							Title: "Level 2: {{name}}",
							Children: []*formula.Step{
								{Title: "Level 3: {{name}}"},
							},
						},
					},
				},
			},
		},
	}

	vars := map[string]string{"name": "test"}
	substituteStepVars(steps, vars)

	// Check all levels got substituted
	if steps[0].Title != "Root: test" {
		t.Errorf("Root title = %q, want %q", steps[0].Title, "Root: test")
	}
	if steps[0].Children[0].Title != "Level 1: test" {
		t.Errorf("Level 1 title = %q, want %q", steps[0].Children[0].Title, "Level 1: test")
	}
	if steps[0].Children[0].Children[0].Title != "Level 2: test" {
		t.Errorf("Level 2 title = %q, want %q", steps[0].Children[0].Children[0].Title, "Level 2: test")
	}
	if steps[0].Children[0].Children[0].Children[0].Title != "Level 3: test" {
		t.Errorf("Level 3 title = %q, want %q", steps[0].Children[0].Children[0].Children[0].Title, "Level 3: test")
	}
}

// TestCompileTimeVsRuntimeMode tests that compile-time preserves placeholders
// and runtime mode substitutes them
func TestCompileTimeVsRuntimeMode(t *testing.T) {
	// Simulate compile-time mode (no variable substitution)
	compileFormula := &formula.Formula{
		Description: "Feature: {{name}}",
		Steps: []*formula.Step{
			{Title: "Implement {{name}}"},
		},
	}

	// In compile-time mode, don't call substituteFormulaVars
	// Placeholders should remain intact
	if compileFormula.Description != "Feature: {{name}}" {
		t.Errorf("Compile-time: Description should preserve placeholder, got %q", compileFormula.Description)
	}

	// Simulate runtime mode (with variable substitution)
	runtimeFormula := &formula.Formula{
		Description: "Feature: {{name}}",
		Steps: []*formula.Step{
			{Title: "Implement {{name}}"},
		},
	}
	vars := map[string]string{"name": "auth"}
	substituteFormulaVars(runtimeFormula, vars)

	if runtimeFormula.Description != "Feature: auth" {
		t.Errorf("Runtime: Description = %q, want %q", runtimeFormula.Description, "Feature: auth")
	}
	if runtimeFormula.Steps[0].Title != "Implement auth" {
		t.Errorf("Runtime: Steps[0].Title = %q, want %q", runtimeFormula.Steps[0].Title, "Implement auth")
	}
}

// =============================================================================
// Gate Bead Tests (bd-4k3c: Gate beads created during cook)
// =============================================================================

// TestCreateGateIssue tests that createGateIssue creates proper gate issues
func TestCreateGateIssue(t *testing.T) {
	tests := []struct {
		name          string
		step          *formula.Step
		parentID      string
		wantID        string
		wantTitle     string
		wantAwaitType string
		wantAwaitID   string
	}{
		{
			name: "gh:run gate with legacy ID",
			step: &formula.Step{
				ID:    "await-ci",
				Title: "Wait for CI",
				Gate: &formula.Gate{
					Type: "gh:run",
					ID:   "release-build",
				},
			},
			parentID:      "mol-release",
			wantID:        "mol-release.gate-await-ci",
			wantTitle:     "Gate: gh:run release-build",
			wantAwaitType: "gh:run",
			wantAwaitID:   "release-build",
		},
		{
			name: "gh:pr gate with await_id",
			step: &formula.Step{
				ID:    "await-pr",
				Title: "Wait for PR",
				Gate: &formula.Gate{
					Type:    "gh:pr",
					AwaitID: "https://github.com/org/repo/pull/123",
				},
			},
			parentID:      "mol-feature",
			wantID:        "mol-feature.gate-await-pr",
			wantTitle:     "Gate: gh:pr https://github.com/org/repo/pull/123",
			wantAwaitType: "gh:pr",
			wantAwaitID:   "https://github.com/org/repo/pull/123",
		},
		{
			name: "gh:pr gate without ID",
			step: &formula.Step{
				ID:    "await-pr",
				Title: "Wait for PR",
				Gate: &formula.Gate{
					Type: "gh:pr",
				},
			},
			parentID:      "mol-feature",
			wantID:        "mol-feature.gate-await-pr",
			wantTitle:     "Gate: gh:pr",
			wantAwaitType: "gh:pr",
			wantAwaitID:   "",
		},
		{
			name: "timer gate",
			step: &formula.Step{
				ID:    "cooldown",
				Title: "Wait for cooldown",
				Gate: &formula.Gate{
					Type:    "timer",
					Timeout: "30m",
				},
			},
			parentID:      "mol-deploy",
			wantID:        "mol-deploy.gate-cooldown",
			wantTitle:     "Gate: timer",
			wantAwaitType: "timer",
			wantAwaitID:   "",
		},
		{
			name: "human gate",
			step: &formula.Step{
				ID:    "approval",
				Title: "Manual approval",
				Gate: &formula.Gate{
					Type:    "human",
					Timeout: "24h",
				},
			},
			parentID:      "mol-release",
			wantID:        "mol-release.gate-approval",
			wantTitle:     "Gate: human",
			wantAwaitType: "human",
			wantAwaitID:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateIssue := createGateIssue(tt.step, tt.parentID)

			if gateIssue == nil {
				t.Fatal("createGateIssue returned nil")
			}

			if gateIssue.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", gateIssue.ID, tt.wantID)
			}
			if gateIssue.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", gateIssue.Title, tt.wantTitle)
			}
			if gateIssue.AwaitType != tt.wantAwaitType {
				t.Errorf("AwaitType = %q, want %q", gateIssue.AwaitType, tt.wantAwaitType)
			}
			if gateIssue.AwaitID != tt.wantAwaitID {
				t.Errorf("AwaitID = %q, want %q", gateIssue.AwaitID, tt.wantAwaitID)
			}
			if gateIssue.IssueType != "gate" {
				t.Errorf("IssueType = %q, want %q", gateIssue.IssueType, "gate")
			}
			if !gateIssue.IsTemplate {
				t.Error("IsTemplate should be true")
			}
		})
	}
}

// TestCreateGateIssue_Repo covers SF2: a formula gate's `repo` field must be
// propagated onto the created gate issue's metadata, matching the
// declarative `metadata.repo` selector documented for ad-hoc gates.
func TestCreateGateIssue_Repo(t *testing.T) {
	t.Run("propagates_repo_to_metadata", func(t *testing.T) {
		step := &formula.Step{
			ID:    "await-ci",
			Title: "Wait for CI",
			Gate: &formula.Gate{
				Type: "gh:run",
				ID:   "release.yml",
				Repo: "srobroek/agentic-packages",
			},
		}

		gateIssue := createGateIssue(step, "mol-release")
		if gateIssue == nil {
			t.Fatal("createGateIssue returned nil")
		}

		var metadata struct {
			Repo string `json:"repo"`
		}
		if err := json.Unmarshal(gateIssue.Metadata, &metadata); err != nil {
			t.Fatalf("gateIssue.Metadata = %s, not valid JSON: %v", gateIssue.Metadata, err)
		}
		if metadata.Repo != "srobroek/agentic-packages" {
			t.Errorf("metadata.repo = %q, want %q", metadata.Repo, "srobroek/agentic-packages")
		}
	})

	t.Run("no_repo_no_metadata", func(t *testing.T) {
		step := &formula.Step{
			ID:    "await-ci",
			Title: "Wait for CI",
			Gate: &formula.Gate{
				Type: "gh:run",
				ID:   "release.yml",
			},
		}

		gateIssue := createGateIssue(step, "mol-release")
		if gateIssue == nil {
			t.Fatal("createGateIssue returned nil")
		}
		if len(gateIssue.Metadata) != 0 {
			t.Errorf("gateIssue.Metadata = %s, want empty", gateIssue.Metadata)
		}
	})

	// Non-gh:* gate types (SF4): `repo` on a human/timer/bead gate step is
	// unrelated, ordinary metadata, not a GitHub repo selector - it must not
	// be written to gateIssue.Metadata, matching repoMetadataForGate's
	// isGitHubGateType restriction for ad-hoc gate creation.
	t.Run("non_github_gate_type_ignores_repo", func(t *testing.T) {
		step := &formula.Step{
			ID:    "human-approval",
			Title: "Wait for approval",
			Gate: &formula.Gate{
				Type: "human",
				Repo: "srobroek/agentic-packages",
			},
		}

		gateIssue := createGateIssue(step, "mol-release")
		if gateIssue == nil {
			t.Fatal("createGateIssue returned nil")
		}
		if len(gateIssue.Metadata) != 0 {
			t.Errorf("gateIssue.Metadata = %s, want empty (non-gh:* gate type must not get metadata.repo)", gateIssue.Metadata)
		}
	})
}

// TestCreateGateIssue_NilGate tests that nil Gate returns nil
func TestCreateGateIssue_NilGate(t *testing.T) {
	step := &formula.Step{
		ID:    "no-gate",
		Title: "Step without gate",
		Gate:  nil,
	}

	gateIssue := createGateIssue(step, "mol-test")
	if gateIssue != nil {
		t.Errorf("Expected nil for step without Gate, got %+v", gateIssue)
	}
}

// TestCreateGateIssue_Timeout tests that timeout is parsed correctly
func TestCreateGateIssue_Timeout(t *testing.T) {
	tests := []struct {
		name        string
		timeout     string
		wantMinutes int
	}{
		{"30 minutes", "30m", 30},
		{"1 hour", "1h", 60},
		{"24 hours", "24h", 1440},
		{"invalid timeout", "invalid", 0},
		{"empty timeout", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := &formula.Step{
				ID:    "timed-step",
				Title: "Timed step",
				Gate: &formula.Gate{
					Type:    "timer",
					Timeout: tt.timeout,
				},
			}

			gateIssue := createGateIssue(step, "mol-test")
			gotMinutes := int(gateIssue.Timeout.Minutes())

			if gotMinutes != tt.wantMinutes {
				t.Errorf("Timeout minutes = %d, want %d", gotMinutes, tt.wantMinutes)
			}
		})
	}
}

// TestCookFormulaToSubgraph_GateBeads tests that gate beads are created in subgraph
func TestCookFormulaToSubgraph_GateBeads(t *testing.T) {
	f := &formula.Formula{
		Formula:     "mol-test-gate",
		Description: "Test gate creation",
		Version:     1,
		Type:        formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:    "build",
				Title: "Build project",
			},
			{
				ID:    "await-ci",
				Title: "Wait for CI",
				Gate: &formula.Gate{
					Type: "gh:run",
					ID:   "ci-workflow",
				},
			},
			{
				ID:        "verify",
				Title:     "Verify deployment",
				DependsOn: []string{"await-ci"},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "mol-test-gate")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	// Should have: root + 3 steps + 1 gate = 5 issues
	if len(subgraph.Issues) != 5 {
		t.Errorf("Expected 5 issues, got %d", len(subgraph.Issues))
		for _, issue := range subgraph.Issues {
			t.Logf("  Issue: %s (%s)", issue.ID, issue.IssueType)
		}
	}

	// Find the gate issue
	var gateIssue *types.Issue
	for _, issue := range subgraph.Issues {
		if issue.IssueType == "gate" {
			gateIssue = issue
			break
		}
	}

	if gateIssue == nil {
		t.Fatal("Gate issue not found in subgraph")
	}

	if gateIssue.ID != "mol-test-gate.gate-await-ci" {
		t.Errorf("Gate ID = %q, want %q", gateIssue.ID, "mol-test-gate.gate-await-ci")
	}
	if gateIssue.AwaitType != "gh:run" {
		t.Errorf("Gate AwaitType = %q, want %q", gateIssue.AwaitType, "gh:run")
	}
	if gateIssue.AwaitID != "ci-workflow" {
		t.Errorf("Gate AwaitID = %q, want %q", gateIssue.AwaitID, "ci-workflow")
	}
}

// TestCookFormulaToSubgraph_GateDependencies tests that step depends on its gate
func TestCookFormulaToSubgraph_GateDependencies(t *testing.T) {
	f := &formula.Formula{
		Formula:     "mol-gate-deps",
		Description: "Test gate dependencies",
		Version:     1,
		Type:        formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:    "await-approval",
				Title: "Wait for approval",
				Gate: &formula.Gate{
					Type:    "human",
					Timeout: "24h",
				},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "mol-gate-deps")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	// Find the blocking dependency: step -> gate
	stepID := "mol-gate-deps.await-approval"
	gateID := "mol-gate-deps.gate-await-approval"

	var foundBlockingDep bool
	for _, dep := range subgraph.Dependencies {
		if dep.IssueID == stepID && dep.DependsOnID == gateID && dep.Type == "blocks" {
			foundBlockingDep = true
			break
		}
	}

	if !foundBlockingDep {
		t.Error("Expected blocking dependency from step to gate not found")
		t.Log("Dependencies found:")
		for _, dep := range subgraph.Dependencies {
			t.Logf("  %s -> %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
		}
	}
}

// TestCookFormulaToSubgraph_GateParentChild tests that gate is a child of the parent
func TestCookFormulaToSubgraph_GateParentChild(t *testing.T) {
	f := &formula.Formula{
		Formula:     "mol-gate-parent",
		Description: "Test gate parent-child relationship",
		Version:     1,
		Type:        formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:    "gated-step",
				Title: "Gated step",
				Gate: &formula.Gate{
					Type: "mail",
				},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "mol-gate-parent")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	// Find the parent-child dependency: gate -> root
	gateID := "mol-gate-parent.gate-gated-step"
	rootID := "mol-gate-parent"

	var foundParentChildDep bool
	for _, dep := range subgraph.Dependencies {
		if dep.IssueID == gateID && dep.DependsOnID == rootID && dep.Type == "parent-child" {
			foundParentChildDep = true
			break
		}
	}

	if !foundParentChildDep {
		t.Error("Expected parent-child dependency for gate not found")
		t.Log("Dependencies found:")
		for _, dep := range subgraph.Dependencies {
			t.Logf("  %s -> %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
		}
	}
}

// =============================================================================
// Standalone Expansion Tests (bd-qzb)
// =============================================================================

// TestCookFormulaToSubgraph_StandaloneExpansion tests that a materialized
// expansion formula produces the correct subgraph with root epic + children.
func TestCookFormulaToSubgraph_StandaloneExpansion(t *testing.T) {
	f := &formula.Formula{
		Formula:     "rule-of-five",
		Description: "Iterative refinement",
		Version:     1,
		Type:        formula.TypeExpansion,
		Template: []*formula.Step{
			{ID: "{target}.draft", Title: "Draft: {target.title}"},
			{ID: "{target}.refine-1", Title: "Refine 1", Needs: []string{"{target}.draft"}},
			{ID: "{target}.refine-2", Title: "Refine 2", Needs: []string{"{target}.refine-1"}},
			{ID: "{target}.refine-3", Title: "Refine 3", Needs: []string{"{target}.refine-2"}},
			{ID: "{target}.refine-4", Title: "Refine 4", Needs: []string{"{target}.refine-3"}},
		},
	}

	// Materialize the expansion (converts Template -> Steps)
	err := formula.MaterializeExpansion(f, "main", nil)
	if err != nil {
		t.Fatalf("MaterializeExpansion failed: %v", err)
	}

	// Cook to subgraph
	subgraph, err := cookFormulaToSubgraph(f, "rule-of-five")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	// Should have: 1 root epic + 5 child steps = 6 issues
	if len(subgraph.Issues) != 6 {
		t.Errorf("expected 6 issues, got %d", len(subgraph.Issues))
		for _, issue := range subgraph.Issues {
			t.Logf("  Issue: %s (%s) %s", issue.ID, issue.IssueType, issue.Title)
		}
	}

	// Root molecule
	if subgraph.Root.ID != "rule-of-five" {
		t.Errorf("Root.ID = %q, want %q", subgraph.Root.ID, "rule-of-five")
	}
	if subgraph.Root.IssueType != types.TypeMolecule {
		t.Errorf("Root.IssueType = %q, want %q", subgraph.Root.IssueType, types.TypeMolecule)
	}

	// Verify child issue IDs
	expectedChildIDs := []string{
		"rule-of-five.main.draft",
		"rule-of-five.main.refine-1",
		"rule-of-five.main.refine-2",
		"rule-of-five.main.refine-3",
		"rule-of-five.main.refine-4",
	}
	for _, expID := range expectedChildIDs {
		found := false
		for _, issue := range subgraph.Issues {
			if issue.ID == expID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected child issue %q not found in subgraph", expID)
		}
	}

	// Verify dependency chain: each refine step depends on the previous
	// Dependencies include parent-child + needs (blocks)
	depMap := make(map[string][]string) // issueID -> depends on
	for _, dep := range subgraph.Dependencies {
		if dep.Type == "blocks" {
			depMap[dep.IssueID] = append(depMap[dep.IssueID], dep.DependsOnID)
		}
	}

	// refine-1 should block on draft
	if deps, ok := depMap["rule-of-five.main.refine-1"]; !ok || len(deps) == 0 {
		t.Error("refine-1 should have a blocking dependency")
	} else {
		found := false
		for _, d := range deps {
			if d == "rule-of-five.main.draft" {
				found = true
			}
		}
		if !found {
			t.Errorf("refine-1 should depend on draft, got deps: %v", deps)
		}
	}
}

// TestCookFormulaToSubgraph_StandaloneExpansionWithWorkflowVars tests that
// {{double-brace}} vars survive materialization and appear in cooked issues.
func TestCookFormulaToSubgraph_StandaloneExpansionWithWorkflowVars(t *testing.T) {
	f := &formula.Formula{
		Formula:     "scoped-expansion",
		Description: "Expansion with workflow vars",
		Version:     1,
		Type:        formula.TypeExpansion,
		Template: []*formula.Step{
			{
				ID:          "{target}.work",
				Title:       "Work on {{feature}}",
				Description: "Build {{feature}} per brief: {{brief}}",
			},
		},
	}

	err := formula.MaterializeExpansion(f, "main", nil)
	if err != nil {
		t.Fatalf("MaterializeExpansion failed: %v", err)
	}

	subgraph, err := cookFormulaToSubgraph(f, "scoped-expansion")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	// Find the work issue
	var workIssue *types.Issue
	for _, issue := range subgraph.Issues {
		if issue.ID == "scoped-expansion.main.work" {
			workIssue = issue
			break
		}
	}

	if workIssue == nil {
		t.Fatal("work issue not found in subgraph")
	}

	// {{double-brace}} vars should be preserved for later substitution
	if workIssue.Title != "Work on {{feature}}" {
		t.Errorf("Title = %q, want %q", workIssue.Title, "Work on {{feature}}")
	}
	if workIssue.Description != "Build {{feature}} per brief: {{brief}}" {
		t.Errorf("Description = %q, want {{vars}} preserved", workIssue.Description)
	}
}

// TestCookFormulaToSubgraph_StepMetadata verifies that a step's Metadata flows
// through cook onto the resulting Issue.Metadata as a JSON object. Regression
// for gastownhall/beads#3341.
func TestCookFormulaToSubgraph_StepMetadata(t *testing.T) {
	f := &formula.Formula{
		Formula: "repro",
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps: []*formula.Step{
			{
				ID:     "work",
				Title:  "Do the work",
				Labels: []string{"worker"},
				Metadata: map[string]interface{}{
					"priority_level": "high",
					"origin":         "repro",
				},
			},
		},
	}

	subgraph, err := cookFormulaToSubgraph(f, "repro")
	if err != nil {
		t.Fatalf("cookFormulaToSubgraph failed: %v", err)
	}

	var workIssue *types.Issue
	for _, issue := range subgraph.Issues {
		if issue.ID == "repro.work" {
			workIssue = issue
			break
		}
	}
	if workIssue == nil {
		t.Fatal("repro.work issue not found in subgraph")
	}
	if len(workIssue.Metadata) == 0 {
		t.Fatalf("workIssue.Metadata is empty; want JSON object carrying step metadata")
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(workIssue.Metadata, &decoded); err != nil {
		t.Fatalf("workIssue.Metadata is not valid JSON: %v (raw: %s)", err, string(workIssue.Metadata))
	}
	if got := decoded["priority_level"]; got != "high" {
		t.Errorf("Metadata[priority_level] = %v, want \"high\"", got)
	}
	if got := decoded["origin"]; got != "repro" {
		t.Errorf("Metadata[origin] = %v, want \"repro\"", got)
	}
}

// TestProcessStepToIssueParentType verifies that a parent step's declared
// type is honored; only a step with children and no declared type defaults
// to epic (GH#5443).
func TestProcessStepToIssueParentType(t *testing.T) {
	tests := []struct {
		name     string
		stepType string
		want     types.IssueType
	}{
		{"undeclared parent defaults to epic", "", types.TypeEpic},
		{"declared built-in type kept on parent", "decision", types.TypeDecision},
		{"declared custom type kept on parent", "duty", types.IssueType("duty")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := &formula.Step{
				ID:       "parent",
				Title:    "parent step",
				Type:     tt.stepType,
				Children: []*formula.Step{{ID: "c", Title: "child step"}},
			}
			issue := processStepToIssue(step, "f")
			if issue.IssueType != tt.want {
				t.Errorf("IssueType = %q, want %q", issue.IssueType, tt.want)
			}
		})
	}
}

func TestStepTypeToIssueType(t *testing.T) {
	tests := []struct {
		stepType string
		want     types.IssueType
	}{
		// Core work types pass through.
		{"task", types.TypeTask},
		{"bug", types.TypeBug},
		{"feature", types.TypeFeature},
		{"epic", types.TypeEpic},
		{"chore", types.TypeChore},
		// Empty (or whitespace-only) defaults to task; surrounding
		// whitespace is trimmed rather than becoming part of the type.
		{"", types.TypeTask},
		{"   ", types.TypeTask},
		{" bug ", types.TypeBug},
		// Other built-in types pass through instead of collapsing to task.
		{"decision", types.TypeDecision},
		{"spike", types.TypeSpike},
		{"story", types.TypeStory},
		// Aliases normalize to their canonical form.
		{"enhancement", types.TypeFeature},
		// Non-built-in types pass through; at pour/cook time,
		// flattenUnregisteredIssueTypes degrades them to task unless they
		// are already registered in types.custom.
		{"duty", types.IssueType("duty")},
	}
	for _, tt := range tests {
		if got := stepTypeToIssueType(tt.stepType); got != tt.want {
			t.Errorf("stepTypeToIssueType(%q) = %q, want %q", tt.stepType, got, tt.want)
		}
	}
}
