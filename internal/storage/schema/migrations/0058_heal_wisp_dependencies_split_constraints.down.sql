-- Reverse of 0058: intentional no-op.
--
-- This migration only adds constraints that were always supposed to be
-- present on wisp_dependencies (fk_wisp_dep_wisp_target,
-- fk_wisp_dep_issue_target, ck_wisp_dep_one_target); dropping them again on
-- rollback would just reintroduce the referential-integrity gap this
-- migration exists to heal. Restore from a prior Dolt commit if a genuine
-- rollback is needed.
SELECT 1;
