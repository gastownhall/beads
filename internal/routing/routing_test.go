package routing

import (
	"errors"
	"reflect"
	"testing"
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
