//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestInitCloneRemoteHostOwnership pins bd init's remote clone
// (initCloneFromRemote) for every spelling of the host. Without --external
// it clones with the dolt CLI into .beads/dolt only for a host bd runs its
// server at (configfile.BdRunsServerAt, through initRemoteCloneMode). For
// any other host (the IPv6 loopback in any spelling, another loopback
// address, another machine) the clone goes through the server init's store
// opens next, as with --external, on the port that store dials
// (initServerCloneEndpoint). When nothing names a port for that server the
// clone refuses before dialing anything, wrapping
// dolt.ErrUnresolvedServerPort, rather than DOLT_CLONE onto whatever
// listens on a default port, and init gives the store's advice for that
// server (initUnresolvedClonePortAdvice): pin the port of a server bd does
// not run, or, under --external, name it. A stale port file, the port of
// bd's last server on 127.0.0.1, names no port for a host bd does not run,
// so it changes neither the refusal nor the port, which init's store dials
// too; for bd's own host it names the port. The server here does not hold
// the database yet (TestInitRemoteCloneGoesWhereTheStoreOpens covers one
// that does).
func TestInitCloneRemoteHostOwnership(t *testing.T) {
	const flagPort, stalePort = 42001, 14444
	for _, tc := range []struct {
		name string
		host string
		// hostFlag: the host is --server-host; otherwise it is the caller's
		// own BEADS_DOLT_SERVER_HOST.
		hostFlag bool
		port     int  // --server-port
		portFile bool // .beads/dolt-server.port names stalePort
		socket   string
		external bool
		// want is "cli" (dolt CLI clone into .beads/dolt), "server" or
		// "refuse".
		want     string
		wantPort int // for "server"; 0 = not asserted
	}{
		{name: "default host", host: "", want: "cli"},
		{name: "127.0.0.1", host: "127.0.0.1", hostFlag: true, want: "cli"},
		{name: "LOCALHOST", host: "LOCALHOST", hostFlag: true, want: "cli"},
		{name: "::1 no port", host: "::1", hostFlag: true, want: "refuse"},
		{name: "[::1] no port", host: "[::1]", hostFlag: true, want: "refuse"},
		{name: "0:0:0:0:0:0:0:1 no port", host: "0:0:0:0:0:0:0:1", hostFlag: true, want: "refuse"},
		{name: "::1 from the environment no port", host: "::1", want: "refuse"},
		{name: "::1 --server-port", host: "::1", hostFlag: true, port: flagPort, want: "server", wantPort: flagPort},
		{name: "127.0.0.2 no port", host: "127.0.0.2", hostFlag: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --server-port", host: "db.example.com", hostFlag: true, port: flagPort, want: "server", wantPort: flagPort},
		{name: "::1 --external no port", host: "::1", hostFlag: true, external: true, want: "refuse"},
		{name: "[::1] --external no port", host: "[::1]", hostFlag: true, external: true, want: "refuse"},
		{name: "0:0:0:0:0:0:0:1 --external no port", host: "0:0:0:0:0:0:0:1", hostFlag: true, external: true, want: "refuse"},
		{name: "::1 from the environment --external no port", host: "::1", external: true, want: "refuse"},
		{name: "::1 --external --server-port", host: "::1", hostFlag: true, port: flagPort, external: true, want: "server", wantPort: flagPort},
		{name: "[::1] --external --server-port", host: "[::1]", hostFlag: true, port: flagPort, external: true, want: "server", wantPort: flagPort},
		{name: "::1 --external socket no port", host: "::1", hostFlag: true, socket: "/tmp/bd-test-dolt.sock", external: true, want: "server"},
		{name: "127.0.0.2 --external no port", host: "127.0.0.2", hostFlag: true, external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --external no port", host: "db.example.com", external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --external --server-port", host: "db.example.com", hostFlag: true, port: flagPort, external: true, want: "server", wantPort: flagPort},
		{name: "127.0.0.1 --external no port", host: "127.0.0.1", hostFlag: true, external: true, want: "refuse"},
		{name: "::1 stale port file", host: "::1", hostFlag: true, portFile: true, want: "refuse"},
		{name: "127.0.0.2 stale port file", host: "127.0.0.2", hostFlag: true, portFile: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "::1 --external stale port file", host: "::1", hostFlag: true, portFile: true, external: true, want: "refuse"},
		{name: "0:0:0:0:0:0:0:1 --external stale port file", host: "0:0:0:0:0:0:0:1", hostFlag: true, portFile: true, external: true, want: "refuse"},
		{name: "::1 from the environment --external stale port file", host: "::1", portFile: true, external: true, want: "refuse"},
		{name: "::1 --external --server-port stale port file", host: "::1", hostFlag: true, port: flagPort, portFile: true, external: true, want: "server", wantPort: flagPort},
		{name: "127.0.0.2 --external stale port file", host: "127.0.0.2", hostFlag: true, portFile: true, external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --external stale port file", host: "db.example.com", hostFlag: true, portFile: true, external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote from the environment --external stale port file", host: "db.example.com", portFile: true, external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "127.0.0.1 --external port file", host: "127.0.0.1", hostFlag: true, portFile: true, external: true, want: "server", wantPort: stalePort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			// The store's test-mode guard turns a missing or production port
			// into 1; nothing here opens a store, and the ports compared are
			// the ones a real init dials.
			t.Setenv("BEADS_TEST_MODE", "")
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), "# Beads Config\n")
			if tc.portFile {
				writeClonePortFile(t, filepath.Join(beadsDir, doltserver.PortFileName), strconv.Itoa(stalePort))
			}
			// What promoteExplicitServerConnFlags does with --server-host,
			// --server-port and --server-socket before init clones.
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
			if tc.port > 0 {
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tc.port))
			}
			t.Setenv("BEADS_DOLT_SERVER_SOCKET", tc.socket)
			// A credential command would make the server a gateway, which
			// keeps the CLI clone on any host.
			t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "")
			loadClonePortConfig(t, beadsDir)

			var server *bootstrapServerProbeConfig
			cli := false
			origServer, origCLI, origCheck := remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB
			t.Cleanup(func() {
				remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB = origServer, origCLI, origCheck
			})
			// The server init clones through does not hold the database yet.
			checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
				return bootstrapServerDBCheck{Reachable: true}
			}
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				server = &ep
				return nil
			}
			remoteCloneViaCLI = func(context.Context, string, string, string) error {
				cli = true
				return nil
			}

			flagHost := ""
			if tc.hostFlag {
				flagHost = tc.host
			}
			cloneCfg := initTimeCloneConfig(true, flagHost, tc.port, tc.socket, "", "init_clone")
			_, err := initCloneFromRemote(t.Context(), beadsDir, "file:///nonexistent/remote", "init_clone", cloneCfg, true, tc.external, "")

			switch tc.want {
			case "cli":
				if err != nil || !cli || server != nil {
					t.Fatalf("err=%v cli=%v server=%+v, want a dolt CLI clone into .beads/dolt only", err, cli, server)
				}
				return
			case "refuse":
				if !errors.Is(err, dolt.ErrUnresolvedServerPort) {
					t.Fatalf("err = %v, want the unresolved-port refusal", err)
				}
				advice, wantAdvice := initUnresolvedClonePortAdvice(err, tc.external, cloneCfg.GetDoltServerHost()), "pin its port"
				if tc.external {
					wantAdvice = "--server-port"
				}
				if !strings.Contains(advice, wantAdvice) {
					t.Errorf("init's advice = %q, want it to say %q", advice, wantAdvice)
				}
				if cli || server != nil {
					t.Fatalf("cloned anyway (cli=%v server=%+v), want no clone", cli, server)
				}
				return
			}
			if err != nil || cli || server == nil {
				t.Fatalf("err=%v cli=%v server=%+v, want a clone on the server only", err, cli, server)
			}
			if server.host != tc.host && !(tc.host == "" && server.host == configfile.DefaultDoltServerHost) {
				t.Errorf("clone dialed host %q, want %q", server.host, tc.host)
			}
			if server.socket != tc.socket {
				t.Errorf("clone dialed socket %q, want %q", server.socket, tc.socket)
			}
			if tc.wantPort == 0 {
				return
			}
			if server.port != tc.wantPort {
				t.Errorf("clone dialed port %d, want %d", server.port, tc.wantPort)
			}
			storeCfg := initStorePortConfig(beadsDir, initStoreServerHost(flagHost))
			if tc.port > 0 {
				storeCfg.ServerPort, storeCfg.ServerPortSource = tc.port, doltserver.PortSourceCallerExplicit
			}
			dolt.ResolveServerPort(storeCfg)
			if storeCfg.ServerPort != server.port {
				t.Errorf("clone dialed port %d, but init's store dials %d", server.port, storeCfg.ServerPort)
			}
		})
	}
}

