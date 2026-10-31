package dolt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/backendnames"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// TestResolveAutoStart verifies all conditions that govern the AutoStart decision.
//
// Each subtest uses t.Setenv for env-var isolation: t.Setenv records the
// original value (including the unset state) and restores it after the test,
// correctly handling cases where a variable was previously unset vs. set to "".
func TestResolveAutoStart(t *testing.T) {
	tests := []struct {
		name             string
		testMode         string // BEADS_TEST_MODE to set; "" leaves it unset/empty
		autoStartEnv     string // BEADS_DOLT_AUTO_START to set; "" leaves it unset/empty
		doltAutoStartCfg string // raw value of "dolt.auto-start" from config.yaml
		currentValue     bool   // AutoStart value supplied by caller
		wantAutoStart    bool
	}{
		{
			name:          "defaults to true for standalone user",
			wantAutoStart: true,
		},
		{
			name:          "disabled when BEADS_TEST_MODE=1",
			testMode:      "1",
			wantAutoStart: false,
		},
		{
			name:          "disabled when BEADS_DOLT_AUTO_START=0",
			autoStartEnv:  "0",
			wantAutoStart: false,
		},
		{
			name:          "enabled when BEADS_DOLT_AUTO_START=1",
			autoStartEnv:  "1",
			wantAutoStart: true,
		},
		{
			name:             "disabled when dolt.auto-start=false in config",
			doltAutoStartCfg: "false",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=0 in config",
			doltAutoStartCfg: "0",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=off in config",
			doltAutoStartCfg: "off",
			wantAutoStart:    false,
		},
		{
			name:          "test mode wins over BEADS_DOLT_AUTO_START=1",
			testMode:      "1",
			autoStartEnv:  "1",
			wantAutoStart: false,
		},
		{
			name:          "caller true preserved when no overrides",
			currentValue:  true,
			wantAutoStart: true,
		},
		{
			// Config opt-out must still win when callers pass current=true.
			name:             "config.yaml opt-out wins over caller true",
			currentValue:     true,
			doltAutoStartCfg: "false",
			wantAutoStart:    false,
		},
		{
			name:          "test mode overrides caller true",
			testMode:      "1",
			currentValue:  true,
			wantAutoStart: false,
		},
		{
			name:          "BEADS_DOLT_AUTO_START=0 overrides caller true",
			autoStartEnv:  "0",
			currentValue:  true,
			wantAutoStart: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", tc.testMode)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStartEnv)

			got := resolveAutoStart(tc.currentValue, tc.doltAutoStartCfg, ServerModeOwned)
			if got != tc.wantAutoStart {
				t.Errorf("resolveAutoStart(current=%v, configVal=%q, mode=Owned) = %v, want %v",
					tc.currentValue, tc.doltAutoStartCfg, got, tc.wantAutoStart)
			}
		})
	}
}

// TestApplyCLIAutoStart_RespectsExternalMode verifies that an external-mode
// repo (metadata.json with explicit dolt_server_port) suppresses the CLI
// auto-start path, preventing the shadow-database fallback when the
// configured external server is transiently unreachable.
//
// Regression for the case where ApplyCLIAutoStart hardcoded ServerModeOwned
// and bypassed resolveAutoStart's external-mode check, so any bd command in
// an external-mode repo could spawn a fallback embedded server when the
// configured server didn't respond (e.g. during a service cutover).
func TestApplyCLIAutoStart_RespectsExternalMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_TEST_MODE", "")

	beadsDir := t.TempDir()
	// metadata.json with explicit dolt_server_port → ServerModeExternal.
	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerPort = 3399
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	if got := doltserver.ResolveServerMode(beadsDir); got != doltserver.ServerModeExternal {
		t.Fatalf("ResolveServerMode = %v, want External (preflight)", got)
	}

	// No config.yaml dolt.auto-start set — relies entirely on External-
	// mode suppression. Pre-fix this would return AutoStart=true (default).
	storeCfg := &Config{}
	ApplyCLIAutoStart(beadsDir, storeCfg)
	if storeCfg.AutoStart {
		t.Errorf("ApplyCLIAutoStart set AutoStart=true in external-mode repo; want false (shadow database protection)")
	}
}

