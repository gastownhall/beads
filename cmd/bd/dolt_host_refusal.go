package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
)

// hostBdDoesNotRun describes host, a configured Dolt server host, for a
// refusal of something only bd's own server allows. notBd is false when bd
// runs its server at host (configfile.BdRunsServerAt), and the caller
// proceeds. Otherwise desc completes "the configured Dolt server host ",
// remote says whether the server is on another machine, and listenAddr is
// where a server on this machine listens, as a client dials it. host comes
// from metadata.json, config.yaml or the environment, so desc and listenAddr
// name it as remedy.PrintedValue spells it.
func hostBdDoesNotRun(host string) (desc, listenAddr string, remote, notBd bool) {
	bare := configfile.UnbracketHost(host)
	switch configfile.ClassifyDoltHost(host) {
	case configfile.DoltHostBdServer:
		return "", "", false, false
	case configfile.DoltHostIPv6Loopback:
		return fmt.Sprintf("is the IPv6 loopback (%s)", remedy.PrintedValue(host)), remedy.PrintedValue("[" + bare + "]"), false, true
	case configfile.DoltHostOtherLoopback:
		return fmt.Sprintf("is %s, a loopback address bd's own server does not listen on", remedy.PrintedValue(host)), remedy.PrintedValue(bare), false, true
	default:
		return fmt.Sprintf("is remote (%s)", remedy.PrintedValue(host)), "", true, true
	}
}

// migrationHostRefusal refuses a dolt-mode migration of a workspace whose
// configured Dolt server bd does not run (configfile.BdRunsServerAt), or
// returns nil. A forward migration (to proxied-server) re-points the
// workspace at the local Dolt root root, the data of bd's own server, and
// would hide that server's data; a reverse one (from proxied-server) leaves
// root behind and dials that server from the next command.
//
// It reads only metadata and the environment, so it holds on a run resumed
// from a journal as on a fresh one: a journal records no host. Callers run it
// until the run switches dolt_mode; past that point the workspace already
// reads its new mode, and finishing the migration changes nothing about which
// server it reaches.
//
// journal is the path of the run's migration journal, or "" on a run without
// one. A refused run with a journal names it: the earlier run that wrote it
// stopped before switching dolt_mode, and clearing the host resumes it.
func migrationHostRefusal(cfg *configfile.Config, forward bool, root, journal string) error {
	host := cfg.GetDoltServerHost()
	desc, listenAddr, remote, notBd := hostBdDoesNotRun(host)
	if !notBd {
		return nil
	}
	const clearHost = "dolt_server_host / dolt.host / BEADS_DOLT_SERVER_HOST"
	resumed := ""
	if journal != "" {
		resumed = fmt.Sprintf("\nAn earlier run of this migration stopped before switching dolt_mode and left its journal at %s; clearing the host resumes that migration. Other bd commands do not read the journal.", journal)
	}
	switch {
	case !forward:
		return HandleError("the configured Dolt server host %s; after this migration bd would dial that server instead of running its own over the local Dolt root %s, and that root's data would be left behind.\nTo run bd's own server over that root, clear %s first%s", desc, root, clearHost, resumed)
	case remote:
		return HandleError("the configured Dolt server host %s; this migration only re-points the proxy at the local Dolt root and would abandon the remote data.\nMigrate on the server host itself, or clear %s first%s", desc, clearHost, resumed)
	default:
		return HandleError("the configured Dolt server host %s; this migration only re-points the proxy at the local Dolt root, which holds the data of bd's own server (it listens on 127.0.0.1 only), and would abandon the data of the server at %s.\nTo migrate bd's own server instead, clear %s first%s", desc, listenAddr, clearHost, resumed)
	}
}

// migrationJournalForRefusal is the journal path migrationHostRefusal names:
// the run's journal when it has one, else "".
func migrationJournalForRefusal(beadsDir string, j *migrateJournal) string {
	if j == nil {
		return ""
	}
	return migrateJournalPath(beadsDir)
}
