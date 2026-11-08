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

		dt, err := resolveLinkDependencyType(depType)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		// link's own checks run before anything is written, and over http
		// before the dial: the dotted-child rule (the role raises it too).
		// Unlike `bd dep add`, link takes any type a workspace spells — the
		// resolver below only canonicalizes the documented aliases and checks
		// the type's length, never the well-known vocabulary.
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
				"type":          string(dt),
			})
		}
		fmt.Printf("%s Linked: %s depends on %s (%s)\n",
			ui.RenderPass("✓"), formatFeedbackIDParen(fromID, fromTitle), formatFeedbackIDParen(toID, toTitle), dt)
		return nil
	},
}

// resolveLinkDependencyType is link's share of the dep-add pair in
// create_deps.go: it maps the documented alias spellings "depends-on" and
// "blocked-by" to the canonical "blocks" storage type that ready/blocked
// gating matches (GH#5585), so an alias stores and gates exactly like
// `--type blocks`. Unlike `bd dep add`, link takes any type a workspace
// spells — its documented custom-type contract (backend/http's custom-type
// tests pin it) — so the only further check is the structural one: the type
// must be non-empty and fit the storage column, the same IsValid limit the
// raw check used to enforce.
func resolveLinkDependencyType(raw string) (types.DependencyType, error) {
	dt := canonicalDependencyType(types.DependencyType(raw))
	if !dt.IsValid() {
		return "", fmt.Errorf("invalid dependency type %q: must be non-empty and at most %d characters", raw, types.MaxDependencyTypeLen)
	}
	return dt, nil
}

// linkTypeFlagHelp renders bd link's --type usage from the accepted-type
// list the shared validators check (createDepsAcceptedTypeList: the
// well-known types plus the depends-on/blocked-by aliases), so the help
// cannot drift from the code again. Link also takes custom types; the help
// says what they do and do not buy.
func linkTypeFlagHelp() string {
	return fmt.Sprintf(
		"Dependency type (%s). Custom types are accepted too (non-empty, max %d chars); they are stored as-is and do not gate ready work",
		createDepsAcceptedTypeList(), types.MaxDependencyTypeLen)
}

func init() {
	linkCmd.Flags().StringP("type", "t", "blocks", linkTypeFlagHelp())
	linkCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(linkCmd)
}
