package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/ui"
)

// initStoreSwitch compares the store a workspace's metadata.json records with
// the one a --reinit-local invocation is about to select (#3495).
//
// --reinit-local skips checkExistingBeadsData, and its issue-count
// confirmation returns success when the count is zero or fails. A reinit that
// names another --database therefore moved the workspace onto a new, empty
// database without confirmation whenever the old one held no issues, leaving
// its database-stored configuration (types.custom, status.custom) behind. The
// comparison reads metadata only, so the decision cannot depend on whether the
// recorded store is reachable or what it holds.
//
// It compares two axes: the engine family (embedded versus the SQL-server
// family, which takes in server and proxied-server alike) and the database
// name. Nothing else about the endpoint is compared: host, port, socket, --db
// and shared-server location changes are out of its scope.
type initStoreSwitch struct {
	recordedMode      string // metadata.json dolt_mode, lowercased; "" when absent
	targetSQLServer   bool   // the family this init resolved
	familyChanged     bool
	recordedDatabase  string // metadata.json dolt_database, raw
	requestedDatabase string // --database, raw; "" reuses the recorded one
	databaseChanged   bool
}

// doltModeFamily reports whether a recorded dolt_mode belongs to the
// SQL-server family. known is false for an absent or unrecognized mode, which
// is not defaulted to embedded: "no recorded mode" and "deliberately
// embedded" stay distinguishable.
func doltModeFamily(mode string) (sqlServer, known bool) {
	switch mode {
	case configfile.DoltModeEmbedded:
		return false, true
	case configfile.DoltModeServer, configfile.DoltModeProxiedServer:
		return true, true
	default:
		return false, false
	}
}

// detectInitStoreSwitch reports how the request differs from the recorded
// store. cfg is the workspace's metadata as discovery reads it (nil when the
// workspace has none); database is the raw --database value; targetSQLServer
// is the engine family init resolved for this run (usesSQLServer).
//
// A family change needs a recognized recorded mode; an absent one skips that
// axis but not the database axis.
//
// A database change needs both names: an absent --database reuses the
// recorded database (init's own name resolution prefers it over the prefix
// default), and a workspace that records none has nothing to switch away
// from. Names compare byte for byte, since two names differing only in case
// can address two different embedded database directories.
func detectInitStoreSwitch(cfg *configfile.Config, database string, targetSQLServer bool) initStoreSwitch {
	sw := initStoreSwitch{requestedDatabase: database, targetSQLServer: targetSQLServer}
	if cfg == nil || cfg.GetBackend() != configfile.BackendDolt {
		return sw
	}
	// Lowercased like existingWorkspaceDoltMode, which decides inheritance.
	sw.recordedMode = strings.ToLower(strings.TrimSpace(cfg.DoltMode))
	if recordedSQLServer, known := doltModeFamily(sw.recordedMode); known {
		sw.familyChanged = recordedSQLServer != targetSQLServer
	}
	sw.recordedDatabase = cfg.DoltDatabase
	sw.databaseChanged = sw.recordedDatabase != "" && database != "" && database != sw.recordedDatabase
	return sw
}

func (s initStoreSwitch) switches() bool {
	return s.familyChanged || s.databaseChanged
}

// initReinitPreflight is the confirmation --reinit-local runs while init
// holds its complete exclusive gate set, before anything is written. Its
// dependencies are fields so the decision can be tested without a terminal
// or a store.
type initReinitPreflight struct {
	reinitLocal  bool
	beadsDir     string // the workspace init writes, as init resolved it
	prefix       string // the raw --prefix the destroy token is formed from
	destroyToken string
	database     string // the raw --database value
	// targetSQLServer is the engine family this init resolved: true for
	// server, proxied-server and shared-server mode, false for embedded.
	targetSQLServer bool

	countIssues func() (int, error)
	stderr      io.Writer
}

func (p initReinitPreflight) run() error {
	if !p.reinitLocal {
		return nil
	}
	var cfg *configfile.Config
	if p.beadsDir != "" {
		var err error
		// LoadForDiscovery, not a getter that swallows errors: an unreadable
		// metadata.json must not read as a workspace that records nothing.
		if cfg, err = configfile.LoadForDiscovery(p.beadsDir); err != nil {
			return fmt.Errorf("cannot read this workspace's .beads/metadata.json before re-initializing: %v", remedy.PrintedError(err))
		}
	}
	if sw := detectInitStoreSwitch(cfg, p.database, p.targetSQLServer); sw.switches() {
		return p.confirmStoreSwitch(sw)
	}
	return runInitReinitPreflight(p.countIssues, p.prefix, p.destroyToken)
}

// confirmStoreSwitch asks for the destroy token before init moves the
// workspace onto another store. It never opens or counts either store.
func (p initReinitPreflight) confirmStoreSwitch(sw initStoreSwitch) error {
	w := p.stderr
	fmt.Fprintf(w, "\n%s Re-initializing would switch this workspace to another store.\n\n", ui.RenderWarn("WARNING:"))
	fmt.Fprintf(w, "  Recorded database:  %s\n", remedy.PrintedValue(sw.recordedDatabase))
	fmt.Fprintf(w, "  Requested database: %s\n\n", remedy.PrintedValue(sw.requestedDatabase))

	if p.destroyToken == FormatDestroyToken(p.prefix) {
		fmt.Fprintf(w, "Destroy token accepted. Proceeding with the store switch.\n")
		return nil
	}
	fmt.Fprintf(w, "Refusing to switch stores in non-interactive mode.\n")
	fmt.Fprintf(w, "  See 'bd help init-safety' for the required --destroy-token format.\n")
	return &exitError{Code: ExitDestroyTokenMissing}
}

// newInitReinitPreflight wires the preflight to the process.
func newInitReinitPreflight(reinitLocal bool, beadsDir, prefix, destroyToken, database string) initReinitPreflight {
	return initReinitPreflight{
		reinitLocal:     reinitLocal,
		beadsDir:        beadsDir,
		prefix:          prefix,
		destroyToken:    destroyToken,
		database:        database,
		targetSQLServer: usesSQLServer(),
		countIssues:     func() (int, error) { return countExistingIssues(prefix) },
		stderr:          os.Stderr,
	}
}
