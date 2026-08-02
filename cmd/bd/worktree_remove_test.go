package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	worktreeRemoveHelperEnv      = "BD_WORKTREE_REMOVE_PROCESS_HELPER"
	worktreeRemoveHelperArgsEnv  = "BD_WORKTREE_REMOVE_PROCESS_ARGS"
	worktreeRemoveHelperHookEnv  = "BD_WORKTREE_REMOVE_PROCESS_HOOK"
	worktreeRemoveHelperTarget   = "BD_WORKTREE_REMOVE_PROCESS_TARGET"
	worktreeRemoveHelperMain     = "BD_WORKTREE_REMOVE_PROCESS_MAIN"
	worktreeRemoveHelperBase     = "BD_WORKTREE_REMOVE_PROCESS_BASE"
	worktreeRemoveHelperRestore  = "BD_WORKTREE_REMOVE_PROCESS_RESTORE_SOURCE"
	worktreeRemoveHelperSentinel = "BD_WORKTREE_REMOVE_PROCESS_SENTINEL"
	worktreeRemoveHookReplace    = "replace-target"
	worktreeRemoveHookSymlink    = "symlink-target"
	worktreeRemoveHookMoveMain   = "move-main"
	worktreeRemoveHookDirty      = "dirty-target"
	worktreeRemoveHookRewrite    = "rewrite-dirty-target"
	worktreeRemoveHookAdvance    = "advance-target"
	worktreeRemoveHookMoveTarget = "move-target-before-remove"
	worktreeRemoveHookCaseRace   = "case-race-before-remove"
	worktreeRemoveHookGitignore  = "change-gitignore-after-remove"
	worktreeRemoveHookRestore    = "restore-target-after-resolution"
	worktreeRemoveHookLock       = "lock-target-before-final"
	worktreeRemoveHookUnlock     = "unlock-target-before-final"
	worktreeRemoveTestActorName  = "Worktree Removal Test"
	worktreeRemoveTestActorEmail = "worktree-removal@example.invalid"
)

