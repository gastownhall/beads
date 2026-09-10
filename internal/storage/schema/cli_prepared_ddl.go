package schema

import (
	"regexp"
	"strings"
)

// This file is the going-forward guard for the defect class cli_migrations.go
// documents: on a pre-2.3 Dolt CLI, a PREPARE'd ALTER TABLE in the batch path
// silently does nothing while EXECUTE reports success (dolthub/dolt#11345).
// A migration that relies on one therefore never reaches the fresh-schema
// bundle, and the bundle drifts from the runtime committed schema.
//
// Two instances shipped before anyone noticed — 0060 (gastownhall/beads#5903)
// and 0065 (this file's change) — six days apart in authorship, both found by
// accident. TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified
// turns "found by accident" into "cannot be added without a decision":
// preparedALTERTableStatements below reads the substituted bundle text, and
// every main-plane migration that still carries a PREPARE'd ALTER there must
// appear in preparedALTERSafeOnFreshBundle with a written reason.
//
// It deliberately needs no Dolt binary. The parity oracle
// (TestCLIBundleMatchesRuntimeCommittedSchema) is the semantic check for the
// same drift, but it went blind twice at once: it excludes wisp_ tables by
// name, and a CI drift to Dolt 2.3.x — which fixed #11345 — made the bundle
// match anyway. A string assertion over generated SQL survives both.
//
// SCOPE IS ALTER TABLE, and it is deliberately a superset of what actually
// breaks. Probed on dolt 2.1.8 with a direct ALTER in the same script as a
// control that must land, prepared statements split like this:
//
//	vanish:  ADD COLUMN, MODIFY COLUMN, DROP COLUMN, ADD PRIMARY KEY
//	execute: ALTER COLUMN ... DROP DEFAULT, RENAME COLUMN, ADD/DROP INDEX,
//	         ADD CONSTRAINT (FK and CHECK), CREATE INDEX, RENAME TABLE, CALL
//
// Flagging every prepared ALTER TABLE rather than just the first row costs one
// justified inventory entry (0050) and buys not having to guess whether some
// ALTER form nobody probed belongs in the first list. Prepared DML against a
// real table vanishes too, but that hazard already has an owner in
// scripts/check-migration-hygiene.sh check E, which flags it at source rather
// than in the bundle.

// preparedALTER is one ALTER TABLE statement that reaches the Dolt CLI only
// through PREPARE/EXECUTE.
type preparedALTER struct {
	// Line is the 1-based line in the scanned SQL where the PREPARE appears.
	Line int
	// Statement is the quoted ALTER text the prepared variable may carry.
	Statement string
}

var (
	preparedSetVarRe      = regexp.MustCompile(`(?is)^\s*set\s+@([a-z0-9_]+)\s*:?=`)
	preparedFromVarRe     = regexp.MustCompile(`(?is)(^|[^a-z0-9_])prepare\s+[a-z0-9_]+\s+from\s+@([a-z0-9_]+)`)
	preparedFromLiteralRe = regexp.MustCompile(`(?is)(^|[^a-z0-9_])prepare\s+[a-z0-9_]+\s+from\s+'`)
	preparedAlterTableRe  = regexp.MustCompile(`(?is)^alter\s+table[^a-z0-9_]`)
)

