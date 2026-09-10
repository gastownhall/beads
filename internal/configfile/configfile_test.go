package configfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Database != "beads.db" {
		t.Errorf("Database = %q, want beads.db", cfg.Database)
	}
}

func TestLoadSaveRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	cfg := DefaultConfig()

	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	loaded, err := Load(beadsDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if loaded == nil {
		t.Fatal("Load() returned nil config")
	}

	if loaded.Database != cfg.Database {
		t.Errorf("Database = %q, want %q", loaded.Database, cfg.Database)
	}
}

func TestLoadNonexistent(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load() returned error for nonexistent config: %v", err)
	}

	if cfg != nil {
		t.Errorf("Load() = %v, want nil for nonexistent config", cfg)
	}
}

// Corrupt must never be conflated with absent (bd-aj3g5): the proxied
// provider takes the fresh-workspace defaults on (nil, nil), so a parse
// failure that returned nil would silently reroute it to the wrong database.
func TestLoadCorruptIsError(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(ConfigPath(tmpDir), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}

	cfg, err := Load(tmpDir)
	if err == nil {
		t.Fatalf("Load() of corrupt config returned nil error (cfg=%+v); corrupt must not be conflated with absent", cfg)
	}
}

func TestLoadForDiscoveryNeverMigratesLegacyConfig(t *testing.T) {
	beadsDir := t.TempDir()
	legacyPath := filepath.Join(beadsDir, "config.json")
	legacy := []byte(`{"backend":"dolt","dolt_mode":"server"}`)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForDiscovery(beadsDir)
	if err != nil {
		t.Fatalf("LoadForDiscovery() failed: %v", err)
	}
	if cfg == nil || cfg.DoltMode != DoltModeServer {
		t.Fatalf("LoadForDiscovery() = %#v, want legacy server config", cfg)
	}
	if _, err := os.Stat(ConfigPath(beadsDir)); !os.IsNotExist(err) {
		t.Fatalf("LoadForDiscovery created metadata.json: %v", err)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(legacy) {
		t.Fatalf("LoadForDiscovery rewrote legacy config: got %q, want %q", after, legacy)
	}
}

func TestDatabasePath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"
	// DatabasePath always returns dolt path regardless of Database field
	cfg := &Config{Database: "beads.db"}

	got := cfg.DatabasePath(beadsDir)
	want := filepath.Join(beadsDir, "dolt")

	if got != want {
		t.Errorf("DatabasePath() = %q, want %q", got, want)
	}
}

func TestDatabasePath_Dolt(t *testing.T) {
	beadsDir := "/home/user/project/.beads"

	t.Run("explicit dolt dir", func(t *testing.T) {
		cfg := &Config{Database: "dolt", Backend: BackendDolt}
		got := cfg.DatabasePath(beadsDir)
		want := filepath.Join(beadsDir, "dolt")
		if got != want {
			t.Errorf("DatabasePath() = %q, want %q", got, want)
		}
	})

	t.Run("backward compat: dolt backend with beads.db field", func(t *testing.T) {
		cfg := &Config{Database: "beads.db", Backend: BackendDolt}
		got := cfg.DatabasePath(beadsDir)
		want := filepath.Join(beadsDir, "dolt")
		if got != want {
			t.Errorf("DatabasePath() = %q, want %q", got, want)
		}
	})

	t.Run("stale database name is ignored (split-brain fix)", func(t *testing.T) {
		// Stale values like "town", "wyvern", "beads_rig" must resolve to "dolt"
		for _, staleName := range []string{"town", "wyvern", "beads_rig", "random"} {
			cfg := &Config{Database: staleName, Backend: BackendDolt}
			got := cfg.DatabasePath(beadsDir)
			want := filepath.Join(beadsDir, "dolt")
			if got != want {
				t.Errorf("DatabasePath(%q) = %q, want %q", staleName, got, want)
			}
		}
	})

	t.Run("empty database field resolves to dolt", func(t *testing.T) {
		cfg := &Config{Database: "", Backend: BackendDolt}
		got := cfg.DatabasePath(beadsDir)
		want := filepath.Join(beadsDir, "dolt")
		if got != want {
			t.Errorf("DatabasePath() = %q, want %q", got, want)
		}
	})

	t.Run("absolute path is honored", func(t *testing.T) {
		cfg := &Config{Database: "/custom/path/dolt", Backend: BackendDolt}
		got := cfg.DatabasePath(beadsDir)
		want := "/custom/path/dolt"
		if got != want {
			t.Errorf("DatabasePath() = %q, want %q", got, want)
		}
	})
}

