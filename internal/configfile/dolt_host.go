package configfile

import (
	"net/netip"
	"strings"
)

// DoltHostKind classifies a configured Dolt server host by who can run the
// server there. ClassifyDoltHost is the only place a host's spelling is
// interpreted; BdRunsServerAt and IsLocalHostString are derived from it, so
// one address gets one answer however it is written.
type DoltHostKind int

const (
	// DoltHostBdServer is the address bd's own Dolt server binds,
	// 127.0.0.1 (doltserver.DefaultConfig), in any spelling a client dial
	// reaches it by: empty (the default host), 127.0.0.1, ::ffff:127.0.0.1,
	// localhost, or 0.0.0.0. A Linux or macOS dial to 0.0.0.0 connects to
	// 127.0.0.1's listener, and on Windows, FreeBSD, OpenBSD and AIX Go
	// itself dials 127.0.0.1 for it.
	DoltHostBdServer DoltHostKind = iota
	// DoltHostIPv6Loopback is the IPv6 loopback, ::1 in any spelling, or the
	// IPv6 unspecified address ::, which a Linux or macOS dial sends to ::1.
	// It is this machine, but bd's own server, which listens on 127.0.0.1
	// only, never answers at ::1. On Windows, FreeBSD, OpenBSD and AIX Go
	// dials 127.0.0.1 for ::, which can reach bd's server; :: is classed
	// here on every platform all the same, so a host gets one answer
	// wherever bd runs.
	DoltHostIPv6Loopback
	// DoltHostOtherLoopback is an IPv4 loopback address other than
	// 127.0.0.1, such as 127.0.0.2. It is on this machine, but bd's own
	// server does not listen there.
	DoltHostOtherLoopback
	// DoltHostRemote is any other host: an address or name of another
	// machine, an IPv6 address with a zone, or a name bd cannot resolve
	// without a lookup.
	DoltHostRemote
)

// ClassifyDoltHost classifies host. It ignores case, surrounding whitespace
// and one pair of enclosing brackets (UnbracketHost), and reads an
// IPv4-mapped IPv6 address as the IPv4 address it maps.
func ClassifyDoltHost(host string) DoltHostKind {
	h := strings.ToLower(UnbracketHost(host))
	if h == "" || h == "localhost" {
		return DoltHostBdServer
	}
	addr, err := netip.ParseAddr(h)
	if err != nil || addr.Zone() != "" {
		return DoltHostRemote
	}
	addr = addr.Unmap()
	switch {
	case addr.Is4() && (addr == netip.AddrFrom4([4]byte{127, 0, 0, 1}) || addr.IsUnspecified()):
		return DoltHostBdServer
	case addr.Is4() && addr.IsLoopback():
		return DoltHostOtherLoopback
	case addr.Is6() && (addr.IsLoopback() || addr.IsUnspecified()):
		return DoltHostIPv6Loopback
	}
	return DoltHostRemote
}

// BdRunsServerAt reports whether the Dolt server at host can be bd's own:
// only then may bd treat it as its own. That covers starting, stopping or
// auto-starting it; recording or trusting a port file for it; counting the
// workspace's local Dolt root (.beads/dolt or the shared root) as that
// server's data, whether to size it, gc it, read its remotes, gate it, clone
// into it, re-root a dolt-mode migration at it, or point bd's server at it;
// and exempting it from the shared-database consent gate.
//
// It is true only for DoltHostBdServer. bd's own server binds 127.0.0.1
// only, so a server at the IPv6 loopback, at another loopback address or on
// another machine is never bd's, even on this machine. A caller decides
// whether bd runs a server there through this predicate alone; it does not
// combine IsLocalHostString with its own exceptions.
func BdRunsServerAt(host string) bool {
	return ClassifyDoltHost(host) == DoltHostBdServer
}

// SameServerHost reports whether hosts a and b name the same server as
// configured: equal ignoring case, after UnbracketHost, so "[::1]" and
// "::1" are one server. It compares spellings, not addresses: "localhost"
// and "127.0.0.1" are different hosts here.
func SameServerHost(a, b string) bool {
	return strings.EqualFold(UnbracketHost(a), UnbracketHost(b))
}

// IsLocalHostString reports whether host is a standard loopback name of this
// machine: bd's own address in any spelling, or the IPv6 loopback. It
// answers the machine-locality question (GH#3545 mode inference, auto-backup
// file sharing, the HTTP plaintext guard). It is NOT an ownership test: a
// server at the IPv6 loopback is on this machine but never bd's. A decision
// about whether bd runs, starts or owns the data of a server uses
// BdRunsServerAt.
func IsLocalHostString(host string) bool {
	switch ClassifyDoltHost(host) {
	case DoltHostBdServer, DoltHostIPv6Loopback:
		return true
	}
	return false
}
