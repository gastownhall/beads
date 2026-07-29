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
