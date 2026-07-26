// Package main implements the bd CLI dependency management commands.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// resolveIDWithRouting resolves a partial issue ID using prefix-based routing.
// It returns the resolved full ID and the store that contains the issue.
// If the issue routes to a different database, a routed store is returned
// and must be closed by the caller via the returned cleanup function.
// If the issue is in the local store, cleanup is a no-op.
//
// The routed store is opened read-only; callers that mutate the returned store
// (e.g. dep add/remove/link writing through the source issue's store) must use
// resolveIDForMutation instead (GH#3231, #4141).
func resolveIDWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (resolvedID string, targetStore storage.DoltStorage, cleanup func(), err error) {
	result, err := resolveAndGetIssueWithRouting(ctx, localStore, id)
	if err != nil {
		return "", nil, func() {}, fmt.Errorf("resolving issue ID %s: %w", id, err)
	}
	if result == nil || result.Issue == nil {
		return "", nil, func() {}, fmt.Errorf("no issue found matching %q", id)
	}
	s := result.Store
	if s == nil {
		s = localStore
	}
	return result.ResolvedID, s, func() { result.Close() }, nil
}

// resolveIDForMutation mirrors resolveIDWithRouting but opens prefix-routed
// target stores writable (resolveAndGetIssueForMutation) so mutation commands
// can commit to the routed repository. Its result validation, local-store
// fallback, and cleanup tail must stay aligned with resolveIDWithRouting.
func resolveIDForMutation(ctx context.Context, localStore storage.DoltStorage, id string) (resolvedID string, targetStore storage.DoltStorage, cleanup func(), err error) {
	result, err := resolveAndGetIssueForMutation(ctx, localStore, id)
	if err != nil {
		return "", nil, func() {}, fmt.Errorf("resolving issue ID %s: %w", id, err)
	}
	if result == nil || result.Issue == nil {
		return "", nil, func() {}, fmt.Errorf("no issue found matching %q", id)
	}
	s := result.Store
	if s == nil {
		s = localStore
	}
	return result.ResolvedID, s, func() { result.Close() }, nil
}

// isChildOf returns true if childID is a hierarchical child of parentID.
// For example, "bd-abc.1" is a child of "bd-abc", and "bd-abc.1.2" is a child of "bd-abc.1".
func isChildOf(childID, parentID string) bool {
	_, isAncestor := hierarchicalParentRelation(childID, parentID)
	return isAncestor
}

func hierarchicalParentRelation(childID, targetID string) (immediateParent string, isAncestor bool) {
	// A child ID has the format "parentID.N" or "parentID.N.M" etc.
	// Use ParseHierarchicalID to get the actual parent
	_, actualParentID, depth := types.ParseHierarchicalID(childID)
	if depth == 0 {
		return "", false // Not a hierarchical ID
	}
	// Check if the immediate parent matches
	if actualParentID == targetID {
		return actualParentID, true
	}
	// Also check if targetID is an ancestor (e.g., "bd-abc" is an ancestor of "bd-abc.1.2")
	return actualParentID, strings.HasPrefix(childID, targetID+".")
}

// isDisallowedHierarchicalDependency reports whether an explicit dependency
// conflicts with hierarchy encoded in a dotted issue ID. The one allowed match
// is a parent-child edge to the immediate dotted-ID parent; blocking and other
// edge types to any parent/ancestor, plus parent-child edges to higher ancestors,
// remain rejected.
func isDisallowedHierarchicalDependency(fromID, toID string, depType types.DependencyType) bool {
	immediateParent, isAncestor := hierarchicalParentRelation(fromID, toID)
	if !isAncestor {
		return false
	}
	return depType != types.DepParentChild || toID != immediateParent
}

// warnIfCyclesExist checks for dependency cycles and prints a warning if found.
func warnIfCyclesExist(s storage.DoltStorage) {
	if s == nil {
		return // Skip cycle check if store is not available
	}
	cycles, err := s.DetectCycles(rootCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to check for cycles: %v\n", err)
		return
	}
	if len(cycles) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s Warning: Dependency cycle detected!\n", ui.RenderWarn("⚠"))
	fmt.Fprintf(os.Stderr, "This can hide issues from the ready work list and cause confusion.\n\n")
	fmt.Fprintf(os.Stderr, "Cycle path:\n")
	for _, cycle := range cycles {
		for j, issue := range cycle {
			if j == 0 {
				fmt.Fprintf(os.Stderr, "  %s", issue.ID)
			} else {
				fmt.Fprintf(os.Stderr, " → %s", issue.ID)
			}
		}
		if len(cycle) > 0 {
			fmt.Fprintf(os.Stderr, " → %s", cycle[0].ID)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}
	fmt.Fprintf(os.Stderr, "\nRun 'bd dep cycles' for detailed analysis.\n\n")
}

var depCmd = &cobra.Command{
	Use:     "dep [issue-id]",
	GroupID: "deps",
	Short:   "Manage dependencies",
	Long: `Manage dependencies between issues.

When called with an issue ID and --blocks flag, creates a blocking dependency:
  bd dep <blocker-id> --blocks <blocked-id>

This is equivalent to:
  bd dep add <blocked-id> <blocker-id>

Examples:
  bd dep bd-xyz --blocks bd-abc    # bd-xyz blocks bd-abc
  bd dep add bd-abc bd-xyz         # Same as above (bd-abc depends on bd-xyz)`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("dep")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		blocksID, _ := cmd.Flags().GetString("blocks")

		if len(args) == 0 && blocksID == "" {
			_ = cmd.Help()
			return nil
		}

		if blocksID != "" {
			if len(args) != 1 {
				return HandleErrorRespectJSON("--blocks requires exactly one issue ID argument")
			}
			blockerID := args[0]

			CheckReadonly("dep --blocks")

			ctx := rootCtx
			if usesProxiedServer() {
				return runDepBlocksProxiedServer(cmd, ctx, blockerID, blocksID)
			}
			depType := "blocks"

			// Resolve partial IDs with routing support. The source issue's store
			// is mutated below, so resolve it write-intent (#4141); the blocker
			// target is only resolved by ID and stays read-only, so a routed read
			// never opens a foreign project writable or runs open-time migrations
			// against its history (bd-6dnrw.32, GH#3231).
			fromID, fromStore, fromCleanup, err := resolveIDForMutation(ctx, store, blocksID)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			defer fromCleanup()

			toID, _, toCleanup, err := resolveIDWithRouting(ctx, store, blockerID)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			defer toCleanup()

			if isDisallowedHierarchicalDependency(fromID, toID, types.DepBlocks) {
				return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
			}

			dep := &types.Dependency{
				IssueID:     fromID,
				DependsOnID: toID,
				Type:        types.DependencyType(depType),
			}

			if err := fromStore.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true}); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}

			noCycleCheck, _ := cmd.Flags().GetBool("no-cycle-check")
			if !noCycleCheck {
				warnIfCyclesExist(fromStore)
			}

			if err := commitPendingIfEmbedded(ctx, fromStore, actor, doltAutoCommitParams{
				Command:  "dep add",
				IssueIDs: []string{fromID, toID},
			}); err != nil {
				return HandleErrorRespectJSON("failed to commit: %v", err)
			}

			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"status":     "added",
					"blocker_id": toID,
					"blocked_id": fromID,
					"type":       depType,
				})
			}

			fmt.Printf("%s Added dependency: %s blocks %s\n",
				ui.RenderPass("✓"), formatFeedbackIDParen(toID, lookupTitle(toID)), formatFeedbackIDParen(fromID, lookupTitle(fromID)))
			return nil
		}

		_ = cmd.Help()
		return nil
	},
}

