CREATE TABLE IF NOT EXISTS `issues` (
  `id` varchar(255) NOT NULL,
  `content_hash` varchar(64),
  `title` varchar(500) NOT NULL,
  `description` longtext NOT NULL,
  `design` longtext NOT NULL,
  `acceptance_criteria` longtext NOT NULL,
  `notes` longtext NOT NULL,
  `status` varchar(32) NOT NULL DEFAULT 'open',
  `priority` int NOT NULL DEFAULT '2',
  `issue_type` varchar(32) NOT NULL DEFAULT 'task',
  `assignee` varchar(255),
  `estimated_minutes` int,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `created_by` varchar(255) DEFAULT '',
  `owner` varchar(255) DEFAULT '',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ,
  `closed_at` datetime,
  `closed_by_session` varchar(255) DEFAULT '',
  `external_ref` varchar(255),
  `spec_id` varchar(1024),
  `compaction_level` int DEFAULT '0',
  `compacted_at` datetime,
  `compacted_at_commit` varchar(64),
  `original_size` int,
  `sender` varchar(255) DEFAULT '',
  `ephemeral` tinyint(1) DEFAULT '0',
  `wisp_type` varchar(32) DEFAULT '',
  `pinned` tinyint(1) DEFAULT '0',
  `is_template` tinyint(1) DEFAULT '0',
  `mol_type` varchar(32) DEFAULT '',
  `work_type` varchar(32) DEFAULT 'mutex',
  `source_system` varchar(255) DEFAULT '',
  `metadata` text DEFAULT '{}',
  `source_repo` varchar(512) DEFAULT '',
  `close_reason` longtext DEFAULT (''),
  `event_kind` varchar(32) DEFAULT '',
  `actor` varchar(255) DEFAULT '',
  `target` varchar(255) DEFAULT '',
  `payload` text DEFAULT (''),
  `await_type` varchar(32) DEFAULT '',
  `await_id` varchar(255) DEFAULT '',
  `timeout_ns` bigint DEFAULT '0',
  `waiters` text DEFAULT (''),
  `hook_bead` varchar(255) DEFAULT '',
  `role_bead` varchar(255) DEFAULT '',
  `agent_state` varchar(32) DEFAULT '',
  `last_activity` datetime,
  `role_type` varchar(32) DEFAULT '',
  `rig` varchar(255) DEFAULT '',
  `due_at` datetime,
  `defer_until` datetime,
  `no_history` tinyint(1) DEFAULT '0',
  `started_at` datetime,
  `is_blocked` tinyint(1) NOT NULL DEFAULT '0',
  `lease_expires_at` datetime,
  `heartbeat_at` datetime,
  `row_lock` bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
);

