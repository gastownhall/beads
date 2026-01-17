package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/ui"
)

// InteractiveConflict represents a conflict to be resolved interactively
type InteractiveConflict struct {
	IssueID string
	Local   *beads.Issue
	Remote  *beads.Issue
	Base    *beads.Issue // May be nil for first sync
}

// InteractiveResolution represents the user's choice for a conflict
type InteractiveResolution struct {
	Choice string       // "local", "remote", "merged", "skip"
	Issue  *beads.Issue // The resolved issue (nil if skipped)
}

// resolveConflictsInteractively handles manual conflict resolution with user prompts.
// Returns resolved issues and the count of skipped conflicts.
func resolveConflictsInteractively(conflicts []InteractiveConflict) ([]*beads.Issue, int, error) {
	// Check if we're in a terminal
	if !ui.IsTerminal() {
		return nil, 0, fmt.Errorf("manual conflict resolution requires an interactive terminal")
	}

	reader := bufio.NewReader(os.Stdin)
	var resolved []*beads.Issue
	skipped := 0

	fmt.Printf("\n%s Manual Conflict Resolution\n", ui.RenderAccent("🔧"))
	fmt.Printf("Found %d conflict(s) requiring manual resolution.\n\n", len(conflicts))

	for i, conflict := range conflicts {
		fmt.Printf("%s Conflict %d/%d: %s\n", ui.RenderAccent("━━━"), i+1, len(conflicts), conflict.IssueID)
		fmt.Println()

		// Display the diff
		displayConflictDiff(conflict)

		// Prompt for choice
		resolution, err := promptConflictResolution(reader, conflict)
		if err != nil {
			return nil, 0, fmt.Errorf("reading user input: %w", err)
		}

		switch resolution.Choice {
		case "skip":
			skipped++
			fmt.Printf("  %s Skipped\n\n", ui.RenderMuted("⏭"))
		case "local":
			resolved = append(resolved, conflict.Local)
			fmt.Printf("  %s Kept local version\n\n", ui.RenderPass("✓"))
		case "remote":
			resolved = append(resolved, conflict.Remote)
			fmt.Printf("  %s Kept remote version\n\n", ui.RenderPass("✓"))
		case "merged":
			resolved = append(resolved, resolution.Issue)
			fmt.Printf("  %s Used field-level merge\n\n", ui.RenderPass("✓"))
		}
	}

	return resolved, skipped, nil
}

