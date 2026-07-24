package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeRemoveWindowsOrdinalGitSelectorRemovesOrdinaryWorktree(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.git(t, fixture.repo, "config", "core.ignorecase", "true")

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		nil,
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireSuccess(t)
	fixture.assertRemovedAndCleaned(t)
}

func TestWorktreeRemoveWindowsOrdinalGitSelectorProtectsCaseSiblingAfterTargetDisappears(
	t *testing.T,
) {
	fixture, upperLane, _ := newWindowsCaseSensitiveRemovalFixture(t, true)
	upperFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatalf("fingerprint uppercase sibling: %v", err)
	}
	lowerFingerprint, err := fingerprintWorktreeFilesystem(fixture.lane)
	if err != nil {
		t.Fatalf("fingerprint lowercase target: %v", err)
	}
	upperBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane")
	lowerBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane")

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookCaseRace,
			worktreeRemoveHelperMain + "=" + fixture.repo,
			worktreeRemoveHelperTarget + "=" + fixture.lane,
		},
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireSuccess(t)

	movedLower := fixture.lane + "-moved"
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase sibling registration was removed after lowercase target disappeared")
	}
	if windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("disappeared lowercase target remains registered")
	}
	if _, err := os.Stat(upperLane); err != nil {
		t.Fatalf("uppercase sibling path was not preserved: %v", err)
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase target path unexpectedly exists: %v", err)
	}
	if _, err := os.Stat(movedLower); err != nil {
		t.Fatalf("interleaved moved target was not preserved: %v", err)
	}
	gotUpperFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatalf("fingerprint preserved uppercase sibling: %v", err)
	}
	if gotUpperFingerprint != upperFingerprint {
		t.Fatal("uppercase sibling changed after lowercase target disappeared")
	}
	gotLowerFingerprint, err := fingerprintWorktreeFilesystem(movedLower)
	if err != nil {
		t.Fatalf("fingerprint interleaved moved target: %v", err)
	}
	if gotLowerFingerprint != lowerFingerprint {
		t.Fatal("interleaved moved target changed during exact-registration removal")
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"); got != upperBranch {
		t.Fatalf("uppercase branch changed: got %s, want %s", got, upperBranch)
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"); got != lowerBranch {
		t.Fatalf("lowercase branch changed: got %s, want %s", got, lowerBranch)
	}
	if got := fixture.git(t, fixture.repo, "config", "--get", "core.ignorecase"); got != "true" {
		t.Fatalf("race hook did not change core.ignorecase: got %q", got)
	}
	wantGitignore := "# bd worktree\nLane/\nignored/\n"
	if got := fixture.readGitignore(t); got != wantGitignore {
		t.Fatalf(".gitignore cleanup targeted the wrong case sibling\ngot:  %q\nwant: %q", got, wantGitignore)
	}
}

func TestWorktreeRemoveWindowsCaseSensitiveSiblingPaths(t *testing.T) {
	fixture, upperLane, _ := newWindowsCaseSensitiveRemovalFixture(t, true)

	upperInfo, err := os.Stat(upperLane)
	if err != nil {
		t.Fatalf("stat uppercase sibling: %v", err)
	}
	lowerInfo, err := os.Stat(fixture.lane)
	if err != nil {
		t.Fatalf("stat lowercase target: %v", err)
	}
	if os.SameFile(upperInfo, lowerInfo) {
		t.Fatal("case-sensitive siblings unexpectedly identify the same directory")
	}
	if sameWorktreePath(upperLane, fixture.lane) {
		t.Fatal("sameWorktreePath conflated stat-proven case-sensitive siblings")
	}

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		nil,
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireSuccess(t)
	if windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("lowercase target remains registered")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase target path still exists: %v", err)
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase sibling was removed instead of the exact lowercase target")
	}
	if _, err := os.Stat(upperLane); err != nil {
		t.Fatalf("uppercase sibling path was not preserved: %v", err)
	}
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/lane")
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/upper-lane")
	wantGitignore := "# bd worktree\nLane/\nignored/\n"
	if got := fixture.readGitignore(t); got != wantGitignore {
		t.Fatalf(".gitignore cleanup targeted the wrong sibling\ngot:  %q\nwant: %q", got, wantGitignore)
	}
}

