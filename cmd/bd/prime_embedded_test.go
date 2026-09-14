//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// bdPrime runs "bd prime" with the given args and returns stdout.
func bdPrime(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"prime"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd prime %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedPrime(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tp")

	// ===== Default Output =====

	t.Run("prime_default", func(t *testing.T) {
		out := bdPrime(t, bd, dir)
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty prime output")
		}
	})

	// ===== Full Flag =====

	t.Run("prime_full", func(t *testing.T) {
		out := bdPrime(t, bd, dir, "--full")
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty prime --full output")
		}
		// Full mode should include command references
		if !strings.Contains(out, "bd") {
			t.Errorf("expected 'bd' command references in --full output: %s", out[:min(200, len(out))])
		}
	})

	// ===== Export Flag =====

	t.Run("prime_export", func(t *testing.T) {
		out := bdPrime(t, bd, dir, "--export")
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty prime --export output")
		}
	})

	// ===== Memories Injected =====

	t.Run("prime_memories_injected", func(t *testing.T) {
		// Store a memory
		cmd := exec.Command(bd, "remember", "always use -race flag in tests", "--key", "prime-test-mem")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd remember failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		// Prime should include the memory
		primeOut := bdPrime(t, bd, dir, "--full")
		if !strings.Contains(primeOut, "race") {
			t.Errorf("expected memory content in prime output: %s", primeOut[:min(500, len(primeOut))])
		}
		memoryIdx := strings.Index(primeOut, "prime-test-mem")
		sessionIdx := strings.Index(primeOut, "SESSION CLOSE PROTOCOL")
		if memoryIdx == -1 || sessionIdx == -1 || memoryIdx > sessionIdx {
			t.Errorf("expected memories before session protocol in prime output")
		}
	})

	// ===== Memories Only =====

	t.Run("prime_memories_only", func(t *testing.T) {
		out := bdPrime(t, bd, dir, "--memories-only")
		if !strings.Contains(out, "prime-test-mem") {
			t.Errorf("expected memory content in --memories-only output: %s", out)
		}
		if strings.Contains(out, "Essential Commands") {
			t.Errorf("expected --memories-only to omit full command guide: %s", out)
		}
	})
}

// TestEmbeddedPrimeConcurrent exercises prime operations concurrently.
func TestEmbeddedPrimeConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "px")

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

			cmd := exec.Command(bd, "prime", "--full")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("prime --full (worker %d): %v\n%s", worker, err, out)
				results[worker] = r
				return
			}
			if len(strings.TrimSpace(string(out))) == 0 {
				r.err = fmt.Errorf("prime --full (worker %d): empty output", worker)
				results[worker] = r
				return
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

// primeChangeDirRepo lays out a minimal beads project (metadata.json is what
// makes .beads a project for -C resolution) inside a fresh git repository on
// branch main, plus the given workspace files. The #5509 tests below build two
// of these and compare `bd -C repo-b prime` run from repo-a against the
// `cd repo-b && bd prime` reference byte for byte.
func primeChangeDirRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := primeTestWorkspace(t, files)
	if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte(primeTestMetadata), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	primeChangeDirGit(t, dir, "init", "-q")
	primeChangeDirGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	return dir
}

// primeChangeDirRedirectClone lays out a git repository on branch main whose
// .beads holds only a redirect to storeBeadsDir: a clone sharing an external
// store, the shape the template's redirect notice describes.
func primeChangeDirRedirectClone(t *testing.T, storeBeadsDir string) string {
	t.Helper()
	dir := primeTestWorkspace(t, map[string]string{".beads/redirect": storeBeadsDir + "\n"})
	primeChangeDirGit(t, dir, "init", "-q")
	primeChangeDirGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	return dir
}

func primeChangeDirGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=bd-test", "GIT_AUTHOR_EMAIL=bd-test@example.com",
		"GIT_COMMITTER_NAME=bd-test", "GIT_COMMITTER_EMAIL=bd-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// primeChangeDirRun runs bd in dir with env and returns stdout.