var depAddCmd = &cobra.Command{
	Use:   "add [issue-id] [depends-on-id]",
	Short: "Add a dependency",
	Long: `Add a dependency between two issues.

The depends-on-id can be provided as:
  - A positional argument: bd dep add issue-123 issue-456
  - A flag: bd dep add issue-123 --blocked-by issue-456
  - A flag: bd dep add issue-123 --depends-on issue-456

The --blocked-by and --depends-on flags are aliases and both mean "issue-123
depends on (is blocked by) the specified issue."

The depends-on-id can be:
  - A local issue ID (e.g., bd-xyz)
  - An external reference: external:<project>:<capability>

For bulk wiring, pass newline-delimited JSON with --file. Each line must be an
object with "from" and "to" fields, and may include "type". The aliases
"issue_id" and "depends_on_id" are also accepted. Use --file - to read stdin.

External references are stored as-is and resolved at query time using
the external_projects config. They block the issue until the capability
is "shipped" in the target project.

Examples:
  bd dep add bd-42 bd-41                              # Positional args
  bd dep add bd-42 --blocked-by bd-41                 # Flag syntax (same effect)
  bd dep add bd-42 --depends-on bd-41                 # Alias (same effect)
  bd dep add gt-xyz external:beads:mol-run-assignee   # Cross-project dependency
  bd dep add bd-42 bd-41 --no-cycle-check             # Skip cycle check (bulk wiring)
  bd dep add --file deps.jsonl                        # Bulk JSONL: {"from":"bd-42","to":"bd-41"}`,
	Args: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		blockedBy, _ := cmd.Flags().GetString("blocked-by")
		dependsOn, _ := cmd.Flags().GetString("depends-on")
		hasFlag := blockedBy != "" || dependsOn != ""

		if file != "" {
			if len(args) != 0 {
				return fmt.Errorf("--file cannot be used with positional issue IDs")
			}
			if hasFlag {
				return fmt.Errorf("--file cannot be used with --blocked-by or --depends-on")
			}
			return nil
		}

		if hasFlag {
			// If a flag is provided, we only need 1 positional arg (the dependent issue)
			if len(args) < 1 {
				return fmt.Errorf("requires at least 1 arg(s), only received %d", len(args))
			}
			if len(args) > 1 {
				return fmt.Errorf("cannot use both positional depends-on-id and --blocked-by/--depends-on flag")
			}
			return nil
		}
		// No flag provided, need exactly 2 positional args
		if len(args) != 2 {
			return fmt.Errorf("requires 2 arg(s), only received %d (or use --blocked-by/--depends-on flag)", len(args))
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("dep add")

		evt := metrics.NewCommandEvent("dep-add")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runDepAddProxiedServer(cmd, rootCtx, args)
		}

		depType, _ := cmd.Flags().GetString("type")
		file, _ := cmd.Flags().GetString("file")

		if file != "" {
			if err := addBulkDependencies(cmd, file, depType); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			return nil
		}

		blockedBy, _ := cmd.Flags().GetString("blocked-by")
		dependsOn, _ := cmd.Flags().GetString("depends-on")

		var dependsOnArg string
		if blockedBy != "" {
			dependsOnArg = blockedBy
		} else if dependsOn != "" {
			dependsOnArg = dependsOn
		} else {
			dependsOnArg = args[1]
		}

		ctx := rootCtx

		var fromID, toID string

		isExternalRef := strings.HasPrefix(dependsOnArg, "external:")

		// Write-intent: the source issue's store is mutated by AddDependency
		// below, so the routed source must open writable (#4141). The depends-on
		// target is only resolved by ID and stays read-only, so resolving it can
		// never open a foreign project writable (bd-6dnrw.32, GH#3231).
		fromID, fromStore, fromCleanup, err := resolveIDForMutation(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer fromCleanup()

		if isExternalRef {
			toID = dependsOnArg
			if err := validateExternalRef(toID); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
		} else {
			var toCleanup func()
			toID, _, toCleanup, err = resolveIDWithRouting(ctx, store, dependsOnArg)
			if err != nil {
				srcPrefix := types.ExtractPrefix(fromID)
				tgtPrefix := types.ExtractPrefix(dependsOnArg)
				if srcPrefix != "" && tgtPrefix != "" && srcPrefix != tgtPrefix {
					toID = dependsOnArg
				} else {
					return HandleErrorRespectJSON("resolving dependency ID %s: %v", dependsOnArg, err)
				}
			} else {
				defer toCleanup()
			}
		}

		dt := types.DependencyType(depType)
		if isDisallowedHierarchicalDependency(fromID, toID, dt) {
			return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
		}

		if !dt.IsValid() {
			return HandleErrorRespectJSON("invalid dependency type %q: must be non-empty and at most 50 characters", depType)
		}

		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        dt,
		}

		if err := fromStore.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true}); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		noCycleCheck, _ := cmd.Flags().GetBool("no-cycle-check")
		if !noCycleCheck {
			warnIfCyclesExist(fromStore)
		}

		if err := commitPendingIfEmbedded(ctx, fromStore, actor, doltAutoCommitParams{
			Command:  "dep add",
			IssueIDs: []string{fromID, toID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          depType,
			})
		}

		fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
			ui.RenderPass("✓"), formatFeedbackIDParen(fromID, lookupTitle(fromID)), formatFeedbackIDParen(toID, lookupTitle(toID)), depType)
		return nil
	},
}

