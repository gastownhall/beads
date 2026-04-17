-- Migration 0028: Move clone-local state to dolt-ignored tables.
--
-- repo_mtimes and local_metadata contain clone-local state that generates
-- Dolt merge conflicts when two clones independently update the same rows.
-- Moving them to dolt_ignore eliminates these conflicts.
--
-- repo_mtimes is an existing committed table that must be dropped, ignored,
-- and recreated. local_metadata is new and just needs an ignore entry before
-- creation (handled in migration 0029).

-- Phase 1: Preserve repo_mtimes data in a temp table.
CREATE TABLE IF NOT EXISTS repo_mtimes_tmp (
    repo_path VARCHAR(512) PRIMARY KEY,
    jsonl_path VARCHAR(512) NOT NULL,
    mtime_ns BIGINT NOT NULL,
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_repo_mtimes_tmp_checked (last_checked)
);
INSERT IGNORE INTO repo_mtimes_tmp SELECT * FROM repo_mtimes;

-- Phase 2: Drop the committed repo_mtimes and register dolt_ignore patterns.
-- dolt_ignore entries must be committed BEFORE creating ignored tables.
DROP TABLE IF EXISTS repo_mtimes;
REPLACE INTO dolt_ignore VALUES ('local_metadata', true);
REPLACE INTO dolt_ignore VALUES ('repo_mtimes', true);
CALL DOLT_ADD('repo_mtimes', 'dolt_ignore');
CALL DOLT_COMMIT('-m', 'chore: move repo_mtimes and local_metadata to dolt_ignore');

-- Phase 3: Recreate repo_mtimes in the working set (now dolt-ignored).
CREATE TABLE IF NOT EXISTS repo_mtimes (
    repo_path VARCHAR(512) PRIMARY KEY,
    jsonl_path VARCHAR(512) NOT NULL,
    mtime_ns BIGINT NOT NULL,
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_repo_mtimes_checked (last_checked)
);

-- Phase 4: Restore data and clean up temp table.
-- repo_mtimes_tmp was never committed, so dropping it leaves no tracked changes.
INSERT IGNORE INTO repo_mtimes SELECT * FROM repo_mtimes_tmp;
DROP TABLE IF EXISTS repo_mtimes_tmp;