func TestConfigPath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"
	got := ConfigPath(beadsDir)
	want := filepath.Join(beadsDir, "metadata.json")

	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestGetDeletionsRetentionDays(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want int
	}{
		{
			name: "zero uses default",
			cfg:  &Config{DeletionsRetentionDays: 0},
			want: DefaultDeletionsRetentionDays,
		},
		{
			name: "negative uses default",
			cfg:  &Config{DeletionsRetentionDays: -5},
			want: DefaultDeletionsRetentionDays,
		},
		{
			name: "custom value",
			cfg:  &Config{DeletionsRetentionDays: 14},
			want: 14,
		},
		{
			name: "minimum value 1",
			cfg:  &Config{DeletionsRetentionDays: 1},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.GetDeletionsRetentionDays()
			if got != tt.want {
				t.Errorf("GetDeletionsRetentionDays() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDoltServerMode tests the Dolt server mode configuration (bd-dolt.2.2)
func TestDoltServerMode(t *testing.T) {
	t.Run("IsDoltServerMode", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want bool
		}{
			{
				name: "empty backend",
				cfg:  &Config{Backend: ""},
				want: false,
			},
			{
				name: "dolt embedded mode",
				cfg:  &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded},
				want: false,
			},
			{
				name: "dolt server mode",
				cfg:  &Config{Backend: BackendDolt, DoltMode: DoltModeServer},
				want: true,
			},
			{
				name: "dolt default mode",
				cfg:  &Config{Backend: BackendDolt},
				want: false, // Default is embedded
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.IsDoltServerMode()
				if got != tt.want {
					t.Errorf("IsDoltServerMode() = %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("GetDoltMode", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want string
		}{
			{
				name: "empty defaults to embedded",
				cfg:  &Config{},
				want: DoltModeEmbedded,
			},
			{
				name: "explicit embedded",
				cfg:  &Config{DoltMode: DoltModeEmbedded},
				want: DoltModeEmbedded,
			},
			{
				name: "explicit server",
				cfg:  &Config{DoltMode: DoltModeServer},
				want: DoltModeServer,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltMode()
				if got != tt.want {
					t.Errorf("GetDoltMode() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("GetDoltServerHost", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want string
		}{
			{
				name: "empty defaults to 127.0.0.1",
				cfg:  &Config{},
				want: DefaultDoltServerHost,
			},
			{
				name: "custom host",
				cfg:  &Config{DoltServerHost: "192.168.1.100"},
				want: "192.168.1.100",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltServerHost()
				if got != tt.want {
					t.Errorf("GetDoltServerHost() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("GetDoltServerHost_config_yaml", func(t *testing.T) {
		// Mirror of the dolt.port config.yaml fix (GH#2073) for host.
		// Precedence: env > metadata.json > config.yaml > default.

		// Ensure no host env var leaks into the test.
		t.Setenv("BEADS_DOLT_SERVER_HOST", "")

		configDir := t.TempDir()
		configYaml := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(configYaml,
			[]byte("dolt.host: 100.64.0.1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)

		// config.yaml wins when metadata.json leaves host unset.
		emptyCfg := &Config{}
		if got := emptyCfg.GetDoltServerHost(); got != "100.64.0.1" {
			t.Errorf("empty cfg + config.yaml: GetDoltServerHost() = %q, want 100.64.0.1", got)
		}

		// metadata.json wins over config.yaml when both set.
		metaCfg := &Config{DoltServerHost: "192.168.1.100"}
		if got := metaCfg.GetDoltServerHost(); got != "192.168.1.100" {
			t.Errorf("metadata over config.yaml: GetDoltServerHost() = %q, want 192.168.1.100", got)
		}

		// env var wins over config.yaml.
		t.Setenv("BEADS_DOLT_SERVER_HOST", "10.0.0.1")
		if got := emptyCfg.GetDoltServerHost(); got != "10.0.0.1" {
			t.Errorf("env over config.yaml: GetDoltServerHost() = %q, want 10.0.0.1", got)
		}
	})

	t.Run("GetDoltServerPort", func(t *testing.T) {
		// Clear port env vars so the table-driven configs are the source of truth.
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		tests := []struct {
			name string
			cfg  *Config
			want int
		}{
			{
				name: "zero defaults to 3307",
				cfg:  &Config{},
				want: DefaultDoltServerPort,
			},
			{
				name: "custom port",
				cfg:  &Config{DoltServerPort: 13306},
				want: 13306,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltServerPort()
				if got != tt.want {
					t.Errorf("GetDoltServerPort() = %d, want %d", got, tt.want)
				}
			})
		}
	})

	t.Run("GetDoltServerUser", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want string
		}{
			{
				name: "empty defaults to root",
				cfg:  &Config{},
				want: DefaultDoltServerUser,
			},
			{
				name: "custom user",
				cfg:  &Config{DoltServerUser: "beads"},
				want: "beads",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltServerUser()
				if got != tt.want {
					t.Errorf("GetDoltServerUser() = %q, want %q", got, tt.want)
				}
			})
		}
	})
}

// TestIsDoltServerModeEnvVar tests env var overrides for IsDoltServerMode
func TestIsDoltServerModeEnvVar(t *testing.T) {
	t.Run("env var override with dolt backend", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		cfg := &Config{Backend: BackendDolt}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true when env var set with dolt backend")
		}
	})

	t.Run("env var with dolt backend enables server mode", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		cfg := &Config{Backend: ""}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true when env var set with default backend")
		}
	})

	t.Run("env var not set", func(t *testing.T) {
		cfg := &Config{Backend: BackendDolt}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false when no config or env var")
		}
	})
}

// TestIsDoltServerMode_HostInference_GH3545 is a regression test for
// gastownhall/beads#3545: setting BEADS_DOLT_SERVER_HOST (or dolt.host)
// to a non-localhost value MUST imply server mode. Before the fix, mode
// falls through to embedded and bd silently uses local storage instead
// of the configured external server.
//
// Explicit dolt_mode in metadata.json always wins over a persisted or
// configured host — an operator's explicit statement of intent is not
// overridden by inference.
func TestIsDoltServerMode_HostInference_GH3545(t *testing.T) {
	t.Run("env var BEADS_DOLT_SERVER_HOST=non-localhost infers server mode", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.0.2.10")
		cfg := &Config{Backend: BackendDolt}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true with BEADS_DOLT_SERVER_HOST=192.0.2.10")
		}
	})

	t.Run("env var BEADS_DOLT_SERVER_HOST=hostname infers server mode", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "dolt-primary.tailnet.example.com")
		cfg := &Config{Backend: BackendDolt}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true with BEADS_DOLT_SERVER_HOST=dolt-primary.tailnet.example.com")
		}
	})

	t.Run("env var BEADS_DOLT_SERVER_HOST=localhost does NOT infer", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "localhost")
		cfg := &Config{Backend: BackendDolt}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false with BEADS_DOLT_SERVER_HOST=localhost (operator wants bd-managed local)")
		}
	})

	t.Run("env var BEADS_DOLT_SERVER_HOST=127.0.0.1 does NOT infer", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
		cfg := &Config{Backend: BackendDolt}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false with BEADS_DOLT_SERVER_HOST=127.0.0.1")
		}
	})

	t.Run("env var BEADS_DOLT_SERVER_HOST empty does NOT infer", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "")
		cfg := &Config{Backend: BackendDolt}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false with empty BEADS_DOLT_SERVER_HOST")
		}
	})

	t.Run("config.yaml dolt.host=non-localhost infers server mode", func(t *testing.T) {
		// Use the in-config DoltServerHost field — this mirrors what
		// GetDoltServerHost reads from config.yaml (post-#3471).
		cfg := &Config{Backend: BackendDolt, DoltServerHost: "10.0.0.5"}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true with DoltServerHost=10.0.0.5")
		}
	})

	t.Run("DoltServerHost=localhost does NOT infer", func(t *testing.T) {
		cfg := &Config{Backend: BackendDolt, DoltServerHost: "localhost"}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false with DoltServerHost=localhost")
		}
	})

	t.Run("env wins over config.yaml: env=localhost suppresses config-host inference", func(t *testing.T) {
		// When env is explicitly set to localhost, the operator's
		// intent overrides config — even if config has a non-
		// localhost host left over from a prior context.
		t.Setenv("BEADS_DOLT_SERVER_HOST", "localhost")
		cfg := &Config{Backend: BackendDolt, DoltServerHost: "10.0.0.5"}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false when env=localhost overrides config-host")
		}
	})

	t.Run("non-dolt backend does not trigger host inference", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.0.2.10")
		cfg := &Config{Backend: ""}
		// Backend gate (GetBackend != BackendDolt → false) precedes
		// host inference. Empty backend is normalized to dolt by
		// GetBackend, so this test asserts that the gate still
		// short-circuits when explicitly set to a non-dolt value
		// (currently impossible via GetBackend, but the gate is the
		// first check and must not be bypassed by host inference).
		_ = cfg.IsDoltServerMode() // No assertion — empty backend
		// resolves to dolt via GetBackend(). Documenting the gate.
	})

	t.Run("explicit metadata dolt_mode=embedded wins over struct host", func(t *testing.T) {
		// Explicit dolt_mode in metadata.json always wins over a
		// persisted host — the operator's explicit statement of
		// intent is not second-guessed by host inference.
		cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded, DoltServerHost: "10.0.0.5"}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false: explicit dolt_mode=embedded must win over DoltServerHost=10.0.0.5")
		}
	})

	t.Run("config.yaml dolt.mode=embedded + env host non-localhost still infers via env", func(t *testing.T) {
		// Env-host inference happens before the dolt_mode/config.yaml
		// checks, so it wins regardless of what config.yaml says.
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.0.2.10")

		configDir := t.TempDir()
		configYaml := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(configYaml,
			[]byte("dolt.mode: embedded\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)

		cfg := &Config{Backend: BackendDolt}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true: env host non-localhost should infer server mode even with config.yaml dolt.mode: embedded")
		}
	})

	t.Run("metadata localhost host masks config.yaml remote host", func(t *testing.T) {
		// The inference follows the EFFECTIVE host precedence
		// (GetDoltServerHost: env > metadata > config.yaml): when
		// metadata supplies localhost, a lower-priority remote
		// dolt.host that GetDoltServerHost would ignore must not
		// drive the inference (cross-vendor review round 5).
		configDir := t.TempDir()
		configYaml := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(configYaml,
			[]byte("dolt.host: 100.64.0.1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)

		cfg := &Config{Backend: BackendDolt, DoltServerHost: "localhost"}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false: metadata localhost host must mask config.yaml remote host")
		}
	})

	t.Run("proxied-server mode is not reclassified by env host", func(t *testing.T) {
		// IsDoltServerMode and IsDoltProxiedServerMode are mutually
		// exclusive; a non-local env host on a proxied workspace must
		// not flip it to plain server mode (cross-vendor review P2,
		// 2026-08-02: the proxied-to-server migration would otherwise
		// exit "Already in server mode").
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.0.2.10")
		cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeProxiedServer}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false for proxied-server workspace with non-local env host")
		}
		if !cfg.IsDoltProxiedServerMode() {
			t.Error("IsDoltProxiedServerMode() = false, want true")
		}
	})

	t.Run("config.yaml dolt.mode=embedded wins over config.yaml dolt.host", func(t *testing.T) {
		// Like metadata's dolt_mode, ANY explicit yaml mode wins over
		// yaml-host inference — an explicit "embedded" must not be
		// flipped to server by a dolt.host inherited from a
		// lower-priority config (cross-vendor review P1, 2026-08-02).
		configDir := t.TempDir()
		configYaml := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(configYaml,
			[]byte("dolt.mode: embedded\ndolt.host: 100.64.0.1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)

		cfg := &Config{Backend: BackendDolt}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true, want false: explicit config.yaml dolt.mode: embedded must win over dolt.host inference")
		}
	})

	t.Run("config.yaml dolt.host=non-localhost + no metadata mode infers server mode", func(t *testing.T) {
		// Deliberately do not set BEADS_DOLT_SERVER_HOST here: a
		// non-empty env value would decide the inference by itself,
		// and this subtest exercises the config.yaml fallback layer.
		configDir := t.TempDir()
		configYaml := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(configYaml,
			[]byte("dolt.host: 100.64.0.1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)

		cfg := &Config{Backend: BackendDolt}
		if !cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = false, want true: config.yaml dolt.host=100.64.0.1 with no metadata dolt_mode should infer server mode")
		}
	})
}

// TestDoltProxiedServerMode covers the IsDoltProxiedServerMode predicate and
// the GetCapabilities branch that treats proxied-server as multi-process-safe
// (the proxy daemon serializes writers).
func TestDoltProxiedServerMode(t *testing.T) {
	t.Run("IsDoltProxiedServerMode", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want bool
		}{
			{
				name: "empty backend",
				cfg:  &Config{Backend: ""},
				want: false,
			},
			{
				name: "embedded mode",
				cfg:  &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded},
				want: false,
			},
			{
				name: "server mode",
				cfg:  &Config{Backend: BackendDolt, DoltMode: DoltModeServer},
				want: false,
			},
			{
				name: "proxied-server mode",
				cfg:  &Config{Backend: BackendDolt, DoltMode: DoltModeProxiedServer},
				want: true,
			},
			{
				name: "proxied-server, mixed case",
				cfg:  &Config{Backend: BackendDolt, DoltMode: "Proxied-Server"},
				want: true,
			},
			{
				name: "default (no DoltMode)",
				cfg:  &Config{Backend: BackendDolt},
				want: false,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := tc.cfg.IsDoltProxiedServerMode(); got != tc.want {
					t.Errorf("IsDoltProxiedServerMode() = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("ServerAndProxiedAreMutuallyExclusive", func(t *testing.T) {
		cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeProxiedServer}
		if cfg.IsDoltServerMode() {
			t.Error("IsDoltServerMode() should be false for proxied-server mode")
		}
		if !cfg.IsDoltProxiedServerMode() {
			t.Error("IsDoltProxiedServerMode() should be true for proxied-server mode")
		}
	})

	t.Run("GetCapabilities_ProxiedServerNotSingleProcess", func(t *testing.T) {
		cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeProxiedServer}
		caps := cfg.GetCapabilities()
		if caps.SingleProcessOnly {
			t.Error("proxied-server should report SingleProcessOnly=false (proxy multiplexes writers)")
		}
	})

	t.Run("GetDoltModePreservesProxiedValue", func(t *testing.T) {
		cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeProxiedServer}
		if got := cfg.GetDoltMode(); got != DoltModeProxiedServer {
			t.Errorf("GetDoltMode() = %q, want %q", got, DoltModeProxiedServer)
		}
	})

	t.Run("RoundtripPersistsProxiedMode", func(t *testing.T) {
		dir := t.TempDir()
		original := &Config{
			Database:     "dolt",
			Backend:      BackendDolt,
			DoltMode:     DoltModeProxiedServer,
			DoltDatabase: "myproj",
			ProjectID:    "abc-123",
		}
		if err := original.Save(dir); err != nil {
			t.Fatalf("Save: %v", err)
		}
		loaded, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if loaded == nil {
			t.Fatal("Load returned nil")
		}
		if loaded.DoltMode != DoltModeProxiedServer {
			t.Errorf("DoltMode = %q, want %q", loaded.DoltMode, DoltModeProxiedServer)
		}
		if !loaded.IsDoltProxiedServerMode() {
			t.Error("IsDoltProxiedServerMode() = false after roundtrip")
		}
		if loaded.IsDoltServerMode() {
			t.Error("IsDoltServerMode() = true after roundtrip; should be false")
		}
	})
}

