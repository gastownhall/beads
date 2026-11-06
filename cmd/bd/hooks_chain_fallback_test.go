package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// The tests in this file cover GH#6751: the generated hook section must not
// let a chained <hook>.old gate fail open when bd is not on PATH, when bd
// hooks run reports a chained hook's reserved status, or when the deadline
// ends a pre-commit or pre-push gate whose outcome is then unknown.

// hookChainWitnessSidecar is a foreign <hook>.old that records each run in
// $HOOK_TEST_WITNESS using shell builtins only, prints a diagnostic, and exits
// with the given status.
func hookChainWitnessSidecar(exit string) string {
	return "#!/bin/sh\nprintf 'ran\\n' >> \"$HOOK_TEST_WITNESS\"\nprintf 'foreign gate says %s\\n' '" + exit + "' >&2\nexit " + exit + "\n"
}

// hookChainWitnessRuns counts the runs a witness sidecar recorded.
func hookChainWitnessRuns(t *testing.T, witness string) int {
	t.Helper()
	content, err := os.ReadFile(witness)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	return strings.Count(string(content), "ran\n")
}

// hookChainRepo is a real repository whose git runs with a PATH that holds
// git and nothing else, so the generated section sees no bd.
type hookChainRepo struct {
	gitPath  string
	dir      string
	hooksDir string
	env      []string
	witness  string
}

func newHookChainRepo(t *testing.T, repoName string) hookChainRepo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the bd-less PATH fixture is POSIX-specific")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	binDir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(binDir, "git")); err != nil {
		t.Fatalf("link git into the bd-less PATH: %v", err)
	}
	repoDir := filepath.Join(t.TempDir(), repoName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	witness := filepath.Join(t.TempDir(), "witness")
	env := hookProcessGitEnv(t.TempDir(), "HOOK_TEST_WITNESS="+witness)
	for i, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			env[i] = "PATH=" + binDir
		}
	}
	gitBin := filepath.Join(binDir, "git")
	runHookProcessGit(t, gitBin, repoDir, env, "init", "--template=", "--initial-branch=main", ".")
	runHookProcessGit(t, gitBin, repoDir, env, "config", "user.name", "Hook Test")
	runHookProcessGit(t, gitBin, repoDir, env, "config", "user.email", "hook-test@example.invalid")

	// Git prepends its exec path to a hook's PATH; neither may offer bd, or
	// these tests would exercise the bd-present branch.
	execPath := strings.TrimSpace(runHookProcessGit(t, gitBin, repoDir, env, "--exec-path"))
	for _, dir := range []string{binDir, execPath} {
		if _, statErr := os.Stat(filepath.Join(dir, "bd")); statErr == nil {
			t.Fatalf("bd is reachable from the hook PATH through %s", dir)
		}
	}
	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	return hookChainRepo{gitPath: gitBin, dir: repoDir, hooksDir: hooksDir, env: env, witness: witness}
}

func (r hookChainRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	return runHookProcessGit(t, r.gitPath, r.dir, r.env, args...)
}

// commit stages a fresh file and commits it, returning the output and whether
// git accepted the commit.
func (r hookChainRepo) commit(t *testing.T, dir string) (string, bool) {
	t.Helper()
	file, err := os.CreateTemp(dir, "tracked-*")
	if err != nil {
		t.Fatalf("write tracked fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("write tracked fixture: %v", err)
	}
	runHookProcessGit(t, r.gitPath, dir, r.env, "add", filepath.Base(file.Name()))
	cmd := exec.Command(r.gitPath, "commit", "-m", "hook chain test")
	cmd.Dir = dir
	cmd.Env = r.env
	output, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("run git commit: %v\n%s", err, output)
		}
	}
	return string(output), err == nil
}

// copyHookChainBinary copies a compiled executable that exits 1 (false) so a
// sidecar can be a binary rather than a script.
func copyHookChainBinary(t *testing.T, dest string, mode os.FileMode) {
	t.Helper()
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false not found")
	}
	resolved, err := filepath.EvalSymlinks(falsePath)
	if err != nil {
		t.Fatalf("resolve false: %v", err)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read false: %v", err)
	}
	if strings.HasPrefix(string(content), "#!") {
		t.Skip("false is a script on this host")
	}
	if err := os.WriteFile(dest, content, 0o700); err != nil {
		t.Fatalf("write binary sidecar: %v", err)
	}
	if err := os.Chmod(dest, mode); err != nil {
		t.Fatalf("chmod binary sidecar: %v", err)
	}
}

