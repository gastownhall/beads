package schema

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The updated_at census (AC-7 of gastownhall/beads#5981): every UPDATE in
// both shipped migration series that rewrites a table whose updated_at
// carries ON UPDATE CURRENT_TIMESTAMP either assigns updated_at itself or is
// covered by its step's updatedAtGuards restore target, assigning nothing
// there but the step's registered flag. Every such table a multi-table UPDATE
// joins without assigning any of its columns counts too: go-mysql-server
// stamps every row the join matches, so the UPDATE must assign that table's
// updated_at to itself or the step must list the table as joinStamped. The
// census parses SQL rather than grepping it, and fails on any UPDATE shape it
// cannot classify, so a new form has to be taught to it rather than slipping
// by.

// censusFile is one migration's up script.
type censusFile struct {
	version int
	name    string
	sql     string
}

// censusSeries is one migration series, keyed like updatedAtGuards.
type censusSeries struct {
	cursorTable string
	files       []censusFile
}

func shippedCensusSeries(t *testing.T) []censusSeries {
	t.Helper()
	var out []censusSeries
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		s := censusSeries{cursorTable: src.cursorTable}
		for _, mf := range src.list() {
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("reading %s/%s: %v", src.dir, mf.name, err)
			}
			s.files = append(s.files, censusFile{version: mf.version, name: mf.name, sql: string(data)})
		}
		if len(s.files) == 0 {
			t.Fatalf("%s: no migrations found", src.dir)
		}
		out = append(out, s)
	}
	return out
}

func TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt(t *testing.T) {
	series := shippedCensusSeries(t)

	stamped, err := censusAutoStampedTables(series)
	if err != nil {
		t.Fatal(err)
	}
	// Floor: the census must see every table known to auto-stamp, or it
	// could pass by finding none.
	for _, table := range []string{"issues", "wisps", "routes", "federation_peers"} {
		if !stamped[table] {
			t.Fatalf("census found no ON UPDATE CURRENT_TIMESTAMP on %s.updated_at; found %v", table, slices.Sorted(maps.Keys(stamped)))
		}
	}

	t.Run("shipped series are covered", func(t *testing.T) {
		for _, p := range censusUpdatedAtCoverage(series, stamped, updatedAtGuards) {
			t.Error(p)
		}
	})

	t.Run("dropping main 0047 coverage fails", func(t *testing.T) {
		guards := maps.Clone(updatedAtGuards)
		delete(guards, repairKey{"schema_migrations", 47})
		requireCensusProblem(t, censusUpdatedAtCoverage(series, stamped, guards),
			"schema_migrations/0047_recompute_mixed_is_blocked.up.sql: UPDATE issues")
	})

	t.Run("a registry flag the body does not assign fails", func(t *testing.T) {
		guards := maps.Clone(updatedAtGuards)
		key := repairKey{"schema_migrations", 47}
		spec := guards[key]
		spec.flag = "ephemeral"
		guards[key] = spec
		requireCensusProblem(t, censusUpdatedAtCoverage(series, stamped, guards),
			"schema_migrations/0047_recompute_mixed_is_blocked.up.sql: UPDATE issues assigns [is_blocked]")
	})

	t.Run("an unguarded new UPDATE fails", func(t *testing.T) {
		mutated := slices.Clone(series)
		main := mutated[0]
		main.files = append(slices.Clone(main.files), censusFile{
			version: 9999,
			name:    "9999_rename_routes.up.sql",
			sql:     "-- a new migration\nUPDATE routes SET prefix = LOWER(prefix) WHERE prefix <> LOWER(prefix);\n",
		})
		mutated[0] = main
		requireCensusProblem(t, censusUpdatedAtCoverage(mutated, stamped, updatedAtGuards),
			"schema_migrations/9999_rename_routes.up.sql: UPDATE routes")
	})

	t.Run("dropping main 0041 join coverage fails", func(t *testing.T) {
		guards := maps.Clone(updatedAtGuards)
		delete(guards, repairKey{"schema_migrations", 41})
		requireCensusProblem(t, censusUpdatedAtCoverage(series, stamped, guards),
			"schema_migrations/0041_split_dependencies_target.up.sql: UPDATE joins issues")
	})

	t.Run("a joinStamped table the step does not join fails", func(t *testing.T) {
		guards := maps.Clone(updatedAtGuards)
		key := repairKey{"schema_migrations", 46}
		spec := guards[key]
		spec.joinStamped = []string{"wisps"}
		guards[key] = spec
		requireCensusProblem(t, censusUpdatedAtCoverage(series, stamped, guards),
			"schema_migrations/0046_add_is_blocked.up.sql: updatedAtGuards lists wisps as joinStamped")
	})

	t.Run("a new UPDATE joining an auto-stamped table fails unless it self-assigns", func(t *testing.T) {
		for _, tc := range []struct {
			sql  string
			want string
		}{
			{"UPDATE labels l JOIN issues i ON i.id = l.issue_id SET l.label = LOWER(l.label);\n", "UPDATE joins issues"},
			{"UPDATE labels l JOIN issues i ON i.id = l.issue_id SET l.label = LOWER(l.label), i.updated_at = i.updated_at;\n", ""},
		} {
			mutated := slices.Clone(series)
			main := mutated[0]
			main.files = append(slices.Clone(main.files), censusFile{version: 9999, name: "9999_relabel.up.sql", sql: tc.sql})
			mutated[0] = main
			problems := censusUpdatedAtCoverage(mutated, stamped, updatedAtGuards)
			if tc.want == "" {
				for _, p := range problems {
					t.Error(p)
				}
				continue
			}
			requireCensusProblem(t, problems, "schema_migrations/9999_relabel.up.sql: "+tc.want)
		}
	})

	t.Run("a self-assigning new UPDATE passes", func(t *testing.T) {
		mutated := slices.Clone(series)
		main := mutated[0]
		main.files = append(slices.Clone(main.files), censusFile{
			version: 9999,
			name:    "9999_recompute.up.sql",
			sql:     "SET @sql = 'UPDATE wisps w SET w.is_blocked = 0, w.updated_at = w.updated_at';\nUPDATE issues SET is_blocked = 0, updated_at = updated_at;\n",
		})
		mutated[0] = main
		for _, p := range censusUpdatedAtCoverage(mutated, stamped, updatedAtGuards) {
			t.Error(p)
		}
	})
}

func requireCensusProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Fatalf("census problems %q, want one containing %q", problems, want)
}

// censusUpdatedAtCoverage returns every coverage problem: an unsafe UPDATE
// its step's restore targets miss, an unsafe UPDATE of a restore target that
// assigns anything but the step's flag (the guard reads only that column to
// tell the body's rewrites from another writer's), an auto-stamped table an
// UPDATE joins without assigning that the step does not list as joinStamped,
// a registry entry naming no shipped step, a restore target the step never
// rewrites unsafely, a joinStamped table the step never joins that way, a
// move source the step never copies from, and any SQL the census cannot
// classify.
func censusUpdatedAtCoverage(series []censusSeries, stamped map[string]bool, guards map[repairKey]updatedAtGuardSpec) []string {
	var problems []string
	seen := map[repairKey]bool{}
	for _, s := range series {
		for _, f := range s.files {
			key := repairKey{s.cursorTable, f.version}
			seen[key] = true
			where := s.cursorTable + "/" + f.name
			updates, err := censusUpdates(f.sql)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", where, err))
				continue
			}
			spec, guarded := guards[key]
			unsafe := map[string]bool{}
			joined := map[string]bool{}
			for _, u := range updates {
				for table, columns := range u {
					if !stamped[table] || slices.Contains(columns, "updated_at") {
						continue
					}
					if len(columns) == 0 {
						// Joined, not assigned: the engine stamps every
						// row the join matches anyway.
						joined[table] = true
						if !guarded || !slices.Contains(spec.joinStamped, table) {
							problems = append(problems, fmt.Sprintf(
								"%s: UPDATE joins %s without assigning its updated_at, and go-mysql-server stamps every %s row the join matches; add `<alias>.updated_at = <alias>.updated_at` to the SET list (updatedAtGuards is only for frozen migrations)",
								where, table, table))
						}
						continue
					}
					unsafe[table] = true
					switch {
					case !guarded || !slices.Contains(spec.restore, table):
						problems = append(problems, fmt.Sprintf(
							"%s: UPDATE %s does not assign updated_at, which its ON UPDATE CURRENT_TIMESTAMP then stamps; add `updated_at = updated_at` to the SET list (updatedAtGuards is only for frozen migrations)",
							where, table))
					case !slices.Equal(columns, []string{spec.flag}):
						problems = append(problems, fmt.Sprintf(
							"%s: UPDATE %s assigns %v, but updatedAtGuards says the step rewrites only its %s flag",
							where, table, columns, spec.flag))
					}
				}
			}
			if !guarded {
				continue
			}
			for _, table := range spec.restore {
				if !unsafe[table] {
					problems = append(problems, fmt.Sprintf("%s: updatedAtGuards restores %s, but the step has no UPDATE of it that leaves updated_at unassigned", where, table))
				}
			}
			for _, table := range spec.joinStamped {
				if !joined[table] {
					problems = append(problems, fmt.Sprintf("%s: updatedAtGuards lists %s as joinStamped, but no UPDATE of the step joins it without assigning it", where, table))
				}
			}
			stripped, err := censusStripComments(f.sql)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", where, err))
				continue
			}
			for dest, source := range spec.moveSources {
				if !slices.Contains(spec.restore, dest) {
					problems = append(problems, fmt.Sprintf("%s: updatedAtGuards move source %s feeds %s, which is not a restore target", where, source, dest))
				}
				move := regexp.MustCompile(`(?is)\bINSERT\s+IGNORE\s+INTO\s+` + dest + `\b[^;]*?\bFROM\s+` + source + `\b`)
				if !move.MatchString(stripped) {
					problems = append(problems, fmt.Sprintf("%s: updatedAtGuards names %s as %s's move source, but the step has no INSERT IGNORE INTO %s ... FROM %s", where, source, dest, dest, source))
				}
			}
		}
	}
	for _, key := range slices.SortedFunc(maps.Keys(guards), func(a, b repairKey) int {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	}) {
		if !seen[key] {
			problems = append(problems, fmt.Sprintf("updatedAtGuards entry %s/%d names no shipped migration", key.cursorTable, key.version))
		}
	}
	return problems
}

