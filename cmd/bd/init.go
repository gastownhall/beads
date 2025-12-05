package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize bd in the current directory",
	Long: `Initialize bd in the current directory by creating a .beads/ directory
and database file. Optionally specify a custom issue prefix.

With --no-db: creates .beads/ directory and issues.jsonl file instead of SQLite database.

With --stealth: configures global git settings for invisible beads usage:
  • Global gitignore to prevent beads files from being committed
  • Claude Code settings with bd onboard instruction
  Perfect for personal use without affecting repo collaborators.`,
	Run: func(cmd *cobra.Command, _ []string) {
		prefix, _ := cmd.Flags().GetString("prefix")
		quiet, _ := cmd.Flags().GetBool("quiet")
		branch, _ := cmd.Flags().GetString("branch")
		contributor, _ := cmd.Flags().GetBool("contributor")
		team, _ := cmd.Flags().GetBool("team")
		stealth, _ := cmd.Flags().GetBool("stealth")
		skipMergeDriver, _ := cmd.Flags().GetBool("skip-merge-driver")
		skipHooks, _ := cmd.Flags().GetBool("skip-hooks")
		force, _ := cmd.Flags().GetBool("force")

		// Initialize config (PersistentPreRun doesn't run for init command)
		if err := config.Initialize(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
			// Non-fatal - continue with defaults
		}

		// Safety guard: check for existing JSONL with issues (bd-emg)
		// This prevents accidental re-initialization in fresh clones
		if !force {
			if err := checkExistingBeadsData(prefix); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		}

		// Handle stealth mode setup
		if stealth {
			if err := setupStealthMode(!quiet); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting up stealth mode: %v\n", err)
				os.Exit(1)
			}

			// In stealth mode, skip git hooks and merge driver installation
			// since we handle it globally
			skipHooks = true
			skipMergeDriver = true
		}

		// Check BEADS_DB environment variable if --db flag not set
		// (PersistentPreRun doesn't run for init command)
		if dbPath == "" {
			if envDB := os.Getenv("BEADS_DB"); envDB != "" {
				dbPath = envDB
			}
		}

		// Determine prefix with precedence: flag > config > auto-detect from git > auto-detect from directory name
		if prefix == "" {
			// Try to get from config file
			prefix = config.GetString("issue-prefix")
		}

		// auto-detect prefix from first issue in JSONL file
		if prefix == "" {
			issueCount, jsonlPath, gitRef := checkGitForIssues()
			if issueCount > 0 {
				firstIssue, err := readFirstIssueFromGit(jsonlPath, gitRef)
				if firstIssue != nil && err == nil {
					prefix = utils.ExtractIssuePrefix(firstIssue.ID)
				}
			}
		}

		// auto-detect prefix from directory name
		if prefix == "" {
			// Auto-detect from directory name
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to get current directory: %v\n", err)
				os.Exit(1)
			}
			prefix = filepath.Base(cwd)
		}

		// Normalize prefix: strip trailing hyphens
		// The hyphen is added automatically during ID generation
		prefix = strings.TrimRight(prefix, "-")

		// Create database
		// Use global dbPath if set via --db flag or BEADS_DB env var, otherwise default to .beads/beads.db
		initDBPath := dbPath
		if initDBPath == "" {
			initDBPath = filepath.Join(".beads", beads.CanonicalDatabaseName)
		}

		// Migrate old database files if they exist
		if err := migrateOldDatabases(initDBPath, quiet); err != nil {
			fmt.Fprintf(os.Stderr, "Error during database migration: %v\n", err)
			os.Exit(1)
		}

		// Determine if we should create .beads/ directory in CWD
		// Only create it if the database will be stored there
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get current directory: %v\n", err)
			os.Exit(1)
		}

		// Prevent nested .beads directories
		// Check if current working directory is inside a .beads directory
		if strings.Contains(filepath.Clean(cwd), string(filepath.Separator)+".beads"+string(filepath.Separator)) ||
			strings.HasSuffix(filepath.Clean(cwd), string(filepath.Separator)+".beads") {
			fmt.Fprintf(os.Stderr, "Error: cannot initialize bd inside a .beads directory\n")
			fmt.Fprintf(os.Stderr, "Current directory: %s\n", cwd)
			fmt.Fprintf(os.Stderr, "Please run 'bd init' from outside the .beads directory.\n")
			os.Exit(1)
		}

		localBeadsDir := filepath.Join(cwd, ".beads")
		initDBDir := filepath.Dir(initDBPath)

		// Convert both to absolute paths for comparison
		localBeadsDirAbs, err := filepath.Abs(localBeadsDir)
		if err != nil {
			localBeadsDirAbs = filepath.Clean(localBeadsDir)
		}
		initDBDirAbs, err := filepath.Abs(initDBDir)
		if err != nil {
			initDBDirAbs = filepath.Clean(initDBDir)
		}

		useLocalBeads := filepath.Clean(initDBDirAbs) == filepath.Clean(localBeadsDirAbs)

		if useLocalBeads {
			// Create .beads directory
			if err := os.MkdirAll(localBeadsDir, 0750); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to create .beads directory: %v\n", err)
				os.Exit(1)
			}

			// Handle --no-db mode: create issues.jsonl file instead of database
			if noDb {
				// Create empty issues.jsonl file
				jsonlPath := filepath.Join(localBeadsDir, "issues.jsonl")
				if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
					// nolint:gosec // G306: JSONL file needs to be readable by other tools
					if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
						fmt.Fprintf(os.Stderr, "Error: failed to create issues.jsonl: %v\n", err)
						os.Exit(1)
					}
				}

				// Create metadata.json for --no-db mode
				cfg := configfile.DefaultConfig()
				if err := cfg.Save(localBeadsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create metadata.json: %v\n", err)
					// Non-fatal - continue anyway
				}

				// Create config.yaml with no-db: true
				if err := createConfigYaml(localBeadsDir, true); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create config.yaml: %v\n", err)
					// Non-fatal - continue anyway
				}

				// Create README.md
				if err := createReadme(localBeadsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create README.md: %v\n", err)
					// Non-fatal - continue anyway
				}

				if !quiet {
					green := color.New(color.FgGreen).SprintFunc()
					cyan := color.New(color.FgCyan).SprintFunc()

					fmt.Printf("\n%s bd initialized successfully in --no-db mode!\n\n", green("✓"))
					fmt.Printf("  Mode: %s\n", cyan("no-db (JSONL-only)"))
					fmt.Printf("  Issues file: %s\n", cyan(jsonlPath))
					fmt.Printf("  Issue prefix: %s\n", cyan(prefix))
					fmt.Printf("  Issues will be named: %s\n\n", cyan(prefix+"-1, "+prefix+"-2, ..."))
					fmt.Printf("Run %s to get started.\n\n", cyan("bd --no-db quickstart"))
				}
				return
			}

			// Create/update .gitignore in .beads directory (idempotent - always update to latest)
			gitignorePath := filepath.Join(localBeadsDir, ".gitignore")
			if err := os.WriteFile(gitignorePath, []byte(doctor.GitignoreTemplate), 0600); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create/update .gitignore: %v\n", err)
				// Non-fatal - continue anyway
			}
		}

		// Ensure parent directory exists for the database
		if err := os.MkdirAll(initDBDir, 0750); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create database directory %s: %v\n", initDBDir, err)
			os.Exit(1)
		}

		ctx := rootCtx
		store, err := sqlite.New(ctx, initDBPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create database: %v\n", err)
			os.Exit(1)
		}

		// === CONFIGURATION METADATA (Pattern A: Fatal) ===
		// Configuration metadata is essential for core functionality and must succeed.
		// These settings define fundamental behavior (issue IDs, sync workflow).
		// Failure here indicates a serious problem that prevents normal operation.

		// Set the issue prefix in config
		if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to set issue prefix: %v\n", err)
			_ = store.Close()
			os.Exit(1)
		}

		// Set sync.branch: use explicit --branch flag, or auto-detect current branch
		// This ensures bd sync --status works after bd init (bd-flil)
		if branch == "" && isGitRepo() {
			// Auto-detect current branch if not specified
			currentBranch, err := getGitBranch()
			if err == nil && currentBranch != "" {
				branch = currentBranch
			}
		}

		if branch != "" {
			if err := syncbranch.Set(ctx, store, branch); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to set sync branch: %v\n", err)
				_ = store.Close()
				os.Exit(1)
			}
			if !quiet {
				fmt.Printf("  Sync branch: %s\n", branch)
			}
		}

		// === TRACKING METADATA (Pattern B: Warn and Continue) ===
		// Tracking metadata enhances functionality (diagnostics, version checks, collision detection)
		// but the system works without it. Failures here degrade gracefully - we warn but continue.
		// Examples: bd_version enables upgrade warnings, repo_id/clone_id help with collision detection.

		// Store the bd version in metadata (for version mismatch detection)
		if err := store.SetMetadata(ctx, "bd_version", Version); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to store version metadata: %v\n", err)
			// Non-fatal - continue anyway
		}

		// Compute and store repository fingerprint
		repoID, err := beads.ComputeRepoID()
		if err != nil {
			if !quiet {
				fmt.Fprintf(os.Stderr, "Warning: could not compute repository ID: %v\n", err)
			}
		} else {
			if err := store.SetMetadata(ctx, "repo_id", repoID); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to set repo_id: %v\n", err)
			} else if !quiet {
				fmt.Printf("  Repository ID: %s\n", repoID[:8])
			}
		}

		// Store clone-specific ID
		cloneID, err := beads.GetCloneID()
		if err != nil {
			if !quiet {
				fmt.Fprintf(os.Stderr, "Warning: could not compute clone ID: %v\n", err)
			}
		} else {
			if err := store.SetMetadata(ctx, "clone_id", cloneID); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to set clone_id: %v\n", err)
			} else if !quiet {
				fmt.Printf("  Clone ID: %s\n", cloneID)
			}
		}

		// Create or preserve metadata.json for database metadata (bd-zai fix)
		if useLocalBeads {
			// First, check if metadata.json already exists
			existingCfg, err := configfile.Load(localBeadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to load existing metadata.json: %v\n", err)
			}

			var cfg *configfile.Config
			if existingCfg != nil {
				// Preserve existing config
				cfg = existingCfg
			} else {
				// Create new config, detecting JSONL filename from existing files
				cfg = configfile.DefaultConfig()
				// Check if beads.jsonl exists but issues.jsonl doesn't (legacy)
				issuesPath := filepath.Join(localBeadsDir, "issues.jsonl")
				beadsPath := filepath.Join(localBeadsDir, "beads.jsonl")
				if _, err := os.Stat(beadsPath); err == nil {
					if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
						cfg.JSONLExport = "beads.jsonl" // Legacy filename
					}
				}
			}
			if err := cfg.Save(localBeadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create metadata.json: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Create config.yaml template
			if err := createConfigYaml(localBeadsDir, false); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create config.yaml: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Create README.md
			if err := createReadme(localBeadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create README.md: %v\n", err)
				// Non-fatal - continue anyway
			}
		}

		// Check if git has existing issues to import (fresh clone scenario)
		issueCount, jsonlPath, gitRef := checkGitForIssues()
		if issueCount > 0 {
			if !quiet {
				fmt.Fprintf(os.Stderr, "\n✓ Database initialized. Found %d issues in git, importing...\n", issueCount)
			}

			if err := importFromGit(ctx, initDBPath, store, jsonlPath, gitRef); err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: auto-import failed: %v\n", err)
					fmt.Fprintf(os.Stderr, "Try manually: git show %s:%s | bd import -i /dev/stdin\n", gitRef, jsonlPath)
				}
				// Non-fatal - continue with empty database
			} else if !quiet {
				fmt.Fprintf(os.Stderr, "✓ Successfully imported %d issues from git.\n\n", issueCount)
			}
		}

		// Run contributor wizard if --contributor flag is set
		if contributor {
			if err := runContributorWizard(ctx, store); err != nil {
				fmt.Fprintf(os.Stderr, "Error running contributor wizard: %v\n", err)
				_ = store.Close()
				os.Exit(1)
			}
		}

		// Run team wizard if --team flag is set
		if team {
			if err := runTeamWizard(ctx, store); err != nil {
				fmt.Fprintf(os.Stderr, "Error running team wizard: %v\n", err)
				_ = store.Close()
				os.Exit(1)
			}
		}

		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close database: %v\n", err)
		}

		// Check if we're in a git repo and hooks aren't installed
		// Install by default unless --skip-hooks is passed
		if !skipHooks && isGitRepo() && !hooksInstalled() {
			if err := installGitHooks(); err != nil && !quiet {
				yellow := color.New(color.FgYellow).SprintFunc()
				fmt.Fprintf(os.Stderr, "\n%s Failed to install git hooks: %v\n", yellow("⚠"), err)
				fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", color.New(color.FgCyan).Sprint("bd doctor --fix"))
			}
		}

		// Check if we're in a git repo and merge driver isn't configured
		// Install by default unless --skip-merge-driver is passed
		if !skipMergeDriver && isGitRepo() && !mergeDriverInstalled() {
			if err := installMergeDriver(); err != nil && !quiet {
				yellow := color.New(color.FgYellow).SprintFunc()
				fmt.Fprintf(os.Stderr, "\n%s Failed to install merge driver: %v\n", yellow("⚠"), err)
				fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", color.New(color.FgCyan).Sprint("bd doctor --fix"))
			}
		}

		// Skip output if quiet mode
		if quiet {
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		cyan := color.New(color.FgCyan).SprintFunc()

		fmt.Printf("\n%s bd initialized successfully!\n\n", green("✓"))
		fmt.Printf("  Database: %s\n", cyan(initDBPath))
		fmt.Printf("  Issue prefix: %s\n", cyan(prefix))
		fmt.Printf("  Issues will be named: %s\n\n", cyan(prefix+"-1, "+prefix+"-2, ..."))
		fmt.Printf("Run %s to get started.\n\n", cyan("bd quickstart"))

		// Run bd doctor diagnostics to catch setup issues early (bd-zwtq)
		doctorResult := runDiagnostics(cwd)
		// Check if there are any warnings or errors (not just critical failures)
		hasIssues := false
		for _, check := range doctorResult.Checks {
			if check.Status != statusOK {
				hasIssues = true
				break
			}
		}
		if hasIssues {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("%s Setup incomplete. Some issues were detected:\n", yellow("⚠"))
			// Show just the warnings/errors, not all checks
			for _, check := range doctorResult.Checks {
				if check.Status != statusOK {
					fmt.Printf("  • %s: %s\n", check.Name, check.Message)
				}
			}
			fmt.Printf("\nRun %s to see details and fix these issues.\n\n", cyan("bd doctor --fix"))
		}
	},
}