// TestWorktreeRemoveProcessHelper executes only the remove subcommand, without
// root PersistentPreRunE. That keeps the separate DB-opening concern out of
// this patch while still crossing Cobra, the real Git process boundary, and
// the destructive `git worktree remove` boundary in a fresh OS process.
func TestWorktreeRemoveProcessHelper(t *testing.T) {
	if os.Getenv(worktreeRemoveHelperEnv) != "1" {
		return
	}

	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(worktreeRemoveHelperArgsEnv)), &args); err != nil {
		t.Fatalf("decode helper arguments: %v", err)
	}

	hooks := worktreeRemoveHooks{}
	switch os.Getenv(worktreeRemoveHelperHookEnv) {
	case "":
	case worktreeRemoveHookRestore:
		hooks.afterTargetResolution = func() error {
			if sentinel := os.Getenv(worktreeRemoveHelperSentinel); sentinel != "" {
				if err := os.WriteFile(sentinel, []byte("reached\n"), 0600); err != nil {
					return err
				}
			}
			return os.Rename(
				os.Getenv(worktreeRemoveHelperRestore),
				os.Getenv(worktreeRemoveHelperTarget),
			)
		}
	case worktreeRemoveHookReplace:
		hooks.beforeFinalCheck = func() error {
			mainWorktree := os.Getenv(worktreeRemoveHelperMain)
			target := os.Getenv(worktreeRemoveHelperTarget)
			if err := runWorktreeRemoveHookGit(
				mainWorktree,
				"worktree",
				"remove",
				"--",
				target,
			); err != nil {
				return err
			}
			if err := runWorktreeRemoveHookGit(
				mainWorktree,
				"worktree",
				"add",
				"--",
				target,
				"lane",
			); err != nil {
				return err
			}
			return runWorktreeRemoveHookGit(target, "reset", "--hard", "HEAD")
		}
	case worktreeRemoveHookSymlink:
		hooks.beforeFinalCheck = func() error {
			target := os.Getenv(worktreeRemoveHelperTarget)
			original := target + "-original"
			if err := os.Rename(target, original); err != nil {
				return err
			}
			return os.Symlink(original, target)
		}
	case worktreeRemoveHookMoveMain:
		hooks.beforeFinalCheck = func() error {
			return runWorktreeRemoveHookGit(
				os.Getenv(worktreeRemoveHelperMain),
				"reset",
				"--hard",
				os.Getenv(worktreeRemoveHelperBase),
			)
		}
	case worktreeRemoveHookDirty:
		hooks.beforeFinalCheck = func() error {
			return os.WriteFile(
				filepath.Join(os.Getenv(worktreeRemoveHelperTarget), "interleaved.txt"),
				[]byte("changed during removal\n"),
				0644,
			)
		}
	case worktreeRemoveHookRewrite:
		hooks.beforeFinalCheck = func() error {
			return os.WriteFile(
				filepath.Join(os.Getenv(worktreeRemoveHelperTarget), "dirty.txt"),
				[]byte("bravo\n"),
				0644,
			)
		}
	case worktreeRemoveHookAdvance:
		hooks.beforeFinalCheck = func() error {
			return runWorktreeRemoveHookGit(
				os.Getenv(worktreeRemoveHelperTarget),
				"commit",
				"--allow-empty",
				"-m",
				"interleaved target commit",
			)
		}
	case worktreeRemoveHookLock:
		hooks.beforeFinalCheck = func() error {
			return runWorktreeRemoveHookGit(
				os.Getenv(worktreeRemoveHelperMain),
				"worktree",
				"lock",
				"--",
				os.Getenv(worktreeRemoveHelperTarget),
			)
		}
	case worktreeRemoveHookUnlock:
		hooks.beforeFinalCheck = func() error {
			return runWorktreeRemoveHookGit(
				os.Getenv(worktreeRemoveHelperMain),
				"worktree",
				"unlock",
				"--",
				os.Getenv(worktreeRemoveHelperTarget),
			)
		}
	case worktreeRemoveHookMoveTarget:
		hooks.beforeRemove = func() error {
			return runWorktreeRemoveHookGit(
				os.Getenv(worktreeRemoveHelperMain),
				"worktree",
				"move",
				"--",
				os.Getenv(worktreeRemoveHelperTarget),
				os.Getenv(worktreeRemoveHelperTarget)+"-moved",
			)
		}
	case worktreeRemoveHookCaseRace:
		hooks.beforeRemove = func() error {
			target := os.Getenv(worktreeRemoveHelperTarget)
			if err := os.Rename(target, target+"-moved"); err != nil {
				return err
			}
			return runWorktreeRemoveHookGit(
				os.Getenv(worktreeRemoveHelperMain),
				"config",
				"core.ignorecase",
				"true",
			)
		}
	case worktreeRemoveHookGitignore:
		hooks.afterRemoval = func() error {
			path := filepath.Join(os.Getenv(worktreeRemoveHelperMain), ".gitignore")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			if _, err := file.WriteString("# concurrent change\n"); err != nil {
				_ = file.Close()
				return err
			}
			return file.Close()
		}
	default:
		t.Fatalf("unknown helper hook %q", os.Getenv(worktreeRemoveHelperHookEnv))
	}

	command := newWorktreeRemoveCommandWithHooks(hooks)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		t.FailNow()
	}
}

func runWorktreeRemoveHookGit(dir string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(
		scrubWorktreeRemovalGitEnv(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, output)
	}
	return nil
}

func TestWorktreeRemoveCobraGrammar(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "explicit empty comparator",
			args:    []string{"lane", "--merged-into="},
			wantErr: "--merged-into requires a non-empty value",
		},
		{
			name:    "repeated comparator",
			args:    []string{"lane", "--merged-into", "main", "--merged-into", "refs/heads/main"},
			wantErr: "--merged-into may be specified only once",
		},
		{
			name:    "repeated force",
			args:    []string{"lane", "--force", "--force"},
			wantErr: "--force may be specified only once",
		},
		{
			name:    "force conflicts with comparator",
			args:    []string{"lane", "--force", "--merged-into", "main"},
			wantErr: "--force and --merged-into cannot be used together",
		},
		{
			name:    "explicit false force still conflicts with comparator",
			args:    []string{"lane", "--force=false", "--merged-into", "main"},
			wantErr: "--force and --merged-into cannot be used together",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := newWorktreeRemoveCommand()
			command.SetArgs(test.args)
			err := command.Execute()
			if err == nil {
				t.Fatalf("command succeeded; want error containing %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestWorktreeRemoveProcessGrammarFailuresDoNotMutate(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "empty comparator",
			args:    []string{fixture.lane, "--merged-into="},
			wantErr: "requires a non-empty value",
		},
		{
			name:    "repeated comparator",
			args:    []string{fixture.lane, "--merged-into", "main", "--merged-into", "refs/heads/main"},
			wantErr: "may be specified only once",
		},
		{
			name:    "repeated force",
			args:    []string{fixture.lane, "--force", "--force"},
			wantErr: "may be specified only once",
		},
		{
			name:    "force comparator conflict",
			args:    []string{fixture.lane, "--force", "--merged-into", "main"},
			wantErr: "cannot be used together",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := fixture.snapshot(t)
			result := runWorktreeRemoveProcess(t, fixture.repo, nil, test.args...)
			result.requireFailure(t, test.wantErr)
			fixture.assertSnapshot(t, before)
		})
	}
}

