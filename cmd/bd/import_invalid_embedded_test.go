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

func TestEmbeddedImportSkipInvalid(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imskip")
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "import.jsonl")
	bad1 := invalidImportLine(t, "imskip-bbb", "Invalid B", "verify")
	bad2 := invalidImportLine(t, "imskip-ddd", "Invalid D", "bogus")
	// CRLF on a rejected line: the saved line drops the terminator only.
	if err := os.WriteFile(jsonlPath, []byte(strings.Join([]string{
		invalidImportLine(t, "imskip-aaa", "Valid A", "open"),
		bad1,
		importMemoryLine,
		invalidImportLine(t, "imskip-ccc", "Valid C", "open"),
		bad2 + "\r",
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rejectsPath := filepath.Join(tmp, "rejects.jsonl")
	if err := os.WriteFile(rejectsPath, []byte("stale content from an earlier run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := bdImportRun(t, bd, dir, "--skip-invalid", "--rejects", rejectsPath, "--json", jsonlPath)
	if err != nil {
		t.Fatalf("bd import --skip-invalid failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var result importResultJSON
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parse --json output: %v\n%s", err, stdout)
	}
	if result.Created != 2 || result.Skipped != 2 || result.Memories != 1 {
		t.Fatalf("created %d, skipped %d, memories %d; want 2, 2, 1\n%s", result.Created, result.Skipped, result.Memories, stdout)
	}
	if len(result.Rejected) != 2 ||
		result.Rejected[0].Line != 2 || result.Rejected[0].ID != "imskip-bbb" || !strings.Contains(result.Rejected[0].Reason, "invalid status: verify") ||
		result.Rejected[1].Line != 5 || result.Rejected[1].ID != "imskip-ddd" || !strings.Contains(result.Rejected[1].Reason, "invalid status: bogus") {
		t.Fatalf("rejected = %+v; want line 2 imskip-bbb and line 5 imskip-ddd with their reasons", result.Rejected)
	}
	if result.RejectsFile != rejectsPath {
		t.Fatalf("rejects_file = %q, want %q", result.RejectsFile, rejectsPath)
	}
	saved, err := os.ReadFile(rejectsPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := bad1 + "\n" + bad2 + "\n"; string(saved) != want {
		t.Fatalf("rejects file =\n%q\nwant\n%q", saved, want)
	}
	if ids := listedIDs(t, bd, dir); !slices.Equal(ids, []string{"imskip-aaa", "imskip-ccc"}) {
		t.Fatalf("imported issues = %v, want imskip-aaa and imskip-ccc", ids)
	}
	if mem := bdMemories(t, bd, dir); !strings.Contains(mem, "screened-memory") {
		t.Fatalf("the memory was not imported:\n%s", mem)
	}

	// The saved lines, repaired, re-import cleanly.
	repaired := strings.ReplaceAll(strings.ReplaceAll(string(saved), `"verify"`, `"open"`), `"bogus"`, `"open"`)
	repairedPath := filepath.Join(tmp, "repaired.jsonl")
	if err := os.WriteFile(repairedPath, []byte(repaired), 0o644); err != nil {
		t.Fatal(err)
	}
	bdImport(t, bd, dir, repairedPath)
	if ids := listedIDs(t, bd, dir); len(ids) != 4 {
		t.Fatalf("after re-importing the repaired rejects, issues = %v, want 4", ids)
	}

	t.Run("rejects_requires_skip_invalid", func(t *testing.T) {
		_, stderr, err := bdImportRun(t, bd, dir, "--rejects", filepath.Join(tmp, "x.jsonl"), jsonlPath)
		if err == nil || !strings.Contains(stderr, "--rejects requires --skip-invalid") {
			t.Fatalf("--rejects without --skip-invalid: err %v, stderr:\n%s", err, stderr)
		}
	})

	t.Run("rejects_refuses_the_input", func(t *testing.T) {
		link := filepath.Join(tmp, "alias.jsonl")
		if err := os.Symlink(jsonlPath, link); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(jsonlPath)
		_, stderr, err := bdImportRun(t, bd, dir, "--skip-invalid", "--rejects", link, jsonlPath)
		if err == nil || !strings.Contains(stderr, "is the input file") {
			t.Fatalf("--rejects naming the input: err %v, stderr:\n%s", err, stderr)
		}
		if after, _ := os.ReadFile(jsonlPath); string(after) != string(before) {
			t.Fatal("the input file changed")
		}
	})
}

func TestEmbeddedImportInvalidRegisteredVocabulary(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "imvoc")
	bdConfig(t, bd, dir, "set", "status.custom", "verify:active")
	bdConfig(t, bd, dir, "set", "types.custom", "dbkind")
	f, err := os.OpenFile(filepath.Join(beadsDir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\ntypes.custom: [yamlkind]\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
	writeImportLines(t, jsonlPath,
		invalidImportLine(t, "imvoc-st", "Registered status", "verify"),
		fmt.Sprintf(`{"id":"imvoc-db","title":"DB type","status":"open","issue_type":"dbkind","created_at":%q,"updated_at":%q}`, now, now),
		fmt.Sprintf(`{"id":"imvoc-ym","title":"YAML type","status":"open","issue_type":"yamlkind","created_at":%q,"updated_at":%q}`, now, now),
	)

	stdout, stderr, err := bdImportRun(t, bd, dir, "--skip-invalid", "--json", jsonlPath)
	if err != nil {
		t.Fatalf("bd import --skip-invalid failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var result importResultJSON
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parse --json output: %v\n%s", err, stdout)
	}
	if result.Created != 3 || len(result.Rejected) != 0 {
		t.Fatalf("created %d, rejected %+v; want 3 created, none rejected", result.Created, result.Rejected)
	}
}

func TestEmbeddedImportInvalidStaleRowNotScreened(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imstl")
	tmp := t.TempDir()
	current := time.Now().UTC().Truncate(time.Second)
	older := current.Add(-time.Hour).Format(time.RFC3339)
	row := func(status, updated string) string {
		return fmt.Sprintf(`{"id":"imstl-aaa","title":"Stored","status":%q,"issue_type":"task","created_at":%q,"updated_at":%q}`, status, older, updated)
	}
	stored := filepath.Join(tmp, "stored.jsonl")
	writeImportLines(t, stored, row("open", current.Format(time.RFC3339)))
	bdImport(t, bd, dir, stored)

	// The same id, older and invalid: the stale guard skips it, so it is
	// never written and never screened.
	stale := filepath.Join(tmp, "stale.jsonl")
	writeImportLines(t, stale, row("verify", older))

	for _, args := range [][]string{{"--json"}, {"--skip-invalid", "--json"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stdout, stderr, err := bdImportRun(t, bd, dir, append(args, stale)...)
			if err != nil {
				t.Fatalf("bd import %v of a stale invalid row failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
			}
			var result importResultJSON
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("parse --json output: %v\n%s", err, stdout)
			}
			if !slices.Equal(result.StaleSkippedIDs, []string{"imstl-aaa"}) || len(result.Rejected) != 0 {
				t.Fatalf("stale_skipped_ids %v, rejected %+v; want the row stale-skipped, not rejected", result.StaleSkippedIDs, result.Rejected)
			}
			if show := bdShowJSON(t, bd, dir, "imstl-aaa"); !strings.Contains(show, `"status":"open"`) && !strings.Contains(show, `"status": "open"`) {
				t.Fatalf("stored row changed:\n%s", show)
			}
		})
	}
}

func TestEmbeddedImportInvalidDryRun(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imdr")
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "import.jsonl")
	writeImportLines(t, jsonlPath,
		invalidImportLine(t, "imdr-aaa", "Valid A", "open"),
		invalidImportLine(t, "imdr-bbb", "Invalid B", "verify"),
	)
	rejectsPath := filepath.Join(tmp, "rejects.jsonl")
	const earlier = "an earlier run's rejects\n"
	if err := os.WriteFile(rejectsPath, []byte(earlier), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("strict", func(t *testing.T) {
		stdout, stderr, err := bdImportRun(t, bd, dir, "--dry-run", jsonlPath)
		if err == nil || !strings.Contains(stdout+stderr, "line 2 (issue imdr-bbb)") {
			t.Fatalf("strict dry run should fail naming line 2 (issue imdr-bbb); err %v, output:\n%s%s", err, stdout, stderr)
		}
	})

	t.Run("skip_invalid", func(t *testing.T) {
		stdout, stderr, err := bdImportRun(t, bd, dir, "--dry-run", "--skip-invalid", "--rejects", rejectsPath, "--json", jsonlPath)
		if err != nil {
			t.Fatalf("dry run --skip-invalid failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var result importResultJSON
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("parse --json output: %v\n%s", err, stdout)
		}
		if !result.DryRun || result.Created != 1 || len(result.Rejected) != 1 || result.Rejected[0].Line != 2 || result.RejectsFile != "" {
			t.Fatalf("dry run result = %+v; want 1 would-create, line 2 rejected, no rejects file", result)
		}
		if want := len(result.Rejected) + len(result.StaleSkippedIDs) + result.DedupHits; result.Skipped != want {
			t.Fatalf("skipped %d, want %d (rejected + stale + dedup)", result.Skipped, want)
		}
	})

	if ids := listedIDs(t, bd, dir); len(ids) != 0 {
		t.Fatalf("a dry run wrote issues %v", ids)
	}
	if got, _ := os.ReadFile(rejectsPath); string(got) != earlier {
		t.Fatalf("a dry run rewrote the rejects file: %q", got)
	}
}
