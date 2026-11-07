package beads

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func initFingerprintGitRepo(t *testing.T, dir string, remote string) {
	t.Helper()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	if remote != "" {
		cmds = append(cmds, []string{"git", "remote", "add", "origin", remote})
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("command %v failed: %v", args, err)
		}
	}
}

func TestCanonicalizeGitURL_HTTPS(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic https",
			input:    "https://github.com/user/repo",
			expected: "github.com/user/repo",
		},
		{
			name:     "https with .git suffix",
			input:    "https://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "https with trailing slash",
			input:    "https://github.com/user/repo/",
			expected: "github.com/user/repo",
		},
		{
			name:     "https with .git and trailing slash",
			input:    "https://github.com/user/repo.git/",
			expected: "github.com/user/repo",
		},
		{
			name:     "http scheme",
			input:    "http://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "mixed case host",
			input:    "https://GitHub.COM/User/Repo.git",
			expected: "github.com/User/Repo",
		},
		{
			name:     "with standard port 443",
			input:    "https://github.com:443/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "with non-standard port",
			input:    "https://gitlab.example.com:8443/user/repo.git",
			expected: "gitlab.example.com:8443/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) returned error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeGitURL_SSH(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ssh scheme",
			input:    "ssh://git@github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "ssh with port 22",
			input:    "ssh://git@github.com:22/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "ssh with non-standard port",
			input:    "ssh://git@gitlab.example.com:2222/user/repo.git",
			expected: "gitlab.example.com:2222/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) returned error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeGitURL_SCP(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic scp-style",
			input:    "git@github.com:user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style without .git",
			input:    "git@github.com:user/repo",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style with trailing slash",
			input:    "git@github.com:user/repo/",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style mixed case host",
			input:    "git@GitHub.COM:user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style with custom user",
			input:    "deploy@gitlab.example.com:group/project.git",
			expected: "gitlab.example.com/group/project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) returned error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeGitURL_Whitespace(t *testing.T) {
	result, err := canonicalizeGitURL("  https://github.com/user/repo.git  \n")
	if err != nil {
		t.Fatalf("canonicalizeGitURL with whitespace returned error: %v", err)
	}
	if result != "github.com/user/repo" {
		t.Errorf("canonicalizeGitURL with whitespace = %q, want %q", result, "github.com/user/repo")
	}
}

func TestComputeRepoID_WithRemote(t *testing.T) {
	tmpDir := t.TempDir()

	initFingerprintGitRepo(t, tmpDir, "https://github.com/testuser/testrepo.git")

	t.Chdir(tmpDir)

	id, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() returned error: %v", err)
	}
	if id == "" {
		t.Error("ComputeRepoID() returned empty string")
	}
	if len(id) != 32 { // hex encoded 16 bytes
		t.Errorf("ComputeRepoID() returned ID of length %d, want 32", len(id))
	}

	// Same remote should produce same ID
	id2, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("second ComputeRepoID() returned error: %v", err)
	}
	if id != id2 {
		t.Errorf("ComputeRepoID() not deterministic: %q != %q", id, id2)
	}
}

func TestComputeRepoID_WithoutRemote(t *testing.T) {
	tmpDir := t.TempDir()

	initFingerprintGitRepo(t, tmpDir, "")

	t.Chdir(tmpDir)

	// Should fall back to path-based ID
	id, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() without remote returned error: %v", err)
	}
	if id == "" {
		t.Error("ComputeRepoID() without remote returned empty string")
	}
	if len(id) != 32 {
		t.Errorf("ComputeRepoID() returned ID of length %d, want 32", len(id))
	}
}

func TestComputeRepoID_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := ComputeRepoID()
	if err == nil {
		t.Error("ComputeRepoID() in non-git dir should return error")
	}
}

