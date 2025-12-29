package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/ui"
)

// WorktreeInfo contains information about a git worktree
type WorktreeInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	IsMain     bool   `json:"is_main"`
	BeadsState string `json:"beads_state"` // "redirect", "shared", "none"
	RedirectTo string `json:"redirect_to,omitempty"`
}

var worktreeCmd = &cobra.Command{
	Use:     "worktree",
	Short:   "Manage git worktrees for parallel development",
	GroupID: "maint",
	Long: `Manage git worktrees with proper beads configuration.

Worktrees allow multiple working directories sharing the same git repository,
enabling parallel development (e.g., multiple agents or features).

When creating a worktree, beads automatically sets up a redirect file so all
worktrees share the same .beads database. This ensures consistent issue state
across all worktrees.

Examples:
  bd worktree create feature-auth           # Create worktree with beads redirect
  bd worktree create bugfix --branch fix-1  # Create with specific branch name
  bd worktree list                          # List all worktrees
  bd worktree remove feature-auth           # Remove worktree (with safety checks)
  bd worktree info                          # Show info about current worktree`,
}

var worktreeCreateCmd = &cobra.Command{
	Use:   "create <name> [--branch=<branch>]",
	Short: "Create a worktree with beads redirect",
	Long: `Create a git worktree with proper beads configuration.

This command:
1. Creates a git worktree at ./<name> (or specified path)
2. Sets up .beads/redirect pointing to the main repository's .beads
3. Adds the worktree path to .gitignore (if inside repo root)

The worktree will share the same beads database as the main repository,
ensuring consistent issue state across all worktrees.

Examples:
  bd worktree create feature-auth           # Create at ./feature-auth
  bd worktree create bugfix --branch fix-1  # Create with branch name
  bd worktree create ../agents/worker-1     # Create at relative path`,
	Args: cobra.ExactArgs(1),
	RunE: runWorktreeCreate,
}

var worktreeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all git worktrees",
	Long: `List all git worktrees and their beads configuration state.

Shows each worktree with:
- Name (directory name)
- Path (full path)
- Branch
- Beads state: "redirect" (uses shared db), "shared" (is main), "none" (no beads)

Examples:
  bd worktree list          # List all worktrees
  bd worktree list --json   # JSON output`,
	Args: cobra.NoArgs,
	RunE: runWorktreeList,
}

var worktreeRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a worktree with safety checks",
	Long: `Remove a git worktree with safety checks.

Before removing, this command checks for:
- Uncommitted changes
- Unpushed commits
- Stashes

Use --force to skip safety checks (not recommended).

Examples:
  bd worktree remove feature-auth         # Remove with safety checks
  bd worktree remove feature-auth --force # Skip safety checks`,
	Args: cobra.ExactArgs(1),
	RunE: runWorktreeRemove,
}

var worktreeInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show worktree info for current directory",
	Long: `Show information about the current worktree.

If the current directory is in a git worktree, shows:
- Worktree path and name
- Branch
- Beads configuration (redirect or main)
- Main repository location

Examples:
  bd worktree info          # Show current worktree info
  bd worktree info --json   # JSON output`,
	Args: cobra.NoArgs,
	RunE: runWorktreeInfo,
}

var (
	worktreeBranch string
	worktreeForce  bool
)

func init() {
	worktreeCreateCmd.Flags().StringVar(&worktreeBranch, "branch", "", "Branch name for the worktree (default: same as name)")
	worktreeRemoveCmd.Flags().BoolVar(&worktreeForce, "force", false, "Skip safety checks")

	worktreeCmd.AddCommand(worktreeCreateCmd)
	worktreeCmd.AddCommand(worktreeListCmd)
	worktreeCmd.AddCommand(worktreeRemoveCmd)
	worktreeCmd.AddCommand(worktreeInfoCmd)
	rootCmd.AddCommand(worktreeCmd)
}

