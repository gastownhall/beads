//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedImportStrictDeps pins the end-to-end skipped-dependency signal
// of `bd import` (GH#6113): an edge whose target is absent from the database
// and the input stream is dropped, and the import used to exit 0 with nothing
// a restore script could gate on. The default must keep exiting 0 but report
// the loss in aggregate; --strict-deps must turn the loss into a gateable
// exit 1 after the rows are committed. Subprocess assertions distinguish the
// strict failure from ordinary errors by checking that the source row landed.
func TestEmbeddedImportStrictDeps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imsd")
	// Second granularity: an un-truncated updated_at can round up when it is
	// stored, making a re-import of the same row stale-skipped. Whole
	// seconds keep a re-run converging on the tie-merge path.
	now := time.Now().UTC().Truncate(time.Second)

	// One row whose blocks edge points at an issue that is neither in the
	// database nor in the import stream: the edge is skipped, the row
	// commits. Each subtest gets its own fresh source row.
	absentTargetFile := func(id string) string {
		path := filepath.Join(t.TempDir(), id+".jsonl")
		writeJSONLFile(t, path, []types.Issue{{
			ID: id, Title: "Source with an absent target " + id, Status: types.StatusOpen, IssueType: types.TypeTask,
			CreatedAt: now, UpdatedAt: now,
			Dependencies: []*types.Dependency{{
				IssueID: id, DependsOnID: "imsd-tgt", Type: types.DepBlocks, CreatedAt: now,
			}},
		}})
		return path
	}

	runImport := func(args ...string) (stdout, stderr string, exitErr error) {
		cmd := exec.Command(bd, append([]string{"import"}, args...)...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdoutBuf, stderrBuf, err := runCommandBuffers(t, cmd)
		return stdoutBuf.String(), stderrBuf.String(), err
	}

	t.Run("default_exit0_summary_and_hint", func(t *testing.T) {
		stdout, stderr, err := runImport(absentTargetFile("imsd-src"))
		if err != nil {
			t.Fatalf("default import must keep exiting 0 when edges are skipped, got %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "Skipped dependency: imsd-src -> imsd-tgt: target not found") {
			t.Errorf("per-edge report was dropped from stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Warning: 1 dependency edge(s) were reported skipped") {
			t.Errorf("missing aggregate skipped-edge summary in stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Hint:") {
			t.Errorf("missing recovery hint in stderr:\n%s", stderr)
		}
		if stdout != "" {
			t.Errorf("text mode must keep stdout clean, got:\n%s", stdout)
		}

		// The source row itself still committed.
		show := exec.Command(bd, "show", "imsd-src")
		show.Dir = dir
		show.Env = bdEnv(dir)
		if out, err := show.CombinedOutput(); err != nil {
			t.Errorf("source row did not commit: %v\n%s", err, out)
		}
	})

	t.Run("strict_text_exit1_row_committed", func(t *testing.T) {
		stdout, stderr, err := runImport("--strict-deps", absentTargetFile("imsd-src2"))
		if err == nil {
			t.Fatalf("strict import must exit nonzero when edges are reported skipped\nstderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Skipped dependency: imsd-src2 -> imsd-tgt: target not found") {
			t.Errorf("per-edge report was dropped from stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Error: 1 dependency edge(s) were reported skipped") {
			t.Errorf("missing aggregate skipped-edge error in stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "--strict-deps") {
			t.Errorf("the aggregate error must name --strict-deps:\n%s", stderr)
		}
		if !strings.Contains(stderr, "remain committed") {
			t.Errorf("the aggregate error must say the imported rows remain committed:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Hint:") {
			t.Errorf("missing recovery hint in stderr:\n%s", stderr)
		}
		if strings.Contains(stderr, "Warning:") {
			t.Errorf("strict mode must not also print the default summary warning:\n%s", stderr)
		}
		if stdout != "" {
			t.Errorf("text mode must keep stdout clean, got:\n%s", stdout)
		}

		// The exit-1 verdict lands AFTER persistence: the source row is
		// committed despite the nonzero exit.
		show := exec.Command(bd, "show", "imsd-src2")
		show.Dir = dir
		show.Env = bdEnv(dir)
		if out, err := show.CombinedOutput(); err != nil {
			t.Errorf("strict failure must leave the source row committed, but bd show imsd-src2 failed: %v\n%s", err, out)
		}
	})
}