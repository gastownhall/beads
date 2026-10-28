package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// These tests build the index envelope git hands a pre-commit hook (GH#4080),
// as git 2.43 creates it, without running a real hook:
//
//   - plain `git commit`: GIT_INDEX_FILE=.git/index, no lock held.
//   - `git commit -a`/`-i`/`-p`: GIT_INDEX_FILE=<gitdir>/index.lock, which is
//     both the index being committed and the lock the parent holds.
//   - `git commit -- <paths>`: GIT_INDEX_FILE=<gitdir>/next-index-<pid>.lock
//     (HEAD plus the paths), while the parent holds <gitdir>/index.lock.
//
// Linked-worktree hooks also inherit an absolute GIT_DIR; main-worktree hooks
// inherit none. The hook's cwd is the worktree top.

// commitIndexRepo is a repository with tracked.txt and a committed .beads
// directory (issues.jsonl, config.yaml, metadata.json).
type commitIndexRepo struct {
	main   string // primary checkout
	top    string // worktree the hook runs in (main, or a linked worktree)
	gitDir string // absolute git dir of top
	linked bool
}

func newCommitIndexRepo(t *testing.T, linked bool) commitIndexRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := commitIndexRepo{main: filepath.Join(root, "main"), linked: linked}
	writeFileForWorktreeTest(t, filepath.Join(r.main, "tracked.txt"), []byte("x\n"))
	writeFileForWorktreeTest(t, filepath.Join(r.main, ".beads", "issues.jsonl"), []byte(`{"id":"bd-committed"}`+"\n"))
	writeFileForWorktreeTest(t, filepath.Join(r.main, ".beads", "config.yaml"), []byte("# committed\n"))
	// BEADS_DIR is only honored for a directory that looks like a project.
	writeFileForWorktreeTest(t, filepath.Join(r.main, ".beads", "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, r.main, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, r.main, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, r.main, "config", "user.name", "test")
	runGitForWorktreeTest(t, r.main, "add", ".")
	runGitForWorktreeTest(t, r.main, "commit", "-q", "-m", "init")
	r.top, r.gitDir = r.main, filepath.Join(r.main, ".git")
	if linked {
		r.top = filepath.Join(root, "wt")
		runGitForWorktreeTest(t, r.main, "worktree", "add", "-q", r.top, "-b", "wt")
		r.gitDir = filepath.Join(r.main, ".git", "worktrees", "wt")
	}
	return r
}

func (r commitIndexRepo) path(rel string) string {
	return filepath.Join(r.top, filepath.FromSlash(rel))
}

// git runs git in the hook worktree with a scrubbed env plus extra, so the
// fixture never follows the envelope the test process is about to inherit.
func (r commitIndexRepo) git(t *testing.T, extra []string, args ...string) string {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = r.top
	cmd.Env = append(scrubGitHookEnv(os.Environ()), extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// pathspecEnvelope mirrors `git commit -- <paths>`: the pending index is HEAD
// plus paths, and the parent holds index.lock (the real index plus paths).
func (r commitIndexRepo) pathspecEnvelope(t *testing.T, paths ...string) string {
	t.Helper()
	next := filepath.Join(r.gitDir, "next-index-4242.lock")
	r.git(t, []string{"GIT_INDEX_FILE=" + next}, "read-tree", "HEAD")
	lock := filepath.Join(r.gitDir, "index.lock")
	copyFileForIndexTest(t, filepath.Join(r.gitDir, "index"), lock)
	if len(paths) > 0 {
		// update-index, unlike git add, also stages a path already removed
		// from both the worktree and the real index.
		args := append([]string{"update-index", "--add", "--remove", "--"}, paths...)
		r.git(t, []string{"GIT_INDEX_FILE=" + next}, args...)
		r.git(t, []string{"GIT_INDEX_FILE=" + lock}, args...)
	}
	return next
}

// allEnvelope mirrors `git commit -a`: the parent holds index.lock and that
// lock file is the index being committed.
func (r commitIndexRepo) allEnvelope(t *testing.T) string {
	t.Helper()
	lock := filepath.Join(r.gitDir, "index.lock")
	copyFileForIndexTest(t, filepath.Join(r.gitDir, "index"), lock)
	r.git(t, []string{"GIT_INDEX_FILE=" + lock}, "add", "-u")
	return lock
}

// enterHook makes the test process look like a pre-commit hook of r with the
// given GIT_INDEX_FILE. Call it after every fixture git command.
func (r commitIndexRepo) enterHook(t *testing.T, indexFile string) {
	t.Helper()
	if r.linked {
		t.Setenv("GIT_DIR", r.gitDir)
	} else {
		unsetEnvForIndexTest(t, "GIT_DIR")
	}
	t.Setenv("GIT_INDEX_FILE", indexFile)
	t.Chdir(r.top)
}

// stagedBlob returns the blob staged for rel in indexFile, or "" when absent.
func (r commitIndexRepo) stagedBlob(t *testing.T, indexFile, rel string) string {
	t.Helper()
	fields := strings.Fields(r.git(t, []string{"GIT_INDEX_FILE=" + indexFile}, "ls-files", "-s", "--", rel))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

func (r commitIndexRepo) blobOf(t *testing.T, rel string) string {
	t.Helper()
	return strings.TrimSpace(r.git(t, nil, "hash-object", "--", rel))
}

// unsetEnvForIndexTest unsets key for the rest of the test and restores it after.
func unsetEnvForIndexTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func copyFileForIndexTest(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshotIndexFiles reads every path, recording a missing one as nil.
func snapshotIndexFiles(t *testing.T, paths ...string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out[p] = data
	}
	return out
}

func assertIndexFilesUnchanged(t *testing.T, before map[string][]byte) {
	t.Helper()
	for p, want := range before {
		got, err := os.ReadFile(p)
		switch {
		case want == nil && err == nil:
			t.Errorf("%s was created", p)
		case want != nil && err != nil:
			t.Errorf("%s: %v", p, err)
		case want != nil && !bytes.Equal(got, want):
			t.Errorf("%s changed", p)
		}
	}
}

// TestGitAddFile_StagesIntoCommitIndex pins AC-1 for gitAddFile: the export
// lands in the index git commits from, with the parent's index.lock held, and
// no other index file in the git dir changes.
func TestGitAddFile_StagesIntoCommitIndex(t *testing.T) {
	tests := []struct {
		name     string
		linked   bool
		envelope func(*testing.T, commitIndexRepo) (indexFile string, others []string)
	}{
		{
			name: "main worktree, git commit -- <paths>",
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string) {
				return r.pathspecEnvelope(t, "tracked.txt"), []string{filepath.Join(r.gitDir, "index"), filepath.Join(r.gitDir, "index.lock")}
			},
		},
		{
			name:   "linked worktree, git commit -- <paths>",
			linked: true,
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string) {
				return r.pathspecEnvelope(t, "tracked.txt"), []string{filepath.Join(r.gitDir, "index"), filepath.Join(r.gitDir, "index.lock")}
			},
		},
		{
			name: "main worktree, git commit -a",
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string) {
				return r.allEnvelope(t), []string{filepath.Join(r.gitDir, "index")}
			},
		},
		{
			// No regression: git's relative value, resolved against the hook cwd.
			name: "main worktree, plain git commit",
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string) {
				return filepath.Join(".git", "index"), nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, tt.linked)
			writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
			indexFile, others := tt.envelope(t, r)
			// bd export rewrites the file after git has built the envelope.
			export := r.path(".beads/issues.jsonl")
			writeFileForWorktreeTest(t, export, []byte(`{"id":"bd-exported"}`+"\n"))
			want := r.blobOf(t, ".beads/issues.jsonl")
			before := snapshotIndexFiles(t, others...)

			r.enterHook(t, indexFile)
			if err := gitAddFile(export); err != nil {
				t.Fatalf("gitAddFile: %v", err)
			}

			absIndex := indexFile
			if !filepath.IsAbs(absIndex) {
				absIndex = filepath.Join(r.top, absIndex)
			}
			if got := r.stagedBlob(t, absIndex, ".beads/issues.jsonl"); got != want {
				t.Errorf("export blob in %s = %q, want %q", absIndex, got, want)
			}
			assertIndexFilesUnchanged(t, before)
		})
	}
}