// TestGeneratedHookChainsWhenBDUnavailable is the process-level regression
// test for GH#6751's primary report, from PR #6840 (vishnujayvel): a chained
// pre-commit.old must not be skipped just because bd is missing from PATH.
// It runs the issue's own repro against a real repository, because the
// section resolves the hooks directory from $0 as git sets it.
func TestGeneratedHookChainsWhenBDUnavailable(t *testing.T) {
	section := "#!/bin/sh\n" + generateHookSection("pre-commit")

	refuses := func(t *testing.T, r hookChainRepo, commitDir string) {
		t.Helper()
		head := strings.TrimSpace(r.git(t, "rev-parse", "-q", "--verify", "HEAD^{commit}"))
		output, ok := r.commit(t, commitDir)
		if ok {
			t.Fatalf("commit succeeded without bd on PATH; the chained pre-commit.old gate was skipped\n%s", output)
		}
		if !strings.Contains(output, "foreign gate says 1") {
			t.Errorf("commit output is missing the chained gate's diagnostic\n%s", output)
		}
		if after := strings.TrimSpace(r.git(t, "rev-parse", "-q", "--verify", "HEAD^{commit}")); after != head {
			t.Errorf("HEAD moved from %s to %s although the gate refused", head, after)
		}
		if runs := hookChainWitnessRuns(t, r.witness); runs != 1 {
			t.Errorf("chained gate ran %d times, want 1", runs)
		}
	}
	seed := func(t *testing.T, r hookChainRepo) {
		t.Helper()
		if output, ok := r.commit(t, r.dir); !ok {
			t.Fatalf("seed commit failed\n%s", output)
		}
	}
	install := func(t *testing.T, r hookChainRepo, sidecar string) {
		t.Helper()
		writeHookProcessFixture(t, r.hooksDir, "pre-commit", section)
		writeHookProcessFixture(t, r.hooksDir, "pre-commit.old", sidecar)
	}

	t.Run("bd missing, chained gate refuses", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		install(t, r, hookChainWitnessSidecar("1"))
		refuses(t, r, r.dir)
	})

	t.Run("bd missing, no chain, commit proceeds", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		writeHookProcessFixture(t, r.hooksDir, "pre-commit", section)
		if output, ok := r.commit(t, r.dir); !ok {
			t.Fatalf("commit failed without a chained hook\n%s", output)
		}
	})

	// The classifier must match the marker lines runChainedHook's recursion
	// guard keys on, not any line that mentions BEADS INTEGRATION.
	for _, tc := range []struct{ name, line string }{
		{name: "foreign hook merely mentioning BEADS INTEGRATION", line: "# unrelated hook, see BEADS INTEGRATION docs for context"},
		{name: "foreign hook starting with '# --- ' but not the BEGIN marker", line: "# --- NOTE: not a BEADS INTEGRATION marker"},
		{name: "foreign hook begin-marker-shaped line with extra words", line: "# --- BEGIN foo BEADS INTEGRATION"},
		{name: "foreign hook naming bd hooks without delegating", line: "# see: bd hooks list; xbd hooks run is not bd; bd hooks runner"},
		{name: "foreign comment quoting the BEGIN marker mid-line", line: "# keep this above the \"# --- BEGIN BEADS INTEGRATION\" line"},
		{name: "foreign comment quoting the shim marker mid-line", line: "# installed next to a # bd-shim v1 hook"},
		{name: "foreign comment with a bd-shim-like word", line: "# bd-shimmer: unrelated tool"},
	} {
		t.Run("bd missing, "+tc.name+", still chains", func(t *testing.T) {
			r := newHookChainRepo(t, "repo")
			seed(t, r)
			install(t, r, strings.Replace(hookChainWitnessSidecar("1"), "\n", "\n"+tc.line+"\n", 1))
			refuses(t, r, r.dir)
		})
	}

	t.Run("bd missing, sidecar without a final newline", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		install(t, r, strings.TrimSuffix(hookChainWitnessSidecar("1"), "\n"))
		refuses(t, r, r.dir)
	})

	t.Run("bd missing, compiled sidecar", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		writeHookProcessFixture(t, r.hooksDir, "pre-commit", section)
		copyHookChainBinary(t, filepath.Join(r.hooksDir, "pre-commit.old"), 0o755)
		output, ok := r.commit(t, r.dir)
		if ok {
			t.Fatalf("commit succeeded; the compiled chained gate was skipped\n%s", output)
		}
	})

	t.Run("bd missing, unreadable compiled sidecar", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		writeHookProcessFixture(t, r.hooksDir, "pre-commit", section)
		copyHookChainBinary(t, filepath.Join(r.hooksDir, "pre-commit.old"), 0o111)
		output, ok := r.commit(t, r.dir)
		if ok {
			t.Fatalf("commit succeeded; an unreadable chained gate was skipped\n%s", output)
		}
	})

	// The shell cannot read the script to classify it, so it is chained, and
	// the interpreter cannot read it either: the gate refuses rather than
	// being skipped.
	t.Run("bd missing, unreadable script sidecar", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads files regardless of mode bits")
		}
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		install(t, r, hookChainWitnessSidecar("0"))
		if err := os.Chmod(filepath.Join(r.hooksDir, "pre-commit.old"), 0o100); err != nil {
			t.Fatalf("chmod sidecar: %v", err)
		}
		head := strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
		output, ok := r.commit(t, r.dir)
		if ok {
			t.Fatalf("commit succeeded; an unreadable chained gate was skipped\n%s", output)
		}
		if after := strings.TrimSpace(r.git(t, "rev-parse", "HEAD")); after != head {
			t.Errorf("HEAD moved from %s to %s although the gate could not run", head, after)
		}
	})

	// A $0 without a slash (the hook run by name from its own directory)
	// resolves the sidecar in the working directory.
	t.Run("bd missing, hook invoked without a slash in $0", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		install(t, r, hookChainWitnessSidecar("1"))
		cmd := exec.Command("/bin/sh", "pre-commit")
		cmd.Dir = r.hooksDir
		cmd.Env = r.env
		output, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 {
			t.Fatalf("hook exit = %v, want 1 from the chained gate\n%s", err, output)
		}
		if !strings.Contains(string(output), "foreign gate says 1") {
			t.Errorf("hook output is missing the chained gate's diagnostic\n%s", output)
		}
		if runs := hookChainWitnessRuns(t, r.witness); runs != 1 {
			t.Errorf("chained gate ran %d times, want 1", runs)
		}
	})

	t.Run("bd missing, repository path with spaces", func(t *testing.T) {
		r := newHookChainRepo(t, "repo with spaces")
		seed(t, r)
		install(t, r, hookChainWitnessSidecar("1"))
		refuses(t, r, r.dir)
	})

	t.Run("bd missing, hook invoked through a symlink", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		store := t.TempDir()
		writeHookProcessFixture(t, store, "pre-commit", section)
		if err := os.Symlink(filepath.Join(store, "pre-commit"), filepath.Join(r.hooksDir, "pre-commit")); err != nil {
			t.Fatalf("link hook: %v", err)
		}
		writeHookProcessFixture(t, r.hooksDir, "pre-commit.old", hookChainWitnessSidecar("1"))
		refuses(t, r, r.dir)
	})

	t.Run("bd missing, commit from a linked worktree", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		seed(t, r)
		install(t, r, hookChainWitnessSidecar("1"))
		worktree := filepath.Join(t.TempDir(), "linked worktree")
		r.git(t, "worktree", "add", "-q", "-b", "topic", worktree)
		head := strings.TrimSpace(runHookProcessGit(t, r.gitPath, worktree, r.env, "rev-parse", "HEAD"))
		output, ok := r.commit(t, worktree)
		if ok {
			t.Fatalf("commit in a linked worktree succeeded; the chained gate was skipped\n%s", output)
		}
		if after := strings.TrimSpace(runHookProcessGit(t, r.gitPath, worktree, r.env, "rev-parse", "HEAD")); after != head {
			t.Errorf("worktree HEAD moved although the gate refused")
		}
		if runs := hookChainWitnessRuns(t, r.witness); runs != 1 {
			t.Errorf("chained gate ran %d times, want 1", runs)
		}
	})

	// A legacy inline bd hook (from bd init, GH#1120: no BEGIN/END marker,
	// only the "# bd (beads)" comment) is bd-owned and is never re-chained.
	t.Run("bd missing, legacy inline bd hook is not re-chained", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		install(t, r, strings.Replace(hookChainWitnessSidecar("9"), "\n", "\n# bd (beads)\n", 1))
		if output, ok := r.commit(t, r.dir); !ok {
			t.Fatalf("commit failed; the bd-owned inline hook was chained into\n%s", output)
		}
		if runs := hookChainWitnessRuns(t, r.witness); runs != 0 {
			t.Errorf("bd-owned .old ran %d times, want 0", runs)
		}
	})
}

