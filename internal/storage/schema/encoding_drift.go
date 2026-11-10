package schema

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SchemaEncodingDriftRecoveryURL is the runbook a SchemaEncodingDriftError
// sends the operator to (docs/recovery/schema-encoding-drift.md).
const SchemaEncodingDriftRecoveryURL = "https://beads.gascity.com/recovery/schema-encoding-drift"

// migrationDriftSignatures are the returned-error phrases a numbered migration
// is refused on (gastownhall/beads#5699). "invalid hash length" is the
// dolthub/dolt#11131 decode panic as a server recovers and returns it; the
// width is deliberately not matched, for the reason isSchemaEncodingDriftErr
// gives. "malformed out-of-band adaptive value" is the error the pinned Dolt
// returns from the same adaptive-text read instead of panicking.
var migrationDriftSignatures = []string{
	"invalid hash length",
	"malformed out-of-band adaptive value",
}

// maxDriftSymptomRunes bounds the symptom a refusal quotes, so an engine that
// appends a long message to the matched phrase cannot flood the summary.
const maxDriftSymptomRunes = 160

// SchemaEncodingDriftError is returned when a numbered schema migration stops
// on an error consistent with schema-encoding drift: stored values written
// under an older Dolt that the current engine cannot decode (dolthub/dolt#11131).
// The migration stopped on its first error and nothing was rolled back.
//
// Unlike the v53 aux re-key, which can skip a drifted table and retry later,
// a numbered migration may move rows (0035, the reported case, copies events
// and other infra rows into the wisp tables) or recompute them (0059 resets
// and rebuilds is_blocked), so skipping the rows it cannot read would leave
// that state silently wrong. The operator has to recover the store out of
// band.
//
// The match is on the returned error only: it is evidence consistent with
// drift, not proof of which table is damaged, and a raw engine panic never
// reaches it.
type SchemaEncodingDriftError struct {
	// Migration is the migration file the pass stopped in, for example
	// "0035_migrate_infra_to_wisps.up.sql".
	Migration string
	// Version is that migration's number.
	Version int
	// CursorTable is the cursor source the migration belongs to:
	// "schema_migrations" or "ignored_schema_migrations".
	CursorTable string
	// Cause is the error the step returned, engine text included.
	Cause error
}

func (e *SchemaEncodingDriftError) Error() string {
	return fmt.Sprintf("schema migration %s (%s v%d) stopped on a stored value it cannot read, "+
		"consistent with schema-encoding drift (dolthub/dolt#11131): %s; "+
		"partial changes may remain, so preserve a full copy before any further writes; see %s",
		e.Migration, e.CursorTable, e.Version, e.Symptom(), SchemaEncodingDriftRecoveryURL)
}

func (e *SchemaEncodingDriftError) Unwrap() error { return e.Cause }

// Symptom is the matched engine phrase from Cause: one line, control
// characters removed, length-capped, and without any appended stack.
func (e *SchemaEncodingDriftError) Symptom() string {
	if e.Cause == nil {
		return ""
	}
	return driftSymptom(e.Cause.Error())
}

// UserMessage returns the full multi-line refusal for terminal output.
//
// The stopped step's partial writes stay uncommitted, so the next pass can
// stop sooner, at the #4566 dirty-table guard, whose message advises
// 'bd dolt commit'. That commit would record the half-applied migration, so
// the refusal quotes the guard's message and says not to.
func (e *SchemaEncodingDriftError) UserMessage() string {
	return fmt.Sprintf("schema migration %s (%s v%d) stopped: a stored value could not be read\n", e.Migration, e.CursorTable, e.Version) +
		"\n" +
		"  Symptom: " + e.Symptom() + "\n" +
		"\n" +
		"  This is consistent with schema-encoding drift (dolthub/dolt#11131): text\n" +
		"  values written under an older Dolt that this engine cannot decode. This\n" +
		"  migration rewrites stored state, so bd will not skip the rows it cannot\n" +
		"  read: doing so would leave that state silently wrong.\n" +
		"\n" +
		"  The migration stopped on its first error and nothing was rolled back:\n" +
		"  earlier migrations in this pass may have completed, and partial changes\n" +
		"  may remain from this one.\n" +
		"\n" +
		"  Before any further writes:\n" +
		"    1. Stop every bd process and agent that uses this store.\n" +
		"    2. Preserve a full copy of the store and its configuration, and work\n" +
		"       only on the copy. 'bd context --json' shows where the store lives.\n" +
		"    3. Do not commit the working set or delete rows to get past this error.\n" +
		"       A later run may report instead\n" +
		"         " + dirtyTablesGuardPrefix + ": ...\n" +
		"       and advise 'bd dolt commit'. Those tables hold this migration's\n" +
		"       partial changes: do not commit them.\n" +
		"    4. Follow the recovery runbook:\n" +
		"       " + SchemaEncodingDriftRecoveryURL + "\n"
}

// Hint returns the one-line next step for JSON error output.
func (e *SchemaEncodingDriftError) Hint() string {
	return "stop every bd process and agent using this store and preserve a full copy of the store " +
		"('bd context --json' shows where it lives); do not run 'bd dolt commit', even when a later run " +
		"reports pre-existing dirty tables: they hold this migration's partial changes. " +
		"Then follow the operator-supervised recovery at " + SchemaEncodingDriftRecoveryURL +
		" (there is no flag that skips this migration)"
}

// isMigrationEncodingDriftErr reports whether err carries one of the
// migrationDriftSignatures, case-insensitively.
func isMigrationEncodingDriftErr(err error) bool {
	return err != nil && indexDriftSignature(err.Error()) >= 0
}

// indexDriftSignature returns the byte offset in s of the earliest
// migrationDriftSignatures match, or -1. The signatures are lowercase ASCII,
// so the match folds ASCII case byte by byte on s itself: the offset is valid
// in s even when Unicode lowercasing would change its byte length.
func indexDriftSignature(s string) int {
	for i := 0; i < len(s); i++ {
		for _, sig := range migrationDriftSignatures {
			if hasASCIIFoldPrefix(s[i:], sig) {
				return i
			}
		}
	}
	return -1
}

// hasASCIIFoldPrefix reports whether s starts with the lowercase ASCII prefix,
// ignoring ASCII case.
func hasASCIIFoldPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for j := 0; j < len(prefix); j++ {
		c := s[j]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[j] {
			return false
		}
	}
	return true
}

// classifyMigrationDrift turns err, returned from the numbered migration mf of
// src, into a *SchemaEncodingDriftError when it carries a drift signature.
// Every other error, and one already classified, is returned unchanged.
func classifyMigrationDrift(err error, src migrationSource, mf migrationFile) error {
	if !isMigrationEncodingDriftErr(err) {
		return err
	}
	var already *SchemaEncodingDriftError
	if errors.As(err, &already) {
		return err
	}
	return &SchemaEncodingDriftError{Migration: mf.name, Version: mf.version, CursorTable: src.cursorTable, Cause: err}
}

// driftSymptom extracts the matched signature from msg, through the end of its
// line, with control characters dropped and the length capped. The line is
// chosen in the original text before anything is removed or cut, so an
// earlier wrapper line or a long prefix cannot push the signature out.
func driftSymptom(msg string) string {
	lines := strings.FieldsFunc(msg, func(r rune) bool { return r == '\r' || r == '\n' })
	line := ""
	if len(lines) > 0 {
		line = lines[0]
	}
	for _, l := range lines {
		if i := indexDriftSignature(l); i >= 0 {
			line = l[i:]
			break
		}
	}
	var b strings.Builder
	n := 0
	for _, r := range line {
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		if n == maxDriftSymptomRunes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
