//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests in this file pin how every import path handles a record that
// fails validation (#4492): `bd import` refuses the file by default, naming
// the line, and writes nothing; --skip-invalid imports the rest and reports
// (and with --rejects saves) the skipped lines; the recovery imports skip and
// quarantine them beside the source.

// bdImportRun runs "bd import" and returns its streams and error.
func bdImportRun(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"import"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, errOut, err := runCommandBuffers(t, cmd)
	return out.String(), errOut.String(), err
}

// invalidImportLine is one JSONL issue line; status "verify" is not a
// built-in status.
func invalidImportLine(t *testing.T, id, title, status string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(map[string]any{
		"id": id, "title": title, "status": status, "issue_type": "task",
		"created_at": now, "updated_at": now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeImportLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listedIDs(t *testing.T, bd, dir string) []string {
	t.Helper()
	var ids []string
	for _, issue := range bdListJSON(t, bd, dir, "--all", "--limit", "0") {
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	return ids
}

const importMemoryLine = `{"_type":"memory","key":"screened-memory","value":"kept with the valid rows"}`

func TestEmbeddedImportInvalidRecordNamesLine(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("strict", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "imbad")
		// config.yaml names a different prefix: an import that writes
		// anything seeds or syncs issue_prefix to it, so a refusal must leave
		// the database's prefix as init wrote it.
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: imnew\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeImportLines(t, jsonlPath,
			invalidImportLine(t, "imbad-aaa", "Valid A", "open"),
			invalidImportLine(t, "imbad-bbb", "Invalid B", "verify"),
			importMemoryLine,
			invalidImportLine(t, "imbad-ccc", "Valid C", "open"),
		)

		stdout, stderr, err := bdImportRun(t, bd, dir, jsonlPath)
		out := stdout + stderr
		if err == nil {
			t.Fatalf("bd import of a file with an invalid status should fail by default; output:\n%s", out)
		}
		for _, want := range []string{"line 2 (issue imbad-bbb)", "invalid status: verify", "nothing was imported", "--skip-invalid", "--rejects"} {
			if !strings.Contains(out, want) {
				t.Fatalf("import error should contain %q; got:\n%s", want, out)
			}
		}
		if ids := listedIDs(t, bd, dir); len(ids) != 0 {
			t.Fatalf("a refused import wrote issues %v", ids)
		}
		if mem := bdMemories(t, bd, dir); strings.Contains(mem, "screened-memory") {
			t.Fatalf("a refused import wrote its memory:\n%s", mem)
		}
		if val := readBack(t, beadsDir, "imbad", "issue_prefix", false); val != "imbad" {
			t.Fatalf("a refused import changed issue_prefix to %q, want %q", val, "imbad")
		}
	})

	// An invalid row after the first 250-row chunk: the screen runs before
	// the first chunk commits, so no chunk lands.
	t.Run("after_a_chunk", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imchk")
		var lines []string
		for i := 1; i <= 300; i++ {
			lines = append(lines, invalidImportLine(t, fmt.Sprintf("imchk-%d", i), fmt.Sprintf("Valid %d", i), "open"))
		}
		lines = append(lines, invalidImportLine(t, "imchk-bad", "Invalid", "verify"))
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeImportLines(t, jsonlPath, lines...)

		stdout, stderr, err := bdImportRun(t, bd, dir, jsonlPath)
		out := stdout + stderr
		if err == nil || !strings.Contains(out, "line 301 (issue imchk-bad)") {
			t.Fatalf("bd import should fail naming line 301 (issue imchk-bad); err %v, output:\n%s", err, out)
		}
		if ids := listedIDs(t, bd, dir); len(ids) != 0 {
			t.Fatalf("a refused chunked import wrote %d issues", len(ids))
		}
	})
}
