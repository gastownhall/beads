package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var renameCmd = &cobra.Command{
	Use:   "rename <old-id> <new-id>",
	Short: "Rename an issue ID",
	Long: `Rename an issue from one ID to another.

This updates:
- The issue's primary ID
- All references in other issues (descriptions, titles, notes, etc.)
- Dependencies pointing to/from this issue
- Labels, comments, and events

Examples:
  bd rename bd-w382l bd-dolt     # Rename to memorable ID
  bd rename gt-abc123 gt-auth    # Use descriptive ID

Note: The new ID must use a valid prefix for this database.`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runRename,
}

func init() {
	rootCmd.AddCommand(renameCmd)
}

func runRename(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("rename is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("rename")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	oldID := args[0]
	newID := args[1]

	if oldID == newID {
		return HandleError("old and new IDs are the same")
	}

	idPattern := regexp.MustCompile(`^[a-z]+-[a-zA-Z0-9._-]+$`)
	if !idPattern.MatchString(newID) {
		return HandleError("invalid new ID format %q: must be prefix-suffix (e.g., bd-dolt)", newID)
	}

	ctx := context.Background()
	if err := ensureStoreActive(); err != nil {
		return HandleError("failed to get storage: %v", err)
	}

	oldIssue, err := store.GetIssue(ctx, oldID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return HandleError("issue %s not found", oldID)
		}
		return HandleError("failed to get issue %s: %v", oldID, err)
	}

	_, err = store.GetIssue(ctx, newID)
	if err == nil {
		return HandleError("issue %s already exists", newID)
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return HandleError("failed to check for existing issue: %v", err)
	}

	gateType := types.TypeGate
	gates, err := store.SearchIssues(ctx, "", types.IssueFilter{IssueType: &gateType})
	if err != nil {
		return HandleError("failed to list gates: %v", err)
	}

	// The references are rewritten only after the rename has committed, one
	// issue at a time, so a title the store refuses there would stop the
	// rewrite part way with the old ID already gone. Refuse such a rename
	// before anything is written.
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return HandleError("failed to list issues: %v", err)
	}
	var titles []titleRewrite
	for _, ref := range referenceUpdates(issues, oldID, newID) {
		if title, ok := ref.updates["title"].(string); ok {
			titles = append(titles, titleRewrite{issueID: ref.issue.ID, before: ref.issue.Title, after: title})
		}
	}
	if err := checkTitleRewrites(titles); err != nil {
		return handleRenameError(fmt.Sprintf("cannot rename %s to %s", oldID, newID), err)
	}

	actor := getActorWithGit()
	if err := renameIssueKeepingBeadGates(ctx, store, oldIssue, newID, beadGatesByTarget(gates)[oldID], actor); err != nil {
		return HandleError("failed to rename issue: %v", err)
	}

	if err := updateReferencesInAllIssues(ctx, store, oldID, newID, actor); err != nil {
		fmt.Printf("Warning: failed to update some references: %v\n", err)
	}

	fmt.Printf("Renamed %s -> %s\n", ui.RenderWarn(oldID), ui.RenderAccent(newID))

	commandDidWrite.Store(true)

	return nil
}

// updateReferencesInAllIssues updates text references to the old ID in all issues
func updateReferencesInAllIssues(ctx context.Context, store storage.DoltStorage, oldID, newID, actor string) error {
	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to list issues: %w", err)
	}

	for _, ref := range referenceUpdates(issues, oldID, newID) {
		if err := store.UpdateIssue(ctx, ref.issue.ID, ref.updates, actor); err != nil {
			return fmt.Errorf("failed to update references in %s: %w", ref.issue.ID, err)
		}
	}

	return nil
}

// referenceUpdate is the update that points one issue's text references to a
// renamed issue at its new ID.
type referenceUpdate struct {
	issue   *types.Issue
	updates map[string]interface{}
}

