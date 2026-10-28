package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
)

// GH#4402: outside a repository, the ancestor walks had no boundary left
// above $HOME, so a user-global ~/.beads holding a legacy config.yaml or a
// database was adopted as a workspace and writes landed in the global
// database with no warning. These tests pin the skip the design adds
// (discovery passes over <home>/.beads unless the bounding repository root
// is $HOME itself) and the opt-ins that keep working.

// newUserGlobalHome builds <tmp>/home holding .beads with an embedded
// workspace (metadata.json from configfile.DefaultConfig plus an
// embeddeddolt/ database directory) and a notes/ subdirectory outside any
// repository — the AC-1 layout.
func newUserGlobalHome(t *testing.T) (home, beadsDir string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	beadsDir = filepath.Join(home, ".beads")
	for _, dir := range []string{
		beadsDir,
		filepath.Join(beadsDir, "embeddeddolt"),
		filepath.Join(home, "notes"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := configfile.DefaultConfig().Save(beadsDir); err != nil {
		t.Fatalf("writing metadata.json: %v", err)
	}
	return home, beadsDir
}

// initUserGlobalGitRepo initializes a git repository with a test identity
// at dir, so tests can shape the repository root that bounds a walk.
func initUserGlobalGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
}

// enterUserGlobalFixture points HOME and USERPROFILE at home, scrubs the
// explicit workspace selectors, and moves the test into dir, resetting the
// package's and git's cached contexts on entry and exit so discovery runs
// against the fixture rather than the suite HOME (testmain_test.go) or a
// previous test's position. Follows the no_repo_root_test.go convention;
// no t.Parallel (chdir).
func enterUserGlobalFixture(t *testing.T, home, dir string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
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

// TestImplicitDiscoverySkipsUserGlobalBeadsDir covers AC-1: with $HOME not a
// repository root and $HOME/.beads a real embedded workspace, every implicit
// discovery primitive standing under $HOME finds no workspace instead of
// adopting the user-global one.
func TestImplicitDiscoverySkipsUserGlobalBeadsDir(t *testing.T) {
	home, beadsDir := newUserGlobalHome(t)

	for name, from := range map[string]string{
		"notes under home": filepath.Join(home, "notes"),
		"home itself":      home,
	} {
		t.Run("from "+name, func(t *testing.T) {
			enterUserGlobalFixture(t, home, from)
			if got := FindBeadsDir(); got != "" {
				t.Errorf("FindBeadsDir() = %q, want \"\"", got)
			}
			if got := FindDatabasePath(); got != "" {
				t.Errorf("FindDatabasePath() = %q, want \"\"", got)
			}
			if got := FindBeadsDirFrom(from); got != "" {
				t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", from, got)
			}
			for _, db := range FindAllDatabases() {
				if utils.PathsEqual(db.BeadsDir, beadsDir) {
					t.Errorf("FindAllDatabases() reported the user-global workspace %q", db.BeadsDir)
				}
			}
		})
	}

	// The `bd -C <dir>` shape: a repository under $HOME with no .beads of
	// its own must not resolve to the user-global directory either.
	t.Run("bd -C into a repository under home", func(t *testing.T) {
		repo := filepath.Join(home, "repo")
		if err := os.MkdirAll(repo, 0o750); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, repo)
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := FindBeadsDirFrom(repo); got != "" {
			t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", repo, got)
		}
	})

	// The reporter's literal layout: ~/.beads/beads.db with no metadata.json.
	// hasBeadsProjectFiles accepts any *.db, so without the skip this is
	// adopted too.
	t.Run("reporter layout: bare beads.db", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		if err := os.RemoveAll(filepath.Join(beadsDir, "embeddeddolt")); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(beadsDir, "metadata.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Errorf("FindDatabasePath() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
	})

	// No home directory means no global location to protect: discovery keeps
	// its old answer.
	t.Run("HOME unset", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		t.Setenv("BEADS_DIR", "")
		t.Setenv("BEADS_DB", "")
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")
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
		if err := os.Chdir(filepath.Join(home, "notes")); err != nil {
			t.Fatal(err)
		}
		if got := FindBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("FindBeadsDir() = %q, want the user-global %q", got, beadsDir)
		}
	})

	// HOME spelled through a symlink while the cwd sits under the real path:
	// the skip compares canonical forms, so it still applies.
	t.Run("HOME via symlink", func(t *testing.T) {
		tmp := t.TempDir()
		home := filepath.Join(tmp, "realhome")
		beadsDir := filepath.Join(home, ".beads")
		for _, dir := range []string{
			beadsDir,
			filepath.Join(beadsDir, "embeddeddolt"),
			filepath.Join(home, "notes"),
		} {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
		}
		if err := configfile.DefaultConfig().Save(beadsDir); err != nil {
			t.Fatalf("writing metadata.json: %v", err)
		}
		link := filepath.Join(tmp, "linkhome")
		if err := os.Symlink(home, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		enterUserGlobalFixture(t, link, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
	})

	// The skip is positional: it passes over only the home directory's own
	// candidate, so a non-repository directory's own .beads and a project's
	// .beads under $HOME still resolve.
	t.Run("own beads dirs still resolve", func(t *testing.T) {
		home, _ := newUserGlobalHome(t)

		notesBeads := filepath.Join(home, "notes", ".beads")
		if err := os.MkdirAll(notesBeads, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(notesBeads, "beads.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); !utils.PathsEqual(got, notesBeads) {
			t.Errorf("FindBeadsDir() = %q, want the notes-local %q", got, notesBeads)
		}

		proj := filepath.Join(home, "proj")
		if err := os.MkdirAll(proj, 0o750); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, proj)
		projBeads := filepath.Join(proj, ".beads")
		if err := os.MkdirAll(projBeads, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := configfile.DefaultConfig().Save(projBeads); err != nil {
			t.Fatalf("writing metadata.json: %v", err)
		}
		enterUserGlobalFixture(t, home, proj)
		if got := FindBeadsDir(); !utils.PathsEqual(got, projBeads) {
			t.Errorf("FindBeadsDir() = %q, want the project's own %q", got, projBeads)
		}
	})

	// A project's .beads/redirect pointing at <home>/.beads is an explicit
	// on-disk choice and keeps working.
	t.Run("project redirect to home still resolves", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		proj := filepath.Join(home, "proj")
		if err := os.MkdirAll(proj, 0o750); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, proj)
		projBeads := filepath.Join(proj, ".beads")
		if err := os.MkdirAll(projBeads, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projBeads, RedirectFileName), []byte(beadsDir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, proj)
		if got := FindBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("FindBeadsDir() = %q, want the redirect target %q", got, beadsDir)
		}
	})

	// A redirect inside <home>/.beads is skipped together with its directory:
	// the walk never reads it.
	t.Run("home redirect skipped with its directory", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		other := filepath.Join(home, "other", ".beads")
		if err := os.MkdirAll(other, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := configfile.DefaultConfig().Save(other); err != nil {
			t.Fatalf("writing metadata.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, RedirectFileName), []byte(other+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
	})
}

// TestImplicitDiscoveryKeepsHomeRepoBeadsDir covers AC-2: when $HOME itself
// is a repository root (a dotfiles repository), its own ~/.beads stays
// discoverable from $HOME and from the repository's subdirectories. This
// passes today and fails if the home exemption is dropped.
func TestImplicitDiscoveryKeepsHomeRepoBeadsDir(t *testing.T) {
	home, beadsDir := newUserGlobalHome(t)
	initUserGlobalGitRepo(t, home)
	embedded := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(filepath.Join(home, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	for name, from := range map[string]string{
		"home itself":    home,
		"a subdirectory": filepath.Join(home, "sub"),
	} {
		t.Run("from "+name, func(t *testing.T) {
			enterUserGlobalFixture(t, home, from)
			if got := FindBeadsDir(); !utils.PathsEqual(got, beadsDir) {
				t.Errorf("FindBeadsDir() = %q, want %q", got, beadsDir)
			}
			if got := FindDatabasePath(); !utils.PathsEqual(got, embedded) {
				t.Errorf("FindDatabasePath() = %q, want %q", got, embedded)
			}
			if got := SkippedUserGlobalBeadsDir(); got != "" {
				t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" inside the home-rooted repository", got)
			}
		})
	}
}

// TestExplicitBeadsDirSelectsUserGlobalBeadsDir covers AC-3: BEADS_DIR and
// BEADS_DB name the user-global directory explicitly and keep working
// exactly as today.
func TestExplicitBeadsDirSelectsUserGlobalBeadsDir(t *testing.T) {
	home, beadsDir := newUserGlobalHome(t)
	embedded := filepath.Join(beadsDir, "embeddeddolt")
	notes := filepath.Join(home, "notes")

	t.Run("BEADS_DIR", func(t *testing.T) {
		enterUserGlobalFixture(t, home, notes)
		t.Setenv("BEADS_DIR", beadsDir)
		ResetCaches()
		git.ResetCaches()
		if got := FindBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("FindBeadsDir() = %q, want %q", got, beadsDir)
		}
		if got := FindDatabasePath(); !utils.PathsEqual(got, embedded) {
			t.Errorf("FindDatabasePath() = %q, want %q", got, embedded)
		}
	})

	t.Run("BEADS_DB in directory form", func(t *testing.T) {
		enterUserGlobalFixture(t, home, notes)
		t.Setenv("BEADS_DB", beadsDir)
		ResetCaches()
		git.ResetCaches()
		if got := FindDatabasePath(); !utils.PathsEqual(got, embedded) {
			t.Errorf("FindDatabasePath() = %q, want %q", got, embedded)
		}
	})

	// A store reachable only by being named is a redirect (context.go:
	// beadsDirIsExternalToCWD), so the skip keeps that classification
	// consistent for a named user-global directory.
	t.Run("named user-global directory is a redirect", func(t *testing.T) {
		enterUserGlobalFixture(t, home, notes)
		t.Setenv("BEADS_DIR", beadsDir)
		ResetCaches()
		git.ResetCaches()
		rc, err := GetRepoContextAllowingNoGit()
		if err != nil {
			t.Fatalf("GetRepoContextAllowingNoGit: %v", err)
		}
		if !rc.IsRedirected {
			t.Errorf("IsRedirected = false, want true: discovery standing in %s finds no workspace, so %q can only have been named", notes, beadsDir)
		}
	})
}

// TestSkippedUserGlobalBeadsDirNote pins when the CLI note has a directory
// to name: the user-global workspace is real (a redirect, metadata.json or
// a database), and an ancestor walk from the cwd would have reached it.
func TestSkippedUserGlobalBeadsDirNote(t *testing.T) {
	t.Run("real workspace", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := SkippedUserGlobalBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
	})

	t.Run("config.yaml only is user configuration", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		if err := os.RemoveAll(filepath.Join(beadsDir, "embeddeddolt")); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(beadsDir, "metadata.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("# user-level config\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" for a config.yaml-only home directory", got)
		}
	})

	t.Run("absent", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		if err := os.RemoveAll(beadsDir); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" when <home>/.beads is absent", got)
		}
	})

	t.Run("not reached from the cwd", func(t *testing.T) {
		// A workspace outside $HOME: no walk from the cwd reaches home, so
		// there is nothing to report even though <home>/.beads exists.
		home, _ := newUserGlobalHome(t)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o750); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, elsewhere)
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" when the walk cannot reach home", got)
		}
	})
}
