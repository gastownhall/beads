package main

// The --reinit-local/--force preflight: what the re-initialization would
// destroy, counted under init's exclusive gate set, and the confirmation it
// asks for before anything is replaced (GH#5628).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/ui"
)

// countReinitTarget is countReinitTargetIssues, as a variable so tests can
// observe which workspace and database the preflight counts.
var countReinitTarget = countReinitTargetIssues

// openReinitTarget is newDoltStoreFromConfig, as a variable so tests can
// answer for the store countReinitTargetIssues opens.
var openReinitTarget = newDoltStoreFromConfig

// reinitStdinIsTerminal is stdinIsTerminal, as a variable so tests can stand
// in for a terminal.
var reinitStdinIsTerminal = stdinIsTerminal

// reinitTargetDatabase returns the database a re-initialization of beadsDir
// replaces, initDatabaseName's answer, reading beadsDir's metadata without
// migrating it.
func reinitTargetDatabase(beadsDir, prefix, database string) string {
	recorded, _ := configfile.LoadForDiscovery(beadsDir)
	return initDatabaseName(recorded, prefix, database)
}

// countReinitTargetIssues counts the issues in database dbName of beadsDir,
// the database a --reinit-local/--force re-initialization of that workspace
// is about to replace (reinitTargetDatabase), so the confirmation can show
// what will be destroyed. An error means the issues could not be counted,
// which is not evidence that there are none.
//
// It counts zero only on positive evidence that there is nothing to destroy:
// no .beads, one without beads project files (settled without opening a
// store, so nothing is created before the confirmation), a database that
// counts no issues, or, under --recreate-missing, a server-mode database the
// open reports absent (reinitTargetDatabaseAbsent). A store that reports no
// statistics has counted nothing, so that is an error too, and so is a store
// that opens another database than dbName (reinitStoreOpensTarget): what it
// counts, zero included, says nothing about dbName.
//
// Without --recreate-missing the open's not-found is an error as well. It
// says only that the server this open dialed by the workspace's
// configuration (metadata.json's dolt_server_host, a central server.json,
// config.yaml's dolt.host) does not list dbName, while init's own store
// dials --server-host, else BEADS_DOLT_SERVER_HOST, else 127.0.0.1
// (initStoreServerHost), and may find it there. Nor does the
// guardMissingServerDatabaseAt refusal before the preflight settle it: it
// asks an endpoint of its own, and does not ask at all for a workspace with
// no project_id and no local Dolt data directory. --recreate-missing is the
// operator's consent to creating a fresh database where the configured one
// is missing, so only under it does a missing database count as nothing to
// destroy.
func countReinitTargetIssues(beadsDir, dbName string) (int, error) {
	if beadsDir == "" {
		return 0, nil
	}
	info, err := os.Stat(beadsDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() || !beads.HasProjectFiles(beadsDir) {
		return 0, nil
	}
	if err := reinitStoreOpensTarget(beadsDir, dbName); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := openReinitTarget(ctx, beadsDir)
	if err != nil {
		if initAllowRecreateMissing && reinitTargetDatabaseAbsent(beadsDir, dbName, err) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = store.Close() }()

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, err
	}
	if stats == nil {
		return 0, errors.New("the store reported no issue statistics")
	}
	return stats.TotalIssues, nil
}

// reinitStoreOpensTarget returns an error unless the store the count opens for
// beadsDir opens dbName, the database init replaces. The store opens the
// database GetDoltDatabase names, for a workspace without metadata too
// (newDoltStoreFromConfig): BEADS_DOLT_SERVER_DATABASE, then metadata.json's
// dolt_database, then "beads". Init reads neither the variable nor that
// default: with dolt_database omitted it replaces the database its issue
// prefix names, and --database overrides both (initDatabaseName).
func reinitStoreOpensTarget(beadsDir, dbName string) error {
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return err
	}
	if opened := normalizeLoadedConfig(cfg).GetDoltDatabase(); opened != dbName {
		return fmt.Errorf("the workspace's store opens database %q, but re-initializing replaces database %q", opened, dbName)
	}
	return nil
}

