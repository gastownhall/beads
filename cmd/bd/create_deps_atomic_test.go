package main

// Regression tests for create-time dependency atomicity.
//
// `bd create --deps` used to commit the issue in one transaction and then add
// each dependency edge in its own transaction with warn-only error handling
// (WarnError + exit 0). A failed dep-add therefore exited 0 with valid JSON
// and left a dep-less bead behind — permanently READY, so an orchestrator
// polling `bd ready` would dispatch it before its prerequisites existed.
//
// The contract under test: the create and every requested dependency edge
// (--deps, --parent, --waits-for) commit in ONE transaction. Any dep failure
// fails the command with a nonzero exit and rolls back the create.
//
// These tests run the real bd binary against an isolated Dolt-backed
// workspace. The binary is built with the gms_pure_go embedded-Dolt engine
// (see buildBDForInitTests), so the tests need no external Dolt server and work
// in both cgo and pure-Go builds.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// createDepsTestEnv returns a hermetic environment for the subprocess bd
// commands. It strips ambient BEADS_/BD_ configuration so a developer or CI
// shell pointing at a shared Dolt server (BEADS_DOLT_SERVER_*) or a real
// workspace (BEADS_DIR) cannot leak in, then pins BEADS_DIR at the isolated
// per-test workspace and keeps the child non-interactive.
func createDepsTestEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") || strings.HasPrefix(e, "BD_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"BEADS_DIR="+filepath.Join(dir, ".beads"),
		"BD_NON_INTERACTIVE=1",
	)
}

// runCreateDepsBD runs bd and returns stdout only. Warnings (e.g. the
// beads.role notice) go to stderr and must never leak into parsed output
// like --silent issue IDs.
func runCreateDepsBD(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = createDepsTestEnv(dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bd %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, out, stderr.String())
	}
	return string(out)
}

// runCreateDepsBDRaw runs bd and returns combined output plus the exit error,
// for asserting on failure output.
func runCreateDepsBDRaw(bd, dir string, args ...string) (string, error) {
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = createDepsTestEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// createDepsIssueTitles returns the set of issue titles visible in `bd list --json`.
func createDepsIssueTitles(t *testing.T, bd, dir string) map[string]bool {
	t.Helper()
	out := runCreateDepsBD(t, bd, dir, "list", "--json")
	start := strings.Index(out, "[")
	if start < 0 {
		// An empty workspace may print a non-JSON "no issues" notice.
		return map[string]bool{}
	}
	var issues []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &issues); err != nil {
		t.Fatalf("parse bd list --json: %v\n%s", err, out)
	}
	titles := make(map[string]bool, len(issues))
	for _, iss := range issues {
		titles[iss.Title] = true
	}
	return titles
}

// createDepsExtractID pulls the created issue ID out of `bd create --json`
// output, which may carry warning lines before the JSON object.
func createDepsExtractID(t *testing.T, out string) string {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in create output:\n%s", out)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &issue); err != nil {
		t.Fatalf("parse create --json output: %v\n%s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("create --json output has empty id:\n%s", out)
	}
	return issue.ID
}

