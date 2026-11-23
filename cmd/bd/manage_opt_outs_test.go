package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/git"
	storagefs "github.com/steveyegge/beads/internal/storage/fs"
	storagegit "github.com/steveyegge/beads/internal/storage/git"
	"github.com/stretchr/testify/require"
)

// The hooks.manage and gitignore.manage opt-outs (bd-cxm9r) on cmd/bd's own
// writers: with the key false every hook and .gitignore writer leaves the files
// byte-identical, and with the key unset bd still manages them.

// clearManageEnvForTest unsets the environment overrides of both manage keys.
func clearManageEnvForTest(t *testing.T) {
	t.Helper()
	for _, name := range []string{"BD_HOOKS_MANAGE", "BD_GITIGNORE_MANAGE"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}

// manageTreeSnapshot maps every path under each root to its mode and content
// (or "absent").
func manageTreeSnapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if os.IsNotExist(err) && path == root {
				snap[root] = "absent"
				return nil
			}
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				snap[path] = "dir " + info.Mode().String()
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snap[path] = info.Mode().String() + "\n" + string(data)
			return nil
		})
		require.NoError(t, err)
	}
	return snap
}

// manageHooksPathConfig reads core.hooksPath from repo ("" when unset).
func manageHooksPathConfig(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--local", "--get", "core.hooksPath")
	cmd.Dir = repo
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// manageHookRepo is a git repository with a user pre-commit hook and a .beads
// directory whose config.yaml holds configYAML. It returns the repository, its
// hooks directory and its .beads directory.
func manageHookRepo(t *testing.T, configYAML string) (repo, hooksDir, beadsDir string) {
	t.Helper()
	repo = newGitRepo(t)
	hooksDir = filepath.Join(repo, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\necho mine\n"), 0o755))
	beadsDir = filepath.Join(repo, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600))
	return repo, hooksDir, beadsDir
}

const (
	manageHooksOff     = "hooks:\n  manage: false\n"
	manageGitignoreOff = "gitignore:\n  manage: false\n"
	manageKeysUnset    = "issue-prefix: demo\n"
)

func TestHooksManageFalseRefusesEveryInstaller(t *testing.T) {
	clearManageEnvForTest(t)
	repo, hooksDir, beadsDir := manageHookRepo(t, manageKeysUnset)
	runInDir(t, repo, func() {
		roots := []string{hooksDir, filepath.Join(beadsDir, "hooks"), filepath.Join(repo, ".beads-hooks")}
		before, hooksPath := manageTreeSnapshot(t, roots...), manageHooksPathConfig(t, repo)

		t.Setenv("BD_HOOKS_MANAGE", "false")
		require.ErrorIs(t, installHooksWithOptions(managedHookNames, false, false, false, false), errHooksNotManaged)
		require.ErrorIs(t, installJJHooks(), errHooksNotManaged)
		require.ErrorIs(t, installStandaloneHooks(managedHookNames, false, true, false, false), errHooksNotManaged)
		require.ErrorIs(t, installHooksWithOptions(managedHookNames, false, false, false, true), errHooksNotManaged)

		require.Equal(t, before, manageTreeSnapshot(t, roots...))
		require.Equal(t, hooksPath, manageHooksPathConfig(t, repo), "core.hooksPath changed")
	})
}

// TestHooksManageFalseExplicitInstallRefusesNonZero pins the refusal of an
// explicit bd hooks install: it exits non-zero, writes nothing, and names the
// layer that turns hooks.manage off with the change that undoes it there.
// `bd config set` writes only the tracked config.yaml, so only that layer
// advises it.
func TestHooksManageFalseExplicitInstallRefusesNonZero(t *testing.T) {
	clearManageEnvForTest(t)
	isolateBeadsDirForTest(t)
	for _, tc := range []struct {
		name, configYAML, localYAML, env string
		want, wantNot                    []string
	}{
		{
			name: "tracked config.yaml", configYAML: manageHooksOff,
			want: []string{"hooks.manage is false in", "config.yaml", "bd config set hooks.manage true"},
		},
		{
			name: "config.local.yaml", configYAML: manageKeysUnset, localYAML: manageHooksOff,
			want: []string{"hooks.manage is false in", "config.local.yaml", "set it to true there"}, wantNot: []string{"bd config set"},
		},
		{
			name: "environment", configYAML: manageKeysUnset, env: "false",
			want: []string{"BD_HOOKS_MANAGE=false", "unset BD_HOOKS_MANAGE"}, wantNot: []string{"bd config set"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, hooksDir, beadsDir := manageHookRepo(t, tc.configYAML)
			if tc.localYAML != "" {
				require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.local.yaml"), []byte(tc.localYAML), 0o600))
			}
			runInDir(t, repo, func() {
				initConfigForTest(t)
				setStandaloneHookMode(t, "")
				if tc.env != "" {
					t.Setenv("BD_HOOKS_MANAGE", tc.env)
				}
				before := manageTreeSnapshot(t, hooksDir)
				var runErr error
				stderr := captureStderr(t, func() { runErr = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
				require.Error(t, runErr, "an explicit install under hooks.manage: false must exit non-zero")
				require.Contains(t, stderr, "not managed by bd")
				for _, want := range tc.want {
					require.Contains(t, stderr, want)
				}
				for _, wantNot := range tc.wantNot {
					require.NotContains(t, stderr, wantNot)
				}
				require.Equal(t, before, manageTreeSnapshot(t, hooksDir))
			})
		})
	}
}

func TestHooksManageUnsetStillInstalls(t *testing.T) {
	clearManageEnvForTest(t)
	repo, hooksDir, _ := manageHookRepo(t, manageKeysUnset)
	runInDir(t, repo, func() {
		require.NoError(t, installHooksWithOptions(managedHookNames, false, false, false, false))
		got, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
		require.NoError(t, err)
		require.Contains(t, string(got), "echo mine")
		require.Contains(t, string(got), hookSectionBeginPrefix)
	})
}

func TestHooksManageFalseInTheInitTargetSkipsInitHooks(t *testing.T) {
	clearManageEnvForTest(t)

	t.Run("not managed", func(t *testing.T) {
		// The launching process has no opt-out; the workspace init targets does.
		repo, hooksDir, beadsDir := manageHookRepo(t, manageHooksOff)
		runInDir(t, repo, func() {
			roots := []string{hooksDir, filepath.Join(beadsDir, "hooks")}
			before, hooksPath := manageTreeSnapshot(t, roots...), manageHooksPathConfig(t, repo)
			out := captureStdout(t, func() error {
				runEmbeddedInitHooks(t.Context(), repo, beadsDir, false, false)
				return nil
			})
			require.Contains(t, out, hooksNotManagedNote)

			selected, err := resolveInitHooksContext(repo, beadsDir)
			require.NoError(t, err)
			require.ErrorIs(t, installHooksWithContext(managedHookNames, false, false, false, true, selected), errHooksNotManaged)

			require.Equal(t, before, manageTreeSnapshot(t, roots...))
			require.Equal(t, hooksPath, manageHooksPathConfig(t, repo), "core.hooksPath changed")
		})
	})

	t.Run("key unset still installs", func(t *testing.T) {
		repo, _, beadsDir := manageHookRepo(t, manageKeysUnset)
		runInDir(t, repo, func() {
			out := captureStdout(t, func() error {
				runEmbeddedInitHooks(t.Context(), repo, beadsDir, false, false)
				return nil
			})
			require.NotContains(t, out, hooksNotManagedNote)
			got, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-commit"))
			require.NoError(t, err)
			require.Contains(t, string(got), hookSectionBeginPrefix)
		})
	})
}

func TestHooksManageFalseConfigDriftAndApply(t *testing.T) {
	clearManageEnvForTest(t)

	t.Run("not managed", func(t *testing.T) {
		repo := newGitRepo(t)
		runInDir(t, repo, func() {
			hooksDir, err := git.GetGitHooksDir()
			require.NoError(t, err)
			t.Setenv("BD_HOOKS_MANAGE", "false")
			before := manageTreeSnapshot(t, hooksDir)

			items := checkHooksDrift()
			require.Len(t, items, 1)
			require.Equal(t, driftStatusSkipped, items[0].Status)
			require.Equal(t, hooksNotManagedDriftMessage, items[0].Message)

			result := applyHooks(true, false)
			require.Equal(t, applyStatusSkipped, result.Status, result.Message+" "+result.Error)
			require.Equal(t, hooksNotManagedDriftMessage, result.Message)

			require.Equal(t, before, manageTreeSnapshot(t, hooksDir))
		})
	})

	t.Run("key unset still managed", func(t *testing.T) {
		repo := newGitRepo(t)
		runInDir(t, repo, func() {
			hooksDir, err := git.GetGitHooksDir()
			require.NoError(t, err)
			require.Contains(t, driftDomains(checkHooksDrift()), "hooks", "missing hooks must be drift")
			result := applyHooks(true, false)
			require.Equal(t, applyStatusApplied, result.Status, result.Message+" "+result.Error)
			_, err = os.Stat(filepath.Join(hooksDir, "pre-commit"))
			require.NoError(t, err)
		})
	})
}

func TestHooksManageFalseHidesTheInfoHookWarning(t *testing.T) {
	clearManageEnvForTest(t)
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	repo := newGitRepo(t)
	runInDir(t, repo, func() {
		render := func() string {
			return captureStdout(t, func() error {
				return renderInfo(map[string]interface{}{"mode": "direct"}, false, "/db", false)
			})
		}
		require.Contains(t, render(), "Git hooks not installed", "with hooks.manage unset bd info must warn")
		t.Setenv("BD_HOOKS_MANAGE", "false")
		require.NotContains(t, render(), "Git hooks not installed")
	})
}

// manageMigrationRepo is a hook-migration fixture: a legacy bd shim with a
// .old sidecar, and a .beads/config.yaml holding configYAML.
func manageMigrationRepo(t *testing.T, configYAML string) (repo, hooksDir string) {
	t.Helper()
	repo, hooksDir = setupHookMigrationRepo(t)
	preCommit := filepath.Join(hooksDir, "pre-commit")
	writeHookMigrationFile(t, preCommit, "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookMigrationFile(t, preCommit+".old", "#!/usr/bin/env sh\necho old-custom\n")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".beads"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "config.yaml"), []byte(configYAML), 0o600))
	return repo, hooksDir
}

func TestHooksManageFalseRefusesHookMigration(t *testing.T) {
	clearManageEnvForTest(t)

	t.Run("not managed", func(t *testing.T) {
		repo, hooksDir := manageMigrationRepo(t, manageHooksOff)
		before := manageTreeSnapshot(t, hooksDir)
		plan, err := doctor.PlanHookMigration(repo)
		require.NoError(t, err)
		execPlan := buildHookMigrationExecutionPlan(plan)
		require.NotZero(t, execPlan.operationCount(), "the fixture must need a migration")
		_, err = applyHookMigrationExecution(execPlan)
		require.ErrorIs(t, err, errHooksNotManaged)
		require.Equal(t, before, manageTreeSnapshot(t, hooksDir))
	})

	t.Run("key unset still migrates", func(t *testing.T) {
		repo, hooksDir := manageMigrationRepo(t, manageKeysUnset)
		plan, err := doctor.PlanHookMigration(repo)
		require.NoError(t, err)
		summary, err := applyHookMigrationExecution(buildHookMigrationExecutionPlan(plan))
		require.NoError(t, err)
		require.Equal(t, 1, summary.WrittenHookCount)
		require.Contains(t, mustReadHookMigrationFile(t, filepath.Join(hooksDir, "pre-commit")), hookSectionBeginPrefix)
	})
}

// TestHooksManageFalseMigrateHooksApplyRefuses pins bd migrate hooks --apply
// under hooks.manage: false. It refuses before it prints the plan or asks for
// consent, and leaves the hooks alone, whether it is pointed at the repository
// root, run from a subdirectory with or without a path (the plan finds the
// repository's hooks from either), or run against a workspace named by
// BEADS_DIR. With the key unset the same subdirectory run migrates.
func TestHooksManageFalseMigrateHooksApplyRefuses(t *testing.T) {
	clearManageEnvForTest(t)
	isolateBeadsDirForTest(t)
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, flag := range []string{"apply", "yes"} {
		f := migrateHooksCmd.Flags().Lookup(flag)
		old, changed := f.Value.String(), f.Changed
		t.Cleanup(func() {
			require.NoError(t, migrateHooksCmd.Flags().Set(flag, old))
			f.Changed = changed
		})
		require.NoError(t, migrateHooksCmd.Flags().Set(flag, "true"))
	}

	// migrate runs bd migrate hooks --apply --yes in dir, with the
	// configuration loaded there, and returns its error, stdout and stderr.
	migrate := func(t *testing.T, dir string, args []string) (runErr error, stdout, stderr string) {
		t.Helper()
		runInDir(t, dir, func() {
			initConfigForTest(t)
			stdout, stderr = captureStdio(t, func() { runErr = migrateHooksCmd.RunE(migrateHooksCmd, args) })
		})
		return runErr, stdout, stderr
	}
	requireRefused := func(t *testing.T, hooksDir, dir string, args []string) {
		t.Helper()
		before := manageTreeSnapshot(t, hooksDir)
		runErr, stdout, stderr := migrate(t, dir, args)
		require.Error(t, runErr, "bd migrate hooks --apply under hooks.manage: false must exit non-zero")
		require.Contains(t, stderr, "not managed by bd")
		require.NotContains(t, stdout, "Hook migration plan", "the refusal comes before the plan is printed")
		require.Equal(t, before, manageTreeSnapshot(t, hooksDir))
	}
	subdirectory := func(t *testing.T, repo string) string {
		t.Helper()
		sub := filepath.Join(repo, "sub")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		return sub
	}

	t.Run("repository root path", func(t *testing.T) {
		repo, hooksDir := manageMigrationRepo(t, manageHooksOff)
		requireRefused(t, hooksDir, t.TempDir(), []string{repo})
	})
	t.Run("from a subdirectory", func(t *testing.T) {
		repo, hooksDir := manageMigrationRepo(t, manageHooksOff)
		requireRefused(t, hooksDir, subdirectory(t, repo), nil)
	})
	t.Run("subdirectory path from outside", func(t *testing.T) {
		// Nothing loaded from the launching directory: only the repository's
		// own workspace can refuse.
		repo, hooksDir := manageMigrationRepo(t, manageHooksOff)
		requireRefused(t, hooksDir, t.TempDir(), []string{subdirectory(t, repo)})
	})
	t.Run("workspace named by BEADS_DIR", func(t *testing.T) {
		repo, hooksDir := manageMigrationRepo(t, manageKeysUnset)
		workspace := filepath.Join(t.TempDir(), ".beads")
		require.NoError(t, os.MkdirAll(workspace, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "config.yaml"), []byte(manageHooksOff), 0o600))
		t.Setenv("BEADS_DIR", workspace)
		requireRefused(t, hooksDir, repo, nil)
	})
	t.Run("key unset still migrates from a subdirectory", func(t *testing.T) {
		repo, hooksDir := manageMigrationRepo(t, manageKeysUnset)
		runErr, _, stderr := migrate(t, subdirectory(t, repo), nil)
		require.NoError(t, runErr, stderr)
		require.Contains(t, mustReadHookMigrationFile(t, filepath.Join(hooksDir, "pre-commit")), hookSectionBeginPrefix)
	})
}

// TestHooksManageFalseCheckHealthReportsNoHooks pins bd doctor --check-health
// under hooks.manage: false: bd never refreshes those hooks, so an outdated bd
// section in one is not reported. With the key unset it still is.
func TestHooksManageFalseCheckHealthReportsNoHooks(t *testing.T) {
	clearManageEnvForTest(t)
	isolateBeadsDirForTest(t)
	for _, tc := range []struct {
		name, configYAML string
		wantHint         bool
	}{
		{"not managed", manageHooksOff, false},
		{"key unset still reported", manageKeysUnset, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, hooksDir, _ := manageHookRepo(t, tc.configYAML)
			outdated := "#!/bin/sh\n# bd-hooks-version: 0.0.1\nbd hooks run pre-commit \"$@\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(outdated), 0o755))
			runInDir(t, repo, func() {
				var runErr error
				stderr := captureStderr(t, func() { runErr = runCheckHealth(repo) })
				if tc.wantHint {
					require.Error(t, runErr)
					require.Contains(t, stderr, "pre-commit outdated")
					return
				}
				require.NoError(t, runErr)
				require.Empty(t, stderr)
			})
		})
	}
}

func TestGitignoreManageFalseLeavesStealthCleanupAlone(t *testing.T) {
	clearManageEnvForTest(t)
	leaked := "node_modules/\n\n" + doctor.ProjectGitignoreHeader + "\n" + strings.Join(doctor.ProjectGitignorePatterns, "\n") + "\n"
	stealthRepo := func(t *testing.T, configYAML string) string {
		t.Helper()
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".beads"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "config.yaml"), []byte(configYAML), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(leaked), 0o644))
		return repo
	}

	owned := stealthRepo(t, manageGitignoreOff)
	before := manageTreeSnapshot(t, filepath.Join(owned, ".gitignore"))
	require.False(t, trackedGitignoreHasBeadsSection(owned), "an owned .gitignore is not a stealth leak")
	removed, err := removeBeadsProjectGitignoreSection(owned)
	require.NoError(t, err)
	require.False(t, removed)
	require.Equal(t, before, manageTreeSnapshot(t, filepath.Join(owned, ".gitignore")))

	managed := stealthRepo(t, manageKeysUnset)
	require.True(t, trackedGitignoreHasBeadsSection(managed))
	removed, err = removeBeadsProjectGitignoreSection(managed)
	require.NoError(t, err)
	require.True(t, removed, "with gitignore.manage unset the leaked section is still removed")
}