type bulkDepInput struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Type        string `json:"type"`
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
}

// newCycleThroughEdges runs a whole-graph cycle check inside the bulk-add
// transaction and returns a rendered scheduling-cycle path when a cycle actually
// traverses one of the edges being added, or "" when none does. Endpoint
// membership is not enough: an issue sitting in a pre-existing committed
// cycle must not block unrelated bulk wiring that merely touches it
// (bd-578h9.9). Only blocks, conditional-blocks, and parent-child edges
// participate. A failed check returns an error — the bulk add must roll back
// rather than commit unverified edges (bd-6dnrw.8).
func newCycleThroughEdges(ctx context.Context, tx storage.Transaction, edges []bulkDepEdge) (string, error) {
	pairs := make([][2]string, 0, len(edges))
	for _, edge := range edges {
		if edge.Type != types.DepBlocks && edge.Type != types.DepConditionalBlocks && edge.Type != types.DepParentChild {
			continue
		}
		pairs = append(pairs, [2]string{edge.IssueID, edge.DependsOnID})
	}
	if len(pairs) == 0 {
		return "", nil
	}
	return tx.CycleThroughEdges(ctx, pairs)
}

type bulkDepEdge struct {
	Line        int
	IssueID     string
	DependsOnID string
	Type        types.DependencyType
	Store       storage.DoltStorage
	StoreKey    string
	Cleanups    []func()
}

func addBulkDependencies(cmd *cobra.Command, file string, defaultType string) error {
	edges, err := readBulkDepEdges(file, defaultType)
	if err != nil {
		return err
	}

	resolved, err := validateBulkDepEdges(rootCtx, edges)
	if err != nil {
		return err
	}
	defer func() {
		for _, edge := range resolved {
			for _, cleanup := range edge.Cleanups {
				cleanup()
			}
		}
	}()

	if len(resolved) == 0 {
		return fmt.Errorf("no dependency edges found")
	}
	targetStore := resolved[0].Store
	targetStoreKey := resolved[0].StoreKey
	for _, edge := range resolved[1:] {
		if edge.StoreKey != targetStoreKey {
			return fmt.Errorf("bulk dep add requires all source issues to resolve to the same store")
		}
	}

	noCycleCheck, _ := cmd.Flags().GetBool("no-cycle-check")
	commitMsg := fmt.Sprintf("dependency: add %d edges", len(resolved))
	if err := transact(rootCtx, targetStore, commitMsg, func(tx storage.Transaction) error {
		return addBulkDependenciesInTx(rootCtx, tx, resolved, noCycleCheck, actor)
	}); err != nil {
		return err
	}

	if !noCycleCheck {
		warnIfCyclesExist(targetStore)
	}

	if jsonOutput {
		out := make([]map[string]interface{}, 0, len(resolved))
		for _, edge := range resolved {
			out = append(out, map[string]interface{}{
				"issue_id":      edge.IssueID,
				"depends_on_id": edge.DependsOnID,
				"type":          string(edge.Type),
			})
		}
		return outputJSON(map[string]interface{}{
			"status":       "added",
			"count":        len(resolved),
			"dependencies": out,
		})
	}

	fmt.Printf("%s Added %d dependencies\n", ui.RenderPass("✓"), len(resolved))
	return nil
}

func addBulkDependenciesInTx(ctx context.Context, tx storage.Transaction, edges []bulkDepEdge, noCycleCheck bool, actor string) error {
	// Make the complete planned hierarchy visible before validating any
	// blocking edge, independent of input-file order.
	for phase := 0; phase < 2; phase++ {
		parentPhase := phase == 0
		for _, edge := range edges {
			if (edge.Type == types.DepParentChild) != parentPhase {
				continue
			}
			dep := &types.Dependency{IssueID: edge.IssueID, DependsOnID: edge.DependsOnID, Type: edge.Type}
			if err := tx.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{SkipCycleCheck: noCycleCheck, EmitEvent: true}); err != nil {
				return fmt.Errorf("line %d: %w", edge.Line, err)
			}
		}
	}
	// Always merge both transaction snapshots before commit. Per-edge checks
	// cannot see uncommitted paths split across regular and wisp storage.
	cyclePath, cycleErr := newCycleThroughEdges(ctx, tx, edges)
	if cycleErr != nil {
		return fmt.Errorf("final cycle check failed (no edges added): %w", cycleErr)
	}
	if cyclePath != "" {
		// Type this rejection so callers can errors.Is(err, domain.ErrDependencyCycle),
		// matching the proxied/domain bulk final gate. NewCycleError renders the
		// message verbatim (no sentinel text appended), so the user-facing string
		// is unchanged.
		return domain.NewCycleError("dependency cycle would be created: %s (no edges added; run 'bd dep cycles' for analysis)", cyclePath)
	}
	return nil
}

