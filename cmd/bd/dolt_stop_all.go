// `bd dolt stop --all` (GH#4282): ending every dolt sql-server bd itself
// manages on this machine, each stop reported.

package main

import (
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/doltserver"
)

// stopDoltServer ends the server one serverDir's state files name — the
// same stop path the workspace-local `bd dolt stop` takes. A variable so
// tests can stub the stop without running one, the same seam as
// discoverServers.
var stopDoltServer = doltserver.StopWithForce

// doltStopAllResult is the report of `bd dolt stop --all`: one entry per
// discovered server, bd's own and foreign alike, so each stop and each
// server bd deliberately left alone is reported (GH#4282).
type doltStopAllResult struct {
	Servers []doltStopAllServerResult `json:"servers"`
}

// doltStopAllServerResult is one discovered server's stop report.
type doltStopAllServerResult struct {
	// Workspace is the server directory bd stopped (empty for servers bd
	// left alone that it could not attribute).
	Workspace string `json:"workspace,omitempty"`
	PID       int    `json:"pid"`
	// Stopped reports a server bd itself ended.
	Stopped bool `json:"stopped"`
	// AlreadyStopped: the server was gone by the time bd reached it —
	// including by its own idle watch — and the stop path's idempotent
	// state cleanup still ran.
	AlreadyStopped bool   `json:"already_stopped,omitempty"`
	Error          string `json:"error,omitempty"`
	// LeftAlone is why bd did not stop this server ("" when it did).
	LeftAlone string `json:"left_alone,omitempty"`
}

// runDoltStopAll is `bd dolt stop --all` (GH#4282): stop every dolt
// sql-server running on this machine that bd itself manages — the ones a
// workspace's pid file names — through the same stop path as the
// workspace-local `bd dolt stop`, and report each. Servers bd did not start
// are left running and reported as not managed: bd never signals a process
// its own state files do not name. A stop that fails is reported and the
// command still exits non-zero.
func runDoltStopAll(force bool) error {
	servers, err := discoverServers()
	if err != nil {
		return HandleError("listing dolt sql-servers: %v", err)
	}
	result := doltStopAllResult{Servers: make([]doltStopAllServerResult, 0, len(servers))}
	failed := 0
	for _, s := range servers {
		if !s.BDManaged {
			entry := doltStopAllServerResult{PID: s.PID, LeftAlone: "not managed by bd"}
			if s.ServerDir != "" {
				entry.Workspace = s.ServerDir
			}
			result.Servers = append(result.Servers, entry)
			continue
		}
		entry := doltStopAllServerResult{Workspace: s.ServerDir, PID: s.PID}
		switch stopErr := stopDoltServer(s.ServerDir, force); {
		case stopErr == nil:
			entry.Stopped = true
		case errors.Is(stopErr, doltserver.ErrServerNotRunning):
			entry.AlreadyStopped = true
		default:
			entry.Error = stopErr.Error()
			failed++
		}
		result.Servers = append(result.Servers, entry)
	}
	if jsonOutput {
		if err := outputJSON(result); err != nil {
			return HandleError("encode dolt stop --all result: %v", err)
		}
		if failed > 0 {
			return SilentExit()
		}
		return nil
	}
	renderDoltStopAll(result)
	if failed > 0 {
		return SilentExit()
	}
	return nil
}

// renderDoltStopAll writes the --all report to stdout, one block per
// server. Extracted from runDoltStopAll so the rendering is unit-testable
// without a process table, like renderDoltStatusAll.
func renderDoltStopAll(result doltStopAllResult) {
	if len(result.Servers) == 0 {
		fmt.Println("No dolt sql-servers running on this machine")
		return
	}
	for _, s := range result.Servers {
		switch {
		case s.Stopped:
			fmt.Printf("Stopped dolt server for %s (PID %d)\n", s.Workspace, s.PID)
		case s.AlreadyStopped:
			fmt.Printf("Already stopped: %s (PID %d; state files cleaned up)\n", s.Workspace, s.PID)
		case s.Error != "":
			fmt.Printf("Could not stop dolt server for %s (PID %d): %s\n", s.Workspace, s.PID, s.Error)
		case s.Workspace != "":
			fmt.Printf("Left alone (not managed by bd): PID %d, workspace %s\n", s.PID, s.Workspace)
		default:
			fmt.Printf("Left alone (not managed by bd): PID %d\n", s.PID)
		}
	}
}
