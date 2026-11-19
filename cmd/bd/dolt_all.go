package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// discoverDoltServers lists the dolt sql-server processes on this machine;
// a variable so tests can supply canned servers.
var discoverDoltServers = doltserver.DiscoverServers

// doltServerListEntry is one server in `bd dolt status --all --json`.
type doltServerListEntry struct {
	PID           int        `json:"pid"`
	Managed       bool       `json:"managed"`
	Workspace     string     `json:"workspace,omitempty"`
	ServerDir     string     `json:"server_dir,omitempty"`
	Port          int        `json:"port,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	UptimeSeconds int64      `json:"uptime_seconds,omitempty"`
	IdleStop      string     `json:"idle_stop,omitempty"`
	Command       string     `json:"command,omitempty"`
}

// doltServerWorkspace names the workspace a server directory belongs to:
// the directory holding .beads, or the server directory itself otherwise
// (the shared server, a BEADS_DIR outside a repo).
func doltServerWorkspace(serverDir string) string {
	if filepath.Base(serverDir) == ".beads" {
		return filepath.Dir(serverDir)
	}
	return serverDir
}

func doltServerListEntries(servers []doltserver.DiscoveredServer, now time.Time) []doltServerListEntry {
	entries := make([]doltServerListEntry, 0, len(servers))
	for _, s := range servers {
		e := doltServerListEntry{PID: s.PID, Managed: s.Managed}
		if !s.Managed {
			e.Command = s.Command
			entries = append(entries, e)
			continue
		}
		e.Workspace = doltServerWorkspace(s.ServerDir)
		e.ServerDir = s.ServerDir
		e.Port = s.Port
		if !s.StartedAt.IsZero() {
			started := s.StartedAt
			e.StartedAt = &started
			e.UptimeSeconds = int64(now.Sub(started) / time.Second)
		}
		e.IdleStop = s.IdleStop.String()
		entries = append(entries, e)
	}
	// bd's own servers first, by workspace; the others by PID.
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Managed != b.Managed {
			return a.Managed
		}
		if a.Managed {
			return a.Workspace < b.Workspace
		}
		return a.PID < b.PID
	})
	return entries
}

// runDoltStatusAll is `bd dolt status --all`: every dolt sql-server on this
// machine, from any directory.
func runDoltStatusAll() error {
	servers, err := discoverDoltServers()
	if err != nil {
		return HandleErrorRespectJSON("listing Dolt servers: %v", err)
	}
	entries := doltServerListEntries(servers, time.Now())
	if jsonOutput {
		return outputJSON(map[string]interface{}{"servers": entries})
	}
	if len(entries) == 0 {
		fmt.Println("No Dolt servers running.")
		return nil
	}
	managed := 0
	for _, e := range entries {
		if e.Managed {
			managed++
		}
	}
	if managed > 0 {
		fmt.Printf("Dolt servers managed by bd: %d\n", managed)
		for _, e := range entries[:managed] {
			fmt.Printf("  ● %s\n", e.Workspace)
			uptime := "unknown"
			if e.StartedAt != nil {
				uptime = (time.Duration(e.UptimeSeconds) * time.Second).String()
			}
			fmt.Printf("      PID %d  Port %d  Uptime %s  Idle stop: %s\n", e.PID, e.Port, uptime, e.IdleStop)
		}
	}
	if others := entries[managed:]; len(others) > 0 {
		if managed > 0 {
			fmt.Println()
		}
		fmt.Printf("Other Dolt servers (not managed by bd): %d\n", len(others))
		for _, e := range others {
			fmt.Printf("  ○ PID %d  %s\n", e.PID, e.Command)
		}
	}
	return nil
}
