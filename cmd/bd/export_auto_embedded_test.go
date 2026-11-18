//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jsonlHasIssueID reports whether the JSONL export at path lists id, read
// through the same parser the auto-export guards use.
func jsonlHasIssueID(t *testing.T, path, id string) bool {
	t.Helper()
	records, err := issueRecordsInJSONL(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, r := range records {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestEmbeddedAutoExportFirstCreateAfterOptIn walks the opt-in recipe the
// configuration docs give (`export.auto` then `export.git-add`) on a fresh
// embedded workspace, then creates one issue inside the default 60s
// export.interval and reads issues.jsonl WITHOUT any manual export. Before
// GH#6955's fix the opt-in's empty auto-export started the throttle window,
// so the first create was skipped and the file did not exist.
func TestEmbeddedAutoExportFirstCreateAfterOptIn(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt auto-export tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "optin")
	bdCommand(t, bd, dir, "config", "set", "export.auto", "true")
	bdCommand(t, bd, dir, "config", "set", "export.git-add", "true")

	id := strings.TrimSpace(bdCreateSilent(t, bd, dir, "first issue after opt-in"))
	if id == "" {
		t.Fatal("bd create --silent printed no id")
	}

	exportPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("issues.jsonl after the first create: %v (the opt-in's empty auto-export throttled the first real export)", err)
	}
	if !jsonlHasIssueID(t, exportPath, id) {
		data, _ := os.ReadFile(exportPath)
		t.Fatalf("issues.jsonl does not list %s after the first create:\n%s", id, data)
	}
}

// TestEmbeddedAutoExportUncommittedWriteReachesJSONL pins GH#6955's second
// skip: under batch or off auto-commit an embedded write stays in the working
// set and HEAD does not move. With HEAD as the change token, auto-export saw
// "no change" and never wrote the new issue, however long the throttle had
// been open. The saved timestamp is aged past export.interval first, so
// change detection is the only thing that can skip the export.
func TestEmbeddedAutoExportUncommittedWriteReachesJSONL(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt auto-export tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	for _, tc := range []struct{ mode, prefix string }{
		{"batch", "aeb"},
		{"off", "aeo"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", tc.prefix)
			bdCommand(t, bd, dir, "config", "set", "export.auto", "true")
			exportPath := filepath.Join(beadsDir, "issues.jsonl")

			// Nonempty baseline: a committed create that auto-export wrote.
			baseline := strings.TrimSpace(bdCreateSilent(t, bd, dir, "baseline issue"))
			if !jsonlHasIssueID(t, exportPath, baseline) {
				t.Fatalf("baseline %s is not in %s; the fixture needs an exported baseline", baseline, exportPath)
			}

			// Open the throttle: age the saved timestamp past the default
			// 60s export.interval.
			state := loadExportAutoState(beadsDir)
			state.Timestamp = time.Now().Add(-2 * time.Hour)
			saveExportAutoState(beadsDir, state)
			if !shouldExport(loadExportAutoState(beadsDir), 60*time.Second) {
				t.Fatal("aged export state still throttles; the fixture must leave only change detection in the way")
			}

			head := embeddedCurrentCommit(t, beadsDir, tc.prefix)
			id := strings.TrimSpace(bdCreateSilent(t, bd, dir, "uncommitted issue", "--dolt-auto-commit", tc.mode))
			assertEmbeddedHeadUnchanged(t, beadsDir, tc.prefix, head, "create --dolt-auto-commit "+tc.mode)

			if !jsonlHasIssueID(t, exportPath, id) {
				data, _ := os.ReadFile(exportPath)
				t.Fatalf("issues.jsonl does not list %s, created under --dolt-auto-commit %s with HEAD unchanged; auto-export took the write for no change:\n%s", id, tc.mode, data)
			}
			if !jsonlHasIssueID(t, exportPath, baseline) {
				t.Fatalf("issues.jsonl lost the baseline %s", baseline)
			}
		})
	}
}
