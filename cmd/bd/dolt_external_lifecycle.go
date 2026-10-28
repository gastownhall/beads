package main

// `bd dolt start` on a workspace whose Dolt server another party manages:
// the typed refusal, with the message that names the declaring setting.
//
// Every other lifecycle command already treats such a workspace as external
// (ResolveServerMode's checks 1, 2, 2b, 2c, 4): bd never auto-starts one and
// dials the declared endpoint instead. `bd dolt start` was the one remaining
// hole — it would launch a server of bd's own over .beads/dolt on the
// declared port, a different database that hides the managed one and takes
// the port the managed server needs, then publish that port in
// .beads/dolt-server.pid/.port so later commands silently resolve to the
// empty database. Refused here, before any state is written, with a message
// that never suggests `bd dolt start` again and names the setting the
// operator can change.

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Stable identity of the `bd dolt start` refusal on a workspace whose server
// is externally managed. Codes are the part of the refusal contract downstream
// consumers branch on, so this one is frozen; the message beside it is
// informative.
const externalDoltStartConflictCode = "external.dolt_start.conflict"

// externalDoltStartRefusal is the typed refusal for `bd dolt start` on a
// workspace whose Dolt server setting declares externally managed
// (doltserver.ExternallyManagedBy); the caller checks that it returns one.
// The message is the externally-managed advice for the workspace's dial
// endpoint — from ExternalPortSetting when a port-naming setting declares it,
// which also names a stale bd port file, else from the declaration
// ExternallyManagedBy found — with the sole-setting remedy appended when
// dolt_server_port alone declares the server external. It never suggests
// `bd dolt start`: that command is what is being refused, and `--force` does
// not bypass this refusal, so neither belongs in the advice.
func externalDoltStartRefusal(beadsDir, setting string, sole bool) *ProxyCapabilityError {
	dial := doltserver.DialConfig(beadsDir)
	endpoint := doltutil.ServerAddr(dial.Host, dial.Port)
	decl := doltserver.ExternalPortSetting(beadsDir, dial.PortSource, dial.Port)
	var body string
	switch {
	case decl.Setting != "":
		body = decl.Advice(endpoint)
	case dial.Port > 0:
		body = doltserver.ExternalPortDeclaration{Setting: setting, Sole: sole}.Advice(endpoint)
	default:
		body = doltserver.ExternalPortDeclaration{Setting: setting, Sole: sole}.Advice("")
	}
	if sole && decl.PortFile == "" && !strings.Contains(body, "Removing dolt_server_port") {
		body += " Removing dolt_server_port lets bd manage the server, with a fixed port set by dolt.port in .beads/config.yaml."
	}
	body += "\nbd does not start or manage that server."
	if doltserver.IsTrackedServerRunning(beadsDir) {
		body += fmt.Sprintf("\nbd's own Dolt server from an earlier start is still running here; if it is not the server %s names, stop it with 'bd dolt stop'.",
			remedy.PrintedValue(setting))
	}
	return &ProxyCapabilityError{
		Code:     externalDoltStartConflictCode,
		Message:  "refusing to start a Dolt server: " + body,
		ExitCode: 1,
		Mutates:  false,
		Reason:   ProxyReasonDesign,
	}
}
