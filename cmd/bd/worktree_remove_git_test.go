package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/worktreeremove"
)

// runWorktreeRemoveGitAdapter exercises the real Git and filesystem adapter
// without the child-process Cobra harness. Command grammar has its own unit
// coverage; these contracts own the adapter's observations and mutations.
func runWorktreeRemoveGitAdapter(
	t *testing.T,
	dir, name string,
	force bool,
	mergedInto string,
	hooks worktreeRemoveHooks,
) error {
	t.Helper()
	if force && mergedInto != "" {
		t.Fatalf("force adapter test requested unreachable comparator state %q", mergedInto)
	}
	t.Chdir(dir)
	options := &worktreeRemoveOptions{
		force: singleWorktreeBoolFlag{name: "force", value: force, set: force},
	}
	if mergedInto != "" {
		options.mergedInto = singleWorktreeStringFlag{name: "merged-into", value: mergedInto, set: true}
	}
	mode := worktreeremove.Normal
	if force {
		mode = worktreeremove.Force
	}
	adapter := &gitWorktreeRemovalAdapter{name: name, options: options, hooks: hooks}
	return runWorktreeRemovalOrchestration(
		context.Background(),
		worktreeRemovalRequest{mode: mode},
		adapter,
		adapter,
		cliWorktreeRemovalPresenter{},
	)
}

func requireWorktreeRemoveGitAdapterFailure(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("adapter removal error = %v, want substring %q", err, want)
	}
}

func TestWorktreeRemoveGitAdapterSafetyRefusals(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testing.T, *worktreeRemovalFixture)
		force   bool
		compare func(*worktreeRemovalFixture) string
		wantErr string
	}{
		{"ignored status", func(t *testing.T, f *worktreeRemovalFixture) {
			f.writeLaneFile(t, filepath.Join("ignored", "cache.bin"), "ignored\n")
		}, false, func(f *worktreeRemovalFixture) string { return "main" }, "modified, untracked, or ignored files"},
		{"missing upstream", nil, false, nil, "no single resolvable upstream"},
		{"configured upstream tag collision", func(t *testing.T, f *worktreeRemovalFixture) {
			f.setUpstream(t)
			f.commitLane(t, "ahead of upstream")
			f.git(t, f.repo, "tag", "heads/main", "lane")
		}, false, nil, "commits not contained in its configured upstream"},
		{"not contained", func(t *testing.T, f *worktreeRemovalFixture) { f.commitLane(t, "uncontained") }, false, func(f *worktreeRemovalFixture) string { return "main" }, "is not contained"},
		{"HEAD pseudoref", nil, false, func(f *worktreeRemovalFixture) string { return "HEAD" }, "worktree-local pseudoref"},
		{"ORIG_HEAD pseudoref", func(t *testing.T, f *worktreeRemovalFixture) { f.writeTargetPseudoref(t, "ORIG_HEAD") }, false, func(f *worktreeRemovalFixture) string { return "ORIG_HEAD" }, "worktree-local pseudoref"},
		{"future pseudoref", func(t *testing.T, f *worktreeRemovalFixture) {
			f.writeTargetPseudoref(t, "FUTURE_HEAD")
			f.git(t, f.repo, "branch", "FUTURE_HEAD")
		}, false, func(f *worktreeRemovalFixture) string { return "FUTURE_HEAD" }, "worktree-local pseudoref"},
		{"irregular pseudoref", func(t *testing.T, f *worktreeRemovalFixture) {
			f.writeTargetPseudoref(t, "BISECT_EXPECTED_REV")
			f.git(t, f.repo, "branch", "BISECT_EXPECTED_REV")
		}, false, func(f *worktreeRemovalFixture) string { return "BISECT_EXPECTED_REV" }, "worktree-local pseudoref"},
		{"refs worktree", func(t *testing.T, f *worktreeRemovalFixture) {
			f.git(t, f.repo, "update-ref", "refs/worktree/proof", f.baseOID)
		}, false, func(f *worktreeRemovalFixture) string { return "refs/worktree/proof" }, "worktree-local ref namespace"},
		{"refs bisect", func(t *testing.T, f *worktreeRemovalFixture) {
			f.git(t, f.repo, "update-ref", "refs/bisect/proof", f.baseOID)
		}, false, func(f *worktreeRemovalFixture) string { return "refs/bisect/proof" }, "worktree-local ref namespace"},
		{"refs rewritten", func(t *testing.T, f *worktreeRemovalFixture) {
			f.git(t, f.repo, "update-ref", "refs/rewritten/proof", f.baseOID)
		}, false, func(f *worktreeRemovalFixture) string { return "refs/rewritten/proof" }, "worktree-local ref namespace"},
		{"revision expression", nil, false, func(f *worktreeRemovalFixture) string { return "HEAD~1" }, "not an accepted ref name or full commit object ID"},
		{"missing full ref", nil, false, func(f *worktreeRemovalFixture) string { return "refs/heads/missing" }, "does not resolve to a commit"},
		{"target branch", nil, false, func(f *worktreeRemovalFixture) string { return "refs/heads/lane" }, "cannot independently prove containment"},
		{"target HEAD OID", nil, false, func(f *worktreeRemovalFixture) string { return f.baseOID }, "target HEAD itself"},
		{"ambiguous short ref", func(t *testing.T, f *worktreeRemovalFixture) {
			f.git(t, f.repo, "branch", "comparison")
			f.git(t, f.repo, "tag", "comparison")
		}, false, func(f *worktreeRemovalFixture) string { return "comparison" }, "is ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			if test.setup != nil {
				test.setup(t, fixture)
			}
			before := fixture.snapshot(t)
			compare := ""
			if test.compare != nil {
				compare = test.compare(fixture)
			}
			err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, test.force, compare, worktreeRemoveHooks{})
			requireWorktreeRemoveGitAdapterFailure(t, err, test.wantErr)
			fixture.assertSnapshot(t, before)
		})
	}
}

