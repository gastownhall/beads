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
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// addDependencyEdgesDirect asserts edges through the DependencyEditor role on
// st — the store that owns the source issue.
//
// It is addDependencyEdgesProxied's twin, and reaches the role the same way:
// through the ACCESSOR, never a constructor, because the accessor is where each
// storage decorator adds its layer. Built per write site rather than once per
// command, for the reason writeOps gives — a routed source hands back an editor
// carrying its own stack.
//
// skipPerEdgeCycleCheck is a separate argument from the --no-cycle-check flag
// for the reason the proxied twin states: only the bulk path trades the
// per-edge probe away.
func addDependencyEdgesDirect(ctx context.Context, st storage.DoltStorage, edges []issueops.DependencyEdge, skipPerEdgeCycleCheck bool) error {
	editor, err := st.DependencyEditor()
	if err != nil {
		return err
	}
	_, err = editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
		Actor:                 actor,
		Edges:                 edges,
		SkipPerEdgeCycleCheck: skipPerEdgeCycleCheck,
	})
	return err
}

// exactDependencyTarget returns the depends_on_id of a raw dependency edge on
// issueID that equals rawTarget exactly. Used by `bd dep remove` so a bare
// slug that was stored verbatim (pre-GH#5005 create --deps bug) is removed
// instead of being resolved to a different, fully-qualified good edge.
func exactDependencyTarget(ctx context.Context, st storage.DependencyQueryStore, issueID, rawTarget string) (string, bool) {
	if st == nil || rawTarget == "" {
		return "", false
	}
	records, err := st.GetDependencyRecords(ctx, issueID)
	if err != nil {
		return "", false
	}
	for _, r := range records {
		if r != nil && r.DependsOnID == rawTarget {
			return rawTarget, true
		}
	}
	return "", false
}

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

			opsCtx, err := issueOpsContext(ctx)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			edge := issueops.DependencyEdge{IssueID: fromID, DependsOnID: toID, Type: types.DependencyType(depType)}
			if err := addDependencyEdgesDirect(opsCtx, fromStore, []issueops.DependencyEdge{edge}, false); err != nil {
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

With no -t/--type the edge is created as type=blocks, which excludes the
dependent from bd ready. When stderr is an interactive terminal, an advisory
note says so once per command; it is silent for scripted and agent callers
(non-TTY stderr) and can be turned off with --quiet or BD_NO_DEP_TYPE_WARNING=1.

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

		dt := canonicalDependencyType(types.DependencyType(depType))
		if isDisallowedHierarchicalDependency(fromID, toID, dt) {
			return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
		}

		if err := validateDependencyType(dt); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		edge := issueops.DependencyEdge{IssueID: fromID, DependsOnID: toID, Type: dt}
		if err := addDependencyEdgesDirect(opsCtx, fromStore, []issueops.DependencyEdge{edge}, false); err != nil {
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

		explicit := cmd.Flags().Changed("type") || cmd.Flags().Changed("blocked-by") || cmd.Flags().Changed("depends-on")
		warnImplicitBlocksDefault(dt, explicit)

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          string(dt),
			})
		}

		fmt.Printf("%s Added dependency: %s %s %s (%s)\n",
			ui.RenderPass("✓"), formatFeedbackIDParen(fromID, lookupTitle(fromID)), depRelationFor(dt).phrase, formatFeedbackIDParen(toID, lookupTitle(toID)), dt)
		return nil
	},
}

// warnImplicitBlocksDefault is the D1 guard: when a dep add edge is created
// with the implicit type=blocks default it warns on stderr. A silent blocks
// edge drops the dependent from bd ready, which is not what an operator
// usually means when wiring a structural parent/child link. An explicit
// choice never warns: -t (including an explicit -t blocks), and the
// --blocked-by/--depends-on aliases, whose names already express the
// blocking relationship. Non-blocks defaults do not warn either.
//
// The warning is advisory and fires on the documented-default majority path,
// so it is scoped to an interactive operator: it is emitted only when stderr
// is a TTY, and it honors the global --quiet flag and BD_NO_DEP_TYPE_WARNING.
// Scripted and agent callers — whose stderr is a pipe or a log file — never
// see it, so it cannot train them to ignore stderr.
func warnImplicitBlocksDefault(dt types.DependencyType, explicit bool) {
	if !shouldWarnImplicitBlocksDefault(dt, explicit, quietFlag, os.Getenv("BD_NO_DEP_TYPE_WARNING"), ui.IsStderrTerminal()) {
		return
	}
	emitImplicitBlocksDefaultWarning()
}

