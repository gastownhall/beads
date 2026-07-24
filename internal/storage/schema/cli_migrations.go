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
	case "0053_repair_rig_wisps.up.sql":
		// The source migration uses PREPARE guards so older upgraded
		// workspaces without local wisp tables can no-op safely. Fresh CLI
		// bundles already have the base wisp tables, and the Dolt CLI test
		// path needs direct DML for deterministic fixture repair.
		return cliMigration0053RepairRigWisps
	case "0054_add_lease_columns.up.sql":
		// Fresh bundle bakes the lease columns directly: the Dolt CLI does not
		// apply the prepared ALTER TABLE statements the runtime migration uses
		// for idempotent re-runs on upgraded databases.
		return cliMigration0054AddLeaseColumns
	case "0055_move_leases_to_table.up.sql":
		// Direct DDL for the same reason as 0054. A fresh bundle has no live
		// leases to copy (0054 just added empty columns), so this is pure
		// schema delta: create the ephemeral leases table, drop the issues/
		// wisps lease columns 0054 added. row_lock stays (see the migration).
		return cliMigration0055MoveLeasesToTable
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

const cliMigration0054AddLeaseColumns = `ALTER TABLE issues ADD COLUMN lease_expires_at DATETIME;
ALTER TABLE issues ADD COLUMN heartbeat_at DATETIME;
ALTER TABLE issues ADD COLUMN row_lock BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_issues_lease ON issues (status, lease_expires_at);
ALTER TABLE wisps ADD COLUMN lease_expires_at DATETIME;
ALTER TABLE wisps ADD COLUMN heartbeat_at DATETIME;
ALTER TABLE wisps ADD COLUMN row_lock BIGINT NOT NULL DEFAULT 0;`

const cliMigration0055MoveLeasesToTable = `CREATE TABLE leases (
    issue_id VARCHAR(255) PRIMARY KEY,
    holder VARCHAR(255) NOT NULL,
    granted_at DATETIME NOT NULL,
    lease_expires_at DATETIME NOT NULL,
    heartbeat_at DATETIME NOT NULL,
    INDEX idx_leases_expires (lease_expires_at)
);
ALTER TABLE issues DROP INDEX idx_issues_lease;
ALTER TABLE issues DROP COLUMN lease_expires_at;
ALTER TABLE issues DROP COLUMN heartbeat_at;
ALTER TABLE wisps DROP COLUMN lease_expires_at;
ALTER TABLE wisps DROP COLUMN heartbeat_at;`

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

const cliMigration0053RepairRigWisps = `SET FOREIGN_KEY_CHECKS = 0;

INSERT IGNORE INTO issues (
    id, content_hash, title, description, design, acceptance_criteria, notes,
    status, priority, issue_type, assignee, estimated_minutes, created_at,
    created_by, owner, updated_at, closed_at, closed_by_session, external_ref,
    spec_id, compaction_level, compacted_at, compacted_at_commit, original_size,
    sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type,
    source_system, metadata, source_repo, close_reason, event_kind, actor,
    target, payload, await_type, await_id, timeout_ns, waiters, hook_bead,
    role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until,
    no_history, started_at
)
SELECT
    id, content_hash, title, description, design, acceptance_criteria, notes,
    status, priority, issue_type, assignee, estimated_minutes, created_at,
    created_by, owner, updated_at, closed_at, closed_by_session, external_ref,
    spec_id, compaction_level, compacted_at, compacted_at_commit, original_size,
    sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type,
    source_system, metadata, source_repo, close_reason, event_kind, actor,
    target, payload, await_type, await_id, timeout_ns, waiters, hook_bead,
    role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until,
    no_history, started_at
FROM wisps
WHERE issue_type = 'rig';

UPDATE issues
SET ephemeral = 0
WHERE issue_type = 'rig'
  AND id IN (SELECT id FROM wisps WHERE issue_type = 'rig');

INSERT IGNORE INTO labels (issue_id, label)
SELECT wl.issue_id, wl.label
FROM wisp_labels wl
JOIN wisps w ON w.id = wl.issue_id
WHERE w.issue_type = 'rig';

INSERT IGNORE INTO dependencies (
    id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
    type, created_at, created_by, metadata, thread_id
)
SELECT
    CONCAT(
        SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 1, 8),
        '-',
        SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 9, 4),
        '-',
        SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 13, 4),
        '-',
        SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 17, 4),
        '-',
        SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 21, 12)
    ),
    wd.issue_id, wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external,
    wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id
FROM wisp_dependencies wd
JOIN wisps w ON w.id = wd.issue_id
WHERE w.issue_type = 'rig';

INSERT IGNORE INTO events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
SELECT we.id, we.issue_id, we.event_type, we.actor, we.old_value, we.new_value, we.comment, we.created_at
FROM wisp_events we
JOIN wisps w ON w.id = we.issue_id
WHERE w.issue_type = 'rig';

INSERT IGNORE INTO comments (id, issue_id, author, text, created_at)
SELECT wc.id, wc.issue_id, wc.author, wc.text, wc.created_at
FROM wisp_comments wc
JOIN wisps w ON w.id = wc.issue_id
WHERE w.issue_type = 'rig';

CREATE TABLE IF NOT EXISTS wisp_child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0
);

INSERT IGNORE INTO child_counters (parent_id, last_child)
SELECT wcc.parent_id, wcc.last_child
FROM wisp_child_counters wcc
JOIN wisps w ON w.id = wcc.parent_id
WHERE w.issue_type = 'rig';

UPDATE child_counters cc
JOIN wisp_child_counters wcc ON wcc.parent_id = cc.parent_id
JOIN wisps w ON w.id = wcc.parent_id
SET cc.last_child = GREATEST(cc.last_child, wcc.last_child)
WHERE w.issue_type = 'rig';

DELETE wd FROM wisp_dependencies wd
JOIN wisps w ON w.id = wd.issue_id
WHERE w.issue_type = 'rig';

REPLACE INTO dependencies (
    id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
    type, created_at, created_by, metadata, thread_id
)
SELECT
    d.id, d.issue_id, d.depends_on_wisp_id, NULL, d.depends_on_external,
    d.type, d.created_at, d.created_by, d.metadata, d.thread_id
FROM dependencies d
JOIN wisps w ON w.id = d.depends_on_wisp_id
WHERE w.issue_type = 'rig';

REPLACE INTO wisp_dependencies (
    issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
    type, created_at, created_by, metadata, thread_id
)
SELECT
    wd.issue_id, wd.depends_on_wisp_id, NULL, wd.depends_on_external,
    wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id
FROM wisp_dependencies wd
JOIN wisps w ON w.id = wd.depends_on_wisp_id
WHERE w.issue_type = 'rig';

DELETE wl FROM wisp_labels wl
JOIN wisps w ON w.id = wl.issue_id
WHERE w.issue_type = 'rig';

DELETE we FROM wisp_events we
JOIN wisps w ON w.id = we.issue_id
WHERE w.issue_type = 'rig';

DELETE wc FROM wisp_comments wc
JOIN wisps w ON w.id = wc.issue_id
WHERE w.issue_type = 'rig';

DELETE wcc FROM wisp_child_counters wcc
JOIN wisps w ON w.id = wcc.parent_id
WHERE w.issue_type = 'rig';

DELETE FROM wisps WHERE issue_type = 'rig';

SET FOREIGN_KEY_CHECKS = 1;`
