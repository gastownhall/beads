package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
// goes through it as that user.
func TestInitRemoteCloneGoesWhereTheStoreOpens(t *testing.T) {
	const port = "13451"
	for _, tc := range []struct {
		name       string
		host       string
		external   bool
		gateway    bool
		serverUser string // --server-user
		serverHas  bool
		wantClone  string // host:port the server clone dials; "" for none
		wantHeldAt string // the server init found holding the database
	}{
		{name: "remote host", host: "db.example.com", wantClone: "db.example.com:" + port},
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
			t.Setenv("BEADS_DOLT_SERVER_PORT", port)
			if tc.gateway {
				t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "echo tok123")
			}
			if tc.serverUser != "" {
				// As promoteExplicitServerConnFlags exports --server-user.
				t.Setenv("BEADS_DOLT_SERVER_USER", tc.serverUser)
			}

			origServer, origCheck := remoteCloneViaServer, checkBootstrapServerDB
			t.Cleanup(func() { remoteCloneViaServer, checkBootstrapServerDB = origServer, origCheck })
			var cloned, users []string
			probes := 0
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				cloned = append(cloned, ep.serverLocation())
				users = append(users, ep.user)
				return nil
			}
			checkBootstrapServerDB = func(ep bootstrapServerProbeConfig) bootstrapServerDBCheck {
				probes++
				users = append(users, ep.user)
				return bootstrapServerDBCheck{Reachable: true, Exists: tc.serverHas}
			}

			beadsDir := filepath.Join(t.TempDir(), ".beads")
			cloneCfg := initTimeCloneConfig(true, tc.host, 0, "", tc.serverUser, "init_remote_clone")
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
			if tc.gateway && tc.serverUser == "" {
				// main's CLI clone into .beads/dolt; its outcome here (no
				// dolt binary, no remote) is beside the point.
				if errors.As(err, &held) || probes != 0 || len(cloned) != 0 {
					t.Fatalf("gateway: err %v, %d server probes, server clones %v; want main's CLI clone, with neither", err, probes, cloned)
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
