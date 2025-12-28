package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"golang.org/x/term"
)

var importCmd = &cobra.Command{
	Use:     "import",
	GroupID: "sync",
	Short:   "Import issues from JSONL format",
	Long: `Import issues from JSON Lines format (one JSON object per line).

Reads from stdin by default, or use -i flag for file input.

Behavior:
  - Existing issues (same ID) are updated
  - New issues are created
  - Collisions (same ID, different content) are detected and reported
  - Use --dedupe-after to find and merge content duplicates after import
  - Use --dry-run to preview changes without applying them

NOTE: Import requires direct database access and does not work with daemon mode.
      The command automatically uses --no-daemon when executed.`,
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("import")
		// Check for positional arguments (common mistake: bd import file.jsonl instead of bd import -i file.jsonl)
		if len(args) > 0 {
			fmt.Fprintf(os.Stderr, "Error: Unexpected argument(s): %v\n\n", args)
			fmt.Fprintf(os.Stderr, "Did you mean: bd import -i %s\n\n", args[0])
			fmt.Fprintf(os.Stderr, "The import command does not accept positional arguments.\n")
			fmt.Fprintf(os.Stderr, "Use the -i flag to specify an input file:\n")
			fmt.Fprintf(os.Stderr, "  bd import -i .beads/issues.jsonl\n\n")
			fmt.Fprintf(os.Stderr, "Or pipe data via stdin:\n")
			fmt.Fprintf(os.Stderr, "  cat data.jsonl | bd import\n")
			os.Exit(1)
		}

		// Ensure database directory exists (auto-create if needed)
		dbDir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dbDir, 0750); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create database directory: %v\n", err)
			os.Exit(1)
		}

		// Import requires direct database access due to complex transaction handling
		// and collision detection. Force direct mode regardless of daemon state.
		//
		// NOTE: We only close the daemon client connection here, not stop the daemon
		// process. This is because import may be called as a subprocess from sync,
		// and stopping the daemon would break the parent sync's connection.
		// The daemon-stale-DB issue is addressed separately by
		// having sync use --no-daemon mode for consistency.
		if daemonClient != nil {
			debug.Logf("Debug: import command forcing direct mode (closes daemon connection)\n")
			_ = daemonClient.Close()
			daemonClient = nil

			var err error
			store, err = sqlite.New(rootCtx, dbPath)
			if err != nil {
				// Check for fresh clone scenario
				beadsDir := filepath.Dir(dbPath)
				if handleFreshCloneError(err, beadsDir) {
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		// We'll check if database needs initialization after reading the JSONL
		// so we can detect the prefix from the imported issues

		input, _ := cmd.Flags().GetString("input")
		skipUpdate, _ := cmd.Flags().GetBool("skip-existing")
		strict, _ := cmd.Flags().GetBool("strict")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		renameOnImport, _ := cmd.Flags().GetBool("rename-on-import")
		dedupeAfter, _ := cmd.Flags().GetBool("dedupe-after")
		clearDuplicateExternalRefs, _ := cmd.Flags().GetBool("clear-duplicate-external-refs")
		orphanHandling, _ := cmd.Flags().GetString("orphan-handling")
		force, _ := cmd.Flags().GetBool("force")
		protectLeftSnapshot, _ := cmd.Flags().GetBool("protect-left-snapshot")
		noGitHistory, _ := cmd.Flags().GetBool("no-git-history")
		_ = noGitHistory // Accepted for compatibility with bd sync subprocess calls

		// Check if stdin is being used interactively (not piped)
		if input == "" && term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintf(os.Stderr, "Error: No input specified.\n\n")
			fmt.Fprintf(os.Stderr, "Usage:\n")
			fmt.Fprintf(os.Stderr, "  bd import -i .beads/issues.jsonl          # Import from file\n")
			fmt.Fprintf(os.Stderr, "  bd import -i .beads/issues.jsonl --dry-run # Preview changes\n")
			fmt.Fprintf(os.Stderr, "  cat data.jsonl | bd import               # Import from pipe\n")
			fmt.Fprintf(os.Stderr, "  bd sync --import-only                    # Import latest JSONL\n\n")
			fmt.Fprintf(os.Stderr, "For more information, run: bd import --help\n")
			os.Exit(1)
		}

		// Open input
		in := os.Stdin
		if input != "" {
			// #nosec G304 - user-provided file path is intentional
			f, err := os.Open(input)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening input file: %v\n", err)
				os.Exit(1)
			}
			defer func() {
				if err := f.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to close input file: %v\n", err)
				}
			}()
			in = f
		}

		// Phase 1: Read and parse all JSONL
		ctx := rootCtx
		scanner := bufio.NewScanner(in)

		var allIssues []*types.Issue
		lineNum := 0

		for scanner.Scan() {
			lineNum++
			rawLine := scanner.Bytes()
			line := string(rawLine)

			// Skip empty lines
			if line == "" {
				continue
			}

			// Detect git conflict markers in raw bytes (before JSON decoding)
			// This prevents false positives when issue content contains these strings
			trimmed := bytes.TrimSpace(rawLine)
			if bytes.HasPrefix(trimmed, []byte("<<<<<<< ")) ||
				bytes.Equal(trimmed, []byte("=======")) ||
				bytes.HasPrefix(trimmed, []byte(">>>>>>> ")) {
				fmt.Fprintf(os.Stderr, "Git conflict markers detected in JSONL file (line %d)\n", lineNum)
				fmt.Fprintf(os.Stderr, "→ Attempting automatic 3-way merge...\n\n")

				// Attempt automatic merge using bd merge command
				if err := attemptAutoMerge(input); err != nil {
					fmt.Fprintf(os.Stderr, "Error: Automatic merge failed: %v\n\n", err)
					fmt.Fprintf(os.Stderr, "To resolve manually:\n")
					fmt.Fprintf(os.Stderr, "  git checkout --ours .beads/issues.jsonl && bd import -i .beads/issues.jsonl\n")
					fmt.Fprintf(os.Stderr, "  git checkout --theirs .beads/issues.jsonl && bd import -i .beads/issues.jsonl\n\n")
					fmt.Fprintf(os.Stderr, "For advanced field-level merging, see: https://github.com/neongreen/mono/tree/main/beads-merge\n")
					os.Exit(1)
				}

				fmt.Fprintf(os.Stderr, "✓ Automatic merge successful\n")
				fmt.Fprintf(os.Stderr, "→ Restarting import with merged JSONL...\n\n")

				// Re-open the input file to read the merged content
				if input != "" {
					// Close current file handle
					if in != os.Stdin {
						_ = in.Close()
					}

					// Re-open the merged file
					// #nosec G304 - user-provided file path is intentional
					f, err := os.Open(input)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error reopening merged file: %v\n", err)
						os.Exit(1)
					}
					defer func() {
						if err := f.Close(); err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to close input file: %v\n", err)
						}
					}()
					in = f
					scanner = bufio.NewScanner(in)
					allIssues = nil // Reset issues list
					lineNum = 0     // Reset line counter
					continue        // Restart parsing from beginning
				} else {
					// Can't retry stdin - should not happen since git conflicts only in files
					fmt.Fprintf(os.Stderr, "Error: Cannot retry merge from stdin\n")
					os.Exit(1)
				}
			}

			// Parse JSON
			var issue types.Issue
			if err := json.Unmarshal([]byte(line), &issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing line %d: %v\n", lineNum, err)
				os.Exit(1)
			}
			issue.SetDefaults() // Apply defaults for omitted fields (beads-399)

			allIssues = append(allIssues, &issue)
		}

		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			os.Exit(1)
		}

		// Check if database needs initialization (prefix not set)
		// Detect prefix from the imported issues
		initCtx := rootCtx
		configuredPrefix, err2 := store.GetConfig(initCtx, "issue_prefix")
		if err2 != nil || strings.TrimSpace(configuredPrefix) == "" {
			// Database exists but not initialized - detect prefix from issues
			detectedPrefix := detectPrefixFromIssues(allIssues)
			prefixSource := "issues"
			if detectedPrefix == "" {
				// No issues to import or couldn't detect prefix, use directory name
				// But avoid using ".beads" as prefix - go up one level
				cwd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: failed to get current directory: %v\n", err)
					os.Exit(1)
				}
				dirName := filepath.Base(cwd)
				if dirName == ".beads" || dirName == "beads" {
					// Running from inside .beads/ - use parent directory
					detectedPrefix = filepath.Base(filepath.Dir(cwd))
				} else {
					detectedPrefix = dirName
				}
				prefixSource = "directory"
			}
			detectedPrefix = strings.TrimRight(detectedPrefix, "-")

			if err := store.SetConfig(initCtx, "issue_prefix", detectedPrefix); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to set issue prefix: %v\n", err)
				os.Exit(1)
			}

			fmt.Fprintf(os.Stderr, "✓ Initialized database with prefix '%s' (detected from %s)\n", detectedPrefix, prefixSource)
		}

		// Phase 2: Use shared import logic
		opts := ImportOptions{
			DryRun:                     dryRun,
			SkipUpdate:                 skipUpdate,
			Strict:                     strict,
			RenameOnImport:             renameOnImport,
			ClearDuplicateExternalRefs: clearDuplicateExternalRefs,
			OrphanHandling:             orphanHandling,
		}

		// If --protect-left-snapshot is set, read the left snapshot and build ID set
		// This protects locally exported issues from git-history-backfill
		if protectLeftSnapshot && input != "" {
			beadsDir := filepath.Dir(input)
			leftSnapshotPath := filepath.Join(beadsDir, "beads.left.jsonl")
			if _, err := os.Stat(leftSnapshotPath); err == nil {
				sm := NewSnapshotManager(input)
				leftIDs, err := sm.BuildIDSet(leftSnapshotPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to read left snapshot: %v\n", err)
				} else if len(leftIDs) > 0 {
					opts.ProtectLocalExportIDs = leftIDs
					fmt.Fprintf(os.Stderr, "Protecting %d issue(s) from left snapshot\n", len(leftIDs))
				}
			}
		}

		result, err := importIssuesCore(ctx, dbPath, store, allIssues, opts)

		// Check for uncommitted changes in JSONL after import
		// Only check if we have an input file path (not stdin) and it's the default beads file
		if result != nil && input != "" && (input == ".beads/issues.jsonl" || input == ".beads/beads.jsonl") {
			checkUncommittedChanges(input, result)
		}

		// Handle errors and special cases
		if err != nil {
			// Check if it's a prefix mismatch error
			if result != nil && result.PrefixMismatch {
				fmt.Fprintf(os.Stderr, "\n=== Prefix Mismatch Detected ===\n")
				fmt.Fprintf(os.Stderr, "Database configured prefix: %s-\n", result.ExpectedPrefix)
				fmt.Fprintf(os.Stderr, "Found issues with different prefixes:\n")
				for prefix, count := range result.MismatchPrefixes {
					fmt.Fprintf(os.Stderr, "  %s- (%d issues)\n", prefix, count)
				}
				fmt.Fprintf(os.Stderr, "\nOptions:\n")
				fmt.Fprintf(os.Stderr, "  --rename-on-import    Auto-rename imported issues to match configured prefix\n")
				fmt.Fprintf(os.Stderr, "  --dry-run             Preview what would be imported\n")
				fmt.Fprintf(os.Stderr, "\nOr use 'bd rename-prefix' after import to fix the database.\n")
				os.Exit(1)
			}

			// Check if it's a collision error
			if result != nil && len(result.CollisionIDs) > 0 {
				// Print collision report before exiting
				fmt.Fprintf(os.Stderr, "\n=== Collision Detection Report ===\n")
				fmt.Fprintf(os.Stderr, "COLLISIONS DETECTED: %d\n\n", result.Collisions)
				fmt.Fprintf(os.Stderr, "Colliding issue IDs: %v\n", result.CollisionIDs)
				fmt.Fprintf(os.Stderr, "\nWith hash-based IDs, collisions should not occur.\n")
				fmt.Fprintf(os.Stderr, "This may indicate manual ID manipulation or a bug.\n")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Import failed: %v\n", err)
			os.Exit(1)
		}

		// Handle dry-run mode
		if dryRun {
			if result.PrefixMismatch {
				fmt.Fprintf(os.Stderr, "\n=== Prefix Mismatch Detected ===\n")
				fmt.Fprintf(os.Stderr, "Database configured prefix: %s-\n", result.ExpectedPrefix)
				fmt.Fprintf(os.Stderr, "Found issues with different prefixes:\n")
				for prefix, count := range result.MismatchPrefixes {
					fmt.Fprintf(os.Stderr, "  %s- (%d issues)\n", prefix, count)
				}
				fmt.Fprintf(os.Stderr, "\nUse --rename-on-import to automatically fix prefixes during import.\n")
			}

			if result.Collisions > 0 {
				fmt.Fprintf(os.Stderr, "\n=== Collision Detection Report ===\n")
				fmt.Fprintf(os.Stderr, "COLLISIONS DETECTED: %d\n", result.Collisions)
				fmt.Fprintf(os.Stderr, "Colliding issue IDs: %v\n", result.CollisionIDs)
			} else if !result.PrefixMismatch {
				fmt.Fprintf(os.Stderr, "No collisions detected.\n")
			}
			msg := fmt.Sprintf("Would create %d new issues, update %d existing issues", result.Created, result.Updated)
			if result.Unchanged > 0 {
				msg += fmt.Sprintf(", %d unchanged", result.Unchanged)
			}
			fmt.Fprintf(os.Stderr, "%s\n", msg)
			fmt.Fprintf(os.Stderr, "\nDry-run mode: no changes made\n")
			os.Exit(0)
		}

		// Print remapping report if collisions were resolved
		if len(result.IDMapping) > 0 {
			fmt.Fprintf(os.Stderr, "\n=== Remapping Report ===\n")
			fmt.Fprintf(os.Stderr, "Issues remapped: %d\n\n", len(result.IDMapping))

			// Sort by old ID for consistent output
			type mapping struct {
				oldID string
				newID string
			}
			mappings := make([]mapping, 0, len(result.IDMapping))
			for oldID, newID := range result.IDMapping {
				mappings = append(mappings, mapping{oldID, newID})
			}
			slices.SortFunc(mappings, func(a, b mapping) int {
				return cmp.Compare(a.oldID, b.oldID)
			})

			fmt.Fprintf(os.Stderr, "Remappings:\n")
			for _, m := range mappings {
				fmt.Fprintf(os.Stderr, "  %s → %s\n", m.oldID, m.newID)
			}
			fmt.Fprintf(os.Stderr, "\nAll text and dependency references have been updated.\n")
		}

		// Flush immediately after import (no debounce) to ensure daemon sees changes
		// Without this, daemon FileWatcher won't detect the import for up to 30s
		// Only flush if there were actual changes to avoid unnecessary I/O
		if result.Created > 0 || result.Updated > 0 || len(result.IDMapping) > 0 {
			flushToJSONLWithState(flushState{forceDirty: true})
		}

		// Update jsonl_content_hash metadata to enable content-based staleness detection
		// This prevents git operations from resurrecting deleted issues by comparing content instead of mtime
		// ALWAYS update metadata after successful import, even if no changes were made (fixes staleness check)
		// This ensures that running `bd import` marks the database as fresh for staleness detection
		// Renamed from last_import_hash - more accurate since updated on both import AND export
		if input != "" {
			if currentHash, err := computeJSONLHash(input); err == nil {
				if err := store.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
					// Non-fatal warning: Metadata update failures are intentionally non-fatal to prevent blocking
					// successful imports. System degrades gracefully to mtime-based staleness detection if metadata
					// is unavailable. This ensures import operations always succeed even if metadata storage fails.
					debug.Logf("Warning: failed to update jsonl_content_hash: %v", err)
				}
				// Also update jsonl_file_hash to prevent integrity check warnings
				// validateJSONLIntegrity() compares this hash against actual JSONL content.
				// Without this, sync that imports but skips re-export leaves jsonl_file_hash stale,
				// causing spurious "hash mismatch" warnings on subsequent operations.
				if err := store.SetJSONLFileHash(ctx, currentHash); err != nil {
					debug.Logf("Warning: failed to update jsonl_file_hash: %v", err)
				}
				// Use RFC3339Nano for nanosecond precision to avoid race with file mtime (fixes #399)
				importTime := time.Now().Format(time.RFC3339Nano)
				if err := store.SetMetadata(ctx, "last_import_time", importTime); err != nil {
					// Non-fatal warning (see above comment about graceful degradation)
					debug.Logf("Warning: failed to update last_import_time: %v", err)
				}
				// Note: mtime tracking removed in bd-v0y fix (git doesn't preserve mtime)
			} else {
				debug.Logf("Warning: failed to read JSONL for hash update: %v", err)
			}
		}

		// Update database mtime to reflect it's now in sync with JSONL
		// This is CRITICAL even when import found 0 changes, because:
		// 1. Import validates DB and JSONL are in sync (no content divergence)
		// 2. Without mtime update, bd sync refuses to export (thinks JSONL is newer)
		// 3. This can happen after git pull updates JSONL mtime but content is identical
		// Fix for: refusing to export: JSONL is newer than database (import first to avoid data loss)
		if err := TouchDatabaseFile(dbPath, input); err != nil {
			debug.Logf("Warning: failed to update database mtime: %v", err)
		}

		// Print summary
		fmt.Fprintf(os.Stderr, "Import complete: %d created, %d updated", result.Created, result.Updated)
		if result.Unchanged > 0 {
			fmt.Fprintf(os.Stderr, ", %d unchanged", result.Unchanged)
		}
		if result.Skipped > 0 {
			fmt.Fprintf(os.Stderr, ", %d skipped", result.Skipped)
		}
		if len(result.IDMapping) > 0 {
			fmt.Fprintf(os.Stderr, ", %d issues remapped", len(result.IDMapping))
		}
		fmt.Fprintf(os.Stderr, "\n")

		// Print skipped dependencies summary if any
		if len(result.SkippedDependencies) > 0 {
			fmt.Fprintf(os.Stderr, "\n⚠️  Warning: Skipped %d dependencies due to missing references:\n", len(result.SkippedDependencies))
			for _, dep := range result.SkippedDependencies {
				fmt.Fprintf(os.Stderr, "  - %s\n", dep)
			}
			fmt.Fprintf(os.Stderr, "\nThis can happen after merges that delete issues referenced by other issues.\n")
			fmt.Fprintf(os.Stderr, "The import continued successfully - you may want to review the skipped dependencies.\n")
		}

		// Print force message if metadata was updated despite no changes
		if force && result.Created == 0 && result.Updated == 0 && len(result.IDMapping) == 0 {
			fmt.Fprintf(os.Stderr, "Metadata updated (database already in sync with JSONL)\n")
		}

		// Run duplicate detection if requested
		if dedupeAfter {
			fmt.Fprintf(os.Stderr, "\n=== Post-Import Duplicate Detection ===\n")

			// Get all issues (fresh after import)
			allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issues for deduplication: %v\n", err)
				os.Exit(1)
			}

			duplicateGroups := findDuplicateGroups(allIssues)
			if len(duplicateGroups) == 0 {
				fmt.Fprintf(os.Stderr, "No duplicates found.\n")
				return
			}

			refCounts := countReferences(allIssues)

			fmt.Fprintf(os.Stderr, "Found %d duplicate group(s)\n\n", len(duplicateGroups))

			for i, group := range duplicateGroups {
				target := chooseMergeTarget(group, refCounts)
				fmt.Fprintf(os.Stderr, "Group %d: %s\n", i+1, group[0].Title)

				for _, issue := range group {
					refs := refCounts[issue.ID]
					marker := "  "
					if issue.ID == target.ID {
						marker = "→ "
					}
					fmt.Fprintf(os.Stderr, "  %s%s (%s, P%d, %d refs)\n",
						marker, issue.ID, issue.Status, issue.Priority, refs)
				}

				sources := make([]string, 0, len(group)-1)
				for _, issue := range group {
					if issue.ID != target.ID {
						sources = append(sources, issue.ID)
					}
				}
				fmt.Fprintf(os.Stderr, "  Suggested: bd merge %s --into %s\n\n",
					strings.Join(sources, " "), target.ID)
			}

			fmt.Fprintf(os.Stderr, "Run 'bd duplicates --auto-merge' to merge all duplicates.\n")
		}
	},
}

