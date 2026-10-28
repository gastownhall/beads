package doltserver

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestExternallyManagedBy pins which setting `bd dolt start` names when it
// refuses: the ResolveServerMode check that made the workspace external, and
// none for a workspace bd owns, an embedded one, or shared-server mode, where
// bd starts the shared server itself.
func TestExternallyManagedBy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      map[string]string
		metaPort int
		dolt     string // metadata.json dolt_mode; "" for server mode
		want     string // "" for none; "metadata" for dolt_server_port in metadata.json
		sole     bool
	}{
		{name: "metadata dolt_server_port", metaPort: 13401, want: "metadata", sole: true},
		{name: "owned", want: ""},
		{name: "explicit embedded", dolt: configfile.DoltModeEmbedded, want: ""},
		{name: "shared server", env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1"}, metaPort: 13402, want: ""},
		{name: "server mode env", env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}, want: "BEADS_DOLT_SERVER_MODE=1"},
		{name: "server mode env outranks metadata port", env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}, metaPort: 13403,
			want: "BEADS_DOLT_SERVER_MODE=1"},
		{name: "server port env", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13404"}, want: "BEADS_DOLT_SERVER_PORT"},
		{name: "legacy port env", env: map[string]string{"BEADS_DOLT_PORT": "13405"}, want: "BEADS_DOLT_PORT"},
		{name: "server port env outranks legacy", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13406", "BEADS_DOLT_PORT": "13407"},
			want: "BEADS_DOLT_SERVER_PORT"},
		{name: "invalid server port env falls to legacy", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "x", "BEADS_DOLT_PORT": "13408"},
			want: "BEADS_DOLT_PORT"},
		{name: "untrimmed port env is ignored", env: map[string]string{"BEADS_DOLT_SERVER_PORT": " 13409"}, want: ""},
		{name: "port env outranks metadata port", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13410"}, metaPort: 13411,
			want: "BEADS_DOLT_SERVER_PORT"},
		{name: "proxied workspace ignores port env", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13412"},
			dolt: configfile.DoltModeProxiedServer, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namedPortRefusalEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			config.ResetForTesting()
			dir := namedPortServerWorkspace(t, tc.metaPort)
			if tc.dolt != "" {
				cfg, err := configfile.Load(dir)
				if err != nil {
					t.Fatal(err)
				}
				cfg.DoltMode = tc.dolt
				if err := cfg.Save(dir); err != nil {
					t.Fatal(err)
				}
			}
			want := tc.want
			if want == "metadata" {
				want = "dolt_server_port in " + configfile.ConfigPath(dir)
			}
			setting, sole := ExternallyManagedBy(dir)
			if setting != want || sole != tc.sole {
				t.Errorf("ExternallyManagedBy = (%q, %v), want (%q, %v)", setting, sole, want, tc.sole)
			}
		})
	}
}

// TestUnreachableEndpointAdviceNamesNonPortDeclarations covers the externally
// managed workspaces no port setting names (ExternalPortSetting is zero):
// BEADS_DOLT_SERVER_MODE=1, and dolt_server_port when the dialed port came
// from bd's port file with bd's pid file beside it. `bd dolt start` refuses
// both, so the advice names the declaring setting instead of suggesting it.
// A shared server keeps the caller's own advice, which is `bd dolt start`.
func TestUnreachableEndpointAdviceNamesNonPortDeclarations(t *testing.T) {
	const port = 13413
	endpoint := "127.0.0.1:" + strconv.Itoa(port)
	for _, tc := range []struct {
		name     string
		env      map[string]string
		metaPort int
		bdState  bool // bd's pid and port files, the port file naming port
		source   PortSource
		want     []string // "" wants no advice
	}{
		{name: "server mode env", env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}, source: PortSourceConfigYaml,
			want: []string{"BEADS_DOLT_SERVER_MODE=1 declares this workspace's Dolt server externally managed, so bd did not start one at " + endpoint}},
		{name: "bd's pid and port files beside dolt_server_port", metaPort: port, bdState: true, source: PortSourcePortFile,
			want: []string{"metadata.json declares this workspace's Dolt server externally managed, so bd did not start one at " + endpoint,
				"Removing dolt_server_port lets bd manage the server."}},
		{name: "shared server", env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_DOLT_SERVER_MODE": "1"},
			source: PortSourceConfigYaml, want: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namedPortRefusalEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			config.ResetForTesting()
			dir := namedPortServerWorkspace(t, tc.metaPort)
			if tc.bdState {
				for path, data := range map[string]string{portPath(dir): strconv.Itoa(port), pidPath(dir): "99999999"} {
					if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := UnreachableEndpointAdvice(dir, "127.0.0.1", port, tc.source)
			for _, want := range tc.want {
				if want == "" && got != "" {
					t.Errorf("advice = %q, want none", got)
				}
				if !strings.Contains(got, want) {
					t.Errorf("advice does not contain %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "bd dolt start") {
				t.Errorf("advice suggests `bd dolt start`, which refuses this workspace:\n%s", got)
			}
		})
	}
}
