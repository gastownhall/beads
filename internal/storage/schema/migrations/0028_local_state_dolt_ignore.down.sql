-- Reverse migration 0028: remove dolt_ignore entries for local_metadata and repo_mtimes.
-- Note: repo_mtimes data in the working set will be lost; the committed table
-- would need to be re-created manually if a full rollback is needed.
DELETE FROM dolt_ignore WHERE pattern IN ('local_metadata', 'repo_mtimes');
