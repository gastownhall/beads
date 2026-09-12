package routing

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
)

func TestDetermineTargetRepo(t *testing.T) {
	tests := []struct {
		name     string
		config   *RoutingConfig
		userRole UserRole
		repoPath string
		want     string
	}{
		{
			name: "explicit override takes precedence",
			config: &RoutingConfig{
				Mode:             "auto",
				DefaultRepo:      "~/planning",
				MaintainerRepo:   ".",
				ContributorRepo:  "~/contributor-planning",
				ExplicitOverride: "/tmp/custom",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     "/tmp/custom",
		},
		{
			name: "auto mode - maintainer uses maintainer repo",
			config: &RoutingConfig{
				Mode:            "auto",
				MaintainerRepo:  ".",
				ContributorRepo: "~/contributor-planning",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     ".",
		},
		{
			name: "auto mode - contributor uses contributor repo",
			config: &RoutingConfig{
				Mode:            "auto",
				MaintainerRepo:  ".",
				ContributorRepo: "~/contributor-planning",
			},
			userRole: Contributor,
			repoPath: ".",
			want:     "~/contributor-planning",
		},
		{
			name: "explicit mode uses default",
			config: &RoutingConfig{
				Mode:        "explicit",
				DefaultRepo: "~/planning",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     "~/planning",
		},
		{
			name: "no config defaults to current directory",
			config: &RoutingConfig{
				Mode: "auto",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineTargetRepo(tt.config, tt.userRole, tt.repoPath)
			if got != tt.want {
				t.Errorf("DetermineTargetRepo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectUserRole_Fallback(t *testing.T) {
	// Test fallback behavior when git is not available - local projects default to maintainer
	role, err := DetectUserRole("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("DetectUserRole() error = %v, want nil", err)
	}
	if role != Maintainer {
		t.Errorf("DetectUserRole() = %v, want %v (local project fallback)", role, Maintainer)
	}
}

type gitCall struct {
	repo string
	args []string
}

type gitResponse struct {
	expect gitCall
	output string
	err    error
}

type gitStub struct {
	t         *testing.T
	responses []gitResponse
	idx       int
}

func (s *gitStub) run(repo string, args ...string) ([]byte, error) {
	if s.idx >= len(s.responses) {
		s.t.Fatalf("unexpected git call %v in repo %s", args, repo)
	}
	resp := s.responses[s.idx]
	s.idx++
	if resp.expect.repo != repo {
		s.t.Fatalf("repo mismatch: got %q want %q", repo, resp.expect.repo)
	}
	if !reflect.DeepEqual(resp.expect.args, args) {
		s.t.Fatalf("args mismatch: got %v want %v", args, resp.expect.args)
	}
	return []byte(resp.output), resp.err
}

func (s *gitStub) verify() {
	if s.idx != len(s.responses) {
		s.t.Fatalf("expected %d git calls, got %d", len(s.responses), s.idx)
	}
}

func TestDetectUserRole_ConfigOverrideMaintainer(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"", []string{"config", "--get", "beads.role"}}, output: "maintainer\n"},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s, got %s", Maintainer, role)
	}
}

func TestDetectUserRole_ConfigOverrideContributor(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"/repo", []string{"config", "--get", "beads.role"}}, output: "contributor\n"},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("/repo")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Contributor {
		t.Fatalf("expected %s, got %s", Contributor, role)
	}
}

func TestDetectUserRole_PushURLMaintainer(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"/repo", []string{"config", "--get", "beads.role"}}, output: "unknown"},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "--push", "origin"}}, output: "git@github.com:owner/repo.git"},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "upstream"}}, err: errors.New("no upstream")},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("/repo")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s, got %s", Maintainer, role)
	}
}

func TestDetectUserRole_HTTPSCredentialsMaintainer(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"/repo", []string{"config", "--get", "beads.role"}}, output: ""},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "--push", "origin"}}, output: "https://token@github.com/owner/repo.git"},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "upstream"}}, err: errors.New("no upstream")},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("/repo")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s, got %s", Maintainer, role)
	}
}

func TestDetectUserRole_HTTPSNoCredentialsContributor(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"", []string{"config", "--get", "beads.role"}}, err: errors.New("missing")},
		{expect: gitCall{"", []string{"remote", "get-url", "--push", "origin"}}, err: errors.New("no push")},
		{expect: gitCall{"", []string{"remote", "get-url", "origin"}}, output: "https://github.com/owner/repo.git"},
		{expect: gitCall{"", []string{"remote", "get-url", "upstream"}}, err: errors.New("no upstream")},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Contributor {
		t.Fatalf("expected %s, got %s", Contributor, role)
	}
}

