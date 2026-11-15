//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Integration tests for be-7eu1d's git-origin collision guard in bd dolt
// remote add (FR-06, FR-07). Its bd doctor half (FR-08, FR-09:
// doctor.CheckDoltRemoteGitOrigin) is server-mode only: bd doctor runs none of
// its checks on an embedded workspace (GH#3794), so it cannot be driven here.

// remoteAdd runs "bd dolt remote add <args>" returning (stdout, stderr, error)
// with separate buffers. Delegates to bdDoltSeparate (dolt_local_only_embedded_test.go).
func remoteAdd(t *testing.T, bd, dir string, args ...string) (string, string, error) {
	t.Helper()
	return bdDoltSeparate(t, bd, dir, append([]string{"remote", "add"}, args...)...)
}

// setGitOrigin adds a git remote named "origin" to the directory.
func setGitOrigin(t *testing.T, dir, url string) {
	t.Helper()
	cmd := gitCommand("remote", "add", "origin", url)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin %s: %v\n%s", url, err, out)
	}
}

// -- bd dolt remote add: collision with git origin --

// TestEmbeddedDoltRemoteAdd_GitOriginCollision_Exits1 asserts that adding a Dolt remote
// whose URL matches the git origin exits non-zero and prints the refusal message.
func TestEmbeddedDoltRemoteAdd_GitOriginCollision_Exits1(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rg")

	originURL := "https://github.com/org/repo.git"
	setGitOrigin(t, dir, originURL)

	_, stderr, err := remoteAdd(t, bd, dir, "origin", originURL)
	if err == nil {
		t.Fatalf("expected bd dolt remote add to exit non-zero on git-origin collision, but succeeded\nstderr: %s", stderr)
	}
	if !strings.Contains(stderr, "refusing to add") {
		t.Errorf("expected 'refusing to add' in stderr\ngot:\n%s", stderr)
	}
	if !strings.Contains(stderr, originURL) {
		t.Errorf("expected rejected URL %q in stderr\ngot:\n%s", originURL, stderr)
	}
}

// TestEmbeddedDoltRemoteAdd_GitOriginCollision_RefusalMessageCopy verifies exact phrases
// from the UX spec (be-fxwbm §2.1) appear in the refusal output.
func TestEmbeddedDoltRemoteAdd_GitOriginCollision_RefusalMessageCopy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rm")

	originURL := "https://github.com/org/repo.git"
	setGitOrigin(t, dir, originURL)

	_, stderr, _ := remoteAdd(t, bd, dir, "origin", originURL)

	wantPhrases := []string{
		"refusing to add",    // §2.1 first line
		"git origin",         // §2.1 first line
		"--allow-git-origin", // §2.1 recovery option
		"dolt.local-only",    // §2.1 opt-out option
	}
	for _, phrase := range wantPhrases {
		if !strings.Contains(stderr, phrase) {
			t.Errorf("refusal message missing %q\nfull stderr:\n%s", phrase, stderr)
		}
	}
}

// TestEmbeddedDoltRemoteAdd_AllowGitOrigin_Exits0 asserts that --allow-git-origin
// bypasses the collision guard and exits 0.
func TestEmbeddedDoltRemoteAdd_AllowGitOrigin_Exits0(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ag")

	originURL := "file:///tmp/fake-git-remote"
	setGitOrigin(t, dir, originURL)

	_, _, err := remoteAdd(t, bd, dir, "--allow-git-origin", "dolt-origin", originURL)
	if err != nil {
		t.Errorf("expected bd dolt remote add --allow-git-origin to exit 0 on collision, got error: %v", err)
	}
}

