package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
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

	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}
	if err := (&configfile.Config{Backend: configfile.BackendSQLite}).Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			cmd := exec.Command(bd, command.args...)
			cmd.Dir = root
			cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%q unexpectedly succeeded for removed backend %s: %s", strings.Join(command.args, " "), configfile.BackendSQLite, out)
			}

			message := strings.ToLower(string(out))
			for _, want := range removedBackendWantSubstrings(configfile.BackendSQLite) {
				if !strings.Contains(message, want) {
					t.Errorf("%q error for %s missing %q: %s", strings.Join(command.args, " "), configfile.BackendSQLite, want, message)
				}
			}

			for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version"} {
				path := filepath.Join(beadsDir, name)
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("%q created local state for removed backend %s at %s (stat error: %v)", strings.Join(command.args, " "), configfile.BackendSQLite, path, statErr)
				}
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
					// With no Dolt data on disk the configured database really is
					// the only copy, so the export path is right and the D-8 heal
					// must not be offered — that edit would open an empty store.
					if strings.Contains(guidance, "to heal") {
						t.Fatalf("error offered a metadata heal for a workspace with no Dolt data: %v", err)
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

// TestStoreFactoriesRefuseUnsupportedBackendInLegacyConfig pins that every
// store factory refuses a legacy config.json naming a removed or unrecognized
// backend before configfile.Load migrates it into metadata.json, dropping the
// keys Config does not know and deleting config.json. Command admission checks
// only the workspace bd runs in, and these factories also open others (an
// auto-routed target, an external project). The workspace holds a Dolt
// database, so the refusal offers the metadata heal, and it names config.json,
// the file that holds the backend, rather than a metadata.json that does not
// exist. A registered backend passes the check, is migrated as before, and
// dispatches to the registry. It covers both builds' factories; the
// CGO_ENABLED=0 ones run in CI only because bazel.yml's PURE_CMD_BD_TESTS
// allowlists it by name.
func TestStoreFactoriesRefuseUnsupportedBackendInLegacyConfig(t *testing.T) {
	factories := map[string]func(context.Context, string) (storage.DoltStorage, error){
		"read-write": newDoltStoreFromConfig,
		"routed":     newExistingDoltStoreFromConfig,
		"read-only":  newReadOnlyStoreFromConfig,
		"preview":    newPreviewStoreFromConfig,
	}
	writeLegacyConfig := func(t *testing.T, backend string) (string, string) {
		t.Helper()
		beadsDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
			t.Fatal(err)
		}
		legacy := `{"backend":"` + backend + `","dolt_database":"beads_legacy","future_binding":"keep-me"}`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.json"), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		return beadsDir, legacy
	}
	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite, "mystery"} {
		for name, open := range factories {
			t.Run(backend+"/"+name, func(t *testing.T) {
				beadsDir, legacy := writeLegacyConfig(t, backend)
				store, err := open(t.Context(), beadsDir)
				if err == nil {
					_ = store.Close()
					t.Fatalf("%s factory opened a workspace whose config.json names backend %q", name, backend)
				}
				// The workspace first: a regression that migrates config.json
				// must report the rewrite, not just the file the error names.
				entries, readErr := os.ReadDir(beadsDir)
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				if readErr != nil || !slices.Equal(names, []string{"config.json", "dolt"}) {
					t.Fatalf("%s factory changed the workspace: %v, %v", name, names, readErr)
				}
				if got, readErr := os.ReadFile(filepath.Join(beadsDir, "config.json")); readErr != nil || string(got) != legacy {
					t.Fatalf("%s factory rewrote config.json: %q, %v", name, got, readErr)
				}
				if !strings.Contains(err.Error(), strconv.Quote(backend)) || !strings.Contains(err.Error(), configfile.BackendNotOpenedGuarantee) {
					t.Fatalf("%s factory error = %v, want the refusal of backend %q with the data-safety guarantee", name, err, backend)
				}
				if !strings.Contains(err.Error(), "config.json") || strings.Contains(err.Error(), "metadata.json") {
					t.Fatalf("%s factory error = %v, want it to name config.json and not metadata.json", name, err)
				}
			})
		}
	}

	const registered = "legacy-config-contract"
	registerContractBackend(t, registered)
	for name, open := range factories {
		t.Run("registered/"+name, func(t *testing.T) {
			beadsDir, _ := writeLegacyConfig(t, registered)
			want := errRegistryReadWrite
			if name == "read-only" || name == "preview" {
				want = errRegistryReadOnly
			}
			if _, err := open(t.Context(), beadsDir); !errors.Is(err, want) {
				t.Fatalf("%s factory error = %v, want the registry's %v", name, err, want)
			}
			if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr != nil {
				t.Fatalf("%s factory did not migrate the admitted legacy config.json: %v", name, statErr)
			}
		})
	}
}

func TestRequireDoltBackend(t *testing.T) {
	beadsDir := t.TempDir()
	for _, cfg := range []*configfile.Config{nil, {}, {Backend: configfile.BackendDolt}} {
		if err := requireDoltBackend(cfg, beadsDir); err != nil {
			t.Fatalf("Dolt config %#v rejected: %v", cfg, err)
		}
	}

	for _, backend := range []string{configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite} {
		err := requireDoltBackend(&configfile.Config{Backend: backend}, beadsDir)
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
