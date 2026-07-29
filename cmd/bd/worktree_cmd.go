package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/metrics"
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

Worktrees automatically share the same beads database as the main repository
via git common directory discovery — no manual redirect configuration needed.

Examples:
  bd worktree create feature-auth           # Create worktree
  bd worktree create bugfix --branch fix-1  # Create with specific branch name
  bd worktree list                          # List all worktrees
  bd worktree remove feature-auth           # Remove worktree (with safety checks)
  bd worktree info                          # Show info about current worktree`,
}

var worktreeCreateCmd = &cobra.Command{
	Use:   "create <name> [--branch=<branch>]",
	Short: "Create a worktree",
	Long: `Create a git worktree for parallel development.

This command:
1. Creates a git worktree at ./<name> (or specified path)
2. Adds the worktree path to .gitignore (if inside repo root)

The worktree automatically shares the same beads database as the main
repository via git common directory discovery — no redirect file needed.

Examples:
  bd worktree create feature-auth           # Create at ./feature-auth
  bd worktree create bugfix --branch fix-1  # Create with branch name
  bd worktree create ../agents/worker-1     # Create at relative path`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWorktreeCreate,
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
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWorktreeList,
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
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWorktreeInfo,
}

var (
	worktreeBranch string
)

func init() {
	worktreeCreateCmd.Flags().StringVar(&worktreeBranch, "branch", "", "Branch name for the worktree (default: same as name)")

	worktreeCmd.AddCommand(worktreeCreateCmd)
	worktreeCmd.AddCommand(worktreeListCmd)
	worktreeCmd.AddCommand(worktreeRemoveCmd)
	worktreeCmd.AddCommand(worktreeInfoCmd)
	rootCmd.AddCommand(worktreeCmd)
}

type singleWorktreeStringFlag struct {
	name  string
	value string
	set   bool
}

func (flag *singleWorktreeStringFlag) Set(value string) error {
	if flag.set {
		return fmt.Errorf("--%s may be specified only once", flag.name)
	}
	flag.set = true
	if value == "" {
		return fmt.Errorf("--%s requires a non-empty value", flag.name)
	}
	flag.value = value
	return nil
}

func (flag *singleWorktreeStringFlag) String() string {
	return flag.value
}

func (flag *singleWorktreeStringFlag) Type() string {
	return "string"
}

type singleWorktreeBoolFlag struct {
	name  string
	value bool
	set   bool
}

func (flag *singleWorktreeBoolFlag) Set(value string) error {
	if flag.set {
		return fmt.Errorf("--%s may be specified only once", flag.name)
	}
	flag.set = true
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid boolean value %q", value)
	}
	flag.value = parsed
	return nil
}

func (flag *singleWorktreeBoolFlag) String() string {
	return strconv.FormatBool(flag.value)
}

func (flag *singleWorktreeBoolFlag) Type() string {
	return "bool"
}

func (flag *singleWorktreeBoolFlag) IsBoolFlag() bool {
	return true
}

type worktreeRemoveOptions struct {
	force      singleWorktreeBoolFlag
	mergedInto singleWorktreeStringFlag
}

func (options *worktreeRemoveOptions) validate() error {
	if options.force.set && options.mergedInto.set {
		return fmt.Errorf("--force and --merged-into cannot be used together")
	}
	return nil
}

func newWorktreeRemoveCommand() *cobra.Command {
	return newWorktreeRemoveCommandWithHooks(worktreeRemoveHooks{})
}

func newWorktreeRemoveCommandWithHook(beforeFinalCheck func() error) *cobra.Command {
	return newWorktreeRemoveCommandWithHooks(worktreeRemoveHooks{
		beforeFinalCheck: beforeFinalCheck,
	})
}

type worktreeRemoveHooks struct {
	afterTargetResolution func() error
	beforeFinalCheck      func() error
	beforeRemove          func() error
	afterRemoval          func() error
}

func newWorktreeRemoveCommandWithHooks(hooks worktreeRemoveHooks) *cobra.Command {
	options := &worktreeRemoveOptions{
		force:      singleWorktreeBoolFlag{name: "force"},
		mergedInto: singleWorktreeStringFlag{name: "merged-into"},
	}

	command := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a worktree with safety checks",
		Long: `Remove a registered git worktree with fail-closed safety checks.

Without --force, the target must be clean and its pinned HEAD must be contained
in either the configured upstream or the single comparator selected by
--merged-into. Comparators may be full refs, unambiguous short ref names, or
full commit object IDs. Revision expressions and worktree-local pseudorefs such
as HEAD and ORIG_HEAD are rejected.

--force skips cleanliness and containment requirements, but it does not skip
registered-identity and concurrent-change checks. --force and --merged-into
are mutually exclusive, and each flag may be specified at most once.

Worktree removal and .gitignore cleanup are not atomic. If removal succeeds but
cleanup fails, this command returns an error that explicitly reports the
worktree as removed; it does not claim or attempt a rollback.

Examples:
  bd worktree remove feature-auth                    # Check the configured upstream
  bd worktree remove feature-auth --merged-into main # Check containment in main
  bd worktree remove feature-auth --force            # Skip clean/containment checks`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			return options.validate()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeRemove(cmd, args, options, hooks)
		},
	}

	command.Flags().Var(&options.force, "force", "Skip cleanliness and containment checks")
	command.Flags().Lookup("force").NoOptDefVal = "true"
	command.Flags().Var(&options.mergedInto, "merged-into", "Require worktree HEAD to be contained in this ref")
	return command
}

var worktreeRemoveCmd = newWorktreeRemoveCommand()

// repairWorktreeBeadsPermissions applies FixBeadsDirPermissions to worktreePath/.beads when
// the directory exists. Git worktree checkout can leave tracked .beads/ at permissive modes.
func repairWorktreeBeadsPermissions(worktreePath string) {
	beadsDir := filepath.Join(worktreePath, ".beads")
	if fixed, err := config.FixBeadsDirPermissions(beadsDir); err != nil {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: could not fix worktree .beads permissions: %v\n", err)
		}
	} else if fixed && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Fixed .beads permissions to %04o\n", config.BeadsDirPerm)
	}
}

