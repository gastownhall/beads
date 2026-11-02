package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestInitCloneKeepsTheTestModeGuard: bd init dials its store's server
// outside dolt.New for its clone (initServerCloneEndpoint), so New's
// BEADS_TEST_MODE guard must hold there too. Under BEADS_TEST_MODE=1 an
// ambient production port (the orchestrator's 3307) is refused before
// anything is dialed; a test server's own port is not; and no port at all is
// refused as no address, never dialed at :0 or a default 3307.
func TestInitCloneKeepsTheTestModeGuard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		port    string
		wantErr error
	}{
		{name: "production port", port: "3307", wantErr: dolt.ErrTestModeProductionPort},
		{name: "test server port", port: "13400"},
		{name: "no port", wantErr: dolt.ErrUnresolvedServerPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpointCredentialsEnv(t, nil)
			t.Setenv("BEADS_TEST_MODE", "1")
			t.Setenv("BEADS_TEST_SERVER", "")
			t.Setenv("BEADS_PRODUCTION_PORT", "")
			if tc.port != "" {
				t.Setenv("BEADS_DOLT_SERVER_PORT", tc.port)
			}
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			_, err := initServerCloneEndpoint(beadsDir, initTimeCloneConfig(true, "", 0, "", "", "init_test_mode"))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("initServerCloneEndpoint: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("initServerCloneEndpoint error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestInitRemoteCloneGoesWhereTheStoreOpens drives bd init's clone
// (initCloneFromRemote) for `bd init --server --remote` against a server bd
// does not run, with the server clone and the server's database list
// stubbed. For a host bd's own server never runs at (another machine, ::1
// or [::1]) the clone goes through the server the store opens next, where
// main cloned with the dolt CLI into .beads/dolt, which nothing served, and
// init's store then created an empty database on that server. When that
// server already holds the database there is nothing to clone: init clones
// nothing and its store opens it, as main's store did, where a DOLT_CLONE
// would fail with "database exists". Under --external main's behavior is
// kept: the clone runs whatever the server holds. So is it for a gateway
// (BEADS_DOLT_CREDENTIAL_COMMAND) on any host: init neither asks the server
// nor clones through it, since both would log in as the static user the
// gateway refuses, and init's store opens the gateway's database with the
// token. A --server-user beside the command is the user init's store
// presents instead of the token, so that server is no gateway: the clone
// goes through it as that user. At 127.0.0.1 the same holds for a server some
// other process runs there, on a live unix socket or on the port
// BEADS_DOLT_SERVER_PORT names (which --server-port sets): bd's own server
// does not serve it, so main's CLI clone into .beads/dolt was stranded too.
// A socket nothing answers on gives way to TCP at the port
// BEADS_DOLT_SERVER_PORT names when TCP answers there, as the store's
// transport does (doltserver.SocketTransport), so the probe and the clone
// dial that port, never the socket; with no port, or nothing answering on
// it, main's CLI clone is kept, as it is for a server at 127.0.0.1 that
// cannot be dialed, so the store reports it in its own words. A server
// there that answers and refuses the probe's login is up: the clone goes
// through it, not into .beads/dolt, which that server does not serve.
func TestInitRemoteCloneGoesWhereTheStoreOpens(t *testing.T) {
	const port = "13451"
	socket := filepath.Join(t.TempDir(), "dolt.sock")
	for _, tc := range []struct {
		name       string
		host       string
		socket     string // --server-socket
		socketPort bool   // BEADS_DOLT_SERVER_PORT names a port beside the socket
		external   bool
		gateway    bool
		serverUser string // --server-user
		serverHas  bool
		deadSocket bool   // the socket does not answer
		tcpLive    bool   // TCP answers at the port, where the socket does not
		serverDown bool   // the database probe cannot dial the server
		authFailed bool   // the server answers and refuses the probe's login
		wantClone  string // the transport the server clone dials; "" for none
		wantHeldAt string // the server init found holding the database
	}{
		{name: "remote host", host: "db.example.com", wantClone: "db.example.com:" + port},
		{name: "BEADS_DOLT_SERVER_PORT at 127.0.0.1", host: "127.0.0.1", wantClone: "127.0.0.1:" + port},
		{name: "BEADS_DOLT_SERVER_PORT at 127.0.0.1 already holding the database", host: "127.0.0.1", serverHas: true, wantHeldAt: "127.0.0.1:" + port},
		{name: "unix socket at 127.0.0.1", host: "127.0.0.1", socket: socket, wantClone: "unix:" + socket},
		{name: "unix socket already holding the database", host: "127.0.0.1", socket: socket, serverHas: true, wantHeldAt: "unix:" + socket},
		{name: "dead unix socket at 127.0.0.1", host: "127.0.0.1", socket: socket, deadSocket: true, serverHas: true},
		{name: "dead unix socket at 127.0.0.1 on BEADS_DOLT_SERVER_PORT, nothing answering there", host: "127.0.0.1", socket: socket, socketPort: true, deadSocket: true, serverHas: true},
		// The store opens the server on that port over TCP, which does not
		// serve .beads/dolt.
		{name: "dead unix socket at 127.0.0.1 on BEADS_DOLT_SERVER_PORT, TCP answering", host: "127.0.0.1", socket: socket, socketPort: true, deadSocket: true, tcpLive: true, wantClone: "127.0.0.1:" + port},
		{name: "dead unix socket at 127.0.0.1 on BEADS_DOLT_SERVER_PORT, TCP answering and already holding the database", host: "127.0.0.1", socket: socket, socketPort: true, deadSocket: true, tcpLive: true, serverHas: true, wantHeldAt: "127.0.0.1:" + port},
		{name: "--external, dead unix socket on BEADS_DOLT_SERVER_PORT, TCP answering", host: "127.0.0.1", socket: socket, socketPort: true, external: true, deadSocket: true, tcpLive: true, wantClone: "127.0.0.1:" + port},
		// Nothing answers on the port BEADS_DOLT_SERVER_PORT names at bd's
		// host: main's CLI clone, so init's store open then reports the
		// server that is down with its externally-managed advice.
		{name: "BEADS_DOLT_SERVER_PORT at 127.0.0.1 with nothing listening", host: "127.0.0.1", serverDown: true},
		// A server that answers there is up even when it refuses the
		// probe's login: the clone goes through it, as for any other
		// server bd does not serve.
		{name: "BEADS_DOLT_SERVER_PORT at 127.0.0.1 refusing the probe's login", host: "127.0.0.1", authFailed: true, wantClone: "127.0.0.1:" + port},
		// At a host bd does not run its server at, the clone still goes
		// through that server, as on main.
		{name: "remote host with nothing listening", host: "db.example.com", serverDown: true, wantClone: "db.example.com:" + port},
		{name: "IPv6 loopback ::1", host: "::1", wantClone: "[::1]:" + port},
		{name: "IPv6 loopback [::1]", host: "[::1]", wantClone: "[::1]:" + port},
		{name: "remote host already holding the database", host: "db.example.com", serverHas: true, wantHeldAt: "db.example.com:" + port},
		{name: "--external already holding the database", host: "127.0.0.1", external: true, serverHas: true, wantClone: "127.0.0.1:" + port},
		{name: "gateway on a remote host", host: "db.example.com", gateway: true, serverHas: true},
		{name: "gateway at ::1", host: "::1", gateway: true, serverHas: true},
		{name: "gateway at [::1]", host: "[::1]", gateway: true, serverHas: true},
		{name: "credential command and --server-user on a remote host", host: "db.example.com", gateway: true, serverUser: "alice", wantClone: "db.example.com:" + port},
		{name: "credential command and --server-user, server already holding the database", host: "db.example.com", gateway: true, serverUser: "alice", serverHas: true, wantHeldAt: "db.example.com:" + port},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpointCredentialsEnv(t, nil)
			// As promoteExplicitServerConnFlags exports --server-host and
			// --server-port for the rest of init's run.
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
			if tc.socket != "" {
				// As promoteExplicitServerConnFlags exports --server-socket.
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", tc.socket)
			}
			if tc.socket == "" || tc.socketPort {
				t.Setenv("BEADS_DOLT_SERVER_PORT", port)
			}
			if tc.gateway {
				t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "echo tok123")
			}
			if tc.serverUser != "" {
				// As promoteExplicitServerConnFlags exports --server-user.
				t.Setenv("BEADS_DOLT_SERVER_USER", tc.serverUser)
			}

			stubInitCloneProbe(t, tc.deadSocket, tc.tcpLive)
			origServer, origCLI, origCheck := remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB
			t.Cleanup(func() {
				remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB = origServer, origCLI, origCheck
			})
			var cloned, users []string
			cli := false
			remoteCloneViaCLI = func(context.Context, string, string, string) error {
				cli = true
				return nil
			}
			// dialed names the transport ep's DSN dials: its socket when it
			// names one, which the DSN dials alone, else host:port.
			dialed := func(ep bootstrapServerProbeConfig) string {
				if ep.socket != "" {
					return "unix:" + ep.socket
				}
				return ep.serverLocation()
			}
			probes := 0
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				cloned = append(cloned, dialed(ep))
				users = append(users, ep.user)
				return nil
			}
			checkBootstrapServerDB = func(ep bootstrapServerProbeConfig) bootstrapServerDBCheck {
				probes++
				users = append(users, ep.user)
				if tc.deadSocket && ep.socket != "" {
					t.Errorf("the database probe dialed %s, a socket nothing answers on", dialed(ep))
				}
				if tc.serverDown {
					return bootstrapServerDBCheck{Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}}
				}
				if tc.authFailed {
					return bootstrapServerDBCheck{Err: &mysql.MySQLError{Number: 1045, Message: "Access denied for user 'root'@'localhost' (using password: NO)"}}
				}
				return bootstrapServerDBCheck{Reachable: true, Exists: tc.serverHas}
			}

			beadsDir := filepath.Join(t.TempDir(), ".beads")
			cloneCfg := initTimeCloneConfig(true, tc.host, 0, tc.socket, tc.serverUser, "init_remote_clone")
			disposition, err := initCloneFromRemote(t.Context(), beadsDir, "file:///nonexistent/remote", "init_remote_clone", cloneCfg, true, tc.external, tc.serverUser)

			if tc.serverUser != "" {
				// The probe and the clone log in as the user init's store
				// presents.
				for _, user := range users {
					if user != tc.serverUser {
						t.Errorf("the server probe or clone logged in as %q, want %q, the user init's store presents", user, tc.serverUser)
					}
				}
			}
			var held *initCloneTargetHeldError
			if (tc.gateway && tc.serverUser == "") || (tc.deadSocket && !tc.tcpLive) {
				// main's CLI clone into .beads/dolt; its outcome here (no
				// dolt binary, no remote) is beside the point.
				if errors.As(err, &held) || probes != 0 || len(cloned) != 0 || !cli {
					t.Fatalf("%s: err %v, %d server probes, server clones %v, CLI clone %v; want main's CLI clone, with neither", tc.name, err, probes, cloned, cli)
				}
				return
			}
			if tc.serverDown && tc.wantClone == "" {
				if errors.As(err, &held) || len(cloned) != 0 || !cli {
					t.Fatalf("%s: err %v, server clones %v, CLI clone %v; want main's CLI clone into .beads/dolt only", tc.name, err, cloned, cli)
				}
				return
			}
			if tc.wantHeldAt != "" {
				if !errors.As(err, &held) || held.location != tc.wantHeldAt {
					t.Fatalf("initCloneFromRemote = (%v, %v), want the database found on %s", disposition, err, tc.wantHeldAt)
				}
				if len(cloned) != 0 {
					t.Errorf("cloned through %v into a server that already holds the database", cloned)
				}
				return
			}
			if err != nil {
				t.Fatalf("initCloneFromRemote: %v", err)
			}
			if disposition != initRemoteCloneBootstrapped {
				t.Errorf("disposition = %v, want bootstrapped", disposition)
			}
			if len(cloned) != 1 || cloned[0] != tc.wantClone {
				t.Errorf("server clones %v, want one through %s, the server init's store opens", cloned, tc.wantClone)
			}
			if cli {
				t.Errorf("cloned into .beads/dolt as well, which the server init's store opens does not serve")
			}
		})
	}
}

