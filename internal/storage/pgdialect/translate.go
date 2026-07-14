// Package pgdialect translates the MySQL-dialect SQL that bd's shared storage
// layer (internal/storage/sqlbuild and internal/storage/issueops) emits into
// PostgreSQL, so that same layer can run unchanged over a pgx-backed
// database/sql connection.
//
// It deliberately handles only the constructs bd actually emits on the storage
// path — a finite, enumerable set (see corpus_test.go). Anything it does not
// recognize passes through untouched and surfaces as a loud Postgres syntax
// error at PREPARE time, never a silent mistranslation. That "fail loud"
// property is the whole safety argument: a divergence is a build failure, not a
// subtly wrong row set.
package pgdialect

import (
	"fmt"
	"strings"
)

// Translate rewrites a single MySQL statement into its Postgres equivalent.
//
// Pass order is deliberate. Function-shaped rewrites (IF, VALUES, CONCAT,
// LOCATE, JSON) run first so the ON DUPLICATE KEY body is already Postgres by
// the time it is wrapped into ON CONFLICT. Placeholder numbering runs last, so
// every earlier pass can move text freely; none of them introduce a literal
// `?`, so left-to-right numbering stays correct.
func Translate(sql string) (string, error) {
	out := sql
	// CONCAT/LOCATE are unambiguous functions anywhere they appear. IF() and
	// VALUES() are NOT translated globally: VALUES is also the INSERT keyword,
	// and both only appear as functions inside an ON DUPLICATE KEY UPDATE body,
	// so they are translated there (rewriteOnDuplicateKey).
	out = rewriteFuncCalls(out, "CONCAT", concatArgs)          // CONCAT(a,b) -> (a || b)
	out = rewriteFuncCalls(out, "LOCATE", locateArgs)          // LOCATE(n,h) -> POSITION(n IN h)
	out = rewriteFuncCalls(out, "INSTR", instrArgs)            // INSTR(h,n) -> POSITION(n IN h) (args reversed vs LOCATE)
	out = rewriteFuncCalls(out, "DATE_FORMAT", dateFormatArgs) // DATE_FORMAT(t,'%Y-%m-%dT%H:%i:%sZ') -> to_char(t,'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	out = rewriteFuncCalls(out, "JSON_OBJECT", jsonObjectArgs) // JSON_OBJECT(k,v,...) -> jsonb_build_object(k,v,...)
	out = rewriteCastChar(out)                                 // CAST(x AS CHAR[(n)]) -> (x)::text
	// CURRENT_TIMESTAMP (not now()) so the replacement contains no token that a
	// later pass would re-match. AT TIME ZONE 'utc' yields naive-UTC to match
	// bd's Dolt storage discipline.
	out = replaceIdentAll(out, "UTC_TIMESTAMP()", "(CURRENT_TIMESTAMP AT TIME ZONE 'utc')")
	out = replaceIdentAll(out, "NOW()", "(CURRENT_TIMESTAMP AT TIME ZONE 'utc')")
	out = rewriteLiteralJSONUnquote(out)
	out = rewriteDynamicJSONPath(out)
	out = rewriteLabelOrderBy(out)
	out = rewriteInsertIgnore(out)
	out = rewriteReplaceInto(out)
	out = rewriteOnDuplicateKey(out)
	out = rewriteUpdateSetTargets(out)
	out = rewriteBackticks(out)
	out = numberPlaceholders(out)
	return out, nil
}

