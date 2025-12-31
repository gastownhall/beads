package main

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/ui"
)

// previewFixes shows what would be fixed without applying changes
func previewFixes(result doctorResult) {
	// Collect all fixable issues
	var fixableIssues []doctorCheck
	for _, check := range result.Checks {
		if (check.Status == statusWarning || check.Status == statusError) && check.Fix != "" {
			fixableIssues = append(fixableIssues, check)
		}
	}

	if len(fixableIssues) == 0 {
		fmt.Println("\n✓ No fixable issues found (dry-run)")
		return
	}

	fmt.Println("\n[DRY-RUN] The following issues would be fixed with --fix:")
	fmt.Println()

	for i, issue := range fixableIssues {
		// Show the issue details
		fmt.Printf("  %d. %s\n", i+1, issue.Name)
		if issue.Status == statusError {
			fmt.Printf("     Status: %s\n", ui.RenderFail("ERROR"))
		} else {
			fmt.Printf("     Status: %s\n", ui.RenderWarn("WARNING"))
		}
		fmt.Printf("     Issue:  %s\n", issue.Message)
		if issue.Detail != "" {
			fmt.Printf("     Detail: %s\n", issue.Detail)
		}
		fmt.Printf("     Fix:    %s\n", issue.Fix)
		fmt.Println()
	}

	fmt.Printf("[DRY-RUN] Would attempt to fix %d issue(s)\n", len(fixableIssues))
	fmt.Println("Run 'bd doctor --fix' to apply these fixes")
}

func applyFixes(result doctorResult) {
	// Collect all fixable issues
	var fixableIssues []doctorCheck
	for _, check := range result.Checks {
		if (check.Status == statusWarning || check.Status == statusError) && check.Fix != "" {
			fixableIssues = append(fixableIssues, check)
		}
	}

	if len(fixableIssues) == 0 {
		fmt.Println("\nNo fixable issues found.")
		return
	}

	// Show what will be fixed
	fmt.Println("\nFixable issues:")
	for i, issue := range fixableIssues {
		fmt.Printf("  %d. %s: %s\n", i+1, issue.Name, issue.Message)
	}

	// Interactive mode - confirm each fix individually
	if doctorInteractive {
		applyFixesInteractive(result.Path, fixableIssues)
		return
	}

	// Ask for confirmation (skip if --yes flag is set)
	if !doctorYes {
		fmt.Printf("\nThis will attempt to fix %d issue(s). Continue? (Y/n): ", len(fixableIssues))
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			return
		}

		response = strings.TrimSpace(strings.ToLower(response))
		if response != "" && response != "y" && response != "yes" {
			fmt.Println("Fix canceled.")
			return
		}
	}

	// Apply fixes
	fmt.Println("\nApplying fixes...")
	applyFixList(result.Path, fixableIssues)
}

// applyFixesInteractive prompts for each fix individually
func applyFixesInteractive(path string, issues []doctorCheck) {
	reader := bufio.NewReader(os.Stdin)
	applyAll := false
	var approvedFixes []doctorCheck

	fmt.Println("\nReview each fix:")
	fmt.Println("  [y]es - apply this fix")
	fmt.Println("  [n]o  - skip this fix")
	fmt.Println("  [a]ll - apply all remaining fixes")
	fmt.Println("  [q]uit - stop without applying more fixes")
	fmt.Println()

	for i, issue := range issues {
		// Show issue details
		fmt.Printf("(%d/%d) %s\n", i+1, len(issues), issue.Name)
		if issue.Status == statusError {
			fmt.Printf("  Status: %s\n", ui.RenderFail("ERROR"))
		} else {
			fmt.Printf("  Status: %s\n", ui.RenderWarn("WARNING"))
		}
		fmt.Printf("  Issue:  %s\n", issue.Message)
		if issue.Detail != "" {
			fmt.Printf("  Detail: %s\n", issue.Detail)
		}
		fmt.Printf("  Fix:    %s\n", issue.Fix)

		// Check if we should apply all remaining
		if applyAll {
			fmt.Println("  → Auto-approved (apply all)")
			approvedFixes = append(approvedFixes, issue)
			continue
		}

		// Prompt for this fix
		fmt.Print("\n  Apply this fix? [y/n/a/q]: ")
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			return
		}

		response = strings.TrimSpace(strings.ToLower(response))
		switch response {
		case "y", "yes":
			approvedFixes = append(approvedFixes, issue)
			fmt.Println("  → Approved")
		case "n", "no", "":
			fmt.Println("  → Skipped")
		case "a", "all":
			applyAll = true
			approvedFixes = append(approvedFixes, issue)
			fmt.Println("  → Approved (applying all remaining)")
		case "q", "quit":
			fmt.Println("  → Quit")
			if len(approvedFixes) > 0 {
				fmt.Printf("\nApplying %d approved fix(es)...\n", len(approvedFixes))
				applyFixList(path, approvedFixes)
			} else {
				fmt.Println("\nNo fixes applied.")
			}
			return
		default:
			// Treat unknown input as skip
			fmt.Println("  → Skipped (unrecognized input)")
		}
		fmt.Println()
	}

	// Apply all approved fixes
	if len(approvedFixes) > 0 {
		fmt.Printf("\nApplying %d approved fix(es)...\n", len(approvedFixes))
		applyFixList(path, approvedFixes)
	} else {
		fmt.Println("\nNo fixes approved.")
	}
}