func readBulkDepEdges(file string, defaultType string) ([]bulkDepEdge, error) {
	var r io.Reader
	var f *os.File
	if file == "-" {
		r = os.Stdin
	} else {
		var err error
		f, err = os.Open(file) // #nosec G304 -- user-supplied bulk dependency file
		if err != nil {
			return nil, fmt.Errorf("open dependency file: %w", err)
		}
		defer f.Close()
		r = f
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var edges []bulkDepEdge
	var errs []string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var in bulkDepInput
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			errs = append(errs, fmt.Sprintf("line %d: invalid JSON: %v", lineNo, err))
			continue
		}

		from := strings.TrimSpace(in.From)
		if from == "" {
			from = strings.TrimSpace(in.IssueID)
		}
		to := strings.TrimSpace(in.To)
		if to == "" {
			to = strings.TrimSpace(in.DependsOnID)
		}
		depType := strings.TrimSpace(in.Type)
		if depType == "" {
			depType = defaultType
		}

		if from == "" {
			errs = append(errs, fmt.Sprintf("line %d: missing from", lineNo))
		}
		if to == "" {
			errs = append(errs, fmt.Sprintf("line %d: missing to", lineNo))
		}
		dt := types.DependencyType(depType)
		if !dt.IsValid() {
			errs = append(errs, fmt.Sprintf("line %d: invalid dependency type %q: must be non-empty and at most 50 characters", lineNo, depType))
		}
		if from == "" || to == "" || !dt.IsValid() {
			continue
		}

		edges = append(edges, bulkDepEdge{
			Line:        lineNo,
			IssueID:     from,
			DependsOnID: to,
			Type:        dt,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dependency file: %w", err)
	}
	if len(errs) > 0 {
		return nil, bulkDepValidationError(errs)
	}
	return edges, nil
}

func validateBulkDepEdges(ctx context.Context, edges []bulkDepEdge) ([]bulkDepEdge, error) {
	resolved := make([]bulkDepEdge, 0, len(edges))
	var errs []string

	for _, edge := range edges {
		current := edge
		// Write-intent: addBulkDependencies writes through current.Store (the
		// source issue's store), so a routed source must open writable (#4141);
		// the depends-on target below stays read-only (bd-6dnrw.32, GH#3231).
		fromID, fromStore, fromCleanup, err := resolveIDForMutation(ctx, store, edge.IssueID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("line %d: resolving issue ID %s: %v", edge.Line, edge.IssueID, err))
			continue
		}
		current.Cleanups = append(current.Cleanups, fromCleanup)
		current.IssueID = fromID
		current.Store = fromStore
		current.StoreKey = dependencyStoreKey(fromStore)

		if strings.HasPrefix(edge.DependsOnID, "external:") {
			if err := validateExternalRef(edge.DependsOnID); err != nil {
				errs = append(errs, fmt.Sprintf("line %d: %v", edge.Line, err))
				resolved = append(resolved, current)
				continue
			}
			current.DependsOnID = edge.DependsOnID
		} else {
			toID, _, toCleanup, err := resolveIDWithRouting(ctx, store, edge.DependsOnID)
			if err != nil {
				srcPrefix := types.ExtractPrefix(current.IssueID)
				tgtPrefix := types.ExtractPrefix(edge.DependsOnID)
				if srcPrefix != "" && tgtPrefix != "" && srcPrefix != tgtPrefix {
					toID = edge.DependsOnID
				} else {
					errs = append(errs, fmt.Sprintf("line %d: resolving dependency ID %s: %v", edge.Line, edge.DependsOnID, err))
					resolved = append(resolved, current)
					continue
				}
			} else {
				current.Cleanups = append(current.Cleanups, toCleanup)
			}
			current.DependsOnID = toID
		}

		if isDisallowedHierarchicalDependency(current.IssueID, current.DependsOnID, current.Type) {
			errs = append(errs, fmt.Sprintf("line %d: cannot add dependency: %s is already a child of %s", edge.Line, current.IssueID, current.DependsOnID))
			resolved = append(resolved, current)
			continue
		}

		resolved = append(resolved, current)
	}

	if len(errs) > 0 {
		for _, edge := range resolved {
			for _, cleanup := range edge.Cleanups {
				cleanup()
			}
		}
		return nil, bulkDepValidationError(errs)
	}
	return resolved, nil
}

func bulkDepValidationError(errs []string) error {
	return fmt.Errorf("bulk dependency validation failed:\n  %s", strings.Join(errs, "\n  "))
}

func dependencyStoreKey(s storage.DoltStorage) string {
	if locator, ok := storage.UnwrapStore(s).(storage.StoreLocator); ok {
		if cliDir := strings.TrimSpace(locator.CLIDir()); cliDir != "" {
			return "cli:" + filepath.Clean(cliDir)
		}
		if path := strings.TrimSpace(locator.Path()); path != "" {
			return "path:" + filepath.Clean(path)
		}
	}
	return fmt.Sprintf("instance:%p", s)
}

