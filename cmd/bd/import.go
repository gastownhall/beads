package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var importCmd = &cobra.Command{
	Use:   "import [file|-]",
	Short: "Import issues from a JSONL file or stdin into the database",
	Long: `Import issues from a JSONL file (newline-delimited JSON) into the database.

If no file is specified, imports from the configured import.path under .beads/
(default: issues.jsonl). Use "-" to read from stdin. This is the incremental counterpart to
'bd export': new issues are created and existing issues are updated (upsert
semantics).

Memory records (lines with "_type":"memory") are automatically detected and
imported as persistent memories (equivalent to 'bd remember'). This makes
'bd export | bd import' a full round-trip for both issues and memories.

Each JSONL line should map to an issue. The importer accepts every field
'bd export' emits — see 'bd export' output for the canonical schema. Only
"title" is required; everything else is optional.

Common fields:
  title                  Required. Short summary.
  description            Long-form body.
  design, notes,         Additional content sections.
    acceptance_criteria
  issue_type             bug | feature | task | epic | chore | ...
  priority               0-4 (0 = critical). 0 is preserved (no omitempty).
  status                 open | in_progress | blocked | closed | ...
                         (rows with status "tombstone" are skipped)
  assignee, owner,       Ownership metadata.
    created_by
  labels                 Array of strings.
  dependencies           Array of {issue_id, depends_on_id, type, ...}.
  comments               Array of comment objects.
  external_ref,          Cross-system identifiers (e.g. "gh-9").
    source_system
  due_at, defer_until    RFC3339 timestamps for scheduling.
  metadata               Arbitrary JSON object preserved verbatim.

Timestamps (created_at, updated_at, started_at, closed_at) are preserved
when present in the JSONL and otherwise filled in by the importer. The
legacy "wisp" boolean is accepted as an alias for "ephemeral".

By default a row only rewrites an existing local issue when its
updated_at is strictly newer. Older rows are skipped (reported as
stale_skipped_ids) and rows with the same updated_at keep every local
column — updated_at has second granularity, so a timestamp tie can be
two distinct same-second updates, and the local row wins the tie
(reported as tie_kept_local_ids; the row's labels/comments/dependencies
still merge). The guard is also enforced inside the upsert itself, so a
local update that lands while the import is running is preserved rather
than overwritten. Existing issues that the import did rewrite are listed
with a field-level summary (updated_issues), so local state changed by
an import is visible. To deliberately restore an older snapshot, pass
--allow-stale, which imports every row even when it overwrites newer
local state.

EXAMPLES:
  bd import                        # Import from configured import.path
  bd import backup.jsonl           # Import from a specific file
  bd import -i backup.jsonl        # Legacy alias for a specific file
  bd import -                      # Read JSONL from stdin
  cat issues.jsonl | bd import -   # Pipe JSONL from another tool
  bd import --dry-run              # Show what would be imported
  bd import --dedup                # Skip issues with duplicate titles
  bd import --allow-stale old.jsonl # Restore an older snapshot (overwrites newer local rows)
  bd import --json                 # Structured output with created and skipped IDs`,
	GroupID:       "sync",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runImport,
}

var (
	importDryRun     bool
	importDedup      bool
	importAllowStale bool
	importInput      string
)

func init() {
	importCmd.Flags().StringVarP(&importInput, "input", "i", "", "Read JSONL from a specific file")
	importCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Show what would be imported without importing")
	importCmd.Flags().BoolVar(&importDedup, "dedup", false, "Skip lines whose title matches an existing open issue")
	importCmd.Flags().BoolVar(&importAllowStale, "allow-stale", false, "Import rows even when older than the local issue (required to restore an older snapshot)")
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("import")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if err := runImportInner(args); err != nil {
		if _, isExit := err.(*exitError); isExit {
			return err
		}
		return HandleErrorRespectJSON("%v", err)
	}
	return nil
}

