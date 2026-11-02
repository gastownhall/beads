//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
)

// TestPreCommitExportRejectsUnsafePaths drives the pre-commit export with a
// stub bd: an unsafe export.path warns naming export.path, never runs the
// export and creates nothing, while in-repository paths still export.
func TestPreCommitExportRejectsUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub bd is a shell script; symlinks need privileges on windows")
	}
	for _, name := range []string{"escape", "sibling-prefix", "absolute", "symlink", "symlink-missing-parent", "dangling-parent", "leaf-symlink", "git-config", "git-case", "git-nested", "git-hooks", "git-symlink", "git-dir-alias", "relative", "nested", "repo-root-file", "default"} {
		t.Run(name, func(t *testing.T) {
			isolateBeadsDirForTest(t)
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			beadsDir := filepath.Join(repo, ".beads")
			writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
			runGitForWorktreeTest(t, repo, "init", "-q")
			runGitForWorktreeTest(t, repo, "add", ".beads/metadata.json")
			t.Chdir(repo)
			t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
			git.ResetCaches()
			t.Cleanup(git.ResetCaches)
			t.Setenv("BEADS_DIR", beadsDir)
			initConfigForTest(t)
			outside := filepath.Join(root, "outside")
			writeFileForWorktreeTest(t, filepath.Join(outside, "sentinel"), []byte("unchanged"))
			path := "issues.jsonl"
			valid := false
			sentinels := []string{filepath.Join(outside, "sentinel")}
			created := []string{filepath.Join(outside, "new"), filepath.Join(root, "missing")}
			switch name {
			case "escape":
				path = "../../outside/sentinel"
			case "sibling-prefix":
				writeFileForWorktreeTest(t, filepath.Join(root, "repo-sibling", "sentinel"), []byte("unchanged"))
				sentinels = append(sentinels, filepath.Join(root, "repo-sibling", "sentinel"))
				path = "../../repo-sibling/sentinel"
			case "absolute":
				path = filepath.Join(outside, "sentinel")
			case "symlink", "symlink-missing-parent":
				if err := os.Symlink(outside, filepath.Join(beadsDir, "link")); err != nil {
					t.Fatal(err)
				}
				path = "link/sentinel"
				if name == "symlink-missing-parent" {
					path = "link/new/sentinel"
				}
			case "dangling-parent":
				if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(beadsDir, "dangling")); err != nil {
					t.Fatal(err)
				}
				path = "dangling/issues.jsonl"
			case "leaf-symlink":
				if err := os.Symlink(filepath.Join(outside, "sentinel"), filepath.Join(beadsDir, "issues.jsonl")); err != nil {
					t.Fatal(err)
				}
				path = ""
			case "git-config":
				path = "../.git/config"
			case "git-case":
				path = "../.GIT/config"
			case "git-nested":
				path = "sub/.GIT/x"
			case "git-hooks":
				path = "../.git/hooks/pre-commit"
			case "git-symlink", "git-dir-alias":
				gitDir := filepath.Join(repo, ".git")
				if name == "git-dir-alias" {
					gitDir = filepath.Join(repo, "git-admin")
					if err := os.Rename(filepath.Join(repo, ".git"), gitDir); err != nil {
						t.Fatal(err)
					}
					t.Setenv("GIT_DIR", gitDir)
					git.ResetCaches()
				}
				if err := os.Symlink(gitDir, filepath.Join(beadsDir, "git-link")); err != nil {
					t.Fatal(err)
				}
				path = "git-link/sentinel"
				sentinels = append(sentinels, filepath.Join(gitDir, "sentinel"))
			case "relative":
				path, valid = "./issues.jsonl", true
			case "nested":
				path, valid = "exports/new/issues.jsonl", true
			case "repo-root-file":
				path, valid = "../issues.jsonl", true
			case "default":
				path, valid = "", true
			}
			if strings.HasPrefix(name, "git-") && name != "git-symlink" && name != "git-dir-alias" {
				sentinels = append(sentinels, filepath.Join(beadsDir, path))
			}
			for _, sentinel := range sentinels {
				writeFileForWorktreeTest(t, sentinel, []byte("unchanged"))
			}
			bin := filepath.Join(root, "bin")
			marker := filepath.Join(root, "called")
			stub := "#!/bin/sh\nprintf called > '" + marker + "'\nprintf exported > \"$3\"\n"
			writeFileForWorktreeTest(t, filepath.Join(bin, "bd"), []byte(stub))
			if err := os.Chmod(filepath.Join(bin, "bd"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			config.Set("export.auto", true)
			config.Set("export.git-add", true)
			config.Set("export.path", path)
			staged := func() string {
				cmd := gitCommand("diff", "--cached", "--name-only")
				cmd.Dir = repo
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git diff --cached: %v\n%s", err, out)
				}
				return string(out)
			}
			stagedBefore := staged()
			warning := captureHookStderr(t, exportJSONLForCommit)
			_, calledErr := os.Stat(marker)
			if valid {
				if calledErr != nil {
					t.Fatalf("export did not run: %v; %s", calledErr, warning)
				}
				if path == "" {
					path = "issues.jsonl"
				}
				if got := readFileForWorktreeTest(t, filepath.Join(beadsDir, path)); got != "exported" {
					t.Errorf("export = %q", got)
				}
			} else {
				if !os.IsNotExist(calledErr) {
					t.Error("unsafe export invoked bd")
				}
				if !strings.Contains(warning, "export.path") {
					t.Errorf("warning does not name export.path: %q", warning)
				}
				if got := staged(); got != stagedBefore {
					t.Errorf("unsafe export changed the index: %q, want %q", got, stagedBefore)
				}
				for _, p := range created {
					if _, err := os.Lstat(p); !os.IsNotExist(err) {
						t.Errorf("created %s: %v", p, err)
					}
				}
			}
			for _, sentinel := range sentinels {
				if got := readFileForWorktreeTest(t, sentinel); got != "unchanged" {
					t.Errorf("sentinel changed: %q", got)
				}
			}
		})
	}
}

