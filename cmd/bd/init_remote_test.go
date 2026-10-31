package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

func TestInitCommandRegistersRemoteFlag(t *testing.T) {
	if initCmd.Flags().Lookup("remote") == nil {
		t.Fatal("init command does not register --remote")
	}
}

func TestInitExplicitRemoteDrivesCloneAndPersistence(t *testing.T) {
	const remote = "git+ssh://git@example.com/right/repo.git"
	resolveConfiguredRemote := func() string {
		return "git+ssh://git@example.com/wrong/repo.git"
	}

	syncURL, source := resolveInitConfiguredSyncRemote(remote, true, resolveConfiguredRemote)
	if syncURL != remote {
		t.Fatalf("syncURL = %q, want explicit remote %q", syncURL, remote)
	}
	if source != initSyncRemoteExplicit {
		t.Fatalf("source = %v, want explicit", source)
	}
	syncFromRemote := syncURL != ""
	syncURLFromConfig := syncURL != "" && source != initSyncRemoteNone
	if !syncFromRemote {
		t.Fatal("explicit remote did not select clone-from-remote path")
	}
	if !syncURLFromConfig {
		t.Fatal("explicit remote was not treated as user-configured sync URL")
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistInitSyncRemote(beadsDir, remote, syncURL, syncFromRemote, syncURLFromConfig, false); err != nil {
		t.Fatalf("persistInitSyncRemote failed: %v", err)
	}
	configBytes, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBytes), remote) {
		t.Fatalf("config.yaml does not contain explicit remote %q:\n%s", remote, configBytes)
	}
}

