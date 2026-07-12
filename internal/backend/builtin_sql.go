package backend

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	beadsmysql "github.com/steveyegge/beads/internal/storage/mysql"
	"github.com/steveyegge/beads/internal/storage/postgres"
	beadssqlite "github.com/steveyegge/beads/internal/storage/sqlite"
)

func init() {
	Register(postgresProvider{})
	Register(mysqlProvider{})
	Register(sqliteProvider{})
}

type postgresProvider struct{}

func (postgresProvider) Name() string { return configfile.BackendPostgres }
func (postgresProvider) Capabilities(*configfile.Config) Capabilities {
	return sqlServerCapabilities()
}
func (postgresProvider) Open(ctx context.Context, opts OpenOptions) (storage.DoltStorage, error) {
	return postgres.NewFromConfig(ctx, opts.BeadsDir)
}

type mysqlProvider struct{}

func (mysqlProvider) Name() string { return configfile.BackendMySQL }
func (mysqlProvider) Capabilities(*configfile.Config) Capabilities {
	return sqlServerCapabilities()
}
func (mysqlProvider) Open(ctx context.Context, opts OpenOptions) (storage.DoltStorage, error) {
	return beadsmysql.NewFromConfig(ctx, opts.BeadsDir)
}

type sqliteProvider struct{}

func (sqliteProvider) Name() string { return configfile.BackendSQLite }
func (sqliteProvider) Capabilities(*configfile.Config) Capabilities {
	return Capabilities{
		Embedded:     true,
		Transactions: true,
		Leases:       true,
	}
}
func (sqliteProvider) Open(ctx context.Context, opts OpenOptions) (storage.DoltStorage, error) {
	return beadssqlite.NewFromConfig(ctx, opts.BeadsDir)
}

func sqlServerCapabilities() Capabilities {
	return Capabilities{
		Transactions:      true,
		Leases:            true,
		ConcurrentWriters: true,
	}
}
