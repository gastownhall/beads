package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// incrementalExportThreshold caps the number of changed issue IDs we'll
// incrementally re-encode before falling back to a full export. At high
// change counts the per-issue SQL work (bulk loaders × changed set) stops
// being cheaper than one `SearchIssues(Limit:0)` sweep.
const incrementalExportThreshold = 5000

// slowExportWarnThreshold is the duration over which an auto-export is
// considered slow enough to warn the user. Any single auto-export that
// exceeds this prints a one-line stderr tip pointing at the fix levers.
const slowExportWarnThreshold = 3 * time.Second

// exportAutoState tracks auto-export state to avoid redundant work.
type exportAutoState struct {
	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
	Issues         int       `json:"issues"`
	Memories       int       `json:"memories"`
}

const exportAutoStateFile = "export-state.json"
const gitAddTimeout = 5 * time.Second

// maybeAutoExport writes a git-tracked JSONL file if enabled and due.
// Called from PersistentPostRun after auto-backup.
//
// This runs in server mode too: clients of a shared dolt sql-server rely on
// the JSONL export for git-durable state exactly like embedded users do — in
// topologies without a Dolt remote it is the only durability. Skipping here
// made `git push` silently publish stale issue state (wy-4ope).
func maybeAutoExport(ctx context.Context, allowEmptyOverwrite bool) error {
	// Skip when running as a git hook to avoid re-export during pre-commit.
	if os.Getenv("BD_GIT_HOOK") == "1" {
		debug.Logf("auto-export: skipping — running as git hook\n")
		return nil
	}

	if !config.GetBool("export.auto") {
		return nil
	}
	if store == nil {
		return nil
	}
	if lm, ok := storage.UnwrapStore(store).(storage.LifecycleManager); ok && lm.IsClosed() {
		return nil
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil
	}

	// Resolve the export path before throttle/check detection so all decisions
	// refer to the path that would actually be written.
	exportPath := config.GetString("export.path")
	if exportPath == "" {
		if globalFlag {
			exportPath = "global-issues.jsonl"
		} else {
			exportPath = "issues.jsonl"
		}
	}
	fullPath := filepath.Join(beadsDir, exportPath)

	// Load state + interval.
	state := loadExportAutoState(beadsDir)
	interval := config.GetDuration("export.interval")
	if interval == 0 {
		interval = 60 * time.Second
	}

	// Change detection via Dolt state hash. This is cheap, so do it before
	// throttle: when there are no changes, there is nothing to throttle.
	currentCommit, err := storeStateHash(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: failed to get current commit: %v\n", err)
		return nil
	}
	if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
		debug.Logf("auto-export: no changes since last export\n")
		return nil
	}

	if !shouldExport(state, interval) {
		debug.Logf("auto-export: throttled (last export %s ago, interval %s)\n",
			time.Since(state.Timestamp).Round(time.Second), interval)
		return nil
	}

	if !allowEmptyOverwrite {
		if skip, existingCount, err := shouldSkipEmptyAutoExport(ctx, fullPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: failed to check existing JSONL: %v\n", err)
			return nil
		} else if skip {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: current database would export 0 issues, but %s already contains %d issue(s); refusing to overwrite. Run `bd init --from-jsonl` to import the JSONL file, or move it aside and retry.\n", fullPath, existingCount)
			return nil
		}
	}
	if !allowEmptyOverwrite {
		if missingIDs, err := missingJSONLIssueIDsInStore(ctx, fullPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: failed to compare existing JSONL against local store: %v\n", err)
			return nil
		} else if len(missingIDs) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: %s contains %d JSONL-only issue record(s) absent from the local Dolt store (%s); refusing to overwrite. Run `bd init --from-jsonl` to import the JSONL file, or move it aside and retry.\n", fullPath, len(missingIDs), strings.Join(sampleStrings(missingIDs, 5), ", "))
			return nil
		}
	}

	// Run the export — memories are excluded from auto-export because they
	// contain private agent context that must not reach git history (GH#3650).
	// Try an incremental re-encode first (dolt_diff over the changed-since-last-
	// export range); fall back to a full export when the store doesn't support
	// diffing, the commit range isn't diffable (e.g. state-hash values in server
	// mode where auto-commit is off), or the change set exceeds the threshold.
	exportStart := time.Now()
	issueCount, memoryCount, didIncremental, err := tryIncrementalExport(
		ctx, fullPath, state.LastDoltCommit, currentCommit,
	)
	if err != nil {
		debug.Logf("auto-export: incremental failed (%v); falling back to full\n", err)
	}
	if !didIncremental {
		issueCount, memoryCount, err = exportToFile(ctx, fullPath, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export failed: %v\n", err)
			return nil
		}
	}

	if dur := time.Since(exportStart); dur > slowExportWarnThreshold && !didIncremental {
		// Only warn on full exports — the whole point of incremental is to
		// get us under the threshold, so a slow incremental is noise.
		fmt.Fprintf(os.Stderr,
			"Warning: auto-export wrote %d issues in %s (runs after every bd command that changes state).\n"+
				"  Large closed-issue backlogs make this expensive. Levers:\n"+
				"    bd purge --force                         remove closed wisps (ephemeral beads)\n"+
				"    bd config set export.interval 10m        reduce auto-export frequency\n"+
				"    bd config set export.auto false          disable auto-export entirely\n",
			issueCount, dur.Round(time.Second))
	}

	mode := "full"
	if didIncremental {
		mode = "incremental"
	}
	debug.Logf("auto-export: wrote %d issues and %d memories to %s (%s, %s)\n",
		issueCount, memoryCount, fullPath, mode, time.Since(exportStart).Round(time.Millisecond))

	// Don't prime the throttle on an empty export (e.g. immediately after
	// `bd init`). Saving state here would block the first real `bd create`
	// from exporting for up to export.interval seconds even though the data
	// has changed. Remove the empty file too so users don't see a stale 0-byte
	// issues.jsonl before any issues exist.
	if issueCount == 0 && memoryCount == 0 {
		_ = os.Remove(fullPath)
		saveExportAutoState(beadsDir, &exportAutoState{
			LastDoltCommit: currentCommit,
			Timestamp:      time.Now(),
			Issues:         0,
			Memories:       0,
		})
		return nil
	}
	warnJSONLWithoutDoltRemote("auto-export")

	// Optional git add — skip when no-git-ops is set (GH#3314), when not in a
	// git repo (standalone BEADS_DIR flow), or when export.git-add is false.
	if config.GetBool("export.git-add") && !config.GetBool("no-git-ops") && isGitRepo() {
		if err := gitAddFile(fullPath); err != nil {
			return fmt.Errorf("auto-export: git add failed: %w", err)
		}
	}

	// Save state
	newState := exportAutoState{
		LastDoltCommit: currentCommit,
		Timestamp:      time.Now(),
		Issues:         issueCount,
		Memories:       memoryCount,
	}
	saveExportAutoState(beadsDir, &newState)
	return nil
}