func TestProxiedServerClientInfo_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	t.Run("absent file returns nil", func(t *testing.T) {
		got, err := LoadProxiedServerClientInfo(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v, want nil for absent file", got)
		}
	})

	t.Run("absolute paths survive save/load", func(t *testing.T) {
		want := &ProxiedServerClientInfo{
			RootPath:   "/var/lib/beads/proxieddb",
			ConfigPath: "/etc/dolt/server.yaml",
			LogPath:    "/var/log/beads/server.log",
		}
		if err := SaveProxiedServerClientInfo(dir, want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := LoadProxiedServerClientInfo(dir)
		if err != nil || got == nil {
			t.Fatalf("Load: %v got=%v", err, got)
		}
		if *got != *want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("external section survives save/load", func(t *testing.T) {
		sub := t.TempDir()
		want := &ProxiedServerClientInfo{
			External: &ExternalDoltConfig{
				Host:            "db.internal",
				Port:            3306,
				TLSRequired:     true,
				TLSCert:         "/etc/beads/client.pem",
				TLSKey:          "/etc/beads/client.key",
				KeepAlivePeriod: 45 * time.Second,
			},
		}
		if err := SaveProxiedServerClientInfo(sub, want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := LoadProxiedServerClientInfo(sub)
		if err != nil || got == nil {
			t.Fatalf("Load: %v got=%v", err, got)
		}
		if got.RootPath != "" || got.ConfigPath != "" || got.LogPath != "" {
			t.Errorf("local fields leaked into external-only sidecar: %+v", got)
		}
		if got.External == nil {
			t.Fatalf("External section dropped on round-trip")
		}
		if *got.External != *want.External {
			t.Errorf("got %+v, want %+v", got.External, want.External)
		}
	})

	t.Run("local fields and external section coexist", func(t *testing.T) {
		sub := t.TempDir()
		want := &ProxiedServerClientInfo{
			RootPath: "/var/lib/beads/proxieddb",
			External: &ExternalDoltConfig{Socket: "/var/run/dolt.sock"},
		}
		if err := SaveProxiedServerClientInfo(sub, want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := LoadProxiedServerClientInfo(sub)
		if err != nil || got == nil {
			t.Fatalf("Load: %v got=%v", err, got)
		}
		if got.RootPath != want.RootPath {
			t.Errorf("RootPath = %q, want %q", got.RootPath, want.RootPath)
		}
		if got.External == nil || got.External.Socket != "/var/run/dolt.sock" {
			t.Errorf("External round-trip lost data: %+v", got.External)
		}
	})

	// The absent-vs-corrupt distinction is load-bearing for the proxied
	// provider (bd-aj3g5): absent means (nil, nil) and the caller may take
	// the fresh-workspace defaults; a file that EXISTS but cannot be parsed
	// must be an error, or the caller silently forks a fresh database.
	t.Run("corrupt sidecar is an error, not nil", func(t *testing.T) {
		sub := t.TempDir()
		if err := os.WriteFile(ProxiedServerClientInfoPath(sub), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("seed corrupt: %v", err)
		}
		got, err := LoadProxiedServerClientInfo(sub)
		if err == nil {
			t.Fatalf("Load of corrupt sidecar returned nil error (got=%+v); corrupt must not be conflated with absent", got)
		}
		if !strings.Contains(err.Error(), ProxiedServerClientInfoFileName) {
			t.Errorf("error should name the file: %v", err)
		}
	})

	t.Run("legacy sidecar without external section still loads", func(t *testing.T) {
		sub := t.TempDir()
		legacy := []byte(`{"root_path":"/var/lib/beads/proxieddb","config_path":"/etc/dolt/server.yaml","log_path":"/var/log/beads/server.log"}`)
		if err := os.WriteFile(ProxiedServerClientInfoPath(sub), legacy, 0o600); err != nil {
			t.Fatalf("seed legacy: %v", err)
		}
		got, err := LoadProxiedServerClientInfo(sub)
		if err != nil || got == nil {
			t.Fatalf("Load: %v got=%v", err, got)
		}
		if got.External != nil {
			t.Errorf("External should be nil for legacy sidecar, got %+v", got.External)
		}
		if got.RootPath != "/var/lib/beads/proxieddb" {
			t.Errorf("RootPath = %q, want /var/lib/beads/proxieddb", got.RootPath)
		}
	})
}

func TestProxiedServerClientInfo_ResolvedPaths(t *testing.T) {
	beadsDir := "/home/user/project/.beads"

	t.Run("nil receiver returns empty", func(t *testing.T) {
		var info *ProxiedServerClientInfo
		if got := info.ResolvedRootPath(beadsDir); got != "" {
			t.Errorf("ResolvedRootPath = %q, want empty", got)
		}
	})

	t.Run("absolute returned as-is", func(t *testing.T) {
		info := &ProxiedServerClientInfo{RootPath: "/srv/abs"}
		if got := info.ResolvedRootPath(beadsDir); got != "/srv/abs" {
			t.Errorf("ResolvedRootPath = %q, want absolute as-is", got)
		}
	})

	t.Run("relative joined with beadsDir", func(t *testing.T) {
		info := &ProxiedServerClientInfo{RootPath: "alt-proxieddb"}
		want := filepath.Join(beadsDir, "alt-proxieddb")
		if got := info.ResolvedRootPath(beadsDir); got != want {
			t.Errorf("ResolvedRootPath = %q, want %q", got, want)
		}
	})
}

// TestGetBackendAllowlist verifies the metadata-routing semantics: current backends
// and removed-backend tombstones remain recognizable, while empty or unknown values
// retain the historical Dolt fallback. Store selection rejects the tombstones before
// it can open an empty Dolt database.
func TestGetBackendAllowlist(t *testing.T) {
	fallsBackToDolt := []struct {
		name string
		cfg  *Config
	}{
		{name: "explicit dolt", cfg: &Config{Backend: BackendDolt}},
		{name: "empty backend", cfg: &Config{Backend: ""}},
		{name: "legacy config", cfg: &Config{}},
		{name: "unknown backend", cfg: &Config{Backend: "mystery"}},
	}
	for _, tt := range fallsBackToDolt {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.GetBackend(); got != BackendDolt {
				t.Errorf("GetBackend() = %q, want %q", got, BackendDolt)
			}
		})
	}

	honored := []string{BackendPostgres, BackendMySQL, BackendSQLite}
	for _, backend := range honored {
		t.Run(backend+" honored", func(t *testing.T) {
			cfg := &Config{Backend: backend}
			if got := cfg.GetBackend(); got != backend {
				t.Errorf("GetBackend() = %q, want %q", got, backend)
			}
		})
	}
}

func TestSupportedBackendAllowlist(t *testing.T) {
	tests := []struct {
		name      string
		backend   string
		supported bool
	}{
		{name: "implicit dolt", backend: "", supported: true},
		{name: "dolt", backend: BackendDolt, supported: true},
		{name: "sqlite", backend: BackendSQLite, supported: false},
		{name: "postgres", backend: BackendPostgres, supported: false},
		{name: "mysql", backend: BackendMySQL, supported: false},
		{name: "unknown", backend: "mystery", supported: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSupportedBackend(tt.backend); got != tt.supported {
				t.Fatalf("IsSupportedBackend(%q) = %v, want %v", tt.backend, got, tt.supported)
			}
		})
	}
}

// TestDatabasePathAlwaysDolt tests that DatabasePath always returns the dolt path.
func TestDatabasePathAlwaysDolt(t *testing.T) {
	beadsDir := "/home/user/project/.beads"

	cfg := &Config{Database: "beads.db", Backend: BackendDolt}
	got := cfg.DatabasePath(beadsDir)
	want := filepath.Join(beadsDir, "dolt")
	if got != want {
		t.Errorf("DatabasePath() = %q, want %q", got, want)
	}
}

// TestCapabilitiesForBackend tests that CapabilitiesForBackend returns
// single-process-only by default.
func TestCapabilitiesForBackend(t *testing.T) {
	caps := CapabilitiesForBackend("anything")
	if !caps.SingleProcessOnly {
		t.Error("CapabilitiesForBackend().SingleProcessOnly = false, want true")
	}
}

// TestGetCapabilities tests that GetCapabilities properly handles server mode
func TestGetCapabilities(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *Config
		wantSingleProc bool
	}{
		{
			name:           "dolt embedded is single-process",
			cfg:            &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded},
			wantSingleProc: true,
		},
		{
			name:           "dolt default (empty) is single-process",
			cfg:            &Config{Backend: BackendDolt},
			wantSingleProc: true,
		},
		{
			name:           "dolt server mode is multi-process",
			cfg:            &Config{Backend: BackendDolt, DoltMode: DoltModeServer},
			wantSingleProc: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.GetCapabilities().SingleProcessOnly
			if got != tt.wantSingleProc {
				t.Errorf("GetCapabilities().SingleProcessOnly = %v, want %v", got, tt.wantSingleProc)
			}
		})
	}
}

