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

		// Reads that call a Dolt function that may write must run in a
		// committing transaction behind the read-only gate (GH#6230): a Read
		// skips CheckReadonly and is rolled back.
		{"select dolt function", "SELECT DOLT_COMMIT('-Am', 'msg')", Mixed},
		{"lowercase dolt function", "select dolt_reset('--hard')", Mixed},
		{"backticked dolt function", "SELECT `dolt_checkout`('main')", Mixed},
		{"qualified dolt function", "SELECT mydb.dolt_merge('feature')", Mixed},
		{"dolt function in where", "SELECT id FROM issues WHERE DOLT_COMMIT('-Am','x') = 0", Mixed},
		{"dolt function in subquery", "SELECT (SELECT DOLT_RESET('--hard'))", Mixed},
		{"dolt function in cte", "WITH t AS (SELECT DOLT_COMMIT('-Am','x') AS h) SELECT h FROM t", Mixed},
		{"dolt function in union", "SELECT 1 UNION SELECT DOLT_PUSH('origin','main')", Mixed},
		// sqlparser.Walk skips these clauses; Classify walks them itself.
		{"dolt function in cte before union", "WITH t AS (SELECT DOLT_COMMIT('-Am','x') AS h) SELECT h FROM t UNION SELECT 1", Mixed},
		{"dolt function in union order by", "SELECT 1 UNION SELECT 2 ORDER BY DOLT_COMMIT('-Am','x')", Mixed},
		{"dolt function in derived union order by", "SELECT * FROM (SELECT 1 UNION SELECT 2 ORDER BY DOLT_RESET('--hard')) AS u", Mixed},
		{"explain analyze dolt function in union order by", "EXPLAIN ANALYZE SELECT 1 UNION SELECT 2 ORDER BY DOLT_COMMIT('-Am','x')", Mixed},
		{"dolt function in named window order by", "SELECT ROW_NUMBER() OVER w FROM issues WINDOW w AS (ORDER BY DOLT_COMMIT('-Am','x'))", Mixed},
		{"dolt function in named window partition by", "SELECT ROW_NUMBER() OVER w FROM issues WINDOW w AS (PARTITION BY DOLT_COMMIT('-Am','x'))", Mixed},
		{"dolt function in substring literal from", "SELECT SUBSTRING('abc' FROM DOLT_COMMIT('-Am','x') FOR 1)", Mixed},
		{"dolt function in substr literal for", "SELECT id FROM issues WHERE SUBSTR('abc' FROM 1 FOR DOLT_RESET('--hard')) = 'a'", Mixed},
		{"dolt function in table function arg", "SELECT * FROM dolt_diff(DOLT_COMMIT('-Am','x'), 'WORKING', 'issues')", Mixed},
		{"dolt function in show filter", "SHOW TABLES WHERE DOLT_COMMIT('-Am','x') = 0", Mixed},
		{"explain dolt function", "EXPLAIN SELECT DOLT_COMMIT('-Am','x')", Mixed},
		{"explain analyze dolt function", "EXPLAIN ANALYZE SELECT DOLT_COMMIT('-Am','x')", Mixed},
		{"unknown dolt function", "SELECT DOLT_FUTURE_PROC()", Mixed},

		// Reads that touch Dolt only through system tables, table functions,
		// read-only functions, or literals and comments stay reads.
		{"dolt system table", "SELECT * FROM dolt_log", Read},
		{"dolt conflicts table", "SELECT * FROM dolt_conflicts", Read},
		{"dolt table function", "SELECT * FROM dolt_log()", Read},
		{"dolt diff table function", "SELECT * FROM dolt_diff('HEAD~1', 'HEAD', 'issues')", Read},
		{"read-only dolt functions", "SELECT DOLT_HASHOF('HEAD'), DOLT_VERSION(), DOLT_MERGE_BASE('main', 'feature')", Read},
		{"more read-only dolt functions", "SELECT dolt_hashof_table('issues'), dolt_hashof_db(), dolt_storage_format(), dolt_join_cost()", Read},
		{"read-only dolt functions in union", "SELECT DOLT_HASHOF('HEAD') UNION SELECT DOLT_VERSION() ORDER BY 1", Read},
		{"read-only dolt function in union order by", "SELECT 1 UNION SELECT 2 ORDER BY DOLT_HASHOF('HEAD')", Read},
		{"dolt system table in cte before union", "WITH t AS (SELECT * FROM dolt_log) SELECT * FROM t UNION SELECT * FROM dolt_log", Read},
		{"named window", "SELECT ROW_NUMBER() OVER w FROM issues WINDOW w AS (ORDER BY id)", Read},
		{"substring literal", "SELECT SUBSTRING('abc' FROM 1 FOR 2)", Read},
		{"as of read-only dolt function", "SELECT * FROM issues AS OF DOLT_HASHOF('HEAD~1')", Read},
		{"unprefixed read functions", "SELECT HASHOF('HEAD'), ACTIVE_BRANCH()", Read},
		{"dolt function in literal", "SELECT 'DOLT_COMMIT(1)'", Read},
		{"dolt function in comment", "SELECT 1 /* dolt_commit('x') */", Read},
		{"dolt column name", "SELECT dolt_commit FROM t", Read},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.query); got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}