// TestInitStoreServerPortHostOwnership pins the port bd init's store dials,
// initStoreServerPort's port with the store's rule applied
// (dolt.ResolveServerPort), beside a stale port file, the port of bd's last
// server on 127.0.0.1. The file names that port only for a host bd runs its
// server at (configfile.BdRunsServerAt). At any other host init's store, like
// every later command (doltserver.DefaultConfig), resolves the port without
// it: an IPv6-loopback host in owned mode then has none, and the store
// refuses with the advice to pin one; another loopback address or another
// machine is an external server by its host, on the documented 3307.
// BEADS_DOLT_SERVER_PORT names the port at every host.
func TestInitStoreServerPortHostOwnership(t *testing.T) {
	const stalePort, envPort = 14444, 42002
	for _, tc := range []struct {
		host string
		want int
	}{
		{host: "", want: stalePort},
		{host: "127.0.0.1", want: stalePort},
		{host: "LOCALHOST", want: stalePort},
		{host: "[127.0.0.1]", want: stalePort},
		{host: "::ffff:127.0.0.1", want: stalePort},
		{host: "0.0.0.0", want: stalePort},
		{host: "::1", want: 0},
		{host: "[::1]", want: 0},
		{host: "0:0:0:0:0:0:0:1", want: 0},
		{host: "::", want: 0},
		{host: "127.0.0.2", want: configfile.DefaultDoltServerPort},
		{host: "db.example.com", want: configfile.DefaultDoltServerPort},
	} {
		// hostFlag: the host is --server-host; otherwise it is the caller's
		// own BEADS_DOLT_SERVER_HOST.
		for _, hostFlag := range []bool{true, false} {
			if hostFlag && tc.host == "" {
				continue // --server-host cannot be empty
			}
			name := "BEADS_DOLT_SERVER_HOST=" + tc.host
			if hostFlag {
				name = "--server-host " + tc.host
			}
			t.Run(name, func(t *testing.T) {
				clonePortTestEnv(t)
				// The store's test-mode guard turns a missing or production
				// port into 1; the ports compared are the ones a real init
				// dials.
				t.Setenv("BEADS_TEST_MODE", "")
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), "# Beads Config\n")
				writeClonePortFile(t, filepath.Join(beadsDir, doltserver.PortFileName), strconv.Itoa(stalePort))
				// What promoteExplicitServerConnFlags does with --server-host.
				t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
				loadClonePortConfig(t, beadsDir)
				flagHost := ""
				if hostFlag {
					flagHost = tc.host
				}
				storePort := func() int {
					storeCfg := initStorePortConfig(beadsDir, initStoreServerHost(flagHost))
					dolt.ResolveServerPort(storeCfg)
					return storeCfg.ServerPort
				}

				if got := storePort(); got != tc.want {
					t.Errorf("init's store dials port %d, want %d", got, tc.want)
				}
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(envPort))
				if got := storePort(); got != envPort {
					t.Errorf("with BEADS_DOLT_SERVER_PORT, init's store dials port %d, want %d", got, envPort)
				}
			})
		}
	}
}