func init() {
	initCmd.Flags().StringP("prefix", "p", "", "Issue prefix (default: current directory name)")
	initCmd.Flags().BoolP("quiet", "q", false, "Suppress output (quiet mode)")
	initCmd.Flags().StringP("branch", "b", "", "Git branch for beads commits (default: current branch)")
	initCmd.Flags().Bool("contributor", false, "Run OSS contributor setup wizard")
	initCmd.Flags().Bool("team", false, "Run team workflow setup wizard")
	initCmd.Flags().Bool("stealth", false, "Enable stealth mode: global gitattributes and gitignore, no local repo tracking")
	initCmd.Flags().Bool("skip-hooks", false, "Skip git hooks installation")
	initCmd.Flags().Bool("skip-merge-driver", false, "Skip git merge driver setup")
	initCmd.Flags().Bool("force", false, "Force re-initialization even if JSONL already has issues (may cause data loss)")
	rootCmd.AddCommand(initCmd)
}

// hooksInstalled checks if bd git hooks are installed
func hooksInstalled() bool {
	gitDir, err := getGitDir()
	if err != nil {
		return false
	}
	preCommit := filepath.Join(gitDir, "hooks", "pre-commit")
	postMerge := filepath.Join(gitDir, "hooks", "post-merge")

	// Check if both hooks exist
	_, err1 := os.Stat(preCommit)
	_, err2 := os.Stat(postMerge)

	if err1 != nil || err2 != nil {
		return false
	}

	// Verify they're bd hooks by checking for signature comment
	// #nosec G304 - controlled path from git directory
	preCommitContent, err := os.ReadFile(preCommit)
	if err != nil || !strings.Contains(string(preCommitContent), "bd (beads) pre-commit hook") {
		return false
	}

	// #nosec G304 - controlled path from git directory
	postMergeContent, err := os.ReadFile(postMerge)
	if err != nil || !strings.Contains(string(postMergeContent), "bd (beads) post-merge hook") {
		return false
	}

	// Verify hooks are executable
	preCommitInfo, err := os.Stat(preCommit)
	if err != nil {
		return false
	}
	if preCommitInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	postMergeInfo, err := os.Stat(postMerge)
	if err != nil {
		return false
	}
	if postMergeInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	return true
}