// TestHooksManageFalseDoctorRunReportsGitHooksNotManaged pins the bd doctor
// run itself, not only the check it calls: the Git Hooks row of runDiagnostics
// reads hooks.manage from the repository under diagnosis.
func TestHooksManageFalseDoctorRunReportsGitHooksNotManaged(t *testing.T) {
	clearManageEnvForTest(t)
	gitHooksRow := func(t *testing.T, configYAML string) doctorCheck {
		t.Helper()
		// The user pre-commit hook carries no bd section, so a managed
		// repository gets a warning for it.
		repo, _, _ := manageHookRepo(t, configYAML)
		var row *doctorCheck
		runInDir(t, repo, func() {
			result := runDiagnostics(repo)
			for i := range result.Checks {
				if result.Checks[i].Name == "Git Hooks" {
					row = &result.Checks[i]
				}
			}
		})
		require.NotNil(t, row, "bd doctor reported no Git Hooks row")
		return *row
	}

	off := gitHooksRow(t, manageHooksOff)
	require.Equal(t, statusOK, off.Status, "Git Hooks under hooks.manage: false: %q", off.Message)
	require.Contains(t, off.Message, "hooks.manage=false")
	require.Empty(t, off.Fix, "a not-managed row must offer no fix")

	unset := gitHooksRow(t, manageKeysUnset)
	require.Equal(t, statusWarning, unset.Status, "Git Hooks with the key unset: %q", unset.Message)
}