// TestInitStoreServerPortReadsTheNewWorkspace pins the port bd init's store
// dials at a host bd does not run its server at, for a workspace nested in one
// whose config.yaml names dolt.port, the port of that workspace's server. Init
// loads that file before its own .beads exists, but every later command in the
// new workspace reads the new workspace's own configuration, which names no
// port, and so does init's summary. Init's store, and its --external clone,
// resolve the port from that configuration too, with or without a stale port
// file: an IPv6-loopback host has none, so the store refuses with the advice
// to pin one and the clone refuses before dialing anything; another loopback
// address or another machine is an external server by its host, on the
// documented 3307. The summary names the same port.
func TestInitStoreServerPortReadsTheNewWorkspace(t *testing.T) {
	const enclosingPort, stalePort = 13999, 14444
	for _, tc := range []struct {
		host string
		want int
	}{
		{host: "::1", want: 0},
		{host: "[::1]", want: 0},
		{host: "0:0:0:0:0:0:0:1", want: 0},
		{host: "::", want: 0},
		{host: "127.0.0.2", want: configfile.DefaultDoltServerPort},
		{host: "db.example.com", want: configfile.DefaultDoltServerPort},
	} {
		// hostFlag: the host is --server-host; otherwise it is the caller's
		// own BEADS_DOLT_SERVER_HOST.
		for _, hostFlag := range []bool{true, false} {
			for _, portFile := range []bool{true, false} {
				name := "BEADS_DOLT_SERVER_HOST=" + tc.host
				if hostFlag {
					name = "--server-host " + tc.host
				}
				if portFile {
					name += "/stale port file"
				}
				t.Run(name, func(t *testing.T) {
					clonePortTestEnv(t)
					t.Setenv("BEADS_CEILING_DIRECTORIES", "")
					// The store's test-mode guard turns a missing or production
					// port into 1; the ports compared are the ones a real init
					// dials.
					t.Setenv("BEADS_TEST_MODE", "")
					town := t.TempDir()
					writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"), fmt.Sprintf("dolt:\n  port: %d\n", enclosingPort))
					rig := filepath.Join(town, "rig")
					beadsDir := filepath.Join(rig, ".beads")
					if err := os.MkdirAll(rig, 0o700); err != nil {
						t.Fatal(err)
					}
					if portFile {
						writeClonePortFile(t, filepath.Join(beadsDir, doltserver.PortFileName), strconv.Itoa(stalePort))
					}
					// What promoteExplicitServerConnFlags does with --server-host.
					t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
					// What init loads before it creates the rig's .beads.
					t.Chdir(rig)
					t.Setenv("BEADS_DIR", "")
					config.ResetForTesting()
					if err := config.Initialize(); err != nil {
						t.Fatalf("config.Initialize: %v", err)
					}
					if got := doltserver.DefaultConfig(beadsDir).Port; got != enclosingPort {
						t.Fatalf("precondition: DefaultConfig under the enclosing config.yaml = port %d, want its dolt.port %d", got, enclosingPort)
					}
					flagHost := ""
					if hostFlag {
						flagHost = tc.host
					}

					storeCfg := initStorePortConfig(beadsDir, initStoreServerHost(flagHost))
					dolt.ResolveServerPort(storeCfg)
					if storeCfg.ServerPort != tc.want {
						t.Errorf("init's store dials port %d, want %d", storeCfg.ServerPort, tc.want)
					}
					clone, err := initServerCloneEndpoint(beadsDir, initTimeCloneConfig(true, flagHost, 0, "", "", "rig"))
					switch {
					case tc.want == 0 && !errors.Is(err, dolt.ErrUnresolvedServerPort):
						t.Errorf("--external clone: port %d, err %v; want the unresolved-port refusal", clone.port, err)
					case tc.want > 0 && (err != nil || clone.port != tc.want):
						t.Errorf("--external clone: port %d, err %v; want port %d", clone.port, err, tc.want)
					}

					// What init writes before it prints the summary.
					meta := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "rig", DoltServerHost: flagHost}
					if err := os.MkdirAll(beadsDir, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := meta.Save(beadsDir); err != nil {
						t.Fatal(err)
					}
					if err := createConfigYaml(beadsDir, false, ""); err != nil {
						t.Fatal(err)
					}
					want := tc.host + " (no port pinned)"
					if tc.want > 0 {
						want = doltutil.PrintedServerAddr(tc.host, tc.want)
					}
					if got := initSummaryServerAddr(beadsDir, flagHost, 0); got != want {
						t.Errorf("initSummaryServerAddr = %q, want %q", got, want)
					}
				})
			}
		}
	}
}

