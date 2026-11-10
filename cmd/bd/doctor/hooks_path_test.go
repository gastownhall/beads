package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setGitConfig sets a git config value in dir, failing the test on error.
func setGitConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	cmd := gitCommand("config", key, value)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s %s failed: %v (%s)", key, value, err, strings.TrimSpace(string(out)))
	}
}

// unsetGitConfig unsets a git config value in dir. Ignores "not set" errors
// since the goal is just to make sure the key is absent.
func unsetGitConfig(t *testing.T, dir, key string) {
	t.Helper()
	cmd := gitCommand("config", "--unset", key)
	cmd.Dir = dir
	_ = cmd.Run()
}

// getGitConfig reads a git config value in dir. Returns "" if unset.
func getGitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := gitCommand("config", "--get", key)
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

// relocatedHookBody is a marker-managed hook the relocation check recognizes.
const relocatedHookBody = "#!/usr/bin/env sh\n" + bdSectionMarkerPrefix + " v1.0.0 ---\nbd hooks run pre-commit \"$@\"\n# --- END BEADS INTEGRATION v1.0.0 ---\n"

// writeRelocatedHook writes body as dir/pre-commit, creating dir.
func writeRelocatedHook(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pre-commit"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// strandedHooksPath returns the absolute hooks path a repository that used to
// live under a different directory would still carry (GH#5853). Neither it nor
// its parent exists.
func strandedHooksPath(t *testing.T, layout string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "moved-away", "repo", layout)
}

// sameDir reports whether two absolute directories name the same location
// once their existing prefixes are symlink-resolved.
func sameDir(a, b string) bool {
	return resolveExistingPrefix(a) == resolveExistingPrefix(b)
}

// assertNotCreated fails when the stranded path or its parent was created.
func assertNotCreated(t *testing.T, stale string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Errorf("stale hooks path parent %s was created (err=%v); a repair must not recreate the former location", filepath.Dir(stale), err)
	}
}

// relocationCase is a repository whose core.hooksPath still names its former
// location while its beads hooks live at target.
type relocationCase struct {
	name   string
	layout string // the former path's trailing layout
	// setup prepares the current hooks and returns the directory the value
	// must be repointed to.
	setup func(t *testing.T, dir string) string
}

func relocationCases() []relocationCase {
	return []relocationCase{
		{
			name:   ".beads/hooks",
			layout: filepath.Join(".beads", "hooks"),
			setup: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, ".beads", "hooks")
				writeRelocatedHook(t, target, relocatedHookBody)
				return target
			},
		},
		{
			name:   ".beads-hooks",
			layout: ".beads-hooks",
			setup: func(t *testing.T, dir string) string {
				target := filepath.Join(dir, ".beads-hooks")
				writeRelocatedHook(t, target, relocatedHookBody)
				return target
			},
		},
		{
			// BEADS_DIR (like .beads/redirect) places the effective storage
			// outside the repository; `bd hooks install --beads` writes
			// <storage>/hooks, so that is where the value must point.
			name:   "redirected storage",
			layout: filepath.Join(".beads", "hooks"),
			setup: func(t *testing.T, dir string) string {
				storage := filepath.Join(t.TempDir(), "external storage")
				if err := os.MkdirAll(storage, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BEADS_DIR", storage)
				target := filepath.Join(storage, "hooks")
				writeRelocatedHook(t, target, relocatedHookBody)
				// A decoy in-repository directory must not win over storage.
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				return target
			},
		},
	}
}

