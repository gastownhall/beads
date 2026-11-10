package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// Where a metadata-less embedded workspace's database name came from.
const (
	metadatalessDatabaseFromEnv     = "named by BEADS_DOLT_SERVER_DATABASE"
	metadatalessDatabaseFromSole    = "the only database in the workspace"
	metadatalessDatabaseFromDefault = "the default; the workspace holds no database yet"
)

// metadatalessDatabaseSelection is the embedded database a workspace without
// metadata.json opens, and why (resolveMetadatalessEmbeddedDatabase).
type metadatalessDatabaseSelection struct {
	Database string
	Source   string
}

// resolveMetadatalessEmbeddedDatabase names the embedded database beadsDir
// opens when it has no metadata.json (GH#4135). Without metadata the
// workspace used to open the literal default database, creating it beside
// the one the workspace already holds: reads answered empty out of that
// phantom, and a later write could land there instead of in the real
// database. The root pre-run, the identity preview and the store factories
// all call this one resolver, so the preview and the real open name the same
// database.
//
// It only reads the workspace: it renames no directory and writes no
// metadata. Callers apply their own sanitization and migration to the name
// it returns, as they do to a name metadata.json supplies.
func resolveMetadatalessEmbeddedDatabase(beadsDir string) (metadatalessDatabaseSelection, error) {
	return selectMetadatalessEmbeddedDatabase(beadsDir, embeddeddolt.Databases(beadsDir), os.Getenv("BEADS_DOLT_SERVER_DATABASE"))
}

// selectMetadatalessEmbeddedDatabase is resolveMetadatalessEmbeddedDatabase
// over the databases the workspace holds (existing, as embeddeddolt.Databases
// names them) and the BEADS_DOLT_SERVER_DATABASE value (override).
//
// Several databases and no override name nothing safe to open, not even when
// one of them is the default, and an override that names none of the
// databases the workspace holds would create another beside them: both are
// refused, before any database opens. An override is matched as written,
// then as sanitizeDBName spells it, and selects the database as it is named
// on disk, so no open maps an existing database to a different, missing one.
func selectMetadatalessEmbeddedDatabase(beadsDir string, existing []string, override string) (metadatalessDatabaseSelection, error) {
	if override != "" {
		if len(existing) == 0 {
			return metadatalessDatabaseSelection{Database: override, Source: metadatalessDatabaseFromEnv}, nil
		}
		for _, name := range []string{override, sanitizeDBName(override)} {
			if slices.Contains(existing, name) {
				return metadatalessDatabaseSelection{Database: name, Source: metadatalessDatabaseFromEnv}, nil
			}
		}
		return metadatalessDatabaseSelection{}, fmt.Errorf("BEADS_DOLT_SERVER_DATABASE=%s names none of the databases in %s (%s), which has no %s; refusing to create another.\n%s",
			remedy.PrintedValue(override), remedy.PrintedValue(beadsDir), remedy.PrintedList(existing, ", "), configfile.ConfigFileName, metadatalessSelectionRemedy(beadsDir))
	}
	switch len(existing) {
	case 0:
		return metadatalessDatabaseSelection{Database: configfile.DefaultDoltDatabase, Source: metadatalessDatabaseFromDefault}, nil
	case 1:
		return metadatalessDatabaseSelection{Database: existing[0], Source: metadatalessDatabaseFromSole}, nil
	}
	return metadatalessDatabaseSelection{}, fmt.Errorf("%s has no %s and its embedded store holds several databases (%s); refusing to pick one.\n%s",
		remedy.PrintedValue(beadsDir), configfile.ConfigFileName, remedy.PrintedList(existing, ", "), metadatalessSelectionRemedy(beadsDir))
}

// metadatalessSelectionRemedy is the advice a refused metadata-less selection
// gives: restore the workspace's metadata, or name the database to use.
func metadatalessSelectionRemedy(beadsDir string) string {
	return fmt.Sprintf("  - Restore %s from a backup or from version control, if you keep it there\n"+
		"  - Or select an existing database for this command: BEADS_DOLT_SERVER_DATABASE=<name> bd ...",
		remedy.PrintedValue(filepath.Join(beadsDir, configfile.ConfigFileName)))
}
