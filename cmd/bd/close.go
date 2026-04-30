package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

var closeCmd = &cobra.Command{
	Use:     "close [id...]",
	Aliases: []string{"done"},
	GroupID: "issues",
	Short:   "Close one or more issues",
	Long: `Close one or more issues.

If no issue ID is provided, closes the last touched issue (from most recent
create, update, show, or close operation).`,
	Args: cobra.MinimumNArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("close")

		// If no IDs provided, use last touched issue
		if len(args) == 0 {
			lastTouched := GetLastTouchedID()
			if lastTouched == "" {
				FatalErrorRespectJSON("no issue ID provided and no last touched issue")
			}
			args = []string{lastTouched}
		}
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			// Check --resolution alias (Jira CLI convention)
			reason, _ = cmd.Flags().GetString("resolution")
		}
		if reason == "" {
			// Check -m alias (git commit convention)
			reason, _ = cmd.Flags().GetString("message")
		}
		if reason == "" {
			// Check --comment alias (desire-path from hq-ftpg)
			reason, _ = cmd.Flags().GetString("comment")
		}

		// --reason-file <path> (with - for stdin) mirrors `bd create --body-file`,
		// so agents can pass structured close templates without shell-escaping hell (#3512).
		if fileReason, ok, err := resolveReasonFile(cmd, reason); err != nil {
			FatalErrorRespectJSON("%v", err)
		} else if ok {
			reason = fileReason
		}

		// Desire-path: "bd done <id> <message>" treats last positional arg as reason
		// when no reason flag was explicitly provided (hq-pe8ce)
		if reason == "" && cmd.CalledAs() == "done" && len(args) >= 2 {
			reason = args[len(args)-1]
			args = args[:len(args)-1]
		}

		if reason == "" {
			reason = "Closed"
		}

		// Validate close reason if configured
		closeValidation := config.GetString("validation.on-close")
		if closeValidation == "error" || closeValidation == "warn" {
			if err := validation.ValidateCloseReason(reason); err != nil {
				if closeValidation == "error" {
					FatalErrorRespectJSON("%v", err)
				}
				// warn mode: print warning but proceed
				fmt.Fprintf(os.Stderr, "%s %v\n", ui.RenderWarn("⚠"), err)
			}
		}

		force, _ := cmd.Flags().GetBool("force")
		continueFlag, _ := cmd.Flags().GetBool("continue")
		noAuto, _ := cmd.Flags().GetBool("no-auto")
		suggestNext, _ := cmd.Flags().GetBool("suggest-next")

		claimNext, _ := cmd.Flags().GetBool("claim-next")

		// Get session ID from flag or environment variable
		session, _ := cmd.Flags().GetString("session")
		if session == "" {
			session = os.Getenv("CLAUDE_SESSION_ID")
		}

		ctx := rootCtx

		// --continue only works with a single issue
		if continueFlag && len(args) > 1 {
			FatalErrorRespectJSON("--continue only works when closing a single issue")
		}

		// --suggest-next only works with a single issue
		if suggestNext && len(args) > 1 {
			FatalErrorRespectJSON("--suggest-next only works when closing a single issue")
		}

		// Resolve partial IDs with routing fallback (beads-0km).
		results, cleanup, resolveErr := resolveCloseTargets(ctx, store, args)
		defer cleanup()
		if resolveErr != nil {
			FatalErrorRespectJSON("%v", resolveErr)
		}
		resolvedIDs := make([]string, 0, len(results))
		for _, r := range results {
			resolvedIDs = append(resolvedIDs, r.ResolvedID)
		}

		// Track which routed stores we mutated so we can flush each in embedded mode.
		// Deduped by pointer; the local store is added unconditionally below.
		mutatedStores := map[storage.DoltStorage]struct{}{}

		// Direct mode
		closedIssues := []*types.Issue{}
		closedCount := 0

		for i, id := range resolvedIDs {
			result := results[i]
			activeStore := result.Store
			// Get issue for checks (nil issue is handled by validateIssueClosable)
			issue := result.Issue

			if err := validateIssueClosable(id, issue, force); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			// Epic close guard: prevent closing epics with open children (mw-local-4so.5.2)
			if !force && issue != nil && issue.IssueType == types.TypeEpic {
				openChildren := countEpicOpenChildren(ctx, activeStore, id)
				if openChildren > 0 {
					fmt.Fprintf(os.Stderr, "cannot close epic %s: %d open child issue(s); close children first or use --force to override\n", id, openChildren)
					continue
				}
			}

			// Check gate satisfaction for machine-checkable gates (GH#1467)
			if !force {
				if err := checkGateSatisfaction(issue); err != nil {
					fmt.Fprintf(os.Stderr, "cannot close %s: %s\n", id, err)
					continue
				}
			}

			// Check if issue has open blockers (GH#962)
			if !force {
				blocked, blockers, err := activeStore.IsBlocked(ctx, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking blockers for %s: %v\n", id, err)
					continue
				}
				if blocked && len(blockers) > 0 {
					fmt.Fprintf(os.Stderr, "cannot close %s: blocked by open issues %v (use --force to override)\n", id, blockers)
					continue
				}
			}

			if err := activeStore.CloseIssue(ctx, id, reason, actor, session); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}
			mutatedStores[activeStore] = struct{}{}

			// Audit log the close (survives Dolt GC flatten)
			oldStatus := "open"
			if issue != nil {
				oldStatus = string(issue.Status)
			}
			audit.LogFieldChange(id, "status", oldStatus, "closed", actor, reason)

			closedCount++

			// Auto-close parent molecule if all steps are now complete.
			// Runs against the same store the step was closed in.
			autoCloseCompletedMolecule(ctx, activeStore, id, actor, session)

			// Re-fetch for display
			closedIssue, _ := activeStore.GetIssue(ctx, id)

			if jsonOutput {
				if closedIssue != nil {
					closedIssues = append(closedIssues, closedIssue)
				}
			} else {
				fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), formatFeedbackID(id, issueTitleOrEmpty(issue)), reason)
			}
		}

		// Pick a store for post-close work (--suggest-next, --continue, --claim-next).
		// All three flags are documented as single-issue paths; for the multi-id case
		// we use the first resolved ID's store, which matches the common case where
		// every ID routes to the same place.
		postCloseStore := store
		if len(results) > 0 && results[0].Store != nil {
			postCloseStore = results[0].Store
		}

		// Handle --suggest-next flag in direct mode
		if suggestNext && len(resolvedIDs) == 1 && closedCount > 0 {
			unblocked, err := postCloseStore.GetNewlyUnblockedByClose(ctx, resolvedIDs[0])
			if err == nil && len(unblocked) > 0 {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"closed":    closedIssues,
						"unblocked": unblocked,
					})
					return
				}
				fmt.Printf("\nNewly unblocked:\n")
				for _, issue := range unblocked {
					fmt.Printf("  • %s (P%d)\n", formatFeedbackID(issue.ID, issue.Title), issue.Priority)
				}
			}
		}

		// Handle --continue flag
		if continueFlag && len(resolvedIDs) == 1 && closedCount > 0 {
			autoClaim := !noAuto
			result, err := AdvanceToNextStep(ctx, postCloseStore, resolvedIDs[0], autoClaim, actor)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not advance to next step: %v\n", err)
			} else if result != nil {
				if jsonOutput {
					// Include continue result in JSON output
					outputJSON(map[string]interface{}{
						"closed":   closedIssues,
						"continue": result,
					})
					return
				}
				PrintContinueResult(result)
			}
		}

		// Handle --claim-next flag
		var claimedNextIssue *types.Issue
		if claimNext && closedCount > 0 && !continueFlag {
			readyIssues, err := postCloseStore.GetReadyWork(ctx, types.WorkFilter{
				Status:     "open",
				Limit:      1,
				SortPolicy: types.SortPolicy("priority"),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not get ready issues: %v\n", err)
			} else if len(readyIssues) > 0 {
				nextIssue := readyIssues[0]
				err := postCloseStore.ClaimIssue(ctx, nextIssue.ID, actor)
				if err == nil {
					claimedNextIssue = nextIssue
					mutatedStores[postCloseStore] = struct{}{}
					if jsonOutput {
						// JSON handled below
					} else {
						fmt.Printf("%s Auto-claimed next ready issue: %s (P%d)\n", ui.RenderPass("✓"), formatFeedbackID(nextIssue.ID, nextIssue.Title), nextIssue.Priority)
					}
					SetLastTouchedID(nextIssue.ID)
				} else {
					fmt.Fprintf(os.Stderr, "Warning: could not claim next issue %s: %v\n", nextIssue.ID, err)
				}
			} else if !jsonOutput {
				fmt.Printf("\n%s No ready issues available to claim.\n", ui.RenderWarn("✨"))
			}
		}

		if jsonOutput && len(closedIssues) > 0 {
			if claimedNextIssue != nil {
				outputJSON(map[string]interface{}{
					"closed":  closedIssues,
					"claimed": claimedNextIssue,
				})
			} else {
				outputJSON(closedIssues)
			}
		}

		// Embedded mode: flush Dolt commit. DoltStore commits
		// inline during CloseIssue so this is only needed for EmbeddedDoltStore.
		// With contributor auto-routing, the close lands in the routed store, not
		// the local one — flush every store we actually mutated.
		if isEmbeddedMode() && closedCount > 0 {
			for s := range mutatedStores {
				if s == nil {
					continue
				}
				if _, err := s.CommitPending(ctx, actor); err != nil {
					FatalErrorRespectJSON("failed to commit: %v", err)
				}
			}
		}

		// Exit non-zero if no issues were actually closed (close guard
		// and other soft failures should surface as non-zero exit codes for scripting)
		totalAttempted := len(resolvedIDs)
		if totalAttempted > 0 && closedCount == 0 {
			os.Exit(1)
		}
	},
}

