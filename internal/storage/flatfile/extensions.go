// extensions.go implements the DoltStorage extension interfaces needed for
// HookFiringStore compatibility: MergeSlot, Slots, BulkIssueStore,
// DependencyQueryStore, AnnotationStore, ConfigMetadataStore,
// AdvancedQueryStore, CompactionStore, and misc Storage methods.
package flatfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// ── MergeSlot ─────────────────────────────────────────────────────────

type mergeSlotState struct {
	SlotID  string   `json:"slot_id"`
	Holder  string   `json:"holder"`
	Waiters []string `json:"waiters"`
}

// mergeSlotID mirrors storage.MergeSlotID: "<issue_prefix>-merge-slot",
// falling back to "bd-merge-slot" when the prefix is not configured.
func (s *FlatFileStore) mergeSlotID(ctx context.Context) string {
	prefix := "bd"
	if p, err := s.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
		prefix = strings.TrimSuffix(p, "-")
	}
	return prefix + "-merge-slot"
}

func (s *FlatFileStore) MergeSlotCreate(ctx context.Context, actor string) (*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	defer s.lockWrite()()
	slotID := s.mergeSlotID(ctx)
	// Idempotent, mirroring storage.MergeSlotCreateImpl: an existing slot is
	// returned untouched so a re-run never wipes the current holder/waiters.
	state, err := s.readMergeSlot(slotID)
	switch {
	case err == nil:
		return mergeSlotIssue(state), nil
	case !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}
	state = &mergeSlotState{SlotID: slotID}
	if err := s.writeMergeSlot(state); err != nil {
		return nil, err
	}
	return mergeSlotIssue(state), nil
}

// mergeSlotIssue projects the slot state as the issue shape the interface
// returns (the SQL reference stores the slot as a real bead).
func mergeSlotIssue(state *mergeSlotState) *types.Issue {
	return &types.Issue{ID: state.SlotID, Title: "Merge Slot", IssueType: "slot"}
}

func (s *FlatFileStore) MergeSlotCheck(ctx context.Context) (*storage.MergeSlotStatus, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	state, err := s.readMergeSlot(s.mergeSlotID(ctx))
	if err != nil {
		return nil, err
	}
	return &storage.MergeSlotStatus{
		SlotID:    state.SlotID,
		Available: state.Holder == "",
		Holder:    state.Holder,
		Waiters:   state.Waiters,
	}, nil
}

func (s *FlatFileStore) MergeSlotAcquire(ctx context.Context, holder, _ string, wait bool) (*storage.MergeSlotResult, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if holder == "" {
		return nil, fmt.Errorf("merge-slot acquire: holder must not be empty")
	}
	defer s.lockWrite()()
	state, err := s.readMergeSlot(s.mergeSlotID(ctx))
	if err != nil {
		return nil, err
	}
	if state.Holder == "" {
		state.Holder = holder
		if err := s.writeMergeSlot(state); err != nil {
			return nil, err
		}
		return &storage.MergeSlotResult{SlotID: state.SlotID, Acquired: true, Holder: holder}, nil
	}
	if state.Holder == holder {
		return &storage.MergeSlotResult{SlotID: state.SlotID, Acquired: true, Holder: holder}, nil
	}
	result := &storage.MergeSlotResult{SlotID: state.SlotID, Acquired: false, Holder: state.Holder}
	if wait {
		// Mirror storage.MergeSlotAcquireImpl: queue the caller once and
		// report its position as the current queue length.
		alreadyWaiting := false
		for _, w := range state.Waiters {
			if w == holder {
				alreadyWaiting = true
				break
			}
		}
		if !alreadyWaiting {
			state.Waiters = append(state.Waiters, holder)
			if err := s.writeMergeSlot(state); err != nil {
				return nil, err
			}
		}
		result.Waiting = true
		result.Position = len(state.Waiters)
	}
	return result, nil
}

func (s *FlatFileStore) MergeSlotRelease(ctx context.Context, holder, _ string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	state, err := s.readMergeSlot(s.mergeSlotID(ctx))
	if err != nil {
		return err
	}
	// Mirror storage.MergeSlotReleaseImpl: a non-matching holder is an error,
	// an empty holder force-releases, and releasing a free slot is idempotent.
	if holder != "" && state.Holder != holder {
		return fmt.Errorf("slot held by %s, not %s", state.Holder, holder)
	}
	if state.Holder == "" {
		return nil
	}
	state.Holder = ""
	return s.writeMergeSlot(state)
}

// readMergeSlot loads the slot state; a never-created slot is ErrNotFound
// (parity with storage.MergeSlotCheckImpl/AcquireImpl, which require
// 'bd merge-slot create' first).
func (s *FlatFileStore) readMergeSlot(slotID string) (*mergeSlotState, error) {
	data, err := os.ReadFile(s.mergeslotPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("merge slot not found: %s (run 'bd merge-slot create' first): %w", slotID, storage.ErrNotFound)
		}
		return nil, err
	}
	var state mergeSlotState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("flatfile: parse merge slot: %w", err)
	}
	return &state, nil
}

func (s *FlatFileStore) writeMergeSlot(state *mergeSlotState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("flatfile: marshal merge slot: %w", err)
	}
	data = append(data, '\n')
	if err := s.atomicWrite(s.mergeslotPath, data); err != nil {
		return fmt.Errorf("flatfile: write merge slot: %w", err)
	}
	return nil
}

// ── Metadata Slots ────────────────────────────────────────────────────

func (s *FlatFileStore) SlotSet(_ context.Context, issueID, key, value, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(issueID)
	if err != nil {
		return err
	}
	meta := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
			return fmt.Errorf("flatfile: parse issue metadata: %w", err)
		}
	}
	meta[key] = value
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("flatfile: marshal issue metadata: %w", err)
	}
	oldData, _ := json.Marshal(issue)
	issue.Metadata = raw
	issue.UpdatedAt = time.Now().UTC()
	if err := s.writeIssue(issue); err != nil {
		return err
	}
	// sqlkit routes SlotSet through UpdateIssue, which records an event;
	// mirror its payloads (old issue snapshot, {"metadata": <json>} updates).
	return s.recordMetadataEvent(issueID, actor, oldData, raw)
}

// recordMetadataEvent records the event UpdateIssue would have minted for a
// metadata-only update, matching the sqlkit slot paths that inherit it.
func (s *FlatFileStore) recordMetadataEvent(issueID, actor string, oldData, rawMeta []byte) error {
	updates := map[string]interface{}{"metadata": string(rawMeta)}
	newData, _ := json.Marshal(updates)
	return s.recordEvent(issueID, types.EventUpdated, actor, string(oldData), string(newData))
}