// storeStateHash returns the hash used for auto-export change detection.
// It prefers a working-set-aware hash (storage.StateHasher) over the HEAD
// commit: in server mode dolt auto-commit is off, so writes stay in the
// working set and HEAD does not advance — HEAD-based detection would go
// permanently quiet after the first export.
func storeStateHash(ctx context.Context) (string, error) {
	if sh, ok := storage.UnwrapStore(store).(storage.StateHasher); ok {
		return sh.GetStateHash(ctx)
	}
	return store.GetCurrentCommit(ctx)
}

// shouldExport reports whether the throttle window has elapsed, or whether
// this is the first auto-export attempt. It returns false only when a recent
// export exists and the configured interval has not elapsed.
//
// Extracted from Jeremy Longshore's GH#4061 throttle-decision refactor.
func shouldExport(state *exportAutoState, interval time.Duration) bool {
	if state.Timestamp.IsZero() {
		return true
	}
	return time.Since(state.Timestamp) >= interval
}

func shouldSkipEmptyAutoExport(ctx context.Context, path string) (bool, int, error) {
	existingCount, err := countIssueRecordsInJSONL(path)
	if err != nil {
		return false, 0, err
	}
	if existingCount == 0 {
		return false, 0, nil
	}

	issues, err := store.SearchIssues(ctx, "", autoExportFilter(ctx))
	if err != nil {
		return false, 0, fmt.Errorf("failed to search issues: %w", err)
	}

	return len(issues) == 0, existingCount, nil
}

func countIssueRecordsInJSONL(path string) (int, error) {
	ids, err := issueIDsInJSONL(path)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

func missingJSONLIssueIDsInStore(ctx context.Context, path string) ([]string, error) {
	// GH#4988: only refuse when *in-scope* JSONL issue records are absent from
	// the store. Ephemeral wisps, templates, and infra types are outside
	// auto-export scope (buildAutoExportFilter / GH#3649). Compaction can
	// delete wisps from Dolt while an older JSONL still lists them; treating
	// those as hard orphans wedged auto-export permanently.
	existing, err := issueRecordsInJSONL(path)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return nil, nil
	}

	// Store-side query stays unfiltered and MaxRows: 0 (opts out of
	// BEADS_MAX_ROWS). This guard's failure mode is a permanent wedge, so a
	// narrower filter here — or a row cap — can only ever manufacture
	// phantom "missing" ids, never fewer (maphew review, GH#4988 follow-up).
	// buildAutoExportFilter is still consulted for infraTypeSet, which
	// classifies the JSONL-side records below.
	_, infraTypeSet := buildAutoExportFilter(ctx)
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{Limit: 0, MaxRows: 0})
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	localIDs := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		localIDs[issue.ID] = struct{}{}
	}

	missing := make([]string, 0)
	for _, rec := range existing {
		if rec.Ephemeral || rec.IsTemplate || infraTypeSet[string(rec.IssueType)] {
			continue
		}
		if _, ok := localIDs[rec.ID]; !ok {
			missing = append(missing, rec.ID)
		}
	}
	return missing, nil
}

