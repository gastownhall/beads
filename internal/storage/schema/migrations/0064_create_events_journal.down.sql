-- Reverse migration 0064: drop the clone-local events journal tables.
-- Both are dolt_ignored, so this only touches the working set.
--
-- DESTRUCTIVE TO CONSUMERS, and silently so. The sequence space lives entirely
-- in bd_events_seq: dropping it resets seq to 1 on the next up-migration, so a
-- parked consumer holding a checkpoint from the old space is now holding a
-- number ABOVE the new head. It reads that as "caught up", returns an empty
-- success, and stalls forever without an error — the one failure the typed
-- truncation error cannot report, because nothing about the new journal looks
-- pruned. Losing or restoring the counter table by hand has the same effect.
-- Anything consuming this workspace must re-baseline (a fresh export, or a full
-- re-read) after a down+up cycle.
DROP TABLE IF EXISTS bd_events_journal;
DROP TABLE IF EXISTS bd_events_seq;
