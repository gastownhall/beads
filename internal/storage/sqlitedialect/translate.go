// Package sqlitedialect translates bd's canonical (MySQL-dialect) SQL to SQLite and
// wraps the modernc.org/sqlite driver. SQLite is closer to MySQL than Postgres is —
// backtick identifiers, `?` placeholders, bool→integer binding, INSTR, json_object,
// REPLACE INTO, and CURRENT_TIMESTAMP (UTC) all work natively — so the translation is
// a small subset of pgdialect's: INSERT IGNORE, the ON DUPLICATE KEY upsert (standard
// ON CONFLICT, shared with Postgres), CONCAT, and unqualified UPDATE SET targets. The
// string-scanning machinery (codeMask/rewriteFuncCalls/…) is shared in shape with
// pgdialect. Isolated here; shared issueops/sqlkit and other backends are untouched.
package sqlitedialect

import (
	"regexp"
	"strings"
)

// Translate rewrites one statement from bd's MySQL dialect to SQLite. Applied at
// prepare time; statements with no MySQL-ism pass through unchanged.
func Translate(sql string) string {
	out := sql
	out = rewriteFuncCalls(out, "CONCAT", concatArgs) // CONCAT(a,b) -> (a || b)
	// SQLite has no LOCATE; INSTR(haystack, needle) is the equivalent but with the
	// arguments REVERSED vs MySQL LOCATE(needle, haystack). Run after CONCAT so a nested
	// LOCATE(CONCAT(...), path) already has its inner CONCAT rewritten. Without this the
	// recursive parent-descendant CTE (ready/blocked --parent) errors and returns nothing.
	out = rewriteFuncCalls(out, "LOCATE", locateArgs) // LOCATE(n,h) -> INSTR(h,n)
	// SQLite json_extract already returns an unquoted scalar, so MySQL's explicit
	// JSON_UNQUOTE wrapper is dropped; the inner JSON_EXTRACT is native SQLite.
	out = rewriteFuncCalls(out, "JSON_UNQUOTE", jsonUnquoteArgs)
	// MySQL JSON_ARRAYAGG(x) -> SQLite json_group_array(x). JSON_OBJECT/JSON_EXTRACT/
	// JSON_ARRAY are native SQLite (same names), so they pass through.
	out = rewriteFuncCalls(out, "JSON_ARRAYAGG", jsonArrayAggArgs)
	// SQLite has no variadic GREATEST/LEAST, but its scalar max()/min() take the
	// same (>=2 arg) form and return the largest/smallest argument. bd emits
	// GREATEST(last_child, ?) in the child-counter upsert (issueops/create.go), so
	// without this rewrite creating or importing any hierarchical dotted issue ID
	// (e.g. x-1.2) fails on the SQLite backend with "no such function: GREATEST".
	out = rewriteFuncCalls(out, "GREATEST", greatestArgs)      // GREATEST(a,b,…) -> max(a,b,…)
	out = rewriteFuncCalls(out, "LEAST", leastArgs)            // LEAST(a,b,…)     -> min(a,b,…)
	out = rewriteFuncCalls(out, "DATE_FORMAT", dateFormatArgs) // -> strftime (SQLite format codes)
	// SQLite CURRENT_TIMESTAMP is UTC, matching bd's naive-UTC storage discipline.
	out = replaceIdentAll(out, "UTC_TIMESTAMP()", "CURRENT_TIMESTAMP")
	out = replaceIdentAll(out, "NOW()", "CURRENT_TIMESTAMP")
	out = rewriteLabelOrderBy(out)
	out = rewriteInsertIgnore(out)     // INSERT IGNORE -> INSERT OR IGNORE
	out = rewriteOnDuplicateKey(out)   // ON DUPLICATE KEY UPDATE -> ON CONFLICT ...
	out = addUpdateAlias(out)          // UPDATE t alias SET -> UPDATE t AS alias SET (SQLite needs AS)
	out = rewriteUpdateSetTargets(out) // SET t.col -> SET col (SQLite forbids qualified)
	return out
}

// updateAliasRe matches an aliased UPDATE target (`UPDATE issues i SET …`), which
// SQLite rejects without the AS keyword. The no-alias form (`UPDATE t SET …`) has only
// one identifier before SET and does not match.
var updateAliasRe = regexp.MustCompile(`(?i)\bUPDATE\s+(\w+)\s+(\w+)\s+SET\b`)

