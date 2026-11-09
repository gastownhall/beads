package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"path/filepath"
)

// TestApplyExplicitImportScreenVocabularyErrorAborts pins the vocabulary
// contract (GH#4492 AC-5): a failed vocabulary read aborts the import with
// the read error — it must not be reported as invalid records, and nothing
// may be written (no kept rows, no rejects file).
func TestApplyExplicitImportScreenVocabularyErrorAborts(t *testing.T) {
	rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
	swapImportScreenFlags(t, true, false, rejectsPath)
	records := screenRecords(t, screenFixtureLines)

	readErr := errors.New("dolt is on fire")
	kept, report, err := applyExplicitImportScreen(context.Background(), records, "issues.jsonl", func(context.Context) ([]string, []string, error) {
		return nil, nil, readErr
	})
	if err == nil {
		t.Fatal("a failed vocabulary read must abort the import")
	}
	if !strings.Contains(err.Error(), "import screen") || !strings.Contains(err.Error(), "dolt is on fire") {
		t.Errorf("abort error = %v, want the read error surfaced, not an invalid-record report", err)
	}
	if len(kept) != 0 {
		t.Errorf("abort returned %d kept rows, want 0", len(kept))
	}
	if len(report.Invalid) != 0 {
		t.Errorf("abort reported %v invalid records, want none — a read failure is not a record verdict", report.Invalid)
	}
	if _, statErr := os.Stat(rejectsPath); !os.IsNotExist(statErr) {
		t.Errorf("abort wrote a rejects file (%v), want no file writes", statErr)
	}
}

// TestScreenImportRecordsHonorsCustomVocabulary pins AC-5's acceptance half:
// rows using CONFIGURED custom statuses and types pass the screen; rows using
// unconfigured values are rejected.
func TestScreenImportRecordsHonorsCustomVocabulary(t *testing.T) {
	lines := []string{
		`{"_type":"issue","id":"cu-1","title":"custom status","status":"verify","priority":2,"issue_type":"spike","custom_status":"verify"}`,
		`{"_type":"issue","id":"cu-2","title":"unconfigured status","status":"wombat","priority":2,"issue_type":"task"}`,
	}
	records := screenRecords(t, lines)

	kept, rejects := screenImportRecords(records, []string{"verify"}, []string{"spike"})
	if len(kept) != 1 || kept[0].ID != "cu-1" {
		t.Errorf("kept = %v, want cu-1 (configured custom status and type)", kept)
	}
	if len(rejects) != 1 || rejects[0].ID != "cu-2" || !strings.Contains(rejects[0].Reason, "invalid status: wombat") {
		t.Errorf("rejects = %+v, want cu-2 refused for its unconfigured status", rejects)
	}
}

// TestScreenImportRecordsRejectsOverlongLabels pins the label-length screen:
// a label the writer would refuse must be caught before any write, with the
// row preserved rather than aborting the file under --skip-invalid.
func TestScreenImportRecordsRejectsOverlongLabels(t *testing.T) {
	long := strings.Repeat("x", 5000)
	records := screenRecords(t, []string{
		`{"_type":"issue","id":"lb-1","title":"overlong label","status":"open","priority":2,"issue_type":"task","labels":["` + long + `"]}`,
	})
	kept, rejects := screenImportRecords(records, nil, nil)
	if len(kept) != 0 {
		t.Errorf("kept = %v, want the overlong-label row refused", kept)
	}
	if len(rejects) != 1 || !strings.Contains(rejects[0].Reason, "label") {
		t.Errorf("rejects = %+v, want a label-length reason", rejects)
	}
}

// fakeVocabularyStore is the classic-store stand-in for recovery-screen unit
// tests: only the screen's vocabulary capability, over a nil DoltStorage.
type fakeVocabularyStore struct {
	storage.DoltStorage // nil — panics on any non-overridden method
	vocabErr            error
}

func (f *fakeVocabularyStore) ReadImportVocabulary(context.Context) ([]string, []string, error) {
	return nil, nil, f.vocabErr
}

// TestRecoveryPlanAbortsWhenVocabularyUnreadable pins AC-5's abort half on
// the recovery path: a vocabulary read failure writes NEITHER data nor a
// quarantine, and reports the read error rather than an invalid-record
// verdict.
func TestRecoveryPlanAbortsWhenVocabularyUnreadable(t *testing.T) {
	dir := t.TempDir()
	source := writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines)
	store := &fakeVocabularyStore{vocabErr: errors.New("resolver offline")}

	_, err := planRecoveryJSONLImport(context.Background(), store, source)
	if err == nil {
		t.Fatal("recovery plan with unreadable vocabulary must abort")
	}
	if !strings.Contains(err.Error(), "recovery import screen") || !strings.Contains(err.Error(), "resolver offline") {
		t.Errorf("abort error = %v, want the vocabulary read failure surfaced", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "issues.rejected.jsonl")); !os.IsNotExist(statErr) {
		t.Errorf("abort wrote a quarantine file (%v), want no writes at all", statErr)
	}
}

// fakeNoVocabularyStore has no ReadImportVocabulary method at all: a store
// without the capability must abort rather than screen against a guessed
// empty vocabulary.
type fakeNoVocabularyStore struct {
	storage.DoltStorage // nil — panics on any non-overridden method
}

// TestRecoveryPlanAbortsWhenCapabilityMissing pins the missing-capability
// abort (GH#4492 design §2): the alternative — screening against "no custom
// vocabulary" — would misclassify every configured-vocabulary row.
func TestRecoveryPlanAbortsWhenCapabilityMissing(t *testing.T) {
	dir := t.TempDir()
	source := writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines)

	_, err := planRecoveryJSONLImport(context.Background(), &fakeNoVocabularyStore{}, source)
	if err == nil {
		t.Fatal("recovery plan on a store without the vocabulary capability must abort")
	}
	if !strings.Contains(err.Error(), "does not support reading the import vocabulary") {
		t.Errorf("abort error = %v, want the missing-capability refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "issues.rejected.jsonl")); !os.IsNotExist(statErr) {
		t.Errorf("abort wrote a quarantine file (%v), want no writes at all", statErr)
	}
}
