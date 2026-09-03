package main

import "testing"

// TestIsReadOnlySQLQuery covers the GH#4121 classifier that decides, before
// the store opens, whether a `bd sql` invocation may use a read-only open.
// The rule set: a single SELECT/SHOW/DESCRIBE statement, EXPLAIN of a SELECT,
// or WITH ... SELECT is read-only; writes, multi-statement input,
// comment-prefixed input, and anything unrecognized are read-write.
func TestIsReadOnlySQLQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  bool
	}{
		// Reads.
		{"select", "SELECT * FROM issues", true},
		{"select_lowercase", "select id from issues where status = 'open'", true},
		{"select_mixed_case_multiline", "Select id, title\nFrom issues\nWhere status = 'open'", true},
		{"select_leading_whitespace", "  \n\tSELECT 1", true},
		{"select_trailing_semicolon", "SELECT 1;", true},
		{"select_semicolon_in_literal", "SELECT * FROM issues WHERE title = 'a;b'", true},
		{"show_tables", "SHOW TABLES", true},
		{"show_create_table", "SHOW CREATE TABLE issues", true},
		{"describe", "DESCRIBE issues", true},
		{"explain_select", "EXPLAIN SELECT * FROM issues", true},
		{"explain_select_lowercase", "explain select 1", true},
		{"explain_analyze_select", "EXPLAIN ANALYZE SELECT * FROM issues", true},
		{"explain_format_json_select", "EXPLAIN FORMAT=JSON SELECT 1", true},
		{"explain_format_tree_select", "EXPLAIN FORMAT = TREE SELECT 1", true},
		{"with_select", "WITH t AS (SELECT id FROM issues) SELECT * FROM t", true},
		{"with_select_lowercase", "with t as (select 1) select * from t", true},
		{"with_multiple_ctes_select", "WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a, b", true},
		// Parity with the proxied-server read classification (sqlQueryIsRead).
		{"pragma", "PRAGMA table_info('issues')", true},

		// Writes.
		{"insert", "INSERT INTO issues (id) VALUES ('x')", false},
		{"insert_lowercase", "insert into issues (id) values ('x')", false},
		{"update", "UPDATE issues SET status = 'closed'", false},
		{"delete", "DELETE FROM dirty_issues WHERE issue_id = 'bd-abc123'", false},
		{"replace", "REPLACE INTO issues (id) VALUES ('x')", false},
		{"create_table", "CREATE TABLE t (id INT)", false},
		{"drop_table", "DROP TABLE issues", false},
		{"with_delete", "WITH t AS (SELECT id FROM issues) DELETE FROM issues WHERE id IN (SELECT id FROM t)", false},
		// MySQL string escapes must not desync the CTE scanner (gate review P1):
		// a backslash-escaped quote inside the CTE body previously lost the
		// parenthesis depth and let a trailing DELETE classify read-only.
		{"with_backslash_escape_delete", `WITH t AS (SELECT 'a\'(' AS x) DELETE FROM issues WHERE id = 'victim'`, false},
		{"with_doubled_quote_escape_delete", `WITH t AS (SELECT 'a''(' AS x) DELETE FROM issues WHERE id = 'victim'`, false},
		// Backslashes fail closed regardless of scanner correctness: escape
		// semantics depend on server sql_mode (NO_BACKSLASH_ESCAPES).
		{"with_backslash_escape_select", `WITH t AS (SELECT 'a\'(' AS x) SELECT * FROM t`, false},
		{"with_doubled_quote_escape_select", `WITH t AS (SELECT 'a''(' AS x) SELECT * FROM t`, true},
		// Comments fail closed: the depth scanner does not parse comment
		// syntax, so any comment keeps the writable open.
		{"inline_block_comment_select", "SELECT /* note */ 1", false},
		{"cte_comment_hidden_paren_delete", "WITH t AS (SELECT 1 /* ) */) DELETE FROM issues", false},
		{"trailing_line_comment_select", "SELECT 1 -- done", false},
		{"hash_comment_select", "SELECT 1 # done", false},

		// EXPLAIN of a non-SELECT: EXPLAIN ANALYZE executes its target, so
		// anything but an explained SELECT stays read-write.
		{"explain_delete", "EXPLAIN DELETE FROM issues", false},
		{"explain_analyze_delete", "EXPLAIN ANALYZE DELETE FROM issues", false},
		{"explain_table_shorthand", "EXPLAIN issues", false},
		{"explain_bare", "EXPLAIN", false},
		{"explain_prefix_of_other_word", "EXPLAINER SELECT 1", false},

		// Multi-statement input.
		{"multi_selects", "SELECT 1; SELECT 2", false},
		{"multi_read_then_write", "SELECT 1; DELETE FROM issues", false},
		{"multi_write_then_read", "DELETE FROM issues; SELECT 1", false},

		// Comment-prefixed and unparseable input stays conservative.
		{"line_comment_prefixed_select", "-- note\nSELECT 1", false},
		{"hash_comment_prefixed_select", "# note\nSELECT 1", false},
		{"block_comment_prefixed_select", "/* note */ SELECT 1", false},
		{"empty", "", false},
		{"whitespace_only", "   \n\t", false},
		{"unrecognized", "FLARBLE GRONK", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadOnlySQLQuery(tc.query); got != tc.want {
				t.Errorf("isReadOnlySQLQuery(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestSQLCommandWantsReadOnlyStore covers the pre-open decision consulted by
// the root PersistentPreRunE: only `bd sql` with exactly one read-only
// statement upgrades the open to read-only.
func TestSQLCommandWantsReadOnlyStore(t *testing.T) {
	cases := []struct {
		name    string
		cmdName string
		args    []string
		want    bool
	}{
		{"sql_select", "sql", []string{"SELECT 1"}, true},
		{"sql_show", "sql", []string{"SHOW TABLES"}, true},
		{"sql_insert", "sql", []string{"INSERT INTO issues (id) VALUES ('x')"}, false},
		{"sql_multi_statement", "sql", []string{"SELECT 1; DELETE FROM issues"}, false},
		{"other_command", "list", []string{"SELECT 1"}, false},
		{"sql_no_args", "sql", nil, false},
		{"sql_extra_args", "sql", []string{"SELECT 1", "SELECT 2"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqlCommandWantsReadOnlyStore(tc.cmdName, tc.args); got != tc.want {
				t.Errorf("sqlCommandWantsReadOnlyStore(%q, %v) = %v, want %v", tc.cmdName, tc.args, got, tc.want)
			}
		})
	}
}
