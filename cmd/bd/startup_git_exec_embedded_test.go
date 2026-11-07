//go:build cgo && linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitExecRecorder puts a `git` shim first on PATH that logs each invocation's
// arguments and then runs the real git, so a test can count the git processes
// a bd subprocess spawns.
type gitExecRecorder struct {
	shimDir, log string
}

func newGitExecRecorder(t *testing.T) *gitExecRecorder {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not found: %v", err)
	}
	r := &gitExecRecorder{shimDir: t.TempDir()}
	r.log = filepath.Join(r.shimDir, "git-calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + r.log + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(r.shimDir, "git"), []byte(script), 0o755); err != nil { // #nosec G306 -- test shim must be executable
		t.Fatal(err)
	}
	return r
}

// run runs bd in dir with the shim on PATH and returns stdout plus the git
// invocations it made.
func (r *gitExecRecorder) run(t *testing.T, bd, dir string, args ...string) (string, []string) {
	t.Helper()
	if err := os.Remove(r.log); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	// Drop every GIT_* variable but the config-file selectors: any of
	// internal/git's discovery overrides (GIT_DIR, GIT_WORK_TREE,
	// GIT_COMMON_DIR, GIT_CEILING_DIRECTORIES, GIT_DISCOVERY_ACROSS_FILESYSTEM,
	// GIT_OBJECT_DIRECTORY, GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT with its
	// GIT_CONFIG_KEY_n/VALUE_n, ...) exported by the runner — a `git -c`
	// wrapper, a hook context — would rightly send bd back to git rev-parse.
	var env []string
	for _, kv := range envWithout(envWithout(bdEnv(dir), "BD_ACTOR"), "PATH") {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") && name != "GIT_CONFIG_GLOBAL" &&
			name != "GIT_CONFIG_SYSTEM" && name != "GIT_CONFIG_NOSYSTEM" {
			continue
		}
		env = append(env, kv)
	}
	// backup.enabled stays at its default, so embedded auto-backup asks
	// whether a git remote exists after every command (isBackupAutoEnabled).
	// Global and system git config are pinned (empty, none) so the answer
	// does not depend on the host's ~/.gitconfig — an include there would
	// rightly send that probe to `git remote`.
	env = envWithout(envWithout(env, "BD_BACKUP_ENABLED"), "BEADS_BACKUP_ENABLED")
	for _, name := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"} {
		env = envWithout(env, name)
	}
	emptyGlobal := filepath.Join(r.shimDir, "empty.gitconfig")
	if err := os.WriteFile(emptyGlobal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(env, "GIT_CONFIG_GLOBAL="+emptyGlobal, "GIT_CONFIG_NOSYSTEM=1",
		"PATH="+r.shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out, stderr)
	}
	data, err := os.ReadFile(r.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return string(out), calls
}

// TestEmbeddedReadOnlyCommandsSpawnNoGit pins the per-call cost tools that
// call bd by the hundred depend on: a read-only command resolves the
// repository in-process, never needs the actor, and decides embedded
// auto-backup (default backup.enabled, with and without a git remote) without
// git, so it runs no git at all — from the main checkout and from a linked
// worktree sharing its database. A write still resolves the actor from git
// config user.name, lazily and to the same value.
func TestEmbeddedReadOnlyCommandsSpawnNoGit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gx")
	issue := bdCreate(t, bd, dir, "startup git probe")

	worktree := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-wt")
	t.Cleanup(func() { _ = os.RemoveAll(worktree) })
	noHooks := "core.hooksPath=" + t.TempDir() // keep bd's installed hooks out of fixture setup
	for _, args := range [][]string{
		{"-c", noHooks, "commit", "--allow-empty", "--no-verify", "-m", "base"},
		{"-c", noHooks, "worktree", "add", "-q", worktree},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	rec := newGitExecRecorder(t)
	reads := func(phase string) {
		t.Helper()
		for _, location := range []struct{ name, dir string }{
			{"main checkout", dir},
			{"linked worktree", worktree},
		} {
			for _, args := range [][]string{
				{"list", "--json"},
				{"show", issue.ID, "--json"},
			} {
				out, calls := rec.run(t, bd, location.dir, args...)
				if !strings.Contains(out, issue.ID) {
					t.Fatalf("%s, %s: bd %s did not find %s:\n%s", phase, location.name, args[0], issue.ID, out)
				}
				if len(calls) != 0 {
					t.Errorf("%s, %s: bd %s spawned %d git process(es), want 0:\n%s",
						phase, location.name, args[0], len(calls), strings.Join(calls, "\n"))
				}
			}
		}
	}
	// No git remote: auto-backup stays off, decided without running git.
	reads("no git remote")
	// With a remote, auto-backup turns on (still decided in-process); the
	// first read may run the backup itself, which needs no git either.
	addRemote := gitCommand("remote", "add", "origin", "https://example.invalid/gitcalls.git")
	addRemote.Dir, addRemote.Env = dir, bdEnv(dir)
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	reads("git remote configured")

	// Positive control for the shim, and the deferred actor's value: a write
	// asks git for user.name exactly once and records it as before.
	out, calls := rec.run(t, bd, dir, "create", "--json", "lazy actor")
	created := parseIssueJSON(t, []byte(out))
	if created.CreatedBy != "Test" {
		t.Errorf("created_by = %q, want git user.name %q", created.CreatedBy, "Test")
	}
	nameLookups := 0
	for _, call := range calls {
		if call == "config user.name" {
			nameLookups++
		}
	}
	if nameLookups != 1 {
		t.Errorf("bd create ran `git config user.name` %d times, want 1; calls:\n%s", nameLookups, strings.Join(calls, "\n"))
	}
}
