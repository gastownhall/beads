-- Ignored migration 0029: ensure wisps.arrival_seq and its unique index exist
-- on every clone.
--
-- Synced migration 0073 adds arrival_seq to issues and wisps -- but wisps is
-- dolt-ignored (migration 0019), so its schema is clone-local, and a workspace
-- that bootstraps or re-clones from a remote whose schema_migrations cursor is
-- already >= 0073 adopts the cursor without ever executing 0073. Its wisps
-- table (materialized by ignored/0001, which predates arrival_seq) would then
-- permanently lack the column, and every shared wisp scan would fail. Same
-- mechanism, same shape, same fix as ignored/0013 (wisps.row_lock), ignored/0020
-- (wisps.storage_class) and ignored/0027 (wisps.current_revision).
--
-- Unlike those, wisps is the plane where this column MEANS something: it holds
-- the events-journal seq of the row's create record (see 0073's header). The
-- UNIQUE index is what makes a value impossible to hand out twice: a counter
-- healed behind it fails the stamp loudly instead of minting a duplicate, and
-- the writer's heal (issueops.healEventSeqCounter) raises the counter past
-- MAX(arrival_seq) before retrying. NULLs are distinct in a unique index, so
-- every unstamped row coexists. The index is here because wisps is clone-local
-- on every clone, upgraded or fresh, and this file is the one both doors run.
-- 0073 creates it as well, under the same guard, for the door this file
-- cannot cover: 0073 rolled back and applied again while this file stays
-- recorded.
--
-- The column and index steps are guarded: a no-op on in-place-upgraded
-- workspaces where synced 0073 already added the column, on workspaces with no
-- local wisps table yet, and on a re-run where the index already exists.
-- Definition mirrors 0073 exactly: BIGINT, nullable, no default. The third
-- step, the seq counter raise, is explained beside it.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'arrival_seq') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND INDEX_NAME = 'uk_wisps_arrival_seq') = 0,
    1, 0
);
SET @sql = IF(@needs_index = 1,
    'CREATE UNIQUE INDEX uk_wisps_arrival_seq ON wisps (arrival_seq)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Raise the seq counter past every surviving stamp. A stamp IS a journal seq
-- and it outlives its record once the journal is pruned, so the counter must
-- never hand out a value at or below MAX(arrival_seq). ignored/0022 seeds
-- bd_events_seq from the journal's MAX(seq) alone, and it re-runs whenever the
-- cursor's sentinel floor replays the ignored series because bd_events_seq went
-- missing (replayFloor 21). With the journal pruned past every record that
-- re-seed lands at 0 while stamps survive, and the low values the swept or
-- deleted wisps held are free: the next create would take one without
-- colliding, so the writer's collision heal never fires, and a new row would
-- sort before older surviving rows. This file re-runs in that same replay,
-- after 0022, so raising the counter here closes the gap. GREATEST never moves
-- the counter backwards, so on an in-place upgrade (every stamp NULL), on a
-- fresh clone (no wisps) and on a healthy re-run it changes nothing. Guarded on
-- the counter table and the stamp column both existing.
SET @needs_raise = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_seq') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'arrival_seq') > 0,
    1, 0
);
SET @sql = IF(@needs_raise = 1,
    'UPDATE bd_events_seq SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(arrival_seq) FROM wisps), 0)) WHERE id = 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
