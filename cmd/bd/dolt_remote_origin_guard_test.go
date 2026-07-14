package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/doltremote"
)

// Unit tests for the git-origin collision guard helpers:
// gitOriginGetURL(), guardNormalizeURL(), doltRemoteMatchesGitOrigin().
// These fail to compile until the builder adds those functions to dolt.go (or
// a new dolt_remote_guard.go). Also tests flag registration and the
// doctor.CategoryDolt constant.
//
// Tests in this file modify the working directory via t.Chdir() and must NOT
// run in parallel.

// --- gitOriginGetURL ---

func TestGitOriginGetURL_NoGitDir_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir() // plain directory, no git repo
	t.Chdir(dir)

	got, err := gitOriginGetURL()
	if err == nil {
		t.Errorf("gitOriginGetURL() in non-git dir: expected error, got nil (url=%q)", got)
	}
	if got != "" {
		t.Errorf("gitOriginGetURL() in non-git dir: want empty string, got %q", got)
	}
}

func TestGitOriginGetURL_NoOriginRemote_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	initBareGitRepoForTest(t, dir)
	t.Chdir(dir)

	// git repo exists but no 'origin' remote is configured
	got, err := gitOriginGetURL()
	if err == nil {
		t.Errorf("gitOriginGetURL() with no origin remote: expected error, got nil (url=%q)", got)
	}
	if got != "" {
		t.Errorf("gitOriginGetURL() with no origin remote: want empty string, got %q", got)
	}
}

func TestGitOriginGetURL_WithOrigin_ReturnsURL(t *testing.T) {
	dir := t.TempDir()
	initBareGitRepoForTest(t, dir)
	wantURL := "https://github.com/org/repo.git"
	runGitCommand(t, dir, "remote", "add", "origin", wantURL)
	t.Chdir(dir)

	got, err := gitOriginGetURL()
	if err != nil {
		t.Fatalf("gitOriginGetURL() with origin: unexpected error: %v", err)
	}
	if got != wantURL {
		t.Errorf("gitOriginGetURL() = %q, want %q", got, wantURL)
	}
}

// --- doltremote.CanonicalForComparison ---
// Verify that all first-class Dolt URL forms and their git-origin equivalents
// normalize to the same canonical string, so the collision guard correctly
// detects matches regardless of which scheme Dolt stored.

func TestCanonicalForComparison_HTTPSVariants(t *testing.T) {
	// git origin (plain https) and Dolt remote (git+ prefix) must be equal.
	plain := doltremote.CanonicalForComparison("https://github.com/org/repo.git")
	gitPlus := doltremote.CanonicalForComparison("git+https://github.com/org/repo.git")
	if plain != gitPlus {
		t.Errorf("https and git+https canonical mismatch: %q vs %q", plain, gitPlus)
	}
}

func TestCanonicalForComparison_SCPAndGitSSH(t *testing.T) {
	// SCP-style git origin and Dolt's git+ssh:// form must be equal.
	scp := doltremote.CanonicalForComparison("git@github.com:org/repo.git")
	ssh := doltremote.CanonicalForComparison("git+ssh://git@github.com/org/repo.git")
	if scp != ssh {
		t.Errorf("SCP and git+ssh canonical mismatch: %q vs %q", scp, ssh)
	}
}

func TestCanonicalForComparison_TrailingSlashAndDotGit(t *testing.T) {
	// Trailing slash and .git variants of the same repo must all be equal.
	base := doltremote.CanonicalForComparison("https://github.com/org/repo")
	dotGit := doltremote.CanonicalForComparison("https://github.com/org/repo.git")
	slash := doltremote.CanonicalForComparison("https://github.com/org/repo/")
	dotGitSlash := doltremote.CanonicalForComparison("https://github.com/org/repo.git/")
	if base != dotGit || base != slash || base != dotGitSlash {
		t.Errorf("trailing-slash/.git variants differ: %q %q %q %q", base, dotGit, slash, dotGitSlash)
	}
}

func TestCanonicalForComparison_HostCaseFolded(t *testing.T) {
	// Host case must not cause false negatives in the collision guard.
	mixed := doltremote.CanonicalForComparison("https://GitHub.com/org/repo.git")
	lower := doltremote.CanonicalForComparison("https://github.com/org/repo.git")
	if mixed != lower {
		t.Errorf("host-case variants canonical mismatch: %q vs %q", mixed, lower)
	}
}

func TestCanonicalForComparison_CredentialsStripped(t *testing.T) {
	// Embedded user[:pass]@ credentials must be stripped before comparison.
	userPass := doltremote.CanonicalForComparison("https://user:pass@github.com/org/repo.git")
	tokenOnly := doltremote.CanonicalForComparison("https://ghp_xxx@github.com/org/repo.git")
	plain := doltremote.CanonicalForComparison("https://github.com/org/repo.git")
	if userPass != plain {
		t.Errorf("user:pass@ credentials not stripped: %q vs %q", userPass, plain)
	}
	if tokenOnly != plain {
		t.Errorf("token-only credentials not stripped: %q vs %q", tokenOnly, plain)
	}
}

func TestCanonicalForComparison_UserlessSCP(t *testing.T) {
	// User-less SCP form must canonicalize equal to the user form and to
	// Dolt's git+ssh:// form.
	userless := doltremote.CanonicalForComparison("github.com:org/repo.git")
	withUser := doltremote.CanonicalForComparison("git@github.com:org/repo.git")
	gitSSH := doltremote.CanonicalForComparison("git+ssh://git@github.com/org/repo.git")
	if userless != withUser {
		t.Errorf("user-less and user SCP canonical mismatch: %q vs %q", userless, withUser)
	}
	if userless != gitSSH {
		t.Errorf("user-less SCP and git+ssh canonical mismatch: %q vs %q", userless, gitSSH)
	}
}

