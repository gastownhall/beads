//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// These tests cover GH#5896: `bd delete` used to leave a JSONL-only issue id
// that no pre-export guard could account for, so auto-export refused, never
// saved state, and refused again on every later command — forever, with the
// documented recovery (`bd init --from-jsonl`) undoing the delete.
//
// They run against a fake store that models EMBEDDED mode's capability set —
// the default open path, and the one the in-window #5806 fix could not reach:
// no StateHasher, no DiffStore, but CommitExists and (new) HistoryPresence.
// That is the same deliberate split TestMaybeAutoExport_EmbeddedModeFallsBack
// CleanlyToFullExport makes: what these assert is the GUARD's wiring — which
// proof sources it consults, what it does with the answers, what reaches the
// file — while the capability's own semantics against a real Dolt engine are
// pinned by the cross-store contract in internal/storage/storagecontract.

// fakeEmbeddedStore models EmbeddedDoltStore's capability surface for
// auto-export: HEAD-only change detection, no dolt_diff, but able to answer
// "was this id ever in my history".
type fakeEmbeddedStore struct {
	storage.DoltStorage
	issues     []*types.Issue
	historical map[string]bool
	// knownCommits is what CommitExists says yes to. An anchor that isn't
	// here models a rewind, a squash, or a state file from another clone.
	knownCommits map[string]bool
	headCommit   string

	// historyAsked records every id handed to HistoricalIssueIDs, so a test
	// can assert what the guard did NOT ask about.
	historyAsked  []string
	historyCalls  int
	historyFailed bool
}

func (f *fakeEmbeddedStore) GetCurrentCommit(_ context.Context) (string, error) {
	if f.headCommit == "" {
		return "head-1", nil
	}
	return f.headCommit, nil
}

func (f *fakeEmbeddedStore) GetInfraTypes(_ context.Context) map[string]bool { return nil }

func (f *fakeEmbeddedStore) GetConfig(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeEmbeddedStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return f.issues, nil
}

func (f *fakeEmbeddedStore) GetLabelsForIssues(_ context.Context, _ []string) (map[string][]string, error) {
	return nil, nil
}

func (f *fakeEmbeddedStore) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

func (f *fakeEmbeddedStore) GetCommentsForIssues(_ context.Context, _ []string) (map[string][]*types.Comment, error) {
	return nil, nil
}

func (f *fakeEmbeddedStore) GetCommentCounts(_ context.Context, _ []string) (map[string]int, error) {
	return nil, nil
}

func (f *fakeEmbeddedStore) GetDependencyCounts(_ context.Context, _ []string) (map[string]*types.DependencyCounts, error) {
	return nil, nil
}

func (f *fakeEmbeddedStore) CommitExists(_ context.Context, hash string) (bool, error) {
	return f.knownCommits[hash], nil
}

func (f *fakeEmbeddedStore) HistoricalIssueIDs(_ context.Context, ids []string) (map[string]struct{}, error) {
	f.historyCalls++
	f.historyAsked = append(f.historyAsked, ids...)
	if f.historyFailed {
		return nil, context.DeadlineExceeded
	}
	present := make(map[string]struct{})
	for _, id := range ids {
		if f.historical[id] {
			present[id] = struct{}{}
		}
	}
	return present, nil
}

// deleteFromStore removes an id from the store's live rows the way a hard
// delete does, leaving its history intact.
func (f *fakeEmbeddedStore) deleteFromStore(id string) {
	kept := f.issues[:0]
	for _, iss := range f.issues {
		if iss.ID != id {
			kept = append(kept, iss)
		}
	}
	f.issues = kept
}

// fakeHistorylessStore is fakeEmbeddedStore without HistoryPresence: the
// degradation case (an older or third-party store), and the fixture the P1
// handoff test needs so nothing but the handoff can prove anything.
type fakeHistorylessStore struct {
	*fakeEmbeddedStore
}

func (f *fakeHistorylessStore) HistoricalIssueIDs(context.Context, []string) (map[string]struct{}, error) {
	panic("HistoryPresence must not be reached on a store that does not implement it")
}

