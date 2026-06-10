package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/dolthub/dolt/go/libraries/utils/filesys"
	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

const (
	proxiedServerRootName   = "proxieddb"
	proxiedServerConfigName = "server_config.yaml"
	proxiedServerLogName    = "server.log"
)

func proxiedServerRoot(beadsDir string) string {
	return filepath.Join(beadsDir, proxiedServerRootName)
}

func proxiedServerConfigPath(beadsDir string) string {
	return filepath.Join(proxiedServerRoot(beadsDir), proxiedServerConfigName)
}

func proxiedServerLogPath(beadsDir string) string {
	return filepath.Join(proxiedServerRoot(beadsDir), proxiedServerLogName)
}

func envOrAbsJoin(envName, beadsDir string) string {
	p := os.Getenv(envName)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(beadsDir, p)
}

// sharedProxyModeEnabled reports whether this scope should route through the
// machine-wide shared db-proxy (BackendLocalSharedServer) instead of its own
// per-scope proxieddb child. Opt-in via BEADS_SHARED_PROXY (1/true/yes/on); OFF
// by default, so every existing proxied scope is byte-for-byte unchanged.
func sharedProxyModeEnabled() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv("BEADS_SHARED_PROXY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func resolveProxiedServerRootPath(beadsDir string) (string, error) {
	// Opt-in: route this scope through the machine-wide shared db-proxy so all
	// proxied scopes collapse onto one db-proxy-child (be-pen9). This is the one
	// rootDir chokepoint every proxied provider already routes through, so the
	// switch lands consistently for both the routed store and the uow provider.
	// The shared backend + external wiring that completes production shared-mode
	// is applied by the consumer (the gascity deployment, per
	// docs/CONNECTION_POOLING_DEPLOYMENT.md).
	if sharedProxyModeEnabled() {
		return doltserver.SharedProxyRootDir()
	}
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_ROOT_PATH", beadsDir); p != "" {
		return p, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", err
	}
	if p := info.ResolvedRootPath(beadsDir); p != "" {
		return p, nil
	}
	return proxiedServerRoot(beadsDir), nil
}

func resolveProxiedServerConfigPath(beadsDir string) (path string, isCustom bool, err error) {
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_CONFIG", beadsDir); p != "" {
		return p, true, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", false, err
	}
	if p := info.ResolvedConfigPath(beadsDir); p != "" {
		return p, true, nil
	}
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(root, proxiedServerConfigName), false, nil
}

func resolveProxiedServerLogPath(beadsDir string) (path string, isCustom bool, err error) {
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_LOG", beadsDir); p != "" {
		return p, true, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", false, err
	}
	if p := info.ResolvedLogPath(beadsDir); p != "" {
		return p, true, nil
	}
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(root, proxiedServerLogName), false, nil
}

func ensureProxiedServerConfig(beadsDir string) (string, error) {
	path, isCustom, err := resolveProxiedServerConfigPath(beadsDir)
	if err != nil {
		return "", err
	}

	if isCustom {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("ensureProxiedServerConfig: custom config %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("ensureProxiedServerConfig: custom config %s: not a regular file", path)
		}
		if _, err := servercfg.YamlConfigFromFile(filesys.LocalFS, path); err != nil {
			return "", fmt.Errorf("ensureProxiedServerConfig: custom config %s: parse: %w", path, err)
		}
		return path, nil
	}

	root := filepath.Dir(path)
	if err := os.MkdirAll(root, config.BeadsDirPerm); err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: mkdir %s: %w", root, err)
	}

	switch _, err := os.Stat(path); {
	case err == nil:
		return path, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("ensureProxiedServerConfig: stat %s: %w", path, err)
	}

	port, err := proxy.PickFreePort()
	if err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: pick free port: %w", err)
	}

	body, err := renderProxiedServerConfig(port)
	if err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: render YAML: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: write %s: %w", path, err)
	}
	return path, nil
}

