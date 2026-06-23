package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// ImportOptions configures import behavior.
type ImportOptions struct {
	DryRun                     bool
	SkipUpdate                 bool
	Strict                     bool
	RenameOnImport             bool
	ClearDuplicateExternalRefs bool
	OrphanHandling             string
	DeletionIDs                []string
	SkipPrefixValidation       bool
	ProtectLocalExportIDs      map[string]time.Time
	// ConflictSkip makes the import insert-if-new instead of UPSERT: an
	// issue whose ID already exists is left untouched. Set only by the
	// auto-import upgrade-recovery fallback (GH#3955); explicit `bd import`
	// leaves this false and keeps UPSERT semantics.
	ConflictSkip bool
	// AllowStale imports rows even when their updated_at is older than the
	// local issue's, overwriting newer local state. Required for the
	// restore-an-older-snapshot recovery workflow, which the default stale
	// guard otherwise silently no-ops per row (bd-6dnrw.9). Only settable
	// via explicit `bd import --allow-stale`; auto-import paths never set it.
	AllowStale bool
}

// ImportResult describes what an import operation did.
type ImportResult struct {
	Created             int
	Updated             int
	Unchanged           int
	Skipped             int
	Deleted             int
	Collisions          int
	IDMapping           map[string]string
	CollisionIDs        []string
	PrefixMismatch      bool
	ExpectedPrefix      string
	MismatchPrefixes    map[string]int
	ImportedIDs         []string
	StaleSkippedIDs     []string
	SkippedDependencies []string
	// UpdatedIssues lists existing local issues whose row the import
	// rewrote (incoming strictly newer, content differs), with a
	// field-level summary, so reverts of local state are visible instead
	// of silent (bd-hj85c).
	UpdatedIssues []ImportChange
	// TieKeptLocalIDs lists incoming rows whose updated_at equals the
	// local issue's but whose content differs. The upsert keeps the local
	// row for these (second-granularity timestamp ties, bd-hj85c); their
	// aux data still merges.
	TieKeptLocalIDs []string
}

// ImportChange describes how an import row modified an existing local issue.
type ImportChange struct {
	ID      string `json:"id"`
	Changes string `json:"changes,omitempty"`
}