func (s *FlatFileStore) SlotGet(_ context.Context, issueID, key string) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	issue, err := s.readIssue(issueID)
	if err != nil {
		return "", err
	}
	if len(issue.Metadata) == 0 {
		return "", fmt.Errorf("flatfile: no slot %q on %s: no metadata", key, issueID)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
		return "", fmt.Errorf("flatfile: parse issue metadata: %w", err)
	}
	v, ok := meta[key]
	if !ok {
		return "", fmt.Errorf("flatfile: no slot %q on %s: key not found", key, issueID)
	}
	// Mirrors sqlkit SlotGet: string values return raw; every other JSON
	// type is re-marshaled to its JSON text form.
	if str, ok := v.(string); ok {
		return str, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("flatfile: marshal slot value %s.%s: %w", issueID, key, err)
	}
	return string(raw), nil
}

func (s *FlatFileStore) SlotClear(_ context.Context, issueID, key, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(issueID)
	if err != nil {
		return err
	}
	if len(issue.Metadata) == 0 {
		return nil
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
		return fmt.Errorf("flatfile: parse issue metadata: %w", err)
	}
	// Mirror sqlkit.SlotClear: clearing an absent key is a no-op that must
	// not rewrite the issue (and so must not bump updated_at).
	if _, ok := meta[key]; !ok {
		return nil
	}
	delete(meta, key)
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("flatfile: marshal issue metadata: %w", err)
	}
	oldData, _ := json.Marshal(issue)
	issue.Metadata = raw
	issue.UpdatedAt = time.Now().UTC()
	if err := s.writeIssue(issue); err != nil {
		return err
	}
	// sqlkit routes SlotClear through UpdateIssue, which records an event.
	return s.recordMetadataEvent(issueID, actor, oldData, raw)
}

// ── BulkIssueStore ───────────────────────────────────────────────────

// DeleteIssues mirrors issueops.DeleteIssuesInTx: wisp/durable partitioning,
// cascade expansion over inbound edges, the orphan guard (non-cascade,
// non-force errors and deletes nothing when an external dependent exists),
// force-orphan reporting, dry-run child-row counts, and FK-cascade cleanup
// of dependency edges referencing the deleted issues.
// The multi-file delete runs under runAtomic: the SQL backends wrap
// issueops.DeleteIssuesInTx in withMutationTx, so a mid-batch failure
// (I/O error, kill) must delete nothing here too — the pre-image journal
// restores every removed file, and no survivor is left with dangling edges.
func (s *FlatFileStore) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	var result *types.DeleteIssuesResult
	err := s.runAtomic(ctx, func() error {
		var err error
		// result survives an error return: the orphan guard reports
		// OrphanedIssues alongside its error, like the SQL reference.
		result, err = s.deleteIssuesJournaled(ids, cascade, force, dryRun)
		return err
	})
	return result, err
}

// deleteIssuesJournaled is DeleteIssues' body; the caller has armed the
// pre-image journal (runAtomic), so any error rolls all removals back.
func (s *FlatFileStore) deleteIssuesJournaled(ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	defer s.lockWrite()()
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{}, nil
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*types.Issue, len(all))
	dependents := make(map[string][]string) // target id -> inbound source ids
	for _, issue := range all {
		byID[issue.ID] = issue
		for _, dep := range issue.Dependencies {
			dependents[dep.DependsOnID] = append(dependents[dep.DependsOnID], issue.ID)
		}
	}
	isStoredWisp := func(id string) bool {
		issue, ok := byID[id]
		return ok && issueops.IsWisp(issue)
	}

	// Partition input ids: active wisps bypass the cascade/guard machinery,
	// exactly like PartitionWispIDsInTx; missing ids stay "regular".
	var initialWispIDs, regularIDs []string
	for _, id := range ids {
		if isStoredWisp(id) {
			initialWispIDs = append(initialWispIDs, id)
		} else {
			regularIDs = append(regularIDs, id)
		}
	}
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	result := &types.DeleteIssuesResult{}

	expandedRegularIDs := regularIDs
	switch {
	case cascade:
		// BFS over inbound edges: delete the whole dependent subtree.
		closure := make(map[string]bool, len(regularIDs))
		queue := append([]string(nil), regularIDs...)
		for _, id := range regularIDs {
			closure[id] = true
		}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, src := range dependents[id] {
				if !closure[src] {
					closure[src] = true
					queue = append(queue, src)
				}
			}
		}
		expandedRegularIDs = make([]string, 0, len(closure))
		for id := range closure {
			expandedRegularIDs = append(expandedRegularIDs, id)
		}
		sort.Strings(expandedRegularIDs)
	case !force:
		// Orphan guard: any dependent outside the deletion set aborts the
		// whole batch before anything is deleted.
		for _, id := range regularIDs {
			var external []string
			for _, src := range dependents[id] {
				if !idSet[src] {
					external = append(external, src)
				}
			}
			if len(external) > 0 {
				result.OrphanedIssues = external
				return result, fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
			}
		}
	default: // force: delete anyway, report the orphaned dependents
		orphanSet := make(map[string]bool)
		for _, id := range regularIDs {
			for _, src := range dependents[id] {
				if !idSet[src] {
					orphanSet[src] = true
				}
			}
		}
		orphans := make([]string, 0, len(orphanSet))
		for id := range orphanSet {
			orphans = append(orphans, id)
		}
		sort.Strings(orphans)
		result.OrphanedIssues = orphans
	}

	// Cascade can pull wisp dependents into the set; re-partition. A wisp
	// passed in ids AND rediscovered by the cascade BFS must not enter
	// cascadeWispIDs a second time: the SQL reference re-adds it and then
	// aborts the whole batch when the duplicate delete hits 0 rows (its own
	// bug); deduping here keeps DeletedCount equal to the number of issues
	// that actually existed.
	initialWispSet := makeStringSet(initialWispIDs)
	var cascadeWispIDs, finalRegularIDs []string
	for _, id := range expandedRegularIDs {
		switch {
		case initialWispSet[id]:
			// already queued via ids
		case isStoredWisp(id):
			cascadeWispIDs = append(cascadeWispIDs, id)
		default:
			finalRegularIDs = append(finalRegularIDs, id)
		}
	}
	allWispIDs := append(append([]string(nil), initialWispIDs...), cascadeWispIDs...)
	allDeletedSet := make(map[string]bool, len(finalRegularIDs)+len(allWispIDs))
	for _, id := range finalRegularIDs {
		allDeletedSet[id] = true
	}
	for _, id := range allWispIDs {
		allDeletedSet[id] = true
	}

	// Child-row counts, mirroring the reference exactly: labels, outbound
	// dependencies, and events of the expanded regular set (initial wisp ids
	// are NOT counted), plus inbound edges from surviving issues.
	for _, id := range expandedRegularIDs {
		issue, ok := byID[id]
		if !ok {
			continue
		}
		result.LabelsCount += len(issue.Labels)
		result.DependenciesCount += len(issue.Dependencies)
		events, err := s.readEventsFile(id)
		if err != nil {
			return nil, err
		}
		result.EventsCount += len(events)
	}
	for _, id := range expandedRegularIDs {
		for _, src := range dependents[id] {
			if !allDeletedSet[src] {
				result.DependenciesCount++
			}
		}
	}
	result.DeletedCount = len(finalRegularIDs) + len(allWispIDs)

	if dryRun {
		return result, nil
	}

	deleted := make(map[string]bool, len(allDeletedSet))
	actualRegular := 0
	for _, id := range finalRegularIDs {
		if err := s.removeIssueFile(id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue // DELETE ... IN (...) ignores missing rows
			}
			return nil, err
		}
		if err := s.removeIssueChildFiles(id); err != nil {
			return nil, err
		}
		actualRegular++
		deleted[id] = true
	}
	for _, id := range allWispIDs {
		if err := s.removeIssueFile(id); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}
		if err := s.removeIssueChildFiles(id); err != nil {
			return nil, err
		}
		deleted[id] = true
	}
	result.DeletedCount = actualRegular + len(allWispIDs)

	if err := s.removeDanglingEdges(all, deleted); err != nil {
		return nil, err
	}
	return result, nil
}