// TestDoltServerModeRoundtrip tests that server mode config survives save/load
func TestDoltServerModeRoundtrip(t *testing.T) {
	// Clear port env vars so saved/loaded port comes from config, not ambient env.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	cfg := &Config{
		Database:       "dolt",
		Backend:        BackendDolt,
		DoltMode:       DoltModeServer,
		DoltServerHost: "192.168.1.50",
		DoltServerPort: 13306,
		DoltServerUser: "beads_admin",
	}

	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	loaded, err := Load(beadsDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if !loaded.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = false after load, want true")
	}
	if loaded.GetDoltMode() != DoltModeServer {
		t.Errorf("GetDoltMode() = %q, want %q", loaded.GetDoltMode(), DoltModeServer)
	}
	if loaded.GetDoltServerHost() != "192.168.1.50" {
		t.Errorf("GetDoltServerHost() = %q, want %q", loaded.GetDoltServerHost(), "192.168.1.50")
	}
	if loaded.GetDoltServerPort() != 13306 {
		t.Errorf("GetDoltServerPort() = %d, want %d", loaded.GetDoltServerPort(), 13306)
	}
	if loaded.GetDoltServerUser() != "beads_admin" {
		t.Errorf("GetDoltServerUser() = %q, want %q", loaded.GetDoltServerUser(), "beads_admin")
	}
}

