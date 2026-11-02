package main

// Store-switch safety for `bd init --reinit-local` (GH#3495).
//
// --reinit-local bypasses checkExistingBeadsData, and its preflight used to
// confirm destruction from countExistingIssues alone: a zero-issue — or
// unreachable — store holding only configuration was silently abandoned when
// the invocation named a different database or engine family, because a
// count of 0, or a count error, answered "nothing to protect". The
// comparison here keys on the workspace's RECORDED metadata instead, so the
// decision never depends on opening, reaching or counting the old store.
//
// Error-message contract: the refusal points at `bd help init-safety` for
// the destroy-token format and never echoes the token value or a complete
// destructive invocation
// (engdocs/adr/0002-init-safety-invariants.md, Invariant 4).

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/ui"
)

// storeSwitchRecord is the recorded workspace state the switch guard
// compares a request against. Mode is the raw recorded dolt_mode,
// lowercased and trimmed ("" when absent — it is deliberately NOT defaulted
// to embedded, so "deliberately embedded" stays distinguishable from "not
// recorded"). Database is the raw recorded dolt_database.
type storeSwitchRecord struct {
	Backend  string
	Mode     string
	Database string
}

// storeSwitchRequest is what this invocation asks init to switch to.
// UsesSQLServer is the final process mode resolved above the mutation gate
// (usesSQLServer() at the call site); Database is the raw --database value,
// where empty means REUSE of the recorded name, not a prefix-derived
// default. ModeSource names where the request's connection mode came from
// (a flag, an env var, or config.yaml), so the notice can name the override
// to remove; a family switch always implies a nonempty source, because a
// bare reinit inherits the recorded mode instead of naming one.
type storeSwitchRequest struct {
	UsesSQLServer bool
	Database      string
	ModeSource    string
}

// storeSwitchDiff names the two sides of a detected switch for the
// confirmation notice. RecordedFamily is the recorded dolt_mode (server and
// proxied-server are ONE family); RequestedFamily is "server" or "embedded".
// RequestedDatabase is the explicit override when the database axis
// triggered; otherwise the request reuses the recorded name.
type storeSwitchDiff struct {
	FamilySwitch      bool
	RecordedFamily    string
	RequestedFamily   string
	DatabaseSwitch    bool
	RecordedDatabase  string
	RequestedDatabase string
	ModeSource        string
}

// loadStoreSwitchRecord reads the selected workspace's recorded store
// metadata without migrating it.
//
// A present-but-unreadable or malformed metadata file is an error, not an
// empty workspace: this guard decides whether a destructive operation may
// proceed, so it must not swallow that case the way the count-based
// helpers (existingWorkspaceDBName, existingWorkspaceDoltMode) do. A
// genuinely absent workspace (no metadata file) is not a switch: nothing
// is recorded to switch away from.
func loadStoreSwitchRecord(beadsDir string) (storeSwitchRecord, error) {
	if beadsDir == "" {
		return storeSwitchRecord{}, nil
	}
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return storeSwitchRecord{}, err
	}
	if cfg == nil {
		return storeSwitchRecord{}, nil
	}
	return storeSwitchRecord{
		Backend:  cfg.GetBackend(),
		Mode:     strings.ToLower(strings.TrimSpace(cfg.DoltMode)),
		Database: cfg.DoltDatabase,
	}, nil
}

