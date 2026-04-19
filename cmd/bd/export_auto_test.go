//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// ---------------------------------------------------------------------------
// Unit tests — no Dolt required.
// ---------------------------------------------------------------------------

func TestOrderedIssueLines_PreservesInsertionOrderAndReplacesInPlace(t *testing.T) {
	o := newOrderedIssueLines()
	o.set("a", []byte(`{"id":"a","v":1}`))
	o.set("b", []byte(`{"id":"b","v":1}`))
	o.set("c", []byte(`{"id":"c","v":1}`))
	// Replace b in-place — must NOT move it to the end.
	o.set("b", []byte(`{"id":"b","v":2}`))
	// Remove a.
	o.remove("a")
	// Add d — appended at the end.
	o.set("d", []byte(`{"id":"d","v":1}`))

	var got []string
	o.each(func(id string, line []byte) {
		got = append(got, string(line))
	})
	want := []string{
		`{"id":"b","v":2}`,
		`{"id":"c","v":1}`,
		`{"id":"d","v":1}`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadExistingIssueLines_ParsesIssuesSkipsMemories(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "issues.jsonl")
	content := strings.Join([]string{
		`{"id":"one","title":"first"}`,
		`{"id":"two","title":"second","comments":[{"id":"c1","text":"hi"}]}`,
		`{"_type":"memory","key":"k","value":"v"}`,
		`   `,
		`not valid json`,
		`{"id":"","title":"empty id"}`,
		`{"id":"three","title":"third"}`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	lines, err := loadExistingIssueLines(path)
	if err != nil {
		t.Fatalf("loadExistingIssueLines: %v", err)
	}

	var ids []string
	lines.each(func(id string, _ []byte) { ids = append(ids, id) })
	wantIDs := []string{"one", "two", "three"}
	if len(ids) != len(wantIDs) {
		t.Fatalf("got ids %v, want %v", ids, wantIDs)
	}
	for i, id := range wantIDs {
		if ids[i] != id {
			t.Errorf("order[%d] = %q, want %q", i, ids[i], id)
		}
	}

	// Memories must be dropped so writeMemoryRecords owns them exclusively.
	lines.each(func(_ string, line []byte) {
		if strings.Contains(string(line), `"_type":"memory"`) {
			t.Errorf("memory record leaked into issue lines: %s", line)
		}
	})

	// Comment records have an "id" field too — confirm we grabbed the
	// OUTER id (which the json.Unmarshal probe does correctly because
	// Go's decoder overwrites repeated keys from left to right, and the
	// issue's "id" is first in the object).
	var two []byte
	lines.each(func(id string, line []byte) {
		if id == "two" {
			two = line
		}
	})
	if two == nil {
		t.Fatal("issue 'two' not loaded")
	}
	if !strings.Contains(string(two), `"comments":[{"id":"c1"`) {
		t.Errorf("comments not preserved verbatim: %s", two)
	}
}

func TestLoadExistingIssueLines_MissingFileReturnsEmpty(t *testing.T) {
	lines, err := loadExistingIssueLines(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	called := false
	lines.each(func(_ string, _ []byte) { called = true })
	if called {
		t.Error("empty set yielded entries")
	}
}

// ---------------------------------------------------------------------------
// Integration tests — require the shared Dolt test server.
// ---------------------------------------------------------------------------

// setupIncrementalExportTest wires a fresh store, puts the cwd in a temp
// beads dir, and registers cleanup. Returns the store, beads dir, and ctx.
func setupIncrementalExportTest(t *testing.T) (*testHarness, context.Context) {
	t.Helper()
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)
	saveAndRestoreGlobals(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(beadsDir, "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStore(t, testDBPath)
	store = s
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()
	rootCtx = ctx

	return &testHarness{store: s, beadsDir: beadsDir}, ctx
}

type testHarness struct {
	store    storage.DoltStorage
	beadsDir string
}

func (h *testHarness) mustCreate(t *testing.T, ctx context.Context, id, title string) {
	t.Helper()
	iss := &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := h.store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
}

func (h *testHarness) mustCommit(t *testing.T, ctx context.Context, msg string) string {
	t.Helper()
	if err := h.store.Commit(ctx, msg); err != nil {
		t.Fatalf("Commit(%q): %v", msg, err)
	}
	hash, err := h.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	return hash
}

func TestChangedIssueIDs_DetectsUpsertsAndRemovals(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)

	// Baseline: create three issues and commit.
	h.mustCreate(t, ctx, "cid-a", "Alpha")
	h.mustCreate(t, ctx, "cid-b", "Beta")
	h.mustCreate(t, ctx, "cid-c", "Gamma")
	c1 := h.mustCommit(t, ctx, "baseline")

	// Delta:
	//   - modify cid-a via UpdateIssue (touches issues row)
	//   - add a label to cid-b (touches labels row only)
	//   - delete cid-c (touches issues row, diff_type=removed)
	if err := h.store.UpdateIssue(ctx, "cid-a", map[string]interface{}{"title": "Alpha Prime"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if err := h.store.AddLabel(ctx, "cid-b", "priority", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if err := h.store.DeleteIssue(ctx, "cid-c"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	c2 := h.mustCommit(t, ctx, "delta")

	ds, ok := storage.UnwrapStore(h.store).(storage.DiffStore)
	if !ok {
		t.Fatal("DoltStore should implement DiffStore")
	}
	changed, err := ds.ChangedIssueIDs(ctx, c1, c2)
	if err != nil {
		t.Fatalf("ChangedIssueIDs: %v", err)
	}

	gotUpserted := idSet(changed.Upserted)
	gotRemoved := idSet(changed.Removed)

	for _, id := range []string{"cid-a", "cid-b"} {
		if !gotUpserted[id] {
			t.Errorf("%s missing from Upserted (got %v)", id, changed.Upserted)
		}
		if gotRemoved[id] {
			t.Errorf("%s wrongly in Removed", id)
		}
	}
	if !gotRemoved["cid-c"] {
		t.Errorf("cid-c missing from Removed (got %v)", changed.Removed)
	}
	if gotUpserted["cid-c"] {
		t.Error("cid-c wrongly in Upserted — a deleted issue must not be upserted even though cascade removes its label/dep rows")
	}
}

func TestTryIncrementalExport_PatchesChangedIssuesAndDropsRemoved(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)

	// Baseline: 5 issues, full export.
	h.mustCreate(t, ctx, "inc-a", "A")
	h.mustCreate(t, ctx, "inc-b", "B")
	h.mustCreate(t, ctx, "inc-c", "C")
	h.mustCreate(t, ctx, "inc-d", "D")
	h.mustCreate(t, ctx, "inc-e", "E")
	c1 := h.mustCommit(t, ctx, "baseline")

	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")
	if _, _, err := exportToFile(ctx, exportPath, true); err != nil {
		t.Fatalf("exportToFile: %v", err)
	}
	if got := countIssueLines(t, exportPath); got != 5 {
		t.Fatalf("baseline export has %d issues, want 5", got)
	}

	// Mutate: rename inc-a, delete inc-b.
	if err := h.store.UpdateIssue(ctx, "inc-a", map[string]interface{}{"title": "A-renamed"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if err := h.store.DeleteIssue(ctx, "inc-b"); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	c2 := h.mustCommit(t, ctx, "mutate")

	issueCount, memoryCount, didIncremental, err := tryIncrementalExport(ctx, exportPath, c1, c2)
	if err != nil {
		t.Fatalf("tryIncrementalExport returned error: %v", err)
	}
	if !didIncremental {
		t.Fatal("expected incremental path to succeed")
	}
	if issueCount != 4 {
		t.Errorf("issueCount = %d, want 4 (5 baseline − 1 deleted)", issueCount)
	}
	_ = memoryCount

	// Verify file state: inc-b gone; inc-a has new title; others unchanged.
	titles := loadIssueTitles(t, exportPath)
	if _, ok := titles["inc-b"]; ok {
		t.Error("inc-b should have been dropped from export")
	}
	if titles["inc-a"] != "A-renamed" {
		t.Errorf("inc-a title = %q, want %q", titles["inc-a"], "A-renamed")
	}
	for _, id := range []string{"inc-c", "inc-d", "inc-e"} {
		if _, ok := titles[id]; !ok {
			t.Errorf("untouched issue %s missing from export", id)
		}
	}
}

func TestTryIncrementalExport_DropsIssueWhenFlippedToTemplate(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)

	h.mustCreate(t, ctx, "flip-a", "Alpha")
	h.mustCreate(t, ctx, "flip-b", "Beta")
	c1 := h.mustCommit(t, ctx, "baseline")

	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")
	if _, _, err := exportToFile(ctx, exportPath, true); err != nil {
		t.Fatalf("exportToFile: %v", err)
	}
	if got := countIssueLines(t, exportPath); got != 2 {
		t.Fatalf("baseline export has %d issues, want 2", got)
	}

	// Flip flip-a to a template in place. UpdateIssue doesn't toggle
	// is_template directly, so go through raw SQL — that mirrors what
	// bd's template-promotion flow eventually writes anyway.
	doltStore, ok := h.store.(interface {
		DB() *sql.DB
	})
	if !ok {
		t.Skip("store does not expose DB() for raw SQL; can't exercise template flip")
	}
	if _, err := doltStore.DB().ExecContext(ctx, `UPDATE issues SET is_template = 1 WHERE id = ?`, "flip-a"); err != nil {
		t.Fatalf("UPDATE is_template: %v", err)
	}
	c2 := h.mustCommit(t, ctx, "promote to template")

	_, _, didIncremental, err := tryIncrementalExport(ctx, exportPath, c1, c2)
	if err != nil {
		t.Fatalf("tryIncrementalExport: %v", err)
	}
	if !didIncremental {
		t.Fatal("expected incremental path to run")
	}

	titles := loadIssueTitles(t, exportPath)
	if _, stillThere := titles["flip-a"]; stillThere {
		t.Error("flip-a should have been dropped from export once flipped to a template")
	}
	if _, ok := titles["flip-b"]; !ok {
		t.Error("flip-b (untouched) must remain in the export")
	}
}

func TestTryIncrementalExport_FallsBackWhenFileMissing(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)

	h.mustCreate(t, ctx, "fb-a", "A")
	c1 := h.mustCommit(t, ctx, "first")
	h.mustCreate(t, ctx, "fb-b", "B")
	c2 := h.mustCommit(t, ctx, "second")

	// No existing file → must return didIncremental=false and leave the
	// disk untouched so the caller falls back to the full-export path.
	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")
	issueCount, _, didIncremental, err := tryIncrementalExport(ctx, exportPath, c1, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if didIncremental {
		t.Fatal("expected fallback when file is missing")
	}
	if issueCount != 0 {
		t.Errorf("issueCount on fallback = %d, want 0", issueCount)
	}
	if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
		t.Error("fallback path must not create a file")
	}
}

func TestTryIncrementalExport_ThresholdExceededFallsBack(t *testing.T) {
	h, ctx := setupIncrementalExportTest(t)

	// Seed one issue so the file exists; baseline commit.
	h.mustCreate(t, ctx, "thr-0", "seed")
	c1 := h.mustCommit(t, ctx, "seed")

	exportPath := filepath.Join(h.beadsDir, "issues.jsonl")
	if _, _, err := exportToFile(ctx, exportPath, true); err != nil {
		t.Fatalf("exportToFile: %v", err)
	}
	sizeBefore, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}

	// Create more issues than the threshold in a single commit.
	for i := 0; i < incrementalExportThreshold+1; i++ {
		id := fmt.Sprintf("thr-%05d", i)
		h.mustCreate(t, ctx, id, fmt.Sprintf("t%05d", i))
	}
	c2 := h.mustCommit(t, ctx, "flood")

	_, _, didIncremental, err := tryIncrementalExport(ctx, exportPath, c1, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if didIncremental {
		t.Fatal("expected fallback when change count exceeds threshold")
	}

	// File should be byte-for-byte unchanged since fallback was taken.
	sizeAfter, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sizeBefore) != len(sizeAfter) {
		t.Errorf("file was touched on fallback (size %d → %d)", len(sizeBefore), len(sizeAfter))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func idSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func countIssueLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, `"_type":"memory"`) {
			continue
		}
		n++
	}
	return n
}

func loadIssueTitles(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, `"_type":"memory"`) {
			continue
		}
		var iss struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			t.Errorf("unmarshal line %q: %v", line, err)
			continue
		}
		if iss.ID != "" {
			out[iss.ID] = iss.Title
		}
	}
	return out
}