// preparedALTERTableStatements returns every ALTER TABLE that sqlText executes
// via PREPARE/EXECUTE rather than directly.
//
// Statements are split on semicolons outside string literals, so a multi-line
// `SET @sql = IF(...)` reads as one statement, and a `PREPARE stmt FROM @sql`
// is resolved against the most recent assignment to that variable — the
// ordering every migration in this tree uses. Both the variable form and the
// `PREPARE stmt FROM '<literal>'` form are recognized. Every string literal in
// the assignment is tested, not just the first, because the common shape is a
// two-branch `IF(cond, '<ddl>', 'SELECT 1')` and either branch may be the DDL;
// CONCAT'd fragments (0055's `CONCAT('ALTER TABLE issues ', @clauses)`) are
// caught by the same rule.
//
// It is a scanner, not a SQL parser. A prepared ALTER assembled entirely from
// variables — with no literal in the assignment beginning "ALTER TABLE" —
// would slip past, as would one whose assignment embeds a literal semicolon.
// Neither shape exists in this tree. The check is a floor on new migrations,
// not a proof about arbitrary SQL.
func preparedALTERTableStatements(sqlText string) []preparedALTER {
	assigned := make(map[string]string)
	var hits []preparedALTER

	for _, stmt := range splitSQLStatements(sqlText) {
		if m := preparedSetVarRe.FindStringSubmatch(stmt.text); m != nil {
			assigned[strings.ToLower(m[1])] = stmt.text
			continue
		}

		source := ""
		switch {
		case preparedFromVarRe.MatchString(stmt.text):
			m := preparedFromVarRe.FindStringSubmatch(stmt.text)
			source = assigned[strings.ToLower(m[2])]
		case preparedFromLiteralRe.MatchString(stmt.text):
			source = stmt.text
		default:
			continue
		}

		for _, lit := range sqlStringLiterals(source) {
			if preparedAlterTableRe.MatchString(strings.TrimSpace(lit) + " ") {
				hits = append(hits, preparedALTER{Line: stmt.line, Statement: strings.TrimSpace(lit)})
			}
		}
	}
	return hits
}

type sqlStatement struct {
	line int
	text string
}

// sqlSplitter is the state splitSQLStatements threads across the bytes of the
// input: the statement being accumulated, the current and statement-start line
// numbers, and whether the cursor sits inside a '...' literal or a `--` comment.
// Pulling the per-byte transition into its own method keeps each concern
// (statement flushing, byte dispatch) small enough to read on its own.
type sqlSplitter struct {
	out       []sqlStatement
	cur       strings.Builder
	line      int
	startLine int
	inString  bool
	inComment bool
}

// flush appends the accumulated statement (trimmed) when non-empty and resets
// the builder to begin the next statement at the current line.
func (s *sqlSplitter) flush() {
	if text := strings.TrimSpace(s.cur.String()); text != "" {
		s.out = append(s.out, sqlStatement{line: s.startLine, text: text})
	}
	s.cur.Reset()
	s.startLine = s.line
}

// step consumes the byte at sqlText[i] and returns how many additional bytes
// it absorbed beyond that one — only an escaped quote inside a literal (a
// doubled single-quote) consumes a second byte — so the caller can advance its
// index. The branch order is significant and mirrors the original if/else-if
// chain: a newline ends a `--` comment before any in-comment byte is dropped,
// and a comment starts only outside a literal.
func (s *sqlSplitter) step(sqlText string, i int) int {
	c := sqlText[i]
	switch {
	case c == '\n':
		s.line++
		s.inComment = false
		if s.cur.Len() == 0 {
			s.startLine = s.line
		}
		s.cur.WriteByte(' ')
	case s.inComment:
		// Dropped: every byte up to the newline that closes the comment.
	case !s.inString && c == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-':
		s.inComment = true
	case c == '\'':
		// A doubled quote inside a literal is an escaped quote, not a
		// terminator: 0049 spells DEFAULT '' as DEFAULT ''''.
		if s.inString && i+1 < len(sqlText) && sqlText[i+1] == '\'' {
			s.cur.WriteString("''")
			return 1
		}
		s.inString = !s.inString
		s.cur.WriteByte(c)
	case c == ';' && !s.inString:
		s.flush()
	default:
		if s.cur.Len() == 0 && (c == ' ' || c == '\t' || c == '\r') {
			s.startLine = s.line
		}
		s.cur.WriteByte(c)
	}
	return 0
}

// splitSQLStatements breaks sqlText at semicolons that are outside a string
// literal, dropping `--` comments (also only outside a literal, since 0053
// builds MD5 fragments that contain neither but 0049's quoted DDL does embed
// doubled quotes). Each statement carries the line its first character sits on.
func splitSQLStatements(sqlText string) []sqlStatement {
	s := sqlSplitter{line: 1, startLine: 1}
	for i := 0; i < len(sqlText); {
		i += s.step(sqlText, i) + 1
	}
	s.flush()
	return s.out
}

