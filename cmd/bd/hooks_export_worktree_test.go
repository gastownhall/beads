package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// mkdirs creates every directory so pathInsideDir can resolve symlinks on both
// sides (macOS /tmp is a symlink, and an unresolvable side compares as outside).
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// runGitForWorktreeTest runs git in dir, failing the test on error. The env is
// scrubbed because the test process may itself be running under a git hook.
func runGitForWorktreeTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = scrubGitHookEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestWorktreeJSONLDir covers the gating matrix for the GH#6680 retarget.
// The rewrite must fire for a linked worktree sharing the primary checkout's
// store and for nothing else: hookWorkTreeRoot() is non-empty for every hook
// run, so an unconditional rewrite would hijack .beads/redirect, BEADS_DIR and
// above-the-repo topologies that resolve outside the committing worktree.
func TestWorktreeJSONLDir(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "main")
	worktree := filepath.Join(root, "wt")
	outside := filepath.Join(root, "shared-store", ".beads")
	mkdirs(t,
		filepath.Join(primary, ".beads"),
		filepath.Join(primary, "beads-store"),
		filepath.Join(primary, "nested", "beads-store"),
		filepath.Join(worktree, ".beads"),
		outside,
	)

	tests := []struct {
		name        string
		beadsDir    string
		hookRoot    string
		primaryRoot string
		want        string
	}{
		{
			name:        "linked worktree sharing the primary store retargets",
			beadsDir:    filepath.Join(primary, ".beads"),
			hookRoot:    worktree,
			primaryRoot: primary,
			want:        filepath.Join(worktree, ".beads"),
		},
		{
			name:        "a renamed top-level store mirrors to the same name",
			beadsDir:    filepath.Join(primary, "beads-store"),
			hookRoot:    worktree,
			primaryRoot: primary,
			want:        filepath.Join(worktree, "beads-store"),
		},
		{
			// The path the worktree tracks is nested/beads-store; mirroring
			// only the leaf would dump the store at a path no commit has ever
			// contained, and leave the tracked copy stale.
			name:        "a nested store keeps its path relative to the primary checkout",
			beadsDir:    filepath.Join(primary, "nested", "beads-store"),
			hookRoot:    worktree,
			primaryRoot: primary,
			want:        filepath.Join(worktree, "nested", "beads-store"),
		},
		{
			// beadsDir == primaryRoot: the store *is* the checkout root, so
			// there is no relative path to mirror. Decline rather than hand
			// back the worktree root itself.
			name:        "a store at the checkout root is never retargeted",
			beadsDir:    primary,
			hookRoot:    worktree,
			primaryRoot: primary,
			want:        primary,
		},
		{
			name:        "worktree owning its beads dir keeps it",
			beadsDir:    filepath.Join(worktree, ".beads"),
			hookRoot:    worktree,
			primaryRoot: primary,
			want:        filepath.Join(worktree, ".beads"),
		},
		{
			name:        "plain repo keeps its discovered beads dir",
			beadsDir:    filepath.Join(primary, ".beads"),
			hookRoot:    primary,
			primaryRoot: "",
			want:        filepath.Join(primary, ".beads"),
		},
		{
			name:        "plain repo redirecting out of the repo is never retargeted",
			beadsDir:    outside,
			hookRoot:    primary,
			primaryRoot: "",
			want:        outside,
		},
		{
			name:        "linked worktree redirecting out of the repo is never retargeted",
			beadsDir:    outside,
			hookRoot:    worktree,
			primaryRoot: primary,
			want:        outside,
		},
		{
			name:        "no hook context keeps the discovered beads dir",
			beadsDir:    outside,
			hookRoot:    "",
			primaryRoot: "",
			want:        outside,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worktreeJSONLDir(tt.beadsDir, tt.hookRoot, tt.primaryRoot)
			if got != tt.want {
				t.Fatalf("worktreeJSONLDir(%q, %q, %q) = %q, want %q",
					tt.beadsDir, tt.hookRoot, tt.primaryRoot, got, tt.want)
			}
		})
	}
}

