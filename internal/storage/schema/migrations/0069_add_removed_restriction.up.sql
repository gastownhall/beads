-- Migration 0069: R7.1 as-of read (gastownhall/beads#5898 revision 9,
-- gastownhall/beads#6136), this slice: be-x5jqd.5 / backend/conformance/
-- versioned_read_contract.go.
--
-- issue_versions already carries removed_at DATETIME and removed_reason
-- VARCHAR(255) (migration 0067), virgin and unused until now: R7.1's
-- AsOfReadInTx (internal/storage/issueops/asof_read.go) reads them as the
-- durable "this version row was removed" marker on the resolved row, and
-- refuses a served answer whenever removed_at is set (R8: never silently
-- substitute a neighboring/surviving/current version).
--
-- removed_restriction VARCHAR(30) is the one column this migration adds: the
-- categorical restriction (gone-retention / gone-erasure /
-- gone-reorganization / unknown) that removed_at's presence alone cannot
-- carry. Nullable and NULL for every existing row -- Live is the absence of
-- a value, never a stored one -- and populated only alongside removed_at,
-- never independently. 30 chars comfortably fits the longest local
-- constant (internal/storage/issueops/asof_read.go's AsOfRestriction,
-- "gone_reorganization", 19 bytes) with headroom, matching change_agent's
-- own no-tight-fit precedent elsewhere in this table.
--
-- Forward-compatible with R17 (not yet implemented on this branch): R17's
-- eventual real hold/remove/erase primitives will be the production WRITER
-- of these three columns; R7.1 only reads them here. No future collision --
-- this migration only adds the one column R17 will also need, it does not
-- claim the writing role.
--
-- Guarded the same way 0067/0068 guard their ADD COLUMNs (see 0067's header
-- for the full explanation): no MariaDB-only IF NOT EXISTS on Dolt 2.2.3's
-- ADD COLUMN, so an INFORMATION_SCHEMA probe + PREPARE is the only
-- replay-safe shape. Needs a CLI-bundle direct-DDL override
-- (cliMigration0069AddRemovedRestriction in cli_migrations.go), the same
-- dolthub/dolt#11345 escape hatch 0067/0068 use, guarded by
-- TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified.
--
-- No wisps twin: issue_versions has no wisps-side counterpart table (design
-- section 16.3), so cliSubstituteAssumesWispTables does not apply here
-- either, matching 0068.
SET @issue_versions_rr_needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'removed_restriction'
);
SET @sql = IF(@issue_versions_rr_needs_add = 1,
    'ALTER TABLE issue_versions ADD COLUMN removed_restriction VARCHAR(30)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