func init() {
	closeCmd.Flags().StringP("reason", "r", "", "Reason for closing")
	closeCmd.Flags().String("resolution", "", "Alias for --reason (Jira CLI convention)")
	_ = closeCmd.Flags().MarkHidden("resolution") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().StringP("message", "m", "", "Alias for --reason (git commit convention)")
	_ = closeCmd.Flags().MarkHidden("message") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().String("comment", "", "Alias for --reason")
	_ = closeCmd.Flags().MarkHidden("comment") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().String("reason-file", "", "Read close reason from file (use - for stdin)")
	closeCmd.Flags().BoolP("force", "f", false, "Force close pinned issues or unsatisfied gates")
	closeCmd.Flags().Bool("continue", false, "Auto-advance to next step in molecule")
	closeCmd.Flags().Bool("no-auto", false, "With --continue, show next step but don't claim it")
	closeCmd.Flags().Bool("suggest-next", false, "Show newly unblocked issues after closing")
	closeCmd.Flags().Bool("claim-next", false, "Automatically claim the next highest priority available issue")
	closeCmd.Flags().String("session", "", "Claude Code session ID (or set CLAUDE_SESSION_ID env var)")
	closeCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(closeCmd)
}

// isMachineCheckableGate returns true if the issue is a gate with a machine-checkable await type.
func isMachineCheckableGate(issue *types.Issue) bool {
	if issue == nil || issue.IssueType != "gate" {
		return false
	}
	switch {
	case strings.HasPrefix(issue.AwaitType, "gh:pr"):
		return true
	case strings.HasPrefix(issue.AwaitType, "gh:run"):
		return true
	case issue.AwaitType == "timer":
		return true
	case issue.AwaitType == "bead":
		return true
	default:
		return false
	}
}