func TestApplyCLIAutoStart_DisableAutoStartWins(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_TEST_MODE", "")

	beadsDir := t.TempDir()
	if err := configfile.DefaultConfig().Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	storeCfg := &Config{DisableAutoStart: true}
	ApplyCLIAutoStart(beadsDir, storeCfg)
	if storeCfg.AutoStart {
		t.Fatal("DisableAutoStart should suppress CLI auto-start defaults")
	}
}

func TestConfigConstructorsRejectNonDoltBackends(t *testing.T) {
	for _, backend := range []string{
		configfile.BackendSQLite,
		configfile.BackendPostgres,
		configfile.BackendMySQL,
		"mystery",
	} {
		t.Run(backend, func(t *testing.T) {
			beadsDir := t.TempDir()
			if err := (&configfile.Config{Backend: backend}).Save(beadsDir); err != nil {
				t.Fatalf("save metadata: %v", err)
			}

			constructors := map[string]func() (*DoltStore, error){
				"standard": func() (*DoltStore, error) {
					return NewFromConfigWithOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true})
				},
				"cli": func() (*DoltStore, error) {
					return NewFromConfigWithCLIOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true})
				},
			}
			for name, construct := range constructors {
				t.Run(name, func(t *testing.T) {
					store, err := construct()
					if store != nil {
						_ = store.Close()
						t.Fatalf("non-Dolt backend %q unexpectedly opened as Dolt", backend)
					}
					if err == nil || !strings.Contains(err.Error(), "cannot be opened as Dolt") {
						t.Fatalf("constructor error = %v, want backend mismatch", err)
					}
					if backend == configfile.BackendPostgres || backend == configfile.BackendMySQL {
						for _, want := range []string{"no longer supported", "was not opened or modified", "bd help init-safety"} {
							if !strings.Contains(err.Error(), want) {
								t.Errorf("removed-backend constructor error missing %q: %v", want, err)
							}
						}
					}
				})
			}
		})
	}
}

// TestConfigConstructorsRefuseGraphWorkspace pins that every metadata-driven
// constructor refuses a workspace whose metadata declares another data model,
// before it resolves a server or writes anything into the workspace (a port
// file, a Dolt root, or the metadata.json that Load would migrate a legacy
// config.json into). The CLI's own store open does not come through here; the
// public beads package's config opens and cmd/bd's direct NewFromConfig*
// callers do.
func TestConfigConstructorsRefuseGraphWorkspace(t *testing.T) {
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	for _, mode := range []string{configfile.GraphModeLink, "future-format"} {
		for _, doltMode := range []string{configfile.DoltModeEmbedded, configfile.DoltModeServer} {
			// "config.json" is the legacy name Load migrates into metadata.json;
			// a refused open must not run that migration either.
			for _, fileName := range []string{configfile.ConfigFileName, "config.json"} {
				t.Run(mode+"/"+doltMode+"/"+fileName, func(t *testing.T) {
					beadsDir := t.TempDir()
					metadata := `{"backend":"dolt","dolt_mode":"` + doltMode + `","dolt_database":"beads_graph_0123","graph_mode":"` + mode + `","graph_future_binding":"keep-me"}`
					path := filepath.Join(beadsDir, fileName)
					if err := os.WriteFile(path, []byte(metadata), 0o600); err != nil {
						t.Fatal(err)
					}
					constructors := map[string]func() (*DoltStore, error){
						"default": func() (*DoltStore, error) {
							return NewFromConfig(t.Context(), beadsDir)
						},
						"standard": func() (*DoltStore, error) {
							return NewFromConfigWithOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true, CreateIfMissing: true})
						},
						"cli": func() (*DoltStore, error) {
							return NewFromConfigWithCLIOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true, ReadOnly: true})
						},
					}
					for name, construct := range constructors {
						t.Run(name, func(t *testing.T) {
							store, err := construct()
							if err == nil {
								_ = store.Close()
								t.Fatalf("graph_mode %q workspace opened as Dolt", mode)
							}
							var formatErr *configfile.UnsupportedWorkspaceFormatError
							if !errors.As(err, &formatErr) || formatErr.BeadsDir != beadsDir || formatErr.GraphMode != mode {
								t.Fatalf("constructor error = %v, want UnsupportedWorkspaceFormatError for %s", err, beadsDir)
							}
							entries, readErr := os.ReadDir(beadsDir)
							if readErr != nil || len(entries) != 1 {
								t.Fatalf("refused open changed the workspace: %v, %v", entries, readErr)
							}
							if got, readErr := os.ReadFile(path); readErr != nil || string(got) != metadata {
								t.Fatalf("refused open rewrote %s: %q, %v", fileName, got, readErr)
							}
						})
					}
				})
			}
		}
	}
}