// displayConflictDiff shows the differences between local and remote versions
func displayConflictDiff(conflict InteractiveConflict) {
	local := conflict.Local
	remote := conflict.Remote

	if local == nil && remote == nil {
		fmt.Println("  Both versions are nil (should not happen)")
		return
	}

	if local == nil {
		fmt.Printf("  %s Local: (deleted)\n", ui.RenderMuted("LOCAL"))
		fmt.Printf("  %s Remote: exists\n", ui.RenderAccent("REMOTE"))
		displayIssueSummary(remote, "    ")
		return
	}

	if remote == nil {
		fmt.Printf("  %s Local: exists\n", ui.RenderAccent("LOCAL"))
		displayIssueSummary(local, "    ")
		fmt.Printf("  %s Remote: (deleted)\n", ui.RenderMuted("REMOTE"))
		return
	}

	// Both exist - show field-by-field diff
	fmt.Printf("  %s\n", ui.RenderMuted("─── Field Differences ───"))
	fmt.Println()

	// Title
	if local.Title != remote.Title {
		fmt.Printf("  %s\n", ui.RenderAccent("title:"))
		fmt.Printf("    %s %s\n", ui.RenderMuted("local:"), local.Title)
		fmt.Printf("    %s %s\n", ui.RenderAccent("remote:"), remote.Title)
	}

	// Status
	if local.Status != remote.Status {
		fmt.Printf("  %s\n", ui.RenderAccent("status:"))
		fmt.Printf("    %s %s\n", ui.RenderMuted("local:"), local.Status)
		fmt.Printf("    %s %s\n", ui.RenderAccent("remote:"), remote.Status)
	}

	// Priority
	if local.Priority != remote.Priority {
		fmt.Printf("  %s\n", ui.RenderAccent("priority:"))
		fmt.Printf("    %s P%d\n", ui.RenderMuted("local:"), local.Priority)
		fmt.Printf("    %s P%d\n", ui.RenderAccent("remote:"), remote.Priority)
	}

	// Assignee
	if local.Assignee != remote.Assignee {
		fmt.Printf("  %s\n", ui.RenderAccent("assignee:"))
		fmt.Printf("    %s %s\n", ui.RenderMuted("local:"), valueOrNone(local.Assignee))
		fmt.Printf("    %s %s\n", ui.RenderAccent("remote:"), valueOrNone(remote.Assignee))
	}

	// Description (show truncated if different)
	if local.Description != remote.Description {
		fmt.Printf("  %s\n", ui.RenderAccent("description:"))
		fmt.Printf("    %s %s\n", ui.RenderMuted("local:"), truncateText(local.Description, 60))
		fmt.Printf("    %s %s\n", ui.RenderAccent("remote:"), truncateText(remote.Description, 60))
	}

	// Notes (show truncated if different)
	if local.Notes != remote.Notes {
		fmt.Printf("  %s\n", ui.RenderAccent("notes:"))
		fmt.Printf("    %s %s\n", ui.RenderMuted("local:"), truncateText(local.Notes, 60))
		fmt.Printf("    %s %s\n", ui.RenderAccent("remote:"), truncateText(remote.Notes, 60))
	}

	// Labels
	localLabels := strings.Join(local.Labels, ", ")
	remoteLabels := strings.Join(remote.Labels, ", ")
	if localLabels != remoteLabels {
		fmt.Printf("  %s\n", ui.RenderAccent("labels:"))
		fmt.Printf("    %s [%s]\n", ui.RenderMuted("local:"), valueOrNone(localLabels))
		fmt.Printf("    %s [%s]\n", ui.RenderAccent("remote:"), valueOrNone(remoteLabels))
	}

	// Updated timestamps
	fmt.Printf("  %s\n", ui.RenderAccent("updated_at:"))
	fmt.Printf("    %s %s\n", ui.RenderMuted("local:"), local.UpdatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("    %s %s\n", ui.RenderAccent("remote:"), remote.UpdatedAt.Format("2006-01-02 15:04:05"))

	// Indicate which is newer
	if local.UpdatedAt.After(remote.UpdatedAt) {
		fmt.Printf("    %s\n", ui.RenderPass("(local is newer)"))
	} else if remote.UpdatedAt.After(local.UpdatedAt) {
		fmt.Printf("    %s\n", ui.RenderPass("(remote is newer)"))
	} else {
		fmt.Printf("    %s\n", ui.RenderMuted("(same timestamp)"))
	}

	fmt.Println()
}

// displayIssueSummary shows a brief summary of an issue
func displayIssueSummary(issue *beads.Issue, indent string) {
	if issue == nil {
		return
	}
	fmt.Printf("%stitle: %s\n", indent, issue.Title)
	fmt.Printf("%sstatus: %s, priority: P%d\n", indent, issue.Status, issue.Priority)
	if issue.Assignee != "" {
		fmt.Printf("%sassignee: %s\n", indent, issue.Assignee)
	}
}

// promptConflictResolution asks the user how to resolve a conflict
func promptConflictResolution(reader *bufio.Reader, conflict InteractiveConflict) (InteractiveResolution, error) {
	local := conflict.Local
	remote := conflict.Remote

	// Build options based on what's available
	var options []string
	var optionMap = make(map[string]string)

	if local != nil {
		options = append(options, "l")
		optionMap["l"] = "local"
		optionMap["local"] = "local"
	}
	if remote != nil {
		options = append(options, "r")
		optionMap["r"] = "remote"
		optionMap["remote"] = "remote"
	}
	if local != nil && remote != nil {
		options = append(options, "m")
		optionMap["m"] = "merged"
		optionMap["merge"] = "merged"
		optionMap["merged"] = "merged"
	}
	options = append(options, "s", "d", "?")
	optionMap["s"] = "skip"
	optionMap["skip"] = "skip"
	optionMap["d"] = "diff"
	optionMap["diff"] = "diff"
	optionMap["?"] = "help"
	optionMap["help"] = "help"

	for {
		fmt.Printf("  Choice [%s]: ", strings.Join(options, "/"))
		input, err := reader.ReadString('\n')
		if err != nil {
			return InteractiveResolution{}, err
		}

		choice := strings.TrimSpace(strings.ToLower(input))
		if choice == "" {
			// Default to merged if both exist, otherwise keep the one that exists
			if local != nil && remote != nil {
				choice = "m"
			} else if local != nil {
				choice = "l"
			} else {
				choice = "r"
			}
		}

		action, ok := optionMap[choice]
		if !ok {
			fmt.Printf("  %s Unknown option '%s'. Type '?' for help.\n", ui.RenderFail("✗"), choice)
			continue
		}

		switch action {
		case "help":
			printResolutionHelp(local != nil, remote != nil)
			continue

		case "diff":
			// Show detailed diff (JSON dump)
			showDetailedDiff(conflict)
			continue

		case "local":
			return InteractiveResolution{Choice: "local", Issue: local}, nil

		case "remote":
			return InteractiveResolution{Choice: "remote", Issue: remote}, nil

		case "merged":
			// Do field-level merge (same as automatic LWW merge)
			merged := mergeFieldLevel(conflict.Base, local, remote)
			return InteractiveResolution{Choice: "merged", Issue: merged}, nil

		case "skip":
			return InteractiveResolution{Choice: "skip", Issue: nil}, nil
		}
	}
}

// printResolutionHelp shows help for resolution options
func printResolutionHelp(hasLocal, hasRemote bool) {
	fmt.Println()
	fmt.Println("  Resolution options:")
	if hasLocal {
		fmt.Println("    l, local  - Keep the local version")
	}
	if hasRemote {
		fmt.Println("    r, remote - Keep the remote version")
	}
	if hasLocal && hasRemote {
		fmt.Println("    m, merge  - Auto-merge (LWW for scalars, union for collections)")
	}
	fmt.Println("    s, skip   - Skip this conflict (leave unresolved)")
	fmt.Println("    d, diff   - Show detailed JSON diff")
	fmt.Println("    ?, help   - Show this help")
	fmt.Println()
}

// showDetailedDiff displays the full JSON of both versions
func showDetailedDiff(conflict InteractiveConflict) {
	fmt.Println()
	fmt.Printf("  %s\n", ui.RenderMuted("─── Detailed Diff (JSON) ───"))
	fmt.Println()

	if conflict.Local != nil {
		fmt.Printf("  %s\n", ui.RenderAccent("LOCAL:"))
		localJSON, _ := json.MarshalIndent(conflict.Local, "  ", "  ")
		fmt.Println(string(localJSON))
		fmt.Println()
	} else {
		fmt.Printf("  %s (deleted)\n", ui.RenderMuted("LOCAL:"))
	}

	if conflict.Remote != nil {
		fmt.Printf("  %s\n", ui.RenderAccent("REMOTE:"))
		remoteJSON, _ := json.MarshalIndent(conflict.Remote, "  ", "  ")
		fmt.Println(string(remoteJSON))
		fmt.Println()
	} else {
		fmt.Printf("  %s (deleted)\n", ui.RenderMuted("REMOTE:"))
	}
}

// Helper functions

func valueOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func truncateText(s string, maxLen int) string {
	if s == "" {
		return "(empty)"
	}
	// Replace newlines with spaces for display
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}