CREATE TABLE IF NOT EXISTS `wisps` (
  `id` varchar(255) NOT NULL,
  `content_hash` varchar(64),
  `title` varchar(500) NOT NULL,
  `description` longtext NOT NULL DEFAULT (''),
  `design` longtext NOT NULL DEFAULT (''),
  `acceptance_criteria` longtext NOT NULL DEFAULT (''),
  `notes` longtext NOT NULL DEFAULT (''),
  `status` varchar(32) NOT NULL DEFAULT 'open',
  `priority` int NOT NULL DEFAULT '2',
  `issue_type` varchar(32) NOT NULL DEFAULT 'task',
  `assignee` varchar(255),
  `estimated_minutes` int,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `created_by` varchar(255) DEFAULT '',
  `owner` varchar(255) DEFAULT '',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ,
  `closed_at` datetime,
  `closed_by_session` varchar(255) DEFAULT '',
  `external_ref` varchar(255),
  `spec_id` varchar(1024),
  `compaction_level` int DEFAULT '0',
  `compacted_at` datetime,
  `compacted_at_commit` varchar(64),
  `original_size` int,
  `sender` varchar(255) DEFAULT '',
  `ephemeral` tinyint(1) DEFAULT '0',
  `wisp_type` varchar(32) DEFAULT '',
  `pinned` tinyint(1) DEFAULT '0',
  `is_template` tinyint(1) DEFAULT '0',
  `mol_type` varchar(32) DEFAULT '',
  `work_type` varchar(32) DEFAULT 'mutex',
  `source_system` varchar(255) DEFAULT '',
  `metadata` text DEFAULT '{}',
  `source_repo` varchar(512) DEFAULT '',
  `close_reason` longtext DEFAULT (''),
  `event_kind` varchar(32) DEFAULT '',
  `actor` varchar(255) DEFAULT '',
  `target` varchar(255) DEFAULT '',
  `payload` text DEFAULT (''),
  `await_type` varchar(32) DEFAULT '',
  `await_id` varchar(255) DEFAULT '',
  `timeout_ns` bigint DEFAULT '0',
  `waiters` text DEFAULT (''),
  `hook_bead` varchar(255) DEFAULT '',
  `role_bead` varchar(255) DEFAULT '',
  `agent_state` varchar(32) DEFAULT '',
  `last_activity` datetime,
  `role_type` varchar(32) DEFAULT '',
  `rig` varchar(255) DEFAULT '',
  `due_at` datetime,
  `defer_until` datetime,
  `no_history` tinyint(1) DEFAULT '0',
  `started_at` datetime,
  `is_blocked` tinyint(1) NOT NULL DEFAULT '0',
  `lease_expires_at` datetime,
  `heartbeat_at` datetime,
  `row_lock` bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
);

