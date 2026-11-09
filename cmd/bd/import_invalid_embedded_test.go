//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// invalidImportFixture is a JSONL import of n issues prefix-1..prefix-n in
// which the issues numbered in invalid carry the unregistered status "verify"
// (GH#4492). Line 1 is a memory record and line 2 is blank, so issue k sits on
// physical line k+2.
func invalidImportFixture(t *testing.T, prefix string, n int, invalid ...int) string {
	t.Helper()
	bad := map[int]bool{}
	for _, k := range invalid {
		bad[k] = true
	}
	when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	b.WriteString(`{"_type":"memory","key":"probe","value":"kept"}` + "\n\n")
	for k := 1; k <= n; k++ {
		issue := types.Issue{ID: fmt.Sprintf("%s-%d", prefix, k), Title: fmt.Sprintf("row %d", k), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: when, UpdatedAt: when}
		if bad[k] {
			issue.Status = "verify"
		}
		line, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("marshal fixture row %d: %v", k, err)
		}
		b.Write(line)
		b.WriteString("\n")
	}
	return b.String()
}

// bdImportResult runs `bd import` with args and returns stdout, stderr and the
// exit error, without failing the test on a nonzero exit.
func bdImportResult(t *testing.T, bd, dir string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"import"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String(), stderr.String(), err
}

// TestEmbeddedImportInvalidRecordStrictRefusesBeforeAnyWrite pins strict
// `bd import` (AC-1): an input with one record the writer refuses, past the
// first chunk boundary, fails before this import writes anything — no chunk,
// no memory — and names the line, the id, the reason and the way forward.
func TestEmbeddedImportInvalidRecordStrictRefusesBeforeAnyWrite(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "strict")

	path := filepath.Join(t.TempDir(), "mixed.jsonl")
	if err := os.WriteFile(path, []byte(invalidImportFixture(t, "strict", 300, 280)), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := bdImportResult(t, bd, dir, path)
	if err == nil {
		t.Fatalf("strict import of an invalid record succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{path + ":282:", "strict-280", "verify", "nothing was imported", "--skip-invalid"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, stderr)
		}
	}

	store := openStore(t, beadsDir, "strict")
	ctx := context.Background()
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalIssues != 0 {
		t.Errorf("refused import left %d issues behind, want 0", stats.TotalIssues)
	}
	if got, _ := store.GetConfig(ctx, kvPrefix+memoryPrefix+"probe"); got != "" {
		t.Errorf("refused import wrote the memory (%q), want none", got)
	}
}

