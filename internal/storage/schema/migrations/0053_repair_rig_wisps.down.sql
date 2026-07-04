-- Reverse of 0053: intentional no-op.
--
-- This is a one-way data repair that promotes durable rig identity beads out of
-- the ephemeral wisp tier. Demoting them again would reintroduce the data loss
-- risk this migration fixes. Restore from a prior Dolt commit if rollback is
-- truly needed.
SELECT 1;
