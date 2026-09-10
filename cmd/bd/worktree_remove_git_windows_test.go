package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeRemoveGitAdapterWindowsOrdinaryIgnoreCaseRemoval(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.git(t, fixture.repo, "config", "core.ignorecase", "true")
	if err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, "main", worktreeRemoveHooks{}); err != nil {
		t.Fatal(err)
	}
	fixture.assertRemovedAndCleaned(t)
}

func TestWorktreeRemoveGitAdapterWindowsMissingCaseVariantRefuses(t *testing.T) {
	fixture, upperLane, gitignore := newWindowsCaseSensitiveRemovalFixture(t, false)
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase target unexpectedly exists: %v", err)
	}
	if sameWorktreePath(upperLane, fixture.lane) {
		t.Fatal("existing and missing case variants aliased")
	}
	beforeRegistry := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z")
	beforeHead := fixture.git(t, upperLane, "rev-parse", "HEAD")
	beforeBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane")
	beforeFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatal(err)
	}
	err = runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, "main", worktreeRemoveHooks{})
	requireWorktreeRemoveGitAdapterFailure(t, err, "registered worktree not found")
	if got := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"); got != beforeRegistry {
		t.Fatal("registry changed")
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase worktree was removed")
	}
	if got := fixture.git(t, upperLane, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatal("uppercase HEAD changed")
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"); got != beforeBranch {
		t.Fatal("uppercase branch changed")
	}
	if got, err := fingerprintWorktreeFilesystem(upperLane); err != nil || got != beforeFingerprint {
		t.Fatalf("uppercase worktree changed: %v", err)
	}
	if got := fixture.readGitignore(t); got != gitignore {
		t.Fatal(".gitignore changed")
	}
	if _, statErr := os.Stat(fixture.lane); !os.IsNotExist(statErr) {
		t.Fatalf("lowercase target exists after refusal: %v", statErr)
	}
}

func TestWorktreeRemoveGitAdapterWindowsPrunableCaseVariants(t *testing.T) {
	for _, test := range []struct {
		name         string
		includeLower bool
		want         string
	}{
		{"single variant", false, "registered worktree not found"},
		{"two variants", true, "failed to resolve target git directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, upperLane, upperStage, lowerStage, _ := newWindowsPrunableCaseVariantFixture(t, test.includeLower)
			if sameWorktreePath(upperLane, fixture.lane) {
				t.Fatal("case variants aliased")
			}
			before := captureWindowsPrunableState(t, fixture, upperStage, lowerStage)
			err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, true, "", worktreeRemoveHooks{})
			requireWorktreeRemoveGitAdapterFailure(t, err, test.want)
			assertWindowsPrunableState(t, fixture, upperLane, upperStage, lowerStage, before)
		})
	}
}

func TestWorktreeRemoveGitAdapterWindowsPrunableWrongCaseRestorationRefuses(t *testing.T) {
	fixture, upperLane, upperStage, lowerStage, _ := newWindowsPrunableCaseVariantFixture(t, true)
	before := captureWindowsPrunableState(t, fixture, upperStage, lowerStage)
	adminFingerprint, err := fingerprintWorktreeFilesystem(filepath.Join(fixture.repo, ".git", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	hooks := worktreeRemoveHooks{afterTargetResolution: func() error { return os.Rename(upperStage, upperLane) }}
	err = runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, true, "", hooks)
	requireWorktreeRemoveGitAdapterFailure(t, err, "failed to resolve target git directory")
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) || !windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("registered case variant was removed")
	}
	if got, err := fingerprintWorktreeFilesystem(filepath.Join(fixture.repo, ".git", "worktrees")); err != nil || got != adminFingerprint {
		t.Fatalf("registry changed: %v", err)
	}
	if _, err := os.Stat(upperStage); !os.IsNotExist(err) {
		t.Fatalf("staging path remains: %v", err)
	}
	if got, err := fingerprintWorktreeFilesystem(upperLane); err != nil || got != before.upperFingerprint {
		t.Fatalf("uppercase worktree changed: %v", err)
	}
	if got, err := fingerprintWorktreeFilesystem(lowerStage); err != nil || got != before.lowerFingerprint {
		t.Fatalf("lowercase worktree changed: %v", err)
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"); got != before.upperBranchOID {
		t.Fatal("uppercase branch changed")
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"); got != before.lowerBranchOID {
		t.Fatal("lowercase branch changed")
	}
	if got := fixture.readGitignore(t); got != before.gitignore {
		t.Fatal(".gitignore changed")
	}
}

type windowsPrunableState struct{ registry, upperBranchOID, lowerBranchOID, gitignore, upperFingerprint, lowerFingerprint string }

