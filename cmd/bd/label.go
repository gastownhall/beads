// Package main implements the bd CLI label management commands.
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

var labelCmd = &cobra.Command{
	Use:     "label",
	GroupID: "issues",
	Short:   "Manage issue labels",
}

// openIssueLifecycle hands back the guarded write role for whichever route this
// invocation is on, each through its own capability accessor. Neither branch
// opens a unit of work, names a use case or composes a transaction: the label
// edit is a patch, and the role is what applies it.
func openIssueLifecycle() (issueops.Lifecycle, error) {
	if usesProxiedServer() {
		return proxiedIssueLifecycle()
	}
	return store.IssueLifecycle()
}

// openIssueReader hands back the read role the same way. `bd label list` is a
// detail read whose answer is already hydrated — issueops.Reader.Get returns
// IssueDetails with Labels on it — so there is nothing here for a label-shaped
// read surface to add.
func openIssueReader() (issueops.Reader, error) {
	if usesProxiedServer() {
		return proxiedIssueReader()
	}
	return store.IssueReader()
}

// resolveLabelTarget turns one positional argument into the EXACT id the roles
// take, on whichever route this invocation is on.
//
// This is the ONE fork left in this command, and it is a fork over how a route
// REACHES the store rather than over what it does once it has an answer. The
// proxied arm is not a second policy: it is the same exact-then-wisp lookup
// internal/workapi already defines for every proxied front door, and its
// not-found is normalized to the same message shape.
func resolveLabelTarget(ctx context.Context, id string) (string, error) {
	if usesProxiedServer() {
		return resolveLabelTargetProxied(ctx, id)
	}
	return utils.ResolvePartialID(ctx, store, id)
}

// applyLabelEdit applies ONE label patch to each named issue through
// issueops.Lifecycle, and is the whole of what `bd label add` and
// `bd label remove` do differently.
//
// ONE CALL PER ISSUE, AND ONE PATCH PER CALL. Every label of the request goes
// into a single LabelPatch, so an N-issue, M-label edit is N calls rather than
// the N*M raw writes both routes used to make. The role is what makes that
// safe to say: LabelPatch applies Replace, then Add, then Remove, de-duplicates
// within an edit, drops an empty entry rather than storing it, and refuses an
// over-length label with ErrFieldTooLong before anything is written.
//
// The wisp plane needs no branch here. UpdateRequest.IssuePlaneOnly stays
// false, so the role resolves the plane INSIDE its own transaction and an
// ephemeral row's labels land in the ephemeral table — which is the four-way
// switch the proxied route used to hand-roll, and the one place a front door
// could put a wisp's label in the durable table by getting a boolean backwards.
//
// WHAT THE LOOP COSTS, stated in full because it is the one place this
// migration is not free. Both routes used to put the whole edit in ONE
// transaction — the direct one through transactHonoringAutoCommit, the proxied
// one through uow.RunTx — and a per-issue role call cannot. So for N issues:
//
//   - N history entries where there was one, each naming its own issue;
//   - under `--dolt-auto-commit on`, which is the DEFAULT, N Dolt version
//     commits where the old transaction made one. Batch mode still makes zero,
//     via issueOpsContext above, so the multiplication is only on the mode
//     that was already committing per command;
//   - a failure on the third id leaves the first two written, where the old
//     shape rolled the good ones back with the bad one.
//
// PARTIAL APPLICATION IS ACCEPTABLE HERE, and that is an argument rather than
// a shrug. issueops.ApplyLabelPatch computes the TARGET SET and returns
// Changed false without writing when it equals the existing one, so an add and
// a remove are both idempotent: re-running the same command over the same ids
// is a no-op on the ids that already landed and does the remaining work on the
// ids that did not. A partial batch therefore CONVERGES on retry, which is the
// property that makes "landed some" a recoverable state rather than a
// corrupted one. It would not be acceptable for an edit whose replay is not
// the identity — which is exactly why this is stated here and not generalized.
//
// THE ATOMIC SHAPE IS EXPRESSIBLE ON THE FACADE ALREADY and is the named
// follow-up, not a vague later: issueops.BatchApplier.ApplyBatch takes one
// ItemUpdate per issue carrying this same IssuePatch, applies them in order and
// commits them together, so the N calls collapse back to one transaction and
// one history entry with no new role and no new request type. It is not in this
// slice because it needs a cmd/bd accessor of its own and because its end gate
// runs a hierarchy and cycle walk a label-only request has no use for.
func applyLabelEdit(ctx context.Context, issueIDs []string, labels []string, operation string) error {
	lifecycle, err := openIssueLifecycle()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	// The role creates its Dolt version commit inside the storage layer, so
	// `--dolt-auto-commit batch` cannot be honored by blanking a commit message
	// the way transactHonoringAutoCommit did on the old direct route: it has to
	// be said on the CONTEXT. This is the one thing a command loses by moving
	// off the raw transaction, and issueOpsContext is where every other
	// role-routed write says it.
	ctx, err = issueOpsContext(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	patch := issueops.IssuePatch{}
	if operation == labelOperationRemoved {
		patch.Labels.Remove = labels
	} else {
		patch.Labels.Add = labels
	}
	for _, issueID := range issueIDs {
		if _, uerr := lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor:   actor,
			IssueID: issueID,
			Patch:   patch,
		}); uerr != nil {
			return HandleErrorRespectJSON("label %s: %s label '%s' on %s: %v",
				operation, operation, strings.Join(labels, "', '"), issueID, uerr)
		}
		// Marked per issue rather than once after the loop: the edits land one
		// call at a time, so a request that failed on its third id has still
		// written its first two and the deferred commit has to know about them.
		commandDidWrite.Store(true)
	}
	return reportLabelEdit(issueIDs, labels, operation, jsonOutput)
}