// hookInfo contains information about an existing hook
type hookInfo struct {
	name        string
	path        string
	exists      bool
	isBdHook    bool
	isPreCommit bool
	content     string
}

// detectExistingHooks scans for existing git hooks
func detectExistingHooks() []hookInfo {
	gitDir, err := getGitDir()
	if err != nil {
		return nil
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	hooks := []hookInfo{
		{name: "pre-commit", path: filepath.Join(hooksDir, "pre-commit")},
		{name: "post-merge", path: filepath.Join(hooksDir, "post-merge")},
		{name: "pre-push", path: filepath.Join(hooksDir, "pre-push")},
	}

	for i := range hooks {
		content, err := os.ReadFile(hooks[i].path)
		if err == nil {
			hooks[i].exists = true
			hooks[i].content = string(content)
			hooks[i].isBdHook = strings.Contains(hooks[i].content, "bd (beads)")
			// Only detect pre-commit framework if not a bd hook
			if !hooks[i].isBdHook {
				hooks[i].isPreCommit = strings.Contains(hooks[i].content, "pre-commit run") ||
					strings.Contains(hooks[i].content, ".pre-commit-config")
			}
		}
	}

	return hooks
}

// promptHookAction asks user what to do with existing hooks
func promptHookAction(existingHooks []hookInfo) string {
	yellow := color.New(color.FgYellow).SprintFunc()

	fmt.Printf("\n%s Found existing git hooks:\n", yellow("⚠"))
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hookType := "custom script"
			if hook.isPreCommit {
				hookType = "pre-commit framework"
			}
			fmt.Printf("  - %s (%s)\n", hook.name, hookType)
		}
	}

	fmt.Printf("\nHow should bd proceed?\n")
	fmt.Printf("  [1] Chain with existing hooks (recommended)\n")
	fmt.Printf("  [2] Overwrite existing hooks\n")
	fmt.Printf("  [3] Skip git hooks installation\n")
	fmt.Printf("Choice [1-3]: ")

	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.TrimSpace(response)

	return response
}