// importIssuesCore imports issues into the Dolt store.
// This is a bridge function that delegates to the Dolt store's batch creation.
func importIssuesCore(ctx context.Context, _ string, store storage.DoltStorage, issues []*types.Issue, opts ImportOptions) (*ImportResult, error) {
	if opts.DryRun || len(issues) == 0 {
		return &ImportResult{Skipped: len(issues)}, nil
	}

	// The stale guard has two halves (bd-pkim8). This pre-filter reports the
	// rows that are already known stale (StaleSkippedIDs) and keeps their
	// labels/comments/dependencies out of the batch entirely. It is a separate
	// read, though, so a local update that commits between it and the batch
	// write would slip through — RejectStaleUpserts below closes that race by
	// re-checking updated_at inside the upsert itself.
	var staleSkippedIDs []string
	var changePlan importChangePlan
	if !opts.AllowStale {
		filtered, skipped, plan, err := filterStaleImportIssues(ctx, store, issues)
		if err != nil {
			return nil, err
		}
		issues = filtered
		staleSkippedIDs = skipped
		changePlan = plan
		if len(issues) == 0 {
			return &ImportResult{Skipped: len(staleSkippedIDs), StaleSkippedIDs: staleSkippedIDs}, nil
		}
	}

	var skippedDependencies []string
	skippedDependencySet := make(map[string]struct{})
	// In-txn half of the stale guard: rows the conditional upsert rejected
	// (local update committed between the pre-filter read and the batch
	// write). The transaction may retry, so dedup by ID.
	staleRejectedSet := make(map[string]struct{})
	err := store.CreateIssuesWithFullOptions(ctx, issues, getActorWithGit(), storage.BatchCreateOptions{
		OrphanHandling:                 storage.OrphanAllow,
		SkipPrefixValidation:           opts.SkipPrefixValidation,
		ConflictSkip:                   opts.ConflictSkip,
		RejectStaleUpserts:             !opts.AllowStale,
		SkipDependencyValidationErrors: true,
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			skipped := fmt.Sprintf("%s -> %s: %s", issueID, dependsOnID, reason)
			if _, ok := skippedDependencySet[skipped]; ok {
				return
			}
			skippedDependencySet[skipped] = struct{}{}
			skippedDependencies = append(skippedDependencies, skipped)
		},
		OnStaleRejected: func(issueID string) {
			staleRejectedSet[issueID] = struct{}{}
		},
	})
	if err != nil {
		return nil, err
	}

	importedIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if _, rejected := staleRejectedSet[issue.ID]; rejected {
			staleSkippedIDs = append(staleSkippedIDs, issue.ID)
			continue
		}
		importedIDs = append(importedIDs, issue.ID)
	}
	// Drop planned updates the in-txn guard rejected (a local update raced
	// in between the pre-filter read and the batch write).
	updatedIssues := make([]ImportChange, 0, len(changePlan.Updates))
	updatedCount := 0
	for _, change := range changePlan.Updates {
		if _, rejected := staleRejectedSet[change.ID]; rejected {
			continue
		}
		updatedIssues = append(updatedIssues, change)
		updatedCount++
	}
	return &ImportResult{
		Created:             len(importedIDs),
		Updated:             updatedCount,
		Skipped:             len(staleSkippedIDs),
		ImportedIDs:         importedIDs,
		StaleSkippedIDs:     staleSkippedIDs,
		SkippedDependencies: skippedDependencies,
		UpdatedIssues:       updatedIssues,
		TieKeptLocalIDs:     changePlan.TieKeptLocal,
	}, nil
}

// importChangePlan reports how the import batch relates to existing local
// issues, so the import can surface what it changed instead of doing it
// silently (bd-hj85c).
type importChangePlan struct {
	// Updates lists existing issues the batch will rewrite: incoming row
	// strictly newer and row content differs.
	Updates []ImportChange
	// TieKeptLocal lists incoming rows with the same updated_at as the
	// local issue but different row content. The stale-guarded upsert keeps
	// every stored column for these (second-granularity timestamp tie),
	// while their aux data still merges.
	TieKeptLocal []string
}

func filterStaleImportIssues(ctx context.Context, store storage.DoltStorage, issues []*types.Issue) ([]*types.Issue, []string, importChangePlan, error) {
	var plan importChangePlan
	ids := make([]string, 0, len(issues))
	seen := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if issue == nil || issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		ids = append(ids, issue.ID)
	}
	if len(ids) == 0 {
		return issues, nil, plan, nil
	}

	localIssues, err := store.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, nil, plan, fmt.Errorf("check existing issues before import: %w", err)
	}
	localByID := make(map[string]*types.Issue, len(localIssues))
	for _, issue := range localIssues {
		if issue != nil && issue.ID != "" && !issue.UpdatedAt.IsZero() {
			localByID[issue.ID] = issue
		}
	}
	if len(localByID) == 0 {
		return issues, nil, plan, nil
	}

	filtered := make([]*types.Issue, 0, len(issues))
	skippedIDs := make([]string, 0)
	for _, issue := range issues {
		if issue == nil || issue.ID == "" || issue.UpdatedAt.IsZero() {
			filtered = append(filtered, issue)
			continue
		}
		local, ok := localByID[issue.ID]
		if !ok {
			filtered = append(filtered, issue)
			continue
		}
		// Compare at second granularity: updated_at is DATETIME(0) in the
		// store, so a sub-second component on the JSONL side must not turn
		// a tie into a spurious "newer" classification.
		incomingAt := issue.UpdatedAt.UTC().Truncate(time.Second)
		localAt := local.UpdatedAt.UTC().Truncate(time.Second)
		if incomingAt.Before(localAt) {
			skippedIDs = append(skippedIDs, issue.ID)
			continue
		}
		if summary := importRowChangeSummary(local, issue); summary != "" {
			if incomingAt.Equal(localAt) {
				plan.TieKeptLocal = append(plan.TieKeptLocal, issue.ID)
			} else {
				plan.Updates = append(plan.Updates, ImportChange{ID: issue.ID, Changes: summary})
			}
		}
		filtered = append(filtered, issue)
	}
	return filtered, skippedIDs, plan, nil
}

