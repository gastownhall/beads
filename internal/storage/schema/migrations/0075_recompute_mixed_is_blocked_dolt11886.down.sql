-- Reverse migration 0075: forget the is_blocked repair obligation.
--
-- Deletes only this key. It does not attempt to restore the corrupt flags
-- 0059 left behind, and it does not touch the ignored series: a store whose
-- ignored cursor is still below 0032 keeps whatever flags it holds until
-- 0075 runs again and re-records the obligation. Deliberately stepping a
-- store back this way is safe only while an operator controls every binary
-- that opens it (the schema-skew guard already refuses older binaries).
DELETE FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075';