func TestDetectUserRole_NoRemoteMaintainer(t *testing.T) {
	// When no git remote is configured, default to maintainer (local project)
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"/local", []string{"config", "--get", "beads.role"}}, err: errors.New("missing")},
		{expect: gitCall{"/local", []string{"remote", "get-url", "--push", "origin"}}, err: errors.New("no remote")},
		{expect: gitCall{"/local", []string{"remote", "get-url", "origin"}}, err: errors.New("no remote")},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("/local")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s for local project with no remote, got %s", Maintainer, role)
	}
}

func TestDetectUserRole_ForkWorkflowDefaultsToContributor(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"/repo", []string{"config", "--get", "beads.role"}}, err: errors.New("missing")},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "--push", "origin"}}, output: "git@github.com:osamu2001/zmx.git"},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "upstream"}}, output: "git@github.com:neurosnap/zmx.git"},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("/repo")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Contributor {
		t.Fatalf("expected %s, got %s", Contributor, role)
	}
}

func TestDetectUserRole_UpstreamSameRepoStillMaintainer(t *testing.T) {
	orig := gitCommandRunner
	stub := &gitStub{t: t, responses: []gitResponse{
		{expect: gitCall{"/repo", []string{"config", "--get", "beads.role"}}, output: ""},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "--push", "origin"}}, output: "git@github.com:owner/repo.git"},
		{expect: gitCall{"/repo", []string{"remote", "get-url", "upstream"}}, output: "https://github.com/owner/repo.git"},
	}}
	gitCommandRunner = stub.run
	t.Cleanup(func() {
		gitCommandRunner = orig
		stub.verify()
	})

	role, err := DetectUserRole("/repo")
	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s, got %s", Maintainer, role)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// everything written to it. Used to assert the deprecation warning is (not)
// emitted.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// TestDetectUserRole_JJSecondaryWorkspace verifies that when bd runs from a jj
// secondary workspace (which has no .git of its own), beads.role is resolved
// from the primary workspace's git config rather than falling through to the
// deprecation warning + URL heuristic. (GH#2950)
func TestDetectUserRole_JJSecondaryWorkspace(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	// Build a primary (.jj/repo is a directory) + secondary (.jj/repo is a file
	// pointing at the primary's .jj/repo) layout, mirroring real jj.
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir) // macOS /var -> /private/var
	primaryDir := filepath.Join(tmpDir, "primary")
	secondaryDir := filepath.Join(tmpDir, "secondary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0750); err != nil {
		t.Fatalf("failed to create primary .jj/repo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(secondaryDir, ".jj"), 0750); err != nil {
		t.Fatalf("failed to create secondary .jj: %v", err)
	}
	repoTarget := filepath.Join(primaryDir, ".jj", "repo")
	if err := os.WriteFile(filepath.Join(secondaryDir, ".jj", "repo"), []byte(repoTarget+"\n"), 0640); err != nil {
		t.Fatalf("failed to write secondary .jj/repo: %v", err)
	}

	if err := os.Chdir(secondaryDir); err != nil {
		t.Fatalf("failed to chdir into secondary: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
		git.ResetCaches()
	})
	git.ResetCaches()

	// Path-aware mock: the secondary has no usable git config (error), but the
	// primary returns maintainer. Match the primary loosely by suffix so we
	// don't depend on symlink/case canonicalization of the resolved path.
	orig := gitCommandRunner
	gitCommandRunner = func(repo string, args ...string) ([]byte, error) {
		if strings.HasSuffix(repo, "primary") {
			return []byte("maintainer\n"), nil
		}
		return nil, errors.New("not a git repository")
	}
	t.Cleanup(func() { gitCommandRunner = orig })

	var role UserRole
	stderr := captureStderr(t, func() {
		role, err = DetectUserRole(".")
	})

	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s, got %s", Maintainer, role)
	}
	if strings.Contains(stderr, "not configured") {
		t.Errorf("expected no role-not-configured warning, got stderr:\n%s", stderr)
	}
}

