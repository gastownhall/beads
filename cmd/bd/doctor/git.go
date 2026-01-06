package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/syncbranch"
)

const (
	hooksExamplesURL = "https://github.com/steveyegge/beads/tree/main/examples/git-hooks"
	hooksUpgradeURL  = "https://github.com/steveyegge/beads/issues/615"
)

// CheckGitHooks verifies that recommended git hooks are installed.
func CheckGitHooks() DoctorCheck {
	// Check if we're in a git repository using worktree-aware detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Recommended hooks and their purposes
	recommendedHooks := map[string]string{
		"pre-commit": "Flushes pending bd changes to JSONL before commit",
		"post-merge": "Imports updated JSONL after git pull/merge",
		"pre-push":   "Exports database to JSONL before push",
	}

	hooksDir := filepath.Join(gitDir, "hooks")
	var missingHooks []string
	var installedHooks []string

	for hookName := range recommendedHooks {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			missingHooks = append(missingHooks, hookName)
		} else {
			installedHooks = append(installedHooks, hookName)
		}
	}

	// Get repo root for external manager detection
	repoRoot := filepath.Dir(gitDir)
	if filepath.Base(gitDir) != ".git" {
		// Worktree case - gitDir might be .git file content
		if cwd, err := os.Getwd(); err == nil {
			repoRoot = cwd
		}
	}

	// Check for external hook managers (lefthook, husky, etc.)
	externalManagers := fix.DetectExternalHookManagers(repoRoot)
	if len(externalManagers) > 0 {
		// External manager detected - check if it's configured to call bd
		integration := fix.CheckExternalHookManagerIntegration(repoRoot)
		if integration != nil {
			// Detection-only managers - we can't verify their config
			if integration.DetectionOnly {
				return DoctorCheck{
					Name:    "Git Hooks",
					Status:  StatusOK,
					Message: fmt.Sprintf("%s detected (cannot verify bd integration)", integration.Manager),
					Detail:  "Ensure your hook config calls 'bd hooks run <hook>'",
				}
			}

			if integration.Configured {
				// Check if any hooks are missing bd integration
				if len(integration.HooksWithoutBd) > 0 {
					return DoctorCheck{
						Name:    "Git Hooks",
						Status:  StatusWarning,
						Message: fmt.Sprintf("%s hooks not calling bd", integration.Manager),
						Detail:  fmt.Sprintf("Missing bd: %s", strings.Join(integration.HooksWithoutBd, ", ")),
						Fix:     "Add or upgrade to 'bd hooks run <hook>'. See " + hooksUpgradeURL,
					}
				}

				// All hooks calling bd - success
				return DoctorCheck{
					Name:    "Git Hooks",
					Status:  StatusOK,
					Message: fmt.Sprintf("All hooks via %s", integration.Manager),
					Detail:  fmt.Sprintf("bd hooks run: %s", strings.Join(integration.HooksWithBd, ", ")),
				}
			}

			// External manager exists but doesn't call bd at all
			return DoctorCheck{
				Name:    "Git Hooks",
				Status:  StatusWarning,
				Message: fmt.Sprintf("%s not calling bd", fix.ManagerNames(externalManagers)),
				Detail:  "Configure hooks to call bd commands",
				Fix:     "Add or upgrade to 'bd hooks run <hook>'. See " + hooksUpgradeURL,
			}
		}
	}

	if len(missingHooks) == 0 {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusOK,
			Message: "All recommended hooks installed",
			Detail:  fmt.Sprintf("Installed: %s", strings.Join(installedHooks, ", ")),
		}
	}

	hookInstallMsg := "Install hooks with 'bd hooks install'. See " + hooksExamplesURL

	if len(installedHooks) > 0 {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Missing %d recommended hook(s)", len(missingHooks)),
			Detail:  fmt.Sprintf("Missing: %s", strings.Join(missingHooks, ", ")),
			Fix:     hookInstallMsg,
		}
	}

	return DoctorCheck{
		Name:    "Git Hooks",
		Status:  StatusWarning,
		Message: "No recommended git hooks installed",
		Detail:  fmt.Sprintf("Recommended: %s", strings.Join([]string{"pre-commit", "post-merge", "pre-push"}, ", ")),
		Fix:     hookInstallMsg,
	}
}

