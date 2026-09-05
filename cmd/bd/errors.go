package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/ui"
)

type exitError struct {
	Code int
}

func (e *exitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

func exitCodeFromError(err error) (int, bool) {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.Code, true
	}
	return 0, false
}

func activeWorkspaceNotFoundError() string {
	return "no active beads workspace found"
}

func activeWorkspaceNotFoundMessage() string {
	return "No active beads workspace found."
}

func diagHint() string {
	return workspaceDiagHint(true)
}

func whereDiagHint() string {
	return workspaceDiagHint(false)
}

func workspaceDiagHint(includeWhere bool) string {
	if includeWhere {
		if !usesSQLServer() {
			return "run 'bd where' to inspect the resolved workspace, or 'bd init' to create a new database"
		}
		return "run 'bd where' to inspect the resolved workspace, run 'bd doctor' to diagnose, or 'bd init' to create a new database"
	}
	if !usesSQLServer() {
		return "check BEADS_DIR/worktree setup, or run 'bd init' to create a new database"
	}
	return "check BEADS_DIR/worktree setup, run 'bd doctor' to diagnose, or run 'bd init' to create a new database"
}

func buildJSONError(message, hint string) interface{} {
	inner := map[string]interface{}{
		"error": message,
	}
	if hint != "" {
		inner["hint"] = hint
	}
	if jsonEnvelopeEnabled() {
		return map[string]interface{}{
			"schema_version": JSONSchemaVersion,
			"data":           inner,
		}
	}
	inner["schema_version"] = JSONSchemaVersion
	return inner
}

func buildJSONCapabilityError(e *ProxyCapabilityError) interface{} {
	inner := map[string]interface{}{"error": e.Message, "code": e.Code, "mutates": e.Mutates}
	if jsonEnvelopeEnabled() {
		return map[string]interface{}{"schema_version": JSONSchemaVersion, "data": inner}
	}
	inner["schema_version"] = JSONSchemaVersion
	return inner
}

// HandleProxyCapabilityError renders a stable capability refusal while
// preserving the normal text/JSON front-door conventions.
func HandleProxyCapabilityError(err error) error {
	var capErr *ProxyCapabilityError
	if !errors.As(err, &capErr) {
		return HandleErrorRespectJSON("%v", err)
	}
	code := capErr.ExitCode
	if code == 0 {
		code = 1
	}
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(buildJSONCapabilityError(capErr))
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", capErr.Message)
	}
	return &exitError{Code: code}
}

func jsonStderrError(message, hint string) {
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(buildJSONError(message, hint))
}

func jsonStdoutError(message, hint string) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(buildJSONError(message, hint))
}

func HandleError(format string, args ...interface{}) error {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	return &exitError{Code: 1}
}

func HandleErrorRespectJSON(format string, args ...interface{}) error {
	if jsonOutput {
		jsonStdoutError(fmt.Sprintf(format, args...), "")
		return &exitError{Code: 1}
	}
	return HandleError(format, args...)
}

func HandleErrorWithHint(message, hint string) error {
	if jsonOutput {
		jsonStderrError(message, hint)
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", message) //nolint:gosec // G705: stderr, not a browser context
		fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)     //nolint:gosec // G705: stderr, not a browser context
	}
	return &exitError{Code: 1}
}

func HandleErrorWithHintRespectJSON(message, hint string) error {
	if jsonOutput {
		jsonStdoutError(message, hint)
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", message)
		fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)
	}
	return &exitError{Code: 1}
}

func SilentExit() error {
	return &exitError{Code: 1}
}

func WarnError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "Warning: "+format+"\n", args...)
}

// ExitMigrationFrozen is the exit code for a write refused because a
// migration freeze marker is active (dc-6jaq). Stable value so scripts can
// branch on "someone is migrating this workspace, come back later" without
// grep'ing stderr, instead of reading it as a generic failure (1) worth
// retrying immediately.
const ExitMigrationFrozen = 14

