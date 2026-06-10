package uow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
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
) (UnitOfWorkProvider, error) {
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
		IdleTimeout:    proxy.IdleTimeoutFromEnv(defaultProxyIdleTimeout),
		PoolSize:       proxy.PoolSizeFromEnv(),
		BackendUser:    rootUser,
	})
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	// On-disk remote probe for the remote-migrate gate: the child dolt
	// sql-server stores the database at <serverRootDir>/<database>, and a
	// freshly started server can report an empty dolt_remotes table even
	// though a remote is persisted in .dolt (GH#2315). Reads
	// repo_state.json directly; a read/parse failure fails open but is
	// logged, never swallowed (bd-6dnrw.33).
	hasRemoteProbe := func() bool {
		for _, dir := range []string{filepath.Join(absServerRootDir, database), absServerRootDir} {
			remotes, err := doltutil.PersistedRemotes(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr,
					"Warning: remote-migrate gate could not inspect %s for persisted remotes (assuming none): %v\n",
					dir, err)
				continue
			}
			if len(remotes) > 0 {
				return true
			}
		}
		return false
	}

	return openAndInitSchema(ctx, ep, database, rootUser, rootPassword, hasRemoteProbe)
}
