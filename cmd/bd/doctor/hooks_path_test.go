package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setGitConfig sets a git config value in dir, failing the test on error.
func setGitConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", key, value)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s %s failed: %v (%s)", key, value, err, strings.TrimSpace(string(out)))
	}
}

// unsetGitConfig unsets a git config value in dir. Ignores "not set" errors
// since the goal is just to make sure the key is absent.
func unsetGitConfig(t *testing.T, dir, key string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--unset", key)
	cmd.Dir = dir
	_ = cmd.Run()
}

// getGitConfig reads a git config value in dir. Returns "" if unset.
func getGitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--get", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestCheckHooksPath_NotInGitRepo(t *testing.T) {
	dir := t.TempDir()
	runInDir(t, dir, func() {
		check := CheckHooksPath()
		if check.Status != StatusOK {
			t.Errorf("expected StatusOK, got %q: %s", check.Status, check.Message)
		}
		if !strings.Contains(check.Message, "N/A") {
			t.Errorf("expected N/A message, got %q", check.Message)
		}
	})
}

func TestCheckHooksPath_Unset(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	// setupGitRepoInDir sets core.hooksPath=.git/hooks for isolation; clear it
	// so we exercise the genuinely-unset case.
	unsetGitConfig(t, dir, "core.hooksPath")

	runInDir(t, dir, func() {
		check := CheckHooksPath()
		if check.Status != StatusOK {
			t.Errorf("expected StatusOK, got %q: %s", check.Status, check.Message)
		}
		if !strings.Contains(check.Message, "not set") {
			t.Errorf("expected message to mention core.hooksPath is not set, got %q", check.Message)
		}
	})
}

func TestCheckHooksPath_SetToExistingDir(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	// setupGitRepoInDir already points core.hooksPath at .git/hooks, which
	// always exists after git init.

	runInDir(t, dir, func() {
		check := CheckHooksPath()
		if check.Status != StatusOK {
			t.Errorf("expected StatusOK, got %q: %s", check.Status, check.Message)
		}
	})
}

func TestCheckHooksPath_MissingBeadsManagedPath(t *testing.T) {
	tests := []struct {
		name        string
		hooksPath   func(dir string) string
		wantAbsHint bool
	}{
		{
			name:      "relative .beads/hooks",
			hooksPath: func(dir string) string { return ".beads/hooks" },
		},
		{
			name:      "relative .beads-hooks",
			hooksPath: func(dir string) string { return ".beads-hooks" },
		},
		{
			name:        "absolute .beads/hooks",
			hooksPath:   func(dir string) string { return filepath.Join(dir, ".beads", "hooks") },
			wantAbsHint: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			setupGitRepoInDir(t, dir)
			hooksPath := tt.hooksPath(dir)
			setGitConfig(t, dir, "core.hooksPath", hooksPath)

			runInDir(t, dir, func() {
				check := CheckHooksPath()
				if check.Status != StatusWarning {
					t.Fatalf("expected StatusWarning, got %q: %s", check.Status, check.Message)
				}
				if !strings.Contains(check.Fix, "bd doctor --fix") {
					t.Errorf("expected beads-managed fix hint, got %q", check.Fix)
				}
				if !strings.Contains(check.Detail, hooksPath) {
					t.Errorf("expected detail to mention configured value %q, got %q", hooksPath, check.Detail)
				}

				if err := FixHooksPath(); err != nil {
					t.Fatalf("FixHooksPath failed: %v", err)
				}
			})

			if got := getGitConfig(t, dir, "core.hooksPath"); got != "" {
				t.Errorf("expected core.hooksPath to be unset after FixHooksPath, got %q", got)
			}
		})
	}
}

func TestCheckHooksPath_MissingNonBeadsPath(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	setGitConfig(t, dir, "core.hooksPath", ".husky/_")

	runInDir(t, dir, func() {
		check := CheckHooksPath()
		if check.Status != StatusWarning {
			t.Fatalf("expected StatusWarning, got %q: %s", check.Status, check.Message)
		}
		if strings.Contains(check.Fix, "bd doctor --fix") {
			t.Errorf("did not expect bd auto-fix to be offered for a non-beads-managed path, got %q", check.Fix)
		}
		if !strings.Contains(check.Fix, "not beads-managed") {
			t.Errorf("expected fix text to say the path is not beads-managed, got %q", check.Fix)
		}

		// The important guard: FixHooksPath must be a no-op for a third-party
		// (e.g. husky) hooksPath, even though its target is also missing.
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath returned error: %v", err)
		}
	})

	if got := getGitConfig(t, dir, "core.hooksPath"); got != ".husky/_" {
		t.Errorf("expected core.hooksPath to remain %q after FixHooksPath, got %q", ".husky/_", got)
	}
}

