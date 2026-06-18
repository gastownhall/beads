package schema

// cliCompatibleMigrationSQL returns migration SQL suitable for `dolt sql -q`
// against a fresh test database. The Dolt CLI accepts PREPARE/EXECUTE DDL but
// does not apply some prepared ALTER TABLE statements in this path, so the
// fresh-schema bundle uses direct DDL for prepared DDL that can change the
// committed schema shape. The bundle's contract is to reproduce the runtime
// committed schema for a fresh database; runtime migrations still use the source
// files and remain the source of truth for upgrades of existing databases.
func cliCompatibleMigrationSQL(name, sqlText string) string {
	switch name {
	case "0008_create_child_counters.up.sql":
		// Fresh bundle bakes the final FK shape that runtime reaches after
		// 0039 drops the original FK and ignored migration 0002 re-adds it.
		return cliMigration0008CreateChildCounters
	case "0023_add_no_history_column.up.sql":
		return cliMigration0023AddNoHistoryColumn
	case "0027_add_started_at.up.sql":
		return cliMigration0027AddStartedAt
	case "0032_drop_schema_migrations_applied_at.up.sql":
		return cliMigration0032DropSchemaMigrationsAppliedAt
	case "0033_add_wisp_type_column.up.sql":
		// No-op on fresh schema: wisp_type is already in squashed base 0001.
		return "SELECT 1;"
	case "0034_add_spec_id_column.up.sql":
		// No-op on fresh schema: spec_id and idx_issues_spec_id are in 0001.
		return "SELECT 1;"
	case "0039_drop_child_counters_fk.up.sql":
		// No-op here because 0008 already emits the final ignored-0002 FK.
		return "SELECT 1;"
	case "0041_split_dependencies_target.up.sql":
		return cliMigration0041SplitDependenciesTarget
	case "0043_drop_dependencies_generated_column.up.sql":
		return cliMigration0043DropDependenciesGeneratedColumn
	case "0046_add_is_blocked.up.sql":
		// Fresh databases contain no issue graph, so only the schema delta is
		// needed; the source migration's recursive backfill is dead work here.
		return cliMigration0046AddIsBlocked
	case "0049_longtext_large_content_columns.up.sql":
		return cliMigration0049LongtextLargeContentColumns
	case "0051_drop_aux_id_defaults.up.sql":
		// Direct DDL: the source migration's PREPARE/EXECUTE guards exist for
		// re-run safety on upgraded databases; a fresh bundle always has the
		// 0004/0005/0009/0010 defaults to drop.
		return cliMigration0051DropAuxIDDefaults
	default:
		return sqlText
	}
}

const cliMigration0008CreateChildCounters = `CREATE TABLE IF NOT EXISTS child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_counter_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE
);`

const cliMigration0023AddNoHistoryColumn = `ALTER TABLE issues ADD COLUMN no_history TINYINT(1) DEFAULT 0;
ALTER TABLE wisps ADD COLUMN no_history TINYINT(1) DEFAULT 0;`

const cliMigration0027AddStartedAt = `ALTER TABLE issues ADD COLUMN started_at DATETIME;
ALTER TABLE wisps ADD COLUMN started_at DATETIME;`

const cliMigration0032DropSchemaMigrationsAppliedAt = `ALTER TABLE schema_migrations DROP COLUMN applied_at;`