func TestGetCloneID(t *testing.T) {
	tmpDir := t.TempDir()

	initFingerprintGitRepo(t, tmpDir, "")

	t.Chdir(tmpDir)

	id, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() returned error: %v", err)
	}
	if id == "" {
		t.Error("GetCloneID() returned empty string")
	}
	if len(id) != 16 { // hex encoded 8 bytes
		t.Errorf("GetCloneID() returned ID of length %d, want 16", len(id))
	}

	// Deterministic
	id2, err := GetCloneID()
	if err != nil {
		t.Fatalf("second GetCloneID() returned error: %v", err)
	}
	if id != id2 {
		t.Errorf("GetCloneID() not deterministic: %q != %q", id, id2)
	}
}

func TestGetCloneID_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := GetCloneID()
	if err == nil {
		t.Error("GetCloneID() in non-git dir should return error")
	}
}

func TestGetCloneID_DifferentClonesDifferentIDs(t *testing.T) {
	// Two different git repos should produce different clone IDs
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	for _, dir := range []string{tmpDir1, tmpDir2} {
		initFingerprintGitRepo(t, dir, "")
	}

	// Get ID from first repo
	t.Chdir(tmpDir1)
	id1, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() for repo1 returned error: %v", err)
	}

	// Get ID from second repo
	if err := os.Chdir(tmpDir2); err != nil {
		t.Fatal(err)
	}
	id2, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() for repo2 returned error: %v", err)
	}

	if id1 == id2 {
		t.Errorf("different clones should have different IDs, both got %q", id1)
	}
}

func TestComputeRepoIDForPath_UsesTargetRepoOutsideCWD(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()

	initFingerprintGitRepo(t, repoA, "https://github.com/testuser/repo-a.git")
	initFingerprintGitRepo(t, repoB, "https://github.com/testuser/repo-b.git")

	t.Chdir(repoB)
	want, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() for target repo returned error: %v", err)
	}

	t.Chdir(repoA)
	got, err := ComputeRepoIDForPath(repoB)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath() returned error: %v", err)
	}

	if got != want {
		t.Errorf("ComputeRepoIDForPath(%q) = %q, want %q", repoB, got, want)
	}
}

// TestComputeRepoID_WorktreeMatchesMainRepo verifies that ComputeRepoIDForPath
// returns the same fingerprint from a worktree as from the main repo, even for
// local repos without a remote (path-based fallback). GH#2867.
func TestComputeRepoID_WorktreeMatchesMainRepo(t *testing.T) {
	mainRepo := t.TempDir()

	// Create main repo (no remote — forces path-based fingerprint)
	initFingerprintGitRepo(t, mainRepo, "")
	// Need at least one commit for worktree creation
	cmd := gitCommand("commit", "--allow-empty", "-m", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Compute fingerprint from main repo
	mainID, err := ComputeRepoIDForPath(mainRepo)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(mainRepo) error: %v", err)
	}

	// Create a worktree
	worktreeDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-wt")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepo
		_ = cmd.Run()
	})

	// Compute fingerprint from worktree — must match main repo
	wtID, err := ComputeRepoIDForPath(worktreeDir)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(worktree) error: %v", err)
	}

	if mainID != wtID {
		t.Errorf("fingerprint mismatch: main=%s worktree=%s (GH#2867)", mainID, wtID)
	}
}

// TestComputeRepoID_WorktreeWithRemoteMatchesMainRepo verifies fingerprint
// stability across worktrees when a remote is configured.
func TestComputeRepoID_WorktreeWithRemoteMatchesMainRepo(t *testing.T) {
	mainRepo := t.TempDir()

	initFingerprintGitRepo(t, mainRepo, "https://github.com/test/repo.git")
	cmd := gitCommand("commit", "--allow-empty", "-m", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	mainID, err := ComputeRepoIDForPath(mainRepo)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(mainRepo) error: %v", err)
	}

	worktreeDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-wt-remote")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepo
		_ = cmd.Run()
	})

	wtID, err := ComputeRepoIDForPath(worktreeDir)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(worktree) error: %v", err)
	}

	if mainID != wtID {
		t.Errorf("fingerprint mismatch with remote: main=%s worktree=%s", mainID, wtID)
	}
}

