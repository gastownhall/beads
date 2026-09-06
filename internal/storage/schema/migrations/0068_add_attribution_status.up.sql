-- Migration 0068: Phase 2 dual-write schema for versioned beads (be-hs42e.3
-- / gastownhall/beads#6135), step 6 of design section 16.3 (be-dt74u
-- amendment, be-hs42e.3's design field) only.
--
-- Steps 1-5 of section 16.3 (version_id CHAR(36) UUID PK swap;
-- participation_generation BIGINT NULL on issues and its wisps shape-parity
-- mirror) are deliberately NOT in this file: be-v33pa's own scope is BASE
-- re-seating + attribution_status (section 16.4) only, per its exit
-- contract. A later bead may append the remaining steps to this same file --
-- scripts/check-migration-hygiene.sh Check C only protects a migration
-- already shipped on the base branch, and this one is still unmerged.
--
-- attribution_status (R14, section 16.4): a NOT NULL column with no default
-- is safe here because issue_versions (created by 0067, a prior migration in
-- this same set) is guaranteed empty at this point in any replay -- Phase 2
-- is this table's first and only writer, and its dual-write code lands in
-- the same build as this column, so there is no pre-existing row for the
-- NOT NULL constraint to reject. See RecordVersionInTx /
-- attributionStatusForActor in internal/storage/issueops/version_history.go
-- for what populates it.
--
-- Guarded the same way 0067's ADD COLUMNs are (see that file's header for
-- the full explanation of why: no MariaDB-only IF NOT EXISTS on Dolt 2.2.3's
-- ADD COLUMN, so an INFORMATION_SCHEMA probe + PREPARE is the only
-- replay-safe shape) -- makes a raw-SQL replay of this file a clean no-op on
-- an already-migrated store. Needs a CLI-bundle direct-DDL override
-- (cliMigration0068AddAttributionStatus in cli_migrations.go), the same
-- dolthub/dolt#11345 escape hatch 0067 uses, guarded by
-- TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified.
--
-- No wisps twin needed: issue_versions has no wisps-side counterpart table
-- at all (design section 16.3), so cliSubstituteAssumesWispTables does not
-- apply to this migration either.
SET @issue_versions_as_needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'attribution_status'
);
SET @sql = IF(@issue_versions_as_needs_add = 1,
    'ALTER TABLE issue_versions ADD COLUMN attribution_status VARCHAR(20) NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
