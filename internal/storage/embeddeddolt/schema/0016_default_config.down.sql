DELETE FROM config WHERE `key` IN (
    'compaction_enabled',
    'compact_tier1_days',
    'compact_tier1_dep_levels',
    'compact_tier2_days',
    'compact_tier2_dep_levels',
    'compact_tier2_commits',
    'compact_batch_size',
    'compact_parallel_workers',
    'auto_compact_enabled'
);
