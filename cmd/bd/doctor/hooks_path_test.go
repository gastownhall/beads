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

// writeRecognizedHooks writes a pre-commit hook bd recognizes as
// beads-managed content into dir, the minimum evidence the GH#5853
// relocation validation accepts for a replacement target.
func writeRecognizedHooks(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("failed to create hooks dir %s: %v", dir, err)
	}
	body := "#!/bin/sh\n# bd-shim 1.0.0\nexec bd hooks run pre-commit \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pre-commit"), []byte(body), 0o755); err != nil {
		t.Fatalf("failed to write recognized hook: %v", err)
	}
}

// strandedHooksRepo builds the GH#5853 fixture in the doctor package: a
// repository whose current hooks directory holds recognizable bd hooks while
// repository-local core.hooksPath still names the absolute hooks directory of
// a former location that no longer exists. It returns the repo root, the
// stale configured value, and the current target.
func strandedHooksRepo(t *testing.T, layout string) (repoDir, stale, current string) {
	t.Helper()
	repoDir = t.TempDir()
	setupGitRepoInDir(t, repoDir)
	switch layout {
	case ".beads-hooks":
		current = filepath.Join(repoDir, ".beads-hooks")
	default:
		current = filepath.Join(repoDir, ".beads", "hooks")
	}
	writeRecognizedHooks(t, current)
	stale = filepath.Join(t.TempDir(), "moved-away", layout)
	setGitConfig(t, repoDir, "core.hooksPath", stale)
	return repoDir, stale, current
}

// TestCheckHooksPath_RelocatedBeadsPath pins AC-2 of the GH#5853 design: the
// doctor Hooks Path check must recognize a stranded former beads hooks
// directory, name the configured and current paths, and recommend the
// explicit repairs — without touching the configuration itself.
func TestCheckHooksPath_RelocatedBeadsPath(t *testing.T) {
	for _, layout := range []string{".beads/hooks", ".beads-hooks"} {
		t.Run(layout, func(t *testing.T) {
			repoDir, stale, current := strandedHooksRepo(t, layout)
			runInDir(t, repoDir, func() {
				check := CheckHooksPath()
				if check.Status != StatusWarning {
					t.Fatalf("expected StatusWarning, got %q: %s", check.Status, check.Message)
				}
				if !strings.Contains(check.Message, "relocated") {
					t.Errorf("expected the warning to identify a relocated beads path, got %q", check.Message)
				}
				for _, want := range []string{stale, current} {
					if !strings.Contains(check.Detail, want) {
						t.Errorf("detail must name %q, got: %s", want, check.Detail)
					}
				}
				for _, want := range []string{"bd doctor --fix", "bd hooks install"} {
					if !strings.Contains(check.Fix, want) {
						t.Errorf("fix must recommend %q, got: %s", want, check.Fix)
					}
				}
			})
			// The check is diagnostic only: the configuration must be
			// byte-identical after running it.
			if got := getGitConfig(t, repoDir, "core.hooksPath"); got != stale {
				t.Errorf("core.hooksPath = %q after CheckHooksPath, want unchanged %q", got, stale)
			}
		})
	}
}