// installExportStub puts a `bd` on PATH that records each invocation in the
// returned sentinel and emulates `bd export -o <path>`.
func installExportStub(t *testing.T) string {
	t.Helper()
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
	return sentinel
}

func setupExportLaneForIndexTest(t *testing.T, r commitIndexRepo) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary is POSIX-only")
	}
	sentinel := installExportStub(t)
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.git-add", true)
	config.Set("export.path", "issues.jsonl")
	config.Set("no-git-ops", false)
	t.Setenv("BEADS_DIR", r.path(".beads"))
	return sentinel
}

// TestPreCommitHasStagedBeadsFiles_ReadsCommitIndex pins AC-2 at unit level:
// the gate asks whether the commit being prepared touches .beads, not whether
// the on-disk index does.
func TestPreCommitHasStagedBeadsFiles_ReadsCommitIndex(t *testing.T) {
	t.Run("pathspec excludes .beads staged in the real index", func(t *testing.T) {
		r := newCommitIndexRepo(t, false)
		writeFileForWorktreeTest(t, r.path(".beads/config.yaml"), []byte("# staged\n"))
		r.git(t, nil, "add", ".beads/config.yaml")
		writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
		next := r.pathspecEnvelope(t, "tracked.txt")
		r.enterHook(t, next)
		if preCommitHasStagedBeadsFiles(r.path(".beads")) {
			t.Error("gate = true, want false: the pending commit has no .beads change")
		}
	})
	t.Run("pathspec includes .beads not staged in the real index", func(t *testing.T) {
		r := newCommitIndexRepo(t, false)
		writeFileForWorktreeTest(t, r.path(".beads/config.yaml"), []byte("# pathspec\n"))
		next := r.pathspecEnvelope(t, ".beads/config.yaml")
		r.enterHook(t, next)
		if !preCommitHasStagedBeadsFiles(r.path(".beads")) {
			t.Error("gate = false, want true: the pending commit changes .beads")
		}
	})
}

