package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
)

// TestGetRepoContextForWorkspace_NormalRepo tests context resolution for a normal git repository
func TestGetRepoContextForWorkspace_NormalRepo(t *testing.T) {
	// Create a temporary git repo
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .beads directory with required files
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	// Create a database file (required for hasBeadsProjectFiles)
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Reset caches before test
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get context for the workspace
	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Verify context fields
	if rc.RepoRoot != resolveSymlinks(tmpDir) {
		t.Errorf("RepoRoot mismatch: expected %s, got %s", resolveSymlinks(tmpDir), rc.RepoRoot)
	}
	if rc.BeadsDir != resolveSymlinks(beadsDir) {
		t.Errorf("BeadsDir mismatch: expected %s, got %s", resolveSymlinks(beadsDir), rc.BeadsDir)
	}
	if rc.IsRedirected {
		t.Error("IsRedirected should be false for workspace-specific context")
	}
	if rc.IsWorktree {
		t.Error("IsWorktree should be false for main repo")
	}
}

// TestGetRepoContextForWorkspace_IgnoresBEADS_DIR verifies that workspace-specific
// context resolution ignores the BEADS_DIR environment variable (DMN-001)
func TestGetRepoContextForWorkspace_IgnoresBEADS_DIR(t *testing.T) {
	// Save original env var
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create two separate repos: repo1 and repo2
	tmpDir := t.TempDir()
	repo1 := filepath.Join(tmpDir, "repo1")
	repo2 := filepath.Join(tmpDir, "repo2")

	for _, repo := range []string{repo1, repo2} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo in %s: %v", repo, err)
		}
		beadsDir := filepath.Join(repo, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("failed to create .beads in %s: %v", repo, err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
			t.Fatalf("failed to create beads.db: %v", err)
		}
	}

	// Set BEADS_DIR to repo2's .beads
	os.Setenv("BEADS_DIR", filepath.Join(repo2, ".beads"))

	// Get context for repo1 - should find repo1's .beads, NOT repo2's
	rc, err := GetRepoContextForWorkspace(repo1)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Verify we got repo1, not repo2
	expectedBeadsDir := resolveSymlinks(filepath.Join(repo1, ".beads"))
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BEADS_DIR was not ignored: expected %s, got %s", expectedBeadsDir, rc.BeadsDir)
	}
	expectedRepoRoot := resolveSymlinks(repo1)
	if rc.RepoRoot != expectedRepoRoot {
		t.Errorf("RepoRoot mismatch: expected %s, got %s", expectedRepoRoot, rc.RepoRoot)
	}
}

// TestGetRepoContextForWorkspace_NonexistentPath tests handling of invalid workspace paths
func TestGetRepoContextForWorkspace_NonexistentPath(t *testing.T) {
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	_, err := GetRepoContextForWorkspace("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for nonexistent workspace path")
	}
}

// TestGetRepoContextForWorkspace_NonGitDirectory tests handling of non-git directories
func TestGetRepoContextForWorkspace_NonGitDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Don't initialize git - just a plain directory
	_, err := GetRepoContextForWorkspace(tmpDir)
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

// TestGetRepoContextForWorkspace_MissingBeadsDir tests error when .beads doesn't exist
func TestGetRepoContextForWorkspace_MissingBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// No .beads directory created
	_, err := GetRepoContextForWorkspace(tmpDir)
	if err == nil {
		t.Error("expected error when .beads directory is missing")
	}
}

// TestRepoContext_Validate tests the Validate method for detecting stale contexts
func TestRepoContext_Validate(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get initial context
	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Validate should pass initially
	if err := rc.Validate(); err != nil {
		t.Errorf("Validate should pass for fresh context: %v", err)
	}

	// Remove the .beads directory to make context stale
	if err := os.RemoveAll(beadsDir); err != nil {
		t.Fatalf("failed to remove .beads: %v", err)
	}

	// Validate should now fail (stale context)
	if err := rc.Validate(); err == nil {
		t.Error("Validate should fail when BeadsDir no longer exists")
	}
}

// TestRepoContext_Validate_RepoRootRemoved tests Validate when repo root is removed
func TestRepoContext_Validate_RepoRootRemoved(t *testing.T) {
	// Create repo inside a removable parent
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "removable-repo")
	if err := os.MkdirAll(repoDir, 0750); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	if err := initGitRepo(repoDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get context
	rc, err := GetRepoContextForWorkspace(repoDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Validate should pass
	if err := rc.Validate(); err != nil {
		t.Errorf("Validate should pass for fresh context: %v", err)
	}

	// Remove the entire repo
	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatalf("failed to remove repo: %v", err)
	}

	// Validate should now fail (both BeadsDir and RepoRoot are gone)
	if err := rc.Validate(); err == nil {
		t.Error("Validate should fail when RepoRoot no longer exists")
	}
}

// TestGetRepoContextForWorkspace_CacheReset verifies that multiple calls return fresh contexts
func TestGetRepoContextForWorkspace_CacheReset(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// First call
	rc1, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("first GetRepoContextForWorkspace failed: %v", err)
	}

	// Second call - should still work (fresh resolution)
	rc2, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("second GetRepoContextForWorkspace failed: %v", err)
	}

	// Both should return valid contexts
	if rc1.BeadsDir != rc2.BeadsDir {
		t.Errorf("BeadsDir mismatch between calls: %s vs %s", rc1.BeadsDir, rc2.BeadsDir)
	}
}

// TestGetRepoContextForWorkspace_RelativePath tests handling of relative workspace paths
func TestGetRepoContextForWorkspace_RelativePath(t *testing.T) {
	// Get original working directory
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(originalWd)
		ResetCaches()
		git.ResetCaches()
	})

	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Change to parent directory
	parentDir := filepath.Dir(tmpDir)
	os.Chdir(parentDir)

	// Use relative path
	relPath := filepath.Base(tmpDir)
	rc, err := GetRepoContextForWorkspace(relPath)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace with relative path failed: %v", err)
	}

	// Verify we got the correct absolute path
	expectedBeadsDir := resolveSymlinks(beadsDir)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir mismatch: expected %s, got %s", expectedBeadsDir, rc.BeadsDir)
	}
}

