package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

var relateCmd = &cobra.Command{
	Use:   "relate <id1> <id2>",
	Short: "Create a bidirectional relates_to link between issues",
	Long: `Create a loose 'see also' relationship between two issues.

The relates_to link is bidirectional - both issues will reference each other.
This enables knowledge graph connections without blocking or hierarchy.

Examples:
  bd relate bd-abc bd-xyz    # Link two related issues
  bd relate bd-123 bd-456    # Create see-also connection`,
	Args: cobra.ExactArgs(2),
	RunE: runRelate,
}

var unrelateCmd = &cobra.Command{
	Use:   "unrelate <id1> <id2>",
	Short: "Remove a relates_to link between issues",
	Long: `Remove a relates_to relationship between two issues.

Removes the link in both directions.

Example:
  bd unrelate bd-abc bd-xyz`,
	Args: cobra.ExactArgs(2),
	RunE: runUnrelate,
}

func init() {
	// Issue ID completions
	relateCmd.ValidArgsFunction = issueIDCompletion
	unrelateCmd.ValidArgsFunction = issueIDCompletion

	// Add as subcommands of dep
	depCmd.AddCommand(relateCmd)
	depCmd.AddCommand(unrelateCmd)
}

// relateHalf is one direction of a bidirectional relates-to link: the edge
// from -> to, written through the store that owns the SOURCE issue's row.
type relateHalf struct {
	from, to string
	st       storage.DoltStorage
}

// writeRelateHalf adds its directed edge through its source store's
// DependencyEditor, the role bd dep add and bd link write through, so each
// direction records its own dependency_added event with its own source row
// (EmitEvent parity with the raw verbs the role replaced).
func (h relateHalf) write(ctx context.Context) error {
	return addDependencyEdgesDirect(ctx, h.st, []issueops.DependencyEdge{{
		IssueID:     h.from,
		DependsOnID: h.to,
		Type:        types.DepRelatesTo,
	}}, false)
}

// removeRelateHalf removes h's edge through its source store's
// DependencyEditor, the role bd dep remove writes through. It records the
// dependency_removed event only when an edge actually came off; removing a
// half that is not there is a no-op success, keeping bd unrelate idempotent.
func (h relateHalf) remove(ctx context.Context) error {
	editor, err := h.st.DependencyEditor()
	if err != nil {
		return err
	}
	_, err = editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{
		Actor:       currentActor(),
		IssueID:     h.from,
		DependsOnID: h.to,
	})
	return err
}

