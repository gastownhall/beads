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
