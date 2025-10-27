package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize bd in the current directory",
	Long: `Initialize bd in the current directory by creating a .beads/ directory
and database file. Optionally specify a custom issue prefix.

With --no-db: creates .beads/ directory and issues.jsonl file instead of SQLite database.`,
	Run: func(cmd *cobra.Command, _ []string) {
		prefix, _ := cmd.Flags().GetString("prefix")
		quiet, _ := cmd.Flags().GetBool("quiet")

		// Initialize config (PersistentPreRun doesn't run for init command)
		if err := config.Initialize(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
			// Non-fatal - continue with defaults
		}

		// Check BEADS_DB environment variable if --db flag not set
		// (PersistentPreRun doesn't run for init command)
		if dbPath == "" {
			if envDB := os.Getenv("BEADS_DB"); envDB != "" {
				dbPath = envDB
			}
		}

		// Determine prefix with precedence: flag > config > auto-detect
		if prefix == "" {
			// Try to get from config file
			prefix = config.GetString("issue-prefix")
		}

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
				if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
					fmt.Fprintf(os.Stderr, "Error: failed to create issues.jsonl: %v\n", err)
					os.Exit(1)
				}
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

		// Create .gitignore in .beads directory
		gitignorePath := filepath.Join(localBeadsDir, ".gitignore")
		gitignoreContent := `# SQLite databases
*.db
*.db-journal
*.db-wal
*.db-shm

# Daemon runtime files
daemon.lock
daemon.log
daemon.pid
bd.sock

# Legacy database files
db.sqlite
bd.db

# Keep JSONL exports and config (source of truth for git)
!*.jsonl
!config.json
`
			if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0600); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create .gitignore: %v\n", err)
				// Non-fatal - continue anyway
			}
		}
	
		// Ensure parent directory exists for the database
		if err := os.MkdirAll(initDBDir, 0750); err != nil {
		 fmt.Fprintf(os.Stderr, "Error: failed to create database directory %s: %v\n", initDBDir, err)
		 os.Exit(1)
		}
		
		store, err := sqlite.New(initDBPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create database: %v\n", err)
			os.Exit(1)
		}

		// Set the issue prefix in config
		ctx := context.Background()
		if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to set issue prefix: %v\n", err)
		_ = store.Close()
		os.Exit(1)
		}

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

		// Create config.json for explicit configuration
		if useLocalBeads {
			cfg := configfile.DefaultConfig(Version)
			if err := cfg.Save(localBeadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create config.json: %v\n", err)
				// Non-fatal - continue anyway
			}
		}

		// Check if git has existing issues to import (fresh clone scenario)
		issueCount, jsonlPath := checkGitForIssues()
		if issueCount > 0 {
		if !quiet {
		fmt.Fprintf(os.Stderr, "\n✓ Database initialized. Found %d issues in git, importing...\n", issueCount)
		}
		
		if err := importFromGit(ctx, initDBPath, store, jsonlPath); err != nil {
		if !quiet {
		fmt.Fprintf(os.Stderr, "Warning: auto-import failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "Try manually: git show HEAD:%s | bd import -i /dev/stdin\n", jsonlPath)
		}
		// Non-fatal - continue with empty database
		} else if !quiet {
		fmt.Fprintf(os.Stderr, "✓ Successfully imported %d issues from git.\n\n", issueCount)
		}
}

if err := store.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to close database: %v\n", err)
}

// Check if we're in a git repo and hooks aren't installed
// Do this BEFORE quiet mode return so hooks get installed for agents
if isGitRepo() && !hooksInstalled() {
	if quiet {
		// Auto-install hooks silently in quiet mode (best default for agents)
		_ = installGitHooks() // Ignore errors in quiet mode
	} else {
		// Defer to interactive prompt below
	}
}

// Skip output if quiet mode
if quiet {
		return
}

		green := color.New(color.FgGreen).SprintFunc()
		cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()

		fmt.Printf("\n%s bd initialized successfully!\n\n", green("✓"))
		fmt.Printf("  Database: %s\n", cyan(initDBPath))
		fmt.Printf("  Issue prefix: %s\n", cyan(prefix))
		fmt.Printf("  Issues will be named: %s\n\n", cyan(prefix+"-1, "+prefix+"-2, ..."))
	
	// Interactive git hooks prompt for humans
	if isGitRepo() && !hooksInstalled() {
		fmt.Printf("%s Git hooks not installed\n", yellow("⚠"))
		fmt.Printf("  Install git hooks to prevent race conditions between commits and auto-flush.\n")
		fmt.Printf("  Run: %s\n\n", cyan("./examples/git-hooks/install.sh"))
		
		// Prompt to install
		fmt.Printf("Install git hooks now? [Y/n] ")
		var response string
		_, _ = fmt.Scanln(&response) // ignore EOF on empty input
		response = strings.ToLower(strings.TrimSpace(response))
		
		if response == "" || response == "y" || response == "yes" {
			if err := installGitHooks(); err != nil {
				fmt.Fprintf(os.Stderr, "Error installing hooks: %v\n", err)
				fmt.Printf("You can install manually with: %s\n\n", cyan("./examples/git-hooks/install.sh"))
			} else {
				fmt.Printf("%s Git hooks installed successfully!\n\n", green("✓"))
			}
		}
	}
	
	fmt.Printf("Run %s to get started.\n\n", cyan("bd quickstart"))
	},
}

func init() {
	initCmd.Flags().StringP("prefix", "p", "", "Issue prefix (default: current directory name)")
	initCmd.Flags().BoolP("quiet", "q", false, "Suppress output (quiet mode)")
	rootCmd.AddCommand(initCmd)
}

// hooksInstalled checks if bd git hooks are installed
func hooksInstalled() bool {
	preCommit := filepath.Join(".git", "hooks", "pre-commit")
	postMerge := filepath.Join(".git", "hooks", "post-merge")
	
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
	
	return true
}

// installGitHooks installs git hooks inline (no external dependencies)
func installGitHooks() error {
	hooksDir := filepath.Join(".git", "hooks")
	
	// Ensure hooks directory exists
	if err := os.MkdirAll(hooksDir, 0750); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}
	
	// pre-commit hook
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	preCommitContent := `#!/bin/sh
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
	
	// post-merge hook
	postMergePath := filepath.Join(hooksDir, "post-merge")
	postMergeContent := `#!/bin/sh
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
if ! bd import -i .beads/issues.jsonl --resolve-collisions >/dev/null 2>&1; then
    echo "Warning: Failed to import bd changes after merge" >&2
    echo "Run 'bd import -i .beads/issues.jsonl --resolve-collisions' manually" >&2
    # Don't fail the merge, just warn
fi

exit 0
`
	
	// Backup existing hooks if present
	for _, hookPath := range []string{preCommitPath, postMergePath} {
		if _, err := os.Stat(hookPath); err == nil {
			// Read existing hook to check if it's already a bd hook
			// #nosec G304 - controlled path from git directory
			content, err := os.ReadFile(hookPath)
			if err == nil && strings.Contains(string(content), "bd (beads)") {
				// Already a bd hook, skip backup
				continue
			}
			
			// Backup non-bd hook
			backup := hookPath + ".backup"
			if err := os.Rename(hookPath, backup); err != nil {
				return fmt.Errorf("failed to backup existing hook: %w", err)
			}
		}
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