// TestInitStoreServerPortAtBDHostIgnoresTheEnclosingPort pins the port bd
// init's store dials at 127.0.0.1, bd's own host, for a workspace nested in
// one whose config.yaml names dolt.port, the port of that workspace's server.
// Init loads that file before its own .beads exists, and on main the store
// filled the port nothing else named from it (dolt.ResolveServerPort), so the
// new database was created on the enclosing workspace's server, and its port
// pinned in the new workspace's port file. Now that port is resolved from the
// new workspace's own configuration: with none named it is 0, which init
// starts a server of the workspace's own for (initStartOwnServer), and its
// --external clone refuses the unresolved port. A port the new workspace
// names, or that the caller names (BEADS_DOLT_SERVER_PORT, which
// --server-port sets, or the legacy BEADS_DOLT_PORT), is dialed as on main,
// and so is a dolt.port that is the new workspace's own: a re-init, or one
// in the user-level config.yaml.
func TestInitStoreServerPortAtBDHostIgnoresTheEnclosingPort(t *testing.T) {
	const enclosingPort, ownPort, userPort, envPort = 13999, 14555, 14666, 42003
	for _, tc := range []struct {
		name         string
		nested       bool              // the workspace is nested in one whose config.yaml names enclosingPort
		ownConfig    string            // the new workspace's own config.yaml, "" for none
		userConfig   string            // the user-level config.yaml, "" for none
		env          map[string]string // the caller's environment
		want         int
		wantResolved bool // the store fills in no port
	}{
		{name: "nested, no port of its own", nested: true, want: 0, wantResolved: true},
		{name: "nested, user-level dolt.port", nested: true, userConfig: fmt.Sprintf("dolt:\n  port: %d\n", userPort), want: userPort, wantResolved: true},
		{name: "nested, BEADS_DOLT_SERVER_PORT", nested: true, env: map[string]string{"BEADS_DOLT_SERVER_PORT": strconv.Itoa(enclosingPort)}, want: enclosingPort},
		{name: "nested, legacy BEADS_DOLT_PORT", nested: true, env: map[string]string{"BEADS_DOLT_PORT": strconv.Itoa(enclosingPort)}, want: enclosingPort},
		{name: "nested, a dolt.port of its own", nested: true, ownConfig: fmt.Sprintf("dolt:\n  port: %d\n", ownPort), want: ownPort},
		{name: "not nested, a dolt.port of its own", ownConfig: fmt.Sprintf("dolt:\n  port: %d\n", ownPort), want: ownPort},
		{name: "not nested, user-level dolt.port", userConfig: fmt.Sprintf("dolt:\n  port: %d\n", userPort), want: userPort},
		{name: "not nested, no port", want: 0},
	} {
		for _, hostFlag := range []bool{true, false} {
			name := tc.name
			if hostFlag {
				name += "/--server-host 127.0.0.1"
			}
			t.Run(name, func(t *testing.T) {
				clonePortTestEnv(t)
				t.Setenv("BEADS_CEILING_DIRECTORIES", "")
				t.Setenv("BEADS_TEST_MODE", "")
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				if tc.userConfig != "" {
					writeClonePortFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bd", "config.yaml"), tc.userConfig)
				}
				town := t.TempDir()
				dir := town
				if tc.nested {
					writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"), fmt.Sprintf("dolt:\n  port: %d\n", enclosingPort))
					dir = filepath.Join(town, "rig")
				}
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if tc.ownConfig != "" {
					writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), tc.ownConfig)
				}
				// What init loads before it creates the workspace's .beads.
				t.Chdir(dir)
				t.Setenv("BEADS_DIR", "")
				config.ResetForTesting()
				if err := config.Initialize(); err != nil {
					t.Fatalf("config.Initialize: %v", err)
				}
				flagHost := ""
				if hostFlag {
					flagHost = "127.0.0.1"
					// What promoteExplicitServerConnFlags does with --server-host.
					t.Setenv("BEADS_DOLT_SERVER_HOST", flagHost)
				}

				storeCfg := initStorePortConfig(beadsDir, initStoreServerHost(flagHost))
				dolt.ResolveServerPort(storeCfg)
				if storeCfg.ServerPort != tc.want {
					t.Errorf("init's store dials port %d, want %d", storeCfg.ServerPort, tc.want)
				}
				if storeCfg.ServerPortResolved != tc.wantResolved {
					t.Errorf("init's store port resolved = %v, want %v", storeCfg.ServerPortResolved, tc.wantResolved)
				}
				clone, err := initServerCloneEndpoint(beadsDir, initTimeCloneConfig(true, flagHost, 0, "", "", "rig"))
				switch {
				case tc.want == 0 && !errors.Is(err, dolt.ErrUnresolvedServerPort):
					t.Errorf("--external clone: port %d, err %v; want the unresolved-port refusal", clone.port, err)
				case tc.want > 0 && (err != nil || clone.port != tc.want):
					t.Errorf("--external clone: port %d, err %v; want port %d", clone.port, err, tc.want)
				}
			})
		}
	}
}

