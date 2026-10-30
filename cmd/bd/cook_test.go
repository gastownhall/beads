package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// TestSubstituteStepVars_LabelsAssigneeMetadata covers GH#5110 and GH#5754:
// runtime-mode cook substituted Title/Description/Notes/Gate but left labels,
// assignee, and metadata literal, so `bd cook --mode runtime` emitted (and
// `--persist` stored) placeholders that no later step ever resolved.
func TestSubstituteStepVars_LabelsAssigneeMetadata(t *testing.T) {
	steps := []*formula.Step{
		{
			ID:       "qa-contract",
			Title:    "API-contract QA: {{widget_id}}",
			Assignee: "{{agent}}",
			Labels:   []string{"qa-run", "widget:{{widget_id}}", "domain:{{domain}}"},
			Metadata: map[string]interface{}{
				"ado_id": "{{ado_id}}",
				"count":  int64(3),
				"nested": map[string]interface{}{"k": "{{domain}}"},
				"list":   []interface{}{"{{domain}}", "static"},
			},
			Children: []*formula.Step{
				{ID: "child", Assignee: "{{agent}}", Labels: []string{"widget:{{widget_id}}"}},
			},
		},
	}

	vars := map[string]string{
		"widget_id": "my_widget",
		"domain":    "payroll",
		"agent":     "design-agent",
		"ado_id":    "731878",
	}
	substituteStepVars(steps, vars)

	step := steps[0]
	if step.Assignee != "design-agent" {
		t.Errorf("Assignee = %q, want %q", step.Assignee, "design-agent")
	}
	wantLabels := []string{"qa-run", "widget:my_widget", "domain:payroll"}
	for i, want := range wantLabels {
		if step.Labels[i] != want {
			t.Errorf("Labels[%d] = %q, want %q", i, step.Labels[i], want)
		}
	}
	if got := step.Metadata["ado_id"]; got != "731878" {
		t.Errorf("Metadata[ado_id] = %v, want %q", got, "731878")
	}
	if got := step.Metadata["count"]; got != int64(3) {
		t.Errorf("Metadata[count] = %v (%T), want int64(3) - non-string scalars must be untouched", got, got)
	}
	if got := step.Metadata["nested"].(map[string]interface{})["k"]; got != "payroll" {
		t.Errorf("Metadata[nested][k] = %v, want %q", got, "payroll")
	}
	if got := step.Metadata["list"].([]interface{})[0]; got != "payroll" {
		t.Errorf("Metadata[list][0] = %v, want %q", got, "payroll")
	}

	child := step.Children[0]
	if child.Assignee != "design-agent" {
		t.Errorf("child Assignee = %q, want %q", child.Assignee, "design-agent")
	}
	if child.Labels[0] != "widget:my_widget" {
		t.Errorf("child Labels[0] = %q, want %q", child.Labels[0], "widget:my_widget")
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

// cookGateFormulaTOML cooks a TOML formula through the same load, resolve,
// control-flow and cook path pour uses (resolveAndCookFormulaWithVars).
func cookGateFormulaTOML(t *testing.T, name, src string) *TemplateSubgraph {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name+".formula.toml"), []byte(src), 0o600); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	sg, err := resolveAndCookFormulaWithVars(name, []string{dir}, nil)
	if err != nil {
		t.Fatalf("cook %q: %v", name, err)
	}
	return sg
}

// blocksEdgesFrom returns the DepBlocks targets of issueID in subgraph order.
func blocksEdgesFrom(sg *TemplateSubgraph, issueID string) []string {
	var out []string
	for _, d := range sg.Dependencies {
		if d.IssueID == issueID && d.Type == types.DepBlocks {
			out = append(out, d.DependsOnID)
		}
	}
	return out
}

// waitsForEdgesFrom returns the DepWaitsFor targets of issueID in subgraph
// order.
func waitsForEdgesFrom(sg *TemplateSubgraph, issueID string) []string {
	var out []string
	for _, d := range sg.Dependencies {
		if d.IssueID == issueID && d.Type == types.DepWaitsFor {
			out = append(out, d.DependsOnID)
		}
	}
	return out
}

// TestCookFormulaToSubgraph_EntryGateWiring pins the entry-gate form that
// GH#3316 leaves unchanged: a [steps.gate] on a step that is not declared
// type = "gate" spawns a companion that blocks the step.
func TestCookFormulaToSubgraph_EntryGateWiring(t *testing.T) {
	gates := map[string]string{
		"human":  `{ type = "human" }`,
		"gh:run": `{ type = "gh:run", id = "ci", repo = "acme/widgets" }`,
		"timer":  `{ type = "timer", timeout = "1h" }`,
	}
	stepTypes := map[string]string{
		"untyped":        "",
		"task":           `type = "task"`,
		"noncanonical-G": `type = "Gate"`,
	}
	for gateName, gateSrc := range gates {
		for typeName, typeLine := range stepTypes {
			t.Run(gateName+"/"+typeName, func(t *testing.T) {
				src := `formula = "entry-gate"
version = 1
type = "workflow"

[[steps]]
id = "wait"
title = "Wait then work"
` + typeLine + `
gate = ` + gateSrc + "\n"
				sg := cookGateFormulaTOML(t, "entry-gate", src)
				stepID, gateID := "entry-gate.wait", "entry-gate.gate-wait"
				gate := sg.IssueMap[gateID]
				if gate == nil || gate.IssueType != types.TypeGate {
					t.Fatalf("companion %s missing or not a gate: %+v (issues %v)", gateID, gate, allIssueIDs(sg))
				}
				if gate.AwaitType != gateName {
					t.Errorf("companion AwaitType = %q, want %q", gate.AwaitType, gateName)
				}
				if got := blocksEdgesFrom(sg, stepID); len(got) != 1 || got[0] != gateID {
					t.Errorf("step blocks edges = %v, want [%s]", got, gateID)
				}
				if got := blocksEdgesFrom(sg, gateID); len(got) != 0 {
					t.Errorf("companion must not block on the step; got blocks edges %v", got)
				}
			})
		}
	}
}

