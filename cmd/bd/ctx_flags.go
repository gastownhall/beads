package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/types"
)

// addCtxFlags registers the declared-execution-context flags on a listing
// command. `bd ready`, `bd list` and `bd blocked` all take them, so the three
// cannot drift on spelling or help text.
//
// The flags state what the CALLER can do; the issue side is a label under
// types.RequiresLabelPrefix (`requires:gpu`, `requires:host=studio`). A reader
// that declares nothing sees every row, as it always has; a reader that
// declares a context, even an empty one, is hidden every issue whose
// requirements it does not meet. See types.RequiresSatisfiedLabels for the
// satisfaction rule.
func addCtxFlags(fs *pflag.FlagSet) {
	fs.StringSlice("ctx", nil, "Declare your execution context as k or k=v tokens (repeatable or comma-separated). Hides issues whose requires:<token> labels this context does not satisfy; --ctx '' declares an empty context. Omitted: no hiding")
	fs.String("ctx-file", "", "Read context tokens from a file, one per line (# comments allowed); combines with --ctx")
}

// ctxFromFlags reads --ctx and --ctx-file. It returns nil when NEITHER flag
// was given, so the filter's "not declared" state is the flags' absence and
// nothing else; any use of either flag yields a non-nil slice, empty when the
// tokens normalize away (`--ctx ”`, an empty file). That nil/empty split is
// the predicate's whole semantics (types.WorkFilter.Ctx), so the slice goes
// through types.NormalizeCtx, never utils.NormalizeLabels.
func ctxFromFlags(cmd *cobra.Command) ([]string, error) {
	if !cmd.Flags().Changed("ctx") && !cmd.Flags().Changed("ctx-file") {
		return nil, nil
	}
	tokens, _ := cmd.Flags().GetStringSlice("ctx")
	out := append([]string{}, tokens...)
	if path, _ := cmd.Flags().GetString("ctx-file"); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the caller's own flag value
		if err != nil {
			return nil, fmt.Errorf("--ctx-file: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
	}
	return types.NormalizeCtx(out), nil
}

// ctxModeConflict is the usage error for a --ctx on a `bd ready` arm that does
// not run the ready query the predicate is built into. --gated lists molecules
// whose gate closed, --mol lists one molecule's children through its own
// query, and --explain answers for a set the caller already sees; none of them
// reads WorkFilter.Ctx, so accepting the flag there would be the silent no-op
// a declared context exists to rule out. Like briefModeConflict it is one body
// with two callers: the RunE above the mode dispatch, and the gatherer.
func ctxModeConflict(ctxDeclared, gated, explain bool, molID string) error {
	if !ctxDeclared {
		return nil
	}
	switch {
	case gated:
		return HandleErrorRespectJSON("--ctx cannot be combined with --gated; the gate-resume listing does not run the ready query the context predicate is part of")
	case molID != "":
		return HandleErrorRespectJSON("--ctx cannot be combined with --mol; the molecule listing does not run the ready query the context predicate is part of")
	case explain:
		return HandleErrorRespectJSON("--ctx cannot be combined with --explain; explain reports why rows are NOT ready, and a declared context hides rows rather than explaining them")
	}
	return nil
}

func ctxModeConflictFromFlags(cmd *cobra.Command) error {
	declared := cmd.Flags().Changed("ctx") || cmd.Flags().Changed("ctx-file")
	gated, _ := cmd.Flags().GetBool("gated")
	explain, _ := cmd.Flags().GetBool("explain")
	molID, _ := cmd.Flags().GetString("mol")
	return ctxModeConflict(declared, gated, explain, molID)
}
