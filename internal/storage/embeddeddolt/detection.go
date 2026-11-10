package embeddeddolt

import "github.com/steveyegge/beads/internal/configfile"

// HasRepository reports whether beadsDir contains an embedded Dolt repository.
// It is configfile.HasEmbeddedDoltRepository, which owns the adapter's coarse
// .dolt marker probe beside the metadata so that the historical SQLite
// classifier (configfile.IsHistoricalSQLiteWorkspace) can use it without
// importing this adapter: the marker must be a non-symlink directory with at
// least one entry, but entry names and private repository files are never
// interpreted or opened.
func HasRepository(beadsDir string) bool {
	return configfile.HasEmbeddedDoltRepository(beadsDir)
}

// Databases names, sorted, the databases of beadsDir's embedded Dolt
// repository: the non-symlink directories under embeddeddolt that pass
// HasRepository's .dolt marker probe. It is empty when beadsDir holds none.
// It is configfile.EmbeddedDoltDatabases.
func Databases(beadsDir string) []string {
	return configfile.EmbeddedDoltDatabases(beadsDir)
}