func TestWorktreeRemoveGitAdapterComparatorAndCleanupContracts(t *testing.T) {
	t.Run("configured upstream success", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained upstream")
		fixture.mergeLaneIntoMain(t)
		fixture.setUpstream(t)
		if err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, "", worktreeRemoveHooks{}); err != nil {
			t.Fatal(err)
		}
		fixture.assertRemovedAndCleaned(t)
	})
	for _, comparator := range []struct{ name, value string }{{"short ref", "main"}, {"full ref", "refs/heads/main"}} {
		t.Run(comparator.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			fixture.commitLane(t, "contained")
			fixture.mergeLaneIntoMain(t)
			if err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, comparator.value, worktreeRemoveHooks{}); err != nil {
				t.Fatal(err)
			}
			fixture.assertRemovedAndCleaned(t)
		})
	}
	t.Run("full descendant object ID", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained by object")
		fixture.mergeLaneIntoMain(t)
		fixture.git(t, fixture.repo, "commit", "--allow-empty", "-m", "descendant")
		comparator := fixture.git(t, fixture.repo, "rev-parse", "main")
		if err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, comparator, worktreeRemoveHooks{}); err != nil {
			t.Fatal(err)
		}
		fixture.assertRemovedAndCleaned(t)
	})
	for _, test := range []struct {
		name, path string
		body       func(*testing.T, *worktreeRemovalFixture)
	}{
		{"force ignored removal", "lane", func(t *testing.T, f *worktreeRemovalFixture) {
			f.writeLaneFile(t, filepath.Join("ignored", "cache.bin"), "ignored\n")
		}},
		{"nested cleanup", "nested/lane", func(t *testing.T, f *worktreeRemovalFixture) {
			f.commitLane(t, "contained nested")
			f.mergeLaneIntoMain(t)
		}},
		{"CRLF cleanup", "lane", func(t *testing.T, f *worktreeRemovalFixture) {
			f.commitLane(t, "contained")
			f.mergeLaneIntoMain(t)
			before := "# bd worktree\r\nlane/\r\n# bd worktree\r\nother/\r\n# unrelated\r\nignored/\r\n"
			if err := os.WriteFile(filepath.Join(f.repo, ".gitignore"), []byte(before), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"leading space cleanup", " lane", func(t *testing.T, f *worktreeRemovalFixture) {
			if err := os.WriteFile(filepath.Join(f.repo, ".gitignore"), []byte("# bd worktree\n lane/\n# bd worktree\nlane/\nignored/\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixtureAt(t, test.path)
			test.body(t, fixture)
			force := test.name == "force ignored removal"
			comparator := "main"
			if force {
				comparator = ""
			}
			if err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, force, comparator, worktreeRemoveHooks{}); err != nil {
				t.Fatal(err)
			}
			fixture.assertRemovedAndCleaned(t)
			if test.name == "CRLF cleanup" && fixture.readGitignore(t) != "# bd worktree\r\nother/\r\n# unrelated\r\nignored/\r\n" {
				t.Fatal("CRLF cleanup changed unrelated bytes")
			}
			if test.name == "leading space cleanup" && fixture.readGitignore(t) != "# bd worktree\nlane/\nignored/\n" {
				t.Fatal("leading-space cleanup conflated entries")
			}
		})
	}
}

func TestWorktreeRemoveGitAdapterScrubsEveryPoisonedEnvironmentKey(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	fixture.commitLane(t, "contained")
	fixture.mergeLaneIntoMain(t)
	decoy := filepath.Join(t.TempDir(), "decoy")
	if err := os.MkdirAll(decoy, 0755); err != nil {
		t.Fatal(err)
	}
	fixture.git(t, decoy, "init")
	shallow := filepath.Join(t.TempDir(), "shallow")
	if err := os.WriteFile(shallow, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"GIT_DIR": filepath.Join(decoy, ".git"), "GIT_WORK_TREE": decoy, "GIT_COMMON_DIR": filepath.Join(decoy, ".git"), "GIT_INDEX_FILE": filepath.Join(decoy, ".git", "index"), "GIT_OBJECT_DIRECTORY": filepath.Join(decoy, ".git", "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(decoy, ".git", "objects"), "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": decoy, "GIT_EXEC_PATH": t.TempDir(), "GIT_SHALLOW_FILE": shallow, "GIT_REPLACE_REF_BASE": "refs/heads", "GIT_NO_REPLACE_OBJECTS": "0",
	} {
		t.Setenv(key, value)
	}
	if err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, "main", worktreeRemoveHooks{}); err != nil {
		t.Fatal(err)
	}
	fixture.assertRemovedAndCleaned(t)
	if _, err := os.Stat(filepath.Join(decoy, ".git")); err != nil {
		t.Fatalf("poisoned environment damaged decoy: %v", err)
	}
}

func TestWorktreeRemoveGitAdapterRejectsForgedMutationIdentities(t *testing.T) {
	fixture := newWorktreeRemovalFixture(t)
	t.Chdir(fixture.repo)
	options := &worktreeRemoveOptions{
		force: singleWorktreeBoolFlag{name: "force"},
		mergedInto: singleWorktreeStringFlag{
			name:  "merged-into",
			value: "main",
			set:   true,
		},
	}
	adapter := &gitWorktreeRemovalAdapter{
		name:    fixture.lane,
		options: options,
	}
	if _, err := adapter.Prepare(context.Background(), worktreeRemovalRequest{mode: worktreeremove.Normal}); err != nil {
		t.Fatalf("prepare real adapter: %v", err)
	}
	if adapter.plan == nil || adapter.plan.gitignoreCleanup == nil {
		t.Fatal("real adapter did not prepare target and managed-ignore identities")
	}
	before := fixture.snapshot(t)

	for _, mutation := range []worktreeremove.Mutation{
		{TargetPath: fixture.lane + "-forged", Force: false},
		{TargetPath: fixture.lane, Force: true},
	} {
		err := adapter.Remove(context.Background(), mutation)
		requireWorktreeRemoveGitAdapterFailure(t, err, "unapproved mutation")
		fixture.assertSnapshot(t, before)
	}

	err := adapter.Cleanup(
		context.Background(),
		worktreeremove.Cleanup{Entry: adapter.plan.gitignoreCleanup.entry + "-forged"},
	)
	requireWorktreeRemoveGitAdapterFailure(t, err, "approved identity")
	fixture.assertSnapshot(t, before)
}

func TestWorktreeRemoveGitAdapterClassifiesRealRemovalFailures(t *testing.T) {
	t.Run("stable locked target is unchanged", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained locked worktree")
		fixture.mergeLaneIntoMain(t)
		fixture.git(t, fixture.repo, "worktree", "lock", "--reason", "test lock", fixture.lane)
		before := fixture.snapshot(t)

		err := runWorktreeRemoveGitAdapter(
			t,
			fixture.repo,
			fixture.lane,
			false,
			"main",
			worktreeRemoveHooks{},
		)
		requireWorktreeRemoveGitAdapterFailure(t, err, "target was revalidated unchanged")
		if strings.Contains(err.Error(), "partial or indeterminate") {
			t.Fatalf("stable failure was classified as indeterminate: %v", err)
		}
		fixture.assertSnapshot(t, before)
	})

	t.Run("moved target is partial or indeterminate", func(t *testing.T) {
		fixture := newWorktreeRemovalFixture(t)
		fixture.commitLane(t, "contained moving worktree")
		fixture.mergeLaneIntoMain(t)
		branchOID := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane")
		moved := fixture.lane + "-moved"
		hooks := worktreeRemoveHooks{beforeRemove: func() error {
			return runWorktreeRemoveHookGit(
				fixture.repo,
				"worktree",
				"move",
				"--",
				fixture.lane,
				moved,
			)
		}}

		err := runWorktreeRemoveGitAdapter(
			t,
			fixture.repo,
			fixture.lane,
			false,
			"main",
			hooks,
		)
		requireWorktreeRemoveGitAdapterFailure(t, err, "partial or indeterminate")
		if !strings.Contains(err.Error(), "registered=false, path_exists=false") {
			t.Fatalf("partial failure omitted observed state: %v", err)
		}
		if !fixture.registered(t, moved) {
			t.Fatal("moved worktree is no longer registered")
		}
		if _, statErr := os.Stat(moved); statErr != nil {
			t.Fatalf("moved worktree did not survive: %v", statErr)
		}
		if _, statErr := os.Stat(fixture.lane); !os.IsNotExist(statErr) {
			t.Fatalf("old worktree path still exists: %v", statErr)
		}
		if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"); got != branchOID {
			t.Fatalf("target branch changed: got %s, want %s", got, branchOID)
		}
		if got := fixture.readGitignore(t); !strings.Contains(got, "lane/") {
			t.Fatalf("failed removal cleaned managed ignore entry: %q", got)
		}
	})
}

