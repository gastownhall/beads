package main

import (
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// resolveProxiedTopology reports which proxied-server SHAPE a workspace is, for
// the capability rows whose answer depends on it.
//
// The shapes are ordered by how much of the store bd owns, and the first one
// that matches wins:
//
//	team-server     the database belongs to beads-team-server, which owns its
//	                schema and identity. Ownership beats transport — a
//	                bts-managed database is somebody else's store even when the
//	                dolt process happens to be local.
//	external-*      an operator's Dolt server that bd only connects to. The
//	                sidecar's External block is what records it, and the socket
//	                field is what separates the two transports.
//	managed-local   no External block: bd spawned this dolt sql-server itself,
//	                under this user, on this filesystem.
//
// A workspace bd cannot read is reported as unknown rather than guessed at.
// Every topology-keyed row treats unknown as refused, so a corrupt sidecar
// costs a capability instead of buying one; the provider open that follows
// fails on the same file with a much better message than this function could
// produce, so this is a policy decision, not the error path.
func resolveProxiedTopology(beadsDir string) ProxyTopology {
	if beadsDir == "" {
		beadsDir = beads.FindBeadsDir()
	}
	if beadsDir == "" {
		return ProxyTopologyUnknown
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return ProxyTopologyUnknown
	}
	if cfg != nil && cfg.IsTeamServerManaged() {
		return ProxyTopologyTeamServer
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return ProxyTopologyUnknown
	}
	if info == nil || info.External == nil {
		return ProxyTopologyManagedLocal
	}
	if info.External.Socket != "" {
		return ProxyTopologyExternalUnix
	}
	return ProxyTopologyExternalTCP
}