// TestInitRemoteCloneOnAConfigPort drives bd init's clone
// (initCloneFromRemote) for `bd init --server --remote` where only a
// config.yaml dolt.port, user-level or the workspace's own, names the port.
// Such a port leaves the endpoint bd's own by its mode, yet the server that
// answers there may be one you run over a data directory of your own. Main
// cloned with the dolt CLI into .beads/dolt, which that server does not
// serve, and init's store then created an empty database on it. Now the
// clone goes through that server, and a database it already holds is used
// without cloning. Main's CLI clone is kept when nothing answers on the port
// (bd's own server will serve .beads/dolt), when bd's state files name a live
// dolt sql-server of its own on that port, and in shared-server mode.
func TestInitRemoteCloneOnAConfigPort(t *testing.T) {
	const port = 13452
	for _, tc := range []struct {
		name      string
		where     string // the config.yaml naming dolt.port: "user" or "project"
		answers   bool   // something answers on the port
		bdServer  bool   // bd's state files name a live dolt sql-server on the port
		recycled  bool   // the state files name the port beside a live PID that is not dolt
		shared    bool
		autoOff   bool
		serverHas bool
		wantClone bool // one server clone through 127.0.0.1:port
		wantHeld  bool
	}{
		{name: "user-level dolt.port, a server bd does not run answering", where: "user", answers: true, wantClone: true},
		{name: "project dolt.port, a server bd does not run answering", where: "project", answers: true, wantClone: true},
		{name: "user-level dolt.port, server already holding the database", where: "user", answers: true, serverHas: true, wantHeld: true},
		{name: "project dolt.port, server already holding the database", where: "project", answers: true, serverHas: true, wantHeld: true},
		{name: "user-level dolt.port, auto-start off, a server answering", where: "user", answers: true, autoOff: true, wantClone: true},
		{name: "user-level dolt.port, stale state files with a recycled PID, a server answering", where: "user", answers: true, recycled: true, wantClone: true},
		{name: "user-level dolt.port, nothing answering", where: "user"},
		{name: "project dolt.port, nothing answering", where: "project"},
		{name: "user-level dolt.port, auto-start off, nothing answering", where: "user", autoOff: true},
		{name: "user-level dolt.port, bd's own server answering", where: "user", answers: true, bdServer: true},
		{name: "shared server, user-level dolt.port, answering", where: "user", answers: true, shared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpointCredentialsEnv(t, nil)
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "")
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
				t.Setenv("BEADS_SHARED_SERVER_DIR", filepath.Join(t.TempDir(), "shared"))
			}
			if tc.autoOff {
				t.Setenv("BEADS_DOLT_AUTO_START", "0")
			}
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			cfgPath := filepath.Join(os.Getenv("HOME"), ".config", "bd", "config.yaml")
			if tc.where == "project" {
				cfgPath = filepath.Join(beadsDir, "config.yaml")
			}
			for _, dir := range []string{beadsDir, filepath.Dir(cfgPath)} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("dolt:\n  port: %d\n", port)), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.bdServer || tc.recycled {
				pid := os.Getpid()
				if tc.bdServer {
					pid, _ = startStandInDoltServer(t)
				}
				if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PortFileName), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PIDFileName), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(root)
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			stubInitCloneProbe(t, false, tc.answers)
			origServer, origCLI, origCheck := remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB
			t.Cleanup(func() {
				remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB = origServer, origCLI, origCheck
			})
			cli := false
			var cloned []string
			remoteCloneViaCLI = func(context.Context, string, string, string) error {
				cli = true
				return nil
			}
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				cloned = append(cloned, ep.serverLocation())
				return nil
			}
			checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
				return bootstrapServerDBCheck{Reachable: true, Exists: tc.serverHas}
			}

			cloneCfg := initTimeCloneConfig(true, "", 0, "", "", "init_config_port")
			_, err := initCloneFromRemote(t.Context(), beadsDir, "file:///nonexistent/remote", "init_config_port", cloneCfg, true, false, "")
			want := fmt.Sprintf("127.0.0.1:%d", port)
			var held *initCloneTargetHeldError
			switch {
			case tc.wantHeld:
				if !errors.As(err, &held) || held.location != want || len(cloned) != 0 || cli {
					t.Fatalf("err %v, server clones %v, CLI clone %v; want the database %s holds used, with no clone", err, cloned, cli, want)
				}
			case tc.wantClone:
				if err != nil || len(cloned) != 1 || cloned[0] != want || cli {
					t.Fatalf("err %v, server clones %v, CLI clone %v; want one server clone through %s and no clone into .beads/dolt", err, cloned, cli, want)
				}
			default:
				// main's CLI clone into .beads/dolt; its outcome here (no
				// dolt binary, no remote) is beside the point.
				if errors.As(err, &held) || len(cloned) != 0 || !cli {
					t.Fatalf("err %v, server clones %v, CLI clone %v; want main's CLI clone", err, cloned, cli)
				}
			}
		})
	}
}