// shouldWarnImplicitBlocksDefault is the testable predicate behind
// warnImplicitBlocksDefault. It takes the quiet flag, the suppression env
// value and the stderr TTY result as parameters so tests can cover every
// combination without a real terminal — the same shape as
// ui.shouldUseHyperlinks.
func shouldWarnImplicitBlocksDefault(dt types.DependencyType, explicit, quiet bool, noWarnEnv string, stderrIsTerminal bool) bool {
	if explicit || dt != types.DepBlocks {
		return false
	}
	// --quiet is documented as "Suppress non-essential output (errors only)",
	// and the other non-error stderr notices in this package (tips.go,
	// metrics.go, routing_read.go) respect it the same way.
	if quiet {
		return false
	}
	// Explicit opt-out for operators who have internalized the default,
	// following the BD_NO_EMOJI / BD_NO_COLOR precedent.
	if noWarnEnv != "" {
		return false
	}
	return stderrIsTerminal
}

// emitImplicitBlocksDefaultWarning writes the D1 warning. Split from the gate
// so the message text can be asserted under a captured (non-TTY) stderr.
func emitImplicitBlocksDefaultWarning() {
	fmt.Fprintf(os.Stderr, "warning: no -t/--type given; edge created as type=blocks — the dependent is excluded from bd ready until the edge resolves. Use -t parent-child for structural parent/child linkage (silence with --quiet or BD_NO_DEP_TYPE_WARNING=1)\n") //nolint:gosec // G705: stderr, not a browser context
}

type bulkDepInput struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Type        string `json:"type"`
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
}