func runImportInner(args []string) error {
	ctx := rootCtx
	if importInput != "" && len(args) > 0 {
		return fmt.Errorf("use either --input or a positional file, not both")
	}

	fromStdin := importInput == "-" || (len(args) > 0 && args[0] == "-")

	if fromStdin {
		return runImportFromReader(ctx, os.Stdin, "stdin")
	}

	// Determine source file
	var jsonlPath string
	if importInput != "" {
		jsonlPath = importInput
	} else if len(args) > 0 {
		jsonlPath = args[0]
	} else {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return fmt.Errorf("%s — %s", activeWorkspaceNotFoundError(), diagHint())
		}
		if globalFlag {
			jsonlPath = filepath.Join(beadsDir, "global-issues.jsonl")
		} else {
			jsonlPath = configuredImportJSONLPath(beadsDir)
		}
	}

	info, err := os.Stat(jsonlPath)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", jsonlPath, err)
	}
	if info.Size() == 0 {
		if jsonOutput {
			return outputJSON(importResultJSON{Source: jsonlPath})
		}
		fmt.Fprintf(os.Stderr, "Empty file: %s\n", jsonlPath)
		return nil
	}

	f, err := os.Open(jsonlPath) //nolint:gosec // G304: CLI argument
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", jsonlPath, err)
	}
	defer f.Close()

	return runImportFromReader(ctx, f, jsonlPath)
}

type importResultJSON struct {
	Source              string         `json:"source"`
	Created             int            `json:"created"`
	Updated             int            `json:"updated,omitempty"`
	Skipped             int            `json:"skipped"`
	DedupHits           int            `json:"dedup_skipped,omitempty"`
	Memories            int            `json:"memories,omitempty"`
	IDs                 []string       `json:"ids,omitempty"`
	UpdatedIssues       []ImportChange `json:"updated_issues,omitempty"`
	TieKeptLocalIDs     []string       `json:"tie_kept_local_ids,omitempty"`
	StaleSkippedIDs     []string       `json:"stale_skipped_ids,omitempty"`
	SkippedDependencies []string       `json:"skipped_dependencies,omitempty"`
	DryRun              bool           `json:"dry_run,omitempty"`
}

