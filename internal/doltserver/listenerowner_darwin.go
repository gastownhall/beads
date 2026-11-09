//go:build darwin

package doltserver

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/steveyegge/beads/internal/configfile"
)

// The lsof reads classifyListener and classifyListenerAt make on darwin;
// tests replace them. Both walk the process tree through processParents.
var (
	listeningPIDs    = lsofListeningPIDs
	listeningSockets = lsofListeningSockets
)

var listenerOwnershipChecked = true

// classifyListener reports whether a TCP listener on port belongs to pid or
// one of its descendants. Darwin has no /proc, so the listener's processes
// come from lsof and the process tree from ps, the same two tools this
// package already uses to find a port's holder and a server's working
// directory. Descendants cover a dolt behind a wrapper script that does not
// exec: the listener's ancestor chain then passes through the wrapper, which
// is the child bd launched.
//
// It is OwnershipUnknown when lsof or ps is missing or fails, and
// OwnershipUnlisted when lsof lists no listener on port at all, although one
// just answered: lsof cannot see another user's sockets without privilege,
// or the connection is forwarded elsewhere.
//
// A child that no longer exists is known not to own anything.
//
// Both reads run only once a greeting has arrived on the port, never on the
// bare readiness poll, and together cost on the order of 100ms.
func classifyListener(pid, port int) Ownership {
	if !processExists(pid) {
		return OwnershipForeign
	}
	holders, err := listeningPIDs(port)
	if err != nil {
		return OwnershipUnknown
	}
	if len(holders) == 0 {
		return OwnershipUnlisted
	}
	parents, err := processParents()
	if err != nil || len(parents) == 0 {
		return OwnershipUnknown
	}
	for _, holder := range holders {
		if descendsFrom(holder, pid, parents) {
			return OwnershipOwned
		}
	}
	return OwnershipForeign
}

// processExists is true while pid names a process this user may signal, or
// one that exists but belongs to someone else.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// lsofListeningPIDs lists the processes holding a TCP listener on port.
// -n and -P skip host and port name lookups, which otherwise dominate the
// run time; -t prints one pid per line. lsof exits 1 with no output when
// nothing matches, which is reported as no listener, not as an error, so the
// caller treats both the same way.
func lsofListeningPIDs(port int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listenerLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output() //nolint:gosec // G702: port is an internal int, not user input
	if err != nil && len(out) == 0 {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseLsofPIDs(out), nil
}

// parseLsofPIDs reads `lsof -t` output: one pid per line.
func parseLsofPIDs(out []byte) []int {
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// classifyListenerAt is classifyListener for one endpoint, as pairing needs
// it: of the TCP listeners on port, those that receive connections dialed to
// host (receivingListeners) must all be held by pid or its descendants
// (classifyHolders). It is OwnershipForeign when any of them is another
// process's, so a child that holds some socket on the port does not make a
// foreign listener on the dialed address its own, and OwnershipUnlisted when
// lsof lists none. It is OwnershipUnknown where classifyListener is, and for
// a host that names no address.
func classifyListenerAt(pid int, host string, port int) Ownership {
	if !processExists(pid) {
		return OwnershipForeign
	}
	want, ok := endpointAddr(host)
	if !ok {
		return OwnershipUnknown
	}
	sockets, err := listeningSockets(port)
	if err != nil {
		return OwnershipUnknown
	}
	addrs := make([]netip.Addr, len(sockets))
	for i, s := range sockets {
		addrs[i] = s.addr
	}
	var holders []int
	for i, recv := range receivingListeners(addrs, want) {
		if recv {
			holders = append(holders, sockets[i].pid)
		}
	}
	return classifyHolders(holders, pid)
}

// lsofListener is one TCP listener lsof lists: its process and local
// address, unmapped.
type lsofListener struct {
	pid  int
	addr netip.Addr
}

// lsofListeningSockets lists the TCP listeners on port with their local
// addresses. Like lsofListeningPIDs, an lsof that exits 1 with no output
// lists none.
func lsofListeningSockets(port int) ([]lsofListener, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listenerLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-F", "ptn", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output() //nolint:gosec // G702: port is an internal int, not user input
	if err != nil && len(out) == 0 {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseLsofListeners(out, port), nil
}

// parseLsofListeners reads `lsof -F ptn` output: a "p<pid>" line starts a
// process, and each of its files has an "f<fd>" line, a "t<IPv4|IPv6>" line
// and an "n<addr>:<port>" line. lsof prints a wildcard address as "*" for
// either family; it becomes that family's unspecified address here. Names on
// another port, and addresses that do not parse, are skipped.
func parseLsofListeners(out []byte, port int) []lsofListener {
	var listeners []lsofListener
	pid, family := 0, ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
			family = ""
		case 'f':
			family = ""
		case 't':
			family = line[1:]
		case 'n':
			i := strings.LastIndexByte(line, ':')
			if pid <= 0 || i < 1 || line[i+1:] != strconv.Itoa(port) {
				continue
			}
			name := line[1:i]
			addr := netip.IPv4Unspecified()
			if family == "IPv6" {
				addr = netip.IPv6Unspecified()
			}
			if name != "*" {
				parsed, err := netip.ParseAddr(configfile.UnbracketHost(name))
				if err != nil {
					continue
				}
				addr = parsed.Unmap()
			}
			listeners = append(listeners, lsofListener{pid: pid, addr: addr})
		}
	}
	return listeners
}