CREATE TABLE IF NOT EXISTS `labels` (
  `issue_id` varchar(255) NOT NULL,
  `label` varchar(255) NOT NULL,
  PRIMARY KEY (`issue_id`,`label`),
  CONSTRAINT `fk_labels_issue` FOREIGN KEY (`issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `wisp_labels` (
  `issue_id` varchar(255) NOT NULL,
  `label` varchar(255) NOT NULL,
  PRIMARY KEY (`issue_id`,`label`),
  CONSTRAINT `fk_wisp_labels_issue` FOREIGN KEY (`issue_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `dependencies` (
  `id` char(36) NOT NULL,
  `issue_id` varchar(255) NOT NULL,
  `type` varchar(32) NOT NULL DEFAULT 'blocks',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `created_by` varchar(255) NOT NULL,
  `metadata` text DEFAULT '{}',
  `thread_id` varchar(255) DEFAULT '',
  `depends_on_issue_id` varchar(255),
  `depends_on_wisp_id` varchar(255),
  `depends_on_external` varchar(255),
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_dep_issue` FOREIGN KEY (`issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `fk_dep_issue_target` FOREIGN KEY (`depends_on_issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `wisp_dependencies` (
  `id` char(36) NOT NULL,
  `issue_id` varchar(255) NOT NULL,
  `depends_on_issue_id` varchar(255),
  `depends_on_wisp_id` varchar(255),
  `depends_on_external` varchar(255),
  `type` varchar(32) NOT NULL DEFAULT 'blocks',
  `created_at` datetime DEFAULT CURRENT_TIMESTAMP,
  `created_by` varchar(255) DEFAULT '',
  `metadata` text DEFAULT '{}',
  `thread_id` varchar(255) DEFAULT '',
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_wisp_dep_issue` FOREIGN KEY (`issue_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `fk_wisp_dep_issue_target` FOREIGN KEY (`depends_on_issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `fk_wisp_dep_wisp_target` FOREIGN KEY (`depends_on_wisp_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `comments` (
  `id` char(36) NOT NULL,
  `issue_id` varchar(255) NOT NULL,
  `author` varchar(255) NOT NULL,
  `text` longtext NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_comments_issue` FOREIGN KEY (`issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `wisp_comments` (
  `id` char(36) NOT NULL,
  `issue_id` varchar(255) NOT NULL,
  `author` varchar(255) DEFAULT '',
  `text` text NOT NULL,
  `created_at` datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_wisp_comments_issue` FOREIGN KEY (`issue_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `events` (
  `id` char(36) NOT NULL,
  `issue_id` varchar(255) NOT NULL,
  `event_type` varchar(32) NOT NULL,
  `actor` varchar(255) NOT NULL,
  `old_value` longtext,
  `new_value` longtext,
  `comment` text,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_events_issue` FOREIGN KEY (`issue_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `wisp_events` (
  `id` char(36) NOT NULL,
  `issue_id` varchar(255) NOT NULL,
  `event_type` varchar(32) NOT NULL,
  `actor` varchar(255) DEFAULT '',
  `old_value` longtext DEFAULT (''),
  `new_value` longtext DEFAULT (''),
  `comment` text DEFAULT (''),
  `created_at` datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_wisp_events_issue` FOREIGN KEY (`issue_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `config` (
  `key` varchar(255) NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`key`)
);

CREATE TABLE IF NOT EXISTS `metadata` (
  `key` varchar(255) NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`key`)
);

CREATE TABLE IF NOT EXISTS `local_metadata` (
  `key` varchar(255) NOT NULL,
  `value` text NOT NULL DEFAULT (''),
  PRIMARY KEY (`key`)
);

CREATE TABLE IF NOT EXISTS `issue_counter` (
  `prefix` varchar(255) NOT NULL,
  `last_id` int NOT NULL DEFAULT '0',
  PRIMARY KEY (`prefix`)
);

CREATE TABLE IF NOT EXISTS `child_counters` (
  `parent_id` varchar(255) NOT NULL,
  `last_child` int NOT NULL DEFAULT '0',
  PRIMARY KEY (`parent_id`),
  CONSTRAINT `fk_counter_parent` FOREIGN KEY (`parent_id`) REFERENCES `issues` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `wisp_child_counters` (
  `parent_id` varchar(255) NOT NULL,
  `last_child` int NOT NULL DEFAULT '0',
  PRIMARY KEY (`parent_id`),
  CONSTRAINT `fk_wisp_child_counters_parent` FOREIGN KEY (`parent_id`) REFERENCES `wisps` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS `custom_statuses` (
  `name` varchar(64) NOT NULL,
  `category` varchar(32) NOT NULL DEFAULT 'unspecified',
  PRIMARY KEY (`name`)
);

CREATE TABLE IF NOT EXISTS `custom_types` (
  `name` varchar(64) NOT NULL,
  PRIMARY KEY (`name`)
);

CREATE TABLE IF NOT EXISTS `repo_mtimes` (
  `repo_path` varchar(512) NOT NULL,
  `jsonl_path` varchar(512) NOT NULL,
  `mtime_ns` bigint NOT NULL,
  `last_checked` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`repo_path`)
);

CREATE INDEX IF NOT EXISTS idx_issues_assignee ON `issues` (`assignee`);
CREATE INDEX IF NOT EXISTS idx_issues_created_at ON `issues` (`created_at`);
CREATE INDEX IF NOT EXISTS idx_issues_defer_until ON `issues` (`defer_until`);
CREATE INDEX IF NOT EXISTS idx_issues_external_ref ON `issues` (`external_ref`);
CREATE INDEX IF NOT EXISTS idx_issues_is_blocked ON `issues` (`is_blocked`,`status`);
CREATE INDEX IF NOT EXISTS idx_issues_issue_type ON `issues` (`issue_type`);
CREATE INDEX IF NOT EXISTS idx_issues_priority ON `issues` (`priority`);
CREATE INDEX IF NOT EXISTS idx_issues_spec_id ON `issues` (`spec_id`);
CREATE INDEX IF NOT EXISTS idx_issues_status_updated_at ON `issues` (`status`,`updated_at`);
CREATE INDEX IF NOT EXISTS idx_issues_lease ON `issues` (`status`,`lease_expires_at`);
CREATE INDEX IF NOT EXISTS idx_wisps_assignee ON `wisps` (`assignee`);
CREATE INDEX IF NOT EXISTS idx_wisps_created_at ON `wisps` (`created_at`);
CREATE INDEX IF NOT EXISTS idx_wisps_external_ref ON `wisps` (`external_ref`);
CREATE INDEX IF NOT EXISTS idx_wisps_is_blocked ON `wisps` (`is_blocked`,`status`);
CREATE INDEX IF NOT EXISTS idx_wisps_issue_type ON `wisps` (`issue_type`);
CREATE INDEX IF NOT EXISTS idx_wisps_priority ON `wisps` (`priority`);
CREATE INDEX IF NOT EXISTS idx_wisps_spec_id ON `wisps` (`spec_id`);
CREATE INDEX IF NOT EXISTS idx_wisps_status ON `wisps` (`status`);
CREATE INDEX IF NOT EXISTS idx_labels_label ON `labels` (`label`);
CREATE INDEX IF NOT EXISTS idx_wisp_labels_label ON `wisp_labels` (`label`);
CREATE INDEX IF NOT EXISTS idx_dep_external_target ON `dependencies` (`depends_on_external`);
CREATE INDEX IF NOT EXISTS idx_dep_issue_target ON `dependencies` (`depends_on_issue_id`);
CREATE INDEX IF NOT EXISTS idx_dep_type_external ON `dependencies` (`type`,`depends_on_external`);
CREATE INDEX IF NOT EXISTS idx_dep_type_issue ON `dependencies` (`type`,`depends_on_issue_id`);
CREATE INDEX IF NOT EXISTS idx_dep_type_wisp ON `dependencies` (`type`,`depends_on_wisp_id`);
CREATE INDEX IF NOT EXISTS idx_dep_wisp_target ON `dependencies` (`depends_on_wisp_id`);
CREATE INDEX IF NOT EXISTS idx_dependencies_issue ON `dependencies` (`issue_id`);
CREATE INDEX IF NOT EXISTS idx_dependencies_thread ON `dependencies` (`thread_id`);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_external_target ON `dependencies` (`issue_id`,`depends_on_external`);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_issue_target ON `dependencies` (`issue_id`,`depends_on_issue_id`);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_wisp_target ON `dependencies` (`issue_id`,`depends_on_wisp_id`);
CREATE INDEX IF NOT EXISTS fk_wisp_dep_issue_target ON `wisp_dependencies` (`depends_on_issue_id`);
CREATE INDEX IF NOT EXISTS fk_wisp_dep_wisp_target ON `wisp_dependencies` (`depends_on_wisp_id`);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type ON `wisp_dependencies` (`type`);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_external ON `wisp_dependencies` (`type`,`depends_on_external`);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_issue ON `wisp_dependencies` (`type`,`depends_on_issue_id`);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_wisp ON `wisp_dependencies` (`type`,`depends_on_wisp_id`);
CREATE UNIQUE INDEX IF NOT EXISTS uk_wisp_dep_external_target ON `wisp_dependencies` (`issue_id`,`depends_on_external`);
CREATE UNIQUE INDEX IF NOT EXISTS uk_wisp_dep_issue_target ON `wisp_dependencies` (`issue_id`,`depends_on_issue_id`);
CREATE UNIQUE INDEX IF NOT EXISTS uk_wisp_dep_wisp_target ON `wisp_dependencies` (`issue_id`,`depends_on_wisp_id`);
CREATE INDEX IF NOT EXISTS idx_comments_created_at ON `comments` (`created_at`);
CREATE INDEX IF NOT EXISTS idx_comments_issue ON `comments` (`issue_id`);
CREATE INDEX IF NOT EXISTS idx_wisp_comments_issue ON `wisp_comments` (`issue_id`);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON `events` (`created_at`);
CREATE INDEX IF NOT EXISTS idx_events_issue ON `events` (`issue_id`);
CREATE INDEX IF NOT EXISTS idx_wisp_events_created_at ON `wisp_events` (`created_at`);
CREATE INDEX IF NOT EXISTS idx_wisp_events_issue ON `wisp_events` (`issue_id`);
CREATE INDEX IF NOT EXISTS idx_repo_mtimes_checked ON `repo_mtimes` (`last_checked`);
