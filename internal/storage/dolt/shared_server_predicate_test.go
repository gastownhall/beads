package dolt

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// clearPredicateEnv fixes every BEADS_DOLT_* signal the predicate reads. The
// predicate reads process state, so a leaked variable from a sibling test would
// otherwise decide the answer. One list, shared by every test in this file: two
// copies would mean the next signal added to the predicate has to be added to
// both, and forgetting one silently reintroduces exactly the leak this exists
// to prevent.
func clearPredicateEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_SERVER_TLS",
	} {
		t.Setenv(k, "")
	}
}

// unmanagedWorkspace is a .beads directory with no server state files: bd never
// started a server here. Whatever it is connected to, it was pointed at.
func unmanagedWorkspace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

// managedWorkspace is a .beads directory carrying the state files
// doltserver.Start writes: a port file naming port, and a PID file naming a
// live process. This is the auto-started single-workspace server of #6088.
//
// The PID is this test process, which is alive by construction. That is a
// faithful stand-in because the predicate deliberately checks liveness only —
// doltserver.ManagesLiveServerOnPort documents why it stops short of the
// `ps`-based command-name check that would distinguish a dolt process.
func managedWorkspace(t *testing.T, port int) string {
	t.Helper()
	dir := unmanagedWorkspace(t)
	writeServerState(t, dir, port, os.Getpid())
	return dir
}

func writeServerState(t *testing.T, beadsDir string, port, pid int) {
	t.Helper()
	if port > 0 {
		if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PortFileName),
			[]byte(strconv.Itoa(port)), 0o600); err != nil {
			t.Fatalf("write port file: %v", err)
		}
	}
	if pid > 0 {
		if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PIDFileName),
			[]byte(strconv.Itoa(pid)), 0o600); err != nil {
			t.Fatalf("write pid file: %v", err)
		}
	}
}

// deadPID returns a PID that is almost certainly not running. Allocating and
// reaping a real child would be exact, but the predicate only needs a number
// no live process answers to; PID 0x7FFFFFFE is above every default pid_max.
const deadPID = 2147483646

// TestSharedServerDatabase pins which topologies the #5920 consent gate applies
// to. Getting this wrong is expensive in both directions: too broad and bd
// refuses to migrate a workspace's own database (#6088, the release blocker
// this predicate was narrowed to fix), too narrow and an upgraded client
// silently promotes the schema for a whole team (#5920, and the env-port bypass
// that reopened it).
func TestSharedServerDatabase(t *testing.T) {
	t.Run("a live bd-managed server for this workspace is not shared", func(t *testing.T) {
		clearPredicateEnv(t)
		cfg := &Config{BeadsDir: managedWorkspace(t, 3307), ServerHost: "127.0.0.1", ServerPort: 3307}
		if sharedServerDatabase(cfg) {
			t.Fatal("a server bd started for this workspace must migrate on open, as embedded does (#6088)")
		}
	})

	t.Run("no workspace fails closed", func(t *testing.T) {
		clearPredicateEnv(t)
		// A bare dolt.New pointed at an endpoint: nothing proves bd owns it,
		// and a library caller attached to a team's server must not migrate it.
		cfg := &Config{ServerHost: "127.0.0.1", ServerPort: 3307}
		if !sharedServerDatabase(cfg) {
			t.Fatal("without a workspace to prove ownership, the database must be treated as shared")
		}
		if !sharedServerDatabase(nil) {
			t.Fatal("a nil config must be treated as shared")
		}
	})

	t.Run("shared-server mode is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		cfg := &Config{BeadsDir: managedWorkspace(t, 3308), ServerHost: "127.0.0.1", ServerPort: 3308}
		if !sharedServerDatabase(cfg) {
			t.Fatal("shared-server mode is one server for many workspaces — the #5920 shape")
		}
	})

	t.Run("explicit external server mode is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		// Even with the ownership proof present: an explicit declaration that
		// the lifecycle is external outranks it.
		cfg := &Config{BeadsDir: managedWorkspace(t, 3307), ServerHost: "127.0.0.1", ServerPort: 3307}
		if !sharedServerDatabase(cfg) {
			t.Fatal("an operator-managed server is not bd's to migrate unprompted")
		}
	})

	for _, tt := range []struct {
		name string
		cfg  Config
	}{
		{name: "remote host", cfg: Config{ServerHost: "db.example.com", ServerPort: 3307}},
		{name: "TLS endpoint", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3307, ServerTLS: true}},
		{name: "unix socket", cfg: Config{ServerHost: "127.0.0.1", ServerSocket: "/tmp/dolt.sock"}},
	} {
		t.Run(tt.name+" is shared", func(t *testing.T) {
			clearPredicateEnv(t)
			cfg := tt.cfg
			cfg.BeadsDir = managedWorkspace(t, 3307)
			if !sharedServerDatabase(&cfg) {
				t.Fatalf("%s cannot be a server bd auto-started for this workspace", tt.name)
			}
		})
	}
}

