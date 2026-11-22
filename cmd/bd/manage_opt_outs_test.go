package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/git"
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

func TestHooksManageFalseExplicitInstallRefusesNonZero(t *testing.T) {
	clearManageEnvForTest(t)
	repo, hooksDir, _ := manageHookRepo(t, manageKeysUnset)
	runInDir(t, repo, func() {
		setStandaloneHookMode(t, "")
		t.Setenv("BD_HOOKS_MANAGE", "false")
		before := manageTreeSnapshot(t, hooksDir)
		var runErr error
		stderr := captureStderr(t, func() { runErr = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
		require.Error(t, runErr, "an explicit install under hooks.manage: false must exit non-zero")
		require.Contains(t, stderr, "hooks.manage")
		require.Contains(t, stderr, "bd config set hooks.manage true")
		require.Equal(t, before, manageTreeSnapshot(t, hooksDir))
	})
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

func TestHooksManageFalseMigrateHooksApplyRefusesBeforePlanning(t *testing.T) {
	clearManageEnvForTest(t)
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

	repo, hooksDir := manageMigrationRepo(t, manageHooksOff)
	before := manageTreeSnapshot(t, hooksDir)
	var runErr error
	out := captureStdout(t, func() error {
		runErr = migrateHooksCmd.RunE(migrateHooksCmd, []string{repo})
		return nil
	})
	require.Error(t, runErr, "bd migrate hooks --apply under hooks.manage: false must exit non-zero")
	require.NotContains(t, out, "Hook migration plan", "the refusal comes before the plan is built or printed")
	require.Equal(t, before, manageTreeSnapshot(t, hooksDir))
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