// codeMask returns a per-byte bitmap: mask[i] is true iff byte i lies outside
// any string literal or comment. bd uses single-quoted strings (” escapes a
// quote), `--` line comments, and `/* */` blocks; it never double-quotes
// strings (those are identifiers in Postgres/ANSI mode), so a double quote is
// always code. Every text rewrite consults this so it never touches quoted data
// or commented-out SQL.
func codeMask(sql string) []bool {
	b := []byte(sql)
	m := make([]bool, len(b))
	var inStr, inLine, inBlock bool
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inBlock:
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				i++ // consume '/'
				inBlock = false
			}
		case inStr:
			if c == '\'' {
				if i+1 < len(b) && b[i+1] == '\'' {
					i++ // escaped quote: consume both
				} else {
					inStr = false
				}
			}
		default:
			switch {
			case c == '\'':
				inStr = true
			case c == '-' && i+1 < len(b) && b[i+1] == '-':
				i++
				inLine = true
			case c == '/' && i+1 < len(b) && b[i+1] == '*':
				i++
				inBlock = true
			default:
				m[i] = true
			}
		}
	}
	return m
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// numberPlaceholders replaces each code-region `?` with $1, $2, … in order.
func numberPlaceholders(sql string) string {
	m := codeMask(sql)
	var b strings.Builder
	n := 0
	for i := 0; i < len(sql); i++ {
		if m[i] && sql[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteByte(sql[i])
	}
	return b.String()
}

// rewriteBackticks turns `ident` into "ident" in code regions. bd only uses
// backticks around reserved-word column names (e.g. `key`).
func rewriteBackticks(sql string) string {
	m := codeMask(sql)
	out := []byte(sql)
	for i := range out {
		if m[i] && out[i] == '`' {
			out[i] = '"'
		}
	}
	return string(out)
}

// replaceIdentAll replaces every code-region occurrence of a literal token
// (case-insensitive) with repl. Used for parameterless functions like
// UTC_TIMESTAMP() and NOW().
func replaceIdentAll(sql, token, repl string) string {
	lower := strings.ToLower(token)
	m := codeMask(sql)
	var b strings.Builder
	// Single forward pass: never re-scans the inserted replacement, so a repl
	// that contains the token (NOW() -> "(now() …)") can't loop.
	for i := 0; i < len(sql); {
		if m[i] && i+len(token) <= len(sql) && strings.ToLower(sql[i:i+len(token)]) == lower {
			b.WriteString(repl)
			i += len(token)
			continue
		}
		b.WriteByte(sql[i])
		i++
	}
	return b.String()
}

// rewriteFuncCalls finds NAME(args…) in code regions (balanced parens,
// string-aware) and replaces each with rebuild(args). It recurses on each
// argument first, so nested calls (LOCATE(CONCAT(…), x)) translate correctly.
func rewriteFuncCalls(sql, name string, rebuild func(args []string) string) string {
	lower := strings.ToLower(name)
	searchFrom := 0
	for {
		idx := findCallStart(sql, lower, searchFrom)
		if idx < 0 {
			return sql
		}
		args, end, ok := splitCallArgs(sql, idx+len(name))
		if !ok {
			searchFrom = idx + len(name) // not a call; skip past this token
			continue
		}
		for i := range args {
			args[i] = rewriteFuncCalls(strings.TrimSpace(args[i]), name, rebuild)
		}
		rebuilt := rebuild(args)
		sql = sql[:idx] + rebuilt + sql[end:]
		// Advance past the rebuilt region — nested same-name calls were already
		// handled by the recursion on args, so we never need to revisit it. This
		// guarantees forward progress (termination) even if a rebuild happened to
		// contain the function name.
		searchFrom = idx + len(rebuilt)
	}
}

// findCallStart returns the index of the next code-region, word-boundaried
// occurrence of name (case-insensitive) that is immediately followed by `(`
// (optionally after spaces), at or after `from`; or -1.
func findCallStart(sql, lowerName string, from int) int {
	m := codeMask(sql)
	for i := from; i+len(lowerName) <= len(sql); i++ {
		if !m[i] {
			continue
		}
		if strings.ToLower(sql[i:i+len(lowerName)]) != lowerName {
			continue
		}
		if i > 0 && isIdentByte(sql[i-1]) {
			continue // not a word boundary
		}
		j := i + len(lowerName)
		for j < len(sql) && (sql[j] == ' ' || sql[j] == '\t' || sql[j] == '\n') {
			j++
		}
		if j < len(sql) && sql[j] == '(' {
			return i
		}
	}
	return -1
}

// splitCallArgs, given the index just after a function name, finds the opening
// paren, walks to the matching close (string-aware), and returns the top-level
// comma-separated arguments plus the index just past the close paren.
func splitCallArgs(sql string, afterName int) (args []string, end int, ok bool) {
	m := codeMask(sql)
	i := afterName
	for i < len(sql) && sql[i] != '(' {
		if sql[i] != ' ' && sql[i] != '\t' && sql[i] != '\n' {
			return nil, 0, false
		}
		i++
	}
	if i >= len(sql) || sql[i] != '(' {
		return nil, 0, false
	}
	depth := 0
	start := i + 1
	for ; i < len(sql); i++ {
		if !m[i] {
			continue
		}
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				args = append(args, sql[start:i])
				return args, i + 1, true
			}
		case ',':
			if depth == 1 {
				args = append(args, sql[start:i])
				start = i + 1
			}
		}
	}
	return nil, 0, false
}

