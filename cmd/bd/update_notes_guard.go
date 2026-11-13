package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// validateNotesUpdate is the GH#6021 guard: `--notes ""` erases the whole
// notes field, and the empty string is exactly what a dead command
// substitution (`--notes "$(cat missing.txt)"`) collapses to — at the flag
// layer the accident and the deliberate clear are the same bytes, so the
// empty value is refused outright and the deliberate clear gets its own verb,
// --clear-notes. GH#8149 later extended the same contract to the description,
// design and acceptance fields of update (their verbs are
// --clear-description, --clear-design and --clear-acceptance); the one
// remaining split is that description input from stdin or a file keeps its
// --allow-empty-description opt-in (emptiness there is an input-plumbing
// property, and create shares that validator with update), while notes has no
// file/stdin variant — inline is the only path, clearing already-empty notes
// is a no-op, and what needs authorizing is an intent, not a pipe. The
// notes-overwrite fence (issueops.NotesReplacement) exempts clears by design
// and --force does not reach here. Called only when --notes was explicitly
// passed.
func validateNotesUpdate(notes string) error {
	if notes != "" {
		return nil
	}
	return fmt.Errorf(`--notes "" would clear the notes field (an empty command substitution is the usual accident); use --clear-notes to clear it deliberately`)
}

// clearNotesRequested reports whether --clear-notes asked for the deliberate
// erase. Combining it with --notes or --append-notes is contradictory and
// refused by cobra's mutual-exclusion registration on the update command.
func clearNotesRequested(cmd *cobra.Command) bool {
	clear, _ := cmd.Flags().GetBool("clear-notes")
	return clear
}