// CheckReadonly aborts the command when bd is running in read-only mode (the
// worker-sandbox posture, see readonlyMode), or when a migration freeze
// marker is active (dc-6jaq, folded in here rather than requiring every write
// command to remember a second call — see migrationFreezeError below). This
// is the chokepoint essentially every write command already calls first, so
// folding the freeze check in here covers the whole write surface at once,
// including commands added after this comment is written — not just a
// hand-picked list that rots (one concrete instance the hand-picked list
// missed: "bd q", cmd/bd/quick.go, the create alias). It exits via os.Exit
// and so cannot run the per-command deferred CloseEventAndAdd — a command
// blocked here records no cli_command event of its own (it never actually
// ran). It does flush metrics first, so events already queued earlier in this
// run are still written and scheduled for upload rather than stranded until
// the next clean exit. Callers that CAN return an error (the root
// PersistentPreRunE, runImport) call migrationFreezeError directly instead,
// so their deferred cleanup still runs.
func CheckReadonly(operation string) {
	if readonlyMode {
		fmt.Fprintf(os.Stderr, "Error: operation '%s' is not allowed in read-only mode\n", operation)
		metrics.CloseAndFlush()
		os.Exit(1)
	}
	if err := migrationFreezeError(operation); err != nil {
		metrics.CloseAndFlush()
		os.Exit(ExitMigrationFrozen)
	}
}

// freezeSearchRoots returns the directories whose ancestry the freeze lookup
// must cover in addition to the working directory: the resolved workspace, so
// the gate keys on the store being written rather than on the caller's shell.
// Without it, `BEADS_DIR=/work/repo/.beads bd create` run from $HOME, `bd -C
// /work/repo create` (which sets BEADS_DIR and never chdirs), and any daemon
// or cron job with an unrelated cwd all walk the wrong tree and write straight
// through a freeze.
//
// PersistentPreRunE exports BEADS_DIR as part of selecting the workspace
// (prepareSelectedCommandContext), so by the time a command's RunE reaches
// CheckReadonly this is a cheap env read. The FindBeadsDir fallback covers the
// callers that run before that — and returns "" rather than erroring when
// there is no workspace at all, which Find skips.
func freezeSearchRoots() []string {
	if dir := os.Getenv("BEADS_DIR"); dir != "" {
		return []string{dir}
	}
	if dir := beads.FindBeadsDir(); dir != "" {
		return []string{dir}
	}
	return nil
}

// migrationFreezeError reports the active migration freeze, if any: it
// returns nil when writes are not frozen, and otherwise prints the refusal to
// stderr and returns an ExitMigrationFrozen exit error. The refusal names the
// marker's own path, so the recovery instruction ("remove that file") is
// always actionable without knowing what created it.
//
// Three callers, each closing a different hole:
//  1. CheckReadonly above — the per-command chokepoint covering the write
//     surface at large (create/update/close/... and everything that calls
//     CheckReadonly, ~120 sites). Only that caller turns this error into an
//     os.Exit, because its void signature leaves it nothing else to do.
//  2. The root PersistentPreRunE in main.go, before autoMigrateOnVersionBump
//     and maybeAutoImportJSONL — those run as store-open side effects
//     *before* any RunE, so waiting for a write command's own CheckReadonly
//     call would let a version-bump migration or a JSONL auto-import slip
//     through a freeze first (the most dangerous writes here, since they
//     run against the very store the freeze protects).
//  3. import.go directly — bd import (runImport) is not gated by
//     CheckReadonly at all today (a pre-existing, separate gap: readonlyMode
//     doesn't block it either), so it cannot inherit the freeze check from
//     caller 1 and needs its own explicit call.
func migrationFreezeError(operation string) error {
	return migrationFreezeRefusal(operation, migration.Find(freezeSearchRoots()...))
}

