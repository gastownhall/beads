package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/execenv"
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
// for `git commit -- <paths>` also in that index.lock, which git installs as
// the new index after the commit. No other index file in the git dir changes.
func TestGitAddFile_StagesIntoCommitIndex(t *testing.T) {
	tests := []struct {
		name   string
		linked bool
		// envelope returns the GIT_INDEX_FILE git hands the hook, the other
		// index files that must get the export too, and those that must not
		// change (or appear).
		envelope func(*testing.T, commitIndexRepo) (indexFile string, alsoStaged, unchanged []string)
	}{
		{
			name: "main worktree, git commit -- <paths>",
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string, []string) {
				return r.pathspecEnvelope(t, "tracked.txt"), []string{filepath.Join(r.gitDir, "index.lock")}, []string{filepath.Join(r.gitDir, "index")}
			},
		},
		{
			name:   "linked worktree, git commit -- <paths>",
			linked: true,
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string, []string) {
				return r.pathspecEnvelope(t, "tracked.txt"), []string{filepath.Join(r.gitDir, "index.lock")}, []string{filepath.Join(r.gitDir, "index")}
			},
		},
		{
			name: "main worktree, git commit -a",
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string, []string) {
				return r.allEnvelope(t), nil, []string{filepath.Join(r.gitDir, "index")}
			},
		},
		{
			// No regression: git's relative value, resolved against the hook cwd.
			name: "main worktree, plain git commit",
			envelope: func(t *testing.T, r commitIndexRepo) (string, []string, []string) {
				return filepath.Join(".git", "index"), nil, []string{filepath.Join(r.gitDir, "index.lock")}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, tt.linked)
			writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
			indexFile, alsoStaged, unchanged := tt.envelope(t, r)
			// bd export rewrites the file after git has built the envelope.
			export := r.path(".beads/issues.jsonl")
			writeFileForWorktreeTest(t, export, []byte(`{"id":"bd-exported"}`+"\n"))
			want := r.blobOf(t, ".beads/issues.jsonl")
			before := snapshotIndexFiles(t, unchanged...)

			r.enterHook(t, indexFile)
			if err := gitAddFile(export); err != nil {
				t.Fatalf("gitAddFile: %v", err)
			}

			absIndex := indexFile
			if !filepath.IsAbs(absIndex) {
				absIndex = filepath.Join(r.top, absIndex)
			}
			for _, staged := range append([]string{absIndex}, alsoStaged...) {
				if got := r.stagedBlob(t, staged, ".beads/issues.jsonl"); got != want {
					t.Errorf("export blob in %s = %q, want %q", staged, got, want)
				}
			}
			assertIndexFilesUnchanged(t, before)
		})
	}
}

func shellQuoteForIndexTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// installExportStub puts a `bd` on PATH that records each invocation in the
// returned sentinel and emulates `bd export -o <path>`. Any other invocation
// runs fallback, or exits 0 when fallback is "".
func installExportStub(t *testing.T, fallback string) string {
	t.Helper()
	binDir := t.TempDir()
	sentinel := filepath.Join(binDir, "invocations")
	stub := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> ` + shellQuoteForIndexTest(sentinel) + "\n" +
		`if [ "$1" = export ] && [ "$2" = -o ]; then printf '%s\n' '{"id":"bd-exported"}' > "$3"; exit 0; fi` + "\n"
	if fallback != "" {
		stub += "exec " + shellQuoteForIndexTest(fallback) + ` "$@"` + "\n"
	}
	stub += "exit 0\n"
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
	sentinel := installExportStub(t, "")
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
		before := snapshotIndexFiles(t, filepath.Join(r.gitDir, "index"))
		r.enterHook(t, next)

		_ = captureHookStderr(t, exportJSONLForCommit)

		export := r.path(".beads/issues.jsonl")
		if got, want := readFileForWorktreeTest(t, sentinel), "export -o "+export+"\n"; got != want {
			t.Fatalf("export subprocess = %q, want %q", got, want)
		}
		// index.lock is the index git installs once the commit succeeds.
		want := r.blobOf(t, ".beads/issues.jsonl")
		for _, index := range []string{next, filepath.Join(r.gitDir, "index.lock")} {
			if got := r.stagedBlob(t, index, ".beads/issues.jsonl"); got != want {
				t.Errorf("export blob in %s = %q, want %q", index, got, want)
			}
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

// gitRmExport stages the deletion of the export in the real index, as
// `git rm [flags] .beads/issues.jsonl` does.
func gitRmExport(t *testing.T, r commitIndexRepo, flags ...string) {
	t.Helper()
	args := append(append([]string{"rm", "-q"}, flags...), "--", ".beads/issues.jsonl")
	r.git(t, nil, args...)
}

func stagedDeletions(t *testing.T, r commitIndexRepo, indexFile string) string {
	t.Helper()
	return r.git(t, []string{"GIT_INDEX_FILE=" + indexFile}, "diff", "--cached", "--diff-filter=D", "--name-only")
}

// TestIsExportFileStagedForDeletion_LinkedWorktree pins AC-3's guard: a
// linked-worktree hook inherits an absolute GIT_DIR, which must not stop the
// guard from finding the export's staged deletion.
func TestIsExportFileStagedForDeletion_LinkedWorktree(t *testing.T) {
	r := newCommitIndexRepo(t, true)
	gitRmExport(t, r)
	r.enterHook(t, filepath.Join(r.gitDir, "index"))
	if !isExportFileStagedForDeletion(r.path(".beads/issues.jsonl")) {
		t.Error("isExportFileStagedForDeletion = false, want true for a git rm'd export")
	}
}

// TestExportJSONLForCommit_KeepsStagedDeletion pins AC-3 end to end: an export
// the commit deletes is neither re-exported nor re-staged. With `git rm` the
// deletion is in <gitdir>/index too; after a plain `rm`, only the index of
// `git commit -a` or `git commit -- <export>` has it, so the guard must read
// that one.
func TestExportJSONLForCommit_KeepsStagedDeletion(t *testing.T) {
	plain := func(t *testing.T, r commitIndexRepo) string { return filepath.Join(r.gitDir, "index") }
	pathspec := func(t *testing.T, r commitIndexRepo) string {
		return r.pathspecEnvelope(t, ".beads/issues.jsonl")
	}
	all := func(t *testing.T, r commitIndexRepo) string { return r.allEnvelope(t) }
	tests := []struct {
		name     string
		linked   bool
		gitRm    bool
		envelope func(*testing.T, commitIndexRepo) string
	}{
		{name: "linked worktree, git rm, plain git commit", linked: true, gitRm: true, envelope: plain},
		{name: "linked worktree, git rm, git commit -- <export>", linked: true, gitRm: true, envelope: pathspec},
		{name: "main worktree, rm, git commit -- <export>", envelope: pathspec},
		{name: "linked worktree, rm, git commit -- <export>", linked: true, envelope: pathspec},
		{name: "main worktree, rm, git commit -a", envelope: all},
		{name: "linked worktree, rm, git commit -a", linked: true, envelope: all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, tt.linked)
			sentinel := setupExportLaneForIndexTest(t, r)
			if tt.gitRm {
				gitRmExport(t, r)
			} else if err := os.Remove(r.path(".beads/issues.jsonl")); err != nil {
				t.Fatal(err)
			}
			indexFile := tt.envelope(t, r)
			if got := stagedDeletions(t, r, indexFile); !strings.Contains(got, ".beads/issues.jsonl") {
				t.Fatalf("fixture: pending index does not stage the deletion: %q", got)
			}
			if !tt.gitRm {
				if got := stagedDeletions(t, r, filepath.Join(r.gitDir, "index")); got != "" {
					t.Fatalf("fixture: <gitdir>/index stages a deletion: %q", got)
				}
			}
			before := snapshotIndexFiles(t, indexFile, filepath.Join(r.gitDir, "index"), filepath.Join(r.gitDir, "index.lock"))
			r.enterHook(t, indexFile)

			_ = captureHookStderr(t, exportJSONLForCommit)

			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Errorf("export ran (%v) although the commit deletes the export", err)
			}
			if got := stagedDeletions(t, r, indexFile); !strings.Contains(got, ".beads/issues.jsonl") {
				t.Errorf("pending index no longer stages the deletion: %q", got)
			}
			assertIndexFilesUnchanged(t, before)
		})
	}
}

// TestExportJSONLForCommit_KeepsDeletionOutsidePathspec pins AC-3 for a
// `git commit -- <paths>` that leaves a git rm'd export out. The commit's index
// is HEAD plus the paths, so only index.lock, which git installs as the index
// after the commit, stages the deletion. The guard must read it: otherwise the
// export runs and gitAddFile stages it into index.lock, reviving the file the
// user removed (GH#3838).
func TestExportJSONLForCommit_KeepsDeletionOutsidePathspec(t *testing.T) {
	tests := []struct {
		name    string
		linked  bool
		rmFlags []string
	}{
		{name: "main worktree, git rm"},
		{name: "main worktree, git rm --cached", rmFlags: []string{"--cached"}},
		{name: "linked worktree, git rm", linked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, tt.linked)
			sentinel := setupExportLaneForIndexTest(t, r)
			gitRmExport(t, r, tt.rmFlags...)
			writeFileForWorktreeTest(t, r.path(".beads/config.yaml"), []byte("# pathspec\n"))
			next := r.pathspecEnvelope(t, ".beads/config.yaml")
			realIndex := filepath.Join(r.gitDir, "index.lock")
			if got := stagedDeletions(t, r, next); got != "" {
				t.Fatalf("fixture: the commit's index stages a deletion: %q", got)
			}
			if got := stagedDeletions(t, r, realIndex); !strings.Contains(got, ".beads/issues.jsonl") {
				t.Fatalf("fixture: index.lock does not stage the deletion: %q", got)
			}
			before := snapshotIndexFiles(t, next, realIndex, filepath.Join(r.gitDir, "index"))
			r.enterHook(t, next)

			_ = captureHookStderr(t, exportJSONLForCommit)

			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Errorf("export ran (%v) although index.lock stages the export's deletion", err)
			}
			assertIndexFilesUnchanged(t, before)
		})
	}
}

// TestGitIndexEnv_Containment pins AC-4: an inherited GIT_INDEX_FILE that is
// not a regular file directly inside the staging repo's git dir is ignored,
// never created or written, and the export is staged into <gitdir>/index.
func TestGitIndexEnv_Containment(t *testing.T) {
	tests := []struct {
		name string
		// candidate returns the GIT_INDEX_FILE value and the file that must
		// stay unchanged (or absent).
		candidate func(*testing.T, commitIndexRepo) (value, guarded string)
		cwd       string
	}{
		{
			name: "another repository's index",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				other := newCommitIndexRepo(t, false)
				p := filepath.Join(other.gitDir, "index")
				return p, p
			},
		},
		{
			name: "a nested repository's index",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				sub := r.path("vendor/sub")
				mkdirs(t, sub)
				runGitForWorktreeTest(t, sub, "init", "-q")
				writeFileForWorktreeTest(t, filepath.Join(sub, "f"), []byte("f\n"))
				runGitForWorktreeTest(t, sub, "add", "f")
				p := filepath.Join(sub, ".git", "index")
				return p, p
			},
		},
		{
			name: "an index below the git dir, not directly in it",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				p := filepath.Join(r.gitDir, "modules", "x", "index")
				mkdirs(t, filepath.Dir(p))
				copyFileForIndexTest(t, filepath.Join(r.gitDir, "index"), p)
				return p, p
			},
		},
		{
			name: "a file in the worktree",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				p := r.path("my-index")
				copyFileForIndexTest(t, filepath.Join(r.gitDir, "index"), p)
				return p, p
			},
		},
		{
			name: "a missing next-index file",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				p := filepath.Join(r.gitDir, "next-index-999.lock")
				return p, p
			},
		},
		{
			name: "a symlink in the git dir to a foreign index",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				other := newCommitIndexRepo(t, false)
				target := filepath.Join(other.gitDir, "index")
				link := filepath.Join(r.gitDir, "foreign-index")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return link, target
			},
		},
		{
			// Relative values resolve against the hook cwd, here a
			// subdirectory, so .git/index names nothing.
			name: "a relative .git/index from a subdirectory",
			cwd:  "sub",
			candidate: func(t *testing.T, r commitIndexRepo) (string, string) {
				mkdirs(t, r.path("sub"))
				return filepath.Join(".git", "index"), r.path("sub/.git/index")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, false)
			value, guarded := tt.candidate(t, r)
			export := r.path(".beads/issues.jsonl")
			writeFileForWorktreeTest(t, export, []byte(`{"id":"bd-exported"}`+"\n"))
			want := r.blobOf(t, ".beads/issues.jsonl")
			before := snapshotIndexFiles(t, guarded)

			r.enterHook(t, value)
			if tt.cwd != "" {
				t.Chdir(r.path(tt.cwd))
			}
			if err := gitAddFile(export); err != nil {
				t.Fatalf("gitAddFile: %v", err)
			}

			assertIndexFilesUnchanged(t, before)
			if got := r.stagedBlob(t, filepath.Join(r.gitDir, "index"), ".beads/issues.jsonl"); got != want {
				t.Errorf("export blob in <gitdir>/index = %q, want %q", got, want)
			}
		})
	}
}

// TestGitIndexEnv_KeyPolicy pins AC-4's key identity: the inherited value is
// read with the same key policy the scrub drops it with, so a Windows
// Git_Index_File is honored and replaced by exactly one GIT_INDEX_FILE entry.
func TestGitIndexEnv_KeyPolicy(t *testing.T) {
	r := newCommitIndexRepo(t, false)
	writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
	next := r.pathspecEnvelope(t, "tracked.txt")
	r.enterHook(t, "")
	unsetEnvForIndexTest(t, "GIT_INDEX_FILE")
	t.Setenv("Git_Index_File", next)

	checkHookEnvForBothHosts(t, func(t *testing.T) {
		env, indexFile, err := gitIndexEnv(r.path(".beads"))
		if err != nil {
			t.Fatalf("gitIndexEnv: %v", err)
		}
		var entries []string
		for _, entry := range env {
			if execenv.KeyEqualForOS(execenv.EntryKey(entry), "GIT_INDEX_FILE", hookEnvGOOS) {
				entries = append(entries, entry)
			}
		}
		if hookEnvGOOS == "windows" {
			if indexFile != next {
				t.Errorf("index file = %q, want the inherited %q", indexFile, next)
			}
			if want := []string{"GIT_INDEX_FILE=" + next}; !slices.Equal(entries, want) {
				t.Errorf("GIT_INDEX_FILE entries = %q, want %q", entries, want)
			}
			return
		}
		if want := filepath.Join(r.gitDir, "index"); indexFile != want {
			t.Errorf("index file = %q, want %q: Git_Index_File is not GIT_INDEX_FILE on %s", indexFile, want, hookEnvGOOS)
		}
		if len(entries) != 0 {
			t.Errorf("GIT_INDEX_FILE entries = %q, want none", entries)
		}
	})
}

// TestGitAddFile_LockPreflightTargetsCommitIndex pins AC-5: the preflight
// checks the lock git add itself would take on each index it writes (the
// honored index and, for `git commit -- <paths>`, index.lock), not the
// parent's <gitdir>/index.lock, and stages into neither when one is held.
func TestGitAddFile_LockPreflightTargetsCommitIndex(t *testing.T) {
	tests := []struct {
		name string
		lock func(r commitIndexRepo, next string) string
	}{
		{name: "next-index lock held", lock: func(r commitIndexRepo, next string) string { return next + ".lock" }},
		{name: "index.lock lock held", lock: func(r commitIndexRepo, next string) string {
			return filepath.Join(r.gitDir, "index.lock.lock")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, false)
			writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
			next := r.pathspecEnvelope(t, "tracked.txt")
			held := tt.lock(r, next)
			if err := os.WriteFile(held, []byte("held by another git add"), 0o644); err != nil {
				t.Fatal(err)
			}
			export := r.path(".beads/issues.jsonl")
			writeFileForWorktreeTest(t, export, []byte(`{"id":"bd-exported"}`+"\n"))
			before := snapshotIndexFiles(t, next, filepath.Join(r.gitDir, "index.lock"), filepath.Join(r.gitDir, "index"))
			r.enterHook(t, next)

			err := gitAddFile(export)
			if err == nil {
				t.Fatal("gitAddFile = nil, want a locked-index error")
			}
			if msg := err.Error(); !strings.Contains(msg, "index is locked") || !strings.Contains(msg, held) {
				t.Errorf("error = %q, want it to name %s", msg, held)
			}
			assertIndexFilesUnchanged(t, before)
		})
	}
}

// TestGitAddFile_StagesRealIndexFirst pins stagingIndexes' order: when the
// commit index's git add fails, the export is already staged in index.lock,
// the index git installs, so it is left staged for the next commit instead of
// reverted by it.
func TestGitAddFile_StagesRealIndexFirst(t *testing.T) {
	r := newCommitIndexRepo(t, false)
	writeFileForWorktreeTest(t, r.path("tracked.txt"), []byte("changed\n"))
	next := r.pathspecEnvelope(t, "tracked.txt")
	// git add fails on a corrupt index.
	if err := os.WriteFile(next, []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	export := r.path(".beads/issues.jsonl")
	writeFileForWorktreeTest(t, export, []byte(`{"id":"bd-exported"}`+"\n"))
	want := r.blobOf(t, ".beads/issues.jsonl")
	r.enterHook(t, next)

	if err := gitAddFile(export); err == nil {
		t.Fatal("gitAddFile = nil, want the commit index's git add error")
	}
	if got := r.stagedBlob(t, filepath.Join(r.gitDir, "index.lock"), ".beads/issues.jsonl"); got != want {
		t.Errorf("export blob in index.lock = %q, want %q", got, want)
	}
}

// commitConfigThroughHook installs bd's pre-commit shim in r, with `bd export`
// stubbed in front of bd, runs a real `git commit -- .beads/config.yaml` with
// the export lane on, and returns the bd invocations the stub recorded.
func commitConfigThroughHook(t *testing.T, r commitIndexRepo, bd string) string {
	t.Helper()
	hooksDir := filepath.Join(r.main, ".git", "hooks")
	mkdirs(t, hooksDir)
	hook := "#!/usr/bin/env sh\n" + generateHookSection("pre-commit")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := installExportStub(t, bd)
	writeFileForWorktreeTest(t, r.path(".beads/config.yaml"), []byte("# pathspec\n"))

	r.git(t, []string{"BD_EXPORT_AUTO=true", "BD_EXPORT_GIT_ADD=true"},
		"commit", "-q", "-m", "pathspec", "--", ".beads/config.yaml")
	return readFileForWorktreeTest(t, sentinel)
}

// TestPreCommitHook_PathspecCommitLeavesIndexClean runs a real
// `git commit -- .beads/config.yaml` through the installed pre-commit shim and
// the real bd (only `bd export` is stubbed: the repository has no database).
// The export must reach both the commit and the index git installs after it.
// Otherwise the old blob stays staged against the new HEAD and the next
// commit that does not export reverts it, or, for a first export, a staged
// deletion is left that the GH#3838 guard then honors (GH#4080).
func TestPreCommitHook_PathspecCommitLeavesIndexClean(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary is POSIX-only")
	}
	bd := buildBDForTest(t)
	tests := []struct {
		name        string
		linked      bool
		firstExport bool
	}{
		{name: "main worktree, export tracked"},
		{name: "main worktree, first export", firstExport: true},
		{name: "linked worktree, export tracked", linked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, tt.linked)
			if tt.firstExport {
				r.git(t, nil, "rm", "-q", "--", ".beads/issues.jsonl")
				r.git(t, nil, "commit", "-q", "-m", "drop the export")
			}

			invocations := commitConfigThroughHook(t, r, bd)

			export := r.path(".beads/issues.jsonl")
			if !strings.Contains(invocations, "export -o "+export+"\n") {
				t.Fatalf("bd invocations = %q, want the hook's export of %s", invocations, export)
			}
			if got, want := r.git(t, nil, "show", "HEAD:.beads/issues.jsonl"), `{"id":"bd-exported"}`+"\n"; got != want {
				t.Errorf("committed export = %q, want %q", got, want)
			}
			if got := r.git(t, nil, "status", "--porcelain", "--", ".beads/issues.jsonl"); got != "" {
				t.Errorf("git status after the commit = %q, want the export clean", got)
			}
		})
	}
}

// TestPreCommitHook_PathspecCommitKeepsStagedDeletion runs a real
// `git commit -- .beads/config.yaml` after the export's deletion was staged.
// Only the index git installs after the commit holds that deletion, so the
// hook must neither export nor stage the file: the commit keeps HEAD's export
// and the deletion stays staged for the next commit (GH#3838, GH#4080).
func TestPreCommitHook_PathspecCommitKeepsStagedDeletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary is POSIX-only")
	}
	bd := buildBDForTest(t)
	tests := []struct {
		name    string
		linked  bool
		rmFlags []string
		status  string // git status --porcelain -- .beads/issues.jsonl after the commit
	}{
		{name: "main worktree, git rm", status: "D  .beads/issues.jsonl\n"},
		{name: "main worktree, git rm --cached", rmFlags: []string{"--cached"},
			status: "D  .beads/issues.jsonl\n?? .beads/issues.jsonl\n"},
		{name: "linked worktree, git rm", linked: true, status: "D  .beads/issues.jsonl\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommitIndexRepo(t, tt.linked)
			gitRmExport(t, r, tt.rmFlags...)

			invocations := commitConfigThroughHook(t, r, bd)

			if !strings.Contains(invocations, "hooks run pre-commit") {
				t.Fatalf("bd invocations = %q, want the pre-commit hook", invocations)
			}
			if strings.Contains(invocations, "export -o") {
				t.Errorf("bd invocations = %q, want no export of a git rm'd file", invocations)
			}
			if got, want := r.git(t, nil, "show", "HEAD:.beads/config.yaml"), "# pathspec\n"; got != want {
				t.Errorf("committed config = %q, want %q", got, want)
			}
			if got, want := r.git(t, nil, "show", "HEAD:.beads/issues.jsonl"), `{"id":"bd-committed"}`+"\n"; got != want {
				t.Errorf("committed export = %q, want HEAD's %q", got, want)
			}
			if got := r.git(t, nil, "status", "--porcelain", "--", ".beads/issues.jsonl"); got != tt.status {
				t.Errorf("git status after the commit = %q, want %q", got, tt.status)
			}
		})
	}
}
