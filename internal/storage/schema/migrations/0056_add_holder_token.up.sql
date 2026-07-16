-- Work-ownership integrity (ga-furrj5, A-B3a): bind a claim to an
-- incarnation-unique holder token.
--
--   holder_token  an opaque per-incarnation credential recorded at claim time
--                 from the caller's ambient BEADS_HOLDER_TOKEN (Gas City sets
--                 it to the session's instance token). Unlike assignee — a
--                 reusable display name that two processes with the same name
--                 share — the token distinguishes runtime incarnations, so a
--                 stale process cannot pass as the current owner of a claim
--                 the name was re-used for.
--
-- The token is enforcement-only state: it is deliberately NOT part of the
-- issue read/scan/JSON surface (a fenced-out worker that could read the
-- current token via `bd show` could just present it), so it lives only in the
-- WHERE clauses of enforced mutations and in the advisory classification. A
-- package-guard test asserts it stays out of the canonical select columns.
--
-- Both tables carry the column: durable no_history work lives in wisps, and
-- ownership integrity is tier-complete.
--
-- Guarded for idempotent re-runs on a regressed schema_migrations row.

-- issues.holder_token
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'holder_token'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN holder_token VARCHAR(64) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.holder_token (guarded on the wisps table existing)
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);
SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'holder_token') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN holder_token VARCHAR(64) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
