package beads

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
)

// GH#4402: outside a repository, the ancestor walks had no boundary left
// above the home directory, so a user-global ~/.beads holding a legacy
// config.yaml or a database was adopted as a workspace and writes landed in
// the global database with no warning. These tests pin the skip the design
// adds (discovery passes over <home>/.beads unless the bounding repository
// root is the home directory itself) and the opt-ins that keep working. The
// home directory is the account's (userHomeDir), which the fixtures stub.

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

// addUserGlobalLinkedWorktree makes mainRepo a git repository with one
// commit and adds a linked worktree of it at worktree.
func addUserGlobalLinkedWorktree(t *testing.T, mainRepo, worktree string) {
	t.Helper()
	if err := os.MkdirAll(mainRepo, 0o750); err != nil {
		t.Fatal(err)
	}
	initUserGlobalGitRepo(t, mainRepo)
	for _, args := range [][]string{
		{"commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", worktree},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// enterUserGlobalFixture points the account home the skip reads
// (userHomeDir), HOME and USERPROFILE at home, scrubs the explicit workspace
// selectors, and moves the test into dir, resetting the package's and git's
// cached contexts on entry and exit so discovery runs against the fixture
// rather than the suite HOME (testmain_test.go) or a previous test's
// position. Follows the no_repo_root_test.go convention; no t.Parallel
// (chdir, and the userHomeDir stub).
func enterUserGlobalFixture(t *testing.T, home, dir string) {
	t.Helper()
	t.Cleanup(SetUserHomeDirForTesting(home))
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

// TestImplicitDiscoverySkipsUserGlobalBeadsDir covers AC-1: with the home
// directory not a repository root and <home>/.beads a real embedded
// workspace, every implicit discovery primitive standing under the home
// directory finds no workspace instead of adopting the user-global one.
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

	// The `bd -C <dir>` shape: a repository under the home directory with no
	// .beads of its own must not resolve to the user-global directory either.
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

	// The skip keys on the account home, not on $HOME, so an empty HOME
	// (env -u HOME, some sudo setups) does not turn it off while the walk
	// still climbs into the account's home directory.
	t.Run("HOME unset", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\": an empty HOME must not adopt the account home's %q", got, beadsDir)
		}
		if got := FindDatabasePath(); got != "" {
			t.Errorf("FindDatabasePath() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
	})

	// The home directory spelled through a symlink while the cwd sits under
	// the real path: the skip compares canonical forms, so it still applies.
	t.Run("home via symlink", func(t *testing.T) {
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
// to name: a workspace-selection walk in this process passed over
// <home>/.beads, and the directory holds a workspace (a redirect,
// metadata.json or a database).
func TestSkippedUserGlobalBeadsDirNote(t *testing.T) {
	t.Run("real workspace", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() before any walk = %q, want \"\": it reports a skip that happened", got)
		}
		if got := FindBeadsDir(); got != "" {
			t.Fatalf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); !utils.PathsEqual(got, beadsDir) {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
		ResetCaches()
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() after ResetCaches = %q, want \"\"", got)
		}
	})

	// The no-database refusal follows FindDatabasePath, whose walk
	// (findDatabaseInTree) records the skip on its own.
	t.Run("recorded by FindDatabasePath", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		if got := FindDatabasePath(); got != "" {
			t.Fatalf("FindDatabasePath() = %q, want \"\"", got)
		}
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
		if got := FindBeadsDir(); got != "" {
			t.Fatalf("FindBeadsDir() = %q, want \"\"", got)
		}
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
		if got := FindBeadsDir(); got != "" {
			t.Fatalf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" when <home>/.beads is absent", got)
		}
	})

	t.Run("not reached from the cwd", func(t *testing.T) {
		// A directory outside the home directory: no walk from the cwd
		// reaches home, so there is nothing to report even though
		// <home>/.beads exists.
		home, _ := newUserGlobalHome(t)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o750); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, elsewhere)
		if got := FindBeadsDir(); got != "" {
			t.Fatalf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" when the walk cannot reach home", got)
		}
	})

	// A git repository under the home directory with no .beads of its own:
	// workspace selection stops at the repository root and never reaches the
	// home directory, so there is no skip to name. The redirect probe, whose
	// walk the repository root does not bound, does pass over <home>/.beads
	// and must not record it.
	t.Run("git repository under home", func(t *testing.T) {
		home, _ := newUserGlobalHome(t)
		proj := filepath.Join(home, "proj")
		if err := os.MkdirAll(proj, 0o750); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, proj)
		enterUserGlobalFixture(t, home, proj)
		_ = GetRedirectInfo()
		_ = GetDiscoveredRedirectInfo()
		if got := FindBeadsDir(); got != "" {
			t.Fatalf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Fatalf("FindDatabasePath() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\": selection inside %s never reached the home directory", got, proj)
		}
	})

	// An explicit BEADS_DIR naming a missing directory: discovery runs no
	// walk, so a mistyped selection is not steered into the global
	// directory.
	t.Run("BEADS_DIR names a missing directory", func(t *testing.T) {
		home, _ := newUserGlobalHome(t)
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		t.Setenv("BEADS_DIR", filepath.Join(home, "work", ".beads"))
		if got := FindBeadsDir(); got != "" {
			t.Fatalf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Fatalf("FindDatabasePath() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\": BEADS_DIR runs no walk", got)
		}
	})
}