// jsonlIssueRecord is a lightweight issue line from issues.jsonl used by
// auto-export safety guards.
type jsonlIssueRecord struct {
	ID         string
	IssueType  types.IssueType
	IsTemplate bool
	Ephemeral  bool
}

// issueRecordsInJSONL returns issue records (id + scope fields) from a JSONL
// export file. Tombstones and non-issue record types are skipped.
func issueRecordsInJSONL(path string) ([]jsonlIssueRecord, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	seen := make(map[string]struct{})
	var records []jsonlIssueRecord
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, err
		}

		if rawType, ok := raw["_type"]; ok {
			var recordType string
			if err := json.Unmarshal(rawType, &recordType); err == nil && recordType != "" && recordType != "issue" {
				continue
			}
		}

		var rec jsonlIssueRecord
		if rawID, ok := raw["id"]; ok {
			_ = json.Unmarshal(rawID, &rec.ID)
		}
		if rec.ID == "" {
			continue
		}

		var status string
		if rawStatus, ok := raw["status"]; ok {
			_ = json.Unmarshal(rawStatus, &status)
		}
		if status == "tombstone" {
			continue
		}

		if rawIT, ok := raw["issue_type"]; ok {
			_ = json.Unmarshal(rawIT, &rec.IssueType)
		}
		if rawTpl, ok := raw["is_template"]; ok {
			_ = json.Unmarshal(rawTpl, &rec.IsTemplate)
		}
		if rawEph, ok := raw["ephemeral"]; ok {
			_ = json.Unmarshal(rawEph, &rec.Ephemeral)
		}

		if _, ok := seen[rec.ID]; ok {
			continue
		}
		seen[rec.ID] = struct{}{}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

func issueIDsInJSONL(path string) ([]string, error) {
	records, err := issueRecordsInJSONL(path)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(records))
	for i, rec := range records {
		ids[i] = rec.ID
	}
	return ids, nil
}

func sampleStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	out := append([]string{}, values[:limit]...)
	out = append(out, "...")
	return out
}

func autoExportFilter(ctx context.Context) types.IssueFilter {
	filter, _ := buildAutoExportFilter(ctx)
	return filter
}

func buildAutoExportFilter(ctx context.Context) (types.IssueFilter, map[string]bool) {
	// MaxRows: 0 opts out of BEADS_MAX_ROWS — auto-export is a data-integrity
	// sweep and must not be capped (designer §4.1).
	filter := types.IssueFilter{Limit: 0, MaxRows: 0}
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
		infraTypes = dolt.DefaultInfraTypes()
	}
	infraTypeSet := make(map[string]bool, len(infraTypes))
	for _, t := range infraTypes {
		infraTypeSet[t] = true
	}
	sort.Strings(infraTypes)
	for _, t := range infraTypes {
		filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
	}
	isTemplate := false
	filter.IsTemplate = &isTemplate

	// Exclude ephemeral wisps — they are private/transient and must not
	// reach git history or external integrations (GH#3649).
	persistentOnly := false
	filter.Ephemeral = &persistentOnly

	return filter, infraTypeSet
}

