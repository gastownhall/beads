package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"golang.org/x/term"
)

var initCmd = &cobra.Command{
	Use:     "init",
	GroupID: "setup",
	Short:   "Initialize bd in the current directory",
	Long: `Initialize bd in the current directory by creating a .beads/ directory
and database file. Optionally specify a custom issue prefix.

With --from-jsonl: imports from the current .beads/issues.jsonl file on disk instead
of scanning git history. Use this after manual JSONL cleanup
to prevent deleted issues from reappearing during re-initialization.

With --stealth: configures per-repository git settings for invisible beads usage:
  • .git/info/exclude to prevent beads files from being committed
  • Claude Code settings with bd onboard instruction
  Perfect for personal use without affecting repo collaborators.

Beads requires a running dolt sql-server for database operations. If a server is detected
on port 3307 or 3306, it is used automatically. Set connection details with --server-host,
--server-port, and --server-user. Password should be set via BEADS_DOLT_PASSWORD
environment variable.`,
	Run: func(cmd *cobra.Command, _ []string) {
		prefix, _ := cmd.Flags().GetString("prefix")
		quiet, _ := cmd.Flags().GetBool("quiet")
		contributor, _ := cmd.Flags().GetBool("contributor")
		team, _ := cmd.Flags().GetBool("team")
		stealth, _ := cmd.Flags().GetBool("stealth")
		skipHooks, _ := cmd.Flags().GetBool("skip-hooks")
		force, _ := cmd.Flags().GetBool("force")
		// fromJSONL flag is accepted but no longer used for SQLite import;
		// Dolt bootstraps from issues.jsonl automatically on first open.
		_, _ = cmd.Flags().GetBool("from-jsonl")

		// Dolt server connection flags
		_, _ = cmd.Flags().GetBool("server") // no-op, kept for backward compatibility
		serverHost, _ := cmd.Flags().GetString("server-host")
		serverPort, _ := cmd.Flags().GetInt("server-port")
		serverUser, _ := cmd.Flags().GetString("server-user")

		// Dolt is the only supported backend
		backend := configfile.BackendDolt

		// Initialize config (PersistentPreRun doesn't run for init command)
		if err := config.Initialize(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
			// Non-fatal - continue with defaults
		}

		// Safety guard: check for existing JSONL with issues
		// This prevents accidental re-initialization in fresh clones
		if !force {
			if err := checkExistingBeadsData(prefix); err != nil {
				FatalError("%v", err)
			}
		}

		// Handle stealth mode setup
		if stealth {
			if err := setupStealthMode(!quiet); err != nil {
				FatalError("setting up stealth mode: %v", err)
			}

			// In stealth mode, skip git hooks installation
			// since we handle it globally
			skipHooks = true
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

		// auto-detect prefix from directory name
		if prefix == "" {
			// Auto-detect from directory name
			cwd, err := os.Getwd()
			if err != nil {
				FatalError("failed to get current directory: %v", err)
			}
			prefix = filepath.Base(cwd)
		}

		// Normalize prefix: strip trailing hyphens
		// The hyphen is added automatically during ID generation
		prefix = strings.TrimRight(prefix, "-")

		// Determine beadsDir first (used for all storage path calculations).
		// BEADS_DIR takes precedence, otherwise use CWD/.beads (with redirect support).
		// This must be computed BEFORE initDBPath to ensure consistent path resolution
		// (avoiding macOS /var -> /private/var symlink issues when directory creation
		// happens between path computations).
		var beadsDirForInit string
		if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
			beadsDirForInit = utils.CanonicalizePath(envBeadsDir)
		} else {
			localBeadsDir := filepath.Join(".", ".beads")
			beadsDirForInit = beads.FollowRedirect(localBeadsDir)
		}

		// Determine storage path.
		//
		// Precedence: --db > BEADS_DIR > default (.beads/dolt)
		// If there's a redirect file, use the redirect target (GH#bd-0qel)
		initDBPath := dbPath
		if initDBPath == "" {
			// Dolt backend: use computed beadsDirForInit
			initDBPath = filepath.Join(beadsDirForInit, "dolt")
		}

		// Determine if we should create .beads/ directory in CWD or main repo root
		// For worktrees, .beads should always be in the main repository root
		cwd, err := os.Getwd()
		if err != nil {
			FatalError("failed to get current directory: %v", err)
		}

		// Check if we're in a git worktree
		// Guard with isGitRepo() check first - on Windows, git commands may hang
		// when run outside a git repository (GH#727)
		isWorktree := false
		if isGitRepo() {
			isWorktree = git.IsWorktree()
		}

		// Prevent initialization from within a worktree
		if isWorktree {
			mainRepoRoot, err := git.GetMainRepoRoot()
			if err != nil {
				FatalError("failed to get main repository root: %v", err)
			}

			fmt.Fprintf(os.Stderr, "Error: cannot run 'bd init' from within a git worktree\n\n")
			fmt.Fprintf(os.Stderr, "Git worktrees share the .beads database from the main repository.\n")
			fmt.Fprintf(os.Stderr, "To fix this:\n\n")
			fmt.Fprintf(os.Stderr, "  1. Initialize beads in the main repository:\n")
			fmt.Fprintf(os.Stderr, "     cd %s\n", mainRepoRoot)
			fmt.Fprintf(os.Stderr, "     bd init\n\n")
			fmt.Fprintf(os.Stderr, "  2. Then create worktrees with beads support:\n")
			fmt.Fprintf(os.Stderr, "     bd worktree create <path> --branch <branch-name>\n\n")
			fmt.Fprintf(os.Stderr, "For more information, see: https://github.com/steveyegge/beads/blob/main/docs/WORKTREES.md\n")
			os.Exit(1)
		}

		// Use the beadsDir computed earlier (before any directory creation)
		// to ensure consistent path representation.
		beadsDir := beadsDirForInit

		// Prevent nested .beads directories
		// Check if current working directory is inside a .beads directory
		if strings.Contains(filepath.Clean(cwd), string(filepath.Separator)+".beads"+string(filepath.Separator)) ||
			strings.HasSuffix(filepath.Clean(cwd), string(filepath.Separator)+".beads") {
			fmt.Fprintf(os.Stderr, "Error: cannot initialize bd inside a .beads directory\n")
			fmt.Fprintf(os.Stderr, "Current directory: %s\n", cwd)
			fmt.Fprintf(os.Stderr, "Please run 'bd init' from outside the .beads directory.\n")
			os.Exit(1)
		}

		initDBDir := filepath.Dir(initDBPath)

		// Convert both to absolute paths for comparison
		beadsDirAbs, err := filepath.Abs(beadsDir)
		if err != nil {
			beadsDirAbs = filepath.Clean(beadsDir)
		}
		initDBDirAbs, err := filepath.Abs(initDBDir)
		if err != nil {
			initDBDirAbs = filepath.Clean(initDBDir)
		}

		useLocalBeads := filepath.Clean(initDBDirAbs) == filepath.Clean(beadsDirAbs)

		if useLocalBeads {
			// Create .beads directory
			if err := os.MkdirAll(beadsDir, 0750); err != nil {
				FatalError("failed to create .beads directory: %v", err)
			}

			// Create/update .gitignore in .beads directory (only if missing or outdated)
			gitignorePath := filepath.Join(beadsDir, ".gitignore")
			check := doctor.CheckGitignore()
			if check.Status != "ok" {
				if err := os.WriteFile(gitignorePath, []byte(doctor.GitignoreTemplate), 0600); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create/update .gitignore: %v\n", err)
					// Non-fatal - continue anyway
				}
			}

			// Ensure interactions.jsonl exists (append-only agent audit log)
			interactionsPath := filepath.Join(beadsDir, "interactions.jsonl")
			if _, err := os.Stat(interactionsPath); os.IsNotExist(err) {
				// nolint:gosec // G306: JSONL file needs to be readable by other tools
				if err := os.WriteFile(interactionsPath, []byte{}, 0644); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create interactions.jsonl: %v\n", err)
					// Non-fatal - continue anyway
				}
			}
		}

		// Ensure git is initialized — bd requires git for role config, sync branches,
		// hooks, worktrees, and fingerprint computation. git init is idempotent so
		// safe to call even if already in a git repo.
		if !isGitRepo() {
			gitInitCmd := exec.Command("git", "init")
			if output, err := gitInitCmd.CombinedOutput(); err != nil {
				FatalError("failed to initialize git repository: %v\n%s", err, output)
			}
			if !quiet {
				fmt.Printf("  %s Initialized git repository\n", ui.RenderPass("✓"))
			}
		}

		// Ensure parent directory exists for the storage backend (.beads/dolt).
		if err := os.MkdirAll(initDBDir, 0750); err != nil {
			FatalError("failed to create storage directory %s: %v", initDBDir, err)
		}

		ctx := rootCtx

		// Create Dolt storage backend
		storagePath := filepath.Join(beadsDir, "dolt")
		// Use prefix-based database name to avoid cross-rig contamination (bd-u8rda)
		dbName := "beads"
		if prefix != "" {
			dbName = "beads_" + prefix
		}
		// Build config. Beads always uses dolt sql-server.
		doltCfg := &dolt.Config{
			Path:     storagePath,
			Database: dbName,
		}
		if serverHost != "" {
			doltCfg.ServerHost = serverHost
		}
		if serverPort != 0 {
			doltCfg.ServerPort = serverPort
		}
		if serverUser != "" {
			doltCfg.ServerUser = serverUser
		}

		var store *dolt.DoltStore
		store, err = dolt.New(ctx, doltCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to connect to dolt server: %v\n", err)
			fmt.Fprintf(os.Stderr, "\nBeads requires a running dolt sql-server. Start one with:\n")
			fmt.Fprintf(os.Stderr, "  gt dolt start    (if using Gas Town)\n")
			fmt.Fprintf(os.Stderr, "  dolt sql-server  (standalone)\n")
			os.Exit(1)
		}

		// === CONFIGURATION METADATA (Pattern A: Fatal) ===
		// Configuration metadata is essential for core functionality and must succeed.
		// These settings define fundamental behavior (issue IDs, sync workflow).
		// Failure here indicates a serious problem that prevents normal operation.

		// Set the issue prefix in config (only if not already configured —
		// avoid clobbering when multiple rigs share the same Dolt database)
		existing, _ := store.GetConfig(ctx, "issue_prefix")
		if existing == "" {
			if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
				_ = store.Close()
				FatalError("failed to set issue prefix: %v", err)
			}
		}

		// === TRACKING METADATA (Pattern B: Warn and Continue) ===
		// Tracking metadata enhances functionality (diagnostics, version checks, collision detection)
		// but the system works without it. Failures here degrade gracefully - we warn but continue.
		// Belt-and-suspenders: write then verify read-back for each field.

		// Store and verify the bd version (for version mismatch detection)
		verifyMetadata(ctx, store, "bd_version", Version)

		// Compute and store repository fingerprint (FR-015)
		repoID, err := beads.ComputeRepoID()
		if err != nil {
			if !quiet {
				fmt.Fprintf(os.Stderr, "Warning: could not compute repository ID: %v\n", err)
			}
		} else {
			if verifyMetadata(ctx, store, "repo_id", repoID) && !quiet {
				fmt.Printf("  Repository ID: %s\n", repoID[:8])
			}
		}

		// Compute and store clone-specific ID (FR-016: skip on failure)
		cloneID, err := beads.GetCloneID()
		if err != nil {
			if !quiet {
				fmt.Fprintf(os.Stderr, "Warning: could not compute clone ID: %v\n", err)
			}
		} else {
			if verifyMetadata(ctx, store, "clone_id", cloneID) && !quiet {
				fmt.Printf("  Clone ID: %s\n", cloneID)
			}
		}

		// Create or preserve metadata.json for database metadata (bd-zai fix)
		if useLocalBeads {
			// First, check if metadata.json already exists
			existingCfg, err := configfile.Load(beadsDir)
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
				issuesPath := filepath.Join(beadsDir, "issues.jsonl")
				beadsPath := filepath.Join(beadsDir, "beads.jsonl")
				if _, err := os.Stat(beadsPath); err == nil {
					if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
						cfg.JSONLExport = "beads.jsonl" // Legacy filename
					}
				}
			}

			// Always store backend explicitly in metadata.json
			cfg.Backend = backend
			// Metadata.json.database should point to the Dolt directory (not beads.db).
			// Backward-compat: older dolt setups left this as "beads.db", which is misleading.
			if backend == configfile.BackendDolt {
				if cfg.Database == "" || cfg.Database == beads.CanonicalDatabaseName {
					cfg.Database = "dolt"
				}

				// Set prefix-based SQL database name to avoid cross-rig contamination (bd-u8rda).
				// E.g., prefix "gt" → database "beads_gt", prefix "bd" → database "beads_bd".
				if prefix != "" {
					cfg.DoltDatabase = "beads_" + prefix
				}

				// Always server mode
				cfg.DoltMode = configfile.DoltModeServer
				if serverHost != "" {
					cfg.DoltServerHost = serverHost
				}
				if serverPort != 0 {
					cfg.DoltServerPort = serverPort
				}
				if serverUser != "" {
					cfg.DoltServerUser = serverUser
				}
			}

			if err := cfg.Save(beadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create metadata.json: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Create config.yaml template (prefix is stored in DB, not config.yaml)
			if err := createConfigYaml(beadsDir, false, ""); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create config.yaml: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Create README.md
			if err := createReadme(beadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create README.md: %v\n", err)
				// Non-fatal - continue anyway
			}
		}

		// Initialize last_import_time metadata to mark the database as synced.
		// This prevents bd doctor from reporting "No last_import_time recorded in database"
		// after init completes. Sets the metadata to current time in RFC3339 format.
		// (mybd-9gw: sync divergence fix)
		if err := store.SetMetadata(ctx, "last_import_time", time.Now().Format(time.RFC3339)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize last_import_time: %v\n", err)
			// Non-fatal - continue anyway
		}

		// Import issues on init:
		// Dolt backend bootstraps itself from `.beads/issues.jsonl` on first open
		// (factory_dolt.go) when present, so no explicit import is needed here.
		// The --from-jsonl flag is handled by Dolt's bootstrap mechanism automatically.

		// Prompt for contributor mode if:
		// - In a git repo (needed to set beads.role config)
		// - Interactive terminal (stdin is TTY)
		// - No explicit --contributor or --team flag provided
		if isGitRepo() && !contributor && !team && shouldPromptForRole() {
			promptedContributor, err := promptContributorMode()
			if err != nil {
				if isCanceled(err) {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
					_ = store.Close()
					exitCanceled()
				}
				// Non-fatal: warn but continue with default behavior
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to prompt for role: %v\n", err)
				}
			} else if promptedContributor {
				contributor = true // Triggers contributor wizard below
			}
		} else if isGitRepo() {
			// If prompt was skipped (non-interactive or CI environment),
			// ensure beads.role is set to avoid "not configured" warning
			// during diagnostics. Only set if not already configured.
			if _, hasRole := getBeadsRole(); !hasRole {
				// Default to maintainer for non-interactive environments
				if err := setBeadsRole("maintainer"); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to set default beads.role: %v\n", err)
				}
			}
		}

		// Run contributor wizard if --contributor flag is set or user chose contributor
		if contributor {
			if err := runContributorWizard(ctx, store); err != nil {
				canceled := isCanceled(err)
				if canceled {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
				}
				_ = store.Close()
				if canceled {
					exitCanceled()
				}
				FatalError("running contributor wizard: %v", err)
			}
		}

		// Run team wizard if --team flag is set
		if team {
			if err := runTeamWizard(ctx, store); err != nil {
				canceled := isCanceled(err)
				if canceled {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
				}
				_ = store.Close()
				if canceled {
					exitCanceled()
				}
				FatalError("running team wizard: %v", err)
			}
		}

		// Auto-commit Dolt state so bd doctor doesn't warn about uncommitted
		// changes and users don't need a separate "bd vc commit" step.
		if err := store.Commit(ctx, "bd init"); err != nil {
			// Non-fatal: some setups (e.g. no tables yet) may have nothing to commit
			if !strings.Contains(err.Error(), "nothing to commit") {
				fmt.Fprintf(os.Stderr, "Warning: failed to commit initial state: %v\n", err)
			}
		}

		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close database: %v\n", err)
		}

		// Fork detection: offer to configure .git/info/exclude (GH#742)
		setupExclude, _ := cmd.Flags().GetBool("setup-exclude")
		if setupExclude {
			// Manual flag - always configure
			if err := setupForkExclude(!quiet); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
			}
		} else if !stealth && isGitRepo() {
			// Auto-detect fork and prompt (skip if stealth - it handles exclude already)
			if isFork, upstreamURL := detectForkSetup(); isFork {
				shouldExclude, err := promptForkExclude(upstreamURL, quiet)
				if err != nil {
					if isCanceled(err) {
						fmt.Fprintln(os.Stderr, "Setup canceled.")
						exitCanceled()
					}
				}
				if shouldExclude {
					if err := setupForkExclude(!quiet); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
					}
				}
			}
		}

		// Check if we're in a git repo and hooks aren't installed
		// Install by default unless --skip-hooks is passed
		// Hooks are installed to .beads/hooks/ (uses git config core.hooksPath)
		// For jujutsu colocated repos, use simplified hooks (no staging needed)
		if !skipHooks && !hooksInstalled() {
			isJJ := git.IsJujutsuRepo()
			isColocated := git.IsColocatedJJGit()

			if isJJ && !isColocated {
				// Pure jujutsu repo (no git) - print alias instructions
				if !quiet {
					printJJAliasInstructions()
				}
			} else if isColocated {
				// Colocated jj+git repo - use simplified hooks
				if err := installJJHooks(); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "\n%s Failed to install jj hooks: %v\n", ui.RenderWarn("⚠"), err)
					fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", ui.RenderAccent("bd doctor --fix"))
				} else if !quiet {
					fmt.Printf("  Hooks installed (jujutsu mode - no staging)\n")
				}
			} else if isGitRepo() {
				// Regular git repo - install hooks to .beads/hooks/
				embeddedHooks, err := getEmbeddedHooks()
				if err == nil {
					if err := installHooksWithOptions(embeddedHooks, false, false, false, true); err != nil && !quiet {
						fmt.Fprintf(os.Stderr, "\n%s Failed to install git hooks to .beads/hooks/: %v\n", ui.RenderWarn("⚠"), err)
						fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", ui.RenderAccent("bd hooks install --beads"))
					} else if !quiet {
						fmt.Printf("  Hooks installed to: .beads/hooks/\n")
					}
				} else if !quiet {
					fmt.Fprintf(os.Stderr, "\n%s Failed to load embedded hooks: %v\n", ui.RenderWarn("⚠"), err)
				}
			}
		}

		// Initialize version tracking: create .local_version file during bd init
		// instead of deferring it to the first bd command.
		// This ensures no "Version Tracking" warning from bd doctor after init.
		if useLocalBeads {
			localVersionPath := filepath.Join(beadsDir, ".local_version")
			if err := writeLocalVersion(localVersionPath, Version); err != nil && !quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to initialize version tracking: %v\n", err)
				// Non-fatal - initialization still succeeded
			}
		}

		// Add agent instructions to AGENTS.md
		// Skip in stealth mode (user wants invisible setup) and quiet mode (suppress all output)
		if !stealth {
			agentsTemplate, _ := cmd.Flags().GetString("agents-template")
			addAgentsInstructions(!quiet, agentsTemplate)
		}

		// Check for missing git upstream and warn if not configured
		if isGitRepo() && !quiet {
			if !gitHasUpstream() {
				fmt.Fprintf(os.Stderr, "\n%s Git upstream not configured\n", ui.RenderWarn("⚠"))
				fmt.Fprintf(os.Stderr, "  For sync workflows, set your upstream with:\n")
				fmt.Fprintf(os.Stderr, "  %s\n\n", ui.RenderAccent("git remote add upstream <repo-url>"))
			}
		}

		// Skip output if quiet mode
		if quiet {
			return
		}

		fmt.Printf("\n%s bd initialized successfully!\n\n", ui.RenderPass("✓"))
		fmt.Printf("  Backend: %s\n", ui.RenderAccent(backend))
		host := serverHost
		if host == "" {
			host = configfile.DefaultDoltServerHost
		}
		port := serverPort
		if port == 0 {
			port = configfile.DefaultDoltServerPort
		}
		user := serverUser
		if user == "" {
			user = configfile.DefaultDoltServerUser
		}
		fmt.Printf("  Mode: %s\n", ui.RenderAccent("server"))
		fmt.Printf("  Server: %s\n", ui.RenderAccent(fmt.Sprintf("%s@%s:%d", user, host, port)))
		fmt.Printf("  Database: %s\n", ui.RenderAccent(storagePath))
		fmt.Printf("  Issue prefix: %s\n", ui.RenderAccent(prefix))
		fmt.Printf("  Issues will be named: %s\n\n", ui.RenderAccent(prefix+"-<hash> (e.g., "+prefix+"-a3f2dd)"))
		fmt.Printf("Run %s to get started.\n\n", ui.RenderAccent("bd quickstart"))

		// Run limited diagnostics to verify init succeeded.
		// Uses runInitDiagnostics (not runDiagnostics) to only check things
		// that should be true immediately after init — skips git-dependent,
		// federation, and other post-setup checks that aren't applicable yet.
		doctorResult := runInitDiagnostics(cwd)
		// Check if there are any warnings or errors (not just critical failures)
		hasIssues := false
		for _, check := range doctorResult.Checks {
			if check.Status != statusOK {
				hasIssues = true
				break
			}
		}
		if hasIssues {
			fmt.Printf("%s Setup incomplete. Some issues were detected:\n", ui.RenderWarn("⚠"))
			// Show just the warnings/errors, not all checks
			for _, check := range doctorResult.Checks {
				if check.Status != statusOK {
					fmt.Printf("  • %s: %s\n", check.Name, check.Message)
				}
			}
			fmt.Printf("\nRun %s to see details and fix these issues.\n\n", ui.RenderAccent("bd doctor --fix"))
		}
	},
}