// TestInitServerAtIPv6LoopbackDialsNoStalePort runs the real bd binary and
// checks the port bd init's own store dials at a host bd does not run its
// server at: the IPv6 loopback, named by --server-host or by
// BEADS_DOLT_SERVER_HOST. Each case offers one stale port, the port of a live
// listener at [::1]: the project's port file, which records the port of bd's
// last server on 127.0.0.1, or the dolt.port of a workspace the project is
// nested in, whose config.yaml init loads before its own .beads exists.
// Neither names the port of the server at ::1, so init must dial neither. It
// fails having connected to nothing and launched no dolt, and outside test
// mode it gives the store's advice to pin the port. BEADS_TEST_SERVER=1 keeps
// the test-mode guard from turning a port-file port into port 1, which would
// hide a dial of it.
func TestInitServerAtIPv6LoopbackDialsNoStalePort(t *testing.T) {
	for _, nested := range []bool{false, true} {
		// hostFlag: the host is --server-host; otherwise it is the caller's
		// own BEADS_DOLT_SERVER_HOST.
		for _, hostFlag := range []bool{true, false} {
			for _, testMode := range []bool{true, false} {
				name := "port file"
				if nested {
					name = "enclosing dolt.port"
				}
				if hostFlag {
					name += "/--server-host ::1"
				} else {
					name += "/BEADS_DOLT_SERVER_HOST=::1"
				}
				if !testMode {
					name += "/BEADS_TEST_MODE unset"
				}
				t.Run(name, func(t *testing.T) {
					port, accepted := testutil.IPv6LoopbackListener(t)
					f := newExternalInitFixture(t)
					if nested {
						town := t.TempDir()
						writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"), fmt.Sprintf("dolt:\n  port: %d\n", port))
						f.repoDir = filepath.Join(town, "rig")
						initGitRepo(t, f.repoDir)
						// The search for config.yaml examines the town and
						// nothing above it.
						f.env = append(f.env, "BEADS_CEILING_DIRECTORIES="+filepath.Dir(town))
					} else {
						writeClonePortFile(t, filepath.Join(f.repoDir, ".beads", doltserver.PortFileName), strconv.Itoa(port))
					}
					f.env = append(f.env, "BEADS_TEST_SERVER=1")
					if !testMode {
						f.env = append(f.env, "BEADS_TEST_MODE=")
					}
					args := []string{"init", "--quiet", "--server", "--prefix", "x", "--skip-hooks", "--skip-agents"}
					if hostFlag {
						args = append(args, "--server-host", "::1")
					} else {
						f.env = append(f.env, "BEADS_DOLT_SERVER_HOST=::1")
					}

					res := f.run("", args...)

					if !res.failed {
						t.Errorf("bd init succeeded with no port named for the server at ::1:\n%s", res.output)
					}
					requireNoLaunch(t, res)
					if n := accepted(); n != 0 {
						t.Errorf("bd init made %d connections to the stale port [::1]:%d:\n%s", n, port, res.output)
					}
					if !testMode && !strings.Contains(res.output, "pin its port") {
						t.Errorf("output does not give the advice to pin the port:\n%s", res.output)
					}
				})
			}
		}
	}
}