// TestExportJSONLForCommit_CommitIndex runs the whole pre-commit export lane
// against a pathspec envelope (AC-2, and AC-1 end to end).
func TestExportJSONLForCommit_CommitIndex(t *testing.T) {
	t.Run("pathspec includes .beads", func(t *testing.T) {
		r := newCommitIndexRepo(t, false)
		sentinel := setupExportLaneForIndexTest(t, r)
		writeFileForWorktreeTest(t, r.path(".beads/config.yaml"), []byte("# pathspec\n"))
		next := r.pathspecEnvelope(t, ".beads/config.yaml")
		before := snapshotIndexFiles(t, filepath.Join(r.gitDir, "index"), filepath.Join(r.gitDir, "index.lock"))
		r.enterHook(t, next)

		_ = captureHookStderr(t, exportJSONLForCommit)

		export := r.path(".beads/issues.jsonl")
		if got, want := readFileForWorktreeTest(t, sentinel), "export -o "+export+"\n"; got != want {
			t.Fatalf("export subprocess = %q, want %q", got, want)
		}
		if got, want := r.stagedBlob(t, next, ".beads/issues.jsonl"), r.blobOf(t, ".beads/issues.jsonl"); got != want {
			t.Errorf("export blob in pending index = %q, want %q", got, want)
		}
		assertIndexFilesUnchanged(t, before)
	})
	t.Run("pathspec excludes .beads", func(t *testing.T) {
		r := newCommitIndexRepo(t, false)
		sentinel := setupExportLaneForIndexTest(t, r)
		writeFileForWorktreeTest(t, r.path(".beads/config.yaml"), []byte("# staged\n"))
		r.git(t, nil, "add", ".beads/config.yaml")
		writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
		next := r.pathspecEnvelope(t, "tracked.txt")
		before := snapshotIndexFiles(t, next)
		r.enterHook(t, next)

		_ = captureHookStderr(t, exportJSONLForCommit)

		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Errorf("export ran (%v) for a commit that does not touch .beads", err)
		}
		assertIndexFilesUnchanged(t, before)
	})
}
