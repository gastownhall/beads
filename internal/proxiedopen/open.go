// Package proxiedopen builds the unit-of-work provider for a proxied-server
// workspace: it resolves the workspace's topology (metadata.json and the
// proxied-server sidecar), applies the open postures (identity assertion,
// fresh-store refusal), resolves and validates the server root, config and
// log, and constructs a managed-local or external provider that speaks
// BDPROXY1 to the per-root `bd db-proxy-child`. The events journal and
// versioned history are activated on every provider it returns.
//
// It is the bd CLI's provider construction, moved out of package main so the
// Go SDK's OpenBestAvailable opens a proxied-server workspace through the
// same code (GH#8323). Nothing here reads a CLI flag: what the CLI decided
// from its flags arrives in Options.
package proxiedopen

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// IdentityPosture selects whether a proxied open asserts the workspace's
// project identity against the database or adopts whatever it finds. The
// zero value asserts.
type IdentityPosture bool

const (
	AssertWorkspaceIdentity IdentityPosture = false
	AdoptWorkspaceIdentity  IdentityPosture = true
)

// FreshStorePosture selects whether a managed-local proxied open may create
// the Dolt store when the resolved root holds none. Only bd init allows it;
// see FreshStoreRefusal for what refusing protects. The zero value refuses.
type FreshStorePosture bool

const (
	RefuseFreshStore FreshStorePosture = false
	AllowFreshStore  FreshStorePosture = true
)

// Options is the open's posture. The zero value is the safe one: assert the
// workspace identity, refuse to create a store, print no advisories.
type Options struct {
	// DatabaseOverride replaces the workspace's configured database name.
	DatabaseOverride string
	Identity         IdentityPosture
	FreshStore       FreshStorePosture
	// ProviderOptions are passed to the provider constructor. Open appends
	// uow.WithProxiedServerMode() to them; OpenTopology passes them as given.
	ProviderOptions []uow.ProviderOption
	// Advisory receives non-fatal advisories (today only the unverifiable
	// dolt version warning). nil discards them.
	Advisory io.Writer
	// ProxyExecutable resolves the binary spawned as `<binary>
	// db-proxy-child` when no proxy is running for the root
	// (uow.WithProxyExecutable). nil keeps the running executable, which is
	// right only when the caller is bd itself.
	ProxyExecutable func() (string, error)
}

// Open resolves a proxied-server workspace's topology and opens its provider.
func Open(ctx context.Context, beadsDir string, o Options) (uow.UnitOfWorkProvider, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: beadsDir must be set")
	}
	topology, err := ResolveProxiedTopology(beadsDir, o.DatabaseOverride, o.Identity)
	if err != nil {
		return nil, err
	}
	if o.FreshStore == RefuseFreshStore {
		if err := FreshStoreRefusal(beadsDir, topology); err != nil {
			return nil, err
		}
	}
	// This is the one entry point that is proxied-server mode by definition;
	// OpenTopology is shared with `bd serve`'s SERVER-mode provider, which
	// must not be told it is proxied. The distinction reaches exactly one
	// thing: a migration-gate refusal whose remedy is `bd dolt pull` has to
	// say where that pull can be run, because proxied mode refuses it at the
	// front door (proxy.dolt_pull.unsupported) and server mode does not.
	o.ProviderOptions = ProxiedProviderOptions(o.ProviderOptions)
	return OpenTopology(ctx, beadsDir, topology, o)
}

// ProxiedProviderOptions is opts plus uow.WithProxiedServerMode(), built on a
// fresh slice so the caller's backing array is never written even when it has
// spare capacity.
func ProxiedProviderOptions(opts []uow.ProviderOption) []uow.ProviderOption {
	return append(append([]uow.ProviderOption{}, opts...), uow.WithProxiedServerMode())
}

// ServerLogProviderOptions is opts plus uow.WithServerLogRotation() when the
// server log is bd's default one, built on a fresh slice like
// ProxiedProviderOptions. A custom log (BEADS_PROXIED_SERVER_LOG or the
// sidecar's log path) is the operator's: bd neither rotates it at proxy start
// nor retires it on migration (proxiedLogAssets), so both follow isCustomLog.
func ServerLogProviderOptions(opts []uow.ProviderOption, isCustomLog bool) []uow.ProviderOption {
	if isCustomLog {
		return opts
	}
	return append(append([]uow.ProviderOption{}, opts...), uow.WithServerLogRotation())
}