// TestInitExternalRemoteWithNoPortAsksForThePort: bd init --server
// --external --remote with no port named refuses to clone with the advice
// its store open gives --external (name the port), and never offers `bd dolt
// start`, which would start bd's own server over .beads/dolt for a server
// --external says bd does not run. On main the clone fell back to
// GetDoltServerPort's 3307 and cloned into 127.0.0.1:3307, a server no
// later command dials, before the store refused the unresolved port.
func TestInitExternalRemoteWithNoPortAsksForThePort(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	env := append(removedBackendTestEnv(beadsDir),
		"BD_DISABLE_METRICS=1", "DO_NOT_TRACK=1", "BD_NON_INTERACTIVE=1",
		"HOME="+t.TempDir(), "PATH="+t.TempDir())
	cmd := exec.Command(bd, "init", "--server", "--external", "--remote", "file://"+filepath.ToSlash(filepath.Join(root, "no-remote")), "--prefix", "ext", "--quiet")
	cmd.Dir = root
	cmd.Env = env
	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)
	if err == nil {
		t.Fatalf("bd init --external with no port succeeded:\n%s", out)
	}
	for _, want := range []string{"no Dolt server port resolved", "--external declares the Dolt server externally managed", "--server-port"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal does not say %q:\n%s", want, out)
		}
	}
	// The output names the test's own --remote path, whose random temp
	// suffix can contain any digits: check only what bd wrote.
	bdText := withoutTestPaths(out, root)
	for _, banned := range []string{"bd dolt start", "127.0.0.1:0", "3307"} {
		if strings.Contains(bdText, banned) {
			t.Errorf("refusal contains %q:\n%s", banned, out)
		}
	}
}

