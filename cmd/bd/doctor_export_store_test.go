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

// TestCollectExportCoverageReadsStoreAndFile pins the store- and file-reading
// half of `bd doctor --check=export` (GH#6955): IDs are compared as sets, not
// counts; the file compared is the one auto-export writes, including a custom
// export.path; and a malformed file is an error, never a clean result.
func TestCollectExportCoverageReadsStoreAndFile(t *testing.T) {
	t.Run("equal counts still find the missing id", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1", "ex-2")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-1", "gone-1")...)

		result, err := collectExportCoverage(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != statusWarning || result.InScope != 2 || result.InFile != 2 ||
			result.Missing != 1 || !reflect.DeepEqual(result.MissingIDs, []string{"ex-2"}) {
			t.Fatalf("result = %+v, want a warning with ex-2 missing", result)
		}
		if !strings.Contains(result.Message, "bd export -o ") {
			t.Errorf("message %q does not carry the refresh action", result.Message)
		}
	})

	t.Run("custom export.path is the file compared", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")
		config.Set("export.path", "custom.jsonl")
		writeJSONLLines(t, filepath.Join(beadsDir, "issues.jsonl"), issueLines("ex-1")...)

		result, err := collectExportCoverage(context.Background(), beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if result.Path != filepath.Join(beadsDir, "custom.jsonl") || result.Missing != 1 {
			t.Fatalf("result = %+v, want custom.jsonl compared and ex-1 missing", result)
		}
	})

	t.Run("malformed file is an error, not a clean result", func(t *testing.T) {
		beadsDir := exportCheckFixture(t, true, "ex-1")
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{not json\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := collectExportCoverage(context.Background(), beadsDir); err == nil {
			t.Fatal("collectExportCoverage accepted a malformed export")
		}
	})
}
