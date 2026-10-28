//go:build cgo

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
)

// TestInitServerModeHonorsCentralServerConfig pins GH#3895's init contract:
// with no --server-* flag and no BEADS_DOLT_SERVER_* env, a server-mode
// init's store dials the endpoint the central server config names — host,
// port, user and TLS included — and starts no local dolt server, because
// the file points every workspace on the machine at a server bd never runs.
func TestInitServerModeHonorsCentralServerConfig(t *testing.T) {
	port := freeLoopbackPort(t)
	central := filepath.Join(t.TempDir(), "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d,"dolt_server_user":"central_user","dolt_server_tls":true}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// End to end: against a dead central endpoint, init fails naming that
	// endpoint and launching nothing.
	f := newExternalInitFixture(t)
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)
	res := f.run("", "init", "--server")

	requireNoLaunch(t, res)
	if !res.failed {
		t.Errorf("bd init succeeded with no server at the central endpoint:\n%s", res.output)
	}
	want := net.JoinHostPort("db.invalid", strconv.Itoa(port))
	if !strings.Contains(res.output, want) {
		t.Errorf("bd init output does not name the central endpoint %q:\n%s", want, res.output)
	}

	// The resolvers init's store reads: the four connection fields all come
	// from the central server config, and the port arrives with its source.
	for _, name := range []string{
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_TLS",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SHARED_SERVER",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", central)
	if got := initStoreServerHost(""); got != "db.invalid" {
		t.Errorf("initStoreServerHost() = %q, want db.invalid", got)
	}
	if got := initStoreServerUser(""); got != "central_user" {
		t.Errorf("initStoreServerUser() = %q, want central_user", got)
	}
	if !initDoltServerTLS() {
		t.Error("initDoltServerTLS() = false, want true from the central server config")
	}
	// At a bd host, the tail of initStoreServerPort returns the central
	// port, resolved, with the central-config source — the same
	// externally-managed mark --external's named port carries.
	gotPort, source, shared, resolved := initStoreServerPort(t.TempDir(), "127.0.0.1")
	if gotPort != port || source != doltserver.PortSourceCentralConfig || shared || !resolved {
		t.Errorf("initStoreServerPort() = (%d, %q, shared=%v, resolved=%v), want (%d, %q, false, true)",
			gotPort, source, shared, resolved, port, doltserver.PortSourceCentralConfig)
	}
	// bd treats the endpoint the central config supplies like --external's:
	// it never auto-starts a server for it.
	if !initCentralSuppliedEndpoint("", 0, doltserver.PortSourceCentralConfig) {
		t.Error("initCentralSuppliedEndpoint() = false, want true for a central-config endpoint")
	}
}

// TestInitPersistsNoCentralConfigFields pins GH#3895's persistence
// contract: a server-mode bd init may dial the central server config's
// endpoint, but .beads/metadata.json records dolt_mode server and none of
// dolt_server_host, dolt_server_port, dolt_server_user or dolt_server_tls
// from that file — the workspace inherits them at runtime, like every
// command, instead of freezing this machine's defaults into git-tracked
// metadata.json. The clone-time config init saves (initTimeCloneConfig)
// carries the same flag-only fields.
func TestInitPersistsNoCentralConfigFields(t *testing.T) {
	port := freeLoopbackPort(t)
	central := filepath.Join(t.TempDir(), "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.central.invalid","dolt_server_port":%d,"dolt_server_user":"central_user","dolt_server_tls":true}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", central)

	// The struct init saves (finalizeSyncedBootstrap): flag-given fields
	// only. It must not pin the default host/user, and it must not copy the
	// central config's endpoint in.
	cloneCfg := initTimeCloneConfig(true, "", 0, "", "", "testdb")
	if cloneCfg.DoltServerHost != "" || cloneCfg.DoltServerHost == configfile.DefaultDoltServerHost {
		t.Errorf("clone config DoltServerHost = %q, want empty (no pinned default, no central copy)", cloneCfg.DoltServerHost)
	}
	if cloneCfg.DoltServerUser != "" || cloneCfg.DoltServerUser == configfile.DefaultDoltServerUser {
		t.Errorf("clone config DoltServerUser = %q, want empty (no pinned default, no central copy)", cloneCfg.DoltServerUser)
	}
	if cloneCfg.DoltServerPort != 0 {
		t.Errorf("clone config DoltServerPort = %d, want 0", cloneCfg.DoltServerPort)
	}
	// The clone ENDPOINT still resolves the central config's fields at
	// runtime, through the same getters the store reads.
	if got := cloneCfg.GetDoltServerHost(); got != "db.central.invalid" {
		t.Errorf("clone endpoint host = %q, want db.central.invalid", got)
	}
	if got := cloneCfg.GetDoltServerUser(); got != "central_user" {
		t.Errorf("clone endpoint user = %q, want central_user", got)
	}

	// End to end, against a reachable server: a server-mode init succeeds
	// through the central endpoint and writes a metadata.json that names
	// the mode and none of the central config's connection fields.
	skipIfNoDolt(t)
	resetInitFlagsForServerInit(t)
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Dolt metadata test on Windows")
	}

	saveAndRestoreGlobals(t)
	dbPath = ""
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	tmpDir := newGitRepo(t)
	t.Chdir(tmpDir)
	_ = runCommandInDir(tmpDir, "git", "config", "remote.origin.url", "https://github.com/test/repo.git")

	// server.json names the suite's live test server: init's store dials
	// it through the central config (the ambient port env vars are
	// neutralized so the central config is the one naming the port).
	centralPath := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(centralPath,
		[]byte(fmt.Sprintf(`{"dolt_server_host":"127.0.0.1","dolt_server_port":%d}`, testDoltServerPort)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	database := uniqueTestDBName(t)
	rootCmd.SetArgs([]string{"init", "--server", "--database", database, "--prefix", "test", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("bd init --server failed: %v", err)
	}

	meta, err := configfile.Load(filepath.Join(tmpDir, ".beads"))
	if err != nil {
		t.Fatalf("load metadata.json: %v", err)
	}
	if meta.DoltMode != configfile.DoltModeServer {
		t.Errorf("DoltMode = %q, want server", meta.DoltMode)
	}
	if meta.DoltServerHost != "" {
		t.Errorf("DoltServerHost = %q, want empty: server.json's host must not be persisted", meta.DoltServerHost)
	}
	if meta.DoltServerPort != 0 {
		t.Errorf("DoltServerPort = %d, want 0: server.json's port must not be persisted", meta.DoltServerPort)
	}
	if meta.DoltServerUser != "" {
		t.Errorf("DoltServerUser = %q, want empty: server.json's user must not be persisted", meta.DoltServerUser)
	}
	if meta.DoltServerTLS {
		t.Error("DoltServerTLS = true, want false: server.json's TLS must not be persisted")
	}
}