// migrationFreezeRefusal renders an already-resolved lookup. Callers that
// need the same Result for more than the refusal — PersistentPreRunE, which
// also derives frozenForMaintenance from it — resolve once and pass it here,
// so a single walk answers the whole invocation and the two readings cannot
// disagree about a marker that appeared or vanished between them.
func migrationFreezeRefusal(operation string, res migration.Result) error {
	if !res.Frozen() {
		return nil
	}

	if res.Err != nil {
		// Fail closed: bd could not tell whether a marker is present (a
		// permission error on the marker or a directory above it). An
		// undeterminable gate is not an open gate, and saying so beats
		// writing into a workspace that may be mid-migration.
		fmt.Fprint(os.Stderr, "⛔ ERROR: cannot determine whether this workspace is frozen for migration.\n")
		fmt.Fprintf(os.Stderr, "   %s\n", ui.SanitizeForTerminal(res.Err.Error()))
		fmt.Fprintf(os.Stderr, "   bd %s is blocked until the marker can be read.\n", operation)
		fmt.Fprintf(os.Stderr, "   Fix the permissions, or set %s to a path bd can stat.\n", migration.EnvFreezeFile)
		return &exitError{Code: ExitMigrationFrozen}
	}

	// The marker can live in a directory bd does not control, so its payload
	// is untrusted input on its way to a terminal. parse already keeps it to
	// one line; sanitizing strips ANSI and control bytes on top of that.
	operator := ""
	reason := ""
	if info := migration.ReadFile(res.Path); info != nil {
		operator = ui.SanitizeForTerminal(info.Operator)
		reason = ui.SanitizeForTerminal(info.Reason)
	}

	if operator != "" {
		fmt.Fprintf(os.Stderr, "⛔ ERROR: workspace is frozen for migration (by %s).\n", operator)
	} else {
		// An empty or unparseable marker (a bare `touch`) records no
		// operator; say nothing rather than printing "(by )".
		fmt.Fprint(os.Stderr, "⛔ ERROR: workspace is frozen for migration.\n")
	}
	if reason != "" {
		fmt.Fprintf(os.Stderr, "   Reason: %s\n", reason)
	}
	fmt.Fprintf(os.Stderr, "   bd %s is blocked by the freeze marker at %s.\n", operation, res.Path)
	if res.FromEnv {
		fmt.Fprintf(os.Stderr, "   To resume writes, remove that file or unset %s.\n", migration.EnvFreezeFile)
	} else {
		// Name the variable on this path too: when the marker sits in a
		// directory the caller cannot write to, pointing the variable
		// elsewhere is the only recovery available to them.
		fmt.Fprint(os.Stderr, "   To resume writes, remove that file.\n")
		fmt.Fprintf(os.Stderr, "   If you cannot remove it, set %s to a path bd should check instead.\n", migration.EnvFreezeFile)
	}
	return &exitError{Code: ExitMigrationFrozen}
}

// migrationFreezeGate is the RunE-side entry point: it renders the refusal and,
// because the refusal has already said everything worth saying, suppresses
// cobra's own error and usage rendering for this invocation.
//
// Without the suppression a freeze refusal on any command that does not set
// SilenceErrors/SilenceUsage statically — duplicate, supersede, dep
// relate/unrelate, backup init/sync/remove/restore, ado sync, migrate-personal,
// batch — is followed by "Error: exit code 14" (exitError's placeholder text)
// and a full usage dump, so a clean refusal reads like a syntax error. Setting
// the flags here rather than on each command covers the ones added after this
// comment too. main() exits on the exitError before its own SilenceErrors
// branch, so nothing prints the placeholder.
func migrationFreezeGate(cmd *cobra.Command, operation string, res migration.Result) error {
	err := migrationFreezeRefusal(operation, res)
	if err != nil && cmd != nil {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
	}
	return err
}

// migrationFreezeGateFor resolves the freeze and gates in one step, for the
// callers that have no Result to thread: the skip-store commands (bd init,
// bd bootstrap), which return from PersistentPreRunE long before it resolves
// one, and any command handed an explicit target path.
func migrationFreezeGateFor(cmd *cobra.Command, operation string, extraRoots ...string) error {
	return migrationFreezeGate(cmd, operation, migration.Find(freezeRootsWith(extraRoots)...))
}

// migrationFreezeErrorFor is migrationFreezeError against extra roots, for
// callers that hold a target path but no *cobra.Command to silence — a command
// given a workspace to operate on that is not the one it was launched in.
func migrationFreezeErrorFor(operation string, extraRoots ...string) error {
	return migrationFreezeRefusal(operation, migration.Find(freezeRootsWith(extraRoots)...))
}

// migrationFreezeActive is the print-nothing probe for code that must SKIP a
// write during a freeze rather than refuse the whole command — the diagnosis
// paths that keep running while frozen but must not apply version tracking or
// schema auto-migration.
func migrationFreezeActive() bool {
	return migrationFreezeActiveFor()
}

// migrationFreezeActiveFor is migrationFreezeActive against extra roots — the
// probe half of the migrationFreezeErrorFor pair, for a diagnosis path handed a
// workspace that is not the one bd was launched in. `bd doctor /frozen/repo`
// skips its own maintenance writes on the strength of this, so it has to look
// at the tree it is about to write to and not just at the caller's cwd.
func migrationFreezeActiveFor(extraRoots ...string) bool {
	return migration.Find(freezeRootsWith(extraRoots)...).Frozen()
}

func freezeRootsWith(extraRoots []string) []string {
	return append(freezeSearchRoots(), extraRoots...)
}