var censusCreateTable = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "`?" + `(\w+)` + "`?" + `\s*\(`)
var censusAutoStamp = regexp.MustCompile(`(?is)\bupdated_at\b[^,]*\bON\s+UPDATE\s+CURRENT_TIMESTAMP\b`)

// censusAutoStampedTables returns every table some shipped CREATE TABLE gives
// an auto-stamped updated_at.
func censusAutoStampedTables(series []censusSeries) (map[string]bool, error) {
	stamped := map[string]bool{}
	for _, s := range series {
		for _, f := range s.files {
			stripped, err := censusStripComments(f.sql)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", s.cursorTable, f.name, err)
			}
			for _, stmt := range strings.Split(stripped, ";") {
				m := censusCreateTable.FindStringSubmatch(stmt)
				if m != nil && censusAutoStamp.MatchString(stmt) {
					stamped[strings.ToLower(m[1])] = true
				}
			}
		}
	}
	return stamped, nil
}

// censusStripComments drops -- comments outside string literals.
func censusStripComments(sql string) (string, error) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					b.WriteByte(c)
					i++
				} else {
					quote = 0
				}
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
			b.WriteByte(c)
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		default:
			b.WriteByte(c)
		}
	}
	if quote != 0 {
		return "", fmt.Errorf("unterminated %c quote", quote)
	}
	return b.String(), nil
}

type censusTokenKind int

const (
	censusWord censusTokenKind = iota
	censusPunct
	censusString
)

type censusToken struct {
	kind censusTokenKind
	text string // a string's text is unescaped ('' -> ')
}

func (t censusToken) is(word string) bool {
	return t.kind == censusWord && strings.EqualFold(t.text, word)
}

func (t censusToken) punct(p string) bool {
	return t.kind == censusPunct && t.text == p
}

