-- THROWAWAY P0 spike migration for the BDP bead-graph row-level fence
-- (engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md Part B4, ruling 13). It lives
-- under testdata/, NOT under migrations/: it is never part of the series and
-- version 9001 claims no slot. It carries exactly the shape a real P1 table
-- file would carry for one replicated graph table: the CREATE TABLE, then a
-- DROP TRIGGER IF EXISTS + CREATE TRIGGER pair per BEFORE INSERT/UPDATE/DELETE,
-- each body in BEGIN ... END form, refusing the row unless the session set
-- @bd_graph_role. No NOW()/UUID()/RAND()/CURRENT_TIMESTAMP, no PREPARE.
CREATE TABLE IF NOT EXISTS graph_beads_spike (
  path VARCHAR(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  revision CHAR(32) NOT NULL,
  last_authority_id CHAR(32) NOT NULL,
  last_epoch BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (path)
);

DROP TRIGGER IF EXISTS graph_beads_spike_bi;
CREATE TRIGGER graph_beads_spike_bi BEFORE INSERT ON graph_beads_spike FOR EACH ROW
BEGIN
  IF @bd_graph_role IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'graph_beads_spike: out-of-role write refused';
  END IF;
END;

DROP TRIGGER IF EXISTS graph_beads_spike_bu;
CREATE TRIGGER graph_beads_spike_bu BEFORE UPDATE ON graph_beads_spike FOR EACH ROW
BEGIN
  IF @bd_graph_role IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'graph_beads_spike: out-of-role write refused';
  END IF;
END;

DROP TRIGGER IF EXISTS graph_beads_spike_bd;
CREATE TRIGGER graph_beads_spike_bd BEFORE DELETE ON graph_beads_spike FOR EACH ROW
BEGIN
  IF @bd_graph_role IS NULL THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'graph_beads_spike: out-of-role write refused';
  END IF;
END;
