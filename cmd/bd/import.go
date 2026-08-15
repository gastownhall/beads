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
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

var importCmd = &cobra.Command{
	Use:   "import [file|-]",
	Short: "Import issues from a JSONL file or stdin into the database",
	Long: `Import issues from a JSONL file (newline-delimited JSON) into the database.

If no file is specified, imports from the configured import.path under .beads/
(default: issues.jsonl). Use "-" to read from stdin; redirecting stdin without
"-" or a file argument is an error, so a typo'd 'bd import < file' cannot
silently import the default file instead. This is the incremental counterpart to
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

Large imports are written in bounded transactions (a few hundred issues
each, with a short pause between commits) with progress on stderr, so
concurrent bd commands keep working while the import runs instead of
stalling on one batch-wide write lock. Rows land in dependency order
with their blocking edges in the same transaction, so a half-finished
import never shows a blocked issue as ready. If an import fails partway,
the already-committed chunks are durable and the command exits nonzero;
re-running the same import is safe and converges (rows upsert,
labels/comments/dependencies deduplicate).

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
		// bd-axluy: `bd import < file` (or `... | bd import`) without "-"
		// used to silently ignore stdin and import the default JSONL — a
		// mutating command diverging from what the user piped. Demand an
		// explicit source instead. /dev/null (the stdin subprocesses get by
		// default) is a character device, so scripted bare `bd import` with
		// no redirection still works.
		if fi, statErr := os.Stdin.Stat(); statErr == nil && fi.Mode()&os.ModeCharDevice == 0 {
			return fmt.Errorf("stdin is redirected, but without \"-\" bd import ignores it and imports the default JSONL instead; use 'bd import -' to import what you piped, or name a file explicitly")
		}
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
	Unchanged           int            `json:"unchanged,omitempty"`
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
	issues, memories, err := parseImportRecords(r)
	if err != nil {
		return err
	}

	if usesProxiedServer() {
		return runImportRecordsProxied(ctx, issues, memories, source)
	}

	if store == nil {
		return fmt.Errorf("no database — run 'bd init' or 'bd bootstrap' first")
	}
	return runImportRecordsClassic(ctx, issues, memories, source)
}

// parseImportRecords scans one JSONL stream into issue rows and memory
// records — the `bd import` / `bd import -` parse loop, shared by the classic
// and proxied modes. Same record vocabulary as parseJSONLFile (the bootstrap
// reader): the optional _schema header and tombstones are skipped, and the
// "wisp_plane" boolean is honored as the explicit wisps-plane marker (and
// the legacy "wisp" alias for "ephemeral") via applyImportWispPlane.
func parseImportRecords(r io.Reader) ([]*types.Issue, []memoryRecord, error) {
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
			return nil, nil, fmt.Errorf("failed to parse JSONL line: %w", err)
		}

		// Skip the optional beads-jsonl header record (§J1.3). A canonical
		// export may prepend a provenance line, e.g.
		// {"_schema":"beads-jsonl/1","_dolt_branch":"main","_sort":"stable-v1"}.
		// It carries no _type and no issue fields; without this guard it falls
		// through to the issue path, unmarshals into an empty Issue, and aborts
		// the whole import with "title is required". parseJSONLFile (the
		// bootstrap reader) has always skipped it; this loop — the one `bd
		// import` and `bd import -` run through — did not.
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal([]byte(line), &mem); err != nil {
					return nil, nil, fmt.Errorf("failed to parse memory record: %w", err)
				}
				if mem.Key != "" && mem.Value != "" {
					memories = append(memories, mem)
				}
				continue
			}
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, nil, fmt.Errorf("failed to parse issue from JSONL: %w", err)
		}
		if issue.Status == "tombstone" {
			continue
		}
		applyImportWispPlane(peek, &issue)
		issue.SetDefaults()
		issues = append(issues, &issue)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to scan JSONL: %w", err)
	}
	return issues, memories, nil
}

// runImportRecordsClassic is the classic (embedded/direct store) import
// pipeline over the parsed records: dedup, dry-run classification, memory
// writes, the batch issue import, the final commit and the issue_prefix
// reconciliation.
func runImportRecordsClassic(ctx context.Context, issues []*types.Issue, memories []memoryRecord, source string) error {
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
		result.Memories = len(memories)
		result.Skipped = dedupHits

		classification, err := classifyDryRunImport(ctx, store, issues, importAllowStale)
		if err != nil {
			return fmt.Errorf("dry-run: %w", err)
		}
		applyImportDryRunClassification(&result, classification)
		return renderImportDryRun(result, len(memories), source, dedupHits)
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
		applyImportOutcome(&result, importResult)
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

	// Sync issue_prefix from config.yaml to the database if stale (be-llaf).
	// store.Commit skips the config table (GH#2455), so we use CommitWithConfig
	// for this intentional config update after the issues commit completes.
	// config.yaml is authoritative here and existing issue IDs are intentionally
	// left unchanged: this deliberately bypasses the `bd config set issue_prefix`
	// guard for the import/migration flow and is not a rename.
	if yamlPrefix := config.GetString("issue-prefix"); yamlPrefix != "" {
		if dbPrefix, _ := store.GetConfig(ctx, "issue_prefix"); dbPrefix != yamlPrefix {
			if setErr := store.SetConfig(ctx, "issue_prefix", yamlPrefix); setErr == nil {
				_ = store.CommitWithConfig(ctx, "bd import: sync issue_prefix from config.yaml")
			}
		}
	}

	return renderImportOutcome(result, source, dedupHits)
}

