package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// Selection sources for resolveMetadatalessEmbeddedDatabase, naming where a
// metadata-less embedded workspace's database came from (the command-level
// warning prints them).
const (
	embeddedSelectionSourceOverride = "override"
	embeddedSelectionSourceSole     = "sole"
	embeddedSelectionSourceDefault  = "default"
)

// metadatalessEmbeddedSelection is the database a workspace without
// metadata.json opens on the embedded arm, and how the selection was made.
type metadatalessEmbeddedSelection struct {
	database string
	source   string
}

// summary names the selected database for the command-level missing-config
// warning: the base behavior printed only the default, so an operator whose
// workspace adopted its sole database could not tell which one bd opened.
func (s metadatalessEmbeddedSelection) summary() string {
	switch s.source {
	case embeddedSelectionSourceOverride:
		return fmt.Sprintf("database %q from BEADS_DOLT_SERVER_DATABASE", s.database)
	case embeddedSelectionSourceSole:
		return fmt.Sprintf("existing database %q, the only embedded database in this workspace", s.database)
	default:
		return fmt.Sprintf("default database name %q (this workspace has no embedded database yet)", s.database)
	}
}

// resolveMetadatalessEmbeddedDatabase selects the database a workspace
// without metadata.json opens on the embedded arm (GH#4135). The default
// this replaces — always open "beads" — created a second, empty database next
// to the real one whenever the workspace held a differently named database,
// and every subsequent read answered out of the empty one (the split-brain of
// GH#4135 modes 1/2/5a).
//
// The rules, shared by the command path (main.go) and both store-factory
// arms (store_factory.go) so the identity preview, a read and a mutating
// open always name the same database:
//
//   - BEADS_DOLT_SERVER_DATABASE selects that database when it names an
//     existing one (or the workspace holds none, where the explicit
//     selection keeps planning workspaces on their named database).
//   - Without it, a workspace holding exactly one embedded database adopts
//     that database.
//   - With no existing database, the canonical default is kept, for fresh
//     and planning workspaces.
//   - A selection that cannot be named — several databases with nothing to
//     single one out, or an override that matches none of them — refuses
//     the open: guessing would either hide the others' issues or, worse,
//     create yet another empty database beside them.
//
// The resolver never writes: it neither renames directories nor creates or
// restores metadata.json. Opening the selected database keeps the creation
// and migration behavior the open arms already have.
func resolveMetadatalessEmbeddedDatabase(beadsDir string) (metadatalessEmbeddedSelection, error) {
	databases := embeddeddolt.Databases(beadsDir)
	if override := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); override != "" {
		if match, ok := matchExistingDatabase(databases, override); ok {
			return metadatalessEmbeddedSelection{database: match, source: embeddedSelectionSourceOverride}, nil
		}
		if len(databases) == 0 {
			return metadatalessEmbeddedSelection{database: sanitizeDBName(override), source: embeddedSelectionSourceOverride}, nil
		}
		return metadatalessEmbeddedSelection{}, errAmbiguousEmbeddedDatabases(beadsDir, databases, override)
	}
	switch len(databases) {
	case 0:
		return metadatalessEmbeddedSelection{database: configfile.DefaultDoltDatabase, source: embeddedSelectionSourceDefault}, nil
	case 1:
		return metadatalessEmbeddedSelection{database: databases[0], source: embeddedSelectionSourceSole}, nil
	default:
		return metadatalessEmbeddedSelection{}, errAmbiguousEmbeddedDatabases(beadsDir, databases, "")
	}
}

// matchExistingDatabase returns the existing database an override names:
// itself, or the database whose sanitized spelling it matches, so a legacy
// hyphenated directory stays reachable by its unsanitized name and an
// override never creates a second directory beside the one it means.
func matchExistingDatabase(databases []string, override string) (string, bool) {
	sanitized := sanitizeDBName(override)
	for _, database := range databases {
		if database == override || database == sanitized {
			return database, true
		}
	}
	return "", false
}

// errAmbiguousEmbeddedDatabases refuses the open of a metadata-less
// workspace whose database cannot be named: several databases with no
// selection that singles one out (override names the explicit selection,
// "" when none was given). Naming the workspace and the candidates lets the
// operator restore the metadata that named them; the remedies are
// deliberately the two that cannot lose data — restore metadata.json from a
// known-good copy, or point the environment at an existing database — and
// not a reinitialization, which would build an empty store over live work.
func errAmbiguousEmbeddedDatabases(beadsDir string, databases []string, override string) error {
	workspace := remedy.PrintedValue(beadsDir)
	candidates := remedy.PrintedList(databases, ", ")
	if override != "" {
		return fmt.Errorf("%s has no metadata.json and BEADS_DOLT_SERVER_DATABASE=%s names none of its embedded databases (%s); refusing to open a database rather than create a second one.\n"+
			"Restore %s from a backup or a recent version, or set BEADS_DOLT_SERVER_DATABASE to one of the existing databases",
			workspace, remedy.PrintedValue(override), candidates,
			remedy.PrintedValue(filepath.Join(beadsDir, "metadata.json")))
	}
	return fmt.Errorf("%s has no metadata.json and holds several embedded databases (%s), so bd cannot tell which one to open; refusing to open a database rather than guess.\n"+
		"Restore %s from a backup or a recent version, or select one of the existing databases with BEADS_DOLT_SERVER_DATABASE",
		workspace, candidates,
		remedy.PrintedValue(filepath.Join(beadsDir, "metadata.json")))
}