func runWorktreeCreate(cmd *cobra.Command, args []string) error {
	CheckReadonly("worktree create")

	evt := metrics.NewCommandEvent("worktree-create")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := context.Background()

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

	// Get repository context (validates .beads exists and resolves paths)
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("%s; %s: %w", activeWorkspaceNotFoundError(), diagHint(), err)
	}

	// Worktree operations use CWD repo (where user is working), not BEADS_DIR repo
	repoRoot := rc.CWDRepoRoot
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	// Determine branch name
	branch := worktreeBranch
	if branch == "" {
		branch = filepath.Base(name)
	}

	// Create the worktree using secure git command
	gitCmd := gitCmdInDir(ctx, repoRoot, "worktree", "add", "-b", branch, worktreePath)
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		// Try without -b if branch already exists
		gitCmd = gitCmdInDir(ctx, repoRoot, "worktree", "add", worktreePath, branch)
		output, err = gitCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to create worktree: %w\n%s", err, string(output))
		}
	}

	// Tracked .beads/ checked out by git worktree add can inherit umask defaults (0755).
	// Align with bd init / GH#3391 so agent loops do not hit permission warnings (GH#3593).
	repairWorktreeBeadsPermissions(worktreePath)

	// Add to .gitignore if worktree is inside repo root
	if strings.HasPrefix(worktreePath, repoRoot+string(os.PathSeparator)) {
		// Use relative path from repo root for gitignore entry
		relWorktreePath, err := filepath.Rel(repoRoot, worktreePath)
		if err != nil {
			relWorktreePath = filepath.Base(worktreePath)
		}
		relWorktreePath = filepath.ToSlash(relWorktreePath)
		if err := addToGitignore(ctx, repoRoot, relWorktreePath); err != nil {
			// Non-fatal, just warn
			fmt.Fprintf(os.Stderr, "Warning: failed to update .gitignore: %v\n", err)
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"path":   worktreePath,
			"branch": branch,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Created worktree: %s\n", ui.RenderPass("✓"), worktreePath)
	fmt.Printf("  Branch: %s\n", branch)
	return nil
}

func runWorktreeList(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("worktree-list")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := context.Background()

	// Get repository context
	rc, err := beads.GetRepoContext()
	if err != nil {
		// Allow listing worktrees even without .beads (but no beads state info)
		// Fall back to git.GetRepoRoot() for this case
		repoRoot := git.GetRepoRoot()
		if repoRoot == "" {
			return fmt.Errorf("not in a git repository")
		}
		return listWorktreesWithoutBeads(ctx, repoRoot)
	}

	// Worktree operations use CWD repo (where user is working)
	repoRoot := rc.CWDRepoRoot
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	// List worktrees using secure git command
	gitCmd := gitCmdInDir(ctx, repoRoot, "worktree", "list", "--porcelain")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Parse worktree list
	worktrees := parseWorktreeList(string(output))

	// Enrich with beads state (using BeadsDir from RepoContext)
	mainBeadsDir := rc.BeadsDir
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

func runWorktreeRemove(
	cmd *cobra.Command,
	args []string,
	options *worktreeRemoveOptions,
	hooks worktreeRemoveHooks,
) error {
	CheckReadonly("worktree remove")

	evt := metrics.NewCommandEvent("worktree-remove")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := cmd.Context()
	plan, err := prepareWorktreeRemoval(
		ctx,
		args[0],
		options,
		hooks.afterTargetResolution,
	)
	if err != nil {
		return fmt.Errorf("cannot prepare worktree removal: %w", err)
	}

	if hooks.beforeFinalCheck != nil {
		if err := hooks.beforeFinalCheck(); err != nil {
			return fmt.Errorf("worktree removal interrupted before final safety check: %w", err)
		}
	}

	if err := plan.revalidate(ctx); err != nil {
		return fmt.Errorf("worktree changed before removal: %w; nothing was removed", err)
	}

	if hooks.beforeRemove != nil {
		if err := hooks.beforeRemove(); err != nil {
			return fmt.Errorf("worktree removal interrupted before the destructive operation: %w", err)
		}
	}

	// Git uses core.ignorecase while selecting a registration by path. The
	// target path comes from the exact registry spelling, so force ordinal
	// matching for the destructive command: a concurrent disappearance or
	// config change must not redirect removal to a case-variant sibling.
	removeArgs := []string{"-c", "core.ignorecase=false", "worktree", "remove"}
	if options.force.value {
		removeArgs = append(removeArgs, "--force")
	}
	removeArgs = append(removeArgs, "--", plan.target.path)
	output, err := plan.git.combinedOutput(ctx, plan.executionRoot, removeArgs...)
	if err != nil {
		return plan.classifyRemovalFailure(ctx, err, output)
	}

	if hooks.afterRemoval != nil {
		if err := hooks.afterRemoval(); err != nil {
			return &worktreeRemovalPartialError{
				path:  plan.target.path,
				stage: "post-removal processing",
				err:   err,
			}
		}
	}

	if plan.gitignoreCleanup != nil {
		if err := plan.gitignoreCleanup.apply(); err != nil {
			return &worktreeRemovalPartialError{
				path:  plan.target.path,
				stage: ".gitignore cleanup",
				err:   err,
			}
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"removed": plan.target.path,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Removed worktree: %s\n", ui.RenderPass("✓"), plan.target.path)
	return nil
}

type worktreeRemovalPartialError struct {
	path  string
	stage string
	err   error
}

func (err *worktreeRemovalPartialError) Error() string {
	return fmt.Sprintf(
		"worktree was removed at %s, but %s failed: %v; removal was not rolled back",
		err.path,
		err.stage,
		err.err,
	)
}

func (err *worktreeRemovalPartialError) Unwrap() error {
	return err.err
}

func runWorktreeInfo(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("worktree-info")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Check if we're in a worktree (use RepoContext if available, fallback to git)
	var isWorktree bool
	rc, rcErr := beads.GetRepoContext()
	if rcErr == nil {
		isWorktree = rc.IsWorktree
	} else {
		isWorktree = git.IsWorktree()
	}

	if !isWorktree {
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

	branch := getWorktreeCurrentBranch(ctx, cwd)
	redirectInfo := beads.GetRedirectInfo()

	if jsonOutput {
		result := map[string]interface{}{
			"is_worktree":      true,
			"path":             cwd,
			"name":             filepath.Base(cwd),
			"branch":           branch,
			"main_repo":        mainRepoRoot,
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

// gitCmdInDir creates a git command that runs in the specified directory.
// This is used for worktree operations that need to run in a specific location
// (either the CWD repo root or a specific worktree path).
//
// Security: Sets core.hooksPath and GIT_TEMPLATE_DIR to disable hooks/templates
// for defense-in-depth, matching the pattern in RepoContext.GitCmd().
func gitCmdInDir(ctx context.Context, dir string, args ...string) *exec.Cmd {
	gitArgs := append([]string{"-c", "core.hooksPath="}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = dir
	// Security: Disable git hooks and templates (SEC-001, SEC-002)
	cmd.Env = append(os.Environ(),
		"GIT_TEMPLATE_DIR=",
	)
	return cmd
}

// listWorktreesWithoutBeads lists worktrees when no .beads directory exists.
// This fallback allows the command to work in repos that haven't been initialized.
func listWorktreesWithoutBeads(ctx context.Context, repoRoot string) error {
	gitCmd := gitCmdInDir(ctx, repoRoot, "worktree", "list", "--porcelain")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	worktrees := parseWorktreeList(string(output))

	// Set beads state to "none" for all worktrees
	for i := range worktrees {
		worktrees[i].BeadsState = "none"
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
		fmt.Printf("%-20s %-40s %-20s %s\n",
			truncate(name, 20),
			truncate(wt.Path, 40),
			truncate(wt.Branch, 20),
			"none")
	}

	return nil
}

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
	// Resolve relative paths from the worktree root (matching FollowRedirect behavior)
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktreePath, target)
	}
	target, _ = filepath.Abs(target)
	return target
}

func resolveWorktreePath(ctx context.Context, repoRoot, name string) (string, error) {
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

	// Consult git's worktree registry - match by name (basename) or path
	// This handles worktrees created in subdirectories (e.g., .worktrees/foo)
	// where the name shown in "bd worktree list" doesn't match a simple path
	gitCmd := gitCmdInDir(ctx, repoRoot, "worktree", "list", "--porcelain")
	output, err := gitCmd.CombinedOutput()
	if err == nil {
		worktrees := parseWorktreeList(string(output))
		for _, wt := range worktrees {
			if wt.Name == name || wt.Path == name {
				if _, err := os.Stat(wt.Path); err == nil {
					return wt.Path, nil
				}
			}
		}
	}

	return "", fmt.Errorf("worktree not found: %s", name)
}

type worktreeRemovalGit struct {
	executable string
	env        []string
}

func newWorktreeRemovalGit() (*worktreeRemovalGit, error) {
	executable, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("cannot find git executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("cannot pin git executable path: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}

	env := scrubWorktreeRemovalGitEnv(os.Environ())
	env = append(
		env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TEMPLATE_DIR=",
	)

	return &worktreeRemovalGit{
		executable: executable,
		env:        env,
	}, nil
}

func scrubWorktreeRemovalGitEnv(env []string) []string {
	exactKeys := map[string]struct{}{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_CEILING_DIRECTORIES":          {},
		"GIT_COMMON_DIR":                   {},
		"GIT_DIR":                          {},
		"GIT_DISCOVERY_ACROSS_FILESYSTEM":  {},
		"GIT_EXEC_PATH":                    {},
		"GIT_GRAFT_FILE":                   {},
		"GIT_IMPLICIT_WORK_TREE":           {},
		"GIT_INDEX_FILE":                   {},
		"GIT_INTERNAL_SUPER_PREFIX":        {},
		"GIT_NAMESPACE":                    {},
		"GIT_NO_REPLACE_OBJECTS":           {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_OPTIONAL_LOCKS":               {},
		"GIT_PREFIX":                       {},
		"GIT_QUARANTINE_PATH":              {},
		"GIT_REPLACE_REF_BASE":             {},
		"GIT_SHALLOW_FILE":                 {},
		"GIT_SUPER_PREFIX":                 {},
		"GIT_TEMPLATE_DIR":                 {},
		"GIT_WORK_TREE":                    {},
	}

	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key := entry
		if separator := strings.IndexByte(entry, '='); separator >= 0 {
			key = entry[:separator]
		}
		upperKey := strings.ToUpper(key)
		if strings.HasPrefix(upperKey, "GIT_CONFIG") {
			continue
		}
		if _, blocked := exactKeys[upperKey]; blocked {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

func (git *worktreeRemovalGit) command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	gitArgs := make([]string, 0, len(args)+4)
	gitArgs = append(gitArgs, "-c", "core.hooksPath=", "-c", "core.fsmonitor=false")
	gitArgs = append(gitArgs, args...)

	command := exec.CommandContext(ctx, git.executable, gitArgs...)
	command.Dir = dir
	command.Env = append([]string(nil), git.env...)
	return command
}

func (git *worktreeRemovalGit) output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := git.command(ctx, dir, args...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		stderr := strings.TrimSpace(string(exitError.Stderr))
		if stderr != "" {
			return output, fmt.Errorf("%w: %s", err, stderr)
		}
	}
	return output, err
}

func (git *worktreeRemovalGit) combinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return git.command(ctx, dir, args...).CombinedOutput()
}

type registeredWorktree struct {
	path        string
	headOID     string
	branch      string
	detached    bool
	bare        bool
	locked      bool
	lockReason  string
	prunable    bool
	pruneReason string
	isMain      bool
}

func listRegisteredWorktrees(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
) ([]registeredWorktree, error) {
	output, err := git.output(ctx, executionRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("failed to read git worktree registry: %w", err)
	}

	var worktrees []registeredWorktree
	var current registeredWorktree
	appendCurrent := func() {
		if current.path == "" {
			return
		}
		current.isMain = len(worktrees) == 0
		worktrees = append(worktrees, current)
		current = registeredWorktree{}
	}

	for _, field := range strings.Split(string(output), "\x00") {
		if field == "" {
			continue
		}
		switch {
		case strings.HasPrefix(field, "worktree "):
			appendCurrent()
			current.path = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "HEAD "):
			current.headOID = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			current.branch = strings.TrimPrefix(field, "branch ")
		case field == "detached":
			current.detached = true
		case field == "bare":
			current.bare = true
		case field == "locked":
			current.locked = true
		case strings.HasPrefix(field, "locked "):
			current.locked = true
			current.lockReason = strings.TrimPrefix(field, "locked ")
		case field == "prunable":
			current.prunable = true
		case strings.HasPrefix(field, "prunable "):
			current.prunable = true
			current.pruneReason = strings.TrimPrefix(field, "prunable ")
		}
	}
	appendCurrent()

	if len(worktrees) == 0 {
		return nil, fmt.Errorf("git worktree registry is empty")
	}
	return worktrees, nil
}

func sameWorktreePath(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftAbsolute = filepath.Clean(leftAbsolute)
	rightAbsolute = filepath.Clean(rightAbsolute)

	// When both paths are missing, peel exact components in lockstep until
	// existing ancestors can prove physical identity. This accepts equivalent
	// ancestor spellings such as a Windows 8.3 alias without ever case-folding
	// an unresolved component.
	for {
		leftInfo, leftStatErr := os.Stat(leftAbsolute)
		rightInfo, rightStatErr := os.Stat(rightAbsolute)
		if leftStatErr == nil || rightStatErr == nil {
			return leftStatErr == nil &&
				rightStatErr == nil &&
				os.SameFile(leftInfo, rightInfo)
		}
		if !os.IsNotExist(leftStatErr) || !os.IsNotExist(rightStatErr) {
			return false
		}
		if filepath.Base(leftAbsolute) != filepath.Base(rightAbsolute) {
			return false
		}
		leftParent := filepath.Dir(leftAbsolute)
		rightParent := filepath.Dir(rightAbsolute)
		if leftParent == leftAbsolute || rightParent == rightAbsolute {
			return false
		}
		leftAbsolute = leftParent
		rightAbsolute = rightParent
	}
}

func findRegisteredWorktreeByPath(
	worktrees []registeredWorktree,
	path string,
) (registeredWorktree, bool) {
	for _, worktree := range worktrees {
		if sameWorktreePath(worktree.path, path) {
			return worktree, true
		}
	}
	return registeredWorktree{}, false
}

func resolveRegisteredWorktree(
	name string,
	currentRoot string,
	mainRoot string,
	worktrees []registeredWorktree,
) (registeredWorktree, error) {
	candidatePaths := make([]string, 0, 3)
	if filepath.IsAbs(name) {
		candidatePaths = append(candidatePaths, name)
	} else {
		if currentCandidate, err := filepath.Abs(name); err == nil {
			candidatePaths = append(candidatePaths, currentCandidate)
		}
		candidatePaths = append(candidatePaths, filepath.Join(currentRoot, name))
		if !sameWorktreePath(currentRoot, mainRoot) {
			candidatePaths = append(candidatePaths, filepath.Join(mainRoot, name))
		}
	}

	allowBasename := filepath.Base(name) == name
	matches := make([]registeredWorktree, 0, 1)
	for _, worktree := range worktrees {
		matched := false
		for _, candidate := range candidatePaths {
			if sameWorktreePath(worktree.path, candidate) {
				matched = true
				break
			}
		}
		if !matched && allowBasename && filepath.Base(worktree.path) == name {
			matched = true
		}
		if !matched {
			continue
		}

		duplicate := false
		for _, existing := range matches {
			if sameWorktreePath(existing.path, worktree.path) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			matches = append(matches, worktree)
		}
	}

	switch len(matches) {
	case 0:
		return registeredWorktree{}, fmt.Errorf("registered worktree not found: %s", name)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, 0, len(matches))
		for _, match := range matches {
			paths = append(paths, match.path)
		}
		sort.Strings(paths)
		return registeredWorktree{}, fmt.Errorf(
			"worktree name %q is ambiguous; use an absolute path (matches: %s)",
			name,
			strings.Join(paths, ", "),
		)
	}
}

type pinnedWorktreeTarget struct {
	path                 string
	pathInfo             os.FileInfo
	gitDir               string
	gitDirInfo           os.FileInfo
	gitMarkerInfo        os.FileInfo
	gitDirFingerprint    string
	gitMarkerFingerprint string
	commonDir            string
	headOID              string
	branch               string
	detached             bool
	bare                 bool
	locked               bool
	lockReason           string
	prunable             bool
	pruneReason          string
	status               string
	statusFingerprint    string
	registryID           string
}

func inspectWorktreeTarget(
	ctx context.Context,
	git *worktreeRemovalGit,
	worktree registeredWorktree,
) (pinnedWorktreeTarget, error) {
	gitDirOutput, err := git.output(ctx, worktree.path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to resolve target git directory: %w", err)
	}
	gitDir := filepath.Clean(strings.TrimSpace(string(gitDirOutput)))
	commonDirOutput, err := git.output(
		ctx,
		worktree.path,
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to resolve target common git directory: %w", err)
	}
	headOutput, err := git.output(
		ctx,
		worktree.path,
		"rev-parse",
		"--verify",
		"--quiet",
		"--end-of-options",
		"HEAD^{commit}",
	)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("target HEAD does not resolve to a commit: %w", err)
	}
	statusOutput, err := git.output(
		ctx,
		worktree.path,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
		"--ignore-submodules=none",
		"--ignored=matching",
	)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to inspect target cleanliness: %w", err)
	}

	headOID := strings.TrimSpace(string(headOutput))
	if headOID == "" || headOID != worktree.headOID {
		return pinnedWorktreeTarget{}, fmt.Errorf(
			"target HEAD disagrees with git worktree registry (registry %q, target %q)",
			worktree.headOID,
			headOID,
		)
	}
	pathInfo, err := os.Lstat(worktree.path)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to pin target directory identity: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return pinnedWorktreeTarget{}, fmt.Errorf("target path is not a real directory: %s", worktree.path)
	}
	gitDirInfo, err := os.Lstat(gitDir)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to pin target git directory identity: %w", err)
	}
	if gitDirInfo.Mode()&os.ModeSymlink != 0 || !gitDirInfo.IsDir() {
		return pinnedWorktreeTarget{}, fmt.Errorf("target git directory is not a real directory: %s", gitDir)
	}
	gitMarkerPath := filepath.Join(worktree.path, ".git")
	gitMarkerInfo, err := os.Lstat(gitMarkerPath)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to pin target git marker identity: %w", err)
	}
	if gitMarkerInfo.Mode()&os.ModeSymlink != 0 || !gitMarkerInfo.Mode().IsRegular() {
		return pinnedWorktreeTarget{}, fmt.Errorf("target git marker is not a regular file: %s", gitMarkerPath)
	}
	gitDirFingerprint, err := fingerprintWorktreeFilesystem(gitDir)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to fingerprint target git directory: %w", err)
	}
	gitMarkerFingerprint, err := fingerprintWorktreeFilesystem(gitMarkerPath)
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to fingerprint target git marker: %w", err)
	}
	statusFingerprint, err := fingerprintWorktreeStatusPaths(worktree.path, string(statusOutput))
	if err != nil {
		return pinnedWorktreeTarget{}, fmt.Errorf("failed to fingerprint target changes: %w", err)
	}

	return pinnedWorktreeTarget{
		path:                 filepath.Clean(worktree.path),
		pathInfo:             pathInfo,
		gitDir:               gitDir,
		gitDirInfo:           gitDirInfo,
		gitMarkerInfo:        gitMarkerInfo,
		gitDirFingerprint:    gitDirFingerprint,
		gitMarkerFingerprint: gitMarkerFingerprint,
		commonDir:            filepath.Clean(strings.TrimSpace(string(commonDirOutput))),
		headOID:              headOID,
		branch:               worktree.branch,
		detached:             worktree.detached,
		bare:                 worktree.bare,
		locked:               worktree.locked,
		lockReason:           worktree.lockReason,
		prunable:             worktree.prunable,
		pruneReason:          worktree.pruneReason,
		status:               string(statusOutput),
		statusFingerprint:    statusFingerprint,
		registryID:           worktreeRegistryIdentity(worktree),
	}, nil
}