// OpenTopology is the single funnel every unit-of-work provider bd builds
// passes through — the proxied path (Open) AND `bd serve`'s own provider for
// server-mode workspaces. Activation is applied by the two constructors it
// dispatches to, each alongside its own open; doing it at the CALL SITES
// instead is what left `bd serve` writing mutations into an empty journal
// while reporting success. o.Identity, o.FreshStore and o.DatabaseOverride
// are Open's; the topology already carries their effect.
func OpenTopology(ctx context.Context, beadsDir string, topology Topology, o Options) (uow.UnitOfWorkProvider, error) {
	if o.ProxyExecutable != nil {
		o.ProviderOptions = append(append([]uow.ProviderOption{}, o.ProviderOptions...), uow.WithProxyExecutable(o.ProxyExecutable))
	}
	var p uow.UnitOfWorkProvider
	var err error
	if topology.External != nil {
		p, err = newExternalProxiedServerUOWProvider(ctx, beadsDir, topology, o.ProviderOptions...)
	} else {
		p, err = newManagedProxiedServerUOWProvider(ctx, beadsDir, topology, o.Advisory, o.ProviderOptions...)
	}
	return p, asProxyUnavailable(err)
}

// newExternalProxiedServerUOWProvider fronts a Dolt SQL server this process
// neither started nor owns. "Proxied" in the name is the PROXY it builds, not
// the workspace mode: since bd-emv a server-mode workspace lands here too, and
// the paths it resolves (root, log) are the same ones proxied mode uses because
// both modes root their server at the same directory.
func newExternalProxiedServerUOWProvider(ctx context.Context, beadsDir string, topology Topology, opts ...uow.ProviderOption) (p uow.UnitOfWorkProvider, err error) {
	defer func() { p, err = activateVersionedHistoryProvider(ctx, p, err) }()
	defer func() { p, err = activateEventsJournalProvider(ctx, beadsDir, uow.OpensForBeadWrites(opts...), p, err) }()
	rootPath, err := ResolveRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := ValidateRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	logPath, isCustomLog, err := ResolveLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := ValidateLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}
	opts = ServerLogProviderOptions(opts, isCustomLog)

	if err := os.MkdirAll(rootPath, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: mkdir %s: %w", rootPath, err)
	}

	return uow.NewExternalDoltServerUOWProvider(
		ctx,
		rootPath,
		topology.Database,
		logPath,
		*topology.External,
		topology.External.ResolvedUser(),
		topology.RootPassword,
		topology.ProxyPort,
		topology.ProxyIdle,
		topology.TeamServer,
		topology.ExpectedProjectID,
		opts...,
	)
}

func newManagedProxiedServerUOWProvider(ctx context.Context, beadsDir string, topology Topology, advisory io.Writer, opts ...uow.ProviderOption) (p uow.UnitOfWorkProvider, err error) {
	defer func() { p, err = activateVersionedHistoryProvider(ctx, p, err) }()
	defer func() { p, err = activateEventsJournalProvider(ctx, beadsDir, uow.OpensForBeadWrites(opts...), p, err) }()
	// Resolve and hardened-probe the external dolt binary before spawning
	// it: an env/sidecar override that is explicitly named but broken
	// should fail loudly here rather than surface as a confusing spawn
	// failure downstream. See internal/doltversion for the resolution
	// precedence (env > sidecar > PATH) and probe hardening (timeout,
	// output cap, pre-exec validation).
	//
	// The error label names this function rather than the proxied-server
	// entry point: since upstream's server-mode path (bd serve ->
	// OpenTopology) also lands here, a hardcoded
	// "newProxiedServerUOWProvider" would mislabel a `bd serve` failure as a
	// proxied-server one.
	doltBin, err := ResolveAndProbeDolt(ctx, "newManagedProxiedServerUOWProvider", advisory)
	if err != nil {
		return nil, err
	}

	rootPath, err := ResolveRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := ValidateRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	configPath, err := EnsureConfig(beadsDir)
	if err != nil {
		return nil, err
	}

	logPath, isCustomLog, err := ResolveLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := ValidateLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}
	opts = ServerLogProviderOptions(opts, isCustomLog)

	return uow.NewDoltServerUOWProvider(
		ctx,
		rootPath,
		topology.Database,
		logPath,
		configPath,
		proxy.BackendLocalServer,
		"root",
		"", // proxy is loopback-only, no auth
		doltBin,
		topology.ProxyPort,
		topology.ProxyIdle,
		topology.TeamServer,
		topology.ExpectedProjectID,
		opts...,
	)
}