// TouchDatabaseFile updates the modification time of the database file.
// This is used after import AND export to ensure the database appears "in sync" with JSONL,
// preventing bd doctor and validatePreExport from incorrectly warning that JSONL is newer.
//
// In SQLite WAL mode, writes go to beads.db-wal and beads.db mtime may not update
// until a checkpoint. Since validation compares JSONL mtime to beads.db mtime only,
// we need to explicitly touch the DB file after both import and export operations.
//
// The function sets DB mtime to max(JSONL mtime, now) + 1ns to handle clock skew.
// If jsonlPath is empty or can't be read, falls back to time.Now().
//
// Fixes issues #278, #301, #321: daemon export leaving JSONL newer than DB.
func TouchDatabaseFile(dbPath, jsonlPath string) error {
	targetTime := time.Now()

	// If we have the JSONL path, use max(JSONL mtime, now) to handle clock skew
	// Use Lstat to get the symlink's own mtime, not the target's (NixOS fix).
	if jsonlPath != "" {
		if info, err := os.Lstat(jsonlPath); err == nil {
			jsonlTime := info.ModTime()
			if jsonlTime.After(targetTime) {
				targetTime = jsonlTime.Add(time.Nanosecond)
			}
		}
	}

	// Best-effort touch - don't fail import if this doesn't work
	return os.Chtimes(dbPath, targetTime, targetTime)
}