func fingerprintWorktreeFilesystem(path string) (string, error) {
	hasher := sha256.New()
	root := filepath.Clean(path)
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, err := fmt.Fprintf(
			hasher,
			"%s\x00%s\x00%d\x00%d\x00",
			relative,
			info.Mode().String(),
			info.Size(),
			info.ModTime().UTC().UnixNano(),
		); err != nil {
			return err
		}

		switch {
		case info.Mode().IsRegular():
			file, err := os.Open(current) //nolint:gosec // path is rooted in a registered worktree or its gitdir
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hasher, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(hasher, target); err != nil {
				return err
			}
		}
		_, err = hasher.Write([]byte{0})
		return err
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func fingerprintWorktreeStatusPaths(worktreePath, status string) (string, error) {
	records := strings.Split(status, "\x00")
	pathSet := make(map[string]struct{})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return "", fmt.Errorf("unexpected porcelain status record %q", record)
		}
		code := record[:2]
		pathSet[record[3:]] = struct{}{}
		if strings.ContainsAny(code, "RC") {
			index++
			if index >= len(records) || records[index] == "" {
				return "", fmt.Errorf("missing source path for porcelain rename/copy record %q", record)
			}
			pathSet[records[index]] = struct{}{}
		}
	}

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	hasher := sha256.New()
	for _, gitPath := range paths {
		cleanPath := filepath.Clean(filepath.FromSlash(gitPath))
		if cleanPath == "." ||
			filepath.IsAbs(cleanPath) ||
			cleanPath == ".." ||
			strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("unsafe path %q in porcelain status", gitPath)
		}
		if _, err := fmt.Fprintf(hasher, "%s\x00", filepath.ToSlash(cleanPath)); err != nil {
			return "", err
		}
		fingerprint, err := fingerprintWorktreeFilesystem(filepath.Join(worktreePath, cleanPath))
		if err != nil {
			if os.IsNotExist(err) {
				fingerprint = "<missing>"
			} else {
				return "", err
			}
		}
		if _, err := fmt.Fprintf(hasher, "%s\x00", fingerprint); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func worktreeRegistryIdentity(worktree registeredWorktree) string {
	return strings.Join([]string{
		filepath.Clean(worktree.path),
		worktree.branch,
		strconv.FormatBool(worktree.detached),
		strconv.FormatBool(worktree.bare),
		strconv.FormatBool(worktree.locked),
		worktree.lockReason,
		strconv.FormatBool(worktree.prunable),
		worktree.pruneReason,
	}, "\x00")
}

