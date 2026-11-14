package main

import "github.com/spf13/pflag"

// A filter flag registered as a plain pflag string keeps only its LAST
// occurrence, so `--status open --status closed` silently narrows to closed.
// These helpers register a filter flag on one of the two values bd list
// already uses instead (list_filter_flags.go):
//
//   - addUnionFilterFlag: the consumer takes a comma-separated set, so a
//     repeat joins the set exactly as the comma form would.
//   - addOnceFilterFlag: the consumer takes ONE value all the way down, so a
//     second non-empty occurrence is refused while flags are parsed, before
//     any hook or RunE runs.
//
// Each call allocates its own value rather than sharing a package-level one:
// a test may register the same flag set on an independent command
// (registerCountFlags), and a shared value would leak one command's filter
// into the other. The values report Type() "string", so GetString, help
// text and the registered default are unchanged.
//
// Only filters are swept. A setter of the same name (bd create --type,
// bd update --status, ...) stays a plain string: there last-one-wins is the
// shell's override idiom and narrows nothing.

// addUnionFilterFlag registers a string filter flag whose repeats union into
// a comma-joined set. Union filters start empty.
func addUnionFilterFlag(fs *pflag.FlagSet, name, shorthand, usage string) {
	fs.VarP(&unionStringFlag{}, name, shorthand, usage)
}

// addOnceFilterFlag registers a single-valued string filter flag that refuses
// a repeated non-empty occurrence. defaultValue is what GetString reports
// when the flag is not given (bd jira sync --state defaults to "all"); it
// does not count as an occurrence, so one explicit value still replaces it.
func addOnceFilterFlag(fs *pflag.FlagSet, name, shorthand, defaultValue, usage string) {
	fs.VarP(&onceStringFlag{name: name, value: defaultValue}, name, shorthand, usage)
}
