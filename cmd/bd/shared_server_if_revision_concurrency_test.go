//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// sharedServerIfRevisionRacers is how many concurrent bd subprocesses race
// the same --if-revision token per verb below. The bug report (mc-zndi7.76
// gap 2) observed 12-13 "winners" out of comparably sized rounds on a real
// Dolt server when the CLI only compared a pre-read RowVersion in Go and
// then sent an unguarded write (mutant MC): embedded-mode CLI tests can
// never catch that, because bd's local exclusive flock serializes every
// mutation regardless of whether the guard is actually wired in. This test
// spawns real concurrent bd processes, each with its own SQL connection,
// against one shared, unlocked Dolt server, so an unguarded write really can
// race — and "win" alongside others — exactly as on a real deployment.
const sharedServerIfRevisionRacers = 10

// TestSharedServerIfRevisionSingleWinner pins mc-zndi7.76 gap 2 (mutant MC):
// N concurrent `bd update|close|assign --if-revision <shared-stale-token>`
// CLI subprocesses race the same row against a real, shared Dolt server with
// no local lock to serialize them. Exactly one must exit 0 and apply its
// write; the rest must exit ExitGuardMismatch (13) having changed nothing.
//
// Delete is deliberately excluded here: delete's production guard is
// independently known-broken under real concurrency (16 of 16 "winners" on
// a real Dolt server, since a deleted row leaves no cell for a concurrent
// deleter to collide on) and is tracked, with its own goroutine-level
// single-winner conformance test, as mc-zndi7.73 — a schema-touching fix
// that is out of scope for this test-only change.
func TestSharedServerIfRevisionSingleWinner(t *testing.T) {
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

	projectDir := filepath.Join(t.TempDir(), "ivrace")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := gitInit(ctx, projectDir); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if out, err := ssExec(ctx, bdBinary, projectDir, baseEnv,
		"init", "--shared-server", "--external", "--prefix", "ivrace", "--quiet", "--non-interactive"); err != nil {
		t.Fatalf("bd init: %s: %v", out, err)
	}

	// ssShowRevision runs "bd show <id> --json" and returns the issue as a
	// map plus its revision token, the way ssParseShowJSON already does for
	// the rest of this package's shared-server helpers.
	ssShowRevision := func(t *testing.T, id string) (map[string]any, string) {
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
		return m, rev
	}

	// race runs sharedServerIfRevisionRacers concurrent `bd <args>` subprocesses,
	// each built from argsFor(i), and returns their exit codes and combined
	// output (the latter only surfaced by assertSingleWinner on failure, to
	// keep passing runs quiet).
	race := func(t *testing.T, argsFor func(i int) []string) ([]int, []string) {
		t.Helper()
		codes := make([]int, sharedServerIfRevisionRacers)
		outputs := make([]string, sharedServerIfRevisionRacers)
		var wg sync.WaitGroup
		for i := range sharedServerIfRevisionRacers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				cmd := exec.CommandContext(ctx, bdBinary, argsFor(i)...)
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
						t.Errorf("racer %d: bd %v failed without an exit code: %v\n%s", i, argsFor(i), err, out)
						return
					}
					codes[i] = ee.ExitCode()
				}
			}(i)
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
		if wins != 1 || guarded != sharedServerIfRevisionRacers-1 || other != 0 {
			t.Fatalf("single-winner race: got %d exit-0 winners, %d exit-%d guard refusals, %d other; want exactly 1 and %d\ncodes: %v\noutputs: %v",
				wins, guarded, ExitGuardMismatch, other, sharedServerIfRevisionRacers-1, codes, outputs)
		}
		return winner
	}

	t.Run("update", func(t *testing.T) {
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "create", "Shared-server update race", "--json")
		if err != nil {
			t.Fatalf("create: %s: %v", out, err)
		}
		id, err := ssJSONField(out, "id")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		_, rev0 := ssShowRevision(t, id)

		codes, outputs := race(t, func(i int) []string {
			return []string{"update", id, "--if-revision", rev0, "--spec-id", fmt.Sprintf("racer-%d", i)}
		})
		winner := assertSingleWinner(t, codes, outputs)

		after, rev1 := ssShowRevision(t, id)
		if rev1 == rev0 {
			t.Errorf("update race: revision did not advance past %s", rev0)
		}
		wantSpecID := fmt.Sprintf("racer-%d", winner)
		if got, _ := after["spec_id"].(string); got != wantSpecID {
			t.Errorf("update race: spec_id = %q, want %q (racer %d's value)", got, wantSpecID, winner)
		}
	})

	t.Run("close", func(t *testing.T) {
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "create", "Shared-server close race", "--json")
		if err != nil {
			t.Fatalf("create: %s: %v", out, err)
		}
		id, err := ssJSONField(out, "id")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		_, rev0 := ssShowRevision(t, id)

		codes, outputs := race(t, func(i int) []string {
			return []string{"close", id, "--if-revision", rev0, "--reason", fmt.Sprintf("racer-%d", i)}
		})
		winner := assertSingleWinner(t, codes, outputs)

		after, rev1 := ssShowRevision(t, id)
		if rev1 == rev0 {
			t.Errorf("close race: revision did not advance past %s", rev0)
		}
		if got, _ := after["status"].(string); got != "closed" {
			t.Errorf("close race: status = %q, want \"closed\"", got)
		}
		wantReason := fmt.Sprintf("racer-%d", winner)
		if got, _ := after["close_reason"].(string); got != wantReason {
			t.Errorf("close race: close_reason = %q, want %q (racer %d's value)", got, wantReason, winner)
		}
	})

	t.Run("assign", func(t *testing.T) {
		out, err := ssExec(ctx, bdBinary, projectDir, baseEnv, "create", "Shared-server assign race", "--json")
		if err != nil {
			t.Fatalf("create: %s: %v", out, err)
		}
		id, err := ssJSONField(out, "id")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		_, rev0 := ssShowRevision(t, id)

		codes, outputs := race(t, func(i int) []string {
			return []string{"assign", id, fmt.Sprintf("racer%d", i), "--if-revision", rev0}
		})
		winner := assertSingleWinner(t, codes, outputs)

		after, rev1 := ssShowRevision(t, id)
		if rev1 == rev0 {
			t.Errorf("assign race: revision did not advance past %s", rev0)
		}
		wantAssignee := fmt.Sprintf("racer%d", winner)
		if got, _ := after["assignee"].(string); got != wantAssignee {
			t.Errorf("assign race: assignee = %q, want %q (racer %d's value)", got, wantAssignee, winner)
		}
	})
}
