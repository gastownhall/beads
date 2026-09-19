package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// doltWorkingRef is the literal dolt_diff() accepts as an endpoint meaning
// "the current working set", including uncommitted writes. Passing this
// instead of a state/root hash is what lets incremental auto-export see
// changes in server mode, where auto-commit is off and HEAD never moves.
const doltWorkingRef = "WORKING"

// slowExportWarnThreshold is the duration over which an auto-export is
// considered slow enough to warn the user. Any single auto-export that
// exceeds this prints a one-line stderr tip pointing at the fix levers.
const slowExportWarnThreshold = 3 * time.Second

// exportAutoStateVersion is the format version stamped into
// .beads/export-state.json. BUMP IT whenever the MEANING of a persisted
// field changes — LastDiffAnchor's semantics above all — so a newer binary's
// state file is discarded by an older one instead of being reinterpreted.
// Adding a purely additive optional field does not need a bump.
//
// Discarding is only safe because of the deletion proofs in
// proveDeletedIssueIDs: dropping the state drops the diff anchor, and before
// GH#5896 an absent anchor plus a pending delete was itself a permanent
// wedge, which made "invalidate the state file" a cure worse than the
// disease. Now an anchorless cycle just proves what it can from history and
// re-anchors on the way out.
const exportAutoStateVersion = 1