// TestGeneratedPrePushChainsWhenBDUnavailable pushes through a generated
// pre-push section with no bd on PATH: the foreign pre-push.old receives git's
// remote arguments and ref lines, and its refusal stops the push.
func TestGeneratedPrePushChainsWhenBDUnavailable(t *testing.T) {
	r := newHookChainRepo(t, "repo")
	if output, ok := r.commit(t, r.dir); !ok {
		t.Fatalf("seed commit failed\n%s", output)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	runHookProcessGit(t, r.gitPath, filepath.Dir(remote), r.env, "init", "-q", "--bare", "--template=", remote)
	r.git(t, "remote", "add", "origin", remote)

	writeHookProcessFixture(t, r.hooksDir, "pre-push", "#!/bin/sh\n"+generateHookSection("pre-push"))
	writeHookProcessFixture(t, r.hooksDir, "pre-push.old", `#!/bin/sh
for _test_arg; do printf '<%s>\n' "$_test_arg"; done > "$HOOK_TEST_WITNESS.argv"
while IFS= read -r _test_line; do printf '%s\n' "$_test_line"; done > "$HOOK_TEST_WITNESS.stdin"
printf 'foreign push gate refused\n' >&2
exit 1
`)

	push := exec.Command(r.gitPath, "push", "origin", "main")
	push.Dir = r.dir
	push.Env = r.env
	output, err := push.CombinedOutput()
	if err == nil {
		t.Fatalf("push succeeded without bd on PATH; the chained pre-push.old gate was skipped\n%s", output)
	}
	if !strings.Contains(string(output), "foreign push gate refused") {
		t.Errorf("push output is missing the chained gate's diagnostic\n%s", output)
	}

	argv, err := os.ReadFile(r.witness + ".argv")
	if err != nil {
		t.Fatalf("chained pre-push.old did not record its argv: %v\n%s", err, output)
	}
	if want := "<origin>\n<" + remote + ">\n"; string(argv) != want {
		t.Errorf("chained pre-push.old argv = %q, want %q", argv, want)
	}
	head := strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
	stdin, err := os.ReadFile(r.witness + ".stdin")
	if err != nil {
		t.Fatalf("chained pre-push.old did not record its stdin: %v", err)
	}
	zero := strings.Repeat("0", len(head))
	if want := "refs/heads/main " + head + " refs/heads/main " + zero + "\n"; string(stdin) != want {
		t.Errorf("chained pre-push.old stdin = %q, want %q", stdin, want)
	}

	verify := exec.Command(r.gitPath, "--git-dir", remote, "rev-parse", "-q", "--verify", "refs/heads/main")
	verify.Env = r.env
	if out, err := verify.CombinedOutput(); err == nil {
		t.Errorf("remote received refs/heads/main although the gate refused: %s", out)
	}
}

// TestGeneratedHookChainFallsThroughToUserContent pins that a successful
// chained hook runs exactly once and then lets the content after the managed
// section run, without bd, and that with bd present the shell never runs the
// chain itself: bd hooks run owns it there.
func TestGeneratedHookChainFallsThroughToUserContent(t *testing.T) {
	trailer := "printf 'trailing user content ran\\n'\nprintf 'trailer\\n' >> \"$HOOK_TEST_WITNESS.trailer\"\n"

	t.Run("git commit without bd", func(t *testing.T) {
		r := newHookChainRepo(t, "repo")
		writeHookProcessFixture(t, r.hooksDir, "pre-commit", "#!/bin/sh\n"+generateHookSection("pre-commit")+trailer)
		writeHookProcessFixture(t, r.hooksDir, "pre-commit.old", hookChainWitnessSidecar("0"))
		output, ok := r.commit(t, r.dir)
		if !ok {
			t.Fatalf("commit failed although the chained gate passed\n%s", output)
		}
		if runs := hookChainWitnessRuns(t, r.witness); runs != 1 {
			t.Errorf("chained gate ran %d times, want 1\n%s", runs, output)
		}
		if !strings.Contains(output, "trailing user content ran") {
			t.Errorf("content after the managed section did not run\n%s", output)
		}
	})

	if runtime.GOOS == "windows" {
		return
	}
	for _, shell := range []struct {
		name   string
		option string
		posix  bool
	}{
		{name: "POSIX sh", posix: true},
		{name: "POSIX sh set -eu", option: "eu", posix: true},
		{name: "bash set -eu", option: "eu"},
		{name: "bash set -e", option: "e"},
	} {
		t.Run(shell.name+" without bd", func(t *testing.T) {
			witness := filepath.Join(t.TempDir(), "witness")
			result := runGeneratedHookProcess(t, hookProcessCase{
				noBD:         true,
				shellOption:  shell.option,
				usePOSIXSh:   shell.posix,
				trailer:      trailer,
				hookDirFiles: []hookProcessFixture{{name: "pre-push.old", body: hookChainWitnessSidecar("0")}},
				extraEnv:     []string{"HOOK_TEST_WITNESS=" + witness},
				args:         []string{"origin", "url with space"},
			})
			if result.exitCode != 0 {
				t.Fatalf("hook exit = %d, want 0\n%s", result.exitCode, result.output)
			}
			if runs := hookChainWitnessRuns(t, witness); runs != 1 {
				t.Errorf("chained hook ran %d times, want 1\n%s", runs, result.output)
			}
			if trailer, err := os.ReadFile(witness + ".trailer"); err != nil || string(trailer) != "trailer\n" {
				t.Errorf("trailing content ran %q (err %v), want once\n%s", trailer, err, result.output)
			}
		})
	}

	// A chained hook that re-enters its parent hook must not be chained a
	// second time: the parent marks the hook it is chaining.
	t.Run("parent re-entry from the chained hook runs it once", func(t *testing.T) {
		witness := filepath.Join(t.TempDir(), "witness")
		result := runGeneratedHookProcess(t, hookProcessCase{
			noBD:     true,
			trailer:  trailer,
			extraEnv: []string{"HOOK_TEST_WITNESS=" + witness},
			hookDirFiles: []hookProcessFixture{{name: "pre-push.old", body: `#!/bin/sh
printf 'ran\n' >> "$HOOK_TEST_WITNESS"
exec ./pre-push "$@"
`}},
		})
		if result.exitCode != 0 {
			t.Fatalf("hook exit = %d, want 0\n%s", result.exitCode, result.output)
		}
		if runs := hookChainWitnessRuns(t, witness); runs != 1 {
			t.Errorf("chained hook ran %d times, want 1\n%s", runs, result.output)
		}
	})

	t.Run("another hook's chain marker does not suppress this chain", func(t *testing.T) {
		witness := filepath.Join(t.TempDir(), "witness")
		result := runGeneratedHookProcess(t, hookProcessCase{
			noBD:         true,
			shellOption:  "u",
			hookDirFiles: []hookProcessFixture{{name: "pre-push.old", body: hookChainWitnessSidecar("0")}},
			extraEnv:     []string{"HOOK_TEST_WITNESS=" + witness, "BD_HOOK_CHAIN_ACTIVE=pre-commit:pre-push-x"},
		})
		if result.exitCode != 0 {
			t.Fatalf("hook exit = %d, want 0\n%s", result.exitCode, result.output)
		}
		if runs := hookChainWitnessRuns(t, witness); runs != 1 {
			t.Errorf("chained hook ran %d times, want 1\n%s", runs, result.output)
		}
	})

	t.Run("bd present leaves the chain to bd hooks run", func(t *testing.T) {
		witness := filepath.Join(t.TempDir(), "witness")
		result := runGeneratedHookProcess(t, hookProcessCase{
			fixtures:     []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}},
			trailer:      trailer,
			hookDirFiles: []hookProcessFixture{{name: "pre-push.old", body: hookChainWitnessSidecar("0")}},
			extraEnv:     []string{"HOOK_TEST_WITNESS=" + witness},
		})
		if result.exitCode != 0 {
			t.Fatalf("hook exit = %d, want 0\n%s", result.exitCode, result.output)
		}
		assertHookProcessBDInvocation(t, result.output, "pre-push")
		if runs := hookChainWitnessRuns(t, witness); runs != 0 {
			t.Errorf("the shell ran the chained hook %d times beside bd hooks run, want 0\n%s", runs, result.output)
		}
		if !strings.Contains(result.output, "trailing user content ran") {
			t.Errorf("content after the managed section did not run\n%s", result.output)
		}
	})
}

