package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// TestInitServerCloneStopsAtTheHostInitOpens pins GH#5961's init side: bd
// init --server with no --server-host must not stamp DefaultDoltServerHost
// into the clone's tracked metadata.json, and its clone endpoints must dial
// the host init's own store opens (initStoreServerHost: --server-host, else
// BEADS_DOLT_SERVER_HOST, else 127.0.0.1) even when the loaded configuration
// names another dolt.host — promoted above the metadata host, that dolt.host
// would otherwise send the clone into one server while init's store opens
// another.
func TestInitServerCloneStopsAtTheHostInitOpens(t *testing.T) {
	endpointCredentialsEnv(t, nil)
	// The configuration bd init loaded names a remote dolt.host — the
	// enclosing workspace's, which every later command merges.
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "config.yaml"),
		[]byte("dolt.host: 192.0.2.50\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", outer)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// A port file, as a workspace whose own server init reached would have:
	// the store's port resolution must find an address to dial.
	const port = 13307
	if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PortFileName),
		[]byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatal(err)
	}

	cloneCfg := initTimeCloneConfig(true, "", 0, "", "", "beads")
	if cloneCfg.DoltServerHost != "" {
		t.Errorf("clone config names dolt_server_host %q; an init with no --server-host must not stamp one into tracked metadata.json (GH#5961)", cloneCfg.DoltServerHost)
	}

	ep := initCloneEndpoint(beadsDir, cloneCfg, "")
	if ep.Host != configfile.DefaultDoltServerHost {
		t.Errorf("initCloneEndpoint host = %q, want %s: the clone must dial the host init's store opens, not the loaded dolt.host 192.0.2.50", ep.Host, configfile.DefaultDoltServerHost)
	}
	if ep.Port != port {
		t.Errorf("initCloneEndpoint port = %d, want the port file's %d", ep.Port, port)
	}

	bep, err := initServerCloneEndpoint(beadsDir, cloneCfg)
	if err != nil {
		t.Fatalf("initServerCloneEndpoint: %v", err)
	}
	if bep.host != configfile.DefaultDoltServerHost {
		t.Errorf("initServerCloneEndpoint host = %q, want %s: the server clone must dial the host init's store opens", bep.host, configfile.DefaultDoltServerHost)
	}
	if bep.port != port {
		t.Errorf("initServerCloneEndpoint port = %d, want the port file's %d", bep.port, port)
	}
}
