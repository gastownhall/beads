-- Work-ownership integrity (ga-furrj5, A-B1): give every claim a fence.
--
--   claim_fence   a monotonic counter bumped on every OWNERSHIP TRANSITION of
--                 the row — claim, unclaim/release, lease reclaim, assignee
--                 change, reopen (closed→open), transfer. It is NOT bumped by
--                 content mutations (notes, metadata, close), unlike row_lock
--                 which every mutating path rewrites. An orchestrator that
--                 read (assignee, claim_fence) can therefore release or
--                 transfer a row guarded on exactly the ownership state it
--                 decided on, and a holder fenced out by a transition can be
--                 rejected with a typed conflict instead of silently stomping
--                 newer ownership.
--
-- Pairing invariant: every statement that bumps claim_fence also rewrites
-- row_lock — a monotonic cell alone cell-merges silently under Dolt (two
-- concurrent N→N+1 bumps write identical values), so the random row_lock is
-- what forces racing transitions to serialize. Enforced by
-- TestFenceBumpAlwaysPairsRowLock in issueops.
--
-- Both tables carry the column: wisps hold Gas City's durable no_history
-- workflow rows, and ownership integrity is tier-complete by design
-- (engdocs/plans/ownership-fencing/DESIGN.md in gascity).
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back (see 0052/0046).

-- issues.claim_fence
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'claim_fence'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN claim_fence BIGINT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.claim_fence (guarded on the wisps table existing at all — older
-- workspaces created issues-only; pattern matches 0054's wisps section)
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);

SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'claim_fence') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN claim_fence BIGINT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