func runImportFromReader(ctx context.Context, r io.Reader, source string) error {
	if store == nil {
		return fmt.Errorf("no database — run 'bd init' or 'bd bootstrap' first")
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	var issues []*types.Issue
	var memories []memoryRecord

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var peek map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			return fmt.Errorf("failed to parse JSONL line: %w", err)
		}

		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal([]byte(line), &mem); err != nil {
					return fmt.Errorf("failed to parse memory record: %w", err)
				}
				if mem.Key != "" && mem.Value != "" {
					memories = append(memories, mem)
				}
				continue
			}
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return fmt.Errorf("failed to parse issue from JSONL: %w", err)
		}
		if issue.Status == "tombstone" {
			continue
		}
		if _, hasWisp := peek["wisp"]; hasWisp && !issue.Ephemeral {
			var wisp bool
			if err := json.Unmarshal(peek["wisp"], &wisp); err == nil && wisp {
				issue.Ephemeral = true
			}
		}
		issue.SetDefaults()
		issues = append(issues, &issue)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to scan JSONL: %w", err)
	}

	// Dedup: skip issues whose title matches an existing open issue
	dedupHits := 0
	if importDedup && len(issues) > 0 {
		issues, dedupHits = filterDuplicatesByTitle(ctx, store, issues)
	}

	result := importResultJSON{
		Source:    source,
		DedupHits: dedupHits,
		DryRun:    importDryRun,
	}

	if importDryRun {
		result.Created = len(issues)
		result.Memories = len(memories)
		result.Skipped = dedupHits
		if jsonOutput {
			return outputJSON(result)
		}
		fmt.Fprintf(os.Stderr, "Would import %d issues and %d memories from %s", len(issues), len(memories), source)
		if dedupHits > 0 {
			fmt.Fprintf(os.Stderr, " (%d duplicates skipped)", dedupHits)
		}
		fmt.Fprintln(os.Stderr)
		return nil
	}

	// Import memories
	for _, mem := range memories {
		storageKey := kvPrefix + memoryPrefix + mem.Key
		if err := store.SetConfig(ctx, storageKey, mem.Value); err != nil {
			return fmt.Errorf("failed to import memory %q: %w", mem.Key, err)
		}
		result.Memories++
	}

	// Import issues
	if len(issues) > 0 {
		opts := ImportOptions{SkipPrefixValidation: true, AllowStale: importAllowStale}
		importResult, err := importIssuesCore(ctx, "", store, issues, opts)
		if err != nil {
			return fmt.Errorf("import failed: %w", err)
		}
		result.Created = importResult.Created
		result.Updated = importResult.Updated
		result.Skipped += importResult.Skipped
		result.SkippedDependencies = append(result.SkippedDependencies, importResult.SkippedDependencies...)
		result.IDs = append(result.IDs, importResult.ImportedIDs...)
		result.UpdatedIssues = append(result.UpdatedIssues, importResult.UpdatedIssues...)
		result.TieKeptLocalIDs = append(result.TieKeptLocalIDs, importResult.TieKeptLocalIDs...)
		result.StaleSkippedIDs = append(result.StaleSkippedIDs, importResult.StaleSkippedIDs...)
	}

	if result.Created > 0 || result.Memories > 0 {
		commitMsg := fmt.Sprintf("bd import: %d issues", result.Created)
		if result.Memories > 0 {
			commitMsg += fmt.Sprintf(", %d memories", result.Memories)
		}
		commitMsg += fmt.Sprintf(" from %s", filepath.Base(source))
		if err := store.Commit(ctx, commitMsg); err != nil {
			// An import can be a working-set no-op: re-importing an
			// identical snapshot, or equal-timestamp rows whose guarded
			// upsert kept every local column (bd-hj85c).
			if !strings.Contains(err.Error(), "nothing to commit") {
				return fmt.Errorf("commit: %w", err)
			}
		}
	}

	if jsonOutput {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Imported %d issues", result.Created)
	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, " and %d memories", result.Memories)
	}
	fmt.Fprintf(os.Stderr, " from %s", source)
	if dedupHits > 0 {
		fmt.Fprintf(os.Stderr, " (%d duplicates skipped)", dedupHits)
	}
	if staleSkipped := result.Skipped - dedupHits; staleSkipped > 0 {
		fmt.Fprintf(os.Stderr, " (%d stale skipped; use --allow-stale to restore older rows)", staleSkipped)
	}
	fmt.Fprintln(os.Stderr)
	if len(result.UpdatedIssues) > 0 {
		fmt.Fprintf(os.Stderr, "Updated %d existing issue(s):\n", len(result.UpdatedIssues))
		for _, change := range result.UpdatedIssues {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", change.ID, change.Changes)
		}
	}
	if len(result.TieKeptLocalIDs) > 0 {
		fmt.Fprintf(os.Stderr, "Kept local state for %d issue(s) with the same updated_at but different content (use --allow-stale to overwrite): %s\n",
			len(result.TieKeptLocalIDs), strings.Join(result.TieKeptLocalIDs, ", "))
	}
	for _, skipped := range result.SkippedDependencies {
		fmt.Fprintf(os.Stderr, "Skipped dependency: %s\n", skipped)
	}
	return nil
}

// filterDuplicatesByTitle removes issues whose title matches an existing open issue.
func filterDuplicatesByTitle(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) ([]*types.Issue, int) {
	existing, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return issues, 0
	}

	titleSet := make(map[string]bool, len(existing))
	for _, issue := range existing {
		if issue.Status != types.StatusClosed {
			titleSet[strings.ToLower(issue.Title)] = true
		}
	}

	var kept []*types.Issue
	skipped := 0
	for _, issue := range issues {
		if titleSet[strings.ToLower(issue.Title)] {
			skipped++
			continue
		}
		kept = append(kept, issue)
	}
	return kept, skipped
}