// CheckGitWorkingTree checks if the git working tree is clean.
// This helps prevent leaving work stranded (AGENTS.md: keep git state clean).
func CheckGitWorkingTree(path string) DoctorCheck {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:    "Git Working Tree",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Working Tree",
			Status:  StatusWarning,
			Message: "Unable to check git status",
			Detail:  err.Error(),
			Fix:     "Run 'git status' and commit/stash changes before syncing",
		}
	}

	status := strings.TrimSpace(string(out))
	if status == "" {
		return DoctorCheck{
			Name:    "Git Working Tree",
			Status:  StatusOK,
			Message: "Clean",
		}
	}

	// Show a small sample of paths for quick debugging.
	lines := strings.Split(status, "\n")
	maxLines := 8
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "…")
	}

	return DoctorCheck{
		Name:    "Git Working Tree",
		Status:  StatusWarning,
		Message: "Uncommitted changes present",
		Detail:  strings.Join(lines, "\n"),
		Fix:     "Commit or stash changes, then follow AGENTS.md: git pull --rebase && git push",
	}
}

// CheckGitUpstream checks whether the current branch is up to date with its upstream.
// This catches common "forgot to pull/push" failure modes (AGENTS.md: pull --rebase, push).
func CheckGitUpstream(path string) DoctorCheck {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Detect detached HEAD.
	cmd = exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = path
	branchOut, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusWarning,
			Message: "Detached HEAD (no branch)",
			Fix:     "Check out a branch before syncing",
		}
	}
	branch := strings.TrimSpace(string(branchOut))

	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	cmd.Dir = path
	upOut, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusWarning,
			Message: fmt.Sprintf("No upstream configured for %s", branch),
			Fix:     fmt.Sprintf("Set upstream then push: git push -u origin %s", branch),
		}
	}
	upstream := strings.TrimSpace(string(upOut))

	ahead, aheadErr := gitRevListCount(path, "@{u}..HEAD")
	behind, behindErr := gitRevListCount(path, "HEAD..@{u}")
	if aheadErr != nil || behindErr != nil {
		detailParts := []string{}
		if aheadErr != nil {
			detailParts = append(detailParts, "ahead: "+aheadErr.Error())
		}
		if behindErr != nil {
			detailParts = append(detailParts, "behind: "+behindErr.Error())
		}
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Unable to compare with upstream (%s)", upstream),
			Detail:  strings.Join(detailParts, "; "),
			Fix:     "Run 'git fetch' then check: git status -sb",
		}
	}

	if ahead == 0 && behind == 0 {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusOK,
			Message: fmt.Sprintf("Up to date (%s)", upstream),
			Detail:  fmt.Sprintf("Branch: %s", branch),
		}
	}

	if ahead > 0 && behind == 0 {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Ahead of upstream by %d commit(s)", ahead),
			Detail:  fmt.Sprintf("Branch: %s, upstream: %s", branch, upstream),
			Fix:     "Run 'git push' (AGENTS.md: git pull --rebase && git push)",
		}
	}

	if behind > 0 && ahead == 0 {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Behind upstream by %d commit(s)", behind),
			Detail:  fmt.Sprintf("Branch: %s, upstream: %s", branch, upstream),
			Fix:     "Run 'git pull --rebase' (then re-run bd sync / bd doctor)",
		}
	}

	return DoctorCheck{
		Name:    "Git Upstream",
		Status:  StatusWarning,
		Message: fmt.Sprintf("Diverged from upstream (ahead %d, behind %d)", ahead, behind),
		Detail:  fmt.Sprintf("Branch: %s, upstream: %s", branch, upstream),
		Fix:     "Run 'git pull --rebase' then 'git push'",
	}
}