func newWedgeHealTestStore(t *testing.T, issues ...*types.Issue) *fakeEmbeddedStore {
	t.Helper()
	historical := make(map[string]bool, len(issues))
	for _, iss := range issues {
		historical[iss.ID] = true
	}
	return &fakeEmbeddedStore{
		issues:       issues,
		historical:   historical,
		knownCommits: map[string]bool{},
		headCommit:   "head-1",
	}
}

func wedgeHealIssue(id, title string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

// setupWedgeHealTest wires config, globals and a .beads dir, and returns the
// beads dir plus the export path.
func setupWedgeHealTest(t *testing.T, store2 storage.DoltStorage) (beadsDir, exportPath string) {
	t.Helper()
	initConfigForTest(t)
	config.Set("export.auto", true)
	// The throttle is not what these tests are about, and a 1ms interval is
	// long enough to swallow a second export that lands in the same
	// millisecond as the first.
	config.Set("export.interval", "1ns")

	saveAndRestoreGlobals(t)
	dir := autoExportTestDir(t)
	store = store2
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	beadsDir = filepath.Join(dir, ".beads")
	return beadsDir, filepath.Join(beadsDir, "issues.jsonl")
}

func jsonlIDs(t *testing.T, path string) []string {
	t.Helper()
	records, err := issueRecordsInJSONL(path)
	if err != nil {
		t.Fatalf("issueRecordsInJSONL(%s): %v", path, err)
	}
	ids := make([]string, len(records))
	for i, rec := range records {
		ids[i] = rec.ID
	}
	sort.Strings(ids)
	return ids
}

// writeWedgedExportState writes the on-disk state a pre-fix binary leaves
// behind: a last-seen commit, an open throttle window, and whatever anchor
// the caller wants to model.
func writeWedgedExportState(t *testing.T, beadsDir, lastCommit, anchor string) {
	t.Helper()
	saveExportAutoState(beadsDir, &exportAutoState{
		LastDoltCommit: lastCommit,
		LastDiffAnchor: anchor,
		Timestamp:      time.Time{},
		Issues:         2,
	})
}

// TestMaybeAutoExport_DeleteHealsInsteadOfWedging_Embedded is the issue's own
// repro on the default backend: create → export → delete → export must
// SUCCEED and the export must reflect the deletion.
func TestMaybeAutoExport_DeleteHealsInsteadOfWedging_Embedded(t *testing.T) {
	fake := newWedgeHealTestStore(t,
		wedgeHealIssue("wh-a", "Alpha"),
		wedgeHealIssue("wh-b", "Beta"),
	)
	beadsDir, exportPath := setupWedgeHealTest(t, fake)

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("first maybeAutoExport: %v", err)
	}
	if got, want := jsonlIDs(t, exportPath), []string{"wh-a", "wh-b"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("setup: exported ids = %v, want %v", got, want)
	}

	// Delete wh-b the way a pre-existing store would have it — gone from the
	// live rows, still in history — and touch wh-a so the test can also tell
	// a healed export from a skipped one by its CONTENT.
	fake.deleteFromStore("wh-b")
	fake.issues[0].Title = "Alpha renamed"
	fake.headCommit = "head-2"

	stderr := captureStderr(t, func() {
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("second maybeAutoExport: %v", err)
		}
	})

	if got, want := jsonlIDs(t, exportPath), []string{"wh-a"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("after delete, exported ids = %v, want %v — the deletion was not reflected", got, want)
	}
	if titles := loadIssueTitles(t, exportPath); titles["wh-a"] != "Alpha renamed" {
		t.Errorf("wh-a title = %q, want %q — the export was skipped, not healed", titles["wh-a"], "Alpha renamed")
	}
	if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "head-2" {
		t.Errorf("LastDoltCommit = %q, want %q — state must advance or the next command wedges on the same comparison", state.LastDoltCommit, "head-2")
	}
	if !strings.Contains(stderr, "wh-b") || !strings.Contains(stderr, "reconciled") {
		t.Errorf("stderr = %q, want a heal notice naming wh-b", stderr)
	}
	if strings.Contains(stderr, "refusing to overwrite") {
		t.Errorf("stderr = %q, want no refusal", stderr)
	}
}