// gateTypedReproTOML is the GH#3316 repro: a prep task, a type = "gate"
// step carrying a human [steps.gate], and a step that depends on it.
const gateTypedReproTOML = `formula = "gate-repro"
version = 1
type = "workflow"

[[steps]]
id = "prep"
title = "Prep evidence"
type = "task"
assignee = "worker"

[[steps]]
id = "agree"
title = "Human approves"
description = "Gather evidence and write the decision template"
type = "gate"
assignee = "pa-agent"
labels = ["needs-human"]
depends_on = ["prep"]
gate = { type = "human" }

[[steps]]
id = "execute"
title = "Do the thing"
type = "task"
depends_on = ["agree"]
`

// TestCookFormulaToSubgraph_GateTypedStep covers the exit-gate form
// (GH#3316): a step declared type = "gate" with a [steps.gate] block is
// prep work whose task bead blocks the companion gate, not the reverse.
func TestCookFormulaToSubgraph_GateTypedStep(t *testing.T) {
	t.Run("issue_repro", func(t *testing.T) {
		sg := cookGateFormulaTOML(t, "gate-repro", gateTypedReproTOML)
		taskID, gateID, prepID := "gate-repro.agree", "gate-repro.gate-agree", "gate-repro.prep"

		task := sg.IssueMap[taskID]
		if task == nil {
			t.Fatalf("task %s missing; issues %v", taskID, allIssueIDs(sg))
		}
		// The prep bead must be a type bd ready includes; gate is excluded
		// from ready work (sqlbuild.ReadyWorkExcludeTypes).
		if task.IssueType != types.TypeTask {
			t.Errorf("task IssueType = %q, want %q", task.IssueType, types.TypeTask)
		}
		if task.Title != "Human approves" || task.Assignee != "pa-agent" ||
			task.Description != "Gather evidence and write the decision template" {
			t.Errorf("task lost step fields: title=%q assignee=%q desc=%q", task.Title, task.Assignee, task.Description)
		}
		if len(task.Labels) != 1 || task.Labels[0] != "needs-human" {
			t.Errorf("task labels = %v, want [needs-human]", task.Labels)
		}
		if task.AwaitType != "" {
			t.Errorf("task AwaitType = %q, want empty (await fields belong to the companion)", task.AwaitType)
		}

		gate := sg.IssueMap[gateID]
		if gate == nil || gate.IssueType != types.TypeGate || gate.AwaitType != "human" {
			t.Fatalf("companion %s = %+v, want a human gate", gateID, gate)
		}

		if got := blocksEdgesFrom(sg, gateID); len(got) != 1 || got[0] != taskID {
			t.Errorf("companion blocks edges = %v, want [%s] (task blocks gate)", got, taskID)
		}
		if got := blocksEdgesFrom(sg, taskID); len(got) != 1 || got[0] != prepID {
			t.Errorf("task blocks edges = %v, want only its declared depends_on [%s]", got, prepID)
		}
	})

	// AC-3: a reference to a gate-typed step waits for its task and for its
	// companion. The task edge holds the order when gate check or resolve
	// closes the companion while the task is still open.
	t.Run("references_wait_for_task_and_companion", func(t *testing.T) {
		// Built in Go, not TOML: formula validation rejects "gate-s" as an
		// unknown step id, but cookFormulaToSubgraph callers can still pass
		// it, and it must not produce a duplicate edge.
		f := &formula.Formula{
			Formula: "gate-refs",
			Version: 1,
			Type:    formula.TypeWorkflow,
			Steps: []*formula.Step{
				{ID: "s", Title: "Prep for approval", Type: "gate", Gate: &formula.Gate{Type: "human"}},
				{ID: "by-depends-on", Title: "depends_on", DependsOn: []string{"s"}},
				{ID: "by-needs", Title: "needs", Needs: []string{"s"}},
				{ID: "by-both-ids", Title: "depends_on step and gate key", DependsOn: []string{"s", "gate-s"}},
				{ID: "unrelated", Title: "No reference"},
			},
		}
		sg, err := cookFormulaToSubgraph(f, "gate-refs")
		if err != nil {
			t.Fatalf("cookFormulaToSubgraph: %v", err)
		}
		taskID, gateID := "gate-refs.s", "gate-refs.gate-s"
		want := []string{taskID, gateID}
		for _, dependent := range []string{"by-depends-on", "by-needs", "by-both-ids"} {
			if got := blocksEdgesFrom(sg, "gate-refs."+dependent); !slices.Equal(got, want) {
				t.Errorf("%s blocks edges = %v, want exactly %v", dependent, got, want)
			}
		}
		if got := blocksEdgesFrom(sg, "gate-refs.unrelated"); len(got) != 0 {
			t.Errorf("unrelated blocks edges = %v, want none", got)
		}
		blockedBy := func(targetID string) []string {
			var out []string
			for _, d := range sg.Dependencies {
				if d.DependsOnID == targetID && d.Type == types.DepBlocks {
					out = append(out, d.IssueID)
				}
			}
			return out
		}
		if got := blockedBy(gateID); len(got) != 3 {
			t.Errorf("issues blocked by the companion = %v, want the three referencing steps", got)
		}
		// The three referencing steps, plus the companion itself.
		if got := blockedBy(taskID); len(got) != 4 || !slices.Contains(got, gateID) {
			t.Errorf("issues blocked by the task = %v, want the companion and the three referencing steps", got)
		}
	})

	t.Run("issue_repro_downstream", func(t *testing.T) {
		sg := cookGateFormulaTOML(t, "gate-repro", gateTypedReproTOML)
		want := []string{"gate-repro.agree", "gate-repro.gate-agree"}
		if got := blocksEdgesFrom(sg, "gate-repro.execute"); !slices.Equal(got, want) {
			t.Errorf("execute blocks edges = %v, want %v", got, want)
		}
	})

	t.Run("child_step_referenced_by_sibling", func(t *testing.T) {
		sg := cookGateFormulaTOML(t, "gate-child", `formula = "gate-child"
version = 1
type = "workflow"

[[steps]]
id = "phase"
title = "Phase"

[[steps.children]]
id = "review"
title = "Prep review"
type = "gate"
gate = { type = "human" }

[[steps.children]]
id = "apply"
title = "Apply"
needs = ["review"]
`)
		taskID, gateID := "gate-child.phase.review", "gate-child.phase.gate-review"
		if sg.IssueMap[gateID] == nil {
			t.Fatalf("companion %s missing; issues %v", gateID, allIssueIDs(sg))
		}
		if got := blocksEdgesFrom(sg, gateID); len(got) != 1 || got[0] != taskID {
			t.Errorf("companion blocks edges = %v, want [%s]", got, taskID)
		}
		if got, want := blocksEdgesFrom(sg, "gate-child.phase.apply"), []string{taskID, gateID}; !slices.Equal(got, want) {
			t.Errorf("apply blocks edges = %v, want %v", got, want)
		}
	})

	t.Run("loop_body_step", func(t *testing.T) {
		sg := cookGateFormulaTOML(t, "gate-loop", `formula = "gate-loop"
version = 1
type = "workflow"

[[steps]]
id = "round"
title = "Rounds"

[steps.loop]
count = 2

[[steps.loop.body]]
id = "review"
title = "Prep review"
type = "gate"
gate = { type = "human" }

[[steps.loop.body]]
id = "apply"
title = "Apply"
needs = ["review"]
`)
		for _, iter := range []string{"iter1", "iter2"} {
			taskID := "gate-loop.round." + iter + ".review"
			gateID := "gate-loop.gate-round." + iter + ".review"
			if sg.IssueMap[gateID] == nil {
				t.Fatalf("companion %s missing; issues %v", gateID, allIssueIDs(sg))
			}
			if got := blocksEdgesFrom(sg, gateID); len(got) != 1 || got[0] != taskID {
				t.Errorf("companion blocks edges = %v, want [%s]", got, taskID)
			}
			if got, want := blocksEdgesFrom(sg, "gate-loop.round."+iter+".apply"), []string{taskID, gateID}; !slices.Equal(got, want) {
				t.Errorf("%s apply blocks edges = %v, want %v; deps %v", iter, got, want, allDeps(sg))
			}
		}
	})

	// A waits_for spawner that is a gate-typed step resolves to its task,
	// where the fanout's children are bonded; the waiter also blocks on the
	// companion. A collapsed needs entry keeps also_blocks on the task edge.
	for name, tc := range map[string]struct {
		waiter     string
		alsoBlocks bool
	}{
		"children_of":        {waiter: `waits_for = "children-of(fanout)"`},
		"needs_all_children": {waiter: "needs = [\"fanout\"]\nwaits_for = \"all-children\"", alsoBlocks: true},
	} {
		t.Run("waits_for_spawner/"+name, func(t *testing.T) {
			sg := cookGateFormulaTOML(t, "gate-fan", `formula = "gate-fan"
version = 1
type = "workflow"

[[steps]]
id = "fanout"
title = "Spawn workers for review"
type = "gate"
gate = { type = "human" }

[[steps]]
id = "summarize"
title = "Summarize the workers"
`+tc.waiter+"\n")
			taskID, gateID, waiterID := "gate-fan.fanout", "gate-fan.gate-fanout", "gate-fan.summarize"
			var waitsFor []*types.Dependency
			for _, d := range sg.Dependencies {
				if d.IssueID == waiterID && d.Type == types.DepWaitsFor {
					waitsFor = append(waitsFor, d)
				}
			}
			if len(waitsFor) != 1 || waitsFor[0].DependsOnID != taskID {
				t.Fatalf("summarize needs one waits-for edge onto the task %s; deps %v", taskID, allDeps(sg))
			}
			var meta types.WaitsForMeta
			if err := json.Unmarshal([]byte(waitsFor[0].Metadata), &meta); err != nil {
				t.Fatalf("waits-for metadata %q: %v", waitsFor[0].Metadata, err)
			}
			if meta.AlsoBlocks != tc.alsoBlocks {
				t.Errorf("waits-for also_blocks = %v, want %v (metadata %s)", meta.AlsoBlocks, tc.alsoBlocks, waitsFor[0].Metadata)
			}
			if got, want := blocksEdgesFrom(sg, waiterID), []string{gateID}; !slices.Equal(got, want) {
				t.Errorf("summarize blocks edges = %v, want %v", got, want)
			}
		})
	}

	// AC-6: the companion carries the gate's await fields and repo selector;
	// the task keeps the step's own metadata.
	for _, gateType := range []string{"gh:run", "gh:pr"} {
		t.Run("github_gate_fields/"+gateType, func(t *testing.T) {
			sg := cookGateFormulaTOML(t, "gate-gh", `formula = "gate-gh"
version = 1
type = "workflow"

[[steps]]
id = "release"
title = "Cut the release"
type = "gate"

[steps.metadata]
owner = "release-team"

[steps.gate]
type = "`+gateType+`"
id = "release.yml"
timeout = "30m"
repo = "acme/widgets"
`)
			taskID, gateID := "gate-gh.release", "gate-gh.gate-release"
			var gates []string
			for _, i := range sg.Issues {
				if i.IssueType == types.TypeGate {
					gates = append(gates, i.ID)
				}
			}
			if len(gates) != 1 || gates[0] != gateID {
				t.Fatalf("gate issues = %v, want exactly [%s]", gates, gateID)
			}
			gate, task := sg.IssueMap[gateID], sg.IssueMap[taskID]
			if gate.AwaitType != gateType || gate.AwaitID != "release.yml" || gate.Timeout != 30*time.Minute {
				t.Errorf("companion await = (%q, %q, %v), want (%q, release.yml, 30m)", gate.AwaitType, gate.AwaitID, gate.Timeout, gateType)
			}
			var gateMeta map[string]string
			if err := json.Unmarshal(gate.Metadata, &gateMeta); err != nil || gateMeta["repo"] != "acme/widgets" {
				t.Errorf("companion metadata = %s (err %v), want repo acme/widgets", gate.Metadata, err)
			}
			if task.AwaitType != "" || task.AwaitID != "" || task.Timeout != 0 {
				t.Errorf("task await = (%q, %q, %v), want empty", task.AwaitType, task.AwaitID, task.Timeout)
			}
			var taskMeta map[string]any
			if err := json.Unmarshal(task.Metadata, &taskMeta); err != nil || taskMeta["owner"] != "release-team" {
				t.Errorf("task metadata = %s (err %v), want owner release-team", task.Metadata, err)
			}
			if _, ok := taskMeta["repo"]; ok {
				t.Errorf("task metadata = %s, repo belongs on the companion", task.Metadata)
			}
		})
	}
}

