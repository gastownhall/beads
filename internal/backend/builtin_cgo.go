//go:build cgo

package backend

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func init() {
	Register(doltProvider{})
}

type doltProvider struct{}

func (doltProvider) Name() string { return configfile.BackendDolt }

func (doltProvider) Capabilities(cfg *configfile.Config) Capabilities {
	caps := Capabilities{
		Embedded:     true,
		Transactions: true,
		RawSQL:       true,
		Leases:       true,
		Maintenance:  true,
		Versioning:   true,
		Branching:    true,
		DoltRemotes:  true,
	}
	if cfg != nil && (cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode()) {
		caps.ConcurrentWriters = true
	}
	return caps
}

func (doltProvider) Open(ctx context.Context, opts OpenOptions) (storage.DoltStorage, error) {
	if opts.ProxiedServer {
		return nil, fmt.Errorf("proxy server store should be uow provider")
	}
	if opts.ServerMode {
		return dolt.NewFromConfigWithOptions(ctx, opts.BeadsDir, &dolt.Config{ReadOnly: opts.ReadOnly})
	}
	database := opts.Database
	if database == "" {
		database = configfile.DefaultDoltDatabase
	}
	branch := opts.Branch
	if branch == "" {
		branch = "main"
	}
	if opts.ReadOnlyCommand {
		return embeddeddolt.OpenForReadOnlyCommand(ctx, opts.BeadsDir, database, branch)
	}
	if opts.ReadOnly {
		return embeddeddolt.OpenReadOnly(ctx, opts.BeadsDir, database, branch)
	}
	if opts.LenientOpen {
		return embeddeddolt.OpenForWorkingSetReconcile(ctx, opts.BeadsDir, database, branch)
	}
	return embeddeddolt.Open(ctx, opts.BeadsDir, database, branch)
}