// TestInitIPv6LoopbackRemoteWithNoPortAsksForThePort: bd init --server
// --server-host ::1 --remote with no port named refuses before cloning with
// the advice init's store gives a server bd does not run with no port: pin
// it. It never suggests omitting --remote, since the URL is not what failed.
func TestInitIPv6LoopbackRemoteWithNoPortAsksForThePort(t *testing.T) {
	bd := buildBDForInitTests(t)
	for _, host := range []string{"::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			env := append(removedBackendTestEnv(beadsDir),
				"BD_DISABLE_METRICS=1", "DO_NOT_TRACK=1", "BD_NON_INTERACTIVE=1",
				"HOME="+t.TempDir(), "PATH="+t.TempDir())
			cmd := exec.Command(bd, "init", "--server", "--server-host", host, "--remote", "file://"+filepath.ToSlash(filepath.Join(root, "no-remote")), "--prefix", "v6", "--quiet")
			cmd.Dir = root
			cmd.Env = env
			outBytes, err := cmd.CombinedOutput()
			out := string(outBytes)
			if err == nil {
				t.Fatalf("bd init --server-host %s with no port succeeded:\n%s", host, out)
			}
			for _, want := range []string{"no Dolt server port resolved", "pin its port"} {
				if !strings.Contains(out, want) {
					t.Errorf("refusal does not say %q:\n%s", want, out)
				}
			}
			for _, banned := range []string{"omit --remote", "bd dolt start"} {
				if strings.Contains(out, banned) {
					t.Errorf("refusal contains %q:\n%s", banned, out)
				}
			}
		})
	}
}

