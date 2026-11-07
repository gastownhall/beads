package beads

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// fakeSSHAliasScript answers `ssh -G [-p port] -- [user@]host` like OpenSSH
// with a config mapping the aliases alias-a, alias-b and github.com-work to
// HostName github.com, alias-2222 and alias-2223 to HostName
// git.example.com on ports 2222 and 2223, alias-22 to git.example.com on the
// default port, and every other host to itself. Like OpenSSH it lowercases
// the host before matching, and a -p port overrides the configured one.
const fakeSSHAliasScript = `#!/bin/sh
[ "$1" = "-G" ] || exit 2
flag_port=""
[ "$2" = "-p" ] && flag_port=$3
for dest; do :; done
host=$(printf '%s' "${dest#*@}" | tr 'A-Z' 'a-z')
echo "host $host"
echo "user git"
port=22
case "$host" in
alias-a|alias-b|github.com-work) echo "hostname github.com" ;;
alias-2222) echo "hostname git.example.com"; port=2222 ;;
alias-2223) echo "hostname git.example.com"; port=2223 ;;
alias-22) echo "hostname git.example.com" ;;
*) echo "hostname $host" ;;
esac
echo "port ${flag_port:-$port}"
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

// stubSSHResolver replaces the ssh -G lookup with a fixed alias map on the
// default port (or the port handed to it) and counts calls. Tests using it
// must not run in parallel.
func stubSSHResolver(t *testing.T, hostNames map[string]string) *int {
	t.Helper()
	calls := 0
	old := resolveSSHEndpoint
	resolveSSHEndpoint = func(_ context.Context, _, host, port string) (sshEndpoint, error) {
		calls++
		if port == "" {
			port = "22"
		}
		if hostName, ok := hostNames[host]; ok {
			return sshEndpoint{hostName: hostName, port: port}, nil
		}
		return sshEndpoint{hostName: host, port: port}, nil
	}
	t.Cleanup(func() { resolveSSHEndpoint = old })
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

// assertUnresolvedFingerprint checks that remote kept its pre-change id, no
// alias was marked resolved, and nothing says resolution was blocked: the
// remote is never looked up, or ssh's answer leaves its identity unchanged.
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
	if fp.AliasResolved() || fp.AliasUnresolved() {
		t.Fatalf("AliasResolved() = %v, AliasUnresolved() = %v for %s, want both false", fp.AliasResolved(), fp.AliasUnresolved(), remote)
	}
}

// assertAliasUnresolved checks that remote kept its pre-change id because
// this session could not resolve its alias host, with a reason containing
// wantReason (any reason when wantReason is empty).
func assertAliasUnresolved(t *testing.T, remote, host, wantReason string, fp RepoFingerprint) {
	t.Helper()
	canonical, err := canonicalizeGitURL(remote)
	if err != nil {
		t.Fatal(err)
	}
	if fp.ID != hashRepoIdentity(canonical) || fp.Source != RepoIDSourceRemote || fp.LegacyID != "" || fp.AliasResolved() {
		t.Fatalf("fingerprint of %s = %+v, want the pre-change id %s, unresolved", remote, fp, hashRepoIdentity(canonical))
	}
	if fp.SSHAlias != host || !fp.AliasUnresolved() || !strings.Contains(fp.SSHAliasUnresolved, wantReason) {
		t.Fatalf("fingerprint of %s = %+v, want alias %q unresolved for a reason containing %q", remote, fp, host, wantReason)
	}
}

// Any failure of the real ssh -G lookup keeps the pre-change id and records
// why, and the lookup stays bounded even when ssh hangs or leaks its output
// pipe.
func TestComputeRepoFingerprint_Fallbacks(t *testing.T) {
	isolateFingerprintGitEnv(t)
	const remote = "git@alias-a:org/repo.git"

	oldTimeout, oldWaitDelay := sshResolveTimeout, sshResolveWaitDelay
	sshResolveTimeout, sshResolveWaitDelay = 300*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { sshResolveTimeout, sshResolveWaitDelay = oldTimeout, oldWaitDelay })

	scripts := map[string]struct{ script, reason string }{
		"nonzero exit":       {"#!/bin/sh\necho 'hostname github.com'\necho 'port 22'\nexit 1\n", "ssh -G failed"},
		"unsupported -G":     {"#!/bin/sh\necho 'unknown option -- G' >&2\nexit 255\n", "ssh -G failed"},
		"absent hostname":    {"#!/bin/sh\necho 'user git'\necho 'port 22'\n", "no hostname"},
		"absent port":        {"#!/bin/sh\necho 'user git'\necho 'hostname github.com'\n", "no port"},
		"empty hostname":     {"#!/bin/sh\necho 'hostname'\necho 'port 22'\n", "malformed hostname line"},
		"two-value hostname": {"#!/bin/sh\necho 'hostname github.com evil.example'\necho 'port 22'\n", "malformed hostname line"},
		"two-value port":     {"#!/bin/sh\necho 'hostname github.com'\necho 'port 22 2222'\n", "malformed port line"},
		"option-like":        {"#!/bin/sh\necho 'hostname -oProxyCommand=evil'\necho 'port 22'\n", "unusable hostname"},
		"path-like":          {"#!/bin/sh\necho 'hostname github.com/evil'\necho 'port 22'\n", "unusable hostname"},
		"invalid hostname":   {"#!/bin/sh\necho 'hostname _bad_.example'\necho 'port 22'\n", "unusable hostname"},
		"deadline":           {"#!/bin/sh\nexec sleep 10\n", "did not answer within"},
		// Ends at WaitDelay or at the deadline, whichever the scheduler
		// reaches first; either way with a reason.
		"inherited pipe": {"#!/bin/sh\nsleep 10 &\necho 'hostname github.com'\necho 'port 22'\n", ""},
	}
	for name, tc := range scripts {
		t.Run(name, func(t *testing.T) {
			installFakeSSH(t, tc.script)
			start := time.Now()
			fp := fingerprintForRemote(t, remote)
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("fingerprint took %v, want a bounded lookup", elapsed)
			}
			assertAliasUnresolved(t, remote, "alias-a", tc.reason, fp)
		})
	}

	t.Run("CRLF output still resolves", func(t *testing.T) {
		installFakeSSH(t, "#!/bin/sh\nprintf 'user git\\r\\nHostName github.com\\r\\nPort 22\\r\\n'\n")
		fp := fingerprintForRemote(t, remote)
		if fp.ID != hashRepoIdentity("github.com/org/repo") {
			t.Fatalf("fingerprint = %+v, want the CRLF hostname adopted", fp)
		}
	})

	t.Run("missing ssh executable", func(t *testing.T) {
		old := sshLookPath
		sshLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		t.Cleanup(func() { sshLookPath = old })
		assertAliasUnresolved(t, remote, "alias-a", "ssh was not found", fingerprintForRemote(t, remote))
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
		assertAliasUnresolved(t, remote, "alias-a", "", fp)
	})
}

// The resolver adopts only the first hostname and port lines of ssh -G
// output, needs both, and adopts the hostname only when it is a DNS
// hostname or IP literal.
func TestParseSSHConfigEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		want     string
		wantPort string
		ok       bool
	}{
		{name: "plain", out: "user git\nhostname github.com\nport 22\n", want: "github.com", wantPort: "22", ok: true},
		{name: "uppercase key", out: "HOSTNAME github.com\nPORT 22\n", want: "github.com", wantPort: "22", ok: true},
		{name: "mixed case key", out: "HostName github.com\nPort 22\n", want: "github.com", wantPort: "22", ok: true},
		{name: "crlf", out: "hostname github.com\r\nport 22\r\n", want: "github.com", wantPort: "22", ok: true},
		{name: "port before hostname", out: "port 2222\nhostname github.com\n", want: "github.com", wantPort: "2222", ok: true},
		{name: "first hostname wins", out: "hostname github.com\nhostname evil.example\nport 22\n", want: "github.com", wantPort: "22", ok: true},
		{name: "first port wins", out: "hostname github.com\nport 2222\nport 22\n", want: "github.com", wantPort: "2222", ok: true},
		{name: "tab separated", out: "hostname\tgithub.com\nport\t22\n", want: "github.com", wantPort: "22", ok: true},
		{name: "uppercase value and trailing dot", out: "hostname GitHub.COM.\nport 22\n", want: "github.com", wantPort: "22", ok: true},
		{name: "ipv4", out: "hostname 192.0.2.10\nport 22\n", want: "192.0.2.10", wantPort: "22", ok: true},
		{name: "ipv6", out: "hostname 2001:db8::1\nport 22\n", want: "2001:db8::1", wantPort: "22", ok: true},
		{name: "absent", out: "user git\nport 22\n"},
		{name: "absent port", out: "user git\nhostname github.com\n"},
		{name: "empty output", out: ""},
		{name: "empty value", out: "hostname \nport 22\n"},
		{name: "bare key line", out: "hostname\nport 22\n"},
		{name: "bare port line", out: "hostname github.com\nport\n"},
		{name: "value with space", out: "hostname two words\nport 22\n"},
		{name: "malformed first hostname before a valid one", out: "hostname\nhostname github.com\nport 22\n"},
		{name: "two-value first hostname before a valid one", out: "hostname a b\nhostname github.com\nport 22\n"},
		{name: "two-value first port before a valid one", out: "hostname github.com\nport 22 2222\nport 22\n"},
		{name: "option-like value", out: "hostname -oProxyCommand=evil\nport 22\n"},
		{name: "underscore label", out: "hostname _bad_.example\nport 22\n"},
		{name: "label ends in hyphen", out: "hostname bad-.example\nport 22\n"},
		{name: "empty label", out: "hostname github..com\nport 22\n"},
		{name: "too long", out: "hostname " + strings.Repeat("a.", 127) + "com\nport 22\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, err := parseSSHConfigEndpoint([]byte(tt.out))
			got, ok := endpoint.hostName, err == nil
			if ok {
				got, ok = normalizeSSHHostName(got)
			}
			if ok != tt.ok || (ok && (got != tt.want || endpoint.port != tt.wantPort)) {
				t.Errorf("parse+normalize(%q) = (%q, port %q, %v), want (%q, port %q, %v)", tt.out, got, endpoint.port, ok, tt.want, tt.wantPort, tt.ok)
			}
		})
	}
}

// Remotes that are not plain SSH destinations are never handed to the
// resolver. When Git would not run the default ssh configuration, the
// resolver still says whether the host is an alias, but the HostName it
// names is not adopted: that transport is recorded as the reason the alias
// stayed unresolved.
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

	for _, env := range []string{"GIT_SSH", "GIT_SSH_COMMAND"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "ssh")
			*calls = 0
			const remote = "git@alias-a:org/repo.git"
			assertAliasUnresolved(t, remote, "alias-a", env+" selects a custom SSH transport", fingerprintForRemote(t, remote))
			if *calls != 1 {
				t.Fatalf("resolver called %d times with %s set, want 1: the lookup shows the host is an alias", *calls, env)
			}
		})
	}

	// GIT_SSH_VARIANT only changes how Git passes options to the plain ssh
	// it still runs, so ssh -G still speaks for its HostName mapping.
	t.Run("GIT_SSH_VARIANT alone still resolves", func(t *testing.T) {
		t.Setenv("GIT_SSH_VARIANT", "ssh")
		*calls = 0
		fp := fingerprintForRemote(t, "git@alias-a:org/repo.git")
		if fp.ID != hashRepoIdentity("github.com/org/repo") || !fp.AliasResolved() || *calls != 1 {
			t.Fatalf("fingerprint = %+v after %d lookups, want alias-a resolved to github.com", fp, *calls)
		}
	})

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
		assertAliasUnresolved(t, remote, "alias-a", "core.sshCommand selects a custom SSH transport", fp)
		if *calls != 1 {
			t.Fatalf("resolver called %d times with core.sshCommand set, want 1: the lookup shows the host is an alias", *calls)
		}
	})

	t.Run("resolver error", func(t *testing.T) {
		old := resolveSSHEndpoint
		resolveSSHEndpoint = func(context.Context, string, string, string) (sshEndpoint, error) {
			return sshEndpoint{}, errors.New("boom")
		}
		t.Cleanup(func() { resolveSSHEndpoint = old })
		const remote = "git@alias-a:org/repo.git"
		assertAliasUnresolved(t, remote, "alias-a", "boom", fingerprintForRemote(t, remote))
	})
}

// Codex review (#8212): ssh -G also reports the port, and two aliases with
// one HostName but different configured ports can be different servers. An
// alias whose configuration selects a port other than 22 keeps its literal
// identity; a port written in the URL still overrides the configured one and
// stays in the resolved identity.
func TestComputeRepoFingerprint_ConfiguredPort(t *testing.T) {
	isolateFingerprintGitEnv(t)
	installFakeSSH(t, fakeSSHAliasScript)

	a := fingerprintForRemote(t, "git@alias-2222:org/repo.git")
	b := fingerprintForRemote(t, "git@alias-2223:org/repo.git")
	if a.ID == b.ID {
		t.Fatalf("aliases of git.example.com on ports 2222 and 2223 share fingerprint %s; they can be different servers", a.ID)
	}
	assertUnresolvedFingerprint(t, "git@alias-2222:org/repo.git", a)
	assertUnresolvedFingerprint(t, "git@alias-2223:org/repo.git", b)

	tests := []struct {
		remote, resolved string
	}{
		// The default port is the endpoint a default-port URL names.
		{"git@alias-22:org/repo.git", "git.example.com/org/repo"},
		// A port written in the URL wins over the configured one, as it does
		// for ssh itself, and keeps its existing hashing.
		{"ssh://git@alias-2222:2222/org/repo.git", "git.example.com:2222/org/repo"},
		{"ssh://git@alias-2223:22/org/repo.git", "git.example.com/org/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			fp := fingerprintForRemote(t, tt.remote)
			if fp.ID != hashRepoIdentity(tt.resolved) || !fp.AliasResolved() {
				t.Fatalf("fingerprint = %+v, want the id of %s", fp, tt.resolved)
			}
		})
	}
	if got, want := fingerprintForRemote(t, "ssh://git@alias-2222:2222/org/repo.git").ID, repoIDForRemote(t, "ssh://git@git.example.com:2222/org/repo.git"); got != want {
		t.Fatalf("alias with an explicit URL port = %s, want the explicit-port URL's id %s", got, want)
	}
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

// Review (#8212): a custom SSH transport keeps the pre-change id with a
// reason only for a host ssh -G shows is an alias whose resolution would
// change the identity. A host ssh maps to itself, to a HostName it may not
// adopt or to another port names the same endpoint in every session, so it
// keeps the pre-change id with no reason, as without the transport.
func TestComputeRepoFingerprint_CustomTransportFlagsOnlyAliases(t *testing.T) {
	isolateFingerprintGitEnv(t)
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /dev/null")

	t.Run("through ssh -G", func(t *testing.T) {
		installFakeSSH(t, fakeSSHAliasScript)
		for _, remote := range []string{
			"git@gitlab.example.com:org/repo.git",
			"git@ssh.dev.azure.com:v3/org/project/repo",
			"ssh://git@gitlab.example.com:2222/org/repo.git",
			"git@alias-2222:org/repo.git",
		} {
			assertUnresolvedFingerprint(t, remote, fingerprintForRemote(t, remote))
		}
		const alias = "git@alias-a:org/repo.git"
		assertAliasUnresolved(t, alias, "alias-a", "GIT_SSH_COMMAND selects a custom SSH transport", fingerprintForRemote(t, alias))
	})

	t.Run("HostName it may not adopt", func(t *testing.T) {
		calls := stubSSHResolver(t, map[string]string{"gitlab.example.com": "altssh.gitlab.example.com"})
		const remote = "git@gitlab.example.com:org/repo.git"
		assertUnresolvedFingerprint(t, remote, fingerprintForRemote(t, remote))
		if *calls != 1 {
			t.Fatalf("resolver called %d times, want 1", *calls)
		}
	})
}

// Review (#8212): OpenSSH echoes a host its configuration does not know, so
// a bare host ssh -G maps to itself may be an alias this machine's SSH
// config lacks while other machines resolve it. It keeps the pre-change id,
// marked unmapped, in every session; a domain name ssh maps to itself is
// taken as the server's own name.
func TestComputeRepoFingerprint_BareHostMappedToItself(t *testing.T) {
	isolateFingerprintGitEnv(t)
	installFakeSSH(t, fakeSSHAliasScript)

	tests := []struct{ remote, host string }{
		{"git@gitserver:org/repo.git", "gitserver"},
		{"ssh://git@GitServer/org/repo.git", "gitserver"},
		{"ssh://git@gitserver:2222/org/repo.git", "gitserver"},
		{"git@build.lan2:org/repo.git", "build.lan2"},
	}
	for _, session := range []struct{ name, sshCommand string }{
		{"default transport", ""},
		{"custom transport", "ssh -i /dev/null"},
	} {
		t.Run(session.name, func(t *testing.T) {
			t.Setenv("GIT_SSH_COMMAND", session.sshCommand)
			for _, tt := range tests {
				canonical, err := canonicalizeGitURL(tt.remote)
				if err != nil {
					t.Fatal(err)
				}
				want := RepoFingerprint{ID: hashRepoIdentity(canonical), Source: RepoIDSourceRemote, SSHAlias: tt.host, SSHAliasUnmapped: true}
				fp := fingerprintForRemote(t, tt.remote)
				if fp != want || !fp.AliasUnmapped() || fp.AliasUnresolved() || fp.AliasResolved() {
					t.Fatalf("fingerprint of %s = %+v, want the pre-change id marked unmapped: %+v", tt.remote, fp, want)
				}
			}
			const forge = "git@gitlab.example.com:org/repo.git"
			assertUnresolvedFingerprint(t, forge, fingerprintForRemote(t, forge))
		})
	}
}

// AliasUncertainty names the host and why another session or machine may
// compute a different id, and is empty for an id every session computes
// alike.
func TestRepoFingerprint_AliasUncertainty(t *testing.T) {
	unresolved := RepoFingerprint{ID: "aaaa", Source: RepoIDSourceRemote, SSHAlias: "alias-a", SSHAliasUnresolved: "GIT_SSH selects a custom SSH transport"}
	if got, want := unresolved.AliasUncertainty(), `hashes the SSH host alias "alias-a" as written because GIT_SSH selects a custom SSH transport`; got != want {
		t.Fatalf("unresolved AliasUncertainty() = %q, want %q", got, want)
	}
	unmapped := RepoFingerprint{ID: "aaaa", Source: RepoIDSourceRemote, SSHAlias: "gitserver", SSHAliasUnmapped: true}
	if got, want := unmapped.AliasUncertainty(), `hashes the bare SSH host "gitserver" as written because this machine's SSH config maps it to no other HostName`; got != want {
		t.Fatalf("unmapped AliasUncertainty() = %q, want %q", got, want)
	}
	for _, fp := range []RepoFingerprint{
		{ID: "aaaa", Source: RepoIDSourceRemote},
		{ID: "aaaa", Source: RepoIDSourceRemote, LegacyID: "bbbb", SSHAlias: "alias-a", SSHHostName: "github.com"},
		{ID: "aaaa", Source: RepoIDSourcePath},
	} {
		if got := fp.AliasUncertainty(); got != "" {
			t.Fatalf("AliasUncertainty() of %+v = %q, want none", fp, got)
		}
	}
}
