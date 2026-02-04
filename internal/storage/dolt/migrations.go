//go:build cgo && !windows
package dolt

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt/migrations"
)

// Migration represents a single schema migration for Dolt.
type Migration struct {
	Name string
	Func func(*sql.DB) error
}

// migrationsList is the ordered list of all Dolt schema migrations.
// Each migration must be idempotent - safe to run multiple times.
// New migrations should be appended to the end of this list.
var migrationsList = []Migration{
	{"wisp_type_column", migrations.MigrateWispTypeColumn},
}

// RunMigrations executes all registered Dolt migrations in order.
// Each migration is idempotent and checks whether its changes have
// already been applied before making modifications.
func RunMigrations(db *sql.DB) error {
	for _, m := range migrationsList {
		if err := m.Func(db); err != nil {
			return fmt.Errorf("dolt migration %q failed: %w", m.Name, err)
		}
	}

	// Commit schema changes via Dolt (idempotent - no-ops if nothing changed)
	_, err := db.Exec("CALL DOLT_COMMIT('-Am', 'schema: auto-migrate')")
	if err != nil {
		// "nothing to commit" is expected when migrations were already applied
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			log.Printf("dolt migration commit warning: %v", err)
		}
	}

	return nil
}

// ListMigrations returns the names of all registered migrations.
func ListMigrations() []string {
	names := make([]string, len(migrationsList))
	for i, m := range migrationsList {
		names[i] = m.Name
	}
	return names
}

