-- Migration 0068: Phase 2 dual-write schema for versioned beads (be-hs42e.3
-- / gastownhall/beads#6135), step 6 of design section 16.3 (be-dt74u
-- amendment, be-hs42e.3's design field), plus step 7 -- the durable_state
-- LONGBLOB retype added at review, which has its own header further down.
--
-- Steps 1-5 of section 16.3 (version_id CHAR(36) UUID PK swap;
-- participation_generation BIGINT NULL on issues and its wisps shape-parity
-- mirror) are deliberately NOT in this file: be-v33pa's own scope is BASE
-- re-seating + attribution_status (section 16.4) only, per its exit
-- contract. A later bead may append the remaining steps to this same file --
-- scripts/check-migration-hygiene.sh Check C only protects a migration
-- already shipped on the base branch, and this one is still unmerged.
--
-- attribution_status (section 16.4, vocabulary aligned to BDP's
-- carried-attribution status, gastownhall/bdp#18, merged 2026-09-07): exactly
-- two legal values, 'claimed' (the mutation arrived with an actor) and
-- 'unknown' (the mutation path had none). 'imported' is provenance, not an
-- assertion, and is NOT a status -- it returns as a separate provenance
-- marker in the phase that first imports history, for which no writer
-- exists yet. A NOT NULL column with no default is safe here because
-- issue_versions (created by 0067, a prior migration in this same set) is
-- guaranteed empty at this point in any replay -- Phase 2 is this table's
-- first and only writer, and its dual-write code lands in the same build as
-- this column, so there is no pre-existing row for the NOT NULL constraint
-- to reject. See RecordVersionInTx / attributionStatusForActor in
-- internal/storage/issueops/version_history.go for what populates it.
--
-- Ordinals are local, not wire addresses: issues.current_revision and
-- issue_versions.revision are per-store ordinals (two disconnected clones can
-- both hold revision 8 for the same issue). The only durable address of a
-- version is version_id (steps 1-3 above, not yet landed). The HTTP API's
-- RowVersion/Revision is a compare-and-set token, never an address, and Phase
-- 3's read surface returns version_id, never the ordinal.
--
-- SINGLE WRITER ONLY until steps 1-3 land: with PRIMARY KEY (issue_id,
-- revision), two disconnected writers that each mint the same ordinal
-- collide on merge and TryAutoResolveMergeConflicts fails the pull for a
-- table it does not know, so enabling versioned history is safe only with a
-- single writer per store until the UUID version_id primary key lands and
-- the ordinal is demoted to an index. Those steps follow as their own PR.
-- "Single writer" means one writer at a time per store, not merely one
-- clone: MAX(revision)+1 is not a safe allocator for two concurrent
-- transactions in one store either (gastownhall/beads#6379, item 4).
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

-- 0068 step 7, added at review (donnabox on gastownhall/beads#6358 item
-- 4): retype issue_versions.durable_state from JSON to LONGBLOB.
--
-- 0067 created durable_state as a Dolt JSON column. Dolt's JSON type does
-- not keep the bytes it is handed -- it parses and renormalizes them, and
-- numbers do not survive the round trip (measured on dolt 2.2.3): 1.0 reads
-- back as 1, 9007199254740993 (past 2^53) reads back as 9007199254740992,
-- 1e300 reads back as 1e+300. A LONGBLOB returns the same bytes it was
-- given, byte for byte (same measurement). That breaks the design's
-- verbatim-bytes promise (R5.1) and makes any content-derived token
-- impossible, because sha256 over the bytes read back would not equal
-- sha256 over the bytes written. The column has to preserve bytes, so it
-- becomes a LONGBLOB. The writer (RecordVersionInTx in
-- internal/storage/issueops/version_history.go) stores the RFC 8785 (JCS)
-- canonical form of the marshalled issue and readers parse those bytes, so
-- the stored bytes are exactly the bytes a content token hashes.
--
-- No data conversion is needed: issue_versions is empty in the Phase 2 era
-- (this migration and the table's first writer ship in the same build --
-- see the attribution_status note above), so the MODIFY changes only the
-- column type. Guarded on DATA_TYPE the same way 0057 guards its LONGTEXT
-- widenings: the retype fires when the column exists and is not yet
-- LONGBLOB, and a raw-SQL replay onto an already-migrated store is a clean
-- no-op (a missing column makes the probe yield NULL, and the IF takes the
-- SELECT 1 branch, the same way 0067's table-exists guards no-op). The
-- CLI-bundle override (cliMigration0068AddAttributionStatus) carries the
-- direct MODIFY for the same pre-2.3 CLI reason as step 6.
SET @issue_versions_ds_needs_retype = (
    SELECT IF(DATA_TYPE <> 'longblob', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'durable_state'
);
SET @sql = IF(@issue_versions_ds_needs_retype = 1,
    'ALTER TABLE issue_versions MODIFY COLUMN durable_state LONGBLOB',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
