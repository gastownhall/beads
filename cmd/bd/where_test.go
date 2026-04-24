package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
)

// TestFindOriginalBeadsDir_BeadsDirEnvWithoutRedirectFallsThrough guards
// against a regression where `bd where` silently dropped its "(via redirect
// from ...)" footnote after #3230 started rebinding BEADS_DIR to the
// post-redirect target. Before the fix, an early `return ""` when BEADS_DIR
// was set but had no redirect file short-circuited the filesystem walk,
// so `bd where` from inside a redirecting worktree never reported the
// redirect source.
//
// Scenario: worktree's .beads/ holds a redirect file pointing to the
// shared .beads/, and BEADS_DIR has been set to the shared target (as
// bd's own startup does for rebound commands). findOriginalBeadsDir must
// still walk up from cwd and discover the worktree's redirecting .beads/.
func TestFindOriginalBeadsDir_BeadsDirEnvWithoutRedirectFallsThrough(t *testing.T) {
	tmp := t.TempDir()

	// Redirect source: worktree/.beads/ with a redirect file.
	worktreeRoot := filepath.Join(tmp, "worktree")
	worktreeBeads := filepath.Join(worktreeRoot, ".beads")
	if err := os.MkdirAll(worktreeBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "main", ".beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	redirectFile := filepath.Join(worktreeBeads, beads.RedirectFileName)
	if err := os.WriteFile(redirectFile, []byte(target), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate bd's post-rebind state: BEADS_DIR points at the resolved
	// target, which does NOT carry a redirect file.
	t.Setenv("BEADS_DIR", target)
	t.Chdir(worktreeRoot)

	got := findOriginalBeadsDir()
	if got == "" {
		t.Fatal("findOriginalBeadsDir returned empty; expected the redirecting worktree .beads/")
	}
	// Compare by base+parent since /var vs /private/var may be resolved either way.
	if !strings.HasSuffix(got, filepath.Join("worktree", ".beads")) {
		t.Errorf("findOriginalBeadsDir = %q, want a path ending in worktree/.beads", got)
	}
}

// TestFindOriginalBeadsDir_BeadsDirEnvWithRedirectReturnsEnv keeps the
// BEADS_DIR fast path working when the env var itself points at a
// redirecting directory (a user-set BEADS_DIR override). The env value
// should win without a filesystem walk.
func TestFindOriginalBeadsDir_BeadsDirEnvWithRedirectReturnsEnv(t *testing.T) {
	tmp := t.TempDir()
	envBeads := filepath.Join(tmp, "alt", ".beads")
	if err := os.MkdirAll(envBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "shared", ".beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envBeads, beads.RedirectFileName), []byte(target), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run cwd from an unrelated directory so the fs walk alone cannot find
	// the redirect — only the BEADS_DIR env check can.
	unrelated := filepath.Join(tmp, "unrelated")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", envBeads)
	t.Chdir(unrelated)

	got := findOriginalBeadsDir()
	// Compare by suffix for macOS /var vs /private/var differences.
	if !strings.HasSuffix(got, filepath.Join("alt", ".beads")) {
		t.Errorf("findOriginalBeadsDir = %q, want a path ending in alt/.beads", got)
	}
}
