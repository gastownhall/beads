package configfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// CentralConfigFileName is the filename for the central/global server config.
const CentralConfigFileName = "server.json"

// DefaultCentralConfigPath returns the platform-appropriate path for the
// central beads server config.
// Linux/macOS: ~/.config/beads/server.json
// Windows: %APPDATA%\beads\server.json
//
// This follows the same convention as DefaultCredentialsPath.
func DefaultCentralConfigPath() string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "beads", CentralConfigFileName)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "beads", CentralConfigFileName)
}

// CentralConfigPath is the central server config this process reads:
// BEADS_CENTRAL_CONFIG when set, else DefaultCentralConfigPath. Empty when
// neither names a file.
func CentralConfigPath() string {
	if p := os.Getenv("BEADS_CENTRAL_CONFIG"); p != "" {
		return p
	}
	return DefaultCentralConfigPath()
}

// centralConfigWarned records the central config paths whose load failure
// CentralConfig has already reported, so a broken file is named once per
// process, not once per getter call.
var centralConfigWarned sync.Map

// CentralConfig is the central server config (CentralConfigPath), read now
// so an edit or a t.Setenv takes effect, or nil when there is none. A file
// that cannot be read or parsed is reported once per process and treated as
// absent: a broken central config never blocks an open.
//
// It is the lowest-precedence source of the server host, port, user and TLS
// (GH#3895): the connection getters, doltserver's port chain and bd init read
// those four fields from it. It never selects the storage mode, socket,
// database or data directory; the workspace's own settings do.
func CentralConfig() *Config {
	path := CentralConfigPath()
	if path == "" {
		return nil
	}
	cfg, err := LoadCentralConfig(path)
	if err != nil {
		if _, warned := centralConfigWarned.LoadOrStore(path, true); !warned {
			fmt.Fprintf(os.Stderr, "Warning: failed to load central config %s: %v\n", path, err)
		}
		return nil
	}
	return cfg
}

// CentralServerHost is the central config's dolt_server_host, or "".
func CentralServerHost() string {
	if c := CentralConfig(); c != nil {
		return c.DoltServerHost
	}
	return ""
}

// CentralServerPort is the central config's dolt_server_port, or 0.
func CentralServerPort() int {
	if c := CentralConfig(); c != nil && c.DoltServerPort > 0 {
		return c.DoltServerPort
	}
	return 0
}

// LoadCentralConfig reads the central server config from the given path.
// Returns (nil, nil) if the file does not exist (graceful skip).
// Returns an error only if the file exists but cannot be parsed.
func LoadCentralConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 - path from DefaultCentralConfigPath or env
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading central config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing central config %s: %w", path, err)
	}
	return &cfg, nil
}

// ApplyCentralDefaults merges server-related fields from the central config
// into the per-project config, but only for fields that are at their zero
// value in the project config. Non-server fields (Database, DoltDatabase,
// DoltDataDir, ProjectID, etc.) are never inherited from the central config
// because they are inherently per-project.
//
// This is a no-op if central is nil.
func ApplyCentralDefaults(project *Config, central *Config) {
	if central == nil {
		return
	}

	// Server connection fields only — these are the fields that are
	// typically identical across all projects on a machine.
	if project.DoltMode == "" && central.DoltMode != "" {
		project.DoltMode = central.DoltMode
	}
	if project.DoltServerHost == "" && central.DoltServerHost != "" {
		project.DoltServerHost = central.DoltServerHost
	}
	if project.DoltServerPort == 0 && central.DoltServerPort != 0 {
		project.DoltServerPort = central.DoltServerPort
	}
	if project.DoltServerUser == "" && central.DoltServerUser != "" {
		project.DoltServerUser = central.DoltServerUser
	}
	// Note: DoltServerTLS is a bool — zero value (false) is indistinguishable
	// from "not set". We only apply central TLS=true when project has TLS=false,
	// which means central can enable TLS but project cannot explicitly disable it
	// via the zero value. To disable TLS when central enables it, use the
	// BEADS_DOLT_SERVER_TLS=0 env var.
	if !project.DoltServerTLS && central.DoltServerTLS {
		project.DoltServerTLS = central.DoltServerTLS
	}
}
