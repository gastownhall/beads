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

	t.Run("ready_never_offers_a_failed_create", func(t *testing.T) {
		out := runCreateDepsBD(t, bd, dir, "ready", "--json")
		for _, title := range []string{"orphan candidate", "partial dep issue", "waits-for orphan"} {
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
}
