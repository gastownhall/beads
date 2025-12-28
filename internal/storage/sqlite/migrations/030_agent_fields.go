package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateAgentFields adds agent-specific fields to the issues table.
// These fields support the agent-as-bead pattern:
//   - hook_bead: current work attached to agent's hook (0..1 cardinality)
//   - role_bead: reference to role definition bead
//   - agent_state: agent-reported state (idle|running|stuck|stopped)
//   - last_activity: timestamp for timeout detection
//   - role_type: agent role (polecat|crew|witness|refinery|mayor|deacon)
//   - rig: rig name (empty for town-level agents)
func MigrateAgentFields(db *sql.DB) error {
	columns := []struct {
		name    string
		sqlType string
	}{
		{"hook_bead", "TEXT DEFAULT ''"},
		{"role_bead", "TEXT DEFAULT ''"},
		{"agent_state", "TEXT DEFAULT ''"},
		{"last_activity", "DATETIME"},
		{"role_type", "TEXT DEFAULT ''"},
		{"rig", "TEXT DEFAULT ''"},
	}

	for _, col := range columns {
		// Check if column already exists
		var columnExists bool
		err := db.QueryRow(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('issues')
			WHERE name = ?
		`, col.name).Scan(&columnExists)
		if err != nil {
			return fmt.Errorf("failed to check %s column: %w", col.name, err)
		}

		if columnExists {
			continue
		}

		// Add the column
		_, err = db.Exec(fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, col.name, col.sqlType))
		if err != nil {
			return fmt.Errorf("failed to add %s column: %w", col.name, err)
		}
	}

	return nil
}