// removeIssueFile deletes an issue's JSON file, mapping a missing file to
// storage.ErrNotFound.
func (s *FlatFileStore) removeIssueFile(id string) error {
	path, err := s.safeIssueFilename(id)
	if err != nil {
		return err
	}
	if err := s.removeFile(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("flatfile: delete issue %s: %w", id, err)
	}
	return nil
}

// removeIssueChildFiles mirrors the comments/events FK ON DELETE CASCADE of
// the SQL backends: the per-issue events JSONL and comments directory go with
// the issue file. Missing child files are tolerated.
func (s *FlatFileStore) removeIssueChildFiles(id string) error {
	eventsPath, err := s.safeEventFilename(id)
	if err != nil {
		return err
	}
	if err := s.removeFile(eventsPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("flatfile: delete events for %s: %w", id, err)
	}
	commentsPath, err := safePath(s.commentsDir, id, "")
	if err != nil {
		return err
	}
	// Journal each comment file so a rolled-back transaction restores them;
	// the empty directory itself is not journaled (same documented gap as
	// UpdateIssueID's directory moves).
	entries, err := os.ReadDir(commentsPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("flatfile: read comments dir for %s: %w", id, err)
	}
	for _, e := range entries {
		if err := s.removeFile(filepath.Join(commentsPath, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("flatfile: delete comment for %s: %w", id, err)
		}
	}
	if err := os.Remove(commentsPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("flatfile: delete comments dir for %s: %w", id, err)
	}
	return nil
}

// removeDanglingEdges mirrors the dependencies FK ON DELETE CASCADE: edges
// whose target was deleted are dropped from every surviving issue's JSON.
// updated_at is deliberately untouched (the SQL cascade never bumps it).
func (s *FlatFileStore) removeDanglingEdges(all []*types.Issue, deleted map[string]bool) error {
	for _, issue := range all {
		if deleted[issue.ID] {
			continue
		}
		kept := issue.Dependencies[:0]
		changed := false
		for _, dep := range issue.Dependencies {
			if deleted[dep.DependsOnID] {
				changed = true
				continue
			}
			kept = append(kept, dep)
		}
		if changed {
			issue.Dependencies = kept
			if err := s.writeIssue(issue); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteIssuesBySourceRepo mirrors issueops.DeleteIssuesBySourceRepoInTx:
// only durable issues match (wisps are never touched), child rows cascade
// with the issue file, and dependency edges referencing the deleted issues
// are dropped from survivors (FK ON DELETE CASCADE parity).
// Runs under runAtomic like DeleteIssues: a mid-batch failure rolls every
// removal back and reports 0 deleted, matching the SQL tx behavior.
func (s *FlatFileStore) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	var count int
	err := s.runAtomic(ctx, func() error {
		var err error
		count, err = s.deleteBySourceRepoJournaled(sourceRepo)
		return err
	})
	if err != nil {
		return 0, err // everything was rolled back
	}
	return count, nil
}

// deleteBySourceRepoJournaled is DeleteIssuesBySourceRepo's body; the caller
// has armed the pre-image journal (runAtomic).
func (s *FlatFileStore) deleteBySourceRepoJournaled(sourceRepo string) (int, error) {
	defer s.lockWrite()()
	all, err := s.loadAllIssues()
	if err != nil {
		return 0, err
	}
	deleted := make(map[string]bool)
	for _, issue := range all {
		if issueops.IsWisp(issue) || issue.SourceRepo != sourceRepo {
			continue
		}
		if err := s.removeIssueFile(issue.ID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return len(deleted), err
		}
		if err := s.removeIssueChildFiles(issue.ID); err != nil {
			return len(deleted), err
		}
		deleted[issue.ID] = true
	}
	if len(deleted) == 0 {
		return 0, nil
	}
	if err := s.removeDanglingEdges(all, deleted); err != nil {
		return len(deleted), err
	}
	return len(deleted), nil
}

// UpdateIssueID renames an issue. Mirrors issueops.updateIssueIDInTx: only
// id, title, description, design, acceptance_criteria, notes, and updated_at
// are taken from the passed issue — every other stored column (status,
// priority, ephemerality, dependencies, ...) is preserved. Renaming onto an
// existing id errors, and a 'renamed' event is recorded keyed to the new id.
func (s *FlatFileStore) UpdateIssueID(_ context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()

	stored, err := s.readIssue(oldID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("issue not found: %s", oldID)
		}
		return err
	}
	// Validate newID before touching the filesystem so a traversal-shaped ID
	// surfaces the ID-validation error instead of probing sibling paths.
	newPath, err := s.safeIssueFilename(newID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("flatfile: cannot rename %s to %s: issue already exists", oldID, newID)
	}

	stored.ID = newID
	stored.Title = issue.Title
	stored.Description = issue.Description
	stored.Design = issue.Design
	stored.AcceptanceCriteria = issue.AcceptanceCriteria
	stored.Notes = issue.Notes
	stored.UpdatedAt = time.Now().UTC()
	// Outbound edges live inside this issue's JSON; keep their source in sync.
	for _, dep := range stored.Dependencies {
		if dep.IssueID == oldID {
			dep.IssueID = newID
		}
	}

	if err := s.writeIssue(stored); err != nil {
		return err
	}
	// Remove old issue file only after new file is safely written. A failed
	// removal must surface: reporting success would leave the issue duplicated
	// under both IDs in every listing.
	if err := s.removeIssueFile(oldID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("flatfile: rename %s to %s: renamed file written but old file not removed: %w", oldID, newID, err)
	}

	// Rename comments directory if it exists.
	oldCommentDir := filepath.Join(s.commentsDir, oldID)
	newCommentDir := filepath.Join(s.commentsDir, newID)
	if _, err := os.Stat(oldCommentDir); err == nil {
		_ = os.Rename(oldCommentDir, newCommentDir)
	}

	// Rename events file if it exists.
	oldEventFile := s.eventFilename(oldID)
	newEventFile := s.eventFilename(newID)
	if _, err := os.Stat(oldEventFile); err == nil {
		_ = os.Rename(oldEventFile, newEventFile)
	}

	if err := s.recordEvent(newID, types.EventType("renamed"), actor, oldID, newID); err != nil {
		return err
	}

	// Update inbound dependency references in all other issues. The rename
	// itself already succeeded, but a failed rewrite leaves edges pointing at
	// the retired ID (which GetDependencies silently drops), so the caller
	// must hear about it to retry cleanup.
	all, err := s.loadAllIssues()
	if err != nil {
		return fmt.Errorf("flatfile: rename %s to %s: issue renamed but inbound-edge rescan failed: %w", oldID, newID, err)
	}
	for _, other := range all {
		if other.ID == newID {
			continue
		}
		changed := false
		for _, dep := range other.Dependencies {
			if dep.DependsOnID == oldID {
				dep.DependsOnID = newID
				changed = true
			}
			if dep.IssueID == oldID {
				dep.IssueID = newID
				changed = true
			}
		}
		if changed {
			if err := s.writeIssue(other); err != nil {
				return fmt.Errorf("flatfile: rename %s to %s: issue renamed but rewriting inbound edges of %s failed: %w", oldID, newID, other.ID, err)
			}
		}
	}
	return nil
}

// ClaimIssue stamps a recoverable lease onto an open issue: assignee, status
// in_progress, started_at (first transition only), a future lease_expires_at, and
// heartbeat_at = now. Mirrors issueops.ClaimIssueInTx: re-claim by the same actor
// is an idempotent success, an issue held by another actor is ErrAlreadyClaimed,
// and any non-open issue (closed, or in_progress-but-unassigned) is
// ErrNotClaimable. Only status==open is claimable; a closed issue is never
// claimable regardless of a residual assignee (flatfile's CloseIssue stamps one).
func (s *FlatFileStore) ClaimIssue(ctx context.Context, id string, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(id)
	if err != nil {
		return err
	}
	if issue.Status == types.StatusInProgress {
		if issue.Assignee == actor {
			return nil // idempotent re-claim by the same actor
		}
		if issue.Assignee != "" {
			return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, issue.Assignee)
		}
		return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, issue.Status)
	}
	if issue.Status != types.StatusOpen {
		return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, issue.Status)
	}
	if issue.Assignee != "" && issue.Assignee != actor {
		return fmt.Errorf("issue already assigned to %q. Use `bd unclaim %s` to release it before re-claiming", issue.Assignee, id)
	}

	oldData, _ := json.Marshal(issue)
	now := time.Now().UTC()
	issue.Assignee = actor
	issue.Status = types.StatusInProgress
	issue.UpdatedAt = now
	if issue.StartedAt == nil {
		issue.StartedAt = &now // preserve the original start time on re-claims
	}
	expires := now.Add(issueops.LeaseTTL(ctx))
	issue.LeaseExpiresAt = &expires
	issue.HeartbeatAt = &now
	if err := s.writeIssue(issue); err != nil {
		return err
	}
	// Mirror issueops.ClaimIssueInTx's RecordFullEventInTable payloads.
	newData, _ := json.Marshal(map[string]interface{}{
		"assignee": actor,
		"status":   "in_progress",
	})
	return s.recordEvent(id, types.EventType("claimed"), actor, string(oldData), string(newData))
}