func TestWorktreeRemoveGitAdapterRevalidatesFilesystemAndGitState(t *testing.T) {
	tests := []struct {
		name, want string
		setup      func(*testing.T, *worktreeRemovalFixture) worktreeRemoveHooks
	}{
		{"symlink replacement", "target path is not a real directory", func(t *testing.T, f *worktreeRemovalFixture) worktreeRemoveHooks {
			return worktreeRemoveHooks{beforeFinalCheck: func() error {
				if err := os.Rename(f.lane, f.lane+"-original"); err != nil {
					return err
				}
				return os.Symlink(f.lane+"-original", f.lane)
			}}
		}},
		{"HEAD", "target HEAD changed", func(t *testing.T, f *worktreeRemovalFixture) worktreeRemoveHooks {
			return worktreeRemoveHooks{beforeFinalCheck: func() error { return runWorktreeRemoveHookGit(f.lane, "commit", "--allow-empty", "-m", "interleaved") }}
		}},
		{"cleanliness", "target cleanliness changed", func(t *testing.T, f *worktreeRemovalFixture) worktreeRemoveHooks {
			return worktreeRemoveHooks{beforeFinalCheck: func() error {
				return os.WriteFile(filepath.Join(f.lane, "interleaved.txt"), []byte("changed\n"), 0644)
			}}
		}},
		{"dirty content", "target changed files changed", func(t *testing.T, f *worktreeRemovalFixture) worktreeRemoveHooks {
			f.writeLaneFile(t, "dirty.txt", "alpha\n")
			return worktreeRemoveHooks{beforeFinalCheck: func() error { return os.WriteFile(filepath.Join(f.lane, "dirty.txt"), []byte("bravo\n"), 0644) }}
		}},
		{"comparator", "comparison target changed", func(t *testing.T, f *worktreeRemovalFixture) worktreeRemoveHooks {
			return worktreeRemoveHooks{beforeFinalCheck: func() error { return runWorktreeRemoveHookGit(f.repo, "reset", "--hard", f.baseOID) }}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			if test.name != "dirty content" {
				fixture.commitLane(t, "contained")
				fixture.mergeLaneIntoMain(t)
			}
			hooks := test.setup(t, fixture)
			force := test.name == "dirty content"
			comparator := "main"
			if force {
				comparator = ""
			}
			err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, force, comparator, hooks)
			requireWorktreeRemoveGitAdapterFailure(t, err, test.want)
			if !fixture.registered(t, fixture.lane) {
				t.Fatal("target was removed after revalidation failure")
			}
			if test.name == "dirty content" {
				contents, readErr := os.ReadFile(filepath.Join(fixture.lane, "dirty.txt"))
				if readErr != nil {
					t.Fatalf("read interleaved dirty file after refusal: %v", readErr)
				}
				if got, want := string(contents), "bravo\n"; got != want {
					t.Fatalf("interleaved dirty file after refusal = %q, want %q", got, want)
				}
			}
			if test.name == "symlink replacement" {
				replacement, statErr := os.Lstat(fixture.lane)
				if statErr != nil {
					t.Fatalf("inspect replacement path: %v", statErr)
				}
				if replacement.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("replacement path mode = %s, want symlink", replacement.Mode())
				}
				original, statErr := os.Stat(fixture.lane + "-original")
				if statErr != nil {
					t.Fatalf("inspect original target directory: %v", statErr)
				}
				if !original.IsDir() {
					t.Fatalf("original target mode = %s, want directory", original.Mode())
				}
			}
		})
	}
}

