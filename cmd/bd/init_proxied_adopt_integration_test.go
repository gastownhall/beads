//go:build cgo

package main

import (
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestProxiedServerInitAdoptSavesIdentityWithoutTeamServer pins GH#7576: outside
// --team-server, an init that ADOPTS an existing database's project identity
// never saved the adopted id back to metadata.json — only team-server mode
// did. The file was written with a locally-minted id before any DB
// connection existed, so every later clone kept that mismatched local id
// forever, while the server kept the one it adopted.
func TestProxiedServerInitAdoptSavesIdentityWithoutTeamServer(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	port := testutil.StartIsolatedDoltContainer(t)

	const prefix = "idadopt"
	database := sanitizePrefixForDB(prefix)

	// Clone A bootstraps the database and mints the identity the server
	// will keep.
	cloneA := bdProxiedInit(t, bd, prefix,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", port,
	)
	serverProjectID := readMetadataValue(t, port, database, "_project_id")
	if serverProjectID == "" {
		t.Fatalf("after clone A's bootstrap, the server has no _project_id recorded")
	}

	cloneAFileCfg, err := configfile.Load(cloneA.beadsDir)
	if err != nil || cloneAFileCfg == nil {
		t.Fatalf("load clone A's %s: %v", configfile.ConfigFileName, err)
	}
	if cloneAFileCfg.ProjectID != serverProjectID {
		t.Fatalf("clone A (the bootstrapper) metadata.json project_id = %q, want the server's %q", cloneAFileCfg.ProjectID, serverProjectID)
	}

	// Clone B inits against the SAME database (not --team-server): it must
	// ADOPT the identity clone A minted, not keep the id it locally minted
	// for itself before ever connecting.
	cloneB := bdProxiedInit(t, bd, prefix,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", port,
	)
	cloneBFileCfg, err := configfile.Load(cloneB.beadsDir)
	if err != nil || cloneBFileCfg == nil {
		t.Fatalf("load clone B's %s: %v", configfile.ConfigFileName, err)
	}
	if cloneBFileCfg.ProjectID != serverProjectID {
		t.Errorf("clone B (the adopter) metadata.json project_id = %q, want the adopted %q: "+
			"the adoption must be saved back to disk outside --team-server too", cloneBFileCfg.ProjectID, serverProjectID)
	}
}
