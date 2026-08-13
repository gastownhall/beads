-- Ignored migration 0025: give clone-local wisps a usable aggregate revision.
-- This is the ignored-plane twin of synced migration 0066.
UPDATE wisps SET row_lock = 1 WHERE row_lock = 0;