// ClaimReadyIssue claims the first ready issue matching filter. Mirrors
// issueops.ClaimReadyIssueInTx: the candidate pool is forced to open,
// unassigned, and unlimited regardless of the caller's filter, and (nil, nil)
// is returned when nothing is claimable (the documented "no ready work"
// contract; see sqlkit capabilities.go).
func (s *FlatFileStore) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	claimFilter := filter
	claimFilter.Status = types.StatusOpen
	claimFilter.Unassigned = true
	claimFilter.Assignee = nil
	claimFilter.Limit = 0

	ready, err := s.GetReadyWork(ctx, claimFilter)
	if err != nil {
		return nil, err
	}
	for _, issue := range ready {
		if err := s.ClaimIssue(ctx, issue.ID, actor); err != nil {
			// A racing/stale claim on this candidate: try the next ready issue.
			if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
				continue
			}
			// A candidate that turned open+assigned between the ready snapshot
			// and the claim would never have entered the reference's pool (its
			// Unassigned filter runs in the same transaction); skip it too.
			if cur, rerr := s.readIssue(issue.ID); rerr == nil &&
				cur.Status == types.StatusOpen && cur.Assignee != "" && cur.Assignee != actor {
				continue
			}
			return nil, err
		}
		return s.GetIssue(ctx, issue.ID)
	}
	return nil, nil
}

// UnclaimIssue releases a claim: clears the assignee and returns the issue to
// open. Mirrors issueops.UnclaimIssueInTx: a closed issue cannot be unclaimed,
// an unassigned issue is an error, and without force only the current owner
// may release the claim.
func (s *FlatFileStore) UnclaimIssue(_ context.Context, id string, actor string, force bool) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(id)
	if err != nil {
		return err
	}
	if issue.Status == types.StatusClosed {
		return fmt.Errorf("cannot unclaim closed issue %s", id)
	}
	if issue.Assignee == "" {
		return fmt.Errorf("issue %s is not assigned", id)
	}
	if !force && issue.Assignee != actor {
		return fmt.Errorf("%w: %s is held by %s (use --force to override)",
			storage.ErrNotOwner, id, issue.Assignee)
	}
	oldData, _ := json.Marshal(issue)
	issue.Assignee = ""
	issue.Status = types.StatusOpen
	issue.LeaseExpiresAt = nil
	issue.HeartbeatAt = nil
	issue.UpdatedAt = time.Now().UTC()
	if err := s.writeIssue(issue); err != nil {
		return err
	}
	// Mirror issueops.UnclaimIssueInTx's RecordFullEventInTable payloads.
	newData, _ := json.Marshal(map[string]interface{}{
		"assignee": "",
		"status":   "open",
	})
	return s.recordEvent(id, types.EventType("unclaimed"), actor, string(oldData), string(newData))
}

// HeartbeatIssue refreshes the lease on an in_progress issue held by actor,
// pushing lease_expires_at forward by the TTL so a reaper won't reclaim it.
// Mirrors issueops.HeartbeatIssueInTx: only the current owner may heartbeat, a
// heartbeat from anyone else is ErrAlreadyClaimed, and an issue no longer
// in_progress (or an ephemeral wisp, which is never leased work) is
// ErrNotClaimable.
func (s *FlatFileStore) HeartbeatIssue(ctx context.Context, id, actor string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("%w: %s", storage.ErrNotClaimable, id)
		}
		return err
	}
	if issue.Ephemeral {
		return fmt.Errorf("%w: %s is ephemeral", storage.ErrNotClaimable, id)
	}
	if issue.Status != types.StatusInProgress {
		return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, issue.Status)
	}
	if issue.Assignee != actor {
		if issue.Assignee != "" {
			return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, issue.Assignee)
		}
		return fmt.Errorf("%w: %s", storage.ErrNotClaimable, id)
	}

	now := time.Now().UTC()
	expires := now.Add(issueops.LeaseTTL(ctx))
	issue.LeaseExpiresAt = &expires
	issue.HeartbeatAt = &now
	issue.UpdatedAt = now
	return s.writeIssue(issue)
}

