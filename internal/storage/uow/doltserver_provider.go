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

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(absServerRootDir, proxy.OpenOpts{
		Backend:        backend,
		ConfigFilePath: serverConfigFilePath,
		LogFilePath:    serverLogFilePath,
		DoltBinPath:    absDoltBinExec,
		Database:       database,
		IdleTimeout:    idleTimeout,
		Port:           proxyPort,
	})
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	return openAndInitSchema(ctx, ep, database, rootUser, rootPassword, "")
}
