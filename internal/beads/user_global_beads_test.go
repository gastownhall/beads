package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
)

// newUserGlobalHome builds <tmp>/home with an empty .beads and a
// non-repository notes/ directory, points HOME and USERPROFILE at it and
// clears BEADS_DIR and BEADS_DB. It returns the canonical home path.
func newUserGlobalHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(resolveSymlinks(t.TempDir()), "home")
	for _, dir := range []string{filepath.Join(home, ".beads"), filepath.Join(home, "notes")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	return home
}

// writeEmbeddedWorkspace makes beadsDir an embedded workspace: metadata.json
// plus an embeddeddolt/ database directory.
func writeEmbeddedWorkspace(t *testing.T, beadsDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := configfile.DefaultConfig().Save(beadsDir); err != nil {
		t.Fatal(err)
	}
}

// userGlobalBeadsHome is newUserGlobalHome with <home>/.beads holding an
// embedded workspace (the AC-1 layout).
func userGlobalBeadsHome(t *testing.T) string {
	t.Helper()
	home := newUserGlobalHome(t)
	writeEmbeddedWorkspace(t, filepath.Join(home, ".beads"))
	return home
}

// chdirForDiscovery moves the process into dir for one discovery case,
// resetting the discovery and git caches on the way in and out.
func chdirForDiscovery(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(orig)
		ResetCaches()
		git.ResetCaches()
	})
	ResetCaches()
	git.ResetCaches()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

