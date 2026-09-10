package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/migration"
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

// CheckReadonly aborts the command when bd is running in read-only mode (the
// worker-sandbox posture, see readonlyMode), or when a MIGRATION-FREEZE
// sentinel is active at the town root (dc-6jaq, folded in here rather than
// requiring every write command to remember a second call — see
// CheckMigrationFreeze below). This is the chokepoint essentially every
// write command already calls first, so folding the freeze check in here
// covers the whole write surface at once, including commands added after
// this comment is written — not just a hand-picked list that rots (one
// concrete instance the hand-picked list missed: "bd q", cmd/bd/quick.go,
// the create alias). It exits via os.Exit and so cannot run the per-command
// deferred CloseEventAndAdd — a command blocked here records no
// cli_command event of its own (it never actually ran). It does flush
// metrics first, so events already queued earlier in this run are still
// written and scheduled for upload rather than stranded until the next
// clean exit.
func CheckReadonly(operation string) {
	if readonlyMode {
		fmt.Fprintf(os.Stderr, "Error: operation '%s' is not allowed in read-only mode\n", operation)
		metrics.CloseAndFlush()
		os.Exit(1)
	}
	CheckMigrationFreeze(operation)
}

// CheckMigrationFreeze aborts the command when a MIGRATION-FREEZE sentinel is
// present at the town root (dc-6jaq). The gt CLI already refuses to write
// under the same sentinel (gt mail send, gt nudge, gt sling, gt assign);
// mail-poller/daemon patrols that also write via bd are stopped separately
// via the migration playbook's plist-unload step, so this closes the
// remaining gap: a human typing 'bd create'/'bd update' etc. mid-migration,
// bypassing the gt-layer gate. Same exit-via-os.Exit tradeoff as
// CheckReadonly above — no cli_command event for a command blocked here,
// but queued metrics are still flushed.
//
// Three callers, each closing a different hole:
//  1. CheckReadonly above — the per-command chokepoint covering the write
//     surface at large (create/update/close/... and everything that calls
//     CheckReadonly, ~120 sites).
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
func CheckMigrationFreeze(operation string) {
	townRoot := findTownRoot()
	if !migration.IsFrozen(townRoot) {
		return
	}

	info := migration.Read(townRoot)
	operator := "unknown"
	reason := ""
	if info != nil {
		operator = info.Operator
		reason = info.Reason
	}

	fmt.Fprintf(os.Stderr, "⛔ ERROR: town is frozen for migration (by %s).\n", operator)
	if reason != "" {
		fmt.Fprintf(os.Stderr, "   Reason: %s\n", reason)
	}
	fmt.Fprintf(os.Stderr, "   bd %s is blocked. Clear the freeze: gt migrate thaw\n", operation)
	metrics.CloseAndFlush()
	os.Exit(1)
}