// TestGetCloneID_WorktreeMatchesMainRepo verifies clone ID stability across worktrees.
func TestGetCloneID_WorktreeMatchesMainRepo(t *testing.T) {
	mainRepo := t.TempDir()

	initFingerprintGitRepo(t, mainRepo, "")
	cmd := gitCommand("commit", "--allow-empty", "-m", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	mainID, err := GetCloneIDForPath(mainRepo)
	if err != nil {
		t.Fatalf("GetCloneIDForPath(mainRepo) error: %v", err)
	}

	worktreeDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-wt-clone")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepo
		_ = cmd.Run()
	})

	wtID, err := GetCloneIDForPath(worktreeDir)
	if err != nil {
		t.Fatalf("GetCloneIDForPath(worktree) error: %v", err)
	}

	if mainID != wtID {
		t.Errorf("clone ID mismatch: main=%s worktree=%s (GH#2867)", mainID, wtID)
	}
}

func TestGetCloneIDForPath_UsesTargetRepoOutsideCWD(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()

	initFingerprintGitRepo(t, repoA, "")
	initFingerprintGitRepo(t, repoB, "")

	t.Chdir(repoB)
	want, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() for target repo returned error: %v", err)
	}

	t.Chdir(repoA)
	got, err := GetCloneIDForPath(repoB)
	if err != nil {
		t.Fatalf("GetCloneIDForPath() returned error: %v", err)
	}

	if got != want {
		t.Errorf("GetCloneIDForPath(%q) = %q, want %q", repoB, got, want)
	}
}

// bd-46vla: callers need to distinguish the canonical remote-derived
// fingerprint from the host-local path fallback (bd doctor downgrades a
// path-fallback mismatch; bd migrate --update-repo-id names the propagation).
func TestComputeRepoIDForPathWithSource(t *testing.T) {
	t.Run("with remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		initFingerprintGitRepo(t, tmpDir, "https://github.com/testuser/testrepo.git")
		id, source, err := ComputeRepoIDForPathWithSource(tmpDir)
		if err != nil {
			t.Fatalf("ComputeRepoIDForPathWithSource: %v", err)
		}
		if source != RepoIDSourceRemote {
			t.Fatalf("source = %q, want %q", source, RepoIDSourceRemote)
		}
		if plain, _ := ComputeRepoIDForPath(tmpDir); plain != id {
			t.Fatalf("ComputeRepoIDForPath = %q, want %q (must agree with WithSource)", plain, id)
		}
	})

	t.Run("without remote falls back to path", func(t *testing.T) {
		tmpDir := t.TempDir()
		initFingerprintGitRepo(t, tmpDir, "")
		id, source, err := ComputeRepoIDForPathWithSource(tmpDir)
		if err != nil {
			t.Fatalf("ComputeRepoIDForPathWithSource: %v", err)
		}
		if source != RepoIDSourcePath {
			t.Fatalf("source = %q, want %q", source, RepoIDSourcePath)
		}
		if id == "" {
			t.Fatal("empty id")
		}
	})
}

// isolateFingerprintGitEnv keeps the developer's git config and SSH
// transport overrides out of the fingerprint tests.
func isolateFingerprintGitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSH", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH_VARIANT", "")
}

// fakeSSHAliasScript answers `ssh -G [opts] -- [user@]host` like OpenSSH
// with a config mapping the aliases alias-a, alias-b and github.com-work to
// HostName github.com, and every other host to itself. Like OpenSSH it
// lowercases the host before matching.
const fakeSSHAliasScript = `#!/bin/sh
[ "$1" = "-G" ] || exit 2
for dest; do :; done
host=$(printf '%s' "${dest#*@}" | tr 'A-Z' 'a-z')
echo "host $host"
echo "user git"
case "$host" in
alias-a|alias-b|github.com-work) echo "hostname github.com" ;;
*) echo "hostname $host" ;;
esac
echo "port 22"
`

