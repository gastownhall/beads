package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"path/filepath"
)

// TestMaybeAutoImportJSONL_FallbackQuarantinesInvalidRows pins the
// server-mode fallback recovery path (GH#4492 AC-3): the fallback importer
// receives the screened plan (never a re-parse), the invalid row is
// quarantined beside the source, and the attempt is stamped.
func TestMaybeAutoImportJSONL_FallbackQuarantinesInvalidRows(t *testing.T) {
	dir := t.TempDir()
	writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines)

	origImporter := fallbackImporter
	var plan *recoveredJSONLPlan
	fallbackImporter = func(_ context.Context, _ storage.DoltStorage, p *recoveredJSONLPlan) (*importLocalResult, error) {
		plan = p
		return &importLocalResult{Issues: 1}, nil
	}
	t.Cleanup(func() { fallbackImporter = origImporter })

	store := &fakeFallbackStore{statsTotalIssues: 0}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if plan == nil {
		t.Fatal("fallback importer was not invoked")
	}
	if plan.Source != filepath.Join(dir, "issues.jsonl") {
		t.Errorf("plan source = %q, want the issues.jsonl path", plan.Source)
	}
	if len(plan.Issues) != 1 || plan.Issues[0].ID != "ce-1" {
		t.Errorf("plan issues = %v, want only ce-1", plan.Issues)
	}
	if len(plan.Rejected) != 1 || plan.Rejected[0].ID != "ce-2" {
		t.Errorf("plan rejects = %+v, want ce-2", plan.Rejected)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "issues.rejected.jsonl"))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("quarantine = %q, want ce-2's exact bytes", saved)
	}
	if _, err := os.Stat(autoImportStampPath(dir)); err != nil {
		t.Errorf("attempt was not stamped: %v", err)
	}
}

// TestMaybeAutoImportJSONL_AllRejectedFileIsStampedNotWritten pins AC-3's
// all-rejected rule on the embedded fast path: a file whose every issue row
// is invalid imports nothing, still quarantines the rows, stamps the
// attempt (so the next command does not re-read it), and does NOT signal a
// database write.
func TestMaybeAutoImportJSONL_AllRejectedFileIsStampedNotWritten(t *testing.T) {
	prev := commandDidWrite.Load()
	t.Cleanup(func() { commandDidWrite.Store(prev) })

	dir := t.TempDir()
	writeScreenJSONL(t, dir, "issues.jsonl", []string{screenFixtureLines[1]})

	store := &fakeJSONLImporterStore{}
	maybeAutoImportJSONL(context.Background(), store, dir)

	if got := store.calls.Load(); got != 1 {
		t.Fatalf("ImportJSONLData invoked %d time(s), want 1", got)
	}
	if commandDidWrite.Load() {
		t.Error("an all-rejected file wrote nothing to the database; commandDidWrite must stay unset")
	}
	if _, err := os.Stat(autoImportStampPath(dir)); err != nil {
		t.Errorf("the all-rejected attempt was not stamped: %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "issues.rejected.jsonl"))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("quarantine = %q, want ce-2's exact bytes", saved)
	}
}
