-- Repair rig identity beads that earlier defaults routed into the ephemeral
-- wisps tier. Rig beads are durable issue state: keep type=rig hidden from
-- ready work in application code, but promote existing rows back to issues.
--
-- Main migrations can run before clone-local ignored migrations have
-- materialized wisp tables in older workspaces. Treat that state as a no-op:
-- there are no local rig wisps to repair yet, and ignored migrations will
-- create the local tables later in the same pass.

SET FOREIGN_KEY_CHECKS = 0;

SET @has_wisps = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
);
SET @has_wisp_labels = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_labels'
);
SET @has_wisp_dependencies = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
);
SET @has_wisp_dependencies_id = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME = 'id'
);
SET @has_wisp_events = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
);
SET @has_wisp_comments = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
);
SET @has_wisp_child_counters = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_child_counters'
);

SET @sql = IF(@has_wisps > 0,
    'INSERT IGNORE INTO issues (id, content_hash, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, estimated_minutes, created_at, created_by, owner, updated_at, closed_at, closed_by_session, external_ref, spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type, source_system, metadata, source_repo, close_reason, event_kind, actor, target, payload, await_type, await_id, timeout_ns, waiters, hook_bead, role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until, no_history, started_at) SELECT id, content_hash, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, estimated_minutes, created_at, created_by, owner, updated_at, closed_at, closed_by_session, external_ref, spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type, source_system, metadata, source_repo, close_reason, event_kind, actor, target, payload, await_type, await_id, timeout_ns, waiters, hook_bead, role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until, no_history, started_at FROM wisps WHERE issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0,
    'UPDATE issues SET ephemeral = 0 WHERE issue_type = ''rig'' AND id IN (SELECT id FROM wisps WHERE issue_type = ''rig'')',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_labels > 0,
    'INSERT IGNORE INTO labels (issue_id, label) SELECT wl.issue_id, wl.label FROM wisp_labels wl JOIN wisps w ON w.id = wl.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_dependencies > 0,
    IF(@has_wisp_dependencies_id > 0,
        'INSERT IGNORE INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT wd.id, wd.issue_id, wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external, wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id FROM wisp_dependencies wd JOIN wisps w ON w.id = wd.issue_id WHERE w.issue_type = ''rig''',
        'INSERT IGNORE INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT CONCAT(SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 1, 8), ''-'', SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 9, 4), ''-'', SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 13, 4), ''-'', SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 17, 4), ''-'', SUBSTR(MD5(CONCAT(wd.issue_id, CHAR(31), COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external))), 21, 12)), wd.issue_id, wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external, wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id FROM wisp_dependencies wd JOIN wisps w ON w.id = wd.issue_id WHERE w.issue_type = ''rig'''
    ),
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_events > 0,
    'INSERT IGNORE INTO events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at) SELECT we.id, we.issue_id, we.event_type, we.actor, we.old_value, we.new_value, we.comment, we.created_at FROM wisp_events we JOIN wisps w ON w.id = we.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_comments > 0,
    'INSERT IGNORE INTO comments (id, issue_id, author, text, created_at) SELECT wc.id, wc.issue_id, wc.author, wc.text, wc.created_at FROM wisp_comments wc JOIN wisps w ON w.id = wc.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_child_counters > 0,
    'INSERT IGNORE INTO child_counters (parent_id, last_child) SELECT wcc.parent_id, wcc.last_child FROM wisp_child_counters wcc JOIN wisps w ON w.id = wcc.parent_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_child_counters > 0,
    'UPDATE child_counters cc JOIN wisp_child_counters wcc ON wcc.parent_id = cc.parent_id JOIN wisps w ON w.id = wcc.parent_id SET cc.last_child = GREATEST(cc.last_child, wcc.last_child) WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_dependencies > 0,
    'DELETE wd FROM wisp_dependencies wd JOIN wisps w ON w.id = wd.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0,
    'REPLACE INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT d.id, d.issue_id, d.depends_on_wisp_id, NULL, d.depends_on_external, d.type, d.created_at, d.created_by, d.metadata, d.thread_id FROM dependencies d JOIN wisps w ON w.id = d.depends_on_wisp_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_dependencies > 0,
    IF(@has_wisp_dependencies_id > 0,
        'REPLACE INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT wd.id, wd.issue_id, wd.depends_on_wisp_id, NULL, wd.depends_on_external, wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id FROM wisp_dependencies wd JOIN wisps w ON w.id = wd.depends_on_wisp_id WHERE w.issue_type = ''rig''',
        'REPLACE INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT wd.issue_id, wd.depends_on_wisp_id, NULL, wd.depends_on_external, wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id FROM wisp_dependencies wd JOIN wisps w ON w.id = wd.depends_on_wisp_id WHERE w.issue_type = ''rig'''
    ),
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_labels > 0,
    'DELETE wl FROM wisp_labels wl JOIN wisps w ON w.id = wl.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_events > 0,
    'DELETE we FROM wisp_events we JOIN wisps w ON w.id = we.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_comments > 0,
    'DELETE wc FROM wisp_comments wc JOIN wisps w ON w.id = wc.issue_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0 AND @has_wisp_child_counters > 0,
    'DELETE wcc FROM wisp_child_counters wcc JOIN wisps w ON w.id = wcc.parent_id WHERE w.issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 0,
    'DELETE FROM wisps WHERE issue_type = ''rig''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
