package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// doltVersionWarnOnce ensures the dolt-version advisory (old/unverifiable
// version) is printed at most once per process, even though
// newManagedProxiedServerUOWProvider can run repeatedly against the same
// resolved binary (retries, multiple UOW providers opened in one command).
// Repeating the same advisory on every call would just be noise once the
// operator has seen it.
var doltVersionWarnOnce sync.Once

func newProxiedServerUOWProvider(ctx context.Context, beadsDir string) (uow.UnitOfWorkProvider, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: beadsDir must be set")
	}

	persisted, _ := configfile.Load(beadsDir)
	database := configfile.DefaultDoltDatabase
	if persisted != nil {
		database = persisted.GetDoltDatabase()
	}

	info, _ := configfile.LoadProxiedServerClientInfo(beadsDir)
	var proxyPort int
	var proxyIdleTimeout time.Duration
	if info != nil {
		proxyPort = info.Port
		proxyIdleTimeout = info.IdleTimeout
	}
	if info != nil && info.External != nil {
		return newExternalProxiedServerUOWProvider(ctx, beadsDir, database, info.External, proxyPort, proxyIdleTimeout)
	}

	return newManagedProxiedServerUOWProvider(ctx, beadsDir, database, proxyPort, proxyIdleTimeout)
}

func newExternalProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
	external *configfile.ExternalDoltConfig,
	proxyPort int,
	proxyIdleTimeout time.Duration,
) (uow.UnitOfWorkProvider, error) {
	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	if err := os.MkdirAll(rootPath, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: mkdir %s: %w", rootPath, err)
	}

	return uow.NewExternalDoltServerUOWProvider(
		ctx,
		rootPath,
		database,
		logPath,
		*external,
		external.ResolvedUser(),
		os.Getenv(configfile.ExternalDoltPasswordEnvVar),
		proxyPort,
		proxyIdleTimeout,
	)
}

func newManagedProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
	proxyPort int,
	proxyIdleTimeout time.Duration,
) (uow.UnitOfWorkProvider, error) {
	// Resolve and hardened-probe the external dolt binary before spawning
	// it: an env/sidecar override that is explicitly named but broken
	// should fail loudly here rather than surface as a confusing spawn
	// failure downstream. See internal/doltversion for the resolution
	// precedence (env > sidecar > PATH) and probe hardening (timeout,
	// output cap, pre-exec validation).
	doltBin, doltSrc, err := doltversion.Resolve(doltversion.ResolveOptions{
		EnvValue: doltversion.ReadEnvOverride(),
		// SidecarValue is a hook point only in this PR — the clone-local
		// sidecar setting itself lands in PR-2.
		SidecarValue: "",
	})
	if err != nil {
		return nil, fmt.Errorf(
			"newProxiedServerUOWProvider: resolving dolt binary (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			doltSrc, err,
		)
	}
	doltID, doltWarn, err := doltversion.ProbeWithPolicy(ctx, doltBin)
	if err != nil {
		return nil, fmt.Errorf(
			"newProxiedServerUOWProvider: probing dolt binary %q (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			doltBin, doltSrc, err,
		)
	}
	if doltWarn != nil && !quietFlag && !jsonOutput {
		doltVersionWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", doltWarn.Message())
		})
	}

	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	// Gate auto_gc_behavior.archive_level: 0 on the resolved external dolt's
	// version — Dolt's YAML config loader uses yaml.UnmarshalStrict, so an
	// older dolt whose own YAMLConfig struct lacks this field would refuse
	// to start rather than ignore the unknown key (gastownhall/beads#4986).
	//
	// Derived directly from doltID (the ProbeWithPolicy result just above)
	// instead of calling doltserver.SupportsArchiveLevelConfig(doltBin),
	// which would fork a second `dolt version` subprocess on this hot path
	// purely to re-derive a version this function already has. Zero/
	// unparsed Version (doltID.Version.Segments empty — the
	// ErrUnparseableVersion-demoted-to-warning case) fails closed to
	// false, same as SupportsArchiveLevelConfig's own fail-closed default
	// when it can't determine a version. doltserver.Start and
	// gc_config.go's SupportsArchiveLevelConfig/doltVersionAtLeast are
	// untouched — that consolidation (routing doltserver.Start's own probe
	// through this package too) is PR-2 scope, not this fix.
	archiveLevelSupported := len(doltID.Version.Segments) > 0 &&
		doltID.Version.AtLeast(doltversion.MustParse(doltserver.MinDoltVersionForArchiveLevelConfig))

	configPath, err := ensureProxiedServerConfig(beadsDir, archiveLevelSupported)
	if err != nil {
		return nil, err
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	return uow.NewDoltServerUOWProvider(
		ctx,
		rootPath,
		database,
		logPath,
		configPath,
		proxy.BackendLocalServer,
		"root",
		"", // proxy is loopback-only, no auth
		doltBin,
		proxyPort,
		proxyIdleTimeout,
	)
}
