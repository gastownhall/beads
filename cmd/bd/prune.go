package main

import (
	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/issueops"
)

var pruneCmd = &cobra.Command{
	Use:     "prune",
	GroupID: "maint",
	Short:   "Delete old closed beads to reclaim space and shrink exports",
	Long: `Permanently delete closed non-ephemeral beads and their associated data.

Use this to trim closed regular beads (tasks, features, bugs, chores, etc.)
that are no longer useful. The common case is a long-lived repo where
closed work has piled up and is bloating auto-export or slowing queries.

Requires --older-than or --pattern. The flag is a safety gate — without
it, a muscle-memory ` + "`--force`" + ` could wipe every closed bead in the repo.
Use ` + "`--pattern '*'`" + ` if you really do want to sweep everything closed.

Deletes: issues, dependencies, labels, events, and comments for matching beads.
Skips: pinned beads (protected), open/in-progress beads, and ephemeral beads.

Also skips closed beads whose ID appears in the description, notes, or
comments of any open / in-progress bead. This protects ADR / decision /
verification trails that downstream beads still cite. Use
--ignore-references to override (e.g., when bulk-decommissioning a
retired label across the rig).

Also skips closed beads a live bead still depends on through a parent-child,
tracks or blocks edge: a closed epic whose child is still open, a closed bead
a live convoy tracks, a closed blocker of a live bead. Live means any status
that is not done. When any are held back, --json reports the count as
live_dependent_skipped. --ignore-references does not turn this off; to remove
such a bead on purpose use ` + "`bd delete`" + `.

To delete closed ephemeral beads (wisps, transient molecules) use
` + "`bd purge`" + ` instead.

--retain-ids retires every id the sweep deletes: a permanent tombstone (the id
and its closed_at) is written in the same transaction, and from then on a
create naming that id is refused (code idempotency_expired) and an import
skips it. Use it when ids are idempotency keys that must never run twice.
Tombstones are permanent.

Without --retain-ids, closed beads whose type the workspace's types.retained
setting names are skipped and reported (retention_required under --json):
their ids are idempotency keys, so only a retaining sweep may delete them.

For full Dolt storage reclaim after deleting many rows, follow with ` + "`bd flatten`" + `
so history can be collapsed and old chunks can be garbage-collected.

EXAMPLES:
  bd prune --older-than 30d              # Preview closed beads >30d old
  bd prune --older-than 30d --force      # Delete them
  bd prune --older-than 90d --dry-run    # Detailed preview with stats
  bd prune --pattern "*" --force         # Delete all closed regular beads
  bd prune --pattern "gm-temp-*" --force # Scope to a pattern`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent("prune")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ignoreRefs, _ := cmd.Flags().GetBool("ignore-references")
		return runPurgeOrPrune(cmd, purgeScope{
			cmdName:        "prune",
			pastTense:      "pruned",
			countKey:       "pruned_count",
			dryRunCountKey: "prune_count",
			subjectNoun:    "closed bead",
			tier:           issueops.SweepDurable,
			// The flag is the NEGATIVE of the request field: --ignore-references
			// asks the role NOT to protect. The reporting is unconditional, so
			// `--json` keeps publishing the keys with the flag set.
			protectReferenced: !ignoreRefs,
			// A stored edge is a stronger pointer than an id in prose, and the
			// text scan above does not see one: a child names its parent by a
			// parent-child edge, not by citing it. Without this a prune deleted
			// the closed parent of an open child and left the child a root.
			protectLiveDependents: true,
			reportsReferences:     true,
		})
	},
}

func init() {
	pruneCmd.Flags().BoolP("force", "f", false, "Actually prune (without this, shows preview)")
	pruneCmd.Flags().Bool("ignore-references", false, "Delete closed beads even when referenced by open beads (use with care; see --help for details)")
	pruneCmd.Flags().Bool("dry-run", false, "Preview what would be pruned with stats")
	pruneCmd.Flags().String("older-than", "", "Only prune beads closed more than N ago (e.g., 30d, 2w, 60, 36h)")
	pruneCmd.Flags().String("pattern", "", "Only prune beads matching ID glob pattern (e.g., 'gm-old-*')")
	pruneCmd.Flags().Bool("retain-ids", false, "Retire every pruned id with a permanent tombstone so it can never be created again (see --help)")
	rootCmd.AddCommand(pruneCmd)
}