// TestEmbeddedImportInvalidRecordSkipKeepsRejects pins `bd import
// --skip-invalid --rejects` (AC-2): the valid records and the memory land, the
// invalid ones do not, their original lines are kept in the rejects file, and
// the JSON report counts and locates them on a stdout that stays valid JSON.
func TestEmbeddedImportInvalidRecordSkipKeepsRejects(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("json_and_rejects_file", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "skipj")
		fixture := invalidImportFixture(t, "skipj", 5, 2, 4)
		path := filepath.Join(t.TempDir(), "mixed.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
		rejects := filepath.Join(t.TempDir(), "rejects.jsonl")

		stdout, stderr, err := bdImportResult(t, bd, dir, "--skip-invalid", "--rejects", rejects, "--json", path)
		if err != nil {
			t.Fatalf("bd import --skip-invalid failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var report struct {
			Created        int    `json:"created"`
			Skipped        int    `json:"skipped"`
			Memories       int    `json:"memories"`
			InvalidSkipped int    `json:"invalid_skipped"`
			RejectsFile    string `json:"rejects_file"`
			InvalidRecords []struct {
				Line   int    `json:"line"`
				ID     string `json:"id"`
				Reason string `json:"reason"`
			} `json:"invalid_records"`
		}
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
		}
		if report.Created != 3 || report.InvalidSkipped != 2 || report.Skipped != 2 || report.Memories != 1 || report.RejectsFile != rejects {
			t.Errorf("report = %+v, want created=3 invalid_skipped=2 skipped=2 memories=1 rejects_file=%s", report, rejects)
		}
		if len(report.InvalidRecords) != 2 || report.InvalidRecords[0].Line != 4 || report.InvalidRecords[0].ID != "skipj-2" ||
			report.InvalidRecords[1].Line != 6 || report.InvalidRecords[1].ID != "skipj-4" || !strings.Contains(report.InvalidRecords[0].Reason, "verify") {
			t.Errorf("invalid_records = %+v, want skipj-2 at line 4 and skipj-4 at line 6, reason naming verify", report.InvalidRecords)
		}
		if !strings.Contains(stderr, path+":4: skipj-2") {
			t.Errorf("stderr warning does not locate the skipped record:\n%s", stderr)
		}

		lines := strings.Split(fixture, "\n")
		saved, err := os.ReadFile(rejects)
		if err != nil {
			t.Fatalf("read rejects file: %v", err)
		}
		if want := lines[3] + "\n" + lines[5] + "\n"; string(saved) != want {
			t.Errorf("rejects file =\n%s\nwant the original lines\n%s", saved, want)
		}

		store := openStore(t, beadsDir, "skipj")
		ctx := context.Background()
		for _, id := range []string{"skipj-1", "skipj-3", "skipj-5"} {
			if _, err := store.GetIssue(ctx, id); err != nil {
				t.Errorf("valid record %s not imported: %v", id, err)
			}
		}
		for _, id := range []string{"skipj-2", "skipj-4"} {
			if _, err := store.GetIssue(ctx, id); err == nil {
				t.Errorf("invalid record %s was imported", id)
			}
		}
		if got, _ := store.GetConfig(ctx, kvPrefix+memoryPrefix+"probe"); got != "kept" {
			t.Errorf("memory = %q, want kept", got)
		}
	})

	t.Run("text_without_rejects_names_the_source", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "skipt")
		path := filepath.Join(t.TempDir(), "mixed.jsonl")
		if err := os.WriteFile(path, []byte(invalidImportFixture(t, "skipt", 3, 3)), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, err := bdImportResult(t, bd, dir, "--skip-invalid", path)
		if err != nil {
			t.Fatalf("bd import --skip-invalid failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		for _, want := range []string{"Imported 2 issues", "1 invalid skipped", path + ":5: skipt-3", "not saved separately"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr does not mention %q:\n%s", want, stderr)
			}
		}
	})

	t.Run("rejects_requires_skip_invalid", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "skipr")
		path := filepath.Join(t.TempDir(), "mixed.jsonl")
		if err := os.WriteFile(path, []byte(invalidImportFixture(t, "skipr", 2)), 0o644); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := bdImportResult(t, bd, dir, "--rejects", filepath.Join(t.TempDir(), "r.jsonl"), path)
		if err == nil || !strings.Contains(stderr, "--rejects requires --skip-invalid") {
			t.Errorf("--rejects alone: err=%v stderr=%s, want a refusal naming --skip-invalid", err, stderr)
		}
	})
}

// unwrapOnlyStore hides the single-transaction importer of the store it wraps,
// so maybeAutoImportJSONL takes the fallback path over a real store, while
// Unwrap still reaches the store's other capabilities.
type unwrapOnlyStore struct{ storage.DoltStorage }

func (u unwrapOnlyStore) Unwrap() storage.DoltStorage { return u.DoltStorage }

