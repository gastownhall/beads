-- Ignored migration 0028: give clone-local wisps a usable aggregate revision.
-- This is the ignored-plane twin of synced migration 0070.
-- updated_at self-assigns (the 0059 / ignored-0015 discipline) so the backfill
-- cannot restamp closed rows or look like a user edit to staleness consumers.
UPDATE wisps SET row_lock = 1, updated_at = updated_at WHERE row_lock = 0;
