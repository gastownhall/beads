//go:build !windows

package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// Stale-container sweep for the Dolt test containers this package starts.
//
// Every container started here carries three labels naming its owner: the
// test binary's pid, the PID namespace that pid belongs to, and the host it
// runs on. Before the first container of a process starts,
// reapStaleDoltContainers lists the containers labeled for this host and
// removes the ones whose owner ran in this process's PID namespace and no
// longer exists.
//
// Why: in-process cleanup (t.Cleanup, TerminateDoltContainer after m.Run)
// never runs when a test binary is killed, or panics on `go test -timeout`,
// and a box running with TESTCONTAINERS_RYUK_DISABLED=true has no reaper
// either. Each such run stranded one idle dolt-sql-server holding 100-470 MiB
// (23 at once on one loaded box, 2026-09-29). The sweep bounds the pile at
// "runs since the last run" instead of "forever".
//
// Safety under concurrent test processes sharing one Docker daemon: a
// container is removed only when its owner ran in this process's PID
// namespace and kill(pid, 0) reports ESRCH. A live owner, a pid reused by
// another live process, or a pid this user may not signal (EPERM) all keep
// the container. A pid means nothing outside its namespace, and sandboxed
// runners (bwrap --unshare-pid, a container without --pid=host) can share the
// hostname and the daemon while seeing none of each other's processes, so a
// container owned from another namespace is never a candidate: if it leaked,
// only a sweep from its own namespace removes it. The namespace label is the
// /proc/self/ns/pid link on Linux, unique among one machine's live
// namespaces, and "" elsewhere; a new namespace that reuses a dead one's
// inode only sees containers whose owners have all exited. Containers without
// the owner labels (older harnesses, other tools) are never touched, and the
// host label keeps a remote DOCKER_HOST's containers, whose pids mean nothing
// here, out of the sweep.

const (
	ownerPIDLabel   = "com.gastownhall.beads.testutil.owner-pid"
	ownerPIDNSLabel = "com.gastownhall.beads.testutil.owner-pidns"
	ownerHostLabel  = "com.gastownhall.beads.testutil.owner-host"
)

// sweepTimeout bounds the whole sweep, so a wedged daemon or container delays
// the first container start of a process instead of hanging it.
const sweepTimeout = 30 * time.Second

var reapStaleOnce sync.Once

// ownerLabels returns the container option that stamps the owner labels.
func ownerLabels() testcontainers.CustomizeRequestOption {
	return testcontainers.WithLabels(ownerLabelValues(os.Getpid()))
}

// ownerLabelValues returns the owner labels for a container owned by pid, a
// process in this process's PID namespace on this host. A hostname, or a Linux
// PID namespace, that cannot be read is stamped as "", which no sweep on this
// host matches: the container leaks rather than risk removal while pid lives.
func ownerLabelValues(pid int) map[string]string {
	host, _ := os.Hostname()
	ns, _ := pidNamespace()
	return map[string]string{
		ownerPIDLabel:   strconv.Itoa(pid),
		ownerPIDNSLabel: ns,
		ownerHostLabel:  host,
	}
}

// pidNamespace identifies the PID namespace that os.Getpid and kill(2)
// resolve pids in: the /proc/self/ns/pid link target on Linux, such as
// "pid:[4026531836]", and "" elsewhere.
func pidNamespace() (string, error) {
	if runtime.GOOS != "linux" {
		return "", nil
	}
	return os.Readlink("/proc/self/ns/pid")
}

// ownedContainer is one `docker ps` row: the container id and the raw values
// of its owner-pid and owner-pidns labels.
type ownedContainer struct {
	id    string
	pid   string
	pidNS string
}

// staleContainers returns the ids of the rows owned from PID namespace ns
// whose owner pid is dead according to alive. Rows owned from another
// namespace, and rows whose pid label is missing, non-numeric or
// non-positive, are kept: their owner cannot be checked from here, so nothing
// is done.
func staleContainers(rows []ownedContainer, ns string, alive func(pid int) bool) []string {
	var stale []string
	for _, r := range rows {
		if r.pidNS != ns {
			continue
		}
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

// dockerCommand returns a docker CLI command bound to ctx. WaitDelay lets it
// return once ctx is done even if a process the docker binary started, such
// as a wrapper script's child, still holds its output pipe open.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = time.Second
	return cmd
}

// listOwnedContainers returns every container (running or not) that carries
// this package's owner-host label for host.
func listOwnedContainers(ctx context.Context, host string) ([]ownedContainer, error) {
	out, err := dockerCommand(ctx, "ps", "-a", "--no-trunc",
		"--filter", "label="+ownerHostLabel+"="+host,
		"--format", fmt.Sprintf("{{.ID}}\t{{.Label %q}}\t{{.Label %q}}", ownerPIDLabel, ownerPIDNSLabel),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	return parseOwnedRows(string(out)), nil
}

// parseOwnedRows parses listOwnedContainers' `docker ps` output: one
// "id<TAB>pid<TAB>pidns" line per container, where a missing label prints as
// an empty field. Lines of any other shape are dropped.
func parseOwnedRows(out string) []ownedContainer {
	var rows []ownedContainer
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(f) != 3 || f[0] == "" {
			continue
		}
		rows = append(rows, ownedContainer{id: f[0], pid: f[1], pidNS: f[2]})
	}
	return rows
}

// sweepStaleDoltContainers removes every container on this host whose owner
// ran in this PID namespace and has exited, and returns the ids it removed.
// It is best effort: a docker failure is reported on stderr and leaves the
// containers alone.
func sweepStaleDoltContainers() []string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return nil
	}
	ns, err := pidNamespace()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: stale Dolt container sweep skipped: %v\n", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()
	rows, err := listOwnedContainers(ctx, host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: stale Dolt container sweep skipped: %v\n", err)
		return nil
	}
	var removed []string
	for _, id := range staleContainers(rows, ns, pidAlive) {
		// -v also removes the anonymous volume behind the image's
		// VOLUME /var/lib/dolt, as testcontainers' own teardown does.
		if err := dockerCommand(ctx, "rm", "-f", "-v", id).Run(); err != nil {
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