// TestMaybeAutoExport_DeleteLastIssueHealsEmptyGuard covers the trap the
// single-issue repro actually hits: with nothing left in the store, the
// EMPTY-OVERWRITE guard fires before the orphan guard, and refuses on "0
// issues vs 1 record" unless it too knows the record is a proven deletion.
func TestMaybeAutoExport_DeleteLastIssueHealsEmptyGuard(t *testing.T) {
	fake := newWedgeHealTestStore(t, wedgeHealIssue("wh-only", "The only one"))
	beadsDir, exportPath := setupWedgeHealTest(t, fake)

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("first maybeAutoExport: %v", err)
	}
	if got := jsonlIDs(t, exportPath); len(got) != 1 {
		t.Fatalf("setup: exported ids = %v, want exactly wh-only", got)
	}

	fake.deleteFromStore("wh-only")
	fake.headCommit = "head-2"

	stderr := captureStderr(t, func() {
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("second maybeAutoExport: %v", err)
		}
	})

	if strings.Contains(stderr, "would export 0 issues") {
		t.Fatalf("empty-overwrite guard refused a fully-proven deletion: %q", stderr)
	}
	if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
		t.Errorf("issues.jsonl still exists after the last issue was deleted (stat err = %v); the empty export removes the file", err)
	}
	if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "head-2" {
		t.Errorf("LastDoltCommit = %q, want %q", state.LastDoltCommit, "head-2")
	}
}

// TestMaybeAutoExport_PreWedgedStoreRecovers is the upgrade path: the delete
// already happened under a binary that had no proof for it, so there is no
// in-process handoff to lean on and the anchor is useless. The first export
// under this code must heal anyway, from history alone.
func TestMaybeAutoExport_PreWedgedStoreRecovers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor string
	}{
		// R4: the state file was lost, or no export ever succeeded.
		{name: "AnchorLost", anchor: ""},
		// R2 stand-in: a well-formed anchor that no longer resolves, as
		// after a history squash, a GC, or a branch checkout.
		{name: "AnchorNoLongerResolves", anchor: "abcdefghijklmnopqrstuvwxyz012345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The store never knew wh-b as a live row in this process; only
			// its history remembers it. That IS the pre-fix on-disk shape.
			fake := newWedgeHealTestStore(t, wedgeHealIssue("wh-a", "Alpha"))
			fake.historical["wh-b"] = true
			beadsDir, exportPath := setupWedgeHealTest(t, fake)

			writeJSONLLines(t, exportPath,
				map[string]any{"_type": "issue", "id": "wh-a", "title": "Alpha", "issue_type": "task"},
				map[string]any{"_type": "issue", "id": "wh-b", "title": "Beta", "issue_type": "task"},
			)
			writeWedgedExportState(t, beadsDir, "head-0", tc.anchor)

			stderr := captureStderr(t, func() {
				if err := maybeAutoExport(context.Background(), false); err != nil {
					t.Fatalf("maybeAutoExport: %v", err)
				}
			})

			if strings.Contains(stderr, "refusing to overwrite") {
				t.Fatalf("still wedged: %q", stderr)
			}
			if got, want := jsonlIDs(t, exportPath), []string{"wh-a"}; strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("exported ids = %v, want %v", got, want)
			}
			if !strings.Contains(stderr, "wh-b") {
				t.Errorf("stderr = %q, want a heal notice naming wh-b", stderr)
			}
			if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "head-1" {
				t.Errorf("LastDoltCommit = %q, want head-1 (state must be re-saved or the wedge survives the upgrade)", state.LastDoltCommit)
			}
		})
	}
}

