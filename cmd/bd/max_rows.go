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
// (designer §2.2 / §4).
//
// The cap is honored on both the direct and the --proxied-server route for
// every command that registers it. It used to be refused under
// --proxied-server, a fact about each command's implementation rather than
// about the flag: `bd dep tree` and `bd list` were moved first, then `bd
// ready`, `bd graph` and `bd find-duplicates`, whose proxied routes already
// reached a seam that enforces it. A new command that registers the flag must
// thread the cap on both routes; one that cannot is refused at the front door
// by the mode-wide max-rows rule in proxy_capability.go until it does.
func addMaxRowsFlag(cmd *cobra.Command) {
	cmd.Flags().Int(maxRowsFlagName, 0,
		"Hard upper bound on rows returned. Returns a non-zero exit (code 2) "+
			"and an error to stderr if exceeded. 0 disables (the default). Overrides "+
			"BEADS_MAX_ROWS for this invocation. Useful in CI/agent rigs that want a "+
			"circuit breaker against pathological queries. Honored on both the direct "+
			"and the --proxied-server route.")
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
	n, source, raw, ok := parseMaxRowsEnv()
	if !ok {
		fmt.Fprintf(os.Stderr,
			"Warning: %s=%q is not a non-negative integer; ignoring.\n",
			maxRowsEnvVar, raw)
		return 0, ""
	}
	return n, source
}

// parseMaxRowsEnv reads BEADS_MAX_ROWS without warning about a malformed
// value. Returns (cap, source, raw, ok); ok is false only for a value that is
// present but unusable, in which case the cap is disabled exactly as
// resolveMaxRowsEnvOnly disables it. An unset or empty variable is a valid
// "no cap" and reports ok.
func parseMaxRowsEnv() (int, string, string, bool) {
	raw, ok := os.LookupEnv(maxRowsEnvVar)
	if !ok || raw == "" {
		return 0, "", raw, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, "", raw, false
	}
	return n, maxRowsEnvVar, raw, true
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
