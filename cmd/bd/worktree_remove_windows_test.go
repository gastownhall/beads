package main

import (
	"os"
	"testing"
)

func TestWorktreeRemoveWindowsE2ELaneAndLaneSiblingIdentity(t *testing.T) {
	fixture, upperLane, _ := newWindowsCaseSensitiveRemovalFixture(t, true)
	upperInfo, err := os.Stat(upperLane)
	if err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Stat(fixture.lane)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(upperInfo, lowerInfo) || sameWorktreePath(upperLane, fixture.lane) {
		t.Fatal("case-sensitive siblings were conflated")
	}
	result := runWorktreeRemoveProcess(t, fixture.repo, nil, fixture.lane, "--merged-into", "main")
	result.requireSuccess(t)
	if windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("lowercase target remains registered")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase target still exists: %v", err)
	}
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) {
		t.Fatal("uppercase sibling was removed")
	}
	if _, err := os.Stat(upperLane); err != nil {
		t.Fatalf("uppercase sibling was not preserved: %v", err)
	}
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/lane")
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/upper-lane")
	if got := fixture.readGitignore(t); got != "# bd worktree\nLane/\nignored/\n" {
		t.Fatalf("cleanup targeted wrong sibling: %q", got)
	}
}

func TestWorktreeRemoveWindowsE2EFinalBoundaryDisappearanceAndIgnoreCaseFlip(t *testing.T) {
	fixture, upperLane, _ := newWindowsCaseSensitiveRemovalFixture(t, true)
	upperFingerprint, err := fingerprintWorktreeFilesystem(upperLane)
	if err != nil {
		t.Fatal(err)
	}
	lowerFingerprint, err := fingerprintWorktreeFilesystem(fixture.lane)
	if err != nil {
		t.Fatal(err)
	}
	upperBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane")
	lowerBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane")
	result := runWorktreeRemoveProcess(t, fixture.repo, []string{worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookCaseRace, worktreeRemoveHelperMain + "=" + fixture.repo, worktreeRemoveHelperTarget + "=" + fixture.lane}, fixture.lane, "--merged-into", "main")
	result.requireSuccess(t)
	movedLower := fixture.lane + "-moved"
	if !windowsRegisteredWorktreePathExact(t, fixture, upperLane) || windowsRegisteredWorktreePathExact(t, fixture, fixture.lane) {
		t.Fatal("registered case siblings changed")
	}
	if _, err := os.Stat(upperLane); err != nil {
		t.Fatalf("uppercase sibling missing: %v", err)
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("lowercase target unexpectedly exists: %v", err)
	}
	if _, err := os.Stat(movedLower); err != nil {
		t.Fatalf("moved target missing: %v", err)
	}
	if got, err := fingerprintWorktreeFilesystem(upperLane); err != nil || got != upperFingerprint {
		t.Fatalf("uppercase sibling changed: %v", err)
	}
	if got, err := fingerprintWorktreeFilesystem(movedLower); err != nil || got != lowerFingerprint {
		t.Fatalf("moved target changed: %v", err)
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/upper-lane"); got != upperBranch {
		t.Fatalf("uppercase branch changed: %s", got)
	}
	if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"); got != lowerBranch {
		t.Fatalf("lowercase branch changed: %s", got)
	}
	if got := fixture.git(t, fixture.repo, "config", "--get", "core.ignorecase"); got != "true" {
		t.Fatalf("core.ignorecase = %q", got)
	}
	if got := fixture.readGitignore(t); got != "# bd worktree\nLane/\nignored/\n" {
		t.Fatalf("cleanup targeted wrong sibling: %q", got)
	}
}
