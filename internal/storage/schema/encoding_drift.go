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
// a numbered migration may be a semantic recompute (0059 resets and rebuilds
// is_blocked), so skipping the rows it cannot read would leave derived state
// silently wrong. The operator has to recover the store out of band.
//
// The match is on the returned error only: it is evidence consistent with
// drift, not proof of which table is damaged, and a raw engine panic never
// reaches it.
type SchemaEncodingDriftError struct {
	// Migration is the migration file the pass stopped in, for example
	// "0059_recompute_null_gate_is_blocked.up.sql".
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
func (e *SchemaEncodingDriftError) UserMessage() string {
	return fmt.Sprintf("schema migration %s (%s v%d) stopped: a stored value could not be read\n", e.Migration, e.CursorTable, e.Version) +
		"\n" +
		"  Symptom: " + e.Symptom() + "\n" +
		"\n" +
		"  This is consistent with schema-encoding drift (dolthub/dolt#11131): text\n" +
		"  values written under an older Dolt that this engine cannot decode. This\n" +
		"  migration recomputes stored state, so bd will not skip the rows it cannot\n" +
		"  read: doing so would leave that state silently wrong.\n" +
		"\n" +
		"  The migration stopped on its first error and nothing was rolled back:\n" +
		"  earlier migrations in this pass may have completed, and partial changes\n" +
		"  may remain from this one.\n" +
		"\n" +
		"  Before any further writes:\n" +
		"    1. Preserve a full copy of the .beads directory and inspect the copy.\n" +
		"    2. Do not commit the working set or delete rows to get past this error.\n" +
		"    3. Follow the recovery runbook:\n" +
		"       " + SchemaEncodingDriftRecoveryURL + "\n"
}

// Hint returns the one-line next step for JSON error output.
func (e *SchemaEncodingDriftError) Hint() string {
	return "preserve a full copy of the .beads directory, then follow the operator-supervised recovery at " +
		SchemaEncodingDriftRecoveryURL + " (there is no flag that skips this migration)"
}

// isMigrationEncodingDriftErr reports whether err carries one of the
// migrationDriftSignatures, case-insensitively.
func isMigrationEncodingDriftErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range migrationDriftSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
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
// line, with control characters dropped and the length capped.
func driftSymptom(msg string) string {
	lower := strings.ToLower(msg)
	start := -1
	for _, sig := range migrationDriftSignatures {
		if i := strings.Index(lower, sig); i >= 0 && (start < 0 || i < start) {
			start = i
		}
	}
	if start < 0 {
		start = 0
	}
	// ToLower can change byte lengths only outside ASCII; the signatures are
	// ASCII, so fall back to the whole message if the offsets disagree.
	if len(lower) != len(msg) {
		start = 0
	}
	line := msg[start:]
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
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
