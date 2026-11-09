//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// importRejectJSON mirrors one entry of the import --json "rejected" array
// (importRejection, cmd/bd/import_screen.go), for the tests below.
type importRejectJSON struct {
	Line   int    `json:"line"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// importSkipInvalidJSON mirrors the import --json result fields the
// skip-invalid tests assert on (importResultJSON, cmd/bd/import.go).
type importSkipInvalidJSON struct {
	Source          string             `json:"source"`
	Created         int                `json:"created"`
	Updated         int                `json:"updated"`
	Unchanged       int                `json:"unchanged"`
	Skipped         int                `json:"skipped"`
	DedupHits       int                `json:"dedup_skipped"`
	Memories        int                `json:"memories"`
	StaleSkippedIDs []string           `json:"stale_skipped_ids"`
	Rejected        []importRejectJSON `json:"rejected"`
	RejectsFile     string             `json:"rejects_file"`
	DryRun          bool               `json:"dry_run"`
}

// bdImportBuffers runs "bd import" with the given stdin (nil for none) and
// returns both streams and the exit error, without failing on a nonzero exit.
func bdImportBuffers(t *testing.T, bd, dir string, stdin io.Reader, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"import"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String(), stderr.String(), err
}

// mixSkipInvalidJSONL builds the canonical mixed file for the skip-invalid
// tests and returns its content. Line 1 valid, line 2 invalid status, line 3
// blank, line 4 valid, line 5 invalid type, line 6 a memory record — so line
// numbers count every physical line and the rejects file must echo lines 2
// and 5 exactly, in source order.
func mixSkipInvalidJSONL(t *testing.T, now time.Time) string {
	t.Helper()
	issueLine := func(id, title string, status types.Status, issueType types.IssueType) string {
		b, err := json.Marshal(types.Issue{
			ID: id, Title: title, Status: status, IssueType: issueType,
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("marshal issue: %v", err)
		}
		return string(b)
	}
	return strings.Join([]string{
		issueLine("imskip-aaa", "Valid A", types.StatusOpen, types.TypeTask),
		issueLine("imskip-bad", "Invalid status", types.Status("verify"), types.TypeTask),
		"",
		issueLine("imskip-ccc", "Valid C", types.StatusOpen, types.TypeBug),
		issueLine("imskip-bad2", "Invalid type", types.StatusOpen, types.IssueType("not-a-type")),
		`{"_type":"memory","key":"skip-key","value":"skip-value"}`,
	}, "\n") + "\n"
}

// skipInvalidMixRejected returns the two raw rejected lines of the given mix
// content, in source order, LF-terminated — the exact bytes a --rejects file
// must contain for that input.
func skipInvalidMixRejected(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "imskip-bad") || strings.Contains(line, "imskip-bad2") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// assertImportSkipInvalidMix asserts the canonical mixed-file outcome: two
// valid rows land, two rejects are reported in source order, the memory lands
// and the rejects file holds exactly the rejected lines.
func assertImportSkipInvalidMix(t *testing.T, bd, dir, content, stdout, stderr, rejectsPath string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("bd import --skip-invalid should succeed; err=%v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var result importSkipInvalidJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("parse --json output %q: %v", stdout, err)
	}
	if result.Created != 2 {
		t.Errorf("Created = %d, want 2 (the two valid records); result: %+v", result.Created, result)
	}
	if result.Memories != 1 {
		t.Errorf("Memories = %d, want 1 (the memory record is kept)", result.Memories)
	}
	if result.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (both invalid records)", result.Skipped)
	}
	if len(result.Rejected) != 2 {
		t.Fatalf("Rejected = %+v, want 2 entries", result.Rejected)
	}
	if result.Rejected[0].Line != 2 || result.Rejected[0].ID != "imskip-bad" || !strings.Contains(result.Rejected[0].Reason, "invalid status: verify") {
		t.Errorf("Rejected[0] = %+v, want line 2 / imskip-bad / invalid status: verify", result.Rejected[0])
	}
	if result.Rejected[1].Line != 5 || result.Rejected[1].ID != "imskip-bad2" || !strings.Contains(result.Rejected[1].Reason, "invalid issue type") {
		t.Errorf("Rejected[1] = %+v, want line 5 / imskip-bad2 / invalid issue type", result.Rejected[1])
	}
	if result.RejectsFile != rejectsPath {
		t.Errorf("RejectsFile = %q, want the saved rejects path %q", result.RejectsFile, rejectsPath)
	}
	if !strings.Contains(stderr, "line 2") || !strings.Contains(stderr, "imskip-bad") {
		t.Errorf("stderr should list the rejected records with their lines; stderr:\n%s", stderr)
	}
	saved, readErr := os.ReadFile(rejectsPath)
	if readErr != nil {
		t.Fatalf("read rejects file: %v", readErr)
	}
	if string(saved) != skipInvalidMixRejected(content) {
		t.Errorf("rejects file should hold exactly the rejected lines in source order; got:\n%s", saved)
	}
	for _, id := range []string{"imskip-aaa", "imskip-ccc"} {
		showCmd := exec.Command(bd, "show", id)
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		if _, _, showErr := runCommandBuffers(t, showCmd); showErr != nil {
			t.Errorf("bd show %s should succeed after the skip-invalid import: %v", id, showErr)
		}
	}
	for _, id := range []string{"imskip-bad", "imskip-bad2"} {
		showCmd := exec.Command(bd, "show", id)
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		if _, _, showErr := runCommandBuffers(t, showCmd); showErr == nil {
			t.Errorf("bd show %s should fail: the rejected record must not land", id)
		}
	}
}

// TestEmbeddedImportSkipInvalidCases extends TestEmbeddedImportSkipInvalid
// across the inputs a --skip-invalid import meets: stdin, duplicate IDs, a
// file with nothing valid (with and without memories), a file with nothing
// invalid, CRLF and whitespace, and a repaired rejects file re-imported on
// its own. Rejected rows are reported on stderr even under --json.
func TestEmbeddedImportSkipInvalidCases(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("stdin_import", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imstdin")
		content := mixSkipInvalidJSONL(t, time.Now().UTC())
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportBuffers(t, bd, dir,
			strings.NewReader(content),
			"-", "--skip-invalid", "--rejects", rejectsPath, "--json")
		assertImportSkipInvalidMix(t, bd, dir, content, stdout, stderr, rejectsPath, err)
	})

	t.Run("duplicate_ids_are_not_rejections", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imdup")
		now := time.Now().UTC()
		later := now.Add(time.Hour)
		older, err := json.Marshal(types.Issue{ID: "imdup-aaa", Title: "Older row", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		newer, err := json.Marshal(types.Issue{ID: "imdup-aaa", Title: "Newer row", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: later})
		if err != nil {
			t.Fatal(err)
		}
		jsonlPath := filepath.Join(t.TempDir(), "dup.jsonl")
		content := string(older) + "\n" + string(newer) + "\n" + `{"id":"imdup-bad","title":"Invalid","status":"verify","created_at":"` + now.Format(time.RFC3339) + `","updated_at":"` + now.Format(time.RFC3339) + `"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("bd import --skip-invalid should succeed; err=%v\nstderr:\n%s", err, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if len(result.Rejected) != 1 || result.Rejected[0].ID != "imdup-bad" {
			t.Errorf("Rejected = %+v, want only imdup-bad (duplicate IDs are upserts, not rejections)", result.Rejected)
		}
		if result.Created+result.Updated != 2 {
			t.Errorf("both same-ID rows should land (one created, one updated); Created=%d Updated=%d", result.Created, result.Updated)
		}
	})

	t.Run("all_invalid_with_memories", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imallm")
		now := time.Now().UTC()
		content := `{"id":"imallm-bad1","title":"Bad status","status":"verify","created_at":"` + now.Format(time.RFC3339) + `","updated_at":"` + now.Format(time.RFC3339) + `"}` + "\n" +
			`{"_type":"memory","key":"allm-key","value":"allm-value"}` + "\n"
		jsonlPath := filepath.Join(t.TempDir(), "all-invalid.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("all-invalid skip import should still succeed (nothing to import, memory kept); err=%v\nstderr:\n%s", err, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if result.Created != 0 {
			t.Errorf("Created = %d, want 0 (every issue row was invalid)", result.Created)
		}
		if result.Memories != 1 {
			t.Errorf("Memories = %d, want 1 (memories are kept when all issues are rejected)", result.Memories)
		}
		saved, readErr := os.ReadFile(rejectsPath)
		if readErr != nil {
			t.Fatalf("read rejects file: %v", readErr)
		}
		if want := strings.SplitAfter(content, "\n")[0]; string(saved) != want {
			t.Errorf("rejects file = %q, want exactly the rejected line %q", saved, want)
		}
	})

	t.Run("all_invalid_no_memories", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imall")
		now := time.Now().UTC()
		content := `{"id":"imall-bad1","title":"Bad status","status":"verify","created_at":"` + now.Format(time.RFC3339) + `","updated_at":"` + now.Format(time.RFC3339) + `"}` + "\n" +
			`{"id":"imall-bad2","title":"Bad type","issue_type":"not-a-type","status":"open","created_at":"` + now.Format(time.RFC3339) + `","updated_at":"` + now.Format(time.RFC3339) + `"}` + "\n"
		jsonlPath := filepath.Join(t.TempDir(), "all-invalid.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("all-invalid skip import should still succeed; err=%v\nstderr:\n%s", err, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if result.Created != 0 || result.Memories != 0 {
			t.Errorf("Created=%d Memories=%d, want 0/0", result.Created, result.Memories)
		}
		if len(result.Rejected) != 2 {
			t.Errorf("Rejected = %+v, want both invalid rows", result.Rejected)
		}
		if issues := bdListJSONAllowError(t, bd, dir); len(issues) != 0 {
			t.Errorf("no rows should land; found %d", len(issues))
		}
	})

	t.Run("no_rejects_writes_empty_file", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imnone")
		now := time.Now().UTC()
		jsonlPath := filepath.Join(t.TempDir(), "all-valid.jsonl")
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imnone-aaa", Title: "Valid A", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("bd import should succeed; err=%v\nstderr:\n%s", err, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d, want 1", result.Created)
		}
		if len(result.Rejected) != 0 {
			t.Errorf("Rejected = %+v, want none", result.Rejected)
		}
		saved, readErr := os.ReadFile(rejectsPath)
		if readErr != nil {
			t.Fatalf("an import with no rejects must still write the (empty) rejects file: %v", readErr)
		}
		if len(saved) != 0 {
			t.Errorf("rejects file should be empty; got %q", saved)
		}
	})

	t.Run("crlf_and_whitespace", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imcrlf")
		// Whole seconds: the file carries the same row twice, and Dolt's
		// DATETIME(0) rounds a ≥.5 fraction up — the duplicate would then
		// read as stale against its own first copy and the result assembly
		// would drop both rows of the shared id.
		now := time.Now().UTC().Truncate(time.Second)
		valid, _ := json.Marshal(types.Issue{ID: "imcrlf-aaa", Title: "Valid A", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now})
		invalid, _ := json.Marshal(types.Issue{ID: "imcrlf-bad", Title: "Invalid", Status: types.Status("verify"), IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now})
		// CRLF terminators, a blank line, and a leading-whitespace line: line
		// numbers count every physical line and raw lines are echoed without
		// their terminator (the rejects file is LF-normalized).
		content := "  " + string(valid) + "\r\n" + string(invalid) + "\r\n\r\n" + string(valid) + "\r\n"
		jsonlPath := filepath.Join(t.TempDir(), "crlf.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("bd import --skip-invalid should succeed; err=%v\nstderr:\n%s", err, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if result.Created != 2 {
			t.Errorf("Created = %d, want 2 (both valid rows, whitespace tolerated)", result.Created)
		}
		if len(result.Rejected) != 1 || result.Rejected[0].Line != 2 {
			t.Errorf("Rejected = %+v, want one entry on line 2 (line numbers count blank lines)", result.Rejected)
		}
		saved, readErr := os.ReadFile(rejectsPath)
		if readErr != nil {
			t.Fatalf("read rejects file: %v", readErr)
		}
		if want := string(invalid) + "\n"; string(saved) != want {
			t.Errorf("rejects file = %q, want the rejected line LF-terminated without CR: %q", saved, want)
		}
	})

	t.Run("repaired_rejects_file_reimports", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imfix")
		now := time.Now().UTC()
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(mixSkipInvalidJSONL(t, now)), 0644); err != nil {
			t.Fatal(err)
		}
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
		if _, _, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath); err != nil {
			t.Fatalf("first import: %v", err)
		}

		// Repair the saved lines and import them on their own, strictly.
		repaired := filepath.Join(t.TempDir(), "repaired.jsonl")
		raw, err := os.ReadFile(rejectsPath)
		if err != nil {
			t.Fatal(err)
		}
		fixed := strings.ReplaceAll(string(raw), `"verify"`, `"open"`)
		fixed = strings.ReplaceAll(fixed, `"not-a-type"`, `"task"`)
		if err := os.WriteFile(repaired, []byte(fixed), 0644); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := bdImportBuffers(t, bd, dir, nil, repaired)
		if err != nil {
			t.Fatalf("a repaired rejects file must import strictly without error; err=%v\nstderr:\n%s", err, stderr)
		}
		for _, id := range []string{"imskip-bad", "imskip-bad2"} {
			showCmd := exec.Command(bd, "show", id)
			showCmd.Dir = dir
			showCmd.Env = bdEnv(dir)
			if _, _, showErr := runCommandBuffers(t, showCmd); showErr != nil {
				t.Errorf("bd show %s should succeed after the repaired re-import: %v", id, showErr)
			}
		}
	})
}