type pinnedWorktreeComparator struct {
	selector    string
	explicit    bool
	ref         string
	terminalRef string
	oid         string
}

type worktreeRemovalPlan struct {
	git              *worktreeRemovalGit
	executionRoot    string
	mainWorktree     string
	commonDir        string
	target           pinnedWorktreeTarget
	comparator       *pinnedWorktreeComparator
	force            bool
	gitignoreCleanup *gitignoreCleanupPlan
}

func prepareWorktreeRemoval(
	ctx context.Context,
	name string,
	options *worktreeRemoveOptions,
	afterTargetResolution func() error,
) (*worktreeRemovalPlan, error) {
	gitRunner, err := newWorktreeRemovalGit()
	if err != nil {
		return nil, err
	}
	currentDirectory, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve current directory: %w", err)
	}
	currentRootOutput, err := gitRunner.output(ctx, currentDirectory, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not in a git worktree: %w", err)
	}
	currentRoot := filepath.Clean(strings.TrimSpace(string(currentRootOutput)))

	worktrees, err := listRegisteredWorktrees(ctx, gitRunner, currentRoot)
	if err != nil {
		return nil, err
	}
	mainWorktree := worktrees[0]
	if mainWorktree.bare {
		return nil, fmt.Errorf("cannot remove a worktree when the primary worktree is bare")
	}
	targetEntry, err := resolveRegisteredWorktree(name, currentRoot, mainWorktree.path, worktrees)
	if err != nil {
		return nil, err
	}
	if targetEntry.isMain {
		return nil, fmt.Errorf("cannot remove the primary worktree")
	}
	if sameWorktreePath(targetEntry.path, currentRoot) {
		return nil, fmt.Errorf("cannot remove the worktree containing the running command")
	}
	if afterTargetResolution != nil {
		if err := afterTargetResolution(); err != nil {
			return nil, fmt.Errorf("worktree removal interrupted after target resolution: %w", err)
		}
	}

	mainCommonDirOutput, err := gitRunner.output(
		ctx,
		mainWorktree.path,
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repository common git directory: %w", err)
	}
	mainCommonDir := filepath.Clean(strings.TrimSpace(string(mainCommonDirOutput)))

	target, err := inspectWorktreeTarget(ctx, gitRunner, targetEntry)
	if err != nil {
		return nil, err
	}
	if !sameWorktreePath(target.commonDir, mainCommonDir) {
		return nil, fmt.Errorf(
			"target common git directory %q does not match repository %q",
			target.commonDir,
			mainCommonDir,
		)
	}

	plan := &worktreeRemovalPlan{
		git:           gitRunner,
		executionRoot: filepath.Clean(mainWorktree.path),
		mainWorktree:  filepath.Clean(mainWorktree.path),
		commonDir:     mainCommonDir,
		target:        target,
		force:         options.force.value,
	}
	if relative, inside := relativeWorktreePath(plan.mainWorktree, target.path); inside {
		plan.gitignoreCleanup, err = prepareGitignoreCleanup(plan.mainWorktree, relative)
		if err != nil {
			return nil, fmt.Errorf("cannot safely prepare .gitignore cleanup: %w", err)
		}
	}

	if !options.force.value {
		if target.status != "" {
			return nil, fmt.Errorf("worktree contains modified, untracked, or ignored files")
		}

		var comparator pinnedWorktreeComparator
		if options.mergedInto.set {
			comparator, err = resolveExplicitWorktreeComparator(
				ctx,
				gitRunner,
				plan.executionRoot,
				target,
				options.mergedInto.value,
			)
		} else {
			comparator, err = resolveUpstreamWorktreeComparator(
				ctx,
				gitRunner,
				plan.executionRoot,
				target,
			)
		}
		if err != nil {
			return nil, err
		}
		if err := verifyWorktreeContainment(ctx, gitRunner, plan.executionRoot, target.headOID, comparator); err != nil {
			return nil, err
		}
		plan.comparator = &comparator
	}

	return plan, nil
}

