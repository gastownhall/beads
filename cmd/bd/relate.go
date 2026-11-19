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

	// Resolve both endpoints with prefix routing, each against the store that
	// owns it (GH#4647): a relates-to half is an edge whose SOURCE is that
	// endpoint, and an edge lives in its source's store.
	ends, cleanup, err := resolveRelateEndpoints(ctx, store, args)
	if err != nil {
		return err
	}
	defer cleanup()
	id1, id2 := ends[0].id, ends[1].id

	if id1 == id2 {
		return fmt.Errorf("cannot relate an issue to itself")
	}

	// Add relates-to dependency: id1 -> id2 (bidirectional, so also id2 -> id1)
	// Per Decision 004, relates-to links are now stored in dependencies table
	if err := writeRelateHalves(ctx, ends, false); err != nil {
		return err
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

	// Resolve both endpoints with prefix routing, as bd relate does: each
	// relates-to half is removed from its source's own store.
	ends, cleanup, err := resolveRelateEndpoints(ctx, store, args)
	if err != nil {
		return err
	}
	defer cleanup()
	id1, id2 := ends[0].id, ends[1].id

	// Remove relates-to dependency in both directions
	// Per Decision 004, relates-to links are now stored in dependencies table.
	// bd unrelate is an explicit dependency verb, so it records history
	// (EmitEvent) like bd dep remove; only structural teardown stays silent.
	if err := writeRelateHalves(ctx, ends, true); err != nil {
		return err
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

// relateEndpoint is one resolved bd relate / bd unrelate argument: the full ID
// and the store that owns the issue, which is where the relates-to half whose
// SOURCE is this endpoint is written or removed.
type relateEndpoint struct {
	id    string
	store storage.DoltStorage
}

// resolveRelateEndpoints resolves both arguments of bd relate / bd unrelate
// with prefix routing (GH#4647), the way bd dep add and bd link resolve theirs,
// opening a routed owner writable because each endpoint's store takes a write.
// Every resolution happens before anything is written, so an endpoint that
// resolves nowhere is named in the error and leaves both stores untouched.
//
// The local store keeps lookup precedence for both arguments. A store the first
// argument already opened is asked for the second before routing opens
// another: a rig's two issues share that one handle rather than opening the
// rig's embedded writer twice. Two handles onto one destination
// (dependencyStoreKey) are collapsed to the first, so a same-store link stays
// one store's write.
//
// The returned cleanup closes only the stores routing opened.
func resolveRelateEndpoints(ctx context.Context, local storage.DoltStorage, args []string) ([2]relateEndpoint, func(), error) {
	var ends [2]relateEndpoint
	var closers []func()
	cleanup := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	id1, st1, close1, err := resolveIDForMutation(ctx, local, args[0])
	if err != nil {
		return ends, func() {}, fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	closers = append(closers, close1)
	ends[0] = relateEndpoint{id: id1, store: st1}

	if st1 != local {
		// Local first, as resolveIDForMutation itself would; then the store
		// the first argument opened; only then a fresh route.
		for _, candidate := range []struct {
			st     storage.DoltStorage
			routed bool
		}{{local, false}, {st1, true}} {
			res, lookupErr := resolveAndGetFromStore(ctx, candidate.st, args[1], candidate.routed)
			if lookupErr == nil && res != nil && res.Issue != nil {
				ends[1] = relateEndpoint{id: res.ResolvedID, store: candidate.st}
				break
			}
			if lookupErr != nil && !isNotFoundErr(lookupErr) {
				cleanup()
				return ends, func() {}, fmt.Errorf("failed to resolve %s: %w", args[1], lookupErr)
			}
		}
	}
	if ends[1].store == nil {
		id2, st2, close2, err := resolveIDForMutation(ctx, local, args[1])
		if err != nil {
			cleanup()
			return ends, func() {}, fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
		closers = append(closers, close2)
		ends[1] = relateEndpoint{id: id2, store: st2}
	}

	if ends[0].store != ends[1].store && dependencyStoreKey(ends[0].store) == dependencyStoreKey(ends[1].store) {
		ends[1].store = ends[0].store
	}
	return ends, cleanup, nil
}

// writeRelateHalves adds (or, with remove, removes) the two relates-to halves
// ends[0] -> ends[1] and ends[1] -> ends[0], each through its source's store,
// recording a dependency_added / dependency_removed event per half.
//
// When both endpoints live in one store the two halves are written the way
// bd relate always wrote them, and a store other than the local one is
// committed after both, since the post-run auto-commit covers only the local
// store.
//
// When they live in different stores there is no transaction spanning both.
// The halves are written in argument order through each source's
// DependencyEditor and each is committed before the next is attempted, so a
// failure leaves at most the earlier half durable. The error names the half
// that failed and says whether the earlier one was committed; rerunning the
// command completes the link (adds and removals are idempotent). Nothing is
// rolled back.
func writeRelateHalves(ctx context.Context, ends [2]relateEndpoint, remove bool) error {
	command, verb := "relate", "add"
	if remove {
		command, verb = "unrelate", "remove"
	}
	halves := [2][2]string{{ends[0].id, ends[1].id}, {ends[1].id, ends[0].id}}

	if ends[0].store == ends[1].store {
		st := ends[0].store
		for _, h := range halves {
			var err error
			if remove {
				err = st.RemoveDependencyWithOptions(ctx, h[0], h[1], currentActor(), storage.DependencyRemoveOptions{EmitEvent: true})
			} else {
				// bd relate is an explicit dependency verb, so it records
				// history like bd link / bd dep add (EmitEvent); only
				// structural edge wiring stays silent.
				err = st.AddDependencyWithOptions(ctx, &types.Dependency{
					IssueID:     h[0],
					DependsOnID: h[1],
					Type:        types.DepRelatesTo,
				}, currentActor(), storage.DependencyAddOptions{EmitEvent: true})
			}
			if err != nil {
				return fmt.Errorf("failed to %s relates-to %s -> %s: %w", verb, h[0], h[1], err)
			}
		}
		if st == store {
			return nil
		}
		if err := commitPendingIfEmbedded(ctx, st, currentActor(), doltAutoCommitParams{
			Command:  command,
			IssueIDs: []string{ends[0].id, ends[1].id},
		}); err != nil {
			return fmt.Errorf("failed to commit relates-to %s <-> %s: %w", ends[0].id, ends[1].id, err)
		}
		return nil
	}

	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return err
	}
	for i, h := range halves {
		st := ends[0].store
		if i == 1 {
			st = ends[1].store
		}
		var writeErr error
		if remove {
			writeErr = removeRelateHalf(opsCtx, st, h[0], h[1])
		} else {
			writeErr = addDependencyEdgesDirect(opsCtx, st, []issueops.DependencyEdge{{
				IssueID:     h[0],
				DependsOnID: h[1],
				Type:        types.DepRelatesTo,
			}}, false)
		}
		if writeErr == nil {
			writeErr = commitPendingIfEmbedded(ctx, st, currentActor(), doltAutoCommitParams{
				Command:  command,
				IssueIDs: []string{h[0], h[1]},
			})
			if writeErr != nil {
				writeErr = fmt.Errorf("commit failed, so this half may not be durable: %w", writeErr)
			}
		}
		if writeErr == nil {
			continue
		}
		if i == 0 {
			return fmt.Errorf("failed to %s relates-to %s -> %s: %w (%s -> %s was not attempted)", verb, h[0], h[1], writeErr, h[1], h[0])
		}
		return fmt.Errorf("failed to %s relates-to %s -> %s: %w (%s -> %s is already committed in %s's store; rerun bd %s %s %s to finish)",
			verb, h[0], h[1], writeErr, h[1], h[0], h[1], command, ends[0].id, ends[1].id)
	}
	return nil
}

// removeRelateHalf removes the relates-to edge source -> target through the
// DependencyEditor of st, the store that owns source.
func removeRelateHalf(ctx context.Context, st storage.DoltStorage, source, target string) error {
	editor, err := st.DependencyEditor()
	if err != nil {
		return err
	}
	_, err = editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{
		Actor:       currentActor(),
		IssueID:     source,
		DependsOnID: target,
	})
	return err
}

// Note: contains, remove, formatRelatesTo functions removed per Decision 004
// relates-to links now use dependencies API instead of Issue.RelatesTo field
