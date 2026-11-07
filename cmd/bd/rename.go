package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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

	actor := getActorWithGit()
	if err := renameIssueKeepingBeadGates(ctx, store, oldIssue, newID, beadGatesByTarget(gates)[oldID], actor); err != nil {
		return HandleError("failed to rename issue: %v", err)
	}

	report, err := updateReferencesInAllIssues(ctx, store, oldID, newID, actor)

	fmt.Printf("Renamed %s -> %s\n", ui.RenderWarn(oldID), ui.RenderAccent(newID))
	report.write(os.Stdout, oldID, newID)
	if err != nil {
		fmt.Printf("Warning: failed to update some references: %v\n", err)
	}

	commandDidWrite.Store(true)

	return nil
}

// renameReferenceStore is the part of the store the reference scan of a
// rename reads and writes through.
type renameReferenceStore interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
}

// renameReport is what the reference scan of a rename did, for the user to
// review. Entries are in natural issue-ID order.
type renameReport struct {
	rewritten []renameFieldsUpdate // updates that rewrote oldID to newID
}

// renameFieldsUpdate names the fields of one issue that an update rewrote.
type renameFieldsUpdate struct {
	issueID string
	fields  []string // in renameRefFields order
}

// updateReferencesInAllIssues updates text references to the old ID in all
// issues, the renamed one included: it runs after the rename, so that issue is
// listed under newID with its text still naming oldID.
func updateReferencesInAllIssues(ctx context.Context, st renameReferenceStore, oldID, newID, actor string) (renameReport, error) {
	var report renameReport
	issues, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return report, fmt.Errorf("failed to list issues: %w", err)
	}

	for _, issue := range issues {
		updates := make(map[string]interface{})
		var fields []string
		for _, f := range renameRefFields {
			text := f.text(issue)
			if rewritten := replaceIDRefs(text, oldID, newID); rewritten != text {
				updates[f.name] = rewritten
				fields = append(fields, f.name)
			}
		}

		if len(updates) > 0 {
			if err := st.UpdateIssue(ctx, issue.ID, updates, actor); err != nil {
				return report, fmt.Errorf("failed to update references in %s: %w", issue.ID, err)
			}
			report.rewritten = append(report.rewritten, renameFieldsUpdate{issueID: issue.ID, fields: fields})
		}
	}

	slices.SortFunc(report.rewritten, func(a, b renameFieldsUpdate) int { return utils.NaturalCompareIDs(a.issueID, b.issueID) })
	return report, nil
}

// write prints the report of a rename of oldID to newID. It prints nothing
// when there is nothing to report.
func (r renameReport) write(w io.Writer, oldID, newID string) {
	if len(r.rewritten) > 0 {
		fmt.Fprintf(w, "Rewrote %s to %s in:\n", oldID, newID)
		for _, u := range r.rewritten {
			fmt.Fprintf(w, "  %s: %s\n", u.issueID, strings.Join(u.fields, ", ")) //nolint:gosec // G705: terminal output, not a browser context
		}
	}
}

// renameRefFields are the issue text fields bd rename rewrites, in the order
// its report lists them. A close_reason update carries no status, so
// UpdateIssue leaves a closed issue closed at its closed_at.
var renameRefFields = []struct {
	name string
	text func(*types.Issue) string
}{
	{"title", func(i *types.Issue) string { return i.Title }},
	{"description", func(i *types.Issue) string { return i.Description }},
	{"design", func(i *types.Issue) string { return i.Design }},
	{"notes", func(i *types.Issue) string { return i.Notes }},
	{"acceptance_criteria", func(i *types.Issue) string { return i.AcceptanceCriteria }},
	{"close_reason", func(i *types.Issue) string { return i.CloseReason }},
}

// replaceIDRefs replaces each mention of oldID in text with newID (see
// forEachIDRef). The replacement is never scanned again, so a newID that
// contains oldID is not rewritten twice.
func replaceIDRefs(text, oldID, newID string) string {
	var b strings.Builder
	last := 0
	forEachIDRef(text, oldID, func(start, end int) {
		b.WriteString(text[last:start])
		b.WriteString(newID)
		last = end
	})
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// forEachIDRef calls fn with the byte range of each mention of id in text. A
// mention is a whole token of ID characters (isIDTokenChar) that is id itself,
// or id followed only by periods, read as sentence punctuation and left out of
// the range. Matching is case-sensitive. A longer token names another issue
// and is no mention: renaming test-a leaves test-a.1, test-a-v2, pre-test-a
// and test-a_x alone.
func forEachIDRef(text, id string, fn func(start, end int)) {
	if id == "" {
		return
	}
	for i := 0; i < len(text); {
		if !isIDTokenChar(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && isIDTokenChar(text[i]) {
			i++
		}
		token := text[start:i]
		if strings.HasPrefix(token, id) && strings.Trim(token[len(id):], ".") == "" {
			fn(start, start+len(id))
		}
	}
}

// isIDTokenChar reports whether c can be part of an issue ID: an ASCII letter
// or digit, or one of the separators bd rename admits in a new ID.
func isIDTokenChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-'
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
