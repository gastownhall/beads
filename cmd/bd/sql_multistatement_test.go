package main

import "testing"

func TestTopLevelStatementCount(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"single", "SELECT 1", 1},
		{"single_trailing_semicolon", "SELECT 1;", 1},
		{"single_multiline", "SELECT id, title\nFROM issues\nWHERE status = 'open'", 1},
		{"two", "SELECT 1; SELECT 2", 2},
		{"two_trailing", "SELECT 1; SELECT 2;", 2},
		{"three_writes", "INSERT INTO t VALUES (1); INSERT INTO t VALUES (2); UPDATE t SET id=3;", 3},
		{"semicolon_in_single_quote", "SELECT ';'", 1},
		{"semicolon_in_double_quote", "SELECT \";;;\" AS x", 1},
		{"semicolon_in_backtick", "SELECT 1 AS `a;b`", 1},
		{"escaped_quote", "SELECT 'it\\'s; fine'", 1},
		{"doubled_quote", "SELECT 'it''s; fine'", 1},
		{"line_comment_dashes", "SELECT 1 -- a; b\n", 1},
		{"line_comment_cr_terminates", "SELECT 1; -- x\rSELECT 2", 2},
		{"line_comment_crlf", "SELECT 1; -- x\r\nSELECT 2", 2},
		{"hash_comment", "SELECT 1 # a; b\n", 1},
		{"hash_comment_cr_terminates", "SELECT 1; # x\rSELECT 2", 2},
		{"block_comment", "SELECT 1 /* a; b; c */", 1},
		{"block_comment_then_stmt", "SELECT 1 /* ; */; SELECT 2", 2},
		{"empty", "", 0},
		{"only_semicolons", ";;;", 0},
		{"whitespace_between", "  SELECT 1 ;  SELECT 2  ", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := topLevelStatementCount(tc.query); got != tc.want {
				t.Errorf("topLevelStatementCount(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

func TestIsMultiStatementSQL(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"SELECT 1", false},
		{"SELECT 1;", false},
		{"SELECT id\nFROM issues", false},
		{"SELECT 1; SELECT 2", true},
		{"DELETE FROM t; DELETE FROM u", true},
		{"SELECT ';'", false},
	}
	for _, tc := range cases {
		if got := isMultiStatementSQL(tc.query); got != tc.want {
			t.Errorf("isMultiStatementSQL(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}
