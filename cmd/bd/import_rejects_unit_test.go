package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultImportRejectsPath pins the quarantine naming rule (GH#4492
// design §5): beside the source, <stem>.rejected.jsonl for .jsonl sources,
// appended suffix for anything else.
func TestDefaultImportRejectsPath(t *testing.T) {
	cases := []struct{ source, want string }{
		{"/w/.beads/issues.jsonl", "/w/.beads/issues.rejected.jsonl"},
		{"/w/backup.txt", "/w/backup.txt.rejected.jsonl"},
		{"/w/plain", "/w/plain.rejected.jsonl"},
	}
	for _, tc := range cases {
		if got := defaultImportRejectsPath(tc.source); got != tc.want {
			t.Errorf("defaultImportRejectsPath(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

// TestAppendImportRejectsRefusesUnusableDestinations pins AC-6's refusal
// rules: a directory, a symlink, and an alias of the import source itself
// are all refused before any write.
func TestAppendImportRejectsRefusesUnusableDestinations(t *testing.T) {
	dir := t.TempDir()
	source := writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines[:1])
	newLine := screenFixtureLines[1]

	t.Run("directory", func(t *testing.T) {
		dest := filepath.Join(dir, "dest-dir")
		if err := os.Mkdir(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := appendImportRejects(dest, source, []string{newLine}); err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("directory destination: err = %v, want the refusal", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dest := filepath.Join(dir, "dest-link")
		if err := os.Symlink(writeScreenJSONL(t, dir, "target.jsonl", screenFixtureLines[:1]), dest); err != nil {
			t.Fatal(err)
		}
		if err := appendImportRejects(dest, source, []string{newLine}); err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Errorf("symlink destination: err = %v, want the refusal", err)
		}
	})

	t.Run("source alias via hardlink", func(t *testing.T) {
		dest := filepath.Join(dir, "dest-hardlink")
		if err := os.Link(source, dest); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if err := appendImportRejects(dest, source, []string{newLine}); err == nil || !strings.Contains(err.Error(), "import source itself") {
			t.Errorf("source-alias destination: err = %v, want the refusal", err)
		}
	})
}

// TestAppendImportRejectsFailureRetainsOldFile pins AC-6's durability rule:
// when the replacement write fails, the earlier quarantine is left exactly
// as it was.
func TestAppendImportRejectsFailureRetainsOldFile(t *testing.T) {
	dir := t.TempDir()
	source := writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines[:1])
	dest := filepath.Join(dir, "issues.rejected.jsonl")
	if err := os.WriteFile(dest, []byte(screenFixtureLines[1]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A read-only directory makes the same-directory temp-file creation fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot drop directory write permission: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := appendImportRejects(dest, source, []string{`{"_type":"issue","id":"ce-3"}`}); err == nil {
		t.Fatal("writes into a read-only directory must fail")
	}
	saved, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read old quarantine: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("old quarantine = %q, want it unchanged after the failed save", saved)
	}
}

// TestRecoveryPlanQuarantinesAndMergesAcrossRuns pins the recovery path's
// quarantine contract (GH#4492 AC-3/AC-6): rejects land beside the source,
// and a later run MERGES into the same file — earlier quarantined records
// survive, exact-duplicate lines are not repeated.
func TestRecoveryPlanQuarantinesAndMergesAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	store := &fakeVocabularyStore{}
	ctx := context.Background()

	source := writeScreenJSONL(t, dir, "issues.jsonl", screenFixtureLines)
	plan, err := planRecoveryJSONLImport(ctx, store, source)
	if err != nil {
		t.Fatalf("recovery plan: %v", err)
	}
	if len(plan.Issues) != 1 || plan.Issues[0].ID != "ce-1" {
		t.Errorf("plan issues = %v, want only ce-1", plan.Issues)
	}
	if len(plan.Rejected) != 1 || plan.Rejected[0].ID != "ce-2" {
		t.Errorf("plan rejects = %+v, want ce-2", plan.Rejected)
	}
	quarantine := filepath.Join(dir, "issues.rejected.jsonl")
	if plan.Quarantine != quarantine {
		t.Errorf("plan quarantine = %q, want %q", plan.Quarantine, quarantine)
	}

	// A second run whose file adds ANOTHER invalid row must keep ce-2 and
	// append ce-3, and must not duplicate ce-2's exact line.
	source = writeScreenJSONL(t, dir, "issues.jsonl", append(append([]string{}, screenFixtureLines...),
		`{"_type":"issue","id":"ce-3","title":"also stale","status":"bogus","priority":2,"issue_type":"task"}`))
	plan, err = planRecoveryJSONLImport(ctx, store, source)
	if err != nil {
		t.Fatalf("second recovery plan: %v", err)
	}
	if len(plan.Rejected) != 2 {
		t.Fatalf("second plan rejects = %+v, want ce-2 and ce-3", plan.Rejected)
	}
	saved, err := os.ReadFile(quarantine)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(saved)), "\n")
	want := []string{screenFixtureLines[1], `{"_type":"issue","id":"ce-3","title":"also stale","status":"bogus","priority":2,"issue_type":"task"}`}
	if len(got) != len(want) {
		t.Fatalf("quarantine lines = %d, want %d: %q", len(got), len(want), saved)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("quarantine line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