// exportToFile atomically exports issues + memories to the given file path.
// Writes to a temp file first, then renames into place so readers never see
// a partial or truncated export. Used by both `bd export -o` and auto-export.
func exportToFile(ctx context.Context, path string, includeMemories bool) (issueCount, memoryCount int, err error) {
	w, err := atomicfile.Create(path, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create export file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = w.Abort()
		}
	}()

	filter, infraTypeSet := buildAutoExportFilter(ctx)
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to search issues: %w", err)
	}

	// Owner-exclusion safety net: auto-export writes the git-committed
	// .beads/issues.jsonl, so the export.exclude_owners config (and legacy
	// export.exclude_owner) must filter here too. Otherwise contributor/personal
	// issues that the manual `bd export` path excludes can still leak into git
	// history and PRs via auto-export (maphew review, be-e2nb). Auto-export has
	// no --exclude-owner flag, so only config-sourced owners apply here.
	if ownerExcludes := buildOwnerExcludeSet(ctx, storeExportSource{}, nil); len(ownerExcludes) > 0 {
		issues = filterOutOwners(issues, ownerExcludes)
	}

	// Store-presence set for the shrink guard (#4069 vs #4988): an
	// out-of-scope row already in the JSONL only blocks the rewrite when its
	// id is STILL in the store. Computed unfiltered here — not from the
	// in-scope `issues` above — because the guard needs to see infra/
	// template/ephemeral ids too, to tell "still in Dolt" apart from
	// "compacted away".
	storeIDs, err := storeKnownIssueIDs(ctx)
	if err != nil {
		return 0, 0, err
	}

	if err := guardAutoExportOverwrite(path, infraTypeSet, includeMemories, storeIDs); err != nil {
		return 0, 0, err
	}

	// Bulk-load relational data
	if len(issues) > 0 {
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
		allDeps, _ := store.GetDependencyRecordsForIssues(ctx, issueIDs)
		commentsMap, _ := store.GetCommentsForIssues(ctx, issueIDs)
		commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)
		depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

		for _, issue := range issues {
			issue.Labels = labelsMap[issue.ID]
			issue.Dependencies = allDeps[issue.ID]
			issue.Comments = commentsMap[issue.ID]
		}

		// Write issues
		enc := json.NewEncoder(w)
		for _, issue := range issues {
			counts := depCounts[issue.ID]
			if counts == nil {
				counts = &types.DependencyCounts{}
			}
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
			if err := enc.Encode(record); err != nil {
				return 0, 0, fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
			}
			issueCount++
		}
	}

	// Write memories
	if includeMemories {
		allConfig, err := store.GetAllConfig(ctx)
		if err == nil {
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
					return issueCount, memoryCount, fmt.Errorf("failed to marshal memory %s: %w", userKey, err)
				}
				if _, err := w.Write(data); err != nil {
					return issueCount, memoryCount, fmt.Errorf("failed to write memory: %w", err)
				}
				if _, err := w.Write([]byte{'\n'}); err != nil {
					return issueCount, memoryCount, fmt.Errorf("failed to write newline: %w", err)
				}
				memoryCount++
			}
		}
	}

	if err := w.Close(); err != nil {
		return issueCount, memoryCount, fmt.Errorf("failed to finalize export: %w", err)
	}

	return issueCount, memoryCount, nil
}

// storeKnownIssueIDs returns the set of issue ids currently present in the
// store, ignoring auto-export scope (infra/template/ephemeral rows are
// included). guardAutoExportOverwrite uses this to implement the
// store-presence rule: an out-of-scope row already in the JSONL is only
// safe to drop when its id is no longer in the store (a TTL-compacted wisp,
// GH#4988); if the store still has it, dropping it repeats #4069's data
// loss. Deliberately unfiltered + MaxRows: 0, for the same reason as
// missingJSONLIssueIDsInStore's store-side query: this guard's failure mode
// is a permanent wedge, so the query must stay maximally permissive.
func storeKnownIssueIDs(ctx context.Context) (map[string]struct{}, error) {
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{Limit: 0, MaxRows: 0})
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	ids := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		ids[issue.ID] = struct{}{}
	}
	return ids, nil
}

func guardAutoExportOverwrite(path string, infraTypes map[string]bool, includeMemories bool, storeIDs map[string]struct{}) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("auto-export shrink guard: inspect existing JSONL: %w", err)
	}
	defer func() { _ = f.Close() }()

	var stats autoExportOverwriteStats
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := classifyExistingAutoExportRecord([]byte(line), infraTypes, includeMemories, storeIDs, &stats); err != nil {
			return fmt.Errorf("auto-export shrink guard: inspect existing JSONL line %d: %w", lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("auto-export shrink guard: inspect existing JSONL: %w", err)
	}

	// Store-presence rule (#4069 vs #4988): block on memories (when
	// excluded), unknown record types, and out-of-scope issue rows whose id
	// is STILL present in the store — those are exactly what #4069 says we
	// must not silently drop. An out-of-scope row absent from the store
	// (e.g. a TTL-compacted wisp) is safe to drop and does not block.
	if stats.FilteredRecords == 0 {
		return nil
	}
	return fmt.Errorf("auto-export shrink guard: refusing to overwrite %s because it contains %d record(s) outside auto-export scope (%d memories, %d infra/template/ephemeral issues, %d unknown); run an explicit export if you want to replace it", path, stats.FilteredRecords, stats.Memories, stats.FilteredIssues, stats.UnknownRecords)
}

type autoExportOverwriteStats struct {
	FilteredRecords int // blocking total: Memories + FilteredIssues + UnknownRecords
	Memories        int
	FilteredIssues  int // infra/template/ephemeral issues still present in the store — blocking (restores GH#4069)
	UnknownRecords  int
}