func ifArgs(args []string) string {
	if len(args) != 3 {
		return "IF(" + strings.Join(args, ", ") + ")" // leave loud
	}
	return "CASE WHEN " + strings.TrimSpace(args[0]) + " THEN " + strings.TrimSpace(args[1]) +
		" ELSE " + strings.TrimSpace(args[2]) + " END"
}

func valuesArgs(args []string) string {
	if len(args) != 1 {
		return "VALUES(" + strings.Join(args, ", ") + ")"
	}
	return "EXCLUDED." + strings.TrimSpace(args[0])
}

func concatArgs(args []string) string {
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return "(" + strings.Join(args, " || ") + ")"
}

func locateArgs(args []string) string {
	if len(args) != 2 {
		return "LOCATE(" + strings.Join(args, ", ") + ")" // 3-arg form not on bd's path; leave loud
	}
	return "POSITION(" + strings.TrimSpace(args[0]) + " IN " + strings.TrimSpace(args[1]) + ")"
}

func instrArgs(args []string) string {
	if len(args) != 2 {
		return "INSTR(" + strings.Join(args, ", ") + ")" // leave loud
	}
	// POSITION reverses INSTR's arg order. Placeholder numbering runs later and
	// left-to-right, so reordering two args that BOTH carry a `?` would swap their
	// $-numbers relative to the caller's bound-arg order — a silent wrong-parameter
	// mistranslation. bd only ever emits INSTR(col, ?), so guard the ambiguous case
	// by leaving it loud to fail at PREPARE rather than swap bindings.
	if strings.Contains(args[0], "?") && strings.Contains(args[1], "?") {
		return "INSTR(" + strings.Join(args, ", ") + ")"
	}
	// MySQL INSTR(haystack, needle) — note the reversed order vs LOCATE.
	return "POSITION(" + strings.TrimSpace(args[1]) + " IN " + strings.TrimSpace(args[0]) + ")"
}

// mysqlDateFormatISO8601 is the ONLY DATE_FORMAT pattern bd emits (counts.go's
// DepJSONObject renders created_at as RFC3339). Any other format literal is
// left untranslated so it fails loudly at PREPARE rather than silently
// producing a differently-shaped timestamp.
const (
	mysqlDateFormatISO8601 = `'%Y-%m-%dT%H:%i:%sZ'`
	pgDateFormatISO8601    = `'YYYY-MM-DD"T"HH24:MI:SS"Z"'`
)

func dateFormatArgs(args []string) string {
	if len(args) != 2 || strings.TrimSpace(args[1]) != mysqlDateFormatISO8601 {
		return "DATE_FORMAT(" + strings.Join(args, ", ") + ")" // unknown format: leave loud
	}
	return "to_char(" + strings.TrimSpace(args[0]) + ", " + pgDateFormatISO8601 + ")"
}

// jsonObjectArgs maps MySQL's comma-form JSON_OBJECT(k1, v1, k2, v2, …) to
// Postgres jsonb_build_object with the same alternating key/value args. An odd
// (or empty) arg count is not a valid object literal, so it is left loud.
func jsonObjectArgs(args []string) string {
	if len(args) == 0 || len(args)%2 != 0 {
		return "JSON_OBJECT(" + strings.Join(args, ", ") + ")" // leave loud
	}
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return "jsonb_build_object(" + strings.Join(args, ", ") + ")"
}