// exportAutoState tracks auto-export state to avoid redundant work.
type exportAutoState struct {
	// FormatVersion is exportAutoStateVersion at write time. Absent (0) means
	// a file written before versioning existed; those are field-for-field
	// identical to v1 and load as-is.
	FormatVersion int `json:"v,omitempty"`

	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
	Issues         int       `json:"issues"`
	Memories       int       `json:"memories"`

	// LastDiffAnchor is the real commit hash (never a working-set/state
	// hash) used as tryIncrementalExport's fromCommit on the next cycle.
	// dolt_diff() rejects non-commit values, so this must never be set from
	// storeStateHash — only from store.GetCurrentCommit.
	LastDiffAnchor string `json:"last_diff_anchor"`
	// LastDirtyIDs carries the previous cycle's raw changed.Upserted ids
	// forward so a working-set value that reverts between two cycles (diff
	// against the anchor sees no net change) still gets re-patched against
	// the live row instead of going stale. Self-healing: a cycle that finds
	// nothing new for a carried id drops it, so this never grows unbounded.
	LastDirtyIDs []string `json:"last_dirty_ids,omitempty"`
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

	// Reconcile the existing JSONL against the store ONCE, and let BOTH
	// fail-safe guards below read the same answer. A deletion this store can
	// prove is not divergence for either of them: the empty-overwrite guard
	// fires first, so without a shared verdict `bd delete <last issue>`
	// wedges there before the orphan guard ever gets to prove anything
	// (GH#5896).
	var provenDeleted []string
	if !allowEmptyOverwrite {
		rec, err := reconcileAutoExportJSONL(ctx, fullPath, state.LastDiffAnchor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: failed to compare existing JSONL against local store: %v\n", err)
			return nil
		}
		provenDeleted = rec.provenDeleted

		if skip, existingCount, err := shouldSkipEmptyAutoExport(ctx, rec); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: failed to check existing JSONL: %v\n", err)
			return nil
		} else if skip {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: current database would export 0 issues, but %s already contains %d issue(s); refusing to overwrite. Run `bd init --from-jsonl` to import the JSONL file, or move it aside and retry.\n", fullPath, existingCount)
			return nil
		}

		if len(rec.unproven) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: auto-export skipped: %s contains %d JSONL-only issue record(s) absent from the local Dolt store (%s); refusing to overwrite. Run `bd init --from-jsonl` to import the JSONL file, or move it aside and retry.\n", fullPath, len(rec.unproven), strings.Join(sampleStrings(rec.unproven, 5), ", "))
			return nil
		}
	}

	// Run the export — memories are excluded from auto-export because they
	// contain private agent context that must not reach git history (GH#3650).
	// Try an incremental re-encode first (dolt_diff over the changed-since-last-
	// export range); fall back to a full export when the store doesn't support
	// diffing, the commit range isn't diffable (e.g. state-hash values in server
	// mode where auto-commit is off), or the change set exceeds the threshold.
	//
	// The diff anchor is always a real commit (never the working-set/state
	// hash used for change detection above) — dolt_diff() rejects anything
	// else — and the diff's "to" endpoint is always the literal "WORKING",
	// which dolt_diff accepts and which captures uncommitted writes in
	// server mode (where auto-commit is off and HEAD does not advance).
	// Resolved after every early-return guard above so tests whose scenario
	// is fully handled by those guards see no extra GetCurrentCommit call.
	exportStart := time.Now()
	anchorCommit, anchorErr := store.GetCurrentCommit(ctx)
	if anchorErr != nil {
		debug.Logf("auto-export: failed to resolve diff anchor (%v); preserving previous anchor\n", anchorErr)
		anchorCommit = state.LastDiffAnchor
	}
	// provenDeleted rides along as FORCED removals: an id proved by the
	// in-process handoff or by store history is invisible to
	// dolt_diff(anchor, WORKING), so an incremental patch would faithfully
	// preserve its stale JSONL line forever even though the guard just
	// cleared the export to run. A full export needs nothing — it rewrites
	// from the store.
	issueCount, memoryCount, dirtyIDs, didIncremental, err := tryIncrementalExport(
		ctx, fullPath, state.LastDiffAnchor, doltWorkingRef, state.LastDirtyIDs, provenDeleted,
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
		dirtyIDs = nil
	}

	// Never heal silently. The user asked for a delete and is getting a line
	// removed from a git-tracked file; say so, and name what went.
	if len(provenDeleted) > 0 {
		fmt.Fprintf(os.Stderr, "auto-export: reconciled %d deleted issue(s) out of %s (deleted from store: %s)\n",
			len(provenDeleted), fullPath, strings.Join(sampleStrings(provenDeleted, 5), ", "))
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
			LastDiffAnchor: anchorCommit,
			LastDirtyIDs:   dirtyIDs,
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
		LastDiffAnchor: anchorCommit,
		LastDirtyIDs:   dirtyIDs,
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

// shouldSkipEmptyAutoExport is the "store would write 0 issues over a
// populated file" fail-safe. It reads the shared reconciliation rather than
// re-parsing the JSONL so that ids already proven deleted don't count as
// records worth protecting — deleting the last exported issue is the shape
// that used to wedge here, one guard earlier than the orphan guard everyone
// blamed (GH#5896).
func shouldSkipEmptyAutoExport(ctx context.Context, rec *autoExportReconciliation) (bool, int, error) {
	existingCount := rec.jsonlIssueCount - len(rec.provenDeleted)
	if existingCount <= 0 {
		return false, 0, nil
	}

	issues, err := store.SearchIssues(ctx, "", autoExportFilter(ctx))
	if err != nil {
		return false, 0, fmt.Errorf("failed to search issues: %w", err)
	}

	return len(issues) == 0, existingCount, nil
}

// historyProofMaxCandidates caps how many JSONL-only ids the store-history
// proof will ask about in one export. A divergence wider than this is not a
// delete pattern — it is GH#4988 territory (a torn, stale, or foreign JSONL),
// where the conservative refusal is the right answer and a 100-way history
// scan is wasted work on the way to it.
const historyProofMaxCandidates = 100

// autoExportReconciliation is the single comparison of the existing JSONL
// against the live store that both pre-export guards read.
type autoExportReconciliation struct {
	// jsonlIssueCount is every issue record in the file, in auto-export
	// scope or not — the number the empty-overwrite guard reports.
	jsonlIssueCount int
	// provenDeleted are in-scope JSONL ids absent from the store that some
	// proof source accounted for as real deletions. The export must drop
	// their lines; neither guard may refuse over them.
	provenDeleted []string
	// unproven are in-scope JSONL ids absent from the store that nothing
	// could account for. The orphan guard refuses over these, unchanged.
	unproven []string
}

// reconcileAutoExportJSONL compares the on-disk export against the store and
// splits the JSONL-only issue ids into proven deletions and unexplained
// divergence.
//
// fromCommit is the previous cycle's diff anchor, used by one of the three
// proof sources; the other two need no anchor, which is what lets a store
// wedged by an earlier binary (no anchor, or an anchor that no longer
// resolves) recover on its first run under this code.
func reconcileAutoExportJSONL(ctx context.Context, path, fromCommit string) (*autoExportReconciliation, error) {
	// GH#4988: only refuse when *in-scope* JSONL issue records are absent from
	// the store. Ephemeral wisps, templates, and infra types are outside
	// auto-export scope (buildAutoExportFilter / GH#3649). Compaction can
	// delete wisps from Dolt while an older JSONL still lists them; treating
	// those as hard orphans wedged auto-export permanently.
	existing, err := issueRecordsInJSONL(path)
	if err != nil {
		return nil, err
	}
	rec := &autoExportReconciliation{jsonlIssueCount: len(existing)}
	if len(existing) == 0 {
		return rec, nil
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

	candidates := make([]string, 0)
	for _, record := range existing {
		if record.Ephemeral || record.IsTemplate || infraTypeSet[string(record.IssueType)] {
			continue
		}
		if _, ok := localIDs[record.ID]; !ok {
			candidates = append(candidates, record.ID)
		}
	}
	if len(candidates) == 0 {
		return rec, nil
	}

	rec.provenDeleted, rec.unproven = proveDeletedIssueIDs(ctx, candidates, fromCommit)
	return rec, nil
}

// proveDeletedIssueIDs splits JSONL-only candidate ids into the ones a proof
// source accounts for as real deletions and the ones nothing can explain.
//
// Sources run cheapest-first, and each one only sees what the previous could
// not prove:
//
//  1. the in-process delete handoff — this very command deleted the id a few
//     milliseconds ago. Free, and the only source that can see a create and a
//     delete that both sit uncommitted in the working set.
//  2. dolt_diff against the previous export's anchor (GH#5806) — a DoltStore
//     fast path that also covers deletions that arrived from a peer.
//  3. the store's own committed history (GH#5896) — anchor-free, works on
//     both stores, and the only source that can heal a store some earlier
//     binary already wedged.
//
// Whatever survives all three is unexplained divergence, and the caller keeps
// refusing to overwrite: that is GH#4988's fail-safe, unchanged.
func proveDeletedIssueIDs(ctx context.Context, candidates []string, fromCommit string) (proven, unproven []string) {
	unproven = candidates

	// P1 — same-process delete handoff.
	proven, unproven = splitProvenDeletions(proven, unproven, commandDeletedIssueIDs.has)
	if len(unproven) == 0 {
		return proven, unproven
	}

	// P2 — dolt_diff against the last export's anchor.
	//
	// The anchor must still be on the CURRENT HEAD's history for that proof
	// to mean anything. dolt_diff(anchor, WORKING) reports a row as "removed"
	// whenever it is absent at WORKING — which is equally true when the
	// history moved out from under the anchor rather than when anyone deleted
	// anything: a `CALL DOLT_RESET('--hard', <earlier>)`, a branch checkout,
	// or any other data-dir rewind on a shared server. Honoring the diff
	// there would silently drop a live record from issues.jsonl, which is
	// exactly #4988's corruption class this guard exists to prevent. So the
	// deletion proof is accepted only when the anchor is reachable from HEAD
	// (CommitExists queries dolt_log, i.e. HEAD's forward history).
	//
	// A missing anchor, an off-history anchor, a diff error, or a store with
	// no DiffStore at all (embedded mode — the default) all just fall through
	// to P3 now, instead of ending the search.
	if fromCommit != "" && diffAnchorOnCurrentHistory(ctx, fromCommit) {
		if ds, ok := storage.UnwrapStore(store).(storage.DiffStore); ok {
			if changed, diffErr := ds.ChangedIssueIDs(ctx, fromCommit, doltWorkingRef); diffErr == nil {
				removed := make(map[string]struct{}, len(changed.Removed))
				for _, id := range changed.Removed {
					removed[id] = struct{}{}
				}
				proven, unproven = splitProvenDeletions(proven, unproven, func(id string) bool {
					_, ok := removed[id]
					return ok
				})
			} else {
				debug.Logf("auto-export: anchor diff %s..%s failed (%v); falling through to the history proof\n",
					fromCommit, doltWorkingRef, diffErr)
			}
		}
	}
	if len(unproven) == 0 {
		return proven, unproven
	}

	// P3 — the store's own committed history. "Was here, is gone" is a
	// deletion this store recorded; "was never here" is data this store never
	// had, which stays unproven. See storage.HistoryPresence for why that
	// asymmetry is what keeps #4988's guarantee intact.
	if len(unproven) > historyProofMaxCandidates {
		debug.Logf("auto-export: %d unproven JSONL-only ids exceeds the history-proof cap %d; refusing without asking\n",
			len(unproven), historyProofMaxCandidates)
		return proven, unproven
	}
	hp, ok := storage.UnwrapStore(store).(storage.HistoryPresence)
	if !ok {
		debug.Logf("auto-export: store does not implement HistoryPresence; JSONL-only ids stay unproven\n")
		return proven, unproven
	}
	historical, err := hp.HistoricalIssueIDs(ctx, unproven)
	if err != nil {
		debug.Logf("auto-export: history proof failed (%v); treating JSONL-only ids as unproven\n", err)
		return proven, unproven
	}
	proven, unproven = splitProvenDeletions(proven, unproven, func(id string) bool {
		_, ok := historical[id]
		return ok
	})
	return proven, unproven
}

// splitProvenDeletions moves the ids isProven accepts from unproven onto
// proven, preserving order on both sides.
func splitProvenDeletions(proven, unproven []string, isProven func(string) bool) ([]string, []string) {
	remaining := make([]string, 0, len(unproven))
	for _, id := range unproven {
		if isProven(id) {
			proven = append(proven, id)
			continue
		}
		remaining = append(remaining, id)
	}
	return proven, remaining
}

// deletedIssueIDSet is the in-process handoff from the delete commands to
// auto-export: "this process just hard-deleted these ids, they are supposed to
// be gone". Deliberately NOT persisted — a cross-process ledger would mean the
// delete path read-modify-writes export-state.json underneath a concurrent
// exporter, and a lost entry there silently re-wedges. When the handoff misses
// (throttled export, separate invocation, peer's delete), the history proof
// picks the id up as soon as the deletion is committed.
type deletedIssueIDSet struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (s *deletedIssueIDSet) add(ids ...string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = make(map[string]struct{}, len(ids))
	}
	for _, id := range ids {
		s.ids[id] = struct{}{}
	}
}

func (s *deletedIssueIDSet) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = nil
}