func TestWorktreeRemoveProcessSafetyFailuresDoNotMutate(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testing.T, *worktreeRemovalFixture)
		args    func(*worktreeRemovalFixture) []string
		wantErr string
	}{
		{
			name: "dirty target",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.writeLaneFile(t, "dirty.txt", "dirty\n")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "main"}
			},
			wantErr: "modified, untracked, or ignored files",
		},
		{
			name: "ignored artifact",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.writeLaneFile(t, filepath.Join("ignored", "cache.bin"), "ignored\n")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "main"}
			},
			wantErr: "modified, untracked, or ignored files",
		},
		{
			name: "missing upstream",
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane}
			},
			wantErr: "no single resolvable upstream",
		},
		{
			name: "configured upstream does not contain target despite tag collision",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.setUpstream(t)
				fixture.commitLane(t, "ahead of upstream")
				fixture.git(t, fixture.repo, "tag", "heads/main", "lane")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane}
			},
			wantErr: "commits not contained in its configured upstream",
		},
		{
			name: "not contained",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.commitLane(t, "uncontained")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "main"}
			},
			wantErr: "is not contained",
		},
		{
			name: "HEAD pseudoref is rejected",
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "HEAD"}
			},
			wantErr: "worktree-local pseudoref",
		},
		{
			// Git 2.43 does not give ORIG_HEAD the same show-ref behavior as
			// newer versions. The command never asks show-ref: its grammar
			// rejects this target-local pseudoref before version-specific DWIM.
			name: "Git 2.43 ORIG_HEAD case is rejected without DWIM",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.writeTargetPseudoref(t, "ORIG_HEAD")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "ORIG_HEAD"}
			},
			wantErr: "worktree-local pseudoref",
		},
		{
			name: "future all-caps HEAD pseudoref is rejected before branch DWIM",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.writeTargetPseudoref(t, "FUTURE_HEAD")
				fixture.git(t, fixture.repo, "branch", "FUTURE_HEAD")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "FUTURE_HEAD"}
			},
			wantErr: "worktree-local pseudoref",
		},
		{
			name: "irregular root pseudoref is rejected before branch DWIM",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.writeTargetPseudoref(t, "BISECT_EXPECTED_REV")
				fixture.git(t, fixture.repo, "branch", "BISECT_EXPECTED_REV")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "BISECT_EXPECTED_REV"}
			},
			wantErr: "worktree-local pseudoref",
		},
		{
			name: "refs worktree namespace is rejected",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.git(t, fixture.repo, "update-ref", "refs/worktree/proof", fixture.baseOID)
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "refs/worktree/proof"}
			},
			wantErr: "worktree-local ref namespace",
		},
		{
			name: "refs bisect namespace is rejected",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.git(t, fixture.repo, "update-ref", "refs/bisect/proof", fixture.baseOID)
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "refs/bisect/proof"}
			},
			wantErr: "worktree-local ref namespace",
		},
		{
			name: "refs rewritten namespace is rejected",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.git(t, fixture.repo, "update-ref", "refs/rewritten/proof", fixture.baseOID)
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "refs/rewritten/proof"}
			},
			wantErr: "worktree-local ref namespace",
		},
		{
			name: "revision expression is rejected",
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "HEAD~1"}
			},
			wantErr: "not an accepted ref name or full commit object ID",
		},
		{
			name: "missing full ref is rejected",
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "refs/heads/missing"}
			},
			wantErr: "does not resolve to a commit",
		},
		{
			name: "target branch comparator is rejected",
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "refs/heads/lane"}
			},
			wantErr: "cannot independently prove containment",
		},
		{
			name: "target HEAD object ID is rejected as tautological",
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", fixture.baseOID}
			},
			wantErr: "target HEAD itself",
		},
		{
			name: "ambiguous short ref is rejected",
			setup: func(t *testing.T, fixture *worktreeRemovalFixture) {
				fixture.git(t, fixture.repo, "branch", "comparison")
				fixture.git(t, fixture.repo, "tag", "comparison")
			},
			args: func(fixture *worktreeRemovalFixture) []string {
				return []string{fixture.lane, "--merged-into", "comparison"}
			},
			wantErr: "is ambiguous",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			if test.setup != nil {
				test.setup(t, fixture)
			}
			before := fixture.snapshot(t)
			result := runWorktreeRemoveProcess(t, fixture.repo, nil, test.args(fixture)...)
			result.requireFailure(t, test.wantErr)
			fixture.assertSnapshot(t, before)
		})
	}
}

