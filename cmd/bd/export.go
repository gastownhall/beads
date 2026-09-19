package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
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
	exportExcludeOwners   []string
	exportVerbose         bool
)

func init() {
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file path (default: stdout)")
	exportCmd.Flags().BoolVar(&exportAll, "all", false, "Include all records (infra, templates, gates, memories)")
	exportCmd.Flags().BoolVar(&exportIncludeInfra, "include-infra", false, "Include infrastructure beads (agents, roles, messages)")
	exportCmd.Flags().BoolVar(&exportScrub, "scrub", false, "Exclude test/pollution records")
	exportCmd.Flags().BoolVar(&exportIncludeMemories, "include-memories", false, "Include persistent memories (from 'bd remember') in the export")
	exportCmd.Flags().BoolVar(&exportNoMemories, "no-memories", false, "Exclude persistent memories (deprecated: now the default)")
	_ = exportCmd.Flags().MarkHidden("no-memories")
	exportCmd.Flags().StringArrayVar(&exportExcludeOwners, "exclude-owner", nil, "Exclude issues created by this identity (repeatable; also reads export.exclude_owners config)")
	exportCmd.Flags().BoolVar(&exportVerbose, "verbose", false, "Print filtered issue count when owners are excluded")
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

	if usesProxiedServer() {
		if uowProvider == nil {
			return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
		}
		// Run the ENTIRE read set inside one read transaction so the exported
		// issues, labels, dependencies, comments, and memories are a single
		// consistent snapshot. Export is read-only: RunTxRead never commits
		// (the attempt is always rolled back on close).
		_, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, error) {
			return struct{}{}, runExportFromSource(ctx, &uowExportSource{uw: uw})
		})
		return err
	}

	return runExportFromSource(ctx, storeExportSource{})
}