// checkGateSatisfaction checks whether a gate issue's condition is satisfied.
// Returns nil if the gate is satisfied (or not a machine-checkable gate), or an error describing why it cannot be closed.
func checkGateSatisfaction(issue *types.Issue) error {
	if !isMachineCheckableGate(issue) {
		return nil
	}

	var resolved bool
	var escalated bool
	var reason string
	var err error

	switch {
	case strings.HasPrefix(issue.AwaitType, "gh:run"):
		resolved, escalated, reason, err = checkGHRun(issue, true)
	case strings.HasPrefix(issue.AwaitType, "gh:pr"):
		resolved, escalated, reason, err = checkGHPR(issue)
	case issue.AwaitType == "timer":
		resolved, escalated, reason, err = checkTimer(issue, time.Now())
	case issue.AwaitType == "bead":
		resolved, reason = checkBeadGate(rootCtx, issue.AwaitID)
		if resolved {
			return nil
		}
		return fmt.Errorf("gate condition not satisfied: %s (use --force to override)", reason)
	}

	if err != nil {
		// If we can't check the condition, allow close with a warning
		fmt.Fprintf(os.Stderr, "Warning: could not evaluate gate condition: %v\n", err)
		return nil
	}

	if resolved {
		return nil
	}

	if escalated {
		return fmt.Errorf("gate condition not satisfied: %s (use --force to override)", reason)
	}

	return fmt.Errorf("gate condition not satisfied: %s (use --force to override)", reason)
}

