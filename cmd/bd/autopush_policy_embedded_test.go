//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GH#6233, auto-push half: with dolt.auto-push on and a rejected origin, a
// local write still succeeds, the remote stays untouched, and the refusal
// rides the existing auto-push warning and throttle (push-state records the
// attempt, not the commit, so change detection survives) — quiet/JSON output
// stays clean, and an explicit bd sync against the rejected origin fails
// instead of converting to a successful no-remote sync.
func TestEmbeddedAutoPushRefusedByAllowedRemotePatterns(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "-b", "main", bareDir)
	originURL := "file://" + bareDir

	// The bare needs an initial branch before Dolt's git transport will
	// push to it (same shape as the bootstrap e2e in init_embedded_test.go).
	seedDir := t.TempDir()
	initGitRepoAt(t, seedDir)
	runGitForBootstrapTest(t, seedDir, "branch", "-M", "main")
	runGitForBootstrapTest(t, seedDir, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, seedDir, "remote", "add", "origin", originURL)
	runGitForBootstrapTest(t, seedDir, "push", "-u", "origin", "main")

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	runGitForBootstrapTest(t, dir, "remote", "add", "origin", originURL)
	runBDInit(t, bd, dir, "--prefix", "apsh", "--skip-hooks", "--skip-agents")

	// Seed the remote while the allowlist is empty.
	bdDolt(t, bd, dir, "push")
	before := doltDataRef(t, originURL)
	if before == "" {
		t.Fatal("precondition: the seed push must publish refs/dolt/data")
	}

	// Opt in to auto-push, then tighten the list past the origin. Each
	// config set is itself a write whose post-run auto-push fires; the
	// tightening one refuses, so drop its throttle record before the write
	// under test.
	bdRunOK(t, bd, dir, "config", "set", "dolt.auto-push", "true")
	bdRunOK(t, bd, dir, "config", "set", "federation.allowed-remote-patterns", "git+https://github.com/myorg/*")
	pushState := filepath.Join(dir, ".beads", "push-state.json")
	if err := os.Remove(pushState); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove push-state after config set: %v", err)
	}

	// The local write succeeds and reports the refusal through the existing
	// auto-push warning.
	out, err := bdCombined(t, bd, dir, "create", "--title", "kept locally despite the rejected origin")
	if err != nil {
		t.Fatalf("bd create failed under a rejected auto-push origin:\n%s", out)
	}
	if !strings.Contains(out, "Warning: dolt auto-push failed") {
		t.Errorf("bd create output lacks the existing auto-push warning:\n%s", out)
	}
	if !strings.Contains(out, "federation.allowed-remote-patterns") {
		t.Errorf("the auto-push warning does not carry the policy refusal:\n%s", out)
	}

	// The remote stays untouched.
	if after := doltDataRef(t, originURL); after != before {
		t.Fatalf("auto-push moved the rejected remote: before=%s after=%s", before, after)
	}

	// The throttle recorded the attempt without latching the commit, so
	// change detection still fires when the list is fixed.
	ps, err := os.ReadFile(pushState)
	if err != nil {
		t.Fatalf("read push-state.json: %v", err)
	}
	var state struct {
		LastPush   string `json:"last_push"`
		LastCommit string `json:"last_commit"`
	}
	if err := json.Unmarshal(ps, &state); err != nil {
		t.Fatalf("parse push-state.json: %v", err)
	}
	if state.LastPush == "" {
		t.Error("push-state.json records no attempt timestamp; the failure throttle is gone")
	}
	if state.LastCommit != "" {
		t.Errorf("push-state.json latched LastCommit %q after a refused push; change detection would miss the retry", state.LastCommit)
	}

	// Quiet/JSON behavior is preserved: no warning in --json output.
	if err := os.Remove(pushState); err != nil {
		t.Fatalf("remove push-state before the json write: %v", err)
	}
	out, err = bdCombined(t, bd, dir, "create", "--title", "json write stays clean", "--json")
	if err != nil {
		t.Fatalf("bd create --json failed under a rejected auto-push origin:\n%s", out)
	}
	if strings.Contains(out, "Warning:") {
		t.Errorf("--json output carries an auto-push warning:\n%s", out)
	}
	if after := doltDataRef(t, originURL); after != before {
		t.Fatalf("the json auto-push moved the rejected remote: before=%s after=%s", before, after)
	}

	// An explicit sync against the rejected origin fails with the policy
	// refusal, not a successful no-remote conversion.
	out, err = bdCombined(t, bd, dir, "sync")
	if err == nil {
		t.Fatalf("bd sync against a rejected origin succeeded:\n%s", out)
	}
	if !strings.Contains(out, "federation.allowed-remote-patterns") {
		t.Errorf("bd sync failure does not name the policy:\n%s", out)
	}
}

// doltDataRef reads the refs/dolt/data hash a git-backed Dolt remote
// publishes, empty when the ref is absent.
func doltDataRef(t *testing.T, originURL string) string {
	t.Helper()
	out, err := gitCommand("ls-remote", originURL, "refs/dolt/data").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote refs/dolt/data: %v\n%s", err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// bdCombined runs bd in dir and returns combined output; the error is the
// caller's to judge.
func bdCombined(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
