-- expected_revision_records: durable storage for R16 per-record
-- compare-and-set (gastownhall/beads#5898 revision 9, this slice: be-x5jqd.3
-- / #6133). Independent of issue_versions (0067/0068, #6135) -- that table
-- is Phase 2's issue-versioning dual-write, tracked under a disjoint design
-- lineage (#6358/#6379, BDP#18/20/21); this table backs R16's own contract
-- (backend/conformance/expected_revision_contract.go) and is never read or
-- written by versioned-history code.
--
-- One row per record id, holding the CURRENT state only -- R16 is a
-- whole-of-state precondition, not a history log, so there is nothing to
-- keep once a row is superseded. The record's Address (the CAS token
-- expected/returned by CompareAndSetVersion) is never stored: it is always
-- recomputed on demand as sha256(durable_state), so there is no address
-- column to drift out of sync with the bytes it names.
--
-- durable_state is LONGBLOB, not JSON, for the same reason issue_versions'
-- durable_state was retyped to LONGBLOB in 0068 step 7: a Dolt JSON column
-- parses and renormalizes what it stores (1.0 reads back as 1, a large
-- integer gets rounded, 1e300 becomes 1e+300), which would silently change
-- the bytes the address is computed over. LONGBLOB returns exactly the
-- bytes written, which is what a content-derived token (R5.1) requires.
--
-- change_at_nanos is BIGINT (UnixNano), not DATETIME: R17-b requires a
-- refusal to carry the refusing version's exact ChangeAttribution.At,
-- round-tripped losslessly, and DATETIME's second-granularity conversion
-- would not preserve nanosecond timestamps the way an int64 field does.
--
-- change_actor/change_agent/change_message are nullable: patch has no
-- attribution keys, unrepresented by "_attribution" leaves them at their
-- zero value, which this table represents as NULL, not empty string.
--
-- Plain, unguarded CREATE TABLE IF NOT EXISTS: a brand-new main-plane table
-- with no ALTER, no PREPARE, and no dolt-ignored (clone-local) table
-- involved, so none of cli_migrations.go's CLI-bundle overrides, an
-- ignored/ twin, or a nondeterminism-allowlist entry apply (scripts/check-migration-hygiene.sh
-- checks B-E).
CREATE TABLE IF NOT EXISTS expected_revision_records (
    id VARCHAR(255) NOT NULL,
    durable_state LONGBLOB NOT NULL,
    change_actor VARCHAR(255),
    change_agent VARCHAR(255),
    change_message TEXT,
    change_at_nanos BIGINT NOT NULL,
    PRIMARY KEY (id)
);
