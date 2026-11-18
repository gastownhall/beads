//go:build linux

package doltserver

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is where classifyListener reads /proc; tests point it at a fake
// tree.
var procRoot = "/proc"

var listenerOwnershipChecked = true

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

// classifyListenerAt is classifyListener for one endpoint, as pairing needs
// it: of the TCP listeners on port, those that receive connections dialed to
// host (receivingListeners) must all be held by pid or its descendants. It
// is OwnershipForeign when any of them is another process's, so a child that
// holds some socket on the port does not make a foreign listener on the
// dialed address its own, and OwnershipUnlisted when none is listed. It is
// OwnershipUnknown where classifyListener is, and for a host that names no
// address.
func classifyListenerAt(pid int, host string, port int) Ownership {
	want, ok := endpointAddr(host)
	if !ok {
		return OwnershipUnknown
	}
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
	listeners, err := listeningSocketAddrs(port)
	if err != nil {
		return OwnershipUnknown
	}
	addrs := make([]netip.Addr, len(listeners))
	for i, l := range listeners {
		addrs[i] = l.addr
	}
	matching := map[string]bool{}
	for i, recv := range receivingListeners(addrs, want) {
		if recv {
			matching[listeners[i].inode] = true
		}
	}
	if len(matching) == 0 {
		return OwnershipUnlisted
	}
	held := map[string]bool{}
	seen := map[int]bool{}
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		for inode := range matching {
			if !held[inode] && processHoldsSocket(p, map[string]bool{inode: true}) {
				held[inode] = true
			}
		}
		queue = append(queue, childPIDs(p)...)
	}
	for inode := range matching {
		if !held[inode] {
			return OwnershipForeign
		}
	}
	return OwnershipOwned
}

// procSocket is one TCP listener in /proc/net/tcp{,6}: its socket inode
// and its local address, unmapped.
type procSocket struct {
	inode string
	addr  netip.Addr
}

// listeningSocketAddrs returns the TCP listeners on port, from /proc/net/tcp
// and /proc/net/tcp6.
func listeningSocketAddrs(port int) ([]procSocket, error) {
	var listeners []procSocket
	read := 0
	for _, name := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, "net", name)) //nolint:gosec // G304: fixed /proc paths
		if err != nil {
			continue
		}
		read++
		listeners, err = parseProcNetTCPListenerAddrs(bufio.NewScanner(f), port, listeners)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if read == 0 {
		return nil, fmt.Errorf("no %s/net/tcp", procRoot)
	}
	return listeners, nil
}

// parseProcNetTCPListenerAddrs appends to listeners the inode and local
// address of every row of a /proc/net/tcp{,6} table that is a listener on
// port. A row whose address does not decode is kept with the zero address,
// which receives no endpoint's connections.
func parseProcNetTCPListenerAddrs(sc *bufio.Scanner, port int, listeners []procSocket) ([]procSocket, error) {
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
		if err != nil || int(p) != port || fields[9] == "0" {
			continue
		}
		listeners = append(listeners, procSocket{inode: fields[9], addr: decodeProcNetAddr(fields[1][:i])})
	}
	return listeners, sc.Err()
}

// decodeProcNetAddr decodes a /proc/net/tcp{,6} local address: 8 (IPv4) or
// 32 (IPv6) hex digits, printed as 32-bit words in host byte order, and
// unmaps an IPv4-mapped IPv6 one. It returns the zero address for anything
// else.
func decodeProcNetAddr(h string) netip.Addr {
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != net.IPv4len && len(raw) != net.IPv6len) {
		return netip.Addr{}
	}
	ip := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(ip[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	addr, _ := netip.AddrFromSlice(ip)
	return addr.Unmap()
}