// TestHookExportPathResolvesRoot anchors a store reached through a symlinked
// checkout to that checkout, and keeps an external store inside its own
// parent rather than the committing checkout.
func TestHookExportPathResolvesRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	runGitForWorktreeTest(t, root, "init", "-q")
	t.Chdir(root)
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	link := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"nested/issues.jsonl", "../issues.jsonl"} {
		if _, err := resolveExportPath(filepath.Join(link, ".beads"), path); err != nil {
			t.Errorf("%s through a symlinked checkout: %v", path, err)
		}
	}
	if _, err := resolveExportPath(filepath.Join(link, ".beads"), "../.git/config"); err == nil {
		t.Error("accepted git metadata through a symlinked checkout")
	}
	writeFileForWorktreeTest(t, filepath.Join(root, "file"), []byte("test"))
	if _, err := resolveExportPath(root, "file/issues.jsonl"); err == nil {
		t.Error("accepted non-directory parent")
	}

	external := filepath.Join(t.TempDir(), "shared", ".beads")
	if err := os.MkdirAll(external, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"issues.jsonl", "../issues.jsonl"} {
		if _, err := resolveExportPath(external, path); err != nil {
			t.Errorf("external store %s: %v", path, err)
		}
	}
	rel, err := filepath.Rel(external, filepath.Join(root, "issues.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExportPath(external, rel); err == nil {
		t.Error("external store anchored to the committing checkout")
	}
}