// ReclaimExpiredLeases reverts in_progress issues whose lease expired more than
// olderThan ago back to ready (status open, assignee/started_at/lease cleared),
// recovering work stranded by dead workers. Mirrors
// issueops.ReclaimExpiredLeasesInTx: an issue is stale when lease_expires_at is
// non-nil and strictly before cutoff = now - olderThan. Wisps are ephemeral and
// never leased, so they are never reclaimed. Returns the issues it reverted with
// the owner each lease was taken from.
func (s *FlatFileStore) ReclaimExpiredLeases(_ context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	defer s.lockWrite()()
	cutoff := time.Now().UTC().Add(-olderThan)
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	var reclaimed []types.ReclaimedLease
	for _, issue := range all {
		if issue.Ephemeral || issue.Status != types.StatusInProgress {
			continue
		}
		if issue.LeaseExpiresAt == nil || !issue.LeaseExpiresAt.Before(cutoff) {
			continue
		}
		prevOwner := issue.Assignee
		now := time.Now().UTC()
		issue.Status = types.StatusOpen
		issue.Assignee = ""
		issue.StartedAt = nil
		issue.LeaseExpiresAt = nil
		issue.HeartbeatAt = nil
		issue.UpdatedAt = now
		if err := s.writeIssue(issue); err != nil {
			return nil, fmt.Errorf("flatfile: reclaim %s: %w", issue.ID, err)
		}
		// Mirror issueops.ReclaimExpiredLeasesInTx's RecordFullEventInTable:
		// one lease_reclaimed event per reverted issue, old_value = the owner
		// the lease was taken from.
		if err := s.recordEvent(issue.ID, types.EventLeaseReclaimed, actor, prevOwner, ""); err != nil {
			return nil, fmt.Errorf("flatfile: record reclaim event for %s: %w", issue.ID, err)
		}
		reclaimed = append(reclaimed, types.ReclaimedLease{ID: issue.ID, PreviousOwner: prevOwner})
	}
	return reclaimed, nil
}

// PromoteFromEphemeral promotes a wisp to a durable issue. Only an actual
// wisp can be promoted (missing IDs and durable issues error, so a second
// promote fails), mirroring issueops.PromoteFromEphemeralInTx. Labels,
// comments, events, and dependencies live in the same unified stores for
// wisps and durable issues, so the SQL backends' aux-table migration is a
// structural no-op here.
func (s *FlatFileStore) PromoteFromEphemeral(_ context.Context, id string, _ string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(id)
	if err != nil {
		return fmt.Errorf("flatfile: wisp %s not found: %w", id, err)
	}
	if !issue.Ephemeral {
		return fmt.Errorf("flatfile: wisp %s not found: not ephemeral", id)
	}
	issue.Ephemeral = false
	issue.UpdatedAt = time.Now().UTC()
	return s.writeIssue(issue)
}

// GetNextChildID reserves and returns the next hierarchical child ID for
// parentID. Mirrors issueops.GetNextChildIDTx: it reads the persisted
// per-parent counter, self-heals to the max existing DIRECT child number
// (grandchildren excluded), advances, and persists the new value — so
// consecutive calls advance even when no issue is created in between.
// Wisps live in the same issues directory, so wisp parents take the same
// unified scan the SQL backends express as wisp-table routing.
func (s *FlatFileStore) GetNextChildID(_ context.Context, parentID string) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	defer s.txShared()()
	all, err := s.loadAllIssues()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readCounter()
	if err != nil {
		return "", err
	}

	lastChild := state.Children[parentID]
	prefix := parentID + "."
	for _, issue := range all {
		suffix, ok := strings.CutPrefix(issue.ID, prefix)
		if !ok || strings.Contains(suffix, ".") {
			continue // not a direct child
		}
		if n, err := strconv.Atoi(suffix); err == nil && n > lastChild {
			lastChild = n
		}
	}

	nextChild := lastChild + 1
	state.Children[parentID] = nextChild
	if err := s.writeCounter(state); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s.%d", parentID, nextChild), nil
}

// ── DependencyQueryStore ─────────────────────────────────────────────

func (s *FlatFileStore) GetDependencyRecords(_ context.Context, issueID string) ([]*types.Dependency, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	issue, err := s.readIssue(issueID)
	if err != nil {
		return nil, err
	}
	return issue.Dependencies, nil
}

func (s *FlatFileStore) GetDependencyRecordsForIssues(_ context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	result := make(map[string][]*types.Dependency, len(issueIDs))
	for _, id := range issueIDs {
		issue, err := s.readIssue(id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue // missing ids simply produce no rows, like SQL IN (...)
			}
			return nil, err
		}
		result[id] = issue.Dependencies
	}
	return result, nil
}

func (s *FlatFileStore) GetAllDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]*types.Dependency, len(all))
	for _, issue := range all {
		if len(issue.Dependencies) > 0 {
			result[issue.ID] = issue.Dependencies
		}
	}
	return result, nil
}

// GetDependencyCounts mirrors issueops.GetDependencyCountsInTx: both counts
// are restricted to type = 'blocks' edges only.
func (s *FlatFileStore) GetDependencyCounts(_ context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	idSet := makeStringSet(issueIDs)
	result := make(map[string]*types.DependencyCounts, len(issueIDs))
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{}
	}
	for _, issue := range all {
		for _, dep := range issue.Dependencies {
			if dep.Type != types.DepBlocks {
				continue
			}
			if idSet[issue.ID] {
				result[issue.ID].DependencyCount++
			}
			if idSet[dep.DependsOnID] {
				result[dep.DependsOnID].DependentCount++
			}
		}
	}
	return result, nil
}

func (s *FlatFileStore) GetBlockingInfoForIssues(_ context.Context, issueIDs []string) (map[string][]string, map[string][]string, map[string]string, error) {
	if err := s.checkClosed(); err != nil {
		return nil, nil, nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, nil, nil, err
	}
	blockedBy := make(map[string][]string)
	blocks := make(map[string][]string)
	parentMap := make(map[string]string)
	idSet := makeStringSet(issueIDs)

	for _, issue := range all {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild {
				if idSet[issue.ID] {
					parentMap[issue.ID] = dep.DependsOnID
				}
				continue
			}
			if idSet[issue.ID] {
				blockedBy[issue.ID] = append(blockedBy[issue.ID], dep.DependsOnID)
			}
			if idSet[dep.DependsOnID] {
				blocks[dep.DependsOnID] = append(blocks[dep.DependsOnID], issue.ID)
			}
		}
	}
	return blockedBy, blocks, parentMap, nil
}