// TestHooksManageFalseProxiedInitTailSkipsHooks pins the server-mode init
// tail, which resolves and installs hooks on its own path: with hooks.manage
// false in the storage it is initializing it prints the note and installs
// nothing, and with the key unset it still installs.
func TestHooksManageFalseProxiedInitTailSkipsHooks(t *testing.T) {
	clearManageEnvForTest(t)
	for _, tc := range []struct {
		name, configYAML string
		wantInstall      bool
	}{
		{"not managed", manageHooksOff, false},
		{"key unset still installs", manageKeysUnset, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, _, storage, _ := newInitHooksFixture(t)
			// An empty hooks directory: no bd hook is installed in the selected
			// repository, so a managed tail has an install to do.
			empty := t.TempDir()
			initExcludeGit(t, selected, "config", "core.hooksPath", empty)
			require.NoError(t, os.WriteFile(filepath.Join(storage, "config.yaml"), []byte(tc.configYAML), 0o600))

			fs := storagefs.NewFileSystemProvider(selected, newBeadsDirTemplates(), newInitFileSystemAdapters(selected)).BeadsDirFSUseCase()
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", true, "")
			in := initProxiedServerInput{skipAgents: true, nonInteractive: true}
			tail := runInitTailContext{workDir: selected, beadsDir: storage, remoteURL: "file:///unused-hooks-fixture-remote", fsUseCase: fs, gitUC: storagegit.NewGitProvider(selected).GitUseCase()}
			out := captureStdout(t, func() error { return runInitProxiedServerTail(cmd, t.Context(), in, tail) })

			installed := filepath.Join(storage, "hooks", "pre-commit")
			if tc.wantInstall {
				require.NotContains(t, out, hooksNotManagedNote)
				require.Contains(t, string(readInitHooksFile(t, installed)), hookSectionBeginPrefix)
				return
			}
			require.Contains(t, out, hooksNotManagedNote)
			_, err := os.Stat(installed)
			require.ErrorIs(t, err, os.ErrNotExist, "the tail installed hooks under hooks.manage: false")
			require.Equal(t, empty, initExcludeGit(t, selected, "config", "--local", "--get", "core.hooksPath"), "core.hooksPath changed")
		})
	}
}

