-- Add a (issue_id, created_at, id) composite index to comments and wisp_comments
-- so the keyset comment-page read (GetIssueCommentsPage) seeks the index for a
-- single issue's thread in (created_at, id) order instead of scanning the issue's
-- comments and sorting. The pre-existing single-column idx_comments_issue /
-- idx_wisp_comments_issue indexes can seek issue_id but leave the created_at
-- range and the id tie-break to a filter+sort.
--
-- Numbered 0056: the next contiguous version after main's 0055 (the migration
-- loader requires gap-free versions — COUNT == MAX, enforced by
-- TestAllMigrationsSQLAppliesThroughDoltCLIAndRecordsLatestVersion — so we
-- cannot pre-emptively skip to 0057). Several in-flight branches (claim-fence,
-- holder-token, revision) also add an 0056 on their own 0055 base; the loader
-- panics on a duplicate version (schema.go checkNoDuplicateVersions), so
-- whichever lands second rebases onto the new main and bumps to the next free
-- number. That collision is resolved loudly at merge, never silently.
--
-- Guarded against the current schema so the migration is idempotent (a clone may
-- re-apply it; see 0052) — it must not error when the index is already present.
--
-- The wisp half is additionally guarded on the wisp_comments table EXISTING:
-- wisp_% tables are dolt-ignored clone-local state that the ignored-migration
-- chain materializes AFTER the main migrations run, so on a fresh clone's first
-- writable open wisp_comments does not yet exist when this migration runs.
-- Treat that as a no-op (exactly like 0035/0037/0053) — the fresh wisp_comments
-- is created with this composite index directly by the canonical wisp schema in
-- ignored/0001_create_local_state_tables.up.sql, while this migration upgrades
-- the wisp_comments of already-materialized (existing) workspaces.

-- comments is a durable table present in every workspace once 0004 has run, so
-- it needs only the index-missing guard.
SET @needs_c = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'comments'
      AND INDEX_NAME = 'idx_comments_issue_created_id'
);
SET @sql = IF(@needs_c = 1, 'CREATE INDEX idx_comments_issue_created_id ON comments (issue_id, created_at, id)', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisp_comments may not exist yet on a fresh clone: guard on table existence AND
-- index-missing so the CREATE INDEX (and its PREPARE) is skipped entirely when
-- the table is absent.
SET @has_wisp_comments = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
);
SET @has_wisp_index = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND INDEX_NAME = 'idx_wisp_comments_issue_created_id'
);
SET @sql = IF(@has_wisp_comments > 0 AND @has_wisp_index = 0, 'CREATE INDEX idx_wisp_comments_issue_created_id ON wisp_comments (issue_id, created_at, id)', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