// rewriteInsertIgnore turns `INSERT IGNORE INTO …` into `INSERT INTO … ON
// CONFLICT DO NOTHING`. bd uses it only for idempotent child-table inserts
// (labels, deps, events, comments) with no trailing clause after VALUES/SELECT.
func rewriteInsertIgnore(sql string) string {
	const ins = "insert ignore" // bd emits a single space between the keywords
	idx := indexCodeFold(sql, ins)
	if idx < 0 {
		return sql
	}
	out := sql[:idx] + "INSERT" + sql[idx+len(ins):]
	if !containsCodeFold(out, "ON CONFLICT") {
		out = strings.TrimRight(out, " \n\t") + " ON CONFLICT DO NOTHING"
	}
	return out
}

// rewriteLabelOrderBy makes label sorting explicit on Postgres. The shared
// query text stays raw (`ORDER BY label` / `ORDER BY issue_id, label`) so the
// Dolt reference path remains unchanged; the SQL backends translate it to a
// code-point collation here.
func rewriteLabelOrderBy(sql string) string {
	sql = replaceIdentAll(sql, "ORDER BY issue_id, label", `ORDER BY issue_id, label COLLATE "C"`)
	return replaceIdentAll(sql, "ORDER BY label", `ORDER BY label COLLATE "C"`)
}

// rewriteReplaceInto converts MySQL `REPLACE INTO t (c1, c2, …) VALUES …` into
// `INSERT INTO t (c1, c2, …) VALUES … ON CONFLICT (c1) DO UPDATE SET c2 =
// EXCLUDED.c2, …`. bd only uses REPLACE on key/value tables (config, metadata,
// local_metadata) where the first column is the primary key, so treating column
// 1 as the conflict target and overwriting the rest is faithful. The column
// tokens keep their backticks here; the later rewriteBackticks pass quotes them.
func rewriteReplaceInto(sql string) string {
	const rep = "replace into"
	idx := indexCodeFold(sql, rep)
	if idx < 0 {
		return sql
	}
	out := sql[:idx] + "INSERT INTO" + sql[idx+len(rep):]
	paren := strings.Index(out[idx:], "(")
	if paren < 0 {
		return out // no column list — leave loud
	}
	cols, _, ok := splitCallArgs(out, idx+paren)
	if !ok || len(cols) == 0 {
		return out
	}
	target := strings.TrimSpace(cols[0])
	sets := make([]string, 0, len(cols)-1)
	for _, c := range cols[1:] {
		c = strings.TrimSpace(c)
		sets = append(sets, c+" = EXCLUDED."+c)
	}
	tail := " ON CONFLICT (" + target + ") DO NOTHING"
	if len(sets) > 0 {
		tail = " ON CONFLICT (" + target + ") DO UPDATE SET " + strings.Join(sets, ", ")
	}
	return strings.TrimRight(out, " \n\t") + tail
}

func skipWS(sql string, i int) int {
	for i < len(sql) && (sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n' || sql[i] == '\r') {
		i++
	}
	return i
}

// wordAtFold is wordAt with case-insensitive matching (for SQL keywords).
func wordAtFold(sql string, i int, word string) bool {
	if i+len(word) > len(sql) || !strings.EqualFold(sql[i:i+len(word)], word) {
		return false
	}
	if i > 0 && isIdentByte(sql[i-1]) {
		return false
	}
	if i+len(word) < len(sql) && isIdentByte(sql[i+len(word)]) {
		return false
	}
	return true
}

func isIdentWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

// rewriteUpdateSetTargets strips the table-alias qualifier from UPDATE SET
// targets: MySQL accepts `UPDATE t a SET a.col = …`, but Postgres requires the
// SET target to be unqualified (`SET col = …`) — it reads `a` as a column
// otherwise. Only the assignment target (left of the top-level `=`) is
// unqualified; alias references on the right-hand side are left intact.
func rewriteUpdateSetTargets(sql string) string {
	m := codeMask(sql)
	start := skipWS(sql, 0)
	if !wordAtFold(sql, start, "update") {
		return sql
	}
	setPos := scanKeyword(sql, m, start, "set")
	if setPos < 0 {
		return sql
	}
	clauseStart := setPos + len("set")
	clauseEnd := scanKeyword(sql, m, clauseStart, "where")
	if clauseEnd < 0 {
		if r := scanKeyword(sql, m, clauseStart, "returning"); r >= 0 {
			clauseEnd = r
		} else {
			clauseEnd = len(sql)
		}
	}
	clause := sql[clauseStart:clauseEnd]
	cm := codeMask(clause)
	var out strings.Builder
	depth, segStart := 0, 0
	for j := 0; j < len(clause); j++ {
		if cm[j] {
			switch clause[j] {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					out.WriteString(unqualifySetTarget(clause[segStart:j]))
					out.WriteByte(',')
					segStart = j + 1
				}
			}
		}
	}
	out.WriteString(unqualifySetTarget(clause[segStart:]))
	return sql[:clauseStart] + out.String() + sql[clauseEnd:]
}