// Validators below emit source-neutral errors. Callers wrap with whichever
// label is meaningful at their site: CLI callers prepend the flag name
// (e.g. "--proxied-server-config-path"); runtime callers (uow factory, etc.)
// prepend whatever label fits — the path may have come from env var or
// the proxied_server_client_info.json sidecar, not necessarily a flag.

func validateProxiedServerConfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	if _, err := servercfg.YamlConfigFromFile(filesys.LocalFS, path); err != nil {
		return fmt.Errorf("%s: parse: %w", path, err)
	}
	return nil
}

func validateProxiedServerRootPath(path string) error {
	switch info, err := os.Stat(path); {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%s: not a directory", path)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func validateProxiedServerLogPath(path string) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("%s: parent directory: %w", path, err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("%s: parent %s is not a directory", path, parent)
	}
	switch info, err := os.Stat(path); {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", path)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func renderProxiedServerConfig(port int) ([]byte, error) {
	host := proxiedServerListenerHost
	logLevel := string(servercfg.LogLevel_Info)
	yc := &servercfg.YAMLConfig{
		LogLevelStr: &logLevel,
		ListenerConfig: servercfg.ListenerYAMLConfig{
			HostStr:    &host,
			PortNumber: &port,
		},
	}
	return yaml.Marshal(yc)
}

const proxiedServerListenerHost = "127.0.0.1"

// TODO: this needs to return a dolt server uow provider as the global
// uow provider used by all commands

//	func newProxiedServerStore(ctx context.Context, cfg *dolt.Config) (storage.DoltStorage, error) {
//		if cfg == nil {
//			return nil, fmt.Errorf("newProxiedServerStore: cfg is nil")
//		}
//		if cfg.BeadsDir == "" {
//			return nil, fmt.Errorf("newProxiedServerStore: cfg.BeadsDir must be set")
//		}
//		if cfg.Database == "" {
//			return nil, fmt.Errorf("newProxiedServerStore: cfg.Database must be set")
//		}
//
//		doltBin, err := exec.LookPath("dolt")
//		if err != nil {
//			return nil, fmt.Errorf("newProxiedServerStore: dolt is not installed (not found in PATH); install from https://docs.dolthub.com/introduction/installation: %w", err)
//		}
//
//		persisted, _ := configfile.Load(cfg.BeadsDir)
//
//		rootPath, isCustomRoot := resolveProxiedServerRootPath(cfg.BeadsDir, persisted)
//		if isCustomRoot {
//			if err := validateProxiedServerRootPath(rootPath); err != nil {
//				return nil, err
//			}
//		}
//
//		configPath, err := ensureProxiedServerConfig(cfg.BeadsDir, persisted)
//		if err != nil {
//			return nil, err
//		}
//
//		logPath, isCustomLog := resolveProxiedServerLogPath(cfg.BeadsDir, persisted)
//		if isCustomLog {
//			if err := validateProxiedServerLogPath(logPath); err != nil {
//				return nil, err
//			}
//		}
//
//		name, email := cfg.CommitterName, cfg.CommitterEmail
//		if name == "" || email == "" {
//			fallbackName, fallbackEmail := proxiedServerCommitter()
//			if name == "" {
//				name = fallbackName
//			}
//			if email == "" {
//				email = fallbackEmail
//			}
//		}
//
//		return doltserver.NewDoltServerStore(
//			ctx,
//			rootPath,
//			cfg.BeadsDir,
//			cfg.Database,
//			name, email,
//			logPath,
//			configPath,
//			proxy.BackendLocalServer,
//			false, // autoSyncToOriginRemote — wired in a future iteration
//			"root",
//			"", // rootPassword: proxy is loopback-only, no auth
//			doltBin,
//		)
//	}
func proxiedServerCommitter() (string, string) {
	name, email := "beads", "beads@localhost"
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			name = v
		}
	}
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			email = v
		}
	}
	return name, email
}