// TestRepointedHomeKeepsItsWorkspace pins why the skip reads the account
// home rather than $HOME: test harnesses and sandboxes point HOME at a
// scratch directory outside any repository and run bd init or bd connect
// there. That .beads is their workspace, not the user's global one, so
// discovery keeps adopting it.
func TestRepointedHomeKeepsItsWorkspace(t *testing.T) {
	scratch, beadsDir := newUserGlobalHome(t)
	accountHome := filepath.Join(t.TempDir(), "account")
	if err := os.MkdirAll(accountHome, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, from := range map[string]string{
		"the scratch home itself": scratch,
		"a subdirectory":          filepath.Join(scratch, "notes"),
	} {
		t.Run("from "+name, func(t *testing.T) {
			enterUserGlobalFixture(t, accountHome, from)
			t.Setenv("HOME", scratch)
			t.Setenv("USERPROFILE", scratch)
			if got := FindBeadsDir(); !utils.PathsEqual(got, beadsDir) {
				t.Errorf("FindBeadsDir() = %q, want the scratch workspace %q", got, beadsDir)
			}
			if got := SkippedUserGlobalBeadsDir(); got != "" {
				t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\"", got)
			}
			if SkipsUserGlobalBeadsAt(scratch) {
				t.Errorf("SkipsUserGlobalBeadsAt(%q) = true, want false: HOME alone does not make a directory the account home", scratch)
			}
		})
	}
}

// TestDefaultUserHomeDirPrefersAccountDatabase pins the resolver the skip
// uses: the OS account database's home, whatever HOME says, so neither a
// repointed nor an empty HOME moves it.
func TestDefaultUserHomeDirPrefersAccountDatabase(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		t.Skipf("no account database home for the current user (%v)", err)
	}
	for name, home := range map[string]string{
		"HOME repointed": t.TempDir(),
		"HOME empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			if got := defaultUserHomeDir(); got != u.HomeDir {
				t.Errorf("defaultUserHomeDir() = %q, want the account home %q", got, u.HomeDir)
			}
		})
	}
}

// TestSkipsUserGlobalBeadsAt pins the question bd import asks before it falls
// back to the working directory's own .beads.
func TestSkipsUserGlobalBeadsAt(t *testing.T) {
	home, _ := newUserGlobalHome(t)

	t.Run("home outside a repository", func(t *testing.T) {
		enterUserGlobalFixture(t, home, home)
		if !SkipsUserGlobalBeadsAt(home) {
			t.Errorf("SkipsUserGlobalBeadsAt(%q) = false, want true", home)
		}
		if notes := filepath.Join(home, "notes"); SkipsUserGlobalBeadsAt(notes) {
			t.Errorf("SkipsUserGlobalBeadsAt(%q) = true, want false: only the home directory's own .beads is user-global", notes)
		}
	})

	t.Run("home is the repository root", func(t *testing.T) {
		dotfiles, _ := newUserGlobalHome(t)
		initUserGlobalGitRepo(t, dotfiles)
		enterUserGlobalFixture(t, dotfiles, dotfiles)
		if SkipsUserGlobalBeadsAt(dotfiles) {
			t.Errorf("SkipsUserGlobalBeadsAt(%q) = true, want false in a home-rooted repository", dotfiles)
		}
	})
}

