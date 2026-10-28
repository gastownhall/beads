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
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/execenv"
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

// TestPreCommitHasStagedBeadsFiles_ReadsCommitIndex pins AC-2: the export
// gate must answer "does the commit being prepared touch .beads?", which
// during a pathspec commit is a question about the pending GIT_INDEX_FILE,
// not the on-disk real index the parent commit has locked away.
func TestPreCommitHasStagedBeadsFiles_ReadsCommitIndex(t *testing.T) {
	rows := []struct {
		name           string
		stageInPending []string
		stageInReal    []string
		want           bool
	}{
		{
			name:           "pending index carries the .beads change",
			stageInPending: []string{".beads/config.yaml"},
			want:           true,
		},
		{
			name:        "real index only: pending index has no .beads change",
			stageInReal: []string{".beads/config.yaml"},
			want:        false,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			repo, gitDir, _ := newCommitIndexRepo(t)
			cfg := filepath.Join(repo, ".beads", "config.yaml")
			commitIndexWrite(t, cfg, []byte("export:\n  auto: false\n"))
			pending := filepath.Join(gitDir, "next-index-4242.lock")
			runCommitIndexGit(t, repo, []string{"GIT_INDEX_FILE=" + pending}, "read-tree", "HEAD")
			for _, p := range row.stageInPending {
				runCommitIndexGit(t, repo, []string{"GIT_INDEX_FILE=" + pending}, "add", "--", p)
			}
			for _, p := range row.stageInReal {
				runCommitIndexGit(t, repo, nil, "add", "--", p)
			}
			if err := os.Unsetenv("GIT_DIR"); err != nil {
				t.Fatal(err)
			}
			t.Chdir(repo)
			t.Setenv("GIT_INDEX_FILE", pending)
			if got := preCommitHasStagedBeadsFiles(filepath.Join(repo, ".beads")); got != row.want {
				t.Fatalf("preCommitHasStagedBeadsFiles = %v, want %v", got, row.want)
			}
		})
	}
}