func (s *deletedIssueIDSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok
}

// commitHistoryChecker is the narrow slice of storage.VersionControl that
// reconcileAutoExportJSONL needs. It is declared locally and asserted
// separately from storage.DiffStore so a store implementing one but not the
// other still degrades to the conservative verdict instead of panicking.
type commitHistoryChecker interface {
	CommitExists(ctx context.Context, commitHash string) (bool, error)
}

// diffAnchorOnCurrentHistory reports whether anchor is still reachable from
// the current HEAD. It is the ancestry precondition on trusting a dolt_diff
// "removed" verdict as proof of a real `bd delete` rather than a history
// rewind — see reconcileAutoExportJSONL. Fails closed: a store that
// cannot answer, or an error while asking, yields false so the caller keeps
// refusing to overwrite.
func diffAnchorOnCurrentHistory(ctx context.Context, anchor string) bool {
	ch, ok := storage.UnwrapStore(store).(commitHistoryChecker)
	if !ok {
		return false
	}
	onHistory, err := ch.CommitExists(ctx, anchor)
	if err != nil {
		debug.Logf("auto-export: cannot verify diff anchor %s against HEAD history (%v); treating JSONL-only ids as unproven\n", anchor, err)
		return false
	}
	if !onHistory {
		debug.Logf("auto-export: diff anchor %s is not on HEAD's history (rewind/checkout?); treating JSONL-only ids as unproven\n", anchor)
	}
	return onHistory
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
// reconcileAutoExportJSONL's store-side query: this guard's failure mode
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
	if state.FormatVersion > exportAutoStateVersion {
		debug.Logf("auto-export: state file %s is format v%d, this binary understands v%d; starting from empty state\n",
			path, state.FormatVersion, exportAutoStateVersion)
		return &exportAutoState{}
	}
	return &state
}

