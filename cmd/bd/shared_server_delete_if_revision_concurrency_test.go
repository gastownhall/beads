//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// sharedServerDeleteIfRevisionRacers is how many concurrent same-token
// `bd delete --if-revision <rev> --force` CLI subprocesses race the same row
// in the same_token subtest below. The bug report (mc-zndi7.73) observed 16
// of 16 racers exiting 0 at this scale on a real Dolt server.
const sharedServerDeleteIfRevisionRacers = 10

// TestSharedServerDeleteIfRevisionSingleWinner pins mc-zndi7.73: on a real,
// shared Dolt server, a guarded delete (`bd delete --if-revision <rev>
// --force`) must behave like every other --if-revision-guarded write
// (TestSharedServerIfRevisionSingleWinner's update/close/assign coverage,
// which deliberately excludes delete and names this test as its
// companion) — exactly one concurrent racer wins, every loser exits
// ExitGuardMismatch (13) classified "precondition failed", never a raw,
// unclassified backend error.
//
// Before the fix, Dolt's commit-time merge treated two concurrent
// transactions that each delete the SAME row as identical diffs and landed
// BOTH with no conflict, so every same-token racer exited 0. A delete racing
// a close or update already produced a real conflict at the storage layer
// (the racing write's row_lock bump is a genuine edit Dolt's merge must
// reconcile against the delete), but internal/storage/dolt/deleter.go used
// withWriteTx (no retry) instead of withRetryTx, so the loser surfaced a raw
// Error 1213 rather than being retried and reclassified as a version
// mismatch. This test pins the single-winner, precondition_failed outcome
// for all three pairings.
func TestSharedServerDeleteIfRevisionSingleWinner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	cp, err := testutil.NewContainerProvider()
	if err != nil {
		// A lane that exists to run the Dolt server suites must not pass
		// green having skipped this test.
		if os.Getenv(testutil.EnvRequireDoltContainer) == "1" {
			t.Fatalf("cannot start Dolt server, but %s=1: %v", testutil.EnvRequireDoltContainer, err)
		}
		t.Skipf("cannot start Dolt container: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })
	containerPort := cp.Port()

	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	baseEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOROOT=" + os.Getenv("GOROOT"),
		"BEADS_SHARED_SERVER_DIR=" + sharedDir,
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(containerPort),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	projectDir := filepath.Join(t.TempDir(), "delrace")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := gitInit(ctx, projectDir); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if out, err := ssExec(ctx, bdBinary, projectDir, baseEnv,
		"init", "--shared-server", "--external", "--prefix", "delrace", "--quiet", "--non-interactive"); err != nil {
		t.Fatalf("bd init: %s: %v", out, err)
	}

	// ssShowRevision runs "bd show <id> --json" and returns the issue's
	// current revision token, the same helper shape as
	// TestSharedServerIfRevisionSingleWinner's ssShowRevision.
	ssShowRevision := func(t *testing.T, id string) string {
		t.Helper()
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "show", id, "--json")
		if err != nil {
			t.Fatalf("bd show %s --json: %s: %v", id, out, err)
		}
		m, err := ssParseShowJSON(out)
		if err != nil {
			t.Fatalf("parse bd show %s --json: %v\nraw: %s", id, err, out)
		}
		rev, _ := m["revision"].(string)
		if rev == "" {
			t.Fatalf("bd show %s --json has no revision field:\n%s", id, out)
		}
		return rev
	}

	// ssRowGone reports whether id no longer resolves via `bd show --json`,
	// the CLI-level proof a delete actually landed.
	ssRowGone := func(t *testing.T, id string) bool {
		t.Helper()
		_, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "show", id, "--json")
		return err != nil
	}

	// race runs one concurrent `bd <args>` subprocess per entry in argvs
	// and returns their exit codes and combined output. Generalized to
	// heterogeneous argv (unlike TestSharedServerIfRevisionSingleWinner's
	// uniform argsFor(i)) so a delete racer can run alongside a close or
	// update racer in the same round.
	race := func(t *testing.T, argvs [][]string) ([]int, []string) {
		t.Helper()
		codes := make([]int, len(argvs))
		outputs := make([]string, len(argvs))
		var wg sync.WaitGroup
		for i, args := range argvs {
			wg.Add(1)
			go func(i int, args []string) {
				defer wg.Done()
				cmd := exec.CommandContext(ctx, bdBinary, args...)
				cmd.Dir = projectDir
				cmd.Env = baseEnv
				out, err := cmd.CombinedOutput()
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
	// to carry the "precondition failed" human text reportIfRevisionFailure
	// emits for the precondition_failed code — never a raw, unclassified
	// backend error (e.g. Dolt's Error 1213) leaking through instead.
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
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "create", "Shared-server delete race (same token)", "--json")
		if err != nil {
			t.Fatalf("create: %s: %v", out, err)
		}
		id, err := ssJSONField(out, "id")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		rev0 := ssShowRevision(t, id)

		argvs := make([][]string, sharedServerDeleteIfRevisionRacers)
		for i := range argvs {
			argvs[i] = []string{"delete", id, "--if-revision", rev0, "--force"}
		}
		codes, outputs := race(t, argvs)
		winner := assertSingleWinner(t, codes, outputs)
		assertLosersPreconditionFailed(t, outputs, winner)

		if !ssRowGone(t, id) {
			t.Errorf("same-token delete race: row %s should be gone after the winning delete", id)
		}
	})

	t.Run("delete_vs_close", func(t *testing.T) {
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "create", "Shared-server delete-vs-close race", "--json")
		if err != nil {
			t.Fatalf("create: %s: %v", out, err)
		}
		id, err := ssJSONField(out, "id")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		rev0 := ssShowRevision(t, id)

		codes, outputs := race(t, [][]string{
			{"delete", id, "--if-revision", rev0, "--force"},
			{"close", id, "--if-revision", rev0, "--reason", "race"},
		})
		winner := assertSingleWinner(t, codes, outputs)
		assertLosersPreconditionFailed(t, outputs, winner)

		gone := ssRowGone(t, id)
		if winner == 0 && !gone {
			t.Errorf("delete-vs-close race: delete won, but row %s still resolves", id)
		}
		if winner == 1 && gone {
			t.Errorf("delete-vs-close race: close won, but row %s is gone", id)
		}
	})

	t.Run("delete_vs_update", func(t *testing.T) {
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "create", "Shared-server delete-vs-update race", "--json")
		if err != nil {
			t.Fatalf("create: %s: %v", out, err)
		}
		id, err := ssJSONField(out, "id")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		rev0 := ssShowRevision(t, id)

		codes, outputs := race(t, [][]string{
			{"delete", id, "--if-revision", rev0, "--force"},
			{"update", id, "--if-revision", rev0, "--spec-id", "race"},
		})
		winner := assertSingleWinner(t, codes, outputs)
		assertLosersPreconditionFailed(t, outputs, winner)

		gone := ssRowGone(t, id)
		if winner == 0 && !gone {
			t.Errorf("delete-vs-update race: delete won, but row %s still resolves", id)
		}
		if winner == 1 && gone {
			t.Errorf("delete-vs-update race: update won, but row %s is gone", id)
		}
	})
}