// installGitHooks installs git hooks inline (no external dependencies)
func installGitHooks() error {
	gitDir, err := getGitDir()
	if err != nil {
		return err
	}
	hooksDir := filepath.Join(gitDir, "hooks")

	// Ensure hooks directory exists
	if err := os.MkdirAll(hooksDir, 0750); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// Detect existing hooks
	existingHooks := detectExistingHooks()

	// Check if any non-bd hooks exist
	hasExistingHooks := false
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hasExistingHooks = true
			break
		}
	}

	// Determine installation mode
	chainHooks := false
	if hasExistingHooks {
		cyan := color.New(color.FgCyan).SprintFunc()
		choice := promptHookAction(existingHooks)
		switch choice {
		case "1", "":
			chainHooks = true
		case "2":
			// Overwrite mode - backup existing hooks
			for _, hook := range existingHooks {
				if hook.exists && !hook.isBdHook {
					timestamp := time.Now().Format("20060102-150405")
					backup := hook.path + ".backup-" + timestamp
					if err := os.Rename(hook.path, backup); err != nil {
						return fmt.Errorf("failed to backup %s: %w", hook.name, err)
					}
					fmt.Printf("  Backed up %s to %s\n", hook.name, filepath.Base(backup))
				}
			}
		case "3":
			fmt.Printf("Skipping git hooks installation.\n")
			fmt.Printf("You can install manually later with: %s\n", cyan("./examples/git-hooks/install.sh"))
			return nil
		default:
			return fmt.Errorf("invalid choice: %s", choice)
		}
	}

	// pre-commit hook
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	var preCommitContent string

	if chainHooks {
		// Find existing pre-commit hook
		var existingPreCommit string
		for _, hook := range existingHooks {
			if hook.name == "pre-commit" && hook.exists && !hook.isBdHook {
				// Move to .pre-commit-old
				oldPath := hook.path + ".old"
				if err := os.Rename(hook.path, oldPath); err != nil {
					return fmt.Errorf("failed to move existing pre-commit: %w", err)
				}
				existingPreCommit = oldPath
				break
			}
		}

		preCommitContent = `#!/bin/sh
#
# bd (beads) pre-commit hook (chained)
#
# This hook chains bd functionality with your existing pre-commit hook.

# Run existing hook first
if [ -x "` + existingPreCommit + `" ]; then
    "` + existingPreCommit + `" "$@"
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        exit $EXIT_CODE
    fi
fi

# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping pre-commit flush" >&2
    exit 0
fi

# Check if we're in a bd workspace
if [ ! -d .beads ]; then
    exit 0
fi

# Flush pending changes to JSONL
if ! bd sync --flush-only >/dev/null 2>&1; then
    echo "Error: Failed to flush bd changes to JSONL" >&2
    echo "Run 'bd sync --flush-only' manually to diagnose" >&2
    exit 1
fi

# If the JSONL file was modified, stage it
if [ -f .beads/issues.jsonl ]; then
    git add .beads/issues.jsonl 2>/dev/null || true
fi

exit 0
`
	} else {
		preCommitContent = `#!/bin/sh
#
# bd (beads) pre-commit hook
#
# This hook ensures that any pending bd issue changes are flushed to
# .beads/issues.jsonl before the commit is created, preventing the
# race condition where daemon auto-flush fires after the commit.

# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping pre-commit flush" >&2
    exit 0
fi

# Check if we're in a bd workspace
if [ ! -d .beads ]; then
    # Not a bd workspace, nothing to do
    exit 0
fi

# Flush pending changes to JSONL
# Use --flush-only to skip git operations (we're already in a git hook)
# Suppress output unless there's an error
if ! bd sync --flush-only >/dev/null 2>&1; then
    echo "Error: Failed to flush bd changes to JSONL" >&2
    echo "Run 'bd sync --flush-only' manually to diagnose" >&2
    exit 1
fi

# If the JSONL file was modified, stage it
if [ -f .beads/issues.jsonl ]; then
    git add .beads/issues.jsonl 2>/dev/null || true
fi

exit 0
`
	}

	// post-merge hook
	postMergePath := filepath.Join(hooksDir, "post-merge")
	var postMergeContent string

	if chainHooks {
		// Find existing post-merge hook
		var existingPostMerge string
		for _, hook := range existingHooks {
			if hook.name == "post-merge" && hook.exists && !hook.isBdHook {
				// Move to .post-merge-old
				oldPath := hook.path + ".old"
				if err := os.Rename(hook.path, oldPath); err != nil {
					return fmt.Errorf("failed to move existing post-merge: %w", err)
				}
				existingPostMerge = oldPath
				break
			}
		}

		postMergeContent = `#!/bin/sh
#
# bd (beads) post-merge hook (chained)
#
# This hook chains bd functionality with your existing post-merge hook.

# Run existing hook first
if [ -x "` + existingPostMerge + `" ]; then
    "` + existingPostMerge + `" "$@"
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        exit $EXIT_CODE
    fi
fi

# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping post-merge import" >&2
    exit 0
fi

# Check if we're in a bd workspace
if [ ! -d .beads ]; then
    exit 0
fi

# Check if issues.jsonl exists and was updated
if [ ! -f .beads/issues.jsonl ]; then
    exit 0
fi

# Import the updated JSONL
if ! bd import -i .beads/issues.jsonl >/dev/null 2>&1; then
    echo "Warning: Failed to import bd changes after merge" >&2
    echo "Run 'bd import -i .beads/issues.jsonl' manually to see the error" >&2
fi

exit 0
`
	} else {
		postMergeContent = `#!/bin/sh
#
# bd (beads) post-merge hook
#
# This hook imports updated issues from .beads/issues.jsonl after a
# git pull or merge, ensuring the database stays in sync with git.

# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping post-merge import" >&2
    exit 0
fi

# Check if we're in a bd workspace
if [ ! -d .beads ]; then
    # Not a bd workspace, nothing to do
    exit 0
fi

# Check if issues.jsonl exists and was updated
if [ ! -f .beads/issues.jsonl ]; then
    exit 0
fi

# Import the updated JSONL
# The auto-import feature should handle this, but we force it here
# to ensure immediate sync after merge
if ! bd import -i .beads/issues.jsonl >/dev/null 2>&1; then
    echo "Warning: Failed to import bd changes after merge" >&2
    echo "Run 'bd import -i .beads/issues.jsonl' manually to see the error" >&2
    # Don't fail the merge, just warn
fi

exit 0
`
	}

	// Write pre-commit hook (executable scripts need 0700)
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(preCommitPath, []byte(preCommitContent), 0700); err != nil {
		return fmt.Errorf("failed to write pre-commit hook: %w", err)
	}

	// Write post-merge hook (executable scripts need 0700)
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(postMergePath, []byte(postMergeContent), 0700); err != nil {
		return fmt.Errorf("failed to write post-merge hook: %w", err)
	}

	if chainHooks {
		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Chained bd hooks with existing hooks\n", green("✓"))
	}

	return nil
}

