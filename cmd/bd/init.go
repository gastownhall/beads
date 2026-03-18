package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"golang.org/x/term"
)

var initCmd = &cobra.Command{
	Use:     "init",
	GroupID: "setup",
	Short:   "Initialize bd in the current directory",
	Long: `Initialize bd in the current directory by creating a .beads/ directory
and Dolt database. Optionally specify a custom issue prefix.

Dolt is the default (and only supported) storage backend. The legacy SQLite
backend has been removed. Use --backend=sqlite to see migration instructions.

Use --database to specify an existing server database name, overriding the
default prefix-based naming. This is useful when an external tool (e.g. gastown)
has already created the database.

With --stealth: configures per-repository git settings for invisible beads usage:
  • .git/info/exclude to prevent beads files from being committed
  Perfect for personal use without affecting repo collaborators.
  To set up a specific AI tool, run: bd setup <claude|cursor|aider|...> --stealth

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
		skipAgents, _ := cmd.Flags().GetBool("skip-agents")
		force, _ := cmd.Flags().GetBool("force")
		fromJSONL, _ := cmd.Flags().GetBool("from-jsonl")
		// Dolt server connection flags
		backendFlag, _ := cmd.Flags().GetString("backend")
		_, _ = cmd.Flags().GetBool("server") // no-op, kept for backward compatibility
		serverHost, _ := cmd.Flags().GetString("server-host")
		serverPort, _ := cmd.Flags().GetInt("server-port")
		serverUser, _ := cmd.Flags().GetString("server-user")
		database, _ := cmd.Flags().GetString("database")
		destroyToken, _ := cmd.Flags().GetString("destroy-token")
		sharedServer, _ := cmd.Flags().GetBool("shared-server")

		// Handle --backend flag: "dolt" is the only supported backend.
		// "sqlite" is accepted for backward compatibility but prints a
		// deprecation notice and exits with an error.
		if backendFlag == "sqlite" {
			fmt.Fprintf(os.Stderr, "%s The SQLite backend has been removed.\n\n", ui.RenderWarn("⚠ DEPRECATED:"))
			fmt.Fprintf(os.Stderr, "Dolt is now the default (and only) storage backend for beads.\n")
			fmt.Fprintf(os.Stderr, "To initialize with Dolt:\n")
			fmt.Fprintf(os.Stderr, "  bd init\n\n")
			fmt.Fprintf(os.Stderr, "To import issues from an existing JSONL export:\n")
			fmt.Fprintf(os.Stderr, "  bd init --from-jsonl\n\n")
			fmt.Fprintf(os.Stderr, "See: https://github.com/steveyegge/beads/blob/main/docs/DOLT-BACKEND.md\n")
			os.Exit(1)
		} else if backendFlag != "" && backendFlag != "dolt" {
			FatalError("unknown backend %q: only \"dolt\" is supported", backendFlag)
		}

		// Validate --database early, before any side effects
		if database != "" {
			if err := dolt.ValidateDatabaseName(database); err != nil {
				FatalError("invalid database name %q: %v", database, err)
			}
		}

		// Dolt is the only supported backend
		backend := configfile.BackendDolt

		// Propagate --shared-server flag to env so that IsSharedServerMode(),
		// ResolveDoltDir(), and DefaultConfig() all see shared mode immediately
		// (before config.yaml exists). Safe: init runs once and exits.
		if sharedServer {
			_ = os.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		}

		// Initialize config (PersistentPreRun doesn't run for init command)
		if err := config.Initialize(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
			// Non-fatal - continue with defaults
		}

		// Safety guard: check for existing beads data
		// This prevents accidental re-initialization
		if !force {
			if err := checkExistingBeadsData(prefix); err != nil {
				FatalError("%v", err)
			}
		}

		// Even with --force, warn about existing data and require confirmation.
		// In non-interactive mode, accepts --destroy-token for explicit opt-in,
		// or --quiet for legacy (deprecated) bypass.
		// This prevents AI agents and users from accidentally destroying data.
		if force {
			if count, err := countExistingIssues(prefix); err == nil && count > 0 {
				fmt.Fprintf(os.Stderr, "\n%s Re-initializing will destroy the existing database.\n\n", ui.RenderWarn("WARNING:"))
				fmt.Fprintf(os.Stderr, "  Existing issues: %d\n\n", count)
				fmt.Fprintf(os.Stderr, "  This action CANNOT be undone. All issues, dependencies, and\n")
				fmt.Fprintf(os.Stderr, "  Dolt commit history will be permanently lost.\n\n")
				fmt.Fprintf(os.Stderr, "  Before proceeding, consider:\n")
				fmt.Fprintf(os.Stderr, "    bd export > backup.jsonl    # Export issues to JSONL\n")
				fmt.Fprintf(os.Stderr, "    bd dolt status              # Check if this is a server config issue\n\n")
				if term.IsTerminal(int(os.Stdin.Fd())) {
					fmt.Fprintf(os.Stderr, "Type 'destroy %d issues' to confirm: ", count)
					scanner := bufio.NewScanner(os.Stdin)
					scanner.Scan()
					expected := fmt.Sprintf("destroy %d issues", count)
					if strings.TrimSpace(scanner.Text()) != expected {
						fmt.Fprintf(os.Stderr, "\nAborted. Database was NOT modified.\n")
						os.Exit(1)
					}
				} else {
					// Non-interactive (piped input, AI agent, etc.)
					expectedToken := fmt.Sprintf("DESTROY-%s", prefix)
					if destroyToken == expectedToken {
						fmt.Fprintf(os.Stderr, "Destroy token accepted. Proceeding with re-initialization.\n")
					} else if quiet {
						// Legacy --quiet behavior (deprecated path)
						fmt.Fprintf(os.Stderr, "Warning: --force --quiet bypasses safety checks. Use --destroy-token=%s instead.\n", expectedToken)
					} else {
						fmt.Fprintf(os.Stderr, "Refusing to destroy %d issues in non-interactive mode.\n", count)
						fmt.Fprintf(os.Stderr, "To proceed, use: bd init --force --destroy-token=%s\n", expectedToken)
						fmt.Fprintf(os.Stderr, "Or export first: bd export > backup.jsonl\n")
						os.Exit(1)
					}
				}
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

		// Normalize prefix: strip leading dots and trailing hyphens.
		// Leading dots produce invalid Dolt database names (e.g. ".claude" -> "bd_.claude").
		// The trailing hyphen is added automatically during ID generation.
		prefix = strings.TrimLeft(prefix, ".")
		prefix = strings.TrimRight(prefix, "-")

		// Sanitize prefix for use as a MySQL database name.
		// Directory names like "001" (common in temp dirs) are invalid because
		// MySQL identifiers must start with a letter or underscore.
		if len(prefix) > 0 && !((prefix[0] >= 'a' && prefix[0] <= 'z') || (prefix[0] >= 'A' && prefix[0] <= 'Z') || prefix[0] == '_') {
			prefix = "bd_" + prefix
		}

		// Determine beadsDir first (used for all storage path calculations).
		// BEADS_DIR takes precedence, otherwise use CWD/.beads (with redirect support).
		// This must be computed BEFORE initDBPath to ensure consistent path resolution
		// (avoiding macOS /var -> /private/var symlink issues when directory creation
		// happens between path computations).
		var beadsDirForInit string
		if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
			beadsDirForInit = utils.CanonicalizePath(envBeadsDir)
		} else {
			beadsDirForInit = beads.GetWorktreeFallbackBeadsDir()
			if beadsDirForInit == "" {
				localBeadsDir := filepath.Join(".", ".beads")
				beadsDirForInit = beads.FollowRedirect(localBeadsDir)
			}
		}

		// Determine storage path.
		//
		// Precedence: --db > BEADS_DIR > default (.beads/dolt)
		// If there's a redirect file, use the redirect target (GH#bd-0qel)
		initDBPath := dbPath
		if initDBPath == "" {
			// Dolt backend: respect dolt_data_dir config / BEADS_DOLT_DATA_DIR env
			initDBPath = doltserver.ResolveDoltDir(beadsDirForInit)
		}

		// Determine if we should create .beads/ directory in CWD or main repo root
		// For worktrees, .beads should always be in the main repository root
		cwd, err := os.Getwd()
		if err != nil {
			FatalError("failed to get current directory: %v", err)
		}

		hasExplicitBeadsDir := os.Getenv("BEADS_DIR") != ""

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

		// Always create local .beads/ when using default location (CWD/.beads).
		// The local directory is needed for metadata.json, config.yaml, .gitignore,
		// interactions.jsonl, and hooks — regardless of where dolt data lives.
		// Only skip when BEADS_DIR explicitly points outside the project.
		//
		// Previous logic only created .beads/ when the dolt data dir was a
		// subdirectory of .beads/, which broke server mode with external
		// BEADS_DOLT_DATA_DIR or BEADS_DOLT_* env vars (GH#2519).
		useLocalBeads := !hasExplicitBeadsDir || filepath.Clean(initDBDirAbs) == filepath.Clean(beadsDirAbs)

		if useLocalBeads {
			// Create .beads directory
			if err := os.MkdirAll(beadsDir, 0750); err != nil {
				FatalError("failed to create .beads directory: %v", err)
			}

			// Create/update .gitignore in .beads directory (only if missing or outdated)
			gitignorePath := filepath.Join(beadsDir, ".gitignore")
			check := doctor.CheckGitignore(cwd)
			if check.Status != "ok" {
				if err := os.WriteFile(gitignorePath, []byte(doctor.GitignoreTemplate), 0600); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create/update .gitignore: %v\n", err)
					// Non-fatal - continue anyway
				}
			}

			// Add .dolt/ and *.db to project-root .gitignore (GH#2034)
			// Prevents users from accidentally committing Dolt database files.
			// Skip when BEADS_DIR points outside the current directory — the CWD
			// may not be a repo we should mutate (e.g. running from a worktree
			// with an external BEADS_DIR). When BEADS_DIR points to the same
			// repo's .beads/, the gitignore update is still appropriate.
			cwdAbs, _ := filepath.Abs(cwd)
			beadsDirIsLocal := strings.HasPrefix(beadsDirAbs, filepath.Clean(cwdAbs)+string(filepath.Separator))
			if beadsDirIsLocal {
				if err := doctor.EnsureProjectGitignore(cwd); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update project .gitignore: %v\n", err)
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
		// Skip when BEADS_DIR is explicitly set — the caller may be creating a
		// standalone .beads/ directory outside any git repo.
		if !isGitRepo() && !hasExplicitBeadsDir {
			gitInitCmd := exec.Command("git", "init")
			if output, err := gitInitCmd.CombinedOutput(); err != nil {
				FatalError("failed to initialize git repository: %v\n%s", err, output)
			}
			if !quiet {
				fmt.Printf("  %s Initialized git repository\n", ui.RenderPass("✓"))
			}
		}

		// Ensure storage directory exists (.beads/dolt).
		// In server mode, dolt.New() connects via TCP and doesn't create local directories,
		// so we create the marker directory explicitly.
		if err := os.MkdirAll(initDBPath, 0750); err != nil {
			FatalError("failed to create storage directory %s: %v", initDBPath, err)
		}

		ctx := rootCtx

		// Create Dolt storage backend
		storagePath := doltserver.ResolveDoltDir(beadsDir)
		// Respect existing config's database name to avoid creating phantom catalog
		// entries when a user has renamed their database (GH#2051).
		dbName := ""
		if existingCfg, _ := configfile.Load(beadsDir); existingCfg != nil && existingCfg.DoltDatabase != "" {
			dbName = existingCfg.DoltDatabase
		} else if prefix != "" {
			// Sanitize hyphens and dots to underscores for SQL-idiomatic database names.
			// Dots are invalid in Dolt/MySQL identifiers (e.g. from ".claude" directories).
			// Must match the sanitization applied to metadata.json DoltDatabase
			// field (line below), otherwise init creates a database with one name
			// but metadata.json records a different name, causing reopens to fail.
			dbName = strings.ReplaceAll(prefix, "-", "_")
			dbName = strings.ReplaceAll(dbName, ".", "_")
		} else {
			dbName = "beads"
		}
		// --database flag overrides all prefix-based naming. This allows callers
		// (e.g. gastown) to specify a pre-existing database name, preventing orphan
		// database creation when the database was already created externally.
		if database != "" {
			dbName = database
		}
		// Auto-bootstrap from git remote if sync.git-remote is configured.
		// This enables the new-machine story: set sync.git-remote in config.yaml,
		// run bd init, and the Dolt database is cloned from the git remote
		// automatically — no manual dolt clone needed.
		gitRemoteURL := config.GetString("sync.git-remote")
		bootstrappedFromRemote := false
		if gitRemoteURL != "" {
			cloned, bootstrapErr := dolt.BootstrapFromGitRemoteWithDB(ctx, storagePath, gitRemoteURL, dbName)
			if bootstrapErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to bootstrap from git remote %s: %v\n", gitRemoteURL, bootstrapErr)
				fmt.Fprintf(os.Stderr, "  Continuing with fresh database initialization.\n")
				// Non-fatal: fall through to normal init
			} else if cloned {
				bootstrappedFromRemote = true
				if !quiet {
					fmt.Printf("  %s Bootstrapped from git remote: %s\n", ui.RenderPass("✓"), gitRemoteURL)
				}
			}
		} else if !force && isGitRepo() && !isBareGitRepo() {
			// Warn if origin has an existing beads database.
			// Don't auto-clone here — bd bootstrap handles that.
			if originURL, err := gitRemoteGetURL("origin"); err == nil && originURL != "" {
				if gitLsRemoteHasRef("origin", "refs/dolt/data") {
					fmt.Fprintf(os.Stderr, "Note: origin has an existing beads database (refs/dolt/data).\n")
					fmt.Fprintf(os.Stderr, "  Run 'bd bootstrap' instead to clone it.\n")
					fmt.Fprintf(os.Stderr, "  Continuing with fresh database initialization.\n\n")
				}
			}
		}

		// Build config. Beads always uses dolt sql-server.
		// AutoStart is always enabled during init — we need a server to initialize the database.
		//
		// Port resolution for init: use ONLY project-local sources (env var, port file)
		// to prevent cross-project data leakage (GH#2336). DefaultConfig falls through
		// to config.yaml / global config, which may resolve to another project's server
		// because metadata.json doesn't exist yet during init. For fresh inits, port 0
		// forces auto-start to allocate an ephemeral port for THIS project.
		initPort := 0
		if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
			if port, err := strconv.Atoi(p); err == nil && port > 0 {
				initPort = port
			}
		}
		if initPort == 0 {
			initPort = doltserver.ReadPortFile(beadsDir)
		}
		// Shared server mode intentionally uses a common port for all projects.
		if initPort == 0 && doltserver.IsSharedServerMode() {
			initPort = doltserver.DefaultSharedServerPort
		}
		doltCfg := &dolt.Config{
			Path:            storagePath,
			BeadsDir:        beadsDir,
			Database:        dbName,
			ServerPort:      initPort,
			CreateIfMissing: true, // bd init is the only path that should create databases
			AutoStart:       os.Getenv("BEADS_DOLT_AUTO_START") != "0",
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

		initLock, err := acquireEmbeddedLock(beadsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer initLock.Unlock()

		// Clean stale noms LOCK files from previously crashed processes
		// before opening the embedded store. Without this, a crashed init
		// leaves LOCK files that cause nil pointer dereference in DoltDB.
		// NOTE: Intentionally skipped for embedded mode. Embedded dolt has its
		// own locking model and we don't want init to mutate embedded data dirs.
		if !isEmbeddedDolt {
			dolt.CleanStaleNomsLocks(doltserver.ResolveDoltDir(beadsDir))
		}

		store, err := newDoltStore(ctx, doltCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to open Dolt store: %v\n", err)
			os.Exit(1)
		}

		// Configure the git remote in the Dolt store so bd dolt push/pull
		// work immediately after bootstrap. Also add the remote when
		// sync.git-remote is configured but bootstrap was skipped (DB already
		// existed) — ensures the remote is always wired up.
		if gitRemoteURL != "" {
			hasRemote, _ := store.HasRemote(ctx, "origin")
			if !hasRemote {
				if err := store.AddRemote(ctx, "origin", gitRemoteURL); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add git remote 'origin': %v\n", err)
					// Non-fatal — user can add manually with: bd dolt remote add origin <url>
				} else if !quiet {
					fmt.Printf("  %s Configured Dolt remote: origin → %s\n", ui.RenderPass("✓"), gitRemoteURL)
				}
			}
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
				cfg = configfile.DefaultConfig()
			}

			// Generate project identity UUID if not already set (GH#2372).
			// This UUID is stored in both metadata.json and the database,
			// and verified on every connection to detect cross-project leakage.
			if cfg.ProjectID == "" {
				cfg.ProjectID = configfile.GenerateProjectID()
			}

			// Always store backend explicitly in metadata.json
			cfg.Backend = backend
			// Metadata.json.database should point to the Dolt directory (not beads.db).
			// Backward-compat: older dolt setups left this as "beads.db", which is misleading.
			if backend == configfile.BackendDolt {
				if cfg.Database == "" || cfg.Database == beads.CanonicalDatabaseName {
					cfg.Database = "dolt"
				}

				// Set SQL database name. --database flag takes precedence over prefix-based
				// naming to avoid cross-rig contamination (bd-u8rda). Only set prefix-based
				// name if not already configured — overwriting a user-renamed database
				// creates phantom catalog entries that crash information_schema (GH#2051).
				if database != "" {
					cfg.DoltDatabase = database
				} else if cfg.DoltDatabase == "" && prefix != "" {
					// Sanitize hyphens to underscores for SQL-idiomatic names (GH#2142).
					cfg.DoltDatabase = strings.ReplaceAll(prefix, "-", "_")
				}

				// Persist the connection mode matching this build.
				if isEmbeddedDolt {
					cfg.DoltMode = configfile.DoltModeEmbedded
				} else {
					cfg.DoltMode = configfile.DoltModeServer
				}
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

			// Write project identity to database for cross-project verification (GH#2372)
			if cfg.ProjectID != "" && store != nil {
				if err := store.SetMetadata(ctx, "_project_id", cfg.ProjectID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to write project ID to database: %v\n", err)
				}
			}

			// Create config.yaml template (prefix is stored in DB, not config.yaml)
			if err := createConfigYaml(beadsDir, false, ""); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create config.yaml: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Enable shared server mode if requested via flag OR env var (GH#2377).
			// Persist to config.yaml so the project continues working without the env var.
			if sharedServer || doltserver.IsSharedServerMode() {
				if err := config.SetYamlConfig("dolt.shared-server", "true"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to enable shared server mode: %v\n", err)
				} else if !quiet {
					fmt.Printf("  %s Shared server mode enabled\n", ui.RenderPass("✓"))
				}
			}

			// In stealth mode, persist no-git-ops: true so bd prime
			// automatically uses stealth session-close protocol (GH#2159)
			if stealth {
				if err := config.SaveConfigValue("no-git-ops", true, beadsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to set no-git-ops in config: %v\n", err)
				}
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

		// Import from local JSONL if requested (GH#2023).
		// This must run after the store is created and prefix is set.
		if fromJSONL {
			localJSONLPath := filepath.Join(beadsDir, "issues.jsonl")
			if _, statErr := os.Stat(localJSONLPath); os.IsNotExist(statErr) {
				_ = store.Close()
				FatalError("--from-jsonl specified but %s does not exist", localJSONLPath)
			}
			issueCount, importErr := importFromLocalJSONL(ctx, store, localJSONLPath)
			if importErr != nil {
				_ = store.Close()
				FatalError("failed to import from JSONL: %v", importErr)
			}
			if !quiet {
				fmt.Printf("  Imported %d issues from %s\n", issueCount, localJSONLPath)
			}
		}

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
		} else if isGitRepo() && !contributor && !team {
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

			// Contributor setup must also pin role detection to contributor.
			// Without this, SSH remotes can be inferred as maintainer and bypass routing.
			if isGitRepo() {
				if err := setBeadsRole("contributor"); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to set beads.role=contributor: %v\n", err)
				}
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

		// Clean up 0-byte noms LOCK files left behind by the store open/close cycle.
		// NOTE: Intentionally skipped for embedded mode. See earlier note.
		if !isEmbeddedDolt {
			dolt.CleanStaleNomsLocks(doltserver.ResolveDoltDir(beadsDir))
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
		hooksExist := hooksInstalled()
		if !skipHooks && (!hooksExist || hooksNeedUpdate()) {
			if hooksExist && !quiet {
				fmt.Printf("  Updating hooks to version %s...\n", Version)
			}
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
				if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "\n%s Failed to install git hooks to .beads/hooks/: %v\n", ui.RenderWarn("⚠"), err)
					fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", ui.RenderAccent("bd hooks install --beads"))
				} else if !quiet {
					fmt.Printf("  Hooks installed to: .beads/hooks/\n")
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
		// Skip in stealth mode (user wants invisible setup) or when explicitly skipped
		if !stealth && !skipAgents {
			agentsTemplate, _ := cmd.Flags().GetString("agents-template")
			if isBareGitRepo() {
				if !quiet {
					fmt.Printf("  Skipping AGENTS.md generation in bare repository\n")
				}
			} else {
				addAgentsInstructions(!quiet, agentsTemplate)
			}
		}

		// Auto-stage and commit beads files so bd doctor doesn't warn about
		// untracked files or dirty working tree in a clean room setup.
		// Only runs when not stealth, in a git repo, and using local storage.
		if !stealth && isGitRepo() && useLocalBeads {
			gitAddCmd := exec.Command("git", "add", ".beads/")
			if _, addErr := gitAddCmd.CombinedOutput(); addErr == nil {
				// Also stage AGENTS.md if it exists
				if _, statErr := os.Stat("AGENTS.md"); statErr == nil {
					agentsCmd := exec.Command("git", "add", "AGENTS.md")
					_ = agentsCmd.Run()
				}
				// Also stage .gitignore if modified by EnsureProjectGitignore
				if _, statErr := os.Stat(".gitignore"); statErr == nil {
					giCmd := exec.Command("git", "add", ".gitignore")
					_ = giCmd.Run()
				}
				commitCmd := exec.Command("git", "commit", "-m", "bd init: initialize beads issue tracking")
				if commitOut, commitErr := commitCmd.CombinedOutput(); commitErr != nil {
					if !quiet && !strings.Contains(string(commitOut), "nothing to commit") {
						fmt.Fprintf(os.Stderr, "Warning: failed to commit beads files: %v\n", commitErr)
					}
				} else if !quiet {
					fmt.Printf("  %s Committed beads files to git\n", ui.RenderPass("✓"))
				}
				// Clean up LOCK files again — the pre-commit hook may have
				// reopened the database and left a new LOCK behind.
				// NOTE: Intentionally skipped for embedded mode. See earlier note.
				if !isEmbeddedDolt {
					dolt.CleanStaleNomsLocks(doltserver.ResolveDoltDir(beadsDir))
				}
			}
		}

		// Check for missing git upstream and warn if not configured.
		// Only warn when remotes exist (has origin but no upstream).
		// Skip for brand-new repos with no remotes — the warning is noise there.
		if isGitRepo() && !quiet {
			if gitHasAnyRemotes() && !gitHasUpstream() {
				fmt.Fprintf(os.Stderr, "\n%s Git upstream not configured\n", ui.RenderWarn("⚠"))
				fmt.Fprintf(os.Stderr, "  For sync workflows, set your upstream with:\n")
				fmt.Fprintf(os.Stderr, "  %s\n\n", ui.RenderAccent("git remote add upstream <repo-url>"))
			}
		}

		// Skip output if quiet mode
		if quiet {
			return
		}

		if bootstrappedFromRemote {
			fmt.Printf("\n%s bd initialized from git remote!\n\n", ui.RenderPass("✓"))
		} else {
			fmt.Printf("\n%s bd initialized successfully!\n\n", ui.RenderPass("✓"))
		}
		fmt.Printf("  Backend: %s\n", ui.RenderAccent(backend))
		if isEmbeddedDolt {
			fmt.Printf("  Mode: %s\n", ui.RenderAccent("embedded"))
		} else {
			host := serverHost
			if host == "" {
				host = configfile.DefaultDoltServerHost
			}
			port := serverPort
			if port == 0 {
				port = doltserver.DefaultConfig(beadsDir).Port
			}
			user := serverUser
			if user == "" {
				user = configfile.DefaultDoltServerUser
			}
			fmt.Printf("  Mode: %s\n", ui.RenderAccent("server"))
			fmt.Printf("  Server: %s\n", ui.RenderAccent(fmt.Sprintf("%s@%s:%d", user, host, port)))
			// Warn when using the default localhost — this is the #1 misconfiguration
			// for setups where Dolt runs on a remote machine (e.g., over Tailscale).
			if serverHost == "" && os.Getenv("BEADS_DOLT_SERVER_HOST") == "" {
				fmt.Fprintf(os.Stderr, "\n  %s Server host defaulted to %s.\n", ui.RenderWarn("⚠"), configfile.DefaultDoltServerHost)
				fmt.Fprintf(os.Stderr, "    If your Dolt server is remote, set BEADS_DOLT_SERVER_HOST or pass --server-host.\n")
			}
		}
		fmt.Printf("  Database: %s\n", ui.RenderAccent(dbName))
		fmt.Printf("  Issue prefix: %s\n", ui.RenderAccent(prefix))
		fmt.Printf("  Issues will be named: %s\n\n", ui.RenderAccent(prefix+"-<hash> (e.g., "+prefix+"-a3f2dd)"))
		fmt.Printf("Run %s to get started.\n\n", ui.RenderAccent("bd quickstart"))

		// Detect backup files from a previous session (GH#2327).
		// This catches the branch-switch scenario: user ran bd init on a new
		// branch and the database was created fresh, but backup JSONL files
		// exist from a prior backup on this or another branch.
		if !bootstrappedFromRemote && dolt.HasBackupFiles(beadsDir) {
			fmt.Printf("  %s Backup files detected in .beads/backup/\n", ui.RenderWarn("!"))
			fmt.Printf("    To restore issues from a previous backup, run:\n")
			fmt.Printf("      %s\n\n", ui.RenderAccent("bd backup restore"))
		}

		// Run limited diagnostics to verify init succeeded.
		// Skipped in embedded mode: diagnostics use dolt.NewFromConfigWithOptions
		// which auto-starts a dolt sql-server. Embedded init already validates
		// the database via initSchema.
		if !isEmbeddedDolt {
			doctorResult := runInitDiagnostics(cwd)
			hasIssues := false
			for _, check := range doctorResult.Checks {
				if check.Status != statusOK {
					hasIssues = true
					break
				}
			}
			if hasIssues {
				fmt.Printf("%s Setup incomplete. Some issues were detected:\n", ui.RenderWarn("⚠"))
				for _, check := range doctorResult.Checks {
					if check.Status != statusOK {
						fmt.Printf("  • %s: %s\n", check.Name, check.Message)
					}
				}
				fmt.Printf("\nRun %s to see details and fix these issues.\n\n", ui.RenderAccent("bd doctor --fix"))
			}
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
	initCmd.Flags().Bool("skip-agents", false, "Skip AGENTS.md and Claude settings generation")
	initCmd.Flags().Bool("force", false, "Force re-initialization even if database already has issues (may cause data loss)")
	initCmd.Flags().Bool("from-jsonl", false, "Import issues from .beads/issues.jsonl instead of git history")
	initCmd.Flags().String("destroy-token", "", "Explicit confirmation token for destructive re-init in non-interactive mode (format: 'DESTROY-<prefix>')")
	initCmd.Flags().String("agents-template", "", "Path to custom AGENTS.md template (overrides embedded default)")

	// Backend selection (dolt is the only supported backend; sqlite accepted for deprecation notice)
	initCmd.Flags().String("backend", "", "Storage backend (default: dolt). --backend=sqlite prints deprecation notice.")

	// Dolt server connection flags
	initCmd.Flags().Bool("server", false, "Use server mode (currently the default; embedded mode returning soon)")
	initCmd.Flags().String("server-host", "", "Dolt server host (default: 127.0.0.1)")
	initCmd.Flags().Int("server-port", 0, "Dolt server port (default: 3307)")
	initCmd.Flags().String("server-user", "", "Dolt server MySQL user (default: root)")
	initCmd.Flags().String("database", "", "Use existing server database name (overrides prefix-based naming)")
	initCmd.Flags().Bool("shared-server", false, "Enable shared Dolt server mode (all projects share one server at ~/.beads/shared-server/)")

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

// checkExistingBeadsDataAt checks for existing database at a specific beadsDir path.
// This is extracted to support both BEADS_DIR and CWD-based resolution.
func checkExistingBeadsDataAt(beadsDir string, prefix string) error {
	// Check if .beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil // No .beads directory, safe to init
	}

	// Check for existing Dolt database
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		// Embedded mode stores databases under `.beads/embeddeddolt/<db>/`.
		// Treat any present embedded DB as "already initialized" (guard against
		// accidental re-init / data loss).
		if isEmbeddedDolt {
			embeddedRoot := filepath.Join(beadsDir, "embeddeddolt")
			entries, err := os.ReadDir(embeddedRoot)
			if err != nil {
				return nil // No embedded root -> fresh clone, safe to init
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				if info, statErr := os.Stat(filepath.Join(embeddedRoot, entry.Name(), ".dolt")); statErr == nil && info.IsDir() {
					location := filepath.Join(embeddedRoot, entry.Name())
					return fmt.Errorf(`
%s Found existing Dolt database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

If the database is genuinely corrupt and unrecoverable:
  bd export > backup.jsonl              # Back up first!
  bd init --force --prefix %s           # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), location, ui.RenderAccent("bd list"), prefix)
				}
			}
			return nil
		}

		// Check both the local directory AND server mode config.
		// In server mode the local dolt/ directory may be empty — the database
		// lives on the Dolt sql-server. Checking only the directory would miss
		// server-mode installations.
		doltPath := doltserver.ResolveDoltDir(beadsDir)
		doltDirExists := false
		if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
			doltDirExists = true
		}
		if doltDirExists || cfg.IsDoltServerMode() {
			// For server mode, distinguish "DB exists" from "DB missing" (FR-010).
			if cfg.IsDoltServerMode() && !doltDirExists {
				host := cfg.GetDoltServerHost()
				port := doltserver.DefaultConfig(beadsDir).Port
				dbName := cfg.GetDoltDatabase()
				password := cfg.GetDoltServerPassword()
				user := cfg.GetDoltServerUser()

				result := checkDatabaseOnServer(host, port, user, password, dbName)
				if result.Reachable && !result.Exists && result.Err == nil {
					// Server is up but DB doesn't exist. Since we also know
					// doltDirExists==false, this is a fresh clone — there's no
					// local database to protect. Allow init to proceed so the
					// user can bootstrap (e.g. via --from-jsonl). (GH#2433)
					return nil
				}
				if result.Reachable && result.Exists {
					// Server up and DB exists — fall through to "already initialized" error.
				} else {
					// Server unreachable or error during check: this is a fresh clone
					// with committed metadata.json but no local dolt/ directory.
					// Allow init to proceed so the user can bootstrap the database
					// (e.g. via --from-jsonl). (GH#2433)
					return nil
				}
			}

			location := doltPath
			if cfg.IsDoltServerMode() {
				host := cfg.GetDoltServerHost()
				port := doltserver.DefaultConfig(beadsDir).Port
				location = fmt.Sprintf("dolt server at %s:%d", host, port)
			}
			return fmt.Errorf(`
%s Found existing Dolt database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

If the database is genuinely corrupt and unrecoverable:
  bd export > backup.jsonl              # Back up first!
  bd init --force --prefix %s           # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), location, ui.RenderAccent("bd list"), prefix)
		}
		// Backend is Dolt but no dolt directory exists yet — this is a fresh
		// clone. Any beads.db file is a legacy SQLite artifact, not the active
		// database. Skip the SQLite checks below and allow init to proceed.
		return nil
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

If the database is genuinely corrupt and unrecoverable:
  bd export > backup.jsonl              # Back up first!
  bd init --force --prefix %s           # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), redirectTarget, targetDBPath, ui.RenderAccent("bd list"), prefix)
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

If the database is genuinely corrupt and unrecoverable:
  bd export > backup.jsonl              # Back up first!
  bd init --force --prefix %s           # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), dbPath, ui.RenderAccent("bd list"), prefix)
	}

	return nil // No database found, safe to init
}

// countExistingIssues attempts to connect to the existing database and count
// issues. Returns 0 if the database is unreachable or empty. Used by --force
// safeguard to show users what they're about to destroy.
func countExistingIssues(_ string) (int, error) {
	beadsDir := ".beads"
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		beadsDir = utils.CanonicalizePath(envBeadsDir)
	} else {
		beadsDir = beads.FollowRedirect(beadsDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := newDoltStoreFromConfig(ctx, beadsDir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = store.Close() }()

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, err
	}
	if stats == nil {
		return 0, nil
	}
	return stats.TotalIssues, nil
}

// checkExistingBeadsData checks for existing database files
// and returns an error if found (safety guard for bd-emg)
//
// Note: This only blocks when a database already exists (workspace is initialized).
// Fresh clones without a database are allowed — init will create the database.
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
		beadsDir = beads.GetWorktreeFallbackBeadsDir()
		if beadsDir == "" {
			return nil // Can't determine shared fallback, allow init to proceed
		}
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
func verifyMetadata(ctx context.Context, store storage.DoltStorage, key, value string) bool {
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