func captureWindowsPrunableState(t *testing.T, fixture *worktreeRemovalFixture, upperStage, lowerStage string) windowsPrunableState {
	t.Helper()
	state := windowsPrunableState{registry: fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"), upperBranchOID: fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"), gitignore: fixture.readGitignore(t)}
	var err error
	state.upperFingerprint, err = fingerprintWorktreeFilesystem(upperStage)
	if err != nil {
		t.Fatal(err)
	}
	if lowerStage != "" {
		state.lowerBranchOID = fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane")
		state.lowerFingerprint, err = fingerprintWorktreeFilesystem(lowerStage)
		if err != nil {
			t.Fatal(err)
		}
	}
	return state
}
func assertWindowsPrunableState(t *testing.T, fixture *worktreeRemovalFixture, upperLane, upperStage, lowerStage string, want windowsPrunableState) {
	t.Helper()
	if got := captureWindowsPrunableState(t, fixture, upperStage, lowerStage); got != want {
		t.Fatalf("prunable state mutated\ngot: %#v\nwant: %#v", got, want)
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase registration removed")
	}
	if _, err := os.Stat(upperLane); !os.IsNotExist(err) {
		t.Fatal("uppercase registry path exists")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatal("lowercase registry path exists")
	}
	if lowerStage != "" && !windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("lowercase registration removed")
	}
}
func newWindowsPrunableCaseVariantFixture(t *testing.T, includeLower bool) (*worktreeRemovalFixture, string, string, string, string) {
	t.Helper()
	fixture, upperLane, gitignore := newWindowsCaseSensitiveRemovalFixture(t, includeLower)
	stageRoot := filepath.Join(filepath.Dir(fixture.repo), "staged")
	if err := os.Mkdir(stageRoot, 0755); err != nil {
		t.Fatal(err)
	}
	upperStage := filepath.Join(stageRoot, "upper")
	if err := os.Rename(upperLane, upperStage); err != nil {
		t.Fatal(err)
	}
	lowerStage := ""
	if includeLower {
		lowerStage = filepath.Join(stageRoot, "lower")
		if err := os.Rename(fixture.lane, lowerStage); err != nil {
			t.Fatal(err)
		}
	}
	return fixture, upperLane, upperStage, lowerStage, gitignore
}
func newWindowsCaseSensitiveRemovalFixture(t *testing.T, includeLower bool) (*worktreeRemovalFixture, string, string) {
	t.Helper()
	root := t.TempDir()
	requireWindowsCaseSensitiveDirectory(t, root)
	fixture := &worktreeRemovalFixture{repo: filepath.Join(root, "repo"), gitignoreEntry: "lane"}
	fixture.lane = filepath.Join(fixture.repo, fixture.gitignoreEntry)
	upperLane := filepath.Join(fixture.repo, "Lane")
	if err := os.Mkdir(fixture.repo, 0755); err != nil {
		t.Fatal(err)
	}
	fixture.git(t, fixture.repo, "init")
	fixture.git(t, fixture.repo, "config", "user.name", worktreeRemoveTestActorName)
	fixture.git(t, fixture.repo, "config", "user.email", worktreeRemoveTestActorEmail)
	fixture.git(t, fixture.repo, "config", "commit.gpgsign", "false")
	fixture.git(t, fixture.repo, "config", "core.ignorecase", "false")
	fixture.git(t, fixture.repo, "config", "core.hooksPath", ".git/hooks")
	fixture.git(t, fixture.repo, "symbolic-ref", "HEAD", "refs/heads/main")
	gitignore := "# bd worktree\nLane/\nignored/\n"
	if includeLower {
		gitignore = "# bd worktree\nLane/\n# bd worktree\nlane/\nignored/\n"
	}
	if err := os.WriteFile(filepath.Join(fixture.repo, ".gitignore"), []byte(gitignore), 0644); err != nil {
		t.Fatal(err)
	}
	fixture.git(t, fixture.repo, "add", ".gitignore")
	fixture.git(t, fixture.repo, "commit", "-m", "base")
	fixture.baseOID = fixture.git(t, fixture.repo, "rev-parse", "HEAD")
	fixture.git(t, fixture.repo, "worktree", "add", "-b", "upper-lane", upperLane)
	if includeLower {
		fixture.git(t, fixture.repo, "worktree", "add", "-b", "lane", fixture.lane)
	}
	return fixture, upperLane, gitignore
}
func windowsRegisteredWorktreePathExact(t *testing.T, fixture *worktreeRemovalFixture, want string) bool {
	t.Helper()
	want = filepath.Clean(want)
	wantParent, err := os.Stat(filepath.Dir(want))
	if err != nil {
		t.Fatal(err)
	}
	output := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z")
	for _, field := range strings.Split(output, "\x00") {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		registered := filepath.Clean(strings.TrimPrefix(field, "worktree "))
		if filepath.Base(registered) != filepath.Base(want) {
			continue
		}
		registeredParent, err := os.Stat(filepath.Dir(registered))
		if err == nil && os.SameFile(registeredParent, wantParent) {
			return true
		}
	}
	return false
}
func requireWindowsCaseSensitiveDirectory(t *testing.T, path string) {
	t.Helper()
	set := exec.Command("fsutil.exe", "file", "SetCaseSensitiveInfo", path, "enable")
	if output, err := set.CombinedOutput(); err != nil {
		t.Fatalf("native Windows worktree boundary requires per-directory case sensitivity: %v\n%s", err, strings.TrimSpace(string(output)))
	}
}
