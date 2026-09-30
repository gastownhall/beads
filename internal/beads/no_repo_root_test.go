package beads

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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

	// An inherited BEADS_DIR wins at FindBeadsDir step 1 (beads.go:929) and would
	// bind an unrelated workspace instead of the one this test just built, so the
	// walk-discovery assertions below would grade the ambient environment rather
	// than the code. The package TestMain scrubs HOME and GIT_CONFIG_* but not
	// BEADS_DIR; three sibling tests already use this per-test convention.
	t.Setenv("BEADS_DIR", "")

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
	// The .beads here is discoverable by walking up from the working directory, so
	// the caller is standing in this workspace and the synthesized context must
	// not claim a redirect. Because externality is decided positionally rather
	// than from an environment snapshot, the BEADS_DIR scrub above is all this
	// assertion needs — there is no package-init value left to stub.
	// TestRecoverNoGit_RedirectProvenance covers the other side.
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

// TestRecoverNoGit_RootsAtTheRepoContainingBeadsDir is the regression for
// rooting the synthesized context.
//
// The failure that reaches recoverNoGit is the CWD's missing repository, not the
// .beads's. Rooting at filepath.Dir(BeadsDir) conflated the two, so a .beads
// that did live inside a repo reported a SUBDIRECTORY of that repo as repo_root
// and the same workspace answered differently depending only on where the caller
// stood. repoRootForBeadsDir asks git from the .beads side, which needs no CWD
// repo.
func TestRecoverNoGit_RootsAtTheRepoContainingBeadsDir(t *testing.T) {
	repoRoot := resolveSymlinks(t.TempDir())
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}

	// The .beads lives in a SUBDIRECTORY of the repo, which is the case that
	// distinguishes the two rootings.
	beadsDir := filepath.Join(repoRoot, "sub", ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
	if err != nil {
		t.Fatalf("recoverNoGit: %v", err)
	}
	if got := resolveSymlinks(rc.RepoRoot); got != repoRoot {
		t.Errorf("RepoRoot = %q, want %q (the repository containing .beads, not its parent directory %q)",
			got, repoRoot, filepath.Dir(beadsDir))
	}
}

// TestRecoverNoGit_RootsAtBeadsParentWithNoGitAnywhere pins the other half of
// the same change: when git cannot answer from the .beads side either,
// repoRootForBeadsDir falls back to the .beads parent, so the documented
// no-git-anywhere behaviour is preserved exactly.
func TestRecoverNoGit_RootsAtBeadsParentWithNoGitAnywhere(t *testing.T) {
	workspace := resolveSymlinks(t.TempDir())
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
	if err != nil {
		t.Fatalf("recoverNoGit: %v", err)
	}
	if got := resolveSymlinks(rc.RepoRoot); got != workspace {
		t.Errorf("RepoRoot = %q, want the .beads parent %q", got, workspace)
	}
}

// TestRecoverNoGit_RedirectProvenance pins what the synthesized context says
// about identity, which is the question `bd context` exists to answer.
//
// isExternalBeadsDir compares git COMMON DIRS, and the CWD side cannot be
// computed without a repository — which is precisely the state this fallback
// serves. Position is the substitute: the workspace is a redirect iff discovery
// standing in the CWD would not have found it. That covers all four
// caller-directed channels (BEADS_DIR, --db, BEADS_DB/BD_DB, -C) uniformly,
// because it asks where the caller IS rather than which variable was set — and
// bd rewrites BEADS_DIR for itself before resolving, so the environment cannot
// answer this question at all.
func TestRecoverNoGit_RedirectProvenance(t *testing.T) {
	newWorkspace := func(t *testing.T, parent string) string {
		t.Helper()
		beadsDir := filepath.Join(parent, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		// hasBeadsProjectFiles gates discovery, so the workspace needs to look
		// like a real one to be found by the walk.
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return resolveSymlinks(beadsDir)
	}

	// chdirTo moves the process into dir for the duration of the subtest. Only
	// the caller's POSITION decides provenance now, so this is the control.
	chdirTo := func(t *testing.T, dir string) {
		t.Helper()
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
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("discoverable from the CWD is not redirected", func(t *testing.T) {
		t.Setenv("BEADS_DIR", "")
		workspace := resolveSymlinks(t.TempDir())
		beadsDir := newWorkspace(t, workspace)
		chdirTo(t, workspace)

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if rc.IsRedirected {
			t.Error("IsRedirected = true for a .beads discoverable by walking up from the working directory")
		}
	})

	t.Run("bd's own re-exported BEADS_DIR does not fake a redirect", func(t *testing.T) {
		workspace := resolveSymlinks(t.TempDir())
		beadsDir := newWorkspace(t, workspace)
		chdirTo(t, workspace)
		// bd exports a BEADS_DIR for itself before resolving (context_cmd.go
		// calls prepareSelectedNoDBContext immediately beforehand), so a
		// walk-found workspace always has one in the environment by the time
		// this runs. Position must be indifferent to it; an environment
		// inventory would report a redirect here.
		t.Setenv("BEADS_DIR", beadsDir)

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if rc.IsRedirected {
			t.Error("IsRedirected = true for a walk-discoverable workspace that bd merely re-exported")
		}
	})

	t.Run("named from a directory that cannot reach it is redirected", func(t *testing.T) {
		t.Setenv("BEADS_DIR", "")
		beadsDir := newWorkspace(t, resolveSymlinks(t.TempDir()))
		// Stand somewhere with no .beads on its ancestor chain. This is the
		// shape every caller-directed channel produces: --db, BEADS_DB/BD_DB
		// and -C all resolve a store the CWD could never have discovered.
		chdirTo(t, resolveSymlinks(t.TempDir()))

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if !rc.IsRedirected {
			t.Error("IsRedirected = false for a workspace the working directory cannot discover")
		}
		if role, ok := rc.Role(); !ok || role != Contributor {
			t.Errorf("Role() = (%q, %v), want (%q, true) — a redirect implies contributor", role, ok, Contributor)
		}
	})

	t.Run("a sibling of the CWD is redirected", func(t *testing.T) {
		t.Setenv("BEADS_DIR", "")
		// The boundary the review left unspecified, settled deliberately: a
		// store reachable only by being named is a redirect even when it sits
		// beside the caller. With no repository for the CWD there is no
		// enclosing scope that could make a sibling local.
		parent := resolveSymlinks(t.TempDir())
		here := filepath.Join(parent, "here")
		sibling := filepath.Join(parent, "sibling")
		if err := os.MkdirAll(here, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(sibling, 0o750); err != nil {
			t.Fatal(err)
		}
		beadsDir := newWorkspace(t, sibling)
		chdirTo(t, here)

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: beadsDir, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if !rc.IsRedirected {
			t.Error("IsRedirected = false for a .beads in a sibling directory of the working directory")
		}
	})

	t.Run("a different discoverable workspace is still a redirect", func(t *testing.T) {
		t.Setenv("BEADS_DIR", "")
		// The CWD can discover a workspace, but not THIS one.
		local := resolveSymlinks(t.TempDir())
		newWorkspace(t, local)
		named := newWorkspace(t, resolveSymlinks(t.TempDir()))
		chdirTo(t, local)

		rc, err := recoverNoGit(nil, &NoRepoRootError{BeadsDir: named, Err: errors.New("not a git repository")})
		if err != nil {
			t.Fatalf("recoverNoGit: %v", err)
		}
		if !rc.IsRedirected {
			t.Error("IsRedirected = false, but the resolved workspace is not the one the CWD discovers")
		}
	})
}
