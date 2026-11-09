package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
)

// Server identity verification, store side (GH#4637): a managed local
// endpoint's answering server must be the one bd recorded for the workspace's
// data root before any discovery, creation or project statement runs through
// the open's pools. The record, the gate and the pairing live in
// internal/doltserver (identity.go); this file decides WHICH opens are gated
// and threads the expectation through openServerConnection.

// verifyServerIdentity is the gate openServerConnection runs, as a package
// var so tests can observe (and prove the absence of) gate operations on
// exempt endpoints.
var verifyServerIdentity = doltserver.VerifyServerIdentity

// serverIdentityExpectation resolves whether this open must verify the
// answering server's identity, and against which data root. A nil return
// exempts the endpoint: no record read, no identity query — the open behaves
// exactly as before (AC-6).
//
// Eligibility follows the approved design: managed shared/owned local TCP
// endpoints are gated, including environment-specified shared ports. Exempt:
// gateways (credential commands), live sockets, TLS, hosts bd never runs a
// server at, caller-explicit ports (an operator's own assertion, e.g.
// `bd init --server-port`), and a workspace whose declared server mode is
// external outside shared mode (bd must keep working with servers it is told
// about but never manages, GH#3516). Explicit disablement
// (dolt.verify-server-identity / BEADS_DOLT_VERIFY_SERVER_IDENTITY) exempts
// every endpoint.
//
// resolvedBeadsDir is the .beads directory the credentials and server state
// use — the same one newServerMode resolves — so a Path-only open (beads.Open)
// is gated against the workspace it will actually serve.
func serverIdentityExpectation(cfg *Config, resolvedBeadsDir string) *doltserver.ServerIdentityExpectation {
	if doltserver.VerifyServerIdentityDisabled(resolvedBeadsDir) {
		return nil
	}
	switch {
	case cfg.ServerSocket != "":
		// A live unix socket: exempt, like the gateway. (A configured socket
		// that fell back to TCP was already normalized to "" by
		// ResolveSocketTransport, so the TCP dial below IS gated.)
		return nil
	case cfg.ServerTLS:
		// TLS endpoints are external servers by construction.
		return nil
	case cfg.Gateway:
		// A credential command owns routing; the gateway reconciliation in
		// cmd/bd/init.go is the identity story for hosted servers.
		return nil
	case cfg.ServerPort <= 0:
		return nil
	case !doltserver.HostRunsBDServer(cfg.ServerHost):
		// A host bd's server never listens on: another machine, the IPv6
		// loopback, another loopback address. Not bd's to verify.
		return nil
	case cfg.ServerPortSharedServer:
		// The shared server is managed whatever source resolved the port —
		// including an environment-specified shared port, which is the
		// WSL2/mirrored-networking case the gate exists for.
		return doltserver.SharedServerIdentityExpectation(resolvedBeadsDir, cfg.ServerHost, cfg.ServerPort)
	case cfg.ServerPortSource == doltserver.PortSourceCallerExplicit:
		// The caller explicitly asserted the port; that assertion is trusted
		// as an endpoint decision, as the port-retarget guard already does.
		return nil
	case doltserver.ResolveServerMode(resolvedBeadsDir) == doltserver.ServerModeExternal:
		// A workspace that declares its server externally managed (outside
		// shared mode): bd never started it and must keep connecting to it.
		return nil
	default:
		// bd's own repo-local server on a port the port chain resolved
		// (auto-start's port file, the shared default, config.yaml) —
		// exactly the launches Start paired.
		return doltserver.WorkspaceIdentityExpectation(resolvedBeadsDir, cfg.ServerHost, cfg.ServerPort)
	}
}

// verifyOpenServerIdentity runs the gate for one pool. expect nil is exempt:
// no record or identity-query operation at all.
func verifyOpenServerIdentity(ctx context.Context, q doltversion.RowQuerier, expect *doltserver.ServerIdentityExpectation) error {
	if expect == nil {
		return nil
	}
	return verifyServerIdentity(ctx, q, *expect)
}