// initGitRepo initializes a git repository in the given directory
func initGitRepo(dir string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	// Suppress git output
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// TestIsPathInSafeBoundary tests security boundary validation (TS-SEC-003)
// This ensures redirect paths cannot escape to sensitive system directories.
func TestIsPathInSafeBoundary(t *testing.T) {
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get user home directory for test comparisons
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home directory: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		// Unsafe system directories - should be rejected
		{"system /etc", "/etc/beads", false},
		{"system /usr", "/usr/local/beads", false},
		{"system /var", "/var/lib/beads", false},
		{"system /root", "/root/.beads", false},
		{"system /bin", "/bin/.beads", false},
		{"system /sbin", "/sbin/.beads", false},
		{"system /opt", "/opt/beads", false},
		{"macOS /System", "/System/Library/.beads", false},
		{"macOS /Library", "/Library/Application Support/.beads", false},
		{"macOS /private", "/private/etc/.beads", false},

		// Safe paths - should be accepted
		{"user home directory", filepath.Join(homeDir, "projects/.beads"), true},
		{"temp directory", os.TempDir(), true},
		// A not-yet-created BEADS_DIR under the temp dir (the common real case --
		// the directory is created after this check passes) must still validate;
		// resolveLongestExistingAncestor is what makes this safe despite the
		// trailing components not existing yet (be-kghzr SEC-003 hardening).
		{"temp directory not-yet-created subpath", filepath.Join(os.TempDir(), "be-kghzr-nonexistent-subpath", ".beads"), true},

		// Another user's home directory - should be rejected regardless of $HOME
		{"other user home /home", "/home/some-other-nonexistent-user/.beads", false},
		{"other user home /Users", "/Users/some-other-nonexistent-user/.beads", false},

		// macOS /Users/Shared is the OS-designated shared directory, not a peer
		// user's home — it must be accepted (be-vc1 / SEC-003 carve-out).
		{"macOS shared subdir", "/Users/Shared/portharbour/.beads", true},
		{"macOS shared root", "/Users/Shared", true},

		// /var/tmp is the FHS-standard secondary temp directory (persists across
		// reboots, unlike /tmp) and must be accepted despite matching the /var
		// unsafePrefixes entry above. Go's own test/build tooling can root
		// GOTMPDIR-influenced temp dirs here even when os.TempDir() itself still
		// reports /tmp, so the os.TempDir()-based carve-out above doesn't cover it
		// (be-odye4).
		{"var/tmp root", "/var/tmp", true},
		{"var/tmp GOTMPDIR-style subdir", "/var/tmp/gotmp/TestSomething1234/001/.beads", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPathInSafeBoundary(tt.path)
			if result != tt.expected {
				t.Errorf("isPathInSafeBoundary(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// TestIsPathInSafeBoundary_OwnServiceHome drives the real boundary —
// isPathInSafeBoundary, unsafePrefixes blocklist and all — against a
// temporary stand-in for the /var/lib service-home carve-out (GH#6323).
//
// The fixture base is appended to a saved copy of unsafePrefixes so the whole
// fixture area is blocklisted exactly the way /var is in production: nothing
// under it can be admitted except through the service-home exception. TMPDIR
// is repointed to a disjoint sibling first, so the os.TempDir() carve-out
// (which admits everything under the temp root) cannot cover the fixture and
// hand the test passes the exception did not earn. The account seams
// (serviceHomeRoot, serviceHomeAccount) stand in for the OS account database,
// which a test cannot repoint at a fixture home.
func TestIsPathInSafeBoundary_OwnServiceHome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the service-home exception never admits uid 0, so a root runner cannot exercise admission through the real boundary")
	}
	fixtureBase := t.TempDir()
	// Disjoint temp root for os.TempDir() once TMPDIR moves below.
	t.Setenv("TMPDIR", t.TempDir())

	serviceRoot := filepath.Join(fixtureBase, "varlib")
	home := filepath.Join(serviceRoot, "flow-estate")
	releaseDir := filepath.Join(home, "releases", "r1")
	for _, dir := range []string{
		serviceRoot,
		home,
		releaseDir,
		filepath.Join(serviceRoot, "some-other-service"),
		filepath.Join(fixtureBase, "outside"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// A release entry that is a symlink escaping the home (but staying inside
	// the blocklisted fixture area): the exception must resolve it and
	// refuse, not follow it out of the account home.
	if err := os.Symlink(filepath.Join(fixtureBase, "outside"), filepath.Join(home, "releases", "r3")); err != nil {
		t.Fatalf("symlink escaping release: %v", err)
	}

	savedPrefixes := unsafePrefixes
	savedRoot, savedAccount := serviceHomeRoot, serviceHomeAccount
	unsafePrefixes = append(append([]string{}, savedPrefixes...), fixtureBase)
	t.Cleanup(func() {
		unsafePrefixes = savedPrefixes
		serviceHomeRoot = savedRoot
		serviceHomeAccount = savedAccount
	})
	serviceHomeRoot = serviceRoot
	accountHome := func() string { return home }

	tests := []struct {
		name     string
		account  func() string
		path     string
		expected bool
	}{
		{"account home itself", accountHome, home, true},
		{"existing release .beads", accountHome, filepath.Join(releaseDir, ".beads"), true},
		{"not-yet-created release .beads", accountHome, filepath.Join(home, "releases", "r2", ".beads"), true},

		{"service root itself is never a home", accountHome, serviceRoot, false},
		{"sibling service home", accountHome, filepath.Join(serviceRoot, "some-other-service", ".beads"), false},
		{"name-prefix sibling of the home", accountHome, filepath.Join(serviceRoot, "flow-estate-mirror", ".beads"), false},
		{"release symlink escaping the home", accountHome, filepath.Join(home, "releases", "r3", ".beads"), false},
		{"unrelated path in the blocked area", accountHome, filepath.Join(fixtureBase, "elsewhere", ".beads"), false},

		{"no account home configured", func() string { return "" }, filepath.Join(home, ".beads"), false},
		{"account home missing from disk", func() string { return filepath.Join(serviceRoot, "no-such-service") }, filepath.Join(serviceRoot, "no-such-service", ".beads"), false},
		{"account home is the service root itself", func() string { return serviceRoot }, filepath.Join(home, ".beads"), false},
		{"account home outside the service root", func() string { return fixtureBase }, filepath.Join(home, ".beads"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceHomeAccount = tt.account
			if got := isPathInSafeBoundary(tt.path); got != tt.expected {
				t.Errorf("isPathInSafeBoundary(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

// TestInOwnServiceHome table-tests the service-home helper directly, with the
// root argument pointed at a temporary fixture (the real /var/lib belongs to
// the system). Ownership is exercised with the runner's own uid; on a root
// runner the fixture home is chowned to nobody's uid (65534) and that uid is
// supplied instead, so positive rows stay meaningful there too.
func TestInOwnServiceHome(t *testing.T) {
	euid := os.Geteuid()
	if euid == 0 {
		euid = 65534 // nobody: see the comment above
	}

	base := t.TempDir()
	root := filepath.Join(base, "varlib")
	home := filepath.Join(root, "svc")
	for _, dir := range []string{root, home, filepath.Join(root, "releases", "r1"), filepath.Join(root, "othersvc")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write afile: %v", err)
	}
	outsideRoot := t.TempDir() // a second temp dir, genuinely outside root
	// Aliases and escape hatches, all with existing targets so
	// resolveLongestExistingAncestor follows them rather than falling back to
	// the lexical spelling.
	aliasToHome := filepath.Join(root, "alias")       // valid home alias -> home
	aliasToRoot := filepath.Join(root, "aliasroot")   // home alias resolving to the root itself
	escapeToSibling := filepath.Join(home, "escape1") // out of the home, still under root
	escapeOut := filepath.Join(home, "escape2")       // out of the root entirely
	for link, target := range map[string]string{
		aliasToHome:     home,
		aliasToRoot:     root,
		escapeToSibling: filepath.Join(root, "othersvc"),
		escapeOut:       outsideRoot,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink %s -> %s: %v", link, target, err)
		}
	}
	// A symlinked root spelling, as macOS's /var -> /private/var shape would
	// produce: requests may arrive through the link or through the physical
	// target, and both must work.
	rootPhys := filepath.Join(base, "varlibphys")
	if err := os.MkdirAll(filepath.Join(rootPhys, "svc"), 0o755); err != nil {
		t.Fatalf("mkdir rootPhys/svc: %v", err)
	}
	rootLink := filepath.Join(base, "varliblink")
	if err := os.Symlink(rootPhys, rootLink); err != nil {
		t.Fatalf("symlink rootLink: %v", err)
	}
	if euid == 65534 {
		// Root runner: make the fixture homes owned by the injected uid so
		// the ownership gate can pass for them.
		for _, dir := range []string{home, filepath.Join(rootPhys, "svc")} {
			if err := os.Chown(dir, 65534, -1); err != nil {
				t.Skipf("cannot chown fixture home to 65534 on this runner: %v", err)
			}
		}
	}

	accountHome := func() string { return home }
	tests := []struct {
		name string
		path string
		root string
		home func() string
		euid int
		want bool
	}{
		{"home itself", home, root, accountHome, euid, true},
		{"existing descendant", filepath.Join(home, "releases", "r1", ".beads"), root, accountHome, euid, true},
		{"missing descendant", filepath.Join(home, "notyet", ".beads"), root, accountHome, euid, true},

		{"request via a home alias", filepath.Join(aliasToHome, ".beads"), root, accountHome, euid, true},
		{"home nominated through an alias", filepath.Join(home, ".beads"), root, func() string { return aliasToHome }, euid, true},
		{"symlinked root, request via the link", filepath.Join(rootLink, "svc", ".beads"), rootLink, func() string { return filepath.Join(rootLink, "svc") }, euid, true},
		{"symlinked root, request in physical form", filepath.Join(rootPhys, "svc", ".beads"), rootLink, func() string { return filepath.Join(rootLink, "svc") }, euid, true},

		{"the service root itself", root, root, accountHome, euid, false},
		{"sibling service home", filepath.Join(root, "othersvc", ".beads"), root, accountHome, euid, false},
		{"name-prefix sibling of the home", filepath.Join(root, "svc2", ".beads"), root, accountHome, euid, false},
		{"request outside the service root", filepath.Join(outsideRoot, ".beads"), root, accountHome, euid, false},
		{"symlink escaping the home to a sibling", filepath.Join(escapeToSibling, ".beads"), root, accountHome, euid, false},
		{"symlink escaping the service root", filepath.Join(escapeOut, ".beads"), root, accountHome, euid, false},
		{"home alias resolving to the root itself", filepath.Join(home, ".beads"), root, func() string { return aliasToRoot }, euid, false},

		{"uid 0 is never admitted", filepath.Join(home, ".beads"), root, accountHome, 0, false},
		{"negative uid", filepath.Join(home, ".beads"), root, accountHome, -1, false},
		{"uid that does not own the home", filepath.Join(home, ".beads"), root, accountHome, euid + 1, false},
		{"empty home", filepath.Join(home, ".beads"), root, func() string { return "" }, euid, false},
		{"relative home", filepath.Join(home, ".beads"), root, func() string { return "svc" }, euid, false},
		{"missing home", filepath.Join(home, ".beads"), root, func() string { return filepath.Join(root, "nothere") }, euid, false},
		{"non-directory home", filepath.Join(home, ".beads"), root, func() string { return filepath.Join(root, "afile") }, euid, false},
		{"home equal to the service root", filepath.Join(home, ".beads"), root, func() string { return root }, euid, false},
		{"home outside the service root", filepath.Join(home, ".beads"), root, func() string { return base }, euid, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inOwnServiceHome(tt.path, tt.root, tt.home(), tt.euid); got != tt.want {
				t.Errorf("inOwnServiceHome(%q, root=%q, home=%q, euid=%d) = %v, want %v", tt.path, tt.root, tt.home(), tt.euid, got, tt.want)
			}
		})
	}
}

// TestIsPathInSafeBoundary_ServiceHomeLookupScoped proves the account lookup
// behind the service-home exception is consulted only for candidate paths —
// those under the lexical /var/lib root or its physical spelling. Every other
// verdict must not touch the OS account database: the carve-out must not add
// an account lookup (with its failure modes and its cost) to unrelated
// paths, whose outcomes stay exactly as they were without the exception.
func TestIsPathInSafeBoundary_ServiceHomeLookupScoped(t *testing.T) {
	lookups := 0
	savedAccount := serviceHomeAccount
	serviceHomeAccount = func() string {
		lookups++
		return ""
	}
	t.Cleanup(func() { serviceHomeAccount = savedAccount })
	// serviceHomeRoot keeps its production value /var/lib: candidate
	// detection is a pure prefix comparison against it (plus its physical
	// spelling), so no fixture is needed and none may be created under the
	// real root.

	// Outcomes for these paths are owned by TestIsPathInSafeBoundary; here
	// only the lookup count matters.
	nonCandidates := []struct {
		name string
		path string
	}{
		{"blocked /etc path", "/etc/beads"},
		{"blocked /usr path", "/usr/local/beads"},
		{"blocked /var path outside the service root", "/var/log/beads/.beads"},
		{"prefix sibling /var/libx is not /var/lib", "/var/libx/beads/.beads"},
		{"peer home under /home", "/home/some-other-nonexistent-user/.beads"},
		{"ordinary temp path", filepath.Join(t.TempDir(), ".beads")},
	}
	for _, tt := range nonCandidates {
		t.Run(tt.name, func(t *testing.T) {
			before := lookups
			isPathInSafeBoundary(tt.path)
			if got := lookups - before; got != 0 {
				t.Errorf("isPathInSafeBoundary(%q) consulted the service-home account lookup %d time(s), want 0", tt.path, got)
			}
		})
	}
	t.Run("candidate /var/lib path consults the lookup", func(t *testing.T) {
		before := lookups
		isPathInSafeBoundary("/var/lib/some-service/.beads")
		if lookups == before {
			t.Error("isPathInSafeBoundary(/var/lib/some-service/.beads) never consulted the service-home account lookup")
		}
	})
}

// TestResolvedPathWithinRoot exercises the symlink-escape hardening helper added
// for be-vc1 — the HIGH finding on the /Users/Shared carve-out. The helper must:
//
//	(a) accept a real subdirectory under root,
//	(b) REJECT a symlink under root whose target resolves outside root (the
//	    TOCTOU/path-traversal vector on the world-writable /Users/Shared),
//	(c) accept a not-yet-created subpath under a real root (a BEADS_DIR that has
//	    not been created yet must still validate, not fail closed).
//
// It uses a temp-dir stand-in for root so it runs on every OS — Linux CI thereby
// proves the escape rejection (the literal /Users/Shared symlink case is darwin-
// only and lives in TestIsPathInSafeBoundary).
func TestResolvedPathWithinRoot(t *testing.T) {
	root := t.TempDir()

	// (a) a real subdirectory under root stays within root.
	realSub := filepath.Join(root, "real")
	if err := os.MkdirAll(realSub, 0o755); err != nil {
		t.Fatalf("mkdir realSub: %v", err)
	}
	if !resolvedPathWithinRoot(realSub, root) {
		t.Errorf("resolvedPathWithinRoot(%q, %q) = false, want true (real subdir under root)", realSub, root)
	}

	// (b) a symlink under root whose target is outside root must be rejected:
	// resolving the symlink lands outside the boundary. This is the vector the
	// security review flagged — /Users/Shared is world-writable, so a co-located
	// user can plant such a link.
	outside := t.TempDir() // a distinct temp dir, genuinely outside root
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	if resolvedPathWithinRoot(escape, root) {
		t.Errorf("resolvedPathWithinRoot(%q -> %q, %q) = true, want false (symlink escapes root)", escape, outside, root)
	}

	// (c) a not-yet-created subpath under a real root still validates: the helper
	// resolves the longest existing ancestor and re-appends the missing tail.
	notYet := filepath.Join(root, "notyet", ".beads")
	if !resolvedPathWithinRoot(notYet, root) {
		t.Errorf("resolvedPathWithinRoot(%q, %q) = false, want true (not-yet-created subpath under real root)", notYet, root)
	}
}

// TestIsPathInSafeBoundary_SharedSymlinkEscape proves, on macOS where /Users/Shared
// actually exists and is world-writable, that a symlink planted under it whose
// target escapes the boundary is REJECTED through isPathInSafeBoundary — the live
// form of the be-vc1 HIGH finding. Skipped on non-darwin and when /Users/Shared is
// absent or not writable, so it never fails spuriously on CI runners.
func TestIsPathInSafeBoundary_SharedSymlinkEscape(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("/Users/Shared is a macOS-specific shared directory")
	}
	const shared = "/Users/Shared"
	if info, err := os.Stat(shared); err != nil || !info.IsDir() {
		t.Skipf("%s not present as a directory: %v", shared, err)
	}

	// Plant a symlink under the world-writable /Users/Shared pointing OUTSIDE the
	// boundary, at /etc (a system dir). Best-effort clear of any stale link from a
	// crashed run, then register cleanup.
	link := filepath.Join(shared, fmt.Sprintf(".be-vc1-escape-test-%d", os.Getpid()))
	_ = os.Remove(link)
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("cannot create symlink in %s (not writable?): %v", shared, err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	// A BEADS_DIR routed *through* the escaping symlink must be rejected: its bytes
	// resolve into /etc, outside /Users/Shared.
	target := filepath.Join(link, ".beads")
	if isPathInSafeBoundary(target) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (path through symlink escaping /Users/Shared to /etc)", target)
	}

	// The escaping symlink itself also resolves outside the boundary.
	if isPathInSafeBoundary(link) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (symlink under /Users/Shared escaping to /etc)", link)
	}
}

// TestIsPathInSafeBoundary_VarTmpSymlinkEscape proves the /var/tmp carve-out
// (be-odye4) applies the same symlink-safe resolution as /Users/Shared: /var/tmp
// is world-writable (drwxrwxrwt) on Linux/BSD, so a co-located user could plant a
// symlink under it whose target escapes to a rejected system directory. Skipped
// when /var/tmp is absent or not writable, so it never fails spuriously.
func TestIsPathInSafeBoundary_VarTmpSymlinkEscape(t *testing.T) {
	const varTmp = "/var/tmp"
	if info, err := os.Stat(varTmp); err != nil || !info.IsDir() {
		t.Skipf("%s not present as a directory: %v", varTmp, err)
	}

	link := filepath.Join(varTmp, fmt.Sprintf(".be-odye4-escape-test-%d", os.Getpid()))
	_ = os.Remove(link)
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("cannot create symlink in %s (not writable?): %v", varTmp, err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	// A BEADS_DIR routed *through* the escaping symlink must be rejected: its bytes
	// resolve into /etc, outside /var/tmp.
	target := filepath.Join(link, ".beads")
	if isPathInSafeBoundary(target) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (path through symlink escaping /var/tmp to /etc)", target)
	}

	// The escaping symlink itself also resolves outside the boundary.
	if isPathInSafeBoundary(link) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (symlink under /var/tmp escaping to /etc)", link)
	}
}

// TestIsPathInSafeBoundary_TempDirSymlinkEscape mirrors
// TestIsPathInSafeBoundary_SharedSymlinkEscape for the os.TempDir() carve-out
// (be-kghzr SEC-003 hardening). Unlike /Users/Shared, os.TempDir() is
// cross-platform and always present, so this test is not OS-gated.
func TestIsPathInSafeBoundary_TempDirSymlinkEscape(t *testing.T) {
	tempDir := os.TempDir()

	// Plant a symlink under the world-writable OS temp dir pointing OUTSIDE the
	// boundary, at /etc (a system dir). Best-effort clear of any stale link from a
	// crashed run, then register cleanup.
	link := filepath.Join(tempDir, fmt.Sprintf(".be-kghzr-escape-test-%d", os.Getpid()))
	_ = os.Remove(link)
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("cannot create symlink in %s (not writable?): %v", tempDir, err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	// A BEADS_DIR routed *through* the escaping symlink must be rejected: its bytes
	// resolve into /etc, outside the temp dir. The trailing ".beads" component does
	// NOT exist, so a bare filepath.EvalSymlinks(absPath) fails on the full path --
	// this is the exact shape that tripped the old unresolved-path fallback.
	target := filepath.Join(link, ".beads")
	if isPathInSafeBoundary(target) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (path through symlink escaping temp dir to /etc)", target)
	}

	// The escaping symlink itself also resolves outside the boundary.
	if isPathInSafeBoundary(link) {
		t.Errorf("isPathInSafeBoundary(%q) = true, want false (symlink under temp dir escaping to /etc)", link)
	}
}

// TestIsPathInSafeBoundary_TempDirPhysicalForm covers the macOS shape where
// $TMPDIR is itself a symlink (/var/folders/... -> /private/var/...): a
// caller-supplied path that has already been symlink-resolved arrives in the
// PHYSICAL form, does not share the unresolved os.TempDir() prefix, and must
// still be admitted by the temp-dir carve-out. On macOS the physical form
// falls under the denied /private prefix, so without the carve-out this test
// fails (the exact TestContext* regression from the be-kghzr hardening); on
// platforms whose physical temp root is not under a denied prefix the
// assertion is satisfied either way — the macos-latest CI lane is the
// distinguishing runner.
func TestIsPathInSafeBoundary_TempDirPhysicalForm(t *testing.T) {
	base := t.TempDir()
	phys := filepath.Join(base, "phys")
	if err := os.MkdirAll(phys, 0o755); err != nil {
		t.Fatalf("mkdir phys: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(phys, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Setenv("TMPDIR", link)

	// Physical form of a (not-yet-created) subpath of the temp dir: must be
	// safe. On macOS the join base itself sits under the symlinked
	// /var/folders, so resolve it to the true physical spelling first — the
	// probe must match what a caller supplies after EvalSymlinks, which is
	// exactly resolveLongestExistingAncestor(TMPDIR)'s form.
	physResolved, err := filepath.EvalSymlinks(phys)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", phys, err)
	}
	target := filepath.Join(physResolved, "proj", ".beads")
	if !isPathInSafeBoundary(target) {
		t.Errorf("isPathInSafeBoundary(%q) = false, want true (physical form of TMPDIR subpath)", target)
	}

	// The symlinked form keeps working too.
	linked := filepath.Join(link, "proj", ".beads")
	if !isPathInSafeBoundary(linked) {
		t.Errorf("isPathInSafeBoundary(%q) = false, want true (symlinked form of TMPDIR subpath)", linked)
	}
}

// TestGetRepoContextForWorkspace_RedirectToUnsafeLocation tests that redirects
// to unsafe locations are rejected (TS-SEC-003 integration test).
func TestGetRepoContextForWorkspace_RedirectToUnsafeLocation(t *testing.T) {
	// Save original env
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create a temporary git repo
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .beads directory with a redirect file pointing outside safe boundary
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write redirect pointing to /etc (unsafe location)
	// Note: FollowRedirect won't follow non-existent paths, but if /etc/.beads existed
	// and contained beads files, this security check would catch it
	redirectFile := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectFile, []byte("/etc/.beads\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// Since /etc/.beads doesn't exist, FollowRedirect returns original path
	// So create a valid beads.db in the local .beads to get past initial validation,
	// then test the boundary check directly via the isPathInSafeBoundary function
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// GetRepoContextForWorkspace should succeed because FollowRedirect
	// returns the original safe path when target doesn't exist
	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		// This is expected if the implementation catches the redirect attempt
		// Even though target doesn't exist, the test verifies the security boundary
		t.Logf("GetRepoContextForWorkspace correctly rejected unsafe redirect: %v", err)
		return
	}

	// If we get here, the context was created with the safe local path
	// (because /etc/.beads doesn't exist and FollowRedirect fell back)
	// Verify it's using the local beads dir, not the unsafe redirect target
	expectedBeadsDir := resolveSymlinks(beadsDir)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir = %q, want safe local path %q", rc.BeadsDir, expectedBeadsDir)
	}
}

// TestGetRepoContextForWorkspace_RedirectWithinRepo tests that redirects
// staying within the same repo or user's directories are allowed.
func TestGetRepoContextForWorkspace_RedirectWithinRepo(t *testing.T) {
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create two git repos in temp directory
	tmpDir := t.TempDir()
	repo1 := filepath.Join(tmpDir, "repo1")
	repo2 := filepath.Join(tmpDir, "repo2")

	for _, repo := range []string{repo1, repo2} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo in %s: %v", repo, err)
		}
	}

	// Create .beads with actual files in repo2
	beadsDir2 := filepath.Join(repo2, ".beads")
	if err := os.MkdirAll(beadsDir2, 0750); err != nil {
		t.Fatalf("failed to create .beads in repo2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir2, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db in repo2: %v", err)
	}

	// Create .beads in repo1 with redirect to repo2
	beadsDir1 := filepath.Join(repo1, ".beads")
	if err := os.MkdirAll(beadsDir1, 0750); err != nil {
		t.Fatalf("failed to create .beads in repo1: %v", err)
	}
	redirectFile := filepath.Join(beadsDir1, "redirect")
	if err := os.WriteFile(redirectFile, []byte(beadsDir2+"\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// GetRepoContextForWorkspace for repo1 should work
	// Note: GetRepoContextForWorkspace ignores BEADS_DIR and looks for .beads in workspace
	// But it does follow the redirect file in the local .beads
	rc, err := GetRepoContextForWorkspace(repo1)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed for safe redirect: %v", err)
	}

	// Verify the redirect was followed to repo2's .beads
	expectedBeadsDir := resolveSymlinks(beadsDir2)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir = %q, want redirected path %q", rc.BeadsDir, expectedBeadsDir)
	}
}

// resolveSymlinks resolves symlinks and returns the canonical path
// This handles macOS temp directory symlinks (/var -> /private/var)
func resolveSymlinks(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// TestRole_ExplicitConfig tests Role() when beads.role is set in git config.
func TestRole_ExplicitConfig(t *testing.T) {
	tests := map[string]struct {
		configValue  string
		expectedRole UserRole
	}{
		"contributor role": {configValue: "contributor", expectedRole: Contributor},
		"maintainer role":  {configValue: "maintainer", expectedRole: Maintainer},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := initGitRepo(tmpDir); err != nil {
				t.Fatalf("failed to init git repo: %v", err)
			}

			// Create .beads directory with required files
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0750); err != nil {
				t.Fatalf("failed to create .beads: %v", err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
				t.Fatalf("failed to create beads.db: %v", err)
			}

			// Set beads.role in git config
			cmd := exec.Command("git", "config", "beads.role", tt.configValue)
			cmd.Dir = tmpDir
			if err := cmd.Run(); err != nil {
				t.Fatalf("failed to set git config: %v", err)
			}

			t.Cleanup(func() {
				ResetCaches()
				git.ResetCaches()
			})

			rc, err := GetRepoContextForWorkspace(tmpDir)
			if err != nil {
				t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
			}

			role, ok := rc.Role()
			if !ok {
				t.Error("Role() returned ok=false, expected ok=true")
			}
			if role != tt.expectedRole {
				t.Errorf("Role() = %q, want %q", role, tt.expectedRole)
			}
		})
	}
}

// TestRole_NoConfig tests Role() when beads.role is not set.
func TestRole_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .beads directory with required files (no git config set)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	role, ok := rc.Role()
	if ok {
		t.Errorf("Role() returned ok=true with role=%q, expected ok=false", role)
	}
	if role != "" {
		t.Errorf("Role() = %q, want empty string", role)
	}
}

func TestGetRepoContext_BEADS_DIR_ExternalRepo(t *testing.T) {
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	tmpDir := t.TempDir()
	sourceRepo := filepath.Join(tmpDir, "source")
	targetRepo := filepath.Join(tmpDir, "target")

	for _, repo := range []string{sourceRepo, targetRepo} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo: %v", err)
		}
	}

	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads in target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	os.Setenv("BEADS_DIR", targetBeadsDir)

	originalWd, _ := os.Getwd()
	if err := os.Chdir(sourceRepo); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(originalWd)
	})

	ResetCaches()
	git.ResetCaches()

	rc, err := GetRepoContext()
	if err != nil {
		t.Fatalf("GetRepoContext failed: %v", err)
	}

	expectedBeadsDir := resolveSymlinks(targetBeadsDir)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir mismatch: expected %s, got %s", expectedBeadsDir, rc.BeadsDir)
	}

	expectedRepoRoot := resolveSymlinks(targetRepo)
	if rc.RepoRoot != expectedRepoRoot {
		t.Errorf("RepoRoot mismatch: expected %s, got %s", expectedRepoRoot, rc.RepoRoot)
	}

	if !rc.IsRedirected {
		t.Error("IsRedirected should be true when BEADS_DIR points to a different repo")
	}
}