// censusTokenize splits sql into words (identifiers, keywords, numbers,
// @variables; backticks removed), single-character punctuation and string
// literals, dropping whitespace and -- comments.
func censusTokenize(sql string) ([]censusToken, error) {
	var out []censusToken
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '\'' || c == '"' || c == '`':
			var b strings.Builder
			j := i + 1
			for ; j < len(sql); j++ {
				if sql[j] == c {
					if j+1 < len(sql) && sql[j+1] == c {
						b.WriteByte(c)
						j++
						continue
					}
					break
				}
				b.WriteByte(sql[j])
			}
			if j >= len(sql) {
				return nil, fmt.Errorf("unterminated %c quote at offset %d", c, i)
			}
			kind := censusString
			if c == '`' {
				kind = censusWord
			}
			out = append(out, censusToken{kind: kind, text: b.String()})
			i = j + 1
		case c == '_' || c == '@' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i + 1
			for j < len(sql) && (sql[j] == '_' || sql[j] == '@' || sql[j] == '$' || sql[j] >= '0' && sql[j] <= '9' || sql[j] >= 'a' && sql[j] <= 'z' || sql[j] >= 'A' && sql[j] <= 'Z') {
				j++
			}
			out = append(out, censusToken{kind: censusWord, text: sql[i:j]})
			i = j
		default:
			out = append(out, censusToken{kind: censusPunct, text: string(c)})
			i++
		}
	}
	return out, nil
}

// censusUpdate maps each table one UPDATE references to the columns it
// assigns there, lowercased and deduplicated, in SET-list order; a table the
// UPDATE only joins maps to an empty list.
type censusUpdate map[string][]string

// censusUpdates finds every UPDATE statement in sql and in the string
// literals it builds statements from (PREPARE'd SQL), recursively.
func censusUpdates(sql string) ([]censusUpdate, error) {
	tokens, err := censusTokenize(sql)
	if err != nil {
		return nil, err
	}
	var out []censusUpdate
	for i, tok := range tokens {
		if tok.kind == censusString && strings.Contains(strings.ToUpper(tok.text), "UPDATE") {
			nested, err := censusUpdates(tok.text)
			if err != nil {
				return nil, fmt.Errorf("in string literal %q: %w", censusClip(tok.text), err)
			}
			out = append(out, nested...)
			continue
		}
		if !tok.is("UPDATE") {
			continue
		}
		if i > 0 {
			prev := tokens[i-1]
			switch {
			case prev.is("ON"), prev.is("FOR"):
				// ON UPDATE CASCADE / CURRENT_TIMESTAMP; SELECT ... FOR UPDATE.
				continue
			case prev.is("KEY"), prev.is("BEFORE"), prev.is("AFTER"):
				return nil, fmt.Errorf("%s UPDATE (an upsert or trigger) is not classified by the census; teach it this form", strings.ToUpper(prev.text))
			}
		}
		u, err := censusParseUpdate(tokens[i+1:])
		if err != nil {
			return nil, fmt.Errorf("UPDATE %s: %w", censusClip(censusJoin(tokens[i+1:])), err)
		}
		out = append(out, u)
	}
	return out, nil
}

var censusJoinWords = []string{"JOIN", "STRAIGHT_JOIN", "INNER", "LEFT", "RIGHT", "CROSS", "OUTER", "NATURAL"}

func censusIsJoinWord(t censusToken) bool {
	return slices.ContainsFunc(censusJoinWords, t.is)
}

var errCensusShape = errors.New("unrecognized UPDATE shape; teach the census this form")

