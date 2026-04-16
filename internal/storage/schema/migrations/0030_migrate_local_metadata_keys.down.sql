-- Reverse migration 0030: copy keys back from local_metadata to committed tables.
-- Note: local_metadata may have been recreated empty, so this is best-effort.
INSERT IGNORE INTO metadata (`key`, value)
    SELECT `key`, value FROM local_metadata WHERE `key` LIKE 'tip\_%' ESCAPE '\\';
INSERT IGNORE INTO metadata (`key`, value)
    SELECT `key`, value FROM local_metadata WHERE `key` IN ('bd_version', 'bd_version_max');
INSERT IGNORE INTO config (`key`, value)
    SELECT `key`, value FROM local_metadata WHERE `key` LIKE '%.last\_sync' ESCAPE '\\';