// TestInitServerExternalRemoteUsesServerCloneMode pins where bd init --remote
// clones a server-mode workspace: through the server under --external, for a
// host bd's own server never runs at (another machine, ::1 or [::1]), and for
// a server some other process runs at 127.0.0.1 (a unix socket, or a port
// that declares it externally managed: BEADS_DOLT_SERVER_PORT, --server-port,
// the legacy BEADS_DOLT_PORT), where a CLI clone into .beads/dolt was
// stranded while init's store created an empty database on that server; with
// the dolt CLI into .beads/dolt for a server bd serves (its own, or the
// shared server), for an endpoint with no address to clone through, and for
// a configured unix socket nothing answers on with no TCP port answering
// either, as on main. Where the store falls back from that socket to TCP
// (doltserver.SocketTransport), the endpoint over TCP decides as above. A
// gateway (BEADS_DOLT_CREDENTIAL_COMMAND) keeps main's CLI clone on
// any host, unless --server-user names the user: init's store then presents
// that user, not the command's token, and opens the server as any other.
func TestInitServerExternalRemoteUsesServerCloneMode(t *testing.T) {
	for _, tc := range []struct {
		name         string
		server       bool
		external     bool
		serverHost   string // --server-host
		serverPort   int    // --server-port
		serverSocket string // --server-socket
		serverUser   string // --server-user
		deadSocket   bool   // the configured socket does not answer
		tcpLive      bool   // TCP answers at every host and port
		env          map[string]string
		want         remoteCloneMode
	}{
		{name: "embedded", want: remoteCloneEmbedded},
		{name: "--external", server: true, external: true, want: remoteCloneExternalServer},
		{name: "bd's own server", server: true, want: remoteCloneCLI},
		{name: "shared server", server: true, env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1"}, want: remoteCloneCLI},
		{name: "shared server on BEADS_DOLT_SERVER_PORT", server: true, env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_DOLT_SERVER_PORT": "13400"}, want: remoteCloneCLI},
		{name: "localhost", server: true, serverHost: "localhost", want: remoteCloneCLI},
		{name: "auto-start off", server: true, env: map[string]string{"BEADS_DOLT_AUTO_START": "0"}, want: remoteCloneCLI},
		// Externally managed with no port: nothing to clone through, so
		// main's CLI clone, and the store refuses the unresolved port.
		{name: "BEADS_DOLT_SERVER_MODE=1 with no port", server: true, env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}, want: remoteCloneCLI},
		// A server some other process runs at 127.0.0.1 does not serve
		// .beads/dolt.
		{name: "BEADS_DOLT_SERVER_PORT", server: true, env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13400"}, want: remoteCloneExternalServer},
		{name: "--server-port", server: true, serverPort: 13400, env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13400"}, want: remoteCloneExternalServer},
		{name: "legacy BEADS_DOLT_PORT", server: true, env: map[string]string{"BEADS_DOLT_PORT": "13400"}, want: remoteCloneExternalServer},
		{name: "unix socket", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", env: map[string]string{"BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneExternalServer},
		// bd's shared server never listens on a unix socket.
		{name: "shared server on a unix socket", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneExternalServer},
		// A socket nothing answers on: the store falls back to TCP at the
		// endpoint's port when TCP answers there
		// (doltserver.SocketTransport), and that endpoint decides; with no
		// port, or nothing answering on it, the store dials the socket and
		// fails, so main's CLI clone stays.
		{name: "dead unix socket", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, env: map[string]string{"BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneCLI},
		{name: "dead unix socket and no port, TCP answering", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, tcpLive: true, env: map[string]string{"BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneCLI},
		{name: "shared server on a dead unix socket", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneCLI},
		{name: "shared server on a dead unix socket, TCP answering", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, tcpLive: true, env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneCLI},
		{name: "dead unix socket on BEADS_DOLT_SERVER_PORT, nothing answering there", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13400", "BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneCLI},
		// The store opens the server on that port over TCP, which does not
		// serve .beads/dolt.
		{name: "dead unix socket on BEADS_DOLT_SERVER_PORT, TCP answering", server: true, serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, tcpLive: true, env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13400", "BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneExternalServer},
		{name: "dead unix socket on a remote host", server: true, serverHost: "db.example.com", serverSocket: "/tmp/bd-init-clone-mode.sock", deadSocket: true, env: map[string]string{"BEADS_DOLT_SERVER_SOCKET": "/tmp/bd-init-clone-mode.sock"}, want: remoteCloneExternalServer},
		{name: "localhost on BEADS_DOLT_SERVER_PORT", server: true, serverHost: "localhost", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "13400"}, want: remoteCloneExternalServer},
		{name: "remote --server-host", server: true, serverHost: "db.example.com", want: remoteCloneExternalServer},
		{name: "remote BEADS_DOLT_SERVER_HOST", server: true, env: map[string]string{"BEADS_DOLT_SERVER_HOST": "db.example.com"}, want: remoteCloneExternalServer},
		{name: "IPv6 loopback ::1", server: true, serverHost: "::1", want: remoteCloneExternalServer},
		{name: "IPv6 loopback [::1]", server: true, serverHost: "[::1]", want: remoteCloneExternalServer},
		// A gateway keeps main's CLI clone on any host: the server clone
		// would log in as the static user the gateway refuses.
		{name: "gateway on a remote host", server: true, serverHost: "db.example.com", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneCLI},
		{name: "gateway at ::1", server: true, serverHost: "::1", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneCLI},
		{name: "gateway at [::1]", server: true, serverHost: "[::1]", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneCLI},
		{name: "gateway under --external", server: true, external: true, serverHost: "db.example.com", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneExternalServer},
		// --server-user wins over the command's token in init's store
		// (dolt.ApplyGatewayCredential leaves a preset user alone), so the
		// server is no gateway to it: a CLI clone would be stranded.
		{name: "credential command and --server-user on a remote host", server: true, serverHost: "db.example.com", serverUser: "alice", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneExternalServer},
		{name: "credential command and --server-user at ::1", server: true, serverHost: "::1", serverUser: "alice", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneExternalServer},
		{name: "credential command and --server-user on bd's own server", server: true, serverUser: "alice", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "echo tok"}, want: remoteCloneCLI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			endpointCredentialsEnv(t, nil)
			t.Setenv("BEADS_TEST_MODE", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			stubInitCloneProbe(t, tc.deadSocket, tc.tcpLive)
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			cloneCfg := initTimeCloneConfig(tc.server, tc.serverHost, tc.serverPort, tc.serverSocket, tc.serverUser, "init_clone_mode")
			mode := remoteCloneEmbedded
			if tc.server {
				endpoint := func() doltserver.Endpoint { return initCloneEndpoint(beadsDir, cloneCfg, tc.serverUser) }
				mode = initRemoteCloneMode(tc.server, tc.external, endpoint, initCloneGateway(cloneCfg, tc.serverUser))
			}
			if mode != tc.want {
				t.Errorf("initRemoteCloneMode = %v, want %v", mode, tc.want)
			}
		})
	}
}

// stubInitCloneProbe makes every configured socket answer, or none
// (socketDead), and TCP answer at every address, or at none (tcpLive).
func stubInitCloneProbe(t *testing.T, socketDead, tcpLive bool) {
	t.Helper()
	orig := initCloneProbe
	initCloneProbe = func(network, addr string, _ time.Duration) error {
		if (network == "unix" && !socketDead) || (network == "tcp" && tcpLive) {
			return nil
		}
		return fmt.Errorf("stub: nothing answers on %s %s", network, addr)
	}
	t.Cleanup(func() { initCloneProbe = orig })
}

// acceptAndClose accepts on ln, closing each connection at once, until ln
// is closed.
func acceptAndClose(t *testing.T, ln net.Listener) {
	t.Helper()
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
}

// TestInitCloneSocketDeadProbesTheSocket pins initCloneSocketDead to the
// store's socket probe: a socket something listens on is live, a path
// nothing listens on is dead, and no socket is never dead. The transport
// the clone dials (initCloneSocketTransport) is the store's: a live socket,
// else TCP where TCP at the endpoint's port answers, else the socket.
func TestInitCloneSocketDeadProbesTheSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "bdsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	live := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	acceptAndClose(t, ln)
	dead := filepath.Join(dir, "dead.sock")
	if initCloneSocketDead("") {
		t.Error("no socket reported dead")
	}
	if initCloneSocketDead(live) {
		t.Errorf("live socket %s reported dead", live)
	}
	if !initCloneSocketDead(dead) {
		t.Error("socket nothing listens on reported live")
	}

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP loopback unavailable: %v", err)
	}
	acceptAndClose(t, tcp)
	tcpPort := tcp.Addr().(*net.TCPAddr).Port
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	for _, tc := range []struct {
		name   string
		socket string
		port   int
		want   string
	}{
		{name: "no socket", socket: "", port: tcpPort, want: ""},
		{name: "live socket, TCP answering", socket: live, port: tcpPort, want: live},
		{name: "dead socket, TCP answering", socket: dead, port: tcpPort, want: ""},
		{name: "dead socket, nothing on TCP", socket: dead, port: closedPort, want: dead},
		{name: "dead socket, no port", socket: dead, port: 0, want: dead},
	} {
		if got := initCloneSocketTransport(tc.socket, "127.0.0.1", tc.port); got != tc.want {
			t.Errorf("%s: initCloneSocketTransport = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestInitRemoteCloneModeFollowsHostOwnership pins bd init's clone mode to
// configfile.BdRunsServerAt in every spelling of the host
// (doltserver.HostRunsBDServer): a server-mode init without --external
// clones with the dolt CLI into .beads/dolt only where bd runs its own
// server, and through the server init's store opens at any other host, as
// TestInitServerExternalRemoteUsesServerCloneMode pins for the canonical
// spellings. --external clones through the server, and an embedded init
// with the embedded engine, whatever the host.
func TestInitRemoteCloneModeFollowsHostOwnership(t *testing.T) {
	for _, tc := range []struct {
		host string
		want remoteCloneMode // a server-mode init without --external
	}{
		{host: "", want: remoteCloneCLI},
		{host: "127.0.0.1", want: remoteCloneCLI},
		{host: "localhost", want: remoteCloneCLI},
		{host: "LOCALHOST", want: remoteCloneCLI},
		{host: "0.0.0.0", want: remoteCloneCLI},
		{host: "[127.0.0.1]", want: remoteCloneCLI},
		{host: "::ffff:127.0.0.1", want: remoteCloneCLI},
		{host: "::1", want: remoteCloneExternalServer},
		{host: "[::1]", want: remoteCloneExternalServer},
		{host: "0:0:0:0:0:0:0:1", want: remoteCloneExternalServer},
		{host: "::", want: remoteCloneExternalServer},
		{host: "127.0.0.2", want: remoteCloneExternalServer},
		{host: "db.example.com", want: remoteCloneExternalServer},
	} {
		t.Run(tc.host, func(t *testing.T) {
			// bd's own server for the workspace, at tc.host.
			ep := func() doltserver.Endpoint { return doltserver.Endpoint{Host: tc.host} }
			// --external, a gateway and an embedded init decide without the
			// endpoint, so resolving it adds nothing (no deprecation warning
			// of its port sources) to flows that keep main's clone.
			unresolved := func() doltserver.Endpoint {
				t.Errorf("initRemoteCloneMode resolved the endpoint for host %q where its mode does not depend on it", tc.host)
				return doltserver.Endpoint{Host: tc.host}
			}
			if got := initRemoteCloneMode(true, false, ep, false); got != tc.want {
				t.Errorf("initRemoteCloneMode(server, not external) for host %q = %v, want %v", tc.host, got, tc.want)
			}
			if got := initRemoteCloneMode(true, true, unresolved, false); got != remoteCloneExternalServer {
				t.Errorf("initRemoteCloneMode(server, external) for host %q = %v, want the server clone", tc.host, got)
			}
			if got := initRemoteCloneMode(true, false, unresolved, true); got != remoteCloneCLI {
				t.Errorf("initRemoteCloneMode(server, gateway) for host %q = %v, want the CLI clone", tc.host, got)
			}
			if got := initRemoteCloneMode(false, false, unresolved, false); got != remoteCloneEmbedded {
				t.Errorf("initRemoteCloneMode(embedded) for host %q = %v, want embedded", tc.host, got)
			}
		})
	}
}

func TestInitDoltServerTLSFromEnv(t *testing.T) {
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{name: "unset", want: false},
		{name: "zero", env: "0", want: false},
		{name: "false", env: "false", want: false},
		{name: "one", env: "1", want: true},
		{name: "true", env: "true", want: true},
		{name: "mixed case true", env: "TrUe", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				t.Setenv("BEADS_DOLT_SERVER_TLS", "")
			} else {
				t.Setenv("BEADS_DOLT_SERVER_TLS", tc.env)
			}
			if got := initDoltServerTLS(); got != tc.want {
				t.Fatalf("initDoltServerTLS() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitExplicitEmptyRemoteSkipsAmbientConfig(t *testing.T) {
	called := false
	resolveConfiguredRemote := func() string {
		called = true
		return "git+ssh://git@example.com/ambient/repo.git"
	}

	syncURL, source := resolveInitConfiguredSyncRemote("", true, resolveConfiguredRemote)
	if called {
		t.Fatal("explicit empty --remote consulted ambient sync config")
	}
	if syncURL != "" {
		t.Fatalf("syncURL = %q, want empty explicit remote", syncURL)
	}
	if source != initSyncRemoteExplicit {
		t.Fatalf("source = %v, want explicit", source)
	}
	if syncURL != "" {
		t.Fatal("explicit empty --remote would trigger early remote safety")
	}
}

func TestPersistInitSyncRemoteExplicitRemoteWritesTargetDir(t *testing.T) {
	tmpDir := t.TempDir()
	callerDir := filepath.Join(tmpDir, "caller")
	targetBeadsDir := filepath.Join(tmpDir, "target", ".beads")
	callerBeadsDir := filepath.Join(callerDir, ".beads")
	if err := os.MkdirAll(callerBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	callerConfig := filepath.Join(callerBeadsDir, "config.yaml")
	const callerRemote = "git+ssh://git@example.com/wrong/repo.git"
	seedSyncRemote(t, callerBeadsDir, callerRemote)
	targetConfig := filepath.Join(targetBeadsDir, "config.yaml")
	if err := os.WriteFile(targetConfig, []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(callerDir); err != nil {
		t.Fatal(err)
	}

	const remote = "git+ssh://git@example.com/right/repo.git"
	if err := persistInitSyncRemote(targetBeadsDir, remote, remote, false, true, false); err != nil {
		t.Fatalf("persistInitSyncRemote failed: %v", err)
	}

	targetBytes, err := os.ReadFile(targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(targetBytes), remote) {
		t.Fatalf("target config.yaml does not contain explicit remote %q:\n%s", remote, targetBytes)
	}

	callerBytes, err := os.ReadFile(callerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(callerBytes), remote) {
		t.Fatalf("caller config.yaml was modified instead of target:\n%s", callerBytes)
	}
	// Stronger than the absence above: the caller still resolves to what it had.
	// A write that landed somewhere unreadable would satisfy "does not contain
	// the new remote" while having quietly destroyed the old one.
	if got := config.GetStringFromDir(callerBeadsDir, "sync.remote"); got != callerRemote {
		t.Fatalf("caller sync.remote = %q, want it untouched at %q:\n%s", got, callerRemote, callerBytes)
	}
}

// `bd init --remote` runs createConfigYaml and then immediately writes
// sync.remote into what it just created. That template is comments and nothing
// else, which is the one document shape yaml.v3 parses to no nodes at all — so a
// writer that round-trips a node tree has nothing to write back but the key it
// added, and the operator's first commit after init carries a config.yaml with
// its entire documentation deleted. It is git-tracked, so they see it.
//
// This runs the real pair in the real order rather than asserting on a replica
// of the template, so it stays true if either side moves.
func TestInitPersistingSyncRemoteKeepsTheConfigTemplate(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := createConfigYaml(beadsDir, false, ""); err != nil {
		t.Fatalf("createConfigYaml: %v", err)
	}
	template, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	const remote = "git+ssh://git@example.com/right/repo.git"
	if err := persistInitSyncRemote(beadsDir, remote, remote, false, true, false); err != nil {
		t.Fatalf("persistInitSyncRemote: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(template), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(string(body), line) {
			t.Errorf("init dropped a line of its own config template: %q\ngot:\n%s", line, body)
		}
	}
	if got := config.GetStringFromDir(beadsDir, "sync.remote"); got != remote {
		t.Errorf("sync.remote = %q, want %q\n%s", got, remote, body)
	}
}

func TestInitTimeCloneConfigExternalDefaultsAreSelfContained(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent.json"))

	cfg := initTimeCloneConfig(true, "", 3312, "", "", "beads_proj")

	if cfg.GetDoltMode() != configfile.DoltModeServer {
		t.Fatalf("dolt mode = %q, want server", cfg.GetDoltMode())
	}
	if cfg.GetDoltServerHost() != configfile.DefaultDoltServerHost {
		t.Fatalf("host = %q, want default %q", cfg.GetDoltServerHost(), configfile.DefaultDoltServerHost)
	}
	if cfg.GetDoltServerUser() != configfile.DefaultDoltServerUser {
		t.Fatalf("user = %q, want default %q", cfg.GetDoltServerUser(), configfile.DefaultDoltServerUser)
	}
	if cfg.GetDoltServerPort() != 3312 {
		t.Fatalf("port = %d, want 3312", cfg.GetDoltServerPort())
	}
	if cfg.GetDoltDatabase() != "beads_proj" {
		t.Fatalf("database = %q, want beads_proj", cfg.GetDoltDatabase())
	}
}
