-- Work-ownership integrity: give every claim a fence.
--
--   claim_fence   a monotonic counter bumped on every OWNERSHIP TRANSITION of
--                 the row — claim; unclaim/release (including --force and the
--                 --if-assignee CAS form); lease reclaim; an assignee change
--                 through the generic update path or an import/upsert; and
--                 reopen (closed->open) through the reopen verb or the generic
--                 update path. There is no separate "transfer" transition: a
--                 transfer IS an assignee change. It is NOT bumped by content
--                 mutations (notes, metadata, close), unlike row_lock which
--                 every mutating path rewrites. An orchestrator that read
--                 (assignee, claim_fence) can therefore release or reassign a
--                 row guarded on exactly the ownership state it decided on,
--                 and a holder fenced out by a transition can be rejected with
--                 a typed conflict instead of silently stomping newer
--                 ownership.
--
-- Every successful claim CAS bumps, including a same-actor claim of an open
-- row already assigned to that actor: two sessions of one user are two
-- ownership holders, and the second session's claim must fence out the
-- first's snapshot. The idempotent same-actor re-claim of an already
-- in_progress row never reaches the CAS (its status is not claimable) and so
-- does not bump. See internal/storage/issueops/fence.go.
--
-- Import-reopen exception: the import/upsert path bumps on an assignee change
-- ALONE, so an import that flips a stored closed row back to open with the
-- SAME assignee does not bump, where the reopen verb does. Import is
-- convergence toward a peer's snapshot, not an ownership verb, and the fence
-- is a live-coordination token — a routine sync must not invalidate the
-- --if-fence guards live workers hold on this replica. Same reference.
--
-- Pairing invariant: every statement that bumps claim_fence also rewrites
-- row_lock — a monotonic cell alone cell-merges silently under Dolt (two
-- concurrent N->N+1 bumps write identical values), so the random row_lock is
-- what forces racing transitions to serialize. Enforced by
-- TestFenceBumpAlwaysPairsRowLock in issueops.
--
-- issues only. wisps is dolt_ignored, so its schema is clone-local and a
-- fresh clone never executes this migration (the schema_migrations cursor
-- arrives at-latest); the wisps column ships on the ignored track as
-- ignored/0019_add_wisp_claim_fence, the 0013_add_wisp_row_lock precedent.
--
-- Guarded so the migration is idempotent on a schema_migrations row that
-- regressed without its DDL rolled back (see 0052/0046).

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