// TestCookFormulaToSubgraph_BareGateStepType pins that a step declared
// type = "gate" without a [steps.gate] block stays a single wait-only gate
// bead: no companion, no await fields, no extra blocks edge (GH#3316).
func TestCookFormulaToSubgraph_BareGateStepType(t *testing.T) {
	sg := cookGateFormulaTOML(t, "bare-gate", `formula = "bare-gate"
version = 1
type = "workflow"

[[steps]]
id = "hold"
title = "Hold for sign-off"
type = "gate"
`)
	if len(sg.Issues) != 2 {
		t.Fatalf("issues = %v, want root + hold", allIssueIDs(sg))
	}
	hold := sg.IssueMap["bare-gate.hold"]
	if hold == nil || hold.IssueType != types.TypeGate {
		t.Fatalf("hold = %+v, want a gate bead", hold)
	}
	if hold.AwaitType != "" || hold.AwaitID != "" || hold.Timeout != 0 {
		t.Errorf("hold await fields = (%q, %q, %v), want empty", hold.AwaitType, hold.AwaitID, hold.Timeout)
	}
	for _, d := range sg.Dependencies {
		if d.Type == types.DepBlocks {
			t.Errorf("unexpected blocks edge %s -> %s", d.IssueID, d.DependsOnID)
		}
	}
}

