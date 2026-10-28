package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestExternalDoltStartRefusalContract pins what consumers branch on: the
// frozen code, the design reason and that nothing changed. The message may be
// reworded, but it must name the declaring setting and endpoint, add the
// sole-setting and earlier-server sentences only when they apply, and never
// advise the start or the --force it refuses.
func TestExternalDoltStartRefusalContract(t *testing.T) {
	const meta = "dolt_server_port in /w/.beads/metadata.json"
	for _, tc := range []struct {
		name     string
		decl     doltserver.ExternalPortDeclaration
		endpoint string
		sole     bool
		tracked  bool
		want     []string
		absent   []string
	}{
		{"metadata port names endpoint",
			doltserver.ExternalPortDeclaration{Setting: meta, NamesEndpoint: true}, "127.0.0.1:3310", false, false,
			[]string{meta + " declares the Dolt server at 127.0.0.1:3310 externally managed"},
			[]string{"Removing dolt_server_port", "bd dolt stop"}},
		{"sole metadata port",
			doltserver.ExternalPortDeclaration{Setting: meta, NamesEndpoint: true}, "127.0.0.1:3310", true, false,
			[]string{meta, "Removing dolt_server_port lets bd manage the server", "dolt.port in .beads/config.yaml"},
			[]string{"bd dolt stop"}},
		{"declaration's own sole sentence is not repeated",
			doltserver.ExternalPortDeclaration{Setting: meta, Sole: true}, "127.0.0.1:3311", false, false,
			[]string{meta + " declares this workspace's Dolt server externally managed", "127.0.0.1:3311"},
			[]string{"Removing dolt_server_port"}},
		{"env port",
			doltserver.ExternalPortDeclaration{Setting: "BEADS_DOLT_SERVER_PORT", NamesEndpoint: true}, "127.0.0.1:3312", false, false,
			[]string{"BEADS_DOLT_SERVER_PORT declares the Dolt server at 127.0.0.1:3312"},
			nil},
		{"server mode env, no port",
			doltserver.ExternalPortDeclaration{Setting: "BEADS_DOLT_SERVER_MODE=1"}, "", false, false,
			[]string{"BEADS_DOLT_SERVER_MODE=1 declares this workspace's Dolt server externally managed"},
			nil},
		{"earlier bd server still running",
			doltserver.ExternalPortDeclaration{Setting: meta, NamesEndpoint: true}, "127.0.0.1:3313", false, true,
			[]string{"bd's own Dolt server from an earlier start is still running here", "bd dolt stop"},
			nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := externalDoltStartRefusalFor(tc.decl, tc.endpoint, tc.sole, tc.tracked)
			if err.Code != "external.dolt_start.conflict" {
				t.Errorf("code = %q, want %q", err.Code, "external.dolt_start.conflict")
			}
			if err.Reason != ProxyReasonDesign {
				t.Errorf("reason = %q, want %q", err.Reason, ProxyReasonDesign)
			}
			if err.ExitCode != 1 {
				t.Errorf("exit code = %d, want 1", err.ExitCode)
			}
			if err.Mutates {
				t.Error("mutates = true; a refused start changes nothing")
			}
			if !strings.HasPrefix(err.Message, "refusing to start a Dolt server: ") {
				t.Errorf("message does not open with the refusal: %q", err.Message)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Message, want) {
					t.Errorf("message does not contain %q: %q", want, err.Message)
				}
			}
			for _, absent := range append([]string{"bd dolt start", "--force"}, tc.absent...) {
				if strings.Contains(err.Message, absent) {
					t.Errorf("message contains %q: %q", absent, err.Message)
				}
			}
		})
	}
}
