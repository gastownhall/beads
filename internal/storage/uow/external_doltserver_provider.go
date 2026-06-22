package uow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pool"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func NewExternalDoltServerUOWProvider(
	ctx context.Context,
	serverRootDir string,
	database string,
	serverLogFilePath string,
	external configfile.ExternalDoltConfig,
	rootUser string,
	rootPassword string,
) (UnitOfWorkProvider, error) {
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

	opts := proxy.OpenOpts{
		Backend:     proxy.BackendExternal,
		LogFilePath: serverLogFilePath,
		External:    external,
		IdleTimeout: defaultProxyIdleTimeout,
	}
	// Opt-in: front the external Dolt server with the query-level connection
	// pooler (a unix socket) instead of the TCP byte-passthrough proxy. Gated
	// by BEADS_DOLT_POOL so default behavior is unchanged.
	if pool.EnabledFromEnv() {
		opts.Backend = proxy.BackendExternalPooled
		opts.PoolSocket = pool.SocketFromEnv(absServerRootDir)
		opts.PoolSize = pool.SizeFromEnv()
	}

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(absServerRootDir, opts)
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	return openAndInitSchema(ctx, ep, database, rootUser, rootPassword)
}
