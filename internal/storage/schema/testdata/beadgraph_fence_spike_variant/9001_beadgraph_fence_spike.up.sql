-- THROWAWAY P0 spike migration, VARIANT: the same version number and the same
-- table as ../beadgraph_fence_spike/9001_beadgraph_fence_spike.up.sql but
-- WITHOUT the trigger block. Applied to a second clone, it is the #4259
-- "same version, different content" fork that content_skew.go exists to
-- detect. Never part of the series.
CREATE TABLE IF NOT EXISTS graph_beads_spike (
  path VARCHAR(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  revision CHAR(32) NOT NULL,
  last_authority_id CHAR(32) NOT NULL,
  last_epoch BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (path)
);