func TestWorktreeRemoveWindowsMissingCaseVariantRefuses(t *testing.T) {
	fixture, upperLane, gitignore := newWindowsCaseSensitiveRemovalFixture(t, false)
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase target unexpectedly exists: %v", err)
	}
	if sameWorktreePath(upperLane, fixture.lane) {
		t.Fatal("sameWorktreePath aliased an existing path to a missing case variant")
	}

	beforeRegistry := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z")
	beforeHead := fixture.git(t, upperLane, "rev-parse", "HEAD")
	beforeBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane")
	beforeFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatalf("fingerprint uppercase worktree: %v", err)
	}

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		nil,
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "registered worktree not found")
	if got := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"); got != beforeRegistry {
		t.Fatalf("worktree registry changed on missing-case refusal\ngot:  %q\nwant: %q", got, beforeRegistry)
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase worktree was removed for a missing lowercase request")
	}
	if got := fixture.git(t, upperLane, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("uppercase worktree HEAD changed: got %s, want %s", got, beforeHead)
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"); got != beforeBranch {
		t.Fatalf("uppercase branch changed: got %s, want %s", got, beforeBranch)
	}
	afterFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatalf("fingerprint preserved uppercase worktree: %v", err)
	}
	if afterFingerprint != beforeFingerprint {
		t.Fatal("uppercase worktree bytes or metadata changed on missing-case refusal")
	}
	if got := fixture.readGitignore(t); got != gitignore {
		t.Fatalf(".gitignore changed on missing-case refusal\ngot:  %q\nwant: %q", got, gitignore)
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("missing lowercase path was created: %v", err)
	}
}

func TestWorktreeRemoveWindowsSinglePrunableCaseVariantRefuses(t *testing.T) {
	fixture, upperLane, upperStage, lowerStage, _ :=
		newWindowsPrunableCaseVariantFixture(t, false)
	if sameWorktreePath(upperLane, fixture.lane) {
		t.Fatal("sameWorktreePath aliased two absent case-variant paths")
	}
	before := captureWindowsPrunableState(t, fixture, upperStage, lowerStage)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		nil,
		fixture.lane,
		"--force",
	)
	result.requireFailure(t, "registered worktree not found")
	assertWindowsPrunableState(t, fixture, upperLane, upperStage, lowerStage, before)
}

func TestWorktreeRemoveWindowsTwoPrunableCaseVariantsStayDistinct(t *testing.T) {
	fixture, upperLane, upperStage, lowerStage, _ :=
		newWindowsPrunableCaseVariantFixture(t, true)
	if sameWorktreePath(upperLane, fixture.lane) {
		t.Fatal("sameWorktreePath collapsed two registered absent case variants")
	}
	before := captureWindowsPrunableState(t, fixture, upperStage, lowerStage)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		nil,
		fixture.lane,
		"--force",
	)
	result.requireFailure(t, "failed to resolve target git directory")
	assertWindowsPrunableState(t, fixture, upperLane, upperStage, lowerStage, before)
}

func TestWorktreeRemoveWindowsPrunableWrongCaseRestorationRefuses(t *testing.T) {
	fixture, upperLane, upperStage, lowerStage, _ :=
		newWindowsPrunableCaseVariantFixture(t, true)
	before := captureWindowsPrunableState(t, fixture, upperStage, lowerStage)
	adminFingerprint, err := fingerprintWorktreeFilesystem(
		filepath.Join(fixture.repo, ".git", "worktrees"),
	)
	if err != nil {
		t.Fatalf("fingerprint worktree registry: %v", err)
	}
	sentinel := filepath.Join(t.TempDir(), "restore-hook-reached")

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookRestore,
			worktreeRemoveHelperTarget + "=" + upperLane,
			worktreeRemoveHelperRestore + "=" + upperStage,
			worktreeRemoveHelperSentinel + "=" + sentinel,
		},
		fixture.lane,
		"--force",
	)
	result.requireFailure(t, "failed to resolve target git directory")
	if content, err := os.ReadFile(sentinel); err != nil {
		t.Fatalf("restore hook did not publish its sentinel: %v", err)
	} else if string(content) != "reached\n" {
		t.Fatalf("restore hook sentinel = %q, want %q", content, "reached\n")
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) ||
		!windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("wrong-case restoration removed a registered case variant")
	}
	gotAdminFingerprint, err := fingerprintWorktreeFilesystem(
		filepath.Join(fixture.repo, ".git", "worktrees"),
	)
	if err != nil {
		t.Fatalf("fingerprint preserved worktree registry: %v", err)
	}
	if gotAdminFingerprint != adminFingerprint {
		t.Fatal("worktree administrative registry changed after wrong-case restoration")
	}
	if _, err := os.Stat(upperStage); !os.IsNotExist(err) {
		t.Fatalf("uppercase staging path still exists after restoration: %v", err)
	}
	upperFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatalf("fingerprint restored uppercase worktree: %v", err)
	}
	if upperFingerprint != before.upperFingerprint {
		t.Fatal("restored uppercase worktree changed during lowercase refusal")
	}
	lowerFingerprint, err := fingerprintWorktreeFilesystem(lowerStage)
	if err != nil {
		t.Fatalf("fingerprint staged lowercase worktree: %v", err)
	}
	if lowerFingerprint != before.lowerFingerprint {
		t.Fatal("staged lowercase worktree changed during wrong-case restoration")
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"); got != before.upperBranchOID {
		t.Fatalf("uppercase branch changed: got %s, want %s", got, before.upperBranchOID)
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"); got != before.lowerBranchOID {
		t.Fatalf("lowercase branch changed: got %s, want %s", got, before.lowerBranchOID)
	}
	if got := fixture.readGitignore(t); got != before.gitignore {
		t.Fatalf(".gitignore changed after wrong-case restoration\ngot:  %q\nwant: %q", got, before.gitignore)
	}
}