// applyImportDryRunClassification folds a dry-run classification into the
// command's JSON result, identically in both modes.
func applyImportDryRunClassification(result *importResultJSON, classification *ImportResult) {
	result.Created = classification.Created
	result.Updated = classification.Updated
	result.Unchanged = classification.Unchanged
	result.Skipped += classification.Skipped
	result.IDs = append(result.IDs, classification.ImportedIDs...)
	result.StaleSkippedIDs = classification.StaleSkippedIDs
	result.UpdatedIssues = classification.UpdatedIssues
	result.TieKeptLocalIDs = classification.TieKeptLocalIDs
}

// applyImportOutcome folds a real import's outcome into the command's JSON
// result, identically in both modes.
func applyImportOutcome(result *importResultJSON, importResult *ImportResult) {
	result.Created = importResult.Created
	result.Updated = importResult.Updated
	result.Skipped += importResult.Skipped
	result.SkippedDependencies = append(result.SkippedDependencies, importResult.SkippedDependencies...)
	result.IDs = append(result.IDs, importResult.ImportedIDs...)
	result.UpdatedIssues = append(result.UpdatedIssues, importResult.UpdatedIssues...)
	result.TieKeptLocalIDs = append(result.TieKeptLocalIDs, importResult.TieKeptLocalIDs...)
	result.StaleSkippedIDs = append(result.StaleSkippedIDs, importResult.StaleSkippedIDs...)
}

// renderImportDryRun reports a dry run (JSON or stderr), identically in both
// modes.
func renderImportDryRun(result importResultJSON, memoriesCount int, source string, dedupHits int) error {
	if jsonOutput {
		return outputJSON(result)
	}
	// The leading count is the sum of the breakdown that follows it
	// (not len(issues)), which can be larger when rows were stale
	// skipped — those are reported separately below instead of being
	// folded into a total the breakdown then wouldn't add up to.
	considered := result.Created + result.Updated + result.Unchanged
	//nolint:gosec // G705: stderr, not a browser context
	fmt.Fprintf(os.Stderr, "Would import %d issues (%d new, %d updated, %d unchanged) and %d memories from %s",
		considered, result.Created, result.Updated, result.Unchanged, memoriesCount, source)
	if dedupHits > 0 {
		fmt.Fprintf(os.Stderr, " (%d duplicates skipped)", dedupHits) //nolint:gosec // G705: stderr, not a browser context
	}
	if len(result.StaleSkippedIDs) > 0 {
		fmt.Fprintf(os.Stderr, " (%d stale skipped)", len(result.StaleSkippedIDs))
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// renderImportOutcome reports a completed import (JSON or stderr),
// identically in both modes.
func renderImportOutcome(result importResultJSON, source string, dedupHits int) error {
	if jsonOutput {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Imported %d issues", result.Created)
	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, " and %d memories", result.Memories)
	}
	fmt.Fprintf(os.Stderr, " from %s", source)
	if dedupHits > 0 {
		fmt.Fprintf(os.Stderr, " (%d duplicates skipped)", dedupHits) //nolint:gosec // G705: stderr, not a browser context
	}
	if staleSkipped := result.Skipped - dedupHits; staleSkipped > 0 {
		fmt.Fprintf(os.Stderr, " (%d stale skipped; use --allow-stale to restore older rows)", staleSkipped) //nolint:gosec // G705: stderr, not a browser context
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

// importTitleSearcher is the read seam the --dedup filter needs. It lives in
// THIS file because naming types.IssueFilter is denied by default under
// cmd/bd and import.go is the named exception for the bulk-movement family
// (.golangci.yml, forbidigo): the classic storage.DoltStorage satisfies it
// directly, and uowImportTitleSearcher adapts the proxied unit of work.
type importTitleSearcher interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}

// uowImportTitleSearcher adapts a unit of work's issue use case to the
// classic []*types.Issue search shape filterDuplicatesByTitle consumes. Both
// stacks run the same issueops search underneath (issues merged with wisps),
// so --dedup sees the same title universe in both modes.
type uowImportTitleSearcher struct {
	uw uow.UnitOfWork
}

func (s uowImportTitleSearcher) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	page, err := s.uw.IssueUseCase().SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// filterDuplicatesByTitle removes issues whose title matches an existing open issue.
func filterDuplicatesByTitle(ctx context.Context, st importTitleSearcher, issues []*types.Issue) ([]*types.Issue, int) {
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
