package schema

import (
	"errors"
	"fmt"
	"strings"
)

// SchemaEncodingDriftRecoveryURL is the recovery runbook the drift refusal
// sends operators to. It is stable — agents and scripts key on it — and must
// match the docs page (docs/recovery/schema-encoding-drift.md); the docsync
// guard in test/docsync pins that pair.
const SchemaEncodingDriftRecoveryURL = "https://beads.gascity.com/recovery/schema-encoding-drift"

// driftSymptomMaxRunes bounds the single symptom line carried into
// operator-facing output. Server-recovered panics append engine stacks to the
// message; the refusal shows the one drift line, never the stack.
const driftSymptomMaxRunes = 160

// SchemaEncodingDriftError is returned when a numbered migration step fails
// with a dolthub/dolt#11131 schema-encoding-drift signature: a TEXT/LONGTEXT
// column whose on-disk storage-encoding tag was re-derived without rewriting
// its rows, so the migration's reads (0059's INSERT..SELECT over
// issues/dependencies/wisps/wisp_dependencies is the reported case,
// gastownhall/beads#5699) cannot decode some stored values. Databases whose
// cursor predates the guard migration hit this in the migration body, before
// the auxiliary re-key's own drift guard (aux_row_id_backfill.go) ever runs.
//
// The error names the class, not a confirmed corrupt table: the same drift
// also produces cells that decode to garbage without erroring (#4380), and no
// signature check can see those.
type SchemaEncodingDriftError struct {
	// Migration is the migration file that was applying when the drift
	// surfaced, e.g. "0059_recompute_null_gate_is_blocked.up.sql".
	Migration string
	// Version is the migration's numbered version.
	Version int
	// CursorTable is the migration source whose cursor the migration belongs
	// to ("schema_migrations" or "ignored_schema_migrations").
	CursorTable string
	// Cause is the underlying error, unwrappable and errors.Is-able.
	Cause error
}

// Error stays actionable for callers without the dedicated renderer: it names
// the drift class, the stopped migration and its cursor source, the bounded
// symptom line, and the recovery runbook.
func (e *SchemaEncodingDriftError) Error() string {
	return fmt.Sprintf("schema encoding drift (dolthub/dolt#11131) stopped migration %s on %s: %s — operator-supervised recovery: %s",
		e.Migration, e.CursorTable, e.Symptom(), SchemaEncodingDriftRecoveryURL)
}

// Unwrap keeps the original cause reachable for errors.Is/errors.As.
func (e *SchemaEncodingDriftError) Unwrap() error { return e.Cause }

// Symptom returns the bounded, single-line excerpt of the engine's own drift
// message: the first line that carries a drift signature, stripped of control
// characters and appended engine stacks, capped in length. Unwrap() retains
// the original, untruncated cause.
func (e *SchemaEncodingDriftError) Symptom() string { return driftSymptomLine(e.Cause) }

// UserMessage renders the full operator-facing refusal: what stopped, why the
// migration cannot be skipped, the partial-state warning, and the recovery
// runbook.
func (e *SchemaEncodingDriftError) UserMessage() string {
	return e.Error() + "\n" +
		"\n" +
		"  This failure is consistent with Dolt schema-encoding drift (dolthub/dolt#11131):\n" +
		"  a TEXT column's on-disk storage encoding was re-derived without rewriting its\n" +
		"  rows, so bd cannot read some stored values. It names the error class, not one\n" +
		"  confirmed corrupted table.\n" +
		"\n" +
		"  This migration cannot skip the unreadable rows and keep going: continuing would\n" +
		"  leave it half-applied with silently wrong results, so bd refuses instead.\n" +
		"\n" +
		"  The migration stopped mid-step: statements that already ran may have changed the\n" +
		"  database, and partial changes may remain. Do not retry the migration, delete\n" +
		"  rows, or commit partial results to get past this refusal. Keep a full copy of\n" +
		"  the database as it is now and inspect it before any further write.\n" +
		"\n" +
		"  Recovery is operator-supervised, never automatic:\n" +
		"        " + SchemaEncodingDriftRecoveryURL + "\n"
}

// migrationEncodingDriftSignatures are the two dolthub/dolt#11131 forms a
// numbered migration can surface as a RETURNED error (raw panics are a
// separate, unconverted driver limitation; see runMigrationStep):
//
//   - "invalid hash length" — the server engine recovers the panic per query
//     and returns it as Error 1105; the width varies with the direction of the
//     tag flip (migration 0057 records ": 19" on read and ": 1" on
//     insert/merge), so the bare phrase is matched, never a specific width or
//     the 1105 wrapper;
//   - "malformed out-of-band adaptive value" — the current Dolt pin's
//     convertToTextStorage checks the out-of-band address before building a
//     hash and returns this instead of panicking on the reported read path.
//
// Error 1105 alone matches neither: an unrelated recovered panic in the same
// server envelope must keep its generic migration-error handling.
var migrationEncodingDriftSignatures = []string{
	"invalid hash length",
	"malformed out-of-band adaptive value",
}

// isMigrationEncodingDriftErr reports whether err carries one of the
// migration-phase drift signatures, case-insensitively. It is deliberately
// separate from the auxiliary re-key's own isSchemaEncodingDriftErr
// (aux_row_id_backfill.go): that classifier preserves its narrower skip-and-
// record policy, and this one additionally recognizes the returned-error
// form the current Dolt pin produces.
func isMigrationEncodingDriftErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range migrationEncodingDriftSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// driftSymptomLine extracts the bounded single-line symptom from cause: the
// first line carrying a drift signature, or the first non-empty line when
// none does (a cause whose text was rewrapped upstream). Control characters
// are stripped and the line capped; appended engine stacks never make it in.
func driftSymptomLine(cause error) string {
	if cause == nil {
		return "unreadable stored value"
	}
	fallback := ""
	for _, line := range strings.Split(cause.Error(), "\n") {
		line = sanitizeSymptomLine(line)
		if line == "" {
			continue
		}
		if fallback == "" {
			fallback = line
		}
		if isMigrationEncodingDriftErr(errors.New(line)) {
			return line
		}
	}
	return fallback
}

// sanitizeSymptomLine trims one symptom line to printable runes and caps its
// length. A server-recovered panic message can carry control bytes; the
// refusal block must not let them redraw the terminal the operator reads.
func sanitizeSymptomLine(line string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(line) {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if r := []rune(s); len(r) > driftSymptomMaxRunes {
		return string(r[:driftSymptomMaxRunes])
	}
	return s
}