// installFakeSSH puts an ssh executable running script first on PATH.
func installFakeSSH(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func repoIDForRemote(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	initFingerprintGitRepo(t, dir, remote)
	id, err := ComputeRepoIDForPath(dir)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(%s) error: %v", remote, err)
	}
	return id
}

// GH#5990: clones reaching one repository through different SSH host
// aliases must share one fingerprint, equal to the canonical-host SSH and
// HTTPS fingerprints (explicit ports keep their existing hashing).
func TestComputeRepoIDForPath_SSHAliasesOfSameRepoAgree(t *testing.T) {
	isolateFingerprintGitEnv(t)
	installFakeSSH(t, fakeSSHAliasScript)

	groups := map[string][]string{
		"default port": {
			"git@github.com:org/repo.git",
			"https://github.com/org/repo.git",
			"git@alias-a:org/repo.git",
			"git@alias-b:org/repo.git",
			"git@github.com-work:org/repo.git",
			"ssh://git@alias-a/org/repo.git",
			"git+ssh://git@ALIAS-B/org/repo",
			"ssh+git://alias-a:22/org/repo.git",
		},
		"explicit port": {
			"ssh://git@github.com:2222/org/repo.git",
			"https://github.com:2222/org/repo.git",
			"ssh://git@alias-a:2222/org/repo.git",
			"ssh://alias-b:2222/org/repo",
		},
	}
	for name, remotes := range groups {
		want := repoIDForRemote(t, remotes[0])
		for _, remote := range remotes[1:] {
			if got := repoIDForRemote(t, remote); got != want {
				t.Errorf("%s: repo id of %s = %s, want %s (same repo as %s)", name, remote, got, want, remotes[0])
			}
		}
	}
	if repoIDForRemote(t, groups["default port"][0]) == repoIDForRemote(t, groups["explicit port"][0]) {
		t.Error("explicit port 2222 must keep hashing differently from the default port")
	}
}

// stubSSHResolver replaces the ssh -G lookup with a fixed alias map and
// counts calls. Tests using it must not run in parallel.
func stubSSHResolver(t *testing.T, hostNames map[string]string) *int {
	t.Helper()
	calls := 0
	old := resolveSSHHostName
	resolveSSHHostName = func(_ context.Context, _, host, _ string) (string, error) {
		calls++
		if hostName, ok := hostNames[host]; ok {
			return hostName, nil
		}
		return host, nil
	}
	t.Cleanup(func() { resolveSSHHostName = old })
	return &calls
}

func fingerprintForRemote(t *testing.T, remote string) RepoFingerprint {
	t.Helper()
	dir := t.TempDir()
	initFingerprintGitRepo(t, dir, remote)
	fp, err := ComputeRepoFingerprintForPath(dir)
	if err != nil {
		t.Fatalf("ComputeRepoFingerprintForPath(%s) error: %v", remote, err)
	}
	return fp
}

