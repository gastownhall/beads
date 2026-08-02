-- Reverse of 0051: intentional no-op.
--
-- Re-introducing DEFAULT (UUID()) on the aux history tables' id columns would
-- restore the per-clone-random fallback this migration exists to remove (and
-- would itself be nondeterministic SQL, banned by check-migration-hygiene.sh).
-- Nothing data-bearing was changed, so there is nothing to unwind; restore
-- from a prior dolt commit if a rollback is truly needed.
SELECT 1;
