package uow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func NewDoltServerUOWProvider(
	ctx context.Context,
	serverRootDir string,
	database string,
	serverLogFilePath string,
	serverConfigFilePath string,
	backend proxy.Backend,
	rootUser string,
	rootPassword string,
	doltBinExec string,
	proxyPort int,
	idleTimeout time.Duration,
	teamServer bool,
	expectedProjectID string,
	opts ...ProviderOption,
) (UnitOfWorkProvider, error) {
	if idleTimeout == 0 {
		idleTimeout = defaultProxyIdleTimeout
	}
	if database == "" {
		return nil, fmt.Errorf("uow: database name must not be empty (caller should default to %q)", "beads")
	}
	if err := backend.Validate(); err != nil {
		return nil, fmt.Errorf("uow: backend: %w", err)
	}
	if rootUser == "" {
		return nil, fmt.Errorf("uow: rootUser must not be empty")
	}
	if doltBinExec == "" {
		return nil, fmt.Errorf("uow: doltBinExec must not be empty")
	}

	absServerRootDir, err := filepath.Abs(serverRootDir)
	if err != nil {
		return nil, fmt.Errorf("uow: resolving server root dir: %w", err)
	}
	absDoltBinExec, err := filepath.Abs(doltBinExec)
	if err != nil {
		return nil, fmt.Errorf("uow: resolving dolt bin exec: %w", err)
	}

	if err := os.MkdirAll(absServerRootDir, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("uow: creating server root directory: %w", err)
	}

	resolved := applyProviderOptions(opts)
	openOpts := proxy.OpenOpts{
		Backend:        backend,
		ConfigFilePath: serverConfigFilePath,
		LogFilePath:    serverLogFilePath,
		RotateLogFile:  resolved.rotateServerLog,
		DoltBinPath:    absDoltBinExec,
		Database:       database,
		IdleTimeout:    idleTimeout,
		Port:           proxyPort,
	}
	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(absServerRootDir, openOpts)
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	// The resolver rediscovers this workspace's proxy when the endpoint just
	// resolved stops answering — an idle-exited proxy leaves a long-lived
	// provider (bd serve) dialing a dead port forever otherwise
	// (gastownhall/beads#7295). Pinned to the endpoint the constructor
	// resolved, unarmed until the open below succeeds, and reusing the SAME
	// OpenOpts so rediscovery adopts or spawns under the workspace's own
	// identity and idle policy rather than promoting anything to never-idle.
	resolver := proxy.NewEndpointResolver(absServerRootDir, openOpts, ep)
	return openAndInitSchema(ctx, proxy.DataNetwork, ep, "", database, rootUser, rootPassword, "", teamServer, expectedProjectID, resolved, resolver)
}
