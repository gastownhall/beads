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
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import issues from JSONL format",
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
		// Ensure database directory exists (auto-create if needed)
		dbDir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dbDir, 0750); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create database directory: %v\n", err)
			os.Exit(1)
		}
		
		// Import requires direct database access due to complex transaction handling
		// and collision detection. Force direct mode regardless of daemon state.
		if daemonClient != nil {
			debug.Logf("Debug: import command forcing direct mode (closes daemon connection)\n")
			_ = daemonClient.Close()
			daemonClient = nil
			
			var err error
			store, err = sqlite.New(dbPath)
			if err != nil {
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
		ctx := context.Background()
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

		allIssues = append(allIssues, &issue)
	}

		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			os.Exit(1)
		}

		// Check if database needs initialization (prefix not set)
		// Detect prefix from the imported issues
		initCtx := context.Background()
		configuredPrefix, err2 := store.GetConfig(initCtx, "issue_prefix")
		if err2 != nil || strings.TrimSpace(configuredPrefix) == "" {
			// Database exists but not initialized - detect prefix from issues
			detectedPrefix := detectPrefixFromIssues(allIssues)
			if detectedPrefix == "" {
				// No issues to import or couldn't detect prefix, use directory name
				cwd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: failed to get current directory: %v\n", err)
					os.Exit(1)
				}
				detectedPrefix = filepath.Base(cwd)
			}
			detectedPrefix = strings.TrimRight(detectedPrefix, "-")
			
			if err := store.SetConfig(initCtx, "issue_prefix", detectedPrefix); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to set issue prefix: %v\n", err)
				os.Exit(1)
			}
			
			fmt.Fprintf(os.Stderr, "✓ Initialized database with prefix '%s' (detected from issues)\n", detectedPrefix)
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
			sort.Slice(mappings, func(i, j int) bool {
				return mappings[i].oldID < mappings[j].oldID
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
			flushToJSONL()
		}

		// Update database mtime to reflect it's now in sync with JSONL
		// This is CRITICAL even when import found 0 changes, because:
		// 1. Import validates DB and JSONL are in sync (no content divergence)
		// 2. Without mtime update, bd sync refuses to export (thinks JSONL is newer)
		// 3. This can happen after git pull updates JSONL mtime but content is identical
		// Fix for: refusing to export: JSONL is newer than database (import first to avoid data loss)
		if err := touchDatabaseFile(dbPath, input); err != nil {
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

// touchDatabaseFile updates the modification time of the database file.
// This is used after import to ensure the database appears "in sync" with JSONL,
// preventing bd doctor from incorrectly warning that JSONL is newer.
//
// In SQLite WAL mode, writes go to beads.db-wal and beads.db mtime may not update
// until a checkpoint. Since bd doctor compares JSONL mtime to beads.db mtime only,
// we need to explicitly touch the DB file after import.
//
// The function sets DB mtime to max(JSONL mtime, now) + 1ns to handle clock skew.
// If jsonlPath is empty or can't be read, falls back to time.Now().
func touchDatabaseFile(dbPath, jsonlPath string) error {
	targetTime := time.Now()
	
	// If we have the JSONL path, use max(JSONL mtime, now) to handle clock skew
	if jsonlPath != "" {
		if info, err := os.Stat(jsonlPath); err == nil {
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
		
		fmt.Fprintf(os.Stderr, "\n⚠️  Warning: .beads/issues.jsonl has uncommitted changes\n")
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
	gitRootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
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
	baseCmd := exec.Command("git", "show", fmt.Sprintf(":1:%s", relPath))
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
	leftCmd := exec.Command("git", "show", fmt.Sprintf(":2:%s", relPath))
	leftCmd.Dir = gitRoot
	leftContent, err := leftCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract 'ours' version: %w", err)
	}
	if err := os.WriteFile(leftPath, leftContent, 0600); err != nil {
		return fmt.Errorf("failed to write left version: %w", err)
	}

	// Extract right version (theirs/MERGE_HEAD)
	rightCmd := exec.Command("git", "show", fmt.Sprintf(":3:%s", relPath))
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
	mergeCmd := exec.Command(exe, "merge", outputPath, basePath, leftPath, rightPath)
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
	mergedContent, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("failed to read merged output: %w", err)
	}

	if err := os.WriteFile(absConflictedPath, mergedContent, 0600); err != nil {
		return fmt.Errorf("failed to write merged result: %w", err)
	}

	// Stage the resolved file
	stageCmd := exec.Command("git", "add", relPath)
	stageCmd.Dir = gitRoot
	if err := stageCmd.Run(); err != nil {
		// Non-fatal - user can stage manually
		fmt.Fprintf(os.Stderr, "Warning: failed to auto-stage merged file: %v\n", err)
	}

	return nil
}

// detectPrefixFromIssues extracts the common prefix from issue IDs
// Only considers the first hyphen, so "vc-baseline-test" -> "vc"
func detectPrefixFromIssues(issues []*types.Issue) string {
	if len(issues) == 0 {
		return ""
	}
	
	// Count prefix occurrences
	prefixCounts := make(map[string]int)
	for _, issue := range issues {
		// Extract prefix from issue ID using first hyphen only
		idx := strings.Index(issue.ID, "-")
		if idx > 0 {
			prefixCounts[issue.ID[:idx]]++
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
	importCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output import statistics in JSON format")
	rootCmd.AddCommand(importCmd)
}