// compareStoreSwitch reports whether the request repoints the workspace
// away from the store its metadata records.
//
// The engine-family axis: the recorded dolt_mode compared with the resolved
// process mode. Server and proxied-server are ONE family — a workspace whose
// recorded family changes from server to embedded (or back) is switching its
// store even when the database name is unchanged, which is why this axis
// exists at all: the count-based preflight cannot see it. An unrecorded or
// unknown recorded mode skips this axis (it cannot prove a family change)
// without disabling the database axis.
//
// The database axis: a nonempty explicit database differing byte-for-byte
// from a nonempty recorded dolt_database. An absent override means reuse,
// not a prefix-derived default, and an unrecorded name cannot be switched
// away from. Non-Dolt backends keep their existing validation and routing;
// this guard decides only Dolt store switches.
func compareStoreSwitch(recorded storeSwitchRecord, request storeSwitchRequest) (storeSwitchDiff, bool) {
	if recorded.Backend != configfile.BackendDolt {
		return storeSwitchDiff{}, false
	}
	var diff storeSwitchDiff
	switch recorded.Mode {
	case configfile.DoltModeEmbedded:
		if request.UsesSQLServer {
			diff.FamilySwitch = true
			diff.RecordedFamily = recorded.Mode
			diff.RequestedFamily = "server"
		}
	case configfile.DoltModeServer, configfile.DoltModeProxiedServer:
		// Same family: a proxied-server workspace re-initialized as
		// --server keeps its server-side store, so only a move to the
		// embedded engine is a family switch. (A --proxied-server request
		// never reaches the mutation gate: its dedicated init route
		// dispatches before mode inheritance.)
		if !request.UsesSQLServer {
			diff.FamilySwitch = true
			diff.RecordedFamily = recorded.Mode
			diff.RequestedFamily = configfile.DoltModeEmbedded
		}
	}
	if request.Database != "" && recorded.Database != "" && request.Database != recorded.Database {
		diff.DatabaseSwitch = true
		diff.RecordedDatabase = recorded.Database
		diff.RequestedDatabase = request.Database
	}
	if !diff.FamilySwitch && !diff.DatabaseSwitch {
		return storeSwitchDiff{}, false
	}
	diff.ModeSource = request.ModeSource
	return diff, true
}

// storeSwitchNotice prints what a store switch means before confirmation is
// demanded. It is unconditional under --quiet: a silent store switch is
// exactly the GH#3495 failure mode.
//
// The content contract: identify both sides of the switch, name the
// database-stored configuration that stops being read (a count-based
// preflight cannot see it, so the notice must), point at the safe commands
// (backup first, reference for the token format), and echo neither a
// destroy-token value nor a complete destructive invocation
// (ADR 0002 Invariant 4) — the flag NAME is how the refusal explains
// authorization without handing over a copy-pasteable command.
func storeSwitchNotice(w io.Writer, diff storeSwitchDiff) {
	fmt.Fprintf(w, "\n%s re-initializing switches this workspace's store", ui.RenderWarn("WARNING:"))
	switch {
	case diff.FamilySwitch && diff.DatabaseSwitch:
		fmt.Fprintf(w, ": engine family %s -> %s and database %s -> %s",
			remedy.PrintedValue(diff.RecordedFamily), remedy.PrintedValue(diff.RequestedFamily),
			remedy.PrintedValue(diff.RecordedDatabase), remedy.PrintedValue(diff.RequestedDatabase))
	case diff.FamilySwitch:
		fmt.Fprintf(w, ": engine family %s -> %s",
			remedy.PrintedValue(diff.RecordedFamily), remedy.PrintedValue(diff.RequestedFamily))
	case diff.DatabaseSwitch:
		fmt.Fprintf(w, ": database %s -> %s",
			remedy.PrintedValue(diff.RecordedDatabase), remedy.PrintedValue(diff.RequestedDatabase))
	}
	if diff.FamilySwitch && diff.ModeSource != "" {
		fmt.Fprintf(w, " (selected by %s)", remedy.PrintedValue(diff.ModeSource))
	}
	fmt.Fprintf(w, ".\n")
	fmt.Fprintf(w, "  The recorded store is left in place; it is not deleted. But this workspace\n")
	fmt.Fprintf(w, "  will stop reading its issues, its history and its configuration,\n")
	fmt.Fprintf(w, "  including the custom issue types (types.custom) and custom\n")
	fmt.Fprintf(w, "  statuses (status.custom) stored with it.\n")
	fmt.Fprintf(w, "  Before proceeding, consider:\n")
	fmt.Fprintf(w, "    bd backup                    # Back up the recorded store\n")
	fmt.Fprintf(w, "    bd help init-safety          # The --destroy-token format and init's safety guards\n\n")
}