func gitRevListCount(path string, rangeExpr string) (int, error) {
	cmd := exec.Command("git", "rev-list", "--count", rangeExpr) // #nosec G204 -- fixed args
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	countStr := strings.TrimSpace(string(out))
	if countStr == "" {
		return 0, nil
	}

	var n int
	if _, err := fmt.Sscanf(countStr, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// CheckSyncBranchHookCompatibility checks if pre-push hook is compatible with sync-branch mode.
// When sync-branch is configured, the pre-push hook must have the sync-branch bypass logic
// (added in version 0.29.0). Without it, users experience circular "bd sync" failures (issue #532).
func CheckSyncBranchHookCompatibility(path string) DoctorCheck {
	// Check if sync-branch is configured
	syncBranch := syncbranch.GetFromYAML()
	if syncBranch == "" {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusOK,
			Message: "N/A (sync-branch not configured)",
		}
	}

	// sync-branch is configured - check pre-push hook version
	// Get actual git directory (handles worktrees where .git is a file)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}
	gitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}

	// Use standard .git/hooks location for consistency with CheckGitHooks (issue #799)
	// Note: core.hooksPath is intentionally NOT checked here to match CheckGitHooks behavior.
	hookPath := filepath.Join(gitDir, "hooks", "pre-push")

	hookContent, err := os.ReadFile(hookPath) // #nosec G304 - path is controlled
	if err != nil {
		// No pre-push hook installed - different issue, covered by checkGitHooks
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusOK,
			Message: "N/A (no pre-push hook installed)",
		}
	}

	// Check if this is a bd hook and extract version
	hookStr := string(hookContent)
	if !strings.Contains(hookStr, "bd-hooks-version:") {
		// Not a bd hook - check if it's an external hook manager
		externalManagers := fix.DetectExternalHookManagers(path)
		if len(externalManagers) > 0 {
			names := fix.ManagerNames(externalManagers)

			// Check if external manager has bd integration
			integration := fix.CheckExternalHookManagerIntegration(path)
			if integration != nil {
				// Detection-only managers - we can't verify their config
				if integration.DetectionOnly {
					return DoctorCheck{
						Name:    "Sync Branch Hook Compatibility",
						Status:  StatusOK,
						Message: fmt.Sprintf("Managed by %s (cannot verify bd integration)", names),
						Detail:  "Ensure pre-push hook calls 'bd hooks run pre-push' for sync-branch",
					}
				}

				if integration.Configured {
					// Has bd integration - check if pre-push is covered
					hasPrepush := false
					for _, h := range integration.HooksWithBd {
						if h == "pre-push" {
							hasPrepush = true
							break
						}
					}

					if hasPrepush {
						var detail string
						// Only report hooks that ARE in config but lack bd integration
						if len(integration.HooksWithoutBd) > 0 {
							detail = fmt.Sprintf("Hooks without bd: %s", strings.Join(integration.HooksWithoutBd, ", "))
						}
						return DoctorCheck{
							Name:    "Sync Branch Hook Compatibility",
							Status:  StatusOK,
							Message: fmt.Sprintf("Managed by %s with bd integration", integration.Manager),
							Detail:  detail,
						}
					}

					// Has bd integration but missing pre-push
					return DoctorCheck{
						Name:    "Sync Branch Hook Compatibility",
						Status:  StatusWarning,
						Message: fmt.Sprintf("Managed by %s (missing pre-push bd integration)", integration.Manager),
						Detail:  "pre-push hook needs 'bd hooks run pre-push' for sync-branch",
						Fix:     fmt.Sprintf("Add or upgrade to 'bd hooks run pre-push' in %s. See %s", integration.Manager, hooksExamplesURL),
					}
				}
			}

			// External manager detected but no bd integration found
			return DoctorCheck{
				Name:    "Sync Branch Hook Compatibility",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Managed by %s (no bd integration detected)", names),
				Detail:  fmt.Sprintf("Pre-push hook managed by %s but no 'bd hooks run' found", names),
				Fix:     fmt.Sprintf("Add or upgrade to 'bd hooks run <hook>' in %s. See %s", names, hooksExamplesURL),
			}
		}

		// No external manager - truly custom hook
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusWarning,
			Message: "Pre-push hook is not a bd hook",
			Detail:  "Cannot verify sync-branch compatibility with custom hooks",
			Fix: "Either run 'bd hooks install --force' to use bd hooks,\n" +
				"  or ensure your custom hook skips validation when pushing to sync-branch",
		}
	}

	// Extract version from hook
	var hookVersion string
	for _, line := range strings.Split(hookStr, "\n") {
		if strings.Contains(line, "bd-hooks-version:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				hookVersion = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	if hookVersion == "" {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusWarning,
			Message: "Could not determine pre-push hook version",
			Detail:  "Cannot verify sync-branch compatibility",
			Fix:     "Run 'bd hooks install --force' to update hooks",
		}
	}

	// MinSyncBranchHookVersion added sync-branch bypass logic
	// If hook version < MinSyncBranchHookVersion, it will cause circular "bd sync" failures
	if CompareVersions(hookVersion, MinSyncBranchHookVersion) < 0 {
		return DoctorCheck{
			Name:    "Sync Branch Hook Compatibility",
			Status:  StatusError,
			Message: fmt.Sprintf("Pre-push hook incompatible with sync-branch mode (version %s)", hookVersion),
			Detail:  fmt.Sprintf("Hook version %s lacks sync-branch bypass (requires %s+). This causes circular 'bd sync' failures during push.", hookVersion, MinSyncBranchHookVersion),
			Fix:     "Run 'bd hooks install --force' to update hooks",
		}
	}

	return DoctorCheck{
		Name:    "Sync Branch Hook Compatibility",
		Status:  StatusOK,
		Message: fmt.Sprintf("Pre-push hook compatible with sync-branch (version %s)", hookVersion),
	}
}