// importRowChangeSummary summarizes the differences between the local issue
// row and the incoming import row, restricted to the columns the import
// upsert rewrites. Returns "" when none of those fields differ. Status,
// priority, and type transitions show old → new; long-form fields are listed
// by name only.
func importRowChangeSummary(local, incoming *types.Issue) string {
	var parts []string
	if local.Status != incoming.Status {
		parts = append(parts, fmt.Sprintf("status %s → %s", local.Status, incoming.Status))
	}
	if local.Priority != incoming.Priority {
		parts = append(parts, fmt.Sprintf("priority %d → %d", local.Priority, incoming.Priority))
	}
	if local.IssueType != incoming.IssueType {
		parts = append(parts, fmt.Sprintf("type %s → %s", local.IssueType, incoming.IssueType))
	}
	if local.Assignee != incoming.Assignee {
		parts = append(parts, "assignee")
	}
	if local.Title != incoming.Title {
		parts = append(parts, "title")
	}
	if local.Description != incoming.Description {
		parts = append(parts, "description")
	}
	if local.Design != incoming.Design {
		parts = append(parts, "design")
	}
	if local.AcceptanceCriteria != incoming.AcceptanceCriteria {
		parts = append(parts, "acceptance_criteria")
	}
	if local.Notes != incoming.Notes {
		if incoming.Notes == "" {
			parts = append(parts, "notes cleared")
		} else {
			parts = append(parts, "notes")
		}
	}
	if local.CloseReason != incoming.CloseReason {
		parts = append(parts, "close_reason")
	}
	if !stringPtrEqual(local.ExternalRef, incoming.ExternalRef) {
		parts = append(parts, "external_ref")
	}
	if !intPtrEqual(local.EstimatedMinutes, incoming.EstimatedMinutes) {
		parts = append(parts, "estimate")
	}
	if string(local.Metadata) != string(incoming.Metadata) {
		parts = append(parts, "metadata")
	}
	return strings.Join(parts, ", ")
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// importLocalResult holds counts from a local JSONL import.
type importLocalResult struct {
	Issues   int
	Memories int
}

// memoryRecord represents a memory entry in the JSONL export.
type memoryRecord struct {
	Type  string `json:"_type"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// importFromLocalJSONL imports issues (and memories) from a local JSONL file on disk
// into the Dolt store. Returns the number of issues imported and any error.
// This is a convenience wrapper around importFromLocalJSONLFull.
func importFromLocalJSONL(ctx context.Context, store storage.DoltStorage, localPath string) (int, error) {
	result, err := importFromLocalJSONLFull(ctx, store, localPath)
	if err != nil {
		return 0, err
	}
	return result.Issues, nil
}

// parseJSONLFile reads a JSONL file and returns parsed issues and config
// entries (memories). Pure function — no store I/O.
func parseJSONLFile(path string) ([]*types.Issue, map[string]string, error) {
	//nolint:gosec // G304: path from user-provided CLI argument
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read JSONL file %s: %w", path, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	// Allow up to 64MB per line for large descriptions
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var issues []*types.Issue
	configEntries := make(map[string]string)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Peek at the record to check for _type field
		var peek map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			return nil, nil, fmt.Errorf("failed to parse JSONL line: %w", err)
		}

		// Skip the optional beads-jsonl metadata/header record.
		// Canonical exports produced by the stable-ordering /
		// git-merge convention prepend a schema+provenance line, e.g.
		// {"_schema":"beads-jsonl/1","_dolt_branch":"main",
		// "_dolt_commit":"...","_sort":"stable-v1"}. It carries no
		// _type and no issue fields; without this guard it falls
		// through to the issue path, unmarshals into an empty Issue,
		// and aborts the whole import with "validation failed for
		// issue : title is required". Identified by the _schema
		// sentinel, which real issue/memory records never carry.
		if _, isHeader := peek["_schema"]; isHeader {
			continue
		}

		// Check if this is a memory record
		if rawType, ok := peek["_type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr == "memory" {
				var mem memoryRecord
				if err := json.Unmarshal([]byte(line), &mem); err != nil {
					return nil, nil, fmt.Errorf("failed to parse memory record: %w", err)
				}
				if mem.Key != "" && mem.Value != "" {
					configEntries[kvPrefix+memoryPrefix+mem.Key] = mem.Value
				}
				continue
			}
		}

		// Regular issue record
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, nil, fmt.Errorf("failed to parse issue from JSONL: %w", err)
		}
		// Skip tombstone entries: these are deleted issues exported by older
		// versions (pre-v0.50) with status "tombstone" and deleted_at set.
		// They are not valid for re-import since "tombstone" is not a real status.
		if issue.Status == "tombstone" {
			continue
		}

		// v0.35–v0.37 exported "wisp" (bool), renamed to "ephemeral" in v0.38+.
		// map old field name so the flag is preserved on import.
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
		return nil, nil, fmt.Errorf("failed to scan JSONL: %w", err)
	}

	return issues, configEntries, nil
}

// importFromLocalJSONLFull imports issues and memories from a local JSONL file
// using UPSERT semantics (an existing issue row is overwritten). Used by the
// explicit recovery paths: `bd bootstrap` and `bd init --from-jsonl`.
func importFromLocalJSONLFull(ctx context.Context, store storage.DoltStorage, localPath string) (*importLocalResult, error) {
	return importFromLocalJSONLWithOpts(ctx, store, localPath, false)
}

// importFromLocalJSONLConflictSkip is the auto-import upgrade-recovery
// fallback (GH#3955; the fallbackImporter seam in auto_import_upgrade.go).
// It is identical to importFromLocalJSONLFull except that an issue whose ID
// already exists is left untouched instead of being overwritten, so a
// regressed emptiness guard can never clobber live rows — worst case is a
// no-op.
func importFromLocalJSONLConflictSkip(ctx context.Context, store storage.DoltStorage, localPath string) (*importLocalResult, error) {
	return importFromLocalJSONLWithOpts(ctx, store, localPath, true)
}

// importFromLocalJSONLWithOpts is the shared implementation. It detects
// memory records (lines with "_type":"memory") and imports them via
// SetConfig, while routing regular issue records through the normal path.
// conflictSkip selects insert-if-new (true) vs UPSERT (false) for issue rows.
func importFromLocalJSONLWithOpts(ctx context.Context, store storage.DoltStorage, localPath string, conflictSkip bool) (*importLocalResult, error) {
	issues, configEntries, err := parseJSONLFile(localPath)
	if err != nil {
		return nil, err
	}

	result := &importLocalResult{}

	// Import memories
	for key, value := range configEntries {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return nil, fmt.Errorf("failed to import config %q: %w", key, err)
		}
		result.Memories++
	}

	// Import issues
	if len(issues) > 0 {
		// Auto-detect prefix from first issue if not already configured
		configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err == nil && strings.TrimSpace(configuredPrefix) == "" {
			firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
			if firstPrefix != "" {
				if err := store.SetConfig(ctx, "issue_prefix", firstPrefix); err != nil {
					return nil, fmt.Errorf("failed to set issue_prefix from imported issues: %w", err)
				}
			}
		}

		opts := ImportOptions{
			SkipPrefixValidation: true,
			ConflictSkip:         conflictSkip,
		}
		importResult, err := importIssuesCore(ctx, "", store, issues, opts)
		if err != nil {
			return nil, err
		}
		result.Issues = importResult.Created
	}

	return result, nil
}
