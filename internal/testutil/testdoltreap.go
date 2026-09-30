//go:build !windows

package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/testcontainers/testcontainers-go"
)

// Stale-container sweep for the Dolt test containers this package starts.
//
// Every container started here carries two labels naming its owner: the test
// binary's pid and the host it runs on. Before the first container of a
// process starts, reapStaleDoltContainers lists the containers labeled for
// this host and removes the ones whose owner pid no longer exists.
//
// Why: in-process cleanup (t.Cleanup, TerminateDoltContainer after m.Run)
// never runs when a test binary is killed, or panics on `go test -timeout`,
// and a box running with TESTCONTAINERS_RYUK_DISABLED=true has no reaper
// either. Each such run stranded one idle dolt-sql-server holding 100-470 MiB
// (23 at once on one loaded box, 2026-09-29). The sweep bounds the pile at
// "runs since the last run" instead of "forever".
//
// Safety under concurrent test processes on one host: a container is removed
// only when kill(pid, 0) reports ESRCH. A live owner, a pid reused by another
// live process, or a pid this user may not signal (EPERM) all keep the
// container. Containers without the owner labels (older harnesses, other
// tools) are never touched, and the host label keeps a remote DOCKER_HOST's
// containers, whose pids mean nothing here, out of the sweep.

const (
	ownerPIDLabel  = "com.gastownhall.beads.testutil.owner-pid"
	ownerHostLabel = "com.gastownhall.beads.testutil.owner-host"
)

var reapStaleOnce sync.Once

// ownerLabels returns the container option that stamps the owner labels.
func ownerLabels() testcontainers.CustomizeRequestOption {
	host, _ := os.Hostname()
	return testcontainers.WithLabels(map[string]string{
		ownerPIDLabel:  strconv.Itoa(os.Getpid()),
		ownerHostLabel: host,
	})
}

// ownedContainer is one `docker ps` row: the container id and the raw value
// of its owner-pid label.
type ownedContainer struct {
	id  string
	pid string
}

// staleContainers returns the ids of the rows whose owner pid is dead
// according to alive. Rows whose pid label is missing, non-numeric or
// non-positive are kept: ownership cannot be established, so nothing is done.
func staleContainers(rows []ownedContainer, alive func(pid int) bool) []string {
	var stale []string
	for _, r := range rows {
		pid, err := strconv.Atoi(strings.TrimSpace(r.pid))
		if err != nil || pid <= 0 {
			continue
		}
		if alive(pid) {
			continue
		}
		stale = append(stale, r.id)
	}
	return stale
}

// pidAlive reports whether a process with the given pid exists. Only ESRCH
// counts as dead; EPERM means the process exists but belongs to someone else.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}

// listOwnedContainers returns every container (running or not) that carries
// this package's owner-host label for host.
func listOwnedContainers(host string) ([]ownedContainer, error) {
	out, err := exec.Command("docker", "ps", "-a", "--no-trunc",
		"--filter", "label="+ownerHostLabel+"="+host,
		"--format", fmt.Sprintf("{{.ID}}\t{{.Label %q}}", ownerPIDLabel),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var rows []ownedContainer
	for _, line := range strings.Split(string(out), "\n") {
		id, pid, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || id == "" {
			continue
		}
		rows = append(rows, ownedContainer{id: id, pid: pid})
	}
	return rows, nil
}

// sweepStaleDoltContainers removes every container on this host whose owner
// process has exited and returns the ids it removed. It is best effort: a
// docker failure is reported on stderr and leaves the containers alone.
func sweepStaleDoltContainers() []string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return nil
	}
	rows, err := listOwnedContainers(host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: stale Dolt container sweep skipped: %v\n", err)
		return nil
	}
	var removed []string
	for _, id := range staleContainers(rows, pidAlive) {
		if err := exec.Command("docker", "rm", "-f", id).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "testutil: removing stale Dolt container %s: %v\n", id, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "testutil: removed stale Dolt test container %s (owner process exited)\n", id[:12])
		removed = append(removed, id)
	}
	return removed
}

// reapStaleDoltContainers runs sweepStaleDoltContainers once per process. It
// is called before the first Dolt container of a process starts, so the cost
// of one `docker ps` is paid once and only by runs that use Docker at all.
func reapStaleDoltContainers() {
	reapStaleOnce.Do(func() { sweepStaleDoltContainers() })
}