type bulkDepEdge struct {
	Line        int
	IssueID     string
	DependsOnID string
	Type        types.DependencyType
	// Defaulted is true when the line carried no "type" and fell back to
	// the command-line default (D1 guard: the implicit default is what the
	// stderr warning targets; explicit per-line types are the user's choice).
	Defaulted bool
	Store     storage.DoltStorage
	StoreKey  string
	Cleanups  []func()
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
	depEdges := make([]issueops.DependencyEdge, 0, len(resolved))
	for _, edge := range resolved {
		depEdges = append(depEdges, issueops.DependencyEdge{
			IssueID:     edge.IssueID,
			DependsOnID: edge.DependsOnID,
			Type:        edge.Type,
		})
	}

	// One request, one transaction, one history entry. The role's
	// all-or-nothing contract is what the hand-rolled bulk transaction used to
	// spell out here, down to the parent-child-first ordering and the
	// whole-graph gate that runs even when the per-edge probe is off — so the
	// request replaces it rather than wrapping it. The version commit comes
	// with the role, which is why there is no transact() and no
	// commitPendingIfEmbedded around this call.
	opsCtx, err := issueOpsContext(rootCtx)
	if err != nil {
		return err
	}
	if err := addDependencyEdgesDirect(opsCtx, targetStore, depEdges, noCycleCheck); err != nil {
		return err
	}

	if !noCycleCheck {
		warnIfCyclesExist(targetStore)
	}

	if !cmd.Flags().Changed("type") {
		for _, edge := range resolved {
			if edge.Defaulted && edge.Type == types.DepBlocks {
				warnImplicitBlocksDefault(edge.Type, false)
				break
			}
		}
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
		defaulted := depType == ""
		if defaulted {
			depType = defaultType
		}

		if from == "" {
			errs = append(errs, fmt.Sprintf("line %d: missing from", lineNo))
		}
		if to == "" {
			errs = append(errs, fmt.Sprintf("line %d: missing to", lineNo))
		}
		dt := canonicalDependencyType(types.DependencyType(depType))
		typeErr := validateDependencyType(dt)
		if typeErr != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", lineNo, typeErr))
		}
		if from == "" || to == "" || typeErr != nil {
			continue
		}

		edges = append(edges, bulkDepEdge{
			Line:        lineNo,
			IssueID:     from,
			DependsOnID: to,
			Type:        dt,
			Defaulted:   defaulted,
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

// depListAnchor is one resolved `bd dep list` argument: the canonical id, the
// store that actually holds it, and the routing handle that has to be closed.
type depListAnchor struct {
	fullID string
	store  storage.DoltStorage
	result *RoutedResult
}

// readDepListEdges asks each anchor's OWN store for its stored edges and
// reassembles the answers into the order the arguments named.
//
// The grouping is what keeps the answer on ONE shape: a failure is a failure, a
// split batch is N role calls merged back into one answer, and the caller's
// argument count picks the shape (see batchMode at the call site). `bd dep list
// a b c --json` documents an array of dependency records, not of issues.
func readDepListEdges(ctx context.Context, anchors []depListAnchor, typeFilter string) ([]issueops.AnchorEdges, error) {
	var depTypes []types.DependencyType
	if typeFilter != "" {
		depTypes = []types.DependencyType{types.DependencyType(typeFilter)}
	}

	// Grouped by store IDENTITY, not by the store's workspace path: two handles
	// onto the same database are still two connections, and asking one of them
	// for the other's ids would answer that every one of them is missing.
	byStore := map[storage.DoltStorage][]string{}
	var order []storage.DoltStorage
	for _, anchor := range anchors {
		if _, seen := byStore[anchor.store]; !seen {
			order = append(order, anchor.store)
		}
		byStore[anchor.store] = append(byStore[anchor.store], anchor.fullID)
	}

	answered := make(map[string]issueops.AnchorEdges, len(anchors))
	for _, st := range order {
		reader, err := st.EdgeReader()
		if err != nil {
			return nil, err
		}
		result, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: byStore[st], Types: depTypes})
		if err != nil {
			return nil, err
		}
		for _, anchor := range result.Anchors {
			answered[anchor.ID] = anchor
		}
	}

	out := make([]issueops.AnchorEdges, 0, len(anchors))
	seen := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		if _, dup := seen[anchor.fullID]; dup {
			continue
		}
		seen[anchor.fullID] = struct{}{}
		out = append(out, answered[anchor.fullID])
	}
	return out, nil
}

// printDepListEdges renders the role's per-anchor answer, and is shared by both
// routes so the two cannot drift apart in what they print.
//
// A GHOST ANCHOR goes to stderr in both modes. Keeping it off stdout is what
// leaves `--json` a flat array of dependency records, which is the shape the
// command documents.
func printDepListEdges(anchors []issueops.AnchorEdges) error {
	for _, anchor := range anchors {
		if anchor.Missing {
			fmt.Fprintf(os.Stderr, "warning: no issue found: %s (skipped)\n", anchor.ID)
		}
	}
	if jsonOutput {
		out := []*types.Dependency{}
		for _, anchor := range anchors {
			out = append(out, anchor.Edges...)
		}
		return outputJSON(out)
	}
	for _, anchor := range anchors {
		if anchor.Missing {
			continue
		}
		if len(anchor.Edges) == 0 {
			fmt.Printf("\n%s has no dependencies\n", anchor.ID)
			continue
		}
		fmt.Printf("\n%s Dependencies of %s:\n\n", ui.RenderAccent("📋"), anchor.ID)
		for _, dep := range anchor.Edges {
			fmt.Printf("  %s via %s\n", dep.DependsOnID, dep.Type)
		}
	}
	fmt.Println()
	return nil
}

