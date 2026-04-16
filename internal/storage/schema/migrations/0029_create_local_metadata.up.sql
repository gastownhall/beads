-- Migration 0029: Create the local_metadata table (dolt-ignored).
--
-- This table stores clone-local key-value state that should not be replicated
-- across Dolt clones: tip display timestamps, bd version stamps, tracker sync
-- cursors, etc. It is dolt-ignored (see migration 0028) and will be recreated
-- empty by EnsureIgnoredTables after server restart, branch checkout, or clone.
CREATE TABLE IF NOT EXISTS local_metadata (
    `key` VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
