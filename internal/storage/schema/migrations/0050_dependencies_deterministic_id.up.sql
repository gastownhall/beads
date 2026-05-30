-- Migration 0050: make dependencies.id a clone-stable, merge-safe primary key.
--
-- gastownhall/beads#4259. Migration 0043 gave the dependencies table a surrogate
-- primary key `id CHAR(36) NOT NULL DEFAULT (UUID())`. UUID() is per-clone-random,
-- so two clones that create the same logical edge -- or that apply 0043
-- independently -- end up with the same row under two different primary keys.
-- Dolt then either refuses the merge ("cannot merge because table dependencies
-- has different primary keys in its common ancestor") or pulls in both rows and
-- trips the uk_dep_* unique keys. Either way `bd dolt pull` breaks with no
-- bd-level recovery.
--
-- The application now derives id deterministically from the natural edge key
-- (issue_id, target) at every insert site, and the upgrade backfill
-- (rekeyDependencyIDs, run from MigrateUp right after this migration) rewrites
-- existing rows' random ids to the deterministic value. This migration does the
-- *schema* half, and is the convergence point every clone reaches when upgrading
-- to the fixed release, whatever its 0043 outcome:
--
--   (1) Drop the random DEFAULT so any future insert that forgets to set id
--       fails loudly (NOT NULL violation) instead of silently re-forking the key.
--   (2) Idempotently re-assert the natural-identity unique keys so a clone whose
--       0043 no-opped on a divergent pre-state converges to the same schema.
--
-- It is intentionally unconditional and idempotent (safe to re-run): it does not
-- probe whether 0043 ran a particular branch, it just asserts the canonical end
-- state. wisp_dependencies is deliberately left alone here: it is dolt-ignored
-- (never merged) and recreated from its 0021 definition by EnsureIgnoredTables,
-- so its (dormant) default cannot be durably dropped by a migration; all wisp
-- insert sites set id explicitly and the backfill re-keys its existing rows.

SET FOREIGN_KEY_CHECKS = 0;

-- (1) Drop the per-clone-random surrogate default. Guarded on COLUMN_DEFAULT so
-- re-running (or running on a clone that never had the default) is a no-op.
SET @has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@has_default = 1,
    'ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- (2) Re-assert the natural-identity unique keys. These already exist after a
-- normal 0043; IF NOT EXISTS makes re-running harmless and repairs a clone whose
-- 0043 branch left one missing. (issue_id, <one typed target>) is the identity
-- the deterministic id is derived from; `type` is deliberately not part of it.
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_issue_target ON dependencies (issue_id, depends_on_issue_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_wisp_target ON dependencies (issue_id, depends_on_wisp_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_external_target ON dependencies (issue_id, depends_on_external);

SET FOREIGN_KEY_CHECKS = 1;