// TestUserGlobalSkipCoversEveryWalk pins the skip in the walks the other
// tests do not reach: FindAllDatabases (it recognizes only dolt/ or *.db),
// the redirect probe (findBeadsDirUpward), and FindBeadsDir's extended walk
// from a linked worktree under the home directory whose main checkout lives
// elsewhere, which climbs past the home directory on its way to that
// checkout.
func TestUserGlobalSkipCoversEveryWalk(t *testing.T) {
	t.Run("FindAllDatabases", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o750); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, home, filepath.Join(home, "notes"))
		for _, db := range FindAllDatabases() {
			if utils.PathsEqual(db.BeadsDir, beadsDir) {
				t.Errorf("FindAllDatabases() reported the user-global workspace %q", db.BeadsDir)
			}
		}
	})

	t.Run("redirect probe", func(t *testing.T) {
		home, beadsDir := newUserGlobalHome(t)
		other := filepath.Join(t.TempDir(), "other", ".beads")
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
		for name, info := range map[string]RedirectInfo{
			"GetRedirectInfo":           GetRedirectInfo(),
			"GetDiscoveredRedirectInfo": GetDiscoveredRedirectInfo(),
			"GetRedirectInfoFrom":       GetRedirectInfoFrom(filepath.Join(home, "notes")),
		} {
			if info.IsRedirected || utils.PathsEqual(info.LocalDir, beadsDir) {
				t.Errorf("%s() = %+v, want no redirect: the probe must pass over %s", name, info, beadsDir)
			}
		}
	})

	// The extended walk climbs from the worktree toward its main checkout,
	// which is not an ancestor, so it meets the home directory. It passes
	// over <home>/.beads, and records nothing: the worktree is inside a
	// repository, whose own .beads is the remedy there, not
	// BEADS_DIR=~/.beads, and bd import there sets that .beads up.
	for _, layout := range []struct {
		name     string
		mainRepo func(home string) string
	}{
		{"main checkout outside home", func(string) string { return filepath.Join(t.TempDir(), "main") }},
		{"main checkout beside it under home", func(home string) string { return filepath.Join(home, "proj") }},
	} {
		t.Run("linked worktree under home, "+layout.name, func(t *testing.T) {
			home, beadsDir := newUserGlobalHome(t)
			worktree := filepath.Join(home, "feature")
			addUserGlobalLinkedWorktree(t, layout.mainRepo(home), worktree)
			enterUserGlobalFixture(t, home, worktree)
			if !git.IsWorktree() {
				t.Fatalf("precondition: %s is not a linked worktree", worktree)
			}
			if got := FindBeadsDir(); got != "" {
				t.Errorf("FindBeadsDir() = %q, want \"\": the extended walk must pass over %s", got, beadsDir)
			}
			if got := FindDatabasePath(); got != "" {
				t.Errorf("FindDatabasePath() = %q, want \"\"", got)
			}
			if got := FindBeadsDirFrom(worktree); got != "" {
				t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", worktree, got)
			}
			if got := SkippedUserGlobalBeadsDir(); got != "" {
				t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\": the worktree is inside a repository", got)
			}
		})
	}
}

