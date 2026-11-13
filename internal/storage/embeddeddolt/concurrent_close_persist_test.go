//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
	"github.com/steveyegge/beads/internal/types"
	"golang.org/x/sync/errgroup"
)

// TestMultiProcessClosePersists is storage-only regression coverage for the
// load shape GH#4767 reports: concurrent processes in embedded (direct) mode
// each claim and close their own issue, every invocation in its own process
// (as each bd command does), and afterwards a fresh open must see every issue
// closed. It passes at base; it does not reproduce the reported lost close
// (it omits CLI routing, hooks/imports and sustained agent load), so a pass is
// not proof that the report is fixed.
func TestMultiProcessClosePersists(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt concurrency tests")
	}
	workers := envInt("BEADS_CLOSE_PERSIST_WORKERS", 8)
	rounds := envInt("BEADS_CLOSE_PERSIST_ROUNDS", 3)

	fixture := newPristineEmbeddedDoltFixture(t, "cc")
	ctx := t.Context()
	ids := make([][]string, workers)
	for w := 0; w < workers; w++ {
		for r := 0; r < rounds; r++ {
			issue := &types.Issue{
				Title:     fmt.Sprintf("worker %d round %d", w, r),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := fixture.store.CreateIssue(ctx, issue, "coordinator"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			ids[w] = append(ids[w], issue.ID)
		}
	}
	closeEmbeddedDoltStore(t, fixture.store)

	testBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for w := 0; w < workers; w++ {
		worker := w
		eg.Go(func() error {
			for _, id := range ids[worker] {
				for _, op := range []string{"claim", "close"} {
					cmd := exec.CommandContext(egCtx, testBin, "-test.run=^TestClosePersistHelperProcess$", "-test.v")
					cmd.Env = append(bazeltest.ShardFreeEnv(os.Environ()),
						"BEADS_CLOSE_PERSIST_HELPER=1",
						"BEADS_CLOSE_PERSIST_DIR="+fixture.beadsDir,
						"BEADS_CLOSE_PERSIST_ID="+id,
						"BEADS_CLOSE_PERSIST_OP="+op,
						"BEADS_CLOSE_PERSIST_ACTOR=worker-"+strconv.Itoa(worker),
					)
					out, err := cmd.CombinedOutput()
					if err != nil || !strings.Contains(string(out), "HELPER_OK") {
						return fmt.Errorf("worker %d %s %s: %v\n%s", worker, op, id, err, out)
					}
				}
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatal(err)
	}

	store, err := embeddeddolt.Open(ctx, fixture.beadsDir, "cc", "main")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEmbeddedDoltStore(t, store)
	var lost []string
	for _, workerIDs := range ids {
		for _, id := range workerIDs {
			got, err := store.GetIssue(ctx, id)
			if err != nil {
				t.Fatalf("GetIssue %s: %v", id, err)
			}
			if got.Status != types.StatusClosed {
				lost = append(lost, fmt.Sprintf("%s=%s", id, got.Status))
			}
		}
	}
	if len(lost) > 0 {
		t.Fatalf("%d of %d successful closes did not persist: %v", len(lost), workers*rounds, lost)
	}
}

// TestClosePersistHelperProcess is the per-invocation subprocess for
// TestMultiProcessClosePersists; it does nothing unless
// BEADS_CLOSE_PERSIST_HELPER is set.
func TestClosePersistHelperProcess(t *testing.T) {
	if os.Getenv("BEADS_CLOSE_PERSIST_HELPER") != "1" {
		return
	}
	ctx := t.Context()
	store, err := embeddeddolt.Open(ctx, os.Getenv("BEADS_CLOSE_PERSIST_DIR"), "cc", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeEmbeddedDoltStore(t, store)
	id := os.Getenv("BEADS_CLOSE_PERSIST_ID")
	actor := os.Getenv("BEADS_CLOSE_PERSIST_ACTOR")
	switch op := os.Getenv("BEADS_CLOSE_PERSIST_OP"); op {
	case "claim":
		err = store.ClaimIssue(ctx, id, actor)
	case "close":
		err = store.CloseIssue(ctx, id, "done", actor, "")
	default:
		t.Fatalf("unknown op %q", op)
	}
	if err != nil {
		t.Fatalf("%s %s: %v", os.Getenv("BEADS_CLOSE_PERSIST_OP"), id, err)
	}
	fmt.Println("HELPER_OK")
}
