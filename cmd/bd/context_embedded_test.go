//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestEmbeddedContext(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tx")

	t.Run("context_default", func(t *testing.T) {
		cmd := exec.Command(bd, "context")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "embedded") && !strings.Contains(stdout.String(), ".beads") {
			t.Errorf("expected embedded mode or .beads in context output: %s", stdout.String())
		}
	})

	t.Run("context_json", func(t *testing.T) {
		cmd := exec.Command(bd, "context", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if len(strings.TrimSpace(stdout.String())) == 0 {
			t.Error("expected non-empty context --json output")
		}
	})

	// bd context must resolve from a discoverable .beads directory even when
	// the scope is not inside a git repository — git is a means of finding the
	// repo root, not a hard requirement for this diagnostic (GH#4772).
	t.Run("context_json_without_git_repo", func(t *testing.T) {
		nonGitDir, _, _ := bdInit(t, bd, "--prefix", "ng")
		if err := os.RemoveAll(filepath.Join(nonGitDir, ".git")); err != nil {
			t.Fatalf("remove .git: %v", err)
		}

		cmd := exec.Command(bd, "context", "--json")
		cmd.Dir = nonGitDir
		cmd.Env = bdEnv(nonGitDir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context --json failed outside git repo: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()

		// Decode rather than probing for key names: ContextInfo's fields are
		// not omitempty, so a wrong or empty beads_dir still emits the key.
		var info ContextInfo
		if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
			t.Fatalf("decoding context JSON: %v\noutput:\n%s", jerr, out)
		}
		assertNonGitContext(t, info, nonGitDir)
	})

	// The other side of the same question, and the one that has to be driven
	// through the real binary: only a subprocess has an environment the
	// caller actually supplied. In-process tests stub the snapshot, so they
	// cannot tell a caller-supplied BEADS_DIR from one bd exported for
	// itself — which is exactly the distinction under test.
	t.Run("context_json_without_git_repo_with_caller_beads_dir", func(t *testing.T) {
		nonGitDir, _, _ := bdInit(t, bd, "--prefix", "nr")
		if err := os.RemoveAll(filepath.Join(nonGitDir, ".git")); err != nil {
			t.Fatalf("remove .git: %v", err)
		}
		beadsDir := filepath.Join(nonGitDir, ".beads")

		// Run from somewhere else entirely, so the only way this workspace
		// can be found is the BEADS_DIR the caller sets here.
		elsewhere := t.TempDir()

		cmd := exec.Command(bd, "context", "--json")
		cmd.Dir = elsewhere
		cmd.Env = append(bdEnv(nonGitDir), "BEADS_DIR="+beadsDir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context --json failed with a caller-supplied BEADS_DIR: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var info ContextInfo
		if jerr := json.Unmarshal([]byte(stdout.String()), &info); jerr != nil {
			t.Fatalf("decoding context JSON: %v\noutput:\n%s", jerr, stdout.String())
		}
		assertRedirectedNonGitContext(t, info, beadsDir)
	})
}

// assertNonGitContext holds `bd context` to the exact answer it must give for
// a workspace at dir that is not inside a git repository: the workspace is
// identified from .beads, the repo root falls back to the .beads parent, and
// the backend still resolves from config.
//
// Paths are compared through EvalSymlinks because the test's temp dir is
// reported canonically (/private/var/... on macOS) by the binary under test.
func assertNonGitContext(t *testing.T, info ContextInfo, dir string) {
	t.Helper()

	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}

	wantBeadsDir := resolve(filepath.Join(dir, ".beads"))
	if got := resolve(info.BeadsDir); got != wantBeadsDir {
		t.Errorf("beads_dir = %q, want %q", info.BeadsDir, wantBeadsDir)
	}
	wantRepoRoot := resolve(dir)
	if got := resolve(info.RepoRoot); got != wantRepoRoot {
		t.Errorf("repo_root = %q, want %q (the .beads parent, since there is no git root)", info.RepoRoot, wantRepoRoot)
	}
	if info.Backend != configfile.BackendDolt {
		t.Errorf("backend = %q, want %q — backend identity must still resolve from config", info.Backend, configfile.BackendDolt)
	}
	if info.BdVersion == "" {
		t.Error("bd_version should be populated")
	}
	if info.IsWorktree {
		t.Error("is_worktree should be false outside a git repository")
	}
	// The identity half, which is the reason a consumer runs this command.
	// A workspace found by walking up from the working directory is NOT a
	// redirect, and reporting one makes the probe lie about identity. bd
	// exports a BEADS_DIR for itself just before resolving, so this assertion
	// is what keeps the answer keyed to the caller's environment rather than
	// the live one.
	if info.IsRedirected {
		t.Error("is_redirected should be false for a .beads found by the working-directory walk")
	}
}

// assertRedirectedNonGitContext is the other half: a workspace the CALLER
// named with BEADS_DIR is a redirect, and carries the Contributor role that
// RepoContext.Role() documents for external-repo mode — even with no git
// repository to compare against.
func assertRedirectedNonGitContext(t *testing.T, info ContextInfo, beadsDir string) {
	t.Helper()

	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}

	if got, want := resolve(info.BeadsDir), resolve(beadsDir); got != want {
		t.Errorf("beads_dir = %q, want %q", info.BeadsDir, want)
	}
	if !info.IsRedirected {
		t.Error("is_redirected should be true for a workspace the caller named with BEADS_DIR")
	}
	if info.Role != "contributor" {
		t.Errorf("role = %q, want \"contributor\" — BEADS_DIR implies external repo mode", info.Role)
	}
}

func TestEmbeddedContextConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xx")

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			cmd := exec.Command(bd, "context")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("context (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