// TestMaybeAutoExport_UnprovableJSONLIDStillRefuses is the fail-safe half.
// An id this store has no history of is indistinguishable from GH#4988's torn
// or replaced store, and must keep refusing with the unchanged message —
// including after a rewind, where the id's commits are no longer reachable
// from HEAD and so are no longer in dolt_history_issues.
func TestMaybeAutoExport_UnprovableJSONLIDStillRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor string
	}{
		{name: "NeverExisted", anchor: ""},
		{name: "RewoundAwayWithStaleAnchor", anchor: "abcdefghijklmnopqrstuvwxyz012345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newWedgeHealTestStore(t, wedgeHealIssue("wh-a", "Alpha"))
			beadsDir, exportPath := setupWedgeHealTest(t, fake)

			writeJSONLLines(t, exportPath,
				map[string]any{"_type": "issue", "id": "wh-a", "title": "Alpha", "issue_type": "task"},
				map[string]any{"_type": "issue", "id": "wh-ghost", "title": "Ghost", "issue_type": "task"},
			)
			writeWedgedExportState(t, beadsDir, "head-0", tc.anchor)

			stderr := captureStderr(t, func() {
				if err := maybeAutoExport(context.Background(), false); err != nil {
					t.Fatalf("maybeAutoExport: %v", err)
				}
			})

			if !strings.Contains(stderr, "refusing to overwrite") {
				t.Errorf("stderr = %q, want the unchanged orphan refusal", stderr)
			}
			if !strings.Contains(stderr, "wh-ghost") {
				t.Errorf("stderr = %q, want the refusal to name wh-ghost", stderr)
			}
			if got := jsonlIDs(t, exportPath); len(got) != 2 {
				t.Errorf("exported ids = %v, want both records preserved — an unprovable divergence must never lose a line", got)
			}
			if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "head-0" {
				t.Errorf("LastDoltCommit = %q, want head-0 (a refusal must not advance state)", state.LastDoltCommit)
			}
		})
	}
}

// TestMaybeAutoExport_HistoryProofFailureRefuses: a store that cannot answer
// the history question is not a store that answered "no". The guard must fall
// back to refusing, not to overwriting.
func TestMaybeAutoExport_HistoryProofFailureRefuses(t *testing.T) {
	fake := newWedgeHealTestStore(t, wedgeHealIssue("wh-a", "Alpha"))
	fake.historical["wh-b"] = true
	fake.historyFailed = true
	beadsDir, exportPath := setupWedgeHealTest(t, fake)

	writeJSONLLines(t, exportPath,
		map[string]any{"_type": "issue", "id": "wh-a", "title": "Alpha", "issue_type": "task"},
		map[string]any{"_type": "issue", "id": "wh-b", "title": "Beta", "issue_type": "task"},
	)
	writeWedgedExportState(t, beadsDir, "head-0", "")

	stderr := captureStderr(t, func() {
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("maybeAutoExport: %v", err)
		}
	})

	if !strings.Contains(stderr, "refusing to overwrite") {
		t.Errorf("stderr = %q, want a refusal when the history proof itself failed", stderr)
	}
	if got := jsonlIDs(t, exportPath); len(got) != 2 {
		t.Errorf("exported ids = %v, want both records preserved", got)
	}
}

// TestMaybeAutoExport_DeleteHandoffProvesWhatHistoryCannot covers the one
// shape no store-side proof can ever reach: an issue created AND deleted
// without either write reaching a commit. The in-process handoff from
// deleteBatch is the only witness, and the store here has no HistoryPresence
// at all, so nothing else can cover for it.
func TestMaybeAutoExport_DeleteHandoffProvesWhatHistoryCannot(t *testing.T) {
	inner := newWedgeHealTestStore(t, wedgeHealIssue("wh-a", "Alpha"))
	fake := &fakeHistorylessStore{fakeEmbeddedStore: inner}
	beadsDir, exportPath := setupWedgeHealTest(t, fake)

	writeJSONLLines(t, exportPath,
		map[string]any{"_type": "issue", "id": "wh-a", "title": "Alpha", "issue_type": "task"},
		map[string]any{"_type": "issue", "id": "wh-uncommitted", "title": "Never committed", "issue_type": "task"},
	)
	writeWedgedExportState(t, beadsDir, "head-0", "")

	commandDeletedIssueIDs.reset()
	t.Cleanup(commandDeletedIssueIDs.reset)
	commandDeletedIssueIDs.add("wh-uncommitted")

	stderr := captureStderr(t, func() {
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("maybeAutoExport: %v", err)
		}
	})

	if strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("the delete handoff did not prove the deletion: %q", stderr)
	}
	if got, want := jsonlIDs(t, exportPath), []string{"wh-a"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("exported ids = %v, want %v", got, want)
	}
}

