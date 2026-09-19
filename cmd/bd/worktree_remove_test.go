package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	worktreeRemoveHelperEnv      = "BD_WORKTREE_REMOVE_PROCESS_HELPER"
	worktreeRemoveHelperArgsEnv  = "BD_WORKTREE_REMOVE_PROCESS_ARGS"
	worktreeRemoveHelperHookEnv  = "BD_WORKTREE_REMOVE_PROCESS_HOOK"
	worktreeRemoveHelperTarget   = "BD_WORKTREE_REMOVE_PROCESS_TARGET"
	worktreeRemoveHelperMain     = "BD_WORKTREE_REMOVE_PROCESS_MAIN"
	worktreeRemoveHookReplace    = "replace-target"
	worktreeRemoveHookCaseRace   = "case-race-before-remove"
	worktreeRemoveHookGitignore  = "change-gitignore-after-remove"
	worktreeRemoveTestActorName  = "Worktree Removal Test"
	worktreeRemoveTestActorEmail = "worktree-removal@example.invalid"
)

// TestWorktreeRemoveProcessHelper runs the command only for the small E2E
// lane. Adapter contracts execute in-process in worktree_remove_git_test.go.
func TestWorktreeRemoveProcessHelper(t *testing.T) {
	if os.Getenv(worktreeRemoveHelperEnv) != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(worktreeRemoveHelperArgsEnv)), &args); err != nil {
		t.Fatalf("decode helper arguments: %v", err)
	}
	hooks := worktreeRemoveHooks{}
	switch os.Getenv(worktreeRemoveHelperHookEnv) {
	case "":
	case worktreeRemoveHookReplace:
		hooks.beforeFinalCheck = func() error {
			mainWorktree, target := os.Getenv(worktreeRemoveHelperMain), os.Getenv(worktreeRemoveHelperTarget)
			if err := runWorktreeRemoveHookGit(mainWorktree, "worktree", "remove", "--", target); err != nil {
				return err
			}
			if err := runWorktreeRemoveHookGit(mainWorktree, "worktree", "add", "--", target, "lane"); err != nil {
				return err
			}
			return runWorktreeRemoveHookGit(target, "reset", "--hard", "HEAD")
		}
	case worktreeRemoveHookGitignore:
		hooks.afterRemoval = func() error {
			path := filepath.Join(os.Getenv(worktreeRemoveHelperMain), ".gitignore")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			if _, err := file.WriteString("# concurrent change\n"); err != nil {
				_ = file.Close()
				return err
			}
			return file.Close()
		}
	case worktreeRemoveHookCaseRace:
		hooks.beforeRemove = func() error {
			target := os.Getenv(worktreeRemoveHelperTarget)
			if err := os.Rename(target, target+"-moved"); err != nil {
				return err
			}
			return runWorktreeRemoveHookGit(os.Getenv(worktreeRemoveHelperMain), "config", "core.ignorecase", "true")
		}
	default:
		t.Fatalf("unknown helper hook %q", os.Getenv(worktreeRemoveHelperHookEnv))
	}
	command := newWorktreeRemoveCommandWithHooks(hooks)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		t.FailNow()
	}
}

func runWorktreeRemoveHookGit(dir string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(scrubWorktreeRemovalGitEnv(os.Environ()), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, output)
	}
	return nil
}

func TestWorktreeRemoveCobraGrammar(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"explicit empty comparator", []string{"lane", "--merged-into="}, "--merged-into requires a non-empty value"},
		{"repeated comparator", []string{"lane", "--merged-into", "main", "--merged-into", "refs/heads/main"}, "--merged-into may be specified only once"},
		{"repeated force", []string{"lane", "--force", "--force"}, "--force may be specified only once"},
		{"force conflicts with comparator", []string{"lane", "--force", "--merged-into", "main"}, "--force and --merged-into cannot be used together"},
		{"explicit false force still conflicts with comparator", []string{"lane", "--force=false", "--merged-into", "main"}, "--force and --merged-into cannot be used together"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := newWorktreeRemoveCommand()
			command.SetArgs(test.args)
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("command error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestWorktreeRemoveE2ECleanExplicitComparatorSuccess(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)
	result := runWorktreeRemoveProcess(t, fixture.repo, nil, fixture.lane, "--merged-into", "main")
	result.requireSuccess(t)
	fixture.assertRemovedAndCleaned(t)
}

func TestWorktreeRemoveE2ERefusesBetweenObservationIdentityMutation(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)
	before := fixture.snapshot(t)
	result := runWorktreeRemoveProcess(t, fixture.repo, []string{worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookReplace, worktreeRemoveHelperTarget + "=" + fixture.lane, worktreeRemoveHelperMain + "=" + fixture.repo}, fixture.lane, "--merged-into", "main")
	result.requireFailure(t, "identity changed")
	fixture.assertSnapshot(t, before)
}