// A resolved alias keeps the pre-change literal-host hash as LegacyID, so a
// database stamped by this checkout before the upgrade still matches.
func TestComputeRepoFingerprint_AliasResolution(t *testing.T) {
	isolateFingerprintGitEnv(t)
	stubSSHResolver(t, map[string]string{
		"alias-a":      "github.com",
		"Alias-A":      "github.com",
		"alias-dot":    "GitHub.com.",
		"alias-ip":     "192.0.2.10",
		"alias-scheme": "github.com",
	})

	tests := []struct {
		remote, legacy, resolved, alias, hostName string
	}{
		{"git@alias-a:org/repo.git", "alias-a/org/repo", "github.com/org/repo", "alias-a", "github.com"},
		{"Alias-A:org/repo/", "alias-a/org/repo", "github.com/org/repo", "alias-a", "github.com"},
		{"ssh://git@alias-scheme/org/repo.git", "alias-scheme/org/repo", "github.com/org/repo", "alias-scheme", "github.com"},
		{"git+ssh://alias-a:2222/org/repo.git", "alias-a:2222/org/repo", "github.com:2222/org/repo", "alias-a", "github.com"},
		{"ssh+git://git@alias-a:22/org/repo", "alias-a/org/repo", "github.com/org/repo", "alias-a", "github.com"},
		{"git@alias-dot:org/repo.git", "alias-dot/org/repo", "github.com/org/repo", "alias-dot", "github.com"},
		{"git@alias-ip:org/repo.git", "alias-ip/org/repo", "192.0.2.10/org/repo", "alias-ip", "192.0.2.10"},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			fp := fingerprintForRemote(t, tt.remote)
			want := RepoFingerprint{
				ID:          hashRepoIdentity(tt.resolved),
				Source:      RepoIDSourceRemote,
				LegacyID:    hashRepoIdentity(tt.legacy),
				SSHAlias:    tt.alias,
				SSHHostName: tt.hostName,
			}
			if fp != want {
				t.Fatalf("fingerprint = %+v, want %+v", fp, want)
			}
			if !fp.AliasResolved() {
				t.Fatal("AliasResolved() = false, want true")
			}
			if !fp.Matches(fp.ID) || !fp.Matches(fp.LegacyID) {
				t.Fatalf("Matches must accept both the resolved and the legacy id: %+v", fp)
			}
			if fp.Matches("") || fp.Matches(hashRepoIdentity("other/repo")) {
				t.Fatal("Matches must reject empty and unrelated ids")
			}
		})
	}

	t.Run("path fallback is unchanged", func(t *testing.T) {
		dir := t.TempDir()
		initFingerprintGitRepo(t, dir, "")
		fp, err := ComputeRepoFingerprintForPath(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := RepoFingerprint{ID: hashRepoIdentity(normalizedRepoPath(dir)), Source: RepoIDSourcePath}
		if fp != want {
			t.Fatalf("fingerprint = %+v, want %+v", fp, want)
		}
		if fp.Matches("") || !fp.Matches(fp.ID) {
			t.Fatalf("Matches on an unresolved fingerprint must accept only ID: %+v", fp)
		}
	})
}

// assertUnresolvedFingerprint checks that remote kept its pre-change id and
// no alias was marked resolved.
func assertUnresolvedFingerprint(t *testing.T, remote string, fp RepoFingerprint) {
	t.Helper()
	canonical, err := canonicalizeGitURL(remote)
	if err != nil {
		t.Fatal(err)
	}
	want := RepoFingerprint{ID: hashRepoIdentity(canonical), Source: RepoIDSourceRemote}
	if fp != want {
		t.Fatalf("fingerprint of %s = %+v, want the pre-change id %+v", remote, fp, want)
	}
	if fp.AliasResolved() {
		t.Fatalf("AliasResolved() = true for %s, want false", remote)
	}
}

