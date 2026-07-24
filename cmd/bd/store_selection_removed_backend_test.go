package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestCommandStartupRejectsRemovedBackendsBeforeLocalWrites(t *testing.T) {
	bd := buildBDForInitTests(t)
	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite} {
		t.Run(backend, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("create beads dir: %v", err)
			}
			if err := (&configfile.Config{Backend: backend}).Save(beadsDir); err != nil {
				t.Fatalf("save config: %v", err)
			}

			cmd := exec.Command(bd, "list", "--json")
			cmd.Dir = root
			cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("removed backend %s unexpectedly opened", backend)
			}
			message := strings.ToLower(string(out))
			for _, want := range removedBackendWantSubstrings(backend) {
				if !strings.Contains(message, want) {
					t.Errorf("startup error for %s missing %q: %s", backend, want, message)
				}
			}

			for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version"} {
				path := filepath.Join(beadsDir, name)
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("removed backend %s created local state at %s (stat error: %v)", backend, path, statErr)
				}
			}
		})
	}
}

func TestCommandStartupRejectsUnknownBackendBeforeLocalWrites(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := (&configfile.Config{Backend: "mystery"}).Save(beadsDir); err != nil {
		t.Fatalf("save unknown-backend metadata: %v", err)
	}

	cmd := exec.Command(bd, "list", "--json")
	cmd.Dir = root
	cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown backend unexpectedly opened:\n%s", out)
	}
	message := string(out)
	if strings.Contains(message, "no beads database found") {
		t.Fatalf("unknown backend stopped at generic discovery instead of metadata guidance:\n%s", out)
	}
	for _, want := range []string{"not recognized", "opened or modified", "dolt"} {
		if !strings.Contains(message, want) {
			t.Errorf("unknown-backend error missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("unknown backend created %s (stat error: %v)", name, statErr)
		}
	}
}

func TestInitGuardRejectsCorruptMetadataBeforeWorkspaceWrites(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
	metadata := []byte("{\n")
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatalf("write corrupt metadata.json: %v", err)
	}

	err := checkExistingBeadsDataAt(beadsDir, "guard")
	if err == nil || !strings.Contains(err.Error(), "metadata.json") {
		t.Fatalf("init guard error = %v, want corrupt metadata refusal", err)
	}
	after, readErr := os.ReadFile(metadataPath)
	if readErr != nil {
		t.Fatalf("read metadata after refusal: %v", readErr)
	}
	if string(after) != string(metadata) {
		t.Fatalf("init guard rewrote corrupt metadata: before %q, after %q", metadata, after)
	}
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("init guard created %s (stat error: %v)", name, statErr)
		}
	}
}

func TestDoctorRejectsUnknownBackendBeforeDoltChecks(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	cfg := &configfile.Config{
		Backend:        "mystery",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 1,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save unknown-backend metadata: %v", err)
	}

	cmd := exec.Command(bd, "doctor", "--server", "--json")
	cmd.Dir = root
	cmd.Env = append(removedBackendTestEnv(beadsDir), "BEADS_DOLT_AUTO_START=0", "BD_DISABLE_METRICS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("doctor unexpectedly ran Dolt checks for unknown backend:\n%s", out)
	}
	for _, want := range []string{"not recognized", "no storage database was opened or modified", "dolt"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("doctor unknown-backend error missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db", "dolt-server.pid", "dolt-server.port"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("doctor created %s for unknown backend (stat error: %v)", name, statErr)
		}
	}
}