// CheckMergeDriver verifies that the git merge driver is correctly configured.
func CheckMergeDriver(path string) DoctorCheck {
	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Get current merge driver configuration
	cmd := exec.Command("git", "config", "merge.beads.driver")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		// Merge driver not configured
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusWarning,
			Message: "Git merge driver not configured",
			Fix:     "Run 'bd init' to configure the merge driver, or manually: git config merge.beads.driver \"bd merge %A %O %A %B\"",
		}
	}

	currentConfig := strings.TrimSpace(string(output))
	correctConfig := "bd merge %A %O %A %B"

	// Check if using old incorrect placeholders
	if strings.Contains(currentConfig, "%L") || strings.Contains(currentConfig, "%R") {
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusError,
			Message: fmt.Sprintf("Incorrect merge driver config: %q (uses invalid %%L/%%R placeholders)", currentConfig),
			Detail:  "Git only supports %O (base), %A (current), %B (other). Using %L/%R causes merge failures.",
			Fix:     "Run 'bd doctor --fix' to update to correct config, or manually: git config merge.beads.driver \"bd merge %A %O %A %B\"",
		}
	}

	// Check if config is correct
	if currentConfig != correctConfig {
		return DoctorCheck{
			Name:    "Git Merge Driver",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Non-standard merge driver config: %q", currentConfig),
			Detail:  fmt.Sprintf("Expected: %q", correctConfig),
			Fix:     fmt.Sprintf("Run 'bd doctor --fix' to update config, or manually: git config merge.beads.driver \"%s\"", correctConfig),
		}
	}

	return DoctorCheck{
		Name:    "Git Merge Driver",
		Status:  StatusOK,
		Message: "Correctly configured",
		Detail:  currentConfig,
	}
}