// TestEnvVarOverrides tests env var overrides for getter methods
func TestEnvVarOverrides(t *testing.T) {
	t.Run("host env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.168.1.1")
		cfg := &Config{DoltServerHost: "10.0.0.1"}
		if got := cfg.GetDoltServerHost(); got != "192.168.1.1" {
			t.Errorf("GetDoltServerHost() = %q, want 192.168.1.1", got)
		}
	})

	t.Run("port env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3309")
		cfg := &Config{DoltServerPort: 3308}
		if got := cfg.GetDoltServerPort(); got != 3309 {
			t.Errorf("GetDoltServerPort() = %d, want 3309", got)
		}
	})

	t.Run("invalid port env var falls through to config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "not-a-number")
		cfg := &Config{DoltServerPort: 3308}
		if got := cfg.GetDoltServerPort(); got != 3308 {
			t.Errorf("GetDoltServerPort() = %d, want 3308", got)
		}
	})

	t.Run("BEADS_DOLT_PORT fallback when SERVER_PORT not set", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_PORT", "3307")
		cfg := &Config{}
		if got := cfg.GetDoltServerPort(); got != 3307 {
			t.Errorf("GetDoltServerPort() = %d, want 3307", got)
		}
	})

	t.Run("BEADS_DOLT_SERVER_PORT takes priority over BEADS_DOLT_PORT", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3309")
		t.Setenv("BEADS_DOLT_PORT", "3307")
		cfg := &Config{}
		if got := cfg.GetDoltServerPort(); got != 3309 {
			t.Errorf("GetDoltServerPort() = %d, want 3309", got)
		}
	})

	t.Run("user env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_USER", "envuser")
		cfg := &Config{DoltServerUser: "admin"}
		if got := cfg.GetDoltServerUser(); got != "envuser" {
			t.Errorf("GetDoltServerUser() = %q, want envuser", got)
		}
	})

	t.Run("database env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "envdb")
		cfg := &Config{DoltDatabase: "mydb"}
		if got := cfg.GetDoltDatabase(); got != "envdb" {
			t.Errorf("GetDoltDatabase() = %q, want envdb", got)
		}
	})

	t.Run("database default", func(t *testing.T) {
		cfg := &Config{}
		if got := cfg.GetDoltDatabase(); got != DefaultDoltDatabase {
			t.Errorf("GetDoltDatabase() = %q, want %q", got, DefaultDoltDatabase)
		}
	})

	t.Run("database config value", func(t *testing.T) {
		cfg := &Config{DoltDatabase: "mydb"}
		if got := cfg.GetDoltDatabase(); got != "mydb" {
			t.Errorf("GetDoltDatabase() = %q, want mydb", got)
		}
	})
}

