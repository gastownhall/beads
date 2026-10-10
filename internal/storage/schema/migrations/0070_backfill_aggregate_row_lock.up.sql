-- Migration 0070: make every durable aggregate revision immediately usable.
-- row_lock is equality-only, so legacy zero rows may share the same non-zero
-- backfill token; the next supported mutation remints a random token.
-- updated_at self-assigns (the 0059 / ignored-0015 discipline) so the backfill
-- cannot restamp closed rows or look like a user edit to staleness consumers.
UPDATE issues SET row_lock = 1, updated_at = updated_at WHERE row_lock = 0;