// CheckSyncBranchConfig checks if sync-branch is properly configured.
func CheckSyncBranchConfig(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Check sync-branch from config.yaml or environment variable
	// This is the source of truth for multi-clone setups
	syncBranch := syncbranch.GetFromYAML()

	// Get current branch
	currentBranch := ""
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = path
	if output, err := cmd.Output(); err == nil {
		currentBranch = strings.TrimSpace(string(output))
	}

	// CRITICAL: Check if we're on the sync branch - this is a misconfiguration
	// that will cause bd sync to fail trying to create a worktree for a branch
	// that's already checked out
	if syncBranch != "" && currentBranch == syncBranch {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusError,
			Message: fmt.Sprintf("On sync branch '%s'", syncBranch),
			Detail:  fmt.Sprintf("Currently on branch '%s' which is configured as the sync branch. bd sync cannot create a worktree for a branch that's already checked out.", syncBranch),
			Fix:     "Switch to your main working branch: git checkout main",
		}
	}

	if syncBranch != "" {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusOK,
			Message: fmt.Sprintf("Configured (%s)", syncBranch),
			Detail:  fmt.Sprintf("Current branch: %s, sync branch: %s", currentBranch, syncBranch),
		}
	}

	// Not configured - this is optional but recommended for multi-clone setups
	// Check if this looks like a multi-clone setup (has remote)
	hasRemote := false
	cmd = exec.Command("git", "remote")
	cmd.Dir = path
	if output, err := cmd.Output(); err == nil && len(strings.TrimSpace(string(output))) > 0 {
		hasRemote = true
	}

	if hasRemote {
		return DoctorCheck{
			Name:    "Sync Branch Config",
			Status:  StatusWarning,
			Message: "sync-branch not configured",
			Detail:  "Multi-clone setups should configure sync-branch for safe data synchronization",
			Fix:     "Run 'bd migrate sync beads-sync' to set up sync branch workflow",
		}
	}

	// No remote - probably a local-only repo, sync-branch not needed
	return DoctorCheck{
		Name:    "Sync Branch Config",
		Status:  StatusOK,
		Message: "N/A (no remote configured)",
	}
}

// CheckSyncBranchHealth detects when the sync branch has diverged from main
// or from the remote sync branch (after a force-push reset).
func CheckSyncBranchHealth(path string) DoctorCheck {
	// Skip if not in a git repo using worktree-aware detection
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Get configured sync branch
	syncBranch := syncbranch.GetFromYAML()
	if syncBranch == "" {
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: "N/A (no sync branch configured)",
		}
	}

	// Check if local sync branch exists
	cmd := exec.Command("git", "rev-parse", "--verify", syncBranch) // #nosec G204 - syncBranch from config file
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Local branch doesn't exist - that's fine, bd sync will create it
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: fmt.Sprintf("N/A (local %s branch not created yet)", syncBranch),
		}
	}

	// Check if remote sync branch exists
	remote := "origin"
	remoteBranch := fmt.Sprintf("%s/%s", remote, syncBranch)
	cmd = exec.Command("git", "rev-parse", "--verify", remoteBranch) // #nosec G204 - remoteBranch from config
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Remote branch doesn't exist - that's fine
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: fmt.Sprintf("N/A (remote %s not found)", remoteBranch),
		}
	}

	// Check 1: Is local sync branch diverged from remote? (after force-push)
	// If they have no common ancestor in recent history, the remote was likely force-pushed
	cmd = exec.Command("git", "merge-base", syncBranch, remoteBranch) // #nosec G204 - branches from config
	cmd.Dir = path
	mergeBaseOutput, err := cmd.Output()
	if err != nil {
		// No common ancestor - branches have completely diverged
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Local %s diverged from remote", syncBranch),
			Detail:  "The remote sync branch was likely reset/force-pushed. Your local branch has orphaned history.",
			Fix:     "Run 'bd doctor --fix' to reset sync branch",
		}
	}

	// Check if local is behind remote (needs to fast-forward)
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))
	cmd = exec.Command("git", "rev-parse", syncBranch) // #nosec G204 - syncBranch from config
	cmd.Dir = path
	localHead, _ := cmd.Output()
	localHeadStr := strings.TrimSpace(string(localHead))

	cmd = exec.Command("git", "rev-parse", remoteBranch) // #nosec G204 - remoteBranch from config
	cmd.Dir = path
	remoteHead, _ := cmd.Output()
	remoteHeadStr := strings.TrimSpace(string(remoteHead))

	// If merge base equals local but not remote, local is behind
	if mergeBase == localHeadStr && mergeBase != remoteHeadStr {
		// Count how far behind
		cmd = exec.Command("git", "rev-list", "--count", fmt.Sprintf("%s..%s", syncBranch, remoteBranch)) // #nosec G204 - branches from config
		cmd.Dir = path
		countOutput, _ := cmd.Output()
		behindCount := strings.TrimSpace(string(countOutput))

		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: fmt.Sprintf("Local %s is %s commits behind remote (will sync)", syncBranch, behindCount),
		}
	}

	// Check 2: Is sync branch far behind main on source files?
	// Get the main branch name
	mainBranch := "main"
	cmd = exec.Command("git", "rev-parse", "--verify", "main")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// Try "master" as fallback
		cmd = exec.Command("git", "rev-parse", "--verify", "master")
		cmd.Dir = path
		if err := cmd.Run(); err != nil {
			// Can't determine main branch
			return DoctorCheck{
				Name:    "Sync Branch Health",
				Status:  StatusOK,
				Message: "OK",
			}
		}
		mainBranch = "master"
	}

	// Count commits main is ahead of sync branch
	cmd = exec.Command("git", "rev-list", "--count", fmt.Sprintf("%s..%s", syncBranch, mainBranch)) // #nosec G204 - branches from config/hardcoded
	cmd.Dir = path
	aheadOutput, err := cmd.Output()
	if err != nil {
		return DoctorCheck{
			Name:    "Sync Branch Health",
			Status:  StatusOK,
			Message: "OK",
		}
	}
	aheadCount := strings.TrimSpace(string(aheadOutput))

	// Check if there are non-.beads/ file differences (stale source code)
	cmd = exec.Command("git", "diff", "--name-only", fmt.Sprintf("%s..%s", syncBranch, mainBranch), "--", ":(exclude).beads/") // #nosec G204 - branches from config/hardcoded
	cmd.Dir = path
	diffOutput, _ := cmd.Output()
	diffFiles := strings.TrimSpace(string(diffOutput))

	if diffFiles != "" && aheadCount != "0" {
		// Count the number of different files
		fileCount := len(strings.Split(diffFiles, "\n"))
		// Parse ahead count as int for comparison
		aheadCountInt := 0
		_, _ = fmt.Sscanf(aheadCount, "%d", &aheadCountInt)

		// Only warn if significantly behind (20+ commits AND 50+ source files)
		// Small drift is normal between bd sync operations
		if fileCount > 50 && aheadCountInt > 20 {
			return DoctorCheck{
				Name:    "Sync Branch Health",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Sync branch %s commits behind %s on source files", aheadCount, mainBranch),
				Detail:  fmt.Sprintf("%d source files differ between %s and %s. The sync branch has stale code.", fileCount, syncBranch, mainBranch),
				Fix:     "Run 'bd doctor --fix' to reset sync branch to main",
			}
		}
	}

	return DoctorCheck{
		Name:    "Sync Branch Health",
		Status:  StatusOK,
		Message: "OK",
	}
}

