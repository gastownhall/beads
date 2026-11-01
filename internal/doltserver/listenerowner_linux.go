//go:build linux

package doltserver

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is where classifyListener reads /proc; tests point it at a fake
// tree.
var procRoot = "/proc"

// classifyListener reports whether a TCP listener on port belongs to pid or
// one of its descendants. Descendants cover a dolt behind a wrapper script
// that does not exec; finding them needs task/*/children
// (CONFIG_PROC_CHILDREN), and without it such a wrapper's dolt is not seen as
// the child's.
//
// It is OwnershipUnknown when /proc cannot answer:
//   - /proc is not this process's PID namespace's (/proc/self is not us);
//   - the child's descriptors cannot be read (permissions, hardening);
//   - /proc/net/tcp cannot be read.
//
// It is OwnershipUnlisted when no listener on port is listed at all,
// although one just answered: /proc/net/tcp is incomplete here (WSL1, some
// sandboxes), or the listener is outside this network namespace and reached
// through a forward (WSL2's mirrored networking shares the port space with
// Windows).
//
// A child that no longer exists is known not to own anything.
func classifyListener(pid, port int) Ownership {
	if procRoot == "/proc" {
		if self, err := os.Readlink("/proc/self"); err != nil || self != strconv.Itoa(os.Getpid()) {
			return OwnershipUnknown
		}
	}
	if _, err := os.ReadDir(filepath.Join(procRoot, strconv.Itoa(pid), "fd")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return OwnershipForeign
		}
		return OwnershipUnknown
	}
	inodes, err := listeningSocketInodes(port)
	if err != nil {
		return OwnershipUnknown
	}
	if len(inodes) == 0 {
		return OwnershipUnlisted
	}
	seen := map[int]bool{}
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		if processHoldsSocket(p, inodes) {
			return OwnershipOwned
		}
		queue = append(queue, childPIDs(p)...)
	}
	return OwnershipForeign
}

// listeningSocketInodes returns the socket inodes of TCP listeners on port,
// from /proc/net/tcp and /proc/net/tcp6.
func listeningSocketInodes(port int) (map[string]bool, error) {
	inodes := map[string]bool{}
	read := 0
	for _, name := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, "net", name)) //nolint:gosec // G304: fixed /proc paths
		if err != nil {
			continue
		}
		read++
		err = parseProcNetTCPListeners(bufio.NewScanner(f), port, inodes)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if read == 0 {
		return nil, fmt.Errorf("no %s/net/tcp", procRoot)
	}
	return inodes, nil
}

// tcpListenState is st for TCP_LISTEN in /proc/net/tcp.
const tcpListenState = "0A"

// parseProcNetTCPListeners adds to inodes the inode of every row of a
// /proc/net/tcp{,6} table that is a listener on port. A row is
// "sl local_address rem_address st ... uid timeout inode ...", with
// local_address "HEXADDR:HEXPORT".
func parseProcNetTCPListeners(sc *bufio.Scanner, port int, inodes map[string]bool) error {
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != tcpListenState {
			continue
		}
		i := strings.LastIndexByte(fields[1], ':')
		if i < 0 {
			continue
		}
		p, err := strconv.ParseUint(fields[1][i+1:], 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		if fields[9] != "0" {
			inodes[fields[9]] = true
		}
	}
	return sc.Err()
}

func processHoldsSocket(pid int, inodes map[string]bool) bool {
	dir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil || !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
			continue
		}
		if inodes[link[len("socket:["):len(link)-1]] {
			return true
		}
	}
	return false
}

func childPIDs(pid int) []int {
	tasks, _ := filepath.Glob(filepath.Join(procRoot, strconv.Itoa(pid), "task", "*", "children"))
	var out []int
	for _, t := range tasks {
		b, err := os.ReadFile(t) //nolint:gosec // G304: path is built from /proc and a pid
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			if c, err := strconv.Atoi(f); err == nil {
				out = append(out, c)
			}
		}
	}
	return out
}
