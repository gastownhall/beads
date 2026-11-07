package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/utils"
)

// rejectEmptyLabelFilter reports whether flagName was supplied but every
// element in raw is blank, in which case the caller meant "match nothing"
// (however it phrased that), not "no filter" -- and "no filter" is exactly
// what an empty NormalizeLabels result means everywhere downstream. Using
// cmd.Flags().Changed keeps that distinction, which the normalized slice
// itself has already destroyed by the time any caller sees it.
//
// Ported from #6632, which routes the listing commands' --label, --label-any
// and --exclude-label through it; here `bd migrate issues --label` uses it
// (GH#6636), where "no filter" would select every issue in --from.
func rejectEmptyLabelFilter(cmd *cobra.Command, flagName string, raw []string) error {
	if !cmd.Flags().Changed(flagName) {
		return nil
	}
	if len(utils.NormalizeLabels(raw)) == 0 {
		return HandleErrorRespectJSON("--%s was supplied but contains no usable label", flagName)
	}
	return nil
}

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