func init() {
	initCmd.Flags().StringP("prefix", "p", "", "Issue prefix (default: current directory name)")
	initCmd.Flags().BoolP("quiet", "q", false, "Suppress output (quiet mode)")
	initCmd.Flags().Bool("contributor", false, "Run OSS contributor setup wizard")
	initCmd.Flags().Bool("team", false, "Run team workflow setup wizard")
	initCmd.Flags().Bool("stealth", false, "Enable stealth mode: global gitattributes and gitignore, no local repo tracking")
	initCmd.Flags().Bool("setup-exclude", false, "Configure .git/info/exclude to keep beads files local (for forks)")
	initCmd.Flags().Bool("skip-hooks", false, "Skip git hooks installation")
	initCmd.Flags().Bool("force", false, "Force re-initialization even if JSONL already has issues (may cause data loss)")
	initCmd.Flags().Bool("from-jsonl", false, "Import from current .beads/issues.jsonl file instead of git history (preserves manual cleanups)")
	initCmd.Flags().String("agents-template", "", "Path to custom AGENTS.md template (overrides embedded default)")

	// Dolt server connection flags
	initCmd.Flags().Bool("server", false, "No-op (server mode is always enabled); kept for backward compatibility")
	initCmd.Flags().String("server-host", "", "Dolt server host (default: 127.0.0.1)")
	initCmd.Flags().Int("server-port", 0, "Dolt server port (default: 3307)")
	initCmd.Flags().String("server-user", "", "Dolt server MySQL user (default: root)")

	rootCmd.AddCommand(initCmd)
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
	output, err := readFromGitRef(jsonlPath, gitRef)
	if err != nil {
		return nil, err
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

// checkExistingBeadsDataAt checks for existing database at a specific beadsDir path.
// This is extracted to support both BEADS_DIR and CWD-based resolution.
func checkExistingBeadsDataAt(beadsDir string, prefix string) error {
	// Check if .beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil // No .beads directory, safe to init
	}

	// Check for existing Dolt database
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		// Check both the local directory AND server mode config.
		// In server mode the local dolt/ directory may be empty — the database
		// lives on the Dolt sql-server. Checking only the directory would miss
		// server-mode installations.
		doltPath := filepath.Join(beadsDir, "dolt")
		doltDirExists := false
		if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
			doltDirExists = true
		}
		if doltDirExists || cfg.IsDoltServerMode() {
			location := doltPath
			if cfg.IsDoltServerMode() {
				host := cfg.GetDoltServerHost()
				port := cfg.GetDoltServerPort()
				location = fmt.Sprintf("dolt server at %s:%d", host, port)
			}
			return fmt.Errorf(`
%s Found existing Dolt database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

To completely reinitialize (data loss warning):
  rm -rf %s && bd init --backend dolt --prefix %s

Aborting.`, ui.RenderWarn("⚠"), location, ui.RenderAccent("bd list"), beadsDir, prefix)
		}
	}

	// Check for redirect file - if present, check the redirect target
	redirectTarget := beads.FollowRedirect(beadsDir)
	if redirectTarget != beadsDir {
		targetDBPath := filepath.Join(redirectTarget, beads.CanonicalDatabaseName)
		if _, err := os.Stat(targetDBPath); err == nil {
			return fmt.Errorf(`
%s Cannot init: redirect target already has database

Local .beads redirects to: %s
That location already has: %s

The redirect target is already initialized. Running init here would overwrite it.

To use the existing database:
  Just run bd commands normally (e.g., %s)
  The redirect will route to the canonical database.

To reinitialize the canonical location (data loss warning):
  rm %s && bd init --prefix %s

Aborting.`, ui.RenderWarn("⚠"), redirectTarget, targetDBPath, ui.RenderAccent("bd list"), targetDBPath, prefix)
		}
		return nil // Redirect target has no database - safe to init
	}

	// Check for existing database file (no redirect case)
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if _, err := os.Stat(dbPath); err == nil {
		return fmt.Errorf(`
%s Found existing database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

To completely reinitialize (data loss warning):
  rm -rf %s && bd init --prefix %s

Aborting.`, ui.RenderWarn("⚠"), dbPath, ui.RenderAccent("bd list"), beadsDir, prefix)
	}

	return nil // No database found, safe to init
}

