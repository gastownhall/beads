-- Storage-class plumbing (Protocol v0.1 §C, bd-7vb13 / bd-8rifr).
--
-- storage_class declares a record's history/replication contract:
-- 'versioned' | 'unversioned' | 'ephemeral'. NULL means unset, which
-- resolves per C1.2: ephemeral for wisp-plane rows, versioned otherwise
-- (types.Issue.EffectiveStorageClass). NULL-as-default deliberately avoids
-- rewriting every existing row's cell (a full prolly-tree churn of exactly
-- the kind this epic exists to remove) and mirrors the JSONL rule that the
-- field is omitted when versioned (C2.4).
--
-- The column is a class MARKER only: this migration changes no behavior.
-- ephemeral rows continue to live in the wisps plane; the unversioned
-- replication leg is bd-1quyq; the events/comments migration is bd-red8u.
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back (see 0052/0046/0054 precedent).

-- issues.storage_class
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'storage_class'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN storage_class VARCHAR(16)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.storage_class: wisps shares the issues row shape and the shared
-- scans select the same column list from both tables. Guarded on the wisps
-- table existing (older workspaces created issues-only; 0054 precedent).
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'storage_class') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN storage_class VARCHAR(16)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