// checkUncommittedChanges detects if the JSONL file has uncommitted changes
// and warns the user if the working tree differs from git HEAD
func checkUncommittedChanges(filePath string, result *ImportResult) {
	// Only warn if no actual changes were made (database already synced)
	if result.Created > 0 || result.Updated > 0 {
		return
	}

	// Get the directory containing the file to use as git working directory
	workDir := filepath.Dir(filePath)

	// Use git diff to check if working tree differs from HEAD
	cmd := fmt.Sprintf("git diff --quiet HEAD %s", filePath)
	exitCode, _ := runGitCommand(cmd, workDir)

	// Exit code 0 = no changes, 1 = changes exist, >1 = error
	if exitCode == 1 {
		// Get line counts for context
		workingTreeLines := countLines(filePath)
		headLines := countLinesInGitHEAD(filePath, workDir)
		
		fmt.Fprintf(os.Stderr, "\n⚠️  Warning: %s has uncommitted changes\n", filePath)
		fmt.Fprintf(os.Stderr, "   Working tree: %d lines\n", workingTreeLines)
		if headLines > 0 {
			fmt.Fprintf(os.Stderr, "   Git HEAD: %d lines\n", headLines)
		}
		fmt.Fprintf(os.Stderr, "\n   Import complete: database already synced with working tree\n")
		fmt.Fprintf(os.Stderr, "   Run: git diff %s\n", filePath)
		fmt.Fprintf(os.Stderr, "   To review uncommitted changes\n")
	}
}

