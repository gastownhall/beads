//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// exportCheckFixture points the global store at a fake holding ids and
// returns the workspace's .beads directory.
func exportCheckFixture(t *testing.T, autoExport bool, ids ...string) string {
	t.Helper()
	initConfigForTest(t)
	config.Set("export.auto", autoExport)
	saveAndRestoreGlobals(t)
	fake := &fakeHeadOnlyStore{}
	for _, id := range ids {
		fake.issues = append(fake.issues, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	}
	store = fake
	return filepath.Join(autoExportTestDir(t), ".beads")
}

func issueLines(ids ...string) []map[string]any {
	recs := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		recs = append(recs, map[string]any{"id": id, "title": id, "issue_type": "task"})
	}
	return recs
}

// TestDoctorExportCheckReportsMissingIDs pins `bd doctor --check=export`'s
// comparison (GH#6955): a store id the export lacks is a warning that counts
// every missing id, names a bounded sorted sample and says how to refresh,
// while ids only the file has never count as missing.
func TestDoctorExportCheckReportsMissingIDs(t *testing.T) {
	t.Run("present file lacking store ids warns with count, samples and refresh", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, false, "ex-7", "ex-1", "ex-2", "ex-3", "ex-4", "ex-5", "ex-6", "ex-8")
		path := filepath.Join(beadsDir, "issues.jsonl")
		writeJSONLLines(t, path, issueLines("ex-1", "gone-1")...)

		report, err := collectExportCheck(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		want := exportCheckReport{
			Path: path, FilePresent: true, InScope: 8, InFile: 2, Missing: 7,
			MissingIDs: []string{"ex-2", "ex-3", "ex-4", "ex-5", "ex-6"},
		}
		if !reflect.DeepEqual(report, want) {
			t.Fatalf("report = %+v\nwant     %+v", report, want)
		}
		c := report.check()
		if c.Status != statusWarning {
			t.Fatalf("status = %q, want %q", c.Status, statusWarning)
		}
		if !strings.Contains(c.Message, "7 in-scope issues missing") {
			t.Errorf("message %q does not count the missing ids", c.Message)
		}
		if c.Detail != "Missing: ex-2, ex-3, ex-4, ex-5, ex-6 (and 2 more)" {
			t.Errorf("detail = %q", c.Detail)
		}
		if !strings.Contains(c.Fix, "bd export -o ") || !strings.Contains(c.Fix, path) {
			t.Errorf("fix %q does not name the refresh for %s", c.Fix, path)
		}
	})

	t.Run("extra file ids with every store id present are clean", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1", "ex-2")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-2", "ex-1", "gone-1", "gone-2")...)

		report, err := collectExportCheck(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if report.Missing != 0 || len(report.MissingIDs) != 0 {
			t.Fatalf("report = %+v, want no missing ids", report)
		}
		if c := report.check(); c.Status != statusOK {
			t.Fatalf("check = %+v, want ok", c)
		}
	})

	t.Run("equal counts still find the missing id", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1", "ex-2")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-1", "gone-1")...)

		report, err := collectExportCheck(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if report.Missing != 1 || !reflect.DeepEqual(report.MissingIDs, []string{"ex-2"}) {
			t.Fatalf("report = %+v, want ex-2 missing", report)
		}
	})

	t.Run("missing file with auto-export on and nonempty scope warns", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")

		report, err := collectExportCheck(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if report.FilePresent || report.Missing != 1 {
			t.Fatalf("report = %+v, want an absent file missing ex-1", report)
		}
		c := report.check()
		if c.Status != statusWarning || !strings.Contains(c.Message, "does not exist") {
			t.Fatalf("check = %+v, want a missing-file warning", c)
		}
	})

	t.Run("missing file with auto-export off is not stale", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, false, "ex-1")

		report, err := collectExportCheck(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if c := report.check(); c.Status != statusOK {
			t.Fatalf("check = %+v, want ok when nothing promised to write the file", c)
		}
	})

	t.Run("custom export.path is the file compared", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")
		config.Set("export.path", "custom.jsonl")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-1")...)

		report, err := collectExportCheck(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if report.Path != filepath.Join(beadsDir, "custom.jsonl") || report.Missing != 1 {
			t.Fatalf("report = %+v, want custom.jsonl compared and ex-1 missing", report)
		}
	})

	t.Run("malformed file is an error, not a clean report", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{not json\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := collectExportCheck(context.Background(), beadsDir); err == nil {
			t.Fatal("collectExportCheck accepted a malformed export")
		}
	})
}