// TestRole_BEADS_DIR_ImpliesContributor tests that BEADS_DIR redirect
// implicitly returns Contributor role without requiring git config.
func TestRole_BEADS_DIR_ImpliesContributor(t *testing.T) {
	// Save original env var
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create two repos: source repo and target repo for redirect
	tmpDir := t.TempDir()
	sourceRepo := filepath.Join(tmpDir, "source")
	targetRepo := filepath.Join(tmpDir, "target")

	for _, repo := range []string{sourceRepo, targetRepo} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo: %v", err)
		}
	}

	// Create .beads with files in target repo
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads in target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Create .beads in source repo with redirect to target
	sourceBeadsDir := filepath.Join(sourceRepo, ".beads")
	if err := os.MkdirAll(sourceBeadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads in source: %v", err)
	}
	redirectFile := filepath.Join(sourceBeadsDir, "redirect")
	if err := os.WriteFile(redirectFile, []byte(targetBeadsDir+"\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// Set BEADS_DIR to the target (simulating direnv setup)
	os.Setenv("BEADS_DIR", targetBeadsDir)

	// Change to source repo to trigger redirect detection
	originalWd, _ := os.Getwd()
	if err := os.Chdir(sourceRepo); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(originalWd)
	})

	// Get context - should detect redirect via GetRedirectInfo
	rc, err := GetRepoContext()
	if err != nil {
		t.Fatalf("GetRepoContext failed: %v", err)
	}

	// IsRedirected should be true
	if !rc.IsRedirected {
		t.Error("IsRedirected should be true when BEADS_DIR points elsewhere")
	}

	// Role should implicitly return Contributor
	role, ok := rc.Role()
	if !ok {
		t.Error("Role() returned ok=false, expected ok=true for redirected context")
	}
	if role != Contributor {
		t.Errorf("Role() = %q, want %q (implicit contributor for BEADS_DIR)", role, Contributor)
	}

	// IsContributor should return true
	if !rc.IsContributor() {
		t.Error("IsContributor() = false, want true for redirected context")
	}

	// IsMaintainer should return false
	if rc.IsMaintainer() {
		t.Error("IsMaintainer() = true, want false for redirected context")
	}
}