// TestResolveExportPath covers the anchors and refusals the shared resolver
// applies to both export lanes.
func TestResolveExportPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_COMMON_DIR", "")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, repo, "init", "-q")
	runGitForWorktreeTest(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	runGitForWorktreeTest(t, repo, "worktree", "add", "-q", filepath.Join(root, "wt"))
	separate := filepath.Join(root, "separate")
	if err := os.MkdirAll(separate, 0o750); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, separate, "init", "-q", "--separate-git-dir", filepath.Join(separate, "meta"))
	writeFileForWorktreeTest(t, filepath.Join(repo, "file"), []byte("x"))
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(repo, ".beads", "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.jsonl"), filepath.Join(repo, ".beads", "dangling.jsonl")); err != nil {
		t.Fatal(err)
	}
	writeFileForWorktreeTest(t, filepath.Join(repo, ".beads", "target.jsonl"), []byte("x"))
	if err := os.Symlink("target.jsonl", filepath.Join(repo, ".beads", "live.jsonl")); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external", ".beads")
	storeTarget := filepath.Join(root, "store-target")
	for _, dir := range []string{external, storeTarget, filepath.Join(repo, "sub", ".beads"), filepath.Join(root, "repo-sibling")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	linkedRepo := filepath.Join(root, "linked")
	if err := os.MkdirAll(linkedRepo, 0o750); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, linkedRepo, "init", "-q")
	if err := os.Symlink(storeTarget, filepath.Join(linkedRepo, ".beads")); err != nil {
		t.Fatal(err)
	}

	beads := filepath.Join(repo, ".beads")
	cases := []struct {
		name, dir, path string
		ok              bool
	}{
		{"default", beads, "issues.jsonl", true},
		{"nested", beads, "exports/issues.jsonl", true},
		{"missing parents", beads, "a/b/issues.jsonl", true},
		{"repository root file", beads, "../issues.jsonl", true},
		{"nested store reaches repository", filepath.Join(repo, "sub", ".beads"), "../../top.jsonl", true},
		{"worktree store", filepath.Join(root, "wt", ".beads"), "issues.jsonl", true},
		{"worktree root file", filepath.Join(root, "wt", ".beads"), "../issues.jsonl", true},
		{"external store", external, "issues.jsonl", true},
		{"external store parent", external, "../issues.jsonl", true},
		{"symlinked store", filepath.Join(linkedRepo, ".beads"), "issues.jsonl", true},
		{"symlinked store nested", filepath.Join(linkedRepo, ".beads"), "x/issues.jsonl", true},

		{"absolute", beads, filepath.Join(root, "x.jsonl"), false},
		{"nul", beads, "a\x00b", false},
		{"empty", beads, "", false},
		{"store dir itself", beads, ".", false},
		{"exact repository root", beads, "..", false},
		{"escape", beads, "../../x.jsonl", false},
		{"sibling prefix", beads, "../../repo-sibling/x.jsonl", false},
		{"nested store escape", filepath.Join(repo, "sub", ".beads"), "../../../x.jsonl", false},
		{"worktree escape", filepath.Join(root, "wt", ".beads"), "../../repo/x.jsonl", false},
		{"external escape", external, "../../x.jsonl", false},
		{"exact external anchor", external, "..", false},
		{"symlinked store external parent", filepath.Join(linkedRepo, ".beads"), "../../store-target/x.jsonl", false},
		{"git component", beads, "../.git/x", false},
		{"git component case", beads, "x/.GIT/y", false},
		{"worktree git file", filepath.Join(root, "wt", ".beads"), "../.git", false},
		{"separate git dir", filepath.Join(separate, ".beads"), "../meta/config", false},
		{"non-directory parent", beads, "../file/x.jsonl", false},
		{"dangling parent", beads, "dangling/x.jsonl", false},
		{"dangling leaf", beads, "dangling.jsonl", false},
		{"live leaf", beads, "live.jsonl", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExportPath(tc.dir, tc.path)
			if tc.ok {
				if err != nil {
					t.Fatalf("resolveExportPath(%q, %q): %v", tc.dir, tc.path, err)
				}
				if want := filepath.Join(tc.dir, tc.path); got != want {
					t.Errorf("got %q, want %q", got, want)
				}
				return
			}
			if err == nil {
				t.Errorf("resolveExportPath(%q, %q) = %q, want an error", tc.dir, tc.path, got)
			}
		})
	}
	for _, p := range []string{filepath.Join(beads, "a"), filepath.Join(root, "missing"), filepath.Join(root, "wt", ".beads")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("validation created %s: %v", p, err)
		}
	}

	t.Run("hook-selected git dir", func(t *testing.T) {
		admin := filepath.Join(external, "admin")
		if err := os.MkdirAll(admin, 0o750); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", admin)
		if _, err := resolveExportPath(external, "admin/x"); err == nil {
			t.Error("accepted the hook's GIT_DIR")
		}
	})

	t.Run("separate git dir case variant", func(t *testing.T) {
		if !fsCaseInsensitive(t, separate) {
			t.Skip("case-variant names are distinct directories on a case-sensitive filesystem")
		}
		// The git directory lives at separate/meta; on a case-insensitive
		// filesystem ../META reaches the same directory, so the export must
		// be refused exactly like ../meta/config.
		if _, err := resolveExportPath(filepath.Join(separate, ".beads"), "../META/config"); err == nil {
			t.Error("accepted a case-variant path into the separate git dir")
		}
		if _, err := resolveExportPath(filepath.Join(separate, ".beads"), "../Meta/hooks/pre-commit"); err == nil {
			t.Error("accepted a mixed-case path into the separate git dir")
		}
	})
}

// fsCaseInsensitive reports whether dir's filesystem resolves case-variant
// names to the same entry, detected by writing one file and statting its
// differently-cased name.
func fsCaseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "export-test-CASE-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(probe) })
	_, err := os.Lstat(filepath.Join(dir, "export-test-case-probe"))
	return err == nil
}

