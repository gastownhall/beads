package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export issues to JSONL format",
	Long: `Export all issues to JSONL (newline-delimited JSON) format.

Each line is a complete JSON object representing one issue, including its
labels, dependencies, and comments.

This command is for issue export, migration, and interoperability. It does
not produce the JSONL backup snapshot used by 'bd backup restore'. For
supported backup/restore flows, use 'bd backup', 'bd backup export-git',
and 'bd backup restore'.

By default, exports only regular issues (excluding infrastructure beads
like agents, rigs, roles, and messages). Use --all to include everything.

Memories (from 'bd remember') are included by default. Use --no-memories
to exclude them.

EXAMPLES:
  bd export                              # Export issues + memories to stdout
  bd export -o backup.jsonl              # Export to file
  bd export --no-memories                # Export issues only
  bd export --all -o full.jsonl          # Include infra + templates + gates
  bd export --scrub -o clean.jsonl       # Exclude test/pollution records`,
	GroupID: "sync",
	RunE:    runExport,
}

var (
	exportOutput       string
	exportAll          bool
	exportIncludeInfra bool
	exportScrub        bool
	exportNoMemories   bool
)

func init() {
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file path (default: stdout)")
	exportCmd.Flags().BoolVar(&exportAll, "all", false, "Include all records (infra, templates, gates)")
	exportCmd.Flags().BoolVar(&exportIncludeInfra, "include-infra", false, "Include infrastructure beads (agents, rigs, roles, messages)")
	exportCmd.Flags().BoolVar(&exportScrub, "scrub", false, "Exclude test/pollution records")
	exportCmd.Flags().BoolVar(&exportNoMemories, "no-memories", false, "Exclude persistent memories from the export")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	ctx := rootCtx

	// Determine output destination
	var w io.Writer
	if exportOutput != "" {
		f, err := os.Create(exportOutput) //nolint:gosec // user-provided output path
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer f.Close()
		w = f
	} else {
		w = os.Stdout
	}

	// Build filter for issues table. Export all statuses (this is a backup tool).
	filter := types.IssueFilter{Limit: 0}

	// Exclude infra types by default (agents, rigs, roles, messages)
	if !exportAll && !exportIncludeInfra {
		var infraTypes []string
		if store != nil {
			infraSet := store.GetInfraTypes(ctx)
			if len(infraSet) > 0 {
				for t := range infraSet {
					infraTypes = append(infraTypes, t)
				}
			}
		}
		if len(infraTypes) == 0 {
			infraTypes = storage.DefaultInfraTypes()
		}
		for _, t := range infraTypes {
			filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
		}
	}

	// Exclude templates by default
	if !exportAll {
		isTemplate := false
		filter.IsTemplate = &isTemplate
	}

	// Fetch all matching issues from the issues table
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return fmt.Errorf("failed to search issues: %w", err)
	}

	// Also fetch wisps (ephemeral beads) using the store's ephemeral routing.
	// SearchIssues with Ephemeral=true queries the wisps table directly.
	ephemeral := true
	wispFilter := filter
	wispFilter.Ephemeral = &ephemeral
	wispIssues, err := store.SearchIssues(ctx, "", wispFilter)
	if err == nil && len(wispIssues) > 0 {
		issues = append(issues, wispIssues...)
	}

	// Scrub test/pollution records if requested
	if exportScrub {
		issues = filterOutPollution(issues)
	}

	if len(issues) == 0 && exportNoMemories {
		if exportOutput != "" {
			fmt.Fprintln(os.Stderr, "No issues to export.")
		}
		return nil
	}

	// Bulk-load relational data
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}

	labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
	allDeps, _ := store.GetDependencyRecordsForIssues(ctx, issueIDs)
	commentsMap, _ := store.GetCommentsForIssues(ctx, issueIDs)
	commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)
	depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

	// Populate relational data on each issue
	for _, issue := range issues {
		issue.Labels = labelsMap[issue.ID]
		issue.Dependencies = allDeps[issue.ID]
		issue.Comments = commentsMap[issue.ID]
	}

	// Write JSONL: one JSON object per line
	count := 0
	for _, issue := range issues {
		counts := depCounts[issue.ID]
		if counts == nil {
			counts = &types.DependencyCounts{}
		}

		// Sanitize zero-value timestamps that can't be marshaled to JSON.
		// NULL datetime columns scanned as time.Time{} (year 0001) cause
		// MarshalJSON to fail with "year outside of range [0,9999]". (GH#2488)
		sanitizeZeroTime(issue)

		record := &exportIssueRecord{
			RecordType: "issue",
			IssueWithCounts: &types.IssueWithCounts{
				Issue:           issue,
				DependencyCount: counts.DependencyCount,
				DependentCount:  counts.DependentCount,
				CommentCount:    commentCounts[issue.ID],
			},
		}

		data, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("failed to marshal issue %s: %w", issue.ID, err)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("failed to write: %w", err)
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return fmt.Errorf("failed to write newline: %w", err)
		}
		count++
	}

	// Export memories if not excluded
	memoryCount := 0
	if !exportNoMemories {
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to read config for memories: %w", err)
		}
		fullPrefix := kvPrefix + memoryPrefix
		for k, v := range allConfig {
			if !strings.HasPrefix(k, fullPrefix) {
				continue
			}
			userKey := strings.TrimPrefix(k, fullPrefix)
			record := map[string]string{
				"_type": "memory",
				"key":   userKey,
				"value": v,
			}
			data, err := json.Marshal(record)
			if err != nil {
				return fmt.Errorf("failed to marshal memory %s: %w", userKey, err)
			}
			if _, err := w.Write(data); err != nil {
				return fmt.Errorf("failed to write: %w", err)
			}
			if _, err := w.Write([]byte{'\n'}); err != nil {
				return fmt.Errorf("failed to write newline: %w", err)
			}
			memoryCount++
		}
	}

	// Sync to disk if writing to file
	if f, ok := w.(*os.File); ok && f != os.Stdout {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("failed to sync output file: %w", err)
		}
	}

	// Print summary to stderr (not stdout, to avoid mixing with JSONL)
	if exportOutput != "" {
		if memoryCount > 0 {
			fmt.Fprintf(os.Stderr, "Exported %d issues and %d memories to %s\n", count, memoryCount, exportOutput)
		} else {
			fmt.Fprintf(os.Stderr, "Exported %d issues to %s\n", count, exportOutput)
		}
	}

	return nil
}

// exportIssueRecord wraps IssueWithCounts with a _type discriminator so that
// every line in the JSONL export is self-describing. Memory lines already
// carry "_type":"memory"; this gives issue lines "_type":"issue". (GH#3271)
type exportIssueRecord struct {
	RecordType string `json:"_type"`
	*types.IssueWithCounts
}

// sanitizeZeroTime replaces Go zero-value time.Time fields with Unix epoch.
// NULL datetime columns in Dolt scan as time.Time{} (year 0001-01-01), which
// causes json.Marshal to fail with "year outside of range [0,9999]". (GH#2488)
func sanitizeZeroTime(issue *types.Issue) {
	epoch := time.Unix(0, 0).UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = epoch
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = epoch
	}
}

// filterOutPollution removes issues that look like test/pollution records.
func filterOutPollution(issues []*types.Issue) []*types.Issue {
	var clean []*types.Issue
	for _, issue := range issues {
		if !isTestIssue(issue.Title) {
			clean = append(clean, issue)
		}
	}
	return clean
}