// TestExportJSONLForCommit_CommitIndex pins the pre-commit export lane
// end to end inside the hook envelopes git actually creates (GH#4080): the
// gate must look at the commit being prepared, the export must land in that
// commit's index, and a pathspec that excludes .beads must leave the pending
// index untouched. A stub `bd` on PATH stands in for the real binary, like
// TestHookLanesUseTheWorktreeJSONL.
func TestExportJSONLForCommit_CommitIndex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	binDir := t.TempDir()
	sentinel := filepath.Join(binDir, "invocations")
	quotedSentinel := "'" + strings.ReplaceAll(sentinel, "'", `'\''`) + "'"
	stub := "#!/bin/sh\n" +
		`printf '%s BD_GIT_HOOK=%s\n' "$*" "${BD_GIT_HOOK-unset}" >> ` + quotedSentinel + "\n" +
		`if [ "$1" = export ] && [ "$2" = -o ]; then printf '%s\n' '{"id":"bd-exported"}' > "$3"; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Prove the stub is reachable and records, else nothing below can fail.
	if err := exec.Command("bd", "probe").Run(); err != nil {
		t.Fatalf("stub bd must be runnable from PATH: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("stub bd must record its invocations: %v", err)
	}

	t.Run("pathspec commit includes .beads", func(t *testing.T) {
		repo, gitDir, exportPath := newCommitIndexRepo(t)
		commitIndexWrite(t, filepath.Join(repo, ".beads", "metadata.json"), []byte("{}\n"))
		runCommitIndexGit(t, repo, nil, "add", "--", ".beads/metadata.json")
		runCommitIndexGit(t, repo, nil, "commit", "-qm", "metadata")
		// The pending index carries the .beads change; the real index has
		// nothing staged.
		commitIndexWrite(t, filepath.Join(repo, ".beads", "config.yaml"), []byte("export:\n  auto: false\n"))
		pending := commitIndexPathspecEnvelope(t, repo, gitDir, ".beads/config.yaml")

		t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))
		initConfigForTest(t)
		config.Set("export.auto", true)
		config.Set("export.git-add", true)
		config.Set("export.path", "issues.jsonl")
		config.Set("no-git-ops", false)
		config.Set("sync.remote", "")
		config.Set("sync.git-remote", "")

		if err := os.RemoveAll(sentinel); err != nil {
			t.Fatal(err)
		}
		if err := os.Unsetenv("GIT_DIR"); err != nil {
			t.Fatal(err)
		}
		t.Chdir(repo)
		t.Setenv("GIT_INDEX_FILE", pending)

		_ = captureHookStderr(t, exportJSONLForCommit)

		if got, want := readFileForWorktreeTest(t, sentinel), "export -o "+exportPath+" BD_GIT_HOOK=1\n"; got != want {
			t.Fatalf("export subprocess = %q, want %q", got, want)
		}
		assertCommitIndexHasExport(t, repo, pending, exportPath)
	})

	t.Run("pathspec commit excludes .beads", func(t *testing.T) {
		repo, gitDir, exportPath := newCommitIndexRepo(t)
		// The real index carries a .beads change; the pending index only the
		// pathspec-selected tracked.txt.
		commitIndexWrite(t, filepath.Join(repo, ".beads", "config.yaml"), []byte("export:\n  auto: false\n"))
		runCommitIndexGit(t, repo, nil, "add", "--", ".beads/config.yaml")
		commitIndexWrite(t, filepath.Join(repo, "tracked.txt"), []byte("v2\n"))
		pending := commitIndexPathspecEnvelope(t, repo, gitDir, "tracked.txt")
		before := commitIndexRead(t, pending)

		t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))
		initConfigForTest(t)
		config.Set("export.auto", true)
		config.Set("export.git-add", true)
		config.Set("export.path", "issues.jsonl")
		config.Set("no-git-ops", false)
		config.Set("sync.remote", "")
		config.Set("sync.git-remote", "")

		if err := os.RemoveAll(sentinel); err != nil {
			t.Fatal(err)
		}
		if err := os.Unsetenv("GIT_DIR"); err != nil {
			t.Fatal(err)
		}
		t.Chdir(repo)
		t.Setenv("GIT_INDEX_FILE", pending)

		_ = captureHookStderr(t, exportJSONLForCommit)

		if _, err := os.Stat(sentinel); err == nil {
			t.Fatalf("export ran although the pathspec excludes .beads:\n%s", readFileForWorktreeTest(t, sentinel))
		}
		if got := commitIndexRead(t, pending); string(got) != string(before) {
			t.Errorf("pending index changed although the pathspec excludes .beads")
		}
		if got, want := readFileForWorktreeTest(t, exportPath), `{"id":"v1"}`+"\n"; got != want {
			t.Errorf("export file = %q, want it untouched %q", got, want)
		}
	})

	// linkedWorktreeDeletionEnvelope stages the deletion of the worktree's
	// export file in the pending index idx and returns idx. plain=true uses
	// the worktree's real index as the pending index (a `git rm` before a
	// plain commit); otherwise the pending index is a pathspec next-index
	// while the parent holds the real index lock.
	linkedWorktreeDeletionEnvelope := func(t *testing.T, wt, wtGitDir string, plain bool) string {
		t.Helper()
		idx := filepath.Join(wtGitDir, "next-index-4242.lock")
		if plain {
			idx = filepath.Join(wtGitDir, "index")
		} else {
			commitIndexCopy(t, filepath.Join(wtGitDir, "index"), idx)
			defer func() {
				// The parent pathspec commit holds the real index lock.
				commitIndexCopy(t, filepath.Join(wtGitDir, "index"), filepath.Join(wtGitDir, "index.lock"))
			}()
		}
		runCommitIndexGit(t, wt, []string{"GIT_INDEX_FILE=" + idx}, "rm", "--cached", "--", ".beads/issues.jsonl")
		return idx
	}

	t.Run("linked worktree, export git rm'd", func(t *testing.T) {
		for _, plain := range []bool{true, false} {
			name := "plain commit envelope"
			if !plain {
				name = "pathspec commit envelope"
			}
			t.Run(name, func(t *testing.T) {
				repo, _, _ := newCommitIndexRepo(t)
				commitIndexWrite(t, filepath.Join(repo, ".beads", "metadata.json"), []byte("{}\n"))
				runCommitIndexGit(t, repo, nil, "add", "--", ".beads/metadata.json")
				runCommitIndexGit(t, repo, nil, "commit", "-qm", "metadata")
				wt, wtGitDir, wtExport := addCommitIndexWorktree(t, repo)
				pending := linkedWorktreeDeletionEnvelope(t, wt, wtGitDir, plain)

				t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))
				initConfigForTest(t)
				config.Set("export.auto", true)
				config.Set("export.git-add", true)
				config.Set("export.path", "issues.jsonl")
				config.Set("no-git-ops", false)
				config.Set("sync.remote", "")
				config.Set("sync.git-remote", "")

				if err := os.RemoveAll(sentinel); err != nil {
					t.Fatal(err)
				}
				// Enter the linked worktree the way git enters a hook: GIT_DIR
				// names the per-worktree admin dir, cwd is the worktree top,
				// and store discovery has fallen back to the primary
				// checkout's shared .beads (BEADS_DIR above).
				t.Setenv("GIT_DIR", wtGitDir)
				t.Chdir(wt)
				t.Setenv("GIT_INDEX_FILE", pending)

				_ = captureHookStderr(t, exportJSONLForCommit)

				if _, err := os.Stat(sentinel); err == nil {
					t.Fatalf("export ran although the export file is staged for deletion:\n%s", readFileForWorktreeTest(t, sentinel))
				}
				deletions := runCommitIndexGit(t, wt, []string{"GIT_INDEX_FILE=" + pending},
					"diff", "--cached", "--diff-filter=D", "--name-only", "--", ".beads/issues.jsonl")
				if !strings.Contains(deletions, ".beads/issues.jsonl") {
					t.Fatalf("pending index no longer stages the export's deletion:\n%s", deletions)
				}
				if _, err := os.Stat(wtExport); err != nil {
					t.Fatalf("export file missing from the worktree: %v", err)
				}
			})
		}
	})
}

// TestIsExportFileStagedForDeletion_LinkedWorktree pins AC-3: in a linked
// worktree the inherited GIT_DIR used to make git treat the guard's cwd
// (.beads) as the worktree top, so the diff never matched the staged
// deletion and the pre-commit lane revived a git rm'd export. With the env
// from gitIndexEnv the pending index is read and the deletion is seen.
func TestIsExportFileStagedForDeletion_LinkedWorktree(t *testing.T) {
	repo, _, _ := newCommitIndexRepo(t)
	wt, wtGitDir, wtExport := addCommitIndexWorktree(t, repo)
	// The pending index stages the export's deletion.
	pending := filepath.Join(wtGitDir, "next-index-4242.lock")
	runCommitIndexGit(t, wt, []string{"GIT_INDEX_FILE=" + pending}, "read-tree", "HEAD")
	runCommitIndexGit(t, wt, []string{"GIT_INDEX_FILE=" + pending}, "rm", "--cached", "--", ".beads/issues.jsonl")

	// The hook envelope of a linked worktree: GIT_DIR names the per-worktree
	// admin dir, cwd is the worktree top.
	t.Setenv("GIT_DIR", wtGitDir)
	t.Chdir(wt)
	t.Setenv("GIT_INDEX_FILE", pending)

	if !isExportFileStagedForDeletion(wtExport) {
		t.Fatal("isExportFileStagedForDeletion = false, want the staged deletion in the pending index to be seen")
	}
}

// TestGitIndexEnv_Containment pins AC-4: an inherited GIT_INDEX_FILE is only
// honored when it names an existing regular file directly inside the
// staging repo's git dir. Every other shape — another repo's index, a
// nested repo's or submodule-style index, a plain worktree file, a missing
// next-index, a symlink — must be left untouched while the export stages
// into the discovered <gitdir>/index. These rows also guard against the
// naive fix of dropping GIT_INDEX_FILE from scrubGitHookEnv outright.
func TestGitIndexEnv_Containment(t *testing.T) {
	rows := []struct {
		name        string
		chdirSubdir bool
		candidate   func(t *testing.T, repo, gitDir string) string
	}{
		{
			name: "another repo's index",
			candidate: func(t *testing.T, repo, gitDir string) string {
				other := filepath.Join(filepath.Dir(repo), "other")
				if err := os.MkdirAll(other, 0o755); err != nil {
					t.Fatal(err)
				}
				runCommitIndexGit(t, other, nil, "init", "-q")
				commitIndexWrite(t, filepath.Join(other, "tracked.txt"), []byte("v1\n"))
				runCommitIndexGit(t, other, nil, "add", "--", "tracked.txt")
				return filepath.Join(other, ".git", "index")
			},
		},
		{
			name: "nested repo's index",
			candidate: func(t *testing.T, repo, gitDir string) string {
				nested := filepath.Join(repo, "vendor", "sub")
				if err := os.MkdirAll(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				runCommitIndexGit(t, nested, nil, "init", "-q")
				commitIndexWrite(t, filepath.Join(nested, "tracked.txt"), []byte("v1\n"))
				runCommitIndexGit(t, nested, nil, "add", "--", "tracked.txt")
				return filepath.Join(nested, ".git", "index")
			},
		},
		{
			name: "submodule-style index below the git dir",
			candidate: func(t *testing.T, repo, gitDir string) string {
				// A descendant of the git dir, not a direct child: only the
				// parent-directory identity check rejects it.
				if err := os.MkdirAll(filepath.Join(gitDir, "modules", "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				candidate := filepath.Join(gitDir, "modules", "x", "index")
				commitIndexCopy(t, filepath.Join(gitDir, "index"), candidate)
				return candidate
			},
		},
		{
			name: "a worktree file",
			candidate: func(t *testing.T, repo, gitDir string) string {
				candidate := filepath.Join(repo, "my-index")
				commitIndexCopy(t, filepath.Join(gitDir, "index"), candidate)
				return candidate
			},
		},
		{
			name: "missing next-index file",
			candidate: func(t *testing.T, repo, gitDir string) string {
				return filepath.Join(gitDir, "next-index-999.lock")
			},
		},
		{
			name: "symlink in the git dir",
			candidate: func(t *testing.T, repo, gitDir string) string {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation needs privileges on Windows")
				}
				other := filepath.Join(filepath.Dir(repo), "other")
				if err := os.MkdirAll(other, 0o755); err != nil {
					t.Fatal(err)
				}
				runCommitIndexGit(t, other, nil, "init", "-q")
				commitIndexWrite(t, filepath.Join(other, "tracked.txt"), []byte("v1\n"))
				runCommitIndexGit(t, other, nil, "add", "--", "tracked.txt")
				link := filepath.Join(gitDir, "link-index")
				if err := os.Symlink(filepath.Join(other, ".git", "index"), link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "relative .git/index with cwd in a subdirectory",
			candidate: func(t *testing.T, repo, gitDir string) string {
				if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(".git", "index")
			},
			// The value resolves against the process cwd (a hook's worktree
			// top); from repo/sub it names a missing repo/sub/.git/index.
			chdirSubdir: true,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			repo, gitDir, exportPath := newCommitIndexRepo(t)
			commitIndexWrite(t, exportPath, []byte(`{"id":"v2"}`+"\n"))
			candidate := row.candidate(t, repo, gitDir)
			var before []byte
			if data, err := os.ReadFile(candidate); err == nil {
				before = data
			}

			if err := os.Unsetenv("GIT_DIR"); err != nil {
				t.Fatal(err)
			}
			if row.chdirSubdir {
				t.Chdir(filepath.Join(repo, "sub"))
			} else {
				t.Chdir(repo)
			}
			t.Setenv("GIT_INDEX_FILE", candidate)

			if err := gitAddFile(exportPath); err != nil {
				t.Fatalf("gitAddFile with an unhonored GIT_INDEX_FILE: %v", err)
			}
			assertCommitIndexHasExport(t, repo, filepath.Join(gitDir, "index"), exportPath)
			after, statErr := os.ReadFile(candidate)
			if before == nil {
				if statErr == nil {
					t.Fatalf("candidate %s was created", candidate)
				}
			} else if statErr != nil || string(after) != string(before) {
				t.Fatalf("candidate %s was modified", candidate)
			}
		})
	}

	// The inherited value is matched with the same key policy as the scrub:
	// a mixed-case Git_Index_File is honored only under the windows policy,
	// and exactly one GIT_INDEX_FILE entry comes back in that case.
	checkHookEnvForBothHosts(t, func(t *testing.T) {
		repo, gitDir, _ := newCommitIndexRepo(t)
		next := filepath.Join(gitDir, "next-index-4242.lock")
		commitIndexCopy(t, filepath.Join(gitDir, "index"), next)
		if err := os.Unsetenv("GIT_INDEX_FILE"); err != nil {
			t.Fatal(err)
		}
		if err := os.Unsetenv("GIT_DIR"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("Git_Index_File", next)

		env, indexFile, err := gitIndexEnv(repo)
		if err != nil {
			t.Fatalf("gitIndexEnv: %v", err)
		}
		keys := 0
		for _, entry := range env {
			if key, _, ok := strings.Cut(entry, "="); ok &&
				execenv.KeyEqualForOS(key, "GIT_INDEX_FILE", hookEnvGOOS) {
				keys++
			}
		}
		if hookEnvGOOS == "windows" {
			if indexFile != next {
				t.Fatalf("indexFile = %q, want the inherited %q", indexFile, next)
			}
			if keys != 1 {
				t.Fatalf("returned env has %d GIT_INDEX_FILE entries, want exactly 1", keys)
			}
		} else {
			if indexFile != filepath.Join(gitDir, "index") {
				t.Fatalf("indexFile = %q, want the discovered git dir index", indexFile)
			}
			if keys != 0 {
				t.Fatalf("returned env has %d GIT_INDEX_FILE entries, want 0", keys)
			}
		}
	})
}

// TestGitAddFile_LockPreflightTargetsCommitIndex pins AC-5: the lock
// preflight must check <index>.lock for the index git add will actually
// use. In a pathspec envelope the parent legitimately holds
// <gitdir>/index.lock for the whole hook — the preflight cannot refuse on
// that lock, but it must still refuse when the pending index's own lock
// (<next>.lock.lock) is held. The no-GIT_INDEX_FILE half of AC-5 is pinned
// by TestGitAddFile_CapturesLockedIndexFailure.
func TestGitAddFile_LockPreflightTargetsCommitIndex(t *testing.T) {
	repo, gitDir, exportPath := newCommitIndexRepo(t)
	pending := commitIndexPathspecEnvelope(t, repo, gitDir)
	commitIndexWrite(t, exportPath, []byte(`{"id":"v2"}`+"\n"))
	// Another git process is writing the pending index itself.
	pendingLock := pending + ".lock"
	if err := os.WriteFile(pendingLock, []byte("held by another git process"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := commitIndexRead(t, pending)

	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("GIT_INDEX_FILE", pending)

	err := gitAddFile(exportPath)
	if err == nil {
		t.Fatal("expected gitAddFile to fail while the pending index's lock is held, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "index is locked") || !strings.Contains(msg, pendingLock) {
		t.Fatalf("expected the error to name the pending index's lock %s, got: %q", pendingLock, msg)
	}
	if got := commitIndexRead(t, pending); string(got) != string(before) {
		t.Fatal("gitAddFile staged into the pending index despite the lock")
	}
}