// scanKeyword returns the index of the next depth-0, code-region occurrence of
// keyword (word-boundaried, case-insensitive) at or after from, or -1.
func scanKeyword(sql string, m []bool, from int, keyword string) int {
	depth := 0
	for j := from; j < len(sql); j++ {
		if !m[j] {
			continue
		}
		switch sql[j] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && wordAtFold(sql, j, keyword) {
			return j
		}
	}
	return -1
}

func unqualifySetTarget(seg string) string {
	cm := codeMask(seg)
	depth, eq := 0, -1
	for j := 0; j < len(seg) && eq < 0; j++ {
		if !cm[j] {
			continue
		}
		switch seg[j] {
		case '(':
			depth++
		case ')':
			depth--
		case '=':
			if depth == 0 {
				eq = j
			}
		}
	}
	if eq < 0 {
		return seg
	}
	lhs := seg[:eq]
	trimmed := strings.TrimSpace(lhs)
	dot := strings.IndexByte(trimmed, '.')
	if dot <= 0 || !isIdentWord(trimmed[:dot]) || !isIdentWord(trimmed[dot+1:]) {
		return seg
	}
	lead := lhs[:len(lhs)-len(strings.TrimLeft(lhs, " \t\r\n"))]
	return lead + trimmed[dot+1:] + " " + seg[eq:]
}

// onConflictTargets maps a table to the columns Postgres needs as the ON
// CONFLICT arbiter (the primary/unique key MySQL's ON DUPLICATE KEY infers).
// Only tables that use a DO UPDATE upsert need an entry; DO NOTHING needs none.
var onConflictTargets = map[string]string{
	"issues":              "id",
	"wisps":               "id",
	"issue_counter":       "prefix",
	"child_counters":      "parent_id",
	"wisp_child_counters": "parent_id",
	"repo_mtimes":         "repo_path",
	"federation_peers":    "name",
}

// rewriteOnDuplicateKey converts `… ON DUPLICATE KEY UPDATE <body>` into
// Postgres `… ON CONFLICT (<target>) DO UPDATE SET <body>`, or `ON CONFLICT DO
// NOTHING` when the body is an idempotent self-assignment (`col = col`). The ON
// DUPLICATE clause is always last in the statement, so the body runs to the end.
func rewriteOnDuplicateKey(sql string) string {
	const odk = "on duplicate key update"
	i := indexCodeFold(sql, odk)
	if i < 0 {
		return sql
	}
	head := strings.TrimRight(sql[:i], " \n\t")
	body := strings.TrimSpace(sql[i+len(odk):])
	if isSelfAssign(body) {
		return head + " ON CONFLICT DO NOTHING"
	}
	// Inside the upsert body, VALUES(col) means the proposed row value
	// (Postgres EXCLUDED.col) and IF(c,a,b) is the ternary (CASE). Translate them
	// here, where they can't be confused with the INSERT ... VALUES keyword.
	body = rewriteFuncCalls(body, "IF", ifArgs)
	body = rewriteFuncCalls(body, "VALUES", valuesArgs)
	target := onConflictTargets[insertTable(sql)]
	if target == "" {
		// Unknown upsert target: emit a form that fails PREPARE loudly rather
		// than guessing an arbiter (surfaced by the corpus gate).
		return head + " ON CONFLICT () DO UPDATE SET " + body
	}
	return head + " ON CONFLICT (" + target + ") DO UPDATE SET " + body
}

// isSelfAssign reports whether body is a single `x = x` assignment.
func isSelfAssign(body string) bool {
	parts := strings.SplitN(body, "=", 2)
	if len(parts) != 2 {
		return false
	}
	l := strings.TrimSpace(parts[0])
	r := strings.TrimSpace(strings.TrimRight(parts[1], " \n\t"))
	return l != "" && l == r && !strings.ContainsAny(l, " (),")
}

