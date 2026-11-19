package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestRenderDoltStatusAll pins the --all listing's two block shapes
// (GH#4282): a bd-owned server with workspace, PID, port, uptime and
// idle-stop state, and a foreign one reported as not managed by bd — with
// bd never claiming more than it knows about it.
func TestRenderDoltStatusAll(t *testing.T) {
	out := captureStdout(t, func() error {
		renderDoltStatusAll([]doltserver.DiscoveredServer{
			{
				PID: 41001, ServerDir: "/w/repo/.beads", Port: 33101, BDManaged: true,
				Uptime: 4*time.Minute + 12*time.Second, IdleStop: "stops after 30m0s idle",
			},
			{
				PID: 41002, ServerDir: "/opt/foreign", Uptime: time.Hour,
			},
			{
				PID: 41003,
			},
		})
		return nil
	})
	want := []string{
		"Dolt sql-servers running on this machine: 3",
		"Dolt server: running (bd-managed)",
		"Workspace: /w/repo/.beads",
		"PID:       41001",
		"Port:      33101",
		"Up:        4m12s",
		"Idle stop: stops after 30m0s idle",
		"Dolt server: running (not managed by bd)",
		"PID:       41002",
		"Workspace: /opt/foreign (no bd pid file names this server)",
		"Workspace: unknown (started outside bd)",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("--all listing is missing %q:\n%s", w, out)
		}
	}
}

// TestRenderDoltStatusAllEmpty pins the no-servers message.
func TestRenderDoltStatusAllEmpty(t *testing.T) {
	out := captureStdout(t, func() error {
		renderDoltStatusAll(nil)
		return nil
	})
	if !strings.Contains(out, "No dolt sql-servers running on this machine") {
		t.Errorf("empty listing = %q", out)
	}
}