// TestGeneratedHookSectionPropagatesBDExitThree pins that a newly generated
// section returns bd's exit 3 instead of reading it as an uninitialized
// database: bd hooks run never opens a database, so a 3 can only be a chained
// hook's own verdict.
func TestGeneratedHookSectionPropagatesBDExitThree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixtures are POSIX shell scripts")
	}
	backends := map[string][]hookProcessFixture{
		"coreutils": {{name: "timeout", body: hookProcessGNUTimeoutStub}},
		"perl": {
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
			{name: "perl", body: hookProcessPerlStub},
		},
		"direct": {
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		},
	}
	for name, fixtures := range backends {
		for _, hookName := range managedHookNames {
			t.Run(name+"/"+hookName, func(t *testing.T) {
				result := runGeneratedHookProcess(t, hookProcessCase{hookName: hookName, fixtures: fixtures, bdExit: 3})
				if result.exitCode != 3 {
					t.Errorf("hook exit = %d, want bd's 3\n%s", result.exitCode, result.output)
				}
				if strings.Contains(result.output, "database not initialized") {
					t.Errorf("hook still reports bd's exit 3 as an uninitialized database\n%s", result.output)
				}
			})
		}
	}
}

// TestGeneratedGateHookRefusesTimedOutChain pins that pre-commit and pre-push
// refuse when the deadline ends bd hooks run while a foreign chained gate is
// installed: the gate may not have finished, so its verdict is unknown. The
// shell never reruns the gate. Other hooks keep continuing on timeout.
func TestGeneratedGateHookRefusesTimedOutChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixtures are POSIX shell scripts")
	}
	backends := []struct {
		name     string
		fixtures []hookProcessFixture
		bdExit   int
	}{
		{name: "coreutils 124", fixtures: []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}}, bdExit: 124},
		{name: "perl 142", fixtures: []hookProcessFixture{
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
			{name: "perl", body: hookProcessPerlStub},
		}, bdExit: 142},
	}
	for _, backend := range backends {
		for _, hookName := range []string{"pre-commit", "pre-push"} {
			t.Run(backend.name+"/"+hookName, func(t *testing.T) {
				witness := filepath.Join(t.TempDir(), "witness")
				result := runGeneratedHookProcess(t, hookProcessCase{
					hookName:     hookName,
					fixtures:     backend.fixtures,
					bdExit:       backend.bdExit,
					hookDirFiles: []hookProcessFixture{{name: hookName + ".old", body: hookChainWitnessSidecar("0")}},
					extraEnv:     []string{"HOOK_TEST_WITNESS=" + witness},
				})
				if result.exitCode != 1 {
					t.Errorf("hook exit = %d, want 1 (refuse)\n%s", result.exitCode, result.output)
				}
				if !strings.Contains(result.output, "gate outcome is unknown") {
					t.Errorf("hook output is missing the unknown-gate-outcome diagnostic\n%s", result.output)
				}
				if runs := hookChainWitnessRuns(t, witness); runs != 0 {
					t.Errorf("the shell reran the timed-out gate %d times, want 0", runs)
				}
			})
		}
		for _, hookName := range []string{"post-checkout", "prepare-commit-msg", "post-merge"} {
			t.Run(backend.name+"/"+hookName+" keeps continuing", func(t *testing.T) {
				result := runGeneratedHookProcess(t, hookProcessCase{
					hookName:     hookName,
					fixtures:     backend.fixtures,
					bdExit:       backend.bdExit,
					hookDirFiles: []hookProcessFixture{{name: hookName + ".old", body: hookChainWitnessSidecar("0")}},
					extraEnv:     []string{"HOOK_TEST_WITNESS=" + filepath.Join(t.TempDir(), "witness")},
				})
				if result.exitCode != 0 || !strings.Contains(result.output, "continuing without beads") {
					t.Errorf("hook exit = %d, want the timeout skip\n%s", result.exitCode, result.output)
				}
			})
		}
	}

	t.Run("real coreutils timeout ends a gate still running", func(t *testing.T) {
		_, helperDir := findHookProcessCoreutilsTimeout(t)
		timeout := "1"
		result := runGeneratedHookProcess(t, hookProcessCase{
			bdBody:       hookProcessLongRunningBDStub,
			timeout:      &timeout,
			pathTail:     helperDir,
			sleepPath:    findHookProcessSleep(t),
			hookDirFiles: []hookProcessFixture{{name: "pre-push.old", body: hookChainWitnessSidecar("0")}},
			extraEnv:     []string{"HOOK_TEST_WITNESS=" + filepath.Join(t.TempDir(), "witness")},
		})
		if result.exitCode != 1 || !strings.Contains(result.output, "gate outcome is unknown") {
			t.Fatalf("hook exit = %d, want the unknown-gate-outcome refusal\n%s", result.exitCode, result.output)
		}
	})
}

