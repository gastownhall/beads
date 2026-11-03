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

// TestEmbeddedImportChunkedForwardDependencyNotSilentlyDropped reproduces
// GH#6113: a chunked restore whose first chunk carries an edge to an issue in
// a later chunk. The chunk that drops the edge must fail gateably
// (--strict-deps exits 1 after committing its rows), and once the target's
// chunk has landed, re-running the dropped edge's chunk must converge: the
// edge lands exactly once and the final strict import exits 0. Exiting 0 on
// every chunk while the edge is lost is the defect.
func TestEmbeddedImportChunkedForwardDependencyNotSilentlyDropped(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imfwd")
	// Whole seconds: an updated_at with sub-second precision can round up
	// when stored, making the re-import of chunk 1 stale-skip its row (and
	// its edges) instead of converging on the tie-merge path.
	now := time.Now().UTC().Truncate(time.Second)

	chunk1 := filepath.Join(t.TempDir(), "chunk1.jsonl")
	writeJSONLFile(t, chunk1, []types.Issue{{
		ID: "imfwd-aaa", Title: "Depends on later chunk", Status: types.StatusOpen, IssueType: types.TypeTask,
		CreatedAt: now, UpdatedAt: now,
		Dependencies: []*types.Dependency{{
			IssueID: "imfwd-aaa", DependsOnID: "imfwd-bbb", Type: types.DepBlocks, CreatedAt: now,
		}},
	}})
	chunk2 := filepath.Join(t.TempDir(), "chunk2.jsonl")
	writeJSONLFile(t, chunk2, []types.Issue{{
		ID: "imfwd-bbb", Title: "Target in later chunk", Status: types.StatusOpen, IssueType: types.TypeTask,
		CreatedAt: now, UpdatedAt: now,
	}})

	run := func(args ...string) (string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String() + stderr.String(), err
	}

	// Chunk 1, strict: the target is in neither the database nor this
	// stream, so the edge is skipped and the import must exit 1 — with the
	// source row committed and the loss named in aggregate.
	out1, err1 := run("import", "--strict-deps", chunk1)
	if err1 == nil {
		t.Fatalf("chunk 1 strict import must exit 1 while imfwd-bbb is unresolved\n%s", out1)
	}
	if !strings.Contains(out1, "Error: 1 dependency edge(s) were reported skipped") {
		t.Fatalf("chunk 1 failure is not the gateable strict-deps error:\n%s", out1)
	}

	// Chunk 2 lands the target.
	if out, err := run("import", chunk2); err != nil {
		t.Fatalf("import chunk2: %v\n%s", err, out)
	}

	// Re-running chunk 1 converges: the edge lands exactly once, and the
	// strict import that failed before now exits 0.
	outFinal, errFinal := run("import", "--strict-deps", chunk1)
	if errFinal != nil {
		t.Fatalf("final strict import must exit 0 once the target landed\n%s", outFinal)
	}
	if strings.Contains(outFinal, "dependency edge") {
		t.Errorf("the converged import still reports skipped edges:\n%s", outFinal)
	}

	show, err := run("show", "imfwd-aaa", "--json")
	if err != nil {
		t.Fatalf("bd show imfwd-aaa: %v\n%s", err, show)
	}
	if n := strings.Count(show, "imfwd-bbb"); n != 1 {
		t.Errorf("edge imfwd-aaa -> imfwd-bbb appears %d times, want exactly 1 (no dropped edge, no duplicate)\nshow:\n%s\nchunk1 output:\n%s", n, show, out1)
	}
}