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
