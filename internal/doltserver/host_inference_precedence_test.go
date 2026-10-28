package doltserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestHostInferenceFollowsConfigYamlHostOverMetadataHost pins GH#5961 at the
// mode-inference tail of HostImpliesServerMode: a workspace whose (git-tracked)
// metadata.json names the local 127.0.0.1 and whose config.yaml names a remote
// db.example.com is a server workspace of an external server — the host the
// dial now resolves (config.yaml) infers the mode, the metadata host it outranks
// infers nothing. The storage-mode resolver (IsDoltServerMode) and the
// lifecycle resolver (ResolveServerMode, check 2b) must keep agreeing.
func TestHostInferenceFollowsConfigYamlHostOverMetadataHost(t *testing.T) {
	beadsDir := endpointTestEnv(t, nil)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt.host: db.example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	writeEndpointMetadata(t, beadsDir, &configfile.Config{DoltServerHost: "127.0.0.1"})

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("configfile.Load: cfg=%v err=%v", cfg, err)
	}
	if !cfg.IsDoltServerMode() {
		t.Errorf("IsDoltServerMode() = false, want true: the remote dolt.host config.yaml names implies server mode over the local metadata.json dolt_server_host")
	}
	if mode := ResolveServerMode(beadsDir); mode != ServerModeExternal {
		t.Errorf("ResolveServerMode() = %v, want ServerModeExternal: a server on another machine cannot have a bd-owned lifecycle", mode)
	}
}