// TestMaybeAutoExport_ScopeSkipsNeverReachAProofSource: out-of-scope JSONL
// rows (ephemeral wisps, templates, infra types) are not candidates in the
// first place — GH#4988's own fix — so they must not cost a history query
// either.
func TestMaybeAutoExport_ScopeSkipsNeverReachAProofSource(t *testing.T) {
	fake := newWedgeHealTestStore(t, wedgeHealIssue("wh-a", "Alpha"))
	_, exportPath := setupWedgeHealTest(t, fake)

	writeJSONLLines(t, exportPath,
		map[string]any{"_type": "issue", "id": "wh-a", "title": "Alpha", "issue_type": "task"},
		map[string]any{"_type": "issue", "id": "wh-wisp", "title": "Compacted wisp", "issue_type": "task", "ephemeral": true},
		map[string]any{"_type": "issue", "id": "wh-tpl", "title": "Template", "issue_type": "task", "is_template": true},
	)

	rec, err := reconcileAutoExportJSONL(context.Background(), exportPath, "")
	if err != nil {
		t.Fatalf("reconcileAutoExportJSONL: %v", err)
	}
	if len(rec.unproven) != 0 || len(rec.provenDeleted) != 0 {
		t.Errorf("unproven = %v, provenDeleted = %v, want both empty", rec.unproven, rec.provenDeleted)
	}
	if fake.historyCalls != 0 {
		t.Errorf("HistoricalIssueIDs called %d times with %v; out-of-scope rows must never reach a proof source", fake.historyCalls, fake.historyAsked)
	}
	if rec.jsonlIssueCount != 3 {
		t.Errorf("jsonlIssueCount = %d, want 3 (the empty-overwrite guard counts every record, in scope or not)", rec.jsonlIssueCount)
	}
}

// TestProveDeletedIssueIDs_HistoryProofIsCapped: past the cap, a divergence is
// not a delete pattern, and the guard refuses without paying for the scan.
func TestProveDeletedIssueIDs_HistoryProofIsCapped(t *testing.T) {
	fake := newWedgeHealTestStore(t)
	setupWedgeHealTest(t, fake)

	candidates := make([]string, historyProofMaxCandidates+1)
	for i := range candidates {
		candidates[i] = "wh-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		fake.historical[candidates[i]] = true
	}

	proven, unproven := proveDeletedIssueIDs(context.Background(), candidates, "")
	if len(proven) != 0 {
		t.Errorf("proven = %d ids, want 0 past the cap", len(proven))
	}
	if len(unproven) != len(candidates) {
		t.Errorf("unproven = %d ids, want all %d", len(unproven), len(candidates))
	}
	if fake.historyCalls != 0 {
		t.Errorf("HistoricalIssueIDs called %d times; past the cap the guard must not ask", fake.historyCalls)
	}
}