// TestIsContributor tests the IsContributor convenience method.
func TestIsContributor(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set role to contributor
	cmd := exec.Command("git", "config", "beads.role", "contributor")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	if !rc.IsContributor() {
		t.Error("IsContributor() = false, want true")
	}
	if rc.IsMaintainer() {
		t.Error("IsMaintainer() = true, want false")
	}
}

// TestIsMaintainer tests the IsMaintainer convenience method.
func TestIsMaintainer(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set role to maintainer
	cmd := exec.Command("git", "config", "beads.role", "maintainer")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	if rc.IsContributor() {
		t.Error("IsContributor() = true, want false")
	}
	if !rc.IsMaintainer() {
		t.Error("IsMaintainer() = false, want true")
	}
}

// TestRequireRole_Configured tests RequireRole when role is set.
func TestRequireRole_Configured(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set role
	cmd := exec.Command("git", "config", "beads.role", "contributor")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	if err := rc.RequireRole(); err != nil {
		t.Errorf("RequireRole() returned error: %v, want nil", err)
	}
}

// TestRequireRole_NotConfigured tests RequireRole when role is not set.
func TestRequireRole_NotConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Don't set any role config

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	err = rc.RequireRole()
	if err == nil {
		t.Error("RequireRole() returned nil, want ErrRoleNotConfigured")
	}
	if err != ErrRoleNotConfigured {
		t.Errorf("RequireRole() returned %v, want ErrRoleNotConfigured", err)
	}
}