func TestCreateDepsAtomicity(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	runCreateDepsBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "test",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	blocker := strings.TrimSpace(runCreateDepsBD(t, bd, dir, "create", "existing blocker", "--silent"))
	if blocker == "" {
		t.Fatal("blocker create returned empty ID")
	}

	t.Run("failed_dep_add_is_fatal_and_rolls_back_create", func(t *testing.T) {
		out, err := runCreateDepsBDRaw(bd, dir, "create", "orphan candidate", "--json",
			"--deps", "depends-on:test-missing1")
		if err == nil {
			t.Errorf("create with unresolvable dep exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "test-missing1") {
			t.Errorf("error output should name the failing dependency target test-missing1, got:\n%s", out)
		}
		if createDepsIssueTitles(t, bd, dir)["orphan candidate"] {
			t.Error("issue \"orphan candidate\" persisted despite failed dep-add (create not rolled back)")
		}
	})

	t.Run("one_failing_dep_rolls_back_valid_deps_and_create", func(t *testing.T) {
		out, err := runCreateDepsBDRaw(bd, dir, "create", "partial dep issue", "--json",
			"--deps", "depends-on:"+blocker+",depends-on:test-missing2")
		if err == nil {
			t.Errorf("create with one unresolvable dep exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "test-missing2") {
			t.Errorf("error output should name the failing dependency target test-missing2, got:\n%s", out)
		}
		if createDepsIssueTitles(t, bd, dir)["partial dep issue"] {
			t.Error("issue \"partial dep issue\" persisted despite failed dep-add (create not rolled back)")
		}
	})

	t.Run("waits_for_missing_spawner_is_fatal_and_rolls_back", func(t *testing.T) {
		out, err := runCreateDepsBDRaw(bd, dir, "create", "waits-for orphan", "--json",
			"--waits-for", "test-missing3")
		if err == nil {
			t.Errorf("create with unresolvable --waits-for exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "test-missing3") {
			t.Errorf("error output should name the failing waits-for target test-missing3, got:\n%s", out)
		}
		if createDepsIssueTitles(t, bd, dir)["waits-for orphan"] {
			t.Error("issue \"waits-for orphan\" persisted despite failed waits-for add (create not rolled back)")
		}
	})

	// Defect A: --waits-for-gate without --waits-for silently no-ops.
	// Before the fix, both commands below exited 0 and created a normal bead
	// with no gate or dependency wired, regardless of the gate value (including
	// invalid values). An operator who wrote --waits-for-gate believing they had
	// armed a hold got a dispatchable bead instead.
	t.Run("waits_for_gate_without_waits_for_is_rejected", func(t *testing.T) {
		for _, gate := range []string{"all-children", "TOTALLY-BOGUS"} {
			title := "gate-no-spawner-" + gate
			out, err := runCreateDepsBDRaw(bd, dir, "create", title, "--json",
				"--waits-for-gate", gate)
			if err == nil {
				t.Errorf("create --waits-for-gate %s (no --waits-for) exited 0 (was silently ignored); output:\n%s", gate, out)
			}
			if createDepsIssueTitles(t, bd, dir)[title] {
				t.Errorf("issue %q persisted despite rejected command (should not exist)", title)
			}
		}
	})

	// Defect B: with both --waits-for and --waits-for-gate INVALID, the bead
	// was written before validation ran, leaving a dep-less dispatchable orphan.
	// After the refactor in create_atomic.go, validation runs pre-write; this
	// test documents the contract and guards against regressions.
	t.Run("invalid_waits_for_gate_value_is_rejected_before_write", func(t *testing.T) {
		out, err := runCreateDepsBDRaw(bd, dir, "create", "invalid-gate-probe", "--json",
			"--waits-for", blocker, "--waits-for-gate", "TOTALLY-BOGUS")
		if err == nil {
			t.Errorf("create with invalid --waits-for-gate exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "TOTALLY-BOGUS") {
			t.Errorf("error should name the invalid gate value; got:\n%s", out)
		}
		if createDepsIssueTitles(t, bd, dir)["invalid-gate-probe"] {
			t.Error("issue \"invalid-gate-probe\" persisted despite failed command (half-write: bead created before validation)")
		}
	})

	// 2026-07-23 maintainer review of PR gastownhall/beads#4918 (should-fix 1):
	// --waits-for-gate without --waits-for was only rejected on the single-issue
	// create path. The non-proxied --file and --graph dispatch in create.go's
	// RunE called createIssuesFromMarkdown/createIssuesFromGraph directly,
	// bypassing rejectSingleIssueFlagsForMarkdown/rejectSingleIssueFlagsForGraph
	// (which already list "waits-for"/"waits-for-gate" in singleIssueOnlyFlags),
	// so the same silently-ignored gate defect survived on the batch routes.
	t.Run("waits_for_gate_rejected_on_file_batch_route", func(t *testing.T) {
		mdFile := filepath.Join(dir, "batch-plan.md")
		if err := os.WriteFile(mdFile, []byte("# Batch issue\n\nDescription\n"), 0o600); err != nil {
			t.Fatalf("write markdown plan: %v", err)
		}
		out, err := runCreateDepsBDRaw(bd, dir, "create", "--file", mdFile, "--waits-for-gate", "all-children")
		if err == nil {
			t.Errorf("create --file with --waits-for-gate (no --waits-for) exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "waits-for-gate") {
			t.Errorf("error should name the rejected --waits-for-gate flag, got:\n%s", out)
		}
	})

	t.Run("waits_for_gate_rejected_on_graph_batch_route", func(t *testing.T) {
		graphFile := filepath.Join(dir, "batch-plan.json")
		plan := `{"nodes":[{"key":"root","title":"Graph batch root","type":"task"}]}`
		if err := os.WriteFile(graphFile, []byte(plan), 0o600); err != nil {
			t.Fatalf("write graph plan: %v", err)
		}
		out, err := runCreateDepsBDRaw(bd, dir, "create", "--graph", graphFile, "--waits-for-gate", "all-children")
		if err == nil {
			t.Errorf("create --graph with --waits-for-gate (no --waits-for) exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "waits-for-gate") {
			t.Errorf("error should name the rejected --waits-for-gate flag, got:\n%s", out)
		}
		if createDepsIssueTitles(t, bd, dir)["Graph batch root"] {
			t.Error("graph node \"Graph batch root\" persisted despite rejected --waits-for-gate combo")
		}
	})

	// 2026-07-23 maintainer review of PR gastownhall/beads#4918 (should-fix 2):
	// store.GetNextChildID reserved (and committed) a child ID before
	// buildWaitsFor validation ran, so a rejected `--parent X --waits-for-gate
	// BOGUS` (no --waits-for) still burned a child ID even though the create
	// itself failed. Validation now runs before the reservation, so a failed
	// attempt must leave no gap in the child numbering.
	t.Run("waits_for_gate_validation_failure_does_not_burn_child_id", func(t *testing.T) {
		parentOut := runCreateDepsBD(t, bd, dir, "create", "child-id-burn-parent", "--json")
		parentID := createDepsExtractID(t, parentOut)

		out, err := runCreateDepsBDRaw(bd, dir, "create", "should-not-exist-child", "--json",
			"--parent", parentID, "--waits-for-gate", "all-children")
		if err == nil {
			t.Errorf("create --parent with --waits-for-gate (no --waits-for) exited 0; output:\n%s", out)
		}

		childOut := runCreateDepsBD(t, bd, dir, "create", "first-real-child", "--json", "--parent", parentID)
		childID := createDepsExtractID(t, childOut)

		wantChildID := parentID + ".1"
		if childID != wantChildID {
			t.Errorf("child ID = %q, want %q (rejected --waits-for-gate attempt burned a child ID)", childID, wantChildID)
		}
	})

	t.Run("ready_never_offers_a_failed_create", func(t *testing.T) {
		out := runCreateDepsBD(t, bd, dir, "ready", "--json")
		for _, title := range []string{
			"orphan candidate", "partial dep issue", "waits-for orphan",
			"gate-no-spawner-all-children", "gate-no-spawner-TOTALLY-BOGUS", "invalid-gate-probe",
		} {
			if strings.Contains(out, title) {
				t.Errorf("bd ready offers %q, a bead whose create should have been rolled back:\n%s", title, out)
			}
		}
	})

	t.Run("happy_path_deps_created_atomically", func(t *testing.T) {
		out := runCreateDepsBD(t, bd, dir, "create", "happy child", "--json",
			"--deps", "depends-on:"+blocker)
		child := createDepsExtractID(t, out)

		depOut := runCreateDepsBD(t, bd, dir, "dep", "list", child, "--json")
		if !strings.Contains(depOut, blocker) {
			t.Errorf("dep list %s should include %s:\n%s", child, blocker, depOut)
		}

		readyOut := runCreateDepsBD(t, bd, dir, "ready", "--json")
		if strings.Contains(readyOut, child) {
			t.Errorf("child %s blocked by open %s must not be ready:\n%s", child, blocker, readyOut)
		}
	})

	t.Run("invalid_dep_type_rejected_before_create", func(t *testing.T) {
		out, err := runCreateDepsBDRaw(bd, dir, "create", "bad dep type issue", "--json",
			"--deps", "bogus-type:"+blocker)
		if err == nil {
			t.Errorf("create with unknown dep type exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, "unknown dependency type") {
			t.Errorf("expected 'unknown dependency type' error, got:\n%s", out)
		}
		if createDepsIssueTitles(t, bd, dir)["bad dep type issue"] {
			t.Error("issue \"bad dep type issue\" persisted despite invalid dep type")
		}
	})

	// GH#4626: --deps discovered-from:X,blocked-by:X used to silently keep
	// only one edge, because dependency uniqueness is per (issue_id, target),
	// not per type. Two DIFFERENT types on the same target must now be
	// rejected before create runs (real-binary regression: exit code and
	// absence of an orphan, which a direct parseDepSpecs unit test can't
	// prove).
	t.Run("multi_type_same_target_rejected_before_create_no_orphan", func(t *testing.T) {
		out, err := runCreateDepsBDRaw(bd, dir, "create", "multi-type collision issue", "--json",
			"--deps", "discovered-from:"+blocker, "--deps", "blocked-by:"+blocker)
		if err == nil {
			t.Errorf("create with multi-type same-target --deps exited 0; output:\n%s", out)
		}
		if !strings.Contains(out, blocker) {
			t.Errorf("error output should name the colliding target %s, got:\n%s", blocker, out)
		}
		if createDepsIssueTitles(t, bd, dir)["multi-type collision issue"] {
			t.Error("issue \"multi-type collision issue\" persisted despite rejected multi-type same-target --deps")
		}
	})

	// Same target, same normalized type (blocked-by and depends-on both
	// alias to DepBlocks with no swap) must NOT be rejected: storage already
	// treats a repeated identical edge as idempotent, so --deps should
	// dedupe rather than hard-fail.
	t.Run("duplicate_identical_dep_is_deduped_not_rejected", func(t *testing.T) {
		out := runCreateDepsBD(t, bd, dir, "create", "deduped dep issue", "--json",
			"--deps", "blocked-by:"+blocker, "--deps", "depends-on:"+blocker)
		child := createDepsExtractID(t, out)

		depOut := runCreateDepsBD(t, bd, dir, "dep", "list", child, "--json")
		if !strings.Contains(depOut, blocker) {
			t.Errorf("dep list %s should include %s:\n%s", child, blocker, depOut)
		}
	})
}
