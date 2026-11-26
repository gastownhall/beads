//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// CommitExists says the diff anchor is on the store's history. The drift path
// reaches the reconcile guard, which asks this question before judging
// JSONL-only records.
func (f *fakeMovingStateStore) CommitExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

// TestMaybeAutoExport_RewoundJSONLIsReexported pins #8585: a git stash or
// checkout can restore an older tracked issues.jsonl while the store is
// unchanged. The no-change return used to trust only the store hash, so the
// export stayed incomplete. The export must be repaired once the file on disk
// is not the one this process last wrote.
func TestMaybeAutoExport_RewoundJSONLIsReexported(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", "1ns")
	saveAndRestoreGlobals(t)
	fake := &fakeMovingStateStore{stateHash: "hash-1"}
	fake.issues = []*types.Issue{
		{ID: "rw-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "rw-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	store = fake
	dir := autoExportTestDir(t)
	exportPath := filepath.Join(dir, ".beads", "issues.jsonl")

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// The rewind: an older copy of the file, with only rw-a, as git stash restores it.
	if err := os.WriteFile(exportPath, []byte(`{"id":"rw-a","title":"A","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("pass after rewind: %v", err)
	}
	titles := loadIssueTitles(t, exportPath)
	if titles["rw-a"] != "A" || titles["rw-b"] != "B" {
		t.Fatalf("issues.jsonl after rewind = %v, want rw-a and rw-b re-exported from the store", titles)
	}
}

// TestMaybeAutoExport_RewoundJSONLKeepsJSONLOnlyRecords: the repair must not
// overwrite a record that exists only in the rewound JSONL. The existing
// reconcile guard refuses that overwrite, and the drift path goes through it.
func TestMaybeAutoExport_RewoundJSONLKeepsJSONLOnlyRecords(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", "1ns")
	saveAndRestoreGlobals(t)
	fake := &fakeMovingStateStore{stateHash: "hash-1"}
	fake.issues = []*types.Issue{
		{ID: "rw-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	store = fake
	dir := autoExportTestDir(t)
	exportPath := filepath.Join(dir, ".beads", "issues.jsonl")

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("first export: %v", err)
	}

	jsonlOnly := `{"id":"rw-only","title":"JSONL only","status":"open","priority":2,"issue_type":"task"}`
	if err := os.WriteFile(exportPath, []byte(`{"id":"rw-a","title":"A","status":"open","priority":2,"issue_type":"task"}`+"\n"+jsonlOnly+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("pass after rewind: %v", err)
	}
	titles := loadIssueTitles(t, exportPath)
	if titles["rw-only"] != "JSONL only" {
		t.Fatalf("JSONL-only record rw-only was overwritten: titles = %v", titles)
	}
}

// TestMaybeAutoExport_DriftDoesNotOverwriteNewerJSONLRecord pins the #8585
// review: a drifted export that holds a record newer than the store's copy (a
// teammate's retitle, pulled in a commit) must not be rewritten whole from the
// store. The repair is refused and the file keeps the newer record.
func TestMaybeAutoExport_DriftDoesNotOverwriteNewerJSONLRecord(t *testing.T) {
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", "1ns")
	saveAndRestoreGlobals(t)
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	fake := &fakeMovingStateStore{stateHash: "hash-1"}
	fake.issues = []*types.Issue{
		{ID: "rw-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, UpdatedAt: t0},
	}
	store = fake
	dir := autoExportTestDir(t)
	exportPath := filepath.Join(dir, ".beads", "issues.jsonl")

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// A teammate's newer copy of rw-a lands in the file: retitled, one hour later.
	newer := `{"id":"rw-a","title":"A, retitled by a teammate","status":"open","priority":2,"issue_type":"task","updated_at":"` + t0.Add(time.Hour).Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(exportPath, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := maybeAutoExport(context.Background(), false); err != nil {
		t.Fatalf("pass after drift: %v", err)
	}
	got, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "A, retitled by a teammate") {
		t.Fatalf("issues.jsonl was overwritten; the newer teammate record should be kept, got:\n%s", got)
	}
}