func saveExportAutoState(beadsDir string, state *exportAutoState) {
	path := filepath.Join(beadsDir, exportAutoStateFile)
	// Stamped here rather than at the call sites so there is exactly one
	// place that decides what version a written file claims to be.
	state.FormatVersion = exportAutoStateVersion
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
// toCommit (per dolt_diff), plus any ids carried over from the previous
// cycle via carryIDs (exportAutoState.LastDirtyIDs). Returns
// didIncremental=false when any precondition fails so the caller can fall
// back to a full export.
//
// forcedRemovals are ids the caller has already proven deleted by some means
// the diff cannot see (the in-process delete handoff, or the store's committed
// history — see proveDeletedIssueIDs). They are unioned into the diff's own
// removals, because a proven-deleted id is by definition absent at BOTH diff
// endpoints in the cases that matter, so the patch would otherwise carry its
// stale line forward forever.
//
// dirtyIDs returns the RAW diff's upserted ids (never carryIDs) for the
// caller to persist as next cycle's carryIDs. Carrying only the
// freshly-diffed ids — not the ones merely carried-in — makes a stable
// no-op cycle self-heal: a carried id that produces no new diff simply
// drops out instead of accumulating forever.
func tryIncrementalExport(ctx context.Context, fullPath, fromCommit, toCommit string, carryIDs, forcedRemovals []string) (issueCount, memoryCount int, dirtyIDs []string, didIncremental bool, err error) {
	if fromCommit == "" {
		debug.Logf("auto-export: incremental skipped — no prior commit hash recorded\n")
		return 0, 0, nil, false, nil
	}
	// Existing file is a hard requirement — without it we have nothing to
	// patch, and a full export is the right answer anyway.
	if _, statErr := os.Stat(fullPath); statErr != nil {
		debug.Logf("auto-export: incremental skipped — existing file not found: %v\n", statErr)
		return 0, 0, nil, false, nil
	}
	ds, ok := storage.UnwrapStore(store).(storage.DiffStore)
	if !ok {
		debug.Logf("auto-export: incremental skipped — store does not implement DiffStore\n")
		return 0, 0, nil, false, nil
	}
	changed, diffErr := ds.ChangedIssueIDs(ctx, fromCommit, toCommit)
	if diffErr != nil {
		// Commits may be unreachable (history rewritten), in which case we
		// cannot trust the diff. Fall back silently.
		return 0, 0, nil, false, diffErr
	}
	debug.Logf("auto-export: diff %s..%s → upserted=%d removed=%d carried=%d\n",
		fromCommit, toCommit, len(changed.Upserted), len(changed.Removed), len(carryIDs))

	// carryIDs must be folded in BEFORE the total==0 fast-path: a cycle
	// whose raw diff is empty can still owe a re-patch for an id carried
	// from last cycle (e.g. a working-set value that reverted between two
	// diff anchors nets to "no change" against the anchor, but the live row
	// still needs re-fetching to correct a stale patch from last cycle).
	removed := make(map[string]bool, len(changed.Removed)+len(forcedRemovals))
	for _, id := range changed.Removed {
		removed[id] = true
	}
	for _, id := range forcedRemovals {
		removed[id] = true
	}

	upsertIDs := unionStrings(changed.Upserted, carryIDs)
	total := len(upsertIDs) + len(removed)
	if total == 0 {
		// Nothing changed and nothing carried over. Still a valid
		// "incremental" outcome: no issues to rewrite, just refresh the
		// file in place so nothing regresses.
		issueCount, memoryCount, err = rewriteExportFile(fullPath, nil, nil, nil)
		if err != nil {
			return 0, 0, nil, false, err
		}
		return issueCount, memoryCount, changed.Upserted, true, nil
	}
	if total > incrementalExportThreshold {
		debug.Logf("auto-export: %d changes exceeds threshold %d; full export\n",
			total, incrementalExportThreshold)
		return 0, 0, nil, false, nil
	}

	// Fetch fresh data for upserted IDs (including anything carried over
	// from last cycle) and apply the same template/infra/owner filters the
	// full export uses.
	var records map[string][]byte
	droppedByFilter := make(map[string]bool)
	if len(upsertIDs) > 0 {
		issues, fetchErr := store.GetIssuesByIDs(ctx, upsertIDs)
		if fetchErr != nil {
			return 0, 0, nil, false, fmt.Errorf("GetIssuesByIDs: %w", fetchErr)
		}
		infraSet := store.GetInfraTypes(ctx)
		// Owner-exclusion safety net, mirroring exportToFile's: without
		// this, a config-excluded owner's issue that changes would leak
		// into the git-committed JSONL via the incremental path even
		// though the full-export path always excludes it (be-shbed).
		ownerExcludes := buildOwnerExcludeSet(ctx, storeExportSource{}, nil)
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
			if _, excluded := ownerExcludes[iss.CreatedBy]; excluded {
				droppedByFilter[iss.ID] = true
				continue
			}
			filtered = append(filtered, iss)
		}
		records, err = encodeIssueRecords(ctx, filtered)
		if err != nil {
			return 0, 0, nil, false, err
		}
		// NOTE: upserted IDs absent from GetIssuesByIDs's result are
		// intentionally NOT dropped. We used to flag them as "removed",
		// which is destructive: any hiccup in the fetch path (partial
		// failure, concurrent close, transient routing weirdness) would
		// wipe otherwise-valid records. Real deletions land in
		// changed.Removed from the issues-table diff; rely on that.
	}

	for id := range droppedByFilter {
		removed[id] = true
	}

	issueCount, memoryCount, err = rewriteExportFile(fullPath, records, removed, upsertIDs)
	if err != nil {
		return 0, 0, nil, false, err
	}
	return issueCount, memoryCount, changed.Upserted, true, nil
}