func TestWorktreeRemoveProcessSuccess(t *testing.T) {
	t.Run("unambiguous short ref", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained")
		fixture.mergeLaneIntoMain(t)

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
	})

	t.Run("configured upstream", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained upstream")
		fixture.mergeLaneIntoMain(t)
		fixture.setUpstream(t)

		result := runWorktreeRemoveProcess(t, fixture.repo, nil, fixture.lane)
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
	})

	t.Run("full descendant object ID", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained by object")
		fixture.mergeLaneIntoMain(t)
		fixture.git(t, fixture.repo, "commit", "--allow-empty", "-m", "descendant")
		comparatorOID := fixture.git(t, fixture.repo, "rev-parse", "main")

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			comparatorOID,
		)
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
	})

	t.Run("force removes a stable dirty target", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.writeLaneFile(t, "dirty.txt", "dirty\n")

		result := runWorktreeRemoveProcess(t, fixture.repo, nil, fixture.lane, "--force")
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
	})

	t.Run("force removes a stable ignored artifact", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.writeLaneFile(t, filepath.Join("ignored", "cache.bin"), "ignored\n")

		result := runWorktreeRemoveProcess(t, fixture.repo, nil, fixture.lane, "--force")
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
	})

	t.Run("nested in-repository path uses git slashes", func(t *testing.T) {
		fixture := newWorktreeRemovalFixtureAt(t, "nested/lane")
		fixture.commitLane(t, "contained nested")
		fixture.mergeLaneIntoMain(t)

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
	})

	t.Run("cleanup preserves unrelated managed pairs byte-for-byte", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained managed pairs")
		fixture.mergeLaneIntoMain(t)
		before := "# bd worktree\r\nlane/\r\n# bd worktree\r\nother/\r\n# unrelated\r\nignored/\r\n"
		if err := os.WriteFile(filepath.Join(fixture.repo, ".gitignore"), []byte(before), 0644); err != nil {
			t.Fatalf("write multi-entry .gitignore: %v", err)
		}

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
		want := "# bd worktree\r\nother/\r\n# unrelated\r\nignored/\r\n"
		if got := fixture.readGitignore(t); got != want {
			t.Fatalf(".gitignore cleanup changed unrelated bytes\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("leading-space target preserves unrelated managed entry", func(t *testing.T) {
		fixture := newWorktreeRemovalFixtureAt(t, " lane")
		unrelated := "# bd worktree\nlane/\nignored/\n"
		if err := os.WriteFile(
			filepath.Join(fixture.repo, ".gitignore"),
			[]byte(unrelated),
			0644,
		); err != nil {
			t.Fatalf("write unrelated .gitignore entry: %v", err)
		}

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireSuccess(t)
		fixture.assertRemovedAndCleaned(t)
		if got := fixture.readGitignore(t); got != unrelated {
			t.Fatalf("cleanup conflated leading-space target with unrelated entry\ngot:  %q\nwant: %q", got, unrelated)
		}
	})
}

func TestWorktreeRemoveProcessScrubsPoisonedGitEnvironment(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)

	decoy := filepath.Join(t.TempDir(), "decoy")
	if err := os.MkdirAll(decoy, 0755); err != nil {
		t.Fatalf("create decoy repository: %v", err)
	}
	fixture.git(t, decoy, "init")
	emptyExecPath := t.TempDir()
	shallowFile := filepath.Join(t.TempDir(), "shallow")
	if err := os.WriteFile(shallowFile, []byte{}, 0600); err != nil {
		t.Fatalf("write poisoned shallow file: %v", err)
	}

	poisonedEnv := []string{
		"GIT_DIR=" + filepath.Join(decoy, ".git"),
		"GIT_WORK_TREE=" + decoy,
		"GIT_COMMON_DIR=" + filepath.Join(decoy, ".git"),
		"GIT_INDEX_FILE=" + filepath.Join(decoy, ".git", "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(decoy, ".git", "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(decoy, ".git", "objects"),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree",
		"GIT_CONFIG_VALUE_0=" + decoy,
		"GIT_EXEC_PATH=" + emptyExecPath,
		"GIT_SHALLOW_FILE=" + shallowFile,
		"GIT_REPLACE_REF_BASE=refs/heads",
		"GIT_NO_REPLACE_OBJECTS=0",
	}

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		poisonedEnv,
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireSuccess(t)
	fixture.assertRemovedAndCleaned(t)
	// The decoy is a standalone primary worktree, not part of fixture.repo's
	// registry. Its .git directory is the durable proof it was untouched.
	if _, err := os.Stat(filepath.Join(decoy, ".git")); err != nil {
		t.Fatalf("poisoned environment damaged decoy repository: %v", err)
	}
}

