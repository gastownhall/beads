//go:build !cgo

package backend

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func init() {
	Register(doltProvider{})
}

type doltProvider struct{}

func (doltProvider) Name() string { return configfile.BackendDolt }

func (doltProvider) Capabilities(cfg *configfile.Config) Capabilities {
	return Capabilities{
		Transactions:      true,
		RawSQL:            true,
		Leases:            true,
		Maintenance:       true,
		Versioning:        true,
		Branching:         true,
		DoltRemotes:       true,
		ConcurrentWriters: cfg != nil && (cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode()),
	}
}

func (doltProvider) Open(ctx context.Context, opts OpenOptions) (storage.DoltStorage, error) {
	if opts.ProxiedServer {
		return nil, fmt.Errorf("proxy server store should be uow provider")
	}
	if !opts.ServerMode {
		return nil, fmt.Errorf("embedded Dolt requires a CGO build; use server mode (bd init --server)")
	}
	return dolt.NewFromConfigWithOptions(ctx, opts.BeadsDir, &dolt.Config{ReadOnly: opts.ReadOnly})
}