func relativeWorktreePath(root, target string) (string, bool) {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return "", false
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func (plan *worktreeRemovalPlan) revalidate(ctx context.Context) error {
	worktrees, err := listRegisteredWorktrees(ctx, plan.git, plan.executionRoot)
	if err != nil {
		return err
	}
	currentEntry, found := findRegisteredWorktreeByPath(worktrees, plan.target.path)
	if !found {
		return fmt.Errorf("target is no longer registered at %s", plan.target.path)
	}
	if worktreeRegistryIdentity(currentEntry) != plan.target.registryID {
		return fmt.Errorf("registered target identity changed")
	}

	currentTarget, err := inspectWorktreeTarget(ctx, plan.git, currentEntry)
	if err != nil {
		return err
	}
	if !sameWorktreePath(currentTarget.gitDir, plan.target.gitDir) {
		return fmt.Errorf("target git directory changed")
	}
	if !sameWorktreePath(currentTarget.commonDir, plan.commonDir) {
		return fmt.Errorf("target common git directory changed")
	}
	if currentTarget.headOID != plan.target.headOID {
		return fmt.Errorf("target HEAD changed from %s to %s", plan.target.headOID, currentTarget.headOID)
	}
	if currentTarget.status != plan.target.status {
		return fmt.Errorf("target cleanliness changed")
	}
	if currentTarget.statusFingerprint != plan.target.statusFingerprint {
		return fmt.Errorf("target changed files changed")
	}
	if !plan.force && currentTarget.status != "" {
		return fmt.Errorf("target is no longer clean")
	}
	if !os.SameFile(currentTarget.pathInfo, plan.target.pathInfo) ||
		!samePinnedFileMetadata(currentTarget.pathInfo, plan.target.pathInfo) {
		return fmt.Errorf("target directory identity changed")
	}
	if !os.SameFile(currentTarget.gitDirInfo, plan.target.gitDirInfo) ||
		!samePinnedFileMetadata(currentTarget.gitDirInfo, plan.target.gitDirInfo) {
		return fmt.Errorf("target git directory identity changed")
	}
	if !os.SameFile(currentTarget.gitMarkerInfo, plan.target.gitMarkerInfo) ||
		!samePinnedFileMetadata(currentTarget.gitMarkerInfo, plan.target.gitMarkerInfo) {
		return fmt.Errorf("target git marker identity changed")
	}
	if currentTarget.gitDirFingerprint != plan.target.gitDirFingerprint {
		return fmt.Errorf("target git directory identity changed (contents mismatch)")
	}
	if currentTarget.gitMarkerFingerprint != plan.target.gitMarkerFingerprint {
		return fmt.Errorf("registered target identity changed (git marker mismatch)")
	}
	if plan.gitignoreCleanup != nil {
		if err := plan.gitignoreCleanup.validate(); err != nil {
			return fmt.Errorf(".gitignore changed before removal: %w", err)
		}
	}

	if plan.comparator == nil {
		return nil
	}
	currentComparator, err := plan.resolveComparator(ctx, currentTarget)
	if err != nil {
		return err
	}
	if currentComparator != *plan.comparator {
		return fmt.Errorf(
			"comparison target changed (was %s, now %s)",
			plan.comparator.oid,
			currentComparator.oid,
		)
	}
	return verifyWorktreeContainment(
		ctx,
		plan.git,
		plan.executionRoot,
		currentTarget.headOID,
		currentComparator,
	)
}

func samePinnedFileMetadata(current, pinned os.FileInfo) bool {
	return os.SameFile(current, pinned) &&
		current.Mode() == pinned.Mode() &&
		current.Size() == pinned.Size() &&
		current.ModTime().Equal(pinned.ModTime())
}

func (plan *worktreeRemovalPlan) classifyRemovalFailure(
	ctx context.Context,
	removeErr error,
	output []byte,
) error {
	reinspectionErr := plan.revalidate(ctx)
	diagnostic := strings.TrimSpace(string(output))
	if reinspectionErr == nil {
		if diagnostic == "" {
			return fmt.Errorf(
				"git worktree remove failed, but the target was revalidated unchanged: %w",
				removeErr,
			)
		}
		return fmt.Errorf(
			"git worktree remove failed, but the target was revalidated unchanged: %w\n%s",
			removeErr,
			diagnostic,
		)
	}

	worktrees, listErr := listRegisteredWorktrees(ctx, plan.git, plan.executionRoot)
	registered := false
	if listErr == nil {
		_, registered = findRegisteredWorktreeByPath(worktrees, plan.target.path)
	}
	_, pathErr := os.Lstat(plan.target.path)
	pathExists := pathErr == nil
	if pathErr != nil && !os.IsNotExist(pathErr) {
		pathExists = true
	}

	state := fmt.Sprintf("registered=%t, path_exists=%t", registered, pathExists)
	if listErr != nil {
		state += fmt.Sprintf(", registry inspection failed: %v", listErr)
	}
	if pathErr != nil && !os.IsNotExist(pathErr) {
		state += fmt.Sprintf(", path inspection failed: %v", pathErr)
	}
	if diagnostic == "" {
		return fmt.Errorf(
			"git worktree remove failed and target state is partial or indeterminate (%s): %w; reinspection: %v",
			state,
			removeErr,
			reinspectionErr,
		)
	}
	return fmt.Errorf(
		"git worktree remove failed and target state is partial or indeterminate (%s): %w; reinspection: %v\n%s",
		state,
		removeErr,
		reinspectionErr,
		diagnostic,
	)
}

func (plan *worktreeRemovalPlan) resolveComparator(
	ctx context.Context,
	target pinnedWorktreeTarget,
) (pinnedWorktreeComparator, error) {
	if plan.comparator.explicit {
		return resolveExplicitWorktreeComparator(
			ctx,
			plan.git,
			plan.executionRoot,
			target,
			plan.comparator.selector,
		)
	}
	return resolveUpstreamWorktreeComparator(ctx, plan.git, plan.executionRoot, target)
}

func isWorktreeLocalPseudoref(selector string) bool {
	switch selector {
	case "HEAD",
		"AUTO_MERGE",
		"BISECT_EXPECTED_REV",
		"MERGE_AUTOSTASH",
		"NOTES_MERGE_PARTIAL",
		"NOTES_MERGE_REF":
		return true
	}
	if !strings.HasSuffix(selector, "_HEAD") {
		return false
	}
	prefix := strings.TrimSuffix(selector, "_HEAD")
	if prefix == "" {
		return false
	}
	for _, character := range prefix {
		if character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' {
			continue
		}
		return false
	}
	return true
}