// initStorePortConfig is a store config with the port bd init puts on its
// store's config for the server at host: initStoreServerPort's port, its
// source and its flags, before the store applies its rule.
func initStorePortConfig(beadsDir, host string) *dolt.Config {
	cfg := &dolt.Config{BeadsDir: beadsDir}
	cfg.ServerPort, cfg.ServerPortSource, cfg.ServerPortSharedServer, cfg.ServerPortResolved = initStoreServerPort(beadsDir, host)
	return cfg
}

// TestInitSummaryServerAddrReadsTheNewWorkspace pins the server address bd
// init's summary prints for a workspace nested in one whose config.yaml names
// dolt: {mode: server, host: H}. Init loads that file before its own .beads
// exists, but every later command in the new workspace reads the new
// workspace's own configuration, which names no host, and dials bd's server
// on the port in its port file. doltserver.DefaultConfig ignores that port
// file for a host bd does not run its server at, so read under the enclosing
// configuration it gives no port; the summary must name the port later
// commands find. With no port file nothing names a port, and it says so.
func TestInitSummaryServerAddrReadsTheNewWorkspace(t *testing.T) {
	const started, flagPort = 14444, 42001
	for _, host := range []string{"127.0.0.1", "::1", "[::1]", "0:0:0:0:0:0:0:1", "::", "127.0.0.2", "db.example.com"} {
		for _, portFile := range []bool{true, false} {
			name := host + "/port file"
			if !portFile {
				name = host + "/no port file"
			}
			t.Run(name, func(t *testing.T) {
				clonePortTestEnv(t)
				t.Setenv("BEADS_CEILING_DIRECTORIES", "")
				town := t.TempDir()
				writeClonePortFile(t, filepath.Join(town, ".beads", "config.yaml"),
					fmt.Sprintf("dolt:\n  mode: server\n  host: %q\n", host))
				rig := filepath.Join(town, "rig")
				beadsDir := filepath.Join(rig, ".beads")
				if err := os.MkdirAll(rig, 0o700); err != nil {
					t.Fatal(err)
				}
				// What init loads before it creates the rig's .beads.
				t.Chdir(rig)
				t.Setenv("BEADS_DIR", "")
				config.ResetForTesting()
				if err := config.Initialize(); err != nil {
					t.Fatalf("config.Initialize: %v", err)
				}
				// What init writes, and the port file of the server it started.
				meta := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "rig"}
				if err := os.MkdirAll(beadsDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := meta.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
				if err := createConfigYaml(beadsDir, false, ""); err != nil {
					t.Fatal(err)
				}
				if portFile {
					writeClonePortFile(t, filepath.Join(beadsDir, doltserver.PortFileName), strconv.Itoa(started))
				}
				if got := doltserver.DefaultConfig(beadsDir).Port; portFile && !configfile.BdRunsServerAt(host) && got != 0 {
					t.Fatalf("precondition: DefaultConfig under the enclosing config.yaml = port %d, want 0 (bd's port file ignored for host %q)", got, host)
				}

				const summaryHost = configfile.DefaultDoltServerHost
				if got, want := initSummaryServerAddr(beadsDir, summaryHost, flagPort), doltutil.PrintedServerAddr(summaryHost, flagPort); got != want {
					t.Errorf("initSummaryServerAddr with --server-port = %q, want %q", got, want)
				}
				got := initSummaryServerAddr(beadsDir, summaryHost, 0)

				// What every later command in the rig loads.
				t.Setenv("BEADS_DIR", beadsDir)
				config.ResetForTesting()
				if err := config.Initialize(); err != nil {
					t.Fatalf("config.Initialize: %v", err)
				}
				later := doltserver.DefaultConfig(beadsDir).Port
				want := summaryHost + " (no port pinned)"
				if portFile {
					want = doltutil.PrintedServerAddr(summaryHost, started)
				}
				if got != want {
					t.Errorf("initSummaryServerAddr = %q, want %q", got, want)
				}
				wantLater := 0
				if portFile {
					wantLater = started
				}
				if later != wantLater {
					t.Errorf("later commands in the rig resolve port %d, want %d", later, wantLater)
				}
			})
		}
	}
}

