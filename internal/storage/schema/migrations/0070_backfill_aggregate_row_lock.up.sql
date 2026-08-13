-- Migration 0066: make every durable aggregate revision immediately usable.
-- row_lock is equality-only, so legacy zero rows may share the same non-zero
-- backfill token; the next supported mutation remints a random token.
UPDATE issues SET row_lock = 1 WHERE row_lock = 0;
