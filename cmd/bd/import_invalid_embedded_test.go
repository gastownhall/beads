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