// TestInitSummaryServerAddrNamesTheStorePort pins the server address bd
// init's summary prints for a fresh workspace to the endpoint init's store
// dialed: the host --server-host or else BEADS_DOLT_SERVER_HOST names, and
// the port of initStoreServerPort with the store's rule applied
// (dolt.ResolveServerPort). A stale port file, the port of bd's last server
// on 127.0.0.1, names no port for a host bd does not run its server at, so
// the summary does not print it either. The legacy BEADS_DOLT_PORT, which
// Gas Town exports, names the port the store dials, and so the port the
// summary prints, as BEADS_DOLT_SERVER_PORT does.
func TestInitSummaryServerAddrNamesTheStorePort(t *testing.T) {
	const stalePort, envPort = 14444, 43217
	for _, tc := range []struct {
		name string
		host string
		// hostFlag: the host is --server-host; otherwise it is the caller's
		// own BEADS_DOLT_SERVER_HOST.
		hostFlag bool
		portFile bool   // .beads/dolt-server.port names stalePort
		portEnv  string // the variable that names envPort, if any
		want     int    // the port the store dials and the summary prints
	}{
		{name: "default host/port file", portFile: true, want: stalePort},
		{name: "default host/BEADS_DOLT_SERVER_PORT", portEnv: "BEADS_DOLT_SERVER_PORT", want: envPort},
		{name: "default host/BEADS_DOLT_PORT", portEnv: "BEADS_DOLT_PORT", want: envPort},
		{name: "default host/BEADS_DOLT_PORT over the port file", portFile: true, portEnv: "BEADS_DOLT_PORT", want: envPort},
		{name: "::1/stale port file", host: "::1", hostFlag: true, portFile: true, want: 0},
		{name: "0:0:0:0:0:0:0:1/stale port file", host: "0:0:0:0:0:0:0:1", hostFlag: true, portFile: true, want: 0},
		{name: "::1/BEADS_DOLT_PORT over the stale port file", host: "::1", hostFlag: true, portFile: true, portEnv: "BEADS_DOLT_PORT", want: envPort},
		{name: "remote/stale port file", host: "db.example.com", hostFlag: true, portFile: true, want: configfile.DefaultDoltServerPort},
		{name: "remote from the environment/stale port file", host: "db.example.com", portFile: true, want: configfile.DefaultDoltServerPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			// The store's test-mode guard turns a missing or production port
			// into 1; the ports compared are the ones a real init dials.
			t.Setenv("BEADS_TEST_MODE", "")
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), "# Beads Config\n")
			if tc.portFile {
				writeClonePortFile(t, filepath.Join(beadsDir, doltserver.PortFileName), strconv.Itoa(stalePort))
			}
			// What promoteExplicitServerConnFlags does with --server-host.
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
			if tc.portEnv != "" {
				t.Setenv(tc.portEnv, strconv.Itoa(envPort))
			}
			loadClonePortConfig(t, beadsDir)
			flagHost := ""
			if tc.hostFlag {
				flagHost = tc.host
			}
			host := initStoreServerHost(flagHost)

			// Init's store opens before init writes metadata.json.
			storeCfg := initStorePortConfig(beadsDir, host)
			dolt.ResolveServerPort(storeCfg)
			if storeCfg.ServerPort != tc.want {
				t.Errorf("init's store dials port %d, want %d", storeCfg.ServerPort, tc.want)
			}

			// What init writes before it prints the summary.
			meta := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "rig", DoltServerHost: flagHost}
			if err := meta.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			wantHost := tc.host
			if wantHost == "" {
				wantHost = configfile.DefaultDoltServerHost
			}
			want := wantHost + " (no port pinned)"
			if tc.want > 0 {
				want = doltutil.PrintedServerAddr(wantHost, tc.want)
			}
			if got := initSummaryServerAddr(beadsDir, flagHost, 0); got != want {
				t.Errorf("initSummaryServerAddr = %q, want %q", got, want)
			}
		})
	}
}