// mergeDriverInstalled checks if bd merge driver is configured correctly
func mergeDriverInstalled() bool {
	// Check git config for merge driver
	cmd := exec.Command("git", "config", "merge.beads.driver")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return false
	}

	// Check if using old invalid placeholders (%L/%R from versions <0.24.0)
	// Git only supports %O (base), %A (current), %B (other)
	driverConfig := strings.TrimSpace(string(output))
	if strings.Contains(driverConfig, "%L") || strings.Contains(driverConfig, "%R") {
		// Stale config with invalid placeholders - needs repair
		return false
	}

	// Check if .gitattributes has the merge driver configured
	gitattributesPath := ".gitattributes"
	content, err := os.ReadFile(gitattributesPath)
	if err != nil {
		return false
	}

	// Look for beads JSONL merge attribute (either canonical or legacy filename)
	hasCanonical := strings.Contains(string(content), ".beads/issues.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	hasLegacy := strings.Contains(string(content), ".beads/beads.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	return hasCanonical || hasLegacy
}

// installMergeDriver configures git to use bd merge for JSONL files
func installMergeDriver() error {
	// Configure git merge driver
	cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %A %O %A %B")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to configure git merge driver: %w\n%s", err, output)
	}

	cmd = exec.Command("git", "config", "merge.beads.name", "bd JSONL merge driver")
	if output, err := cmd.CombinedOutput(); err != nil {
		// Non-fatal, the name is just descriptive
		fmt.Fprintf(os.Stderr, "Warning: failed to set merge driver name: %v\n%s", err, output)
	}

	// Create or update .gitattributes
	gitattributesPath := ".gitattributes"

	// Read existing .gitattributes if it exists
	var existingContent string
	content, err := os.ReadFile(gitattributesPath)
	if err == nil {
		existingContent = string(content)
	}

	// Check if beads merge driver is already configured
	// Check for either pattern (issues.jsonl is canonical, beads.jsonl is legacy)
	hasBeadsMerge := (strings.Contains(existingContent, ".beads/issues.jsonl") ||
		strings.Contains(existingContent, ".beads/beads.jsonl")) &&
		strings.Contains(existingContent, "merge=beads")

	if !hasBeadsMerge {
		// Append beads merge driver configuration (issues.jsonl is canonical)
		beadsMergeAttr := "\n# Use bd merge for beads JSONL files\n.beads/issues.jsonl merge=beads\n"

		newContent := existingContent
		if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
			newContent += "\n"
		}
		newContent += beadsMergeAttr

		// Write updated .gitattributes (0644 is standard for .gitattributes)
		// #nosec G306 - .gitattributes needs to be readable
		if err := os.WriteFile(gitattributesPath, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("failed to update .gitattributes: %w", err)
		}
	}

	return nil
}

