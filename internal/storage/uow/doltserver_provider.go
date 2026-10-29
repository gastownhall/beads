package uow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"

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
	resolve := func(ctx context.Context) (proxy.Endpoint, error) {
		return proxy.GetCreateDatabaseProxyServerEndpointContext(ctx, absServerRootDir, openOpts)
	}
	ep, err := resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	// Keep the schema pool: steady-state opens still use one MySQL session.
	// Bootstrap authority stays on ep until initialization succeeds. Only
	// subsequent physical connections may discover a replacement endpoint.
	var initialized atomic.Bool
	beforeConnect := managedProxyBeforeConnect(&initialized, resolve)
	provider, err := openAndInitSchema(ctx, ep, "", database, rootUser, rootPassword, "", teamServer, expectedProjectID, resolved, mysql.BeforeConnect(beforeConnect))
	if err != nil {
		return nil, err
	}
	initialized.Store(true)
	return provider, nil
}

// managedProxyBeforeConnect uses the driver's per-connect configuration copy.
// BeforeConnect runs before the TCP dial timeout, so discovery keeps its native
// open budget and caller cancellation without consuming the five-second dial
// budget. No statements or transactions are retried here.
func managedProxyBeforeConnect(initialized *atomic.Bool, resolve func(context.Context) (proxy.Endpoint, error)) func(context.Context, *mysql.Config) error {
	return func(ctx context.Context, cfg *mysql.Config) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !initialized.Load() {
			return nil
		}
		ep, err := resolve(ctx)
		if err != nil {
			return fmt.Errorf("uow: rediscover proxy endpoint: %w", err)
		}
		cfg.Addr = ep.Address()
		return nil
	}
}
