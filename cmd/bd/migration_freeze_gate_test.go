// Tests for the MIGRATION-FREEZE write gate (dc-6jaq): bd write commands must
// refuse to run while a MIGRATION-FREEZE sentinel sits at the town root, the
// same gate the gt CLI already applies to gt mail send/nudge/sling/assign.
//
// This file MUST NOT carry a cgo build tag: it exercises the default sqlite
// backend via a bd binary built with the gms_pure_go tag (mirrors
// update_multi_id_exit_test.go's convention).

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// migrationFreezeEnv returns a hermetic environment for bd subprocess runs:
// no inherited BEADS_* variables, HOME pinned to the test dir, metrics and
// daemons disabled.
func migrationFreezeEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") || strings.HasPrefix(e, "GT_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"HOME="+dir,
		"USERPROFILE="+dir,
		"BD_NON_INTERACTIVE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_NO_DAEMON=1",
		"BEADS_DOLT_AUTO_START=0",
	)
}

// runBDMigrationFreeze runs the bd binary and returns stdout, stderr, and the
// exit code. Only a failure to launch the process fails the test; nonzero
// exits are returned to the caller for assertion.
func runBDMigrationFreeze(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runBDMigrationFreezeWithEnv(t, bd, dir, nil, args...)
}

// runBDMigrationFreezeWithEnv is runBDMigrationFreeze plus caller-supplied
// environment variables layered on top of the hermetic base env (e.g.
// BD_DEBUG=1, for tests that need to observe debug.Logf output).
func runBDMigrationFreezeWithEnv(t *testing.T, bd, dir string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(migrationFreezeEnv(dir), extraEnv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("bd %v did not run: %v", args, err)
		}
		return outBuf.String(), errBuf.String(), ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), 0
}

// setupMigrationFreezeWorkspace builds bd and initializes a fresh sqlite-
// backed database in a temp dir that also carries mayor/town.json, so the
// same directory doubles as a bd workspace and a fake town root — findTownRoot
// finds it at walk-up distance 0.
func setupMigrationFreezeWorkspace(t *testing.T) (bd, dir string) {
	t.Helper()
	bd = buildBDForInitTests(t)
	dir = t.TempDir()
	runGitForBootstrapTest(t, dir, "init", "-q")
	runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")

	if err := os.MkdirAll(filepath.Join(dir, "mayor"), 0755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing mayor/town.json: %v", err)
	}

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir,
		"init", "--prefix", "test", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	if code != 0 {
		t.Fatalf("bd init failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	return bd, dir
}

// freezeTown writes a MIGRATION-FREEZE sentinel at dir (the fake town root).
func freezeTown(t *testing.T, dir, operator, reason string) {
	t.Helper()
	content := operator + "\t2026-08-16T12:00:00Z\t" + reason + "\n"
	if err := os.WriteFile(filepath.Join(dir, "MIGRATION-FREEZE"), []byte(content), 0644); err != nil {
		t.Fatalf("writing MIGRATION-FREEZE: %v", err)
	}
}

func TestCreateBlockedDuringMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	freezeTown(t, dir, "mayor", "dolt v2 migration")

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "should not be created", "-p", "2")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
	}
	if !strings.Contains(stderr, "mayor") {
		t.Errorf("stderr missing operator 'mayor':\n%s", stderr)
	}
	if !strings.Contains(stderr, "dolt v2 migration") {
		t.Errorf("stderr missing reason 'dolt v2 migration':\n%s", stderr)
	}
	if !strings.Contains(stderr, "gt migrate thaw") {
		t.Errorf("stderr missing recovery hint 'gt migrate thaw':\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when blocked, got:\n%s", stdout)
	}
}

func TestUpdateBlockedDuringMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	// Create the issue BEFORE freezing — create itself isn't under test here.
	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "pre-freeze issue", "-p", "2", "--json")
	if code != 0 {
		t.Fatalf("setup bd create failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &issue); err != nil || issue.ID == "" {
		t.Fatalf("parsing create --json output: %v\n%s", err, stdout)
	}

	freezeTown(t, dir, "athos", "server migration in progress")

	stdout, stderr, code = runBDMigrationFreeze(t, bd, dir, "update", issue.ID, "-p", "1")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
	}
}

// TestCreateNotBlockedWithoutFreeze is the regression-safety check: normal bd
// usage (no MIGRATION-FREEZE sentinel present, the overwhelming common case)
// must be completely unaffected by this gate.
func TestCreateNotBlockedWithoutFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "normal issue", "-p", "2", "--json")
	if code != 0 {
		t.Fatalf("bd create failed (exit %d) with no freeze sentinel present:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &issue); err != nil {
		t.Fatalf("parsing create --json output: %v\n%s", err, stdout)
	}
	if issue.ID == "" {
		t.Fatalf("bd create --json returned no id:\n%s", stdout)
	}
}

// TestQuickBlockedDuringMigrationFreeze regression-checks the sharpest gap
// flagged in review of the original gate (dc-6jaq, PR #5826): the gate
// hand-picked five commands (create, update, close, remember, import), but
// "bd q" (quick.go) is create's own documented shorthand and called
// CheckReadonly directly rather than going through create's RunE — so it
// was never gated even though create was. Now that CheckReadonly itself
// folds in the freeze check, every one of its ~120 call sites is covered
// automatically, "q" included.
func TestQuickBlockedDuringMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	freezeTown(t, dir, "mayor", "dolt v2 migration")

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "q", "should not be created")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when blocked, got:\n%s", stdout)
	}
}