// IsBlocked mirrors issueops.IsBlockedInTx: the verdict follows the
// is_blocked projection (blocking edges to active targets, plus inherited
// parent blocking), and the blocker descriptions enumerate direct
// blocks/waits-for/conditional-blocks edges whose target is active — bare id
// for 'blocks', "<id> (<type>)" for the other types.
func (s *FlatFileStore) IsBlocked(_ context.Context, issueID string) (bool, []string, error) {
	if err := s.checkClosed(); err != nil {
		return false, nil, err
	}
	issue, err := s.readIssue(issueID)
	if err != nil {
		return false, nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return false, nil, err
	}
	if !computeBlockedSet(all)[issueID] {
		return false, nil, nil
	}
	statusMap := make(map[string]types.Status, len(all))
	for _, a := range all {
		statusMap[a.ID] = a.Status
	}
	var blockers []string
	for _, dep := range issue.Dependencies {
		if dep.Type != types.DepBlocks && dep.Type != types.DepWaitsFor && dep.Type != types.DepConditionalBlocks {
			continue
		}
		st, ok := statusMap[dep.DependsOnID]
		if !ok || !activeStatus(st) {
			continue
		}
		if dep.Type != types.DepBlocks {
			blockers = append(blockers, dep.DependsOnID+" ("+string(dep.Type)+")")
		} else {
			blockers = append(blockers, dep.DependsOnID)
		}
	}
	return true, blockers, nil
}

// GetNewlyUnblockedByClose mirrors issueops.GetNewlyUnblockedByCloseInTx:
// candidates are active (not closed/pinned) issues with a 'blocks' edge on
// the hypothetically-closed issue; a candidate stays blocked when any OTHER
// 'blocks' edge targets an existing active issue. conditional-blocks edges
// participate on neither side.
func (s *FlatFileStore) GetNewlyUnblockedByClose(_ context.Context, closedIssueID string) ([]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	statusMap := make(map[string]types.Status, len(all))
	for _, a := range all {
		statusMap[a.ID] = a.Status
	}

	var result []*types.Issue
	for _, issue := range all {
		if !activeStatus(issue.Status) {
			continue
		}
		dependsOnClosed := false
		stillBlocked := false
		for _, dep := range issue.Dependencies {
			if dep.Type != types.DepBlocks {
				continue
			}
			if dep.DependsOnID == closedIssueID {
				dependsOnClosed = true
				continue
			}
			if st, ok := statusMap[dep.DependsOnID]; ok && activeStatus(st) {
				stillBlocked = true
			}
		}
		if dependsOnClosed && !stillBlocked {
			result = append(result, issue)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// DetectCycles mirrors issueops.DetectCyclesInTx: only 'blocks' and
// 'conditional-blocks' edges form the cycle-detection graph.
func (s *FlatFileStore) DetectCycles(_ context.Context) ([][]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(all))
	for _, issue := range all {
		issueMap[issue.ID] = issue
	}
	adj := blockingAdjacency(all)
	// Simple cycle detection via DFS with coloring.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)
	var cycles [][]*types.Issue
	var path []string

	var dfs func(node string)
	dfs = func(node string) {
		color[node] = gray
		path = append(path, node)
		for _, next := range adj[node] {
			if color[next] == gray {
				// Found a cycle — extract it.
				start := -1
				for i, p := range path {
					if p == next {
						start = i
						break
					}
				}
				if start >= 0 {
					var cycle []*types.Issue
					for _, id := range path[start:] {
						if iss, ok := issueMap[id]; ok {
							cycle = append(cycle, iss)
						}
					}
					cycles = append(cycles, cycle)
				}
			} else if color[next] == white {
				dfs(next)
			}
		}
		path = path[:len(path)-1]
		color[node] = black
	}
	for _, issue := range all {
		if color[issue.ID] == white {
			dfs(issue.ID)
		}
	}
	return cycles, nil
}

// FindWispDependentsRecursive mirrors issueops.FindWispDependentsRecursiveInTx:
// nil/empty input returns (nil, nil); otherwise it walks wisp-sourced edges
// (the wisp_dependencies table on SQL) transitively, so durable dependents
// are excluded, and the seeds themselves are not part of the result.
func (s *FlatFileStore) FindWispDependentsRecursive(_ context.Context, ids []string) (map[string]bool, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	// Reverse edges whose SOURCE is a wisp (wisp_dependencies rows).
	dependents := make(map[string][]string)
	for _, issue := range all {
		if !issueops.IsWisp(issue) {
			continue
		}
		for _, dep := range issue.Dependencies {
			dependents[dep.DependsOnID] = append(dependents[dep.DependsOnID], issue.ID)
		}
	}
	seeds := makeStringSet(ids)
	result := make(map[string]bool)
	queue := append([]string{}, ids...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, dependent := range dependents[id] {
			if result[dependent] || seeds[dependent] {
				continue
			}
			result[dependent] = true
			queue = append(queue, dependent)
		}
	}
	return result, nil
}

func (s *FlatFileStore) IterAllDependencyRecords(_ context.Context) (storage.Iter[types.Dependency], error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	var deps []*types.Dependency
	for _, issue := range all {
		deps = append(deps, issue.Dependencies...)
	}
	return storage.NewSliceIter(deps), nil
}

func (s *FlatFileStore) CountDependentsByStatus(_ context.Context, issueID string, status types.Status) (int64, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return 0, err
	}
	var count int64
	for _, issue := range all {
		if issue.Status != status {
			continue
		}
		// No type filter — parent-child dependents count too (mirrors the
		// bare per-target COUNT in the SQL backends).
		for _, dep := range issue.Dependencies {
			if dep.DependsOnID == issueID {
				count++
				break
			}
		}
	}
	return count, nil
}

// ── AnnotationStore ──────────────────────────────────────────────────

// AddComment records a `commented` audit EVENT, not a structured comment.
// Mirrors issueops.AddCommentEventInTx.
func (s *FlatFileStore) AddComment(_ context.Context, issueID, actor, comment string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(issueID)
	if err != nil {
		return err
	}
	return s.recordCommentEvent(issueID, issueops.IsWisp(issue), types.EventCommented, actor, comment)
}

func (s *FlatFileStore) ImportIssueComment(_ context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	defer s.lockWrite()()
	return s.importIssueCommentLocked(issueID, author, text, createdAt)
}

// importIssueCommentLocked is ImportIssueComment's body for callers that
// already hold writeMu (CreateIssue's inline-comment import); writeMu is not
// reentrant, so they must not go through the exported method.
func (s *FlatFileStore) importIssueCommentLocked(issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	// Mirror issueops.ImportIssueCommentInTx: reject before insert.
	if _, err := s.readIssue(issueID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("issue %s not found", issueID)
		}
		return nil, err
	}
	// Mint a unique ID: issueops.ImportIssueCommentInTx mints a fresh UUIDv7
	// per call, so two comments sharing a created_at (second-granular Dolt
	// DATETIME exports) must both survive. A bare UnixNano would collide and
	// the second writeCommentFile would silently replace the first.
	comment := &types.Comment{
		ID:        fmt.Sprintf("%d-%d", createdAt.UnixNano(), s.commentSeq.Add(1)),
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: createdAt,
	}
	if err := s.writeCommentFile(comment); err != nil {
		return nil, err
	}
	return comment, nil
}

