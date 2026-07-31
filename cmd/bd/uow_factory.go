package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// newProxiedServerUOWProvider opens the proxied-server provider and, in
// team-server mode, asserts that the shared bts-managed database is serving
// THIS workspace's project (gastownhall/beads: the proxied-path sibling of the
// gateway's DoltStore.verifyProjectIdentity guard).
func newProxiedServerUOWProvider(ctx context.Context, beadsDir, databaseOverride string) (uow.UnitOfWorkProvider, error) {
	return openProxiedServerUOWProvider(ctx, beadsDir, databaseOverride, assertWorkspaceIdentity)
}

// newProxiedServerUOWProviderAdopting skips that assertion. Only two callers
// legitimately have no workspace identity to assert: `bd init --team-server`,
// which ADOPTS the identity the shared database already carries (asserting the
// locally-minted placeholder would reject every correct init), and server-wide
// database maintenance, which is not scoped to one project's database.
func newProxiedServerUOWProviderAdopting(ctx context.Context, beadsDir, databaseOverride string) (uow.UnitOfWorkProvider, error) {
	return openProxiedServerUOWProvider(ctx, beadsDir, databaseOverride, adoptWorkspaceIdentity)
}

// identityPosture selects whether a proxied open asserts the workspace's
// project identity against the database or adopts whatever it finds.
type identityPosture bool

const (
	assertWorkspaceIdentity identityPosture = false
	adoptWorkspaceIdentity  identityPosture = true
)

func openProxiedServerUOWProvider(ctx context.Context, beadsDir, databaseOverride string, posture identityPosture) (uow.UnitOfWorkProvider, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: beadsDir must be set")
	}

	// NOTE: a load error is swallowed here (pre-existing behavior), which
	// leaves persisted == nil and therefore silently falls back to the default
	// database with teamServer=false. The identity assertion below inherits
	// that: an unreadable metadata.json degrades to no assertion rather than a
	// refusal. Tracked separately with the wider silent-fallback smell.
	persisted, _ := configfile.Load(beadsDir)
	database := configfile.DefaultDoltDatabase
	var teamServer bool
	var expectedProjectID string
	if persisted != nil {
		database = persisted.GetDoltDatabase()
		teamServer = persisted.IsTeamServerManaged()
		if posture == assertWorkspaceIdentity {
			expectedProjectID = persisted.ProjectID
			// An empty local identity would reach checkTeamServerIdentity as
			// "nothing to assert" — indistinguishable from the adoption path,
			// which would silently disable the guard. A team-server workspace
			// always has an adopted ProjectID after a successful init, so an
			// empty one here is a broken workspace, not a legacy one.
			if teamServer && expectedProjectID == "" {
				return nil, fmt.Errorf(
					"newProxiedServerUOWProvider: this team-server workspace has no project identity in %s; re-run 'bd init --team-server' to adopt the identity provisioned in the shared database (it never writes to that database)",
					configfile.ConfigFileName)
			}
		}
	}
	if databaseOverride != "" {
		database = databaseOverride
	}

	info, _ := configfile.LoadProxiedServerClientInfo(beadsDir)
	var proxyPort int
	var proxyIdleTimeout time.Duration
	if info != nil {
		proxyPort = info.Port
		proxyIdleTimeout = info.IdleTimeout
	}
	if info != nil && info.External != nil {
		return newExternalProxiedServerUOWProvider(ctx, beadsDir, database, info.External, proxyPort, proxyIdleTimeout, teamServer, expectedProjectID)
	}

	return newManagedProxiedServerUOWProvider(ctx, beadsDir, database, proxyPort, proxyIdleTimeout, teamServer, expectedProjectID)
}

func newExternalProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
	external *configfile.ExternalDoltConfig,
	proxyPort int,
	proxyIdleTimeout time.Duration,
	teamServer bool,
	expectedProjectID string,
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
		teamServer,
		expectedProjectID,
	)
}

func newManagedProxiedServerUOWProvider(
	ctx context.Context,
	beadsDir, database string,
	proxyPort int,
	proxyIdleTimeout time.Duration,
	teamServer bool,
	expectedProjectID string,
) (uow.UnitOfWorkProvider, error) {
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: dolt is not installed (not found in PATH); install from https://docs.dolthub.com/introduction/installation: %w", err)
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
	archiveLevelSupported := doltserver.SupportsArchiveLevelConfig(doltBin)

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
		teamServer,
		expectedProjectID,
	)
}
