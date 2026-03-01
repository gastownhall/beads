package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/types"
)

// gitCmdTimeout is the timeout for git subprocess commands in doctor checks.
// Prevents doctor checks from blocking indefinitely if git hangs.
const gitCmdTimeout = 30 * time.Second

const (
	hooksExamplesURL = "https://github.com/steveyegge/beads/tree/main/examples/git-hooks"
	hooksUpgradeURL  = "https://github.com/steveyegge/beads/issues/615"
)

// bdShimMarker identifies bd shim hooks (GH#946)
const bdShimMarker = "# bd-shim"

// bdInlineHookMarker identifies inline hooks created by bd init (GH#1120)
// These hooks have the logic embedded directly rather than calling bd hooks run
const bdInlineHookMarker = "# bd (beads)"

// bdHooksRunPattern matches hooks that call bd hooks run
var bdHooksRunPattern = regexp.MustCompile(`\bbd\s+hooks\s+run\b`)

// CheckGitHooks verifies that recommended git hooks are installed.
func CheckGitHooks(cliVersion string) DoctorCheck {
	// Check if we're in a git repository using worktree-aware detection
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Hooks",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Recommended hooks and their purposes
	recommendedHooks := map[string]string{
		"pre-commit": "Syncs pending bd changes before commit",
		"post-merge": "Syncs database after git pull/merge",
		"pre-push":   "Validates database state before push",
	}
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
	repoRoot := git.GetRepoRoot()

	// Check for external hook managers (lefthook, husky, etc.)
	externalManagers := fix.DetectExternalHookManagers(repoRoot)
	if len(externalManagers) > 0 {
		// First, check if bd shims are installed (GH#946)
		// If the actual hooks are bd shims, they're calling bd regardless of what
		// the external manager config says (user may have leftover config files)
		if hasBdShims, bdHooks := areBdShimsInstalled(hooksDir); hasBdShims {
			if outdated, oldest := findOutdatedBDHookVersions(hooksDir, bdHooks, cliVersion); len(outdated) > 0 {
				return DoctorCheck{
					Name:    "Git Hooks",
					Status:  StatusWarning,
					Message: "Installed bd hooks are outdated",
					Detail: fmt.Sprintf(
						"Outdated: %s (oldest: %s, current: %s)",
						strings.Join(outdated, ", "),
						oldest,
						cliVersion,
					),
					Fix: "Run 'bd hooks install --force' to update hooks",
				}
			}
			return DoctorCheck{
				Name:    "Git Hooks",
				Status:  StatusOK,
				Message: "bd shims installed (ignoring external manager config)",
				Detail:  fmt.Sprintf("bd hooks run: %s", strings.Join(bdHooks, ", ")),
			}
		}

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
		if outdated, oldest := findOutdatedBDHookVersions(hooksDir, installedHooks, cliVersion); len(outdated) > 0 {
			return DoctorCheck{
				Name:    "Git Hooks",
				Status:  StatusWarning,
				Message: "Installed bd hooks are outdated",
				Detail: fmt.Sprintf(
					"Outdated: %s (oldest: %s, current: %s)",
					strings.Join(outdated, ", "),
					oldest,
					cliVersion,
				),
				Fix: "Run 'bd hooks install --force' to update hooks",
			}
		}
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

func findOutdatedBDHookVersions(
	hooksDir string,
	hookNames []string,
	cliVersion string,
) ([]string, string) {
	if !IsValidSemver(cliVersion) {
		return nil, ""
	}
	var outdated []string
	var oldest string
	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		content, err := os.ReadFile(hookPath)
		if err != nil {
			continue
		}
		contentStr := string(content)
		hookVersion, ok := parseBDHookVersion(contentStr)
		if !ok || !IsValidSemver(hookVersion) {
			// No version comment found. If this is a bd hook (has shim marker,
			// inline marker, or calls bd hooks run), treat it as outdated since
			// all current hook templates include a version comment. (GH#1466)
			if isBdHookContent(contentStr) {
				outdated = append(outdated, fmt.Sprintf("%s@unknown", hookName))
				if oldest == "" {
					oldest = "0.0.0"
				}
			}
			continue
		}
		if CompareVersions(hookVersion, cliVersion) < 0 {
			outdated = append(outdated, fmt.Sprintf("%s@%s", hookName, hookVersion))
			if oldest == "" || CompareVersions(hookVersion, oldest) < 0 {
				oldest = hookVersion
			}
		}
	}
	return outdated, oldest
}

// isBdHookContent checks if hook content is a bd hook (shim, inline, or calls bd hooks run).
func isBdHookContent(content string) bool {
	return strings.Contains(content, bdShimMarker) ||
		strings.Contains(content, bdInlineHookMarker) ||
		bdHooksRunPattern.MatchString(content)
}

func parseBDHookVersion(content string) (string, bool) {
	if !strings.Contains(content, "bd-hooks-version:") {
		return "", false
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "bd-hooks-version:") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return "", false
		}
		version := strings.TrimSpace(parts[1])
		if version == "" {
			return "", false
		}
		return version, true
	}
	return "", false
}