// ImportComment writes a fully-specified comment, preserving its ID. Used by
// the flat-file migration so comment identity survives a Dolt→flatfile→Dolt
// round trip (ImportIssueComment mints a new ID, which is lossy). Not part of
// storage.DoltStorage; callers hold the concrete *FlatFileStore.
func (s *FlatFileStore) ImportComment(_ context.Context, comment *types.Comment) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	return s.importCommentLocked(comment)
}

// importCommentLocked is ImportComment's body for callers that already hold
// writeMu (the batch write phase); writeMu is not reentrant, so they must
// not go through the exported method.
func (s *FlatFileStore) importCommentLocked(comment *types.Comment) error {
	if comment.ID == "" {
		// Seq suffix keeps same-created_at minted IDs (and filenames) distinct.
		comment.ID = fmt.Sprintf("%d-%d", comment.CreatedAt.UnixNano(), s.commentSeq.Add(1))
	}
	return s.writeCommentFile(comment)
}

// writeCommentFile persists one comment as comments/{issueID}/{createdAt}-{id}.json.
// The sanitized comment ID in the filename keeps two comments with the same
// created_at (common after a Dolt export, where DATETIME is second-granular)
// from overwriting each other's file.
func (s *FlatFileStore) writeCommentFile(comment *types.Comment) error {
	dir, err := safePath(s.commentsDir, comment.IssueID, "")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(dir, 0o755)
	filename := comment.CreatedAt.Format("2006-01-02T15-04-05.000000000Z") + "-" + sanitizeFilenameComponent(comment.ID) + ".json"
	data, err := json.MarshalIndent(comment, "", "  ")
	if err != nil {
		return fmt.Errorf("flatfile: marshal comment: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, filename)
	if err := s.atomicWrite(path, data); err != nil {
		return fmt.Errorf("flatfile: write comment: %w", err)
	}
	return nil
}

// sanitizeFilenameComponent reduces an ID to filesystem-safe characters,
// capped at 64 bytes. Comment IDs are UUIDs or UnixNano strings, which pass
// through unchanged; anything else degrades to '-' rather than erroring.
func sanitizeFilenameComponent(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id) && len(out) < 64; i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func (s *FlatFileStore) GetCommentCounts(_ context.Context, issueIDs []string) (map[string]int, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	result := make(map[string]int, len(issueIDs))
	for _, id := range issueIDs {
		// A missing comments dir counts 0 (readDirSafe), but real I/O errors
		// must surface — a swallowed error would render count 0 for an issue
		// that has comments, where the SQL COUNT query fails instead.
		count, err := s.countCommentFiles(id)
		if err != nil {
			return nil, err
		}
		result[id] = count
	}
	return result, nil
}

func (s *FlatFileStore) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	result := make(map[string][]*types.Comment, len(issueIDs))
	for _, id := range issueIDs {
		comments, err := s.GetIssueComments(ctx, id)
		if err != nil {
			return nil, err
		}
		result[id] = comments
	}
	return result, nil
}

func (s *FlatFileStore) GetLabelsForIssues(_ context.Context, issueIDs []string) (map[string][]string, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(issueIDs))
	for _, id := range issueIDs {
		issue, err := s.readIssue(id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue // missing ids simply produce no rows, like SQL IN (...)
			}
			return nil, err
		}
		result[id] = issue.Labels
	}
	return result, nil
}

// ── ConfigMetadataStore ──────────────────────────────────────────────

func (s *FlatFileStore) GetMetadata(_ context.Context, key string) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	return s.getKV(s.configKVPath, metadataKeyPrefix+key)
}

func (s *FlatFileStore) SetMetadata(_ context.Context, key, value string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.txShared()()
	return s.setKV(s.configKVPath, metadataKeyPrefix+key, value)
}

// DeleteConfig removes a config key. Metadata-namespaced keys are a silent
// no-op: the SQL DeleteConfig touches only the config table, where no such
// row can exist (SetConfig rejects the reserved prefix), and deleting a
// missing config key succeeds silently there.
func (s *FlatFileStore) DeleteConfig(_ context.Context, key string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if strings.HasPrefix(key, metadataKeyPrefix) {
		return nil
	}
	defer s.txShared()()
	s.kvMu.Lock()
	defer s.kvMu.Unlock()

	kv, err := s.readKVFile(s.configKVPath)
	if err != nil {
		return err
	}
	delete(kv, key)
	return s.writeKVFile(s.configKVPath, kv)
}

// GetCustomStatuses returns configured custom status names sorted by name,
// read from the normalized store (ORDER BY name parity with SQL backends).
func (s *FlatFileStore) GetCustomStatuses(ctx context.Context) ([]string, error) {
	detailed, err := s.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(detailed))
	for i, cs := range detailed {
		names[i] = cs.Name
	}
	return names, nil
}

// GetCustomStatusesDetailed returns configured custom statuses with their
// categories, sorted by name.
func (s *FlatFileStore) GetCustomStatusesDetailed(_ context.Context) ([]types.CustomStatus, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	list, err := s.readCustomStatuses()
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// GetCustomTypes returns configured custom type names sorted by name.
func (s *FlatFileStore) GetCustomTypes(_ context.Context) ([]string, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	list, err := s.readCustomTypes()
	if err != nil {
		return nil, err
	}
	sort.Strings(list)
	return list, nil
}

// GetInfraTypes mirrors issueops.ResolveInfraTypesInTx: the 'types.infra'
// config key wins, then config.yaml's types.infra, then
// domain.DefaultInfraTypes(). A fresh map is built per call so callers cannot
// corrupt shared state. A config read error falls through to YAML/defaults,
// matching sqlkit's explicit fallback (a silent nil here would let
// 'bd wisp gc' hard-delete protected infra beads).
func (s *FlatFileStore) GetInfraTypes(ctx context.Context) map[string]bool {
	var typeList []string
	if value, err := s.GetConfig(ctx, "types.infra"); err == nil && value != "" {
		typeList = issueops.ParseCommaSeparatedList(value)
	}
	if len(typeList) == 0 {
		if yamlTypes := config.GetInfraTypesFromYAML(); len(yamlTypes) > 0 {
			typeList = yamlTypes
		}
	}
	if len(typeList) == 0 {
		typeList = domain.DefaultInfraTypes()
	}
	result := make(map[string]bool, len(typeList))
	for _, t := range typeList {
		result[t] = true
	}
	return result
}

// IsInfraTypeCtx reports whether t is a configured infrastructure type.
func (s *FlatFileStore) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}

// ── AdvancedQueryStore ───────────────────────────────────────────────

