package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// `bd dep tree` on ONE role and ONE body. What is left here is the front door's
// own work: flag vocabulary, id resolution, and rendering.

// treeTarget is a resolved root and the walker that can answer about it. The two
// travel together because on the DIRECT route resolveIDWithRouting may resolve
// the id against a prefix-ROUTED database, and the walk then has to be asked of
// that store rather than of the local one.
type treeTarget struct {
	rootID  string
	walker  issueops.TreeWalker
	cleanup func()
}

// resolveTreeTarget resolves the argument to an exact id and hands back the tree
// role for whichever route this invocation is on.
//
// RESOLUTION HAPPENS FIRST, AND THE ROLE IS ASKED FOR ONLY AFTER IT SUCCEEDS: a
// lookup that finds nothing must report the lookup failure, not a missing
// surface the command never got to use.
func resolveTreeTarget(ctx context.Context, arg string) (treeTarget, error) {
	if usesProxiedServer() {
		return proxiedTreeTarget(ctx, arg)
	}
	rootID, treeStore, cleanup, err := resolveIDWithRouting(ctx, store, arg)
	if err != nil {
		return treeTarget{}, err
	}
	walker, err := treeStore.TreeWalker()
	if err != nil {
		cleanup()
		return treeTarget{}, err
	}
	return treeTarget{rootID: rootID, walker: walker, cleanup: cleanup}, nil
}

// proxiedTreeTarget resolves the argument against the proxied server and hands
// back the provider's tree surface.
//
// THIS ROUTE GAINS PARTIAL-ID RESOLUTION, which it has never had: it passed the
// argument to the use case verbatim, so `bd dep tree a1b2` worked on a direct
// workspace and failed on a team server.
func proxiedTreeTarget(ctx context.Context, arg string) (treeTarget, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return treeTarget{}, err
	}
	rootID, err := utils.ResolvePartialID(ctx, uowMolReader{uw: uw}, arg)
	uw.Close(ctx)
	if err != nil {
		return treeTarget{}, fmt.Errorf("resolving issue ID %s: %w", arg, err)
	}
	if uowProvider == nil {
		return treeTarget{}, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.TreeWalkerSource)
	if !ok {
		return treeTarget{}, fmt.Errorf("proxied-server provider %T does not offer the dependency-tree surface", uowProvider)
	}
	walker, err := src.TreeWalker()
	if err != nil {
		return treeTarget{}, err
	}
	return treeTarget{rootID: rootID, walker: walker, cleanup: func() {}}, nil
}

// runDepTree is the whole of `bd dep tree` on both routes.
func runDepTree(cmd *cobra.Command, ctx context.Context, args []string) error {
	maxDepth, _ := cmd.Flags().GetInt("max-depth")
	reverse, _ := cmd.Flags().GetBool("reverse")
	directionFlag, _ := cmd.Flags().GetString("direction")
	statusFilter, _ := cmd.Flags().GetString("status")
	formatStr, _ := cmd.Flags().GetString("format")
	if strings.EqualFold(formatStr, "json") {
		jsonOutput = true
		formatStr = ""
	}

	// --reverse is the deprecated spelling of --direction=up, and it loses to an
	// explicit --direction exactly as it did before.
	if directionFlag == "" && reverse {
		directionFlag = string(issueops.TreeUp)
	} else if directionFlag == "" {
		directionFlag = string(issueops.TreeDown)
	}

	// The flag vocabulary is checked HERE rather than left to the role: these
	// refusals name a FLAG and are worded for the person who typed one, while
	// the role's name a request field. The role refuses the same two things
	// independently.
	direction := issueops.TreeDirection(directionFlag)
	switch direction {
	case issueops.TreeDown, issueops.TreeUp, issueops.TreeBoth:
	default:
		return HandleErrorRespectJSON("--direction must be 'down', 'up', or 'both'")
	}
	if maxDepth < 1 {
		return HandleErrorRespectJSON("--max-depth must be >= 1")
	}

	// The cap is resolved here and CARRIED ON THE REQUEST: the proxied route
	// used to refuse --max-rows outright because it threaded no cap. It threads
	// one now, so the flag means the same thing wherever the command runs.
	maxRows, maxRowsSource, err := resolveMaxRows(cmd)
	if err != nil {
		return err
	}

	target, err := resolveTreeTarget(ctx, args[0])
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	defer target.cleanup()

	result, err := target.walker.WalkTree(ctx, issueops.WalkTreeRequest{
		RootID:        target.rootID,
		Direction:     direction,
		MaxDepth:      maxDepth,
		Status:        types.Status(statusFilter),
		MaxRows:       maxRows,
		MaxRowsSource: maxRowsSource,
	})
	if err != nil {
		if capErr := handleMaxRowsError(err); capErr != nil {
			return capErr
		}
		return HandleErrorRespectJSON("%v", err)
	}
	tree := result.Nodes

	// Handle format presets (json handled earlier, near the flag read).
	if formatStr == "mermaid" {
		// The raw argument, not the resolved id: this is only read when the tree
		// is empty.
		outputMermaidTree(tree, args[0])
		return nil
	}

	if jsonOutput {
		// The role's slice is empty rather than nil for a successful call, so
		// this is `[]` and never `null` without a guard here.
		return outputJSON(tree)
	}

	if len(tree) == 0 {
		switch direction {
		case issueops.TreeUp:
			fmt.Printf("\n%s has no dependents\n", target.rootID)
		case issueops.TreeBoth:
			fmt.Printf("\n%s has no dependencies or dependents\n", target.rootID)
		default:
			fmt.Printf("\n%s has no dependencies\n", target.rootID)
		}
		return nil
	}

	switch direction {
	case issueops.TreeUp:
		fmt.Printf("\n%s Dependent tree for %s:\n\n", ui.RenderAccent("🌲"), target.rootID)
	case issueops.TreeBoth:
		fmt.Printf("\n%s Full dependency graph for %s:\n\n", ui.RenderAccent("🌲"), target.rootID)
	default:
		fmt.Printf("\n%s Dependency tree for %s:\n\n", ui.RenderAccent("🌲"), target.rootID)
	}

	renderTree(tree, maxDepth, string(direction))
	fmt.Println()
	return nil
}
