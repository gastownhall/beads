//go:build cgo

package main

import (
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// proxiedDeleteIfRevisionRacers is how many concurrent same-token
// `bd delete --if-revision <rev> --force` CLI subprocesses race the same row
// in the same_token subtest below, each through its own local dbproxy
// connecting to the one shared Dolt server (requireSharedProxiedServer).
const proxiedDeleteIfRevisionRacers = 10

// TestProxiedDeleteIfRevisionSingleWinner pins mc-zndi7.73 on the proxied
// route — the topology every shared-dolt-server clone actually writes
// through. It is the proxied-tier companion to
// TestSharedServerDeleteIfRevisionSingleWinner (same three pairings: a
// same-token delete race, delete vs close, delete vs update), proved here
// against real `bd` subprocesses each going through their own local dbproxy
// instance rather than connecting to the shared Dolt server directly. Every
// existing proxied --if-revision test (TestProxiedIfRevisionDeleteMatchAndMismatch,
// TestProxiedIfRevisionOutranksReassignFence) races sequentially — set up a
// "holder", then try a stale "thief" afterward — which can never observe two
// writes actually overlapping at the storage layer. This test launches all
// racers concurrently via goroutines, the way
// TestSharedServerIfRevisionSingleWinner's race helper does.
func TestProxiedDeleteIfRevisionSingleWinner(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// race runs one concurrent `bd <args>` subprocess per entry in argvs,
	// all against the same proxied project dir p, and returns their exit
	// codes and combined output.
	race := func(t *testing.T, p proxiedProject, argvs [][]string) ([]int, []string) {
		t.Helper()
		codes := make([]int, len(argvs))
		outputs := make([]string, len(argvs))
		var wg sync.WaitGroup
		for i, args := range argvs {
			wg.Add(1)
			go func(i int, args []string) {
				defer wg.Done()
				out, err := bdProxiedRun(t, bd, p.dir, args...)
				outputs[i] = string(out)
				switch {
				case err == nil:
					codes[i] = 0
				default:
					var ee *exec.ExitError
					if !errors.As(err, &ee) {
						t.Errorf("racer %d: bd %v failed without an exit code: %v\n%s", i, args, err, out)
						return
					}
					codes[i] = ee.ExitCode()
				}
			}(i, args)
		}
		wg.Wait()
		return codes, outputs
	}

	// assertSingleWinner checks that exactly one racer exited 0 and every
	// other racer exited ExitGuardMismatch, and returns the winner's index.
	assertSingleWinner := func(t *testing.T, codes []int, outputs []string) int {
		t.Helper()
		winner := -1
		wins, guarded, other := 0, 0, 0
		for i, code := range codes {
			switch code {
			case 0:
				wins++
				winner = i
			case ExitGuardMismatch:
				guarded++
			default:
				other++
			}
		}
		if wins != 1 || guarded != len(codes)-1 || other != 0 {
			t.Fatalf("single-winner race: got %d exit-0 winners, %d exit-%d guard refusals, %d other; want exactly 1 and %d\ncodes: %v\noutputs: %v",
				wins, guarded, ExitGuardMismatch, other, len(codes)-1, codes, outputs)
		}
		return winner
	}

	// assertLosersPreconditionFailed requires every loser's combined output
	// to carry the "precondition failed" text reportIfRevisionFailure emits
	// for the precondition_failed code, never a raw, unclassified backend
	// error.
	assertLosersPreconditionFailed := func(t *testing.T, outputs []string, winner int) {
		t.Helper()
		for i, out := range outputs {
			if i == winner {
				continue
			}
			if !strings.Contains(out, "precondition failed") {
				t.Errorf("racer %d: loser output does not say \"precondition failed\":\n%s", i, out)
			}
		}
	}

	t.Run("same_token", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pdrst")
		issue := bdProxiedCreate(t, bd, p.dir, "Proxied delete race (same token)")
		rev0 := proxiedRevStr(bdProxiedShowRevision(t, bd, p.dir, issue.ID))

		argvs := make([][]string, proxiedDeleteIfRevisionRacers)
		for i := range argvs {
			argvs[i] = []string{"delete", issue.ID, "--if-revision", rev0, "--force"}
		}
		codes, outputs := race(t, p, argvs)
		winner := assertSingleWinner(t, codes, outputs)
		assertLosersPreconditionFailed(t, outputs, winner)

		db := openProxiedDB(t, p)
		assertRowAbsent(t, db, "issues", issue.ID)
	})

	t.Run("delete_vs_close", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pdrvc")
		issue := bdProxiedCreate(t, bd, p.dir, "Proxied delete-vs-close race")
		rev0 := proxiedRevStr(bdProxiedShowRevision(t, bd, p.dir, issue.ID))

		codes, outputs := race(t, p, [][]string{
			{"delete", issue.ID, "--if-revision", rev0, "--force"},
			{"close", issue.ID, "--if-revision", rev0, "--reason", "race"},
		})
		winner := assertSingleWinner(t, codes, outputs)
		assertLosersPreconditionFailed(t, outputs, winner)

		db := openProxiedDB(t, p)
		if winner == 0 {
			assertRowAbsent(t, db, "issues", issue.ID)
		} else {
			assertRowExists(t, db, "issues", issue.ID)
			if got := readStatus(t, db, issue.ID); got != types.StatusClosed {
				t.Errorf("delete-vs-close race: close won, status = %q, want %q", got, types.StatusClosed)
			}
		}
	})

	t.Run("delete_vs_update", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pdrvu")
		issue := bdProxiedCreate(t, bd, p.dir, "Proxied delete-vs-update race")
		rev0 := proxiedRevStr(bdProxiedShowRevision(t, bd, p.dir, issue.ID))

		codes, outputs := race(t, p, [][]string{
			{"delete", issue.ID, "--if-revision", rev0, "--force"},
			{"update", issue.ID, "--if-revision", rev0, "--spec-id", "race"},
		})
		winner := assertSingleWinner(t, codes, outputs)
		assertLosersPreconditionFailed(t, outputs, winner)

		db := openProxiedDB(t, p)
		if winner == 0 {
			assertRowAbsent(t, db, "issues", issue.ID)
		} else {
			assertRowExists(t, db, "issues", issue.ID)
		}
	})
}