func TestWorktreeRemoveProcessRejectsIdentityInterleaving(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)
	before := fixture.snapshot(t)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookReplace,
			worktreeRemoveHelperTarget + "=" + fixture.lane,
			worktreeRemoveHelperMain + "=" + fixture.repo,
		},
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "identity changed")
	fixture.assertSnapshot(t, before)
}

func TestWorktreeRemoveProcessRejectsSymlinkReplacement(t *testing.T) {
	probeRoot := t.TempDir()
	probeTarget := filepath.Join(probeRoot, "target")
	probeLink := filepath.Join(probeRoot, "link")
	if err := os.Mkdir(probeTarget, 0755); err != nil {
		t.Fatalf("create symlink capability probe: %v", err)
	}
	if err := os.Symlink(probeTarget, probeLink); err != nil {
		t.Fatalf("required directory symlink capability unavailable: %v", err)
	}
	if err := os.Remove(probeLink); err != nil {
		t.Fatalf("remove symlink capability probe: %v", err)
	}

	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookSymlink,
			worktreeRemoveHelperTarget + "=" + fixture.lane,
		},
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "target path is not a real directory")
	info, err := os.Lstat(fixture.lane)
	if err != nil {
		t.Fatalf("inspect replacement symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement path mode = %s, want symlink", info.Mode())
	}
	if _, err := os.Stat(fixture.lane + "-original"); err != nil {
		t.Fatalf("original target was not preserved: %v", err)
	}
	if !fixture.registered(t, fixture.lane) {
		t.Fatal("symlink replacement removed the registered target")
	}
}

func TestWorktreeRemoveProcessRevalidatesHeadAndCleanliness(t *testing.T) {
	tests := []struct {
		name    string
		hook    string
		wantErr string
	}{
		{
			name:    "HEAD changes",
			hook:    worktreeRemoveHookAdvance,
			wantErr: "target HEAD changed",
		},
		{
			name:    "cleanliness changes",
			hook:    worktreeRemoveHookDirty,
			wantErr: "target cleanliness changed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			fixture.commitLane(t, "contained")
			fixture.mergeLaneIntoMain(t)

			result := runWorktreeRemoveProcess(
				t,
				fixture.repo,
				[]string{
					worktreeRemoveHelperHookEnv + "=" + test.hook,
					worktreeRemoveHelperTarget + "=" + fixture.lane,
				},
				fixture.lane,
				"--merged-into",
				"main",
			)
			result.requireFailure(t, test.wantErr)
			if !fixture.registered(t, fixture.lane) {
				t.Fatal("target was removed after concurrent target mutation")
			}
		})
	}
}

func TestWorktreeRemoveProcessRevalidatesBareLockState(t *testing.T) {
	t.Run("locked to unlocked", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		want := fixture.snapshot(t)
		fixture.git(t, fixture.repo, "worktree", "lock", fixture.lane)

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			[]string{
				worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookUnlock,
				worktreeRemoveHelperMain + "=" + fixture.repo,
				worktreeRemoveHelperTarget + "=" + fixture.lane,
			},
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireFailure(t, "registered target identity changed")
		fixture.assertSnapshot(t, want)
	})

	t.Run("unlocked to locked", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.git(t, fixture.repo, "worktree", "lock", fixture.lane)
		want := fixture.snapshot(t)
		fixture.git(t, fixture.repo, "worktree", "unlock", fixture.lane)

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			[]string{
				worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookLock,
				worktreeRemoveHelperMain + "=" + fixture.repo,
				worktreeRemoveHelperTarget + "=" + fixture.lane,
			},
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireFailure(t, "registered target identity changed")
		fixture.assertSnapshot(t, want)
	})
}