// migrateOldDatabases detects and migrates old database files to beads.db
func migrateOldDatabases(targetPath string, quiet bool) error {
	targetDir := filepath.Dir(targetPath)
	targetName := filepath.Base(targetPath)

	// If target already exists, no migration needed
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	}

	// Create .beads directory if it doesn't exist
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return fmt.Errorf("failed to create .beads directory: %w", err)
	}

	// Look for existing .db files in the .beads directory
	pattern := filepath.Join(targetDir, "*.db")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to search for existing databases: %w", err)
	}

	// Filter out the target file name and any backup files
	var oldDBs []string
	for _, match := range matches {
		baseName := filepath.Base(match)
		if baseName != targetName && !strings.HasSuffix(baseName, ".backup.db") {
			oldDBs = append(oldDBs, match)
		}
	}

	if len(oldDBs) == 0 {
		// No old databases to migrate
		return nil
	}

	if len(oldDBs) > 1 {
		// Multiple databases found - ambiguous, require manual intervention
		return fmt.Errorf("multiple database files found in %s: %v\nPlease manually rename the correct database to %s and remove others",
			targetDir, oldDBs, targetName)
	}

	// Migrate the single old database
	oldDB := oldDBs[0]
	if !quiet {
		fmt.Fprintf(os.Stderr, "→ Migrating database: %s → %s\n", filepath.Base(oldDB), targetName)
	}

	// Rename the old database to the new canonical name
	if err := os.Rename(oldDB, targetPath); err != nil {
		return fmt.Errorf("failed to migrate database %s to %s: %w", oldDB, targetPath, err)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "✓ Database migration complete\n\n")
	}

	return nil
}

// createConfigYaml creates the config.yaml template in the specified directory
func createConfigYaml(beadsDir string, noDbMode bool) error {
	configYamlPath := filepath.Join(beadsDir, "config.yaml")

	// Skip if already exists
	if _, err := os.Stat(configYamlPath); err == nil {
		return nil
	}

	noDbLine := "# no-db: false"
	if noDbMode {
		noDbLine = "no-db: true  # JSONL-only mode, no SQLite database"
	}

	configYamlTemplate := fmt.Sprintf(`# Beads Configuration File
# This file configures default behavior for all bd commands in this repository
# All settings can also be set via environment variables (BD_* prefix)
# or overridden with command-line flags

# Issue prefix for this repository (used by bd init)
# If not set, bd init will auto-detect from directory name
# Example: issue-prefix: "myproject" creates issues like "myproject-1", "myproject-2", etc.
# issue-prefix: ""

# Use no-db mode: load from JSONL, no SQLite, write back after each command
# When true, bd will use .beads/issues.jsonl as the source of truth
# instead of SQLite database
%s

# Disable daemon for RPC communication (forces direct database access)
# no-daemon: false

# Disable auto-flush of database to JSONL after mutations
# no-auto-flush: false

# Disable auto-import from JSONL when it's newer than database
# no-auto-import: false

# Enable JSON output by default
# json: false

# Default actor for audit trails (overridden by BD_ACTOR or --actor)
# actor: ""

# Path to database (overridden by BEADS_DB or --db)
# db: ""

# Auto-start daemon if not running (can also use BEADS_AUTO_START_DAEMON)
# auto-start-daemon: true

# Debounce interval for auto-flush (can also use BEADS_FLUSH_DEBOUNCE)
# flush-debounce: "5s"

# Git branch for beads commits (bd sync will commit to this branch)
# IMPORTANT: Set this for team projects so all clones use the same sync branch.
# This setting persists across clones (unlike database config which is gitignored).
# Can also use BEADS_SYNC_BRANCH env var for local override.
# If not set, bd sync will require you to run 'bd config set sync.branch <branch>'.
# sync-branch: "beads-sync"

# Multi-repo configuration (experimental - bd-307)
# Allows hydrating from multiple repositories and routing writes to the correct JSONL
# repos:
#   primary: "."  # Primary repo (where this database lives)
#   additional:   # Additional repos to hydrate from (read-only)
#     - ~/beads-planning  # Personal planning repo
#     - ~/work-planning   # Work planning repo

# Integration settings (access with 'bd config get/set')
# These are stored in the database, not in this file:
# - jira.url
# - jira.project
# - linear.url
# - linear.api-key
# - github.org
# - github.repo
`, noDbLine)

	if err := os.WriteFile(configYamlPath, []byte(configYamlTemplate), 0600); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return nil
}

