package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export issues to JSONL format",
	Long: `Export all issues to JSONL (newline-delimited JSON) format.

Each line is a complete JSON object representing one issue, including its
labels, dependencies, and comments.

This command is for issue export, migration, and interoperability. It exports
records from the issues table; it is not a full database backup and does not
capture Dolt branches, commit history, working-set state, or non-issue tables.
For supported full backup/restore flows, use 'bd backup init', 'bd backup sync',
and 'bd backup restore'.

By default, exports only regular issues (excluding infrastructure beads
like agents, roles, and messages). Use --all to include everything.

Memories (from 'bd remember') are excluded by default because they may
contain sensitive agent context. Use --include-memories or --all to
include them.

EXAMPLES:
  bd export                              # Export issues to stdout
  bd export -o issues.jsonl              # Export issues to file
  bd export --include-memories           # Export issues + memories
  bd export --all -o full.jsonl          # Include infra + templates + gates + memories
  bd export --scrub -o clean.jsonl       # Exclude test/pollution records`,
	GroupID:       "sync",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runExport,
}

var (
	exportOutput          string
	exportAll             bool
	exportIncludeInfra    bool
	exportScrub           bool
	exportNoMemories      bool
	exportIncludeMemories bool
)

func init() {
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file path (default: stdout)")
	exportCmd.Flags().BoolVar(&exportAll, "all", false, "Include all records (infra, templates, gates, memories)")
	exportCmd.Flags().BoolVar(&exportIncludeInfra, "include-infra", false, "Include infrastructure beads (agents, roles, messages)")
	exportCmd.Flags().BoolVar(&exportScrub, "scrub", false, "Exclude test/pollution records")
	exportCmd.Flags().BoolVar(&exportIncludeMemories, "include-memories", false, "Include persistent memories (from 'bd remember') in the export")
	exportCmd.Flags().BoolVar(&exportNoMemories, "no-memories", false, "Exclude persistent memories (deprecated: now the default)")
	_ = exportCmd.Flags().MarkHidden("no-memories")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("export")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	// Determine output destination. File output uses atomic writes
	// (temp file + rename) so concurrent exports and crashes never
	// leave a truncated or interleaved JSONL file.
	var w io.Writer
	var aw *atomicfile.Writer
	if exportOutput != "" {
		var err error
		aw, err = atomicfile.Create(exportOutput, 0o644)
		if err != nil {
			return HandleErrorRespectJSON("failed to create output file: %v", err)
		}
		defer func() {
			// Abort is a no-op if Close was already called.
			_ = aw.Abort()
		}()
		w = aw
	} else {
		w = os.Stdout
	}

	// Build filter for issues table. Export all statuses by default.
	filter := types.IssueFilter{Limit: 0}

	// Exclude infra types by default (agents, roles, messages).
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
			infraTypes = domain.DefaultInfraTypes()
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

	// Exclude ephemeral wisps by default — they are private/transient and
	// must not reach git history or external integrations (GH#3649).
	// --all overrides to include everything.
	if !exportAll {
		persistentOnly := false
		filter.Ephemeral = &persistentOnly
	}

	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleErrorRespectJSON("failed to search issues: %v", err)
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
			return HandleErrorRespectJSON("failed to marshal issue %s: %v", issue.ID, err)
		}
		if _, err := w.Write(data); err != nil {
			return HandleErrorRespectJSON("failed to write: %v", err)
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return HandleErrorRespectJSON("failed to write newline: %v", err)
		}
		count++
	}

	// Export memories only when explicitly requested (GH#3650).
	// Memories may contain sensitive agent context and are excluded by default.
	memoryCount := 0
	if (exportIncludeMemories || exportAll) && !exportNoMemories {
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			return HandleErrorRespectJSON("failed to read config for memories: %v", err)
		}
		fullPrefix := kvPrefix + memoryPrefix
		// Sort keys for deterministic output order (GH#3474).
		var memKeys []string
		for k := range allConfig {
			if strings.HasPrefix(k, fullPrefix) {
				memKeys = append(memKeys, k)
			}
		}
		sort.Strings(memKeys)
		for _, k := range memKeys {
			v := allConfig[k]
			userKey := strings.TrimPrefix(k, fullPrefix)
			record := map[string]string{
				"_type": "memory",
				"key":   userKey,
				"value": v,
			}
			data, err := json.Marshal(record)
			if err != nil {
				return HandleErrorRespectJSON("failed to marshal memory %s: %v", userKey, err)
			}
			if _, err := w.Write(data); err != nil {
				return HandleErrorRespectJSON("failed to write: %v", err)
			}
			if _, err := w.Write([]byte{'\n'}); err != nil {
				return HandleErrorRespectJSON("failed to write newline: %v", err)
			}
			memoryCount++
		}
	}

	// Finalize atomic write if writing to file (fsync + rename).
	if aw != nil {
		if err := aw.Close(); err != nil {
			return HandleErrorRespectJSON("failed to finalize export file: %v", err)
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