func TestWorktreeRemoveE2EReportsRemovalSuccessCleanupFailure(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)
	result := runWorktreeRemoveProcess(t, fixture.repo, []string{worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookGitignore, worktreeRemoveHelperMain + "=" + fixture.repo}, fixture.lane, "--merged-into", "main")
	result.requireFailure(t, "worktree was removed")
	if !strings.Contains(result.combined(), ".gitignore cleanup failed") {
		t.Fatalf("partial error did not name cleanup failure:\n%s", result.combined())
	}
	if fixture.registered(t, fixture.lane) {
		t.Fatal("partial outcome did not remove target")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	gitignore := fixture.readGitignore(t)
	if !strings.Contains(gitignore, "lane/") || !strings.Contains(gitignore, "# concurrent change") {
		t.Fatalf("failed cleanup overwrote concurrent .gitignore content:\n%s", gitignore)
	}
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/lane")
}

func TestScrubWorktreeRemovalGitEnv(t *testing.T) {
	input := []string{"PATH=/trusted/bin", "HOME=/home/test", "GIT_DIR=/wrong", "git_work_tree=/wrong-case", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.worktree", "GIT_OBJECT_DIRECTORY=/wrong-objects", "GIT_EXEC_PATH=/wrong-exec"}
	want := []string{"PATH=/trusted/bin", "HOME=/home/test"}
	if got := scrubWorktreeRemovalGitEnv(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("scrubWorktreeRemovalGitEnv() = %#v, want %#v", got, want)
	}
	runner, err := newWorktreeRemovalGit()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(runner.executable) {
		t.Fatalf("git executable is not absolute: %q", runner.executable)
	}
}

type worktreeRemoveProcessResult struct {
	stdout, stderr string
	exitCode       int
}

func (result worktreeRemoveProcessResult) combined() string { return result.stdout + result.stderr }
func (result worktreeRemoveProcessResult) requireSuccess(t *testing.T) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("worktree remove failed with exit code %d\nstdout:\n%s\nstderr:\n%s", result.exitCode, result.stdout, result.stderr)
	}
}
func (result worktreeRemoveProcessResult) requireFailure(t *testing.T, substring string) {
	t.Helper()
	if result.exitCode == 0 {
		t.Fatalf("worktree remove succeeded; want failure containing %q", substring)
	}
	if !strings.Contains(result.combined(), substring) {
		t.Fatalf("failure output did not contain %q\nstdout:\n%s\nstderr:\n%s", substring, result.stdout, result.stderr)
	}
}

func runWorktreeRemoveProcess(t *testing.T, dir string, extraEnv []string, args ...string) worktreeRemoveProcessResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode helper arguments: %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestWorktreeRemoveProcessHelper$")
	command.Dir = dir
	command.Env = overrideWorktreeRemoveEnv(os.Environ(), append([]string{worktreeRemoveHelperEnv + "=1", worktreeRemoveHelperArgsEnv + "=" + string(encodedArgs), "BD_DISABLE_METRICS=1"}, extraEnv...))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("launch worktree remove helper: %v", err)
		}
		exitCode = exitError.ExitCode()
	}
	return worktreeRemoveProcessResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func overrideWorktreeRemoveEnv(base, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		key := override
		if separator := strings.IndexByte(override, '='); separator >= 0 {
			key = override[:separator]
		}
		filtered := result[:0]
		for _, entry := range result {
			entryKey := entry
			if separator := strings.IndexByte(entry, '='); separator >= 0 {
				entryKey = entry[:separator]
			}
			if !strings.EqualFold(entryKey, key) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, override)
	}
	return result
}

type worktreeRemovalFixture struct{ repo, lane, baseOID, gitignoreEntry string }