// TestGeneratedHookWithoutEligibleChainKeepsSkip pins the existing skip when
// there is no foreign gate to protect: without bd, or past the deadline, the
// hook still succeeds when no eligible <hook>.old exists.
func TestGeneratedHookWithoutEligibleChainKeepsSkip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixtures are POSIX shell scripts")
	}
	witnessBody := func(extra string) string {
		return "#!/bin/sh\n" + extra + "\nprintf 'ran\\n' >> \"$HOOK_TEST_WITNESS\"\nexit 1\n"
	}
	ineligible := []struct {
		name  string
		files []hookProcessFixture
		env   []string
	}{
		{name: "no sidecar"},
		{name: "non-executable sidecar"},
		{name: "directory sidecar"},
		{name: "managed section copy", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody(generateHookSection("pre-push"))}}},
		{name: "thin shim", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody("# bd-shim v2")}}},
		{name: "legacy version marker", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody("# bd-hooks-version: 0.40.0")}}},
		{name: "inline marker", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody("# bd (beads)")}}},
		{name: "delegation", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody(`exec bd hooks run pre-push "$@"`)}}},
		{name: "delegation with tabs and spaces", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody("  /usr/local/bin/bd\thooks   \trun pre-push")}}},
		{name: "parent already chaining this hook", files: []hookProcessFixture{{name: "pre-push.old", body: witnessBody("")}}, env: []string{"BD_HOOK_CHAIN_ACTIVE=pre-commit:pre-push"}},
	}
	for _, shell := range []struct {
		name   string
		option string
		posix  bool
	}{
		{name: "POSIX sh", posix: true},
		{name: "bash set -eu", option: "eu"},
	} {
		for _, tc := range ineligible {
			t.Run("bd missing/"+shell.name+"/"+tc.name, func(t *testing.T) {
				witness := filepath.Join(t.TempDir(), "witness")
				hookCase := hookProcessCase{
					noBD:         true,
					shellOption:  shell.option,
					usePOSIXSh:   shell.posix,
					hookDirFiles: tc.files,
					extraEnv:     append([]string{"HOOK_TEST_WITNESS=" + witness}, tc.env...),
					trailer:      "printf 'trailing user content ran\\n'\n",
				}
				result := runGeneratedHookProcessWithSetup(t, hookCase, func(hookDir string) {
					old := filepath.Join(hookDir, "pre-push.old")
					switch tc.name {
					case "non-executable sidecar":
						if err := os.WriteFile(old, []byte(witnessBody("")), 0o644); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(old, 0o644); err != nil {
							t.Fatal(err)
						}
					case "directory sidecar":
						if err := os.Mkdir(old, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				})
				if result.exitCode != 0 {
					t.Errorf("hook exit = %d, want the existing skip\n%s", result.exitCode, result.output)
				}
				if runs := hookChainWitnessRuns(t, witness); runs != 0 {
					t.Errorf("ineligible sidecar ran %d times, want 0\n%s", runs, result.output)
				}
				if !strings.Contains(result.output, "trailing user content ran") {
					t.Errorf("content after the managed section did not run\n%s", result.output)
				}
				if strings.Contains(result.output, "beads:") {
					t.Errorf("bd-less skip without a chain should stay silent\n%s", result.output)
				}
			})
		}
	}

	for _, backend := range []struct {
		name     string
		fixtures []hookProcessFixture
		bdExit   int
	}{
		{name: "coreutils 124", fixtures: []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}}, bdExit: 124},
		{name: "perl 142", fixtures: []hookProcessFixture{
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
			{name: "perl", body: hookProcessPerlStub},
		}, bdExit: 142},
	} {
		for _, hookName := range []string{"pre-commit", "pre-push"} {
			for _, sidecar := range []struct {
				name  string
				files []hookProcessFixture
			}{
				{name: "no sidecar"},
				{name: "bd-owned sidecar", files: []hookProcessFixture{{name: hookName + ".old", body: witnessBody("# bd-shim v2")}}},
			} {
				t.Run("deadline/"+backend.name+"/"+hookName+"/"+sidecar.name, func(t *testing.T) {
					result := runGeneratedHookProcess(t, hookProcessCase{
						hookName:     hookName,
						fixtures:     backend.fixtures,
						bdExit:       backend.bdExit,
						hookDirFiles: sidecar.files,
						extraEnv:     []string{"HOOK_TEST_WITNESS=" + filepath.Join(t.TempDir(), "witness")},
					})
					if result.exitCode != 0 {
						t.Errorf("hook exit = %d, want the timeout skip\n%s", result.exitCode, result.output)
					}
					if !strings.Contains(result.output, "continuing without beads") {
						t.Errorf("hook output is missing the timeout skip warning\n%s", result.output)
					}
				})
			}
		}
	}
}