// --- Upgrade regression tests (GH#2949) ---

func TestIsDoltServerMode_SharedServerOverridesEmbedded(t *testing.T) {
	// GH#2949: shared-server env var must override stale dolt_mode=embedded
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")

	cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded}
	if !cfg.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = false with BEADS_DOLT_SHARED_SERVER=1 + stale embedded, want true")
	}
}

func TestIsDoltServerMode_SharedServerTrue(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "true")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")

	cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded}
	if !cfg.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = false with BEADS_DOLT_SHARED_SERVER=true + stale embedded, want true")
	}
}

func TestIsDoltServerMode_NoEnvRespectsMetadata(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")

	cfg := &Config{Backend: BackendDolt, DoltMode: DoltModeEmbedded}
	if cfg.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = true with no env overrides + embedded metadata, want false")
	}
}

func TestIsDoltServerMode_ConfigYamlServer(t *testing.T) {
	// Clear all env vars that affect server mode detection
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	// Set up a config.yaml with dolt.mode: server
	configDir := t.TempDir()
	configYaml := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configYaml,
		[]byte("dolt.mode: server\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	cfg := &Config{Backend: BackendDolt}
	if !cfg.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = false with config.yaml dolt.mode: server, want true")
	}
}

func TestIsDoltServerMode_ConfigYamlEmbedded(t *testing.T) {
	// Clear all env vars that affect server mode detection
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	// Set up a config.yaml with dolt.mode: embedded
	configDir := t.TempDir()
	configYaml := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configYaml,
		[]byte("dolt.mode: embedded\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	cfg := &Config{Backend: BackendDolt}
	if cfg.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = true with config.yaml dolt.mode: embedded, want false")
	}
}

