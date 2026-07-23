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

// archiveLevelSupportNoticeFormat is the one-time stderr notice emitted when
// generating a fresh managed-server config.yaml against an external dolt
// that predates auto_gc_behavior.archive_level support. Never fires for an
// existing config.yaml (ensureProxiedServerConfig only renders on first
// bootstrap), so this is not a per-invocation warning.
const archiveLevelSupportNoticeFormat = "Info: external dolt predates archive_level config support (need >= Dolt %s); " +
	"this managed server's background auto-GC may still produce zstd archives.\n"

const (
	proxiedServerConfigName = "config.yaml"
	proxiedServerLogName    = "server.log"
)

func proxiedServerRoot(beadsDir string) string {
	return doltserver.ResolveDoltDir(beadsDir)
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

func resolveProxiedServerRootPath(beadsDir string) (string, error) {
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

// ensureProxiedServerConfig resolves (creating if needed) the managed
// sql-server config.yaml. archiveLevelSupported gates whether a freshly
// generated config sets auto_gc_behavior.archive_level: 0 (see
// renderProxiedServerConfig); it has no effect on an existing config.yaml,
// which is loaded as-is.
func ensureProxiedServerConfig(beadsDir string, archiveLevelSupported bool) (string, error) {
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
		if _, err := servercfg.YamlConfigFromFile(filesys.LocalFS, path); err != nil {
			return "", fmt.Errorf("ensureProxiedServerConfig: existing config %s: parse: %w", path, err)
		}
		return path, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("ensureProxiedServerConfig: stat %s: %w", path, err)
	}

	port, err := proxy.PickFreePort()
	if err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: pick free port: %w", err)
	}

	if !archiveLevelSupported {
		fmt.Fprintf(os.Stderr, archiveLevelSupportNoticeFormat, doltserver.MinDoltVersionForArchiveLevelConfig)
	}

	body, err := renderProxiedServerConfig(port, archiveLevelSupported)
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

// renderProxiedServerConfig renders the managed sql-server config.yaml. When
// archiveLevelSupported is true it sets auto_gc_behavior.archive_level: 0,
// so this server's background auto-GC writes classic Snappy table files
// instead of zstd archives (gastownhall/beads#4986); auto-GC itself stays
// enabled (the sibling "enable" key is left unset, which Dolt defaults to
// true). When false, the key is omitted entirely — an older external dolt's
// own YAMLConfig struct may not have this field, and Dolt's config loader
// uses yaml.UnmarshalStrict, so an unrecognized key is a hard parse error at
// server startup rather than a silently-ignored one. Callers must gate
// archiveLevelSupported on doltserver.SupportsArchiveLevelConfig.
func renderProxiedServerConfig(port int, archiveLevelSupported bool) ([]byte, error) {
	host := proxiedServerListenerHost
	logLevel := string(servercfg.LogLevel_Info)
	yc := &servercfg.YAMLConfig{
		LogLevelStr: &logLevel,
		ListenerConfig: servercfg.ListenerYAMLConfig{
			HostStr:    &host,
			PortNumber: &port,
		},
	}
	if archiveLevelSupported {
		archiveLevel := 0
		yc.BehaviorConfig.AutoGCBehavior = &servercfg.AutoGCBehaviorYAMLConfig{
			ArchiveLevel_: &archiveLevel,
		}
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
