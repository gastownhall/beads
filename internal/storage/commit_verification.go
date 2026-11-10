package storage

import (
	"fmt"
	"strings"
)

// UncommittedChangesError reports that a full-working-set commit
// (DOLT_COMMIT('-Am')) left tracked changes behind: Dolt answered with success
// or with "nothing to commit", yet dolt_status still lists tables that are not
// Beads-owned clone-local state. Those changes are saved in the working set
// but are not in HEAD, so a push will not send them (GH#5433).
//
// It is a verification finding, not a write failure: the mutations that
// dirtied the tables have landed, and replaying them would double-apply.
type UncommittedChangesError struct {
	// Tables are the dolt_status table names still dirty after the commit,
	// sorted and de-duplicated. A rename row keeps Dolt's "old -> new" form.
	Tables []string
	// IgnorePatterns are enabled dolt_ignore patterns that Beads does not
	// own. They are a hint at the cause, not proof of it or of who added
	// them, and are empty when there are none or the optional read failed.
	IgnorePatterns []string
	// Committed reports that DOLT_COMMIT did create a commit (a partial
	// commit): some changes reached HEAD, the tables above did not.
	Committed bool
}

func (e *UncommittedChangesError) Error() string {
	tables := strings.Join(e.Tables, ", ")
	var b strings.Builder
	if e.Committed {
		fmt.Fprintf(&b, "commit incomplete: a commit was created, but tracked changes remain in %s", tables)
	} else {
		fmt.Fprintf(&b, "commit incomplete: tracked changes remain in %s", tables)
	}
	b.WriteString("; these changes are not in HEAD and push will not send them")
	if len(e.IgnorePatterns) > 0 {
		fmt.Fprintf(&b, " (enabled dolt_ignore patterns not set by bd, a possible cause: %s)", strings.Join(e.IgnorePatterns, ", "))
	}
	b.WriteString("; the writes may still be saved in the working set: back up the store, inspect and correct any unwanted configuration, then retry the commit; do not replay the mutations")
	return b.String()
}