func addUpdateAlias(sql string) string {
	return updateAliasRe.ReplaceAllStringFunc(sql, func(m string) string {
		g := updateAliasRe.FindStringSubmatch(m)
		if strings.EqualFold(g[2], "set") { // guard: `UPDATE t SET` — g[2] can't be SET here, but be safe
			return m
		}
		return "UPDATE " + g[1] + " AS " + g[2] + " SET"
	})
}

// replaceIdentAll replaces every code-region occurrence of a literal token
// (case-insensitive) with repl, in a single forward pass.
func replaceIdentAll(sql, token, repl string) string {
	lower := strings.ToLower(token)
	m := codeMask(sql)
	var b strings.Builder
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

func rewriteLabelOrderBy(sql string) string {
	sql = replaceIdentAll(sql, "ORDER BY issue_id, label", "ORDER BY issue_id, label COLLATE BINARY")
	return replaceIdentAll(sql, "ORDER BY label", "ORDER BY label COLLATE BINARY")
}

var insertIgnoreRe = regexp.MustCompile(`(?i)\bINSERT\s+IGNORE\s+INTO\b`)

// rewriteInsertIgnore: MySQL INSERT IGNORE INTO -> SQLite INSERT OR IGNORE INTO.
func rewriteInsertIgnore(sql string) string {
	return insertIgnoreRe.ReplaceAllString(sql, "INSERT OR IGNORE INTO")
}

// --- ON DUPLICATE KEY UPDATE -> ON CONFLICT (standard SQL, same syntax as Postgres) ---

// onConflictTargets maps a table to its ON CONFLICT arbiter (the key MySQL's ON
// DUPLICATE KEY infers). Only DO UPDATE upserts need an entry.
var onConflictTargets = map[string]string{
	"issues":              "id",
	"wisps":               "id",
	"issue_counter":       "prefix",
	"child_counters":      "parent_id",
	"wisp_child_counters": "parent_id",
	"repo_mtimes":         "repo_path",
	"federation_peers":    "name",
}

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
	// Inside the upsert body, VALUES(col) is the proposed row value (excluded.col) and
	// IF(c,a,b) is the ternary (CASE) — translated here where they can't be confused
	// with the INSERT ... VALUES keyword.
	body = rewriteFuncCalls(body, "IF", ifArgs)
	body = rewriteFuncCalls(body, "VALUES", valuesArgs)
	target := onConflictTargets[insertTable(sql)]
	if target == "" {
		return head + " ON CONFLICT () DO UPDATE SET " + body // unknown: fail loud at PREPARE
	}
	return head + " ON CONFLICT (" + target + ") DO UPDATE SET " + body
}

func isSelfAssign(body string) bool {
	parts := strings.SplitN(body, "=", 2)
	if len(parts) != 2 {
		return false
	}
	l := strings.TrimSpace(parts[0])
	r := strings.TrimSpace(strings.TrimRight(parts[1], " \n\t"))
	return l != "" && l == r && !strings.ContainsAny(l, " (),")
}

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

// --- arg rebuilders ---

// jsonUnquoteArgs unwraps JSON_UNQUOTE(x) -> x: SQLite's json_extract (the x) already
// returns unquoted scalars, so the wrapper is redundant.
func jsonUnquoteArgs(args []string) string {
	if len(args) != 1 {
		return "JSON_UNQUOTE(" + strings.Join(args, ", ") + ")"
	}
	return strings.TrimSpace(args[0])
}

// mysqlDateFormatISO8601 is the only DATE_FORMAT pattern bd emits (counts.go renders
// created_at as RFC3339). Any other literal is left loud so it fails at PREPARE rather
// than silently producing a differently-shaped timestamp.
const mysqlDateFormatISO8601 = `'%Y-%m-%dT%H:%i:%sZ'`

// dateFormatArgs: MySQL DATE_FORMAT(t,'%Y-%m-%dT%H:%i:%sZ') -> SQLite
// strftime('%Y-%m-%dT%H:%M:%SZ', t) (SQLite uses %M minutes / %S seconds).
func dateFormatArgs(args []string) string {
	if len(args) != 2 || strings.TrimSpace(args[1]) != mysqlDateFormatISO8601 {
		return "DATE_FORMAT(" + strings.Join(args, ", ") + ")"
	}
	return "strftime('%Y-%m-%dT%H:%M:%SZ', " + strings.TrimSpace(args[0]) + ")"
}

// jsonArrayAggArgs: MySQL JSON_ARRAYAGG(x) -> SQLite json_group_array(x).
func jsonArrayAggArgs(args []string) string {
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return "json_group_array(" + strings.Join(args, ", ") + ")"
}

func concatArgs(args []string) string {
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return "(" + strings.Join(args, " || ") + ")"
}