// TestEmbeddedAutoImportSkipsInvalidRowsAndQuarantines pins auto-import's
// recovery of an empty database from a JSONL file with an invalid record
// (AC-3, GH#4492), on both paths: the valid records and the memory import,
// the invalid record is kept byte for byte in .beads/issues.rejected.jsonl
// with a warning that locates it, the file is stamped as attempted, and the
// command that triggered the import goes on.
func TestEmbeddedAutoImportSkipsInvalidRowsAndQuarantines(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("single_transaction", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "aq")
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		fixture := invalidImportFixture(t, "aq", 3, 2)
		if err := os.WriteFile(jsonlPath, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}

		// A write command runs auto-import in its pre-run, then goes on.
		cmd := exec.Command(bd, "create", "post-import marker", "--type", "task")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdoutBuf, stderrBuf, err := runCommandBuffers(t, cmd)
		stdout, stderr := stdoutBuf.String(), stderrBuf.String()
		if err != nil {
			t.Fatalf("bd create after auto-import failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		for _, want := range []string{"auto-imported 2 issues", jsonlPath + ":4: aq-2", "issues.rejected.jsonl"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr does not mention %q:\n%s", want, stderr)
			}
		}

		ids := map[string]bool{}
		for _, issue := range bdListJSON(t, bd, dir) {
			ids[issue.ID] = true
		}
		if len(ids) != 3 || !ids["aq-1"] || !ids["aq-3"] || ids["aq-2"] {
			t.Errorf("issues = %v, want aq-1, aq-3 and the marker, not aq-2", ids)
		}
		assertAutoImportQuarantined(t, beadsDir, jsonlPath, strings.Split(fixture, "\n")[3])

		store := openStore(t, beadsDir, "aq")
		if got, _ := store.GetConfig(context.Background(), kvPrefix+memoryPrefix+"probe"); got != "kept" {
			t.Errorf("memory = %q, want kept", got)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "aqf")
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		fixture := invalidImportFixture(t, "aqf", 3, 2)
		if err := os.WriteFile(jsonlPath, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}

		store := openStore(t, beadsDir, "aqf")
		ctx := context.Background()
		maybeAutoImportJSONL(ctx, unwrapOnlyStore{store}, beadsDir)

		for _, id := range []string{"aqf-1", "aqf-3"} {
			if _, err := store.GetIssue(ctx, id); err != nil {
				t.Errorf("valid record %s not imported: %v", id, err)
			}
		}
		if _, err := store.GetIssue(ctx, "aqf-2"); err == nil {
			t.Errorf("invalid record aqf-2 was imported")
		}
		if got, _ := store.GetConfig(ctx, kvPrefix+memoryPrefix+"probe"); got != "kept" {
			t.Errorf("memory = %q, want kept", got)
		}
		assertAutoImportQuarantined(t, beadsDir, jsonlPath, strings.Split(fixture, "\n")[3])
	})
}

