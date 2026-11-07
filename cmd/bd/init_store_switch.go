package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/ui"
	"golang.org/x/term"
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

// describe prints the switch on w. It is printed whichever way the
// confirmation ends and under --quiet too, since the switch changes where the
// workspace's data lives. It states what the workspace stops reading without
// claiming to know what the recorded store holds: neither store is opened.
func (s initStoreSwitch) describe(w io.Writer) {
	recordedFamily := "not recorded"
	if sqlServer, known := doltModeFamily(s.recordedMode); known {
		recordedFamily = doltFamilyName(sqlServer)
		if s.recordedMode != recordedFamily {
			recordedFamily += " (dolt_mode " + s.recordedMode + ")"
		}
	} else if s.recordedMode != "" {
		recordedFamily = "not recognized (dolt_mode " + remedy.PrintedValue(s.recordedMode) + ")"
	}
	recordedDatabase := "not recorded"
	if s.recordedDatabase != "" {
		recordedDatabase = remedy.PrintedValue(s.recordedDatabase)
	}
	requestedDatabase := remedy.PrintedValue(s.requestedDatabase)
	if s.requestedDatabase == "" {
		requestedDatabase = recordedDatabase + " (unchanged)"
	}

	fmt.Fprintf(w, "\n%s Re-initializing would switch this workspace to another store.\n\n", ui.RenderWarn("WARNING:"))
	fmt.Fprintf(w, "  Recorded engine family:  %s\n", recordedFamily)
	fmt.Fprintf(w, "  Requested engine family: %s\n", doltFamilyName(s.targetSQLServer))
	fmt.Fprintf(w, "  Recorded database:       %s\n", recordedDatabase)
	fmt.Fprintf(w, "  Requested database:      %s\n\n", requestedDatabase)
	fmt.Fprintf(w, "  The recorded store is left in place; nothing in it is deleted. This\n")
	fmt.Fprintf(w, "  workspace would stop reading its issues, Dolt history and database-stored\n")
	fmt.Fprintf(w, "  configuration (types.custom, status.custom), none of which is copied to\n")
	fmt.Fprintf(w, "  the requested store.\n\n")
	fmt.Fprintf(w, "  To keep the recorded store, re-run without:\n")
	if s.familyChanged {
		if s.targetSQLServer {
			fmt.Fprintf(w, "    - the setting that selects server mode: --server, --shared-server or\n")
			fmt.Fprintf(w, "      --proxied-server, BEADS_DOLT_SERVER_MODE, BEADS_DOLT_SHARED_SERVER or\n")
			fmt.Fprintf(w, "      BEADS_DOLT_PROXIED_SERVER, or dolt.mode in config.yaml\n")
		} else {
			fmt.Fprintf(w, "    - the setting that selects embedded mode: BEADS_DOLT_SERVER_MODE,\n")
			fmt.Fprintf(w, "      BEADS_DOLT_SHARED_SERVER or BEADS_DOLT_PROXIED_SERVER, or dolt.mode in\n")
			fmt.Fprintf(w, "      config.yaml (unset, init keeps the recorded mode)\n")
		}
	}
	if s.databaseChanged {
		fmt.Fprintf(w, "    - --database %s\n", remedy.PrintedValue(s.requestedDatabase))
	}
	fmt.Fprintf(w, "\n  Back up the recorded store first with bd backup (see 'bd backup --help').\n")
	fmt.Fprintf(w, "  See 'bd help init-safety' for how a store switch is confirmed.\n\n")
}

// doltFamilyName names an engine family for the switch diagnostic.
func doltFamilyName(sqlServer bool) string {
	if sqlServer {
		return configfile.DoltModeServer
	}
	return configfile.DoltModeEmbedded
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

	// nonInteractive is init's resolved isNonInteractiveInit; isTerminal
	// reports whether stdin is a terminal. Both must allow a prompt.
	nonInteractive bool
	isTerminal     func() bool

	countIssues func() (int, error)
	stdin       io.Reader
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
	return p.confirmPopulatedStore()
}

// confirmStoreSwitch asks for the destroy token, or on an interactive
// terminal a typed phrase, before init moves the workspace onto another
// store. It never opens or counts either store, and a confirmation authorizes
// the switch once: no count-based prompt follows it.
func (p initReinitPreflight) confirmStoreSwitch(sw initStoreSwitch) error {
	w := p.stderr
	sw.describe(w)

	if p.destroyToken == FormatDestroyToken(p.prefix) {
		fmt.Fprintf(w, "Destroy token accepted. Proceeding with the store switch.\n")
		return nil
	}
	if !p.nonInteractive && p.isTerminal() {
		// A phrase naming the store left behind, never the destroy token:
		// ADR 0002 forbids printing the token's value.
		left := sw.recordedDatabase
		if left == "" {
			left = sw.recordedMode
		}
		fmt.Fprintf(w, "Type 'leave %s behind' to confirm: ", remedy.PrintedValue(left))
		scanner := bufio.NewScanner(p.stdin)
		scanner.Scan()
		if strings.TrimSpace(scanner.Text()) != "leave "+left+" behind" {
			fmt.Fprintf(w, "\nAborted. The workspace was NOT switched.\n")
			return &exitError{Code: ExitLocalExistsRefused}
		}
		return nil
	}
	fmt.Fprintf(w, "Refusing to switch stores in non-interactive mode.\n")
	fmt.Fprintf(w, "  See 'bd help init-safety' for the required --destroy-token format.\n")
	return &exitError{Code: ExitDestroyTokenMissing}
}

// newInitReinitPreflight wires the preflight to the process.
func newInitReinitPreflight(reinitLocal, nonInteractive bool, beadsDir, prefix, destroyToken, database string) initReinitPreflight {
	return initReinitPreflight{
		reinitLocal:     reinitLocal,
		beadsDir:        beadsDir,
		prefix:          prefix,
		destroyToken:    destroyToken,
		database:        database,
		targetSQLServer: usesSQLServer(),
		nonInteractive:  nonInteractive,
		isTerminal:      func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		countIssues:     func() (int, error) { return countExistingIssues(prefix) },
		stdin:           os.Stdin,
		stderr:          os.Stderr,
	}
}
