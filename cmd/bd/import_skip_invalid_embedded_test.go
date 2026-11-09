//go:build cgo

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"encoding/json"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"os/exec"
	"path/filepath"
)

// TestEmbeddedImportSkipInvalidPreservesRejects pins GH#4492 AC-2: with
// --skip-invalid --rejects the valid row and the memory import, the invalid
// row is absent from the database and preserved verbatim in the rejects
// file, and the outcome reports the skip and where the records went.
func TestEmbeddedImportSkipInvalidPreservesRejects(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ce")
	jsonlPath := embeddedScreenFixture(t)
	rejectsPath := filepath.Join(filepath.Dir(jsonlPath), "rejects.jsonl")

	out := bdRunOK(t, bd, dir, "import", "--skip-invalid", "--rejects", rejectsPath, jsonlPath)
	for _, want := range []string{"Imported 1 issues", "(1 invalid records skipped; preserved in"} {
		if !strings.Contains(out, want) {
			t.Errorf("skip-invalid import output = %q, want it to contain %q", out, want)
		}
	}

	store := openStore(t, beadsDir, "ce")
	defer store.Close()
	ctx := context.Background()
	if _, err := store.GetIssue(ctx, "ce-1"); err != nil {
		t.Errorf("valid row ce-1 was not imported: %v", err)
	}
	if _, err := store.GetIssue(ctx, "ce-2"); err == nil {
		t.Error("invalid row ce-2 was imported despite --skip-invalid")
	}
	if mem, err := store.GetConfig(ctx, kvkeys.MemoryConfigKeyPrefix+"hint"); err != nil || mem != "beware" {
		t.Errorf("memory record was not imported (got %q, err %v)", mem, err)
	}

	saved, err := os.ReadFile(rejectsPath)
	if err != nil {
		t.Fatalf("read rejects file: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("rejects file = %q, want ce-2's exact bytes: %q", saved, screenFixtureLines[1])
	}
}

// TestEmbeddedImportSkipInvalidJSONReport pins the --json wire shape of a
// skip-invalid import (GH#4492 AC-2): the invalid count and per-record
// locations ride along in invalid_skipped / invalid_records / rejects_file
// without corrupting the JSON on stdout.
func TestEmbeddedImportSkipInvalidJSONReport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ce")
	jsonlPath := embeddedScreenFixture(t)
	rejectsPath := filepath.Join(filepath.Dir(jsonlPath), "rejects.jsonl")

	cmd := exec.Command(bd, "import", "--json", "--skip-invalid", "--rejects", rejectsPath, jsonlPath)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, _, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd import --json --skip-invalid failed: %v\nstdout:\n%s", err, stdout.String())
	}

	var report struct {
		Created        int                   `json:"created"`
		InvalidSkipped int                   `json:"invalid_skipped"`
		InvalidRecords []importInvalidRecord `json:"invalid_records"`
		RejectsFile    string                `json:"rejects_file"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not the JSON result: %v\n%s", err, stdout.String())
	}
	if report.Created != 1 {
		t.Errorf("created = %d, want 1 (ce-1)", report.Created)
	}
	if report.InvalidSkipped != 1 {
		t.Errorf("invalid_skipped = %d, want 1", report.InvalidSkipped)
	}
	if report.RejectsFile != rejectsPath {
		t.Errorf("rejects_file = %q, want %q", report.RejectsFile, rejectsPath)
	}
	if len(report.InvalidRecords) != 1 {
		t.Fatalf("invalid_records = %+v, want one entry", report.InvalidRecords)
	}
	rec := report.InvalidRecords[0]
	if rec.Line != 2 || rec.ID != "ce-2" || !strings.Contains(rec.Reason, "invalid status: verify") {
		t.Errorf("invalid_records[0] = %+v, want line 2 / ce-2 / invalid status", rec)
	}
}

// TestEmbeddedImportRejectsRequiresSkipInvalid pins the flag pairing
// (GH#4492 design §3): --rejects without --skip-invalid is a usage error,
// because there is nothing for the file to preserve in strict mode.
func TestEmbeddedImportRejectsRequiresSkipInvalid(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ce")
	jsonlPath := embeddedScreenFixture(t)

	out, _ := bdRunFailCode(t, bd, dir, "import", "--rejects", filepath.Join(filepath.Dir(jsonlPath), "r.jsonl"), jsonlPath)
	if !strings.Contains(out, "requires --skip-invalid") {
		t.Errorf("--rejects without --skip-invalid output = %q, want the usage error", out)
	}
}
