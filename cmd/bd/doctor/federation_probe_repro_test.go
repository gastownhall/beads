//go:build cgo

package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestCheckFederationRemotesAPI_ServerWithoutPIDFile reproduces bd-0r8dcw:
// a Dolt server bd did not start (launchd/systemd/container) writes no PID
// file under the workspace. The federation check must still see that the
// configured endpoint answers, rather than warning "Server not running" and
// counting origin as a federation peer.
func TestCheckFederationRemotesAPI_ServerWithoutPIDFile(t *testing.T) {
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	dbName := fmt.Sprintf("doctest_nopid_%d", time.Now().UnixNano())
	t.Cleanup(func() { dropDoctorTestDatabase(dbName, port) })

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            doltDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := store.AddRemote(ctx, "origin", "file:///tmp/bd-0r8dcw-origin"); err != nil {
		_ = store.Close()
		t.Fatalf("add origin remote: %v", err)
	}
	_ = store.Close()

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       "server",
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   dbName,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Deliberately no dolt-server.pid: the server is externally managed.

	check := CheckFederationRemotesAPI(tmpDir)
	if check.Status != StatusOK || check.Message != "N/A (no federation peers configured)" {
		t.Fatalf("server reachable at 127.0.0.1:%d with only origin configured: want ok %q, got %s %q (detail: %s)",
			port, "N/A (no federation peers configured)", check.Status, check.Message, check.Detail)
	}
}
