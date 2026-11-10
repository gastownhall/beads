package main

import (
	"os"

	"github.com/steveyegge/beads/internal/configfile"
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
func selectMetadatalessEmbeddedDatabase(_ string, existing []string, override string) (metadatalessDatabaseSelection, error) {
	if override != "" {
		return metadatalessDatabaseSelection{Database: override, Source: metadatalessDatabaseFromEnv}, nil
	}
	if len(existing) == 1 {
		return metadatalessDatabaseSelection{Database: existing[0], Source: metadatalessDatabaseFromSole}, nil
	}
	return metadatalessDatabaseSelection{Database: configfile.DefaultDoltDatabase, Source: metadatalessDatabaseFromDefault}, nil
}