// TestCheckHooksPath_RelocatedBeadsPath: a moved repository's stranded
// absolute core.hooksPath is reported as a relocated beads path naming both
// the configured and the current directory, and the check changes nothing
// (GH#5853).
func TestCheckHooksPath_RelocatedBeadsPath(t *testing.T) {
	for _, tc := range relocationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DIR", "")
			dir := t.TempDir()
			setupGitRepoInDir(t, dir)
			target := tc.setup(t, dir)
			stale := strandedHooksPath(t, tc.layout)
			setGitConfig(t, dir, "core.hooksPath", stale)

			runInDir(t, dir, func() {
				check := CheckHooksPath()
				if check.Status != StatusWarning {
					t.Fatalf("expected StatusWarning, got %q: %s", check.Status, check.Message)
				}
				if !strings.Contains(check.Message, "moved repository") {
					t.Errorf("message does not identify a relocated beads path: %q", check.Message)
				}
				if !strings.Contains(check.Detail, stale) {
					t.Errorf("detail does not name the configured path %q: %q", stale, check.Detail)
				}
				if !strings.Contains(check.Detail, target) && !strings.Contains(check.Detail, resolveExistingPrefix(target)) {
					t.Errorf("detail does not name the current path %q: %q", target, check.Detail)
				}
				for _, want := range []string{"bd doctor --fix", "bd hooks install"} {
					if !strings.Contains(check.Fix, want) {
						t.Errorf("fix %q does not recommend %q", check.Fix, want)
					}
				}
			})

			if got := getGitConfig(t, dir, "core.hooksPath"); got != stale {
				t.Errorf("CheckHooksPath changed core.hooksPath to %q; a check must be read-only", got)
			}
			assertNotCreated(t, stale)
		})
	}
}

// TestInspectHooksPath_UninspectablePath: a configured hooks path that is not
// a directory, or that cannot be inspected, is not evidence the directory is
// gone. The inspection reports the error instead of Missing, the check names
// it, and the fix neither unsets nor repoints the value (GH#5853).
func TestInspectHooksPath_UninspectablePath(t *testing.T) {
	cases := []struct {
		name string
		// arrange sets up the repository and returns the configured value.
		arrange func(t *testing.T, dir string) string
		want    string
	}{
		{
			// The current managed location, but a file: no GH#4440 unset.
			name: "not a directory",
			arrange: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, ".beads", "hooks")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("not a directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: "is not a directory",
		},
		{
			// A former .beads/hooks behind an unreadable parent, with valid
			// current hooks: a permission failure is not a move.
			name: "permission denied",
			arrange: func(t *testing.T, dir string) string {
				if runtime.GOOS == "windows" || os.Geteuid() == 0 {
					t.Skip("directory permissions do not deny this user")
				}
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				locked := filepath.Join(t.TempDir(), "locked")
				if err := os.MkdirAll(locked, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(locked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
				return filepath.Join(locked, "repo", ".beads", "hooks")
			},
			want: "could not inspect",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DIR", "")
			dir := t.TempDir()
			setupGitRepoInDir(t, dir)
			configured := tc.arrange(t, dir)
			setGitConfig(t, dir, "core.hooksPath", configured)

			runInDir(t, dir, func() {
				q, ok := CurrentHooksPathQuery()
				if !ok {
					t.Fatal("CurrentHooksPathQuery: not in a repository")
				}
				s := InspectHooksPath(q)
				if !strings.Contains(s.InspectError, tc.want) {
					t.Errorf("InspectError = %q, want it to contain %q", s.InspectError, tc.want)
				}
				if s.Missing() || s.Managed || s.RelocatedTo != "" {
					t.Errorf("state = %+v; an uninspectable path must be neither missing, managed nor relocated", s)
				}
				check := CheckHooksPath()
				if check.Status != StatusWarning || !strings.Contains(check.Detail, tc.want) {
					t.Errorf("check = %q %q / %q, want a warning naming %q", check.Status, check.Message, check.Detail, tc.want)
				}
				if strings.Contains(check.Fix, "--unset") || strings.Contains(check.Fix, "repoint") {
					t.Errorf("fix %q offers an unset or repoint for a path not proven gone", check.Fix)
				}
				if err := FixHooksPath(); err != nil {
					t.Fatalf("FixHooksPath failed: %v", err)
				}
			})

			if got := getGitConfig(t, dir, "core.hooksPath"); got != configured {
				t.Errorf("core.hooksPath = %q after fix, want it left at %q", got, configured)
			}
		})
	}
}

// TestFixHooksPath_RelocatedBeadsPath: bd doctor --fix repoints a moved
// repository's stranded core.hooksPath to the validated current directory in
// the local config, never recreating the former location; a second run is a
// no-op (GH#5853).
func TestFixHooksPath_RelocatedBeadsPath(t *testing.T) {
	for _, tc := range relocationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DIR", "")
			dir := t.TempDir()
			setupGitRepoInDir(t, dir)
			target := tc.setup(t, dir)
			stale := strandedHooksPath(t, tc.layout)
			setGitConfig(t, dir, "core.hooksPath", stale)

			runInDir(t, dir, func() {
				if err := FixHooksPath(); err != nil {
					t.Fatalf("FixHooksPath failed: %v", err)
				}
			})

			got := getGitConfig(t, dir, "core.hooksPath")
			if !filepath.IsAbs(got) || !sameDir(got, target) {
				t.Fatalf("core.hooksPath = %q, want the absolute current directory %q", got, target)
			}
			local := gitCommand("config", "--local", "--get", "core.hooksPath")
			local.Dir = dir
			if out, err := local.Output(); err != nil || strings.TrimSpace(string(out)) != got {
				t.Errorf("repointed value is not in the local config: %q (%v)", out, err)
			}
			assertNotCreated(t, stale)
			if body, err := os.ReadFile(filepath.Join(target, "pre-commit")); err != nil || string(body) != relocatedHookBody {
				t.Errorf("FixHooksPath modified the current hooks: %q (%v)", body, err)
			}

			runInDir(t, dir, func() {
				if check := CheckHooksPath(); check.Status != StatusOK {
					t.Errorf("after the repair, Hooks Path = %q: %s", check.Status, check.Message)
				}
				if err := FixHooksPath(); err != nil {
					t.Fatalf("second FixHooksPath failed: %v", err)
				}
			})
			if again := getGitConfig(t, dir, "core.hooksPath"); again != got {
				t.Errorf("second FixHooksPath changed core.hooksPath from %q to %q", got, again)
			}
		})
	}
}

