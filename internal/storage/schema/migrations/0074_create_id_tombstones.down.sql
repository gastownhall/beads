-- Reverse migration 0074: drop the id tombstone tables.
--
-- DESTRUCTIVE TO THE GUARANTEE, and silently so. Every id a retaining sweep
-- retired becomes creatable again: a late retry of the operation a pinned id
-- named re-creates the row the operation already created. A workspace that
-- runs this must be treated as a new logical scope by every producer that
-- relied on the tombstones (no client key may be reused across it).
DROP TABLE IF EXISTS id_tombstones;
DROP TABLE IF EXISTS wisp_id_tombstones;
