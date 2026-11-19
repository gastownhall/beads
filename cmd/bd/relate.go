package main

import (
	"context"
	"encoding/json"
	"errors"
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
// recording a dependency_added / dependency_removed event per half. Both
// branches write on issueOpsContext, so dolt.auto-commit batch/off defers the
// Dolt commits wherever the two endpoints live.
func writeRelateHalves(ctx context.Context, ends [2]relateEndpoint, remove bool) error {
	if ends[0].store == ends[1].store {
		return writeSameStoreRelateHalves(ctx, ends, remove)
	}
	return writeCrossStoreRelateHalves(ctx, ends, remove)
}

// relateHalf is one directed relates-to edge, source -> target, and the store
// that owns its source, where the edge lives. existing is the type of the edge
// the source already holds to the target ("" for none), once readRelateHalf
// has read it.
type relateHalf struct {
	source, target string
	store          storage.DoltStorage
	existing       types.DependencyType
}

// relateHalves returns the halves ends[0] -> ends[1] and ends[1] -> ends[0],
// in that order.
func relateHalves(ends [2]relateEndpoint) [2]relateHalf {
	return [2]relateHalf{
		{source: ends[0].id, target: ends[1].id, store: ends[0].store},
		{source: ends[1].id, target: ends[0].id, store: ends[1].store},
	}
}

// relateVerbs names the command and the verb its messages use for one half.
func relateVerbs(remove bool) (command, verb string) {
	if remove {
		return "unrelate", "remove"
	}
	return "relate", "add"
}

// writeSameStoreRelateHalves writes both halves into the one store that holds
// both endpoints, the way bd relate always wrote them. A store other than the
// local one is committed after both, since the post-run auto-commit covers
// only the local store.
func writeSameStoreRelateHalves(ctx context.Context, ends [2]relateEndpoint, remove bool) error {
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return err
	}
	command, verb := relateVerbs(remove)
	st := ends[0].store
	for _, h := range relateHalves(ends) {
		if remove {
			err = st.RemoveDependencyWithOptions(opsCtx, h.source, h.target, currentActor(), storage.DependencyRemoveOptions{EmitEvent: true})
		} else {
			// bd relate is an explicit dependency verb, so it records
			// history like bd link / bd dep add (EmitEvent); only
			// structural edge wiring stays silent.
			err = st.AddDependencyWithOptions(opsCtx, &types.Dependency{
				IssueID:     h.source,
				DependsOnID: h.target,
				Type:        types.DepRelatesTo,
			}, currentActor(), storage.DependencyAddOptions{EmitEvent: true})
		}
		if err != nil {
			return fmt.Errorf("failed to %s relates-to %s -> %s: %w", verb, h.source, h.target, err)
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

// writeCrossStoreRelateHalves writes the halves when the endpoints live in
// different stores. No transaction spans the two, so before writing anything
// it reads the edge each half's source already holds to its target (a pair
// holds at most one edge in each direction) and acts on it:
//
//   - relate refuses, writing nothing, when either half already holds an edge
//     of another type. That half would be refused on every attempt, so
//     writing the other one would leave a one-sided link no rerun completes.
//   - unrelate removes only relates-to halves. An edge of another type, such
//     as a cross-rig blocks or tracks edge, stays in place and is named on
//     stderr.
//
// The halves are then written in argument order through each source's
// DependencyEditor, and each is committed before the next is attempted, so a
// failure leaves at most the earlier half durable. Nothing is rolled back;
// relateHalfError says how to converge.
func writeCrossStoreRelateHalves(ctx context.Context, ends [2]relateEndpoint, remove bool) error {
	first, err := readRelateHalf(ctx, ends[0], ends[1])
	if err != nil {
		return err
	}
	second, err := readRelateHalf(ctx, ends[1], ends[0])
	if err != nil {
		return err
	}
	halves := [2]relateHalf{first, second}
	if !remove {
		if err := refuseRelateOverOtherEdge(halves); err != nil {
			return err
		}
	}
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return err
	}
	command, _ := relateVerbs(remove)
	for i, h := range halves {
		if remove && h.existing != types.DepRelatesTo {
			warnRelateHalfKept(h)
			continue
		}
		if err := writeCrossStoreRelateHalf(opsCtx, h, remove); err != nil {
			return relateHalfError(ends, i, remove, err)
		}
		if err := commitPendingIfEmbedded(ctx, h.store, currentActor(), doltAutoCommitParams{
			Command:  command,
			IssueIDs: []string{h.source, h.target},
		}); err != nil {
			return relateHalfError(ends, i, remove, fmt.Errorf("commit failed, so this half may not be durable: %w", err))
		}
	}
	return nil
}

// readRelateHalf returns the half source -> target with the type of the edge
// source already holds to target in its own store, "" when it holds none.
// Like the editor's writes, the read follows a wisp source onto the wisp
// plane.
func readRelateHalf(ctx context.Context, source, target relateEndpoint) (relateHalf, error) {
	h := relateHalf{source: source.id, target: target.id, store: source.store}
	records, err := source.store.GetDependencyRecords(ctx, source.id)
	if err != nil {
		return h, fmt.Errorf("failed to read the edges of %s, so nothing was written: %w", source.id, err)
	}
	for _, r := range records {
		if r != nil && r.DependsOnID == target.id {
			h.existing = r.Type
			break
		}
	}
	return h, nil
}

// refuseRelateOverOtherEdge is bd relate's answer when a half already holds an
// edge of another type: the pair is already linked by that edge, and a
// relates-to half could never be written in its place.
func refuseRelateOverOtherEdge(halves [2]relateHalf) error {
	for _, h := range halves {
		if h.existing != "" && h.existing != types.DepRelatesTo {
			return fmt.Errorf("cannot relate %s and %s: %s -> %s already exists with type %q; nothing was written",
				halves[0].source, halves[0].target, h.source, h.target, h.existing)
		}
	}
	return nil
}

// warnRelateHalfKept names, on stderr, a half bd unrelate leaves in place
// because its edge is of another type. A half that holds no edge had nothing
// to remove and is not reported.
func warnRelateHalfKept(h relateHalf) {
	if h.existing == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s -> %s is a %q edge, not relates-to; left in place\n",
		ui.RenderWarn("⚠"), h.source, h.target, h.existing)
}

// writeCrossStoreRelateHalf adds or removes one half through the
// DependencyEditor of the store that owns its source.
func writeCrossStoreRelateHalf(ctx context.Context, h relateHalf, remove bool) error {
	if !remove {
		return addDependencyEdgesDirect(ctx, h.store, []issueops.DependencyEdge{{
			IssueID:     h.source,
			DependsOnID: h.target,
			Type:        types.DepRelatesTo,
		}}, false)
	}
	editor, err := h.store.DependencyEditor()
	if err != nil {
		return err
	}
	_, err = editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{
		Actor:       currentActor(),
		IssueID:     h.source,
		DependsOnID: h.target,
	})
	return err
}