// TestConfigConstructorsRefuseGraphFormatMarker pins that every
// metadata-driven constructor refuses a workspace holding the
// graph-preview-format marker graph-preview builds write, whatever its
// metadata says (none, stripped of graph_mode in either metadata file, an
// explicit dependency, or unparseable), and leaves the workspace unchanged.
func TestConfigConstructorsRefuseGraphFormatMarker(t *testing.T) {
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	layouts := []struct {
		name     string
		fileName string // "" writes no metadata file
		metadata string
	}{
		{name: "marker only"},
		{name: "stripped embedded metadata", fileName: configfile.ConfigFileName, metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"beads_graph_0123"}`},
		{name: "stripped server metadata", fileName: configfile.ConfigFileName, metadata: `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_graph_0123"}`},
		{name: "stripped legacy config.json", fileName: "config.json", metadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"beads_graph_0123"}`},
		{name: "dependency metadata", fileName: configfile.ConfigFileName, metadata: `{"backend":"dolt","dolt_mode":"server","graph_mode":"dependency"}`},
		{name: "unparseable metadata", fileName: configfile.ConfigFileName, metadata: `{"backend":"dolt",`},
	}
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			want := map[string]string{configfile.GraphFormatMarkerFile: "link-preview-v9\n"}
			if layout.fileName != "" {
				want[layout.fileName] = layout.metadata
			}
			for name, content := range want {
				if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			constructors := map[string]func() (*DoltStore, error){
				"default": func() (*DoltStore, error) {
					return NewFromConfig(t.Context(), beadsDir)
				},
				"standard": func() (*DoltStore, error) {
					return NewFromConfigWithOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true, CreateIfMissing: true})
				},
				"cli": func() (*DoltStore, error) {
					return NewFromConfigWithCLIOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true, ReadOnly: true})
				},
			}
			for name, construct := range constructors {
				t.Run(name, func(t *testing.T) {
					store, err := construct()
					if err == nil {
						_ = store.Close()
						t.Fatalf("workspace with a %s marker opened as Dolt", configfile.GraphFormatMarkerFile)
					}
					var formatErr *configfile.UnsupportedWorkspaceFormatError
					if !errors.As(err, &formatErr) || formatErr.BeadsDir != beadsDir || !formatErr.FormatMarker {
						t.Fatalf("constructor error = %v, want the format marker refusal for %s", err, beadsDir)
					}
					entries, readErr := os.ReadDir(beadsDir)
					if readErr != nil || len(entries) != len(want) {
						t.Fatalf("refused open changed the workspace: %v, %v", entries, readErr)
					}
					for fileName, content := range want {
						if got, readErr := os.ReadFile(filepath.Join(beadsDir, fileName)); readErr != nil || string(got) != content {
							t.Fatalf("refused open rewrote %s: %q, %v", fileName, got, readErr)
						}
					}
				})
			}
		})
	}
}

// TestConfigConstructorsRefuseNonDoltBackendInLegacyConfig pins that every
// metadata-driven constructor refuses a legacy config.json naming a backend it
// cannot open as Dolt (removed, unrecognized, or another binary's registered
// backend) before configfile.Load migrates that file into metadata.json,
// dropping the keys Config does not know. The workspace holds a Dolt database,
// so a removed backend's refusal offers the metadata heal; it must not have
// rewritten the metadata first, and it names config.json, the file that holds
// the backend, rather than a metadata.json that does not exist.
func TestConfigConstructorsRefuseNonDoltBackendInLegacyConfig(t *testing.T) {
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	const registered = "legacy-config-registered-fixture"
	backendnames.Add(registered)
	t.Cleanup(func() { backendnames.Remove(registered) })
	for _, backend := range []string{configfile.BackendSQLite, configfile.BackendPostgres, configfile.BackendMySQL, "mystery", registered} {
		for _, doltMode := range []string{configfile.DoltModeEmbedded, configfile.DoltModeServer} {
			t.Run(backend+"/"+doltMode, func(t *testing.T) {
				beadsDir := t.TempDir()
				if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
					t.Fatal(err)
				}
				legacy := `{"backend":"` + backend + `","dolt_mode":"` + doltMode + `","dolt_database":"beads_legacy","future_binding":"keep-me"}`
				legacyPath := filepath.Join(beadsDir, "config.json")
				if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
					t.Fatal(err)
				}
				constructors := map[string]func() (*DoltStore, error){
					"default": func() (*DoltStore, error) {
						return NewFromConfig(t.Context(), beadsDir)
					},
					"standard": func() (*DoltStore, error) {
						return NewFromConfigWithOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true, CreateIfMissing: true})
					},
					"cli": func() (*DoltStore, error) {
						return NewFromConfigWithCLIOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true, ReadOnly: true})
					},
				}
				for name, construct := range constructors {
					t.Run(name, func(t *testing.T) {
						store, err := construct()
						if err == nil {
							_ = store.Close()
							t.Fatalf("backend %q opened as Dolt", backend)
						}
						if !strings.Contains(err.Error(), "cannot be opened as Dolt") || !strings.Contains(err.Error(), strconv.Quote(backend)) {
							t.Fatalf("constructor error = %v, want the refusal of backend %q", err, backend)
						}
						// The registered backend's refusal names no file.
						if backend != registered && (!strings.Contains(err.Error(), "config.json") || strings.Contains(err.Error(), "metadata.json")) {
							t.Fatalf("constructor error = %v, want it to name config.json and not metadata.json", err)
						}
						entries, readErr := os.ReadDir(beadsDir)
						names := make([]string, 0, len(entries))
						for _, entry := range entries {
							names = append(names, entry.Name())
						}
						if readErr != nil || !slices.Equal(names, []string{"config.json", "dolt"}) {
							t.Fatalf("refused open changed the workspace: %v, %v", names, readErr)
						}
						if got, readErr := os.ReadFile(legacyPath); readErr != nil || string(got) != legacy {
							t.Fatalf("refused open rewrote config.json: %q, %v", got, readErr)
						}
					})
				}
			})
		}
	}
}

func TestCLIDirUsesSharedDoltRootInSharedServerMode(t *testing.T) {
	sharedRoot := t.TempDir()
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedRoot)

	store := &DoltStore{
		serverMode: true,
		beadsDir:   filepath.Join(t.TempDir(), ".beads"),
		dbPath:     filepath.Join(t.TempDir(), ".beads", "dolt"),
		database:   "shared_db",
	}

	want := filepath.Join(sharedRoot, "dolt", "shared_db")
	if got := store.CLIDir(); got != want {
		t.Fatalf("CLIDir() = %q, want %q", got, want)
	}
}

func TestCLIDirUsesDbPathOutsideSharedServerMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")

	dbPath := filepath.Join(t.TempDir(), ".beads", "dolt")
	store := &DoltStore{
		serverMode: true,
		beadsDir:   filepath.Join(t.TempDir(), ".beads"),
		dbPath:     dbPath,
		database:   "local_db",
	}

	want := filepath.Join(dbPath, "local_db")
	if got := store.CLIDir(); got != want {
		t.Fatalf("CLIDir() = %q, want %q", got, want)
	}
}

func TestApplyResolvedConfig(t *testing.T) {
	// The config.yaml rung of the pool-knob ladders, for library consumers.
	// The config.GetString rungs read a package-global viper populated only
	// by cmd/bd's config.Initialize(), so for a process that links beads as
	// a library they always return "" and the project's configured values
	// were silently ignored. applyResolvedConfig falls back to a direct read
	// of <beadsDir>/config.yaml, the same pattern dolt.auto-start carries.
	writeConfigYAML := func(t *testing.T, beadsDir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("writing config.yaml: %v", err)
		}
	}
	ladderFileCfg := func() *configfile.Config {
		return &configfile.Config{Backend: configfile.BackendDolt, DoltDatabase: "ladder"}
	}

	t.Run("config.yaml populates pool knobs with no env and no global viper", func(t *testing.T) {
		// The premise is load-bearing: if this package ever initializes the
		// global viper, the dir-fallback rung under test is no longer the one
		// answering and the subtest would pass vacuously.
		if config.GetString("dolt.pool-read-timeout") != "" || config.GetString("dolt.max-conns") != "" {
			t.Fatal("global viper unexpectedly configured; this subtest's premise is broken")
		}
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_MAX_CONNS", "")
		beadsDir := t.TempDir()
		writeConfigYAML(t, beadsDir, "dolt:\n  pool-read-timeout: 120s\n  pool-write-timeout: 45\n  max-conns: 25\n")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, ladderFileCfg(), cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.PoolReadTimeout != 120*time.Second {
			t.Fatalf("PoolReadTimeout = %v, want 120s from config.yaml", cfg.PoolReadTimeout)
		}
		if cfg.PoolWriteTimeout != 45*time.Second {
			t.Fatalf("PoolWriteTimeout = %v, want 45s from config.yaml (bare number = seconds)", cfg.PoolWriteTimeout)
		}
		if cfg.MaxOpenConns != 25 {
			t.Fatalf("MaxOpenConns = %d, want 25 from config.yaml", cfg.MaxOpenConns)
		}
	})

	t.Run("env var wins over config.yaml for pool deadlines", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "90s")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		beadsDir := t.TempDir()
		writeConfigYAML(t, beadsDir, "dolt:\n  pool-read-timeout: 120s\n")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, ladderFileCfg(), cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.PoolReadTimeout != 90*time.Second {
			t.Fatalf("PoolReadTimeout = %v, want the env's 90s over the file's 120s", cfg.PoolReadTimeout)
		}
	})

	t.Run("no config.yaml leaves the pool deadlines unset", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), t.TempDir(), ladderFileCfg(), cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.PoolReadTimeout != 0 || cfg.PoolWriteTimeout != 0 {
			t.Fatalf("pool deadlines = %v/%v, want 0/0 so buildServerDSN applies its default", cfg.PoolReadTimeout, cfg.PoolWriteTimeout)
		}
	})

	t.Run("fills server config for legacy metadata without dolt_mode", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltDatabase: "beads_codex",
			ProjectID:    "proj-123",
		}
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.BeadsDir != beadsDir {
			t.Fatalf("BeadsDir = %q, want %q", cfg.BeadsDir, beadsDir)
		}
		if cfg.Path != fileCfg.DatabasePath(beadsDir) {
			t.Fatalf("Path = %q, want %q", cfg.Path, fileCfg.DatabasePath(beadsDir))
		}
		if cfg.Database != "beads_codex" {
			t.Fatalf("Database = %q, want beads_codex", cfg.Database)
		}
		if cfg.ServerHost != fileCfg.GetDoltServerHost() {
			t.Fatalf("ServerHost = %q, want %q", cfg.ServerHost, fileCfg.GetDoltServerHost())
		}
		if cfg.ServerUser != fileCfg.GetDoltServerUser() {
			t.Fatalf("ServerUser = %q, want %q", cfg.ServerUser, fileCfg.GetDoltServerUser())
		}
		wantPort := doltserver.DefaultConfig(beadsDir).Port
		if cfg.ServerPort != wantPort {
			t.Fatalf("ServerPort = %d, want %d", cfg.ServerPort, wantPort)
		}
	})

	t.Run("warns when data-dir set in server mode (GH#2438)", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_CodeWriter7",
			DoltDataDir:  "/some/stale/path",
		}
		cfg := &Config{}

		// Capture stderr to verify warning
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		output := buf.String()
		if !strings.Contains(output, "dolt_data_dir is set") {
			t.Errorf("expected data-dir warning in server mode, got: %q", output)
		}
		if !strings.Contains(output, "server mode") {
			t.Errorf("expected 'server mode' in warning, got: %q", output)
		}
	})

	t.Run("no warning when data-dir empty in server mode", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_CodeWriter7",
		}
		cfg := &Config{}

		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		if buf.Len() > 0 {
			t.Errorf("expected no warning when data-dir is empty, got: %q", buf.String())
		}
	})

	t.Run("preserves caller overrides", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltDatabase: "beads_codex",
		}
		cfg := &Config{
			BeadsDir:   "/override/.beads",
			Database:   "caller_db",
			ServerHost: "10.0.0.9",
			ServerPort: 15432,
			ServerUser: "custom",
		}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.BeadsDir != "/override/.beads" {
			t.Fatalf("BeadsDir override lost: %q", cfg.BeadsDir)
		}
		if cfg.Database != "caller_db" {
			t.Fatalf("Database override lost: %q", cfg.Database)
		}
		if cfg.ServerHost != "10.0.0.9" {
			t.Fatalf("ServerHost override lost: %q", cfg.ServerHost)
		}
		if cfg.ServerPort != 15432 {
			t.Fatalf("ServerPort override lost: %d", cfg.ServerPort)
		}
		if cfg.ServerUser != "custom" {
			t.Fatalf("ServerUser override lost: %q", cfg.ServerUser)
		}
	})

	// Socket precedence: caller, then BEADS_DOLT_SERVER_SOCKET, then
	// metadata.json. Each case pins the env var and clears the credential
	// command, which runs in server mode and could fail the resolve first.
	socketFileCfg := func(t *testing.T, socket string) *configfile.Config {
		t.Helper()
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "")
		return &configfile.Config{
			Backend:          configfile.BackendDolt,
			DoltMode:         configfile.DoltModeServer,
			DoltDatabase:     "beads_codex",
			DoltServerSocket: socket,
		}
	}

	t.Run("applies socket from metadata", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), t.TempDir(), socketFileCfg(t, "/metadata/dolt.sock"), cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.ServerSocket != "/metadata/dolt.sock" {
			t.Fatalf("ServerSocket = %q, want the metadata socket %q", cfg.ServerSocket, "/metadata/dolt.sock")
		}
	})

	t.Run("caller socket wins over env and metadata", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/env/dolt.sock")
		cfg := &Config{ServerSocket: "/caller/dolt.sock"}

		if err := applyResolvedConfig(context.Background(), t.TempDir(), socketFileCfg(t, "/metadata/dolt.sock"), cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.ServerSocket != "/caller/dolt.sock" {
			t.Fatalf("ServerSocket = %q, want the caller's socket %q", cfg.ServerSocket, "/caller/dolt.sock")
		}
	})

	t.Run("env socket wins over metadata", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/env/dolt.sock")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), t.TempDir(), socketFileCfg(t, "/metadata/dolt.sock"), cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.ServerSocket != "/env/dolt.sock" {
			t.Fatalf("ServerSocket = %q, want the env socket %q", cfg.ServerSocket, "/env/dolt.sock")
		}
	})

	t.Run("no socket anywhere leaves TCP config untouched", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")
		t.Setenv("BEADS_DOLT_SERVER_HOST", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		beadsDir := t.TempDir()
		fileCfg := socketFileCfg(t, "")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.ServerSocket != "" {
			t.Fatalf("ServerSocket = %q, want empty (TCP)", cfg.ServerSocket)
		}
		if cfg.ServerHost != fileCfg.GetDoltServerHost() {
			t.Fatalf("ServerHost = %q, want %q", cfg.ServerHost, fileCfg.GetDoltServerHost())
		}
		if want := doltserver.DefaultConfig(beadsDir).Port; cfg.ServerPort != want {
			t.Fatalf("ServerPort = %d, want %d", cfg.ServerPort, want)
		}
	})

	t.Run("pool timeouts populate from env vars (bd-vz0y9)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "90s")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "45")
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{Backend: configfile.BackendDolt}, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.PoolReadTimeout != 90*time.Second {
			t.Fatalf("PoolReadTimeout = %v, want 90s", cfg.PoolReadTimeout)
		}
		if cfg.PoolWriteTimeout != 45*time.Second {
			t.Fatalf("PoolWriteTimeout = %v, want 45s (bare number = seconds)", cfg.PoolWriteTimeout)
		}
	})

	t.Run("caller-set pool timeouts win over env vars", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "90s")
		cfg := &Config{PoolReadTimeout: 2 * time.Minute}

		if err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{Backend: configfile.BackendDolt}, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.PoolReadTimeout != 2*time.Minute {
			t.Fatalf("PoolReadTimeout = %v, want caller's 2m", cfg.PoolReadTimeout)
		}
	})

	t.Run("propagates ServerPortSource and ServerPortSharedServer alongside ServerPort (be-9tju)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		beadsDir := t.TempDir()
		if err := doltserver.EnsurePortFile(beadsDir, 14567); err != nil {
			t.Fatalf("EnsurePortFile: %v", err)
		}
		fileCfg := &configfile.Config{Backend: configfile.BackendDolt}
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

		if cfg.ServerPort != 14567 {
			t.Fatalf("ServerPort = %d, want 14567", cfg.ServerPort)
		}
		if cfg.ServerPortSource != doltserver.PortSourcePortFile {
			t.Fatalf("ServerPortSource = %q, want %q (applyResolvedConfig must propagate doltserver.DefaultConfig's PortSource, not just Port)", cfg.ServerPortSource, doltserver.PortSourcePortFile)
		}
		if cfg.ServerPortSharedServer != false {
			t.Fatalf("ServerPortSharedServer = %v, want false", cfg.ServerPortSharedServer)
		}
	})

	t.Run("chained through applyConfigDefaults: port-file resolution is never mislabeled caller-explicit (be-9tju)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		beadsDir := t.TempDir()
		if err := doltserver.EnsurePortFile(beadsDir, 14567); err != nil {
			t.Fatalf("EnsurePortFile: %v", err)
		}
		fileCfg := &configfile.Config{Backend: configfile.BackendDolt}
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		applyConfigDefaults(cfg)

		if cfg.ServerPortSource != doltserver.PortSourcePortFile {
			t.Fatalf("ServerPortSource = %q, want %q (a port-file-resolved port must not be mislabeled caller_explicit — GH#4052 auto-start fail-closed logic depends on this)", cfg.ServerPortSource, doltserver.PortSourcePortFile)
		}
		if cfg.ServerPort != 14567 {
			t.Fatalf("ServerPort = %d, want 14567 (unchanged)", cfg.ServerPort)
		}
	})

	t.Run("legacy BEADS_DOLT_PORT redirects away from a non-authoritative port-file port (be-9tju)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "43211")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		beadsDir := t.TempDir()
		if err := doltserver.EnsurePortFile(beadsDir, 14567); err != nil {
			t.Fatalf("EnsurePortFile: %v", err)
		}
		fileCfg := &configfile.Config{Backend: configfile.BackendDolt}
		cfg := &Config{}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		applyConfigDefaults(cfg)

		if cfg.ServerPort != 43211 {
			t.Fatalf("ServerPort = %d, want 43211 (legacy BEADS_DOLT_PORT must override a non-authoritative port-file-resolved port — regression of hq-27t bug class)", cfg.ServerPort)
		}
		if cfg.ServerPortSource != doltserver.PortSourceEnv {
			t.Fatalf("ServerPortSource = %q, want %q", cfg.ServerPortSource, doltserver.PortSourceEnv)
		}
	})

	t.Run("genuinely caller-explicit ServerPort still outranks ambient BEADS_DOLT_SERVER_PORT env (be-wf9a.1 regression safety)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "43211")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{Backend: configfile.BackendDolt}
		cfg := &Config{ServerPort: 15000}

		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		applyConfigDefaults(cfg)

		if cfg.ServerPort != 15000 {
			t.Fatalf("ServerPort = %d, want 15000 (caller-explicit preset must outrank ambient env)", cfg.ServerPort)
		}
		if cfg.ServerPortSource != doltserver.PortSourceCallerExplicit {
			t.Fatalf("ServerPortSource = %q, want %q", cfg.ServerPortSource, doltserver.PortSourceCallerExplicit)
		}
	})
}

// TestConfigConstructorsRefuseHistoricalSQLiteWorkspace pins that every
// metadata-driven constructor, and the pre-check a caller runs before writing
// into a workspace it then opens (RequireDiscoveredDoltBackend, OpenGated's),
// refuses a historical SQLite workspace with the refusal command admission
// prints, before configfile.Load migrates a pre-Dolt config.json into
// metadata.json, dropping the keys Config does not know.
func TestConfigConstructorsRefuseHistoricalSQLiteWorkspace(t *testing.T) {
	// If the refusal were missing, the open must fail fast rather than reach
	// the suite's shared test server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	opens := map[string]func(context.Context, string) error{
		"RequireDiscoveredDoltBackend": func(_ context.Context, beadsDir string) error {
			return RequireDiscoveredDoltBackend(beadsDir)
		},
		"NewFromConfig": func(ctx context.Context, beadsDir string) error {
			return closeOnSuccess(NewFromConfig(ctx, beadsDir))
		},
		"NewFromConfigWithOptions": func(ctx context.Context, beadsDir string) error {
			return closeOnSuccess(NewFromConfigWithOptions(ctx, beadsDir, &Config{DisableAutoStart: true, CreateIfMissing: true}))
		},
		"NewFromConfigWithCLIOptions": func(ctx context.Context, beadsDir string) error {
			return closeOnSuccess(NewFromConfigWithCLIOptions(ctx, beadsDir, &Config{DisableAutoStart: true, ReadOnly: true}))
		},
	}
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		for name, open := range opens {
			t.Run(layout.Name+"/"+name, func(t *testing.T) {
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				layout.Write(t, beadsDir)
				before := legacyworkspace.TreeDigest(t, beadsDir)
				err := open(t.Context(), beadsDir)
				if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, before, after)
				}
				if err == nil || err.Error() != legacyworkspace.HistoricalSQLiteRefusal {
					t.Fatalf("%s error = %v, want admission's refusal %q", name, err, legacyworkspace.HistoricalSQLiteRefusal)
				}
			})
		}
	}
}

func closeOnSuccess(store *DoltStore, err error) error {
	if err == nil {
		_ = store.Close()
	}
	return err
}

// TestReadOnlyConstructorsLeaveLegacyConfigJSONInPlace pins that a read-only
// open (Config.ReadOnly) through the metadata-driven constructors reads a
// legacy config.json in place rather than migrating it into metadata.json,
// which drops the keys Config does not know: a read-only open writes nothing
// into the workspace it reads, often one bd was not launched in (an external
// project, a routed read, doctor's additional repos). A writable open still
// migrates it, as before.
func TestReadOnlyConstructorsLeaveLegacyConfigJSONInPlace(t *testing.T) {
	// The open must fail fast rather than reach the suite's shared server.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	const legacy = `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_ro","jsonl_export":"issues.jsonl"}`
	opens := map[string]struct {
		open     func(context.Context, string) error
		migrates bool
	}{
		"NewFromConfigWithOptions read-only": {open: func(ctx context.Context, beadsDir string) error {
			return closeOnSuccess(NewFromConfigWithOptions(ctx, beadsDir, &Config{ReadOnly: true, DisableAutoStart: true}))
		}},
		"NewFromConfigWithCLIOptions read-only": {open: func(ctx context.Context, beadsDir string) error {
			return closeOnSuccess(NewFromConfigWithCLIOptions(ctx, beadsDir, &Config{ReadOnly: true, DisableAutoStart: true}))
		}},
		"NewFromConfigWithOptions writable": {open: func(ctx context.Context, beadsDir string) error {
			return closeOnSuccess(NewFromConfigWithOptions(ctx, beadsDir, &Config{DisableAutoStart: true}))
		}, migrates: true},
	}
	for name, tc := range opens {
		t.Run(name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			legacyworkspace.Layout{Files: map[string]string{"config.json": legacy}}.Write(t, beadsDir)
			before := legacyworkspace.TreeDigest(t, beadsDir)
			_ = tc.open(t.Context(), beadsDir)
			after := legacyworkspace.TreeDigest(t, beadsDir)
			if tc.migrates {
				if _, ok := after["metadata.json"]; !ok {
					t.Fatalf("%s did not migrate the legacy config.json: %v", name, slices.Sorted(maps.Keys(after)))
				}
				return
			}
			if !maps.Equal(after, before) {
				t.Fatalf("%s changed the workspace: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
			}
		})
	}
}