func TestLegacyInitFlagsReachRemovedBackendGuidance(t *testing.T) {
	bd := buildBDForInitTests(t)
	tests := []struct {
		backend string
		args    []string
		wants   []string
	}{
		{
			backend: configfile.BackendPostgres,
			args: []string{
				"init", "--backend=postgres",
				"--pg-url=postgres://beads@127.0.0.1:5432/beads",
				"--pg-schema=legacy_workspace",
			},
			wants: []string{"no longer supported", "general-purpose server databases", "simple and resource-light", "dolt"},
		},
		{
			backend: configfile.BackendMySQL,
			args: []string{
				"init", "--backend=mysql",
				"--mysql-url=beads@tcp(127.0.0.1:3306)/",
				"--mysql-database=legacy_workspace",
			},
			wants: []string{"no longer supported", "general-purpose server databases", "simple and resource-light", "dolt"},
		},
		{
			backend: configfile.BackendSQLite,
			args: []string{
				"init", "--backend=sqlite",
				"--sqlite-path=beads.db",
			},
			wants: []string{"no longer supported", "single engine", "dolt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			cmd := exec.Command(bd, tt.args...)
			cmd.Dir = root
			cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("legacy %s init unexpectedly succeeded: %s", tt.backend, out)
			}

			message := string(out)
			if strings.Contains(message, "unknown flag") {
				t.Errorf("legacy %s init stopped at argument parsing instead of rollback guidance: %s", tt.backend, message)
			}
			for _, want := range tt.wants {
				if !strings.Contains(message, want) {
					t.Errorf("legacy %s init error missing %q: %s", tt.backend, want, message)
				}
			}
			if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
				t.Fatalf("rejected legacy %s init created workspace state (stat error: %v)", tt.backend, statErr)
			}
		})
	}
}

func TestDoltAdministrativeCommandsRejectRemovedBackends(t *testing.T) {
	bd := buildBDForInitTests(t)
	commands := []struct {
		name string
		args []string
	}{
		{name: "show", args: []string{"dolt", "show"}},
		{name: "status", args: []string{"dolt", "status"}},
		{name: "set", args: []string{"dolt", "set", "host", "127.0.0.1"}},
		{name: "test", args: []string{"dolt", "test"}},
		{name: "start", args: []string{"dolt", "start"}},
		{name: "stop", args: []string{"dolt", "stop"}},
		{name: "killall", args: []string{"dolt", "killall"}},
		{name: "clean-databases", args: []string{"dolt", "clean-databases", "--dry-run"}},
	}

	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite} {
		t.Run(backend, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("create beads dir: %v", err)
			}
			if err := (&configfile.Config{Backend: backend}).Save(beadsDir); err != nil {
				t.Fatalf("save config: %v", err)
			}

			for _, command := range commands {
				t.Run(command.name, func(t *testing.T) {
					cmd := exec.Command(bd, command.args...)
					cmd.Dir = root
					cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
					out, err := cmd.CombinedOutput()
					if err == nil {
						t.Fatalf("%q unexpectedly succeeded for removed backend %s: %s", strings.Join(command.args, " "), backend, out)
					}

					message := strings.ToLower(string(out))
					for _, want := range removedBackendWantSubstrings(backend) {
						if !strings.Contains(message, want) {
							t.Errorf("%q error for %s missing %q: %s", strings.Join(command.args, " "), backend, want, message)
						}
					}

					for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version"} {
						path := filepath.Join(beadsDir, name)
						if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
							t.Fatalf("%q created local state for removed backend %s at %s (stat error: %v)", strings.Join(command.args, " "), backend, path, statErr)
						}
					}
				})
			}
		})
	}
}

func TestDoltReadOnlyCommandsRejectUnknownBackend(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := (&configfile.Config{Backend: "mystery", DoltMode: configfile.DoltModeServer, DoltServerPort: 1}).Save(beadsDir); err != nil {
		t.Fatalf("save unknown-backend metadata: %v", err)
	}

	for _, args := range [][]string{{"dolt", "show"}, {"dolt", "status"}, {"dolt", "killall"}} {
		cmd := exec.Command(bd, args...)
		cmd.Dir = root
		cmd.Env = append(removedBackendTestEnv(beadsDir), "BEADS_DOLT_AUTO_START=0", "BD_DISABLE_METRICS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("bd %s unexpectedly treated unknown metadata as Dolt:\n%s", strings.Join(args, " "), out)
			continue
		}
		if !strings.Contains(string(out), "not recognized") || !strings.Contains(string(out), "opened or modified") {
			t.Errorf("bd %s error lacks fail-closed guidance:\n%s", strings.Join(args, " "), out)
		}
	}
}