// The two label edits this command performs, spelled once. They are the words
// the JSON "status" member and the human line both carry, so they are constants
// rather than two string literals that have to agree.
const (
	labelOperationAdded   = "added"
	labelOperationRemoved = "removed"
)

// reportLabelEdit prints what landed, in the shape both routes have always
// printed it: one JSON row per (issue, label) pair, or one human line per issue
// naming every label at once.
func reportLabelEdit(issueIDs []string, labels []string, operation string, jsonOut bool) error {
	if jsonOut {
		results := make([]map[string]interface{}, 0, len(issueIDs)*len(labels))
		for _, issueID := range issueIDs {
			for _, label := range labels {
				results = append(results, map[string]interface{}{
					"status":   operation,
					"issue_id": issueID,
					"label":    label,
				})
			}
		}
		return outputJSON(results)
	}
	verb, prep := "Added", "to"
	if operation == labelOperationRemoved {
		verb, prep = "Removed", "from"
	}
	noun := "label"
	if len(labels) > 1 {
		noun = "labels"
	}
	labelDesc := strings.Join(labels, "', '")
	for _, issueID := range issueIDs {
		fmt.Printf("%s %s %s '%s' %s %s\n", ui.RenderPass("✓"), verb, noun, labelDesc, prep, issueID)
	}
	return nil
}

// parseLabelArgs splits positional args into issue IDs and labels. The final
// arg is the label spec; commas separate multiple labels ("label1,label2").
func parseLabelArgs(args []string) (issueIDs []string, labels []string) {
	labels = splitLabelArg(args[len(args)-1])
	issueIDs = args[:len(args)-1]
	return
}

// splitLabelArg splits a comma-separated label argument into individual
// labels, trimming whitespace and dropping empty entries. Matches the
// comma-separated convention of bd create --labels.
func splitLabelArg(arg string) []string {
	parts := strings.Split(arg, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			labels = append(labels, part)
		}
	}
	return labels
}

// resolveLabelIssueIDs resolves every issue-ID positional arg to the EXACT id
// the role takes, failing hard on the first arg that doesn't resolve.
//
// Resolution stays a front-door job and does not move behind the role:
// GetRequest.ID and UpdateRequest.IssueID are both exact by contract, for the
// reason those docs give — an affordance that can resolve to a different issue
// than the caller named has no place on a contract two front doors share. This
// is therefore the ONE thing that still forks by route, and it forks on how a
// route reaches the store rather than on what it then does with the answer.
//
// Labels come last on the command line, so when several ID-position args fail
// the caller almost certainly passed labels space-separated ("bd label add
// bd-123 a b c"); the error hints at the comma-separated form instead of
// silently skipping the bad args (bd-vu5kv).
func resolveLabelIssueIDs(ctx context.Context, subcommand string, issueIDs []string) ([]string, error) {
	resolved := make([]string, 0, len(issueIDs))
	for _, id := range issueIDs {
		fullID, err := resolveLabelTarget(ctx, id)
		if err != nil {
			if len(issueIDs) > 1 {
				return nil, fmt.Errorf("resolving issue ID %q: %w (to %s multiple labels, pass one comma-separated argument: bd label %s <issue-id> label1,label2)",
					id, err, subcommand, subcommand)
			}
			return nil, fmt.Errorf("resolving issue ID %q: %w", id, err)
		}
		resolved = append(resolved, fullID)
	}
	return resolved, nil
}

