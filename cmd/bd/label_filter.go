package main

import (
	"strings"

	"github.com/spf13/cobra"
)

// rejectEmptyLabelPatterns refuses the two pattern flags `bd list` and
// `bd ready` share when one was supplied but carries no pattern: a pattern is
// matched, not compared, and a supplied-but-empty one used to be silently read
// as "no pattern", dropping the label predicate and widening the listing
// (GH#6636). Both gatherers call it ahead of the route split, so the direct
// and proxied routes of both commands refuse the same spellings.
//
// The two flags draw the emptiness line in different places, deliberately.
// --label-pattern is a glob over normalized labels, which are trimmed, so a
// whitespace-only glob can never match one and is refused after trimming.
// --label-regex is an unanchored substring REGEXP over the raw label, and
// labels may contain spaces (label_whitespace_warn.go), so a whitespace-only
// regex is a real predicate that can still match; only the exact empty
// string is refused. Neither value is rewritten on the way through:
// accepted patterns reach the query byte-for-byte, padding and syntax
// included.
func rejectEmptyLabelPatterns(cmd *cobra.Command, glob, regex string) error {
	if cmd.Flags().Changed("label-pattern") && strings.TrimSpace(glob) == "" {
		return HandleErrorRespectJSON("--label-pattern was supplied but contains no pattern")
	}
	if cmd.Flags().Changed("label-regex") && regex == "" {
		return HandleErrorRespectJSON("--label-regex was supplied but contains no pattern")
	}
	return nil
}
