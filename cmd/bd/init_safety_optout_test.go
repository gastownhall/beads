package main

// The --remote "" opt-out the unverified-remote refusal advertises
// (gascity#7665): following the message's remedy must initialize a fresh
// local database without probing — or wiring — the ambient remote.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitReinitLocalUnverifiedOriginRemoteOptOutSucceeds walks the
// recovery path the unverified-remote refusal names, end to end:
//
//  1. `bd init --reinit-local` against an unreachable git origin refuses
//     (exit 10) and advertises the explicit-empty-remote remedy.
//  2. Re-running with `--remote ""` succeeds: a fresh local database, no
//     probe of the ambient origin, and no sync.remote/configured Dolt
//     remote pointing at it.
//
// The first step's refusal is covered by
// TestInitReinitLocalWithUnreachableGitOriginRefusesUnverified; this test
// pins that the advertised remedy actually works — a message that steers
// users into a dead end would be its own bug.
func TestInitReinitLocalUnverifiedOriginRemoteOptOutSucceeds(t *testing.T) {
	bdBin := buildBDForInitTests(t)

	originURL := filepath.Join(t.TempDir(), "does-not-exist.git")
	workDir := t.TempDir()
	runGitForBootstrapTest(t, workDir, "init", "-b", "main")
	runGitForBootstrapTest(t, workDir, "config", "core.hooksPath", ".git/hooks")
	runGitForBootstrapTest(t, workDir, "remote", "add", "origin", originURL)

	homeDir := t.TempDir() // must differ from workDir: metrics.go caches to $HOME/.beads
	baseArgs := []string{"init", "--reinit-local", "--prefix", "uv", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents"}

	// Step 1: the refusal, and the remedy it advertises.
	refuse := exec.Command(bdBin, baseArgs...)
	refuse.Dir = workDir
	refuse.Env = hermeticInitEnv(homeDir)
	var refuteOut bytes.Buffer
	refuse.Stdout = &refuteOut
	refuse.Stderr = &refuteOut
	if err := refuse.Run(); err == nil {
		t.Fatalf("bd init --reinit-local succeeded against an unreachable origin; expected refusal.\noutput:\n%s", refuteOut.String())
	} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != ExitRemoteDivergenceRefused {
		t.Fatalf("bd init --reinit-local: want exit %d, got %v\noutput:\n%s", ExitRemoteDivergenceRefused, err, refuteOut.String())
	}
	if !strings.Contains(refuteOut.String(), `--remote ""`) {
		t.Errorf("unverified-remote refusal does not advertise the --remote \"\" remedy:\n%s", refuteOut.String())
	}

	// Step 2: follow the remedy. It must succeed without touching the
	// ambient remote.
	optOut := exec.Command(bdBin, append(append([]string{}, baseArgs...), "--remote", "")...)
	optOut.Dir = workDir
	optOut.Env = hermeticInitEnv(homeDir)
	var optOutErr bytes.Buffer
	optOut.Stderr = &optOutErr
	if err := optOut.Run(); err != nil {
		t.Fatalf("bd init --reinit-local --remote \"\" failed after following the advertised remedy: %v\nstderr:\n%s", err, optOutErr.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, ".beads")); err != nil {
		t.Fatalf(".beads should have been created by the opt-out init: %v", err)
	}

	// No probing: the probe warning must not appear in the successful run.
	if strings.Contains(optOutErr.String(), "could not verify refs/dolt/data") {
		t.Errorf("the --remote \"\" opt-out still probed the ambient origin:\n%s", optOutErr.String())
	}
	// No configuring: init must not have written the ambient origin into
	// the workspace config as the sync remote.
	configBytes, err := os.ReadFile(filepath.Join(workDir, ".beads", "config.yaml"))
	if err == nil && strings.Contains(string(configBytes), "sync.remote") {
		t.Errorf("the --remote \"\" opt-out configured a sync remote anyway:\n%s", configBytes)
	}
	// Nor as a Dolt remote in any database's repo state (server mode under
	// .beads/dolt, embedded under .beads/embeddeddolt).
	_ = filepath.WalkDir(filepath.Join(workDir, ".beads"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.Name() != "repo_state.json" {
			return nil //nolint:nilerr // best-effort scan; a missing file is the passing case
		}
		state, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(state), "does-not-exist") {
			t.Errorf("the --remote \"\" opt-out wired the ambient origin as a Dolt remote (%s):\n%s", path, state)
		}
		return nil
	})
}