// TestExportAutoState_VersionRoundTripAndFutureDiscard is the GH#5896 R5
// acceptance: today's unversioned files load unchanged, a file from a future
// binary is discarded rather than reinterpreted, and — the part that makes
// discarding safe at all — discarding does not wedge, because the deletion
// proofs no longer need the anchor it threw away.
func TestExportAutoState_VersionRoundTripAndFutureDiscard(t *testing.T) {
	t.Run("StampsTheCurrentVersion", func(t *testing.T) {
		dir := t.TempDir()
		saveExportAutoState(dir, &exportAutoState{LastDoltCommit: "c1", LastDiffAnchor: "a1"})

		data, err := os.ReadFile(filepath.Join(dir, exportAutoStateFile))
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal state: %v", err)
		}
		if got := raw["v"]; got != float64(exportAutoStateVersion) {
			t.Errorf("state file v = %v, want %d", got, exportAutoStateVersion)
		}

		state := loadExportAutoState(dir)
		if state.LastDoltCommit != "c1" || state.LastDiffAnchor != "a1" {
			t.Errorf("round trip lost fields: %+v", state)
		}
	})

	t.Run("LoadsAnUnversionedFileAsIs", func(t *testing.T) {
		dir := t.TempDir()
		legacy := `{"last_dolt_commit":"c1","last_diff_anchor":"a1","issues":7}`
		if err := os.WriteFile(filepath.Join(dir, exportAutoStateFile), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		state := loadExportAutoState(dir)
		if state.LastDoltCommit != "c1" || state.LastDiffAnchor != "a1" || state.Issues != 7 {
			t.Errorf("pre-versioning state file did not load as-is: %+v", state)
		}
	})

	t.Run("DiscardsAFutureVersionWithoutWedging", func(t *testing.T) {
		fake := newWedgeHealTestStore(t, wedgeHealIssue("wh-a", "Alpha"))
		fake.historical["wh-b"] = true
		beadsDir, exportPath := setupWedgeHealTest(t, fake)

		writeJSONLLines(t, exportPath,
			map[string]any{"_type": "issue", "id": "wh-a", "title": "Alpha", "issue_type": "task"},
			map[string]any{"_type": "issue", "id": "wh-b", "title": "Beta", "issue_type": "task"},
		)
		future := `{"v":999,"last_dolt_commit":"head-1","last_diff_anchor":"future-anchor"}`
		if err := os.WriteFile(filepath.Join(beadsDir, exportAutoStateFile), []byte(future), 0o600); err != nil {
			t.Fatal(err)
		}
		if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "" || state.LastDiffAnchor != "" {
			t.Fatalf("future-version state was not discarded: %+v", state)
		}

		// Discarding drops the anchor AND the last-seen commit, so this run
		// looks like a cold start with a stale JSONL — pre-#5896 the exact
		// R4 permanent wedge. It must export and re-anchor instead.
		stderr := captureStderr(t, func() {
			if err := maybeAutoExport(context.Background(), false); err != nil {
				t.Fatalf("maybeAutoExport: %v", err)
			}
		})
		if strings.Contains(stderr, "refusing to overwrite") {
			t.Fatalf("discarding a future-version state file wedged the export: %q", stderr)
		}
		if got, want := jsonlIDs(t, exportPath), []string{"wh-a"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("exported ids = %v, want %v", got, want)
		}
		state := loadExportAutoState(beadsDir)
		if state.FormatVersion != exportAutoStateVersion || state.LastDoltCommit != "head-1" {
			t.Errorf("state after re-anchor = %+v, want v%d at head-1", state, exportAutoStateVersion)
		}
	})
}

// The tests below run against a REAL DoltStore over the test sql-server —
// server mode, where the store does implement DiffStore and the incremental
// path is live. What the fake-store tests cannot reach is exactly what these
// are for: dolt_history_issues answering for itself, and proven-deleted ids
// having to survive a dolt_diff-driven incremental PATCH rather than a full
// rewrite from the store.

