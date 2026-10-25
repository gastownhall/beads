package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

func fixEndpointWorkspace(t *testing.T) (string, *configfile.Config) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_PASSWORD", "BEADS_DOLT_AUTO_START", "BEADS_DOLT_SERVER_USER",
		"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_DATA_DIR",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(home, "no-credentials"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "fix_endpoint"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir, cfg
}

// TestServerMetadataReconcileNeverDialsAnUnresolvedPort covers the reconcile
// bd bootstrap runs first on a fresh clone of a `bd init --server` project:
// no port is named and no server runs. When the next command starts bd's own
// server there is nothing to reconcile yet; otherwise the reconcile refuses
// with the store's advice. It used to dial 127.0.0.1:0 and fail the whole
// bootstrap with "dial tcp 127.0.0.1:0: connect: connection refused".
func TestServerMetadataReconcileNeverDialsAnUnresolvedPort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoStart string
		stopped   bool // the data directory already holds a database
		wantErr   string
	}{
		{name: "fresh clone"},
		{name: "auto-start off", autoStart: "0", wantErr: "dolt.auto-start is off"},
		// A stopped server whose data directory holds databases is not a
		// fresh clone: say the repair could not run instead of skipping it.
		{name: "stopped server with databases", stopped: true, wantErr: "this workspace's Dolt server is not running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, _ := fixEndpointWorkspace(t)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStart)
			if tc.stopped {
				if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "fix_endpoint", ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			_, msg, err := ResolveAuthoritativeServerMetadata(filepath.Dir(beadsDir), false)
			if tc.wantErr == "" {
				if err != nil || msg != "" {
					t.Errorf("reconcile = %q, %v; want nothing to reconcile before bd's server first starts", msg, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "127.0.0.1:0") {
				t.Errorf("reconcile error = %v, want the store's advice %q and no dial of port 0", err, tc.wantErr)
			}
		})
	}
}
