package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateCompactionConfig(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO config (key, value) VALUES
			('compaction_enabled', 'false'),
			('compact_tier1_days', '30'),
			('compact_tier1_dep_levels', '2'),
			('compact_tier2_days', '90'),
			('compact_tier2_dep_levels', '5'),
			('compact_tier2_commits', '100'),
			('compact_model', 'claude-3-5-haiku-20241022'),
			('compact_batch_size', '50'),
			('compact_parallel_workers', '5'),
			('auto_compact_enabled', 'false')
	`)
	if err != nil {
		return fmt.Errorf("failed to add compaction config defaults: %w", err)
	}
	return nil
}