// runGitCommand executes a git command and returns exit code and output
// workDir is the directory to run the command in (empty = current dir)
func runGitCommand(cmd string, workDir string) (int, string) {
	// #nosec G204 - command is constructed internally
	gitCmd := exec.Command("sh", "-c", cmd)
	if workDir != "" {
		gitCmd.Dir = workDir
	}
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(output)
		}
		return -1, string(output)
	}
	return 0, string(output)
}

// countLines counts the number of lines in a file
func countLines(filePath string) int {
	// #nosec G304 - file path is controlled by caller
	f, err := os.Open(filePath)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	return lines
}

// countLinesInGitHEAD counts lines in the file as it exists in git HEAD
func countLinesInGitHEAD(filePath string, workDir string) int {
	// First, find the git root
	findRootCmd := "git rev-parse --show-toplevel 2>/dev/null"
	exitCode, gitRootOutput := runGitCommand(findRootCmd, workDir)
	if exitCode != 0 {
		return 0
	}
	gitRoot := strings.TrimSpace(gitRootOutput)

	// Make filePath relative to git root
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return 0
	}

	relPath, err := filepath.Rel(gitRoot, absPath)
	if err != nil {
		return 0
	}

	cmd := fmt.Sprintf("git show HEAD:%s 2>/dev/null | wc -l", relPath)
	exitCode, output := runGitCommand(cmd, workDir)
	if exitCode != 0 {
		return 0
	}

	var lines int
	_, err = fmt.Sscanf(strings.TrimSpace(output), "%d", &lines)
	if err != nil {
		return 0
	}
	return lines
}

