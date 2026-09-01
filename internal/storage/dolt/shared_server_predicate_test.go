package dolt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestSharedServerDatabase pins which topologies the #5920 consent gate
// applies to. Getting this wrong is expensive in both directions: too broad
// and bd refuses to migrate a workspace's own database (the release blocker
// this predicate was introduced to fix), too narrow and an upgraded client
// silently promotes the schema for a whole team.
//
// Every case fixes the environment explicitly — the predicate reads process
// state, so a leaked BEADS_DOLT_* from another test would otherwise decide the
// answer.
func TestSharedServerDatabase(t *testing.T) {
	// A workspace with no metadata.json: doltserver resolves it as a server
	// whose lifecycle bd owns.
	ownedWorkspace := func(t *testing.T) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		return dir
	}

	clearEnv := func(t *testing.T) {
		t.Helper()
		for _, k := range []string{
			"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE",
			"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		} {
			t.Setenv(k, "")
		}
	}

	t.Run("bd-owned local server is not shared", func(t *testing.T) {
		clearEnv(t)
		cfg := &Config{BeadsDir: ownedWorkspace(t), ServerHost: "127.0.0.1", ServerPort: 3307}
		if sharedServerDatabase(cfg) {
			t.Fatal("a server bd auto-started for this workspace must migrate on open, as embedded does")
		}
	})

	t.Run("no workspace fails closed", func(t *testing.T) {
		clearEnv(t)
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
		clearEnv(t)
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		cfg := &Config{BeadsDir: ownedWorkspace(t), ServerHost: "127.0.0.1", ServerPort: 3308}
		if !sharedServerDatabase(cfg) {
			t.Fatal("shared-server mode is one server for many workspaces — the #5920 shape")
		}
	})

	t.Run("explicit external server mode is shared", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		cfg := &Config{BeadsDir: ownedWorkspace(t), ServerHost: "127.0.0.1", ServerPort: 3307}
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
		{name: "gateway credential", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3307, Gateway: true}},
	} {
		t.Run(tt.name+" is shared", func(t *testing.T) {
			clearEnv(t)
			cfg := tt.cfg
			cfg.BeadsDir = ownedWorkspace(t)
			if !sharedServerDatabase(&cfg) {
				t.Fatalf("%s cannot be a server bd auto-started for this workspace", tt.name)
			}
		})
	}
}