// TestLabelAddBlockedDuringMigrationFreeze checks a second, unrelated write
// command that was never part of the original hand-picked five either —
// evidence the fold-into-CheckReadonly fix covers the write surface
// generally, not just the one bypass ("bd q") review happened to name.
func TestLabelAddBlockedDuringMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "pre-freeze issue for label", "-p", "2", "--json")
	if code != 0 {
		t.Fatalf("setup bd create failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &issue); err != nil || issue.ID == "" {
		t.Fatalf("parsing create --json output: %v\n%s", err, stdout)
	}

	freezeTown(t, dir, "mayor", "dolt v2 migration")

	stdout, stderr, code = runBDMigrationFreeze(t, bd, dir, "label", "add", issue.ID, "should-not-be-added")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
	}
}

// TestAutoMigrateSkippedDuringMigrationFreeze is the structural ordering
// check (dc-6jaq review, ask #2): a frozen write must be blocked before
// PersistentPreRunE's own store-touching side effects run, not after, from
// inside the write command's own RunE. autoMigrateOnVersionBump
// (version_tracking.go) opens its own store connection and can apply a real
// schema migration — the most dangerous write in this path — and ran
// unconditionally for every non-preview command before this fix, freeze or
// not.
//
// An old .local_version forces trackBdVersion to detect a version "bump" so
// autoMigrateOnVersionBump's body actually does something observable
// instead of short-circuiting on "no upgrade detected" — then BD_DEBUG=1
// surfaces its unconditional "auto-migrate:"-prefixed debug.Logf lines, so
// their absence is direct evidence the function was never entered.
//
// The "list" case is review round 2's ask #1: a command classified
// read-only must NOT be blocked (diagnosis has to keep working during a
// freeze — exit 0, no refusal), but the same two maintenance side effects
// must still be skipped, because they are this hook's own writes and run
// independently of the command's classification. Reproduced pre-fix:
// freeze the town, seed .local_version with a stale version, run
// `BD_DEBUG=1 bd list` — exit 0, but the "auto-migrate:" line appeared and
// .local_version was rewritten to the current version anyway.
func TestAutoMigrateSkippedDuringMigrationFreeze(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantExit    int
		wantBlocked bool
	}{
		{name: "create", args: []string{"create", "should not be created", "-p", "2"}, wantExit: 1, wantBlocked: true},
		{name: "list", args: []string{"list"}, wantExit: 0, wantBlocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bd, dir := setupMigrationFreezeWorkspace(t)

			localVersionPath := filepath.Join(dir, ".beads", localVersionFile)
			if err := os.WriteFile(localVersionPath, []byte("0.0.1\n"), 0644); err != nil {
				t.Fatalf("writing fake old %s: %v", localVersionFile, err)
			}

			freezeTown(t, dir, "mayor", "dolt v2 migration")

			stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, []string{"BD_DEBUG=1"}, tt.args...)

			if code != tt.wantExit {
				t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantExit, stdout, stderr)
			}
			if tt.wantBlocked && !strings.Contains(stderr, "frozen for migration") {
				t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
			}
			if strings.Contains(stderr, "auto-migrate:") {
				t.Errorf("autoMigrateOnVersionBump ran its store-opening body during a freeze (found an "+
					"'auto-migrate:' debug log line in stderr) — the freeze check must skip it while "+
					"frozen regardless of whether the command itself is a read or a write:\n%s", stderr)
			}

			// The skip must be real, not just quiet: the frozen store's
			// .local_version must stay exactly as seeded, not get silently
			// rewritten to the running binary's version by trackBdVersion.
			got, err := os.ReadFile(localVersionPath)
			if err != nil {
				t.Fatalf("reading %s after run: %v", localVersionFile, err)
			}
			if strings.TrimSpace(string(got)) != "0.0.1" {
				t.Errorf("%s = %q after a frozen run, want unchanged \"0.0.1\" (trackBdVersion must not "+
					"write during a freeze)", localVersionFile, strings.TrimSpace(string(got)))
			}
		})
	}
}

// TestAutoMigrateStillRunsWithoutFreeze is the companion regression-safety
// check for the ask-#2 fix: without a freeze sentinel, the new early gate in
// PersistentPreRunE must not interfere with autoMigrateOnVersionBump's normal
// version-bump reconciliation.
func TestAutoMigrateStillRunsWithoutFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	localVersionPath := filepath.Join(dir, ".beads", localVersionFile)
	if err := os.WriteFile(localVersionPath, []byte("0.0.1\n"), 0644); err != nil {
		t.Fatalf("writing fake old %s: %v", localVersionFile, err)
	}

	stdout, stderr, code := runBDMigrationFreezeWithEnv(t, bd, dir, []string{"BD_DEBUG=1"},
		"create", "normal issue", "-p", "2", "--json")

	if code != 0 {
		t.Fatalf("bd create failed (exit %d) with no freeze sentinel present:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "auto-migrate:") {
		t.Errorf("expected autoMigrateOnVersionBump to run (an 'auto-migrate:' debug log line) when not frozen, got none:\nstderr:\n%s", stderr)
	}
}
