package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
)

// runTeamWizard guides the user through team workflow setup
func runTeamWizard(ctx context.Context, store storage.Storage) error {
	fmt.Printf("\n%s %s\n\n", ui.RenderBold("bd"), ui.RenderBold("Team Workflow Setup Wizard"))
	fmt.Println("This wizard will configure beads for team collaboration.")
	fmt.Println()

	// Step 1: Check if we're in a git repository
	fmt.Printf("%s Detecting git repository setup...\n", ui.RenderAccent("▶"))

	if !isGitRepo() {
		fmt.Printf("%s Not in a git repository\n", ui.RenderWarn("⚠"))
		fmt.Println("\n  Initialize git first:")
		fmt.Println("  git init")
		fmt.Println()
		return fmt.Errorf("not in a git repository")
	}

	// Get current branch
	currentBranch, err := getGitBranch()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	fmt.Printf("%s Current branch: %s\n", ui.RenderPass("✓"), currentBranch)

	// Step 2: Check for protected main branch
	fmt.Printf("\n%s Checking branch configuration...\n", ui.RenderAccent("▶"))

	fmt.Println("\nIs your main branch protected (prevents direct commits)?")
	fmt.Println("  GitHub: Settings → Branches → Branch protection rules")
	fmt.Println("  GitLab: Settings → Repository → Protected branches")
	fmt.Print("\nProtected main branch? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	protectedMain := (response == "y" || response == "yes")

	var syncBranch string

	if protectedMain {
		fmt.Printf("\n%s Protected main detected\n", ui.RenderPass("✓"))
		fmt.Println("\n  Beads will commit issue updates to a separate branch.")
		fmt.Printf("  Default sync branch: %s\n", ui.RenderAccent("beads-metadata"))
		fmt.Print("\n  Sync branch name [press Enter for default]: ")

		branchName, _ := reader.ReadString('\n')
		branchName = strings.TrimSpace(branchName)

		if branchName == "" {
			syncBranch = "beads-metadata"
		} else {
			syncBranch = branchName
		}

		fmt.Printf("\n%s Sync branch set to: %s\n", ui.RenderPass("✓"), syncBranch)

		// Set sync.branch config
		if err := store.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
			return fmt.Errorf("failed to set sync branch: %w", err)
		}

		// Create the sync branch if it doesn't exist
		fmt.Printf("\n%s Creating sync branch...\n", ui.RenderAccent("▶"))

		if err := createSyncBranch(syncBranch); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create sync branch: %v\n", err)
			fmt.Println("  You can create it manually: git checkout -b", syncBranch)
		} else {
			fmt.Printf("%s Sync branch created\n", ui.RenderPass("✓"))
		}

	} else {
		fmt.Printf("%s Direct commits to %s\n", ui.RenderPass("✓"), currentBranch)
		syncBranch = currentBranch
	}

	// Step 3: Configure team settings
	fmt.Printf("\n%s Configuring team settings...\n", ui.RenderAccent("▶"))

	// Set team.enabled to true
	if err := store.SetConfig(ctx, "team.enabled", "true"); err != nil {
		return fmt.Errorf("failed to enable team mode: %w", err)
	}

	// Set team.sync_branch
	if err := store.SetConfig(ctx, "team.sync_branch", syncBranch); err != nil {
		return fmt.Errorf("failed to set team sync branch: %w", err)
	}

	fmt.Printf("%s Team mode enabled\n", ui.RenderPass("✓"))

	// Step 4: Configure auto-sync
	fmt.Println("\n  Enable automatic sync (daemon commits/pushes)?")
	fmt.Println("  • Auto-commit: Commits issue changes every 5 seconds")
	fmt.Println("  • Auto-push: Pushes commits to remote")
	fmt.Print("\nEnable auto-sync? [Y/n]: ")

	response, _ = reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	autoSync := !(response == "n" || response == "no")

	if autoSync {
		if err := store.SetConfig(ctx, "daemon.auto_commit", "true"); err != nil {
			return fmt.Errorf("failed to enable auto-commit: %w", err)
		}

		if err := store.SetConfig(ctx, "daemon.auto_push", "true"); err != nil {
			return fmt.Errorf("failed to enable auto-push: %w", err)
		}

		fmt.Printf("%s Auto-sync enabled\n", ui.RenderPass("✓"))
	} else {
		fmt.Printf("%s Auto-sync disabled (manual sync with 'bd sync')\n", ui.RenderWarn("⚠"))
	}

	// Step 5: Summary
	fmt.Printf("\n%s %s\n\n", ui.RenderPass("✓"), ui.RenderBold("Team setup complete!"))

	fmt.Println("Configuration:")
	if protectedMain {
		fmt.Printf("  Protected main: %s\n", ui.RenderAccent("yes"))
		fmt.Printf("  Sync branch: %s\n", ui.RenderAccent(syncBranch))
		fmt.Printf("  Commits will go to: %s\n", ui.RenderAccent(syncBranch))
		fmt.Printf("  Merge to main via: %s\n", ui.RenderAccent("Pull Request"))
	} else {
		fmt.Printf("  Protected main: %s\n", ui.RenderAccent("no"))
		fmt.Printf("  Commits will go to: %s\n", ui.RenderAccent(currentBranch))
	}

	if autoSync {
		fmt.Printf("  Auto-sync: %s\n", ui.RenderAccent("enabled"))
	} else {
		fmt.Printf("  Auto-sync: %s\n", ui.RenderAccent("disabled"))
	}

	fmt.Println()
	fmt.Println("How it works:")
	fmt.Println("  • All team members work on the same repository")
	fmt.Println("  • Issues are shared via git commits")
	fmt.Println("  • Use 'bd list' to see all team's issues")

	if protectedMain {
		fmt.Println("  • Issue updates commit to", syncBranch)
		fmt.Println("  • Periodically merge", syncBranch, "to main via PR")
	}

	if autoSync {
		fmt.Println("  • Daemon automatically commits and pushes changes")
	} else {
		fmt.Println("  • Run 'bd sync' manually to sync changes")
	}

	fmt.Println()
	fmt.Printf("Try it: %s\n", ui.RenderAccent("bd create \"Team planning issue\" -p 2"))
	fmt.Println()
	
	if protectedMain {
		fmt.Println("Next steps:")
		fmt.Printf("  1. %s\n", "Share the "+syncBranch+" branch with your team")
		fmt.Printf("  2. %s\n", "Team members: git pull origin "+syncBranch)
		fmt.Printf("  3. %s\n", "Periodically: merge "+syncBranch+" to main via PR")
		fmt.Println()
	}

	return nil
}

// getGitBranch returns the current git branch name
// Uses symbolic-ref instead of rev-parse to work in fresh repos without commits (bd-flil)
func getGitBranch() (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

// createSyncBranch creates a new branch for beads sync
func createSyncBranch(branchName string) error {
	// Check if branch already exists
	cmd := exec.Command("git", "rev-parse", "--verify", branchName)
	if err := cmd.Run(); err == nil {
		// Branch exists, nothing to do
		return nil
	}
	
	// Create new branch from current HEAD
	cmd = exec.Command("git", "checkout", "-b", branchName)
	if err := cmd.Run(); err != nil {
		return err
	}
	
	// Switch back to original branch
	currentBranch, err := getGitBranch()
	if err == nil && currentBranch != branchName {
		cmd = exec.Command("git", "checkout", "-")
		_ = cmd.Run() // Ignore error, branch creation succeeded
	}
	
	return nil
}
