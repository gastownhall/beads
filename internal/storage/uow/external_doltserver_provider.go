package uow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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

	tlsConfigName, releaseTLS, err := registerExternalTLSConfig(external)
	if err != nil {
		return nil, fmt.Errorf("uow: external TLS: %w", err)
	}

	resolved := applyProviderOptions(opts)
	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(absServerRootDir, proxy.OpenOpts{
		Backend:       proxy.BackendExternal,
		LogFilePath:   serverLogFilePath,
		RotateLogFile: resolved.rotateServerLog,
		External:      external,
		IdleTimeout:   idleTimeout,
		Port:          proxyPort,
	})
	if err != nil {
		releaseTLS()
		return nil, fmt.Errorf("uow: get proxy endpoint: %w", err)
	}

	provider, err := openAndInitSchema(ctx, ep, externalBackendAddress(external), database, rootUser, rootPassword, tlsConfigName, teamServer, expectedProjectID, resolved)
	if err != nil {
		releaseTLS()
		return nil, err
	}
	sqlProvider, ok := provider.(*doltSQLProvider)
	if !ok {
		closeErr := provider.Close(ctx)
		releaseTLS()
		return nil, errors.Join(fmt.Errorf("uow: external provider is a %T, which cannot release its TLS config", provider), closeErr)
	}
	sqlProvider.releaseTLS = releaseTLS
	return sqlProvider, nil
}

// externalBackendAddress names the external server behind the proxy: its
// socket, or its host and port.
func externalBackendAddress(external configfile.ExternalDoltConfig) string {
	if external.Socket != "" {
		return external.Socket
	}
	return doltutil.ServerAddr(external.Host, external.Port)
}

var externalTLSConfigSeq atomic.Uint64

// registerExternalTLSConfig registers the external server's TLS settings with
// the MySQL driver under a name used by this provider alone. The driver clones
// a registered config when a pool opens, so each pool keeps the config, and
// the certificate store handles, it opened with. release removes the name and
// closes those handles.
func registerExternalTLSConfig(external configfile.ExternalDoltConfig) (name string, release func(), err error) {
	if !external.TLSRequired {
		return "", func() {}, nil
	}
	tc, closer, err := external.TLSClientConfig()
	if err != nil {
		return "", nil, err
	}
	closeStore := func() {
		if closer != nil {
			_ = closer.Close()
		}
	}
	name = fmt.Sprintf("beads-external-%s-%d", server.ExternalDoltServerID(external), externalTLSConfigSeq.Add(1))
	if err := mysql.RegisterTLSConfig(name, tc); err != nil {
		closeStore()
		return "", nil, fmt.Errorf("register TLS config: %w", err)
	}
	return name, func() {
		mysql.DeregisterTLSConfig(name)
		closeStore()
	}, nil
}
