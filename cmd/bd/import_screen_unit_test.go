package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"path/filepath"
)

// writeScreenJSONL writes the given raw lines to dir/name and returns the
// path. Lines are used verbatim: a quarantined record must keep its exact
// bytes, so fixtures cannot go through a marshaler.
func writeScreenJSONL(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// screenFixtureLines is the mixed plan every screen test builds on: one
// valid row, one row whose status the writer would refuse, and one memory
// record, on known physical lines.
var screenFixtureLines = []string{
	`{"_type":"issue","id":"ce-1","title":"valid row","status":"open","priority":2,"issue_type":"task"}`,
	`{"_type":"issue","id":"ce-2","title":"stale status row","status":"verify","priority":2,"issue_type":"task"}`,
	`{"_type":"memory","key":"hint","value":"beware"}`,
}

// screenRecords parses fixture lines through the recovery reader so tests
// assert on the same located records production code screens.
func screenRecords(t *testing.T, lines []string) []importLocatedRecord {
	t.Helper()
	records, _, err := parseJSONLFileLocated(writeScreenJSONL(t, t.TempDir(), "issues.jsonl", lines))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return records
}

// swapImportScreenFlags saves the screen's global knobs and restores them on
// cleanup, so a test that flips --skip-invalid/--dry-run/--rejects cannot
// leak its policy into the next test.
func swapImportScreenFlags(t *testing.T, skipInvalid, dryRun bool, rejectsPath string) {
	t.Helper()
	origSkip, origDry, origRejects := importSkipInvalid, importDryRun, importRejectsPath
	importSkipInvalid, importDryRun, importRejectsPath = skipInvalid, dryRun, rejectsPath
	t.Cleanup(func() {
		importSkipInvalid, importDryRun, importRejectsPath = origSkip, origDry, origRejects
	})
}

// okVocabulary is the vocabulary reader every fixture row passes: built-in
// statuses and types only.
func okVocabulary(context.Context) ([]string, []string, error) { return nil, nil, nil }

// TestStrictImportRefusalErrorNamesLineIDAndRemedy pins the strict refusal's
// report (GH#4492 AC-1): the source, the count, every rejected row's physical
// line, id and reason (bounded), the statement that nothing was imported by
// this import phase, and both remedies.
func TestStrictImportRefusalErrorNamesLineIDAndRemedy(t *testing.T) {
	rejects := []importRejection{
		{Line: 7, ID: "ce-2", Reason: "invalid status: verify", Raw: "raw"},
	}
	err := strictImportRefusalError("issues.jsonl", rejects)
	msg := err.Error()
	for _, want := range []string{
		"refusing to import issues.jsonl",
		"1 invalid record(s)",
		"line 7 (id ce-2): invalid status: verify",
		"nothing was imported by this import phase",
		"--skip-invalid",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message = %q, want it to contain %q", msg, want)
		}
	}
}

// TestStrictImportRefusalErrorBoundsLongLists pins the bounded listing: at
// most importRecordLineLimit per-record lines, then one remainder count.
func TestStrictImportRefusalErrorBoundsLongLists(t *testing.T) {
	rejects := make([]importRejection, importRecordLineLimit+3)
	for i := range rejects {
		rejects[i] = importRejection{Line: i + 1, ID: "ce-bulk", Reason: "invalid status: verify", Raw: "raw"}
	}
	msg := strictImportRefusalError("bulk.jsonl", rejects).Error()
	if got := strings.Count(msg, "line "); got > importRecordLineLimit {
		t.Errorf("refusal message listed %d per-record lines, want at most %d", got, importRecordLineLimit)
	}
	if !strings.Contains(msg, "... and 3 more") {
		t.Errorf("refusal message = %q, want a remainder count of 3", msg)
	}
}

// TestApplyExplicitImportScreenStrictWritesNothing pins the strict-mode
// contract (GH#4492 AC-1): with invalid rows present the screen refuses
// BEFORE the rejects file or any database write happens — even though a
// rejects path is configured, strict mode must not touch it.
func TestApplyExplicitImportScreenStrictWritesNothing(t *testing.T) {
	swapImportScreenFlags(t, false, false, filepath.Join(t.TempDir(), "rejects.jsonl"))
	records := screenRecords(t, screenFixtureLines)

	kept, report, err := applyExplicitImportScreen(context.Background(), records, "issues.jsonl", okVocabulary)
	if err == nil {
		t.Fatal("strict screen accepted a plan with an invalid record")
	}
	if !strings.Contains(err.Error(), "nothing was imported by this import phase") {
		t.Errorf("strict refusal = %v, want the nothing-imported statement", err)
	}
	if len(kept) != 0 {
		t.Errorf("strict refusal returned %d kept rows, want 0", len(kept))
	}
	if len(report.Invalid) != 1 || report.Invalid[0].ID != "ce-2" || report.Invalid[0].Line != 2 {
		t.Errorf("screen report = %+v, want the line-2 ce-2 rejection", report.Invalid)
	}
	if _, statErr := os.Stat(importRejectsPath); !os.IsNotExist(statErr) {
		t.Errorf("strict refusal wrote a rejects file (%v), want no file writes", statErr)
	}
}

// TestApplyExplicitImportScreenEmptyPlanSkipsVocabularyRead pins the
// read-once-for-nonempty-plan rule (GH#4492 design §2): an empty plan never
// touches the store.
func TestApplyExplicitImportScreenEmptyPlanSkipsVocabularyRead(t *testing.T) {
	swapImportScreenFlags(t, false, false, "")
	called := false
	kept, _, err := applyExplicitImportScreen(context.Background(), nil, "issues.jsonl", func(context.Context) ([]string, []string, error) {
		called = true
		return nil, nil, nil
	})
	if err != nil {
		t.Fatalf("empty plan screen: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("empty plan returned %d rows", len(kept))
	}
	if called {
		t.Error("vocabulary reader was called for an empty plan, want no read")
	}
}