// TestFixHooksPath_RelocatedBeadsPath_Ineligible: a missing path that is not
// provably a moved repository's beads hooks — no validated replacement,
// unrecognized hooks, a symlinked destination, a value outside the local
// config, an override that shadows it, an existing former directory, or a value
// that changed after the check — is never repointed, and the former location
// is never created (GH#5853).
func TestFixHooksPath_RelocatedBeadsPath_Ineligible(t *testing.T) {
	beadsLayout := filepath.Join(".beads", "hooks")
	cases := []struct {
		name string
		// arrange sets up dir and returns the value core.hooksPath must
		// still hold after the fix; stale is the former path.
		arrange func(t *testing.T, dir, stale string) string
		// wantInDetail is a refusal fragment the check must report, if any.
		wantInDetail string
	}{
		{
			name: "no replacement directory",
			arrange: func(t *testing.T, dir, stale string) string {
				setGitConfig(t, dir, "core.hooksPath", stale)
				return stale
			},
			wantInDetail: "no beads hooks directory exists",
		},
		{
			name: "replacement holds no beads hooks",
			arrange: func(t *testing.T, dir, stale string) string {
				// Mentions bd, but is no beads hook.
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), "#!/bin/sh\necho bd\n")
				setGitConfig(t, dir, "core.hooksPath", stale)
				return stale
			},
			wantInDetail: "holds no beads hooks",
		},
		{
			name: "replacement is a symlink",
			arrange: func(t *testing.T, dir, stale string) string {
				real := filepath.Join(t.TempDir(), "real-hooks")
				writeRelocatedHook(t, real, relocatedHookBody)
				if err := os.Symlink(real, filepath.Join(dir, ".beads", "hooks")); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				setGitConfig(t, dir, "core.hooksPath", stale)
				return stale
			},
			wantInDetail: "is a symlink",
		},
		{
			name: "third-party layout",
			arrange: func(t *testing.T, dir, stale string) string {
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				husky := filepath.Join(filepath.Dir(filepath.Dir(stale)), ".husky", "_")
				setGitConfig(t, dir, "core.hooksPath", husky)
				return husky
			},
		},
		{
			name: "global-only value",
			arrange: func(t *testing.T, dir, stale string) string {
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				unsetGitConfig(t, dir, "core.hooksPath")
				t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
				cmd := gitCommand("config", "--global", "core.hooksPath", stale)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git config --global: %v (%s)", err, out)
				}
				return stale
			},
			wantInDetail: "global config",
		},
		{
			name: "differing worktree override",
			arrange: func(t *testing.T, dir, stale string) string {
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				setGitConfig(t, dir, "core.hooksPath", stale)
				setGitConfig(t, dir, "extensions.worktreeConfig", "true")
				other := filepath.Join(t.TempDir(), "elsewhere", ".beads", "hooks")
				cmd := gitCommand("config", "--worktree", "core.hooksPath", other)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git config --worktree: %v (%s)", err, out)
				}
				return stale
			},
			wantInDetail: "worktree config",
		},
		{
			name: "command-line override",
			arrange: func(t *testing.T, dir, stale string) string {
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				setGitConfig(t, dir, "core.hooksPath", stale)
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
				t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(t.TempDir(), "elsewhere", ".beads", "hooks"))
				return stale
			},
			wantInDetail: "command config",
		},
		{
			name: "former directory still exists",
			arrange: func(t *testing.T, dir, stale string) string {
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				if err := os.MkdirAll(stale, 0o755); err != nil {
					t.Fatal(err)
				}
				setGitConfig(t, dir, "core.hooksPath", stale)
				return stale
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DIR", "")
			dir := t.TempDir()
			setupGitRepoInDir(t, dir)
			stale := strandedHooksPath(t, beadsLayout)
			want := tc.arrange(t, dir, stale)
			readRaw := func() string {
				cmd := gitCommand("config", "--show-scope", "--get-all", "core.hooksPath")
				cmd.Dir = dir
				out, _ := cmd.Output()
				return string(out)
			}
			before := readRaw()

			runInDir(t, dir, func() {
				check := CheckHooksPath()
				if strings.Contains(check.Message, "moved repository") {
					t.Errorf("an ineligible path was reported as relocated: %q / %q", check.Message, check.Detail)
				}
				if tc.wantInDetail != "" && !strings.Contains(check.Detail, tc.wantInDetail) {
					t.Errorf("detail %q does not explain the refusal (%q)", check.Detail, tc.wantInDetail)
				}
				if err := FixHooksPath(); err != nil {
					t.Fatalf("FixHooksPath failed: %v", err)
				}
			})

			if after := readRaw(); after != before {
				t.Errorf("FixHooksPath changed core.hooksPath:\nbefore: %s\nafter:  %s", before, after)
			}
			if _, err := os.Lstat(want); tc.name != "former directory still exists" && !os.IsNotExist(err) {
				t.Errorf("FixHooksPath created %s (err=%v)", want, err)
			}
		})
	}
}