// TestHookJSONLDirWithRealWorktrees pins the wiring, not just the helpers:
// it builds a real `git worktree add` fixture, enters it the way git enters a
// hook (GIT_DIR exported), and drives hookJSONLDir end to end. A regression
// that stops detecting the linked worktree — or that starts retargeting the
// primary checkout — fails here even though worktreeJSONLDir is untouched.
func TestHookJSONLDirWithRealWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(root, "main")
	mkdirs(t, filepath.Join(primary, ".beads"))

	runGitForWorktreeTest(t, primary, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, primary, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, primary, "config", "user.name", "test")
	runGitForWorktreeTest(t, primary, "commit", "-q", "--allow-empty", "-m", "init")

	worktree := filepath.Join(root, "wt")
	runGitForWorktreeTest(t, primary, "worktree", "add", "-q", worktree, "-b", "wt")

	primaryBeads := filepath.Join(primary, ".beads")

	// A pre-commit hook in the linked worktree: GIT_DIR points at the
	// per-worktree admin dir, and FindBeadsDir has fallen back to the
	// primary checkout's shared .beads.
	t.Setenv("GIT_DIR", filepath.Join(primary, ".git", "worktrees", "wt"))
	if got, want := hookJSONLDir(primaryBeads), filepath.Join(worktree, ".beads"); got != want {
		t.Errorf("linked worktree: hookJSONLDir(%q) = %q, want %q", primaryBeads, got, want)
	}

	// The same hook in the primary checkout must be left alone.
	t.Setenv("GIT_DIR", filepath.Join(primary, ".git"))
	if got := hookJSONLDir(primaryBeads); got != primaryBeads {
		t.Errorf("primary checkout: hookJSONLDir(%q) = %q, want it unchanged", primaryBeads, got)
	}

	// A redirect target outside the repository stays put even from the
	// linked worktree — retargeting it would strand the real store and add
	// an untracked dump to the commit.
	outside := filepath.Join(root, "shared-store", ".beads")
	mkdirs(t, outside)
	t.Setenv("GIT_DIR", filepath.Join(primary, ".git", "worktrees", "wt"))
	if got := hookJSONLDir(outside); got != outside {
		t.Errorf("out-of-repo store: hookJSONLDir(%q) = %q, want it unchanged", outside, got)
	}
}

// TestHookLinkedWorktreePrimaryRootNonRepo verifies the probe declines rather
// than guessing when it cannot describe the directory.
func TestHookLinkedWorktreePrimaryRootNonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if got := hookLinkedWorktreePrimaryRoot(""); got != "" {
		t.Errorf("empty hookRoot: got %q, want \"\"", got)
	}
	// GIT_CEILING_DIRECTORIES stops discovery from walking out of the temp
	// dir into a real repository that may enclose it.
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if got := hookLinkedWorktreePrimaryRoot(dir); got != "" {
		t.Errorf("non-repository hookRoot: got %q, want \"\"", got)
	}
}

// TestHookSubprocessEnvForcesHookSuppression pins the invariant the helper is
// named for: the `bd` subprocesses a hook shells out to must always see
// BD_GIT_HOOK=1 so their PersistentPostRun auto-export (and auto-backup) stay
// suppressed.
func TestHookSubprocessEnvForcesHookSuppression(t *testing.T) {
	t.Run("preserves an already-set marker exactly once", func(t *testing.T) {
		got := hookSubprocessEnv([]string{"PATH=/bin", "BD_GIT_HOOK=1", "GIT_DIR=/repo/.git"})
		if !slices.Contains(got, "BD_GIT_HOOK=1") {
			t.Fatalf("hookSubprocessEnv() = %#v, want BD_GIT_HOOK=1", got)
		}
		if !slices.Contains(got, "GIT_DIR=/repo/.git") {
			t.Fatalf("hookSubprocessEnv() = %#v, want git routing preserved", got)
		}
		count := 0
		for _, e := range got {
			if e == "BD_GIT_HOOK=1" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("hookSubprocessEnv() = %#v, want exactly one BD_GIT_HOOK entry", got)
		}
	})

	t.Run("sets the marker when the caller did not", func(t *testing.T) {
		got := hookSubprocessEnv([]string{"PATH=/bin"})
		if !slices.Contains(got, "BD_GIT_HOOK=1") {
			t.Fatalf("hookSubprocessEnv() = %#v, want BD_GIT_HOOK=1 forced", got)
		}
	})

	t.Run("overrides a conflicting value", func(t *testing.T) {
		got := hookSubprocessEnv([]string{"BD_GIT_HOOK=0"})
		if slices.Contains(got, "BD_GIT_HOOK=0") {
			t.Fatalf("hookSubprocessEnv() = %#v, want BD_GIT_HOOK=0 replaced", got)
		}
		if !slices.Contains(got, "BD_GIT_HOOK=1") {
			t.Fatalf("hookSubprocessEnv() = %#v, want BD_GIT_HOOK=1", got)
		}
	})
}