func classifyExistingAutoExportRecord(line []byte, infraTypes map[string]bool, includeMemories bool, storeIDs map[string]struct{}, stats *autoExportOverwriteStats) error {
	var record struct {
		Type       string          `json:"_type"`
		IssueType  types.IssueType `json:"issue_type"`
		IsTemplate bool            `json:"is_template"`
		Ephemeral  bool            `json:"ephemeral"`
		ID         string          `json:"id"`
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return err
	}

	switch record.Type {
	case "memory":
		if !includeMemories {
			stats.FilteredRecords++
			stats.Memories++
		}
		return nil
	case "", "issue":
		if record.ID == "" {
			stats.FilteredRecords++
			stats.UnknownRecords++
			return nil
		}
		if infraTypes[string(record.IssueType)] || record.IsTemplate || record.Ephemeral {
			// Store-presence rule: only block when the row still exists in
			// Dolt (#4069's exact scenario). A row that's gone from the
			// store (TTL-compacted wisp — GH#4988) is safe to drop; the
			// rewrite doesn't lose anything the store didn't already lose.
			if _, present := storeIDs[record.ID]; present {
				stats.FilteredRecords++
				stats.FilteredIssues++
			}
		}
		return nil
	default:
		stats.FilteredRecords++
		stats.UnknownRecords++
		return nil
	}
}

func loadExportAutoState(beadsDir string) *exportAutoState {
	path := filepath.Join(beadsDir, exportAutoStateFile)
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		debug.Logf("auto-export: state-load miss (%s): %v\n", path, err)
		return &exportAutoState{}
	}
	var state exportAutoState
	if err := json.Unmarshal(data, &state); err != nil {
		debug.Logf("auto-export: state-load parse error for %s (%d bytes): %v; first 200 bytes=%q\n",
			path, len(data), err, string(data[:min(len(data), 200)]))
		return &exportAutoState{}
	}
	return &state
}

func saveExportAutoState(beadsDir string, state *exportAutoState) {
	path := filepath.Join(beadsDir, exportAutoStateFile)
	data, err := json.Marshal(state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-export: failed to marshal state: %v\n", err)
		return
	}
	// Write atomically — concurrent bd invocations read this file in
	// maybeAutoExport, and a plain os.WriteFile leaves a brief window
	// after O_TRUNC but before the data lands where readers see an empty
	// file. An empty state looks like "no prior commit" to the rest of
	// the pipeline, which forces a full export on a repo where the
	// incremental path would otherwise fire.
	if err := atomicfile.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-export: failed to save state: %v\n", err)
	}
}

// gitAddFile stages a file in the enclosing git repo. When called from
// inside a git hook, it scrubs inherited GIT_* env vars (so git
// rediscovers the repo from cwd rather than treating cmd.Dir as the
// worktree root) and skips staging when the target is outside the hook's
// worktree (the .beads/redirect case, where staging would pollute the
// main repo's index). See GH#3311, scrubGitHookEnv, hookWorkTreeRoot.
func gitAddFile(path string) error {
	if wt := hookWorkTreeRoot(); wt != "" && !pathInsideDir(path, wt) {
		// Running inside a hook AND target is outside the hook's worktree.
		// Staging here would pollute a different repo's index; skip.
		return nil
	}

	env := scrubGitHookEnv(os.Environ())
	if lockPath, err := gitIndexLockPath(path, env); err == nil && lockPath != "" {
		if _, statErr := os.Stat(lockPath); statErr == nil {
			return fmt.Errorf("git index is locked at %s; skipping auto-stage", lockPath)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("failed to check git index lock %s: %w", lockPath, statErr)
		}
	} else if err != nil {
		debug.Logf("auto-export: git add lock preflight skipped: %v\n", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitAddTimeout)
	defer cancel()
	// Pass the basename only, defensively: cmd.Dir is the parent of path, so
	// a full path argument would double-root (cd .beads && git add
	// .beads/issues.jsonl → pathspec looks under .beads/.beads/) if a caller
	// ever passed a relative path here. Both current callers pass absolute
	// paths, so this guards against a regression rather than fixing a live
	// failure. See GH#4351.
	// Keep cmd.Dir = parent so GH#3311 hook worktree staging still resolves
	// the index path under the repo root (not bare "issues.jsonl" at root).
	cmd := exec.CommandContext(ctx, "git", "add", "--", filepath.Base(path))
	cmd.Dir = filepath.Dir(path)
	cmd.Env = env
	// Capture combined output so the caller's warning surfaces git's stderr
	// (e.g. "paths are ignored", "Unable to create index.lock") instead of
	// just the exit-status text.
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("git add timed out after %s", gitAddTimeout)
	}
	if err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("%w: %s", err, trimmed)
		}
		return err
	}
	return nil
}

func gitIndexLockPath(path string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitAddTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = filepath.Dir(path)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git rev-parse timed out after %s", gitAddTimeout)
	}
	if err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return "", fmt.Errorf("%w: %s", err, trimmed)
		}
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", nil
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(path), gitDir)
	}
	return filepath.Join(gitDir, "index.lock"), nil
}