// TestSharedServerDatabaseOwnershipProof covers the bypasses that made the
// #5920 gate miss the reported deployment, and the false positives that a
// coarser rule would create.
//
// The unifying point: a local TCP endpoint on 127.0.0.1 looks identical however
// bd arrived at it. The env var, a config.yaml pin, `bd init --server-port`, a
// library caller's hand-built Config and a stale port file all produce one, and
// each was (or would be) a separate silent bypass. Only the state files bd
// writes when it starts a server distinguish them.
func TestSharedServerDatabaseOwnershipProof(t *testing.T) {
	// 3307 is both the reported deployment's port and
	// configfile.DefaultDoltServerPort, which is why these collisions are
	// likely rather than exotic.
	const port = 3307

	t.Run("env-pointed server with no bd-managed server is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
		cfg := &Config{
			BeadsDir:         unmanagedWorkspace(t),
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourceEnv,
		}
		if !sharedServerDatabase(cfg) {
			t.Fatal("BEADS_DOLT_SERVER_PORT names a server that already existed; bd never started it")
		}
	})

	// The reported bug's second door. applyConfigDefaults stamps
	// PortSourceCallerExplicit onto ANY caller-preset port, and
	// `bd init --server-port` is the documented way to attach a workspace to an
	// existing server — so treating that source as proof of ownership left the
	// blocker open through the flag bd's own help text recommends.
	t.Run("bd init --server-port at an external server is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		cfg := &Config{
			BeadsDir:         unmanagedWorkspace(t),
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourceCallerExplicit,
		}
		if !sharedServerDatabase(cfg) {
			t.Fatal("a caller asserting a port is not bd having started that server")
		}
	})

	// Same door, library spelling: an embedder building a Config by hand.
	t.Run("hand-built library config at an external server is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		cfg := &Config{BeadsDir: unmanagedWorkspace(t), ServerHost: "127.0.0.1", ServerPort: port}
		if !sharedServerDatabase(cfg) {
			t.Fatal("an embedder pointed at a team's server must not migrate it")
		}
	})

	// A port file records that bd once bound a port, not that the process
	// answering it now is bd's. Without a liveness check, a workspace whose own
	// server died launders whatever server later occupies that port.
	t.Run("stale port file whose server died is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		dir := unmanagedWorkspace(t)
		writeServerState(t, dir, port, deadPID)
		t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
		cfg := &Config{
			BeadsDir:         dir,
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourceEnv,
		}
		if !sharedServerDatabase(cfg) {
			t.Fatal("the recorded server is gone; an external server now on that port is not ours to migrate")
		}
	})

	t.Run("port file with no pid file is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		dir := unmanagedWorkspace(t)
		writeServerState(t, dir, port, 0)
		cfg := &Config{BeadsDir: dir, ServerHost: "127.0.0.1", ServerPort: port}
		if !sharedServerDatabase(cfg) {
			t.Fatal("a port file alone is a record, not a live server")
		}
	})

	// The port file must name the port we actually connected on: a live server
	// bd manages on one port says nothing about a different endpoint.
	t.Run("live bd server on a different port is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		cfg := &Config{BeadsDir: managedWorkspace(t, 40083), ServerHost: "127.0.0.1", ServerPort: port}
		if !sharedServerDatabase(cfg) {
			t.Fatal("bd's server is on another port; this endpoint is somebody else's")
		}
	})

	// The #6088 direction, and the false positive a coarser rule would create:
	// a single-user workspace that pinned dolt.port in config.yaml — the very
	// configuration bd's own auto-start warning recommends ("To pin a port: set
	// dolt.port in .beads/config.yaml") — with bd auto-starting its own server
	// on that port. Classifying the pin itself as shared would hard-refuse it.
	t.Run("config.yaml-pinned port with a live bd server still migrates", func(t *testing.T) {
		clearPredicateEnv(t)
		cfg := &Config{
			BeadsDir:         managedWorkspace(t, port),
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourceConfigYaml,
		}
		if sharedServerDatabase(cfg) {
			t.Fatal("a pinned port bd auto-started on is still this workspace's own server")
		}
	})

	// Same, for the env var: exporting BEADS_DOLT_SERVER_PORT is also how a
	// caller tells bd which port to BIND before doltserver.Start, which is the
	// shape #5781's lenient-open recovery runs in.
	t.Run("env port that bd itself bound still migrates", func(t *testing.T) {
		clearPredicateEnv(t)
		t.Setenv("BEADS_DOLT_SERVER_PORT", "34567")
		cfg := &Config{
			BeadsDir:         managedWorkspace(t, 34567),
			ServerHost:       "127.0.0.1",
			ServerPort:       34567,
			ServerPortSource: doltserver.PortSourceEnv,
		}
		if sharedServerDatabase(cfg) {
			t.Fatal("bd started this server for this workspace and recorded it; it must still migrate on open")
		}
	})

	// dolt's own config.yaml inside the workspace's dolt dir is not written by
	// bd as an ownership record — an operator can run their own long-lived
	// sql-server over that directory, or leave a stale file behind.
	t.Run("dolt config.yaml port without a live bd server is shared", func(t *testing.T) {
		clearPredicateEnv(t)
		cfg := &Config{
			BeadsDir:         unmanagedWorkspace(t),
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourceDoltConfigYaml,
		}
		if !sharedServerDatabase(cfg) {
			t.Fatal("nothing in bd writes dolt's config.yaml as proof of ownership")
		}
	})
}