// FixGitHooks fixes missing or broken git hooks by calling bd hooks install.
func FixGitHooks(path string) error {
	return fix.GitHooks(path)
}

// FixMergeDriver fixes the git merge driver configuration to use correct placeholders.
func FixMergeDriver(path string) error {
	return fix.MergeDriver(path)
}

// FixSyncBranchHealth fixes database-JSONL sync issues.
func FixSyncBranchHealth(path string) error {
	return fix.DBJSONLSync(path)
}

// FindOrphanedIssues identifies issues referenced in git commits but still open in the database.
// This is the shared core logic used by both 'bd orphans' and 'bd doctor' commands.
// Returns empty slice if not a git repo, no database, or no orphans found (no error).
func FindOrphanedIssues(path string) ([]OrphanIssue, error) {
	// Skip if not in a git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return []OrphanIssue{}, nil // Not a git repo, return empty list
	}

	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Skip if no .beads directory
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return []OrphanIssue{}, nil
	}

	// Get database path
	dbPath := filepath.Join(beadsDir, "beads.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return []OrphanIssue{}, nil
	}

	// Open database read-only
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return []OrphanIssue{}, nil
	}
	defer db.Close()

	// Get issue prefix from config
	var issuePrefix string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'issue_prefix'").Scan(&issuePrefix)
	if err != nil || issuePrefix == "" {
		issuePrefix = "bd" // default
	}

	// Get all open/in_progress issues with their titles (title is optional for compatibility)
	var rows *sql.Rows
	rows, err = db.Query("SELECT id, title, status FROM issues WHERE status IN ('open', 'in_progress')")
	// If the query fails (e.g., no title column), fall back to simpler query
	if err != nil {
		rows, err = db.Query("SELECT id, '', status FROM issues WHERE status IN ('open', 'in_progress')")
		if err != nil {
			return []OrphanIssue{}, nil
		}
	}
	defer rows.Close()

	openIssues := make(map[string]*OrphanIssue)
	for rows.Next() {
		var id, title, status string
		if err := rows.Scan(&id, &title, &status); err == nil {
			openIssues[id] = &OrphanIssue{
				IssueID: id,
				Title:   title,
				Status:  status,
			}
		}
	}

	if len(openIssues) == 0 {
		return []OrphanIssue{}, nil
	}

	// Get git log
	cmd = exec.Command("git", "log", "--oneline", "--all")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return []OrphanIssue{}, nil
	}

	// Parse commits for issue references
	// Match pattern like (bd-xxx) or (bd-xxx.1) including hierarchical IDs
	pattern := fmt.Sprintf(`\(%s-[a-z0-9.]+\)`, regexp.QuoteMeta(issuePrefix))
	re := regexp.MustCompile(pattern)

	var orphanedIssues []OrphanIssue
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		// Extract commit hash and message
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 1 {
			continue
		}

		commitHash := parts[0]
		commitMsg := ""
		if len(parts) > 1 {
			commitMsg = parts[1]
		}

		// Find issue IDs in this commit
		matches := re.FindAllString(line, -1)
		for _, match := range matches {
			issueID := strings.Trim(match, "()")
			if orphan, exists := openIssues[issueID]; exists {
				// Only record first (most recent) commit per issue
				if orphan.LatestCommit == "" {
					orphan.LatestCommit = commitHash
					orphan.LatestCommitMessage = commitMsg
				}
			}
		}
	}

	// Collect issues with commit references
	for _, orphan := range openIssues {
		if orphan.LatestCommit != "" {
			orphanedIssues = append(orphanedIssues, *orphan)
		}
	}

	return orphanedIssues, nil
}