// TestDetectUserRole_JJSecondaryWorkspace_NonCwdRepoPath verifies that the jj
// secondary resolution honors the repoPath argument rather than the current
// working directory. Here cwd is a neutral, non-jj directory and the secondary
// workspace is passed explicitly as repoPath — the role must still resolve from
// the primary's git config, with no deprecation warning. (GH#2950)
func TestDetectUserRole_JJSecondaryWorkspace_NonCwdRepoPath(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir) // macOS /var -> /private/var
	primaryDir := filepath.Join(tmpDir, "primary")
	secondaryDir := filepath.Join(tmpDir, "secondary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0750); err != nil {
		t.Fatalf("failed to create primary .jj/repo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(secondaryDir, ".jj"), 0750); err != nil {
		t.Fatalf("failed to create secondary .jj: %v", err)
	}
	repoTarget := filepath.Join(primaryDir, ".jj", "repo")
	if err := os.WriteFile(filepath.Join(secondaryDir, ".jj", "repo"), []byte(repoTarget+"\n"), 0640); err != nil {
		t.Fatalf("failed to write secondary .jj/repo: %v", err)
	}

	// cwd is a neutral directory that is NOT a jj workspace. This is what
	// distinguishes this test from TestDetectUserRole_JJSecondaryWorkspace:
	// the jj resolution must come from repoPath, not cwd.
	neutralDir := filepath.Join(tmpDir, "neutral")
	if err := os.MkdirAll(neutralDir, 0750); err != nil {
		t.Fatalf("failed to create neutral dir: %v", err)
	}
	if err := os.Chdir(neutralDir); err != nil {
		t.Fatalf("failed to chdir into neutral dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
		git.ResetCaches()
	})
	git.ResetCaches()

	orig := gitCommandRunner
	gitCommandRunner = func(repo string, args ...string) ([]byte, error) {
		if strings.HasSuffix(repo, "primary") {
			return []byte("maintainer\n"), nil
		}
		return nil, errors.New("not a git repository")
	}
	t.Cleanup(func() { gitCommandRunner = orig })

	var role UserRole
	stderr := captureStderr(t, func() {
		role, err = DetectUserRole(secondaryDir)
	})

	if err != nil {
		t.Fatalf("DetectUserRole error = %v", err)
	}
	if role != Maintainer {
		t.Fatalf("expected %s, got %s", Maintainer, role)
	}
	if strings.Contains(stderr, "not configured") {
		t.Errorf("expected no role-not-configured warning, got stderr:\n%s", stderr)
	}
}

func TestDetectUserRoleIgnoresInheritedGitRouting(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Chdir(t.TempDir()) // The explicit target must also work outside CWD.
	runGit := func(t *testing.T, repo string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitenv.ScrubRouting(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, out)
		}
	}
	for _, tc := range []struct {
		name, role, global string
		inline, warning    bool
		want               UserRole
	}{
		{"repository", "maintainer", "", false, false, Maintainer},
		{"inline", "maintainer", "", true, false, Maintainer},
		{"missing_remote", "", "", false, true, Contributor},
		{"invalid_remote", "invalid", "", false, true, Contributor},
		{"default_global", "", "maintainer", true, false, Maintainer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, target, decoy := t.TempDir(), t.TempDir(), t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
				t.Setenv(key, home)
			}
			if tc.global != "" {
				if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\nrole = "+tc.global+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, repo := range []string{target, decoy} {
				runGit(t, repo, "init", "--quiet")
			}
			runGit(t, decoy, "config", "beads.role", "contributor")
			runGit(t, decoy, "remote", "add", "origin", "git@example.invalid:owner/decoy.git")
			runGit(t, target, "remote", "add", "origin", "https://example.invalid/owner/target.git")
			if tc.role != "" {
				runGit(t, target, "config", "beads.role", tc.role)
			}
			poison := map[string]string{"GIT_DIR": filepath.Join(decoy, ".git"), "GIT_WORK_TREE": decoy}
			if tc.inline {
				poison = map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "beads.role", "GIT_CONFIG_VALUE_0": "contributor"}
			}
			for key, value := range poison {
				t.Setenv(key, value)
			}
			var got UserRole
			var err error
			stderr := captureStderr(t, func() { got, err = DetectUserRole(target) })
			if err != nil || got != tc.want {
				t.Errorf("DetectUserRole(target) = %q, %v; want %q", got, err, tc.want)
			}
			if strings.Contains(stderr, "beads.role not configured") != tc.warning {
				t.Errorf("fallback warning = %q; want warning=%v", stderr, tc.warning)
			}
			for key, value := range poison {
				if os.Getenv(key) != value {
					t.Errorf("reader changed parent environment %s", key)
				}
			}
		})
	}
}