// unionStrings returns the deduplicated union of a and b, preserving a's
// element order followed by any of b's elements not already present in a.
func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// rewriteExportFile applies a set of upserts and removals to an existing
// JSONL export and writes the result atomically. upsertOrder preserves
// append order for brand-new issue IDs; previously-present IDs keep their
// original file position even when their bodies are replaced.
func rewriteExportFile(path string, upserts map[string][]byte, removed map[string]bool, upsertOrder []string) (issueCount, memoryCount int, err error) {
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

	w, err := atomicfile.Create(path, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create export file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = w.Abort()
		}
	}()

	lines.each(func(id string, line []byte) {
		if err != nil {
			return
		}
		if _, werr := w.Write(line); werr != nil {
			err = werr
			return
		}
		if _, werr := w.Write([]byte{'\n'}); werr != nil {
			err = werr
			return
		}
		issueCount++
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to write issue line: %w", err)
	}

	// Re-emit whatever memory lines were already in the file, verbatim and
	// unchanged — auto-export never reads live config for memories (that
	// would leak private agent context into git history, GH#3650). A file
	// with no prior memory section (the auto-export-only common case) stays
	// that way; a file seeded by a manual `bd export --include-memories`
	// keeps its memories across every subsequent incremental patch.
	for _, memLine := range lines.memoryLines {
		if _, werr := w.Write(memLine); werr != nil {
			return 0, 0, fmt.Errorf("failed to write memory line: %w", werr)
		}
		if _, werr := w.Write([]byte{'\n'}); werr != nil {
			return 0, 0, fmt.Errorf("failed to write newline: %w", werr)
		}
		memoryCount++
	}

	if err := w.Close(); err != nil {
		return 0, 0, fmt.Errorf("failed to finalize export: %w", err)
	}
	return issueCount, memoryCount, nil
}