// relateHalfError explains a failed cross-store half. When the second half
// fails the first is already committed, so the error also says how to
// converge: rerun the command, which repeats only what is missing, or, when
// the store refused the half with a type conflict a rerun would only meet
// again, undo the first half with bd dep unrelate.
func relateHalfError(ends [2]relateEndpoint, i int, remove bool, err error) error {
	command, verb := relateVerbs(remove)
	halves := relateHalves(ends)
	h, first := halves[i], halves[0]
	if i == 0 {
		return fmt.Errorf("failed to %s relates-to %s -> %s: %w (%s -> %s was not attempted)",
			verb, h.source, h.target, err, h.target, h.source)
	}
	state := fmt.Sprintf("%s -> %s is already committed in %s's store", first.source, first.target, first.source)
	next := fmt.Sprintf("rerun bd dep %s %s %s to finish", command, ends[0].id, ends[1].id)
	var conflict *issueops.DependencyTypeConflictError
	switch {
	case remove:
		state = fmt.Sprintf("%s holds no relates-to edge to %s any more", first.source, first.target)
	case errors.As(err, &conflict):
		next = fmt.Sprintf("bd dep unrelate %s %s removes it", ends[0].id, ends[1].id)
	}
	return fmt.Errorf("failed to %s relates-to %s -> %s: %w (%s; %s)", verb, h.source, h.target, err, state, next)
}

// Note: contains, remove, formatRelatesTo functions removed per Decision 004
// relates-to links now use dependencies API instead of Issue.RelatesTo field
