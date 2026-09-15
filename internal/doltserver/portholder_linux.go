//go:build linux

package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const portHolderSource = "proc"

// resolvePortHolderInDir joins the listening sockets in /proc/net/tcp{,6}
// against each process's open descriptors in /proc/<pid>/fd.
//
// This deliberately does NOT reuse this package's findPIDOnPort on Linux, which
// shells out to lsof: an ownership transfer spawns nothing but dolt, and lsof
// being absent or denied would otherwise look identical to a free port. There
// is no lsof fallback here — an unreadable /proc is PortHolderUndetermined, and
// the caller records the gate unavailable rather than passing it.
//
// procfs also gives a binding lsof cannot: a held descriptor under the data
// dir, which survives a chdir, where lsof's cwd view does not.
func resolvePortHolderInDir(port int, dir string) (int, string, PortHolderOutcome) {
	inodes, err := listeningInodesOnPort(port)
	if err != nil {
		return 0, "", PortHolderUndetermined
	}
	if len(inodes) == 0 {
		return 0, "", PortHolderNoHolder
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, "", PortHolderUndetermined
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		if !holdsAnyInode(pid, inodes) {
			continue
		}
		if dir == "" {
			return pid, "", PortHolderHeld
		}
		if boundBy, bound := processBinding(pid, dir); bound {
			return pid, boundBy, PortHolderHeld
		}
		// The port holder is real but not this workspace's. Keep looking: a
		// dual-stack server owns one inode per family, and the walk may not
		// have reached the pid that owns the other one yet.
	}
	// A listening inode exists but no process in /proc claims it. That is a
	// socket owned by a process this user cannot see, not an absence.
	if dir == "" {
		return 0, "", PortHolderUndetermined
	}
	return 0, "", PortHolderNoHolder
}

// processBinding reports how pid is bound to dir. A held descriptor beats a
// matching working directory: a process can chdir anywhere, but a Dolt server
// holds its storage open for as long as it serves.
func processBinding(pid int, dir string) (string, bool) {
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		want = filepath.Clean(dir)
	}
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	if entries, readErr := os.ReadDir(fdDir); readErr == nil {
		for _, entry := range entries {
			target, linkErr := os.Readlink(filepath.Join(fdDir, entry.Name()))
			if linkErr != nil {
				continue // closed while iterating
			}
			if isUnderDir(filepath.Clean(target), want) {
				return "fd-lock", true
			}
		}
	}
	cwd, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err == nil && filepath.Clean(cwd) == want {
		return "cwd", true
	}
	return "", false
}

func holdsAnyInode(pid int, inodes map[string]bool) bool {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, linkErr := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if linkErr != nil {
			continue
		}
		rest, ok := strings.CutPrefix(target, "socket:[")
		if ok && inodes[strings.TrimSuffix(rest, "]")] {
			return true
		}
	}
	return false
}

// listeningInodesOnPort returns the socket inodes of every LISTEN row on port,
// across both address families. A server bound to :: and one bound to 127.0.0.1
// are different inodes and either may be the one this port means.
func listeningInodesOnPort(port int) (map[string]bool, error) {
	const tcpListen = "0A"
	inodes := make(map[string]bool)
	read := 0
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table) // #nosec G304 -- fixed procfs paths
		if err != nil {
			if os.IsNotExist(err) {
				continue // tcp6 is absent on kernels built without IPv6
			}
			return nil, fmt.Errorf("read %s: %w", table, err)
		}
		read++
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] { // skip the header row
			fields := strings.Fields(line)
			// sl local_address rem_address st ... inode
			if len(fields) < 10 || fields[3] != tcpListen {
				continue
			}
			rowPort, err := procNetPort(fields[1])
			if err != nil || rowPort != port {
				continue
			}
			inodes[fields[9]] = true
		}
	}
	if read == 0 {
		// Neither table exists: this is not a kernel with the procfs network
		// tables, so nothing was observed, free or otherwise.
		return nil, fmt.Errorf("no procfs tcp table is readable")
	}
	return inodes, nil
}

// procNetPort decodes the port half of a /proc/net/tcp{,6} local_address column
// ("HEXIP:HEXPORT"). Only the port is needed: this lookup asks who holds the
// port, and a holder bound to a wildcard address holds it for loopback too.
func procNetPort(column string) (int, error) {
	_, portHex, ok := strings.Cut(column, ":")
	if !ok {
		return 0, fmt.Errorf("no port separator in %q", column)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return 0, fmt.Errorf("port %q: %w", portHex, err)
	}
	return int(port), nil
}