var depListCmd = &cobra.Command{
	Use:   "list [issue-id...]",
	Short: "List dependencies or dependents of one or more issues",
	Long: `List dependencies or dependents of one or more issues with optional type filtering.

By default shows dependencies (what issues depend on). Use --direction to control:
  - down: Show dependencies (what this issue depends on) - default
  - up:   Show dependents (what depends on this issue)

Multiple IDs can be provided for batch dep listing. With --json, the output
is a flat array of dependency records across all requested issues.

Use --type to filter by dependency type (e.g., tracks, blocks, parent-child).

Examples:
  bd dep list gt-abc                     # Show what gt-abc depends on
  bd dep list gt-abc gt-def              # Batch: deps for both issues
  bd dep list gt-abc --direction=up      # Show what depends on gt-abc
  bd dep list gt-abc --direction=up -t tracks  # Show what tracks gt-abc (convoy tracking)`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("dep-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runDepListProxiedServer(cmd, rootCtx, args)
		}

		ctx := rootCtx
		direction, _ := cmd.Flags().GetString("direction")
		typeFilter, _ := cmd.Flags().GetString("type")
		if direction == "" {
			direction = "down"
		}

		type resolvedID struct {
			fullID string
			store  storage.DoltStorage
			result *RoutedResult
		}
		var resolved []resolvedID
		batchMode := len(args) > 1
		for _, arg := range args {
			routedResult, err := resolveAndGetIssueWithRouting(ctx, store, arg)
			if err != nil {
				if batchMode {
					fmt.Fprintf(os.Stderr, "warning: resolving %s: %v (skipped)\n", arg, err)
					continue
				}
				return HandleErrorRespectJSON("resolving %s: %v", arg, err)
			}
			if routedResult == nil || routedResult.Issue == nil {
				if batchMode {
					fmt.Fprintf(os.Stderr, "warning: no issue found: %s (skipped)\n", arg)
					continue
				}
				return HandleErrorRespectJSON("no issue found: %s", arg)
			}
			depStore := store
			if routedResult.Routed && routedResult.Store != nil {
				depStore = routedResult.Store
			}
			resolved = append(resolved, resolvedID{
				fullID: routedResult.ResolvedID,
				store:  depStore,
				result: routedResult,
			})
		}
		if batchMode && len(resolved) == 0 {
			if jsonOutput {
				return outputJSON([]*types.Dependency{})
			}
			fmt.Fprintln(os.Stderr, "no resolvable issues in batch")
			return nil
		}
		defer func() {
			for _, r := range resolved {
				if r.result != nil {
					r.result.Close()
				}
			}
		}()

		if len(resolved) > 1 && direction == "down" {
			allSameStore := true
			firstStore := resolved[0].store
			for _, r := range resolved[1:] {
				if r.store != firstStore {
					allSameStore = false
					break
				}
			}
			if allSameStore {
				ids := make([]string, len(resolved))
				for i, r := range resolved {
					ids[i] = r.fullID
				}
				depMap, err := firstStore.GetDependencyRecordsForIssues(ctx, ids)
				if err == nil {
					var allDeps []*types.Dependency
					for _, id := range ids {
						for _, dep := range depMap[id] {
							if typeFilter == "" || string(dep.Type) == typeFilter {
								allDeps = append(allDeps, dep)
							}
						}
					}
					if jsonOutput {
						if allDeps == nil {
							allDeps = []*types.Dependency{}
						}
						return outputJSON(allDeps)
					}
					for _, id := range ids {
						deps := depMap[id]
						if len(deps) == 0 {
							fmt.Printf("\n%s has no dependencies\n", id)
							continue
						}
						fmt.Printf("\n%s %s depends on:\n\n", ui.RenderAccent("📋"), id)
						for _, dep := range deps {
							if typeFilter != "" && string(dep.Type) != typeFilter {
								continue
							}
							fmt.Printf("  %s via %s\n", dep.DependsOnID, dep.Type)
						}
					}
					fmt.Println()
					return nil
				}
			}
		}

		var allIssues []*types.IssueWithDependencyMetadata
		for _, r := range resolved {
			var issues []*types.IssueWithDependencyMetadata
			var err error
			if direction == "up" {
				issues, err = r.store.GetDependentsWithMetadata(ctx, r.fullID)
			} else {
				issues, err = r.store.GetDependenciesWithMetadata(ctx, r.fullID)
			}
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			if typeFilter != "" {
				var filtered []*types.IssueWithDependencyMetadata
				for _, iss := range issues {
					if string(iss.DependencyType) == typeFilter {
						filtered = append(filtered, iss)
					}
				}
				issues = filtered
			}
			allIssues = append(allIssues, issues...)
		}

		if jsonOutput {
			if allIssues == nil {
				allIssues = []*types.IssueWithDependencyMetadata{}
			}
			return outputJSON(allIssues)
		}

		if len(allIssues) == 0 {
			if len(resolved) == 1 {
				if direction == "up" {
					fmt.Printf("\nNo issues depend on %s\n", resolved[0].fullID)
				} else {
					fmt.Printf("\n%s has no dependencies\n", resolved[0].fullID)
				}
			} else {
				fmt.Println("\nNo dependencies found")
			}
			return nil
		}

		for _, iss := range allIssues {
			var idStr string
			switch iss.Status {
			case types.StatusOpen:
				idStr = ui.StatusOpenStyle.Render(iss.ID)
			case types.StatusInProgress:
				idStr = ui.StatusInProgressStyle.Render(iss.ID)
			case types.StatusBlocked:
				idStr = ui.StatusBlockedStyle.Render(iss.ID)
			case types.StatusClosed:
				idStr = ui.StatusClosedStyle.Render(iss.ID)
			default:
				idStr = iss.ID
			}
			fmt.Printf("  %s: %s [P%d] (%s) via %s\n",
				idStr, iss.Title, iss.Priority, iss.Status, iss.DependencyType)
		}
		fmt.Println()
		return nil
	},
}

var depRemoveCmd = &cobra.Command{
	Use:           "remove [issue-id] [depends-on-id]",
	Aliases:       []string{"rm"},
	Short:         "Remove a dependency",
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("dep remove")

		evt := metrics.NewCommandEvent("dep-remove")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runDepRemoveProxiedServer(cmd, rootCtx, args)
		}

		ctx := rootCtx

		// Resolve partial IDs with routing support. The source issue's store is
		// mutated by RemoveDependency below, so resolve it write-intent (#4141);
		// the depends-on target is only resolved by ID and stays read-only
		// (bd-6dnrw.32, GH#3231).
		var fromID, toID string
		fromID, fromStore, fromCleanup, err := resolveIDForMutation(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer fromCleanup()

		isExternalRef := strings.HasPrefix(args[1], "external:")

		if isExternalRef {
			toID = args[1]
			if err := validateExternalRef(toID); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
		} else {
			var toCleanup func()
			toID, _, toCleanup, err = resolveIDWithRouting(ctx, store, args[1])
			if err != nil {
				srcPrefix := types.ExtractPrefix(fromID)
				tgtPrefix := types.ExtractPrefix(args[1])
				if srcPrefix != "" && tgtPrefix != "" && srcPrefix != tgtPrefix {
					toID = args[1]
				} else {
					return HandleErrorRespectJSON("resolving dependency ID %s: %v", args[1], err)
				}
			} else {
				defer toCleanup()
			}
		}

		fullFromID := fromID
		fullToID := toID

		// Explicit dep verb: record a dependency_removed history event (parity
		// with bd dep add's EmitEvent and the proxied bd dep remove path).
		if err := fromStore.RemoveDependencyWithOptions(ctx, fullFromID, fullToID, actor, storage.DependencyRemoveOptions{EmitEvent: true}); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if err := commitPendingIfEmbedded(ctx, fromStore, actor, doltAutoCommitParams{
			Command:  "dep remove",
			IssueIDs: []string{fullFromID, fullToID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":        "removed",
				"issue_id":      fullFromID,
				"depends_on_id": fullToID,
			})
		}

		fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
			ui.RenderPass("✓"), formatFeedbackIDParen(fullFromID, lookupTitle(fullFromID)), formatFeedbackIDParen(fullToID, lookupTitle(fullToID)))
		return nil
	},
}