// orderedIssueLines is an insertion-ordered map of issue ID → raw JSONL
// line (without trailing newline). Removal is O(1) via the map; the order
// slice may contain IDs that have since been removed, which the iterator
// skips.
//
// memoryLines carries forward any pre-existing memory records verbatim.
// Auto-export never regenerates memories from live config (GH#3650 — that
// would leak private agent context into git history), so a file seeded by a
// manual `bd export --include-memories` must have its memory lines preserved
// byte-for-byte across every subsequent incremental patch.
type orderedIssueLines struct {
	order       []string
	lines       map[string][]byte
	memoryLines [][]byte
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
// orderedIssueLines with issue records keyed by id and any memory records
// captured verbatim into memoryLines (see the type doc — they are carried
// forward, never regenerated). Missing file returns an empty set so
// first-incremental callers behave like a fresh write.
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
		// Struct-decode _type (mirroring classifyExistingAutoExportRecord)
		// rather than substring-matching: robust to key order/spacing and
		// unaffected by nested "id"/"_type" keys inside comments etc., since
		// Go's decoder only ever populates the outer struct's fields.
		var probe struct {
			Type string `json:"_type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		// bytes.Split shares the backing buffer; copy so later appends
		// can't silently overwrite our captured line.
		cpy := make([]byte, len(raw))
		copy(cpy, raw)
		if probe.Type == "memory" {
			out.memoryLines = append(out.memoryLines, cpy)
			continue
		}
		if probe.ID == "" {
			continue
		}
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
		rec := &exportIssueRecord{
			RecordType: "issue",
			IssueWithCounts: &types.IssueWithCounts{
				Issue:           iss,
				DependencyCount: counts.DependencyCount,
				DependentCount:  counts.DependentCount,
				CommentCount:    commentCounts[iss.ID],
			},
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", iss.ID, err)
		}
		out[iss.ID] = data
	}
	return out, nil
}
