-- Reverse migration 0075: forget an owed is_blocked repair. The flags it
-- already corrected stay corrected; run `bd recompute-blocked` to repair a
-- store rolled back before the repair ran.
DELETE FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075';