// checkExistingBeadsData checks for existing database files
// and returns an error if found (safety guard for bd-emg)
//
// Note: This only blocks when a database already exists (workspace is initialized).
// Fresh clones with JSONL but no database are allowed - init will create the database
// and import from JSONL automatically (bd-4h9: fixes circular dependency with doctor --fix).
//
// For worktrees, checks the main repository root instead of current directory
// since worktrees should share the database with the main repository.
//
// For redirects, checks the redirect target and errors if it already has a database.
// This prevents accidentally overwriting an existing canonical database (GH#bd-0qel).
func checkExistingBeadsData(prefix string) error {
	// Check BEADS_DIR environment variable first (matches FindBeadsDir pattern)
	// When BEADS_DIR is set, it takes precedence over CWD and worktree checks
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		absBeadsDir := utils.CanonicalizePath(envBeadsDir)
		return checkExistingBeadsDataAt(absBeadsDir, prefix)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil // Can't determine CWD, allow init to proceed
	}

	// Determine where to check for .beads directory
	// Guard with isGitRepo() check first - on Windows, git commands may hang
	// when run outside a git repository (GH#727)
	var beadsDir string
	if isGitRepo() && git.IsWorktree() {
		// For worktrees, .beads should be in the main repository root
		mainRepoRoot, err := git.GetMainRepoRoot()
		if err != nil {
			return nil // Can't determine main repo root, allow init to proceed
		}
		beadsDir = filepath.Join(mainRepoRoot, ".beads")
	} else {
		// For regular repos (or non-git directories), check current directory
		beadsDir = filepath.Join(cwd, ".beads")
	}

	return checkExistingBeadsDataAt(beadsDir, prefix)
}