// greatestArgs: MySQL GREATEST(a, b, …) -> SQLite scalar max(a, b, …). SQLite's max
// is the aggregate with a single argument and the scalar largest-of with two or
// more, so only the multi-arg form is rewritten; a lone GREATEST(x) is left loud
// so it fails at PREPARE rather than silently collapsing into an aggregate.
func greatestArgs(args []string) string {
	if len(args) < 2 {
		return "GREATEST(" + strings.Join(args, ", ") + ")"
	}
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return "max(" + strings.Join(args, ", ") + ")"
}

// leastArgs: MySQL LEAST(a, b, …) -> SQLite scalar min(a, b, …). Same one-vs-many
// caveat as greatestArgs.
func leastArgs(args []string) string {
	if len(args) < 2 {
		return "LEAST(" + strings.Join(args, ", ") + ")"
	}
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return "min(" + strings.Join(args, ", ") + ")"
}

// locateArgs rewrites MySQL LOCATE(needle, haystack) -> SQLite INSTR(haystack, needle):
// SQLite's INSTR takes the string first and the substring second, reversed from LOCATE.
func locateArgs(args []string) string {
	if len(args) != 2 {
		return "LOCATE(" + strings.Join(args, ", ") + ")" // 3-arg form is not on bd's path; leave loud
	}
	return "INSTR(" + strings.TrimSpace(args[1]) + ", " + strings.TrimSpace(args[0]) + ")"
}

func ifArgs(args []string) string {
	if len(args) != 3 {
		return "IF(" + strings.Join(args, ", ") + ")"
	}
	return "CASE WHEN " + strings.TrimSpace(args[0]) + " THEN " + strings.TrimSpace(args[1]) +
		" ELSE " + strings.TrimSpace(args[2]) + " END"
}

// valuesArgs: VALUES(col) -> excluded.col (SQLite's proposed-row alias; case-insensitive).
func valuesArgs(args []string) string {
	if len(args) != 1 {
		return "VALUES(" + strings.Join(args, ", ") + ")"
	}
	return "excluded." + strings.TrimSpace(args[0])
}

// --- unqualified UPDATE SET targets (SQLite forbids SET t.col) ---

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
		clauseEnd = len(sql)
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

func unqualifySetTarget(seg string) string {
	eq := strings.IndexByte(seg, '=')
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

func isIdentWord(s string) bool {
	s = strings.Trim(s, "`\"")
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

func skipWS(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func wordAtFold(s string, i int, w string) bool {
	if i+len(w) > len(s) || strings.ToLower(s[i:i+len(w)]) != w {
		return false
	}
	return i+len(w) == len(s) || !isIdentByte(s[i+len(w)])
}

// scanKeyword returns the code-region index of the standalone keyword kw at/after from.
func scanKeyword(s string, m []bool, from int, kw string) int {
	for i := from; i+len(kw) <= len(s); i++ {
		if !m[i] {
			continue
		}
		if strings.ToLower(s[i:i+len(kw)]) != kw {
			continue
		}
		if i > 0 && isIdentByte(s[i-1]) {
			continue
		}
		if i+len(kw) < len(s) && isIdentByte(s[i+len(kw)]) {
			continue
		}
		return i
	}
	return -1
}

// --- shared string-scanning machinery (shape mirrors pgdialect) ---

func codeMask(sql string) []bool {
	b := []byte(sql)
	mask := make([]bool, len(b))
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
				i++
				inBlock = false
			}
		case inStr:
			if c == '\'' {
				if i+1 < len(b) && b[i+1] == '\'' {
					i++
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
				mask[i] = true
			}
		}
	}
	return mask
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func indexCodeFold(sql, sub string) int {
	m := codeMask(sql)
	lower := strings.ToLower(sub)
	for i := 0; i+len(sub) <= len(sql); i++ {
		if m[i] && strings.ToLower(sql[i:i+len(sub)]) == lower {
			return i
		}
	}
	return -1
}

func findCallStart(sql, lowerName string, from int) int {
	m := codeMask(sql)
	for i := from; i+len(lowerName) <= len(sql); i++ {
		if !m[i] || strings.ToLower(sql[i:i+len(lowerName)]) != lowerName {
			continue
		}
		if i > 0 && isIdentByte(sql[i-1]) {
			continue
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
			searchFrom = idx + len(name)
			continue
		}
		for i := range args {
			args[i] = rewriteFuncCalls(strings.TrimSpace(args[i]), name, rebuild)
		}
		rebuilt := rebuild(args)
		sql = sql[:idx] + rebuilt + sql[end:]
		searchFrom = idx + len(rebuilt)
	}
}