// TestMaybeAutoExport_DeleteHealsInsteadOfWedging_ServerMode is the issue's
// repro on the server-backed store, with the anchor deliberately blanked so
// the #5806 anchor-diff proof cannot fire and the history proof has to carry
// it — the R2/R4 residue this fix is for.
func TestMaybeAutoExport_DeleteHealsInsteadOfWedging_ServerMode(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", "1ns")

	h.mustCreate(t, ctx, "sw-a", "Alpha")
	h.mustCreate(t, ctx, "sw-b", "Beta")
	h.mustCommit(t, ctx, "baseline")
	if err := maybeAutoExport(ctx, false); err != nil {
		t.Fatalf("first maybeAutoExport: %v", err)
	}
	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")
	if got := countIssueLines(t, exportPath); got != 2 {
		t.Fatalf("setup: %d issue lines, want 2", got)
	}

	// Delete through the store directly — no in-process handoff — and blank
	// the anchor, which is what a squashed history or a lost state file
	// leaves behind. Only dolt_history_issues can answer now.
	if err := h.store.DeleteIssue(ctx, "sw-b"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	h.mustCommit(t, ctx, "delete sw-b")
	state := loadExportAutoState(h.beadsDir)
	state.LastDiffAnchor = ""
	state.Timestamp = time.Time{}
	saveExportAutoState(h.beadsDir, state)

	stderr := captureStderr(t, func() {
		if err := maybeAutoExport(ctx, false); err != nil {
			t.Fatalf("second maybeAutoExport: %v", err)
		}
	})

	if strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("still wedged on the server-backed store: %q", stderr)
	}
	if got, want := jsonlIDs(t, exportPath), []string{"sw-a"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("exported ids = %v, want %v", got, want)
	}
	if !strings.Contains(stderr, "sw-b") {
		t.Errorf("stderr = %q, want a heal notice naming sw-b", stderr)
	}
	if loadExportAutoState(h.beadsDir).LastDiffAnchor == "" {
		t.Error("the export re-anchored to nothing; the next cycle would take the same anchorless path forever")
	}
}