func TestCanonicalForComparison_NonDefaultSSHUserPreserved(t *testing.T) {
	// Non-default SSH users select the remote account/home directory and
	// must NOT be stripped like HTTP(S) transport credentials - otherwise
	// distinct remotes for different accounts on the same host would
	// falsely canonicalize to the same string.
	alice := doltremote.CanonicalForComparison("alice@example.com:repo.git")
	bob := doltremote.CanonicalForComparison("bob@example.com:repo.git")
	if alice == bob {
		t.Errorf("distinct SSH users must not canonicalize equal, both got %q", alice)
	}

	aliceGitSSH := doltremote.CanonicalForComparison("git+ssh://alice@example.com/repo.git")
	if alice != aliceGitSSH {
		t.Errorf("SCP and git+ssh forms of the same non-default user must match: %q vs %q", alice, aliceGitSSH)
	}
	if bob == aliceGitSSH {
		t.Errorf("git+ssh://alice@... must not match a bob@... origin: %q vs %q", aliceGitSSH, bob)
	}
}

func TestCanonicalForComparison_DefaultGitSSHUserStillFolded(t *testing.T) {
	// The conventional "git" SSH user remains the documented default-equivalent
	// case: bare host:path and explicit git@host:path must still match.
	userless := doltremote.CanonicalForComparison("github.com:org/repo.git")
	withGitUser := doltremote.CanonicalForComparison("git@github.com:org/repo.git")
	if userless != withGitUser {
		t.Errorf("default git@ user must still fold with userless form: %q vs %q", withGitUser, userless)
	}
}

func TestCanonicalForComparison_SchemeStaysDistinct(t *testing.T) {
	// http and https must never be folded together.
	http := doltremote.CanonicalForComparison("http://github.com/org/repo")
	https := doltremote.CanonicalForComparison("https://github.com/org/repo")
	if http == https {
		t.Errorf("http and https canonical must differ, both got %q", http)
	}
}

// --- doltRemoteMatchesGitOrigin ---

func TestDoltRemoteMatchesGitOrigin_NoGitDir_ReturnsFalse(t *testing.T) {
	dir := t.TempDir() // no git repo
	t.Chdir(dir)

	// Must return false (not panic, not error) when git origin is unavailable.
	if doltRemoteMatchesGitOrigin("https://github.com/org/repo.git") {
		t.Error("doltRemoteMatchesGitOrigin(): want false when no git dir, got true")
	}
}

func TestDoltRemoteMatchesGitOrigin_MatchingURL_ReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	initBareGitRepoForTest(t, dir)
	originURL := "https://github.com/org/repo.git"
	runGitCommand(t, dir, "remote", "add", "origin", originURL)
	t.Chdir(dir)

	// Exact match.
	if !doltRemoteMatchesGitOrigin(originURL) {
		t.Errorf("doltRemoteMatchesGitOrigin(%q) = false, want true (exact match)", originURL)
	}
	// Dolt-normalized form (git+ prefix) must also match.
	if !doltRemoteMatchesGitOrigin("git+https://github.com/org/repo.git") {
		t.Error("doltRemoteMatchesGitOrigin(git+https://…) = false, want true")
	}
}

func TestDoltRemoteMatchesGitOrigin_NormalizedMatch_ReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	initBareGitRepoForTest(t, dir)
	runGitCommand(t, dir, "remote", "add", "origin", "https://github.com/org/repo.git")
	t.Chdir(dir)

	// URL without .git — should match after normalization.
	if !doltRemoteMatchesGitOrigin("https://github.com/org/repo") {
		t.Error("doltRemoteMatchesGitOrigin() = false for normalized match, want true")
	}
}

func TestDoltRemoteMatchesGitOrigin_DifferentURL_ReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	initBareGitRepoForTest(t, dir)
	runGitCommand(t, dir, "remote", "add", "origin", "https://github.com/org/repo.git")
	t.Chdir(dir)

	if doltRemoteMatchesGitOrigin("https://doltremoteapi.dolthub.com/org/repo") {
		t.Error("doltRemoteMatchesGitOrigin() = true for different URL, want false")
	}
}

// --- flag registration ---

func TestAllowGitOriginFlag_RegisteredOnDoltRemoteAddCmd(t *testing.T) {
	f := doltRemoteAddCmd.Flags().Lookup("allow-git-origin")
	if f == nil {
		t.Fatal("--allow-git-origin flag is not registered on doltRemoteAddCmd")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--allow-git-origin flag type = %q, want \"bool\"", f.Value.Type())
	}
}

// --- doctor.CategoryDolt ---

func TestCategoryDolt_Exists(t *testing.T) {
	if doctor.CategoryDolt == "" {
		t.Fatal("doctor.CategoryDolt is empty — builder must define this constant in doctor/types.go")
	}
}

// --- helpers ---

// initBareGitRepoForTest creates a minimal git repo in dir (no commits needed).
func initBareGitRepoForTest(t *testing.T, dir string) {
	t.Helper()
	runGitCommand(t, dir, "init")
	runGitCommand(t, dir, "config", "user.email", "test@test.com")
	runGitCommand(t, dir, "config", "user.name", "Test")
	runGitCommand(t, dir, "config", "core.hooksPath", "/dev/null")
}

// runGitCommand runs a git command in dir and fatals on error.
func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
