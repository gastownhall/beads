package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// cityOwnedTestStore provisions a server-mode .beads store whose config.yaml
// carries originLine, and returns the workspace root and store directory.
func cityOwnedTestStore(t *testing.T, originLine string) (string, string) {
	t.Helper()
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltServerPort: 1}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(originLine), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return root, beadsDir
}

func runBDInStore(t *testing.T, bd, root, beadsDir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = root
	cmd.Env = append(removedBackendTestEnv(beadsDir), "BEADS_DOLT_AUTO_START=0", "BD_DISABLE_METRICS=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func assertNoLifecycleState(t *testing.T, beadsDir, command string) {
	t.Helper()
	for _, name := range []string{"dolt-server.pid", "dolt-server.port", "dolt"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s created %s in a refused store (stat error: %v)", command, name, statErr)
		}
	}
}

// TestDoltLifecycleCommandsRefuseCityOwnedStore proves that set, start, stop
// and killall read the resolved store's gc.endpoint_origin and refuse, naming
// the city's commands, before any lifecycle effect.
func TestDoltLifecycleCommandsRefuseCityOwnedStore(t *testing.T) {
	bd := buildBDForInitTests(t)
	root, beadsDir := cityOwnedTestStore(t, config.GasCityEndpointOriginKey+": "+string(config.GasCityOriginManagedCity)+"\n")

	for _, verb := range []string{"set", "start", "stop", "killall"} {
		t.Run(verb, func(t *testing.T) {
			args := []string{"dolt", verb}
			if verb == "set" {
				args = append(args, "host", "127.0.0.1")
			}
			out, err := runBDInStore(t, bd, root, beadsDir, args...)
			if err == nil {
				t.Fatalf("bd %s succeeded on a city-owned store:\n%s", strings.Join(args, " "), out)
			}
			for _, want := range []string{
				"Gas City owns this store's Dolt server (gc.endpoint_origin=managed_city)",
				"'bd dolt " + verb + "' is refused",
				"To start: gc start",
				"To check status: gc doctor",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("bd %s output missing %q:\n%s", strings.Join(args, " "), want, out)
				}
			}
			assertNoLifecycleState(t, beadsDir, "bd "+strings.Join(args, " "))
		})
	}
}

// TestDoltCommandsRejectInvalidOwnershipStamp proves an unknown
// gc.endpoint_origin fails every ownership-reading command instead of being
// treated as "not city-owned".
func TestDoltCommandsRejectInvalidOwnershipStamp(t *testing.T) {
	bd := buildBDForInitTests(t)
	root, beadsDir := cityOwnedTestStore(t, config.GasCityEndpointOriginKey+": somebody_else\n")

	for _, args := range [][]string{
		{"dolt", "set", "host", "127.0.0.1"},
		{"dolt", "start"},
		{"dolt", "stop"},
		{"dolt", "killall"},
		{"dolt", "status"},
	} {
		t.Run(args[1], func(t *testing.T) {
			out, err := runBDInStore(t, bd, root, beadsDir, args...)
			if err == nil {
				t.Fatalf("bd %s succeeded with an invalid gc.endpoint_origin:\n%s", strings.Join(args, " "), out)
			}
			if !strings.Contains(out, `invalid gc.endpoint_origin "somebody_else"`) {
				t.Errorf("bd %s output lacks the invalid-origin error:\n%s", strings.Join(args, " "), out)
			}
			assertNoLifecycleState(t, beadsDir, "bd "+strings.Join(args, " "))
		})
	}
}

// TestConfigSetDoltDebugHintFollowsStoreOwnership proves `bd config set
// dolt.debug` prints the restart command of the store's lifecycle owner.
//
// The store is a shared-server workspace that has not created its database
// yet: dolt.debug is a yaml-only key, so the command runs without opening a
// store (no server needed) while still resolving the active .beads directory.
func TestConfigSetDoltDebugHintFollowsStoreOwnership(t *testing.T) {
	bd := buildBDForInitTests(t)
	tests := []struct {
		name       string
		originLine string
		want       string
	}{
		{"city-owned", config.GasCityEndpointOriginKey + ": " + string(config.GasCityOriginInheritedCity) + "\n", "gc stop && gc start"},
		{"unstamped", "", "bd dolt stop && bd dolt start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads: %v", err)
			}
			body := "dolt.shared-server: true\n" + tt.originLine
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o600); err != nil {
				t.Fatalf("write config.yaml: %v", err)
			}
			out, err := runBDInStore(t, bd, root, beadsDir, "config", "set", "dolt.debug", "true")
			if err != nil {
				t.Fatalf("bd config set dolt.debug true: %v\n%s", err, out)
			}
			if !strings.Contains(out, "→ "+tt.want) {
				t.Fatalf("bd config set dolt.debug output lacks restart hint %q:\n%s", tt.want, out)
			}
		})
	}
}
