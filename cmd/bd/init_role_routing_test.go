package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
)

// TestBeadsRoleWriterIgnoresInheritedGitRouting pins the init role pair to the
// same boundary as every beads.role reader. The reader half of the stack was
// hardened first, which is what makes the writer half load-bearing: with the
// writer still inherited, `GIT_DIR=<other repo>/.git bd init` writes the role
// into that other repository, then reports success while every scrubbed reader
// looks at the repository the user is actually standing in and finds nothing.
//
// Both subtests must assert *where* the value landed. A roundtrip assertion is
// vacuous here: before the fix the write and the read followed the same
// redirect, so they agreed with each other about the wrong repository.
func TestBeadsRoleWriterIgnoresInheritedGitRouting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("role routing fixture requires Git")
	}
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}

	// localRole reads repo's own config file directly, so it reports where a
	// value physically landed rather than what discovery would resolve to.
	localRole := func(t *testing.T, repo string) string {
		t.Helper()
		cmd := exec.Command("git", "config", "--local", "--get", "beads.role")
		cmd.Dir = repo
		cmd.Env = gitenv.ScrubRoutingAndSuppression(os.Environ())
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	setLocalRole := func(t *testing.T, repo, role string) {
		t.Helper()
		cmd := exec.Command("git", "config", "--local", "beads.role", role)
		cmd.Dir = repo
		cmd.Env = gitenv.ScrubRoutingAndSuppression(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git config: %v: %s", err, out)
		}
	}
	poison := func(t *testing.T, decoy string) {
		t.Helper()
		t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
		t.Setenv("GIT_WORK_TREE", decoy)
	}

	t.Run("write stays in the selected repository", func(t *testing.T) {
		target, decoy := newGitRepo(t), newGitRepo(t)
		t.Chdir(target)
		poison(t, decoy)

		if err := setBeadsRole("contributor"); err != nil {
			t.Fatalf("setBeadsRole: %v", err)
		}
		if got := localRole(t, target); got != "contributor" {
			t.Errorf("target beads.role = %q, want %q: the write did not land where bd is standing", got, "contributor")
		}
		if got := localRole(t, decoy); got != "" {
			t.Errorf("decoy beads.role = %q, want unset: an inherited GIT_DIR captured the write", got)
		}
	})

	t.Run("read ignores a redirected repository", func(t *testing.T) {
		target, decoy := newGitRepo(t), newGitRepo(t)
		setLocalRole(t, decoy, "maintainer")
		t.Chdir(target)
		poison(t, decoy)

		// Fixture guard: the decoy really does hold a readable role, so a
		// "not maintainer" result below can only come from ignoring it.
		if got := localRole(t, decoy); got != "maintainer" {
			t.Fatalf("decoy fixture beads.role = %q, want %q", got, "maintainer")
		}
		role, hasRole := getBeadsRole()
		if hasRole {
			t.Errorf("getBeadsRole() = %q, true; want unset: the read followed an inherited GIT_DIR", role)
		}
	})

	// A redirect is not the only way to defeat the pair, and the two subtests
	// above cannot tell the two vectors apart: they pass on the GIT_DIR half
	// alone, so downgrading getBeadsRole to gitenv.ScrubRouting leaves them
	// green. Suppression is the load-bearing half here — init.go treats a
	// getBeadsRole miss as permission to write a default role — so it needs a
	// pin of its own.
	t.Run("read ignores inherited config suppression", func(t *testing.T) {
		home, target := t.TempDir(), newGitRepo(t)
		for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
			t.Setenv(key, home)
		}
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\n\trole = contributor\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(target)

		for _, tc := range []struct{ name, blind string }{
			// Fixture guard: the global role must be readable at all, and must
			// not be shadowed by a local one, or the blinded case below would
			// pass for the wrong reason.
			{"unblinded", ""},
			{"global_null", os.DevNull},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.blind != "" {
					t.Setenv("GIT_CONFIG_GLOBAL", tc.blind)
				}
				role, hasRole := getBeadsRole()
				if !hasRole || role != "contributor" {
					t.Errorf("getBeadsRole() = %q, %t; want %q, true: a blinded read is what makes init write a default role", role, hasRole, "contributor")
				}
			})
		}
	})
}