// sqlStringLiterals returns the contents of every single-quoted literal in
// text, with doubled quotes left as-is (the callers only inspect the leading
// keyword).
func sqlStringLiterals(text string) []string {
	var out []string
	var cur strings.Builder
	inString := false

	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '\'' {
			if inString {
				cur.WriteByte(c)
			}
			continue
		}
		if inString && i+1 < len(text) && text[i+1] == '\'' {
			cur.WriteString("''")
			i++
			continue
		}
		if inString {
			out = append(out, cur.String())
			cur.Reset()
		}
		inString = !inString
	}
	return out
}

// preparedALTERSafeOnFreshBundle is the reviewed inventory of main-plane
// migrations whose PREPARE'd ALTER TABLE may stay in the CLI fresh bundle,
// each with the measured reason a silent no-op there is harmless.
//
// There are exactly two acceptable reasons, and a new entry has to be one of
// them:
//
//	(a) The migration's guard does not fire on a fresh database. Most of these
//	    exist to repair databases that drifted in the field; on an empty one
//	    the INFORMATION_SCHEMA probe is already satisfied, the prepared text is
//	    'SELECT 1', and whether the CLI runs it is immaterial.
//	(b) The ALTER form is one that does execute on the pinned CLI (see the
//	    measured split in this file's header).
//
// A migration that fits neither needs a direct-DDL override in
// cliCompatibleMigrationSQL. That is what 0060 (gastownhall/beads#5903) and
// 0065 needed, and neither is listed here.
//
// Every entry below was measured on 2026-08-21, not argued: build the bundle
// truncated to just before migration N, snapshot information_schema, apply N's
// frozen text on dolt 2.3.1 (where prepared DDL always executes), and diff.
// 0065's un-overridden text moves that diff, which is what makes the empty
// diffs for the (a) entries mean something. Redo it before adding an entry --
// a wrong justification here is invisible to every test in this package.
var preparedALTERSafeOnFreshBundle = map[string]string{
	"0037_uuid_primary_keys.up.sql": "(a) Rekeys events/comments/issue_snapshots ids only where the column is still " +
		"BIGINT; squashed base 0001 already creates them CHAR(36). Measured: applying 0037 to the fresh bundle " +
		"changes nothing.",
	"0038_drop_hop_columns.up.sql": "(a) Drops issues/wisps quality_score and crystallizes, which 0001 never creates. " +
		"Measured: applying 0038 to the fresh bundle changes nothing.",
	"0042_add_on_update_cascade.up.sql": "(a) Re-adds the aux-table foreign keys with ON UPDATE CASCADE only where " +
		"they lack it; 0001 and 0008's override already emit the CASCADE form. Measured: applying 0042 to the fresh " +
		"bundle changes nothing.",
	"0047_recompute_mixed_is_blocked.up.sql": "(a) Splits wisp_dependencies.depends_on_id only where the pre-split " +
		"column survives; 0021 creates the table already split. Measured: applying 0047 to the fresh bundle changes " +
		"nothing.",
	"0050_dependencies_deterministic_id.up.sql": "(b) Its guard DOES fire on a fresh bundle -- 0043's override " +
		"recreates dependencies.id with DEFAULT (UUID()) -- but the statement is ALTER COLUMN ... DROP DEFAULT, " +
		"which executes through the CLI batch path on 2.1.8/2.2.0. Measured: the default is gone from the bundle " +
		"built by the pinned CLI, and the pre-2.3 and 2.3.1 bundles agree on that column.",
	"0057_events_value_columns_idempotent_longtext.up.sql": "(a) Widens events.old_value/new_value only where still " +
		"TEXT; 0048 widens them unconditionally earlier in the bundle. Measured: applying 0057 to the fresh bundle " +
		"changes nothing.",
	"0058_heal_wisp_dependencies_split_constraints.up.sql": "(a) Re-adds wisp_dependencies' target FKs and check " +
		"constraint only where absent; 0021 creates the table with all three. Measured: applying 0058 to the fresh " +
		"bundle changes nothing.",
}