// createReadme creates the README.md file in the .beads directory
func createReadme(beadsDir string) error {
	readmePath := filepath.Join(beadsDir, "README.md")

	// Skip if already exists
	if _, err := os.Stat(readmePath); err == nil {
		return nil
	}

	readmeTemplate := `# Beads - AI-Native Issue Tracking

Welcome to Beads! This repository uses **Beads** for issue tracking - a modern, AI-native tool designed to live directly in your codebase alongside your code.

## What is Beads?

Beads is issue tracking that lives in your repo, making it perfect for AI coding agents and developers who want their issues close to their code. No web UI required - everything works through the CLI and integrates seamlessly with git.

**Learn more:** [github.com/steveyegge/beads](https://github.com/steveyegge/beads)

## Quick Start

### Essential Commands

` + "```bash" + `
# Create new issues
bd create "Add user authentication"

# View all issues
bd list

# View issue details
bd show <issue-id>

# Update issue status
bd update <issue-id> --status in_progress
bd update <issue-id> --status done

# Sync with git remote
bd sync
` + "```" + `

### Working with Issues

Issues in Beads are:
- **Git-native**: Stored in ` + "`.beads/issues.jsonl`" + ` and synced like code
- **AI-friendly**: CLI-first design works perfectly with AI coding agents
- **Branch-aware**: Issues can follow your branch workflow
- **Always in sync**: Auto-syncs with your commits

## Why Beads?

✨ **AI-Native Design**
- Built specifically for AI-assisted development workflows
- CLI-first interface works seamlessly with AI coding agents
- No context switching to web UIs

🚀 **Developer Focused**
- Issues live in your repo, right next to your code
- Works offline, syncs when you push
- Fast, lightweight, and stays out of your way

🔧 **Git Integration**
- Automatic sync with git commits
- Branch-aware issue tracking
- Intelligent JSONL merge resolution

## Get Started with Beads

Try Beads in your own projects:

` + "```bash" + `
# Install Beads
curl -sSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

# Initialize in your repo
bd init

# Create your first issue
bd create "Try out Beads"
` + "```" + `

## Learn More

- **Documentation**: [github.com/steveyegge/beads/docs](https://github.com/steveyegge/beads/tree/main/docs)
- **Quick Start Guide**: Run ` + "`bd quickstart`" + `
- **Examples**: [github.com/steveyegge/beads/examples](https://github.com/steveyegge/beads/tree/main/examples)

---

*Beads: Issue tracking that moves at the speed of thought* ⚡
`

	// Write README.md (0644 is standard for markdown files)
	// #nosec G306 - README needs to be readable
	if err := os.WriteFile(readmePath, []byte(readmeTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %w", err)
	}

	return nil
}

// readFirstIssueFromJSONL reads the first issue from a JSONL file
func readFirstIssueFromJSONL(path string) (*types.Issue, error) {
	// #nosec G304 -- helper reads JSONL file chosen by current bd command
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// skip empty lines
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err == nil {
			return &issue, nil
		} else {
			// Skip malformed lines with warning
			fmt.Fprintf(os.Stderr, "Warning: skipping malformed JSONL line %d: %v\n", lineNum, err)
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading JSONL file: %w", err)
	}

	return nil, nil
}

// readFirstIssueFromGit reads the first issue from a git ref (bd-0is: supports sync-branch)
func readFirstIssueFromGit(jsonlPath, gitRef string) (*types.Issue, error) {
	// Get content from git (use ToSlash for Windows compatibility)
	gitPath := filepath.ToSlash(jsonlPath)
	cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", gitRef, gitPath)) // #nosec G204
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read from git: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		// skip empty lines
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err == nil {
			return &issue, nil
		}
		// Skip malformed lines silently (called during auto-detection)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning git content: %w", err)
	}

	return nil, nil
}

// setupStealthMode configures global git settings for stealth operation
func setupStealthMode(verbose bool) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	// Setup global gitignore
	if err := setupGlobalGitIgnore(homeDir, verbose); err != nil {
		return fmt.Errorf("failed to setup global gitignore: %w", err)
	}

	// Setup claude settings
	if err := setupClaudeSettings(verbose); err != nil {
		return fmt.Errorf("failed to setup claude settings: %w", err)
	}

	if verbose {
		green := color.New(color.FgGreen).SprintFunc()
		cyan := color.New(color.FgCyan).SprintFunc()
		fmt.Printf("\n%s Stealth mode configured successfully!\n\n", green("✓"))
		fmt.Printf("  Global gitignore: %s\n", cyan(".beads/ and .claude/settings.local.json ignored"))
		fmt.Printf("  Claude settings: %s\n\n", cyan("configured with bd onboard instruction"))
		fmt.Printf("Your beads setup is now %s - other repo collaborators won't see any beads-related files.\n\n", cyan("invisible"))
	}

	return nil
}