// runExportFromSource is the whole classic export body with the storage reads
// routed through an exportSource. Everything downstream of the reads —
// filtering, sanitizeZeroTime, record shaping, marshal order, atomic file
// handling, the stderr summary — is shared verbatim between the embedded and
// proxied-server modes, which is what keeps the two outputs byte-identical.
func runExportFromSource(ctx context.Context, src exportSource) error {
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
	// Opt out of BEADS_MAX_ROWS (designer §4.1) — export is a data-integrity
	// path and must never abort partway through an export run.
	filter := types.IssueFilter{
		Limit:         0,
		MaxRows:       0,
		MaxRowsSource: "",
	}

	// Exclude infra types by default (agents, roles, messages).
	if !exportAll && !exportIncludeInfra {
		var infraTypes []string
		infraSet := src.GetInfraTypes(ctx)
		if len(infraSet) > 0 {
			for t := range infraSet {
				infraTypes = append(infraTypes, t)
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

	issues, err := src.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleErrorRespectJSON("failed to search issues: %v", err)
	}

	// Scrub test/pollution records if requested
	if exportScrub {
		issues = filterOutPollution(issues)
	}

	// Owner-keyed filtering: exclude issues by created_by identity.
	// Merges --exclude-owner flag values with export.exclude_owners config.
	ownerExcludes := buildOwnerExcludeSet(ctx, src, exportExcludeOwners)
	filteredOwnerCount := 0
	if len(ownerExcludes) > 0 {
		before := len(issues)
		issues = filterOutOwners(issues, ownerExcludes)
		filteredOwnerCount = before - len(issues)
	}

	if len(issues) == 0 && exportNoMemories {
		if exportOutput != "" {
			fmt.Fprintln(os.Stderr, "No issues to export.")
		}
		return nil
	}

	// Bulk-load relational data
	rel, err := src.LoadExportRelations(ctx, issues)
	if err != nil {
		return HandleErrorRespectJSON("failed to load relational data: %v", err)
	}

	// Explicit plane markers (bd-r9uce): a no_history=true row is either an
	// unpromoted no-history wisp (wisps table) or a promoted one (durable
	// issues-table row still carrying the stray flag) — only table membership
	// can tell them apart, and import routes by the "wisp_plane" marker
	// stamped here. Ephemeral rows are unambiguous (ephemeral=true only ever lives in
	// the wisps table) and are deliberately NOT stamped, keeping their export
	// bytes unchanged.
	var noHistoryIDs []string
	for _, issue := range issues {
		if issue.NoHistory && !issue.Ephemeral {
			noHistoryIDs = append(noHistoryIDs, issue.ID)
		}
	}
	wispPlane := map[string]bool{}
	if len(noHistoryIDs) > 0 {
		wispPlane, err = src.WispPlaneIDs(ctx, noHistoryIDs)
		if err != nil {
			return HandleErrorRespectJSON("failed to classify wisp-plane rows: %v", err)
		}
	}

	// Populate relational data on each issue
	for _, issue := range issues {
		issue.Labels = rel.labels[issue.ID]
		issue.Dependencies = rel.deps[issue.ID]
		issue.Comments = rel.comments[issue.ID]
	}

	// Write JSONL: one JSON object per line
	count := 0
	for _, issue := range issues {
		counts := rel.depCounts[issue.ID]
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
				CommentCount:    rel.commentCounts[issue.ID],
			},
			WispPlane: wispPlane[issue.ID],
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
		allConfig, err := src.GetAllConfig(ctx)
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
		if exportVerbose && filteredOwnerCount > 0 {
			fmt.Fprintf(os.Stderr, "  (%d filtered as personal by owner exclusion)\n", filteredOwnerCount)
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
	// WispPlane is the explicit wisps-plane marker (bd-r9uce): true when the
	// row lives in the WISPS table AND its flags alone cannot prove it (the
	// no_history shape; ephemeral rows are self-describing and stay
	// unstamped). Import routes by this marker — never by no_history — so a
	// promoted no-history wisp (durable issues-table row still carrying the
	// stray flag) round-trips to the durable plane instead of being silently
	// re-planed. Declared after the embedded struct so it serializes last.
	// Deliberately a FRESH key, not the legacy "wisp" alias key: pre-fix
	// binaries' alias branch would import a marked no-history wisp as
	// ephemeral (purge-eligible, export-excluded), so an unknown-to-them key
	// that degrades to flag routing is the data-safe choice (lion, #5368).
	WispPlane bool `json:"wisp_plane,omitempty"`
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

// buildOwnerExcludeSet merges --exclude-owner flag values with the
// export.exclude_owners (and legacy export.exclude_owner) config entries.
// Returns the combined set as a map for O(1) lookup.
func buildOwnerExcludeSet(ctx context.Context, src exportSource, flagOwners []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, o := range flagOwners {
		if o != "" {
			set[o] = struct{}{}
		}
	}
	// export.* keys are YAML-only (config.IsYamlOnlyKey returns true for the
	// "export." prefix), so bd config set stores them in config.yaml rather than
	// the database. Read from YAML first, then fall back to the database for any
	// instance that was written directly to the store.
	addOwners := func(val string) {
		for _, o := range strings.Split(val, ",") {
			if o = strings.TrimSpace(o); o != "" {
				set[o] = struct{}{}
			}
		}
	}
	if val := config.GetYamlConfig("export.exclude_owners"); val != "" {
		addOwners(val)
	}
	if val := config.GetYamlConfig("export.exclude_owner"); val != "" {
		set[strings.TrimSpace(val)] = struct{}{}
	}
	// Also read from database for any value stored there directly.
	if val, err := src.GetConfig(ctx, "export.exclude_owners"); err == nil && val != "" {
		addOwners(val)
	}
	if val, err := src.GetConfig(ctx, "export.exclude_owner"); err == nil && val != "" {
		set[strings.TrimSpace(val)] = struct{}{}
	}
	return set
}

// filterOutOwners removes issues whose created_by identity is in the exclude set.
func filterOutOwners(issues []*types.Issue, exclude map[string]struct{}) []*types.Issue {
	var keep []*types.Issue
	for _, issue := range issues {
		if _, excluded := exclude[issue.CreatedBy]; !excluded {
			keep = append(keep, issue)
		}
	}
	return keep
}