func primeChangeDirRun(t *testing.T, bd, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s in %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), dir, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// primeFirstDiff describes the first line at which got and want differ.
func primeFirstDiff(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			return fmt.Sprintf("line %d: got %q, want %q", i+1, g, w)
		}
	}
	return "no differing line"
}

// TestEmbeddedPrimeChangeDirPrimesTarget is the issue's repro end to end
// (#5509): from repo-a, `bd -C repo-b prime` must emit repo-b's PRIME.md,
// identically to `cd repo-b && bd prime`, while plain `bd prime` and an
// env-only BEADS_DIR redirect keep priming the cwd.
func TestEmbeddedPrimeChangeDirPrimesTarget(t *testing.T) {
	bd := buildBDForInitTests(t)
	const primeA = "# PRIME repo-a\n"
	const primeB = "# PRIME repo-b\n"
	repoA := primeChangeDirRepo(t, map[string]string{".beads/PRIME.md": primeA})
	repoB := primeChangeDirRepo(t, map[string]string{".beads/PRIME.md": primeB})
	env := bdEnv(repoA)

	if got := primeChangeDirRun(t, bd, repoB, env, "prime", "--no-memories"); got != primeB {
		t.Fatalf("reference cd repo-b && bd prime = %q, want %q", got, primeB)
	}
	if got := primeChangeDirRun(t, bd, repoA, env, "-C", repoB, "prime", "--no-memories"); got != primeB {
		t.Fatalf("bd -C repo-b prime from repo-a = %q, want repo-b %q", got, primeB)
	}
	relB, err := filepath.Rel(repoA, repoB)
	if err != nil {
		t.Fatalf("relative path to repo-b: %v", err)
	}
	if got := primeChangeDirRun(t, bd, repoA, env, "-C", relB, "prime", "--no-memories"); got != primeB {
		t.Fatalf("bd -C %s prime from repo-a = %q, want repo-b %q", relB, got, primeB)
	}
	if got := primeChangeDirRun(t, bd, repoA, env, "prime", "--no-memories"); got != primeA {
		t.Fatalf("bd prime in repo-a = %q, want %q", got, primeA)
	}
	envRedirect := append(bdEnv(repoA), "BEADS_DIR="+filepath.Join(repoB, ".beads"))
	if got := primeChangeDirRun(t, bd, repoA, envRedirect, "prime", "--no-memories"); got != primeA {
		t.Fatalf("env-only BEADS_DIR redirect must stay local-first: got %q, want %q", got, primeA)
	}
}

// TestEmbeddedPrimeChangeDirDefaultTemplateMatchesTarget covers the generated
// (no PRIME.md) path of #5509. The default template's git-authority wording
// comes from git probes and the AGENTS.md/CLAUDE.md divergence reminder from
// the workspace files; both must describe the -C target, not the cwd. repo-a
// has a remote with an upstream-tracking branch and no agent files; repo-b has
// no remote and both agent files carrying the bd marker.
func TestEmbeddedPrimeChangeDirDefaultTemplateMatchesTarget(t *testing.T) {
	bd := buildBDForInitTests(t)
	repoA := primeChangeDirRepo(t, nil)
	primeChangeDirGit(t, repoA, "commit", "-q", "--allow-empty", "-m", "init")
	primeChangeDirGit(t, repoA, "remote", "add", "origin", filepath.Join(t.TempDir(), "origin.git"))
	primeChangeDirGit(t, repoA, "update-ref", "refs/remotes/origin/main", "HEAD")
	primeChangeDirGit(t, repoA, "config", "branch.main.remote", "origin")
	primeChangeDirGit(t, repoA, "config", "branch.main.merge", "refs/heads/main")
	repoB := primeChangeDirRepo(t, map[string]string{
		"AGENTS.md": "# Agents\n" + markerBlock,
		"CLAUDE.md": "# Claude\n" + markerBlock,
	})
	env := bdEnv(repoA)

	want := primeChangeDirRun(t, bd, repoB, env, "prime", "--no-memories")
	cwdOut := primeChangeDirRun(t, bd, repoA, env, "prime", "--no-memories")
	if want == cwdOut {
		t.Fatal("fixture: repo-a and repo-b must prime differently")
	}
	got := primeChangeDirRun(t, bd, repoA, env, "-C", repoB, "prime", "--no-memories")
	if got != want {
		t.Fatalf("bd -C repo-b prime from repo-a differs from cd repo-b && bd prime: %s", primeFirstDiff(got, want))
	}
	if got == cwdOut {
		t.Fatal("bd -C repo-b prime from repo-a still primes repo-a")
	}
	// Name the two repo-b facts that must have followed -C.
	for _, needle := range []string{
		"No git remote configured",
		"AGENTS.md and CLAUDE.md are independent files",
	} {
		if !strings.Contains(want, needle) {
			t.Errorf("fixture: cd repo-b && bd prime lacks %q", needle)
		}
		if !strings.Contains(got, needle) {
			t.Errorf("bd -C repo-b prime from repo-a lacks %q", needle)
		}
		if strings.Contains(cwdOut, needle) {
			t.Errorf("fixture: bd prime in repo-a must not contain %q", needle)
		}
	}
}

