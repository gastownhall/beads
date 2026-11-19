package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/doltserver"
)

// discoverServers lists every dolt sql-server on this machine (see
// doltserver.DiscoverServers). A variable so tests can stub the process
// table the same way doltserver's own tests do.
var discoverServers = doltserver.DiscoverServers

// runDoltStatusAll is `bd dolt status --all` (GH#4282): the Dolt servers of
// every workspace on this machine, not just the active one's, from any
// directory — no workspace is needed. bd's own servers report the state the
// idle watch keeps (workspace, PID, port, uptime, idle-stop); servers
// started outside bd report exactly that: bd does not manage them.
func runDoltStatusAll() error {
	servers, err := discoverServers()
	if err != nil {
		return HandleError("listing dolt sql-servers: %v", err)
	}
	if jsonOutput {
		if err := outputJSON(servers); err != nil {
			return HandleError("%v", err)
		}
		return nil
	}
	renderDoltStatusAll(servers)
	return nil
}

// renderDoltStatusAll writes the --all listing to stdout. Extracted from
// runDoltStatusAll so the rendering is unit-testable without a process
// table (the discovery itself is covered by doltserver's own tests).
func renderDoltStatusAll(servers []doltserver.DiscoveredServer) {
	if len(servers) == 0 {
		fmt.Println("No dolt sql-servers running on this machine")
		return
	}
	fmt.Printf("Dolt sql-servers running on this machine: %d\n", len(servers))
	for _, s := range servers {
		if s.BDManaged {
			fmt.Println("Dolt server: running (bd-managed)")
			fmt.Printf("  Workspace: %s\n", s.ServerDir)
			fmt.Printf("  PID:       %d\n", s.PID)
			fmt.Printf("  Port:      %d\n", s.Port)
			if s.Uptime > 0 {
				fmt.Printf("  Up:        %s\n", s.Uptime)
			}
			fmt.Printf("  Idle stop: %s\n", s.IdleStop)
			continue
		}
		fmt.Println("Dolt server: running (not managed by bd)")
		fmt.Printf("  PID:       %d\n", s.PID)
		if s.ServerDir != "" {
			fmt.Printf("  Workspace: %s (no bd pid file names this server)\n", s.ServerDir)
		} else {
			fmt.Println("  Workspace: unknown (started outside bd)")
		}
	}
}
