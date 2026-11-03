//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
