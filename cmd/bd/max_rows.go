package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// maxRowsEnvVar names the environment variable that opts in to the defensive
// row cap. Used as both the lookup key and the source attribution string in
// error messages.
const maxRowsEnvVar = "BEADS_MAX_ROWS"

// maxRowsFlagName is the --max-rows flag name. Declared as a constant so the
// flag lookup and the source attribution string stay in sync.
const maxRowsFlagName = "max-rows"

// addMaxRowsFlag registers the --max-rows int flag on cmd. Use on every
// user-facing command that should honor the per-invocation cap override
// (designer §2.2 / §4). Help text matches the designer spec verbatim.
func addMaxRowsFlag(cmd *cobra.Command) {
	cmd.Flags().Int(maxRowsFlagName, 0,
		"Hard upper bound on rows fetched from storage. Returns a non-zero exit (code 2) "+
			"and an error to stderr if exceeded. 0 disables (the default). Overrides "+
			"BEADS_MAX_ROWS for this invocation. Useful in CI/agent rigs that want a "+
			"circuit breaker against pathological queries. Not supported under "+
			"--proxied-server: an explicit --max-rows or BEADS_MAX_ROWS cap errors out "+
			"rather than silently going unenforced.")
}

// rejectMaxRowsUnderProxiedServer errors out when an explicit --max-rows
// flag or BEADS_MAX_ROWS env cap resolves to a nonzero cap but the command
// is about to divert to proxied-server mode. The proxied repository path
// (internal/storage/domain/db) doesn't thread MaxRows through the UOW
// pipeline, so silently ignoring the cap there would be the worst outcome
// for a safety flag — reject explicitly instead of no-oping. Call this
// after usesProxiedServer() is known true but before diverting to the
// *ProxiedServer function, on every command that calls addMaxRowsFlag.
func rejectMaxRowsUnderProxiedServer(cmd *cobra.Command) error {
	maxRows, _, err := resolveMaxRows(cmd)
	if err != nil {
		return err
	}
	return rejectResolvedMaxRowsUnderProxiedServer(maxRows)
}

// rejectResolvedMaxRowsUnderProxiedServer is rejectMaxRowsUnderProxiedServer
// split out for callers that have already resolved the cap for their own
// purposes (e.g. to build a filter) and would otherwise call resolveMaxRows
// a second time — resolveMaxRowsEnvOnly emits a stderr warning on a
// malformed BEADS_MAX_ROWS every time it runs, so a second resolve doubles
// that warning under proxied mode (be-x42v.4 round-4 follow-up).
func rejectResolvedMaxRowsUnderProxiedServer(maxRows int) error {
	if maxRows > 0 {
		return HandleErrorRespectJSON("--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode")
	}
	return nil
}

// resolveMaxRows picks the effective cap from --max-rows then BEADS_MAX_ROWS.
// Returns (cap, source, err) where cap == 0 disables the cap and source is
// one of "--max-rows", "BEADS_MAX_ROWS", or "". A negative flag or bad int
// is a usage error: err is non-nil (exit code 1, main.go's
// exitCodeFromError convention — see HandleErrorRespectJSON) and the
// caller must propagate it immediately (`if err != nil { return err }`),
// mirroring how gatherListInput's callers already handle its (T, error)
// return. A non-integer env value emits a warning to stderr and is ignored
// (returns cap == 0, err == nil).
//
// Precedence (designer §2.1):
//
//  1. --max-rows N            (flag changed, highest)
//  2. BEADS_MAX_ROWS=N        (env var)
//  3. disabled                (default; cap == 0)
//
// --max-rows 0 explicitly disables the cap even when BEADS_MAX_ROWS=N is set.
// This is intentional: ops shells with a global env can run a known-unbounded
// query without unsetting the env first.
func resolveMaxRows(cmd *cobra.Command) (int, string, error) {
	if cmd != nil && cmd.Flags().Changed(maxRowsFlagName) {
		n, err := cmd.Flags().GetInt(maxRowsFlagName)
		if err != nil {
			return 0, "", HandleErrorRespectJSON("--max-rows: %v", err)
		}
		if n < 0 {
			return 0, "", HandleErrorRespectJSON("--max-rows must be non-negative; got %d", n)
		}
		return n, "--" + maxRowsFlagName, nil
	}
	maxRows, source := resolveMaxRowsEnvOnly()
	return maxRows, source, nil
}

// resolveMaxRowsEnvOnly reads BEADS_MAX_ROWS without consulting any flag.
// Used by the doctor family of commands (designer §4): bd doctor, bd lint,
// bd doctor-conventions, bd doctor-pollution. These are internal sweeps
// where the operator may want a guardrail via env var but no per-invocation
// flag is needed.
//
// On a bad env value, emits a warning to stderr and returns (0, ""). The
// command proceeds with the cap disabled rather than aborting — failing
// closed here would break automation that has a global BEADS_MAX_ROWS set
// but accidentally got a typo.
func resolveMaxRowsEnvOnly() (int, string) {
	raw, ok := os.LookupEnv(maxRowsEnvVar)
	if !ok || raw == "" {
		return 0, ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		fmt.Fprintf(os.Stderr,
			"Warning: %s=%q is not a non-negative integer; ignoring.\n",
			maxRowsEnvVar, raw)
		return 0, ""
	}
	return n, maxRowsEnvVar
}

// handleMaxRowsError checks whether err is a *issueops.ErrTooManyRows from
// the storage layer and, if so, emits the two-line stderr error message
// (designer §2.3) and returns an exit-coded error (code 2, main.go's
// exitCodeFromError convention — see &exitError{Code: 2} elsewhere in this
// package, e.g. list_input.go's skip-labels-conflict check). Returns nil
// when err is nil or any other type, letting the caller continue its
// existing error path (typically `return HandleError(...)`).
//
// Every caller must check the return value and propagate it immediately:
//
//	if capErr := handleMaxRowsError(err); capErr != nil {
//	    return capErr
//	}
//	return HandleError("%v", err)
//
// This used to call os.Exit(2) directly, which — since it runs from deep
// inside RunE, sometimes several calls below it — bypassed both the
// calling command's own `defer func() { metrics.CloseEventAndAdd(evt) }()`
// and main()'s post-ExecuteC metrics.CloseAndFlush(), stranding queued
// metrics. Returning through RunE like every other command error lets both
// run before process exit.
//
// The error is intentionally rendered without ANSI color and without
// touching stdout: a half-rendered JSON array on stdout would cause `jq`
// downstream to fail in a way unrelated to the cap, hiding the real cause.
func handleMaxRowsError(err error) error {
	if err == nil {
		return nil
	}
	var capErr *issueops.ErrTooManyRows
	if !errors.As(err, &capErr) {
		return nil
	}
	source := capErr.Source
	if source == "" {
		source = maxRowsEnvVar
	}
	fmt.Fprintf(os.Stderr, "Error: too many rows: %d found, %s=%d exceeded.\n",
		capErr.Found, source, capErr.Cap)
	fmt.Fprintln(os.Stderr,
		"       Refine the query (add filters, set --limit), or raise the cap with")
	fmt.Fprintln(os.Stderr,
		"       --max-rows N or BEADS_MAX_ROWS=N.")
	return &exitError{Code: 2}
}
