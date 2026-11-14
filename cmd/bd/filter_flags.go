package main

import "github.com/spf13/pflag"

// The filter-flag sweep (GH#6548): a repeated plain pflag string keeps only
// its last occurrence, so `bd search --status open --status closed` answered
// confidently over a silently narrowed set. bd list's five flags were fixed
// in #5739 with unionStringFlag/onceStringFlag; these helpers register the
// same values across the rest of the tree so the fix has exactly one
// spelling.
//
// Which helper a flag gets is a per-registration decision from its
// downstream contract, not a property of the flag's name:
//
//   - union, for filters whose consumer already accepts a comma-separated
//     OR set: `bd search --status` and `bd human list --status` reach
//     workapi's comma-OR status filter, `bd count --id` reaches
//     issueops.CountRequest.IDFilter (documented as a comma-separated id
//     set), and `bd gitlab sync --label/--type` feed documented comma lists.
//     A repeat joins the same set the equivalent comma input builds.
//   - once, for filters that are single-valued all the way down: a repeat
//     fails during parsing instead of silently narrowing. The instructive
//     case is `bd count --status`, which REFUSES where `bd search --status`
//     unions under the same flag name: issueops.CountRequest documents
//     Status as one stored status whose unrecognized values match nothing
//     and return 0, so unioning there would have turned a silent truncation
//     into a silent zero — the same defect wearing a different hat.
//
// Value-setting flags of the same names (bd create --type, bd update
// --status, bd gate create --type, ...) deliberately keep the plain
// spelling: last-one-wins is the shell's own override idiom on a setter
// (`bd create $DEFAULTS --type bug`) and narrows nothing.
//
// Both helpers allocate a value per registration rather than sharing a
// package-level one, for the reason registerCountFlags already documents: a
// test may register the same flag set on an independent command, and a
// shared value would leak one command's filter into the other.

// addUnionFilterFlag registers a comma-accumulating filter flag: repeated
// occurrences union like the equivalent comma input, with the same
// deduplication, and setting the empty string clears it (unionStringFlag).
func addUnionFilterFlag(fs *pflag.FlagSet, name, shorthand, usage string) {
	fs.VarP(&unionStringFlag{}, name, shorthand, usage)
}

// addOnceFilterFlag registers a repeat-refusing filter flag: a second
// non-empty occurrence without an intervening explicit empty reset fails
// during parsing with an error naming the flag (onceStringFlag).
func addOnceFilterFlag(fs *pflag.FlagSet, name, shorthand, defaultValue, usage string) {
	fs.VarP(&onceStringFlag{name: name, value: defaultValue}, name, shorthand, usage)
}