// TestGitOutput tests the GitOutput helper method.
func TestGitOutput(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set a test config value
	cmd := exec.Command("git", "config", "test.value", "hello-world")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Test successful output
	output, err := rc.GitOutput(t.Context(), "config", "--get", "test.value")
	if err != nil {
		t.Errorf("GitOutput() returned error: %v", err)
	}
	expected := "hello-world\n"
	if output != expected {
		t.Errorf("GitOutput() = %q, want %q", output, expected)
	}

	// Test error case (non-existent config)
	_, err = rc.GitOutput(t.Context(), "config", "--get", "nonexistent.key")
	if err == nil {
		t.Error("GitOutput() returned nil error for non-existent key, want error")
	}
}

// TestGitCmd_WorktreeContext tests that GitCmd correctly operates on the main repo
// even when running from a git worktree context (GH#2538).
func TestGitCmd_WorktreeContext(t *testing.T) {

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Create main repo with initial commit (worktrees require at least one commit)
	mainRepo := t.TempDir()
	if err := initGitRepoWithCommit(mainRepo); err != nil {
		t.Fatalf("failed to init main repo: %v", err)
	}

	// Create .beads directory with required files
	beadsDir := filepath.Join(mainRepo, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	testFile := filepath.Join(beadsDir, "test.jsonl")
	if err := os.WriteFile(testFile, []byte(`{"id":"test-1"}`), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Create a git worktree
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	branchCmd := exec.Command("git", "branch", "test-worktree-branch")
	branchCmd.Dir = mainRepo
	if err := branchCmd.Run(); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}
	wtCmd := exec.Command("git", "worktree", "add", worktreeDir, "test-worktree-branch")
	wtCmd.Dir = mainRepo
	output, err := wtCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create worktree: %v\nOutput: %s", err, output)
	}

	// Save and restore working directory
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() { os.Chdir(originalWd) })

	// Change to worktree directory to simulate running from worktree context
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir to worktree: %v", err)
	}
	git.ResetCaches()

	// Get RepoContext - should resolve to main repo's .beads
	rc, err := GetRepoContext()
	if err != nil {
		t.Fatalf("GetRepoContext failed: %v", err)
	}

	expectedBeadsDir := resolveSymlinks(beadsDir)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir = %q, want %q", rc.BeadsDir, expectedBeadsDir)
	}

	// GH#2538: The key test - use GitCmd to add a file in the main repo
	// This should work even though we're "running" from the worktree
	ctx := context.Background()
	// Resolve symlinks on testFile (macOS /tmp -> /private/var) to match rc.RepoRoot
	resolvedTestFile := resolveSymlinks(testFile)
	relPath, err := filepath.Rel(rc.RepoRoot, resolvedTestFile)
	if err != nil {
		t.Fatalf("failed to get relative path: %v", err)
	}

	addCmd := rc.GitCmd(ctx, "add", "-f", relPath)
	addOutput, err := addCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GitCmd git add failed: %v\nOutput: %s", err, addOutput)
	}

	// Verify the file was staged
	statusCmd := rc.GitCmd(ctx, "status", "--porcelain", relPath)
	statusOutput, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status failed: %v", err)
	}
	if string(statusOutput) == "" {
		t.Error("file was not staged - git status shows no changes")
	}
}