// assertAutoImportQuarantined checks that auto-import kept the rejected line
// in the quarantine next to jsonlPath and stamped jsonlPath as attempted.
func assertAutoImportQuarantined(t *testing.T, beadsDir, jsonlPath, rejectedLine string) {
	t.Helper()
	saved, err := os.ReadFile(filepath.Join(beadsDir, "issues.rejected.jsonl"))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if string(saved) != rejectedLine+"\n" {
		t.Errorf("quarantine =\n%s\nwant the original line\n%s", saved, rejectedLine)
	}
	info, err := os.Stat(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !autoImportStampMatches(beadsDir, info) {
		t.Errorf("%s was not stamped as attempted", jsonlPath)
	}
}

// TestEmbeddedRecoveryImportsReportInvalidRecords pins AC-4 for the two
// recovery imports: `bd init --from-jsonl` and `bd bootstrap` import the valid
// records, keep the invalid one in .beads/issues.rejected.jsonl, and name it by
// its physical line instead of failing or dropping it silently.
func TestEmbeddedRecoveryImportsReportInvalidRecords(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// recoveryDir is a git repo whose .beads holds only metadata.json and the
	// mixed issues.jsonl.
	recoveryDir := func(t *testing.T, prefix string) (dir, beadsDir, jsonlPath, fixture string) {
		t.Helper()
		dir = t.TempDir()
		initGitRepoAt(t, dir)
		beadsDir = filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		jsonlPath = filepath.Join(beadsDir, "issues.jsonl")
		fixture = invalidImportFixture(t, prefix, 3, 2)
		if err := os.WriteFile(jsonlPath, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, beadsDir, jsonlPath, fixture
	}

	check := func(t *testing.T, bd, dir, beadsDir, jsonlPath, fixture, prefix, output string) {
		t.Helper()
		for _, want := range []string{"skipped 1 invalid record", jsonlPath + ":4: " + prefix + "-2", "issues.rejected.jsonl"} {
			if !strings.Contains(output, want) {
				t.Errorf("output does not mention %q:\n%s", want, output)
			}
		}
		ids := map[string]bool{}
		for _, issue := range bdListJSON(t, bd, dir) {
			ids[issue.ID] = true
		}
		if len(ids) != 2 || !ids[prefix+"-1"] || !ids[prefix+"-3"] {
			t.Errorf("issues = %v, want %s-1 and %s-3", ids, prefix, prefix)
		}
		saved, err := os.ReadFile(filepath.Join(beadsDir, "issues.rejected.jsonl"))
		if err != nil {
			t.Fatalf("read quarantine: %v", err)
		}
		if want := strings.Split(fixture, "\n")[3] + "\n"; string(saved) != want {
			t.Errorf("quarantine =\n%s\nwant\n%s", saved, want)
		}
	}

	t.Run("init_from_jsonl", func(t *testing.T) {
		dir, beadsDir, jsonlPath, fixture := recoveryDir(t, "rqi")
		cmd := exec.Command(bd, "init", "--prefix", "rqi", "--from-jsonl", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("init --from-jsonl failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		check(t, bd, dir, beadsDir, jsonlPath, fixture, "rqi", stdout.String()+stderr.String())
	})

	t.Run("bootstrap", func(t *testing.T) {
		dir, beadsDir, jsonlPath, fixture := recoveryDir(t, "rqb")
		cmd := exec.Command(bd, "bootstrap")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader("y\n")
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bootstrap failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		check(t, bd, dir, beadsDir, jsonlPath, fixture, "rqb", stdout.String()+stderr.String())
	})
}

// TestEmbeddedImportCustomVocabularyAccepted pins AC-5's acceptance half: the
// import screen validates against the workspace's configured statuses and
// types, so rows using them import on the explicit and the auto-import path
// instead of being refused or quarantined.
func TestEmbeddedImportCustomVocabularyAccepted(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// customFixture is invalidImportFixture with row 2 on the custom status
	// "verify" and row 3 on the custom type "spike".
	customFixture := func(t *testing.T, prefix string) string {
		lines := strings.Split(invalidImportFixture(t, prefix, 3, 2), "\n")
		lines[4] = strings.Replace(lines[4], `"issue_type":"task"`, `"issue_type":"spike"`, 1)
		return strings.Join(lines, "\n")
	}
	configure := func(t *testing.T, dir string) {
		t.Helper()
		for _, kv := range [][2]string{{"status.custom", "verify"}, {"types.custom", "spike"}} {
			cmd := exec.Command(bd, "config", "set", kv[0], kv[1])
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bd config set %s: %v\n%s", kv[0], err, out)
			}
		}
	}
	check := func(t *testing.T, dir, beadsDir, prefix string) {
		t.Helper()
		got := map[string]string{}
		for _, issue := range bdListJSON(t, bd, dir, "--all") {
			got[issue.ID] = string(issue.Status) + "/" + string(issue.IssueType)
		}
		if got[prefix+"-2"] != "verify/task" || got[prefix+"-3"] != "open/spike" || got[prefix+"-1"] != "open/task" {
			t.Errorf("issues = %v, want the custom status and type kept", got)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "issues.rejected.jsonl")); !os.IsNotExist(err) {
			t.Errorf("quarantine written for configured values (stat err %v)", err)
		}
	}

	t.Run("explicit_strict", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cve")
		configure(t, dir)
		path := filepath.Join(t.TempDir(), "custom.jsonl")
		if err := os.WriteFile(path, []byte(customFixture(t, "cve")), 0o644); err != nil {
			t.Fatal(err)
		}
		if stdout, stderr, err := bdImportResult(t, bd, dir, path); err != nil {
			t.Fatalf("strict import of configured custom values failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		check(t, dir, beadsDir, "cve")
	})

	t.Run("auto_import", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cva")
		configure(t, dir)
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(customFixture(t, "cva")), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bd, "create", "post-import marker", "--type", "task")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd create after auto-import failed: %v\n%s", err, out)
		}
		check(t, dir, beadsDir, "cva")
	})
}