// TestEmbeddedPrimeChangeDirRedirectNoticeFollowsTarget covers the default
// template's redirect notice under -C (#5509): it must describe the -C
// target's redirect state in both directions. From a plain repo,
// `bd -C clone prime` must carry the clone's "Redirected" notice naming the
// clone's store; from a redirect clone, `bd -C plain prime` must carry no
// notice and never name the cwd's store. Each direction is compared byte for
// byte with `cd <target> && bd prime`.
func TestEmbeddedPrimeChangeDirRedirectNoticeFollowsTarget(t *testing.T) {
	bd := buildBDForInitTests(t)
	plain := primeChangeDirRepo(t, nil)
	store := primeTestWorkspace(t, map[string]string{".beads/metadata.json": primeTestMetadata})
	storeBeads := filepath.Join(store, ".beads")
	clone := primeChangeDirRedirectClone(t, storeBeads)
	env := bdEnv(plain)

	refPlain := primeChangeDirRun(t, bd, plain, env, "prime", "--no-memories")
	refClone := primeChangeDirRun(t, bd, clone, env, "prime", "--no-memories")
	if !strings.Contains(refClone, "Redirected") || !strings.Contains(refClone, storeBeads) {
		t.Fatalf("fixture: cd clone && bd prime must carry a Redirected notice naming %s:\n%s", storeBeads, refClone)
	}
	if strings.Contains(refPlain, "Redirected") || strings.Contains(refPlain, storeBeads) {
		t.Fatalf("fixture: cd plain && bd prime must carry no redirect notice:\n%s", refPlain)
	}

	// cwd = plain repo, -C = redirect clone: the clone's notice must appear.
	got := primeChangeDirRun(t, bd, plain, env, "-C", clone, "prime", "--no-memories")
	if !strings.Contains(got, "Redirected") || !strings.Contains(got, storeBeads) {
		t.Errorf("bd -C clone prime from plain lacks the clone's Redirected notice naming %s", storeBeads)
	}
	if got != refClone {
		t.Errorf("bd -C clone prime from plain differs from cd clone && bd prime: %s", primeFirstDiff(got, refClone))
	}

	// cwd = redirect clone, -C = plain repo: no notice, and never the cwd's store.
	got = primeChangeDirRun(t, bd, clone, env, "-C", plain, "prime", "--no-memories")
	if strings.Contains(got, "Redirected") || strings.Contains(got, storeBeads) {
		t.Errorf("bd -C plain prime from clone carries the cwd's redirect notice:\n%s", got)
	}
	if got != refPlain {
		t.Errorf("bd -C plain prime from clone differs from cd plain && bd prime: %s", primeFirstDiff(got, refPlain))
	}
}
