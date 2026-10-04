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
-- kept: StillServes and Resolve answer for any address from its own row,
-- whatever epoch it was minted in.
--
-- Survival is RECORDED on the row, never inferred from other rows
-- (gastownhall/beads#6664, bee-ghosttrack review 5360880888, Major 1).
-- gone_at_epoch is NULL while the store serves the address and otherwise the
-- epoch the store lost it in; it is set once and never cleared, so a lost
-- address stays lost. carried_from is NULL for a fresh mint, and for a row a
-- token-scheme change carried to a new epoch it is the address of the
-- lineage's ROOT (never the previous carry), so every member of a lineage is
-- one indexed lookup from the newest. The index on carried_from serves exactly
-- the two lookups that follow a lineage: the newest carry of a root, and every
-- carry of a root when the lineage is lost. The CHECK is a database-level
-- tripwire against a stale or rewound epoch being written into a loss marker;
-- it is >=, not >, because a carry row is born at the new epoch and can be lost
-- in that same epoch.
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
    carried_from VARCHAR(255) NULL,
    gone_at_epoch INT NULL,
    PRIMARY KEY (address),
    INDEX idx_epoch_minted_addresses_carried_from (carried_from),
    CONSTRAINT ck_epoch_minted_addresses_gone_not_before_mint CHECK (gone_at_epoch IS NULL OR gone_at_epoch >= minted_epoch)
);
