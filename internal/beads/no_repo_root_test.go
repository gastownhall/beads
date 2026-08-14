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

	// The current one: type-based, so the unsafe-location error is never
	// mistaken for a recoverable missing-git-root.
	var noRoot *NoRepoRootError
	if errors.As(unsafeErr, &noRoot) {
		t.Error("unsafe-location error must never select the no-git fallback")
	}

	realErr := &NoRepoRootError{BeadsDir: "/tmp/ws/.beads", Err: errors.New("not a git repository")}
	if !errors.As(realErr, &noRoot) {
		t.Error("NoRepoRootError must select the no-git fallback")
	}
	if noRoot.BeadsDir != "/tmp/ws/.beads" {
		t.Errorf("BeadsDir = %q, want the boundary-checked dir carried by the error", noRoot.BeadsDir)
	}
	if !strings.Contains(realErr.Error(), probe) {
		t.Errorf("error message changed: %q — the wording is user-facing", realErr.Error())
	}
	if !errors.Is(realErr, realErr.Err) {
		t.Error("NoRepoRootError must unwrap to the underlying git failure")
	}
}