// Any failure of the real ssh -G lookup keeps the pre-change id, and the
// lookup stays bounded even when ssh hangs or leaks its output pipe.
func TestComputeRepoFingerprint_Fallbacks(t *testing.T) {
	isolateFingerprintGitEnv(t)
	const remote = "git@alias-a:org/repo.git"

	oldTimeout, oldWaitDelay := sshResolveTimeout, sshResolveWaitDelay
	sshResolveTimeout, sshResolveWaitDelay = 300*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { sshResolveTimeout, sshResolveWaitDelay = oldTimeout, oldWaitDelay })

	scripts := map[string]string{
		"nonzero exit":       "#!/bin/sh\necho 'hostname github.com'\nexit 1\n",
		"unsupported -G":     "#!/bin/sh\necho 'unknown option -- G' >&2\nexit 255\n",
		"absent hostname":    "#!/bin/sh\necho 'user git'\necho 'port 22'\n",
		"empty hostname":     "#!/bin/sh\necho 'hostname'\n",
		"two-value hostname": "#!/bin/sh\necho 'hostname github.com evil.example'\n",
		"option-like":        "#!/bin/sh\necho 'hostname -oProxyCommand=evil'\n",
		"path-like":          "#!/bin/sh\necho 'hostname github.com/evil'\n",
		"deadline":           "#!/bin/sh\nexec sleep 10\n",
		"inherited pipe":     "#!/bin/sh\nsleep 10 &\necho 'hostname github.com'\n",
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			installFakeSSH(t, script)
			start := time.Now()
			fp := fingerprintForRemote(t, remote)
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("fingerprint took %v, want a bounded lookup", elapsed)
			}
			assertUnresolvedFingerprint(t, remote, fp)
		})
	}

	t.Run("CRLF output still resolves", func(t *testing.T) {
		installFakeSSH(t, "#!/bin/sh\nprintf 'user git\\r\\nHostName github.com\\r\\n'\n")
		fp := fingerprintForRemote(t, remote)
		if fp.ID != hashRepoIdentity("github.com/org/repo") {
			t.Fatalf("fingerprint = %+v, want the CRLF hostname adopted", fp)
		}
	})

	t.Run("missing ssh executable", func(t *testing.T) {
		old := sshLookPath
		sshLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		t.Cleanup(func() { sshLookPath = old })
		assertUnresolvedFingerprint(t, remote, fingerprintForRemote(t, remote))
	})

	t.Run("ssh only on a current-directory PATH entry", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fake ssh is a shell script")
		}
		work := t.TempDir()
		if err := os.MkdirAll(filepath.Join(work, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, "bin", "ssh"), []byte(fakeSSHAliasScript), 0o700); err != nil { // #nosec G306 -- test executable
			t.Fatal(err)
		}
		gitPath, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		initFingerprintGitRepo(t, dir, remote)
		t.Setenv("PATH", "bin"+string(os.PathListSeparator)+filepath.Dir(gitPath))
		t.Chdir(work)
		fp, err := ComputeRepoFingerprintForPath(dir)
		if err != nil {
			t.Fatal(err)
		}
		assertUnresolvedFingerprint(t, remote, fp)
	})
}

// Remotes that are not plain SSH destinations, or whose SSH transport Git
// would not run through the default ssh configuration, are never handed to
// the resolver.
func TestComputeRepoFingerprint_NeverResolved(t *testing.T) {
	isolateFingerprintGitEnv(t)
	calls := stubSSHResolver(t, map[string]string{"alias-a": "github.com"})

	remotes := []string{
		"git@-oProxyCommand=evil:org/repo.git",
		"ev;l@alias-a:org/repo.git",
		"ssh://-oProxyCommand=evil@alias-a/org/repo.git",
		"git@192.0.2.1:org/repo.git",
		"ssh://git@192.0.2.1:2222/org/repo.git",
		"https://alias-a/org/repo.git",
		"git://alias-a/org/repo.git",
		"file:///srv/git/repo.git",
		"/srv/git/repo.git",
		"C:/src/repo",
		"c:org/repo",
	}
	for _, remote := range remotes {
		t.Run(remote, func(t *testing.T) {
			*calls = 0
			fp := fingerprintForRemote(t, remote)
			assertUnresolvedFingerprint(t, remote, fp)
			if *calls != 0 {
				t.Fatalf("resolver called %d times for %s, want 0", *calls, remote)
			}
		})
	}

	for _, env := range []string{"GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "ssh")
			*calls = 0
			const remote = "git@alias-a:org/repo.git"
			assertUnresolvedFingerprint(t, remote, fingerprintForRemote(t, remote))
			if *calls != 0 {
				t.Fatalf("resolver called %d times with %s set, want 0", *calls, env)
			}
		})
	}

	t.Run("core.sshCommand", func(t *testing.T) {
		const remote = "git@alias-a:org/repo.git"
		dir := t.TempDir()
		initFingerprintGitRepo(t, dir, remote)
		cmd := exec.Command("git", "config", "core.sshCommand", "ssh -F /dev/null")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		*calls = 0
		fp, err := ComputeRepoFingerprintForPath(dir)
		if err != nil {
			t.Fatal(err)
		}
		assertUnresolvedFingerprint(t, remote, fp)
		if *calls != 0 {
			t.Fatalf("resolver called %d times with core.sshCommand set, want 0", *calls)
		}
	})

	t.Run("resolver error", func(t *testing.T) {
		old := resolveSSHHostName
		resolveSSHHostName = func(context.Context, string, string, string) (string, error) {
			return "", errors.New("boom")
		}
		t.Cleanup(func() { resolveSSHHostName = old })
		const remote = "git@alias-a:org/repo.git"
		assertUnresolvedFingerprint(t, remote, fingerprintForRemote(t, remote))
	})
}

