-- Placeholder for TestOldIgnoredCeilingOpensAfter0028 (T2.9,
-- BEADS-JOURNAL-PLAN.md PR A2): stands in for the newest ignored migration a
-- binary built before this plan's 0025-0028 would have embedded (the d3ab
-- CLI's measured ignored ceiling in the plan's evidence base, §4.1). Its
-- content is never executed by the test -- migrationSource.latest() and
-- .list() only parse the filename for a version number -- so this file need
-- not match the real ignored/0024 migration's SQL.
SELECT 1;
