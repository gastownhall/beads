//go:build cgo

package main

// End-to-end coverage for GH#5922's fix: the bd children a git hook
// re-executes (`bd export` on the pre-commit lane, `bd import` on the
// post-merge and post-checkout lanes) must run this bd binary, never whatever
// `bd` PATH selects. The tests build the real CLI, invoke it by absolute path
// with a decoy `bd` first on PATH that records any invocation, and assert the
// decoy never runs and the work still lands. At base the decoy ran, the
// pre-commit export warned, and the commit kept a stale JSONL.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// hookChildFixture bundles what the hook-child tests need: the built bd, the
// git binary's directory (for a PATH that contains git but no host bd), and a
// decoy bd directory whose `bd` records every invocation into marker and
// exits 1.
type hookChildFixture struct {
	bd       string
	gitPath  string
	decoyDir string
	marker   string
}

// newHookChildFixture builds the real CLI and the decoy. The decoy dir goes
// first on PATH for every bd invocation below, so any child that resolves bd
// through PATH runs the decoy and leaves evidence in marker.
func newHookChildFixture(t *testing.T) *hookChildFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script decoy binary is POSIX-only")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	f := &hookChildFixture{bd: buildEmbeddedBD(t), gitPath: gitPath}

	decoyDir := t.TempDir()
	f.marker = filepath.Join(decoyDir, "decoy-ran")
	quoted := "'" + strings.ReplaceAll(f.marker, "'", `'\''`) + "'"
	stub := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> ` + quoted + "\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(decoyDir, "bd"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	f.decoyDir = decoyDir
	return f
}

// hookChildEnv is the environment the real bd runs under: bdEnv's hermetic
// base, with PATH replaced so the decoy bd resolves first and the host's own
// PATH (which can hold a real bd) is gone. The decoy directory precedes the
// git binary's directory; nothing else survives.
func (f *hookChildFixture) hookChildEnv(dir string) []string {
	return f.envWithDecoyPATH(dir, f.decoyDir)
}

func (f *hookChildFixture) envWithDecoyPATH(dir, first string) []string {
	env := envWithout(bdEnv(dir), "PATH")
	path := first
	if path != "" {
		path += string(os.PathListSeparator)
	}
	path += filepath.Dir(f.gitPath)
	return append(env, "PATH="+path)
}

// runBD runs the built bd with env in dir and returns stdout, stderr and the
// run error without fataling, so a subtest can keep asserting after a failure.
func (f *hookChildFixture) runBD(dir string, env []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(f.bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), errBuf.String(), err
}

// runGit runs git in dir with a PATH that holds git but no bd, so the
// installed hook shims (which resolve bd from PATH) stay inert during fixture
// setup instead of running the host bd against the test store.
func (f *hookChildFixture) runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(f.gitPath, args...)
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(f.envWithDecoyPATH(dir, ""))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", args, err, out.String())
	}
	return out.String()
}

// decoyRan reports whether the decoy bd executed, resetting the marker.
func (f *hookChildFixture) decoyRan(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat(f.marker); err == nil {
		if err := os.Remove(f.marker); err != nil {
			t.Fatal(err)
		}
		return true
	}
	return false
}

// resetDecoy removes any marker left by a previous invocation so the next
// assertion starts from a clean record.
func (f *hookChildFixture) resetDecoy(t *testing.T) {
	t.Helper()
	if err := os.Remove(f.marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// setBDConfig runs `bd config set key value` with the plain hermetic env.
func (f *hookChildFixture) setBDConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	if _, stderr, err := f.runBD(dir, bdEnv(dir), "config", "set", key, value); err != nil {
		t.Fatalf("bd config set %s %s: %v\n%s", key, value, err, stderr)
	}
}

// TestEmbeddedHookChildrenRunTheHookBinary pins the GH#5922 fix on the export
// and import lanes: the hook children run the binary the hook parent is,
// never the decoy bd PATH selects.
func TestEmbeddedHookChildrenRunTheHookBinary(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	f := newHookChildFixture(t)

	t.Run("pre-commit export", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, f.bd, "--prefix", "hcx")
		f.setBDConfig(t, dir, "export.git-add", "true")
		f.setBDConfig(t, dir, "export.auto", "true")

		// One committed issue so the tracked JSONL exists and is current.
		bdCreateSilent(t, f.bd, dir, "hook child committed issue", "--type", "task")
		bdExport(t, f.bd, dir, "-o", filepath.Join(".beads", "issues.jsonl"))
		f.runGit(t, dir, "add", "-A")
		f.runGit(t, dir, "commit", "-q", "-m", "fixture: initial export")

		// A pending issue whose creation must not refresh the JSONL: the
		// incidental auto-export is suppressed with BD_GIT_HOOK=1, so the only
		// thing that can land it in the tracked file is the hook's child.
		pendingTitle := "hook child pending issue"
		cmd := exec.Command(f.bd, "create", "--silent", "--title", pendingTitle, "--type", "task")
		cmd.Dir = dir
		cmd.Env = append(bdEnv(dir), "BD_GIT_HOOK=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd create with suppressed auto-export: %v\n%s", err, out)
		}

		// The export gate needs a staged .beads change.
		cfg, err := os.OpenFile(filepath.Join(beadsDir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.WriteString("# staged for the export gate\n"); err != nil {
			t.Fatal(err)
		}
		cfg.Close()
		f.runGit(t, dir, "add", "--", ".beads/config.yaml")

		// Fixture precondition: the on-disk JSONL is stale.
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		jsonl, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(jsonl), pendingTitle) {
			t.Fatalf("fixture: JSONL already holds the pending issue:\n%s", jsonl)
		}

		f.resetDecoy(t)
		_, stderr, err := f.runBD(dir, f.hookChildEnv(dir), "hooks", "run", "pre-commit")

		if f.decoyRan(t) {
			t.Error("the decoy bd ran: the pre-commit export child resolved 'bd' from PATH instead of the running binary (GH#5922)")
		}
		if strings.Contains(stderr, "pre-commit export warning") {
			t.Errorf("the pre-commit export failed:\n%s", stderr)
		}
		if err != nil {
			t.Errorf("bd hooks run pre-commit: %v\n%s", err, stderr)
		}

		jsonl, err = os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(jsonl), pendingTitle) {
			t.Errorf("exported JSONL lacks the pending issue:\n%s", jsonl)
		}
		staged := f.runGit(t, dir, "cat-file", "-p", ":.beads/issues.jsonl")
		if !strings.Contains(staged, pendingTitle) {
			t.Errorf("staged JSONL lacks the pending issue (not staged into the commit):\n%s", staged)
		}
	})
}
