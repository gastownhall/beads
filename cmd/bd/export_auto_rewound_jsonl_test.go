//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
