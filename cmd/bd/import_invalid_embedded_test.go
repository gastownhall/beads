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