func TestWorktreeRemoveProcessForceRevalidatesDirtyContent(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.writeLaneFile(t, "dirty.txt", "alpha\n")

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookRewrite,
			worktreeRemoveHelperTarget + "=" + fixture.lane,
		},
		fixture.lane,
		"--force",
	)
	result.requireFailure(t, "target changed files changed")
	if !fixture.registered(t, fixture.lane) {
		t.Fatal("force removed target after dirty file bytes changed")
	}
	content, err := os.ReadFile(filepath.Join(fixture.lane, "dirty.txt"))
	if err != nil {
		t.Fatalf("read interleaved dirty file: %v", err)
	}
	if string(content) != "bravo\n" {
		t.Fatalf("dirty file = %q, want interleaved content", content)
	}
}

func TestWorktreeRemoveProcessRejectsComparatorInterleaving(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookMoveMain,
			worktreeRemoveHelperMain + "=" + fixture.repo,
			worktreeRemoveHelperBase + "=" + fixture.baseOID,
		},
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "comparison target changed")
	if !fixture.registered(t, fixture.lane) {
		t.Fatal("target was removed after comparator changed")
	}
}

func TestWorktreeRemoveProcessReportsPartialGitignoreCleanup(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookGitignore,
			worktreeRemoveHelperMain + "=" + fixture.repo,
		},
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "worktree was removed")
	if !strings.Contains(result.combined(), ".gitignore cleanup failed") {
		t.Fatalf("partial error did not name cleanup failure:\n%s", result.combined())
	}
	if fixture.registered(t, fixture.lane) {
		t.Fatal("partial outcome did not remove target worktree")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("target path still exists after partial outcome: %v", err)
	}
	gitignore := fixture.readGitignore(t)
	if !strings.Contains(gitignore, "lane/") ||
		!strings.Contains(gitignore, "# concurrent change") {
		t.Fatalf("failed cleanup overwrote concurrent .gitignore content:\n%s", gitignore)
	}
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/lane")
}

func TestWorktreeRemoveProcessRejectsUnsafeGitignoreBeforeRemoval(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained")
		fixture.mergeLaneIntoMain(t)
		gitignore := filepath.Join(fixture.repo, ".gitignore")
		if err := os.Remove(gitignore); err != nil {
			t.Fatalf("remove .gitignore file: %v", err)
		}
		if err := os.Mkdir(gitignore, 0755); err != nil {
			t.Fatalf("replace .gitignore with directory: %v", err)
		}

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireFailure(t, "is not a regular file")
		if !fixture.registered(t, fixture.lane) {
			t.Fatal("unsafe .gitignore preflight removed the target")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained")
		fixture.mergeLaneIntoMain(t)
		gitignore := filepath.Join(fixture.repo, ".gitignore")
		external := filepath.Join(t.TempDir(), "external.gitignore")
		externalContent := []byte("external sentinel\n")
		if err := os.WriteFile(external, externalContent, 0644); err != nil {
			t.Fatalf("write external file: %v", err)
		}
		if err := os.Remove(gitignore); err != nil {
			t.Fatalf("remove .gitignore file: %v", err)
		}
		if err := os.Symlink(external, gitignore); err != nil {
			t.Fatalf("required file symlink capability unavailable: %v", err)
		}

		result := runWorktreeRemoveProcess(
			t,
			fixture.repo,
			nil,
			fixture.lane,
			"--merged-into",
			"main",
		)
		result.requireFailure(t, "is not a regular file")
		if !fixture.registered(t, fixture.lane) {
			t.Fatal("symlink .gitignore preflight removed the target")
		}
		content, err := os.ReadFile(external)
		if err != nil {
			t.Fatalf("read external sentinel: %v", err)
		}
		if !bytes.Equal(content, externalContent) {
			t.Fatalf("external symlink target changed: %q", content)
		}
	})
}

func TestWorktreeRemoveProcessReportsIndeterminatePrimaryFailure(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		[]string{
			worktreeRemoveHelperHookEnv + "=" + worktreeRemoveHookMoveTarget,
			worktreeRemoveHelperMain + "=" + fixture.repo,
			worktreeRemoveHelperTarget + "=" + fixture.lane,
		},
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "partial or indeterminate")
	if !strings.Contains(result.combined(), "registered=false, path_exists=false") {
		t.Fatalf("primary failure did not report inspected state:\n%s", result.combined())
	}
	moved := fixture.lane + "-moved"
	if !fixture.registered(t, moved) {
		t.Fatal("interleaved moved worktree was not preserved for inspection")
	}
}