// TestGitignoreManageFalseInitCommitStagesOnlyBdFiles pins bd init's bootstrap
// commit under gitignore.manage: false, set in the workspace or in the user
// config. bd did not write that .beads/.gitignore and cannot tell that it keeps
// the database out, so both init paths stage bd's own files by name: never the
// database, a machine-local file, another file of the repository's, or the
// project .gitignore bd did not change. With the key unset the commit still
// stages the whole directory through the .beads/.gitignore bd wrote.
func TestGitignoreManageFalseInitCommitStagesOnlyBdFiles(t *testing.T) {
	clearManageEnvForTest(t)
	isolateBeadsDirForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	userConfig := filepath.Join(home, ".config", "bd", "config.yaml")

	bdFiles := []string{".beads/README.md", ".beads/config.yaml", ".beads/hooks/pre-commit", ".beads/metadata.json"}
	managedFiles := []string{".beads/.gitignore", ".beads/README.md", ".beads/config.yaml", ".beads/extra.txt",
		".beads/hooks/pre-commit", ".beads/metadata.json", ".gitignore"}

	// initCommitRepo is a repository holding what bd init leaves in .beads,
	// configYAML as its config.yaml, beadsGitignore as its .beads/.gitignore
	// ("" for none), and an untracked project .gitignore.
	initCommitRepo := func(t *testing.T, configYAML, beadsGitignore string) string {
		t.Helper()
		repo := newGitRepo(t)
		files := map[string]string{
			".beads/config.yaml":                           configYAML,
			".beads/metadata.json":                         "{}\n",
			".beads/README.md":                             "# beads\n",
			".beads/hooks/pre-commit":                      "#!/bin/sh\n",
			".beads/embeddeddolt/demo/.dolt/noms/manifest": "database\n",
			".beads/.local_version":                        "1.0.0\n",
			".beads/extra.txt":                             "the repository's own\n",
			".gitignore":                                   "node_modules/\n",
		}
		if beadsGitignore != "" {
			files[".beads/.gitignore"] = beadsGitignore
		}
		for name, content := range files {
			path := filepath.Join(repo, filepath.FromSlash(name))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		}
		return repo
	}
	committed := func(t *testing.T, repo string) []string {
		t.Helper()
		cmd := exec.Command("git", "show", "--name-only", "--format=", "HEAD")
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.Fields(string(out))
	}
	commitEmbedded := func(t *testing.T, repo string) []string {
		t.Helper()
		commitEmbeddedInitArtifacts(repo, true)
		return committed(t, repo)
	}
	commitProxied := func(t *testing.T, repo string) []string {
		t.Helper()
		_, err := storagegit.NewGitProvider(repo).GitUseCase().CommitInitArtifacts(t.Context(), proxiedInitCommitParams(repo))
		require.NoError(t, err)
		return committed(t, repo)
	}

	for _, path := range []struct {
		name   string
		commit func(*testing.T, string) []string
	}{{"embedded", commitEmbedded}, {"proxied", commitProxied}} {
		t.Run(path.name, func(t *testing.T) {
			t.Run("workspace opt-out", func(t *testing.T) {
				require.Equal(t, bdFiles, path.commit(t, initCommitRepo(t, manageGitignoreOff, "")))
			})
			t.Run("user config opt-out", func(t *testing.T) {
				require.NoError(t, os.MkdirAll(filepath.Dir(userConfig), 0o700))
				require.NoError(t, os.WriteFile(userConfig, []byte(manageGitignoreOff), 0o600))
				t.Cleanup(func() { require.NoError(t, os.Remove(userConfig)) })
				require.Equal(t, bdFiles, path.commit(t, initCommitRepo(t, manageKeysUnset, "")))
			})
			t.Run("key unset", func(t *testing.T) {
				require.Equal(t, managedFiles, path.commit(t, initCommitRepo(t, manageKeysUnset, doctor.GitignoreTemplate)))
			})
		})
	}

	t.Run("embedded: a bd file the repository ignores stays out", func(t *testing.T) {
		repo := initCommitRepo(t, manageGitignoreOff, "README.md\n")
		require.Equal(t, []string{".beads/config.yaml", ".beads/hooks/pre-commit", ".beads/metadata.json"}, commitEmbedded(t, repo))
	})
}
