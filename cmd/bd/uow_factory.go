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
// resolveAndProbeDolt can run repeatedly against the same resolved binary
// (retries, multiple UOW providers opened in one command, or bd init
// --proxied-server's own preflight followed by the UOW provider it opens a
// few lines later). Repeating the same advisory on every call would just be
// noise once the operator has seen it.
var doltVersionWarnOnce sync.Once

// resolveAndProbeDolt resolves the external dolt binary (env override >
// sidecar > PATH) and hardened-probes it, printing the version-recommendation
// advisory (if any) at most once per process via doltVersionWarnOnce.
//
// bd init --proxied-server's own preflight (init_proxied_server.go) and
// newManagedProxiedServerUOWProvider both need exactly this
// resolve+probe+warn sequence. A single non-quiet `bd init --proxied-server`
// invocation runs the preflight and then opens a UOW provider a few lines
// later, so before this was shared, an old/unverifiable dolt printed the
// identical advisory twice and forked `dolt version` twice. errPrefix is
// used to keep call-site-specific error context (e.g. "bd init
// --proxied-server" vs "newProxiedServerUOWProvider") in the wrapped error.
func resolveAndProbeDolt(ctx context.Context, errPrefix string) (doltBin string, doltID doltversion.Identity, err error) {
	doltBin, doltSrc, err := doltversion.Resolve(doltversion.ResolveOptions{
		EnvValue: doltversion.ReadEnvOverride(),
		// SidecarValue is a hook point only in this PR — the clone-local
		// sidecar setting itself lands in PR-2.
		SidecarValue: "",
	})
	if err != nil {
		return "", doltversion.Identity{}, fmt.Errorf(
			"%s: resolving dolt binary (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			errPrefix, doltSrc, err,
		)
	}
	doltID, doltWarn, err := doltversion.ProbeWithPolicy(ctx, doltBin)
	if err != nil {
		return doltBin, doltID, fmt.Errorf(
			"%s: probing dolt binary %q (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			errPrefix, doltBin, doltSrc, err,
		)
	}
	// Gated on both quietFlag and jsonOutput, matching this package's
	// convention of keeping JSON-mode stdout/stderr free of advisory chatter
	// (see e.g. tips.go, metrics.go).
	if doltWarn != nil && !quietFlag && !jsonOutput {
		doltVersionWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", doltWarn.Message())
		})
	}
	return doltBin, doltID, nil
}

// doltArchiveLevelSupported reports whether id's dolt build is known to
// support the auto_gc_behavior.archive_level YAML config key, gating
// newManagedProxiedServerUOWProvider's decision to write it. Split out from
// that function so the derivation is independently unit-testable (see
// uow_factory_test.go) — the archive_level decision has its own failure
// mode (gastownhall/beads#4986) and deserves a direct test rather than only
// end-to-end coverage through the UOW provider.
//
// Derived directly from id (a doltversion.ProbeWithPolicy result) instead of
// calling doltserver.SupportsArchiveLevelConfig(doltBin), which would fork a
// second `dolt version` subprocess purely to re-derive a version the caller
// already has. Zero/unparsed Version (id.Version.Segments empty — the
// ErrUnparseableVersion-demoted-to-warning case) fails closed to false, same
// as SupportsArchiveLevelConfig's own fail-closed default when it can't
// determine a version. doltserver.Start and gc_config.go's
// SupportsArchiveLevelConfig/doltVersionAtLeast are untouched — that
// consolidation (routing doltserver.Start's own probe through this package
// too) is PR-2 scope, not this fix.
//
// Also requires an empty Prerelease. AtLeast deliberately ignores
// Version.Prerelease (see doltversion.Version.AtLeast) so the warn-only
// RecommendedMin check treats "2.0.0-dev" as satisfying a ">= 2.0.0" floor.
// That tolerance is wrong here: this decision writes a config key into the
// YAML a specific dolt build reads, and a prerelease/dev build of the floor
// version (e.g. "1.52.1-rc1") is not guaranteed to already have the field —
// unlike the old doltVersionAtLeast (internal/doltserver/gc_config.go),
// whose strconv.Atoi on the raw dotted segment fails to parse any
// prerelease-suffixed string and so already failed closed for exactly this
// case. Silently dropping this guard would reopen the
// gastownhall/beads#4986 failure mode: archive_level gets written, dolt
// sql-server's yaml.UnmarshalStrict rejects the unknown key, and the server
// refuses to start.
func doltArchiveLevelSupported(id doltversion.Identity) bool {
	return len(id.Version.Segments) > 0 &&
		id.Version.Prerelease == "" &&
		id.Version.AtLeast(doltversion.MustParse(doltserver.MinDoltVersionForArchiveLevelConfig))
}

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
	doltBin, doltID, err := resolveAndProbeDolt(ctx, "newProxiedServerUOWProvider")
	if err != nil {
		return nil, err
	}

	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	// See doltArchiveLevelSupported for the fail-closed rationale
	// (gastownhall/beads#4986), including the prerelease guard.
	archiveLevelSupported := doltArchiveLevelSupported(doltID)

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
