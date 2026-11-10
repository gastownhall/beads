package storage

import (
	"strings"
)

// UncommittedChangesError reports a full embedded commit (DOLT_COMMIT '-Am')
// that left tracked changes behind in the working set (GH#5433). An enabled
// dolt_ignore pattern covering a tracked table makes Dolt skip staging that
// table while its working-set delta stays, so the commit answers success —
// committing the other dirty tables — or "nothing to commit", with data that
// will never reach HEAD and never be pushed.
//
// The fields are forensic, not an asserted cause: Tables are exactly what
// dolt_status still lists, and Patterns are the enabled ignore patterns that
// are not Beads' canonical clone-local set, which suggest where the skip
// comes from without proving who wrote them.
type UncommittedChangesError struct {
	// Tables are the dolt_status table_name values that remain changed after
	// the commit: the tracked tables whose changes are saved in the working
	// set but not in HEAD. A rename appears in Dolt's "<from> -> <to>"
	// spelling.
	Tables []string
	// Patterns are enabled dolt_ignore patterns outside Beads' canonical
	// clone-local set that plausibly cover the remaining tables. It is an
	// optional diagnostic: empty when no such pattern exists or when the
	// read that collects them failed.
	Patterns []string
	// Committed reports DOLT_COMMIT's own response. True means it reported
	// success and committed the changes outside the remaining tables — a
	// partial publication; false means it reported nothing to commit, so
	// nothing landed at all.
	Committed bool
}

func (e *UncommittedChangesError) Error() string {
	var b strings.Builder
	b.WriteString("commit incomplete: ")
	if e.Committed {
		b.WriteString("DOLT_COMMIT reported success, but tracked changes remain in ")
	} else {
		b.WriteString("tracked changes remain in ")
	}
	b.WriteString(strings.Join(e.Tables, ", "))
	b.WriteString("; these changes are not in HEAD and push will not send them")
	if len(e.Patterns) > 0 {
		b.WriteString(". Enabled dolt_ignore pattern(s) that may cover them: ")
		b.WriteString(strings.Join(e.Patterns, ", "))
		b.WriteString(" (suggested cause, not proof)")
	}
	b.WriteString(". The writes are saved in the working set: back up the store, inspect the dolt_ignore configuration, correct unwanted patterns, and retry the commit; do not replay the writes")
	return b.String()
}