// TestSharedServerDatabaseThroughResolvedConfig closes the gap between the unit
// tests above, which hand-build a Config, and what a real open produces.
//
// Every other test here asserts the predicate's behavior for a given Config. If
// the config path stopped producing that Config — a caller presetting the port,
// a change to ApplyResolvedServerPort's precedence, applyConfigDefaults'
// re-stamp condition (store.go, be-9tju) — the bug would return with every one
// of them still green. This one drives the real path.
func TestSharedServerDatabaseThroughResolvedConfig(t *testing.T) {
	clearPredicateEnv(t)

	// The reported deployment's exact workspace shape: server mode, and NO
	// dolt_server_port. The endpoint exists only in the environment.
	//
	// The port is deliberately NOT 3307. This test binary runs with
	// BEADS_TEST_MODE=1, whose production-port firewall rewrites a resolved
	// production port to 1 (applyConfigDefaults) — which would mask the very
	// resolution this test exists to pin. Provenance is what is under test, not
	// the number.
	const envPort = 45671

	dir := unmanagedWorkspace(t)
	metadata := `{"database":"beads","backend":"dolt","dolt_mode":"server","dolt_database":"beads"}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(envPort))

	fileCfg, err := configfile.Load(dir)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	cfg := &Config{}
	if err := applyResolvedConfig(context.Background(), dir, fileCfg, cfg); err != nil {
		t.Fatalf("applyResolvedConfig: %v", err)
	}
	applyConfigDefaults(cfg)

	// The connection path honors the env var...
	if cfg.ServerPort != envPort {
		t.Fatalf("resolved ServerPort = %d, want %d — the env var must decide the endpoint",
			cfg.ServerPort, envPort)
	}
	// ...while the lifecycle resolver, which reads metadata.json only, does
	// not. That disagreement IS the bug; if it ever closes, this test should be
	// revisited rather than silently continuing to pass for a new reason.
	if got := doltserver.ResolveServerMode(dir); got != doltserver.ServerModeOwned {
		t.Logf("note: ResolveServerMode now returns %v for the env-port shape (was ServerModeOwned)", got)
	}
	// The predicate must gate it regardless.
	if !sharedServerDatabase(cfg) {
		t.Fatal("a workspace whose endpoint came only from BEADS_DOLT_SERVER_PORT must be gated, " +
			"not migrated in place (#5920 bypass)")
	}
}
