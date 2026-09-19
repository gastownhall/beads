-- Ignored migration 0016: record the granting replica on every lease
-- (wy-jpd3.7).
--
-- A lease is only meaningful on the replica that granted it. Every other
-- replica's view of the holder's liveness is stale by up to one sync
-- interval, so a reaper running there can revert a unit that is very much
-- alive on the machine that granted its lease. granted_node names that
-- machine (config.NodeID()) so reclaim can refuse a positively-foreign lease
-- instead of relying on the operator to partition every reaper by hand.
--
-- Lives on the IGNORED track because leases does: the table is dolt_ignored
-- (never replicated, clone-local), materialized by ignored/0012 on fresh
-- clones and by synced 0055 on in-place upgrades. Same shape as
-- ignored/0013: guarded so it is a no-op on a workspace that already carries
-- the column and on one with no leases table yet.
--
-- Empty string is the "provenance unknown" value, and DEFAULT '' is what
-- pre-existing lease rows adopt. Reclaim treats unknown as local (fail-open)
-- so this upgrade can never strand a stale lease the reaper could previously
-- recover; a heartbeat re-stamps an unknown row with the local node.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'leases') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'leases'
          AND COLUMN_NAME = 'granted_node') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE leases ADD COLUMN granted_node VARCHAR(255) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