// insertTable extracts the target table from a leading `INSERT [IGNORE] INTO
// <table>`. The table is concrete at runtime (fmt.Sprintf filled it in).
func insertTable(sql string) string {
	toks := strings.Fields(sql)
	for i := 0; i+1 < len(toks); i++ {
		if strings.EqualFold(toks[i], "INTO") {
			t := strings.Trim(toks[i+1], "`\"")
			if p := strings.IndexAny(t, "("); p >= 0 {
				t = t[:p]
			}
			return t
		}
	}
	return ""
}

// rewriteLiteralJSONUnquote handles the fixed-path form
// JSON_UNQUOTE(JSON_EXTRACT(<expr>, '$.<key>')) (the waits-for gate check),
// rewriting it to (<expr> #>> '{<key>}'). Only the single-level literal-path
// variant is handled; dynamic ?-path metadata filters translate separately.
func rewriteLiteralJSONUnquote(sql string) string {
	const marker = "json_unquote(json_extract("
	for {
		i := indexCodeFold(sql, marker)
		if i < 0 {
			return sql
		}
		open := i + len("JSON_UNQUOTE(") + len("JSON_EXTRACT")
		args, extractEnd, ok := splitCallArgs(sql, open)
		if !ok || len(args) != 2 || extractEnd >= len(sql) || sql[extractEnd] != ')' {
			return sql // malformed / not the literal form: leave loud
		}
		key := jsonPathToKey(strings.TrimSpace(args[1]))
		if key == "" {
			return sql // dynamic path: leave for the param translator
		}
		repl := "(" + strings.TrimSpace(args[0]) + " #>> '{" + key + "}')"
		sql = sql[:i] + repl + sql[extractEnd+1:]
	}
}

// rewriteDynamicJSONPath translates the dynamic ?-path metadata filters
// sqlbuild emits (filter.go): the has-key form JSON_EXTRACT(metadata, ?) IS NOT
// NULL and the equality form JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?. The
// MySQL path argument (e.g. '$.sprint' or '$."gc.routed_to"') is a bound
// parameter, so the bare-key conversion happens at runtime via the
// bd_mysql_jsonkey SQL function (defined in the Postgres schema):
//
//	JSON_UNQUOTE(JSON_EXTRACT(<e>, ?)) -> (<e> ->> bd_mysql_jsonkey(?))
//	JSON_EXTRACT(<e>, ?)               -> (<e> -> bd_mysql_jsonkey(?))
//
// The unquote form is rewritten first so its inner JSON_EXTRACT is consumed
// before the bare-extract pass runs. Literal-path forms (a quoted '$.key'
// argument) are left untouched here; rewriteLiteralJSONUnquote already handled
// them and any bare literal JSON_EXTRACT is left loud.
func rewriteDynamicJSONPath(sql string) string {
	sql = rewriteDynamicJSONUnquote(sql)
	sql = rewriteDynamicJSONExtract(sql)
	return sql
}

func rewriteDynamicJSONUnquote(sql string) string {
	const marker = "json_unquote(json_extract("
	searchFrom := 0
	for {
		i := indexCodeFoldFrom(sql, marker, searchFrom)
		if i < 0 {
			return sql
		}
		open := i + len("JSON_UNQUOTE(") + len("JSON_EXTRACT")
		args, extractEnd, ok := splitCallArgs(sql, open)
		if !ok || len(args) != 2 || extractEnd >= len(sql) || sql[extractEnd] != ')' {
			searchFrom = i + len(marker) // malformed / not this form: skip
			continue
		}
		if strings.TrimSpace(args[1]) != "?" {
			searchFrom = i + len(marker) // literal path: leave for the literal pass
			continue
		}
		repl := "(" + strings.TrimSpace(args[0]) + " ->> bd_mysql_jsonkey(?))"
		sql = sql[:i] + repl + sql[extractEnd+1:] // +1 consumes the outer JSON_UNQUOTE ')'
		searchFrom = i + len(repl)
	}
}