// TestInitStartOwnServerJoinsAServerOnItsOwnPort pins initStartOwnServer's
// check before it starts a nested workspace's own server: when a server
// already answers on the port the workspace's own configuration names (here
// a user-level dolt.port), it starts nothing, and init's store dials that
// server, as it would for a workspace nested in nothing. With auto-start off
// it starts nothing either.
func TestInitStartOwnServerJoinsAServerOnItsOwnPort(t *testing.T) {
	clonePortTestEnv(t)
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
	port := ln.Addr().(*net.TCPAddr).Port
	writeClonePortFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bd", "config.yaml"), fmt.Sprintf("dolt:\n  port: %d\n", port))
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	var ep doltserver.Endpoint
	_ = config.WithBeadsDir(beadsDir, func() { ep = doltserver.ResolveEndpoint(beadsDir, nil).WithHost("127.0.0.1") })
	if ep.Port != port || !ep.BDStartsServer() {
		t.Fatalf("the workspace's own endpoint is port %d, BDStartsServer %v; want port %d that bd would start", ep.Port, ep.BDStartsServer(), port)
	}
	for _, autoStart := range []bool{true, false} {
		started, err := initStartOwnServer(beadsDir, "127.0.0.1", "", autoStart)
		if started || err != nil {
			t.Errorf("initStartOwnServer(autoStart %v) = (%v, %v), want (false, nil): a server answers on %d", autoStart, started, err, port)
		}
	}
	if _, err := os.Stat(filepath.Join(beadsDir, doltserver.PortFileName)); err == nil {
		t.Errorf("initStartOwnServer started a server beside the one answering on %d", port)
	}
}
