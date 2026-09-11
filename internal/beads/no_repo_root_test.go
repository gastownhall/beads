package beads

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestGetRepoContextAllowingNoGit_RecoversOutsideGitRepo verifies the
// git-independent entry point resolves a workspace that is not inside a git
// repository, rooting the context at the .beads parent (GH#4772).
func TestGetRepoContextAllowingNoGit_RecoversOutsideGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0o600); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
		ResetCaches()
		git.ResetCaches()
	})
	ResetCaches()
	git.ResetCaches()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Baseline: the git-requiring entry point still refuses, and does so with
	// the typed error rather than a bare fmt.Errorf.
	if _, err := GetRepoContext(); err == nil {
		t.Fatal("GetRepoContext should still fail outside a git repository")
	} else {
		var noRoot *NoRepoRootError
		if !errors.As(err, &noRoot) {
			t.Fatalf("GetRepoContext error = %v, want a *NoRepoRootError", err)
		}
	}

	rc, err := GetRepoContextAllowingNoGit()
	if err != nil {
		t.Fatalf("GetRepoContextAllowingNoGit failed outside a git repository: %v", err)
	}
	wantBeadsDir := resolveSymlinks(beadsDir)
	if rc.BeadsDir != wantBeadsDir {
		t.Errorf("BeadsDir = %q, want %q", rc.BeadsDir, wantBeadsDir)
	}
	if want := filepath.Dir(wantBeadsDir); rc.RepoRoot != want {
		t.Errorf("RepoRoot = %q, want %q (the .beads parent)", rc.RepoRoot, want)
	}
	if rc.CWDRepoRoot != "" {
		t.Errorf("CWDRepoRoot = %q, want empty outside a git repository", rc.CWDRepoRoot)
	}
	// The .beads here was found by walking up from the working directory, not
	// named by BEADS_DIR, so the synthesized context must not claim a
	// redirect. TestRecoverNoGit_RedirectProvenance covers the other side.
	if rc.IsRedirected {
		t.Error("IsRedirected = true for a .beads found by the working-directory walk")
	}
	if rc.IsWorktree {
		t.Error("IsWorktree = true with no git repository to be a worktree of")
	}
}

// TestNoRepoRootError_DoesNotMatchUnsafeLocation is the regression for the
// discriminator itself.
//
// The no-git fallback must fire on exactly one failure: "a valid,
// boundary-checked .beads was found, but there is no git root". The other
// failure buildRepoContext can return — the SEC-003 unsafe-location rejection
// — embeds the offending path verbatim in its message, so a substring test
// over the message text is controlled by the path being rejected: a workspace
// whose path contains the probe phrase would take the fallback and have its
// unsafe-location error silently cleared.
//
// errors.As over a typed error cannot be spoofed that way, which is why the
// selection is typed.
func TestNoRepoRootError_DoesNotMatchUnsafeLocation(t *testing.T) {
	const probe = "cannot determine repository root"

	hostilePath := filepath.Join("/etc", probe, ".beads")
	unsafeErr := fmt.Errorf("BEADS_DIR points to unsafe location: %s", hostilePath)

	// The old discriminator: a path-controlled false positive.
	if !strings.Contains(unsafeErr.Error(), probe) {
		t.Fatalf("test setup no longer reproduces the substring collision: %v", unsafeErr)
	}

	// The assertion that matters: drive the SELECTION, not errors.As. Asking
	// errors.As about two hand-built values tests the standard library and
	// passes no matter which discriminator this package actually uses — with
	// the typed check swapped back for strings.Contains, a test shaped that
	// way stays green while the bug it is named after is fully reintroduced.
	// recoverNoGit is that selection, so this drives it directly.
	if rc, err := recoverNoGit(nil, unsafeErr); err == nil {
		t.Errorf("unsafe-location error was recovered into a context (%+v); it must propagate", rc)
	} else if err != unsafeErr {
		t.Errorf("unsafe-location error came back changed: %v", err)
	}

	// The other two failures buildRepoContext can return must also pass
	// through untouched, for the same reason.
	noBeadsErr := fmt.Errorf("no .beads directory found")
	if _, err := recoverNoGit(nil, noBeadsErr); err != noBeadsErr {
		t.Errorf("no-.beads error came back changed: %v", err)
	}
	if rc, err := recoverNoGit(&RepoContext{BeadsDir: "/tmp/ok/.beads"}, nil); err != nil || rc == nil || rc.BeadsDir != "/tmp/ok/.beads" {
		t.Errorf("a successful context must pass through untouched: rc=%+v err=%v", rc, err)
	}

	// And the one failure that IS recoverable must be recovered, with the
	// boundary-checked directory the error carried.
	realErr := &NoRepoRootError{BeadsDir: "/tmp/ws/.beads", Err: errors.New("not a git repository")}
	rc, err := recoverNoGit(nil, realErr)
	if err != nil {
		t.Fatalf("recoverNoGit(NoRepoRootError) = %v, want a synthesized context", err)
	}
	if rc.BeadsDir != "/tmp/ws/.beads" {
		t.Errorf("BeadsDir = %q, want the boundary-checked dir carried by the error", rc.BeadsDir)
	}
	if rc.RepoRoot != filepath.Dir("/tmp/ws/.beads") {
		t.Errorf("RepoRoot = %q, want the .beads parent", rc.RepoRoot)
	}

	if !strings.Contains(realErr.Error(), probe) {
		t.Errorf("error message changed: %q — the wording is user-facing", realErr.Error())
	}
	if !errors.Is(realErr, realErr.Err) {
		t.Error("NoRepoRootError must unwrap to the underlying git failure")
	}
}

// TestRecoverNoGit_RedirectProvenance pins what the synthesized context says
// about identity, which is the question `bd context` exists to answer.
//
// isExternalBeadsDir compares git COMMON DIRS, and the CWD side cannot be
// computed without a repository — which is precisely the state this fallback
// serves. So the normal path's answer is unavailable and BEADS_DIR is the only
// evidence there is: naming a directory explicitly is a redirect, and Role()
// documents that "BEADS_DIR implies contributor (external repo mode)". Leaving
// the field zero made `bd context` deny a redirect plainly present in the
// environment, and withheld the Contributor role that goes with it.
func TestRecoverNoGit_RedirectProvenance(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	beadsDir = resolveSymlinks(beadsDir)

	t.Run("named by BEADS_DIR is redirected", func(t *testing.T) {
		t.Setenv("BEADS_DIR", beadsDir)

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if !rc.IsRedirected {
			t.Error("IsRedirected = false for a workspace named by BEADS_DIR")
		}
		if role, ok := rc.Role(); !ok || role != Contributor {
			t.Errorf("Role() = (%q, %v), want (%q, true) — BEADS_DIR implies contributor", role, ok, Contributor)
		}
	})

	t.Run("found by the CWD walk is not redirected", func(t *testing.T) {
		t.Setenv("BEADS_DIR", "")

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if rc.IsRedirected {
			t.Error("IsRedirected = true for a .beads found by walking up from the working directory")
		}
	})

	t.Run("BEADS_DIR naming a different directory is not this one", func(t *testing.T) {
		t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), "elsewhere", ".beads"))

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if rc.IsRedirected {
			t.Error("IsRedirected = true, but BEADS_DIR does not name the directory that was resolved")
		}
	})
}
