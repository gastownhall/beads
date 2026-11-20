package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/proxiedopen"
)

// The proxied-server path resolution, config generation and validation live in
// internal/proxiedopen, shared with the Go SDK's proxied-server open. The names
// below are this package's spellings of them, kept so the CLI's call sites and
// tests read as they did before the move.

const (
	managedProxiedServerConfigMarker = proxiedopen.ManagedConfigMarker
	managedPortRecordPrefix          = proxiedopen.ManagedPortRecordPrefix
	proxiedServerConfigName          = proxiedopen.ConfigFileName
	proxiedServerLogName             = proxiedopen.LogFileName
	proxiedServerListenerHost        = proxiedopen.ListenerHost
)

func isManagedProxiedServerConfig(body []byte) bool { return proxiedopen.IsManagedConfig(body) }

func proxiedServerRoot(beadsDir string) string {
	return doltserver.ResolveDoltDir(beadsDir)
}

func proxiedServerConfigPath(beadsDir string) string {
	return filepath.Join(proxiedServerRoot(beadsDir), proxiedServerConfigName)
}

func proxiedServerLogPath(beadsDir string) string {
	return filepath.Join(proxiedServerRoot(beadsDir), proxiedServerLogName)
}

func resolveProxiedServerRootPath(beadsDir string) (string, error) {
	return proxiedopen.ResolveRootPath(beadsDir)
}

func resolveProxiedServerConfigPath(beadsDir string) (path string, isCustom bool, err error) {
	return proxiedopen.ResolveConfigPath(beadsDir)
}

func resolveProxiedServerLogPath(beadsDir string) (path string, isCustom bool, err error) {
	return proxiedopen.ResolveLogPath(beadsDir)
}

func ensureProxiedServerConfig(beadsDir string) (string, error) {
	return proxiedopen.EnsureConfig(beadsDir)
}

func validateProxiedServerConfig(path string) error { return proxiedopen.ValidateConfig(path) }

func validateProxiedServerRootPath(path string) error { return proxiedopen.ValidateRootPath(path) }

func validateProxiedServerLogPath(path string) error { return proxiedopen.ValidateLogPath(path) }

func renderProxiedServerConfig(port int) ([]byte, error) { return proxiedopen.RenderConfig(port) }

func renderLegacyProxiedServerConfig(port int) ([]byte, error) {
	return proxiedopen.RenderLegacyConfig(port)
}

func managedPortConflictPolicy(configPath string, inUsePort int) error {
	return proxiedopen.ManagedPortConflictPolicy(configPath, inUsePort)
}

func cutManagedPortRecord(rest []byte) (port int, yamlBody []byte, ok bool) {
	return proxiedopen.CutManagedPortRecord(rest)
}

// validateManagedProxiedServerConfigAtInit validates the effective existing
// managed-local config before init performs filesystem mutations. A missing
// Beads-default path is valid because init/runtime will generate the
// loopback-only config. Missing custom paths remain hard errors.
//
// explicitRootPath is the --proxied-server-root-path flag value: at this
// point the sidecar is not persisted yet, so resolveProxiedServerConfigPath
// cannot see the flag and it must be applied here. An explicit
// --proxied-server-config-path needs no handling here — init.go already
// validated it at flag-parse time, and when no config env override is set it
// IS the effective config, so there is nothing further to check.
func validateManagedProxiedServerConfigAtInit(beadsDir, explicitPath, explicitRootPath string) error {
	if explicitPath != "" && os.Getenv("BEADS_PROXIED_SERVER_CONFIG") == "" {
		return nil
	}
	path, isCustom, err := resolveProxiedServerConfigPath(beadsDir)
	if err != nil {
		return err
	}
	if !isCustom && explicitRootPath != "" && os.Getenv("BEADS_PROXIED_SERVER_ROOT_PATH") == "" {
		path = filepath.Join(explicitRootPath, proxiedServerConfigName)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) && !isCustom {
			return nil
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	return validateProxiedServerConfig(path)
}

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

// validateEphemeralIdleTimeout rejects the combination of an ephemeral root
// and an explicit --proxied-server-idle-timeout of 0 or negative: with
// BEADS_EPHEMERAL_ROOT=1 the proxy must be able to idle-exit on its own, but
// an explicit 0 means "never", and a negative value is rejected for the same
// reason it always is — both are the flag asking for the opposite of what the
// env var requires. Neither signal should be silently overridden by the
// other, so this is a hard error naming both.
//
// init.go's RunE calls this with the flag's raw parsed value, before it
// converts an explicit 0 to proxy.IdleTimeoutNever.
func validateEphemeralIdleTimeout(ephemeralRoot, idleTimeoutSet bool, serverProxyIdleTimeout time.Duration) error {
	if !ephemeralRoot || !idleTimeoutSet || serverProxyIdleTimeout > 0 {
		return nil
	}
	return fmt.Errorf("BEADS_EPHEMERAL_ROOT=1 conflicts with --proxied-server-idle-timeout=%s: "+
		"an ephemeral root needs the proxy able to idle-exit on its own, but that flag explicitly "+
		"disables idle shutdown; omit --proxied-server-idle-timeout to keep the built-in idle "+
		"default, or pass a positive duration", serverProxyIdleTimeout)
}