// scrubGitHookEnv returns env with the GIT_* variables that can poison
// git's repo/worktree auto-discovery or object-store resolution removed,
// so git falls back to auto-discovery from cwd. The scrub is
// unconditional: if a user has intentionally exported any of these vars
// for scripting purposes, they will be stripped from the git-add child
// process. That is the correct trade-off here; we never want beads'
// auto-stage to honor a GIT_DIR pointing at an unrelated repo.
//
// Covered vars:
//   - Repo/worktree discovery: GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR,
//     GIT_PREFIX, GIT_CEILING_DIRECTORIES, GIT_DISCOVERY_ACROSS_FILESYSTEM
//   - Index routing: GIT_INDEX_FILE
//   - Object routing: GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES
//   - Config injection (any GIT_CONFIG* — e.g. GIT_CONFIG_PARAMETERS set
//     when the parent ran `git -c core.worktree=… commit`): the whole
//     GIT_CONFIG namespace, which includes _COUNT, _KEY_n, _VALUE_n,
//     _GLOBAL, _SYSTEM, _NOSYSTEM, and the legacy GIT_CONFIG itself.
func scrubGitHookEnv(env []string) []string {
	// The GIT_CONFIG prefix (no trailing "=") is intentional: it matches
	// GIT_CONFIG=, GIT_CONFIG_COUNT=, GIT_CONFIG_KEY_n=, GIT_CONFIG_VALUE_n=,
	// GIT_CONFIG_PARAMETERS=, GIT_CONFIG_GLOBAL=, GIT_CONFIG_SYSTEM=, and
	// GIT_CONFIG_NOSYSTEM= — the whole family — in one entry. No standard
	// git env var starts with GIT_CONFIG that we want to preserve.
	prefixes := []string{
		"GIT_DIR=",
		"GIT_WORK_TREE=",
		"GIT_INDEX_FILE=",
		"GIT_COMMON_DIR=",
		"GIT_PREFIX=",
		"GIT_OBJECT_DIRECTORY=",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
		"GIT_CEILING_DIRECTORIES=",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=",
		"GIT_CONFIG",
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, p := range prefixes {
			if strings.HasPrefix(e, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

// hookWorkTreeRoot returns the root of the worktree whose git hook we
// are running inside, based on the inherited GIT_DIR env var. Returns ""
// when GIT_DIR is not set (the normal non-hook case) or cannot be
// resolved to a work-tree.
//
// Resolution rules:
//   - In a linked worktree, GIT_DIR points at main/.git/worktrees/<name>
//     and that directory contains a "gitdir" file whose contents are the
//     absolute path to the worktree's .git FILE. The worktree root is
//     the parent of that .git file.
//   - In a non-worktree, GIT_DIR is typically ".git" or "<repo>/.git";
//     the worktree root is its parent.
func hookWorkTreeRoot() string {
	gitDir := os.Getenv("GIT_DIR")
	if gitDir == "" {
		return ""
	}
	var root string
	//nolint:gosec // G304: path is GIT_DIR/gitdir, a well-known git internal file.
	if data, err := os.ReadFile(filepath.Join(gitDir, "gitdir")); err == nil {
		if dotGit := strings.TrimSpace(string(data)); dotGit != "" {
			root = filepath.Dir(dotGit)
		}
	}
	if root == "" && filepath.Base(gitDir) == ".git" {
		root = filepath.Dir(gitDir)
	}
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return abs
}

// pathInsideDir reports whether path is the same as dir or a descendant
// of dir, after resolving symlinks on both sides. Returns false on any
// resolution error (conservative: when in doubt, treat as outside).
//
// Resolves the PARENT of path rather than path itself, which handles the
// common "target file does not yet exist" case: on macOS /tmp is a
// symlink to /private/tmp, so asymmetric EvalSymlinks on a nonexistent
// file vs its existing parent would otherwise produce a spurious false.
// Callers (gitAddFile) always pass a path whose parent exists (either
// beadsDir, which FindBeadsDir verified, or a directory just created by
// the export write), so this single-level resolution is sufficient.
func pathInsideDir(path, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(absPath)); err == nil {
		absPath = filepath.Join(r, filepath.Base(absPath))
	}
	if r, err := filepath.EvalSymlinks(absDir); err == nil {
		absDir = r
	}
	sep := string(filepath.Separator)
	return absPath == absDir || strings.HasPrefix(absPath, absDir+sep)
}

// tryIncrementalExport attempts to update an existing export file in place
// by re-encoding only the issues that changed between fromCommit and
// toCommit (per dolt_diff). Returns didIncremental=false when any
// precondition fails so the caller can fall back to a full export.
func tryIncrementalExport(ctx context.Context, fullPath, fromCommit, toCommit string) (issueCount, memoryCount int, didIncremental bool, err error) {
	if fromCommit == "" {
		debug.Logf("auto-export: incremental skipped — no prior commit hash recorded\n")
		return 0, 0, false, nil
	}
	if fromCommit == toCommit {
		debug.Logf("auto-export: incremental skipped — commit hash unchanged (%s)\n", fromCommit)
		return 0, 0, false, nil
	}
	// Existing file is a hard requirement — without it we have nothing to
	// patch, and a full export is the right answer anyway.
	if _, statErr := os.Stat(fullPath); statErr != nil {
		debug.Logf("auto-export: incremental skipped — existing file not found: %v\n", statErr)
		return 0, 0, false, nil
	}
	ds, ok := storage.UnwrapStore(store).(storage.DiffStore)
	if !ok {
		debug.Logf("auto-export: incremental skipped — store does not implement DiffStore\n")
		return 0, 0, false, nil
	}
	changed, diffErr := ds.ChangedIssueIDs(ctx, fromCommit, toCommit)
	if diffErr != nil {
		// Commits may be unreachable (history rewritten), in which case we
		// cannot trust the diff. Fall back silently.
		return 0, 0, false, diffErr
	}
	debug.Logf("auto-export: diff %s..%s → upserted=%d removed=%d\n",
		fromCommit, toCommit, len(changed.Upserted), len(changed.Removed))
	total := len(changed.Upserted) + len(changed.Removed)
	if total == 0 {
		// The commit hash changed but no relevant tables did. Still a
		// valid "incremental" outcome: no issues to rewrite, just refresh
		// the file's memory section so nothing regresses.
		issueCount, memoryCount, err = rewriteExportFile(ctx, fullPath, nil, nil, nil)
		if err != nil {
			return 0, 0, false, err
		}
		return issueCount, memoryCount, true, nil
	}
	if total > incrementalExportThreshold {
		debug.Logf("auto-export: %d changes exceeds threshold %d; full export\n",
			total, incrementalExportThreshold)
		return 0, 0, false, nil
	}

	// Fetch fresh data for upserted IDs and apply the same
	// template/infra filter the full export uses.
	var records map[string][]byte
	droppedByFilter := make(map[string]bool)
	if len(changed.Upserted) > 0 {
		issues, fetchErr := store.GetIssuesByIDs(ctx, changed.Upserted)
		if fetchErr != nil {
			return 0, 0, false, fmt.Errorf("GetIssuesByIDs: %w", fetchErr)
		}
		infraSet := store.GetInfraTypes(ctx)
		filtered := make([]*types.Issue, 0, len(issues))
		for _, iss := range issues {
			// Record IDs that GetIssuesByIDs returned but we deliberately
			// filtered out. Those DO need dropping from the export because
			// the full-export path excludes them; leaving a stale record
			// in place would diverge the two outputs.
			if iss.IsTemplate {
				droppedByFilter[iss.ID] = true
				continue
			}
			if len(infraSet) > 0 && infraSet[string(iss.IssueType)] {
				droppedByFilter[iss.ID] = true
				continue
			}
			filtered = append(filtered, iss)
		}
		records, err = encodeIssueRecords(ctx, filtered)
		if err != nil {
			return 0, 0, false, err
		}
		// NOTE: upserted IDs absent from GetIssuesByIDs's result are
		// intentionally NOT dropped. We used to flag them as "removed",
		// which is destructive: any hiccup in the fetch path (partial
		// failure, concurrent close, transient routing weirdness) would
		// wipe otherwise-valid records. Real deletions land in
		// changed.Removed from the issues-table diff; rely on that.
	}

	removed := make(map[string]bool, len(changed.Removed)+len(droppedByFilter))
	for _, id := range changed.Removed {
		removed[id] = true
	}
	for id := range droppedByFilter {
		removed[id] = true
	}

	issueCount, memoryCount, err = rewriteExportFile(ctx, fullPath, records, removed, changed.Upserted)
	if err != nil {
		return 0, 0, false, err
	}
	return issueCount, memoryCount, true, nil
}

// rewriteExportFile applies a set of upserts and removals to an existing
// JSONL export and writes the result atomically. upsertOrder preserves
// append order for brand-new issue IDs; previously-present IDs keep their
// original file position even when their bodies are replaced.
func rewriteExportFile(ctx context.Context, path string, upserts map[string][]byte, removed map[string]bool, upsertOrder []string) (issueCount, memoryCount int, err error) {
	lines, err := loadExistingIssueLines(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read existing export: %w", err)
	}

	// Apply upserts: replace-in-place for known IDs, append (in input
	// order) for brand-new IDs. This keeps stable ordering across runs
	// instead of scrambling the file on every change.
	for _, id := range upsertOrder {
		line, ok := upserts[id]
		if !ok {
			continue
		}
		lines.set(id, line)
	}

	for id := range removed {
		lines.remove(id)
	}

	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath) //nolint:gosec // user-configured output path
	if err != nil {
		return 0, 0, err
	}
	bw := bufio.NewWriter(f)

	abort := func(e error) (int, int, error) {
		_ = bw.Flush()
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return 0, 0, e
	}

	lines.each(func(id string, line []byte) {
		if err != nil {
			return
		}
		if _, werr := bw.Write(line); werr != nil {
			err = werr
			return
		}
		if werr := bw.WriteByte('\n'); werr != nil {
			err = werr
			return
		}
		issueCount++
	})
	if err != nil {
		return abort(err)
	}

	memoryCount, err = writeMemoryRecords(ctx, bw)
	if err != nil {
		return abort(fmt.Errorf("write memories: %w", err))
	}

	if err := bw.Flush(); err != nil {
		return abort(fmt.Errorf("flush: %w", err))
	}
	if err := f.Sync(); err != nil {
		return abort(fmt.Errorf("sync: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("rename: %w", err)
	}
	return issueCount, memoryCount, nil
}

// orderedIssueLines is an insertion-ordered map of issue ID → raw JSONL
// line (without trailing newline). Removal is O(1) via the map; the order
// slice may contain IDs that have since been removed, which the iterator
// skips.
type orderedIssueLines struct {
	order []string
	lines map[string][]byte
}

func newOrderedIssueLines() *orderedIssueLines {
	return &orderedIssueLines{lines: make(map[string][]byte)}
}

func (o *orderedIssueLines) set(id string, line []byte) {
	if _, present := o.lines[id]; !present {
		o.order = append(o.order, id)
	}
	o.lines[id] = line
}

func (o *orderedIssueLines) remove(id string) {
	delete(o.lines, id)
}

func (o *orderedIssueLines) each(fn func(id string, line []byte)) {
	for _, id := range o.order {
		if line, ok := o.lines[id]; ok {
			fn(id, line)
		}
	}
}

// loadExistingIssueLines parses a JSONL export file and returns an
// orderedIssueLines containing only the issue records (memory records are
// skipped — the caller re-emits them from the current config). Missing
// file returns an empty set so first-incremental callers behave like a
// fresh write.
func loadExistingIssueLines(path string) (*orderedIssueLines, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the user-configured export path
	if err != nil {
		if os.IsNotExist(err) {
			return newOrderedIssueLines(), nil
		}
		return nil, err
	}
	out := newOrderedIssueLines()
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		if bytes.Contains(raw, []byte(`"_type":"memory"`)) {
			continue
		}
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		if probe.ID == "" {
			continue
		}
		// bytes.Split shares the backing buffer; copy so later appends
		// can't silently overwrite our captured line.
		cpy := make([]byte, len(raw))
		copy(cpy, raw)
		out.set(probe.ID, cpy)
	}
	return out, nil
}

// encodeIssueRecords produces the JSON wire form for a batch of issues
// using the same bulk-loader pattern the full export uses. Returns a map
// from issue ID → line bytes (no trailing newline).
func encodeIssueRecords(ctx context.Context, issues []*types.Issue) (map[string][]byte, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	ids := make([]string, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	labelsMap, _ := store.GetLabelsForIssues(ctx, ids)
	allDeps, _ := store.GetDependencyRecordsForIssues(ctx, ids)
	commentsMap, _ := store.GetCommentsForIssues(ctx, ids)
	commentCounts, _ := store.GetCommentCounts(ctx, ids)
	depCounts, _ := store.GetDependencyCounts(ctx, ids)

	out := make(map[string][]byte, len(issues))
	for _, iss := range issues {
		iss.Labels = labelsMap[iss.ID]
		iss.Dependencies = allDeps[iss.ID]
		iss.Comments = commentsMap[iss.ID]
		counts := depCounts[iss.ID]
		if counts == nil {
			counts = &types.DependencyCounts{}
		}
		sanitizeZeroTime(iss)
		rec := &types.IssueWithCounts{
			Issue:           iss,
			DependencyCount: counts.DependencyCount,
			DependentCount:  counts.DependentCount,
			CommentCount:    commentCounts[iss.ID],
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", iss.ID, err)
		}
		out[iss.ID] = data
	}
	return out, nil
}

// writeMemoryRecords emits memory records (kv.memory.* config entries) to
// w in the same format the full export uses. Errors from GetAllConfig are
// swallowed to match the full-export path's behavior.
func writeMemoryRecords(ctx context.Context, w io.Writer) (int, error) {
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return 0, nil //nolint:nilerr // match full-export tolerance
	}
	fullPrefix := kvPrefix + memoryPrefix
	count := 0
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
			continue
		}
		if _, err := w.Write(data); err != nil {
			return count, err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