// TestEmbeddedRecoveryQuarantinePreservesAndFailsClosed pins AC-6 on the
// auto-import recovery path: an existing quarantine keeps its earlier records
// when new ones are added, and a quarantine that cannot be saved safely (here
// a symlink out of the workspace) stops the import before it writes anything,
// while the outer command still runs.
func TestEmbeddedRecoveryQuarantinePreservesAndFailsClosed(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	createMarker := func(t *testing.T, dir string) string {
		t.Helper()
		cmd := exec.Command(bd, "create", "post-import marker", "--type", "task")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd create after auto-import failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		return stderr.String()
	}

	t.Run("keeps_earlier_records", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "qm")
		quarantine := filepath.Join(beadsDir, "issues.rejected.jsonl")
		old := `{"id":"qm-old","status":"verify"}`
		if err := os.WriteFile(quarantine, []byte(old+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture := invalidImportFixture(t, "qm", 3, 2)
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
		createMarker(t, dir)
		saved, err := os.ReadFile(quarantine)
		if err != nil {
			t.Fatal(err)
		}
		if want := old + "\n" + strings.Split(fixture, "\n")[3] + "\n"; string(saved) != want {
			t.Errorf("quarantine =\n%s\nwant the earlier record and the new one\n%s", saved, want)
		}
	})

	t.Run("unsafe_quarantine_imports_nothing", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "qf")
		outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
		if err := os.WriteFile(outside, []byte("elsewhere\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(beadsDir, "issues.rejected.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(invalidImportFixture(t, "qf", 3, 2)), 0o644); err != nil {
			t.Fatal(err)
		}
		stderr := createMarker(t, dir)
		if !strings.Contains(stderr, "nothing was imported") {
			t.Errorf("stderr does not report the failed quarantine save:\n%s", stderr)
		}
		for _, issue := range bdListJSON(t, bd, dir, "--all") {
			if strings.HasPrefix(issue.ID, "qf-") && issue.Title != "post-import marker" {
				t.Errorf("issue %s imported although the quarantine could not be saved", issue.ID)
			}
		}
		if got, _ := os.ReadFile(outside); string(got) != "elsewhere\n" {
			t.Errorf("symlink target = %q, want it untouched", got)
		}
	})
}

// importDryRunReport is the part of `bd import --json` the dry-run tests read.
type importDryRunReport struct {
	Created        int    `json:"created"`
	Skipped        int    `json:"skipped"`
	InvalidSkipped int    `json:"invalid_skipped"`
	RejectsFile    string `json:"rejects_file"`
	InvalidRecords []struct {
		Line int    `json:"line"`
		ID   string `json:"id"`
	} `json:"invalid_records"`
}