// attemptAutoMerge attempts to resolve git conflicts using bd merge 3-way merge
func attemptAutoMerge(conflictedPath string) error {
	// Validate inputs
	if conflictedPath == "" {
		return fmt.Errorf("no file path provided for merge")
	}

	// Get git repository root
	gitRootCmd := exec.Command("git", "rev-parse", "--show-toplevel") // #nosec G204 -- fixed git invocation for repo root discovery
	gitRootOutput, err := gitRootCmd.Output()
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	gitRoot := strings.TrimSpace(string(gitRootOutput))

	// Convert conflicted path to absolute path relative to git root
	absConflictedPath := conflictedPath
	if !filepath.IsAbs(conflictedPath) {
		absConflictedPath = filepath.Join(gitRoot, conflictedPath)
	}

	// Get base (merge-base), left (ours/HEAD), and right (theirs/MERGE_HEAD) versions
	// These are the three inputs needed for 3-way merge

	// Extract relative path from git root for git commands
	relPath, err := filepath.Rel(gitRoot, absConflictedPath)
	if err != nil {
		relPath = conflictedPath
	}

	// Create temp directory for merge artifacts
	tmpDir, err := os.MkdirTemp("", "bd-merge-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	basePath := filepath.Join(tmpDir, "base.jsonl")
	leftPath := filepath.Join(tmpDir, "left.jsonl")
	rightPath := filepath.Join(tmpDir, "right.jsonl")
	outputPath := filepath.Join(tmpDir, "merged.jsonl")

	// Extract base version (merge-base)
	baseCmd := exec.Command("git", "show", fmt.Sprintf(":1:%s", relPath)) // #nosec G204 -- relPath limited to files tracked in current repo
	baseCmd.Dir = gitRoot
	baseContent, err := baseCmd.Output()
	if err != nil {
		// Stage 1 might not exist if file was added in both branches
		// Create empty base in this case
		baseContent = []byte{}
	}
	if err := os.WriteFile(basePath, baseContent, 0600); err != nil {
		return fmt.Errorf("failed to write base version: %w", err)
	}

	// Extract left version (ours/HEAD)
	leftCmd := exec.Command("git", "show", fmt.Sprintf(":2:%s", relPath)) // #nosec G204 -- relPath limited to files tracked in current repo
	leftCmd.Dir = gitRoot
	leftContent, err := leftCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract 'ours' version: %w", err)
	}
	if err := os.WriteFile(leftPath, leftContent, 0600); err != nil {
		return fmt.Errorf("failed to write left version: %w", err)
	}

	// Extract right version (theirs/MERGE_HEAD)
	rightCmd := exec.Command("git", "show", fmt.Sprintf(":3:%s", relPath)) // #nosec G204 -- relPath limited to files tracked in current repo
	rightCmd.Dir = gitRoot
	rightContent, err := rightCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract 'theirs' version: %w", err)
	}
	if err := os.WriteFile(rightPath, rightContent, 0600); err != nil {
		return fmt.Errorf("failed to write right version: %w", err)
	}

	// Get current executable to call bd merge
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve current executable: %w", err)
	}

	// Invoke bd merge command
	mergeCmd := exec.Command(exe, "merge", outputPath, basePath, leftPath, rightPath) // #nosec G204 -- executes current bd binary for deterministic merge
	mergeOutput, err := mergeCmd.CombinedOutput()
	if err != nil {
		// Check exit code - bd merge returns 1 if there are conflicts, 2 for errors
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				// Conflicts exist - merge tool did its best but couldn't resolve everything
				return fmt.Errorf("merge conflicts could not be automatically resolved:\n%s", mergeOutput)
			}
		}
		return fmt.Errorf("merge command failed: %w\n%s", err, mergeOutput)
	}

	// Merge succeeded - copy merged result back to original file
	// #nosec G304 -- merged output created earlier in this function
	mergedContent, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("failed to read merged output: %w", err)
	}

	if err := os.WriteFile(absConflictedPath, mergedContent, 0600); err != nil {
		return fmt.Errorf("failed to write merged result: %w", err)
	}

	// Stage the resolved file
	stageCmd := exec.Command("git", "add", relPath) // #nosec G204 -- relPath constrained to file within current repo
	stageCmd.Dir = gitRoot
	if err := stageCmd.Run(); err != nil {
		// Non-fatal - user can stage manually
		fmt.Fprintf(os.Stderr, "Warning: failed to auto-stage merged file: %v\n", err)
	}

	return nil
}