func TestWorktreeRemoveProcessReportsUnchangedPrimaryFailure(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained locked worktree")
	fixture.mergeLaneIntoMain(t)
	fixture.git(t, fixture.repo, "worktree", "lock", "--reason", "test lock", fixture.lane)

	result := runWorktreeRemoveProcess(
		t,
		fixture.repo,
		nil,
		fixture.lane,
		"--merged-into",
		"main",
	)
	result.requireFailure(t, "target was revalidated unchanged")
	if strings.Contains(result.combined(), "partial or indeterminate") {
		t.Fatalf("unchanged primary failure was mislabeled:\n%s", result.combined())
	}
	if !fixture.registered(t, fixture.lane) {
		t.Fatal("locked target was removed after Git reported failure")
	}
}

func TestScrubWorktreeRemovalGitEnv(t *testing.T) {
	input := []string{
		"PATH=/trusted/bin",
		"HOME=/home/test",
		"GIT_DIR=/wrong",
		"git_work_tree=/wrong-case",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree",
		"GIT_OBJECT_DIRECTORY=/wrong-objects",
		"GIT_EXEC_PATH=/wrong-exec",
	}
	got := scrubWorktreeRemovalGitEnv(input)
	want := []string{
		"PATH=/trusted/bin",
		"HOME=/home/test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scrubWorktreeRemovalGitEnv() = %#v, want %#v", got, want)
	}

	runner, err := newWorktreeRemovalGit()
	if err != nil {
		t.Fatalf("newWorktreeRemovalGit: %v", err)
	}
	if !filepath.IsAbs(runner.executable) {
		t.Fatalf("git executable is not pinned to an absolute path: %q", runner.executable)
	}
}

type worktreeRemoveProcessResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func (result worktreeRemoveProcessResult) combined() string {
	return result.stdout + result.stderr
}

func (result worktreeRemoveProcessResult) requireSuccess(t *testing.T) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf(
			"worktree remove failed with exit code %d\nstdout:\n%s\nstderr:\n%s",
			result.exitCode,
			result.stdout,
			result.stderr,
		)
	}
}

func (result worktreeRemoveProcessResult) requireFailure(t *testing.T, substring string) {
	t.Helper()
	if result.exitCode == 0 {
		t.Fatalf("worktree remove succeeded; want failure containing %q", substring)
	}
	if !strings.Contains(result.combined(), substring) {
		t.Fatalf(
			"failure output did not contain %q\nstdout:\n%s\nstderr:\n%s",
			substring,
			result.stdout,
			result.stderr,
		)
	}
}

func runWorktreeRemoveProcess(
	t *testing.T,
	dir string,
	extraEnv []string,
	args ...string,
) worktreeRemoveProcessResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode helper arguments: %v", err)
	}

	command := exec.Command(executable, "-test.run=^TestWorktreeRemoveProcessHelper$")
	command.Dir = dir
	command.Env = overrideWorktreeRemoveEnv(
		os.Environ(),
		append(
			[]string{
				worktreeRemoveHelperEnv + "=1",
				worktreeRemoveHelperArgsEnv + "=" + string(encodedArgs),
				"BD_DISABLE_METRICS=1",
			},
			extraEnv...,
		),
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("launch worktree remove helper: %v", err)
		}
		exitCode = exitError.ExitCode()
	}
	return worktreeRemoveProcessResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

func overrideWorktreeRemoveEnv(base []string, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		key := override
		if separator := strings.IndexByte(override, '='); separator >= 0 {
			key = override[:separator]
		}
		filtered := result[:0]
		for _, entry := range result {
			entryKey := entry
			if separator := strings.IndexByte(entry, '='); separator >= 0 {
				entryKey = entry[:separator]
			}
			if !strings.EqualFold(entryKey, key) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, override)
	}
	return result
}

type worktreeRemovalFixture struct {
	repo           string
	lane           string
	baseOID        string
	gitignoreEntry string
}

func newWorktreeRemovalFixture(t *testing.T) *worktreeRemovalFixture {
	return newWorktreeRemovalFixtureAt(t, "lane")
}

