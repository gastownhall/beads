// Tests for the commit-hook GIT_INDEX_FILE envelope (GH#4080).
//
// This file MUST NOT carry a `//go:build cgo` tag, like
// hooks_export_worktree_test.go: it exercises the pure-Go hook code paths in
// the pure-Go (CGO_ENABLED=0) and Windows lanes too.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCommitIndexGit runs git in dir with the test process's scrubbed
// environment plus extraEnv appended (so explicit GIT_INDEX_FILE entries win
// over anything inherited), failing the test on error and returning combined
// output. Fixture git commands never see a GIT_INDEX_FILE the test did not
// pass explicitly: the scrub keeps a hook envelope from leaking between the
// fixture and the assertions.
func runCommitIndexGit(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = dir
	cmd.Env = append(scrubGitHookEnv(os.Environ()), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// commitIndexWrite creates path's parents and writes content.
func commitIndexWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitIndexRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// commitIndexCopy copies src to dst, failing the test on error.
func commitIndexCopy(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, commitIndexRead(t, src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newCommitIndexRepo creates a repo with tracked.txt and .beads/issues.jsonl
// committed. It returns the worktree root, its absolute git dir, and the
// export path. The export still holds its committed content; callers rewrite
// it before staging so the blob assertions are about a real change.
func newCommitIndexRepo(t *testing.T) (repo, gitDir, exportPath string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runCommitIndexGit(t, repo, nil, "init", "-q", "-b", "main")
	runCommitIndexGit(t, repo, nil, "config", "user.email", "t@t")
	runCommitIndexGit(t, repo, nil, "config", "user.name", "t")
	commitIndexWrite(t, filepath.Join(repo, "tracked.txt"), []byte("v1\n"))
	exportPath = filepath.Join(repo, ".beads", "issues.jsonl")
	commitIndexWrite(t, exportPath, []byte(`{"id":"v1"}`+"\n"))
	runCommitIndexGit(t, repo, nil, "add", "--", "tracked.txt", ".beads/issues.jsonl")
	runCommitIndexGit(t, repo, nil, "commit", "-qm", "init")
	gitDir = strings.TrimSpace(runCommitIndexGit(t, repo, nil, "rev-parse", "--absolute-git-dir"))
	if gitDir == "" {
		t.Fatal("git rev-parse --absolute-git-dir returned nothing")
	}
	return repo, gitDir, exportPath
}

// addCommitIndexWorktree adds a linked worktree to repo and returns the
// worktree root, its per-worktree absolute git dir, and the worktree's copy
// of the export path.
func addCommitIndexWorktree(t *testing.T, repo string) (worktree, gitDir, exportPath string) {
	t.Helper()
	root := filepath.Dir(repo)
	worktree = filepath.Join(root, "wt")
	runCommitIndexGit(t, repo, nil, "worktree", "add", "-q", worktree, "-b", "wt")
	t.Cleanup(func() {
		cmd := gitCommand("worktree", "remove", "--force", worktree)
		cmd.Dir = repo
		_ = cmd.Run()
	})
	gitDir = strings.TrimSpace(runCommitIndexGit(t, worktree, nil, "rev-parse", "--absolute-git-dir"))
	exportPath = filepath.Join(worktree, ".beads", "issues.jsonl")
	return worktree, gitDir, exportPath
}

// commitIndexPathspecEnvelope simulates the hook environment of
// `git commit -- <paths>`: GIT_INDEX_FILE names a temporary
// next-index-<pid>.lock directly inside gitDir, seeded from HEAD plus
// stagePaths, while the parent commit holds gitDir/index.lock for the whole
// hook. It returns the pending index's path.
func commitIndexPathspecEnvelope(t *testing.T, repo, gitDir string, stagePaths ...string) string {
	t.Helper()
	pending := filepath.Join(gitDir, "next-index-4242.lock")
	runCommitIndexGit(t, repo, []string{"GIT_INDEX_FILE=" + pending}, "read-tree", "HEAD")
	for _, p := range stagePaths {
		runCommitIndexGit(t, repo, []string{"GIT_INDEX_FILE=" + pending}, "add", "--", p)
	}
	// The parent pathspec commit holds the real index lock.
	commitIndexCopy(t, filepath.Join(gitDir, "index"), filepath.Join(gitDir, "index.lock"))
	return pending
}

// commitIndexMinusAEnvelope simulates the hook environment of `git commit -a`:
// GIT_INDEX_FILE is gitDir/index.lock itself (the file the parent commit is
// building), holding the staged worktree changes.
func commitIndexMinusAEnvelope(t *testing.T, repo, gitDir string) string {
	t.Helper()
	pending := filepath.Join(gitDir, "index.lock")
	commitIndexCopy(t, filepath.Join(gitDir, "index"), pending)
	runCommitIndexGit(t, repo, []string{"GIT_INDEX_FILE=" + pending}, "add", "-u")
	return pending
}

// assertCommitIndexHasExport checks that indexFile stages exportPath's
// current content as a blob.
func assertCommitIndexHasExport(t *testing.T, dir, indexFile, exportPath string) {
	t.Helper()
	want := strings.TrimSpace(runCommitIndexGit(t, dir, nil, "hash-object", exportPath))
	got := runCommitIndexGit(t, dir, []string{"GIT_INDEX_FILE=" + indexFile},
		"ls-files", "--stage", "--", exportPath)
	if !strings.Contains(got, want) {
		t.Fatalf("index %s does not stage export blob %s:\n%s", indexFile, want, got)
	}
}

// TestGitAddFile_PathspecCommitHookStagesIntoTemporaryIndex reproduces
// GH#4080: during `git commit -- <pathspecs>`, Git runs pre-commit with
// GIT_INDEX_FILE pointing at a temporary .git/next-index-<pid>.lock while
// the parent commit holds .git/index.lock. Auto-export must stage into that
// temporary index (so the export lands in the commit being prepared) rather
// than scrubbing GIT_INDEX_FILE and colliding with the real index lock.
func TestGitAddFile_PathspecCommitHookStagesIntoTemporaryIndex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "bd-gh4080-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(env []string, args ...string) string {
		t.Helper()
		c := gitCommand(args...)
		c.Dir = repo
		c.Env = append(os.Environ(), env...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	runGit(nil, "init", "-q")
	runGit(nil, "config", "user.email", "t@t")
	runGit(nil, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(nil, "add", "tracked.txt")
	runGit(nil, "commit", "-qm", "init")

	target := filepath.Join(repo, ".beads", "issues.jsonl")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"id":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the pathspec-commit hook envelope: the temporary index is a
	// copy of the real one, and the parent commit owns .git/index.lock.
	indexData, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	tempIndexRel := filepath.Join(".git", "next-index-4080.lock")
	if err := os.WriteFile(filepath.Join(repo, tempIndexRel), indexData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "index.lock"), []byte("held by parent git commit"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	// Git runs hooks from the worktree root with a root-relative index path.
	t.Chdir(repo)
	t.Setenv("GIT_INDEX_FILE", tempIndexRel)

	if err := gitAddFile(target); err != nil {
		t.Fatalf("gitAddFile during pathspec commit hook: %v", err)
	}

	staged := runGit([]string{"GIT_INDEX_FILE=" + filepath.Join(repo, tempIndexRel)}, "ls-files", "--stage")
	if !strings.Contains(staged, ".beads/issues.jsonl") {
		t.Fatalf("expected .beads/issues.jsonl staged in the temporary pathspec index, got:\n%s", staged)
	}
}

// TestGitAddFile_StagesIntoCommitIndex covers the hook envelopes git 2.43
// actually creates (see gitIndexEnv): gitAddFile must stage the export into
// the index the running commit builds its tree from — the inherited
// GIT_INDEX_FILE — not the on-disk real index the parent commit has locked.
func TestGitAddFile_StagesIntoCommitIndex(t *testing.T) {
	rows := []struct {
		name string
		// envelope builds the pending index and returns its path, or "" for
		// the plain relative .git/index of a no-pathspec commit.
		envelope func(t *testing.T, repo, gitDir, exportPath string) string
		linked   bool
		// setGitDir: the hook envelope sets GIT_DIR (linked worktree) or
		// leaves it unset (main worktree, the reported case).
		setGitDir func(t *testing.T, gitDir string)
		// protected are git dir index files that must not change.
		protected func(t *testing.T, gitDir string) map[string][]byte
	}{
		{
			name: "main worktree, git commit -- <paths>",
			envelope: func(t *testing.T, repo, gitDir, exportPath string) string {
				return commitIndexPathspecEnvelope(t, repo, gitDir)
			},
			setGitDir: func(t *testing.T, gitDir string) {
				if err := os.Unsetenv("GIT_DIR"); err != nil {
					t.Fatal(err)
				}
			},
			protected: realIndexFiles,
		},
		{
			name: "linked worktree, git commit -- <paths>",
			envelope: func(t *testing.T, repo, gitDir, exportPath string) string {
				return commitIndexPathspecEnvelope(t, repo, gitDir)
			},
			linked: true,
			setGitDir: func(t *testing.T, gitDir string) {
				t.Setenv("GIT_DIR", gitDir)
			},
			protected: realIndexFiles,
		},
		{
			name: "main worktree, git commit -a",
			envelope: func(t *testing.T, repo, gitDir, exportPath string) string {
				return commitIndexMinusAEnvelope(t, repo, gitDir)
			},
			setGitDir: func(t *testing.T, gitDir string) {
				if err := os.Unsetenv("GIT_DIR"); err != nil {
					t.Fatal(err)
				}
			},
			protected: func(t *testing.T, gitDir string) map[string][]byte {
				// git commit -a's index.lock IS the pending index; only the
				// real index must survive untouched.
				return map[string][]byte{
					filepath.Join(gitDir, "index"): commitIndexRead(t, filepath.Join(gitDir, "index")),
				}
			},
		},
		{
			name: "plain commit keeps relative GIT_INDEX_FILE=.git/index",
			envelope: func(t *testing.T, repo, gitDir, exportPath string) string {
				return ""
			},
			setGitDir: func(t *testing.T, gitDir string) {
				if err := os.Unsetenv("GIT_DIR"); err != nil {
					t.Fatal(err)
				}
			},
			protected: func(t *testing.T, gitDir string) map[string][]byte {
				return nil
			},
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			repo, gitDir, exportPath := newCommitIndexRepo(t)
			worktree := repo
			if row.linked {
				worktree, gitDir, exportPath = addCommitIndexWorktree(t, repo)
			}
			// The envelope is built from the worktree the hook runs in: its
			// per-worktree git dir owns the pending index there.
			pending := row.envelope(t, worktree, gitDir, exportPath)
			// The export gets fresh content so staging is a real change. It
			// is written after the envelope is built so no fixture step
			// stages it into the pending index: the row below must prove
			// gitAddFile, not the envelope, is what staged it.
			commitIndexWrite(t, exportPath, []byte(`{"id":"v2"}`+"\n"))
			commitIndexWrite(t, exportPath, []byte(`{"id":"v2"}`+"\n"))
			var before map[string][]byte
			if row.protected != nil {
				before = row.protected(t, gitDir)
			}

			// Enter the envelope the way git enters a hook: cwd is the
			// worktree top (the base git itself uses for a relative value),
			// GIT_DIR only in linked worktrees.
			t.Chdir(worktree)
			if row.setGitDir != nil {
				row.setGitDir(t, gitDir)
			}
			if pending == "" {
				// A plain no-pathspec commit exports a root-relative index.
				t.Setenv("GIT_INDEX_FILE", filepath.Join(".git", "index"))
			} else {
				t.Setenv("GIT_INDEX_FILE", pending)
			}

			if err := gitAddFile(exportPath); err != nil {
				t.Fatalf("gitAddFile in a %s envelope: %v", row.name, err)
			}
			assertIndex := pending
			if assertIndex == "" {
				// The plain commit's relative .git/index names the real index.
				assertIndex = filepath.Join(gitDir, "index")
			}
			assertCommitIndexHasExport(t, worktree, assertIndex, exportPath)
			for path, want := range before {
				if got := commitIndexRead(t, path); string(got) != string(want) {
					t.Errorf("index file %s changed by gitAddFile:\nbefore %d bytes, after %d bytes",
						path, len(want), len(got))
				}
			}
		})
	}
}

// realIndexFiles snapshots the real index and the parent's held index lock,
// the two files a pathspec-commit envelope must leave alone.
func realIndexFiles(t *testing.T, gitDir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range []string{"index", "index.lock"} {
		path := filepath.Join(gitDir, name)
		if data, err := os.ReadFile(path); err == nil {
			out[path] = data
		}
	}
	return out
}