// confirmStoreSwitchInput carries the switch confirmation's injectable
// state: the detected diff, the tokens, and the reader and writer the
// interactive phrase path and the notice and refusal use. StdinIsTTY routes
// between the typed phrase (a real terminal) and the token requirement
// (everything else).
type confirmStoreSwitchInput struct {
	Diff          storeSwitchDiff
	DestroyToken  string
	ExpectedToken string
	StdinIsTTY    bool
	Stdin         io.Reader
	Stderr        io.Writer
}

// storeSwitchPhrase is the typed text an interactive switch confirmation must
// match. Naming what is being left behind — the recorded database — makes the
// operator say the consequence instead of a bare "yes"; a family-only switch
// with no recorded database name falls back to naming the store itself.
func storeSwitchPhrase(diff storeSwitchDiff) string {
	if diff.RecordedDatabase != "" {
		return "leave " + diff.RecordedDatabase + " behind"
	}
	return "leave the recorded store behind"
}

// confirmStoreSwitch demands authorization for a detected store switch:
// a matching destroy token, or the matching interactive phrase at a real
// terminal. Either authorizes the switch once. A mismatched phrase is the
// operator declining at the prompt (ExitLocalExistsRefused); everything
// else without a terminal refuses for want of a token
// (ExitDestroyTokenMissing). The old store is never opened, counted or
// inspected to make this decision.
func confirmStoreSwitch(in confirmStoreSwitchInput) error {
	storeSwitchNotice(in.Stderr, in.Diff)
	if in.ExpectedToken != "" && in.DestroyToken == in.ExpectedToken {
		fmt.Fprintln(in.Stderr, "Destroy token accepted. Proceeding with the store switch.")
		return nil
	}
	if in.StdinIsTTY {
		phrase := storeSwitchPhrase(in.Diff)
		fmt.Fprintf(in.Stderr, "Type '%s' to confirm: ", remedy.PrintedValue(phrase))
		scanner := bufio.NewScanner(in.Stdin)
		scanner.Scan()
		if strings.TrimSpace(scanner.Text()) == phrase {
			fmt.Fprintln(in.Stderr, "\nConfirmed. Proceeding with the store switch.")
			return nil
		}
		fmt.Fprintf(in.Stderr, "\nAborted. The workspace's store was not switched.\n")
		return &exitError{Code: ExitLocalExistsRefused}
	}
	fmt.Fprintf(in.Stderr, "Refusing to switch this workspace's store without a matching --destroy-token.\n")
	fmt.Fprintf(in.Stderr, "  See 'bd help init-safety' for the required --destroy-token format.\n")
	fmt.Fprintf(in.Stderr, "  Or re-run the command at a terminal to type the confirmation instead.\n")
	return &exitError{Code: ExitDestroyTokenMissing}
}

// reinitPreflightInput is the destructive reinit preflight's state. File
// discovery and process-state resolution stay at the call site; the
// injected fields exist so the switch decision and both confirmation
// policies are unit-testable without a workspace, a TTY or a store.
type reinitPreflightInput struct {
	ReinitLocal   bool
	BeadsDir      string
	Request       storeSwitchRequest
	DestroyToken  string
	ExpectedToken string
	// StdinIsTTY feeds the prompt-vs-token decision of the count-based
	// confirmation below; the switch confirmation above never reads
	// stdin for its decision.
	StdinIsTTY bool
	Stdin      io.Reader
	Stderr     io.Writer

	// LoadRecorded reads the workspace's recorded store metadata.
	LoadRecorded func(beadsDir string) (storeSwitchRecord, error)
	// CountExisting counts issues in the recorded store, opening it. It
	// is never called on the switch path: a switch is decided from the
	// recorded metadata, before the old store is opened.
	CountExisting func() (int, error)
}