// TestFixHooksPath_RelocatedBeadsPath pins AC-3 of the GH#5853 design:
// 'bd doctor --fix' repoints repository-local core.hooksPath from an eligible
// stranded former beads hooks directory to the validated absolute current
// target — including effective redirected storage — and never creates or
// modifies anything at the stale path.
func TestFixHooksPath_RelocatedBeadsPath(t *testing.T) {
	for _, layout := range []string{".beads/hooks", ".beads-hooks"} {
		t.Run(layout, func(t *testing.T) {
			repoDir, stale, current := strandedHooksRepo(t, layout)
			runInDir(t, repoDir, func() {
				if err := FixHooksPath(); err != nil {
					t.Fatalf("FixHooksPath failed: %v", err)
				}
			})
			if got := getGitConfig(t, repoDir, "core.hooksPath"); got != current {
				t.Errorf("core.hooksPath = %q after FixHooksPath, want repointed to %q", got, current)
			}
			if _, err := os.Stat(stale); !os.IsNotExist(err) {
				t.Errorf("the stale directory %s must not be created by the repair (stat err=%v)", stale, err)
			}
			// A second repair is a no-op: the configuration now points at an
			// existing directory.
			runInDir(t, repoDir, func() {
				if err := FixHooksPath(); err != nil {
					t.Fatalf("second FixHooksPath failed: %v", err)
				}
			})
			if got := getGitConfig(t, repoDir, "core.hooksPath"); got != current {
				t.Errorf("core.hooksPath = %q after a second FixHooksPath, want unchanged %q", got, current)
			}
		})
	}

	t.Run("redirected storage target", func(t *testing.T) {
		repoDir := t.TempDir()
		setupGitRepoInDir(t, repoDir)
		// Effective storage redirected outside the repository (BEADS_DIR with
		// project files); its hooks directory is the validated replacement.
		storage := filepath.Join(t.TempDir(), "external storage")
		if err := os.MkdirAll(storage, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", storage)
		storageHooks := filepath.Join(storage, "hooks")
		writeRecognizedHooks(t, storageHooks)
		stale := filepath.Join(t.TempDir(), "moved-away", ".beads", "hooks")
		setGitConfig(t, repoDir, "core.hooksPath", stale)

		runInDir(t, repoDir, func() {
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getGitConfig(t, repoDir, "core.hooksPath"); got != storageHooks {
			t.Errorf("core.hooksPath = %q after FixHooksPath, want the effective storage target %q", got, storageHooks)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("the stale directory %s must not be created by the repair (stat err=%v)", stale, err)
		}
	})
}

// TestFixHooksPath_RefusesIneligibleRelocation pins AC-5 of the GH#5853
// design: an ineligible missing path — no validated replacement, hooks bd
// does not recognize, a non-repository-local value, a differing effective
// override, or an unsafe destination — is never repointed by the repair.
func TestFixHooksPath_RefusesIneligibleRelocation(t *testing.T) {
	// strandedHooksRepoFixture returns a repo whose core.hooksPath names the
	// given stale value, with the given current target directory state.
	setup := func(t *testing.T) (repoDir, stale, current string) {
		repoDir = t.TempDir()
		setupGitRepoInDir(t, repoDir)
		current = filepath.Join(repoDir, ".beads", "hooks")
		stale = filepath.Join(t.TempDir(), "moved-away", ".beads", "hooks")
		setGitConfig(t, repoDir, "core.hooksPath", stale)
		return repoDir, stale, current
	}

	t.Run("no replacement resolves", func(t *testing.T) {
		repoDir, stale, _ := setup(t)
		runInDir(t, repoDir, func() {
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getGitConfig(t, repoDir, "core.hooksPath"); got != stale {
			t.Errorf("core.hooksPath = %q, want unchanged %q: no validated replacement means no repair", got, stale)
		}
	})

	t.Run("replacement holds no recognizable hooks", func(t *testing.T) {
		repoDir, stale, current := setup(t)
		if err := os.MkdirAll(current, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(current, "pre-commit"), []byte("#!/bin/sh\necho not bd\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		runInDir(t, repoDir, func() {
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getGitConfig(t, repoDir, "core.hooksPath"); got != stale {
			t.Errorf("core.hooksPath = %q, want unchanged %q: arbitrary content is not evidence of relocation", got, stale)
		}
	})

	t.Run("global-only value", func(t *testing.T) {
		repoDir, stale, current := setup(t)
		writeRecognizedHooks(t, current)
		globalConfig := filepath.Join(t.TempDir(), "gitconfig")
		t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
		unsetGitConfig(t, repoDir, "core.hooksPath")
		gc := gitCommand("config", "--global", "core.hooksPath", stale)
		gc.Dir = repoDir
		if out, err := gc.CombinedOutput(); err != nil {
			t.Fatalf("git config --global failed: %v (%s)", err, out)
		}
		runInDir(t, repoDir, func() {
			st := InspectHooksPath()
			if st.State != HooksPathMissing {
				t.Fatalf("precondition: a global-only value must classify as Missing, got %+v", st)
			}
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		got := getGitConfig(t, repoDir, "core.hooksPath")
		if got != stale {
			t.Errorf("global core.hooksPath = %q, want unchanged %q: bd never rewrites global config", got, stale)
		}
	})

	t.Run("differing command override", func(t *testing.T) {
		repoDir, stale, current := setup(t)
		writeRecognizedHooks(t, current)
		otherStale := filepath.Join(t.TempDir(), "other-location", ".beads", "hooks")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
		t.Setenv("GIT_CONFIG_VALUE_0", otherStale)
		runInDir(t, repoDir, func() {
			st := InspectHooksPath()
			if st.State != HooksPathMissing {
				t.Fatalf("precondition: an effective command override must classify as Missing, got %+v", st)
			}
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getLocalGitConfig(t, repoDir, "core.hooksPath"); got != stale {
			t.Errorf("local core.hooksPath = %q, want unchanged %q: bd never rewrites through a command override", got, stale)
		}
	})

	t.Run("changed config between inspection and repair", func(t *testing.T) {
		repoDir, _, current := setup(t)
		writeRecognizedHooks(t, current)
		runInDir(t, repoDir, func() {
			check := CheckHooksPath()
			if check.Status != StatusWarning {
				t.Fatalf("precondition: expected the relocated warning, got %+v", check)
			}
			// The user repairs the configuration before the fix pass runs.
			setGitConfig(t, repoDir, "core.hooksPath", current)
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getGitConfig(t, repoDir, "core.hooksPath"); got != current {
			t.Errorf("core.hooksPath = %q, want %q: the repair must re-read and respect the changed configuration", got, current)
		}
	})

	t.Run("stale path exists after all", func(t *testing.T) {
		repoDir, stale, current := setup(t)
		writeRecognizedHooks(t, current)
		writeRecognizedHooks(t, stale) // the "former" location still exists
		runInDir(t, repoDir, func() {
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getGitConfig(t, repoDir, "core.hooksPath"); got != stale {
			t.Errorf("core.hooksPath = %q, want unchanged %q: an existing destination is not a relocation", got, stale)
		}
	})

	t.Run("replacement is a symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires elevated privileges on Windows")
		}
		repoDir, stale, current := setup(t)
		realHooks := filepath.Join(t.TempDir(), "real-hooks")
		writeRecognizedHooks(t, realHooks)
		if err := os.Symlink(realHooks, current); err != nil {
			t.Fatalf("symlink %s -> %s: %v", current, realHooks, err)
		}
		runInDir(t, repoDir, func() {
			if err := FixHooksPath(); err != nil {
				t.Fatalf("FixHooksPath failed: %v", err)
			}
		})
		if got := getGitConfig(t, repoDir, "core.hooksPath"); got != stale {
			t.Errorf("core.hooksPath = %q, want unchanged %q: a symlinked destination is never a repair target", got, stale)
		}
	})
}

// getLocalGitConfig reads a repository-local git config value from dir.
// Returns "" when the key is absent locally.
func getLocalGitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := gitCommand("config", "--local", "--get", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestFixHooksPath_ManagedPathDeletedInPlaceStillUnsets pins AC-6 of the
// GH#5853 design: the GH#4440 contract is unchanged by the relocation work.
// A current beads-managed hooks directory deleted in place is unset — even
// when a redirected storage directory elsewhere still holds valid hooks,
// which the relocation arm must not treat as a repoint target.
func TestFixHooksPath_ManagedPathDeletedInPlaceStillUnsets(t *testing.T) {
	repoDir := t.TempDir()
	setupGitRepoInDir(t, repoDir)
	// The deleted-in-place current managed path: repository-local, absolute.
	managed := filepath.Join(repoDir, ".beads", "hooks")
	setGitConfig(t, repoDir, "core.hooksPath", managed)
	if err := os.MkdirAll(managed, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(managed); err != nil {
		t.Fatal(err)
	}
	// Redirected storage elsewhere holds valid hooks: a tempting repoint
	// target the repair must not take — the value is unset instead.
	storage := filepath.Join(t.TempDir(), "external storage")
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", storage)
	writeRecognizedHooks(t, filepath.Join(storage, "hooks"))

	runInDir(t, repoDir, func() {
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, repoDir, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath = %q after FixHooksPath, want unset: the GH#4440 in-place deletion contract is unchanged", got)
	}
}

// TestFixHooksPath_RelocatedLinkedWorktree pins AC-7 of the GH#5853 design:
// repairing an eligible stranded core.hooksPath from a linked worktree
// repoints it to the main repository's hooks directory (or its effective
// storage), and git actually reaches the repaired directory from both the
// linked worktree and the main worktree — verified by running a harmless
// sentinel hook through a real commit, not by file presence alone.
func TestFixHooksPath_RelocatedLinkedWorktree(t *testing.T) {
	repoDir := t.TempDir()
	setupGitRepoInDir(t, repoDir)
	// A linked worktree needs at least one commit to exist at all.
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "initial"}} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, strings.TrimSpace(string(out)))
		}
	}
	worktree := filepath.Join(t.TempDir(), "linked-wt")
	wtCmd := gitCommand("worktree", "add", worktree)
	wtCmd.Dir = repoDir
	if out, err := wtCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	current := filepath.Join(repoDir, ".beads", "hooks")
	writeRecognizedHooks(t, current)
	stale := filepath.Join(t.TempDir(), "moved-away", ".beads", "hooks")
	setGitConfig(t, repoDir, "core.hooksPath", stale)

	runInDir(t, worktree, func() {
		check := CheckHooksPath()
		if check.Status != StatusWarning {
			t.Fatalf("expected the relocated warning from the linked worktree, got %+v", check)
		}
		if !strings.Contains(check.Detail, current) {
			t.Errorf("detail must name the current target %q from a linked worktree, got: %s", current, check.Detail)
		}
		if err := FixHooksPath(); err != nil {
			t.Fatalf("FixHooksPath failed: %v", err)
		}
	})

	if got := getGitConfig(t, worktree, "core.hooksPath"); got != current {
		t.Fatalf("core.hooksPath = %q after repair from the worktree, want the main repository's %q", got, current)
	}

	// End-to-end: a harmless sentinel hook written into the repaired
	// directory must fire from a real commit in both worktrees. The sentinel
	// keeps the bd-shim marker so it remains recognized bd hook content.
	for _, tc := range []struct {
		name, dir string
	}{
		{"from_linked_worktree", worktree},
		{"from_main_worktree", repoDir},
	} {
		marker := filepath.Join(t.TempDir(), "marker-"+tc.name)
		hook := "#!/bin/sh\n# bd-shim sentinel\ntouch " + marker + "\n"
		for _, name := range []string{"pre-commit", "prepare-commit-msg"} {
			if err := os.WriteFile(filepath.Join(current, name), []byte(hook), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cmd := gitCommand("commit", "--allow-empty", "-m", "sentinel "+tc.name)
		cmd.Dir = tc.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %s failed: %v (%s)", tc.name, err, strings.TrimSpace(string(out)))
		}
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("the sentinel hook did not run for a commit %s: git is not using the repaired hooks directory (stat: %v)", tc.name, err)
		}
	}
}
