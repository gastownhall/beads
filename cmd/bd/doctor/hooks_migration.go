package doctor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	hookMarkerStateNone   = "none"
	hookMarkerStateValid  = "valid"
	hookMarkerStateBroken = "broken"

	hookMarkerBeginTag = "BEGIN BEADS INTEGRATION"
	hookMarkerEndTag   = "END BEADS INTEGRATION"
)

var managedHookNames = []string{
	"pre-commit",
	"post-merge",
	"pre-push",
	"post-checkout",
	"prepare-commit-msg",
}

// HookMigrationHookPlan describes migration state for a single hook file.
type HookMigrationHookPlan struct {
	Name             string `json:"name"`
	HookPath         string `json:"hook_path"`
	Exists           bool   `json:"exists"`
	MarkerState      string `json:"marker_state"`
	LegacyBDHook     bool   `json:"legacy_bd_hook"`
	HasOldSidecar    bool   `json:"has_old_sidecar"`
	HasBackupSidecar bool   `json:"has_backup_sidecar"`
	State            string `json:"state"`
	NeedsMigration   bool   `json:"needs_migration"`
	SuggestedAction  string `json:"suggested_action,omitempty"`
	ReadError        string `json:"read_error,omitempty"`
}

// HookMigrationPlan summarizes migration state for all managed hooks.
type HookMigrationPlan struct {
	Path                string                  `json:"path"`
	RepoRoot            string                  `json:"repo_root,omitempty"`
	HooksDir            string                  `json:"hooks_dir,omitempty"`
	IsGitRepo           bool                    `json:"is_git_repo"`
	Hooks               []HookMigrationHookPlan `json:"hooks"`
	TotalHooks          int                     `json:"total_hooks"`
	NeedsMigrationCount int                     `json:"needs_migration_count"`
	BrokenMarkerCount   int                     `json:"broken_marker_count"`
}

// PlanHookMigration builds a read-only migration plan for git hooks.
func PlanHookMigration(path string) (HookMigrationPlan, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return HookMigrationPlan{}, fmt.Errorf("resolve path: %w", err)
	}

	plan := HookMigrationPlan{
		Path:       absPath,
		TotalHooks: len(managedHookNames),
		Hooks:      make([]HookMigrationHookPlan, 0, len(managedHookNames)),
	}

	repoRoot, hooksDir, err := resolveGitHooksDir(absPath)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Not a git repository (or no git metadata reachable from path).
			return plan, nil
		}
		return HookMigrationPlan{}, err
	}

	plan.IsGitRepo = true
	plan.RepoRoot = repoRoot
	plan.HooksDir = hooksDir

	for _, hookName := range managedHookNames {
		hook := inspectHookMigration(hooksDir, hookName)
		if hook.NeedsMigration {
			plan.NeedsMigrationCount++
		}
		if hook.MarkerState == hookMarkerStateBroken {
			plan.BrokenMarkerCount++
		}
		plan.Hooks = append(plan.Hooks, hook)
	}

	return plan, nil
}

func inspectHookMigration(hooksDir, hookName string) HookMigrationHookPlan {
	hookPath := filepath.Join(hooksDir, hookName)
	plan := HookMigrationHookPlan{
		Name:             hookName,
		HookPath:         hookPath,
		HasOldSidecar:    fileExists(hookPath + ".old"),
		HasBackupSidecar: fileExists(hookPath + ".backup"),
		MarkerState:      hookMarkerStateNone,
	}

	content, err := os.ReadFile(hookPath) // #nosec G304 -- path is derived from git hooks dir + known hook names
	if err == nil {
		plan.Exists = true
		contentStr := string(content)
		plan.MarkerState = detectHookMarkerState(contentStr)
		plan.LegacyBDHook = isLegacyBDHook(contentStr)
	} else if !errors.Is(err, os.ErrNotExist) {
		plan.ReadError = err.Error()
		plan.State = "read_error"
		plan.SuggestedAction = "Inspect hook file permissions/content manually before migration."
		return plan
	}

	classifyHookMigration(&plan)
	return plan
}