// setupGlobalGitIgnore configures global gitignore to ignore beads and claude files
func setupGlobalGitIgnore(homeDir string, verbose bool) error {
	// Check if user already has a global gitignore file configured
	cmd := exec.Command("git", "config", "--global", "core.excludesfile")
	output, err := cmd.Output()

	var ignorePath string

	if err == nil && len(output) > 0 {
		// User has already configured a global gitignore file, use it
		ignorePath = strings.TrimSpace(string(output))

		// Expand tilde if present (git config may return ~/... which Go doesn't expand)
		if strings.HasPrefix(ignorePath, "~/") {
			ignorePath = filepath.Join(homeDir, ignorePath[2:])
		} else if ignorePath == "~" {
			ignorePath = homeDir
		}

		if verbose {
			fmt.Printf("Using existing configured global gitignore file: %s\n", ignorePath)
		}
	} else {
		// No global gitignore file configured, check if standard location exists
		configDir := filepath.Join(homeDir, ".config", "git")
		standardIgnorePath := filepath.Join(configDir, "ignore")

		if _, err := os.Stat(standardIgnorePath); err == nil {
			// Standard global gitignore file exists, use it
			// No need to set git config - git automatically uses this standard location
			ignorePath = standardIgnorePath
			if verbose {
				fmt.Printf("Using existing global gitignore file: %s\n", ignorePath)
			}
		} else {
			// No global gitignore file exists, create one in standard location
			// No need to set git config - git automatically uses this standard location
			ignorePath = standardIgnorePath

			// Ensure config directory exists
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("failed to create git config directory: %w", err)
			}

			if verbose {
				fmt.Printf("Creating new global gitignore file: %s\n", ignorePath)
			}
		}
	}

	// Read existing ignore file if it exists
	var existingContent string
	// #nosec G304 - user config path
	if content, err := os.ReadFile(ignorePath); err == nil {
		existingContent = string(content)
	}

	// Check if beads patterns already exist
	beadsPattern := "**/.beads/"
	claudePattern := "**/.claude/settings.local.json"

	hasBeads := strings.Contains(existingContent, beadsPattern)
	hasClaude := strings.Contains(existingContent, claudePattern)

	if hasBeads && hasClaude {
		if verbose {
			fmt.Printf("Global gitignore already configured for stealth mode\n")
		}
		return nil
	}

	// Append missing patterns
	newContent := existingContent
	if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
		newContent += "\n"
	}

	if !hasBeads || !hasClaude {
		newContent += "\n# Beads stealth mode configuration (added by bd init --stealth)\n"
	}

	if !hasBeads {
		newContent += beadsPattern + "\n"
	}
	if !hasClaude {
		newContent += claudePattern + "\n"
	}

	// Write the updated ignore file
	// #nosec G306 - config file needs 0644
	if err := os.WriteFile(ignorePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write global gitignore: %w", err)
	}

	if verbose {
		fmt.Printf("Configured global gitignore for stealth mode\n")
	}

	return nil
}

// checkExistingBeadsData checks for existing database files
// and returns an error if found (safety guard for bd-emg)
//
// Note: This only blocks when a database already exists (workspace is initialized).
// Fresh clones with JSONL but no database are allowed - init will create the database
// and import from JSONL automatically (bd-4h9: fixes circular dependency with doctor --fix).
func checkExistingBeadsData(prefix string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil // Can't determine CWD, allow init to proceed
	}

	beadsDir := filepath.Join(cwd, ".beads")

	// Check if .beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil // No .beads directory, safe to init
	}

	// Check for existing database file
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if _, err := os.Stat(dbPath); err == nil {
		yellow := color.New(color.FgYellow).SprintFunc()
		cyan := color.New(color.FgCyan).SprintFunc()

		return fmt.Errorf(`
%s Found existing database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

To completely reinitialize (data loss warning):
  rm -rf .beads && bd init --prefix %s

Aborting.`, yellow("⚠"), dbPath, cyan("bd list"), prefix)
	}

	// Fresh clones (JSONL exists but no database) are allowed - init will
	// create the database and import from JSONL automatically.
	// This fixes the circular dependency where init told users to run
	// "bd doctor --fix" but doctor couldn't create a database (bd-4h9).

	return nil // No database found, safe to init
}



// setupClaudeSettings creates or updates .claude/settings.local.json with onboard instruction
func setupClaudeSettings(verbose bool) error {
	claudeDir := ".claude"
	settingsPath := filepath.Join(claudeDir, "settings.local.json")

	// Create .claude directory if it doesn't exist
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Check if settings.local.json already exists
	var existingSettings map[string]interface{}
	// #nosec G304 - user config path
	if content, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(content, &existingSettings); err != nil {
			// Don't silently overwrite - the user has a file with invalid JSON
			// that likely contains important settings they don't want to lose
			return fmt.Errorf("existing %s contains invalid JSON: %w\nPlease fix the JSON syntax manually before running bd init", settingsPath, err)
		}
	} else if !os.IsNotExist(err) {
		// File exists but couldn't be read (permissions issue, etc.)
		return fmt.Errorf("failed to read existing %s: %w", settingsPath, err)
	} else {
		// File doesn't exist - create new empty settings
		existingSettings = make(map[string]interface{})
	}

	// Add or update the prompt with onboard instruction
	onboardPrompt := "Before starting any work, run 'bd onboard' to understand the current project state and available issues."

	// Check if prompt already contains onboard instruction
	if promptValue, exists := existingSettings["prompt"]; exists {
		if promptStr, ok := promptValue.(string); ok {
			if strings.Contains(promptStr, "bd onboard") {
				if verbose {
					fmt.Printf("Claude settings already configured with bd onboard instruction\n")
				}
				return nil
			}
			// Update existing prompt to include onboard instruction
			existingSettings["prompt"] = promptStr + "\n\n" + onboardPrompt
		} else {
			// Existing prompt is not a string, replace it
			existingSettings["prompt"] = onboardPrompt
		}
	} else {
		// Add new prompt with onboard instruction
		existingSettings["prompt"] = onboardPrompt
	}

	// Write updated settings
	updatedContent, err := json.MarshalIndent(existingSettings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings JSON: %w", err)
	}

	// #nosec G306 - config file needs 0644
	if err := os.WriteFile(settingsPath, updatedContent, 0644); err != nil {
		return fmt.Errorf("failed to write claude settings: %w", err)
	}

	if verbose {
		fmt.Printf("Configured Claude settings with bd onboard instruction\n")
	}

	return nil
}