func TestCheckHooksPath_ExistingNonBeadsPath(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	huskyDir := filepath.Join(dir, ".husky", "_")
	if err := os.MkdirAll(huskyDir, 0755); err != nil {
		t.Fatalf("failed to create husky dir: %v", err)
	}
	setGitConfig(t, dir, "core.hooksPath", ".husky/_")

	runInDir(t, dir, func() {
		check := CheckHooksPath()
		if check.Status != StatusOK {
			t.Errorf("expected StatusOK for an existing third-party hooksPath, got %q: %s", check.Status, check.Message)
		}
	})
}

func TestFixHooksPath_NoOpWhenTargetExists(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	beadsHooksDir := filepath.Join(dir, ".beads", "hooks")
	if err := os.MkdirAll(beadsHooksDir, 0755); err != nil {
		t.Fatalf("failed to create .beads/hooks: %v", err)
	}
	setGitConfig(t, dir, "core.hooksPath", ".beads/hooks")

	runInDir(t, dir, func() {
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, dir, "core.hooksPath"); got != ".beads/hooks" {
		t.Errorf("expected core.hooksPath to remain %q when target exists, got %q", ".beads/hooks", got)
	}
}

// TestCheckHooksPath_SymlinkedRepoRoot pins the fix for the macOS-red build at
// 868dd077a: git canonicalizes the repo root it reports, but the configured
// core.hooksPath is whatever string was written, so an absolute beads-managed
// path reached through a symlink used to string-compare unequal and be
// misreported as third-party — leaving FixHooksPath refusing to unset it.
//
// macOS hits this on every temp dir (/var is a symlink to /private/var), which
// is how CI caught it, but nothing about it is macOS-specific: an explicit
// symlink reproduces it anywhere git resolves the root.
func TestCheckHooksPath_SymlinkedRepoRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated privileges on Windows")
	}

	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatalf("failed to create real repo dir: %v", err)
	}
	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("failed to symlink %s -> %s: %v", linkDir, realDir, err)
	}
	setupGitRepoInDir(t, realDir)

	// The value a user (or an older bd) wrote: absolute, and carrying the
	// unresolved symlink prefix. Its target does not exist, so it is dangling.
	hooksPath := filepath.Join(linkDir, ".beads", "hooks")
	setGitConfig(t, realDir, "core.hooksPath", hooksPath)

	runInDir(t, realDir, func() {
		check := CheckHooksPath()
		if check.Status != StatusWarning {
			t.Fatalf("expected StatusWarning, got %q: %s", check.Status, check.Message)
		}
		if !strings.Contains(check.Fix, "bd doctor --fix") {
			t.Errorf("expected beads-managed fix hint for a symlinked repo root, got %q", check.Fix)
		}
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, realDir, "core.hooksPath"); got != "" {
		t.Errorf("expected core.hooksPath to be unset after FixHooksPath, got %q", got)
	}
}