var depTreeCmd = &cobra.Command{
	Use:   "tree [issue-id]",
	Short: "Show dependency tree",
	Long: `Show dependency tree rooted at the given issue.

By default, shows dependencies (what blocks this issue). Use --direction to control:
  - down: Show dependencies (what blocks this issue) - default
  - up:   Show dependents (what this issue blocks)
  - both: Show full graph in both directions

Examples:
  bd dep tree gt-0iqq                    # Show what blocks gt-0iqq
  bd dep tree gt-0iqq --direction=up     # Show what gt-0iqq blocks
  bd dep tree gt-0iqq --status=open      # Only show open issues
  bd dep tree gt-0iqq --depth=3          # Limit to 3 levels deep

--max-rows / BEADS_MAX_ROWS caveat: the tree walk has no query filter to
thread the cap through, so the full tree is always built first and the
node count is checked afterward (post-hoc), not during the walk.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("dep-tree")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			if err := rejectMaxRowsUnderProxiedServer(cmd); err != nil {
				return err
			}
			return runDepTreeProxiedServer(cmd, rootCtx, args)
		}

		ctx := rootCtx

		fullID, treeStore, treeCleanup, err := resolveIDWithRouting(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer treeCleanup()

		showAllPaths, _ := cmd.Flags().GetBool("show-all-paths")
		maxDepth, _ := cmd.Flags().GetInt("max-depth")
		reverse, _ := cmd.Flags().GetBool("reverse")
		direction, _ := cmd.Flags().GetString("direction")
		statusFilter, _ := cmd.Flags().GetString("status")
		formatStr, _ := cmd.Flags().GetString("format")
		if strings.EqualFold(formatStr, "json") {
			jsonOutput = true
			formatStr = ""
		}

		if direction == "" && reverse {
			direction = "up"
		} else if direction == "" {
			direction = "down"
		}

		if direction != "down" && direction != "up" && direction != "both" {
			return HandleErrorRespectJSON("--direction must be 'down', 'up', or 'both'")
		}

		if maxDepth < 1 {
			return HandleErrorRespectJSON("--max-depth must be >= 1")
		}

		var tree []*types.TreeNode

		if direction == "both" {
			downTree, err := treeStore.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, false)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}

			upTree, err := treeStore.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, true)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}

			tree = mergeBidirectionalTrees(downTree, upTree, fullID)
		} else {
			tree, err = treeStore.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, direction == "up")
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
		}

		if statusFilter != "" {
			tree = filterTreeByStatus(tree, types.Status(statusFilter))
		}

		// Apply defensive row cap (be-x42v) on the final tree-node count.
		// Tree walks have no IssueFilter to thread through, so the cap is
		// enforced at the CLI layer instead of in storage.
		treeMaxRows, treeMaxRowsSource, err := resolveMaxRows(cmd)
		if err != nil {
			return err
		}
		if treeMaxRows > 0 && len(tree) > treeMaxRows {
			if capErr := handleMaxRowsError(&issueops.ErrTooManyRows{
				Found:  len(tree),
				Cap:    treeMaxRows,
				Source: treeMaxRowsSource,
			}); capErr != nil {
				return capErr
			}
		}

		// Handle format presets (json handled earlier, near flag read)
		if formatStr == "mermaid" {
			outputMermaidTree(tree, args[0])
			return nil
		}

		if jsonOutput {
			if tree == nil {
				tree = []*types.TreeNode{}
			}
			return outputJSON(tree)
		}

		if len(tree) == 0 {
			switch direction {
			case "up":
				fmt.Printf("\n%s has no dependents\n", fullID)
			case "both":
				fmt.Printf("\n%s has no dependencies or dependents\n", fullID)
			default:
				fmt.Printf("\n%s has no dependencies\n", fullID)
			}
			return nil
		}

		switch direction {
		case "up":
			fmt.Printf("\n%s Dependent tree for %s:\n\n", ui.RenderAccent("🌲"), fullID)
		case "both":
			fmt.Printf("\n%s Full dependency graph for %s:\n\n", ui.RenderAccent("🌲"), fullID)
		default:
			fmt.Printf("\n%s Dependency tree for %s:\n\n", ui.RenderAccent("🌲"), fullID)
		}

		renderTree(tree, maxDepth, direction)
		fmt.Println()
		return nil
	},
}

var depCyclesCmd = &cobra.Command{
	Use:           "cycles",
	Short:         "Detect dependency cycles",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("dep-cycles")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runDepCyclesProxiedServer(cmd, rootCtx)
		}

		ctx := rootCtx
		cycles, err := store.DetectCycles(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if jsonOutput {
			if cycles == nil {
				cycles = [][]*types.Issue{}
			}
			return outputJSON(cycles)
		}

		if len(cycles) == 0 {
			fmt.Printf("\n%s No dependency cycles detected\n\n", ui.RenderPass("✓"))
			return nil
		}

		fmt.Printf("\n%s Found %d dependency cycles:\n\n", ui.RenderFail("⚠"), len(cycles))
		for i, cycle := range cycles {
			fmt.Printf("%d. Cycle involving:\n", i+1)
			for _, issue := range cycle {
				fmt.Printf("   - %s: %s\n", issue.ID, issue.Title)
			}
			fmt.Println()
		}
		return nil
	},
}

// outputMermaidTree outputs a dependency tree in Mermaid.js flowchart format
func outputMermaidTree(tree []*types.TreeNode, rootID string) {
	if len(tree) == 0 {
		fmt.Println("flowchart TD")
		fmt.Printf("  %s[\"No dependencies\"]\n", rootID)
		return
	}

	fmt.Println("flowchart TD")

	// Output nodes
	nodesSeen := make(map[string]bool)
	for _, node := range tree {
		if !nodesSeen[node.ID] {
			emoji := getStatusEmoji(node.Status)
			label := fmt.Sprintf("%s %s: %s", emoji, node.ID, node.Title)
			// Escape quotes and backslashes in label
			label = strings.ReplaceAll(label, "\\", "\\\\")
			label = strings.ReplaceAll(label, "\"", "\\\"")
			fmt.Printf("  %s[\"%s\"]\n", node.ID, label)

			nodesSeen[node.ID] = true
		}
	}

	fmt.Println()

	// Output edges - use explicit parent relationships from ParentID
	for _, node := range tree {
		if node.ParentID != "" && node.ParentID != node.ID {
			fmt.Printf("  %s --> %s\n", node.ParentID, node.ID)
		}
	}
}

// getStatusEmoji returns a symbol indicator for a given status
func getStatusEmoji(status types.Status) string {
	switch status {
	case types.StatusOpen:
		return "☐" // U+2610 Ballot Box
	case types.StatusInProgress:
		return "◧" // U+25E7 Square Left Half Black
	case types.StatusBlocked:
		return "⚠" // U+26A0 Warning Sign
	case types.StatusDeferred:
		return "❄" // U+2744 Snowflake (on ice)
	case types.StatusClosed:
		return "☑" // U+2611 Ballot Box with Check
	default:
		return "?"
	}
}

// treeRenderer holds state for rendering a tree with proper connectors
type treeRenderer struct {
	// Track which nodes we've already displayed (for "shown above" handling)
	seen map[string]bool
	// Track connector state at each depth level (true = has more siblings)
	activeConnectors []bool
	// Maximum depth reached
	maxDepth int
	// Direction of traversal
	direction string
	// Whether the root node has open children (i.e., is blocked)
	rootBlocked bool
}

// renderTree renders the tree with proper box-drawing connectors
func renderTree(tree []*types.TreeNode, maxDepth int, direction string) {
	if len(tree) == 0 {
		return
	}

	r := &treeRenderer{
		seen:             make(map[string]bool),
		activeConnectors: make([]bool, maxDepth+1),
		maxDepth:         maxDepth,
		direction:        direction,
	}

	// Build a map of parent -> children for proper sibling tracking
	children := make(map[string][]*types.TreeNode)
	var root *types.TreeNode

	for _, node := range tree {
		if node.Depth == 0 {
			root = node
		} else {
			children[node.ParentID] = append(children[node.ParentID], node)
		}
	}

	if root == nil && len(tree) > 0 {
		root = tree[0]
	}

	// Check if root has open blocking dependencies (GH#3565).
	// Only genuine blockers (blocks, conditional-blocks, waits-for) count;
	// parent-child, related, discovered-from, etc. do not block.
	if root != nil {
		hasOpenBlockers := false
		for _, child := range children[root.ID] {
			if (child.Status == types.StatusOpen || child.Status == types.StatusInProgress) &&
				child.EdgeFromParent.IsBlockingEdge() {
				hasOpenBlockers = true
				break
			}
		}
		r.rootBlocked = hasOpenBlockers
	}

	// Render recursively from root
	r.renderNode(root, children, 0, true)
}

// renderNode renders a single node and its children
func (r *treeRenderer) renderNode(node *types.TreeNode, children map[string][]*types.TreeNode, depth int, isLast bool) {
	if node == nil {
		return
	}

	// Build the prefix with connectors
	var prefix strings.Builder

	// Add vertical lines for active parent connectors
	for i := 0; i < depth; i++ {
		if r.activeConnectors[i] {
			prefix.WriteString("│   ")
		} else {
			prefix.WriteString("    ")
		}
	}

	// Add the branch connector for non-root nodes
	if depth > 0 {
		if isLast {
			prefix.WriteString("└── ")
		} else {
			prefix.WriteString("├── ")
		}
	}

	// Check if we've seen this node before (diamond dependency)
	if r.seen[node.ID] {
		fmt.Printf("%s%s (shown above)\n", prefix.String(), ui.RenderMuted(node.ID))
		return
	}
	r.seen[node.ID] = true

	// Format the node line
	line := formatTreeNode(node, depth == 0 && r.rootBlocked)

	// Add truncation warning if at max depth and has children
	if node.Truncated || (depth == r.maxDepth && len(children[node.ID]) > 0) {
		line += ui.RenderWarn(" …")
	}

	fmt.Printf("%s%s\n", prefix.String(), line)

	// Render children
	nodeChildren := children[node.ID]
	for i, child := range nodeChildren {
		// Update connector state for this depth
		// For depth 0 (root level), never show vertical connector since root has no siblings
		if depth > 0 {
			r.activeConnectors[depth] = (i < len(nodeChildren)-1)
		}
		r.renderNode(child, children, depth+1, i == len(nodeChildren)-1)
	}
}

// formatTreeNode formats a single tree node with status, ready indicator, etc.
// isBlocked indicates the node has open blocking dependencies and should not show [READY].
func formatTreeNode(node *types.TreeNode, isBlocked bool) string {
	// Handle external dependencies specially
	if IsExternalRef(node.ID) {
		// External deps use their title directly which includes the status indicator
		var idStr string
		switch node.Status {
		case types.StatusClosed:
			idStr = ui.StatusClosedStyle.Render(node.Title)
		case types.StatusBlocked:
			idStr = ui.StatusBlockedStyle.Render(node.Title)
		default:
			idStr = node.Title
		}
		return fmt.Sprintf("%s (external)", idStr)
	}

	// Color the ID based on status
	var idStr string
	switch node.Status {
	case types.StatusOpen:
		idStr = ui.StatusOpenStyle.Render(node.ID)
	case types.StatusInProgress:
		idStr = ui.StatusInProgressStyle.Render(node.ID)
	case types.StatusBlocked:
		idStr = ui.StatusBlockedStyle.Render(node.ID)
	case types.StatusClosed:
		idStr = ui.StatusClosedStyle.Render(node.ID)
	default:
		idStr = node.ID
	}

	// Build the line
	line := fmt.Sprintf("%s: %s [P%d] (%s)",
		idStr, node.Title, node.Priority, node.Status)

	// Show edge type for non-root nodes (GH#3565)
	if node.Depth > 0 && node.EdgeFromParent != "" {
		line += " " + ui.RenderMuted(fmt.Sprintf("[%s]", node.EdgeFromParent))
	}

	// Add READY/BLOCKED indicator for root node
	if node.Status == types.StatusOpen && node.Depth == 0 {
		if isBlocked {
			line += " " + ui.FailStyle.Bold(true).Render("[BLOCKED]")
		} else {
			line += " " + ui.PassStyle.Bold(true).Render("[READY]")
		}
	}

	return line
}

// filterTreeByStatus filters the tree to only include nodes with the given status
// Note: keeps parent chain to maintain tree structure
func filterTreeByStatus(tree []*types.TreeNode, status types.Status) []*types.TreeNode {
	if len(tree) == 0 {
		return tree
	}

	// First pass: identify which nodes match the status
	matches := make(map[string]bool)
	for _, node := range tree {
		if node.Status == status {
			matches[node.ID] = true
		}
	}

	// If no matches, return empty
	if len(matches) == 0 {
		return []*types.TreeNode{}
	}

	// Second pass: keep matching nodes and their ancestors
	// Build parent map
	parentOf := make(map[string]string)
	for _, node := range tree {
		if node.ParentID != "" && node.ParentID != node.ID {
			parentOf[node.ID] = node.ParentID
		}
	}

	// Mark all ancestors of matching nodes
	keep := make(map[string]bool)
	for id := range matches {
		keep[id] = true
		// Walk up to root
		current := id
		for {
			parent, ok := parentOf[current]
			if !ok || parent == current {
				break
			}
			keep[parent] = true
			current = parent
		}
	}

	// Filter the tree
	var filtered []*types.TreeNode
	for _, node := range tree {
		if keep[node.ID] {
			filtered = append(filtered, node)
		}
	}

	return filtered
}

// mergeBidirectionalTrees merges up and down trees into a single visualization
// The root appears once, with dependencies shown below and dependents shown above
func mergeBidirectionalTrees(downTree, upTree []*types.TreeNode, rootID string) []*types.TreeNode {
	// For bidirectional display, we show the down tree (dependencies) as the main tree
	// and add a visual separator with the up tree (dependents)
	//
	// For simplicity, we'll just return the down tree for now
	// A more sophisticated implementation would show both with visual separation

	// Find root in each tree
	var result []*types.TreeNode

	// Add dependents section if any (excluding root)
	hasUpNodes := false
	for _, node := range upTree {
		if node.ID != rootID {
			hasUpNodes = true
			break
		}
	}

	if hasUpNodes {
		// Add a header node for dependents section
		// We'll mark these with negative depth for visual distinction
		for _, node := range upTree {
			if node.ID == rootID {
				continue // Skip root, we'll add it once from down tree
			}
			// Clone node and mark it as "up" direction
			upNode := *node
			upNode.Depth = node.Depth // Keep original depth
			result = append(result, &upNode)
		}
	}

	// Add the down tree (dependencies)
	result = append(result, downTree...)

	return result
}

// validateExternalRef validates the format of an external dependency reference.
// Valid format: external:<project>:<capability>
func validateExternalRef(ref string) error {
	if !strings.HasPrefix(ref, "external:") {
		return fmt.Errorf("external reference must start with 'external:'")
	}

	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid external reference format: expected 'external:<project>:<capability>', got '%s'", ref)
	}

	project := parts[1]
	capability := parts[2]

	if project == "" {
		return fmt.Errorf("external reference missing project name")
	}
	if capability == "" {
		return fmt.Errorf("external reference missing capability name")
	}

	return nil
}

// IsExternalRef returns true if the dependency reference is an external reference.
func IsExternalRef(ref string) bool {
	return strings.HasPrefix(ref, "external:")
}

// ParseExternalRef parses an external reference into project and capability.
// Returns empty strings if the format is invalid.
func ParseExternalRef(ref string) (project, capability string) {
	if !IsExternalRef(ref) {
		return "", ""
	}
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

func init() {
	// dep command shorthand flag
	depCmd.Flags().StringP("blocks", "b", "", "Issue ID that this issue blocks (shorthand for: bd dep add <blocked> <blocker>)")
	depCmd.Flags().Bool("no-cycle-check", false, "Skip per-edge cycle checks for speed (bulk wiring); bulk --file adds still run one final whole-graph check before commit")

	depAddCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|tracks|related|parent-child|discovered-from|until|caused-by|validates|relates-to|supersedes)")
	depAddCmd.Flags().String("blocked-by", "", "Issue ID that blocks the first issue (alternative to positional arg)")
	depAddCmd.Flags().String("depends-on", "", "Issue ID that the first issue depends on (alias for --blocked-by)")
	depAddCmd.Flags().String("file", "", "Read dependency edges from JSONL file, or '-' for stdin")
	depAddCmd.Flags().Bool("no-cycle-check", false, "Skip per-edge cycle checks for speed (bulk wiring); bulk --file adds still run one final whole-graph check before commit")

	depTreeCmd.Flags().Bool("show-all-paths", false, "Show all paths to nodes (no deduplication for diamond dependencies)")
	depTreeCmd.Flags().IntP("max-depth", "d", 50, "Maximum tree depth to display (safety limit)")
	depTreeCmd.Flags().Bool("reverse", false, "Show dependent tree (deprecated: use --direction=up)")
	depTreeCmd.Flags().String("direction", "", "Tree direction: 'down' (dependencies), 'up' (dependents), or 'both'")
	depTreeCmd.Flags().String("status", "", "Filter to only show issues with this status (open, in_progress, blocked, deferred, closed)")
	depTreeCmd.Flags().String("format", "", "Output format: 'mermaid' for Mermaid.js flowchart")
	// Defensive row cap (be-x42v): applied to TreeNode count after the tree is built.
	addMaxRowsFlag(depTreeCmd)
	// Note: --type flag intentionally omitted from depTreeCmd — TreeNode lacks
	// dependency type info so filtering is not possible. Use 'bd dep list --type' instead.

	depListCmd.Flags().String("direction", "down", "Direction: 'down' (dependencies), 'up' (dependents)")
	depListCmd.Flags().StringP("type", "t", "", "Filter by dependency type (e.g., tracks, blocks, parent-child)")

	// Issue ID completions for dep subcommands
	depAddCmd.ValidArgsFunction = issueIDCompletion
	depRemoveCmd.ValidArgsFunction = issueIDCompletion
	depListCmd.ValidArgsFunction = issueIDCompletion
	depTreeCmd.ValidArgsFunction = issueIDCompletion

	depCmd.AddCommand(depAddCmd)
	depCmd.AddCommand(depRemoveCmd)
	depCmd.AddCommand(depListCmd)
	depCmd.AddCommand(depTreeCmd)
	depCmd.AddCommand(depCyclesCmd)
	rootCmd.AddCommand(depCmd)
}
