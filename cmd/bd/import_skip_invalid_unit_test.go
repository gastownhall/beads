package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyExplicitImportScreenSkipInvalidPreservesRejects pins --skip-invalid
// with --rejects (GH#4492 AC-2): valid rows pass through, the invalid row's
// RAW bytes land in the rejects file, and the report says where.
func TestApplyExplicitImportScreenSkipInvalidPreservesRejects(t *testing.T) {
	rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
	swapImportScreenFlags(t, true, false, rejectsPath)
	records := screenRecords(t, screenFixtureLines)

	kept, report, err := applyExplicitImportScreen(context.Background(), records, "issues.jsonl", okVocabulary)
	if err != nil {
		t.Fatalf("skip-invalid screen: %v", err)
	}
	if len(kept) != 1 || kept[0].ID != "ce-1" {
		t.Fatalf("kept rows = %v, want only ce-1", kept)
	}
	if len(report.Invalid) != 1 || report.Invalid[0].ID != "ce-2" {
		t.Fatalf("screen report = %+v, want the ce-2 rejection", report.Invalid)
	}
	if report.SavedTo != rejectsPath {
		t.Errorf("report.SavedTo = %q, want %q", report.SavedTo, rejectsPath)
	}
	saved, err := os.ReadFile(rejectsPath)
	if err != nil {
		t.Fatalf("read rejects file: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("rejects file = %q, want the ce-2 line's exact bytes %q", saved, screenFixtureLines[1])
	}
}
