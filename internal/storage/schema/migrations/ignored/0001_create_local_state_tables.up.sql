DROP TABLE IF EXISTS __temp__wisps;
CREATE TABLE __temp__wisps (
    id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64),
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    design TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255),
    estimated_minutes INT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    owner VARCHAR(255) DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session VARCHAR(255) DEFAULT '',
    external_ref VARCHAR(255),
    spec_id VARCHAR(1024),
    compaction_level INT DEFAULT 0,
    compacted_at DATETIME,
    compacted_at_commit VARCHAR(64),
    original_size INT,
    sender VARCHAR(255) DEFAULT '',
    ephemeral TINYINT(1) DEFAULT 0,
    wisp_type VARCHAR(32) DEFAULT '',
    pinned TINYINT(1) DEFAULT 0,
    is_template TINYINT(1) DEFAULT 0,
    mol_type VARCHAR(32) DEFAULT '',
    work_type VARCHAR(32) DEFAULT 'mutex',
    source_system VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    source_repo VARCHAR(512) DEFAULT '',
    close_reason TEXT DEFAULT '',
    event_kind VARCHAR(32) DEFAULT '',
    actor VARCHAR(255) DEFAULT '',
    target VARCHAR(255) DEFAULT '',
    payload TEXT DEFAULT '',
    await_type VARCHAR(32) DEFAULT '',
    await_id VARCHAR(255) DEFAULT '',
    timeout_ns BIGINT DEFAULT 0,
    waiters TEXT DEFAULT '',
    hook_bead VARCHAR(255) DEFAULT '',
    role_bead VARCHAR(255) DEFAULT '',
    agent_state VARCHAR(32) DEFAULT '',
    last_activity DATETIME,
    role_type VARCHAR(32) DEFAULT '',
    rig VARCHAR(255) DEFAULT '',
    due_at DATETIME,
    defer_until DATETIME,
    no_history TINYINT(1) DEFAULT 0,
    started_at DATETIME,
    INDEX idx_wisps_status (status),
    INDEX idx_wisps_priority (priority),
    INDEX idx_wisps_issue_type (issue_type),
    INDEX idx_wisps_assignee (assignee),
    INDEX idx_wisps_created_at (created_at),
    INDEX idx_wisps_spec_id (spec_id),
    INDEX idx_wisps_external_ref (external_ref)
);

DROP TABLE IF EXISTS __temp__wisp_labels;
CREATE TABLE __temp__wisp_labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label),
    INDEX idx_wisp_labels_label (label)
);

DROP TABLE IF EXISTS __temp__wisp_dependencies;
CREATE TABLE __temp__wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    PRIMARY KEY (issue_id, depends_on_id),
    INDEX idx_wisp_dep_depends (depends_on_id),
    INDEX idx_wisp_dep_type (type),
    INDEX idx_wisp_dep_type_depends (type, depends_on_id)
);

DROP TABLE IF EXISTS __temp__wisp_events;
CREATE TABLE __temp__wisp_events (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) DEFAULT '',
    old_value TEXT DEFAULT '',
    new_value TEXT DEFAULT '',
    comment TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_events_issue (issue_id),
    INDEX idx_wisp_events_created_at (created_at)
);

DROP TABLE IF EXISTS __temp__wisp_comments;
CREATE TABLE __temp__wisp_comments (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    author VARCHAR(255) DEFAULT '',
    text TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_comments_issue (issue_id)
);

DROP TABLE IF EXISTS __temp__repo_mtimes;
CREATE TABLE __temp__repo_mtimes (
    repo_path VARCHAR(512) PRIMARY KEY,
    jsonl_path VARCHAR(512) NOT NULL,
    mtime_ns BIGINT NOT NULL,
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_repo_mtimes_checked (last_checked)
);

DROP TABLE IF EXISTS __temp__local_metadata;
CREATE TABLE __temp__local_metadata (
    `key` VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__wisps TO wisps', 'DROP TABLE __temp__wisps');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_labels');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__wisp_labels TO wisp_labels', 'DROP TABLE __temp__wisp_labels');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__wisp_dependencies TO wisp_dependencies', 'DROP TABLE __temp__wisp_dependencies');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_events');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__wisp_events TO wisp_events', 'DROP TABLE __temp__wisp_events');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__wisp_comments TO wisp_comments', 'DROP TABLE __temp__wisp_comments');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'repo_mtimes');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__repo_mtimes TO repo_mtimes', 'DROP TABLE __temp__repo_mtimes');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'local_metadata');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__local_metadata TO local_metadata', 'DROP TABLE __temp__local_metadata');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