// TestEmbeddedImportInvalidRegisteredVocabularyModes pins the vocabulary
// half of #4492 source by source and in both modes, beside
// TestEmbeddedImportInvalidRegisteredVocabulary's combined-source case: a
// record using a custom status or type the workspace actually registered —
// in the database (bd config set) or in config.yaml — is NOT a reject. The screen reads the writer's own vocabulary, so what the writer
// accepts, the import accepts; a screen that rejected these would be worse
// than the bug being fixed.
func TestEmbeddedImportInvalidRegisteredVocabularyModes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// registeredVocabularyRows returns one row per custom combination: a
	// custom status on a built-in type, a built-in status on a custom type,
	// and both custom at once. Registered vocabulary must accept all three.
	registeredVocabularyRows := func(prefix string, now time.Time) []types.Issue {
		return []types.Issue{
			{ID: prefix + "-cst", Title: "Custom status", Status: types.Status("verify"), IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
			{ID: prefix + "-ctp", Title: "Custom type", Status: types.StatusOpen, IssueType: types.IssueType("quest"), CreatedAt: now, UpdatedAt: now},
			{ID: prefix + "-both", Title: "Both custom", Status: types.Status("verify"), IssueType: types.IssueType("quest"), CreatedAt: now, UpdatedAt: now},
		}
	}

	// assertAllImported proves the registered-vocabulary file imports clean
	// both ways: strictly (a registered record must not even be a reject
	// under the default) and with --skip-invalid --rejects --json (zero
	// rejects, empty rejects file). Each run gets a fresh workspace — with
	// the vocabulary registered in IT, by register — so the first import
	// cannot mask the second's counts and neither run borrows the other's
	// registration.
	assertAllImported := func(t *testing.T, prefix string, register func(t *testing.T, dir, beadsDir string), jsonlPath string) {
		t.Helper()
		strictDir, strictBeads, _ := bdInit(t, bd, "--prefix", prefix+"s")
		register(t, strictDir, strictBeads)
		strictOut, strictErrTxt, err := bdImportBuffers(t, bd, strictDir, nil, jsonlPath)
		if err != nil {
			t.Fatalf("strict import must succeed when the vocabulary is registered (a registered record is not a reject);\nstdout:\n%s\nstderr:\n%s", strictOut, strictErrTxt)
		}
		if issues := bdListJSONAllowError(t, bd, strictDir); len(issues) != 3 {
			t.Errorf("strict import should land all 3 records; found %d", len(issues))
		}

		skipDir, skipBeads, _ := bdInit(t, bd, "--prefix", prefix+"k")
		register(t, skipDir, skipBeads)
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
		stdout, stderr, err := bdImportBuffers(t, bd, skipDir, nil, jsonlPath, "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("bd import --skip-invalid should succeed; err=%v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if result.Created != 3 {
			t.Errorf("Created = %d, want 3 (registered vocabulary must not reject); result: %+v", result.Created, result)
		}
		if len(result.Rejected) != 0 {
			t.Errorf("Rejected = %+v, want none — every record is in the registered vocabulary", result.Rejected)
		}
		saved, readErr := os.ReadFile(rejectsPath)
		if readErr != nil {
			t.Fatalf("read rejects file: %v", readErr)
		}
		if len(saved) != 0 {
			t.Errorf("rejects file should be empty; got %q", saved)
		}
	}

	t.Run("db_registered_status_and_type", func(t *testing.T) {
		register := func(t *testing.T, dir, beadsDir string) {
			t.Helper()
			bdRunOK(t, bd, dir, "config", "set", "status.custom", "verify:active")
			bdRunOK(t, bd, dir, "config", "set", "types.custom", "quest")
		}
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeJSONLFile(t, jsonlPath, registeredVocabularyRows("imreg", time.Now().UTC()))
		assertAllImported(t, "imreg", register, jsonlPath)
	})

	t.Run("yaml_overlay_status_and_type", func(t *testing.T) {
		register := func(t *testing.T, dir, beadsDir string) {
			t.Helper()
			yaml := "status:\n  custom: [verify:active]\ntypes:\n  custom: [quest]\n"
			path := filepath.Join(beadsDir, "config.yaml")
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				existing = nil // no config.yaml yet — the sections become the whole file
			}
			if err := os.WriteFile(path, append(existing, yaml...), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeJSONLFile(t, jsonlPath, registeredVocabularyRows("imreg", time.Now().UTC()))
		assertAllImported(t, "imyal", register, jsonlPath)
	})
}

// TestEmbeddedImportInvalidStaleAndDedupCases pins the ordering half of
// #4492's screen: the stale filter runs FIRST, so an invalid record that is
// about to be skipped anyway is never a reject. A stale row will not be
// written, so it cannot be rejected either — the strict default succeeds,
// reports the row as stale-skipped, and the stored (newer) data survives
// untouched. Only --allow-stale, which puts that row back on the write path,
// puts it back under the screen.
func TestEmbeddedImportInvalidStaleAndDedupCases(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// seedNewerRow imports a fresh valid snapshot so the workspace holds a
	// row for id with the given (newer) timestamps, then returns the dir.
	seedNewerRow := func(t *testing.T, prefix, id, title string, at time.Time) string {
		t.Helper()
		dir, _, _ := bdInit(t, bd, "--prefix", prefix)
		seedPath := filepath.Join(t.TempDir(), "seed.jsonl")
		writeJSONLFile(t, seedPath, []types.Issue{
			{ID: id, Title: title, Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: at, UpdatedAt: at},
		})
		bdImport(t, bd, dir, seedPath)
		return dir
	}

	t.Run("stale_invalid_row_is_stale_skipped_not_rejected", func(t *testing.T) {
		newer := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
		dir := seedNewerRow(t, "imst", "imst-aaa", "Stored newer", newer)

		// The incoming snapshot is older AND its imst-aaa row is invalid.
		older := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imst-aaa", Title: "Stale invalid", Status: types.Status("verify"), IssueType: types.TypeTask, CreatedAt: older, UpdatedAt: older},
			{ID: "imst-bbb", Title: "Fresh valid", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: newer, UpdatedAt: newer},
		})

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--json")
		if err != nil {
			t.Fatalf("strict import must succeed: the invalid row is stale-skipped before the screen ever sees it;\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if len(result.Rejected) != 0 {
			t.Errorf("Rejected = %+v, want none — a row that will not be written cannot be rejected", result.Rejected)
		}
		if len(result.StaleSkippedIDs) != 1 || result.StaleSkippedIDs[0] != "imst-aaa" {
			t.Errorf("StaleSkippedIDs = %v, want [imst-aaa] (reported as stale-skipped, not rejected)", result.StaleSkippedIDs)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d, want 1 (only imst-bbb)", result.Created)
		}
		// The stored row is preserved: same title, same updated_at.
		showCmd := exec.Command(bd, "show", "imst-aaa", "--json")
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		showOut, _, showErr := runCommandBuffers(t, showCmd)
		if showErr != nil {
			t.Fatalf("bd show imst-aaa: %v", showErr)
		}
		var shown []struct {
			Title     string    `json:"title"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(showOut.Bytes(), &shown); err != nil || len(shown) != 1 {
			t.Fatalf("parse bd show --json output %q: %v (len=%d)", showOut.String(), err, len(shown))
		}
		if shown[0].Title != "Stored newer" {
			t.Errorf("stored row must keep its (newer) content; title = %q", shown[0].Title)
		}
		if shown[0].UpdatedAt.Unix() != newer.Unix() {
			t.Errorf("stored row must keep its (newer) updated_at %s; got %s", newer.Format(time.RFC3339), shown[0].UpdatedAt.Format(time.RFC3339))
		}
	})

	t.Run("dedup_hit_is_not_screened", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imdd")
		existingID := bdCreateSilent(t, bd, dir, "Dup Title")

		now := time.Now().UTC().Truncate(time.Second)
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imdd-bad", Title: "Dup Title", Status: types.Status("verify"), IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
			{ID: "imdd-ccc", Title: "Fresh valid", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--dedup", "--json")
		if err != nil {
			t.Fatalf("strict import must succeed: the invalid row is a dedup hit, removed before the screen;\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if result.DedupHits != 1 {
			t.Errorf("DedupHits = %d, want 1 (the invalid duplicate is a dedup hit, not a reject)", result.DedupHits)
		}
		if len(result.Rejected) != 0 {
			t.Errorf("Rejected = %+v, want none", result.Rejected)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d, want 1 (only imdd-ccc)", result.Created)
		}
		showCmd := exec.Command(bd, "show", existingID)
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		if _, _, showErr := runCommandBuffers(t, showCmd); showErr != nil {
			t.Errorf("bd show %s: %v", existingID, showErr)
		}
	})

	t.Run("allow_stale_puts_the_row_back_under_the_screen", func(t *testing.T) {
		newer := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
		dir := seedNewerRow(t, "imas", "imas-aaa", "Stored newer", newer)

		older := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imas-aaa", Title: "Stale invalid", Status: types.Status("verify"), IssueType: types.TypeTask, CreatedAt: older, UpdatedAt: older},
			{ID: "imas-bbb", Title: "Fresh valid", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: newer, UpdatedAt: newer},
		})

		// Strict + --allow-stale: the invalid row would be written, so it is
		// screened, and the strict default fails naming its line.
		_, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--allow-stale")
		if err == nil {
			t.Fatalf("strict --allow-stale import must fail on the invalid row that is about to land")
		}
		if !strings.Contains(stderr, "line 1") || !strings.Contains(stderr, "invalid status: verify") || !strings.Contains(stderr, "nothing was imported") {
			t.Errorf("strict failure should name line 1, the reason, and say nothing was imported; got:\n%s", stderr)
		}
		if issues := bdListJSONAllowError(t, bd, dir); len(issues) != 1 {
			t.Errorf("a failed strict import must not write anything; found %d issues", len(issues))
		}

		// Skip + --allow-stale: the row is rejected, the valid row lands.
		rejectsPath := filepath.Join(t.TempDir(), "rejects.jsonl")
		stdout, stderr, err := bdImportBuffers(t, bd, dir, nil, jsonlPath, "--allow-stale", "--skip-invalid", "--rejects", rejectsPath, "--json")
		if err != nil {
			t.Fatalf("bd import --allow-stale --skip-invalid should succeed; err=%v\nstderr:\n%s", err, stderr)
		}
		var result importSkipInvalidJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
			t.Fatalf("parse --json output %q: %v", stdout, err)
		}
		if len(result.Rejected) != 1 || result.Rejected[0].Line != 1 || result.Rejected[0].ID != "imas-aaa" {
			t.Errorf("Rejected = %+v, want one entry: line 1 / imas-aaa", result.Rejected)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d, want 1 (only imas-bbb)", result.Created)
		}
	})
}