// TestUserGlobalSkipEndsTheWalkAtHome pins that the walks end at the home
// directory they pass over (passOverUserGlobalBeads). <home>/.beads used to
// end every walk outside a repository, so a .beads above the home directory
// was never reached; passing over it must not adopt one there in its place.
// Only a repository root above the home directory takes a walk past it, up to
// that root and no further.
func TestUserGlobalSkipEndsTheWalkAtHome(t *testing.T) {
	// <sandbox>/root/home is the account home, holding the user-global
	// .beads and notes/. <sandbox>/root/.beads lies above it and
	// <sandbox>/.beads above that, where only a walk that climbs past the
	// home directory finds them. Each holds a database (dolt/), so every walk,
	// FindAllDatabases included, would take it.
	type fixture struct{ sandbox, root, home, notes string }
	setup := func(t *testing.T) fixture {
		t.Helper()
		sandbox, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		f := fixture{sandbox: sandbox, root: filepath.Join(sandbox, "root")}
		f.home = filepath.Join(f.root, "home")
		f.notes = filepath.Join(f.home, "notes")
		for _, beadsDir := range []string{
			filepath.Join(sandbox, ".beads"),
			filepath.Join(f.root, ".beads"),
			filepath.Join(f.home, ".beads"),
		} {
			if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := configfile.DefaultConfig().Save(beadsDir); err != nil {
				t.Fatalf("writing metadata.json: %v", err)
			}
		}
		if err := os.MkdirAll(f.notes, 0o750); err != nil {
			t.Fatal(err)
		}
		// Keep the temp-root ceiling clear of these walks; it is not what
		// this pins.
		t.Setenv("TMPDIR", filepath.Join(sandbox, "tmp"))
		return f
	}
	assertNoWorkspace := func(t *testing.T, from, repoRoot string) {
		t.Helper()
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Errorf("FindDatabasePath() = %q, want \"\"", got)
		}
		if got := FindBeadsDirFrom(from); got != "" {
			t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", from, got)
		}
		if got := FindAllDatabases(); len(got) != 0 {
			t.Errorf("FindAllDatabases() = %+v, want none", got)
		}
		if got := findBeadsDirUpward(from, repoRoot); got != "" {
			t.Errorf("findBeadsDirUpward(%q, %q) = %q, want \"\"", from, repoRoot, got)
		}
	}

	for _, name := range []string{"notes", "home itself"} {
		t.Run("outside a repository, from "+name, func(t *testing.T) {
			f := setup(t)
			from := f.notes
			if name == "home itself" {
				from = f.home
			}
			enterUserGlobalFixture(t, f.home, from)
			assertNoWorkspace(t, from, "")
			if got, want := SkippedUserGlobalBeadsDir(), filepath.Join(f.home, ".beads"); !utils.PathsEqual(got, want) {
				t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, want)
			}
		})
	}

	t.Run("outside a repository, no user-global directory", func(t *testing.T) {
		// The walk ends at the home directory whether or not it holds a
		// .beads: nothing above it is a workspace of a directory under it.
		f := setup(t)
		if err := os.RemoveAll(filepath.Join(f.home, ".beads")); err != nil {
			t.Fatal(err)
		}
		enterUserGlobalFixture(t, f.home, f.notes)
		assertNoWorkspace(t, f.notes, "")
	})

	t.Run("repository under home", func(t *testing.T) {
		// Its root does not lie above the home directory, so the walks it
		// does not bound itself (FindBeadsDirFrom, the redirect probe) end
		// at the home directory, as outside a repository, and record
		// nothing: the remedy inside a repository is its own .beads.
		f := setup(t)
		proj := filepath.Join(f.home, "proj")
		if err := os.MkdirAll(proj, 0o750); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, proj)
		enterUserGlobalFixture(t, f.home, proj)
		assertNoWorkspace(t, proj, proj)
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" inside the repository", got)
		}
	})

	t.Run("linked worktree beside its main checkout under home", func(t *testing.T) {
		// FindBeadsDir's extended walk climbs from <home>/feature toward
		// <home>/proj, which is not an ancestor, and ends at the home
		// directory: neither root lies above it.
		f := setup(t)
		worktree := filepath.Join(f.home, "feature")
		addUserGlobalLinkedWorktree(t, filepath.Join(f.home, "proj"), worktree)
		enterUserGlobalFixture(t, f.home, worktree)
		if !git.IsWorktree() {
			t.Fatalf("precondition: %s is not a linked worktree", worktree)
		}
		assertNoWorkspace(t, worktree, worktree)
	})

	t.Run("repository root above home", func(t *testing.T) {
		// The enclosing repository's own .beads is reached past the home
		// directory; the user-global one is still passed over.
		f := setup(t)
		initUserGlobalGitRepo(t, f.root)
		enterUserGlobalFixture(t, f.home, f.notes)
		want := filepath.Join(f.root, ".beads")
		if got := FindBeadsDir(); !utils.PathsEqual(got, want) {
			t.Errorf("FindBeadsDir() = %q, want the enclosing repository's %q", got, want)
		}
		if got := FindDatabasePath(); !strings.HasPrefix(got, want+string(filepath.Separator)) {
			t.Errorf("FindDatabasePath() = %q, want a database in %q", got, want)
		}
		if got := FindBeadsDirFrom(f.notes); !utils.PathsEqual(got, want) {
			t.Errorf("FindBeadsDirFrom(%q) = %q, want %q", f.notes, got, want)
		}
	})

	t.Run("repository root above home, nothing above the root", func(t *testing.T) {
		// Past the home directory a walk ends at the repository root:
		// FindBeadsDirFrom and the redirect probe, which the root does not
		// otherwise bound, do not reach <sandbox>/.beads above it.
		f := setup(t)
		if err := os.RemoveAll(filepath.Join(f.root, ".beads")); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, f.root)
		enterUserGlobalFixture(t, f.home, f.notes)
		assertNoWorkspace(t, f.notes, f.root)
		// Selection from notes/ is inside the repository: nothing to name.
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\" inside the repository", got)
		}
	})

	t.Run("repository root above home, from home itself", func(t *testing.T) {
		// Standing in the home directory, selection passed over its .beads,
		// so the refusal names it even inside the enclosing repository.
		f := setup(t)
		if err := os.RemoveAll(filepath.Join(f.root, ".beads")); err != nil {
			t.Fatal(err)
		}
		initUserGlobalGitRepo(t, f.root)
		enterUserGlobalFixture(t, f.home, f.home)
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got, want := SkippedUserGlobalBeadsDir(), filepath.Join(f.home, ".beads"); !utils.PathsEqual(got, want) {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, want)
		}
	})
}