func isWorktreeLocalRef(ref string) bool {
	for _, namespace := range []string{
		"refs/bisect",
		"refs/rewritten",
		"refs/worktree",
	} {
		if ref == namespace || strings.HasPrefix(ref, namespace+"/") {
			return true
		}
	}
	return false
}

func resolveExplicitWorktreeComparator(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	target pinnedWorktreeTarget,
	selector string,
) (pinnedWorktreeComparator, error) {
	if isWorktreeLocalPseudoref(selector) {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"--merged-into value %q is a worktree-local pseudoref; use a full shared ref or full commit object ID",
			selector,
		)
	}

	if strings.HasPrefix(selector, "refs/") {
		if isWorktreeLocalRef(selector) {
			return pinnedWorktreeComparator{}, fmt.Errorf(
				"--merged-into value %q is in a worktree-local ref namespace; use a shared ref or full commit object ID",
				selector,
			)
		}
		valid, err := gitRefNameIsValid(ctx, git, executionRoot, selector, false)
		if err != nil {
			return pinnedWorktreeComparator{}, err
		}
		if !valid {
			return pinnedWorktreeComparator{}, fmt.Errorf("--merged-into value %q is not a valid full ref", selector)
		}
		return pinWorktreeComparatorRef(ctx, git, executionRoot, target, selector, selector, true)
	}

	validShortName, err := gitRefNameIsValid(ctx, git, executionRoot, selector, true)
	if err != nil {
		return pinnedWorktreeComparator{}, err
	}
	if !validShortName {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"--merged-into value %q is not an accepted ref name or full commit object ID",
			selector,
		)
	}

	matches, err := findWorktreeComparatorRefs(ctx, git, executionRoot, selector)
	if err != nil {
		return pinnedWorktreeComparator{}, err
	}
	hashLength, err := repositoryObjectIDLength(ctx, git, executionRoot)
	if err != nil {
		return pinnedWorktreeComparator{}, err
	}
	fullOID := isHexObjectID(selector, hashLength)

	if len(matches) > 1 || fullOID && len(matches) > 0 {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"--merged-into value %q is ambiguous; use a full ref name or a non-ref full object ID (matches: %s)",
			selector,
			strings.Join(matches, ", "),
		)
	}
	if len(matches) == 1 {
		return pinWorktreeComparatorRef(ctx, git, executionRoot, target, selector, matches[0], true)
	}
	if !fullOID {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"--merged-into value %q does not name an existing unambiguous ref",
			selector,
		)
	}

	oid, err := resolveWorktreeCommitOID(ctx, git, executionRoot, selector)
	if err != nil {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"--merged-into object ID %q does not resolve to a commit: %w",
			selector,
			err,
		)
	}
	if oid == target.headOID {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"--merged-into object ID %q is the target HEAD itself; use a ref or descendant commit that independently proves containment",
			selector,
		)
	}
	return pinnedWorktreeComparator{
		selector: selector,
		explicit: true,
		oid:      oid,
	}, nil
}

