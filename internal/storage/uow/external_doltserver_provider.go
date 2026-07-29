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

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(absServerRootDir, proxy.OpenOpts{
		Backend:     proxy.BackendExternal,
		LogFilePath: serverLogFilePath,
		External:    external,
		IdleTimeout: idleTimeout,
		Port:        proxyPort,
	})
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	return openAndInitSchema(ctx, ep, database, rootUser, rootPassword, tlsConfigName)
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