// censusParseUpdate parses the tokens after an UPDATE keyword up to the end of
// its statement: modifiers, table references (aliases, JOIN ... ON, comma
// joins), then the SET list up to a top-level WHERE, ORDER BY or LIMIT.
func censusParseUpdate(ts []censusToken) (censusUpdate, error) {
	for end, t := range ts {
		if t.punct(";") {
			ts = ts[:end]
			break
		}
	}
	i := 0
	for i < len(ts) && (ts[i].is("LOW_PRIORITY") || ts[i].is("IGNORE")) {
		i++
	}

	aliases := map[string]string{}
	var tables []string
	expectTable := true
	for ; i < len(ts) && !ts[i].is("SET"); i++ {
		t := ts[i]
		if expectTable {
			if t.kind != censusWord {
				return nil, fmt.Errorf("%w: table reference %q", errCensusShape, t.text)
			}
			table := t.text
			if i+2 < len(ts) && ts[i+1].punct(".") && ts[i+2].kind == censusWord {
				table = ts[i+2].text // schema-qualified
				i += 2
			}
			table = strings.ToLower(table)
			tables = append(tables, table)
			aliases[table] = table
			if i+1 < len(ts) && ts[i+1].is("AS") {
				i++
			}
			if i+1 < len(ts) && ts[i+1].kind == censusWord && !ts[i+1].is("SET") && !ts[i+1].is("ON") && !censusIsJoinWord(ts[i+1]) {
				aliases[strings.ToLower(ts[i+1].text)] = table
				i++
			}
			expectTable = false
			continue
		}
		switch {
		case t.is("JOIN"), t.is("STRAIGHT_JOIN"), t.punct(","):
			expectTable = true
		case censusIsJoinWord(t):
		case t.is("ON"):
			depth := 0
			for i+1 < len(ts) {
				n := ts[i+1]
				if depth == 0 && (n.is("SET") || n.punct(",") || censusIsJoinWord(n)) {
					break
				}
				if n.punct("(") {
					depth++
				} else if n.punct(")") {
					depth--
				}
				i++
			}
		default:
			return nil, fmt.Errorf("%w: %q in the table references", errCensusShape, t.text)
		}
	}
	if i >= len(ts) || len(tables) == 0 || expectTable {
		return nil, fmt.Errorf("%w: no SET clause after the table references", errCensusShape)
	}

	u := censusUpdate{}
	depth := 0
	start := i + 1
	assign := func(lhs []censusToken) error {
		var qualifier, column string
		switch {
		case len(lhs) == 1 && lhs[0].kind == censusWord:
			column = lhs[0].text
		case len(lhs) == 3 && lhs[0].kind == censusWord && lhs[1].punct(".") && lhs[2].kind == censusWord:
			qualifier, column = lhs[0].text, lhs[2].text
		default:
			return fmt.Errorf("%w: assignment target %q", errCensusShape, censusJoin(lhs))
		}
		var table string
		if qualifier != "" {
			var ok bool
			if table, ok = aliases[strings.ToLower(qualifier)]; !ok {
				return fmt.Errorf("%w: assignment qualifier %q names no table reference", errCensusShape, qualifier)
			}
		} else if len(tables) == 1 {
			table = tables[0]
		} else {
			return fmt.Errorf("%w: unqualified %q in a multi-table UPDATE", errCensusShape, column)
		}
		if column = strings.ToLower(column); !slices.Contains(u[table], column) {
			u[table] = append(u[table], column)
		}
		return nil
	}
	split := func(end int) error {
		item := ts[start:end]
		eq := slices.IndexFunc(item, func(t censusToken) bool { return t.punct("=") })
		if eq <= 0 || eq == len(item)-1 {
			return fmt.Errorf("%w: SET item %q", errCensusShape, censusJoin(item))
		}
		return assign(item[:eq])
	}
	for j := start; j <= len(ts); j++ {
		if j == len(ts) || depth == 0 && (ts[j].is("WHERE") || ts[j].is("ORDER") || ts[j].is("LIMIT")) {
			if err := split(j); err != nil {
				return nil, err
			}
			break
		}
		switch {
		case ts[j].punct("("):
			depth++
		case ts[j].punct(")"):
			depth--
		case depth == 0 && ts[j].punct(","):
			if err := split(j); err != nil {
				return nil, err
			}
			start = j + 1
		}
	}
	// A table the UPDATE references but assigns nothing in still gets an
	// entry: the engine applies its ON UPDATE clause to the rows the join
	// matches.
	for _, table := range tables {
		if _, ok := u[table]; !ok {
			u[table] = []string{}
		}
	}
	return u, nil
}

func censusJoin(ts []censusToken) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.text
		if t.kind == censusString {
			parts[i] = "'" + t.text + "'"
		}
	}
	return strings.Join(parts, " ")
}

func censusClip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
