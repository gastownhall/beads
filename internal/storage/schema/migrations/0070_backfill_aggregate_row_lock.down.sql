-- The aggregate revision backfill is intentionally irreversible: restoring 0
-- would make legacy and genuinely never-written states indistinguishable.
SELECT 1;