// initGitRepoWithCommit creates a git repo with an initial commit.
func initGitRepoWithCommit(dir string) error {
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %v failed: %w", args[1:], err)
		}
	}
	readmeFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test Repo"), 0644); err != nil {
		return err
	}
	addCmd := exec.Command("git", "add", "README.md")
	addCmd.Dir = dir
	if err := addCmd.Run(); err != nil {
		return err
	}
	commitCmd := gitCommand("commit", "-m", "Initial commit")
	commitCmd.Dir = dir
	return commitCmd.Run()
}

func TestRoleIgnoresInheritedGitRouting(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home, target, decoy := t.TempDir(), t.TempDir(), t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	runGit := func(t *testing.T, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = gitenv.ScrubRouting(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, out)
		}
	}
	for _, repo := range []string{target, decoy} {
		runGit(t, "-C", repo, "init", "--quiet")
	}
	runGit(t, "-C", target, "config", "beads.role", "maintainer")
	runGit(t, "-C", decoy, "config", "beads.role", "contributor")
	t.Chdir(decoy)
	rc := &RepoContext{RepoRoot: target} // The caller has already selected this repo.
	for _, inline := range []bool{false, true} {
		name, generic := "repository", "maintainer"
		poison := map[string]string{"GIT_DIR": filepath.Join(decoy, ".git"), "GIT_WORK_TREE": decoy}
		if inline {
			name, generic = "inline", "injected-role"
			poison = map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "beads.role", "GIT_CONFIG_VALUE_0": generic}
		}
		t.Run(name, func(t *testing.T) {
			for key, value := range poison {
				t.Setenv(key, value)
			}
			if role, ok := rc.Role(); !ok || role != Maintainer {
				t.Errorf("Role() = %q, %v; want maintainer, true", role, ok)
			}
			if out, err := rc.GitOutput(t.Context(), "config", "--get", "beads.role"); err != nil || strings.TrimSpace(out) != generic {
				t.Errorf("generic GitOutput = %q, %v; want %q", out, err, generic)
			}
			for key, value := range poison {
				if os.Getenv(key) != value {
					t.Errorf("Role changed parent environment %s", key)
				}
			}
		})
	}
	t.Run("common_dir", func(t *testing.T) {
		commonDir := filepath.Join(decoy, ".git")
		t.Setenv("GIT_COMMON_DIR", commonDir)
		// Unlike GIT_DIR, this key is not overridden by the generic repository pins.
		if role, ok := rc.Role(); !ok || role != Maintainer {
			t.Errorf("Role() = %q, %v; want maintainer, true", role, ok)
		}
		if out, err := rc.GitOutput(t.Context(), "config", "--get", "beads.role"); err != nil || strings.TrimSpace(out) != string(Contributor) {
			t.Errorf("generic GitOutput = %q, %v; want contributor", out, err)
		}
		if os.Getenv("GIT_COMMON_DIR") != commonDir {
			t.Error("Role changed parent GIT_COMMON_DIR")
		}
	})
	t.Run("live_values", func(t *testing.T) {
		for _, value := range []string{"contributor", "invalid", ""} {
			runGit(t, "-C", target, "config", "beads.role", value)
			if role, ok := rc.Role(); !ok || role != UserRole(value) {
				t.Errorf("fresh Role() = %q, %v; want %q, true", role, ok, value)
			}
		}
	})
	t.Run("absent_and_global", func(t *testing.T) {
		runGit(t, "-C", target, "config", "--unset", "beads.role")
		if role, ok := rc.Role(); ok || role != "" {
			t.Errorf("absent Role() = %q, %v", role, ok)
		}
		if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\nrole = contributor\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if role, ok := rc.Role(); !ok || role != Contributor {
			t.Errorf("global Role() = %q, %v", role, ok)
		}
	})
	t.Run("redirected", func(t *testing.T) {
		redirected := &RepoContext{RepoRoot: t.TempDir(), IsRedirected: true}
		if role, ok := redirected.Role(); !ok || role != Contributor {
			t.Errorf("redirected Role() = %q, %v", role, ok)
		}
	})
}

// writeBeadsProjectForTest creates beadsDir holding a project file, which is
// what hasBeadsProjectFiles needs for discovery to accept it.
func writeBeadsProjectForTest(t *testing.T, beadsDir string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create %s: %v", beadsDir, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}
}

// chdirForRepoContextTest moves the process into dir with fresh resolution
// caches. Under Bazel the ambient CWD is the runfiles tree, outside any
// repository, so every test here must stand somewhere explicit.
func chdirForRepoContextTest(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})
	t.Chdir(dir)
	ResetCaches()
	git.ResetCaches()
}

// nonGitTargetFromGitCWDForTest builds the GH#5700 shape: a git repository A
// to stand in, and a beads project T outside A that is in no git repository.
// BEADS_DIR names T's .beads, as `bd -C T` exports it, and the CWD is A.
func nonGitTargetFromGitCWDForTest(t *testing.T) (callerRepo, target string) {
	t.Helper()
	callerRepo = resolveSymlinks(t.TempDir())
	if err := initGitRepo(callerRepo); err != nil {
		t.Fatalf("failed to init caller repo: %v", err)
	}
	target = resolveSymlinks(t.TempDir())
	writeBeadsProjectForTest(t, filepath.Join(target, ".beads"))

	t.Setenv("BEADS_DIR", filepath.Join(target, ".beads"))
	chdirForRepoContextTest(t, callerRepo)
	return callerRepo, target
}