//nolint:dupl // labelAddCmd and labelRemoveCmd are similar but serve different operations
var labelAddCmd = &cobra.Command{
	Use:           "add [issue-id...] [label[,label...]]",
	Short:         "Add one or more labels to one or more issues",
	Long:          "Add labels to issues. Issue IDs come first; the final argument is the label. Pass multiple labels comma-separated: bd label add bd-123 label1,label2",
	Args:          cobra.MinimumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label add")

		evt := metrics.NewCommandEvent("label-add")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return runLabelAdd(rootCtx, args)
	},
}

//nolint:dupl // labelRemoveCmd and labelAddCmd are similar but serve different operations
var labelRemoveCmd = &cobra.Command{
	Use:           "remove [issue-id...] [label[,label...]]",
	Short:         "Remove one or more labels from one or more issues",
	Long:          "Remove labels from issues. Issue IDs come first; the final argument is the label. Pass multiple labels comma-separated: bd label remove bd-123 label1,label2",
	Args:          cobra.MinimumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label remove")

		evt := metrics.NewCommandEvent("label-remove")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return runLabelRemove(rootCtx, args)
	},
}
var labelListCmd = &cobra.Command{
	Use:           "list [issue-id]",
	Short:         "List labels for an issue",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("label-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return runLabelList(rootCtx, args)
	},
}

// labelListAllSearcher is the whole storage surface `bd label list-all`
// needs. SearchIssues already hydrates Issue.Labels in bulk; omitting
// GetLabels here keeps the per-issue lookup — one extra connection and
// transaction per issue in embedded mode — unreachable (GH#5325).
type labelListAllSearcher interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}

func countLabelsAcrossIssues(ctx context.Context, s labelListAllSearcher) (map[string]int, error) {
	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, err
	}
	labelCounts := make(map[string]int)
	for _, issue := range issues {
		for _, label := range issue.Labels {
			labelCounts[label]++
		}
	}
	return labelCounts, nil
}

var labelListAllCmd = &cobra.Command{
	Use:           "list-all",
	Short:         "List all unique labels in the database",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("label-list-all")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelListAllProxiedServer(rootCtx)
		}

		labelCounts, err := countLabelsAcrossIssues(rootCtx, store)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		type labelInfo struct {
			Label string `json:"label"`
			Count int    `json:"count"`
		}
		if len(labelCounts) == 0 {
			if jsonOutput {
				return outputJSON([]labelInfo{})
			}
			fmt.Println("\nNo labels found in database")
			return nil
		}
		labels := make([]string, 0, len(labelCounts))
		for label := range labelCounts {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		if jsonOutput {
			result := make([]labelInfo, 0, len(labels))
			for _, label := range labels {
				result = append(result, labelInfo{
					Label: label,
					Count: labelCounts[label],
				})
			}
			return outputJSON(result)
		}
		fmt.Printf("\n%s All labels (%d unique):\n", ui.RenderAccent("🏷"), len(labels))
		maxLen := 0
		for _, label := range labels {
			if len(label) > maxLen {
				maxLen = len(label)
			}
		}
		for _, label := range labels {
			padding := strings.Repeat(" ", maxLen-len(label))
			fmt.Printf("  %s%s  (%d issues)\n", label, padding, labelCounts[label])
		}
		fmt.Println()
		return nil
	},
}

var labelPropagateCmd = &cobra.Command{
	Use:           "propagate [parent-id] [label]",
	Short:         "Propagate a label from a parent issue to all its children",
	Long:          "Push a label from a parent down to all direct children that don't already have it. Useful for applying branch: labels across an epic's subtasks.",
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label propagate")

		evt := metrics.NewCommandEvent("label-propagate")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelPropagateProxiedServer(rootCtx, args)
		}

		ctx := rootCtx

		parentID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("resolving parent %s: %v", args[0], err)
		}
		label := strings.TrimSpace(args[1])
		if label == "" {
			return HandleErrorRespectJSON("label cannot be empty")
		}

		if strings.HasPrefix(label, "provides:") {
			return HandleErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
		}

		children, err := store.SearchIssues(ctx, "", types.IssueFilter{ParentID: &parentID})
		if err != nil {
			return HandleErrorRespectJSON("searching children of %s: %v", parentID, err)
		}

		if len(children) == 0 {
			if jsonOutput {
				return outputJSON([]map[string]interface{}{})
			}
			fmt.Printf("No children found for %s\n", parentID)
			return nil
		}

		commitMsg := fmt.Sprintf("bd: propagate label '%s' from %s to %d children", label, parentID, len(children))
		err = transactHonoringAutoCommit(ctx, store, commitMsg, func(tx storage.Transaction) error {
			for _, child := range children {
				if err := tx.AddLabel(ctx, child.ID, label, actor); err != nil {
					return fmt.Errorf("add label '%s' on %s: %w", label, child.ID, err)
				}
			}
			return nil
		})
		if err != nil {
			return HandleErrorRespectJSON("label propagate: %v", err)
		}

		if jsonOutput {
			results := make([]map[string]interface{}, 0, len(children))
			for _, child := range children {
				results = append(results, map[string]interface{}{
					"status":   "propagated",
					"issue_id": child.ID,
					"label":    label,
				})
			}
			return outputJSON(results)
		}
		for _, child := range children {
			fmt.Printf("%s Propagated label '%s' to %s\n", ui.RenderPass("✓"), label, child.ID)
		}
		return nil
	},
}

