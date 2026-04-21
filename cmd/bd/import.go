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
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var importCmd = &cobra.Command{
	Use:   "import [file|-]",
	Short: "Import issues from a JSONL file or stdin into the database",
	Long: `Import issues from a JSONL file (newline-delimited JSON) into the database.

If no file is specified, imports from .beads/issues.jsonl (the git-tracked
export). Use "-" to read from stdin. This is the incremental counterpart to
'bd export': new issues are created and existing issues are updated (upsert
semantics).

Memory records (lines with "_type":"memory") are automatically detected and
imported as persistent memories (equivalent to 'bd remember'). This makes
'bd export | bd import' a full round-trip for both issues and memories.

Each JSONL line should map to an issue with at minimum "title". Optional
fields: description, issue_type (type), priority, acceptance_criteria.

EXAMPLES:
  bd import                        # Import from .beads/issues.jsonl
  bd import backup.jsonl           # Import from a specific file
  bd import -                      # Read JSONL from stdin
  cat issues.jsonl | bd import -   # Pipe JSONL from another tool
  bd import --dry-run              # Show what would be imported
  bd import --dedup                # Skip issues with duplicate titles
  bd import --json                 # Structured output with created IDs`,
	GroupID: "sync",
	RunE:    runImport,
}

var (
	importDryRun bool
	importDedup  bool
)

func init() {
	importCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Show what would be imported without importing")
	importCmd.Flags().BoolVar(&importDedup, "dedup", false, "Skip lines whose title matches an existing open issue")
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	ctx := rootCtx
	fromStdin := len(args) > 0 && args[0] == "-"

	if fromStdin {
		return runImportFromReader(ctx, os.Stdin, "stdin")
	}

	// Determine source file
	var jsonlPath string
	if len(args) > 0 {
		jsonlPath = args[0]
	} else {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return fmt.Errorf("%s — %s", activeWorkspaceNotFoundError(), diagHint())
		}
		if globalFlag {
			jsonlPath = filepath.Join(beadsDir, "global-issues.jsonl")
		} else {
			jsonlPath = filepath.Join(beadsDir, "issues.jsonl")
		}
	}

	info, err := os.Stat(jsonlPath)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", jsonlPath, err)
	}
	if info.Size() == 0 {
		if jsonOutput {
			outputJSON(importResultJSON{Source: jsonlPath})
			return nil
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
	Source    string   `json:"source"`
	Created   int      `json:"created"`
	Skipped   int      `json:"skipped"`
	DedupHits int      `json:"dedup_skipped,omitempty"`
	Memories  int      `json:"memories,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
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
			outputJSON(result)
			return nil
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
		opts := ImportOptions{SkipPrefixValidation: true}
		importResult, err := importIssuesCore(ctx, "", store, issues, opts)
		if err != nil {
			return fmt.Errorf("import failed: %w", err)
		}
		result.Created = importResult.Created
		result.Skipped += importResult.Skipped
		for _, issue := range issues {
			result.IDs = append(result.IDs, issue.ID)
		}
	}

	// Commit
	commitMsg := fmt.Sprintf("bd import: %d issues", result.Created)
	if result.Memories > 0 {
		commitMsg += fmt.Sprintf(", %d memories", result.Memories)
	}
	commitMsg += fmt.Sprintf(" from %s", filepath.Base(source))
	if err := store.Commit(ctx, commitMsg); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if jsonOutput {
		outputJSON(result)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Imported %d issues", result.Created)
	if result.Memories > 0 {
		fmt.Fprintf(os.Stderr, " and %d memories", result.Memories)
	}
	fmt.Fprintf(os.Stderr, " from %s", source)
	if dedupHits > 0 {
		fmt.Fprintf(os.Stderr, " (%d duplicates skipped)", dedupHits)
	}
	fmt.Fprintln(os.Stderr)
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