// autoCloseCompletedMolecule checks if closing a step completed an auto-closing
// parent molecule, and if so, closes the molecule root. Ordinary epics remain
// open when all children finish so they can become explicitly close-eligible
// instead of being closed as a side effect of the final child close.
func autoCloseCompletedMolecule(ctx context.Context, s storage.DoltStorage, closedStepID, actorName, session string) {
	moleculeID := findParentMolecule(ctx, s, closedStepID)
	if moleculeID == "" {
		return // Not part of a molecule
	}

	// Check if molecule root is already closed
	root, err := s.GetIssue(ctx, moleculeID)
	if err != nil || root == nil || root.Status == types.StatusClosed || !shouldAutoCloseCompletedRoot(root) {
		return
	}

	// Load progress to check completion
	progress, err := getMoleculeProgress(ctx, s, moleculeID)
	if err != nil {
		return // Best effort — don't fail the close
	}

	if progress.Completed < progress.Total {
		return // Not all steps complete yet
	}

	// All steps complete — auto-close the molecule root
	if err := s.CloseIssue(ctx, moleculeID, "all steps complete", actorName, session); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not auto-close completed molecule %s: %v\n", moleculeID, err)
		return
	}

	if !jsonOutput {
		fmt.Printf("%s Auto-closed completed molecule %s\n", ui.RenderPass("✓"), formatFeedbackID(moleculeID, root.Title))
	}
}

// shouldAutoCloseCompletedRoot returns true for molecule roots that should
// auto-close when their final step closes. Regular epics stay open and become
// explicit close-eligible work, while ephemeral wisps, template-driven
// molecules, and molecule-type coordination roots keep their cleanup behavior.
func shouldAutoCloseCompletedRoot(root *types.Issue) bool {
	if root == nil {
		return false
	}

	if root.IssueType == types.TypeMolecule || root.Ephemeral {
		return true
	}

	if root.IssueType != types.TypeEpic {
		return false
	}

	for _, label := range root.Labels {
		if label == BeadsTemplateLabel {
			return true
		}
	}

	return false
}