// areBdShimsInstalled checks if the installed hooks are bd shims, call bd hooks run,
// or are inline bd hooks created by bd init.
// This helps detect when bd hooks are installed directly but an external manager config exists.
// Returns (true, installedHooks) if bd hooks are detected, (false, nil) otherwise.
// (GH#946, GH#1120)
func areBdShimsInstalled(hooksDir string) (bool, []string) {
	hooks := []string{"pre-commit", "post-merge", "pre-push"}
	var bdHooks []string

	for _, hookName := range hooks {
		hookPath := filepath.Join(hooksDir, hookName)
		content, err := os.ReadFile(hookPath)
		if err != nil {
			continue
		}
		contentStr := string(content)
		// Check for bd-shim marker, bd hooks run call, or inline bd hook marker (from bd init)
		if strings.Contains(contentStr, bdShimMarker) ||
			strings.Contains(contentStr, bdInlineHookMarker) ||
			bdHooksRunPattern.MatchString(contentStr) {
			bdHooks = append(bdHooks, hookName)
		}
	}

	return len(bdHooks) > 0, bdHooks
}

// CheckGitWorkingTree checks if the git working tree is clean.
// This helps prevent leaving work stranded (AGENTS.md: keep git state clean).
func CheckGitWorkingTree(path string) DoctorCheck {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:    "Git Working Tree",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	cmd = exec.CommandContext(ctx, "git", "status", "--porcelain")
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

	// Parse raw porcelain lines preserving leading spaces for correct XY parsing.
	// strings.TrimSpace above strips the leading space from the first " D ..."
	// line, corrupting porcelain format. Use the raw output for line parsing.
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")

	// In redirect worktrees (.beads/redirect exists), deleted .beads/ files
	// are expected — the actual data lives at the redirect target (the rig).
	// Filter these out so they don't trigger a false warning.
	redirectPath := filepath.Join(path, ".beads", "redirect")
	if _, err := os.Stat(redirectPath); err == nil {
		var filtered []string
		for _, line := range lines {
			if isExpectedRedirectChange(line) {
				continue
			}
			filtered = append(filtered, line)
		}
		if len(filtered) == 0 {
			return DoctorCheck{
				Name:    "Git Working Tree",
				Status:  StatusOK,
				Message: "Clean (redirect worktree, .beads/ deletions expected)",
			}
		}
		lines = filtered
	}

	// Show a small sample of paths for quick debugging.
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

// isExpectedRedirectChange returns true if a git status --porcelain line
// represents an expected change in a redirect worktree: deleted .beads/ files
// or the untracked .beads/redirect file itself.
// Porcelain format: XY PATH where X=index status, Y=worktree status.
// Deletions show as " D .beads/..." (unstaged) or "D  .beads/..." (staged).
func isExpectedRedirectChange(line string) bool {
	if len(line) < 4 {
		return false
	}
	xy := line[:2]
	filePath := line[3:]
	if !strings.HasPrefix(filePath, ".beads/") {
		return false
	}
	// Deleted .beads/ files (expected: data lives at redirect target)
	if xy == " D" || xy == "D " || xy == "DD" {
		return true
	}
	// Untracked .beads/redirect file (expected: the redirect marker itself)
	if xy == "??" && filePath == ".beads/redirect" {
		return true
	}
	return false
}

// CheckGitUpstream checks whether the current branch is up to date with its upstream.
// This catches common "forgot to pull/push" failure modes (AGENTS.md: pull --rebase, push).
func CheckGitUpstream(path string) DoctorCheck {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Detect detached HEAD.
	cmd = exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
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

	// Check if any remotes exist — no point warning about upstream if there's no remote
	remoteCmd := exec.CommandContext(ctx, "git", "remote")
	remoteCmd.Dir = path
	remoteOut, err := remoteCmd.Output()
	if err != nil || strings.TrimSpace(string(remoteOut)) == "" {
		return DoctorCheck{
			Name:    "Git Upstream",
			Status:  StatusOK,
			Message: "N/A — no remotes configured",
		}
	}

	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
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

	ahead, aheadErr := gitRevListCount(ctx, path, "@{u}..HEAD")
	behind, behindErr := gitRevListCount(ctx, path, "HEAD..@{u}")
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
			Fix:     "Run 'git pull --rebase' (then re-run bd doctor)",
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

func gitRevListCount(ctx context.Context, path string, rangeExpr string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", rangeExpr) // #nosec G204 -- fixed args
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

// FixMergeDriver sets the git merge driver configuration to the correct value.
func FixMergeDriver() error {
	correctConfig := "bd merge %A %O %A %B"
	cmd := exec.Command("git", "config", "merge.beads.driver", correctConfig) // #nosec G204
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set merge.beads.driver: %w", err)
	}
	// Also ensure merge.beads.name is set
	nameCmd := exec.Command("git", "config", "merge.beads.name", "Beads merge driver") // #nosec G204
	if err := nameCmd.Run(); err != nil {
		return fmt.Errorf("failed to set merge.beads.name: %w", err)
	}
	return nil
}

// CheckGitHooksDoltCompatibility checks if installed git hooks are compatible with Dolt backend.
// Hooks installed before Dolt support was added don't have the backend check and will
// fail with confusing errors on git pull/commit.
func CheckGitHooksDoltCompatibility(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Only relevant for Dolt backend
	if backend != configfile.BackendDolt {
		return DoctorCheck{
			Name:    "Git Hooks Dolt Compatibility",
			Status:  StatusOK,
			Message: "N/A (not using Dolt backend)",
		}
	}

	// Check if we're in a git repository
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return DoctorCheck{
			Name:    "Git Hooks Dolt Compatibility",
			Status:  StatusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Check post-merge hook (most likely to cause issues with Dolt)
	postMergePath := filepath.Join(hooksDir, "post-merge")
	content, err := os.ReadFile(postMergePath)
	if err != nil {
		// No hook installed - that's fine
		return DoctorCheck{
			Name:    "Git Hooks Dolt Compatibility",
			Status:  StatusOK,
			Message: "N/A (no post-merge hook installed)",
		}
	}

	contentStr := string(content)

	// Shim hooks (bd-shim) delegate to 'bd hook' which handles Dolt correctly
	if strings.Contains(contentStr, bdShimMarker) {
		return DoctorCheck{
			Name:    "Git Hooks Dolt Compatibility",
			Status:  StatusOK,
			Message: "Shim hooks (Dolt handled by bd hook command)",
		}
	}

	// Check if it's a bd inline hook
	if !strings.Contains(contentStr, bdInlineHookMarker) && !strings.Contains(contentStr, "bd") {
		return DoctorCheck{
			Name:    "Git Hooks Dolt Compatibility",
			Status:  StatusOK,
			Message: "N/A (not a bd hook)",
		}
	}

	// Check if inline hook has the Dolt backend skip logic
	if strings.Contains(contentStr, `"backend"`) && strings.Contains(contentStr, `"dolt"`) {
		return DoctorCheck{
			Name:    "Git Hooks Dolt Compatibility",
			Status:  StatusOK,
			Message: "Inline hooks have Dolt backend check",
		}
	}

	// Hook exists but lacks Dolt check - this will cause errors
	_ = beadsDir // silence unused warning
	return DoctorCheck{
		Name:    "Git Hooks Dolt Compatibility",
		Status:  StatusError,
		Message: "Git hooks incompatible with Dolt backend",
		Detail:  "Installed hooks are outdated and incompatible with the Dolt backend.",
		Fix:     "Run 'bd hooks install --force' to update hooks for Dolt compatibility",
	}
}

// fixGitHooks fixes missing or broken git hooks by calling bd hooks install.
func fixGitHooks(path string) error {
	return fix.GitHooks(path)
}

// FindOrphanedIssues identifies issues referenced in git commits but still open in the database.
// This is the shared core logic used by both 'bd orphans' and 'bd doctor' commands.
// Returns empty slice if not a git repo, no issues from provider, or no orphans found (no error).
//
// Parameters:
//   - gitPath: The directory to scan for git commits
//   - provider: The issue provider to get open issues and prefix from
func FindOrphanedIssues(gitPath string, provider types.IssueProvider) ([]OrphanIssue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	// Skip if not in a git repo
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = gitPath
	if err := cmd.Run(); err != nil {
		return []OrphanIssue{}, nil // Not a git repo, return empty list
	}

	// Get issue prefix from provider
	issuePrefix := provider.GetIssuePrefix()

	// Get all open/in_progress issues from provider
	issues, err := provider.GetOpenIssues(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting open issues: %w", err)
	}

	openIssues := make(map[string]*OrphanIssue)
	for _, issue := range issues {
		openIssues[issue.ID] = &OrphanIssue{
			IssueID: issue.ID,
			Title:   issue.Title,
			Status:  string(issue.Status),
		}
	}

	if len(openIssues) == 0 {
		return []OrphanIssue{}, nil
	}

	// Get git log
	cmd = exec.CommandContext(ctx, "git", "log", "--oneline", "--all")
	cmd.Dir = gitPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading git log: %w", err)
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
	// Orphaned issue detection requires a local database provider which was removed
	// during the Dolt-only migration. This check is disabled until reimplemented
	// against the Dolt store.
	return DoctorCheck{
		Name:     "Orphaned Issues",
		Status:   StatusOK,
		Message:  "N/A (not yet implemented for Dolt backend)",
		Category: CategoryGit,
	}
}