// TestEmbeddedImportInvalidRecordDryRun pins AC-7: `bd import --dry-run`
// screens like the real import — strict refuses, --skip-invalid reports the
// same counts — while writing neither the database nor the rejects file, and
// an undecodable line is located by its physical line in every import mode
// before anything is written.
func TestEmbeddedImportInvalidRecordDryRun(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	assertNoIssues := func(t *testing.T, dir, prefix string) {
		t.Helper()
		for _, issue := range bdListJSON(t, bd, dir, "--all") {
			if strings.HasPrefix(issue.ID, prefix+"-") {
				t.Errorf("issue %s written", issue.ID)
			}
		}
	}

	t.Run("strict_refuses_like_the_real_import", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "drs")
		path := filepath.Join(t.TempDir(), "mixed.jsonl")
		if err := os.WriteFile(path, []byte(invalidImportFixture(t, "drs", 3, 2)), 0o644); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := bdImportResult(t, bd, dir, "--dry-run", path)
		if err == nil {
			t.Fatalf("strict dry run of an invalid record succeeded\nstderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, path+":4: drs-2") {
			t.Errorf("stderr does not locate the invalid record:\n%s", stderr)
		}
		assertNoIssues(t, dir, "drs")
	})

	t.Run("skip_invalid_matches_the_real_import_and_writes_nothing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "drk")
		path := filepath.Join(t.TempDir(), "mixed.jsonl")
		if err := os.WriteFile(path, []byte(invalidImportFixture(t, "drk", 3, 2)), 0o644); err != nil {
			t.Fatal(err)
		}
		rejects := filepath.Join(t.TempDir(), "rejects.jsonl")
		run := func(args ...string) (importDryRunReport, string) {
			t.Helper()
			stdout, stderr, err := bdImportResult(t, bd, dir, append(args, "--skip-invalid", "--rejects", rejects, "--json", path)...)
			if err != nil {
				t.Fatalf("bd import %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
			}
			var report importDryRunReport
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
			}
			return report, stderr
		}

		dry, stderr := run("--dry-run")
		if dry.Created != 2 || dry.Skipped != 1 || dry.InvalidSkipped != 1 || dry.RejectsFile != "" ||
			len(dry.InvalidRecords) != 1 || dry.InvalidRecords[0].Line != 4 || dry.InvalidRecords[0].ID != "drk-2" {
			t.Errorf("dry-run report = %+v, want created=2 skipped=1 invalid_skipped=1, drk-2 at line 4, no rejects_file", dry)
		}
		if !strings.Contains(stderr, path+":4: drk-2") {
			t.Errorf("dry-run stderr does not locate the invalid record:\n%s", stderr)
		}
		if _, err := os.Lstat(rejects); !os.IsNotExist(err) {
			t.Errorf("dry run wrote the rejects file (lstat err %v)", err)
		}
		assertNoIssues(t, dir, "drk")

		actual, _ := run()
		if actual.Created != dry.Created || actual.Skipped != dry.Skipped || actual.InvalidSkipped != dry.InvalidSkipped {
			t.Errorf("real import = %+v, dry run = %+v: the counts differ", actual, dry)
		}
	})

	t.Run("undecodable_line_is_located_in_every_mode", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dru")
		lines := strings.Split(invalidImportFixture(t, "dru", 3), "\n")
		lines[3] = `{"id":"dru-2","title":` // physical line 4
		fixture := strings.Join(lines, "\n")
		path := filepath.Join(t.TempDir(), "broken.jsonl")
		if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, mode := range [][]string{nil, {"--skip-invalid"}, {"--dry-run"}, {"--dry-run", "--skip-invalid"}} {
			_, stderr, err := bdImportResult(t, bd, dir, append(mode, path)...)
			if err == nil {
				t.Errorf("bd import %v of an undecodable line succeeded", mode)
			}
			if !strings.Contains(stderr, path+":4:") {
				t.Errorf("bd import %v does not locate the undecodable line:\n%s", mode, stderr)
			}
		}
		assertNoIssues(t, dir, "dru")

		// The recovery import reports the same location and writes nothing.
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bd, "create", "post-import marker", "--type", "task")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		_, stderrBuf, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd create after a failed auto-import: %v\n%s", err, stderrBuf.String())
		}
		if !strings.Contains(stderrBuf.String(), jsonlPath+":4:") {
			t.Errorf("auto-import does not locate the undecodable line:\n%s", stderrBuf.String())
		}
		for _, issue := range bdListJSON(t, bd, dir, "--all") {
			if issue.Title != "post-import marker" {
				t.Errorf("auto-import wrote %s from a file it could not decode", issue.ID)
			}
		}
	})
}