func (s *FlatFileStore) GetRepoMtime(_ context.Context, repoPath string) (int64, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	kv, err := s.readKVFile(s.repoMtimePath)
	if err != nil {
		return 0, err
	}
	v := kv[repoPath]
	if v == "" {
		return 0, nil
	}
	var n int64
	_, _ = fmt.Sscanf(v, "%d", &n)
	return n, nil
}

func (s *FlatFileStore) SetRepoMtime(_ context.Context, repoPath, _ string, mtimeNs int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.txShared()()
	return s.setKV(s.repoMtimePath, repoPath, fmt.Sprintf("%d", mtimeNs))
}

// ClearRepoMtime mirrors issueops.ClearRepoMtimeInTx's key asymmetry:
// Set/Get use the path verbatim, but Clear ~-expands and abs-normalizes it
// first, so clearing a relative path used to set is a silent no-op.
func (s *FlatFileStore) ClearRepoMtime(_ context.Context, repoPath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.txShared()()
	s.kvMu.Lock()
	defer s.kvMu.Unlock()

	kv, err := s.readKVFile(s.repoMtimePath)
	if err != nil {
		return err
	}
	delete(kv, expandAndAbsPath(repoPath))
	return s.writeKVFile(s.repoMtimePath, kv)
}

// expandAndAbsPath mirrors the unexported issueops helper of the same name:
// ~ expansion, then filepath.Abs, falling back to the input on error.
func expandAndAbsPath(p string) string {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func (s *FlatFileStore) GetMoleculeProgress(_ context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	stats := &types.MoleculeProgressStats{MoleculeID: moleculeID}
	for _, issue := range all {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild && dep.DependsOnID == moleculeID {
				stats.Total++
				switch issue.Status { //nolint:exhaustive // only closed/in-progress affect molecule progress
				case types.StatusClosed:
					// FirstClosed/LastClosed stay nil: the SQL reference never
					// populates them, and the conformance suite pins that.
					stats.Completed++
				case types.StatusInProgress:
					stats.InProgress++
					if stats.CurrentStepID == "" {
						stats.CurrentStepID = issue.ID
					}
				default:
					// Other statuses don't affect molecule progress.
				}
				break
			}
		}
	}
	// Get molecule title
	if mol, err := s.readIssue(moleculeID); err == nil {
		stats.MoleculeTitle = mol.Title
	}
	return stats, nil
}

// GetMoleculeLastActivity mirrors issueops.GetMoleculeLastActivityInTx:
// a missing molecule is an error; a childless molecule reports its own
// updated_at as molecule_updated; otherwise the max child updated_at wins
// as step_updated, upgraded to step_closed only when the max child
// closed_at is STRICTLY after it. Children are scanned in the molecule's
// own bucket (wisp vs durable), matching the SQL WispTableRouting.
func (s *FlatFileStore) GetMoleculeLastActivity(_ context.Context, moleculeID string) (*types.MoleculeLastActivity, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	mol, err := s.readIssue(moleculeID)
	if err != nil {
		return nil, fmt.Errorf("flatfile: molecule %s not found: %w", moleculeID, err)
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	var lastUpdated time.Time
	var lastUpdatedID string
	var lastClosed *time.Time
	var lastClosedID string
	for _, issue := range all {
		if issue.Ephemeral != mol.Ephemeral {
			continue // other bucket, invisible to the routed SQL scan
		}
		isChild := false
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild && dep.DependsOnID == moleculeID {
				isChild = true
				break
			}
		}
		if !isChild {
			continue
		}
		if lastUpdatedID == "" || issue.UpdatedAt.After(lastUpdated) {
			lastUpdated = issue.UpdatedAt
			lastUpdatedID = issue.ID
		}
		if issue.ClosedAt != nil && (lastClosed == nil || issue.ClosedAt.After(*lastClosed)) {
			lastClosed = issue.ClosedAt
			lastClosedID = issue.ID
		}
	}

	if lastUpdatedID == "" {
		return &types.MoleculeLastActivity{
			MoleculeID:   moleculeID,
			LastActivity: mol.UpdatedAt,
			Source:       "molecule_updated",
		}, nil
	}

	result := &types.MoleculeLastActivity{
		MoleculeID:   moleculeID,
		LastActivity: lastUpdated,
		Source:       "step_updated",
		SourceStepID: lastUpdatedID,
	}
	if lastClosed != nil && lastClosed.After(lastUpdated) {
		result.LastActivity = *lastClosed
		result.Source = "step_closed"
		result.SourceStepID = lastClosedID
	}
	return result, nil
}

// GetStaleIssues mirrors issueops.GetStaleIssuesInTx: wisps are always
// excluded (the query hits only the issues table), a non-empty filter.Status
// REPLACES the default open+in_progress set entirely, and results are
// ordered by updated_at ascending (stalest first) before the limit applies.
func (s *FlatFileStore) GetStaleIssues(_ context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -filter.Days)
	var result []*types.Issue
	for _, issue := range all {
		if issueops.IsWisp(issue) {
			continue
		}
		if filter.Status != "" {
			if string(issue.Status) != filter.Status {
				continue
			}
		} else if issue.Status != types.StatusOpen && issue.Status != types.StatusInProgress {
			continue
		}
		if issue.UpdatedAt.Before(cutoff) {
			result = append(result, issue)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.Before(result[j].UpdatedAt)
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

// ── CompactionStore ──────────────────────────────────────────────────

func (s *FlatFileStore) CheckEligibility(_ context.Context, _ string, _ int) (bool, string, error) {
	return false, "flat-file backend does not compact", nil
}

func (s *FlatFileStore) ApplyCompaction(_ context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.lockWrite()()
	issue, err := s.readIssue(issueID)
	if err != nil {
		return err
	}
	issue.CompactionLevel = tier
	now := time.Now().UTC()
	issue.CompactedAt = &now
	issue.CompactedAtCommit = &commitHash
	issue.OriginalSize = originalSize
	issue.UpdatedAt = now
	return s.writeIssue(issue)
}

func (s *FlatFileStore) GetTier1Candidates(_ context.Context) ([]*types.CompactionCandidate, error) {
	return nil, nil
}

func (s *FlatFileStore) GetTier2Candidates(_ context.Context) ([]*types.CompactionCandidate, error) {
	return nil, nil
}

// SnapshotIssue is a no-op: the flat-file backend does not compact (see
// CheckEligibility), so there is no destructive overwrite to archive against.
func (s *FlatFileStore) SnapshotIssue(_ context.Context, _ string, _ int) error {
	return nil
}

// GetCompactionSnapshot always reports no snapshot: the flat-file backend never
// archives one because it does not compact. (nil, nil) is the interface's
// documented "none exists" result.
func (s *FlatFileStore) GetCompactionSnapshot(_ context.Context, _ string) (*types.IssueSnapshot, error) {
	return nil, nil
}

// RestoreFromSnapshot always reports no snapshot to restore, for the same reason
// as GetCompactionSnapshot.
func (s *FlatFileStore) RestoreFromSnapshot(_ context.Context, _ string) (*types.IssueSnapshot, error) {
	return nil, nil
}