// CheckOrphanedIssues detects issues referenced in git commits but still open.
// This catches cases where someone implemented a fix with "(bd-xxx)" in the commit
// message but forgot to run "bd close".
func CheckOrphanedIssues(path string) DoctorCheck {
	// Skip if not in a git repo (check from path directory)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (not a git repository)",
			Category: CategoryGit,
		}
	}

	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Skip if no .beads directory
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (no .beads directory)",
			Category: CategoryGit,
		}
	}

	// Get database path from config or use canonical name
	dbPath := filepath.Join(beadsDir, "beads.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryGit,
		}
	}

	// Use the shared FindOrphanedIssues function
	orphans, err := FindOrphanedIssues(path)
	if err != nil {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "N/A (unable to check orphaned issues)",
			Category: CategoryGit,
		}
	}

	// Check for "no open issues" case - this requires checking the database
	// since FindOrphanedIssues silently returns empty slice
	db, err := openDBReadOnly(dbPath)
	if err == nil {
		defer db.Close()
		rows, err := db.Query("SELECT COUNT(*) FROM issues WHERE status IN ('open', 'in_progress')")
		if err == nil {
			defer rows.Close()
			if rows.Next() {
				var count int
				if err := rows.Scan(&count); err == nil && count == 0 {
					return DoctorCheck{
						Name:     "Orphaned Issues",
						Status:   StatusOK,
						Message:  "No open issues to check",
						Category: CategoryGit,
					}
				}
			}
		}
	}

	if len(orphans) == 0 {
		return DoctorCheck{
			Name:     "Orphaned Issues",
			Status:   StatusOK,
			Message:  "No issues referenced in commits but still open",
			Category: CategoryGit,
		}
	}

	// Build detail message
	var details []string
	for _, orphan := range orphans {
		details = append(details, fmt.Sprintf("%s (commit %s)", orphan.IssueID, orphan.LatestCommit))
	}

	return DoctorCheck{
		Name:     "Orphaned Issues",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d issue(s) referenced in commits but still open", len(orphans)),
		Detail:   strings.Join(details, ", "),
		Fix:      "Run 'bd show <id>' to check if implemented, then 'bd close <id>' if done",
		Category: CategoryGit,
	}
}

// openDBReadOnly opens a SQLite database in read-only mode
func openDBReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite3", sqliteConnString(dbPath, true))
}
