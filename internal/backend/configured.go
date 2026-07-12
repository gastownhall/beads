// Package backend opens the built-in storage backend selected by a Beads
// workspace's metadata. It deliberately contains no executable-plugin
// discovery: callers of the public Go API must never execute a command named
// in committed workspace metadata.
package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	beadsmysql "github.com/steveyegge/beads/internal/storage/mysql"
	"github.com/steveyegge/beads/internal/storage/postgres"
	beadssqlite "github.com/steveyegge/beads/internal/storage/sqlite"
)

// Capabilities describes optional backend behavior without exposing a concrete
// database driver or connection handle.
type Capabilities struct {
	Embedded          bool
	Transactions      bool
	RawSQL            bool
	Leases            bool
	Maintenance       bool
	Versioning        bool
	Branching         bool
	DoltRemotes       bool
	ConcurrentWriters bool
}

// Descriptor identifies the built-in backend opened for a workspace.
type Descriptor struct {
	Name         string
	External     bool
	Capabilities Capabilities
}

// OpenConfigured opens the built-in backend selected by metadata.json. It
// fails closed for unsupported names, rather than silently opening Dolt.
func OpenConfigured(ctx context.Context, beadsDir string, readOnly bool) (storage.DoltStorage, Descriptor, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, Descriptor{}, fmt.Errorf("load %s: %w", configfile.ConfigPath(beadsDir), err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	name := strings.ToLower(strings.TrimSpace(cfg.Backend))
	if name == "" {
		name = configfile.BackendDolt
	}
	switch name {
	case configfile.BackendDolt:
		store, err := openDolt(ctx, beadsDir, cfg, readOnly)
		if err != nil {
			return nil, Descriptor{}, err
		}
		return store, Descriptor{Name: configfile.BackendDolt, Capabilities: doltCapabilities(cfg)}, nil
	case configfile.BackendPostgres:
		store, err := postgres.NewFromConfig(ctx, beadsDir)
		return store, Descriptor{Name: configfile.BackendPostgres, Capabilities: sqlServerCapabilities()}, err
	case configfile.BackendMySQL:
		store, err := beadsmysql.NewFromConfig(ctx, beadsDir)
		return store, Descriptor{Name: configfile.BackendMySQL, Capabilities: sqlServerCapabilities()}, err
	case configfile.BackendSQLite:
		store, err := beadssqlite.NewFromConfig(ctx, beadsDir)
		return store, Descriptor{Name: configfile.BackendSQLite, Capabilities: Capabilities{Embedded: true, Transactions: true, Leases: true}}, err
	default:
		return nil, Descriptor{}, fmt.Errorf("unsupported configured backend %q", name)
	}
}

func doltCapabilities(cfg *configfile.Config) Capabilities {
	return Capabilities{
		Embedded:          !cfg.IsDoltServerMode() && !cfg.IsDoltProxiedServerMode(),
		Transactions:      true,
		RawSQL:            true,
		Leases:            true,
		Maintenance:       true,
		Versioning:        true,
		Branching:         true,
		DoltRemotes:       true,
		ConcurrentWriters: cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode(),
	}
}

func sqlServerCapabilities() Capabilities {
	return Capabilities{Transactions: true, Leases: true, ConcurrentWriters: true}
}
