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
	// Interpreter names the non-shell interpreter of the active hook, or
	// "binary content"; empty for shell hooks (GH#6348).
	Interpreter string `json:"interpreter,omitempty"`
}

// Hook migration states for hooks run by a non-shell interpreter (GH#6348).
const (
	// HookStateNonShellCustom is a non-shell hook with no bd markers: bd
	// cannot add its shell section, so there is nothing to migrate.
	HookStateNonShellCustom = "non_shell_custom"
	// HookStateNonShellRepair is a non-shell hook holding complete bd
	// sections an earlier install appended; migration removes them.
	HookStateNonShellRepair = "non_shell_marker_repair"
)

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
		if interp, nonShell := NonShellHookInterpreter(contentStr); nonShell {
			plan.Interpreter = interp
		}
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

	// A non-shell active hook is classified by its interpreter before any
	// marker or legacy handling: those assume a shell script (GH#6348).
	if hook.Interpreter != "" {
		switch {
		case hook.MarkerState != hookMarkerStateNone:
			hook.State = HookStateNonShellRepair
			hook.NeedsMigration = true
			hook.SuggestedAction = fmt.Sprintf("Remove bd's shell section from the %s hook, which runs under %s; "+
				"keep the script and its sidecars; bd integration stays skipped.", hook.Name, hook.Interpreter)
		default:
			hook.State = HookStateNonShellCustom
		}
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
	beginCount := strings.Count(content, hookMarkerBeginTag)
	endCount := strings.Count(content, hookMarkerEndTag)

	switch {
	case beginCount == 1 && endCount == 1:
		beginIdx := strings.Index(content, hookMarkerBeginTag)
		endIdx := strings.Index(content, hookMarkerEndTag)
		if beginIdx < endIdx {
			return hookMarkerStateValid
		}
		return hookMarkerStateBroken
	case beginCount == 0 && endCount == 0:
		return hookMarkerStateNone
	default:
		return hookMarkerStateBroken
	}
}

func isLegacyBDHook(content string) bool {
	return strings.Contains(content, "# bd-shim") ||
		strings.Contains(content, "bd-hooks-version:") ||
		strings.Contains(content, "# bd (beads)")
}

// IsUnmodifiedLegacyHook returns true if content contains only known BD-managed
// lines (shebang, shim markers, hook delegation, shell boilerplate). If the user
// added custom logic to a legacy shim, this returns false.
func IsUnmodifiedLegacyHook(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if isKnownLegacyHookLine(line) {
			continue
		}
		return false
	}
	return true
}

// isKnownLegacyHookLine returns true for lines that appear in BD-generated
// legacy hook shims (thin shim format and inline hook format).
func isKnownLegacyHookLine(line string) bool {
	trimmed := strings.TrimSpace(line)

	// Empty lines and shebangs
	if trimmed == "" || strings.HasPrefix(trimmed, "#!") {
		return true
	}

	// Shell control flow and builtins used in hook templates
	switch trimmed {
	case "fi", "then", "else", "exit 0", "exit 1":
		return true
	}

	// Any comment line containing BD/beads identifiers
	if strings.HasPrefix(trimmed, "#") {
		lower := strings.ToLower(trimmed)
		for _, keyword := range []string{"bd", "beads", "hook", "shim"} {
			if strings.Contains(lower, keyword) {
				return true
			}
		}
		// Generic template comments (PATH, Install, Warning)
		for _, keyword := range []string{"PATH", "Install", "Warning"} {
			if strings.Contains(trimmed, keyword) {
				return true
			}
		}
		return false
	}

	// Known executable lines from legacy hook templates
	knownPrefixes := []string{
		"exec bd hook",
		"bd hooks run",
		"export BD_GIT_HOOK",
		"if ! command -v bd",
		"if command -v bd",
		"_bd_exit=$?",
	}
	for _, prefix := range knownPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}

	// echo lines with BD-related content (warnings about bd not being installed)
	if strings.HasPrefix(trimmed, "echo") {
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "bd") || strings.Contains(lower, "beads") {
			return true
		}
	}

	return false
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