func TestDoltKillallRejectsCorruptMetadataBeforeServerCleanup(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	metadata := []byte("{\n")
	metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatalf("write corrupt metadata: %v", err)
	}

	cmd := exec.Command(bd, "dolt", "killall")
	cmd.Dir = root
	cmd.Env = append(removedBackendTestEnv(beadsDir),
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_SHARED_SERVER_DIR="+filepath.Join(root, "shared-server"),
		"BD_DISABLE_METRICS=1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd dolt killall unexpectedly ignored corrupt metadata:\n%s", out)
	}
	message := strings.ToLower(string(out))
	for _, want := range []string{"loading config", "parsing config"} {
		if !strings.Contains(message, want) {
			t.Errorf("bd dolt killall corrupt-metadata error missing %q:\n%s", want, out)
		}
	}
	after, readErr := os.ReadFile(metadataPath)
	if readErr != nil {
		t.Fatalf("read metadata after refusal: %v", readErr)
	}
	if string(after) != string(metadata) {
		t.Fatalf("bd dolt killall rewrote corrupt metadata: before %q, after %q", metadata, after)
	}
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version", "dolt-server.pid", "dolt-server.port"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("bd dolt killall created %s before rejecting corrupt metadata (stat error: %v)", name, statErr)
		}
	}
}

func removedBackendTestEnv(beadsDir string) []string {
	var env []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BEADS_") || strings.HasPrefix(entry, "BD_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "BEADS_DIR="+beadsDir)
}

func TestStoreFactoriesRemovedBackendsFailLoud(t *testing.T) {
	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite} {
		t.Run(backend, func(t *testing.T) {
			beadsDir := t.TempDir()
			cfg := &configfile.Config{Backend: backend}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatalf("save config: %v", err)
			}

			factories := map[string]func(context.Context, string) (interface{ Close() error }, error){
				"read-write": func(ctx context.Context, dir string) (interface{ Close() error }, error) {
					return newDoltStoreFromConfig(ctx, dir)
				},
				"read-only": func(ctx context.Context, dir string) (interface{ Close() error }, error) {
					return newReadOnlyStoreFromConfig(ctx, dir)
				},
			}

			for name, open := range factories {
				t.Run(name, func(t *testing.T) {
					store, err := open(t.Context(), beadsDir)
					if err == nil {
						if store != nil {
							_ = store.Close()
						}
						t.Fatalf("expected removed-backend error for %s", backend)
					}
					if !strings.Contains(err.Error(), "no longer supported") {
						t.Fatalf("error should explain that %s is no longer supported: %v", backend, err)
					}
					guidance := strings.ToLower(err.Error())
					if !strings.Contains(guidance, "export") || !strings.Contains(guidance, "dolt") {
						t.Fatalf("error should provide safe migration guidance: %v", err)
					}
					for _, name := range []string{"embeddeddolt", "dolt", "beads.db"} {
						path := filepath.Join(beadsDir, name)
						if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
							t.Fatalf("removed backend %s created local storage at %s (stat error: %v)", backend, path, statErr)
						}
					}
				})
			}
		})
	}
}

func TestRequireDoltBackend(t *testing.T) {
	for _, cfg := range []*configfile.Config{nil, {}, {Backend: configfile.BackendDolt}} {
		if err := requireDoltBackend(cfg); err != nil {
			t.Fatalf("Dolt config %#v rejected: %v", cfg, err)
		}
	}

	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite} {
		err := requireDoltBackend(&configfile.Config{Backend: backend})
		if err == nil || !strings.Contains(err.Error(), "no longer supported") || !strings.Contains(err.Error(), "export") {
			t.Fatalf("removed backend %q guard error = %v, want rollback and migration guidance", backend, err)
		}
	}
}

// removedBackendWantSubstrings returns the guidance substrings every removed-backend
// rejection must carry, including the backend-specific rationale marker.
func removedBackendWantSubstrings(backend string) []string {
	wants := []string{"no longer supported", "was not opened", "export", "dolt"}
	if backend == configfile.BackendSQLite {
		return append(wants, "single engine")
	}
	return append(wants, "general-purpose server databases", "simple and resource-light")
}
