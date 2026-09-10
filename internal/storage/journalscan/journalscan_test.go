package journalscan

import "testing"

// TestSQLWritesBeadTable pins the DML detector the completeness guards rest on.
// A false negative here silently disarms every guard, so the templated forms —
// plain %s and the explicit-argument-index %[1]s — are covered alongside the
// literal table names.
func TestSQLWritesBeadTable(t *testing.T) {
	writes := []string{
		"INSERT INTO issues (id) VALUES (?)",
		"insert into issues (id) values (?)",
		"INSERT IGNORE INTO wisp_labels (issue_id, label) VALUES (?, ?)",
		"REPLACE INTO comments (id) VALUES (?)",
		"UPDATE wisps SET status = ? WHERE id = ?",
		"DELETE FROM dependencies WHERE issue_id = ?",
		"INSERT INTO %s (issue_id, label) VALUES (?, ?)",
		"INSERT INTO %[1]s (parent_id, last_child) VALUES (?, ?)",
		"UPDATE %[2]s SET x = 1 WHERE id = ?",
		"\n\t\tDELETE FROM  wisp_comments\n\t\tWHERE issue_id = ?\n\t",
		"DELETE FROM `issues` WHERE id = ?",
	}
	for _, lit := range writes {
		if !SQLWritesBeadTable(lit) {
			t.Errorf("SQLWritesBeadTable(%q) = false, want true", lit)
		}
	}

	reads := []string{
		"SELECT id FROM issues WHERE id = ?",
		"SELECT COUNT(*) FROM dependencies",
		// Aux tables that are not work-bead state.
		"INSERT INTO events (id) VALUES (?)",
		"DELETE FROM leases WHERE issue_id = ?",
		"UPDATE config SET value = ? WHERE `key` = ?",
		"INSERT INTO bd_events_journal (seq) VALUES (?)",
		// A table whose name merely starts with a bead table's name.
		"INSERT INTO issues_archive (id) VALUES (?)",
	}
	for _, lit := range reads {
		if SQLWritesBeadTable(lit) {
			t.Errorf("SQLWritesBeadTable(%q) = true, want false", lit)
		}
	}
}