func classifyHookMigration(hook *HookMigrationHookPlan) {
	if hook.ReadError != "" {
		return
	}

	switch hook.MarkerState {
	case hookMarkerStateValid:
		hook.State = "marker_managed"
		return
	case hookMarkerStateBroken:
		hook.State = "marker_broken"
		hook.NeedsMigration = true
		hook.SuggestedAction = "Repair BEGIN/END marker mismatch, then rerun hook migration."
		return
	}

	if hook.LegacyBDHook {
		hook.NeedsMigration = true
		switch {
		case hook.HasOldSidecar && hook.HasBackupSidecar:
			hook.State = "legacy_with_both_sidecars"
			hook.SuggestedAction = "Prefer .old as preserved body, retain sidecars as migrated artifacts, inject managed section."
		case hook.HasOldSidecar:
			hook.State = "legacy_with_old_sidecar"
			hook.SuggestedAction = "Restore preserved body from .old and inject managed section."
		case hook.HasBackupSidecar:
			hook.State = "legacy_with_backup_sidecar"
			hook.SuggestedAction = "Restore preserved body from .backup and inject managed section."
		default:
			hook.State = "legacy_only"
			hook.SuggestedAction = "Convert legacy hook in place to managed marker section."
		}
		return
	}

	if !hook.Exists {
		switch {
		case hook.HasOldSidecar && hook.HasBackupSidecar:
			hook.State = "missing_with_both_sidecars"
			hook.NeedsMigration = true
			hook.SuggestedAction = "Recreate hook from sidecar content and inject managed section."
		case hook.HasOldSidecar:
			hook.State = "missing_with_old_sidecar"
			hook.NeedsMigration = true
			hook.SuggestedAction = "Recreate hook from .old sidecar and inject managed section."
		case hook.HasBackupSidecar:
			hook.State = "missing_with_backup_sidecar"
			hook.NeedsMigration = true
			hook.SuggestedAction = "Recreate hook from .backup sidecar and inject managed section."
		default:
			hook.State = "missing_no_artifacts"
		}
		return
	}

	if hook.HasOldSidecar || hook.HasBackupSidecar {
		hook.State = "custom_with_sidecars"
		hook.NeedsMigration = true
		hook.SuggestedAction = "Preserve custom hook body, inject managed section, retire sidecar artifacts."
		return
	}

	hook.State = "unmanaged_custom"
}

func detectHookMarkerState(content string) string {
	hasBegin := strings.Contains(content, hookMarkerBeginTag)
	hasEnd := strings.Contains(content, hookMarkerEndTag)

	switch {
	case hasBegin && hasEnd:
		return hookMarkerStateValid
	case hasBegin || hasEnd:
		return hookMarkerStateBroken
	default:
		return hookMarkerStateNone
	}
}

func isLegacyBDHook(content string) bool {
	return strings.Contains(content, "# bd-shim") ||
		strings.Contains(content, "bd-hooks-version:") ||
		strings.Contains(content, "# bd (beads)")
}

func resolveGitHooksDir(path string) (repoRoot string, hooksDir string, err error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel", "--git-common-dir")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("unexpected git rev-parse output")
	}

	repoRoot = strings.TrimSpace(lines[0])
	gitCommonDir := strings.TrimSpace(lines[1])
	if !filepath.IsAbs(gitCommonDir) {
		gitCommonDir = filepath.Join(repoRoot, gitCommonDir)
	}

	hooksDir = filepath.Join(gitCommonDir, "hooks")

	hooksPathCmd := exec.Command("git", "config", "--get", "core.hooksPath")
	hooksPathCmd.Dir = repoRoot
	if hooksPathOut, hooksPathErr := hooksPathCmd.Output(); hooksPathErr == nil {
		hooksPath := strings.TrimSpace(string(hooksPathOut))
		if hooksPath != "" {
			hooksPath = expandHookPathTilde(hooksPath)
			if !filepath.IsAbs(hooksPath) {
				hooksPath = filepath.Join(repoRoot, hooksPath)
			}
			hooksDir = hooksPath
		}
	}

	return repoRoot, hooksDir, nil
}

func expandHookPathTilde(path string) string {
	switch {
	case strings.HasPrefix(path, "~/"), strings.HasPrefix(path, "~\\"):
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	case path == "~":
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
