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
			"circuit breaker against pathological queries.")
}

// resolveMaxRows picks the effective cap from --max-rows then BEADS_MAX_ROWS.
// Returns (cap, source) where cap == 0 disables the cap and source is one of
// "--max-rows", "BEADS_MAX_ROWS", or "". A negative flag is a usage error
// and aborts with exit code 1. A non-integer env value emits a warning to
// stderr and is ignored (returns cap == 0).
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
func resolveMaxRows(cmd *cobra.Command) (int, string) {
	if cmd != nil && cmd.Flags().Changed(maxRowsFlagName) {
		n, err := cmd.Flags().GetInt(maxRowsFlagName)
		if err != nil {
			FatalError("--max-rows: %v", err)
		}
		if n < 0 {
			FatalError("--max-rows must be non-negative; got %d", n)
		}
		return n, "--" + maxRowsFlagName
	}
	return resolveMaxRowsEnvOnly()
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
// (designer §2.3) and exits with code 2. Returns without action when err
// is nil or any other type, letting the caller continue its existing error
// path.
//
// The error is intentionally rendered without ANSI color and without
// touching stdout: a half-rendered JSON array on stdout would cause `jq`
// downstream to fail in a way unrelated to the cap, hiding the real cause.
func handleMaxRowsError(err error) {
	if err == nil {
		return
	}
	var capErr *issueops.ErrTooManyRows
	if !errors.As(err, &capErr) {
		return
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
	os.Exit(2)
}
