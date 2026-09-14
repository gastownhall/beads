package main

import (
	"github.com/spf13/cobra"
)

// Repeated filter flags, everywhere but `bd list`.
//
// #5739 fixed `bd list`: a repeated --status kept only the last value, so a
// 477-issue census answered over 3 rows and nothing in the output said so.
// list_filter_flags.go holds the two value types that fix decided on —
// unionStringFlag for a filter whose downstream takes a comma-separated OR
// set, onceStringFlag for one that is single-valued all the way down. This
// file applies them to the rest of the command tree.
//
// Registration goes through addUnionFilterFlag / addOnceFilterFlag rather than
// through a package-level var per flag, for the reason registerCountFlags
// documents: a test may register the same flag set on an INDEPENDENT command,
// and a shared value would leak one command's filter into the other. Each call
// allocates its own value and records how to reset it.
//
// The reset exists because a pflag value persists across in-process Execute()
// calls (the cmd/bd test binary, library embedders) and an accumulating value
// would otherwise carry one invocation's filter into the next. It runs from a
// cobra finalizer, so every command gets it without a per-command edit anyone
// can forget. KNOWN LIMIT, shared with #5739: a parse that FAILS never reaches
// the finalizer, so an in-process caller that swallows a refusal should call
// resetAllFilterFlags itself before re-executing.
type filterFlagEntry struct {
	cmd   *cobra.Command
	name  string
	reset func()
}

var registeredFilterFlags []filterFlagEntry

// addUnionFilterFlag registers a filter flag whose repeats UNION into the
// comma form the downstream already accepts: `--status open --status closed`
// means exactly `--status open,closed`.
func addUnionFilterFlag(cmd *cobra.Command, name, shorthand, def, usage string, dest *string) {
	v := &unionStringFlag{value: def, def: def, dest: dest}
	if dest != nil {
		*dest = def
	}
	if shorthand == "" {
		cmd.Flags().Var(v, name, usage)
	} else {
		cmd.Flags().VarP(v, name, shorthand, usage)
	}
	recordFilterFlag(cmd, name, v.reset)
}

// addOnceFilterFlag registers a filter flag that REFUSES a repeat. It belongs
// on filters that are single-valued all the way down, where joining the values
// would either fail validation on the joined string (a loud but misdirected
// error) or match nothing and return an empty result that reads as a clean
// answer — the same defect in a new spelling.
func addOnceFilterFlag(cmd *cobra.Command, name, shorthand, def, usage string, dest *string) {
	v := &onceStringFlag{name: name, value: def, def: def, dest: dest}
	if dest != nil {
		*dest = def
	}
	if shorthand == "" {
		cmd.Flags().Var(v, name, usage)
	} else {
		cmd.Flags().VarP(v, name, shorthand, usage)
	}
	recordFilterFlag(cmd, name, v.reset)
}

func recordFilterFlag(cmd *cobra.Command, name string, reset func()) {
	registeredFilterFlags = append(registeredFilterFlags, filterFlagEntry{cmd: cmd, name: name, reset: reset})
}

// resetAllFilterFlags returns every registered filter flag to its unparsed
// state: the value goes back to the flag's DEFAULT (not to empty — `bd jira
// sync --state` defaults to "all") and pflag's Changed bit is lowered, so an
// in-process caller sees neither the previous filter nor a stale "the user
// passed --status" signal.
func resetAllFilterFlags() {
	for _, e := range registeredFilterFlags {
		e.reset()
		if fl := e.cmd.Flags().Lookup(e.name); fl != nil {
			fl.Changed = false
		}
	}
	// listCmd's five predate this registry and keep their own reset (#5739).
	resetListFilterFlags(listCmd.Flags())
}

func init() {
	cobra.OnFinalize(resetAllFilterFlags)
}
