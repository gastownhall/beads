package configfile

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
)

// MetadatalessDatabaseError refuses to open an embedded workspace that has no
// metadata (neither metadata.json nor a legacy config.json) when the database
// the opener fell back to cannot be the one the workspace already holds:
// the embedded store holds a database other than Database, or holds several.
// Opening the fallback would create a sibling database next to the real one,
// or pick one of several, and every read or write after that diverges from
// the configured workspace (GH#4381). Match it with errors.As.
type MetadatalessDatabaseError struct {
	// BeadsDir is the workspace's .beads directory.
	BeadsDir string
	// Database is the database the opener would have opened.
	Database string
	// Found names, sorted, the databases already in the embedded store.
	Found []string
}

func (e *MetadatalessDatabaseError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "no %s or legacy %s in %s, and bd cannot safely infer which embedded database to use: it would open %s, but %s holds %s; no database was opened or created",
		ConfigFileName, LegacyConfigFileName, remedy.PrintedValue(e.BeadsDir),
		remedy.PrintedValue(e.Database),
		remedy.PrintedValue(filepath.Join(e.BeadsDir, "embeddeddolt")),
		remedy.PrintedList(e.Found, ", "))
	if len(e.Found) > 1 {
		b.WriteString("; with several databases present, one of them may hold writes the others lack, so do not delete any before checking it")
	}
	fmt.Fprintf(&b, "; restore %s with \"dolt_database\" set to the intended database, or, in a git worktree, point .beads/redirect at the workspace that holds that metadata, then retry",
		remedy.PrintedValue(filepath.Join(e.BeadsDir, ConfigFileName)))
	return b.String()
}

// CheckMetadatalessEmbeddedDatabase decides whether an opener that found no
// workspace metadata (Load returned nil, nil) may open database in beadsDir's
// embedded store. It allows an empty store and a store whose only database is
// database; for anything else it returns a *MetadatalessDatabaseError naming
// the databases found, including when database is one of several, because a
// stray sibling must not license further divergent writes.
//
// It uses the same coarse discovery as EmbeddedDoltDatabases and never opens,
// locks or writes storage. It only refuses: callers keep every other rule they
// apply to the open (require-existing, bootstrap, read-only, invalid names).
// Call it only for an embedded open of a workspace with no metadata.
func CheckMetadatalessEmbeddedDatabase(beadsDir, database string) error {
	found := EmbeddedDoltDatabases(beadsDir)
	if len(found) == 0 || len(found) == 1 && found[0] == database {
		return nil
	}
	return &MetadatalessDatabaseError{BeadsDir: beadsDir, Database: database, Found: found}
}