func TestWorktreeRemoveGitAdapterRevalidatesLockStateInBothDirections(t *testing.T) {
	for _, test := range []struct {
		name   string
		before func(*testing.T, *worktreeRemovalFixture) worktreeRemovalSnapshot
		hook   func(*worktreeRemovalFixture) worktreeRemoveHooks
	}{
		{
			name: "locked to unlocked",
			before: func(t *testing.T, fixture *worktreeRemovalFixture) worktreeRemovalSnapshot {
				want := fixture.snapshot(t)
				fixture.git(t, fixture.repo, "worktree", "lock", fixture.lane)
				return want
			},
			hook: func(fixture *worktreeRemovalFixture) worktreeRemoveHooks {
				return worktreeRemoveHooks{beforeFinalCheck: func() error {
					return runWorktreeRemoveHookGit(fixture.repo, "worktree", "unlock", "--", fixture.lane)
				}}
			},
		},
		{
			name: "unlocked to locked",
			before: func(t *testing.T, fixture *worktreeRemovalFixture) worktreeRemovalSnapshot {
				fixture.git(t, fixture.repo, "worktree", "lock", fixture.lane)
				want := fixture.snapshot(t)
				fixture.git(t, fixture.repo, "worktree", "unlock", fixture.lane)
				return want
			},
			hook: func(fixture *worktreeRemovalFixture) worktreeRemoveHooks {
				return worktreeRemoveHooks{beforeFinalCheck: func() error {
					return runWorktreeRemoveHookGit(fixture.repo, "worktree", "lock", "--", fixture.lane)
				}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			want := test.before(t, fixture)
			err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, "main", test.hook(fixture))
			requireWorktreeRemoveGitAdapterFailure(t, err, "registered target identity changed")
			fixture.assertSnapshot(t, want)
		})
	}
}

