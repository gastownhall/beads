package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// TestResolveExportPath covers the pure export.path validator: valid
// defaults and nested paths pass; escapes, Git metadata, and leaf symlinks
// are refused with an error naming export.path (bd-1tmym4 / #7888).
func TestResolveExportPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on windows")
	}

	// newWorkspace builds a repo-shaped tree: <root>/repo/.beads.
	newWorkspace := func(t *testing.T) (root, repo, beadsDir string) {
		t.Helper()
		root = t.TempDir()
		repo = filepath.Join(root, "repo")
		beadsDir = filepath.Join(repo, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		return root, repo, beadsDir
	}

	t.Run("valid paths pass", func(t *testing.T) {
		_, repo, beadsDir := newWorkspace(t)
		for _, p := range []string{"issues.jsonl", "exports/issues.jsonl", "../issues.jsonl"} {
			got, err := resolveExportPath(beadsDir, p, repo, nil)
			if err != nil {
				t.Errorf("%q: unexpected error %v", p, err)
				continue
			}
			if want := filepath.Join(beadsDir, filepath.Clean(p)); got != want {
				t.Errorf("%q: got %s, want %s", p, got, want)
			}
		}
	})

	t.Run("empty, absolute, NUL, and exact-root refuse", func(t *testing.T) {
		_, repo, beadsDir := newWorkspace(t)
		// Volume names (c:\evil) are refused by the same check on Windows;
		// on Unix that spelling is just an odd relative filename.
		for _, p := range []string{"", "/etc/passwd", "nul\x00byte", "."} {
			if _, err := resolveExportPath(beadsDir, p, repo, nil); err == nil {
				t.Errorf("%q: expected a refusal", p)
			} else if !strings.Contains(err.Error(), "export.path") {
				t.Errorf("%q: refusal does not name export.path: %v", p, err)
			}
		}
	})

	t.Run("escapes refuse", func(t *testing.T) {
		root, repo, beadsDir := newWorkspace(t)
		// ../../x escapes the repo root; repo-evil is a sibling of the repo —
		// a same-prefix name that must not pass as "inside".
		if err := os.MkdirAll(filepath.Join(root, "repo-evil"), 0o750); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"../../evil.jsonl", "../../repo-evil/x.jsonl"} {
			if _, err := resolveExportPath(beadsDir, p, repo, nil); err == nil {
				t.Errorf("%q: expected a refusal", p)
			}
		}
	})

	t.Run("Git metadata refuses by component and by real dir", func(t *testing.T) {
		_, repo, beadsDir := newWorkspace(t)
		// .GIT spelled any case, via component check, without any gitDirs.
		if _, err := resolveExportPath(beadsDir, "../.GIT/hooks/x.jsonl", repo, nil); err == nil {
			t.Error("../.GIT/hooks/x.jsonl: expected a refusal")
		}
		// A directory linked from inside .beads onto the real git dir never
		// spells .git in export.path: only the gitDirs exclusion catches it.
		gitDir := filepath.Join(repo, ".git")
		if err := os.MkdirAll(gitDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(gitDir, filepath.Join(beadsDir, "linked-meta")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveExportPath(beadsDir, "linked-meta/x.jsonl", repo, []string{gitDir}); err == nil {
			t.Error("linked metadata alias: expected a refusal")
		}
		// The component check is exact: a sibling directory that merely
		// starts with .git is not git metadata and stays allowed.
		if _, err := resolveExportPath(beadsDir, "../.git-meta2/x.jsonl", repo, []string{gitDir}); err != nil {
			t.Errorf("sibling of .git: unexpected refusal %v", err)
		}
	})

	t.Run("leaf symlinks refuse, live and dangling", func(t *testing.T) {
		_, repo, beadsDir := newWorkspace(t)
		victim := filepath.Join(repo, "victim.jsonl")
		if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(beadsDir, "live.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(repo, "no-such"), filepath.Join(beadsDir, "dangling.jsonl")); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"live.jsonl", "dangling.jsonl"} {
			if _, err := resolveExportPath(beadsDir, p, repo, nil); err == nil {
				t.Errorf("%q: expected a refusal", p)
			}
		}
		if got, _ := os.ReadFile(victim); string(got) != "original\n" {
			t.Errorf("victim modified: %q", got)
		}
	})

	t.Run("linked and dangling parent directories refuse", func(t *testing.T) {
		_, repo, beadsDir := newWorkspace(t)
		// A linked parent inside .beads points outside the repo.
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(beadsDir, "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveExportPath(beadsDir, "linked/sub/x.jsonl", repo, nil); err == nil {
			t.Error("linked parent escaping the anchor: expected a refusal")
		}
		// A dangling parent link cannot be resolved at all.
		if err := os.Symlink(filepath.Join(repo, "gone"), filepath.Join(beadsDir, "dangling-dir")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveExportPath(beadsDir, "dangling-dir/x.jsonl", repo, nil); err == nil {
			t.Error("dangling parent: expected a refusal")
		}
	})

	t.Run("external store falls back to its parent", func(t *testing.T) {
		root, _, _ := newWorkspace(t)
		store := filepath.Join(root, "shared-store", ".beads")
		if err := os.MkdirAll(store, 0o750); err != nil {
			t.Fatal(err)
		}
		// No repoRoot: ../issues.jsonl stays inside the store's parent...
		if _, err := resolveExportPath(store, "../issues.jsonl", "", nil); err != nil {
			t.Errorf("../issues.jsonl on an external store: unexpected refusal %v", err)
		}
		// ...but anything beyond the parent escapes.
		if _, err := resolveExportPath(store, "../../evil.jsonl", "", nil); err == nil {
			t.Error("../../evil.jsonl on an external store: expected a refusal")
		}
	})

	t.Run("symlinked store keeps its resolved contents", func(t *testing.T) {
		root, repo, beadsDir := newWorkspace(t)
		external := filepath.Join(root, "external-store")
		if err := os.MkdirAll(external, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(beadsDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, beadsDir); err != nil {
			t.Fatal(err)
		}
		// The store itself is deliberately symlinked: its own contents stay
		// exportable, resolved to the external directory.
		if got, err := resolveExportPath(beadsDir, "issues.jsonl", repo, nil); err != nil {
			t.Errorf("issues.jsonl in a symlinked store: unexpected refusal %v", err)
		} else if want := filepath.Join(beadsDir, "issues.jsonl"); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
		// A path above the store stays anchored to the repository root: the
		// store's external parent is never an anchor, so ../x lands back in
		// the repo — lexically — rather than resolving through the link.
		got, err := resolveExportPath(beadsDir, "../outside-store.jsonl", repo, nil)
		if err != nil {
			t.Fatalf("../outside-store.jsonl above a symlinked store: %v", err)
		}
		if want := filepath.Join(repo, "outside-store.jsonl"); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})

	t.Run("missing destination parents resolve through the anchor", func(t *testing.T) {
		_, repo, beadsDir := newWorkspace(t)
		got, err := resolveExportPath(beadsDir, "exports/nested/deep/issues.jsonl", repo, nil)
		if err != nil {
			t.Fatalf("nested missing parents: %v", err)
		}
		if want := filepath.Join(beadsDir, "exports", "nested", "deep", "issues.jsonl"); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}

// TestAutoExportSkipsUnsafeExportPath: the auto-export lane warns naming
// export.path and skips the export — creating nothing — without failing
// the enclosing command (bd-1tmym4 / #7888).
func TestAutoExportSkipsUnsafeExportPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need privileges on windows")
	}

	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.path", "../.git/evil.jsonl")

	saveAndRestoreGlobals(t)
	fake := &fakeStateHashStore{
		stateHash: "hash-after-write",
		issues:    []*types.Issue{{ID: "bd-1", Title: "an issue", Priority: 2, CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC()}},
	}
	store = fake

	dir := autoExportTestDir(t)
	// The directory the unsafe export.path points into: at base the export
	// creates the file there.
	if err := os.MkdirAll(filepath.Join(dir, ".beads", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	saveExportAutoState(filepath.Join(dir, ".beads"), &exportAutoState{
		LastDoltCommit: "hash-before-write",
		Timestamp:      time.Time{}, // zero: throttle window open
	})

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("maybeAutoExport must let the enclosing command succeed: %v", err)
	}

	gitEscape := filepath.Join(dir, ".beads", ".git", "evil.jsonl")
	if _, err := os.Stat(gitEscape); !os.IsNotExist(err) {
		t.Errorf("auto-export created %s (err=%v)", gitEscape, err)
	}
	if fake.stateHashCalls != 0 {
		t.Errorf("state hash calls = %d, want 0 (the guard refuses before change detection)", fake.stateHashCalls)
	}
}
// TestPreCommitExportRejectsUnsafePaths: the pre-commit hook lane refuses
// escaping, Git-metadata, and leaf-symlink export paths — no subprocess, no
// destination creation, no staging — and lets the enclosing hook succeed
// (bd-1tmym4 / #7888).
func TestPreCommitExportRejectsUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"bd-1"}`+"\n"))

	runGitForWorktreeTest(t, repo, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, repo, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, repo, "config", "user.name", "test")
	runGitForWorktreeTest(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	// The pre-commit export only runs when the commit already touches .beads.
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "staged.txt"), []byte("x\n"))
	runGitForWorktreeTest(t, repo, "add", ".beads/staged.txt")

	// A stub `bd` on PATH records invocations; the real refusal under test is
	// that it is never called with an unsafe destination.
	binDir := t.TempDir()
	sentinel := filepath.Join(binDir, "invocations")
	quotedSentinel := "'" + strings.ReplaceAll(sentinel, "'", `'\''`) + "'"
	stub := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> ` + quotedSentinel + "\n" +
		`if [ "$1" = export ] && [ "$2" = -o ]; then printf '%s\n' '{"id":"bd-exported"}' > "$3"; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.git-add", true)
	config.Set("no-git-ops", false)

	t.Setenv("BEADS_DIR", beadsDir)

	victim := filepath.Join(root, "victim.jsonl")
	cases := []struct {
		name       string
		exportPath string
		setup      func()
	}{
		{
			name:       "escaping path",
			exportPath: "../../evil.jsonl",
		},
		{
			name:       "Git metadata path",
			exportPath: "../.git/evil.jsonl",
		},
		{
			name:       "leaf symlink",
			exportPath: "issues.jsonl",
			setup: func() {
				if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				// Replace the ordinary fixture issues.jsonl with a symlink.
				if err := os.Remove(filepath.Join(beadsDir, "issues.jsonl")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(beadsDir, "issues.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup()
			}
			if err := os.RemoveAll(sentinel); err != nil {
				t.Fatal(err)
			}
			config.Set("export.path", tc.exportPath)

			exportJSONLForCommit() // must warn and return, not fail

			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				got := readFileForWorktreeTest(t, sentinel)
				t.Errorf("export subprocess ran for an unsafe export.path: %q", got)
			}
			if tc.name == "escaping path" {
				if _, err := os.Stat(filepath.Join(root, "evil.jsonl")); !os.IsNotExist(err) {
					t.Errorf("export escaped the repository: %s exists", filepath.Join(root, "evil.jsonl"))
				}
			}
			if tc.name == "Git metadata path" {
				if _, err := os.Stat(filepath.Join(repo, ".git", "evil.jsonl")); !os.IsNotExist(err) {
					t.Errorf("export entered Git metadata: %s exists", filepath.Join(repo, ".git", "evil.jsonl"))
				}
			}
			if tc.name == "leaf symlink" {
				got, err := os.ReadFile(victim)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != "original\n" {
					t.Errorf("export wrote through the leaf symlink: victim now %q", got)
				}
				if fi, lerr := os.Lstat(filepath.Join(beadsDir, "issues.jsonl")); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("leaf symlink was replaced: %v (%v)", fi, lerr)
				}
			}
			if staged := stagedPathsForWorktreeTest(t, repo); len(staged) > 1 {
				t.Errorf("unsafe export staged paths: %v", staged)
			}
		})
	}
}

// TestHookExportPathResolvesRoot: a relative export.path resolves against
// the hook's own worktree root, so ../issues.jsonl lands at the repository
// root, not outside it (bd-1tmym4 / #7888).
func TestHookExportPathResolvesRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"bd-1"}`+"\n"))

	runGitForWorktreeTest(t, repo, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, repo, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, repo, "config", "user.name", "test")
	runGitForWorktreeTest(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "staged.txt"), []byte("x\n"))
	runGitForWorktreeTest(t, repo, "add", ".beads/staged.txt")

	binDir := t.TempDir()
	sentinel := filepath.Join(binDir, "invocations")
	quotedSentinel := "'" + strings.ReplaceAll(sentinel, "'", `'\''`) + "'"
	stub := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> ` + quotedSentinel + "\n" +
		`if [ "$1" = export ] && [ "$2" = -o ]; then printf '%s\n' '{"id":"bd-exported"}' > "$3"; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.git-add", false)
	config.Set("no-git-ops", false)
	config.Set("export.path", "../issues.jsonl")

	t.Setenv("BEADS_DIR", beadsDir)

	exportJSONLForCommit()

	want := "export -o " + filepath.Join(repo, "issues.jsonl") + "\n"
	if got := readFileForWorktreeTest(t, sentinel); got != want {
		t.Fatalf("export subprocess = %q, want %q", got, want)
	}
	if got := readFileForWorktreeTest(t, filepath.Join(repo, "issues.jsonl")); got != `{"id":"bd-exported"}`+"\n" {
		t.Errorf("root-level export = %q", got)
	}
}