// TestHookLanesUseTheWorktreeJSONL pins the two call sites hookJSONLDir was
// added for, which the resolver's own tests cannot reach: reverting either of
// them to the discovered beadsDir — the whole user-visible fix — leaves every
// other test in this file green.
//
// A stub `bd` on PATH stands in for the real binary so the hook's subprocess
// is observable without a store: it records its argv and the BD_GIT_HOOK it
// inherited, and emulates `bd export -o <path>` by writing that file.
func TestHookLanesUseTheWorktreeJSONL(t *testing.T) {
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
	primary := filepath.Join(root, "main")
	primaryBeads := filepath.Join(primary, ".beads")
	primaryJSONL := filepath.Join(primaryBeads, "issues.jsonl")
	// BEADS_DIR is only honored for a directory that looks like a project.
	writeFileForWorktreeTest(t, filepath.Join(primaryBeads, "metadata.json"), []byte("{}\n"))
	writeFileForWorktreeTest(t, primaryJSONL, []byte(`{"id":"bd-primary"}`+"\n"))

	runGitForWorktreeTest(t, primary, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, primary, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, primary, "config", "user.name", "test")
	runGitForWorktreeTest(t, primary, "commit", "-q", "--allow-empty", "-m", "init")

	worktree := filepath.Join(root, "wt")
	runGitForWorktreeTest(t, primary, "worktree", "add", "-q", worktree, "-b", "wt")
	worktreeJSONL := filepath.Join(worktree, ".beads", "issues.jsonl")
	writeFileForWorktreeTest(t, worktreeJSONL, []byte(`{"id":"bd-worktree"}`+"\n"))
	// The pre-commit export only runs when the commit already touches .beads.
	// Stage a sibling for that gate so the JSONL's own staging stays a real
	// assertion below rather than something the fixture did.
	writeFileForWorktreeTest(t, filepath.Join(worktree, ".beads", "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, worktree, "add", ".beads/metadata.json")

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

	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.git-add", true)
	config.Set("export.path", "issues.jsonl")
	config.Set("no-git-ops", false)
	config.Set("import.auto", true)
	config.Set("sync.remote", "")
	config.Set("sync.git-remote", "")

	// Enter the linked worktree the way git enters a hook: GIT_DIR names the
	// per-worktree admin dir, and store discovery has fallen back to the
	// primary checkout's shared .beads.
	t.Setenv("GIT_DIR", filepath.Join(primary, ".git", "worktrees", "wt"))
	t.Setenv("BEADS_DIR", primaryBeads)

	t.Run("pre-commit exports the worktree's copy and leaves the primary's alone", func(t *testing.T) {
		if err := os.RemoveAll(sentinel); err != nil {
			t.Fatal(err)
		}
		_ = captureHookStderr(t, exportJSONLForCommit)

		if got, want := readFileForWorktreeTest(t, sentinel), "export -o "+worktreeJSONL+" BD_GIT_HOOK=1\n"; got != want {
			t.Fatalf("export subprocess = %q, want %q", got, want)
		}
		if got, want := readFileForWorktreeTest(t, worktreeJSONL), `{"id":"bd-exported"}`+"\n"; got != want {
			t.Errorf("worktree JSONL = %q, want the exported %q", got, want)
		}
		if got, want := readFileForWorktreeTest(t, primaryJSONL), `{"id":"bd-primary"}`+"\n"; got != want {
			t.Errorf("primary JSONL = %q, want it untouched %q", got, want)
		}
		if staged := stagedPathsForWorktreeTest(t, worktree); !slices.Contains(staged, ".beads/issues.jsonl") {
			t.Errorf("worktree staged paths = %v, want .beads/issues.jsonl among them", staged)
		}
		if staged := stagedPathsForWorktreeTest(t, primary); len(staged) != 0 {
			t.Errorf("primary staged paths = %v, want none", staged)
		}
	})

	t.Run("post-merge imports the worktree's copy with the hook marker kept", func(t *testing.T) {
		if err := os.RemoveAll(sentinel); err != nil {
			t.Fatal(err)
		}
		_ = captureHookStderr(t, func() { importJSONLForSync("post-merge") })

		// BD_GIT_HOOK=1 is half the assertion: clearing it lets the import
		// subprocess's own PersistentPostRun auto-export write the primary's
		// JSONL from the worktree's branch state (GH#6680, second symptom).
		if got, want := readFileForWorktreeTest(t, sentinel), "import --quiet "+worktreeJSONL+" BD_GIT_HOOK=1\n"; got != want {
			t.Fatalf("import subprocess = %q, want %q", got, want)
		}
	})
}

// writeFileForWorktreeTest creates path's parents and writes content. This
// duplicates the package's writeFile helper on purpose: that one lives in a
// //go:build cgo test file, and these tests must still compile in the pure-Go
// (CGO_ENABLED=0) and Windows lanes, which exclude it.
func writeFileForWorktreeTest(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileForWorktreeTest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// stagedPathsForWorktreeTest lists the paths staged in dir's index, so a test
// can tell "the hook staged this" from "the fixture did".
func stagedPathsForWorktreeTest(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = dir
	cmd.Env = scrubGitHookEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --cached in %s: %v\n%s", dir, err, out)
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

// TestNearestExistingDir pins the fallback that keeps the GH#3838 staged-
// deletion guard alive when the export directory does not exist yet.
func TestNearestExistingDir(t *testing.T) {
	root := t.TempDir()
	if got := nearestExistingDir(root); got != root {
		t.Errorf("existing dir: got %q, want %q", got, root)
	}
	missing := filepath.Join(root, "a", "b", "c")
	if got := nearestExistingDir(missing); got != root {
		t.Errorf("missing dir: got %q, want %q", got, root)
	}
}