// TestMaybeAutoExport_ProvenDeletionReachesTheIncrementalPatch is plumbing
// consequence #1, and the one that fails SILENTLY if missed: the guard can
// clear the export while the incremental patch still carries the deleted
// record forward, because a row absent at BOTH diff endpoints produces no
// diff row at all.
//
// The setup makes that shape exactly: sx-x is created and deleted entirely
// AFTER the anchor, so dolt_diff(anchor, WORKING) never mentions it, while
// sx-y is modified so the run genuinely stays on the incremental path.
func TestMaybeAutoExport_ProvenDeletionReachesTheIncrementalPatch(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", "1ns")

	spy := &spyDiffStore{DoltStorage: h.store}
	store = spy

	h.mustCreate(t, ctx, "sx-y", "Yankee")
	h.mustCommit(t, ctx, "baseline")
	if err := maybeAutoExport(ctx, false); err != nil {
		t.Fatalf("first maybeAutoExport: %v", err)
	}
	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")
	anchor := loadExportAutoState(h.beadsDir).LastDiffAnchor
	if anchor == "" {
		t.Fatal("setup: no diff anchor recorded, the incremental path can't be under test")
	}

	// sx-x exists only between two commits that both come after the anchor,
	// and its JSONL line is written by an export that ran while it was live.
	h.mustCreate(t, ctx, "sx-x", "X-ray")
	h.mustCommit(t, ctx, "add sx-x")
	if err := maybeAutoExport(ctx, false); err != nil {
		t.Fatalf("second maybeAutoExport: %v", err)
	}
	if _, ok := loadIssueTitles(t, exportPath)["sx-x"]; !ok {
		t.Fatalf("setup: sx-x must be in the export before it is deleted")
	}
	// Pin the anchor back to before sx-x ever existed. Now the diff's two
	// endpoints both lack it, exactly like a create+delete inside one
	// throttle window.
	state := loadExportAutoState(h.beadsDir)
	state.LastDiffAnchor = anchor
	state.Timestamp = time.Time{}
	saveExportAutoState(h.beadsDir, state)

	if err := h.store.DeleteIssue(ctx, "sx-x"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	h.mustCommit(t, ctx, "delete sx-x")
	if err := h.store.UpdateIssue(ctx, "sx-y", map[string]interface{}{"title": "Yankee renamed"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	if err := maybeAutoExport(ctx, false); err != nil {
		t.Fatalf("third maybeAutoExport: %v", err)
	}

	// LastDirtyIDs reaches state only from a successful incremental patch —
	// the full-export fallback nils it — and sx-y is the only upserted id, so
	// exactly {sx-y} is what says "this run was incremental AND it ran".
	if got, want := loadExportAutoState(h.beadsDir).LastDirtyIDs, []string{"sx-y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LastDirtyIDs = %v, want %v — the run was skipped or fell back to a full export, and this test only means something on the incremental path", got, want)
	}
	titles := loadIssueTitles(t, exportPath)
	if _, stillThere := titles["sx-x"]; stillThere {
		t.Error("sx-x survived the incremental patch: a deletion proven outside dolt_diff never reached the file as a forced removal")
	}
	if titles["sx-y"] != "Yankee renamed" {
		t.Errorf("sx-y title = %q, want %q", titles["sx-y"], "Yankee renamed")
	}

	// Content-equivalence control: an incrementally-patched file must describe
	// the same issue set, field for field, as a fresh full export of the same
	// state — including in what it OMITS.
	controlPath := filepath.Join(t.TempDir(), "control.jsonl")
	if _, _, err := exportToFile(ctx, controlPath, false); err != nil {
		t.Fatalf("control exportToFile: %v", err)
	}
	got := jsonlRecordsByID(t, exportPath)
	want := jsonlRecordsByID(t, controlPath)
	if len(got) != len(want) {
		t.Fatalf("incremental export has %d records, control full export has %d", len(got), len(want))
	}
	for id, wantRec := range want {
		gotRec, ok := got[id]
		if !ok {
			t.Errorf("record %s present in control export, missing from incremental export", id)
			continue
		}
		if !reflect.DeepEqual(gotRec, wantRec) {
			t.Errorf("record %s differs between incremental and full export:\n  incremental: %v\n  full:        %v", id, gotRec, wantRec)
		}
	}
}

// TestMaybeAutoExport_HistoryProofDoesNotSurviveARewind is the safety sibling
// of TestMaybeAutoExport_HistoryRewindDoesNotProveDeletion, aimed at the new
// proof source rather than the anchor one. After a hard reset past an id's
// creation, dolt_history_issues no longer reaches those commits, so the
// history proof must come up empty and the guard must keep refusing — with
// the line still in the file.
func TestMaybeAutoExport_HistoryProofDoesNotSurviveARewind(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", "1ns")

	h.mustCreate(t, ctx, "sr-a", "Alpha")
	c1 := h.mustCommit(t, ctx, "baseline")
	if err := maybeAutoExport(ctx, false); err != nil {
		t.Fatalf("first maybeAutoExport: %v", err)
	}
	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")

	h.mustCreate(t, ctx, "sr-x", "Rewound")
	h.mustCommit(t, ctx, "add sr-x")
	if err := maybeAutoExport(ctx, false); err != nil {
		t.Fatalf("second maybeAutoExport: %v", err)
	}
	if titles := loadIssueTitles(t, exportPath); titles["sr-x"] != "Rewound" {
		t.Fatalf("setup: sr-x must be exported before the rewind, got %v", titles)
	}

	raw, ok := storage.UnwrapStore(h.store).(storage.RawDBAccessor)
	if !ok {
		t.Skip("store does not expose raw DB access")
	}
	if _, err := raw.DB().ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", c1); err != nil {
		t.Fatalf("CALL DOLT_RESET('--hard', %s): %v", c1, err)
	}
	// Blank the anchor so the anchor-diff proof cannot even be consulted:
	// whatever happens next is the history proof's answer alone.
	state := loadExportAutoState(h.beadsDir)
	state.LastDiffAnchor = ""
	state.Timestamp = time.Time{}
	saveExportAutoState(h.beadsDir, state)

	stderr := captureStderr(t, func() {
		if err := maybeAutoExport(ctx, false); err != nil {
			t.Fatalf("third maybeAutoExport: %v", err)
		}
	})

	if !strings.Contains(stderr, "refusing to overwrite") {
		t.Errorf("stderr = %q, want the unchanged refusal: a rewound-away id is data this store LOST, not a deletion it recorded", stderr)
	}
	if _, stillThere := loadIssueTitles(t, exportPath)["sr-x"]; !stillThere {
		t.Error("sr-x was silently dropped from issues.jsonl after a history rewind: the history proof accepted a rewound-away row as a proven deletion")
	}
}