func rewriteDynamicJSONExtract(sql string) string {
	searchFrom := 0
	for {
		idx := findCallStart(sql, "json_extract", searchFrom)
		if idx < 0 {
			return sql
		}
		args, end, ok := splitCallArgs(sql, idx+len("JSON_EXTRACT"))
		if !ok || len(args) != 2 || strings.TrimSpace(args[1]) != "?" {
			searchFrom = idx + len("JSON_EXTRACT") // literal / malformed path: leave loud
			continue
		}
		repl := "(" + strings.TrimSpace(args[0]) + " -> bd_mysql_jsonkey(?))"
		sql = sql[:idx] + repl + sql[end:]
		searchFrom = idx + len(repl)
	}
}

// rewriteCastChar rewrites CAST(<expr> AS CHAR) and CAST(<expr> AS CHAR(n)) into
// (<expr>)::text. bd emits CAST(metadata AS CHAR) to serialize a JSON column to
// its text form (counts.go DepJSONObject). This rewrite is MANDATORY, not
// cosmetic: Postgres reads a bare CHAR as char(1), so CAST(jsonb AS CHAR)
// prepares silently and truncates the value to a single character.
func rewriteCastChar(sql string) string {
	searchFrom := 0
	for {
		idx := findCallStart(sql, "cast", searchFrom)
		if idx < 0 {
			return sql
		}
		args, end, ok := splitCallArgs(sql, idx+len("CAST"))
		if !ok || len(args) != 1 {
			searchFrom = idx + len("CAST")
			continue
		}
		expr, isChar := splitCastAsChar(args[0])
		if !isChar {
			searchFrom = idx + len("CAST") // not AS CHAR (e.g. AS SIGNED): leave loud
			continue
		}
		repl := "(" + strings.TrimSpace(expr) + ")::text"
		sql = sql[:idx] + repl + sql[end:]
		searchFrom = idx + len(repl)
	}
}

// splitCastAsChar reports whether inner has the shape "<expr> AS CHAR" or
// "<expr> AS CHAR(n)" and, if so, returns <expr>. The AS is matched at paren
// depth 0 in a code region, so an AS nested inside <expr> (e.g. a nested CAST)
// is ignored.
func splitCastAsChar(inner string) (expr string, ok bool) {
	m := codeMask(inner)
	depth := 0
	for i := 0; i < len(inner); i++ {
		if !m[i] {
			continue
		}
		switch inner[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && wordAtFold(inner, i, "as") {
			if isCharType(strings.TrimSpace(inner[i+len("as"):])) {
				return inner[:i], true
			}
			return "", false
		}
	}
	return "", false
}

// isCharType reports whether tail is a CHAR type token: "CHAR" or "CHAR(n)".
func isCharType(tail string) bool {
	if !wordAtFold(tail, 0, "char") {
		return false
	}
	rest := strings.TrimSpace(tail[len("char"):])
	if rest == "" {
		return true // CHAR
	}
	return rest[0] == '(' && rest[len(rest)-1] == ')' // CHAR(n)
}

// jsonPathToKey converts a simple literal MySQL JSON path '$.key' into the bare
// key, or "" if it is not a simple single-level literal.
func jsonPathToKey(lit string) string {
	if len(lit) < 4 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		return ""
	}
	p := lit[1 : len(lit)-1]
	if !strings.HasPrefix(p, "$.") {
		return ""
	}
	k := strings.Trim(p[2:], `"`)
	if k == "" || strings.ContainsAny(k, ".[]*") {
		return ""
	}
	return k
}

func indexCodeFold(sql, sub string) int { return indexCodeFoldFrom(sql, sub, 0) }

// indexCodeFoldFrom is indexCodeFold starting the scan at `from`. Rewrites that
// may legitimately leave a match in place (a literal path they must skip) use
// this to advance past it instead of looping forever.
func indexCodeFoldFrom(sql, sub string, from int) int {
	if from < 0 {
		from = 0
	}
	m := codeMask(sql)
	lower := strings.ToLower(sub)
	for i := from; i+len(sub) <= len(sql); i++ {
		if m[i] && strings.ToLower(sql[i:i+len(sub)]) == lower {
			return i
		}
	}
	return -1
}

func containsCodeFold(sql, sub string) bool { return indexCodeFold(sql, sub) >= 0 }
