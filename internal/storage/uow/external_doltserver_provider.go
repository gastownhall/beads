package uow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func NewExternalDoltServerUOWProvider(
	ctx context.Context,
	serverRootDir string,
	database string,
	serverLogFilePath string,
	external configfile.ExternalDoltConfig,
	rootUser string,
	rootPassword string,
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
	if rootUser == "" {
		return nil, fmt.Errorf("uow: rootUser must not be empty")
	}
	if err := external.Validate(); err != nil {
		return nil, fmt.Errorf("uow: external: %w", err)
	}

	absServerRootDir, err := filepath.Abs(serverRootDir)
	if err != nil {
		return nil, fmt.Errorf("uow: resolving server root dir: %w", err)
	}

	if err := os.MkdirAll(absServerRootDir, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("uow: creating server root directory: %w", err)
	}

	tlsConfigName, err := registerExternalTLSConfig(external)
	if err != nil {
		return nil, fmt.Errorf("uow: external TLS: %w", err)
	}

	resolved := applyProviderOptions(opts)
	// The upstream is external, but the proxy in front of it is local and
	// retires on idle exactly like a managed one, so new connections
	// rediscover it too. Only its address is refreshed: the upstream, its TLS
	// config and the credentials stay the ones this provider opened with.
	discover := workspaceProxyDiscovery(absServerRootDir, proxy.OpenOpts{
		Backend:       proxy.BackendExternal,
		LogFilePath:   serverLogFilePath,
		RotateLogFile: resolved.rotateServerLog,
		External:      external,
		IdleTimeout:   idleTimeout,
		Port:          proxyPort,
	})
	ep, err := discover()
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	return openAndInitSchema(ctx, proxy.DataNetwork, ep, externalBackendAddress(external), database, rootUser, rootPassword, tlsConfigName, teamServer, expectedProjectID, resolved, newProxyEndpointResolver(discover))
}

// externalBackendAddress names the external server behind the proxy: its
// socket, or its host and port.
func externalBackendAddress(external configfile.ExternalDoltConfig) string {
	if external.Socket != "" {
		return external.Socket
	}
	return doltutil.ServerAddr(external.Host, external.Port)
}

func registerExternalTLSConfig(external configfile.ExternalDoltConfig) (string, error) {
	if !external.TLSRequired {
		return "", nil
	}
	tc, err := external.TLSClientConfig()
	if err != nil {
		return "", err
	}
	name := "beads-external-" + server.ExternalDoltServerID(external)
	if err := mysql.RegisterTLSConfig(name, tc); err != nil {
		return "", fmt.Errorf("register TLS config: %w", err)
	}
	return name, nil
}
