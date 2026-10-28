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

// centralConfigWarned records the central-config paths whose load failure has
// already been warned about, so a broken file prints its warning once per
// process per path no matter how many connection resolvers read it.
var centralConfigWarned sync.Map

// CentralConfig returns the machine's central server config: the file
// BEADS_CENTRAL_CONFIG names, else ~/.config/beads/server.json
// (DefaultCentralConfigPath). It is the lowest-precedence source of the four
// server connection fields — host, port, user and TLS — for every connection
// resolver (configfile's getters, doltserver's port chain), on both the CLI
// and the library open path. It never selects a storage mode, socket,
// database or data dir: those stay per-workspace decisions, so the
// mode-selection resolvers (IsDoltServerMode, HostImpliesServerMode,
// PortImpliesServerMode, doltserver.ResolveServerMode's mode checks) keep
// ignoring it.
//
// A missing file returns nil. An unreadable or malformed file prints
//     Warning: failed to load central config <path>: <err>
// once per process per path (the first reader wins) and returns nil, so gate
// planning and port resolution share the open paths' warn-and-proceed
// posture without repeating the line. There is deliberately no content cache:
// a t.Setenv or a file edit takes effect on the next call.
func CentralConfig() *Config {
	path := os.Getenv("BEADS_CENTRAL_CONFIG")
	if path == "" {
		path = DefaultCentralConfigPath()
	}
	if path == "" {
		return nil
	}
	central, err := LoadCentralConfig(path)
	if err != nil {
		if _, warned := centralConfigWarned.LoadOrStore(path, true); !warned {
			fmt.Fprintf(os.Stderr, "Warning: failed to load central config %s: %v\n", path, err)
		}
		return nil
	}
	return central
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