// TestHookGitDirs covers the GIT_DIR / GIT_COMMON_DIR / commondir selection
// behind the hook-side git metadata check: a linked worktree's admin dir
// carries a commondir file naming the shared git dir, and the env override
// takes precedence over that file.
func TestHookGitDirs(t *testing.T) {
	t.Run("commondir file", func(t *testing.T) {
		gitDir := t.TempDir()
		common := filepath.Join(gitDir, "shared-git")
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("shared-git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", gitDir)
		t.Setenv("GIT_COMMON_DIR", "")
		got := hookGitDirs()
		if len(got) != 2 || got[0] != gitDir || got[1] != common {
			t.Errorf("hookGitDirs() = %v, want [%s %s]", got, gitDir, common)
		}
	})

	t.Run("commondir file absolute", func(t *testing.T) {
		gitDir := t.TempDir()
		common := t.TempDir()
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(common+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", gitDir)
		t.Setenv("GIT_COMMON_DIR", "")
		got := hookGitDirs()
		if len(got) != 2 || got[0] != gitDir || got[1] != common {
			t.Errorf("hookGitDirs() = %v, want [%s %s]", got, gitDir, common)
		}
	})

	t.Run("GIT_COMMON_DIR wins over commondir file", func(t *testing.T) {
		gitDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("shared-git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		common := t.TempDir()
		t.Setenv("GIT_DIR", gitDir)
		t.Setenv("GIT_COMMON_DIR", common)
		got := hookGitDirs()
		if len(got) != 2 || got[0] != gitDir || got[1] != common {
			t.Errorf("hookGitDirs() = %v, want [%s %s]", got, gitDir, common)
		}
	})

	t.Run("no commondir file", func(t *testing.T) {
		gitDir := t.TempDir()
		t.Setenv("GIT_DIR", gitDir)
		t.Setenv("GIT_COMMON_DIR", "")
		if got := hookGitDirs(); len(got) != 1 || got[0] != gitDir {
			t.Errorf("hookGitDirs() = %v, want [%s]", got, gitDir)
		}
	})

	t.Run("empty commondir file", func(t *testing.T) {
		gitDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", gitDir)
		t.Setenv("GIT_COMMON_DIR", "")
		if got := hookGitDirs(); len(got) != 1 || got[0] != gitDir {
			t.Errorf("hookGitDirs() = %v, want [%s]", got, gitDir)
		}
	})

	t.Run("no GIT_DIR", func(t *testing.T) {
		t.Setenv("GIT_DIR", "")
		t.Setenv("GIT_COMMON_DIR", t.TempDir())
		if got := hookGitDirs(); got != nil {
			t.Errorf("hookGitDirs() = %v, want nil", got)
		}
	})
}

// TestAutoExportRejectsUnsafePaths: ordinary auto-export warns naming
// export.path and leaves an escaping or leaf-linked destination alone, while
// the enclosing command still succeeds.
func TestAutoExportRejectsUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	for _, name := range []string{"escape", "leaf-symlink"} {
		t.Run(name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			config.Set("export.git-add", false)
			saveAndRestoreGlobals(t)
			fake := &fakeStateHashStore{stateHash: "hash-after-write"}
			store = fake

			dir := autoExportTestDir(t)
			beadsDir := filepath.Join(dir, ".beads")
			saveExportAutoState(beadsDir, &exportAutoState{LastDoltCommit: "hash-before-write"})
			target := filepath.Join(t.TempDir(), "issues.jsonl")
			writeFileForWorktreeTest(t, target, nil)
			path := ""
			if name == "escape" {
				rel, err := filepath.Rel(beadsDir, target)
				if err != nil {
					t.Fatal(err)
				}
				path = rel
			} else if err := os.Symlink(target, filepath.Join(beadsDir, "issues.jsonl")); err != nil {
				t.Fatal(err)
			}
			config.Set("export.path", path)

			var err error
			warning := captureHookStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err != nil {
				t.Fatalf("maybeAutoExport: %v", err)
			}
			if !strings.Contains(warning, "export.path") {
				t.Errorf("warning does not name export.path: %q", warning)
			}
			if _, err := os.Stat(target); err != nil {
				t.Errorf("destination removed: %v", err)
			}
			if name == "leaf-symlink" {
				if info, err := os.Lstat(filepath.Join(beadsDir, "issues.jsonl")); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("leaf symlink replaced: %v", err)
				}
			}
			if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "hash-before-write" {
				t.Errorf("export state advanced to %q", state.LastDoltCommit)
			}
			if fake.stateHashCalls != 0 {
				t.Errorf("state hash calls = %d, want 0 (refusal precedes change detection)", fake.stateHashCalls)
			}
		})
	}

	t.Run("default", func(t *testing.T) {
		initConfigForTest(t)
		config.Set("export.auto", true)
		saveAndRestoreGlobals(t)
		store = &fakeStateHashStore{stateHash: "hash-after-write"}
		dir := autoExportTestDir(t)
		beadsDir := filepath.Join(dir, ".beads")
		saveExportAutoState(beadsDir, &exportAutoState{LastDoltCommit: "hash-before-write", Timestamp: time.Time{}})
		var err error
		warning := captureHookStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
		if err != nil || strings.Contains(warning, "export.path") {
			t.Fatalf("default export.path refused: %v, %q", err, warning)
		}
		if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "hash-after-write" {
			t.Errorf("export did not run: state %q", state.LastDoltCommit)
		}
	})
}