// resolveRelateEndpoints resolves both relate/unrelate endpoints through the
// routing cascade a dependency verb uses (#3168): the local store first, then
// the routes.jsonl prefix route, then auto-routing. Both endpoints become
// SOURCE issues of the bidirectional link — each store gains its own outgoing
// edge — so both resolve write-intent: a routed endpoint's store is the one its
// edge is written through (#4141).
//
// Both endpoints resolve before any write, so an unresolvable one (AC-3)
// fails the command naming itself with no edge or event written anywhere.
// Unlike bd dep add there is no unresolved-target passthrough: a relates-to
// link is a resolved-issues verb, not an add-now-route-later edge.
func resolveRelateEndpoints(ctx context.Context, args []string) (id1, id2 string, store1, store2 storage.DoltStorage, cleanup func(), err error) {
	var cleanup1, cleanup2 func()
	id1, store1, cleanup1, err = resolveIDForMutation(ctx, store, args[0])
	if err != nil {
		return "", "", nil, nil, func() {}, fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	id2, store2, cleanup2, err = resolveIDForMutation(ctx, store, args[1])
	if err != nil {
		cleanup1()
		return "", "", nil, nil, func() {}, fmt.Errorf("failed to resolve %s: %w", args[1], err)
	}
	return id1, id2, store1, store2, func() { cleanup1(); cleanup2() }, nil
}

func runRelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("relate")

	evt := metrics.NewCommandEvent("relate")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	if usesProxiedServer() {
		return runRelateProxiedServer(ctx, args)
	}

	id1, id2, store1, store2, cleanup, err := resolveRelateEndpoints(ctx, args)
	if err != nil {
		return err
	}
	defer cleanup()

	if id1 == id2 {
		return fmt.Errorf("cannot relate an issue to itself")
	}

	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	// Deduplicate writable stores by ACTUAL DESTINATION, not by ExtractPrefix:
	// prefixes can share a database (a multi-prefix rig) and longest-match
	// routes can send different prefixes to one directory, while two handles
	// on one database are still separate destinations to the caller's own
	// store key. One destination keeps the single-store behavior — both
	// edges through one store, one commit, partial errors reported as they
	// happen.
	oneDestination := dependencyStoreKey(store1) == dependencyStoreKey(store2)

	edge1 := relateHalf{from: id1, to: id2, st: store1}
	edge2 := relateHalf{from: id2, to: id1, st: store1}
	if !oneDestination {
		edge2.st = store2
	}

	// Direction 1: id1 -> id2, in id1's store.
	if err := edge1.write(opsCtx); err != nil {
		return fmt.Errorf("failed to add relates-to %s -> %s: %w", id1, id2, err)
	}

	if !oneDestination {
		// The two halves have no cross-store atomicity (none exists between
		// two Dolt databases), so commit this direction before attempting the
		// other: a later failure then strands at most one written-but-known
		// half, never two half-committed ones.
		if err := commitPendingIfEmbedded(ctx, store1, currentActor(), doltAutoCommitParams{
			Command:  "relate",
			IssueIDs: []string{id1, id2},
		}); err != nil {
			return fmt.Errorf("failed to commit relates-to %s -> %s; the reverse edge %s -> %s was not added — rerun `bd relate %s %s` to retry: %w",
				id1, id2, id2, id1, args[0], args[1], err)
		}
	}

	// Direction 2: id2 -> id1, in id2's store.
	if err := edge2.write(opsCtx); err != nil {
		if !oneDestination {
			return fmt.Errorf("relates-to %s -> %s was added in its source store, but adding the reverse edge %s -> %s failed — rerun `bd relate %s %s` to retry: %w",
				id1, id2, id2, id1, args[0], args[1], err)
		}
		return fmt.Errorf("failed to add relates-to %s -> %s: %w", id2, id1, err)
	}

	if err := commitPendingIfEmbedded(ctx, edge2.st, currentActor(), doltAutoCommitParams{
		Command:  "relate",
		IssueIDs: []string{id1, id2},
	}); err != nil {
		return fmt.Errorf("failed to commit: %v", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id1":     id1,
			"id2":     id2,
			"related": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Linked %s ↔ %s\n", ui.RenderPass("✓"), id1, id2)
	return nil
}

func runUnrelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("unrelate")

	evt := metrics.NewCommandEvent("unrelate")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	if usesProxiedServer() {
		return runUnrelateProxiedServer(ctx, args)
	}

	id1, id2, store1, store2, cleanup, err := resolveRelateEndpoints(ctx, args)
	if err != nil {
		return err
	}
	defer cleanup()

	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	// Same destination-deduplication as relate: one destination keeps the
	// single-store remove behavior.
	oneDestination := dependencyStoreKey(store1) == dependencyStoreKey(store2)

	edge1 := relateHalf{from: id1, to: id2, st: store1}
	edge2 := relateHalf{from: id2, to: id1, st: store1}
	if !oneDestination {
		edge2.st = store2
	}

	// Remove id1 -> id2, in id1's store.
	if err := edge1.remove(opsCtx); err != nil {
		return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id1, id2, err)
	}

	if !oneDestination && edge2.st != store1 {
		if err := commitPendingIfEmbedded(ctx, store1, currentActor(), doltAutoCommitParams{
			Command:  "unrelate",
			IssueIDs: []string{id1, id2},
		}); err != nil {
			return fmt.Errorf("failed to commit removal of relates-to %s -> %s; the reverse edge %s -> %s was not removed — rerun `bd unrelate %s %s` to retry: %w",
				id1, id2, id2, id1, args[0], args[1], err)
		}
	}

	// Remove id2 -> id1, in id2's store.
	if err := edge2.remove(opsCtx); err != nil {
		if !oneDestination {
			return fmt.Errorf("relates-to %s -> %s was removed in its source store, but removing the reverse edge %s -> %s failed — rerun `bd unrelate %s %s` to retry: %w",
				id1, id2, id2, id1, args[0], args[1], err)
		}
		return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id2, id1, err)
	}

	if err := commitPendingIfEmbedded(ctx, edge2.st, currentActor(), doltAutoCommitParams{
		Command:  "unrelate",
		IssueIDs: []string{id1, id2},
	}); err != nil {
		return fmt.Errorf("failed to commit: %v", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id1":       id1,
			"id2":       id2,
			"unrelated": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Unlinked %s ↔ %s\n", ui.RenderPass("✓"), id1, id2)
	return nil
}

// Note: contains, remove, formatRelatesTo functions removed per Decision 004
// relates-to links now use dependencies API instead of Issue.RelatesTo field
