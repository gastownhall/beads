-- Migration 0030: Migrate clone-local keys from committed tables to local_metadata.
--
-- Copies tip timestamps, version stamps, and tracker sync cursors from the
-- committed metadata/config tables into the dolt-ignored local_metadata table,
-- then deletes the originals so they no longer generate merge conflicts.
--
-- This is best-effort: local_metadata is ephemeral (recreated empty on working-set
-- reset), so all readers must handle "key not found" as the normal case. The copy
-- here just provides a smooth upgrade experience.

-- Copy tip display timestamps
INSERT IGNORE INTO local_metadata (`key`, value)
    SELECT `key`, value FROM metadata WHERE `key` LIKE 'tip\_%' ESCAPE '\\';

-- Copy version stamps
INSERT IGNORE INTO local_metadata (`key`, value)
    SELECT `key`, value FROM metadata WHERE `key` IN ('bd_version', 'bd_version_max');

-- Copy tracker sync cursors
INSERT IGNORE INTO local_metadata (`key`, value)
    SELECT `key`, value FROM config WHERE `key` LIKE '%.last\_sync' ESCAPE '\\';

-- Remove migrated keys from committed tables.
-- The post-migration commit in initSchemaOnDB stages and commits these deletions.
DELETE FROM metadata WHERE `key` LIKE 'tip\_%' ESCAPE '\\';
DELETE FROM metadata WHERE `key` IN ('bd_version', 'bd_version_max');
DELETE FROM config WHERE `key` LIKE '%.last\_sync' ESCAPE '\\';
