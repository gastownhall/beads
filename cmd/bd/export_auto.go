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
	"strings"
	"time"

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

// maybeAutoExport writes a git-tracked JSONL file if enabled and due.
// Called from PersistentPostRun after auto-backup.
func maybeAutoExport(ctx context.Context) {
	// Skip when running as a git hook to avoid re-export during pre-commit.
	if os.Getenv("BD_GIT_HOOK") == "1" {
		debug.Logf("auto-export: skipping — running as git hook\n")
		return
	}

	if !config.GetBool("export.auto") {
		return
	}
	if store == nil {
		return
	}
	if lm, ok := storage.UnwrapStore(store).(storage.LifecycleManager); ok && lm.IsClosed() {
		return
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	// Load state and check throttle
	state := loadExportAutoState(beadsDir)
	debug.Logf("auto-export: loaded state from %s: last_commit=%q timestamp=%s issues=%d\n",
		beadsDir, state.LastDoltCommit, state.Timestamp.Format(time.RFC3339), state.Issues)
	interval := config.GetDuration("export.interval")
	if interval == 0 {
		interval = 60 * time.Second
	}
	if !state.Timestamp.IsZero() && time.Since(state.Timestamp) < interval {
		debug.Logf("auto-export: throttled (last export %s ago, interval %s)\n",
			time.Since(state.Timestamp).Round(time.Second), interval)
		return
	}

	// Change detection via Dolt commit hash
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: failed to get current commit: %v\n", err)
		return
	}
	if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
		debug.Logf("auto-export: no changes since last export\n")
		return
	}

	// Determine output path
	exportPath := config.GetString("export.path")
	if exportPath == "" {
		if globalFlag {
			exportPath = "global-issues.jsonl"
		} else {
			exportPath = "issues.jsonl"
		}
	}
	fullPath := filepath.Join(beadsDir, exportPath)

	exportStart := time.Now()
	issueCount, memoryCount, didIncremental, err := tryIncrementalExport(
		ctx, fullPath, state.LastDoltCommit, currentCommit,
	)
	if err != nil {
		debug.Logf("auto-export: incremental failed (%v); falling back to full\n", err)
	}
	if !didIncremental {
		issueCount, memoryCount, err = exportToFile(ctx, fullPath, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export failed: %v\n", err)
			return
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
		return
	}

	// Optional git add — skip silently when not in a git repo (standalone
	// BEADS_DIR flow) to avoid noisy "exit status 128" warnings on every write.
	if config.GetBool("export.git-add") && isGitRepo() {
		if err := gitAddFile(fullPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export: git add failed: %v\n", err)
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
}

// exportToFile exports issues + memories to the given file path.
// Used by both `bd export -o` and auto-export.
func exportToFile(ctx context.Context, path string, includeMemories bool) (issueCount, memoryCount int, err error) {
	f, err := os.Create(path) //nolint:gosec // user-configured output path
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create export file: %w", err)
	}
	defer f.Close()

	// Build filter: exclude infra types and templates
	filter := types.IssueFilter{Limit: 0}
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
	for _, t := range infraTypes {
		filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
	}
	isTemplate := false
	filter.IsTemplate = &isTemplate

	// Fetch issues
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to search issues: %w", err)
	}

	// Also fetch wisps
	ephemeral := true
	wispFilter := filter
	wispFilter.Ephemeral = &ephemeral
	wispIssues, err := store.SearchIssues(ctx, "", wispFilter)
	if err == nil && len(wispIssues) > 0 {
		issues = append(issues, wispIssues...)
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
		enc := json.NewEncoder(f)
		for _, issue := range issues {
			counts := depCounts[issue.ID]
			if counts == nil {
				counts = &types.DependencyCounts{}
			}
			sanitizeZeroTime(issue)
			record := &types.IssueWithCounts{
				Issue:           issue,
				DependencyCount: counts.DependencyCount,
				DependentCount:  counts.DependentCount,
				CommentCount:    commentCounts[issue.ID],
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
				if _, err := f.Write(data); err != nil {
					return issueCount, memoryCount, fmt.Errorf("failed to write memory: %w", err)
				}
				if _, err := f.Write([]byte{'\n'}); err != nil {
					return issueCount, memoryCount, fmt.Errorf("failed to write newline: %w", err)
				}
				memoryCount++
			}
		}
	}

	if err := f.Sync(); err != nil {
		return issueCount, memoryCount, fmt.Errorf("failed to sync: %w", err)
	}

	return issueCount, memoryCount, nil
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-export: failed to save state (tmp write): %v\n", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "Warning: auto-export: failed to save state (rename): %v\n", err)
	}
}

// gitAddFile stages a file in the enclosing git repo.
func gitAddFile(path string) error {
	cmd := exec.Command("git", "add", path)
	cmd.Dir = filepath.Dir(path)
	return cmd.Run()
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
