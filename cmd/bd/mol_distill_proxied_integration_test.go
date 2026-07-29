//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxiedServerMolDistill(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("distills_epic_to_formula_file", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mds")
		epic := bdProxiedCreate(t, bd, p.dir, "Ship feature auth", "--type", "epic")
		bdProxiedCreate(t, bd, p.dir, "Implement auth", "--type", "task", "--parent", epic.ID)
		bdProxiedCreate(t, bd, p.dir, "Test auth", "--type", "task", "--parent", epic.ID)

		outputDir := filepath.Join(p.dir, "out")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatalf("mkdir output dir: %v", err)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "distill", epic.ID, "auth-workflow", "--output", outputDir, "--json")
		if err != nil {
			t.Fatalf("bd mol distill --json: %v\n%s", err, out)
		}
		var got struct {
			FormulaName string `json:"formula_name"`
			FormulaPath string `json:"formula_path"`
			Steps       int    `json:"steps"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.FormulaName != "auth-workflow" {
			t.Errorf("formula_name = %s, want auth-workflow", got.FormulaName)
		}
		if got.Steps != 2 {
			t.Errorf("steps = %d, want 2", got.Steps)
		}
		if _, err := os.Stat(got.FormulaPath); err != nil {
			t.Errorf("expected formula file at %s: %v", got.FormulaPath, err)
		}
	})

	t.Run("dry_run_writes_nothing", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mdsd")
		epic := bdProxiedCreate(t, bd, p.dir, "Dry run epic", "--type", "epic")
		bdProxiedCreate(t, bd, p.dir, "Dry run step", "--type", "task", "--parent", epic.ID)

		outputDir := filepath.Join(p.dir, "dryout")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatalf("mkdir output dir: %v", err)
		}

		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "distill", epic.ID, "dry-workflow", "--output", outputDir, "--dry-run")
		if err != nil {
			t.Fatalf("bd mol distill --dry-run: %v\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "Dry run") {
			t.Errorf("expected dry-run preview, got: %s", stdout)
		}
		entries, err := os.ReadDir(outputDir)
		if err != nil {
			t.Fatalf("read output dir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected no files written under --dry-run, found: %+v", entries)
		}
	})

	t.Run("var_flag_both_syntaxes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mdsv")
		epic := bdProxiedCreate(t, bd, p.dir, "Release feature-auth", "--type", "epic")
		bdProxiedCreate(t, bd, p.dir, "Deploy feature-auth to prod", "--type", "task", "--parent", epic.ID)

		outputDir := filepath.Join(p.dir, "varout")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatalf("mkdir output dir: %v", err)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "mol", "distill", epic.ID, "var-workflow-1",
			"--output", outputDir, "--var", "branch=feature-auth", "--json")
		if err != nil {
			t.Fatalf("bd mol distill (spawn-style var) --json: %v\n%s", err, out)
		}
		var got1 struct {
			Variables []string `json:"variables"`
		}
		if err := json.Unmarshal(out, &got1); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(got1.Variables) != 1 || got1.Variables[0] != "branch" {
			t.Errorf("variables = %+v, want [branch]", got1.Variables)
		}

		out2, err := bdProxiedRun(t, bd, p.dir, "mol", "distill", epic.ID, "var-workflow-2",
			"--output", outputDir, "--var", "feature-auth=branch", "--json")
		if err != nil {
			t.Fatalf("bd mol distill (substitution-style var) --json: %v\n%s", err, out2)
		}
		var got2 struct {
			Variables []string `json:"variables"`
		}
		if err := json.Unmarshal(out2, &got2); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out2)
		}
		if len(got2.Variables) != 1 || got2.Variables[0] != "branch" {
			t.Errorf("variables = %+v, want [branch]", got2.Variables)
		}
	})
}
