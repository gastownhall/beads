//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func doltHeadCommit(t *testing.T, dir string, env []string) string {
	t.Helper()
	out, err := runBDExecAllowErrorWithEnv(t, dir, env, "--json", "vc", "status")
	if err != nil {
		t.Fatalf("bd vc status failed: %v\n%s", err, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		// Some commands can emit warnings; try from first '{'
		if idx := strings.Index(out, "{"); idx >= 0 {
			if err2 := json.Unmarshal([]byte(out[idx:]), &m); err2 != nil {
				t.Fatalf("failed to parse vc status JSON: %v\n%s", err2, out)
			}
		} else {
			t.Fatalf("failed to parse vc status JSON: %v\n%s", err, out)
		}
	}
	commit, _ := m["commit"].(string)
	if commit == "" {
		t.Fatalf("missing commit in vc status output:\n%s", out)
	}
	return commit
}

func runCommandInDirCombinedOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- test helper executes trusted binaries
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func findDoltRepoDir(t *testing.T, dir string) string {
	t.Helper()

	// Embedded driver may create either:
	// - a dolt repo directly at .beads/dolt/
	// - a dolt environment at .beads/dolt/ with a db subdir containing .dolt/
	base := filepath.Join(dir, ".beads", "dolt")
	candidates := []string{
		base,
		filepath.Join(base, "beads"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, ".dolt")); err == nil {
			return c
		}
	}

	var found string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".dolt" {
			found = filepath.Dir(path)
			return fs.SkipDir
		}
		return nil
	})
	if found == "" {
		t.Fatalf("could not find Dolt repo dir under %s", base)
	}
	return found
}

func doltHeadAuthor(t *testing.T, dir string) string {
	t.Helper()

	doltDir := findDoltRepoDir(t, dir)
	out, err := runCommandInDirCombinedOutput(doltDir, "dolt", "log", "-n", "1")
	if err != nil {
		t.Fatalf("dolt log failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Author:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Author:"))
		}
	}
	t.Fatalf("missing Author in dolt log output:\n%s", out)
	return ""
}

func TestDoltAutoCommit_On_WritesAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1",
	}

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	// A write command should create a new Dolt commit (auto-commit default is on in embedded mode).
	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "create", "Auto-commit test", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after == before {
		t.Fatalf("expected Dolt HEAD to change after write; before=%s after=%s", before, after)
	}

	// Commit author should be deterministic (not the authenticated SQL user like root@%).
	expectedName := os.Getenv("GIT_AUTHOR_NAME")
	if expectedName == "" {
		expectedName = "beads"
	}
	expectedEmail := os.Getenv("GIT_AUTHOR_EMAIL")
	if expectedEmail == "" {
		expectedEmail = "beads@local"
	}
	expectedAuthor := fmt.Sprintf("%s <%s>", expectedName, expectedEmail)
	if got := doltHeadAuthor(t, tmpDir); got != expectedAuthor {
		t.Fatalf("expected Dolt commit author %q, got %q", expectedAuthor, got)
	}

	// A read-only command should not create another commit.
	out, err = runBDExecAllowErrorWithEnv(t, tmpDir, env, "list")
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, out)
	}
	afterList := doltHeadCommit(t, tmpDir, env)
	if afterList != after {
		t.Fatalf("expected Dolt HEAD unchanged after read command; before=%s after=%s", after, afterList)
	}
}

func TestDoltAutoCommit_Off_DoesNotAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1",
	}

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	// Disable auto-commit via persistent flag (must come before subcommand).
	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "off", "create", "Auto-commit off", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after != before {
		t.Fatalf("expected Dolt HEAD unchanged with auto-commit off; before=%s after=%s", before, after)
	}
}
