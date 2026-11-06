package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/routing"
)

// TestDetectUserRoleForRoutingWarnsOnceOnReadFailure pins bd-a2qnf4 (GH#6765):
// when the role read behind role-dependent auto-routing fails, the CLI warns
// once per invocation — with git's diagnostic — and continues with no role,
// which routes through the default repo instead of a role-specific one.
func TestDetectUserRoleForRoutingWarnsOnceOnReadFailure(t *testing.T) {
	isolateBeadsDirForTest(t)
	oldPathWarned, oldReadWarned := rolePathNotARepoWarned, roleReadFailureWarned
	rolePathNotARepoWarned, roleReadFailureWarned = false, false
	t.Cleanup(func() { rolePathNotARepoWarned, roleReadFailureWarned = oldPathWarned, oldReadWarned })

	repo := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[beads\n\trole = maintainer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)

	cfg := &routing.RoutingConfig{
		Mode:            "auto",
		MaintainerRepo:  "maintainer-repo",
		ContributorRepo: "contributor-repo",
		DefaultRepo:     "default-repo",
	}
	if !cfg.UsesUserRole() {
		t.Fatal("fixture routing config must depend on the user role")
	}

	var first, second routing.UserRole
	stderr := captureStderr(t, func() {
		first = detectUserRoleForRouting(cfg)
		second = detectUserRoleForRouting(cfg)
	})

	if first != "" || second != "" {
		t.Errorf("roles = (%q, %q); want empty (default route) on both reads", first, second)
	}
	if n := strings.Count(stderr, "could not read beads.role"); n != 1 {
		t.Errorf("read-failure warning printed %d times, want exactly once:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, "bad config") {
		t.Errorf("warning does not carry git's diagnostic:\n%s", stderr)
	}
	// The empty role routes through the default repo, not a role-specific one.
	target, rule := routing.DetermineTargetRepoWithRule(cfg, first, ".")
	if target != "default-repo" || rule != routing.RuleDefault {
		t.Errorf("routing with empty role = (%q, %v); want (default-repo, RuleDefault)", target, rule)
	}
}