// detectPrefixFromIssues extracts the common prefix from issue IDs
// Uses utils.ExtractIssuePrefix which handles multi-part prefixes correctly
func detectPrefixFromIssues(issues []*types.Issue) string {
	if len(issues) == 0 {
		return ""
	}

	// Count prefix occurrences
	prefixCounts := make(map[string]int)
	for _, issue := range issues {
		prefix := utils.ExtractIssuePrefix(issue.ID)
		if prefix != "" {
			prefixCounts[prefix]++
		}
	}

	// Find most common prefix
	maxCount := 0
	commonPrefix := ""
	for prefix, count := range prefixCounts {
		if count > maxCount {
			maxCount = count
			commonPrefix = prefix
		}
	}

	return commonPrefix
}

func init() {
	importCmd.Flags().StringP("input", "i", "", "Input file (default: stdin)")
	importCmd.Flags().BoolP("skip-existing", "s", false, "Skip existing issues instead of updating them")
	importCmd.Flags().Bool("strict", false, "Fail on dependency errors instead of treating them as warnings")
	importCmd.Flags().Bool("dedupe-after", false, "Detect and report content duplicates after import")
	importCmd.Flags().Bool("dry-run", false, "Preview collision detection without making changes")
	importCmd.Flags().Bool("rename-on-import", false, "Rename imported issues to match database prefix (updates all references)")
	importCmd.Flags().Bool("clear-duplicate-external-refs", false, "Clear duplicate external_ref values (keeps first occurrence)")
	importCmd.Flags().String("orphan-handling", "", "How to handle missing parent issues: strict/resurrect/skip/allow (default: use config or 'allow')")
	importCmd.Flags().Bool("force", false, "Force metadata update even when database is already in sync with JSONL")
	importCmd.Flags().Bool("protect-left-snapshot", false, "Protect issues in left snapshot from git-history-backfill")
	importCmd.Flags().Bool("no-git-history", false, "Skip git history backfill for deletions (passed by bd sync)")
	importCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output import statistics in JSON format")
	rootCmd.AddCommand(importCmd)
}
