package doltserver

import (
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
