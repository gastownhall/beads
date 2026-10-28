package sqlclass

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  Kind
	}{
		{"select", "SELECT 1", Read},
		{"lowercase select", "  select id from issues", Read},
		{"union", "SELECT 1 UNION SELECT 2", Read},
		{"parenthesized union", "(SELECT 1) UNION (SELECT 2)", Read},
		{"show", "SHOW TABLES", Read},
		{"describe", "DESCRIBE issues", Read},
		{"explain select", "EXPLAIN SELECT * FROM issues", Read},
		{"leading block comment", "/* note */ SELECT 1", Read},
		{"leading line comment", "-- note\nSELECT 1", Read},

		// CTE reads: a prefix scanner that treats the column list after the CTE
		// name as the CTE body sees "AS" as the main verb and calls it a write.
		{"cte select", "WITH t AS (SELECT 1 AS n) SELECT n FROM t", Read},
		{"cte column list", "WITH t(n) AS (SELECT 1) SELECT n FROM t", Read},
		{"recursive cte column list", "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 3) SELECT i FROM n", Read},
		{"recursive cte", "WITH RECURSIVE t AS (SELECT 1 AS i UNION ALL SELECT i + 1 FROM t WHERE i < 3) SELECT * FROM t", Read},
		{"multiple ctes with column lists", "WITH a(x) AS (SELECT 1), b(y) AS (SELECT x FROM a) SELECT y FROM b", Read},
		{"cte nested parens", "WITH t AS (SELECT (1 + (2 * 3)) AS n) SELECT n FROM t", Read},
		{"cte keywords in literal", "WITH t AS (SELECT ') DELETE FROM issues' AS s) SELECT s FROM t", Read},
		{"cte escaped quote in literal", `WITH t AS (SELECT 'a\'(' AS s) SELECT s FROM t`, Read},
		{"cte comment with keywords", "WITH t AS (SELECT 1 AS n /* ) DELETE */) SELECT n FROM t", Read},
		{"cte union main", "WITH t(n) AS (SELECT 1) SELECT n FROM t UNION SELECT 2", Read},

		// CTE writes must stay writes: a read transaction is rolled back.
		{"cte delete", "WITH t AS (SELECT id FROM issues) DELETE FROM issues WHERE id IN (SELECT id FROM t)", Write},
		{"cte column list update", "WITH t(i) AS (SELECT id FROM issues) UPDATE issues SET priority = 1 WHERE id IN (SELECT i FROM t)", Write},
		{"recursive cte delete", "WITH RECURSIVE t(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM t WHERE i < 3) DELETE FROM issues WHERE priority IN (SELECT i FROM t)", Write},
		{"cte escaped quote hides delete", `WITH t AS (SELECT 'a\'(' AS x) DELETE FROM issues`, Write},
		{"insert select with cte", "INSERT INTO labels (issue_id, label) WITH t AS (SELECT id FROM issues) SELECT id, 'x' FROM t", Write},

		{"insert", "INSERT INTO labels (issue_id, label) VALUES ('a', 'b')", Write},
		{"replace", "REPLACE INTO labels (issue_id, label) VALUES ('a', 'b')", Write},
		{"update", "UPDATE issues SET priority = 1", Write},
		{"delete", "DELETE FROM issues", Write},
		{"create table", "CREATE TABLE x (id INT PRIMARY KEY)", Write},
		{"alter table", "ALTER TABLE issues ADD COLUMN x INT", Write},
		{"drop table", "DROP TABLE x", Write},
		{"set", "SET @x = 1", Write},
		{"select into variable", "SELECT 1 INTO @x", Write},

		// Statements that may write AND return rows, or that the parser
		// cannot classify, must run in a committing transaction that still
		// renders any result set.
		{"call", "CALL DOLT_COMMIT('-Am', 'msg')", Mixed},
		{"explain analyze delete", "EXPLAIN ANALYZE DELETE FROM issues", Mixed},
		{"unparseable", "PRAGMA table_info(issues)", Mixed},
		{"garbage", "NOT SQL AT ALL (", Mixed},
		{"empty", "", Mixed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.query); got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// Dolt exposes mutating procedures as SELECT-callable functions. They change
// repository state, so they must not be routed as reads: a Read skips the
// read-only guard and runs in a rolled-back read transaction (GH#6230).
func TestClassifyDoltProcedureSelectIsNotRead(t *testing.T) {
	for _, query := range []string{
		"SELECT DOLT_COMMIT('-Am', 'msg')",
		"SELECT DOLT_RESET('--hard')",
		"SELECT DOLT_CHECKOUT('main')",
		"SELECT DOLT_MERGE('feature')",
		"select dolt_reset('--hard')",
		"SELECT `dolt_checkout`('main')",
		"SELECT mydb.dolt_merge('feature')",
		"SELECT id FROM issues WHERE DOLT_COMMIT('-Am','x') = 0",
		"SELECT (SELECT DOLT_RESET('--hard'))",
		"WITH t AS (SELECT DOLT_COMMIT('-Am','x') AS h) SELECT h FROM t",
		"SELECT 1 UNION SELECT DOLT_PUSH('origin','main')",
		"SELECT * FROM dolt_diff(DOLT_COMMIT('-Am','x'), 'WORKING', 'issues')",
		"SHOW TABLES WHERE DOLT_COMMIT('-Am','x') = 0",
		"EXPLAIN SELECT DOLT_COMMIT('-Am','x')",
		"EXPLAIN ANALYZE SELECT DOLT_COMMIT('-Am','x')",
		// A dolt_ name the pinned engine does not register still fails
		// closed: a newer server may expose it as a SELECT-callable write.
		"SELECT DOLT_FUTURE_PROC()",
	} {
		if got := Classify(query); got == Read {
			t.Errorf("Classify(%q) = %v, want a committing kind (mixed)", query, got)
		}
	}
}