// runInitReinitPreflight confirms the destructive local replacement while
// the caller holds init's complete exclusive gate set.
//
// A store switch — the request naming a different engine family or
// database than the recorded workspace — is confirmed from the RECORDED
// metadata, never by opening or counting the old store, and a successful
// confirmation authorizes the switch once (no second count-based prompt
// follows). With no detected switch, the existing issue-count confirmation
// policy applies unchanged.
func runInitReinitPreflight(in reinitPreflightInput) error {
	if !in.ReinitLocal {
		return nil
	}
	recorded, err := in.LoadRecorded(in.BeadsDir)
	if err != nil {
		return fmt.Errorf("reading the existing workspace's store metadata: %w", err)
	}
	if diff, switched := compareStoreSwitch(recorded, in.Request); switched {
		return confirmStoreSwitch(confirmStoreSwitchInput{
			Diff:          diff,
			DestroyToken:  in.DestroyToken,
			ExpectedToken: in.ExpectedToken,
			StdinIsTTY:    in.StdinIsTTY,
			Stdin:         in.Stdin,
			Stderr:        in.Stderr,
		})
	}

	count, countErr := in.CountExisting()
	if countErr != nil {
		// The old preflight treated a count error like an empty store and
		// proceeded silently. Say what actually happened — the contents
		// could not be inspected — never that the store was empty.
		fmt.Fprintf(in.Stderr, "%s could not inspect the existing store before re-initializing (%s); proceeding without inspecting it.\n",
			ui.RenderWarn("Warning:"), remedy.PrintedText(countErr.Error()))
		return nil
	}
	if count == 0 {
		return nil
	}

	fmt.Fprintf(in.Stderr, "\n%s Re-initializing will destroy the existing database.\n\n", ui.RenderWarn("WARNING:"))
	fmt.Fprintf(in.Stderr, "  Existing issues: %d\n\n", count)
	fmt.Fprintf(in.Stderr, "  This action CANNOT be undone. All issues, dependencies, and\n")
	fmt.Fprintf(in.Stderr, "  Dolt commit history will be permanently lost.\n\n")
	fmt.Fprintf(in.Stderr, "  Before proceeding, consider:\n")
	fmt.Fprintf(in.Stderr, "    bd export > issue-export.jsonl    # Export issue records, not full DB state\n")
	fmt.Fprintf(in.Stderr, "    bd dolt status              # Check if this is a server config issue\n\n")
	if in.StdinIsTTY {
		fmt.Fprintf(in.Stderr, "Type 'destroy %d issues' to confirm: ", count)
		scanner := bufio.NewScanner(in.Stdin)
		scanner.Scan()
		expected := fmt.Sprintf("destroy %d issues", count)
		if strings.TrimSpace(scanner.Text()) != expected {
			fmt.Fprintf(in.Stderr, "\nAborted. Database was NOT modified.\n")
			return &exitError{Code: ExitLocalExistsRefused}
		}
		return nil
	}

	// Non-interactive (piped input, AI agent, etc.)
	//
	// ADR invariant (engdocs/adr/0002-init-safety-invariants.md): runtime
	// error text must not echo a complete destructive invocation. See
	// 'bd help init-safety' for the token format.
	if in.DestroyToken == in.ExpectedToken && in.ExpectedToken != "" {
		fmt.Fprintf(in.Stderr, "Destroy token accepted. Proceeding with re-initialization.\n")
		return nil
	}
	fmt.Fprintf(in.Stderr, "Refusing to destroy %d issues in non-interactive mode.\n", count)
	fmt.Fprintf(in.Stderr, "  See 'bd help init-safety' for the required --destroy-token format.\n")
	fmt.Fprintf(in.Stderr, "  Or export issue records first: bd export > issue-export.jsonl\n")
	return &exitError{Code: ExitDestroyTokenMissing}
}
