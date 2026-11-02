package uow

import (
	"context"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// proxyEndpointResolver re-resolves the workspace proxy's endpoint for every
// new physical connection of a provider pool (#7295). A proxy with a finite
// idle timeout exits once its last client disconnects, and the replacement the
// next opener spawns may publish a different OS-assigned port. A pool whose DSN
// pinned the construction-time address would keep dialing the retired port,
// so the driver's BeforeConnect hook replaces the address on its per-connection
// copy of the configuration before it dials.
//
// discover is the same proxy discovery the constructor ran: it authenticates
// the published proxy, adopts a live one without taking the spawn lock, and
// otherwise spawns a replacement under the existing proxy lifecycle rules.
type proxyEndpointResolver struct {
	discover func() (proxy.Endpoint, error)
}

func newProxyEndpointResolver(discover func() (proxy.Endpoint, error)) *proxyEndpointResolver {
	return &proxyEndpointResolver{discover: discover}
}

// workspaceProxyDiscovery binds discovery to the absolute workspace root and the
// exact OpenOpts a constructor used, so every rediscovery asks for the same
// proxy, backend and upstream the provider was opened against.
func workspaceProxyDiscovery(rootDir string, opts proxy.OpenOpts) func() (proxy.Endpoint, error) {
	return func() (proxy.Endpoint, error) {
		return proxy.GetCreateDatabaseProxyServerEndpoint(rootDir, opts)
	}
}

// beforeConnect is the mysql.BeforeConnect hook. cfg is the driver's clone for
// this one connection: only its address changes, so database, credentials,
// TLS, timeouts and ClientFoundRows stay as the provider was opened with.
func (r *proxyEndpointResolver) beforeConnect(ctx context.Context, cfg *mysql.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ep, err := r.discover()
	if err != nil {
		return err
	}
	cfg.Addr = ep.Address()
	return nil
}