// referenceUpdates lists the updates that point the text references to oldID
// in issues at newID. The renamed issue itself, listed under oldID before the
// rename and under newID after it, keeps its text.
func referenceUpdates(issues []*types.Issue, oldID, newID string) []referenceUpdate {
	// Pattern to match the old ID as a word boundary
	oldPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(oldID) + `\b`)

	var refs []referenceUpdate
	for _, issue := range issues {
		if issue.ID == oldID || issue.ID == newID {
			continue // Skip the renamed issue itself
		}

		updates := make(map[string]interface{})

		// Check and update each text field
		if oldPattern.MatchString(issue.Title) {
			updates["title"] = oldPattern.ReplaceAllString(issue.Title, newID)
		}
		if oldPattern.MatchString(issue.Description) {
			updates["description"] = oldPattern.ReplaceAllString(issue.Description, newID)
		}
		if oldPattern.MatchString(issue.Design) {
			updates["design"] = oldPattern.ReplaceAllString(issue.Design, newID)
		}
		if oldPattern.MatchString(issue.Notes) {
			updates["notes"] = oldPattern.ReplaceAllString(issue.Notes, newID)
		}
		if oldPattern.MatchString(issue.AcceptanceCriteria) {
			updates["acceptance_criteria"] = oldPattern.ReplaceAllString(issue.AcceptanceCriteria, newID)
		}

		if len(updates) > 0 {
			refs = append(refs, referenceUpdate{issue: issue, updates: updates})
		}
	}
	return refs
}

// titleRewrite is one issue's title before and after a rename rewrites the
// issue IDs in it.
type titleRewrite struct {
	issueID, before, after string
}

// titleRewriteError is a rename refused before its first write: rewriting the
// issue IDs in the titles it names would leave titles that fail
// types.ValidateIssueTitle, the rule create, import and the update gate apply.
type titleRewriteError struct {
	refused []string // "<issue ID>: <why the title is refused>"
}

func (e *titleRewriteError) Error() string {
	return "rewriting the issue IDs in these titles would make them invalid: " + strings.Join(e.refused, "; ")
}