// An ordinary forge host keeps its pre-change id even when the local SSH
// config points its transport elsewhere (Host github.com / HostName
// ssh.github.com); only bare aliases and strict subdomains of the HostName
// are adopted.
func TestComputeRepoFingerprint_CanonicalHostPolicy(t *testing.T) {
	isolateFingerprintGitEnv(t)

	t.Run("ssh.github.com override through real ssh -G", func(t *testing.T) {
		installFakeSSH(t, "#!/bin/sh\nfor dest; do :; done\ncase \"${dest#*@}\" in\ngithub.com) echo 'hostname ssh.github.com' ;;\n*) echo \"hostname ${dest#*@}\" ;;\nesac\n")
		const remote = "git@github.com:org/repo.git"
		fp := fingerprintForRemote(t, remote)
		assertUnresolvedFingerprint(t, remote, fp)
		if https := repoIDForRemote(t, "https://github.com/org/repo.git"); fp.ID != https {
			t.Fatalf("ssh id %s != https id %s", fp.ID, https)
		}
	})

	hostNames := map[string]string{
		"github.com":           "ssh.github.com",
		"gitlab.example.com":   "altssh.gitlab.example.com",
		"a.example.com":        "b.example.com",
		"work.github.com":      "github.com",
		"work.example.org":     "org",
		"host.xn--p1ai":        "ssh.host.xn--p1ai",
		"build.lan2":           "github.com",
		"deep.work.github.com": "github.com",
	}
	calls := stubSSHResolver(t, hostNames)

	tests := []struct {
		remote     string
		wantLookup bool
		resolved   string // empty: the pre-change id is kept
	}{
		{"git@github.com:org/repo.git", false, ""},
		{"ssh://git@GitHub.com:2222/org/repo.git", false, ""},
		{"git@host.xn--p1ai:org/repo.git", false, ""},
		{"git@gitlab.example.com:org/repo.git", true, ""},
		{"git@a.example.com:org/repo.git", true, ""},
		{"git@work.example.org:org/repo.git", true, ""},
		{"git@work.github.com:org/repo.git", true, "github.com/org/repo"},
		{"git@deep.work.github.com:org/repo.git", true, "github.com/org/repo"},
		{"git@build.lan2:org/repo.git", true, "github.com/org/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			*calls = 0
			fp := fingerprintForRemote(t, tt.remote)
			if gotLookup := *calls > 0; gotLookup != tt.wantLookup {
				t.Fatalf("lookup made = %v, want %v", gotLookup, tt.wantLookup)
			}
			if tt.resolved == "" {
				assertUnresolvedFingerprint(t, tt.remote, fp)
				return
			}
			if fp.ID != hashRepoIdentity(tt.resolved) || !fp.AliasResolved() {
				t.Fatalf("fingerprint = %+v, want id of %s", fp, tt.resolved)
			}
		})
	}
}