// TestFixHooksPath_RelocatedBeadsPath_ChangedBeforeRepair: the repair re-reads
// core.hooksPath immediately before writing, so a value that changed after
// the check is not overwritten with the relocation target (GH#5853).
func TestFixHooksPath_RelocatedBeadsPath_ChangedBeforeRepair(t *testing.T) {
	t.Setenv("BEADS_DIR", "")
	dir := t.TempDir()
	setupGitRepoInDir(t, dir)
	writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
	stale := strandedHooksPath(t, filepath.Join(".beads", "hooks"))
	setGitConfig(t, dir, "core.hooksPath", stale)
	husky := filepath.Join(filepath.Dir(stale), ".husky")

	runInDir(t, dir, func() {
		if check := CheckHooksPath(); !strings.Contains(check.Message, "moved repository") {
			t.Fatalf("precondition: expected a relocated path, got %q", check.Message)
		}
		// The user (or another tool) changes the value between the check
		// and the fix pass.
		setGitConfig(t, dir, "core.hooksPath", husky)
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, dir, "core.hooksPath"); got != husky {
		t.Errorf("core.hooksPath = %q, want the changed third-party value %q left alone", got, husky)
	}
	assertNotCreated(t, stale)
}

// TestFixHooksPath_ManagedDeletedInPlaceStillUnsets: a current beads location
// deleted in place keeps the GH#4440 repair — the value is unset, never
// repointed to another beads hooks directory that happens to exist, in the
// repository or in out-of-repo storage (GH#5853).
func TestFixHooksPath_ManagedDeletedInPlaceStillUnsets(t *testing.T) {
	cases := []struct {
		name string
		// arrange returns the missing managed value to configure.
		arrange func(t *testing.T, dir string) string
	}{
		{
			name: "shared layout deleted beside .beads/hooks",
			arrange: func(t *testing.T, dir string) string {
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				return filepath.Join(dir, ".beads-hooks")
			},
		},
		{
			name: "out-of-repo storage hooks deleted",
			arrange: func(t *testing.T, dir string) string {
				storage := filepath.Join(t.TempDir(), "external storage")
				if err := os.MkdirAll(storage, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BEADS_DIR", storage)
				writeRelocatedHook(t, filepath.Join(dir, ".beads", "hooks"), relocatedHookBody)
				return filepath.Join(storage, "hooks")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DIR", "")
			dir := t.TempDir()
			setupGitRepoInDir(t, dir)
			missing := tc.arrange(t, dir)
			setGitConfig(t, dir, "core.hooksPath", missing)

			runInDir(t, dir, func() {
				check := CheckHooksPath()
				if strings.Contains(check.Message, "moved repository") {
					t.Errorf("a managed path deleted in place was reported as relocated: %q", check.Message)
				}
				if !strings.Contains(check.Fix, "unset core.hooksPath") {
					t.Errorf("Fix = %q, want the unset remedy", check.Fix)
				}
				if err := FixHooksPath(); err != nil {
					t.Fatalf("FixHooksPath failed: %v", err)
				}
			})

			if got := getGitConfig(t, dir, "core.hooksPath"); got != "" {
				t.Errorf("core.hooksPath = %q, want it unset", got)
			}
			if _, err := os.Lstat(missing); !os.IsNotExist(err) {
				t.Errorf("FixHooksPath created %s (err=%v)", missing, err)
			}
		})
	}
}

// TestFixHooksPath_RelocatedLinkedWorktree: after a moved main repository's
// linked-worktree metadata is repaired, running the fix from the linked
// worktree repoints the shared value to the main repository's hooks, and git
// runs them from both worktrees (GH#5853).
func TestFixHooksPath_RelocatedLinkedWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook fixture")
	}
	t.Setenv("BEADS_DIR", "")
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, strings.TrimSpace(string(out)))
		}
	}
	before := filepath.Join(root, "a")
	mainRepo := filepath.Join(before, "main")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(mainRepo, "init", "-q")
	git(mainRepo, "commit", "-q", "--allow-empty", "-m", "init")
	git(mainRepo, "worktree", "add", "-q", filepath.Join(before, "wt"))

	sentinel := filepath.Join(root, "ran")
	hook := "#!/bin/sh\n" + bdSectionMarkerPrefix + " v1.0.0 ---\necho ran >> '" + sentinel + "'\n# --- END BEADS INTEGRATION v1.0.0 ---\n"
	writeRelocatedHook(t, filepath.Join(mainRepo, ".beads", "hooks"), hook)
	git(mainRepo, "config", "core.hooksPath", filepath.Join(mainRepo, ".beads", "hooks"))

	after := filepath.Join(root, "b")
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	movedMain, movedWT := filepath.Join(after, "main"), filepath.Join(after, "wt")
	git(movedMain, "worktree", "repair", movedWT)
	stale := filepath.Join(mainRepo, ".beads", "hooks")

	runInDir(t, movedWT, func() {
		if check := CheckHooksPath(); !strings.Contains(check.Message, "moved repository") {
			t.Fatalf("expected a relocated path from the linked worktree, got %q: %s", check.Message, check.Detail)
		}
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	want := filepath.Join(movedMain, ".beads", "hooks")
	if got := getGitConfig(t, movedWT, "core.hooksPath"); !sameDir(got, want) {
		t.Fatalf("core.hooksPath = %q, want %q", got, want)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("the former location %s was created (err=%v)", stale, err)
	}
	git(movedMain, "hook", "run", "pre-commit")
	git(movedWT, "hook", "run", "pre-commit")
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("hooks did not run: %v", err)
	}
	if n := strings.Count(string(data), "ran"); n != 2 {
		t.Errorf("hook ran %d times, want once from each worktree", n)
	}
}
