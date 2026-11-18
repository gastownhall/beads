//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedSyncCloseRaceHalts reproduces GH#7783 end to end: replica A
// closes a bead and syncs; replica B, which never pulled the close, claims the
// same bead and then syncs. The claim's edit and the remote's close contest
// the atomic close group with exactly one closed side — a close race. Before
// the fix, the pull's field-level automerge settled it last-write-wins and
// pushed: whichever side happened to be newer silently undid the other's
// lifecycle intent (the reopened bead was then pushed, so even machines that
// had already seen the close lost it).
//
// The claim's updated_at is pinned ten years into the future before the
// sync, so on the pre-fix behavior LWW deterministically takes the claim —
// the halt the fix produces is therefore proven to be the close-race
// decline, not a timestamp coincidence. The assertions pin all three legs:
// the halt itself (exit 2, nothing pushed), the diagnostic naming the issue
// and both statuses, and the remote keeping the close.
func TestEmbeddedSyncCloseRaceHalts(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	remoteDir := t.TempDir()
	remoteURL := "file://" + remoteDir

	// Replica A owns the bead and publishes it.
	dirA, _, _ := bdInit(t, bd, "--prefix", "cr", "--skip-hooks", "--skip-agents")
	bdDolt(t, bd, dirA, "remote", "add", "origin", remoteURL)
	issue := bdCreate(t, bd, dirA, "Close race bead", "--type", "task")
	bdDolt(t, bd, dirA, "commit", "-m", "create the close-race bead")
	bdDolt(t, bd, dirA, "push")

	// Replica B clones the remote BEFORE the close exists, so its next merge
	// base predates it.
	dirB, _, _ := bdInit(t, bd, "--prefix", "cr", "--remote", remoteURL, "--skip-hooks", "--skip-agents")
	if got := bdShow(t, bd, dirB, issue.ID); got.Status != types.StatusOpen {
		t.Fatalf("clone does not see the open bead (status %s) — clone step is broken", got.Status)
	}

	// A closes the bead and syncs the close to the remote.
	bdClose(t, bd, dirA, issue.ID, "--reason", "done on A")
	if out, err := bdRunWithFlockRetry(t, bd, dirA, "sync"); err != nil {
		t.Fatalf("replica A: bd sync after close failed: %v\n%s", err, out)
	}

	// B claims the bead WITHOUT pulling the close, commits the claim, and
	// pins its updated_at far past A's close, so last-write-wins would
	// deterministically keep the claim if the race were settled that way.
	bdUpdate(t, bd, dirB, issue.ID, "--claim", "--actor", "claimant")
	bdDolt(t, bd, dirB, "commit", "-m", "claim the bead without pulling")
	beadsDirB := filepath.Join(dirB, ".beads")
	cfgB, _ := configfile.Load(beadsDirB)
	databaseB := ""
	if cfgB != nil {
		databaseB = cfgB.GetDoltDatabase()
	}
	dbB, cleanupB, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDirB, "embeddeddolt"), databaseB, "main")
	if err != nil {
		t.Fatalf("OpenSQL on replica B: %v", err)
	}
	future := time.Now().UTC().AddDate(10, 0, 0).Truncate(time.Second)
	if _, err := dbB.ExecContext(t.Context(),
		"UPDATE issues SET updated_at = ? WHERE id = ?", future, issue.ID); err != nil {
		t.Fatalf("pin claim updated_at on replica B: %v", err)
	}
	if _, err := dbB.ExecContext(t.Context(),
		"CALL DOLT_COMMIT('-Am', 'pin claim timestamp ten years ahead')"); err != nil {
		t.Fatalf("commit pinned claim on replica B: %v", err)
	}
	// The bd subprocess needs the embedded store's lock, so this SQL
	// connection must close before the sync runs.
	if err := cleanupB(); err != nil {
		t.Fatalf("cleanup replica B SQL: %v", err)
	}

	// B syncs: the pull hits the close race and must halt with exit 2,
	// pushing nothing, and name the issue and both statuses.
	syncCmd := exec.Command(bd, "sync")
	syncCmd.Dir = dirB
	syncCmd.Env = bdEnv(dirB)
	var syncOut, syncErr bytes.Buffer
	syncCmd.Stdout = &syncOut
	syncCmd.Stderr = &syncErr
	runErr := syncCmd.Run()
	combined := syncOut.String() + syncErr.String()
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			t.Fatalf("bd sync on replica B failed to run: %v\n%s", runErr, combined)
		}
		exitCode = ee.ExitCode()
	}
	if exitCode != 2 {
		t.Fatalf("bd sync on replica B exited %d, want 2 (merge-conflict halt)\n%s", exitCode, combined)
	}
	for _, want := range []string{
		"sync halted, nothing pushed",
		"conflicted table: issues",
		"not auto-merging issue " + issue.ID,
		`"in_progress"`,
		`"closed"`,
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("bd sync halt output missing %q:\n%s", want, combined)
		}
	}

	// A third clone of the remote proves nothing was pushed past the halt:
	// the remote still carries A's close, status and reason both.
	dirC, _, _ := bdInit(t, bd, "--prefix", "cr", "--remote", remoteURL, "--skip-hooks", "--skip-agents")
	remote := bdShow(t, bd, dirC, issue.ID)
	if remote.Status != types.StatusClosed {
		t.Errorf("remote issue status after the halted sync = %s, want closed", remote.Status)
	}
	if remote.CloseReason != "done on A" {
		t.Errorf("remote issue close_reason after the halted sync = %q, want %q", remote.CloseReason, "done on A")
	}
	if remote.ClosedAt == nil {
		t.Error("remote issue closed_at after the halted sync is nil, want the close timestamp")
	}
}
