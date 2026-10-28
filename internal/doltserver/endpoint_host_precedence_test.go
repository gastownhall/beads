package doltserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestResolveEndpointConfigYamlHostOutranksMetadataHost pins GH#5961 at the
// pairing site: host and port are resolved by separate chains and then joined,
// so once config.yaml dolt.host outranks metadata.json dolt_server_host
// (GH#5961) the reporter's pair — a committed 127.0.0.1 host plus the yaml
// 3307 port — dials 192.0.2.50:3307, the endpoint both files name together,
// not the 127.0.0.1:3307 composite neither declares.
func TestResolveEndpointConfigYamlHostOutranksMetadataHost(t *testing.T) {
	beadsDir := endpointTestEnv(t, nil)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt.mode: server\ndolt.host: 192.0.2.50\ndolt.port: 3307\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The loaded configuration is what every bd command runs with.
	t.Setenv("BEADS_DIR", beadsDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// The reporter's metadata.json pair: an explicit server mode, the
	// machine-local host bd init stamped, and a fossil hash-derived port. No
	// .beads/dolt-server.port exists, so a fresh clone has none either.
	fileCfg := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 27169,
	}
	writeEndpointMetadata(t, beadsDir, fileCfg)

	ep := ResolveEndpoint(beadsDir, fileCfg)

	if ep.Host != "192.0.2.50" {
		t.Errorf("Host = %q, want 192.0.2.50, the host .beads/config.yaml names over metadata.json's dolt_server_host", ep.Host)
	}
	if ep.Port != 3307 || ep.PortSource != PortSourceConfigYaml {
		t.Errorf("Port = %d (%v), want 3307 (PortSourceConfigYaml), the port .beads/config.yaml names over the deprecated metadata.json dolt_server_port", ep.Port, ep.PortSource)
	}
}