// TestFixHooksPath_ClearsDanglingOutOfRepoStorageHooks pins the other half of
// the shared-predicate contract. `bd hooks install --beads` configures
// <effective .beads>/hooks, which BEADS_DIR can place outside the repository, so
// `bd doctor --fix` must recognize that value as beads-managed too — otherwise
// the check reports bd's own dangling hooksPath as third-party and deliberately
// refuses to unset it, and nothing repairs a repo whose hooks are all shadowed.
func TestFixHooksPath_ClearsDanglingOutOfRepoStorageHooks(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	// External storage that FindBeadsDir will accept: BEADS_DIR plus at least
	// one project file. Its hooks/ directory is deliberately absent, which is
	// what makes the configured path dangling and therefore doctor's business.
	storage := filepath.Join(t.TempDir(), "external storage")
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", storage)
	storageHooks := filepath.Join(storage, "hooks")
	setGitConfig(t, dir, "core.hooksPath", storageHooks)

	runInDir(t, dir, func() {
		check := CheckHooksPath()
		if check.Status != StatusWarning {
			t.Fatalf("expected a warning for a dangling storage hooks path, got %q: %s", check.Status, check.Message)
		}
		if !strings.Contains(check.Fix, "bd doctor --fix") {
			t.Errorf("expected the beads-managed fix hint, got %q", check.Fix)
		}
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, dir, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath = %q, want it unset after FixHooksPath", got)
	}
}

// TestFixHooksPath_LeavesDanglingStorageHooksWhenStorageIsGone records an
// accepted limitation, not a desired behavior: the storage arm recognizes the
// directory `bd hooks install --beads` would configure *right now*, not the value
// install actually wrote. BeadsManagedStorageHooksDir resolves through
// beads.FindBeadsDir, whose every arm is gated on the storage directory existing
// and holding project files, so deleting .beads/ — the exact trigger
// CheckHooksPath documents for GH#4440 — removes the only value the predicate
// could have matched. The dangling out-of-repo hooksPath is then classified as
// third-party and deliberately left alone.
//
// Tracked in bd-s76jm: record what install configured (a local beads.hooksPath
// key) and match the record, with the current recomputation as the fallback. When
// that lands this expectation flips to "unset" and this test becomes its
// counterfactual.
func TestFixHooksPath_LeavesDanglingStorageHooksWhenStorageIsGone(t *testing.T) {
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	storage := filepath.Join(t.TempDir(), "external storage")
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", storage)
	storageHooks := filepath.Join(storage, "hooks")
	setGitConfig(t, dir, "core.hooksPath", storageHooks)

	// The user deletes the whole storage directory without running
	// `bd hooks uninstall` first, leaving core.hooksPath behind in the common dir.
	if err := os.RemoveAll(storage); err != nil {
		t.Fatal(err)
	}

	runInDir(t, dir, func() {
		if got := BeadsManagedStorageHooksDir(); got != "" {
			t.Fatalf("precondition: no storage may resolve once it is deleted, got %q", got)
		}
		check := CheckHooksPath()
		if check.Status != StatusWarning {
			t.Fatalf("expected a warning for a dangling storage hooks path, got %q: %s", check.Status, check.Message)
		}
		if !strings.Contains(check.Fix, "not beads-managed") {
			t.Errorf("expected the third-party hint the unrecognized value earns, got %q", check.Fix)
		}
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, dir, "core.hooksPath"); got != storageHooks {
		t.Errorf("core.hooksPath = %q, want it still set to %q: the value is unclearable once the derivation inputs are gone (bd-s76jm)", got, storageHooks)
	}
}

// TestIsBeadsManagedHooksPath_LeavesThirdPartyAlone guards the widened
// symlink-resolving match against over-reach: resolving paths must not make
// an unrelated hooks directory look beads-managed, since a false positive here
// means bd unsets a hooksPath it does not own (husky, lefthook).
func TestIsBeadsManagedHooksPath_LeavesThirdPartyAlone(t *testing.T) {
	root := t.TempDir()
	// The out-of-repo storage directory `bd hooks install --beads` configures
	// when BEADS_DIR or a .beads/redirect points outside the repository.
	storage := filepath.Join(t.TempDir(), "external storage")
	storageHooks := filepath.Join(storage, "hooks")
	cases := []struct {
		name string
		// storageHooksDir is what the caller resolved for this repository; ""
		// means no beads storage resolved at all.
		storageHooksDir string
		hooksPath       string
		want            bool
	}{
		{"relative beads hooks", "", ".beads/hooks", true},
		{"relative shared beads hooks", "", ".beads-hooks", true},
		{"absolute beads hooks", "", filepath.Join(root, ".beads", "hooks"), true},
		{"absolute shared beads hooks", "", filepath.Join(root, ".beads-hooks"), true},
		{"relative husky", "", ".husky/_", false},
		{"relative githooks", "", ".githooks", false},
		{"absolute husky", "", filepath.Join(root, ".husky", "_"), false},
		{"beads hooks under a different repo", "", filepath.Join(t.TempDir(), ".beads", "hooks"), false},
		{"deeper path below beads hooks", "", filepath.Join(root, ".beads", "hooks", "pre-commit"), false},
		{"empty", "", "", false},
		// The install target must be recognized so uninstall and
		// `bd doctor --fix` can clear the value install itself wrote (GH#4440).
		{"out-of-repo storage hooks", storageHooks, storageHooks, true},
		// ...and only that directory: the arm is gated on the resolved storage,
		// not on "any absolute path", so it never reaches a third-party dir.
		{"unrelated out-of-repo hooks dir", storageHooks, filepath.Join(t.TempDir(), "hooks"), false},
		{"storage itself, not its hooks dir", storageHooks, storage, false},
		{"storage hooks with no storage resolved", "", storageHooks, false},
		{"in-repo forms still match with storage resolved", storageHooks, filepath.Join(root, ".beads-hooks"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBeadsManagedHooksPath(root, tc.storageHooksDir, tc.hooksPath); got != tc.want {
				t.Errorf("IsBeadsManagedHooksPath(%q, %q, %q) = %v, want %v",
					root, tc.storageHooksDir, tc.hooksPath, got, tc.want)
			}
		})
	}
}