// TestSharedServerDatabaseEndpointProvenance pins the second half of the
// predicate: WHO named the endpoint bd connected to.
//
// The release blocker this covers is a workspace pointed at an externally
// managed dolt sql-server purely through BEADS_DOLT_SERVER_PORT. Nothing in
// metadata.json records the server, so ResolveServerMode — whose port arm reads
// metadata.json only — called it ServerModeOwned, the gate never ran, and an
// upgraded bd silently promoted the schema of a server it had never started.
// The connection path has honored that env var all along
// (configfile.GetDoltServerPort reads it first), which is the asymmetry.
//
// The matrix runs on the resolved Config.ServerPortSource rather than on the raw
// environment, because the env var alone cannot tell the two cases apart: it is
// also how a caller TELLS bd which port to bind before doltserver.Start.
func TestSharedServerDatabaseEndpointProvenance(t *testing.T) {
	workspace := func(t *testing.T) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		return dir
	}

	clearEnv := func(t *testing.T) {
		t.Helper()
		for _, k := range []string{
			"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE",
			"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		} {
			t.Setenv(k, "")
		}
	}

	for _, tt := range []struct {
		name   string
		source doltserver.PortSource
		shared bool
		why    string
	}{
		{
			name:   "env var",
			source: doltserver.PortSourceEnv,
			shared: true,
			why:    "BEADS_DOLT_SERVER_PORT points at a server that already existed; bd did not start it",
		},
		{
			name:   "beads config.yaml",
			source: doltserver.PortSourceConfigYaml,
			shared: true,
			why:    "a dolt.port written into config.yaml is an operator recording where a server lives",
		},
		{
			name:   "metadata.json",
			source: doltserver.PortSourceMetadataJSON,
			shared: true,
			why:    "the #5920 shape ResolveServerMode already gated; provenance must agree with it",
		},
		{
			name:   "external host default",
			source: doltserver.PortSourceExternalHostDefault,
			shared: true,
			why:    "the documented 3307 fallback is reached only when a host was configured",
		},
		{
			name:   "shared server default",
			source: doltserver.PortSourceSharedServerDefault,
			shared: true,
			why:    "3308 is the one-server-many-workspaces default",
		},
		{
			name:   "port file",
			source: doltserver.PortSourcePortFile,
			shared: false,
			why:    ".beads/dolt-server.port is bd's own record of the server it started here",
		},
		{
			name:   "dolt config.yaml",
			source: doltserver.PortSourceDoltConfigYaml,
			shared: false,
			why:    "the config of the dolt server living inside this workspace's own dolt dir",
		},
		{
			name:   "caller explicit",
			source: doltserver.PortSourceCallerExplicit,
			shared: false,
			why:    "an in-process assertion (bd init --server-port, the #5781 recovery) outranks ambient env by design",
		},
		{
			name:   "unset",
			source: doltserver.PortSourceUnset,
			shared: false,
			why:    "no endpoint was named at all; auto-start allocates an ephemeral port",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			cfg := &Config{
				BeadsDir:         workspace(t),
				ServerHost:       "127.0.0.1",
				ServerPort:       3307,
				ServerPortSource: tt.source,
			}
			if got := sharedServerDatabase(cfg); got != tt.shared {
				t.Fatalf("sharedServerDatabase(port source %q) = %v, want %v — %s", tt.source, got, tt.shared, tt.why)
			}
		})
	}

	// The escape hatch. doltserver.Start binds a port for THIS workspace and
	// records it; an operator who then exports the same port in the environment
	// has not handed the server to anyone else. The env read wins the precedence
	// chain, so the source reads "env" — the port file is what proves otherwise.
	t.Run("env port matching this workspace's own port file is owned", func(t *testing.T) {
		clearEnv(t)
		dir := workspace(t)
		if err := os.WriteFile(filepath.Join(dir, doltserver.PortFileName), []byte("34567"), 0o600); err != nil {
			t.Fatalf("write port file: %v", err)
		}
		t.Setenv("BEADS_DOLT_SERVER_PORT", "34567")
		cfg := &Config{
			BeadsDir:         dir,
			ServerHost:       "127.0.0.1",
			ServerPort:       34567,
			ServerPortSource: doltserver.PortSourceEnv,
		}
		if sharedServerDatabase(cfg) {
			t.Fatal("bd started this server for this workspace and recorded the port; it must still migrate on open (#6088)")
		}
	})

	// ...and the hatch is not a blanket exemption: a port file left behind by an
	// unrelated server does not launder a different endpoint into ownership.
	t.Run("env port disagreeing with the port file is shared", func(t *testing.T) {
		clearEnv(t)
		dir := workspace(t)
		if err := os.WriteFile(filepath.Join(dir, doltserver.PortFileName), []byte("34567"), 0o600); err != nil {
			t.Fatalf("write port file: %v", err)
		}
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
		cfg := &Config{
			BeadsDir:         dir,
			ServerHost:       "127.0.0.1",
			ServerPort:       3307,
			ServerPortSource: doltserver.PortSourceEnv,
		}
		if !sharedServerDatabase(cfg) {
			t.Fatal("the port file names a different server; the env endpoint is still someone else's")
		}
	})

	// The reported shape end to end: dolt_mode=server, NO dolt_server_port,
	// endpoint supplied entirely by the environment. This is what gascity's
	// 28-database server on port 3307 looks like from inside bd.
	t.Run("gascity shape: server mode with no persisted port is shared", func(t *testing.T) {
		clearEnv(t)
		dir := workspace(t)
		metadata := `{"database":"beads","backend":"dolt","dolt_mode":"server","dolt_database":"beads"}`
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
		cfg := &Config{
			BeadsDir:         dir,
			ServerHost:       "127.0.0.1",
			ServerPort:       3307,
			ServerPortSource: doltserver.PortSourceEnv,
		}
		if !sharedServerDatabase(cfg) {
			t.Fatal("an env-pointed server with no ownership record must be gated, not migrated in place")
		}
		// The bypass was exactly this disagreement: the lifecycle resolver said
		// owned while the connection path was dialing an env-named endpoint.
		if got := doltserver.ResolveServerMode(dir); got != doltserver.ServerModeOwned {
			t.Fatalf("ResolveServerMode = %v, want ServerModeOwned — if this changed, the predicate no "+
				"longer covers a case the lifecycle resolver misses and this test has stopped testing the bug", got)
		}
	})
}