const cliMigration0041SplitDependenciesTarget = `DELETE FROM dolt_nonlocal_tables;
CALL DOLT_COMMIT('-Am', 'disable nonlocal tables for fk migrations');
SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE dependencies ADD COLUMN depends_on_issue_id VARCHAR(255) NULL;
ALTER TABLE dependencies ADD COLUMN depends_on_wisp_id VARCHAR(255) NULL;
ALTER TABLE dependencies ADD COLUMN depends_on_external VARCHAR(255) NULL;

UPDATE dependencies SET depends_on_external = depends_on_id WHERE depends_on_id LIKE 'external:%';
UPDATE dependencies d JOIN wisps w ON w.id = d.depends_on_id SET d.depends_on_wisp_id = d.depends_on_id WHERE d.depends_on_external IS NULL;
UPDATE dependencies d JOIN issues i ON i.id = d.depends_on_id SET d.depends_on_issue_id = d.depends_on_id WHERE d.depends_on_external IS NULL AND d.depends_on_wisp_id IS NULL;
UPDATE dependencies SET depends_on_external = depends_on_id WHERE depends_on_external IS NULL AND depends_on_wisp_id IS NULL AND depends_on_issue_id IS NULL;

ALTER TABLE dependencies DROP INDEX idx_dependencies_depends_on;
ALTER TABLE dependencies DROP INDEX idx_dependencies_depends_on_type;
ALTER TABLE dependencies DROP PRIMARY KEY;
ALTER TABLE dependencies DROP COLUMN depends_on_id;

ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE dependencies ADD COLUMN depends_on_id VARCHAR(255) AS (COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) STORED;
ALTER TABLE dependencies ADD PRIMARY KEY (issue_id, depends_on_id);
ALTER TABLE dependencies ADD INDEX idx_dep_wisp_target (depends_on_wisp_id);
ALTER TABLE dependencies ADD INDEX idx_dep_issue_target (depends_on_issue_id);
ALTER TABLE dependencies ADD INDEX idx_dep_external_target (depends_on_external);
ALTER TABLE dependencies ADD INDEX idx_dep_type_target (type, depends_on_id);
ALTER TABLE dependencies ADD CONSTRAINT ck_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1);

SET FOREIGN_KEY_CHECKS = 1;`

const cliMigration0043DropDependenciesGeneratedColumn = `SET FOREIGN_KEY_CHECKS = 0;

ALTER TABLE dependencies DROP INDEX idx_dep_type_target;
ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_issue_target;
ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_issue;
ALTER TABLE dependencies DROP PRIMARY KEY;
ALTER TABLE dependencies DROP COLUMN depends_on_id;

ALTER TABLE dependencies ADD COLUMN id CHAR(36) NOT NULL DEFAULT (UUID()) PRIMARY KEY FIRST;
ALTER TABLE dependencies ADD UNIQUE KEY uk_dep_issue_target (issue_id, depends_on_issue_id);
ALTER TABLE dependencies ADD UNIQUE KEY uk_dep_wisp_target (issue_id, depends_on_wisp_id);
ALTER TABLE dependencies ADD UNIQUE KEY uk_dep_external_target (issue_id, depends_on_external);
ALTER TABLE dependencies ADD INDEX idx_dep_type_issue (type, depends_on_issue_id);
ALTER TABLE dependencies ADD INDEX idx_dep_type_wisp (type, depends_on_wisp_id);
ALTER TABLE dependencies ADD INDEX idx_dep_type_external (type, depends_on_external);
ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE;

SET FOREIGN_KEY_CHECKS = 1;`

const cliMigration0046AddIsBlocked = `ALTER TABLE issues ADD COLUMN is_blocked TINYINT(1) NOT NULL DEFAULT 0;
CREATE INDEX idx_issues_is_blocked ON issues(is_blocked, status);`

const cliMigration0049LongtextLargeContentColumns = `ALTER TABLE issues MODIFY COLUMN description LONGTEXT NOT NULL, MODIFY COLUMN design LONGTEXT NOT NULL, MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL, MODIFY COLUMN notes LONGTEXT NOT NULL;
ALTER TABLE issues MODIFY COLUMN close_reason LONGTEXT DEFAULT '';
ALTER TABLE wisps MODIFY COLUMN description LONGTEXT NOT NULL DEFAULT '', MODIFY COLUMN design LONGTEXT NOT NULL DEFAULT '', MODIFY COLUMN acceptance_criteria LONGTEXT NOT NULL DEFAULT '', MODIFY COLUMN notes LONGTEXT NOT NULL DEFAULT '';
ALTER TABLE wisps MODIFY COLUMN close_reason LONGTEXT DEFAULT '';
ALTER TABLE comments MODIFY COLUMN text LONGTEXT NOT NULL;`

const cliMigration0051DropAuxIDDefaults = `ALTER TABLE events ALTER COLUMN id DROP DEFAULT;
ALTER TABLE comments ALTER COLUMN id DROP DEFAULT;
ALTER TABLE issue_snapshots ALTER COLUMN id DROP DEFAULT;
ALTER TABLE compaction_snapshots ALTER COLUMN id DROP DEFAULT;`