// TestGetRepoContext_NonGitBeadsDirFromGitCWD pins GH#5700: `bd -C T` exports
// BEADS_DIR without changing directory, so when the caller stands in git
// repository A and T is in no repository, RepoRoot must be T, not A.
func TestGetRepoContext_NonGitBeadsDirFromGitCWD(t *testing.T) {
	assertRoots := func(t *testing.T, wantRepoRoot, wantCWDRepoRoot string) {
		t.Helper()
		rc, err := GetRepoContext()
		if err != nil {
			t.Fatalf("GetRepoContext failed: %v", err)
		}
		if rc.RepoRoot != wantRepoRoot {
			t.Errorf("RepoRoot = %s, want the named project %s (caller repo is %s)",
				rc.RepoRoot, wantRepoRoot, wantCWDRepoRoot)
		}
		if rc.CWDRepoRoot != wantCWDRepoRoot {
			t.Errorf("CWDRepoRoot = %s, want the caller's repo %s", rc.CWDRepoRoot, wantCWDRepoRoot)
		}
	}

	t.Run("in-process", func(t *testing.T) {
		// Linux in-process discovery answers "not a repository" for T.
		callerRepo, target := nonGitTargetFromGitCWDForTest(t)
		assertRoots(t, target, callerRepo)
	})

	t.Run("exec-git", func(t *testing.T) {
		// Set-but-empty makes git.CommonDirInProcess decline (it checks with
		// os.LookupEnv) while git's own discovery is unchanged, so this runs
		// the exec path macOS and Windows always take.
		t.Setenv("GIT_CEILING_DIRECTORIES", "")
		callerRepo, target := nonGitTargetFromGitCWDForTest(t)
		assertRoots(t, target, callerRepo)
	})

	t.Run("target-is-ancestor", func(t *testing.T) {
		// The Gas Town HQ shape: a non-git town root T holding a git rig.
		// Walking up from the rig finds T/.beads, so only work-tree
		// containment (not discoverability) says T is not the rig's own.
		target := resolveSymlinks(t.TempDir())
		writeBeadsProjectForTest(t, filepath.Join(target, ".beads"))
		rig := filepath.Join(target, "rig")
		if err := os.MkdirAll(rig, 0750); err != nil {
			t.Fatalf("failed to create rig: %v", err)
		}
		if err := initGitRepo(rig); err != nil {
			t.Fatalf("failed to init rig repo: %v", err)
		}
		t.Setenv("BEADS_DIR", filepath.Join(target, ".beads"))
		chdirForRepoContextTest(t, rig)
		assertRoots(t, target, rig)
	})

	t.Run("sibling-sharing-a-prefix", func(t *testing.T) {
		// T is <tmp>/repo-other beside A at <tmp>/repo, so A's path is a
		// string prefix of T's and only the separator boundary keeps T out
		// of A's work tree.
		parent := resolveSymlinks(t.TempDir())
		callerRepo := filepath.Join(parent, "repo")
		if err := os.MkdirAll(callerRepo, 0750); err != nil {
			t.Fatalf("failed to create caller repo: %v", err)
		}
		if err := initGitRepo(callerRepo); err != nil {
			t.Fatalf("failed to init caller repo: %v", err)
		}
		target := filepath.Join(parent, "repo-other")
		writeBeadsProjectForTest(t, filepath.Join(target, ".beads"))
		t.Setenv("BEADS_DIR", filepath.Join(target, ".beads"))
		chdirForRepoContextTest(t, callerRepo)
		assertRoots(t, target, callerRepo)
	})

	t.Run("worktree-masked-redirect", func(t *testing.T) {
		// A's .beads redirects to non-git storage S. From a linked worktree W
		// the redirect is invisible to GetRedirectInfo once bd's pre-run rebind
		// exports BEADS_DIR=S/.beads, so W must resolve as A does: rooted at S.
		mainRepo := resolveSymlinks(t.TempDir())
		if err := initGitRepoWithCommit(mainRepo); err != nil {
			t.Fatalf("failed to init main repo: %v", err)
		}
		storage := resolveSymlinks(t.TempDir())
		storageBeadsDir := filepath.Join(storage, ".beads")
		writeBeadsProjectForTest(t, storageBeadsDir)
		if err := os.MkdirAll(filepath.Join(mainRepo, ".beads"), 0750); err != nil {
			t.Fatalf("failed to create main .beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(mainRepo, ".beads", RedirectFileName), []byte(storageBeadsDir+"\n"), 0644); err != nil {
			t.Fatalf("failed to write redirect: %v", err)
		}
		worktree := filepath.Join(resolveSymlinks(t.TempDir()), "wt")
		runGitInDir(t, mainRepo, "worktree", "add", "-q", "--detach", worktree)

		t.Setenv("BEADS_DIR", storageBeadsDir)
		chdirForRepoContextTest(t, mainRepo)
		assertRoots(t, storage, mainRepo)

		chdirForRepoContextTest(t, worktree)
		assertRoots(t, storage, worktree)
	})
}

// TestRole_NonGitBeadsDirFromGitCWD_ImpliesContributor pins the role half of
// GH#5700: a beads project in no repository, named from git repository A, is
// external, so Role() is the implicit Contributor rather than A's beads.role.
func TestRole_NonGitBeadsDirFromGitCWD_ImpliesContributor(t *testing.T) {
	callerRepo, _ := nonGitTargetFromGitCWDForTest(t)
	runGitInDir(t, callerRepo, "config", "beads.role", "maintainer")

	rc, err := GetRepoContext()
	if err != nil {
		t.Fatalf("GetRepoContext failed: %v", err)
	}
	if !rc.IsRedirected {
		t.Error("IsRedirected = false, want true: the named .beads is in no repository while the CWD is in one")
	}
	role, ok := rc.Role()
	if !ok || role != Contributor {
		t.Errorf("Role() = (%q, %v), want (%q, true), not the caller repo's beads.role", role, ok, Contributor)
	}
}

// TestGetRepoContext_InTreeBeadsDirGitCannotPlace is the other half of
// GH#5700: git reporting "not a git repository" for a .beads does not make it
// foreign. One that belongs to the CWD repository's work tree stays local even
// when git discovery cannot reach the repository from it.
func TestGetRepoContext_InTreeBeadsDirGitCannotPlace(t *testing.T) {
	assertLocal := func(t *testing.T, repo string) {
		t.Helper()
		rc, err := GetRepoContext()
		if err != nil {
			t.Fatalf("GetRepoContext failed: %v", err)
		}
		if rc.RepoRoot != repo {
			t.Errorf("RepoRoot = %s, want the CWD repository %s", rc.RepoRoot, repo)
		}
		if rc.IsRedirected {
			t.Error("IsRedirected = true for a .beads inside the CWD repository's work tree")
		}
	}

	t.Run("ceiling", func(t *testing.T) {
		// Discovery from A/sub/x/.beads stops at the ceiling A/sub, so git
		// reports no repository for it, while A itself (above the ceiling)
		// still resolves from the CWD.
		repo := resolveSymlinks(t.TempDir())
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}
		beadsDir := filepath.Join(repo, "sub", "x", ".beads")
		writeBeadsProjectForTest(t, beadsDir)
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(repo, "sub"))
		t.Setenv("BEADS_DIR", beadsDir)
		chdirForRepoContextTest(t, repo)
		assertLocal(t, repo)
	})

	t.Run("symlinked-own-beads", func(t *testing.T) {
		// A/.beads is a symlink to storage in no repository. W is a linked
		// worktree of A; the symlink is untracked, so W has no .beads and
		// discovery falls back to the main checkout's.
		repo := resolveSymlinks(t.TempDir())
		if err := initGitRepoWithCommit(repo); err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}
		store := filepath.Join(resolveSymlinks(t.TempDir()), "store")
		writeBeadsProjectForTest(t, store)
		if err := os.Symlink(store, filepath.Join(repo, ".beads")); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		worktree := filepath.Join(resolveSymlinks(t.TempDir()), "wt")
		runGitInDir(t, repo, "worktree", "add", "-q", "--detach", worktree)

		for _, tc := range []struct {
			name     string
			cwd      string
			beadsDir string
		}{
			// Walk-up discovery yields the lexical A/.beads.
			{name: "discovered", cwd: repo, beadsDir: ""},
			// ExplicitBeadsDir canonicalizes it to the storage path.
			{name: "explicit", cwd: repo, beadsDir: filepath.Join(repo, ".beads")},
			// From W, the worktree fallback yields the lexical A/.beads.
			{name: "worktree-discovered", cwd: worktree, beadsDir: ""},
			{name: "worktree-explicit", cwd: worktree, beadsDir: filepath.Join(repo, ".beads")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("BEADS_DIR", tc.beadsDir)
				chdirForRepoContextTest(t, tc.cwd)
				assertLocal(t, repo)
			})
		}
	})

	t.Run("symlinked-nested-beads", func(t *testing.T) {
		// A/sub/.beads is a symlink to storage in no repository: a nested
		// workspace, which walk-up discovery finds from A/sub and below.
		repo := resolveSymlinks(t.TempDir())
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}
		sub := filepath.Join(repo, "sub")
		deeper := filepath.Join(sub, "deeper")
		if err := os.MkdirAll(deeper, 0750); err != nil {
			t.Fatalf("failed to create %s: %v", deeper, err)
		}
		store := filepath.Join(resolveSymlinks(t.TempDir()), "store")
		writeBeadsProjectForTest(t, store)
		if err := os.Symlink(store, filepath.Join(sub, ".beads")); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		for _, tc := range []struct {
			name     string
			cwd      string
			beadsDir string
		}{
			// Walk-up discovery yields the lexical A/sub/.beads.
			{name: "discovered", cwd: sub, beadsDir: ""},
			{name: "discovered-below", cwd: deeper, beadsDir: ""},
			// ExplicitBeadsDir canonicalizes it to the storage path.
			{name: "explicit", cwd: sub, beadsDir: filepath.Join(sub, ".beads")},
			{name: "explicit-below", cwd: deeper, beadsDir: filepath.Join(sub, ".beads")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("BEADS_DIR", tc.beadsDir)
				chdirForRepoContextTest(t, tc.cwd)
				assertLocal(t, repo)
			})
		}
	})
}

