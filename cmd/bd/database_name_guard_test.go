package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestDatabaseMigrationRejectsTraversal: a committed metadata.json can set
// dolt_database to a path — "../escaped" cleans the embedded data dir's
// parent, so the legacy hyphenation migration would rename a directory
// outside .beads/embeddeddolt into it and rewrite metadata.json. The open
// refuses before any name-derived effect, naming dolt_database
// (bd-1tmym4 / #7888).
func TestDatabaseMigrationRejectsTraversal(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeMetadataConfig(t, beadsDir, configfile.DoltModeEmbedded, "../escaped")
	// The legacy migration's rename needs its data directory to exist; at
	// base that is what lets it move the victim in.
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")

	// The directory the traversal names: the migration's oldDir joins the
	// embedded data dir with "../escaped", which cleans to .beads/escaped —
	// outside the data directory. At base the auto-sanitize migration moves
	// it under .beads/embeddeddolt.
	victim := filepath.Join(beadsDir, "escaped")
	if err := os.MkdirAll(victim, 0o750); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(victim, "keep.txt")
	if err := os.WriteFile(keep, []byte("user data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadataBefore, err := os.ReadFile(configfile.MetadataFilePath(beadsDir))
	if err != nil {
		t.Fatal(err)
	}

	_, storeErr := newDoltStoreFromConfig(t.Context(), beadsDir)

	if storeErr == nil {
		t.Fatal("open accepted a traversing dolt_database")
	}
	if !strings.Contains(storeErr.Error(), "dolt_database") {
		t.Errorf("refusal does not name dolt_database: %v", storeErr)
	}
	if got, rerr := os.ReadFile(keep); rerr != nil || string(got) != "user data\n" {
		t.Errorf("traversal victim was moved or modified: %q (%v)", got, rerr)
	}
	if _, serr := os.Stat(filepath.Join(beadsDir, "embeddeddolt", "_escaped")); !os.IsNotExist(serr) {
		t.Errorf("migration created a sanitized directory for a refused name: %v", serr)
	}
	metadataAfter, rerr := os.ReadFile(configfile.MetadataFilePath(beadsDir))
	if rerr != nil || string(metadataAfter) != string(metadataBefore) {
		t.Errorf("metadata.json was rewritten by a refused open (%v)", rerr)
	}
}