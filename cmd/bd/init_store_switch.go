package main

import (
	"fmt"
	"io"
	"os"

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
// It compares the database name and nothing else about the endpoint: host,
// port, socket, --db and shared-server location changes are out of its scope.
type initStoreSwitch struct {
	recordedDatabase  string // metadata.json dolt_database, raw
	requestedDatabase string // --database, raw; "" reuses the recorded one
	databaseChanged   bool
}

// detectInitStoreSwitch reports how the request differs from the recorded
// store. cfg is the workspace's metadata as discovery reads it (nil when the
// workspace has none); database is the raw --database value.
//
// A database change needs both names: an absent --database reuses the
// recorded database (init's own name resolution prefers it over the prefix
// default), and a workspace that records none has nothing to switch away
// from. Names compare byte for byte, since two names differing only in case
// can address two different embedded database directories.
func detectInitStoreSwitch(cfg *configfile.Config, database string) initStoreSwitch {
	sw := initStoreSwitch{requestedDatabase: database}
	if cfg == nil || cfg.GetBackend() != configfile.BackendDolt {
		return sw
	}
	sw.recordedDatabase = cfg.DoltDatabase
	sw.databaseChanged = sw.recordedDatabase != "" && database != "" && database != sw.recordedDatabase
	return sw
}

func (s initStoreSwitch) switches() bool {
	return s.databaseChanged
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

	stderr io.Writer
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
	if sw := detectInitStoreSwitch(cfg, p.database); sw.switches() {
		return p.confirmStoreSwitch(sw)
	}
	return runInitReinitPreflight(p.reinitLocal, p.prefix, p.destroyToken)
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
		reinitLocal:  reinitLocal,
		beadsDir:     beadsDir,
		prefix:       prefix,
		destroyToken: destroyToken,
		database:     database,
		stderr:       os.Stderr,
	}
}
