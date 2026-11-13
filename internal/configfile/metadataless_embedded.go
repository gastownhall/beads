package configfile

import (
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/remedy"
)

// MetadatalessDatabaseError refuses to open or create an embedded Dolt
// database in a workspace whose metadata.json is absent, when the workspace's
// embedded store already holds a database the fallback cannot be told apart
// from. Without metadata, bd falls back to a default database name (usually
// "beads"); on a workspace that lost its metadata but kept its database
// (typically one named after the project), that fallback silently creates and
// hydrates a SIBLING of the workspace's real database, and later writes
// diverge between the two (GH#4381). The CLI's pre-run, the store factories
// and the public SDK open all return this error for that state, before any
// storage is opened. Match it with errors.As.
type MetadatalessDatabaseError struct {
	// BeadsDir is the workspace's .beads directory, which has no metadata.json.
	BeadsDir string
	// Attempted is the database name the caller would have opened or created.
	Attempted string
	// Existing, sorted, are the databases the workspace's embedded store
	// already holds (EmbeddedDoltDatabases).
	Existing []string
}

func (e *MetadatalessDatabaseError) Error() string {
	return fmt.Sprintf("no workspace metadata at %s: refusing to open or create embedded database %s while %s already holds %s; without metadata nothing says which database is this workspace's own — a fallback open would create or use a sibling of it, and later writes would diverge between them (GH#4381); %s; restore %s, or another metadata.json naming the intended dolt_database, and rerun; if this working tree is the wrong one, a .beads/redirect can point at the workspace that owns this data",
		remedy.PrintedValue(e.BeadsDir),
		remedy.PrintedValue(e.Attempted),
		remedy.PrintedValue(filepath.Join(e.BeadsDir, "embeddeddolt")),
		remedy.PrintedList(e.Existing, ", "),
		BackendNotOpenedGuarantee,
		remedy.PrintedValue(filepath.Join(e.BeadsDir, "metadata.json")))
}

// CheckMetadatalessEmbeddedDatabase reports whether a metadata-less workspace
// (the caller has already established that Load returned no config) may open
// or create the embedded database named database. It allows the two
// unambiguous cases: an empty store (zero databases — nothing a fallback could
// shadow, so bootstrap keeps working), and a store holding exactly that
// database (the fallback resolves to the workspace's only database, whatever
// name it was reached by). Any other hold — a sole database under another
// name, or several databases, even with the attempted name among them — is
// refused with a *MetadatalessDatabaseError naming all of them.
//
// It is discovery-only: it lists database directories through
// EmbeddedDoltDatabases (the embedded adapter's coarse .dolt marker probe) and
// never opens the engine or takes locks. Naming policy stays with the caller:
// each call site passes the database name it would actually use, so
// environment overrides and name sanitization keep their existing meaning.
func CheckMetadatalessEmbeddedDatabase(beadsDir, database string) error {
	existing := EmbeddedDoltDatabases(beadsDir)
	if len(existing) == 0 || (len(existing) == 1 && existing[0] == database) {
		return nil
	}
	return &MetadatalessDatabaseError{BeadsDir: beadsDir, Attempted: database, Existing: existing}
}