// TestInitCloneDialsTheHostInitsStoreOpens: bd init --server with no
// --server-host opens its store at BEADS_DOLT_SERVER_HOST or 127.0.0.1
// (initStoreServerHost), whatever dolt.host the loaded config names, so its
// clone dials that host too, with that host's password. The clone config
// names no dolt_server_host: a 127.0.0.1 stamped there would land in the
// git-tracked metadata.json, below the config.yaml dolt.host that now
// outranks it (GH#5961).
func TestInitCloneDialsTheHostInitsStoreOpens(t *testing.T) {
	const port = 13400
	endpointCredentialsEnv(t, []string{"127.0.0.1", "192.0.2.50"}, port)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	userCfg := filepath.Join(os.Getenv("HOME"), ".config", "bd", "config.yaml")
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	for _, dir := range []string{beadsDir, filepath.Dir(userCfg)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(userCfg, []byte("dolt:\n  host: 192.0.2.50\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	cloneCfg := initTimeCloneConfig(true, "", 0, "", "", "init_clone_host")
	if cloneCfg.DoltServerHost != "" {
		t.Errorf("clone config dolt_server_host = %q, want none", cloneCfg.DoltServerHost)
	}
	wantPass := fmt.Sprintf("127.0.0.1-%d", port)
	ep, err := initServerCloneEndpoint(beadsDir, cloneCfg)
	if err != nil {
		t.Fatalf("initServerCloneEndpoint: %v", err)
	}
	if ep.host != "127.0.0.1" || ep.port != port || ep.pass != wantPass {
		t.Errorf("clone endpoint = %s:%d password %q, want 127.0.0.1:%d password %q", ep.host, ep.port, ep.pass, port, wantPass)
	}
	if got := initCloneEndpoint(beadsDir, cloneCfg, ""); got.Host != "127.0.0.1" || got.Port != port || got.Password() != wantPass {
		t.Errorf("clone mode endpoint = %s password %q, want 127.0.0.1:%d password %q", got.Addr(), got.Password(), port, wantPass)
	}
}