// TestGetRepoContext_RefusesRepositoryGitDoesNotOpen pins the safe.directory
// half of GH#5700. GitCmd pins GIT_DIR, and git skips its ownership check for
// an explicit GIT_DIR, so a .beads in another user's repository must never
// make that repository RepoRoot: `bd dolt remote add` would then run its
// core.fsmonitor as the caller. GetRepoContext refuses it instead, and the
// git-independent entry point still locates the workspace.
func TestGetRepoContext_RefusesRepositoryGitDoesNotOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fsmonitor marker is a shell script")
	}

	// newRefusedTarget builds T, a repository whose core.fsmonitor leaves a
	// marker, holding a beads project, and a caller repository A, then makes
	// every repository another user's except A, which command-scope config
	// allow-lists. Both variables also send discovery to git itself
	// (discoveryEnvOverrides).
	newRefusedTarget := func(t *testing.T) (callerRepo, target, marker string) {
		t.Helper()
		callerRepo = resolveSymlinks(t.TempDir())
		if err := initGitRepo(callerRepo); err != nil {
			t.Fatalf("failed to init caller repo: %v", err)
		}
		target = resolveSymlinks(t.TempDir())
		if err := initGitRepo(target); err != nil {
			t.Fatalf("failed to init target repo: %v", err)
		}
		writeBeadsProjectForTest(t, filepath.Join(target, ".beads"))
		scripts := resolveSymlinks(t.TempDir())
		marker = filepath.Join(scripts, "fsmonitor-ran")
		hook := filepath.Join(scripts, "fsmonitor")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
			t.Fatalf("failed to write fsmonitor hook: %v", err)
		}
		runGitInDir(t, target, "config", "core.fsmonitor", hook)

		t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "safe.directory")
		t.Setenv("GIT_CONFIG_VALUE_0", callerRepo)
		if out, err := exec.Command("git", "-C", target, "rev-parse", "--git-dir").CombinedOutput(); err == nil {
			t.Skipf("this git opens a repository another user owns (no safe.directory check): %s", out)
		}
		return callerRepo, target, marker
	}

	// assertRefused requires GetRepoContext to refuse T while the caller's own
	// repository still resolves; on a regression it shows what the resolved
	// context would have run.
	assertRefused := func(t *testing.T, callerRepo, marker string) {
		t.Helper()
		if root := git.GetRepoRoot(); root != callerRepo {
			t.Fatalf("CWD repository root = %q, want %q: the caller's repository must resolve for this test to reach T", root, callerRepo)
		}
		rc, err := GetRepoContext()
		if err == nil {
			addErr := rc.GitCmd(context.Background(), "add", ".beads").Run()
			_, markerErr := os.Stat(marker)
			t.Fatalf("GetRepoContext rooted RepoRoot at %s, a repository git refuses to open; GitCmd add there (err %v) ran its core.fsmonitor as the caller: %v",
				rc.RepoRoot, addErr, markerErr == nil)
		}
		var noRoot *NoRepoRootError
		if !errors.As(err, &noRoot) {
			t.Fatalf("GetRepoContext error = %v, want a *NoRepoRootError", err)
		}
	}

	t.Run("named", func(t *testing.T) {
		callerRepo, target, marker := newRefusedTarget(t)
		t.Setenv("BEADS_DIR", filepath.Join(target, ".beads"))
		chdirForRepoContextTest(t, callerRepo)
		assertRefused(t, callerRepo, marker)

		rc, err := GetRepoContextAllowingNoGit()
		if err != nil {
			t.Fatalf("GetRepoContextAllowingNoGit failed: %v", err)
		}
		if rc.RepoRoot != target || rc.CWDRepoRoot != callerRepo || !rc.IsRedirected {
			t.Errorf("recovered context = (RepoRoot %q, CWDRepoRoot %q, IsRedirected %v), want (%q, %q, true)",
				rc.RepoRoot, rc.CWDRepoRoot, rc.IsRedirected, target, callerRepo)
		}
		if role, ok := rc.Role(); !ok || role != Contributor {
			t.Errorf("Role() = (%q, %v), want (%q, true)", role, ok, Contributor)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("the refused repository's core.fsmonitor ran")
		}
	})

	t.Run("redirect", func(t *testing.T) {
		// The same refusal covers a .beads/redirect into that repository.
		callerRepo, target, marker := newRefusedTarget(t)
		localBeadsDir := filepath.Join(callerRepo, ".beads")
		if err := os.MkdirAll(localBeadsDir, 0750); err != nil {
			t.Fatalf("failed to create %s: %v", localBeadsDir, err)
		}
		if err := os.WriteFile(filepath.Join(localBeadsDir, RedirectFileName), []byte(filepath.Join(target, ".beads")+"\n"), 0644); err != nil {
			t.Fatalf("failed to write redirect: %v", err)
		}
		t.Setenv("BEADS_DIR", "")
		chdirForRepoContextTest(t, callerRepo)
		assertRefused(t, callerRepo, marker)

		// Discovery from A follows A's redirect to T, so position alone would
		// call T local and send Role() to git config in T; the refusal keeps
		// the redirect.
		rc, err := GetRepoContextAllowingNoGit()
		if err != nil {
			t.Fatalf("GetRepoContextAllowingNoGit failed: %v", err)
		}
		if !rc.IsRedirected {
			t.Error("recovered context IsRedirected = false for a redirect into a refused repository")
		}
		if role, ok := rc.Role(); !ok || role != Contributor {
			t.Errorf("Role() = (%q, %v), want (%q, true)", role, ok, Contributor)
		}
	})
}