func gitRefNameIsValid(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	ref string,
	allowOneLevel bool,
) (bool, error) {
	args := []string{"check-ref-format"}
	if allowOneLevel {
		args = append(args, "--allow-onelevel")
	}
	args = append(args, ref)
	_, err := git.output(ctx, executionRoot, args...)
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("failed to validate git ref name %q: %w", ref, err)
}

func findWorktreeComparatorRefs(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	selector string,
) ([]string, error) {
	candidates := []string{
		"refs/" + selector,
		"refs/tags/" + selector,
		"refs/heads/" + selector,
		"refs/remotes/" + selector,
		"refs/remotes/" + selector + "/HEAD",
	}
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateSet[candidate] = struct{}{}
	}

	args := []string{"for-each-ref", "--format=%(refname)", "--"}
	args = append(args, candidates...)
	output, err := git.output(ctx, executionRoot, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate --merged-into ref %q: %w", selector, err)
	}

	matchSet := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		ref := strings.TrimSpace(line)
		if _, candidate := candidateSet[ref]; candidate {
			matchSet[ref] = struct{}{}
		}
	}
	matches := make([]string, 0, len(matchSet))
	for ref := range matchSet {
		matches = append(matches, ref)
	}
	sort.Strings(matches)
	return matches, nil
}

func repositoryObjectIDLength(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
) (int, error) {
	output, err := git.output(ctx, executionRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return 0, fmt.Errorf("failed to determine repository object format: %w", err)
	}
	switch strings.TrimSpace(string(output)) {
	case "sha1":
		return 40, nil
	case "sha256":
		return 64, nil
	default:
		return 0, fmt.Errorf("unsupported git object format %q", strings.TrimSpace(string(output)))
	}
}

func isHexObjectID(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'f' ||
			character >= 'A' && character <= 'F' {
			continue
		}
		return false
	}
	return true
}

func pinWorktreeComparatorRef(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	target pinnedWorktreeTarget,
	selector string,
	ref string,
	explicit bool,
) (pinnedWorktreeComparator, error) {
	terminalRef, err := resolveWorktreeTerminalRef(ctx, git, executionRoot, ref)
	if err != nil {
		return pinnedWorktreeComparator{}, err
	}
	if isWorktreeLocalRef(ref) || isWorktreeLocalRef(terminalRef) {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"comparison ref %q resolves through a worktree-local ref namespace and cannot independently prove containment",
			ref,
		)
	}
	if ref == target.branch || terminalRef == target.branch {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"comparison ref %q resolves to the target worktree branch and cannot independently prove containment",
			ref,
		)
	}
	oid, err := resolveWorktreeCommitOID(ctx, git, executionRoot, ref)
	if err != nil {
		return pinnedWorktreeComparator{}, fmt.Errorf("comparison ref %q does not resolve to a commit: %w", ref, err)
	}
	return pinnedWorktreeComparator{
		selector:    selector,
		explicit:    explicit,
		ref:         ref,
		terminalRef: terminalRef,
		oid:         oid,
	}, nil
}

func resolveWorktreeTerminalRef(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	ref string,
) (string, error) {
	current := ref
	seen := make(map[string]struct{})
	for range 16 {
		if _, duplicate := seen[current]; duplicate {
			return "", fmt.Errorf("symbolic ref cycle while resolving %q", ref)
		}
		seen[current] = struct{}{}

		output, err := git.output(ctx, executionRoot, "symbolic-ref", "--quiet", current)
		if err == nil {
			next := strings.TrimSpace(string(output))
			if !strings.HasPrefix(next, "refs/") {
				return "", fmt.Errorf("symbolic ref %q resolves outside refs/: %q", current, next)
			}
			current = next
			continue
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return current, nil
		}
		return "", fmt.Errorf("failed to inspect symbolic ref %q: %w", current, err)
	}
	return "", fmt.Errorf("symbolic ref chain for %q exceeds 16 links", ref)
}

func resolveWorktreeCommitOID(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	refOrOID string,
) (string, error) {
	output, err := git.output(
		ctx,
		executionRoot,
		"rev-parse",
		"--verify",
		"--quiet",
		"--end-of-options",
		refOrOID+"^{commit}",
	)
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(output))
	hashLength, err := repositoryObjectIDLength(ctx, git, executionRoot)
	if err != nil {
		return "", err
	}
	if !isHexObjectID(oid, hashLength) {
		return "", fmt.Errorf("git returned invalid commit object ID %q", oid)
	}
	return strings.ToLower(oid), nil
}

func resolveUpstreamWorktreeComparator(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	target pinnedWorktreeTarget,
) (pinnedWorktreeComparator, error) {
	if target.branch == "" || target.detached {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"cannot verify unpushed commits: target is detached; use --merged-into <ref>",
		)
	}
	output, err := git.output(
		ctx,
		executionRoot,
		"for-each-ref",
		"--format=%(upstream)",
		"--",
		target.branch,
	)
	if err != nil {
		return pinnedWorktreeComparator{}, fmt.Errorf("failed to inspect configured upstream: %w", err)
	}
	upstreamRef := strings.TrimSpace(string(output))
	if upstreamRef == "" || strings.Contains(upstreamRef, "\n") {
		return pinnedWorktreeComparator{}, fmt.Errorf(
			"cannot verify unpushed commits: the target branch has no single resolvable upstream; configure an upstream or use --merged-into <ref>",
		)
	}
	return pinWorktreeComparatorRef(
		ctx,
		git,
		executionRoot,
		target,
		upstreamRef,
		upstreamRef,
		false,
	)
}

