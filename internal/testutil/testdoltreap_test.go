//go:build !windows

package testutil

import (
	"context"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dolt"
)

// testPIDNS stands in for this process's PID namespace in the pure tests.
const testPIDNS = "pid:[4026531836]"

func TestStaleContainers_OnlyDeadOwnersAreStale(t *testing.T) {
	alive := func(pid int) bool { return pid == 100 || pid == 300 }
	rows := []ownedContainer{
		{id: "live-a", pid: "100", pidNS: testPIDNS},
		{id: "dead-b", pid: "200", pidNS: testPIDNS},
		{id: "live-c", pid: " 300 ", pidNS: testPIDNS},
		{id: "dead-d", pid: "400", pidNS: testPIDNS},
		{id: "no-label", pid: "", pidNS: testPIDNS},
		{id: "garbage", pid: "abc", pidNS: testPIDNS},
		{id: "zero", pid: "0", pidNS: testPIDNS},
		{id: "negative", pid: "-7", pidNS: testPIDNS},
	}
	got := staleContainers(rows, testPIDNS, alive)
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
	rows := []ownedContainer{
		{id: "x", pid: "", pidNS: testPIDNS},
		{id: "y", pid: "nope", pidNS: testPIDNS},
		{id: "z", pid: "0", pidNS: testPIDNS},
	}
	if got := staleContainers(rows, testPIDNS, alive); got != nil {
		t.Fatalf("staleContainers = %v, want nil", got)
	}
}

// A pid names a process only inside its PID namespace: checked from another
// one, a live owner reads as ESRCH. Rows owned from another namespace must
// never reach alive.
func TestStaleContainers_SkipsOtherPIDNamespaces(t *testing.T) {
	alive := func(pid int) bool {
		t.Fatalf("alive called for pid %d owned from another PID namespace", pid)
		return true
	}
	rows := []ownedContainer{
		{id: "sibling-sandbox", pid: "200", pidNS: "pid:[4026546206]"},
		{id: "no-pidns-label", pid: "200", pidNS: ""},
	}
	if got := staleContainers(rows, testPIDNS, alive); got != nil {
		t.Fatalf("staleContainers = %v, want nil", got)
	}
}

func TestParseOwnedRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want []ownedContainer
	}{
		{name: "no containers", out: "", want: nil},
		{
			name: "trailing newline",
			out:  "aaa\t12\tpid:[7]\nbbb\t34\tpid:[7]\n",
			want: []ownedContainer{{id: "aaa", pid: "12", pidNS: "pid:[7]"}, {id: "bbb", pid: "34", pidNS: "pid:[7]"}},
		},
		{name: "CRLF", out: "aaa\t12\tpid:[7]\r\n", want: []ownedContainer{{id: "aaa", pid: "12", pidNS: "pid:[7]"}}},
		// A missing label prints as an empty field. An empty pidns is what
		// every owner outside Linux stamps, so the row must survive parsing.
		{name: "empty pidns label", out: "aaa\t12\t\n", want: []ownedContainer{{id: "aaa", pid: "12"}}},
		{name: "empty labels", out: "aaa\t\t\n", want: []ownedContainer{{id: "aaa"}}},
		{name: "malformed lines", out: "aaa\t12\nbbb\n\t12\tpid:[7]\n", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseOwnedRows(tc.out); !slices.Equal(got, tc.want) {
				t.Errorf("parseOwnedRows(%q) = %+v, want %+v", tc.out, got, tc.want)
			}
		})
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
	ns, err := pidNamespace()
	if err != nil {
		t.Fatalf("pidNamespace: %v", err)
	}
	if got := req.Labels[ownerPIDNSLabel]; got != ns {
		t.Errorf("%s = %q, want %q", ownerPIDNSLabel, got, ns)
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

// TestSweepStaleDoltContainers starts one Dolt container owned by a child
// process and one owned by this process, ends the child, sweeps, and checks
// that only the container whose owner exited and its volume are gone.
func TestSweepStaleDoltContainers(t *testing.T) {
	if hasTestSkip("dolt") {
		t.Skip("skipping: Dolt tests skipped (BEADS_TEST_SKIP=dolt)")
	}
	// The sweep acts on containers, so it needs docker whichever backend
	// BEADS_TEST_DOLT_SERVER selects; under the local one checkDolt only finds
	// the dolt CLI (the dolt-server lane's remote workers have no docker).
	if !useLocalDoltServer() {
		if state := checkDolt(); state != doltReady {
			skipOrFailDoltUnavailable(t, state)
		}
	} else if !isDockerAvailable() || !isDoltImageCached() {
		t.Skipf("container backend not available on this host (docker + %s)", DoltDockerImage)
	}
	if host, err := os.Hostname(); err != nil || host == "" {
		t.Skipf("hostname unavailable: %v", err)
	}

	start := func(t *testing.T, ownerPID int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
		defer cancel()
		ctr, err := dolt.Run(ctx, DoltDockerImage,
			dolt.WithDatabase("beads_test"),
			testcontainers.WithEnv(map[string]string{"DOLT_ROOT_HOST": "%"}),
			testcontainers.WithLabels(ownerLabelValues(ownerPID)),
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

	// A sibling test binary's sweep (go test ./... runs them in parallel)
	// removes a container as soon as it sees the owner dead, even mid-start.
	// So the dead-owned container's owner is a child process that runs until
	// the container is up and its volumes are read, and only then exits.
	owner := exec.Command("sleep", "600") // outlives both starts
	if err := owner.Start(); err != nil {
		t.Fatalf("starting the owner process: %v", err)
	}
	endOwner := sync.OnceFunc(func() {
		_ = owner.Process.Kill()
		// Reaped, its pid reports ESRCH; a zombie would still read as alive.
		_ = owner.Wait()
	})
	t.Cleanup(endOwner)

	deadOwned := start(t, owner.Process.Pid)
	liveOwned := start(t, os.Getpid())
	deadVolumes := containerVolumes(t, deadOwned)
	if len(deadVolumes) == 0 {
		t.Fatalf("dead-owned container %s has no volume; %s no longer declares VOLUME /var/lib/dolt?", deadOwned, DoltDockerImage)
	}
	endOwner()

	removed := sweepStaleDoltContainers()

	// Once its owner has exited, a sibling's sweep may remove the dead-owned
	// container first, or still be removing it when ours fails to, so only
	// its end state is checked, and given time to settle.
	if slices.Contains(removed, liveOwned) {
		t.Errorf("sweep removed the live-owned container %s", liveOwned)
	}
	if !dockerObjectGone("inspect", deadOwned) {
		t.Errorf("dead-owned container %s still exists after the sweep", deadOwned)
	}
	for _, v := range deadVolumes {
		if !dockerObjectGone("volume", "inspect", v) {
			t.Errorf("volume %s of the dead-owned container survived the sweep", v)
		}
	}
	if err := exec.Command("docker", "inspect", liveOwned).Run(); err != nil {
		t.Errorf("live-owned container %s missing after the sweep: %v", liveOwned, err)
	}
}

// dockerObjectGone reports whether `docker args...`, an inspect of one object,
// starts failing within sweepTimeout, which also bounds a sibling's removal.
func dockerObjectGone(args ...string) bool {
	deadline := time.Now().Add(sweepTimeout)
	for exec.Command("docker", args...).Run() == nil {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

// containerVolumes returns the names of the volumes mounted into container id.
func containerVolumes(t *testing.T, id string) []string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format",
		`{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}`, id).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", id, err)
	}
	return strings.Fields(string(out))
}
