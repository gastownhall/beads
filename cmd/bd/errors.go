package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func activeWorkspaceNotFoundError() string {
	return "no active beads workspace found"
}

func activeWorkspaceNotFoundMessage() string {
	return "No active beads workspace found."
}

// diagHint returns the appropriate diagnostic hint when the active beads
// workspace cannot be resolved. In embedded mode, 'bd doctor' is not
// available so the hint omits it.
func diagHint() string {
	return workspaceDiagHint(true)
}

func whereDiagHint() string {
	return workspaceDiagHint(false)
}

func workspaceDiagHint(includeWhere bool) string {
	if includeWhere {
		if isEmbeddedMode() {
			return "run 'bd where' to inspect the resolved workspace, or 'bd init' to create a new database"
		}
		return "run 'bd where' to inspect the resolved workspace, run 'bd doctor' to diagnose, or 'bd init' to create a new database"
	}
	if isEmbeddedMode() {
		return "check BEADS_DIR/worktree setup, or run 'bd init' to create a new database"
	}
	return "check BEADS_DIR/worktree setup, run 'bd doctor' to diagnose, or run 'bd init' to create a new database"
}

// FatalError writes an error message to stderr and exits with code 1.
// Use this for fatal errors that prevent the command from completing.
//
// Pattern A from ERROR_HANDLING.md:
// - User input validation failures
// - Critical preconditions not met
// - Unrecoverable system errors
//
// Example:
//
//	if err := store.CreateIssue(ctx, issue, actor); err != nil {
//	    FatalError("%v", err)
//	}
func FatalError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if jsonOutput {
		data, _ := json.MarshalIndent(map[string]string{"error": msg}, "", "  ")
		fmt.Fprintln(os.Stderr, string(data))
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	}
	os.Exit(1)
}

// FatalErrorRespectJSON writes an error message and exits with code 1.
// If --json flag is set, outputs structured JSON to stdout.
// Otherwise, outputs plain text to stderr.
//
// Use this for errors in commands that support --json output.
//
// Example:
//
//	if err := store.GetIssue(ctx, id); err != nil {
//	    FatalErrorRespectJSON("%v", err)
//	}
func FatalErrorRespectJSON(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if jsonOutput {
		data, _ := json.MarshalIndent(map[string]string{"error": msg}, "", "  ") // json.MarshalIndent on simple maps does not fail in practice
		fmt.Println(string(data))
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	}
	os.Exit(1)
}

// FatalErrorWithHintRespectJSON writes an error message with a hint and exits.
// If --json is set, emits structured JSON to stdout so callers can parse it.
func FatalErrorWithHintRespectJSON(message, hint string) {
	if jsonOutput {
		outputJSON(map[string]string{"error": message, "hint": hint})
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", message)
		fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)
	}
	os.Exit(1)
}

// FatalErrorWithHint writes an error message with a hint to stderr and exits.
// Use this when you can provide an actionable suggestion to fix the error.
//
// Example:
//
//	FatalErrorWithHint("database not found", "Run 'bd init' to create a database")
func FatalErrorWithHint(message, hint string) {
	if jsonOutput {
		data, _ := json.MarshalIndent(map[string]string{"error": message, "hint": hint}, "", "  ")
		fmt.Fprintln(os.Stderr, string(data))
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", message)
		fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)
	}
	os.Exit(1)
}

// WarnError writes a warning message to stderr and returns.
// Use this for optional operations that enhance functionality but aren't required.
//
// Pattern B from ERROR_HANDLING.md:
// - Metadata operations
// - Cleanup operations
// - Auxiliary features (git hooks, merge drivers)
//
// Example:
//
//	if err := createConfigYaml(beadsDir, false); err != nil {
//	    WarnError("failed to create config.yaml: %v", err)
//	}
func WarnError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "Warning: "+format+"\n", args...)
}

// CheckReadonly exits with an error if readonly mode is enabled.
// Call this at the start of write commands (create, update, close, delete, sync, etc.).
// Used by worker sandboxes that should only read beads, not modify them.
//
// Example:
//
//	var createCmd = &cobra.Command{
//	    Run: func(cmd *cobra.Command, args []string) {
//	        CheckReadonly("create")
//	        // ... rest of command
//	    },
//	}
func CheckReadonly(operation string) {
	if readonlyMode {
		FatalError("operation '%s' is not allowed in read-only mode", operation)
	}
}