func verifyWorktreeContainment(
	ctx context.Context,
	git *worktreeRemovalGit,
	executionRoot string,
	headOID string,
	comparator pinnedWorktreeComparator,
) error {
	output, err := git.output(
		ctx,
		executionRoot,
		"merge-base",
		"--is-ancestor",
		headOID,
		comparator.oid,
	)
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		if comparator.explicit {
			return fmt.Errorf(
				"worktree HEAD %s is not contained in --merged-into value %q at %s",
				headOID,
				comparator.selector,
				comparator.oid,
			)
		}
		return fmt.Errorf("worktree has commits not contained in its configured upstream")
	}
	return fmt.Errorf(
		"failed to verify worktree containment (%s in %s): %w\n%s",
		headOID,
		comparator.oid,
		err,
		strings.TrimSpace(string(output)),
	)
}

func getWorktreeCurrentBranch(ctx context.Context, dir string) string {
	gitCmd := gitCmdInDir(ctx, dir, "branch", "--show-current")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(output))
}

func addToGitignore(ctx context.Context, repoRoot, entry string) error {
	entry = strings.TrimSuffix(filepath.ToSlash(entry), "/")
	if entry == "" {
		return fmt.Errorf("gitignore entry must not be empty")
	}
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	// If git already ignores this path (e.g., via a parent pattern like
	// ".worktrees/"), avoid appending one line per worktree.
	ignored, err := isIgnoredByGit(ctx, repoRoot, entry)
	if err == nil && ignored {
		return nil
	}

	// Read existing content
	content, err := os.ReadFile(gitignorePath) //nolint:gosec // G304: gitignorePath from known repoRoot
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if already present or covered by a parent-directory pattern.
	// e.g. if ".worktrees" is in .gitignore, ".worktrees/my-branch" is already covered.
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSuffix(filepath.ToSlash(line), "/")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == entry || strings.HasPrefix(entry+"/", trimmed+"/") {
			return nil // Already present or covered by a parent pattern
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

func isIgnoredByGit(ctx context.Context, repoRoot, entry string) (bool, error) {
	normalized := strings.TrimSuffix(filepath.ToSlash(entry), "/")
	if normalized == "" {
		return false, nil
	}

	gitCmd := gitCmdInDir(ctx, repoRoot, "check-ignore", "-q", "--no-index", "--", normalized)
	err := gitCmd.Run()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, err
}

type gitignoreCleanupPlan struct {
	repoRoot string
	path     string
	info     os.FileInfo
	original []byte
	updated  []byte
}

type gitignoreLine struct {
	raw  []byte
	body string
}

func prepareGitignoreCleanup(repoRoot, entry string) (*gitignoreCleanupPlan, error) {
	entry = strings.TrimSuffix(filepath.ToSlash(entry), "/")
	if entry == "" {
		return nil, fmt.Errorf("gitignore entry must not be empty")
	}
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if _, err := os.Lstat(gitignorePath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	info, content, err := readStableRegularFile(gitignorePath)
	if err != nil {
		return nil, err
	}
	updated, changed := removeManagedGitignoreEntry(content, entry)
	if !changed {
		return nil, nil
	}
	return &gitignoreCleanupPlan{
		repoRoot: repoRoot,
		path:     gitignorePath,
		info:     info,
		original: content,
		updated:  updated,
	}, nil
}

func readStableRegularFile(path string) (os.FileInfo, []byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}

	file, err := os.Open(path) //nolint:gosec // path is the pinned primary worktree .gitignore
	if err != nil {
		return nil, nil, err
	}
	openedBefore, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !openedBefore.Mode().IsRegular() || !samePinnedFileMetadata(openedBefore, before) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s changed while it was being opened", path)
	}
	content, readErr := io.ReadAll(file)
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if statErr != nil {
		return nil, nil, statErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}

	after, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 ||
		!after.Mode().IsRegular() ||
		!samePinnedFileMetadata(openedAfter, openedBefore) ||
		!samePinnedFileMetadata(after, openedAfter) {
		return nil, nil, fmt.Errorf("%s changed while it was being read", path)
	}
	return after, content, nil
}

func removeManagedGitignoreEntry(content []byte, entry string) ([]byte, bool) {
	lines := splitGitignoreLines(content)
	updated := make([]byte, 0, len(content))
	changed := false
	for index := 0; index < len(lines); {
		if lines[index].body == "# bd worktree" &&
			index+1 < len(lines) &&
			gitignoreLineMatchesEntry(lines[index+1].body, entry) {
			changed = true
			index += 2
			continue
		}
		updated = append(updated, lines[index].raw...)
		index++
	}
	return updated, changed
}

func splitGitignoreLines(content []byte) []gitignoreLine {
	lines := make([]gitignoreLine, 0, bytes.Count(content, []byte{'\n'})+1)
	for start := 0; start < len(content); {
		end := len(content)
		if newline := bytes.IndexByte(content[start:], '\n'); newline >= 0 {
			end = start + newline + 1
		}
		bodyEnd := end
		if bodyEnd > start && content[bodyEnd-1] == '\n' {
			bodyEnd--
		}
		if bodyEnd > start && content[bodyEnd-1] == '\r' {
			bodyEnd--
		}
		lines = append(lines, gitignoreLine{
			raw:  content[start:end],
			body: string(content[start:bodyEnd]),
		})
		start = end
	}
	return lines
}

func gitignoreLineMatchesEntry(line, entry string) bool {
	normalized := strings.TrimSuffix(
		filepath.ToSlash(line),
		"/",
	)
	return normalized == entry
}

func (plan *gitignoreCleanupPlan) apply() error {
	if err := plan.validate(); err != nil {
		return err
	}

	temp, err := os.CreateTemp(plan.repoRoot, ".gitignore.bd-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(plan.info.Mode().Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(plan.updated); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	if err := plan.validate(); err != nil {
		return fmt.Errorf("destination changed before atomic cleanup: %w", err)
	}
	if err := os.Rename(tempPath, plan.path); err != nil {
		return fmt.Errorf("atomically replace %s: %w", plan.path, err)
	}
	removeTemp = false
	return nil
}

func (plan *gitignoreCleanupPlan) validate() error {
	currentInfo, currentContent, err := readStableRegularFile(plan.path)
	if err != nil {
		return err
	}
	if !samePinnedFileMetadata(currentInfo, plan.info) ||
		!bytes.Equal(currentContent, plan.original) {
		return fmt.Errorf("%s changed after removal was prepared", plan.path)
	}
	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
