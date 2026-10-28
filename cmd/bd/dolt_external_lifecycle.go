package main

// `bd dolt start` for workspaces whose Dolt server is externally managed.
//
// A setting such as metadata.json's dolt_server_port declares that someone
// other than bd runs this workspace's server (ResolveServerMode returns
// ServerModeExternal). Auto-start honors that and never starts one, but the
// explicit start used to go on to doltserver.Start, which put a bd-owned
// sql-server over .beads/dolt on the external endpoint's port: a second,
// empty database that answers in place of the real one (GH#6585). This file
// is that start's refusal.

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Stable identity of the `bd dolt start` refusal on an externally managed
// workspace. Like proxyDoltStartConflictCode it is frozen; it has its own
// namespace so a consumer reading proxy.* as a proxied-topology refusal does
// not misclassify this one.
const externalDoltStartConflictCode = "external.dolt_start.conflict"

// externalDoltStartRefusal refuses `bd dolt start` for beadsDir when setting
// (doltserver.ExternallyManagedBy) declares its server externally managed.
// --force does not bypass it: the start it would allow is the one that makes
// the shadow database. Like proxiedDoltStartRefusal it is enforced in the
// command, since `bd dolt` never reaches main.go's capability gate.
func externalDoltStartRefusal(beadsDir, setting string, sole bool) *ProxyCapabilityError {
	dial := doltserver.DialConfig(beadsDir)
	decl := doltserver.ExternalPortSetting(beadsDir, dial.PortSource, dial.Port)
	if decl.Setting == "" {
		decl = doltserver.ExternalPortDeclaration{Setting: setting}
	}
	endpoint := ""
	if dial.Port > 0 {
		endpoint = doltutil.ServerAddr(dial.Host, dial.Port)
	}
	tracked := doltserver.IsTrackedServerRunning(doltserver.ResolveServerDir(beadsDir))
	return externalDoltStartRefusalFor(decl, endpoint, sole, tracked)
}

// externalDoltStartRefusalFor builds the refusal from what
// externalDoltStartRefusal read: decl names the declaring setting, endpoint is
// the address bd dials ("" when no port resolved), sole reports that removing
// the setting lets bd manage the server, and trackedLive that a server bd
// started earlier is still running here. The text never suggests
// `bd dolt start` or --force, which this refusal exists to stop.
func externalDoltStartRefusalFor(decl doltserver.ExternalPortDeclaration, endpoint string, sole, trackedLive bool) *ProxyCapabilityError {
	// The Sole sentence is added below, with the setting that pins a port
	// for bd's own server.
	decl.Sole = false
	var advice string
	if endpoint == "" {
		advice = fmt.Sprintf("%s declares this workspace's Dolt server externally managed, so bd did not start one.", decl.Setting)
	} else {
		advice = decl.Advice(endpoint)
	}
	var b strings.Builder
	b.WriteString("refusing to start a Dolt server: ")
	b.WriteString(advice)
	b.WriteString("\nbd does not start or manage that server.")
	if sole {
		b.WriteString(" Removing dolt_server_port lets bd manage the server, with a fixed port set by dolt.port in .beads/config.yaml.")
	}
	if trackedLive {
		fmt.Fprintf(&b, "\nbd's own Dolt server from an earlier start is still running here; if it is not the server %s names, "+
			"stop it with 'bd dolt stop'.", decl.Setting)
	}
	return &ProxyCapabilityError{
		Code:     externalDoltStartConflictCode,
		Message:  b.String(),
		ExitCode: 1,
		Reason:   ProxyReasonDesign,
	}
}
