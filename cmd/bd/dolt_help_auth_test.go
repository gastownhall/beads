package main

import (
	"strings"
	"testing"
)

// GH#5011: remote-server auth must be discoverable from CLI help.
func TestDoltHelpMentionsRemoteAuth(t *testing.T) {
	for _, body := range []string{doltCmd.Long, doltSetCmd.Long} {
		for _, needle := range []string{
			"BEADS_DOLT_PASSWORD",
			"BEADS_DOLT_SERVER_TLS",
			"credentials",
		} {
			if !strings.Contains(body, needle) {
				t.Errorf("dolt help missing %q", needle)
			}
		}
	}
	if !strings.Contains(doltSetCmd.Long, "metadata.json") {
		t.Error("dolt set help should explain why password is not a set key")
	}
}

// GH#5741: the default is embedded Dolt, so the help must not describe a
// sql-server as the architecture every database uses, and must scope the
// server-only subcommands.
func TestDoltHelpDescribesEmbeddedDefault(t *testing.T) {
	if strings.Contains(doltCmd.Long, "Beads uses a dolt sql-server for all database operations") {
		t.Error("dolt help still describes a sql-server as the architecture for all databases")
	}
	for _, needle := range []string{
		"embedded (in-process) by default",
		"server mode only",
	} {
		if !strings.Contains(doltCmd.Long, needle) {
			t.Errorf("dolt help missing %q", needle)
		}
	}
}

// GH#5969: hash-derived per-project ports were removed in v0.60.0 (GH#2415).
// The help must describe the OS-assigned port, that stop discards it, and
// dolt.port as the pin; the env var and `bd dolt set port` would make the
// server externally managed.
func TestDoltStartHelpDescribesPortAllocation(t *testing.T) {
	help := strings.Join(strings.Fields(doltStartCmd.Long), " ")
	if strings.Contains(help, "derived from the project path") {
		t.Error("dolt start help still describes a port derived from the project path")
	}
	for _, needle := range []string{
		"free port the OS assigns",
		".beads/dolt-server.port",
		"'bd dolt stop' removes",
		"dolt.port",
		".beads/config.local.yaml",
		"'bd dolt show'",
	} {
		if !strings.Contains(help, needle) {
			t.Errorf("dolt start help missing %q:\n%s", needle, doltStartCmd.Long)
		}
	}
	for _, avoid := range []string{"BEADS_DOLT_SERVER_PORT", "bd dolt set port"} {
		if strings.Contains(help, avoid) {
			t.Errorf("dolt start help recommends %q, which marks the server externally managed", avoid)
		}
	}
}