type windowsPrunableState struct {
	registry         string
	upperBranchOID   string
	lowerBranchOID   string
	gitignore        string
	upperFingerprint string
	lowerFingerprint string
}

func captureWindowsPrunableState(
	t *testing.T,
	fixture *worktreeRemovalFixture,
	upperStage string,
	lowerStage string,
) windowsPrunableState {
	t.Helper()
	state := windowsPrunableState{
		registry:       fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"),
		upperBranchOID: fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"),
		gitignore:      fixture.readGitignore(t),
	}
	var err error
	state.upperFingerprint, err = fingerprintWorktreeFilesystem(upperStage)
	if err != nil {
		t.Fatalf("fingerprint staged uppercase worktree: %v", err)
	}
	if lowerStage != "" {
		state.lowerBranchOID = fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane")
		state.lowerFingerprint, err = fingerprintWorktreeFilesystem(lowerStage)
		if err != nil {
			t.Fatalf("fingerprint staged lowercase worktree: %v", err)
		}
	}
	return state
}

func assertWindowsPrunableState(
	t *testing.T,
	fixture *worktreeRemovalFixture,
	upperLane string,
	upperStage string,
	lowerStage string,
	want windowsPrunableState,
) {
	t.Helper()
	got := captureWindowsPrunableState(t, fixture, upperStage, lowerStage)
	if got != want {
		t.Fatalf("prunable worktree state mutated on refusal\ngot:  %#v\nwant: %#v", got, want)
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase prunable registration was removed")
	}
	if _, err := os.Stat(upperLane); !os.IsNotExist(err) {
		t.Fatalf("uppercase registry path unexpectedly exists: %v", err)
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase registry path unexpectedly exists: %v", err)
	}
	if lowerStage != "" && !windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("lowercase prunable registration was removed")
	}
}

func newWindowsPrunableCaseVariantFixture(
	t *testing.T,
	includeLower bool,
) (*worktreeRemovalFixture, string, string, string, string) {
	t.Helper()
	fixture, upperLane, gitignore := newWindowsCaseSensitiveRemovalFixture(t, includeLower)
	stageRoot := filepath.Join(filepath.Dir(fixture.repo), "staged")
	if err := os.Mkdir(stageRoot, 0755); err != nil {
		t.Fatalf("create staging directory: %v", err)
	}
	upperStage := filepath.Join(stageRoot, "upper")
	if err := os.Rename(upperLane, upperStage); err != nil {
		t.Fatalf("stage uppercase worktree: %v", err)
	}
	lowerStage := ""
	if includeLower {
		lowerStage = filepath.Join(stageRoot, "lower")
		if err := os.Rename(fixture.lane, lowerStage); err != nil {
			t.Fatalf("stage lowercase worktree: %v", err)
		}
	}
	return fixture, upperLane, upperStage, lowerStage, gitignore
}

func newWindowsCaseSensitiveRemovalFixture(
	t *testing.T,
	includeLower bool,
) (*worktreeRemovalFixture, string, string) {
	t.Helper()
	root := t.TempDir()
	requireWindowsCaseSensitiveDirectory(t, root)

	fixture := &worktreeRemovalFixture{
		repo:           filepath.Join(root, "repo"),
		gitignoreEntry: "lane",
	}
	fixture.lane = filepath.Join(fixture.repo, fixture.gitignoreEntry)
	upperLane := filepath.Join(fixture.repo, "Lane")
	if err := os.Mkdir(fixture.repo, 0755); err != nil {
		t.Fatalf("create repository: %v", err)
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
	if err := os.WriteFile(
		filepath.Join(fixture.repo, ".gitignore"),
		[]byte(gitignore),
		0644,
	); err != nil {
		t.Fatalf("write .gitignore: %v", err)
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

func windowsRegisteredWorktreePathExact(
	t *testing.T,
	fixture *worktreeRemovalFixture,
	want string,
) bool {
	t.Helper()
	want = filepath.Clean(want)
	wantParent, err := os.Stat(filepath.Dir(want))
	if err != nil {
		t.Fatalf("stat expected registry parent: %v", err)
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
		t.Fatalf(
			"native Windows worktree boundary requires per-directory case sensitivity: %v\n%s",
			err,
			strings.TrimSpace(string(output)),
		)
	}
}
