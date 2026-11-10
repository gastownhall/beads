package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

var linkCmd = &cobra.Command{
	Use:     "link <id1> <id2>",
	GroupID: "issues",
	Short:   "Link two issues with a dependency",
	Long: `Link two issues with a dependency.

Shorthand for 'bd dep add <id1> <id2>'. By default creates a "blocks"
dependency (id2 blocks id1). Use --type to specify a different relationship.

Examples:
  bd link bd-123 bd-456                    # bd-456 blocks bd-123
  bd link bd-123 bd-456 --type related     # bd-123 related to bd-456
  bd link bd-123 bd-456 --type parent-child`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("link")

		evt := metrics.NewCommandEvent("link")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLinkProxiedServer(cmd, rootCtx, args)
		}

		id1 := args[0]
		id2 := args[1]
		depType, _ := cmd.Flags().GetString("type")

		ctx := rootCtx

		// Resolve partial IDs with routing support. The source issue's store
		// is mutated by AddDependency below, so resolve it write-intent
		// (#4141); the dependency target is only resolved by ID and stays
		// read-only (bd-6dnrw.32, GH#3231).
		fromID, fromStore, fromCleanup, err := resolveIDForMutation(ctx, store, id1)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer fromCleanup()

		toID, _, toCleanup, err := resolveIDWithRouting(ctx, store, id2)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer toCleanup()

		// link's own checks run before anything is written, and over http
		// before the dial: the type's length and the dotted-child rule (the
		// role raises it too), both on the canonical type.
		dt, err := linkDependencyType(depType)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if err := issueops.CheckDottedChildDependency(fromID, toID, dt); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		// Read the titles while the resolution's reads are fresh, as `bd dep
		// add` does: an edge changes neither title, and over http a lookup
		// after the write would be two more round trips.
		var fromTitle, toTitle string
		if !jsonOutput {
			fromTitle, toTitle = lookupTitle(fromID), lookupTitle(toID)
		}

		// The write goes through the DependencyEditor role, the route `bd dep
		// add` takes, so it is served on every backend: a local store and an
		// http workspace alike (the http client refuses the raw
		// AddDependencyWithOptions by design). The role records the
		// dependency_added event the raw verb's EmitEvent did.
		edge := issueops.DependencyEdge{IssueID: fromID, DependsOnID: toID, Type: dt}
		if err := addDependencyEdgesDirect(opsCtx, fromStore, []issueops.DependencyEdge{edge}, false); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		warnIfCyclesExist(fromStore)

		if err := commitPendingIfEmbedded(ctx, fromStore, currentActor(), doltAutoCommitParams{
			Command:  "link",
			IssueIDs: []string{fromID, toID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(fromID)

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          dt,
			})
		}
		fmt.Printf("%s Linked: %s depends on %s (%s)\n",
			ui.RenderPass("✓"), formatFeedbackIDParen(fromID, fromTitle), formatFeedbackIDParen(toID, toTitle), dt)
		return nil
	},
}

// linkDependencyType resolves the --type value bd link stores. The documented
// aliases "blocked-by" and "depends-on" become the canonical "blocks" that
// ready/blocked gating matches, as they do for `bd dep add` (GH#5585). Unlike
// `bd dep add`, link takes any other type a workspace spells, kept exactly as
// given, so it never asks validateDependencyType: only the length is checked.
func linkDependencyType(raw string) (types.DependencyType, error) {
	dt := canonicalDependencyType(types.DependencyType(raw))
	if !dt.IsValid() {
		return "", fmt.Errorf("invalid dependency type %q: must be non-empty and at most %d characters", raw, types.MaxDependencyTypeLen)
	}
	return dt, nil
}

func init() {
	// The list is the one validateDependencyType checks for `bd dep add`, so
	// the help cannot drift from the types bd knows.
	linkCmd.Flags().StringP("type", "t", "blocks", fmt.Sprintf(
		"Dependency type: one of %s (blocked-by and depends-on are stored as blocks), or any non-empty custom type of at most %d characters, stored as given; a custom type does not gate ready work",
		createDepsAcceptedTypeList(), types.MaxDependencyTypeLen))
	linkCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(linkCmd)
}