func init() {
	// Issue ID completions
	labelAddCmd.ValidArgsFunction = issueIDCompletion
	labelRemoveCmd.ValidArgsFunction = issueIDCompletion
	labelListCmd.ValidArgsFunction = issueIDCompletion
	labelPropagateCmd.ValidArgsFunction = issueIDCompletion

	labelCmd.AddCommand(labelAddCmd)
	labelCmd.AddCommand(labelRemoveCmd)
	labelCmd.AddCommand(labelListCmd)
	labelCmd.AddCommand(labelListAllCmd)
	labelCmd.AddCommand(labelPropagateCmd)
	rootCmd.AddCommand(labelCmd)
}

// runLabelAdd, runLabelRemove and runLabelList are ONE body each, for both
// routes. The route fork is gone from these three commands: what used to be an
// `if usesProxiedServer() { return run…ProxiedServer(...) }` in the middle of a
// RunE — with a second, separately-maintained implementation on the other side
// of it — is now a fork inside resolveLabelTarget and inside the accessor, and
// both arms end at the same role.
func runLabelAdd(ctx context.Context, args []string) error {
	issueIDs, labels := parseLabelArgs(args)
	if len(labels) == 0 {
		return HandleErrorRespectJSON("label cannot be empty")
	}
	// The reserved-prefix refusal is checked BEFORE anything is resolved, and
	// that ORDER CHANGED: the direct route used to resolve every id first and
	// refuse the label afterwards. A caller that both typo'd an id and reached
	// for a reserved label is now told about the label — the one of the two
	// this command will never accept, at any id — rather than being sent to fix
	// an id that was never going to be labeled. The proxied route already
	// checked in this order, so this is the two routes agreeing on the earlier
	// of the two answers rather than a new rule.
	for _, label := range labels {
		if strings.HasPrefix(label, "provides:") {
			return HandleErrorRespectJSON("'provides:' labels are reserved for cross-project capabilities. Hint: use 'bd ship %s' instead", strings.TrimPrefix(label, "provides:"))
		}
	}
	issueIDs, err := resolveLabelIssueIDs(ctx, "add", issueIDs)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	return applyLabelEdit(ctx, issueIDs, labels, labelOperationAdded)
}

func runLabelRemove(ctx context.Context, args []string) error {
	issueIDs, labels := parseLabelArgs(args)
	if len(labels) == 0 {
		return HandleErrorRespectJSON("label cannot be empty")
	}
	issueIDs, err := resolveLabelIssueIDs(ctx, "remove", issueIDs)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	return applyLabelEdit(ctx, issueIDs, labels, labelOperationRemoved)
}

func runLabelList(ctx context.Context, args []string) error {
	issueID, err := resolveLabelTarget(ctx, args[0])
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
	}
	reader, err := openIssueReader()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	// The detail read already carries the labels, sorted, for whichever
	// plane holds the row — so there is no label-shaped read here, and no
	// wisp branch either.
	details, err := reader.Get(ctx, issueops.GetRequest{ID: issueID})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	labels := details.Labels
	if jsonOutput {
		if labels == nil {
			labels = []string{}
		}
		return outputJSON(labels)
	}
	if len(labels) == 0 {
		fmt.Printf("\n%s has no labels\n", issueID)
		return nil
	}
	fmt.Printf("\n%s Labels for %s:\n", ui.RenderAccent("🏷"), issueID)
	for _, label := range labels {
		fmt.Printf("  - %s\n", label)
	}
	fmt.Println()
	return nil
}