func TestWorktreeRemoveGitAdapterRejectsUnsafeGitignore(t *testing.T) {
	for _, test := range []struct {
		name       string
		makeUnsafe func(*testing.T, string) func()
	}{
		{"directory", func(t *testing.T, path string) func() {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
			return func() {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("inspect unsafe .gitignore directory: %v", err)
				}
				if !info.IsDir() {
					t.Fatalf("unsafe .gitignore mode = %s, want directory", info.Mode())
				}
			}
		}},
		{"symlink", func(t *testing.T, path string) func() {
			external := filepath.Join(t.TempDir(), "external")
			externalContent := []byte("external sentinel\n")
			if err := os.WriteFile(external, externalContent, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, path); err != nil {
				t.Fatal(err)
			}
			return func() {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("inspect unsafe .gitignore symlink: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("unsafe .gitignore mode = %s, want symlink", info.Mode())
				}
				content, err := os.ReadFile(external)
				if err != nil {
					t.Fatalf("read external sentinel: %v", err)
				}
				if !bytes.Equal(content, externalContent) {
					t.Fatalf("external symlink target changed: %q", content)
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeRemovalFixture(t)
			fixture.commitLane(t, "contained")
			fixture.mergeLaneIntoMain(t)
			beforeRegistry := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z")
			beforeHead := fixture.git(t, fixture.lane, "rev-parse", "HEAD")
			beforeBranch := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane")
			beforeStatus := fixture.git(t, fixture.lane, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
			verifyUnsafe := test.makeUnsafe(t, filepath.Join(fixture.repo, ".gitignore"))
			err := runWorktreeRemoveGitAdapter(t, fixture.repo, fixture.lane, false, "main", worktreeRemoveHooks{})
			requireWorktreeRemoveGitAdapterFailure(t, err, "is not a regular file")
			if !fixture.registered(t, fixture.lane) {
				t.Fatal("unsafe .gitignore removed target")
			}
			if got := fixture.git(t, fixture.repo, "worktree", "list", "--porcelain", "-z"); got != beforeRegistry {
				t.Fatalf("unsafe .gitignore refusal changed registry\ngot:  %q\nwant: %q", got, beforeRegistry)
			}
			if got := fixture.git(t, fixture.lane, "rev-parse", "HEAD"); got != beforeHead {
				t.Fatalf("unsafe .gitignore refusal changed HEAD: got %s, want %s", got, beforeHead)
			}
			if got := fixture.git(t, fixture.repo, "rev-parse", "refs/heads/lane"); got != beforeBranch {
				t.Fatalf("unsafe .gitignore refusal changed branch: got %s, want %s", got, beforeBranch)
			}
			if got := fixture.git(t, fixture.lane, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching"); got != beforeStatus {
				t.Fatalf("unsafe .gitignore refusal changed target status: got %q, want %q", got, beforeStatus)
			}
			verifyUnsafe()
		})
	}
}
