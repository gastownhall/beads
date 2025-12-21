package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
)

// runContributorWizard guides the user through OSS contributor setup
func runContributorWizard(ctx context.Context, store storage.Storage) error {
	fmt.Printf("\n%s %s\n\n", ui.RenderBold("bd"), ui.RenderBold("Contributor Workflow Setup Wizard"))
	fmt.Println("This wizard will configure beads for OSS contribution.")
	fmt.Println()

	// Step 1: Detect fork relationship
	fmt.Printf("%s Detecting git repository setup...\n", ui.RenderAccent("▶"))

	isFork, upstreamURL := detectForkSetup()

	if isFork {
		fmt.Printf("%s Detected fork workflow (upstream: %s)\n", ui.RenderPass("✓"), upstreamURL)
	} else {
		fmt.Printf("%s No upstream remote detected\n", ui.RenderWarn("⚠"))
		fmt.Println("\n  For fork workflows, add an 'upstream' remote:")
		fmt.Println("  git remote add upstream <original-repo-url>")
		fmt.Println()

		// Ask if they want to continue anyway
		fmt.Print("Continue with contributor setup? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))

		if response != "y" && response != "yes" {
			fmt.Println("Setup canceled.")
			return nil
		}
	}

	// Step 2: Check push access to origin
	fmt.Printf("\n%s Checking repository access...\n", ui.RenderAccent("▶"))

	hasPushAccess, originURL := checkPushAccess()

	if hasPushAccess {
		fmt.Printf("%s You have push access to origin (%s)\n", ui.RenderPass("✓"), originURL)
		fmt.Printf("  %s You can commit directly to this repository.\n", ui.RenderWarn("⚠"))
		fmt.Println()
		fmt.Print("Do you want to use a separate planning repo anyway? [Y/n]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))

		if response == "n" || response == "no" {
			fmt.Println("\nSetup canceled. Your issues will be stored in the current repository.")
			return nil
		}
	} else {
		fmt.Printf("%s Read-only access to origin (%s)\n", ui.RenderPass("✓"), originURL)
		fmt.Println("  Planning repo recommended to keep experimental work separate.")
	}

	// Step 3: Configure planning repository
	fmt.Printf("\n%s Setting up planning repository...\n", ui.RenderAccent("▶"))

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	defaultPlanningRepo := filepath.Join(homeDir, ".beads-planning")

	fmt.Printf("\nWhere should contributor planning issues be stored?\n")
	fmt.Printf("Default: %s\n", ui.RenderAccent(defaultPlanningRepo))
	fmt.Print("Planning repo path [press Enter for default]: ")

	reader := bufio.NewReader(os.Stdin)
	planningPath, _ := reader.ReadString('\n')
	planningPath = strings.TrimSpace(planningPath)

	if planningPath == "" {
		planningPath = defaultPlanningRepo
	}

	// Expand ~ if present
	if strings.HasPrefix(planningPath, "~/") {
		planningPath = filepath.Join(homeDir, planningPath[2:])
	}

	// Create planning repository if it doesn't exist
	if _, err := os.Stat(planningPath); os.IsNotExist(err) {
		fmt.Printf("\nCreating planning repository at %s\n", ui.RenderAccent(planningPath))

		if err := os.MkdirAll(planningPath, 0750); err != nil {
			return fmt.Errorf("failed to create planning repo directory: %w", err)
		}

		// Initialize git repo in planning directory
		cmd := exec.Command("git", "init")
		cmd.Dir = planningPath
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize git in planning repo: %w", err)
		}

		// Initialize beads in planning repo
		beadsDir := filepath.Join(planningPath, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			return fmt.Errorf("failed to create .beads in planning repo: %w", err)
		}

		// Create issues.jsonl (canonical name, bd-6xd)
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		// #nosec G306 -- planning repo JSONL must be shareable across collaborators
		if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
			return fmt.Errorf("failed to create issues.jsonl: %w", err)
		}

		// Create README in planning repo
		readmePath := filepath.Join(planningPath, "README.md")
		readmeContent := fmt.Sprintf(`# Beads Planning Repository

This repository stores contributor planning issues for OSS projects.

## Purpose

- Keep experimental planning separate from upstream PRs
- Track discovered work and implementation notes
- Maintain private todos and design exploration

## Usage

Issues here are automatically created when working on forked repositories.

Created by: bd init --contributor
`)
		// #nosec G306 -- README should be world-readable
		if err := os.WriteFile(readmePath, []byte(readmeContent), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create README: %v\n", err)
		}

		// Initial commit in planning repo
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = planningPath
		_ = cmd.Run()

		cmd = exec.Command("git", "commit", "-m", "Initial commit: beads planning repository")
		cmd.Dir = planningPath
		_ = cmd.Run()

		fmt.Printf("%s Planning repository created\n", ui.RenderPass("✓"))
	} else {
		fmt.Printf("%s Using existing planning repository\n", ui.RenderPass("✓"))
	}

	// Step 4: Configure contributor routing
	fmt.Printf("\n%s Configuring contributor auto-routing...\n", ui.RenderAccent("▶"))

	// Set contributor.planning_repo config
	if err := store.SetConfig(ctx, "contributor.planning_repo", planningPath); err != nil {
		return fmt.Errorf("failed to set planning repo config: %w", err)
	}

	// Set contributor.auto_route to true
	if err := store.SetConfig(ctx, "contributor.auto_route", "true"); err != nil {
		return fmt.Errorf("failed to enable auto-routing: %w", err)
	}

	fmt.Printf("%s Auto-routing enabled\n", ui.RenderPass("✓"))

	// If this is a fork, configure sync to pull beads from upstream (bd-bx9)
	// This ensures `bd sync` gets the latest issues from the source repo,
	// not from the fork's potentially outdated origin/main
	if isFork {
		if err := store.SetConfig(ctx, "sync.remote", "upstream"); err != nil {
			return fmt.Errorf("failed to set sync remote: %w", err)
		}
		fmt.Printf("%s Sync configured to pull from upstream (source repo)\n", ui.RenderPass("✓"))
	}

	// Step 5: Summary
	fmt.Printf("\n%s %s\n\n", ui.RenderPass("✓"), ui.RenderBold("Contributor setup complete!"))

	fmt.Println("Configuration:")
	fmt.Printf("  Current repo issues: %s\n", ui.RenderAccent(".beads/issues.jsonl"))
	fmt.Printf("  Planning repo issues: %s\n", ui.RenderAccent(filepath.Join(planningPath, ".beads/issues.jsonl")))
	fmt.Println()
	fmt.Println("How it works:")
	fmt.Println("  • Issues you create will route to the planning repo")
	fmt.Println("  • Planning stays out of your PRs to upstream")
	fmt.Println("  • Use 'bd list' to see issues from both repos")
	fmt.Println()
	fmt.Printf("Try it: %s\n", ui.RenderAccent("bd create \"Plan feature X\" -p 2"))
	fmt.Println()

	return nil
}

// detectForkSetup checks if we're in a fork by looking for upstream remote
func detectForkSetup() (isFork bool, upstreamURL string) {
	cmd := exec.Command("git", "remote", "get-url", "upstream")
	output, err := cmd.Output()
	if err != nil {
		// No upstream remote found
		return false, ""
	}

	upstreamURL = strings.TrimSpace(string(output))
	return true, upstreamURL
}

// checkPushAccess determines if we have push access to origin
func checkPushAccess() (hasPush bool, originURL string) {
	// Get origin URL
	cmd := exec.Command("git", "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return false, ""
	}

	originURL = strings.TrimSpace(string(output))

	// SSH URLs indicate likely push access (git@github.com:...)
	if strings.HasPrefix(originURL, "git@") {
		return true, originURL
	}

	// HTTPS URLs typically indicate read-only clone
	if strings.HasPrefix(originURL, "https://") {
		return false, originURL
	}

	// Other protocols (file://, etc.) assume push access
	return true, originURL
}