func newWorktreeRemovalFixtureAt(t *testing.T, gitignoreEntry string) *worktreeRemovalFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &worktreeRemovalFixture{
		repo:           filepath.Join(root, "repo"),
		gitignoreEntry: gitignoreEntry,
	}
	fixture.lane = filepath.Join(fixture.repo, filepath.FromSlash(fixture.gitignoreEntry))
	if err := os.MkdirAll(fixture.repo, 0755); err != nil {
		t.Fatalf("create repository: %v", err)
	}

	fixture.git(t, fixture.repo, "init")
	fixture.git(t, fixture.repo, "config", "user.name", worktreeRemoveTestActorName)
	fixture.git(t, fixture.repo, "config", "user.email", worktreeRemoveTestActorEmail)
	fixture.git(t, fixture.repo, "config", "commit.gpgsign", "false")
	fixture.git(t, fixture.repo, "config", "core.hooksPath", ".git/hooks")
	fixture.git(t, fixture.repo, "symbolic-ref", "HEAD", "refs/heads/main")
	gitignore := fmt.Sprintf("# bd worktree\n%s/\nignored/\n", fixture.gitignoreEntry)
	if err := os.WriteFile(filepath.Join(fixture.repo, ".gitignore"), []byte(gitignore), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	fixture.git(t, fixture.repo, "add", ".gitignore")
	fixture.git(t, fixture.repo, "commit", "-m", "base")
	fixture.baseOID = fixture.git(t, fixture.repo, "rev-parse", "HEAD")
	fixture.git(t, fixture.repo, "worktree", "add", "-b", "lane", fixture.lane)
	return fixture
}

func (fixture *worktreeRemovalFixture) setUpstream(t *testing.T) {
	t.Helper()
	fixture.git(t, fixture.lane, "branch", "--set-upstream-to=main", "lane")
}

func (fixture *worktreeRemovalFixture) commitLane(t *testing.T, message string) {
	t.Helper()
	fixture.git(t, fixture.lane, "commit", "--allow-empty", "-m", message)
}

func (fixture *worktreeRemovalFixture) mergeLaneIntoMain(t *testing.T) {
	t.Helper()
	fixture.git(t, fixture.repo, "merge", "--ff-only", "lane")
}

func (fixture *worktreeRemovalFixture) writeLaneFile(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(fixture.lane, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create target file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write target file: %v", err)
	}
}

func (fixture *worktreeRemovalFixture) writeTargetPseudoref(t *testing.T, name string) {
	t.Helper()
	gitPath := fixture.git(t, fixture.lane, "rev-parse", "--git-path", name)
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(fixture.lane, gitPath)
	}
	head := fixture.git(t, fixture.lane, "rev-parse", "HEAD")
	if err := os.WriteFile(gitPath, []byte(head+"\n"), 0644); err != nil {
		t.Fatalf("write target pseudoref %s: %v", name, err)
	}
}

func (fixture *worktreeRemovalFixture) registered(t *testing.T, path string) bool {
	t.Helper()
	output := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z")
	for _, field := range strings.Split(output, "\x00") {
		if strings.HasPrefix(field, "worktree ") &&
			sameWorktreePath(strings.TrimPrefix(field, "worktree "), path) {
			return true
		}
	}
	return false
}

type worktreeRemovalSnapshot struct {
	registry  string
	head      string
	status    string
	branchOID string
	gitignore string
}

func (fixture *worktreeRemovalFixture) snapshot(t *testing.T) worktreeRemovalSnapshot {
	t.Helper()
	return worktreeRemovalSnapshot{
		registry: fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"),
		head:     fixture.git(t, fixture.lane, "rev-parse", "HEAD"),
		status: fixture.git(
			t,
			fixture.lane,
			"status",
			"--porcelain=v1",
			"-z",
			"--untracked-files=all",
			"--ignored=matching",
		),
		branchOID: fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"),
		gitignore: fixture.readGitignore(t),
	}
}

func (fixture *worktreeRemovalFixture) assertSnapshot(t *testing.T, want worktreeRemovalSnapshot) {
	t.Helper()
	got := fixture.snapshot(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree state mutated on refusal\ngot:  %#v\nwant: %#v", got, want)
	}
	if !fixture.registered(t, fixture.lane) {
		t.Fatal("target worktree is no longer registered after refusal")
	}
}

func (fixture *worktreeRemovalFixture) readGitignore(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixture.repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	return string(content)
}

func (fixture *worktreeRemovalFixture) assertRemovedAndCleaned(t *testing.T) {
	t.Helper()
	if fixture.registered(t, fixture.lane) {
		t.Fatal("target remains registered after successful removal")
	}
	if _, err := os.Stat(fixture.lane); !os.IsNotExist(err) {
		t.Fatalf("target path still exists after successful removal: %v", err)
	}
	fixture.git(t, fixture.repo, "rev-parse", "--verify", "refs/heads/lane")
	if content := fixture.readGitignore(t); strings.Contains(content, fixture.gitignoreEntry+"/") {
		t.Fatalf(".gitignore still contains worktree entry:\n%s", content)
	}
}

func (fixture *worktreeRemovalFixture) git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(
		scrubWorktreeRemovalGitEnv(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