func newWorktreeRemovalFixture(t *testing.T) *worktreeRemovalFixture {
	return newWorktreeRemovalFixtureAt(t, "lane")
}
func newWorktreeRemovalFixtureAt(t *testing.T, gitignoreEntry string) *worktreeRemovalFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &worktreeRemovalFixture{repo: filepath.Join(root, "repo"), gitignoreEntry: gitignoreEntry}
	fixture.lane = filepath.Join(fixture.repo, filepath.FromSlash(gitignoreEntry))
	if err := os.MkdirAll(fixture.repo, 0755); err != nil {
		t.Fatal(err)
	}
	fixture.git(t, fixture.repo, "init")
	fixture.git(t, fixture.repo, "config", "user.name", worktreeRemoveTestActorName)
	fixture.git(t, fixture.repo, "config", "user.email", worktreeRemoveTestActorEmail)
	fixture.git(t, fixture.repo, "config", "commit.gpgsign", "false")
	fixture.git(t, fixture.repo, "config", "core.hooksPath", ".git/hooks")
	fixture.git(t, fixture.repo, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(fixture.repo, ".gitignore"), []byte(fmt.Sprintf("# bd worktree\n%s/\nignored/\n", fixture.gitignoreEntry)), 0644); err != nil {
		t.Fatal(err)
	}
	fixture.git(t, fixture.repo, "add", ".gitignore")
	fixture.git(t, fixture.repo, "commit", "-m", "base")
	fixture.baseOID = fixture.git(t, fixture.repo, "rev-parse", "HEAD")
	fixture.git(t, fixture.repo, "worktree", "add", "-b", "lane", fixture.lane)
	return fixture
}
func (fixture *worktreeRemovalFixture) setUpstream(t *testing.T) {
	t.Helper()
	fixture.git(t, fixture.lane, "branch", "--set-upstream-to=main", "lane")
}
func (fixture *worktreeRemovalFixture) commitLane(t *testing.T, message string) {
	t.Helper()
	fixture.git(t, fixture.lane, "commit", "--allow-empty", "-m", message)
}
func (fixture *worktreeRemovalFixture) mergeLaneIntoMain(t *testing.T) {
	t.Helper()
	fixture.git(t, fixture.repo, "merge", "--ff-only", "lane")
}
func (fixture *worktreeRemovalFixture) writeLaneFile(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(fixture.lane, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func (fixture *worktreeRemovalFixture) writeTargetPseudoref(t *testing.T, name string) {
	t.Helper()
	gitPath := fixture.git(t, fixture.lane, "rev-parse", "--git-path", name)
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(fixture.lane, gitPath)
	}
	head := fixture.git(t, fixture.lane, "rev-parse", "HEAD")
	if err := os.WriteFile(gitPath, []byte(head+"\n"), 0644); err != nil {
		t.Fatalf("write target pseudoref %s: %v", name, err)
	}
}
func (fixture *worktreeRemovalFixture) registered(t *testing.T, path string) bool {
	t.Helper()
	output := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z")
	for _, field := range strings.Split(output, "\x00") {
		if strings.HasPrefix(field, "worktree ") && sameWorktreePath(strings.TrimPrefix(field, "worktree "), path) {
			return true
		}
	}
	return false
}

type worktreeRemovalSnapshot struct{ registry, head, status, branchOID, gitignore string }

func (fixture *worktreeRemovalFixture) snapshot(t *testing.T) worktreeRemovalSnapshot {
	t.Helper()
	return worktreeRemovalSnapshot{registry: fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"), head: fixture.git(t, fixture.lane, "rev-parse", "HEAD"), status: fixture.git(t, fixture.lane, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching"), branchOID: fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"), gitignore: fixture.readGitignore(t)}
}
func (fixture *worktreeRemovalFixture) assertSnapshot(t *testing.T, want worktreeRemovalSnapshot) {
	t.Helper()
	if got := fixture.snapshot(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree state mutated on refusal\ngot: %#v\nwant: %#v", got, want)
	}
	if !fixture.registered(t, fixture.lane) {
		t.Fatal("target is no longer registered after refusal")
	}
}
func (fixture *worktreeRemovalFixture) readGitignore(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixture.repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
func (fixture *worktreeRemovalFixture) assertRemovedAndCleaned(t *testing.T) {
	t.Helper()
	if fixture.registered(t, fixture.lane) {
		t.Fatal("target remains registered")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("target path still exists: %v", err)
	}
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/lane")
	if strings.Contains(fixture.readGitignore(t), fixture.gitignoreEntry+"/") {
		t.Fatalf(".gitignore still contains entry")
	}
}
func (fixture *worktreeRemovalFixture) git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(scrubWorktreeRemovalGitEnv(os.Environ()), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