func gitInitForDiscovery(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

func assertNoUserGlobalDatabase(t *testing.T, beadsDir string) {
	t.Helper()
	for _, db := range FindAllDatabases() {
		if strings.HasPrefix(db.Path, beadsDir) || strings.HasPrefix(db.BeadsDir, beadsDir) {
			t.Errorf("FindAllDatabases() lists %+v, under the user-level %s", db, beadsDir)
		}
	}
}

// TestImplicitDiscoverySkipsUserGlobalBeadsDir pins GH#4402: outside a
// repository rooted at $HOME, the ancestor walk must not adopt the user-level
// ~/.beads as the workspace.
func TestImplicitDiscoverySkipsUserGlobalBeadsDir(t *testing.T) {
	for _, rel := range []string{"notes", "."} {
		t.Run("cwd="+rel, func(t *testing.T) {
			home := userGlobalBeadsHome(t)
			beadsDir := filepath.Join(home, ".beads")
			cwd := filepath.Join(home, rel)
			chdirForDiscovery(t, cwd)

			if got := FindBeadsDir(); got != "" {
				t.Errorf("FindBeadsDir() = %q, want \"\"", got)
			}
			if got := FindDatabasePath(); got != "" {
				t.Errorf("FindDatabasePath() = %q, want \"\"", got)
			}
			if got := FindBeadsDirFrom(cwd); got != "" {
				t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", cwd, got)
			}
			assertNoUserGlobalDatabase(t, beadsDir)
			if got := SkippedUserGlobalBeadsDir(); got != beadsDir {
				t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
			}
		})
	}

	t.Run("bd -C into a repository under home", func(t *testing.T) {
		home := userGlobalBeadsHome(t)
		repo := filepath.Join(home, "repo")
		gitInitForDiscovery(t, repo)
		chdirForDiscovery(t, filepath.Join(home, "notes"))
		if got := FindBeadsDirFrom(repo); got != "" {
			t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", repo, got)
		}
	})

	t.Run("legacy beads.db only", func(t *testing.T) {
		// The reporter's literal layout: a pre-metadata ~/.beads/beads.db.
		home := newUserGlobalHome(t)
		beadsDir := filepath.Join(home, ".beads")
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		chdirForDiscovery(t, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Errorf("FindDatabasePath() = %q, want \"\"", got)
		}
		assertNoUserGlobalDatabase(t, beadsDir)
		if got := SkippedUserGlobalBeadsDir(); got != beadsDir {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
	})

	t.Run("config.yaml only", func(t *testing.T) {
		// ~/.beads/config.yaml is the legacy user-level config file, not a
		// workspace: it is skipped and earns no note.
		home := newUserGlobalHome(t)
		if err := os.WriteFile(filepath.Join(home, ".beads", "config.yaml"), []byte("# user config\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		chdirForDiscovery(t, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\"", got)
		}
	})

	t.Run("no user-level beads dir", func(t *testing.T) {
		home := newUserGlobalHome(t)
		if err := os.Remove(filepath.Join(home, ".beads")); err != nil {
			t.Fatal(err)
		}
		chdirForDiscovery(t, filepath.Join(home, "notes"))
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\"", got)
		}
	})

	t.Run("HOME unset", func(t *testing.T) {
		// With no home directory there is no global location to protect.
		home := userGlobalBeadsHome(t)
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")
		chdirForDiscovery(t, filepath.Join(home, "notes"))
		if got, want := FindBeadsDir(), filepath.Join(home, ".beads"); got != want {
			t.Errorf("FindBeadsDir() = %q, want %q", got, want)
		}
		if got := SkippedUserGlobalBeadsDir(); got != "" {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\"", got)
		}
	})

	t.Run("HOME through a symlink", func(t *testing.T) {
		home := userGlobalBeadsHome(t)
		link := filepath.Join(filepath.Dir(home), "home-link")
		if err := os.Symlink(home, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		t.Setenv("HOME", link)
		t.Setenv("USERPROFILE", link)
		chdirForDiscovery(t, filepath.Join(home, "notes"))
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Errorf("FindDatabasePath() = %q, want \"\"", got)
		}
	})

	t.Run("workspaces of their own under home", func(t *testing.T) {
		home := userGlobalBeadsHome(t)
		notesBeads := filepath.Join(home, "notes", ".beads")
		writeEmbeddedWorkspace(t, notesBeads)
		proj := filepath.Join(home, "proj")
		gitInitForDiscovery(t, proj)
		projBeads := filepath.Join(proj, ".beads")
		writeEmbeddedWorkspace(t, projBeads)

		for cwd, want := range map[string]string{filepath.Join(home, "notes"): notesBeads, proj: projBeads} {
			t.Run(filepath.Base(cwd), func(t *testing.T) {
				chdirForDiscovery(t, cwd)
				if got := FindBeadsDir(); got != want {
					t.Errorf("FindBeadsDir() = %q, want %q", got, want)
				}
				if got := FindDatabasePath(); got != filepath.Join(want, "embeddeddolt") {
					t.Errorf("FindDatabasePath() = %q, want %q", got, filepath.Join(want, "embeddeddolt"))
				}
				if got := SkippedUserGlobalBeadsDir(); got != filepath.Join(home, ".beads") {
					// The note is only printed once discovery found nothing,
					// so it does not depend on what lies below home.
					t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, filepath.Join(home, ".beads"))
				}
			})
		}
	})

	t.Run("project redirect to the user-level beads dir", func(t *testing.T) {
		// A project's own .beads/redirect naming ~/.beads is an explicit
		// on-disk choice and keeps working.
		home := userGlobalBeadsHome(t)
		beadsDir := filepath.Join(home, ".beads")
		proj := filepath.Join(home, "proj")
		gitInitForDiscovery(t, proj)
		if err := os.MkdirAll(filepath.Join(proj, ".beads"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proj, ".beads", RedirectFileName), []byte(beadsDir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		chdirForDiscovery(t, proj)
		if got := FindBeadsDir(); got != beadsDir {
			t.Errorf("FindBeadsDir() = %q, want %q", got, beadsDir)
		}
		if got, want := FindDatabasePath(), filepath.Join(beadsDir, "embeddeddolt"); got != want {
			t.Errorf("FindDatabasePath() = %q, want %q", got, want)
		}
	})

	t.Run("redirect inside the user-level beads dir", func(t *testing.T) {
		// ~/.beads/redirect is passed over together with its directory.
		home := newUserGlobalHome(t)
		beadsDir := filepath.Join(home, ".beads")
		target := filepath.Join(filepath.Dir(home), "elsewhere", ".beads")
		writeEmbeddedWorkspace(t, target)
		if err := os.WriteFile(filepath.Join(beadsDir, RedirectFileName), []byte(target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		notes := filepath.Join(home, "notes")
		chdirForDiscovery(t, notes)
		if got := FindBeadsDir(); got != "" {
			t.Errorf("FindBeadsDir() = %q, want \"\"", got)
		}
		if got := FindDatabasePath(); got != "" {
			t.Errorf("FindDatabasePath() = %q, want \"\"", got)
		}
		if got := FindBeadsDirFrom(notes); got != "" {
			t.Errorf("FindBeadsDirFrom(%q) = %q, want \"\"", notes, got)
		}
		if info := GetRedirectInfoFrom(notes); info.IsRedirected || info.LocalDir != "" {
			t.Errorf("GetRedirectInfoFrom(%q) = %+v, want no redirect", notes, info)
		}
		if info := GetRedirectInfo(); info.IsRedirected || info.LocalDir != "" {
			t.Errorf("GetRedirectInfo() = %+v, want no redirect", info)
		}
		if got := SkippedUserGlobalBeadsDir(); got != beadsDir {
			t.Errorf("SkippedUserGlobalBeadsDir() = %q, want %q", got, beadsDir)
		}
	})
}

// TestImplicitDiscoveryKeepsHomeRepoBeadsDir: a repository rooted at $HOME (a
// dotfiles repository) owns <home>/.beads, so discovery inside it still finds
// it.
func TestImplicitDiscoveryKeepsHomeRepoBeadsDir(t *testing.T) {
	for _, rel := range []string{".", "sub"} {
		t.Run("cwd="+rel, func(t *testing.T) {
			home := userGlobalBeadsHome(t)
			beadsDir := filepath.Join(home, ".beads")
			gitInitForDiscovery(t, home)
			cwd := filepath.Join(home, rel)
			if err := os.MkdirAll(cwd, 0o750); err != nil {
				t.Fatal(err)
			}
			chdirForDiscovery(t, cwd)

			if got := FindBeadsDir(); got != beadsDir {
				t.Errorf("FindBeadsDir() = %q, want %q", got, beadsDir)
			}
			if got, want := FindDatabasePath(), filepath.Join(beadsDir, "embeddeddolt"); got != want {
				t.Errorf("FindDatabasePath() = %q, want %q", got, want)
			}
			if got := FindBeadsDirFrom(cwd); got != beadsDir {
				t.Errorf("FindBeadsDirFrom(%q) = %q, want %q", cwd, got, beadsDir)
			}
			if got := SkippedUserGlobalBeadsDir(); got != "" {
				t.Errorf("SkippedUserGlobalBeadsDir() = %q, want \"\"", got)
			}
		})
	}
}