// checkTitleRewrites returns a *titleRewriteError naming each rewrite whose
// new title fails types.ValidateIssueTitle, or nil when there is none. The
// renames commit issue by issue, so they run it over every title they would
// rewrite before their first write rather than stop part way. A title the
// rewrite leaves as it is passes, as the update gate passes an unchanged one.
func checkTitleRewrites(rewrites []titleRewrite) error {
	var refused []string
	for _, rw := range rewrites {
		if rw.after == rw.before {
			continue
		}
		if err := types.ValidateIssueTitle(rw.after); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", rw.issueID, err))
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return &titleRewriteError{refused: refused}
}

// handleRenameError reports err, which stopped a rename, after failed. A
// *titleRewriteError stopped it before anything was written, so the report
// says so and how to let the rename through.
func handleRenameError(failed string, err error) error {
	var titleErr *titleRewriteError
	if errors.As(err, &titleErr) {
		return HandleErrorWithHint(fmt.Sprintf("%s: %v", failed, err),
			"Nothing was renamed. Shorten the title of each issue named above with bd update <id> --title, then try again.")
	}
	return HandleError("%s: %v", failed, err)
}

// beadGateRenameStore is the part of the store a rename writes through.
type beadGateRenameStore interface {
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error
}

// beadGatesByTarget indexes the bead gates in issues by the ID of the bead
// each one waits on (see beadGateTargetID).
func beadGatesByTarget(issues []*types.Issue) map[string][]*types.Issue {
	gates := make(map[string][]*types.Issue)
	for _, issue := range issues {
		if issue.IssueType != types.TypeGate || issue.AwaitType != "bead" {
			continue
		}
		if targetID, pendingReason := beadGateTargetID(issue.AwaitID); pendingReason == "" {
			gates[targetID] = append(gates[targetID], issue)
		}
	}
	return gates
}

// beadGateMove is one bead gate following its bead to a new ID.
type beadGateMove struct {
	gateID    string
	fromID    string // await_id before the rename
	toID      string // await_id after it; a <rig>: prefix is kept
	seen      bool   // the gate records a sighting of fromID
	staleSeen bool   // the gate records a sighting of toID, left from an earlier await_id
}

// planBeadGateMoves lists the moves of gates, which wait on oldID, for its
// rename to newID.
func planBeadGateMoves(gates []*types.Issue, oldID, newID string) []beadGateMove {
	moves := make([]beadGateMove, 0, len(gates))
	for _, gate := range gates {
		toID := strings.TrimSuffix(gate.AwaitID, oldID) + newID
		moves = append(moves, beadGateMove{
			gateID:    gate.ID,
			fromID:    gate.AwaitID,
			toID:      toID,
			seen:      beadGateTargetSeen(gate),
			staleSeen: beadGateSeenID(gate) == toID,
		})
	}
	return moves
}

// renameIssueKeepingBeadGates renames issue to newID and points gates, the
// bead gates waiting on it, at the new ID. bd gate check resolves a gate whose
// awaited bead is missing once await_seen records a sighting of that same
// await_id, and UpdateIssueID commits on its own, so the writes are ordered
// to leave no state in between that resolves a gate:
//
//  1. Each gate's await_id moves to the new ID while its sighting still names
//     the old one, so until the bead has the new ID the gate reads as never
//     seen and stays pending. A sighting that already names the new ID would
//     match early, so it is dropped in the same write.
//  2. The issue is renamed. UpdateIssueID writes neither await_id nor
//     metadata, so the moves survive it, a gate's own rename included.
//  3. Each sighting is recorded again under the new ID. Losing one costs only
//     the sighting, which the next check records again while the bead exists,
//     so a failure here is a warning.
//
// If step 1 or 2 fails, the gates are moved back and the error is returned. A
// failed write may still have landed (a lost commit response), so a gate
// moved back loses its sighting too: it stays pending until a check sees the
// bead again. A gate that cannot be moved back is left on the new ID without a
// sighting of it, which also stays pending.
func renameIssueKeepingBeadGates(ctx context.Context, st beadGateRenameStore, issue *types.Issue, newID string, gates []*types.Issue, actorName string) error {
	oldID := issue.ID
	moves := planBeadGateMoves(gates, oldID, newID)
	for i, m := range moves {
		if err := st.UpdateIssue(ctx, m.gateID, beadGateRetargetUpdate(m.toID, m.staleSeen), actorName); err != nil {
			err = fmt.Errorf("failed to point gate %s at %s: %w", m.gateID, m.toID, err)
			return errors.Join(err, moveBeadGatesBack(ctx, st, moves[:i+1], actorName))
		}
	}

	issue.ID = newID
	if err := st.UpdateIssueID(ctx, oldID, newID, issue, actorName); err != nil {
		issue.ID = oldID
		return errors.Join(err, moveBeadGatesBack(ctx, st, moves, actorName))
	}

	for _, m := range moves {
		if !m.seen {
			continue
		}
		gateID := m.gateID
		if gateID == oldID {
			gateID = newID // the gate waits on itself
		}
		if err := st.UpdateIssue(ctx, gateID, beadGateSeenUpdate(m.toID), actorName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: gate %s: could not move its sighting of %s to %s: %v\n", gateID, m.fromID, m.toID, err)
		}
	}
	return nil
}

// moveBeadGatesBack points each moved gate at its old await_id again and
// drops its sighting (see renameIssueKeepingBeadGates).
func moveBeadGatesBack(ctx context.Context, st beadGateRenameStore, moves []beadGateMove, actorName string) error {
	var errs []error
	for _, m := range moves {
		if err := st.UpdateIssue(ctx, m.gateID, beadGateRetargetUpdate(m.fromID, true), actorName); err != nil {
			errs = append(errs, fmt.Errorf("failed to point gate %s back at %s: %w", m.gateID, m.fromID, err))
		}
	}
	return errors.Join(errs...)
}
