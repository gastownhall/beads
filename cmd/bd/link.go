package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// linkWellKnownTypeNames is the sorted list of --type values bd link accepts.
// It is derived from the same WellKnownDependencyTypes list that
// validateDependencyType enforces, so the help text cannot drift from the code
// again: the previous static string named five of the nineteen accepted types
// and neither alias (review on #5648).
func linkWellKnownTypeNames() []string {
	wellKnown := types.WellKnownDependencyTypes()
	names := make([]string, 0, len(wellKnown))
	for _, dt := range wellKnown {
		names = append(names, string(dt))
	}
	sort.Strings(names)
	return names
}

func linkTypeFlagHelp() string {
	return fmt.Sprintf("Dependency type (%s); 'blocked-by' and 'depends-on' are accepted as aliases for 'blocks'",
		strings.Join(linkWellKnownTypeNames(), "|"))
}

func linkLongHelp() string {
	return fmt.Sprintf(`Link two issues with a dependency.

Shorthand for 'bd dep add <id1> <id2>'. By default creates a "blocks"
dependency (id2 blocks id1). Use --type to specify a different relationship.

Accepted --type values:
%s
'blocked-by' and 'depends-on' are accepted as aliases for 'blocks'; custom
dependency types are rejected, matching 'bd dep add' and 'bd create --deps'.

Examples:
  bd link bd-123 bd-456                    # bd-456 blocks bd-123
  bd link bd-123 bd-456 --type related     # bd-123 related to bd-456
  bd link bd-123 bd-456 --type parent-child`, wrapTypeNames(linkWellKnownTypeNames(), 76))
}

// wrapTypeNames renders names as comma-separated, indented lines no wider
// than width, so the nineteen-entry list stays readable in --help.
func wrapTypeNames(names []string, width int) string {
	var b strings.Builder
	line := "  "
	for i, n := range names {
		item := n
		if i < len(names)-1 {
			item += ","
		}
		if len(line) > 2 && len(line)+1+len(item) > width {
			b.WriteString(line + "\n")
			line = "  "
		}
		if len(line) > 2 {
			line += " "
		}
		line += item
	}
	b.WriteString(line)
	return b.String()
}

var linkCmd = &cobra.Command{
	Use:           "link <id1> <id2>",
	GroupID:       "issues",
	Short:         "Link two issues with a dependency",
	Long:          linkLongHelp(),
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

		dt := canonicalDependencyType(types.DependencyType(depType))
		if isDisallowedHierarchicalDependency(fromID, toID, dt) {
			return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
		}

		if err := validateDependencyType(dt); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        dt,
		}

		if err := fromStore.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		warnIfCyclesExist(fromStore)

		if err := commitPendingIfEmbedded(ctx, fromStore, actor, doltAutoCommitParams{
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
			ui.RenderPass("✓"), formatFeedbackIDParen(fromID, lookupTitle(fromID)), formatFeedbackIDParen(toID, lookupTitle(toID)), dt)
		return nil
	},
}

func init() {
	linkCmd.Flags().StringP("type", "t", "blocks", linkTypeFlagHelp())
	linkCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(linkCmd)
}