// reinitTargetDatabaseAbsent reports whether openErr, the error the count's
// store open returned for beadsDir, proves that beadsDir is a server-mode
// workspace whose database dbName does not exist on the server its store
// dials: that server, on the open's own connection (its transport, user and
// password), did not list the database (dolt.ErrDatabaseNotFound), and there
// is no local copy of it (reinitTargetHasLocalCopy). Any other open error, an
// unreachable server included, is not proof. Nothing else is asked: a probe
// of its own would have to dial by every rule the open dials by, and one that
// took another socket, user or host would answer from another server's
// catalog. Init's own store need not dial that server, so the count takes
// this as nothing to destroy only under --recreate-missing
// (countReinitTargetIssues).
//
// It proves absence only where guardMissingServerDatabaseAt, which runs
// before the preflight, asks the same question, so it takes the guard's
// predicate, cfg.IsDoltServerMode. effectiveServerMode would also take a
// workspace whose server mode comes only from a config.yaml
// dolt.shared-server, which the guard never checks: metadata.json can still
// pin dolt_mode=embedded there (GH#2946), with the issues in an embedded
// database.
//
// The open asks about the database its configuration names, GetDoltDatabase,
// so the answer is about dbName, the database init replaces, only when that
// is dbName (reinitStoreOpensTarget): BEADS_DOLT_SERVER_DATABASE can point the
// store at another database, and with dolt_database omitted the store asks
// about "beads" while init replaces the database its issue prefix names.
func reinitTargetDatabaseAbsent(beadsDir, dbName string, openErr error) bool {
	if !errors.Is(openErr, dolt.ErrDatabaseNotFound) {
		return false
	}
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil || cfg == nil || !cfg.IsDoltServerMode() {
		return false
	}
	if cfg.GetDoltDatabase() != dbName {
		return false
	}
	return !reinitTargetHasLocalCopy(beadsDir, cfg, dbName)
}

