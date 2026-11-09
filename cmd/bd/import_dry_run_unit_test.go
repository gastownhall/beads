package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"path/filepath"
)

// TestApplyExplicitImportScreenDryRunWritesNothing pins the dry-run contract
// (GH#4492 AC-7): --dry-run classifies with the same strict/skip semantics
// but writes neither a rejects file nor anything else.
func TestApplyExplicitImportScreenDryRunWritesNothing(t *testing.T) {
	rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
	swapImportScreenFlags(t, true, true, rejectsPath)
	records := screenRecords(t, screenFixtureLines)

	kept, _, err := applyExplicitImportScreen(context.Background(), records, "issues.jsonl", okVocabulary)
	if err != nil {
		t.Fatalf("dry-run skip screen: %v", err)
	}
	if len(kept) != 1 || kept[0].ID != "ce-1" {
		t.Fatalf("kept rows = %v, want only ce-1", kept)
	}
	if _, statErr := os.Stat(rejectsPath); !os.IsNotExist(statErr) {
		t.Errorf("dry-run wrote a rejects file (%v), want no file writes", statErr)
	}
}

// TestRecoveryPlanUndecodableLineReportsPhysicalLocation pins AC-7's
// undecodable-line rule: a line that cannot decode is fatal, reported with
// its PHYSICAL line number, before any import-phase write — the quarantine
// must not exist and no plan is returned.
func TestRecoveryPlanUndecodableLineReportsPhysicalLocation(t *testing.T) {
	dir := t.TempDir()
	source := writeScreenJSONL(t, dir, "issues.jsonl", []string{
		`{"_type":"issue","id":"ce-1","title":"valid row","status":"open","priority":2,"issue_type":"task"}`,
		`{"_type":"issue","id":"ce-2","title":"also valid","status":"open","priority":2,"issue_type":"task"}`,
		`{"_type":"issue","id": not json`,
	})
	store := &fakeVocabularyStore{}

	_, err := planRecoveryJSONLImport(context.Background(), store, source)
	if err == nil {
		t.Fatal("undecodable line must fail the recovery import")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error = %v, want the physical location \"line 3\"", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "issues.rejected.jsonl")); !os.IsNotExist(statErr) {
		t.Errorf("undecodable line wrote a quarantine file (%v), want no writes", statErr)
	}
}