// TestGeneratedHookSectionKeepsMarkerScannersCompatible pins that the
// section's own classifier literals do not confuse the Go code that scans hook
// files for markers: preservation, migration planning, version detection and
// marker repair must still see one intact, marker-managed section, keep the
// user's content, and write valid shell.
func TestGeneratedHookSectionKeepsMarkerScannersCompatible(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	syntaxOK := func(t *testing.T, label, content string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "hook")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(shPath, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s is not valid shell: %v\n%s\n%s", label, err, out, content)
		}
	}
	keepsUser := func(t *testing.T, label, content string) {
		t.Helper()
		for _, line := range []string{"echo user-before", "echo user-after"} {
			if !strings.Contains(content, line) {
				t.Errorf("%s lost user line %q\n%s", label, line, content)
			}
		}
	}

	for _, hookName := range managedHookNames {
		t.Run(hookName, func(t *testing.T) {
			section := generateHookSection(hookName)
			if got := strings.Count(section, hookSectionBeginPrefix); got != 1 {
				t.Errorf("section contains the BEGIN marker text %d times, want 1", got)
			}
			if got := strings.Count(section, hookSectionEndPrefix); got != 1 {
				t.Errorf("section contains the END marker text %d times, want 1", got)
			}
			for _, legacy := range []string{inlineHookMarker, "# bd-shim", "bd-hooks-version:"} {
				if strings.Contains(section, legacy) {
					t.Errorf("section contains legacy marker literal %q", legacy)
				}
			}

			content := "#!/bin/sh\necho user-before\n\n" + section + "\necho user-after\n"
			syntaxOK(t, "hook with user content", content)

			preserved, ok := shouldPreserveHookContent(content, false)
			if !ok {
				t.Fatalf("preservation dropped a hook that has user content")
			}
			keepsUser(t, "preservation", preserved)
			if strings.Contains(preserved, hookSectionBeginPrefix) || strings.Contains(preserved, "bd hooks run") {
				t.Errorf("preservation kept part of the managed section\n%s", preserved)
			}

			hookFile := filepath.Join(t.TempDir(), hookName)
			if err := os.WriteFile(hookFile, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := getHookVersion(hookFile)
			if err != nil || !info.IsBdHook || info.Version != Version {
				t.Errorf("getHookVersion = %+v, %v; want a bd hook at version %s", info, err, Version)
			}

			removed, found := removeHookSection(content)
			if !found {
				t.Fatalf("removeHookSection did not find the section")
			}
			keepsUser(t, "removal", removed)
			syntaxOK(t, "removal", removed)
			if strings.Contains(removed, "BEADS") || strings.Contains(removed, "_bd_") {
				t.Errorf("removal left part of the managed section\n%s", removed)
			}

			reinjected := injectHookSection(content, section)
			if reinjected != content {
				t.Errorf("reinstalling the same section changed the hook\n got: %s\nwant: %s", reinjected, content)
			}

			for label, broken := range map[string]string{
				"END dropped":   removeMarkerLine(content, strings.Index(content, hookSectionEndPrefix), hookSectionEndPrefix),
				"BEGIN dropped": removeMarkerLine(content, strings.Index(content, hookSectionBeginPrefix), hookSectionBeginPrefix),
			} {
				repaired := injectHookSection(broken, section)
				keepsUser(t, "repair after "+label, repaired)
				syntaxOK(t, "repair after "+label, repaired)
				if b, e := strings.Count(repaired, hookSectionBeginPrefix), strings.Count(repaired, hookSectionEndPrefix); b != 1 || e != 1 {
					t.Errorf("repair after %s has %d BEGIN and %d END markers, want 1 each\n%s", label, b, e, repaired)
				}
				stripped, _ := removeHookSection(broken)
				syntaxOK(t, "removal after "+label, stripped)
				if strings.Contains(stripped, hookSectionBeginPrefix) || strings.Contains(stripped, hookSectionEndPrefix) {
					t.Errorf("removal after %s left a marker\n%s", label, stripped)
				}
			}
		})
	}

	t.Run("migration plan", func(t *testing.T) {
		repoDir, hooksDir := setupHookMigrationRepo(t)
		for _, hookName := range managedHookNames {
			content := "#!/bin/sh\necho user-before\n\n" + generateHookSection(hookName) + "\necho user-after\n"
			writeHookMigrationFile(t, filepath.Join(hooksDir, hookName), content)
			writeHookMigrationFile(t, filepath.Join(hooksDir, hookName+".old"), "#!/bin/sh\necho foreign gate\n")
		}
		plan, err := doctor.PlanHookMigration(repoDir)
		if err != nil {
			t.Fatalf("PlanHookMigration: %v", err)
		}
		if plan.NeedsMigrationCount != 0 || plan.BrokenMarkerCount != 0 {
			t.Errorf("plan wants %d migrations and sees %d broken markers, want none", plan.NeedsMigrationCount, plan.BrokenMarkerCount)
		}
		for _, hook := range plan.Hooks {
			if hook.State != "marker_managed" || hook.LegacyBDHook {
				t.Errorf("%s planned as state %q legacy=%v, want marker_managed and not legacy", hook.Name, hook.State, hook.LegacyBDHook)
			}
		}
	})
}