// reinitTargetHasLocalCopy reports whether beadsDir may hold a local copy of
// database dbName anywhere bd keeps one: an embedded repository under any
// name, or a dbName directory in embeddeddolt, in the data directory the
// guard checks (DoltDirPath, which follows this process's shared-server
// mode), in beadsDir's own data directory, or in the shared server's when
// beadsDir's own configuration selects shared-server mode
// (IsSharedServerModeForDir). A dbName path that cannot be shown not to exist
// counts as a copy. Every path resolves without side effects, so asking
// creates nothing before the confirmation.
func reinitTargetHasLocalCopy(beadsDir string, cfg *configfile.Config, dbName string) bool {
	if configfile.HasEmbeddedDoltRepository(beadsDir) {
		return true
	}
	dataDirs := []string{doltserver.DoltDirPath(beadsDir), cfg.DatabasePath(beadsDir), filepath.Join(beadsDir, "embeddeddolt")}
	if doltserver.IsSharedServerModeForDir(beadsDir) {
		if shared, err := doltserver.SharedDoltPath(); err == nil {
			dataDirs = append(dataDirs, shared)
		}
	}
	for _, dir := range dataDirs {
		if _, err := os.Stat(filepath.Join(dir, dbName, ".dolt")); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// runInitReinitPreflight confirms the destructive local replacement of
// database dbName in beadsDir while the caller holds init's complete
// exclusive gate set. It proceeds without asking only when the count succeeds
// and finds nothing: a workspace whose issues cannot be counted may hold any
// number of them. It asks for the typed phrase only on a terminal, and never
// when init runs non-interactively (nonInteractive, isNonInteractiveInit's
// answer: --non-interactive, BD_NON_INTERACTIVE or CI), which skips every
// prompt; there it asks for the destroy token.
func runInitReinitPreflight(reinitLocal, nonInteractive bool, beadsDir, dbName, prefix, destroyToken string) error {
	if !reinitLocal {
		return nil
	}
	count, err := countReinitTarget(beadsDir, dbName)
	if err == nil && count == 0 {
		return nil
	}
	interactive := !nonInteractive && reinitStdinIsTerminal()
	return confirmReinitDestroy(os.Stderr, os.Stdin, interactive, beadsDir, count, err, prefix, destroyToken)
}

// confirmReinitDestroy shows what re-initializing target destroys and asks
// for the typed confirmation (interactive) or the destroy token (not). A
// non-nil countErr means the count is unknown, and the confirmation is still
// required: the databases most likely to fail the count are the ones with the
// most issues to lose.
func confirmReinitDestroy(w io.Writer, in io.Reader, interactive bool, target string, count int, countErr error, prefix, destroyToken string) error {
	unknown := countErr != nil
	fmt.Fprintf(w, "\n%s Re-initializing will destroy the existing database.\n\n", ui.RenderWarn("WARNING:"))
	fmt.Fprintf(w, "  Target: %s\n", remedy.PrintedValue(target))
	if unknown {
		fmt.Fprintf(w, "  Existing issues: unknown (could not count them: %s)\n\n", remedy.PrintedMessage(countErr))
	} else {
		fmt.Fprintf(w, "  Existing issues: %d\n\n", count)
	}
	fmt.Fprintf(w, "  This action CANNOT be undone. All issues, dependencies, and\n")
	fmt.Fprintf(w, "  Dolt commit history will be permanently lost.\n\n")
	fmt.Fprintf(w, "  Before proceeding, consider:\n")
	fmt.Fprintf(w, "    bd export > issue-export.jsonl    # Export issue records, not full DB state\n")
	fmt.Fprintf(w, "    bd dolt status              # Check if this is a server config issue\n")
	if unknown {
		fmt.Fprintf(w, "    bd doctor                   # Find out why the issues could not be counted\n")
	}
	fmt.Fprintf(w, "\n")
	if interactive {
		// An unknown count has no number to type back, so the phrase names
		// all of them; no count typed back can stand in for it.
		expected := fmt.Sprintf("destroy %d issues", count)
		if unknown {
			expected = "destroy all issues"
		}
		fmt.Fprintf(w, "Type '%s' to confirm: ", expected)
		scanner := bufio.NewScanner(in)
		scanner.Scan()
		if strings.TrimSpace(scanner.Text()) != expected {
			fmt.Fprintf(w, "\nAborted. Database was NOT modified.\n")
			return &exitError{Code: ExitLocalExistsRefused}
		}
		return nil
	}

	// Non-interactive (piped input, AI agent, etc.)
	//
	// ADR invariant (engdocs/adr/0002-init-safety-invariants.md): runtime
	// error text must not echo a complete destructive invocation. See
	// 'bd help init-safety' for the token format. This closes the
	// 58f5989bf failure class where an agent copy-pasted the suggestion.
	expectedToken := FormatDestroyToken(prefix)
	if destroyToken == expectedToken {
		fmt.Fprintf(w, "Destroy token accepted. Proceeding with re-initialization.\n")
		return nil
	}
	if unknown {
		fmt.Fprintf(w, "Refusing to destroy an unknown number of issues in non-interactive mode.\n")
		fmt.Fprintf(w, "  See 'bd help init-safety' for the required --destroy-token format.\n")
		return &exitError{Code: ExitDestroyTokenMissing}
	}
	fmt.Fprintf(w, "Refusing to destroy %d issues in non-interactive mode.\n", count)
	fmt.Fprintf(w, "  See 'bd help init-safety' for the required --destroy-token format.\n")
	fmt.Fprintf(w, "  Or export issue records first: bd export > issue-export.jsonl\n")
	return &exitError{Code: ExitDestroyTokenMissing}
}