// resolveReasonFile resolves the --reason-file flag for `bd close`.
// Returns (content, true, nil) when --reason-file was set and read successfully.
// Returns (_, false, nil) when --reason-file was not set.
// Returns an error on conflict with an existing reason, file read failure, or empty content.
// Mirrors the --body-file pattern from `bd create` so agents can pass structured close
// templates without shell-escaping hell.
func resolveReasonFile(cmd *cobra.Command, existingReason string) (string, bool, error) {
	if !cmd.Flags().Changed("reason-file") {
		return "", false, nil
	}
	if existingReason != "" {
		return "", false, fmt.Errorf("cannot specify both --reason-file and --reason/--resolution/--message/--comment")
	}
	path, _ := cmd.Flags().GetString("reason-file")
	content, err := readBodyFile(path)
	if err != nil {
		return "", false, fmt.Errorf("reading reason file %q: %w", path, err)
	}
	if strings.TrimSpace(content) == "" {
		return "", false, fmt.Errorf("--reason-file %q is empty; close reason is required", path)
	}
	return content, true, nil
}

// resolveCloseTargets resolves a batch of partial issue IDs for `bd close`,
// preserving input order. For each ID it tries the local store first, then a
// shared contributor-routed store, then explicit prefix routing via routes.jsonl.
//
// Why "shared": opening the routed embeddeddolt store more than once in the same
// process deadlocks on its flock (same class as GH#3586 / beads-i4s). Per-id
// routing in close.go would hit that for every multi-id close where the IDs all
// route to the same target — i.e. the common case. We open at most one routed
// handle and reuse it for every routed ID; the cleanup func releases it.
//
// Each returned RoutedResult.Store points to whichever store actually owns the
// issue. The caller invokes cleanup() once when done; per-result Close() is a
// no-op for routed-via-shared-handle entries because they don't own the handle.
func resolveCloseTargets(ctx context.Context, localStore storage.DoltStorage, ids []string) ([]*RoutedResult, func(), error) {
	results := make([]*RoutedResult, 0, len(ids))
	var sharedRouted storage.DoltStorage
	var sharedRoutedTried bool
	cleanup := func() {
		for _, r := range results {
			r.Close()
		}
		if sharedRouted != nil {
			_ = sharedRouted.Close()
		}
	}
	ensureShared := func() (storage.DoltStorage, error) {
		if sharedRouted != nil {
			return sharedRouted, nil
		}
		if sharedRoutedTried {
			return nil, fmt.Errorf("no auto-routed store available")
		}
		sharedRoutedTried = true
		rs, routed, err := openRoutedReadStore(ctx, localStore)
		if err != nil {
			return nil, err
		}
		if !routed {
			return nil, fmt.Errorf("no auto-routed store available")
		}
		sharedRouted = rs
		return rs, nil
	}
	for _, id := range ids {
		// Local first.
		if r, err := resolveAndGetFromStore(ctx, localStore, id, false); err == nil {
			results = append(results, r)
			continue
		} else if !isNotFoundErr(err) {
			cleanup()
			return nil, func() {}, fmt.Errorf("resolving ID %s: %w", id, err)
		}
		// Shared routed store (contributor auto-routing).
		if rs, rerr := ensureShared(); rerr == nil {
			if r, err := resolveAndGetFromStore(ctx, rs, id, true); err == nil {
				// Per-id RoutedResult does not own the shared handle; cleanup() does.
				results = append(results, r)
				continue
			}
		}
		// Last resort: prefix routing opens its own store; per-result Close() handles it.
		if r, err := resolveViaPrefixRouting(ctx, id); err == nil {
			results = append(results, r)
			continue
		}
		cleanup()
		return nil, func() {}, fmt.Errorf("resolving ID %s: no issue found matching %q", id, id)
	}
	return results, cleanup, nil
}

// countEpicOpenChildren returns the number of open (non-closed) children for an epic.
// Uses GetDependentsWithMetadata to find parent-child relationships.
// Takes an explicit store so callers can route to the store actually holding the epic
// (relevant for contributor auto-routing where the epic lives in the planning repo).
func countEpicOpenChildren(ctx context.Context, s storage.DoltStorage, epicID string) int {
	dependents, err := s.GetDependentsWithMetadata(ctx, epicID)
	if err != nil {
		return 0
	}
	count := 0
	for _, dep := range dependents {
		if dep.DependencyType == types.DepParentChild && dep.Issue.Status != types.StatusClosed {
			count++
		}
	}
	return count
}
