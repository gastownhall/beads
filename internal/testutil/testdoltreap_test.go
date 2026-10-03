//go:build !windows

package testutil

import (
	"context"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dolt"
)

func TestStaleContainers_OnlyDeadOwnersAreStale(t *testing.T) {
	alive := func(pid int) bool { return pid == 100 || pid == 300 }
	rows := []ownedContainer{
		{id: "live-a", pid: "100"},
		{id: "dead-b", pid: "200"},
		{id: "live-c", pid: " 300 "},
		{id: "dead-d", pid: "400"},
		{id: "no-label", pid: ""},
		{id: "garbage", pid: "abc"},
		{id: "zero", pid: "0"},
		{id: "negative", pid: "-7"},
	}
	got := staleContainers(rows, alive)
	want := []string{"dead-b", "dead-d"}
	if !slices.Equal(got, want) {
		t.Fatalf("staleContainers = %v, want %v", got, want)
	}
}

func TestStaleContainers_NeverCallsAliveForUnownedRows(t *testing.T) {
	alive := func(pid int) bool {
		t.Fatalf("alive called for pid %d; rows without a valid owner pid must be skipped", pid)
		return true
	}
	rows := []ownedContainer{{id: "x", pid: ""}, {id: "y", pid: "nope"}, {id: "z", pid: "0"}}
	if got := staleContainers(rows, alive); got != nil {
		t.Fatalf("staleContainers = %v, want nil", got)
	}
}

func TestOwnerLabels_StampThisProcess(t *testing.T) {
	var req testcontainers.GenericContainerRequest
	if err := ownerLabels().Customize(&req); err != nil {
		t.Fatalf("ownerLabels: %v", err)
	}
	if got, want := req.Labels[ownerPIDLabel], strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("%s = %q, want %q", ownerPIDLabel, got, want)
	}
	host, _ := os.Hostname()
	if got := req.Labels[ownerHostLabel]; got != host {
		t.Errorf("%s = %q, want %q", ownerHostLabel, got, host)
	}
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("pidAlive(self) = false")
	}
	// No process can have a pid this large on Linux (pid_max <= 2^22) or
	// macOS (PID_MAX 99998): kill(2) reports ESRCH.
	if pidAlive(math.MaxInt32) {
		t.Fatalf("pidAlive(%d) = true, want false", math.MaxInt32)
	}
}

// TestSweepStaleDoltContainers starts one Dolt container labelled with a dead
// owner pid and one labelled with this process, sweeps, and checks that only
// the dead-owned container is gone.
func TestSweepStaleDoltContainers(t *testing.T) {
	if state := checkDolt(); state != doltReady {
		skipOrFailDoltUnavailable(t, state)
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skipf("hostname unavailable: %v", err)
	}

	start := func(t *testing.T, ownerPID int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
		defer cancel()
		ctr, err := dolt.Run(ctx, DoltDockerImage,
			dolt.WithDatabase("beads_test"),
			testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
			testcontainers.WithLabels(map[string]string{
				ownerPIDLabel:  strconv.Itoa(ownerPID),
				ownerHostLabel: host,
			}),
		)
		if err != nil {
			t.Fatalf("starting Dolt container: %v", err)
		}
		t.Cleanup(func() {
			// Best effort: the dead-owned container is expected to be gone.
			_ = testcontainers.TerminateContainer(ctr)
		})
		return ctr.GetContainerID()
	}

	deadOwned := start(t, math.MaxInt32)
	liveOwned := start(t, os.Getpid())

	removed := sweepStaleDoltContainers()

	if !slices.Contains(removed, deadOwned) {
		t.Errorf("sweep removed %v; want it to include the dead-owned container %s", removed, deadOwned)
	}
	if slices.Contains(removed, liveOwned) {
		t.Errorf("sweep removed the live-owned container %s", liveOwned)
	}
	if exec.Command("docker", "inspect", deadOwned).Run() == nil {
		t.Errorf("dead-owned container %s still exists after the sweep", deadOwned)
	}
	if err := exec.Command("docker", "inspect", liveOwned).Run(); err != nil {
		t.Errorf("live-owned container %s missing after the sweep: %v", liveOwned, err)
	}
}
