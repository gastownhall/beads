-- epoch_minted_addresses: durable storage for R20 epoch-transition
-- enforcement (gastownhall/beads#5898 revision 9, this slice: be-x5jqd.4 /
-- #6136). Independent of store_epoch (0067, #6135) -- that table holds only
-- the store-wide singleton epoch counter (the current generation number);
-- this table tracks the individual addresses minted under each generation,
-- which store_epoch has no room to hold. It is also independent of
-- expected_revision_records (R16, #6133) and any RetentionFixture/R17
-- table: R20 epoch reasoning is evaluated on its own, without consulting
-- retention/erasure state, and this migration adds no R17 resolve, remove,
-- hold, force-remove, erase, or mint logic.
--
-- One row per minted address, not one row per id: MintUnderEpoch is
-- deterministic in (store_id, id, epoch) -- see epochAddress() in
-- internal/storage/issueops/epoch_cas.go -- so minting the same id again
-- under the same epoch reproduces the same address and upserts the same
-- row, while a later mint of that id under a bumped epoch produces a
-- DIFFERENT address and adds a new row alongside the old one. Old rows are
-- kept, not superseded in place: StillServes and Resolve must still be able
-- to answer for a pre-bump address (as "no longer served" / "gone
-- reorganization") after the epoch moves on, which requires the old row to
-- still exist.
--
-- address is the PRIMARY KEY: it is the deterministic token the fixture's
-- StillServes/Resolve/CurrentAddressFor hooks look addresses up by, so it
-- must be unique and indexed; it is never recomputed from the other columns
-- at read time; it is written once, whatever the epoch was at mint time.
--
-- store_id and minted_id are VARCHAR(255) for parity with this schema's
-- other identifier columns. minted_epoch is INT to match store_epoch.epoch's
-- own type. minted_at is DATETIME, not a nanosecond-precision integer: unlike
-- expected_revision_records.change_at_nanos (R16, 0069 on that slice), no
-- R20 contract case compares minted_at for exact round-tripping -- it exists
-- for observability only, so it follows store_epoch.bumped_at's own
-- DATETIME precedent rather than needing BIGINT.
--
-- Plain, unguarded CREATE TABLE IF NOT EXISTS: a brand-new main-plane table
-- with no ALTER, no PREPARE, and no dolt-ignored (clone-local) table
-- involved, so none of cli_migrations.go's CLI-bundle overrides, an
-- ignored/ twin, or a nondeterminism-allowlist entry apply
-- (scripts/check-migration-hygiene.sh checks B-E).
CREATE TABLE IF NOT EXISTS epoch_minted_addresses (
    address VARCHAR(255) NOT NULL,
    store_id VARCHAR(255) NOT NULL,
    minted_id VARCHAR(255) NOT NULL,
    minted_epoch INT NOT NULL,
    minted_at DATETIME NOT NULL,
    PRIMARY KEY (address)
);