// warnDroppedDepEdges prints a stderr-only notice for every stored "down"
// dependency edge of anchorID that shown (the Relations role's answer) left
// out because its target has no row in this database — i.e. a cross-repo or
// `external:` target (bd-mtla: `bd link` across databases reports success
// and writes the row, but the single-id `bd dep list <id>` a caller runs
// right after has no way to tell that from no dependency existing at all).
//
// It never writes to stdout, so the documented `bd dep list <id>` and
// `--json` shapes for the common (fully-local) case are unchanged; a script
// parsing stdout sees nothing new. A best-effort read: an error here is
// swallowed rather than surfaced, since the command's actual answer was
// already produced successfully by the Relations role above.
func warnDroppedDepEdges(ctx context.Context, reader issueops.EdgeReader, anchorID, typeFilter string, shown []*issueops.RelatedIssue) {
	var depTypes []types.DependencyType
	if typeFilter != "" {
		depTypes = []types.DependencyType{types.DependencyType(typeFilter)}
	}
	result, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: []string{anchorID}, Types: depTypes})
	if err != nil || len(result.Anchors) != 1 {
		return
	}
	known := make(map[string]struct{}, len(shown))
	for _, iss := range shown {
		known[iss.ID] = struct{}{}
	}
	var dropped []*types.Dependency
	for _, dep := range result.Anchors[0].Edges {
		if _, ok := known[dep.DependsOnID]; !ok {
			dropped = append(dropped, dep)
		}
	}
	if len(dropped) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %s has %d additional dependency edge(s) whose target has no row in this database (cross-repo/external) and are not shown above:\n", anchorID, len(dropped))
	for _, dep := range dropped {
		fmt.Fprintf(os.Stderr, "  %s via %s\n", dep.DependsOnID, dep.Type)
	}
	fmt.Fprintf(os.Stderr, "For raw edge records, run: bd dep list %s %s\n", anchorID, anchorID)
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

		var resolved []depListAnchor
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
			resolved = append(resolved, depListAnchor{
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

		// The multi-id edge listing is on the EdgeReader role, not Relations:
		// Relations is anchored on ONE issue, answers with the issues on the far
		// end of its edges rather than the edges themselves, and drops every
		// edge whose target this database has no row for.
		//
		// The accessor is taken PER STORE rather than once for the command: a
		// routed anchor answers from its own store, carrying its own decorator
		// stack.
		//
		// The shape is chosen on batchMode — the count the CALLER TYPED — and
		// not on len(resolved). Those differ exactly when an anchor did not
		// resolve, and the help text promises the records shape "with --json
		// ... across all requested issues", so a skipped anchor must not
		// silently change what a script is parsing.
		if batchMode && direction == "down" {
			anchors, err := readDepListEdges(ctx, resolved, typeFilter)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			return printDepListEdges(anchors)
		}

		// The neighbor query is on the Relations role: one call per anchor, each
		// with an explicit direction, because the role refuses to guess one. The
		// accessor is taken per resolved anchor rather than once for the command
		// — a routed anchor answers from its own store, carrying its own
		// decorator stack.
		request := issueops.RelatedRequest{Direction: issueops.RelationOut}
		if direction == "up" {
			request.Direction = issueops.RelationIn
		}
		if typeFilter != "" {
			request.Types = []types.DependencyType{types.DependencyType(typeFilter)}
		}

		var allIssues []*issueops.RelatedIssue
		for _, r := range resolved {
			rel, err := r.store.IssueRelations()
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			request.ID = r.fullID
			issues, err := rel.Related(ctx, request)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			allIssues = append(allIssues, issues...)
		}

		// Relations silently drops "down" edges whose target has no row in
		// this database (the doc comment above the batch branch says so).
		// EdgeReader doesn't have that gap, and batchMode&&"down" already
		// used it above, so this is reached for "down" only when there is
		// exactly one resolved anchor — warn on stderr (never stdout/--json,
		// so the documented single-id shape and any script parsing it are
		// untouched) naming any edge Relations left out, so a `bd link`
		// across databases doesn't look indistinguishable from no link at
		// all (bd-mtla). "up" has the same gap but no inbound EdgeReader
		// role exists to detect it from here — tracked separately.
		if direction == "down" && len(resolved) == 1 {
			if reader, err := resolved[0].store.EdgeReader(); err == nil {
				warnDroppedDepEdges(ctx, reader, resolved[0].fullID, typeFilter, allIssues)
			}
		}

		if jsonOutput {
			if allIssues == nil {
				allIssues = []*issueops.RelatedIssue{}
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
		} else if exact, ok := exactDependencyTarget(ctx, fromStore, fromID, args[1]); ok {
			// Prefer an exact depends_on_id match against raw edge records
			// before partial-ID resolution (GH#5005). Otherwise
			// `bd dep remove X 8vezf` resolves to the qualified good edge and
			// deletes it while leaving a dangling bare-id row behind.
			toID = exact
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

		// Explicit dep verb: the role records a dependency_removed history entry
		// for a genuine removal, matching bd dep add's edge event and the
		// proxied bd dep remove path.
		//
		editor, err := fromStore.DependencyEditor()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		result, err := editor.RemoveDependency(opsCtx, issueops.RemoveDependencyRequest{
			Actor:       actor,
			IssueID:     fullFromID,
			DependsOnID: fullToID,
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if err := commitPendingIfEmbedded(ctx, fromStore, actor, doltAutoCommitParams{
			Command:  "dep remove",
			IssueIDs: []string{fullFromID, fullToID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		if jsonOutput {
			status := "removed"
			if !result.Removed {
				status = "not_found"
			}
			return outputJSON(map[string]interface{}{
				"status":        status,
				"removed":       result.Removed,
				"issue_id":      fullFromID,
				"depends_on_id": fullToID,
			})
		}
		if !result.Removed {
			fmt.Printf("No dependency found: %s → %s\n",
				formatFeedbackIDParen(fullFromID, lookupTitle(fullFromID)), formatFeedbackIDParen(fullToID, lookupTitle(fullToID)))
			return nil
		}

		fmt.Printf("%s Removed dependency: %s → %s\n",
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

A node reached by two paths is shown ONCE, under the first path that got
there, and a cycle simply ends the descent. --show-all-paths is a deprecated
no-op; use 'bd dep cycles' to find circular dependencies.

--max-rows / BEADS_MAX_ROWS caveat: the tree walk has no query filter to
thread the cap through, so the full tree is always built first and the
node count is checked afterward (post-hoc), not during the walk. The cap is
honored on the --proxied-server route too, which it was not before.`,
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

		// Both routes, one body: which accessor answers and how the root id is
		// resolved are both inside resolveTreeTarget.
		return runDepTree(cmd, rootCtx, args)
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

		// Both routes, one body: the only difference between them is which
		// accessor answers, and that is inside openCycleDetector.
		return runDepCycles()
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

	depAddCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|tracks|related|parent-child|discovered-from|until|caused-by|validates|relates-to|supersedes); 'blocked-by' and 'depends-on' are accepted as aliases for 'blocks'")
	depAddCmd.Flags().String("blocked-by", "", "Issue ID that blocks the first issue (alternative to positional arg)")
	depAddCmd.Flags().String("depends-on", "", "Issue ID that the first issue depends on (alias for --blocked-by)")
	depAddCmd.Flags().String("file", "", "Read dependency edges from JSONL file, or '-' for stdin")
	depAddCmd.Flags().Bool("no-cycle-check", false, "Skip per-edge cycle checks for speed (bulk wiring); bulk --file adds still run one final whole-graph check before commit")

	// DEPRECATED NO-OP, and it always was one: nothing has ever read this flag,
	// so a diamond has always been rendered under one parent only. The role's
	// contract states the first-visit rule as a promise
	// (issueops/treewalker.go, TreeResult.Nodes) and this flag stays accepted so
	// no script breaks. Same story as TreeNode.Truncated.
	depTreeCmd.Flags().Bool("show-all-paths", false, "Deprecated no-op: accepted and ignored. A node reached by two paths is shown once, under the first.")
	depTreeCmd.Flags().IntP("max-depth", "d", 50, "Maximum tree depth to display (safety limit)")
	depTreeCmd.Flags().Bool("reverse", false, "Show dependent tree (deprecated: use --direction=up)")
	depTreeCmd.Flags().String("direction", "", "Tree direction: 'down' (dependencies), 'up' (dependents), or 'both'")
	depTreeCmd.Flags().String("status", "", "Filter to only show issues with this status (open, in_progress, blocked, deferred, closed)")
	depTreeCmd.Flags().String("format", "", "Output format: 'mermaid' for Mermaid.js flowchart")
	// Defensive row cap (be-x42v): applied to the node count after the walk, by
	// the role, on BOTH routes — hence the routed variant of the flag.
	addRoutedMaxRowsFlag(depTreeCmd)
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