// TestEmbeddedDoltRemoteAdd_AllowGitOrigin_WarningOnStderr asserts the one-liner warning
// from be-fxwbm §2.2 appears on stderr when --allow-git-origin is set.
func TestEmbeddedDoltRemoteAdd_AllowGitOrigin_WarningOnStderr(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "aw")

	originURL := "file:///tmp/fake-git-remote2"
	setGitOrigin(t, dir, originURL)

	_, stderr, _ := remoteAdd(t, bd, dir, "--allow-git-origin", "dolt-origin", originURL)

	// §2.2: "Warning: '<url>' matches the git origin — proceeding because --allow-git-origin is set."
	if !strings.Contains(stderr, "Warning:") {
		t.Errorf("expected 'Warning:' on stderr when --allow-git-origin is set\ngot:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--allow-git-origin") {
		t.Errorf("expected '--allow-git-origin' in warning\ngot:\n%s", stderr)
	}
}

// TestEmbeddedDoltRemoteAdd_NoGitOrigin_ProceedsNormally asserts no refusal fires when
// the git repo has no 'origin' remote.
func TestEmbeddedDoltRemoteAdd_NoGitOrigin_ProceedsNormally(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "no")
	// bdInit creates a git repo, but no origin is set.

	remoteDir := t.TempDir()
	_, stderr, err := remoteAdd(t, bd, dir, "dolt-origin", "file://"+remoteDir)
	if err != nil {
		t.Errorf("expected bd dolt remote add to succeed when no git origin, got error: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "refusing to add") {
		t.Errorf("unexpected refusal when no git origin\nstderr:\n%s", stderr)
	}
}

// TestEmbeddedDoltRemoteAdd_DifferentURL_ProceedsNormally asserts the guard does not
// fire when the Dolt remote URL is different from the git origin.
func TestEmbeddedDoltRemoteAdd_DifferentURL_ProceedsNormally(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "du")

	setGitOrigin(t, dir, "https://github.com/org/repo.git")

	// Different URL — should not trigger guard.
	remoteDir := t.TempDir()
	_, stderr, err := remoteAdd(t, bd, dir, "dolt-origin", "file://"+remoteDir)
	if err != nil {
		t.Errorf("expected bd dolt remote add with different URL to succeed, got error: %v\nstderr: %s", err, stderr)
	}
	if strings.Contains(stderr, "refusing to add") {
		t.Errorf("unexpected refusal when URL doesn't match git origin\nstderr:\n%s", stderr)
	}
}

// TestEmbeddedDoltRemoteAdd_LocalOnly_FiresBeforeGuard verifies that when
// dolt.local-only=true is set, the local-only refusal fires before the
// git-origin guard — the local-only guard takes priority (be-3v2ou ordering).
func TestEmbeddedDoltRemoteAdd_LocalOnly_FiresBeforeGuard(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lo")
	bdConfig(t, bd, dir, "set", "dolt.local-only", "true")

	originURL := "https://github.com/org/repo.git"
	setGitOrigin(t, dir, originURL)

	_, stderr, err := remoteAdd(t, bd, dir, "origin", originURL)
	if err == nil {
		t.Fatal("expected bd dolt remote add to fail with dolt.local-only=true")
	}
	// Must mention local-only, not git-origin collision.
	if strings.Contains(stderr, "refusing to add") {
		t.Errorf("git-origin guard fired before local-only guard\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "local-only") && !strings.Contains(stderr, "dolt.local-only") {
		t.Errorf("expected local-only message, got:\n%s", stderr)
	}
}

// TestEmbeddedDoltRemoteAdd_JSON_AllowGitOrigin_WarningOnStderr asserts that even in
// --json mode, the override warning still appears on stderr (not stdout).
func TestEmbeddedDoltRemoteAdd_JSON_AllowGitOrigin_WarningOnStderr(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "jw")

	originURL := "file:///tmp/origin-collision-json-mode"
	setGitOrigin(t, dir, originURL)

	// Run with --json and --allow-git-origin on collision — warning must go to stderr.
	cmd := exec.Command(bd, "dolt", "--json", "remote", "add", "--allow-git-origin", "origin", originURL)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()

	if !strings.Contains(stderr.String(), "Warning:") {
		t.Errorf("--json --allow-git-origin: expected Warning on stderr, got:\nstderr: %s\nstdout: %s",
			stderr.String(), stdout.String())
	}
}