// applyFixList applies a list of fixes and reports results
func applyFixList(path string, fixes []doctorCheck) {
	// Apply fixes in a dependency-aware order.
	// Rough dependency chain:
	// permissions/daemon cleanup → config sanity → DB integrity/migrations → DB↔JSONL sync.
	order := []string{
		"Permissions",
		"Daemon Health",
		"Database Config",
		"JSONL Config",
		"Database Integrity",
		"Database",
		"Schema Compatibility",
		"JSONL Integrity",
		"DB-JSONL Sync",
	}
	priority := make(map[string]int, len(order))
	for i, name := range order {
		priority[name] = i
	}
	slices.SortStableFunc(fixes, func(a, b doctorCheck) int {
		pa, oka := priority[a.Name]
		if !oka {
			pa = 1000
		}
		pb, okb := priority[b.Name]
		if !okb {
			pb = 1000
		}
		if pa < pb {
			return -1
		}
		if pa > pb {
			return 1
		}
		return 0
	})

	fixedCount := 0
	errorCount := 0

	for _, check := range fixes {
		fmt.Printf("\nFixing %s...\n", check.Name)

		var err error
		switch check.Name {
		case "Gitignore":
			err = doctor.FixGitignore()
		case "Redirect Tracking":
			err = doctor.FixRedirectTracking()
		case "Git Hooks":
			err = fix.GitHooks(path)
		case "Daemon Health":
			err = fix.Daemon(path)
		case "DB-JSONL Sync":
			err = fix.DBJSONLSync(path)
		case "Permissions":
			err = fix.Permissions(path)
		case "Database":
			err = fix.DatabaseVersion(path)
		case "Database Integrity":
			// Corruption detected - try recovery from JSONL
			// Pass force and source flags for enhanced recovery
			err = fix.DatabaseCorruptionRecoveryWithOptions(path, doctorForce, doctorSource)
		case "Schema Compatibility":
			err = fix.SchemaCompatibility(path)
		case "Repo Fingerprint":
			err = fix.RepoFingerprint(path)
		case "Git Merge Driver":
			err = fix.MergeDriver(path)
		case "Sync Branch Config":
			// No auto-fix: sync-branch should be added to config.yaml (version controlled)
			fmt.Printf("  ⚠ Add 'sync-branch: beads-sync' to .beads/config.yaml\n")
			continue
		case "Database Config":
			err = fix.DatabaseConfig(path)
		case "JSONL Config":
			err = fix.LegacyJSONLConfig(path)
		case "JSONL Integrity":
			err = fix.JSONLIntegrity(path)
		case "Deletions Manifest":
			err = fix.MigrateTombstones(path)
		case "Untracked Files":
			err = fix.UntrackedJSONL(path)
		case "Sync Branch Health":
			// Get sync branch from config
			syncBranch := syncbranch.GetFromYAML()
			if syncBranch == "" {
				fmt.Printf("  ⚠ No sync branch configured in config.yaml\n")
				continue
			}
			err = fix.SyncBranchHealth(path, syncBranch)
		case "Merge Artifacts":
			err = fix.MergeArtifacts(path)
		case "Orphaned Dependencies":
			err = fix.OrphanedDependencies(path, doctorVerbose)
		case "Child-Parent Dependencies":
			// Requires explicit opt-in flag (destructive, may remove intentional deps)
			if !doctorFixChildParent {
				fmt.Printf("  ⚠ Child→parent deps require explicit opt-in: bd doctor --fix --fix-child-parent\n")
				continue
			}
			err = fix.ChildParentDependencies(path, doctorVerbose)
		case "Duplicate Issues":
			// No auto-fix: duplicates require user review
			fmt.Printf("  ⚠ Run 'bd duplicates' to review and merge duplicates\n")
			continue
		case "Test Pollution":
			// No auto-fix: test cleanup requires user review
			fmt.Printf("  ⚠ Run 'bd doctor --check=pollution' to review and clean test issues\n")
			continue
		case "Git Conflicts":
			// No auto-fix: git conflicts require manual resolution
			fmt.Printf("  ⚠ Resolve conflicts manually: git checkout --ours or --theirs .beads/issues.jsonl\n")
			continue
		case "Stale Closed Issues":
			// consolidate cleanup into doctor --fix
			err = fix.StaleClosedIssues(path)
		case "Expired Tombstones":
			// consolidate cleanup into doctor --fix
			err = fix.ExpiredTombstones(path)
		case "Compaction Candidates":
			// No auto-fix: compaction requires agent review
			fmt.Printf("  ⚠ Run 'bd compact --analyze' to review candidates\n")
			continue
		case "Large Database":
			// No auto-fix: pruning deletes data, must be user-controlled
			fmt.Printf("  ⚠ Run 'bd cleanup --older-than 90' to prune old closed issues\n")
			continue
		default:
			fmt.Printf("  ⚠ No automatic fix available for %s\n", check.Name)
			fmt.Printf("  Manual fix: %s\n", check.Fix)
			continue
		}

		if err != nil {
			errorCount++
			fmt.Printf("  %s Error: %v\n", ui.RenderFail("✗"), err)
			fmt.Printf("  Manual fix: %s\n", check.Fix)
		} else {
			fixedCount++
			fmt.Printf("  %s Fixed\n", ui.RenderPass("✓"))
		}
	}

	// Summary
	fmt.Printf("\nFix summary: %d fixed, %d errors\n", fixedCount, errorCount)
	if errorCount > 0 {
		fmt.Println("\nSome fixes failed. Please review the errors above and apply manual fixes as needed.")
	}
}
