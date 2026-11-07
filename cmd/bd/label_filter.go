package main

import (
	"strings"

	"github.com/spf13/cobra"
)

// rejectEmptyLabelPatterns refuses a supplied --label-pattern or --label-regex
// that carries no pattern (GH#6636). Downstream, an empty pattern string means
// "no pattern" and the predicate is dropped, so a supplied-but-empty value
// would silently widen the query; cmd.Flags().Changed is the only place the
// omitted/empty distinction survives.
//
// The two flags draw the line differently. Labels are normalized (trimmed), so
// a glob that is blank after trimming can match nothing usable and is refused.
// A regex is matched unanchored and labels may contain spaces, so a regex of
// only whitespace is a real predicate; only the exactly-empty regex is refused.
// Accepted values are never rewritten.
func rejectEmptyLabelPatterns(cmd *cobra.Command, glob, regex string) error {
	if cmd.Flags().Changed("label-pattern") && strings.TrimSpace(glob) == "" {
		return HandleErrorRespectJSON("--label-pattern was supplied but contains no pattern")
	}
	if cmd.Flags().Changed("label-regex") && regex == "" {
		return HandleErrorRespectJSON("--label-regex was supplied but contains no pattern")
	}
	return nil
}