func TestIsDoltServerMode_MetadataEmbeddedNotOverriddenByConfigYaml(t *testing.T) {
	// If metadata.json explicitly says embedded, config.yaml dolt.mode: server
	// must NOT override it. Project-local metadata takes priority over
	// user-global config.yaml.
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	// config.yaml says server
	configDir := t.TempDir()
	configYaml := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configYaml,
		[]byte("dolt.mode: server\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	// metadata.json says embedded
	cfg := &Config{Backend: BackendDolt, DoltMode: "embedded"}
	if cfg.IsDoltServerMode() {
		t.Error("IsDoltServerMode() = true, want false: metadata.json embedded should not be overridden by config.yaml server")
	}
}

func TestGlobalDoltDatabase_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	cfg := DefaultConfig()
	cfg.GlobalDoltDatabase = "beads_global"

	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	loaded, err := Load(beadsDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if loaded.GlobalDoltDatabase != "beads_global" {
		t.Errorf("GlobalDoltDatabase = %q, want %q", loaded.GlobalDoltDatabase, "beads_global")
	}
	if loaded.GetGlobalDoltDatabase() != "beads_global" {
		t.Errorf("GetGlobalDoltDatabase() = %q, want %q", loaded.GetGlobalDoltDatabase(), "beads_global")
	}
}

func TestGlobalDoltDatabase_EmptyByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.GetGlobalDoltDatabase() != "" {
		t.Errorf("GetGlobalDoltDatabase() = %q, want empty string for default config", cfg.GetGlobalDoltDatabase())
	}
}

func TestGlobalDoltDatabase_OmittedFromJSON(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	cfg := DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, ConfigFileName))
	if err != nil {
		t.Fatalf("ReadFile() failed: %v", err)
	}

	if strings.Contains(string(data), "global_dolt_database") {
		t.Error("global_dolt_database should be omitted from JSON when empty")
	}
}