// TestCollectStepsGateDirection pins GH#3316 (bd-4nndgi) at the
// collectSteps level: for a step declared type = "gate" the task is the
// preparation work for its companion gate, so the task blocks the gate.
// The triage version of this test dropped Type: "gate" and asserted the
// flip for every [steps.gate]; that contradicts the documented entry-gate
// form (TestCookFormulaToSubgraph_EntryGateWiring), so it pins only the
// gate-typed shape.
func TestCollectStepsGateDirection(t *testing.T) {
	steps := []*formula.Step{
		{ID: "agree", Title: "Human approves", Type: "gate", Gate: &formula.Gate{Type: "human"}},
	}
	idMapping := make(map[string]string)
	var issues []*types.Issue
	var deps []*types.Dependency
	collectSteps(steps, "mol-x", idMapping, nil, &issues, &deps, nil)

	taskID, gateID := idMapping["agree"], idMapping["gate-agree"]
	if taskID != "mol-x.agree" || gateID != "mol-x.gate-agree" {
		t.Fatalf("idMapping = %v, want agree→mol-x.agree, gate-agree→mol-x.gate-agree", idMapping)
	}
	var taskBlocksGate, gateBlocksTask bool
	for _, d := range deps {
		if d.Type != types.DepBlocks {
			continue
		}
		if d.IssueID == gateID && d.DependsOnID == taskID {
			taskBlocksGate = true
		}
		if d.IssueID == taskID && d.DependsOnID == gateID {
			gateBlocksTask = true
		}
	}
	if gateBlocksTask {
		t.Errorf("task %s depends on its gate %s: gate blocks its own prep task (inverted edge)", taskID, gateID)
	}
	if !taskBlocksGate {
		t.Errorf("gate %s does not depend on task %s: want task --blocks--> gate", gateID, taskID)
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

// cookFormulaTOMLWithVars cooks a TOML formula through
// resolveAndCookFormulaWithVars with a non-nil vars map, so the step
// condition filter runs as it does for pour and wisp (cookGateFormulaTOML
// passes nil and skips it).
func cookFormulaTOMLWithVars(t *testing.T, name, src string, vars map[string]string) (*TemplateSubgraph, error) {
	t.Helper()
	if vars == nil {
		vars = map[string]string{}
	}
	dir := writePipelineFormula(t, name, src)
	return resolveAndCookFormulaWithVars(name, []string{dir}, vars)
}

// conditionalFanoutFormula is a formula whose consumer names the conditional
// fanout step through field (needs or depends_on).
func conditionalFanoutFormula(name, field string) string {
	return `formula = "` + name + `"
version = 1
type = "workflow"

[[steps]]
id = "fanout"
title = "Fan out"
type = "task"
condition = "{{fanout_enabled}}"

[[steps]]
id = "consumer"
title = "Consume"
type = "task"
` + field + ` = ["fanout"]
`
}

// TestCookFormulaConditionDroppedNeedsTargetErrors: a step whose needs or
// depends_on target is dropped by its condition must fail the cook, not pour
// without its edge (GH#6437, PR #6642).
func TestCookFormulaConditionDroppedNeedsTargetErrors(t *testing.T) {
	for _, field := range []string{"needs", "depends_on"} {
		t.Run(field, func(t *testing.T) {
			name := "dangling-" + strings.ReplaceAll(field, "_", "-")
			sg, err := cookFormulaTOMLWithVars(t, name, conditionalFanoutFormula(name, field),
				map[string]string{"fanout_enabled": "false"})
			if err == nil {
				consumerID := name + ".consumer"
				t.Fatalf("cook succeeded although consumer's %s target %q was dropped by its condition; consumer blocks edges: %v",
					field, "fanout", blocksEdgesFrom(sg, consumerID))
			}
			if sg != nil {
				t.Errorf("cook returned a subgraph alongside the error")
			}
			if !errors.Is(err, formula.ErrValidation) {
				t.Errorf("errors.Is(err, formula.ErrValidation) = false for %v", err)
			}
			for _, want := range []string{`"consumer"`, field, `"fanout"`, "{{fanout_enabled}}"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestCookFormulaConditionDroppedWaitsForSpawnerErrors: a gate whose
// waits_for spawner is dropped by its condition must fail the cook, not pour
// a gate:<value> step that waits for no one (GH#6437, PR #6642).
func TestCookFormulaConditionDroppedWaitsForSpawnerErrors(t *testing.T) {
	const name = "dangling-waits-for"
	sg, err := cookFormulaTOMLWithVars(t, name, `formula = "dangling-waits-for"
version = 1
type = "workflow"

[[steps]]
id = "fanout"
title = "Fan out"
type = "task"
condition = "{{fanout_enabled}}"

[[steps]]
id = "gate"
title = "Wait for the fanout"
type = "task"
needs = ["fanout"]
waits_for = "all-children"
`, map[string]string{"fanout_enabled": "false"})
	if err == nil {
		t.Fatalf("cook succeeded although gate's waits_for spawner %q was dropped by its condition; issues %v", "fanout", allIssueIDs(sg))
	}
	if sg != nil {
		t.Errorf("cook returned a subgraph alongside the error")
	}
	if !errors.Is(err, formula.ErrValidation) {
		t.Errorf("errors.Is(err, formula.ErrValidation) = false for %v", err)
	}
	for _, want := range []string{`step "gate": waits_for`, `"fanout"`, "{{fanout_enabled}}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestCookFormulaConditionKeptNeedsTargetSucceeds: when the condition keeps
// fanout, the consumer's needs edge is cooked as before (GH#6437).
func TestCookFormulaConditionKeptNeedsTargetSucceeds(t *testing.T) {
	const name = "kept-needs"
	sg, err := cookFormulaTOMLWithVars(t, name, conditionalFanoutFormula(name, "needs"),
		map[string]string{"fanout_enabled": "true"})
	if err != nil {
		t.Fatalf("cook %q: %v", name, err)
	}
	if got, want := blocksEdgesFrom(sg, name+".consumer"), []string{name + ".fanout"}; !slices.Equal(got, want) {
		t.Errorf("consumer blocks edges = %v, want %v", got, want)
	}
}

// TestCookFormulaLoopContainerExternalNeedsSkipped pins the skip that
// collectDependencies keeps for a reference control flow replaced: ApplyLoops
// replaces the loop container round with its expanded body and
// rewriteLoopDependencies rewrites only in-body references, so after's needs
// on round still names no issue at cook. The pour succeeds without that edge;
// erroring on every dangling reference would break it (GH#6437).
func TestCookFormulaLoopContainerExternalNeedsSkipped(t *testing.T) {
	const name = "loop-external-needs"
	sg, err := cookFormulaTOMLWithVars(t, name, `formula = "loop-external-needs"
version = 1
type = "workflow"

[[steps]]
id = "round"
title = "Rounds"

[steps.loop]
count = 2

[[steps.loop.body]]
id = "work"
title = "Work"

[[steps]]
id = "after"
title = "After the rounds"
needs = ["round"]
`, map[string]string{})
	if err != nil {
		t.Fatalf("cook %q: %v", name, err)
	}
	for _, want := range []string{name + ".round.iter1.work", name + ".round.iter2.work", name + ".after"} {
		if sg.IssueMap[want] == nil {
			t.Errorf("missing issue %q; have %v", want, allIssueIDs(sg))
		}
	}
	if got := blocksEdgesFrom(sg, name+".after"); slices.Contains(got, name+".round") {
		t.Errorf("after blocks edges = %v, want none onto the replaced container %q", got, name+".round")
	}
}

// TestCookFormulaConditionPrimitiveDefaultDropsUnreferencedDeploy: the
// shipped condition primitive (examples/formulas/primitives/
// condition.formula.toml) is the blessed unreferenced-leaf shape. Cooking
// it with its own default (deploy=false) must keep succeeding - the
// dropped-reference rejection must not over-error on a dropped step that
// nothing names (GH#6437).
func TestCookFormulaConditionPrimitiveDefaultDropsUnreferencedDeploy(t *testing.T) {
	sg, err := resolveAndCookFormulaWithVars("condition", []string{primitivesDir(t)}, map[string]string{})
	if err != nil {
		t.Fatalf("cook condition primitive with its deploy=false default: %v", err)
	}
	if hasIssueID(sg, "condition.deploy") {
		t.Errorf("deploy issue present although the default drops the step; issues: %v", allIssueIDs(sg))
	}
	if got := len(childIssueIDs(sg, "condition")); got != 2 {
		t.Errorf("child issues = %d, want 2 (build, test): %v", got, allIssueIDs(sg))
	}
}

// The dropped-reference rejection covers only references the formula wrote.
// Loops, compose.branch and advice add needs edges of their own, and a
// condition that drops one end of such an edge must not fail the pour with a
// remedy that names a line the formula does not have. The tests below pour
// each construct with its condition on and off.

// TestCookFormulaConditionDroppedLastLoopBodyStepKeepsChain: a counted loop
// chains iteration 2's first body step to iteration 1's last one. When the
// last one's condition drops it, the chain goes to what it needed instead.
func TestCookFormulaConditionDroppedLastLoopBodyStepKeepsChain(t *testing.T) {
	const name = "loop-conditional-review"
	const src = `formula = "loop-conditional-review"
version = 1
type = "workflow"

[vars.with_review]
default = "false"

[[steps]]
id = "round"
title = "Rounds"

[steps.loop]
count = 2

[[steps.loop.body]]
id = "work"
title = "Work"

[[steps.loop.body]]
id = "review"
title = "Review"
needs = ["work"]
condition = "{{with_review}}"
`
	tests := []struct {
		name       string
		vars       map[string]string
		wantChain  string // what round.iter2.work blocks on
		wantReview bool
	}{
		{name: "review dropped by the default", vars: map[string]string{}, wantChain: name + ".round.iter1.work"},
		{name: "review kept", vars: map[string]string{"with_review": "true"}, wantChain: name + ".round.iter1.review", wantReview: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sg, err := cookFormulaTOMLWithVars(t, name, src, tt.vars)
			if err != nil {
				t.Fatalf("cook %q: %v", name, err)
			}
			if got, want := blocksEdgesFrom(sg, name+".round.iter2.work"), []string{tt.wantChain}; !slices.Equal(got, want) {
				t.Errorf("round.iter2.work blocks edges = %v, want %v", got, want)
			}
			for _, id := range []string{name + ".round.iter1.review", name + ".round.iter2.review"} {
				if hasIssueID(sg, id) != tt.wantReview {
					t.Errorf("issue %q present = %v, want %v; issues %v", id, !tt.wantReview, tt.wantReview, allIssueIDs(sg))
				}
			}
		})
	}
}

// TestCookFormulaConditionDroppedLastLoopBodyStepRoutesBareGate: a bare
// waits_for gate first in a loop body declares no needs, so from iteration 2
// on it infers its spawner from the chain edge onto iteration 1's last step.
// That edge is bd's own, so when the last step's condition drops it the pour
// still succeeds, and the gate waits for the step the chain is routed to.
//
// The test also pins a known gap that predates the condition filter:
// Validate does not walk loop.body, so this gate escapes the check that
// rejects a bare gate with no needs among steps and children. Iteration 1's
// gate has no chain edge to infer a spawner from, so it is poured as a
// gate:all-children label that waits for no one. Walking loop.body in
// Validate is its own change, because it rejects formulas that cook today;
// that change rejects this formula, and this test changes with it.
func TestCookFormulaConditionDroppedLastLoopBodyStepRoutesBareGate(t *testing.T) {
	const name = "loop-conditional-gate"
	const src = `formula = "loop-conditional-gate"
version = 1
type = "workflow"

[vars.with_review]
default = "false"

[[steps]]
id = "round"
title = "Rounds"

[steps.loop]
count = 2

[[steps.loop.body]]
id = "gate"
title = "Wait for the round before"
waits_for = "all-children"

[[steps.loop.body]]
id = "work"
title = "Work"

[[steps.loop.body]]
id = "review"
title = "Review"
needs = ["work"]
condition = "{{with_review}}"
`
	tests := []struct {
		name        string
		vars        map[string]string
		wantSpawner string // what round.iter2.gate waits for
	}{
		{name: "review dropped by the default", vars: map[string]string{}, wantSpawner: name + ".round.iter1.work"},
		{name: "review kept", vars: map[string]string{"with_review": "true"}, wantSpawner: name + ".round.iter1.review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sg, err := cookFormulaTOMLWithVars(t, name, src, tt.vars)
			if err != nil {
				t.Fatalf("cook %q: %v", name, err)
			}
			if got, want := waitsForEdgesFrom(sg, name+".round.iter2.gate"), []string{tt.wantSpawner}; !slices.Equal(got, want) {
				t.Errorf("round.iter2.gate waits-for edges = %v, want %v; deps %v", got, want, allDeps(sg))
			}

			gate1ID := name + ".round.iter1.gate"
			gate1 := sg.IssueMap[gate1ID]
			if gate1 == nil {
				t.Fatalf("issue %s missing; issues %v", gate1ID, allIssueIDs(sg))
			}
			if !slices.Contains(gate1.Labels, "gate:all-children") {
				t.Errorf("round.iter1.gate labels = %v, want gate:all-children", gate1.Labels)
			}
			if got := slices.Concat(waitsForEdgesFrom(sg, gate1ID), blocksEdgesFrom(sg, gate1ID)); len(got) != 0 {
				t.Errorf("round.iter1.gate waits for %v, want no one (the known gap above); deps %v", got, allDeps(sg))
			}
		})
	}
}

// TestCookFormulaConditionBareGateRoutedToNothingErrors: when the dropped last
// body step needs nothing, the chain edge that iteration 2's bare gate takes
// its spawner from has nowhere to go, and the gate would be poured as a
// gate:all-children label that waits for no one. The pour fails instead,
// naming the gate, the dropped step and its condition.
func TestCookFormulaConditionBareGateRoutedToNothingErrors(t *testing.T) {
	const name = "loop-conditional-gate-only"
	const src = `formula = "loop-conditional-gate-only"
version = 1
type = "workflow"

[vars.with_review]
default = "false"

[[steps]]
id = "round"
title = "Rounds"

[steps.loop]
count = 2

[[steps.loop.body]]
id = "gate"
title = "Wait for the round before"
waits_for = "all-children"

[[steps.loop.body]]
id = "review"
title = "Review"
condition = "{{with_review}}"
`
	t.Run("review dropped by the default", func(t *testing.T) {
		sg, err := cookFormulaTOMLWithVars(t, name, src, map[string]string{})
		if err == nil {
			t.Fatalf("cook succeeded although round.iter2.gate's spawner was dropped with nothing to route to; round.iter2.gate waits-for edges %v",
				waitsForEdgesFrom(sg, name+".round.iter2.gate"))
		}
		if sg != nil {
			t.Errorf("cook returned a subgraph alongside the error")
		}
		if !errors.Is(err, formula.ErrValidation) {
			t.Errorf("errors.Is(err, formula.ErrValidation) = false for %v", err)
		}
		for _, want := range []string{`step "round.iter2.gate": waits_for "all-children"`, `"round.iter1.review"`, "{{with_review}}", "nothing to wait for"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("review kept", func(t *testing.T) {
		sg, err := cookFormulaTOMLWithVars(t, name, src, map[string]string{"with_review": "true"})
		if err != nil {
			t.Fatalf("cook %q: %v", name, err)
		}
		if got, want := waitsForEdgesFrom(sg, name+".round.iter2.gate"), []string{name + ".round.iter1.review"}; !slices.Equal(got, want) {
			t.Errorf("round.iter2.gate waits-for edges = %v, want %v; deps %v", got, want, allDeps(sg))
		}
	})
}

// TestCookFormulaConditionDroppedAdviceTargetDropsItsAdvice: advice steps
// take their target's condition, so when deploy is dropped its before and
// after steps go with it, instead of failing the pour on verify-deploy's
// edge onto deploy or pouring them around a step that is not there.
func TestCookFormulaConditionDroppedAdviceTargetDropsItsAdvice(t *testing.T) {
	const name = "advice-conditional-deploy"
	const src = `formula = "advice-conditional-deploy"
version = 1
type = "workflow"

[vars.deploy]
default = "false"

[[steps]]
id = "build"
title = "Build"

[[steps]]
id = "deploy"
title = "Deploy"
needs = ["build"]
condition = "{{deploy}} == true"

[[advice]]
target = "deploy"

[advice.before]
id = "approve-{step.id}"
title = "Approve {step.title}"

[advice.after]
id = "verify-{step.id}"
title = "Verify {step.title}"
`
	adviceIssues := []string{name + ".approve-deploy", name + ".deploy", name + ".verify-deploy"}

	t.Run("deploy dropped by the default", func(t *testing.T) {
		sg, err := cookFormulaTOMLWithVars(t, name, src, map[string]string{})
		if err != nil {
			t.Fatalf("cook %q: %v", name, err)
		}
		if !hasIssueID(sg, name+".build") {
			t.Errorf("build missing; issues %v", allIssueIDs(sg))
		}
		for _, id := range adviceIssues {
			if hasIssueID(sg, id) {
				t.Errorf("issue %q poured although deploy was dropped; issues %v", id, allIssueIDs(sg))
			}
		}
	})

	t.Run("deploy kept", func(t *testing.T) {
		sg, err := cookFormulaTOMLWithVars(t, name, src, map[string]string{"deploy": "true"})
		if err != nil {
			t.Fatalf("cook %q: %v", name, err)
		}
		for _, id := range adviceIssues {
			if !hasIssueID(sg, id) {
				t.Errorf("issue %q missing; issues %v", id, allIssueIDs(sg))
			}
		}
		if got, want := blocksEdgesFrom(sg, name+".deploy"), []string{name + ".build", name + ".approve-deploy"}; !slices.Equal(got, want) {
			t.Errorf("deploy blocks edges = %v, want %v", got, want)
		}
		if got, want := blocksEdgesFrom(sg, name+".verify-deploy"), []string{name + ".deploy"}; !slices.Equal(got, want) {
			t.Errorf("verify-deploy blocks edges = %v, want %v", got, want)
		}
	})
}

// TestCookFormulaConditionDroppedBranchStepJoinWaitsForTheRest: compose.branch
// makes the join need every parallel step. When e2e is dropped, deploy waits
// for the parallel step that stays, and through e2e for prepare.
func TestCookFormulaConditionDroppedBranchStepJoinWaitsForTheRest(t *testing.T) {
	const name = "branch-conditional-e2e"
	const src = `formula = "branch-conditional-e2e"
version = 1
type = "workflow"

[vars.run_e2e]
default = "false"

[[steps]]
id = "prepare"
title = "Prepare"

[[steps]]
id = "unit"
title = "Unit tests"

[[steps]]
id = "e2e"
title = "End-to-end tests"
condition = "{{run_e2e}}"

[[steps]]
id = "deploy"
title = "Deploy"

[[compose.branch]]
from = "prepare"
steps = ["unit", "e2e"]
join = "deploy"
`
	tests := []struct {
		name       string
		vars       map[string]string
		wantDeploy []string // what deploy blocks on
		wantE2E    bool
	}{
		{name: "e2e dropped by the default", vars: map[string]string{}, wantDeploy: []string{name + ".unit", name + ".prepare"}},
		{name: "e2e kept", vars: map[string]string{"run_e2e": "true"}, wantDeploy: []string{name + ".unit", name + ".e2e"}, wantE2E: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sg, err := cookFormulaTOMLWithVars(t, name, src, tt.vars)
			if err != nil {
				t.Fatalf("cook %q: %v", name, err)
			}
			if got := blocksEdgesFrom(sg, name+".deploy"); !slices.Equal(got, tt.wantDeploy) {
				t.Errorf("deploy blocks edges = %v, want %v", got, tt.wantDeploy)
			}
			if got, want := blocksEdgesFrom(sg, name+".unit"), []string{name + ".prepare"}; !slices.Equal(got, want) {
				t.Errorf("unit blocks edges = %v, want %v", got, want)
			}
			if hasIssueID(sg, name+".e2e") != tt.wantE2E {
				t.Errorf("e2e present = %v, want %v; issues %v", !tt.wantE2E, tt.wantE2E, allIssueIDs(sg))
			}
		})
	}
}