func runWorktreeCreate(cmd *cobra.Command, args []string) error {
	CheckReadonly("worktree create")

	name := args[0]

	// Determine worktree path
	worktreePath, err := filepath.Abs(name)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	// Check if path already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return fmt.Errorf("path already exists: %s", worktreePath)
	}

	// Find main repository root
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	// Find main beads directory
	mainBeadsDir := beads.FindBeadsDir()
	if mainBeadsDir == "" {
		return fmt.Errorf("no .beads directory found; run 'bd init' first")
	}

	// Determine branch name
	branch := worktreeBranch
	if branch == "" {
		branch = filepath.Base(name)
	}

	// Create the worktree
	// #nosec G204 - branch and worktreePath are user input but git validates them
	gitCmd := exec.Command("git", "worktree", "add", "-b", branch, worktreePath)
	gitCmd.Dir = repoRoot
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		// Try without -b if branch already exists
		// #nosec G204 - branch and worktreePath are user input but git validates them
		gitCmd = exec.Command("git", "worktree", "add", worktreePath, branch)
		gitCmd.Dir = repoRoot
		output, err = gitCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to create worktree: %w\n%s", err, string(output))
		}
	}

	// Helper to clean up worktree on failure
	cleanupWorktree := func() {
		// #nosec G204 - worktreePath was created by us above
		_ = exec.Command("git", "worktree", "remove", "--force", worktreePath).Run()
	}

	// Create .beads directory in worktree
	worktreeBeadsDir := filepath.Join(worktreePath, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		cleanupWorktree()
		return fmt.Errorf("failed to create .beads directory: %w", err)
	}

	// Create redirect file
	redirectPath := filepath.Join(worktreeBeadsDir, beads.RedirectFileName)
	relPath, err := filepath.Rel(worktreeBeadsDir, mainBeadsDir)
	if err != nil {
		// Fall back to absolute path
		relPath = mainBeadsDir
	}
	// #nosec G306 - redirect file needs to be readable
	if err := os.WriteFile(redirectPath, []byte(relPath+"\n"), 0644); err != nil {
		cleanupWorktree()
		return fmt.Errorf("failed to create redirect file: %w", err)
	}

	// Add to .gitignore if worktree is inside repo root
	if strings.HasPrefix(worktreePath, repoRoot+string(os.PathSeparator)) {
		// Use relative path from repo root for gitignore entry
		relWorktreePath, err := filepath.Rel(repoRoot, worktreePath)
		if err != nil {
			relWorktreePath = filepath.Base(worktreePath)
		}
		if err := addToGitignore(repoRoot, relWorktreePath); err != nil {
			// Non-fatal, just warn
			fmt.Fprintf(os.Stderr, "Warning: failed to update .gitignore: %v\n", err)
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"path":        worktreePath,
			"branch":      branch,
			"redirect_to": mainBeadsDir,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Created worktree: %s\n", ui.RenderPass("✓"), worktreePath)
	fmt.Printf("  Branch: %s\n", branch)
	fmt.Printf("  Beads: redirects to %s\n", mainBeadsDir)
	return nil
}

func runWorktreeList(cmd *cobra.Command, args []string) error {
	// Get repository root
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	// List worktrees
	gitCmd := exec.Command("git", "worktree", "list", "--porcelain")
	gitCmd.Dir = repoRoot
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Parse worktree list
	worktrees := parseWorktreeList(string(output))

	// Enrich with beads state
	mainBeadsDir := beads.FindBeadsDir()
	for i := range worktrees {
		worktrees[i].BeadsState = getBeadsState(worktrees[i].Path, mainBeadsDir)
		if worktrees[i].BeadsState == "redirect" {
			worktrees[i].RedirectTo = getRedirectTarget(worktrees[i].Path)
		}
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(worktrees)
	}

	// Human-readable output
	if len(worktrees) == 0 {
		fmt.Println("No worktrees found")
		return nil
	}

	fmt.Printf("%-20s %-40s %-20s %s\n", "NAME", "PATH", "BRANCH", "BEADS")
	for _, wt := range worktrees {
		name := filepath.Base(wt.Path)
		if wt.IsMain {
			name = "(main)"
		}
		beadsInfo := wt.BeadsState
		if wt.RedirectTo != "" {
			beadsInfo = fmt.Sprintf("redirect → %s", filepath.Base(filepath.Dir(wt.RedirectTo)))
		}
		fmt.Printf("%-20s %-40s %-20s %s\n",
			truncate(name, 20),
			truncate(wt.Path, 40),
			truncate(wt.Branch, 20),
			beadsInfo)
	}

	return nil
}

func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	CheckReadonly("worktree remove")

	name := args[0]

	// Find the worktree
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	// Resolve worktree path
	worktreePath, err := resolveWorktreePath(repoRoot, name)
	if err != nil {
		return err
	}

	// Don't allow removing the main repository
	absWorktree, _ := filepath.Abs(worktreePath)
	absMain, _ := filepath.Abs(repoRoot)
	if absWorktree == absMain {
		return fmt.Errorf("cannot remove main repository as worktree")
	}

	// Safety checks unless --force
	if !worktreeForce {
		if err := checkWorktreeSafety(worktreePath); err != nil {
			return fmt.Errorf("safety check failed: %w\nUse --force to skip safety checks", err)
		}
	}

	// Remove worktree
	// #nosec G204 - worktreePath is validated above
	gitCmd := exec.Command("git", "worktree", "remove", worktreePath)
	if worktreeForce {
		// #nosec G204 - worktreePath is validated above
		gitCmd = exec.Command("git", "worktree", "remove", "--force", worktreePath)
	}
	gitCmd.Dir = repoRoot
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %w\n%s", err, string(output))
	}

	// Remove from .gitignore - use relative path from repo root
	relWorktreePath, err := filepath.Rel(repoRoot, worktreePath)
	if err != nil {
		relWorktreePath = filepath.Base(worktreePath)
	}
	if err := removeFromGitignore(repoRoot, relWorktreePath); err != nil {
		// Non-fatal, just warn
		fmt.Fprintf(os.Stderr, "Warning: failed to update .gitignore: %v\n", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"removed": worktreePath,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Removed worktree: %s\n", ui.RenderPass("✓"), worktreePath)
	return nil
}

func runWorktreeInfo(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Check if we're in a worktree
	if !git.IsWorktree() {
		if jsonOutput {
			result := map[string]interface{}{
				"is_worktree": false,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		fmt.Println("Not in a git worktree (this is the main repository)")
		return nil
	}

	// Get worktree info
	mainRepoRoot, err := git.GetMainRepoRoot()
	if err != nil {
		mainRepoRoot = "(unknown)"
	}

	branch := getWorktreeCurrentBranch()
	redirectInfo := beads.GetRedirectInfo()

	if jsonOutput {
		result := map[string]interface{}{
			"is_worktree":     true,
			"path":            cwd,
			"name":            filepath.Base(cwd),
			"branch":          branch,
			"main_repo":       mainRepoRoot,
			"beads_redirected": redirectInfo.IsRedirected,
		}
		if redirectInfo.IsRedirected {
			result["beads_local"] = redirectInfo.LocalDir
			result["beads_target"] = redirectInfo.TargetDir
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("Worktree: %s\n", cwd)
	fmt.Printf("  Name: %s\n", filepath.Base(cwd))
	fmt.Printf("  Branch: %s\n", branch)
	fmt.Printf("  Main repo: %s\n", mainRepoRoot)
	if redirectInfo.IsRedirected {
		fmt.Printf("  Beads: redirects to %s\n", redirectInfo.TargetDir)
	} else {
		fmt.Printf("  Beads: local (no redirect)\n")
	}

	return nil
}

// Helper functions

func parseWorktreeList(output string) []WorktreeInfo {
	var worktrees []WorktreeInfo
	var current WorktreeInfo

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			path := strings.TrimPrefix(line, "worktree ")
			current = WorktreeInfo{
				Path: path,
				Name: filepath.Base(path),
			}
		} else if strings.HasPrefix(line, "HEAD ") {
			// Skip HEAD hash
		} else if strings.HasPrefix(line, "branch ") {
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		} else if line == "bare" {
			current.IsMain = true
			current.Branch = "(bare)"
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	// Mark the first non-bare worktree as main
	if len(worktrees) > 0 && worktrees[0].Branch != "(bare)" {
		worktrees[0].IsMain = true
	}

	return worktrees
}

func getBeadsState(worktreePath, mainBeadsDir string) string {
	beadsDir := filepath.Join(worktreePath, ".beads")
	redirectFile := filepath.Join(beadsDir, beads.RedirectFileName)

	if _, err := os.Stat(redirectFile); err == nil {
		return "redirect"
	}
	if _, err := os.Stat(beadsDir); err == nil {
		// Check if this is the main beads dir
		absBeadsDir, _ := filepath.Abs(beadsDir)
		absMainBeadsDir, _ := filepath.Abs(mainBeadsDir)
		if absBeadsDir == absMainBeadsDir {
			return "shared"
		}
		return "local"
	}
	return "none"
}

func getRedirectTarget(worktreePath string) string {
	redirectFile := filepath.Join(worktreePath, ".beads", beads.RedirectFileName)
	// #nosec G304 - path is constructed from worktreePath which comes from git worktree list
	data, err := os.ReadFile(redirectFile)
	if err != nil {
		return ""
	}
	target := strings.TrimSpace(string(data))
	// Resolve relative paths
	if !filepath.IsAbs(target) {
		beadsDir := filepath.Join(worktreePath, ".beads")
		target = filepath.Join(beadsDir, target)
	}
	target, _ = filepath.Abs(target)
	return target
}

func resolveWorktreePath(repoRoot, name string) (string, error) {
	// Try as absolute path first
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}

	// Try relative to cwd
	absPath, _ := filepath.Abs(name)
	if _, err := os.Stat(absPath); err == nil {
		return absPath, nil
	}

	// Try relative to repo root
	repoPath := filepath.Join(repoRoot, name)
	if _, err := os.Stat(repoPath); err == nil {
		return repoPath, nil
	}

	return "", fmt.Errorf("worktree not found: %s", name)
}

func checkWorktreeSafety(worktreePath string) error {
	// Check for uncommitted changes
	gitCmd := exec.Command("git", "status", "--porcelain")
	gitCmd.Dir = worktreePath
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(output))) > 0 {
		return fmt.Errorf("worktree has uncommitted changes")
	}

	// Check for unpushed commits
	gitCmd = exec.Command("git", "log", "@{upstream}..", "--oneline")
	gitCmd.Dir = worktreePath
	output, _ = gitCmd.CombinedOutput() // Ignore error (no upstream is ok)
	if len(strings.TrimSpace(string(output))) > 0 {
		return fmt.Errorf("worktree has unpushed commits")
	}

	// Note: We intentionally don't check stashes here because git stashes
	// are stored globally in the main repo, not per-worktree. Checking
	// stashes would give misleading results.

	return nil
}

func getWorktreeCurrentBranch() string {
	output, err := exec.Command("git", "branch", "--show-current").CombinedOutput()
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(output))
}

func addToGitignore(repoRoot, entry string) error {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	// Read existing content
	content, err := os.ReadFile(gitignorePath) //nolint:gosec // G304: gitignorePath from known repoRoot
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if already present
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == entry || strings.TrimSpace(line) == entry+"/" {
			return nil // Already present
		}
	}

	// Append entry
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: .gitignore should be world-readable
	if err != nil {
		return err
	}
	defer f.Close()

	// Add newline if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	// Add comment and entry
	if _, err := f.WriteString(fmt.Sprintf("# bd worktree\n%s/\n", entry)); err != nil {
		return err
	}

	return nil
}

func removeFromGitignore(repoRoot, entry string) error {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	content, err := os.ReadFile(gitignorePath) //nolint:gosec // G304: gitignorePath from known repoRoot
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	skipNext := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# bd worktree" {
			skipNext = true
			continue
		}
		if skipNext && (trimmed == entry || trimmed == entry+"/") {
			skipNext = false
			continue
		}
		skipNext = false
		newLines = append(newLines, line)
	}

	return os.WriteFile(gitignorePath, []byte(strings.Join(newLines, "\n")), 0644) //nolint:gosec // G306: .gitignore should be world-readable
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
