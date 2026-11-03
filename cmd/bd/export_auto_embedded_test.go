//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedAutoExportFirstCreateAfterOptIn walks the documented opt-in
// recipe from docs/reference/configuration.md (GH#6955): on a fresh
// workspace, the two `bd config set` commands each run their own post-command
// auto-export over an EMPTY store, which used to open a full export.interval
// throttle window. The first `bd create` then produced no .beads/issues.jsonl
// until a later write — or none at all. The automatic file must exist and
// contain the new issue with no explicit export.
func TestEmbeddedAutoExportFirstCreateAfterOptIn(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fe")

	// The documented two-config-command opt-in sequence.
	for _, kv := range [][]string{
		{"export.auto", "true"},
		{"export.git-add", "true"},
	} {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("bd config set %s %s: %v\n%s", kv[0], kv[1], err, out)
		}
	}

	// The first durable create, well inside the default 60s interval.
	id := bdCreateSilent(t, bd, dir, "First issue after opt-in", "--type", "task")

	data, err := os.ReadFile(filepath.Join(beadsDir, "issues.jsonl"))
	if err != nil {
		t.Fatalf("automatic export file missing after the first create: %v", err)
	}
	if !strings.Contains(string(data), id) {
		t.Fatalf("issues.jsonl does not contain the newly created issue %s:\n%s", id, data)
	}
}