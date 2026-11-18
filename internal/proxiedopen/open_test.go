package proxiedopen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// TestZeroOptionsAreTheSafePosture pins that a caller who fills in nothing
// gets the root-command posture: assert the workspace identity, refuse to
// create a store, print nothing.
func TestZeroOptionsAreTheSafePosture(t *testing.T) {
	var o Options
	if o.Identity != AssertWorkspaceIdentity {
		t.Errorf("zero Identity = %v, want AssertWorkspaceIdentity", o.Identity)
	}
	if o.FreshStore != RefuseFreshStore {
		t.Errorf("zero FreshStore = %v, want RefuseFreshStore", o.FreshStore)
	}
	if o.Advisory != nil {
		t.Errorf("zero Advisory = %v, want nil", o.Advisory)
	}
}

func TestOpenRequiresBeadsDir(t *testing.T) {
	_, err := Open(context.Background(), "", Options{})
	if err == nil || !strings.Contains(err.Error(), "beadsDir must be set") {
		t.Fatalf("Open(\"\") = %v, want a beadsDir error", err)
	}
}

// TestOpenRefusesAnEmptyManagedRootWithoutCreatingIt is GH#8323's
// precondition at this layer: a managed-local workspace whose root holds no
// database is refused, and the refusal creates nothing.
func TestOpenRefusesAnEmptyManagedRootWithoutCreatingIt(t *testing.T) {
	beadsDir := t.TempDir()
	_, err := Open(context.Background(), beadsDir, Options{})
	if err == nil || !strings.Contains(err.Error(), "refusing to start a proxied Dolt server") {
		t.Fatalf("Open = %v, want the fresh-store refusal", err)
	}
	if _, statErr := os.Stat(doltserver.DoltDirPath(beadsDir)); !os.IsNotExist(statErr) {
		t.Fatalf("the refusal created the root %s (stat err %v)", doltserver.DoltDirPath(beadsDir), statErr)
	}
}

func TestFreshStoreRefusalExemptsExternalTopology(t *testing.T) {
	ext := Topology{Database: "beads", External: &configfile.ExternalDoltConfig{Host: "db.invalid", Port: 3306}}
	if err := FreshStoreRefusal(t.TempDir(), ext); err != nil {
		t.Fatalf("external topology refused: %v", err)
	}
}

func TestResolveProxiedTopologyReadsTheSidecar(t *testing.T) {
	beadsDir := t.TempDir()
	t.Setenv(configfile.ExternalDoltPasswordEnvVar, "s3cret")
	if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{Host: "db.invalid", Port: 3306},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveProxiedTopology(beadsDir, "override_db", AssertWorkspaceIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if got.Database != "override_db" {
		t.Errorf("Database = %q, want the override", got.Database)
	}
	if got.External == nil || got.External.Host != "db.invalid" || got.External.Port != 3306 {
		t.Errorf("External = %+v, want the sidecar's server", got.External)
	}
	if got.RootPassword != "s3cret" {
		t.Errorf("RootPassword = %q, want the env var's value", got.RootPassword)
	}
}

// TestEnsureConfigGeneratesALoopbackConfig pins that the generated config
// passes the managed trust boundary it is checked against.
func TestEnsureConfigGeneratesALoopbackConfig(t *testing.T) {
	orig := ListenerOwnershipProvable
	ListenerOwnershipProvable = func() bool { return true }
	t.Cleanup(func() { ListenerOwnershipProvable = orig })

	beadsDir := t.TempDir()
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", filepath.Join(t.TempDir(), "root"))
	path, err := EnsureConfig(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsManagedConfig(body) {
		t.Fatalf("generated config lacks the managed marker:\n%s", body)
	}
	cfg, err := servercfg.NewYamlConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host() != ListenerHost {
		t.Errorf("listener.host = %q, want %q", cfg.Host(), ListenerHost)
	}
	if string(cfg.LogLevel()) != string(servercfg.LogLevel_Warning) {
		t.Errorf("log_level = %q, want warning where listener ownership is provable", cfg.LogLevel())
	}
	if port, _, ok := CutManagedPortRecord(body[len(ManagedConfigMarker):]); !ok || port != cfg.Port() {
		t.Errorf("port record = %d (ok %t), want listener.port %d", port, ok, cfg.Port())
	}
}

// TestOptionsProxyExecutableReachesTheSpawnPath pins that
// Options.ProxyExecutable is what the proxy spawn consults: with no proxy
// running, a resolver reporting proxy.ErrNoProxyExecutable fails the open
// with that sentinel, so a library host never execs itself.
func TestOptionsProxyExecutableReachesTheSpawnPath(t *testing.T) {
	beadsDir := t.TempDir()
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", filepath.Join(t.TempDir(), "root"))
	called := false
	topology := Topology{
		Database: "beads",
		External: &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 3306},
	}
	_, err := OpenTopology(context.Background(), beadsDir, topology, Options{
		ProxyExecutable: func() (string, error) {
			called = true
			return "", fmt.Errorf("no bd: %w", proxy.ErrNoProxyExecutable)
		},
	})
	if !errors.Is(err, proxy.ErrNoProxyExecutable) {
		t.Fatalf("OpenTopology err = %v, want proxy.ErrNoProxyExecutable", err)
	}
	if !called {
		t.Fatal("Options.ProxyExecutable was not consulted on the spawn path")
	}
}