// shouldPromptForRole returns true if we should prompt the user for their role.
// Skips prompt in non-interactive contexts (CI, scripts, piped input).
func shouldPromptForRole() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// getBeadsRole reads the beads.role git config value.
// Returns the role and true if configured, or empty string and false if not set.
func getBeadsRole() (string, bool) {
	cmd := exec.Command("git", "config", "--get", "beads.role")
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	role := strings.TrimSpace(string(output))
	if role == "" {
		return "", false
	}
	return role, true
}

// setBeadsRole writes the beads.role git config value.
func setBeadsRole(role string) error {
	cmd := exec.Command("git", "config", "beads.role", role)
	return cmd.Run()
}

// promptContributorMode prompts the user to determine if they are a contributor.
// Returns true if the user indicates they are a contributor, false otherwise.
//
// Behavior:
// - If beads.role is already set: shows current role, offers to change
// - If not set: prompts "Contributing to someone else's repo? [y/N]"
// - Sets git config beads.role based on answer
func promptContributorMode() (isContributor bool, err error) {
	ctx := getRootContext()
	reader := bufio.NewReader(os.Stdin)

	// Check if role is already configured
	existingRole, hasRole := getBeadsRole()
	if hasRole {
		fmt.Printf("\n%s Already configured as: %s\n", ui.RenderAccent("▶"), ui.RenderBold(existingRole))
		fmt.Print("Change role? [y/N]: ")

		response, err := readLineWithContext(ctx, reader, os.Stdin)
		if err != nil {
			return false, fmt.Errorf("failed to read input: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))

		if response != "y" && response != "yes" {
			// Keep existing role
			return existingRole == "contributor", nil
		}
		// Fall through to re-prompt
		fmt.Println()
	}

	// Prompt for role
	fmt.Print("Contributing to someone else's repo? [y/N]: ")

	response, err := readLineWithContext(ctx, reader, os.Stdin)
	if err != nil {
		return false, fmt.Errorf("failed to read input: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))

	isContributor = response == "y" || response == "yes"

	// Set the role in git config
	role := "maintainer"
	if isContributor {
		role = "contributor"
	}

	if err := setBeadsRole(role); err != nil {
		return isContributor, fmt.Errorf("failed to set beads.role config: %w", err)
	}

	return isContributor, nil
}

// verifyMetadata writes a metadata field and verifies the write succeeded.
// Returns true if write+verify succeeded, false with warning if either failed.
func verifyMetadata(ctx context.Context, store *dolt.DoltStore, key, value string) bool {
	if err := store.SetMetadata(ctx, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write %s metadata: %v\n", key, err)
		fmt.Fprintf(os.Stderr, "  Run 'bd doctor --fix' to repair.\n")
		return false
	}
	// Verify read-back
	readBack, err := store.GetMetadata(ctx, key)
	if err != nil || readBack != value {
		fmt.Fprintf(os.Stderr, "Warning: %s metadata write did not persist (wrote %q, read %q)\n", key, value, readBack)
		fmt.Fprintf(os.Stderr, "  Run 'bd doctor --fix' to repair.\n")
		return false
	}
	return true
}
