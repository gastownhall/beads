package main

import (
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
// Shared by ready, list, count and orphans: each gathers this flag's raw
// value off its own command and calls this before doing anything else with
// it, so the same supplied-but-empty filter is refused everywhere it can be
// spelled rather than in just the one command it was first noticed on.
func rejectEmptyLabelFilter(cmd *cobra.Command, flagName string, raw []string) error {
	if !cmd.Flags().Changed(flagName) {
		return nil
	}
	if len(utils.NormalizeLabels(raw)) == 0 {
		return HandleErrorRespectJSON("--%s was supplied but contains no usable label", flagName)
	}
	return nil
}
