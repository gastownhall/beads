package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
)

// SyncIntegrityResult contains the results of a pre-sync integrity check.
type SyncIntegrityResult struct {
	ForcedPush       *ForcedPushCheck  `json:"forced_push,omitempty"`
	PrefixMismatch   *PrefixMismatch   `json:"prefix_mismatch,omitempty"`
	OrphanedChildren *OrphanedChildren `json:"orphaned_children,omitempty"`
	HasProblems      bool              `json:"has_problems"`
}

// ForcedPushCheck detects if sync branch has diverged from remote.
type ForcedPushCheck struct {
	Detected  bool   `json:"detected"`
	LocalRef  string `json:"local_ref,omitempty"`
	RemoteRef string `json:"remote_ref,omitempty"`
	Message   string `json:"message"`
}

// PrefixMismatch detects issues with wrong prefix in JSONL.
type PrefixMismatch struct {
	ConfiguredPrefix string   `json:"configured_prefix"`
	MismatchedIDs    []string `json:"mismatched_ids,omitempty"`
	Count            int      `json:"count"`
}

// OrphanedChildren detects issues with parent that doesn't exist.
type OrphanedChildren struct {
	OrphanedIDs []string `json:"orphaned_ids,omitempty"`
	Count       int      `json:"count"`
}

// showSyncIntegrityCheck performs pre-sync integrity checks without modifying state.
// Detects forced pushes, prefix mismatches, and orphaned children.
// Exits with code 1 if problems are detected.
func showSyncIntegrityCheck(ctx context.Context, jsonlPath string) {
	fmt.Println("Sync Integrity Check")
	fmt.Println("====================")

	result := &SyncIntegrityResult{}

	// Check 1: Detect forced pushes on sync branch
	forcedPush := checkForcedPush(ctx)
	result.ForcedPush = forcedPush
	if forcedPush.Detected {
		result.HasProblems = true
	}
	printForcedPushResult(forcedPush)

	// Check 2: Detect prefix mismatches in JSONL
	prefixMismatch, err := checkPrefixMismatch(ctx, jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: prefix check failed: %v\n", err)
	} else {
		result.PrefixMismatch = prefixMismatch
		if prefixMismatch != nil && prefixMismatch.Count > 0 {
			result.HasProblems = true
		}
		printPrefixMismatchResult(prefixMismatch)
	}

	// Check 3: Detect orphaned children (parent issues that don't exist)
	orphaned, err := checkOrphanedChildrenInJSONL(jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: orphaned check failed: %v\n", err)
	} else {
		result.OrphanedChildren = orphaned
		if orphaned != nil && orphaned.Count > 0 {
			result.HasProblems = true
		}
		printOrphanedChildrenResult(orphaned)
	}

	// Summary
	fmt.Println("\nSummary")
	fmt.Println("-------")
	if result.HasProblems {
		fmt.Println("Problems detected! Review above and consider:")
		if result.ForcedPush != nil && result.ForcedPush.Detected {
			fmt.Println("  - Force push: Reset local sync branch or use 'bd sync --from-main'")
		}
		if result.PrefixMismatch != nil && result.PrefixMismatch.Count > 0 {
			fmt.Println("  - Prefix mismatch: Use 'bd import --rename-on-import' to fix")
		}
		if result.OrphanedChildren != nil && result.OrphanedChildren.Count > 0 {
			fmt.Println("  - Orphaned children: Remove parent references or create missing parents")
		}
		os.Exit(1)
	} else {
		fmt.Println("No problems detected. Safe to sync.")
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	}
}

// checkForcedPush detects if the sync branch has diverged from remote.
// This can happen when someone force-pushes to the sync branch.
func checkForcedPush(ctx context.Context) *ForcedPushCheck {
	result := &ForcedPushCheck{
		Detected: false,
		Message:  "No sync branch configured or no remote",
	}

	// Get sync branch name
	if err := ensureStoreActive(); err != nil {
		return result
	}

	syncBranch, _ := syncbranch.Get(ctx, store)
	if syncBranch == "" {
		return result
	}

	// Check if sync branch exists locally
	checkLocalCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+syncBranch) //nolint:gosec // syncBranch from config
	if checkLocalCmd.Run() != nil {
		result.Message = fmt.Sprintf("Sync branch '%s' does not exist locally", syncBranch)
		return result
	}

	// Get local ref
	localRefCmd := exec.CommandContext(ctx, "git", "rev-parse", syncBranch) //nolint:gosec // syncBranch from config
	localRefOutput, err := localRefCmd.Output()
	if err != nil {
		result.Message = "Failed to get local sync branch ref"
		return result
	}
	localRef := strings.TrimSpace(string(localRefOutput))
	result.LocalRef = localRef

	// Check if remote tracking branch exists
	remote := "origin"
	if configuredRemote, err := store.GetConfig(ctx, "sync.remote"); err == nil && configuredRemote != "" {
		remote = configuredRemote
	}

	// Get remote ref
	remoteRefCmd := exec.CommandContext(ctx, "git", "rev-parse", remote+"/"+syncBranch) //nolint:gosec // remote and syncBranch from config
	remoteRefOutput, err := remoteRefCmd.Output()
	if err != nil {
		result.Message = fmt.Sprintf("Remote tracking branch '%s/%s' does not exist", remote, syncBranch)
		return result
	}
	remoteRef := strings.TrimSpace(string(remoteRefOutput))
	result.RemoteRef = remoteRef

	// If refs match, no divergence
	if localRef == remoteRef {
		result.Message = "Sync branch is in sync with remote"
		return result
	}

	// Check if local is ahead of remote (normal case)
	aheadCmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", remoteRef, localRef) //nolint:gosec // refs from git rev-parse
	if aheadCmd.Run() == nil {
		result.Message = "Local sync branch is ahead of remote (normal)"
		return result
	}

	// Check if remote is ahead of local (behind, needs pull)
	behindCmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", localRef, remoteRef) //nolint:gosec // refs from git rev-parse
	if behindCmd.Run() == nil {
		result.Message = "Local sync branch is behind remote (needs pull)"
		return result
	}

	// If neither is ancestor, branches have diverged - likely a force push
	result.Detected = true
	result.Message = fmt.Sprintf("Sync branch has DIVERGED from remote! Local: %s, Remote: %s. This may indicate a force push on the remote.", localRef[:8], remoteRef[:8])

	return result
}

func printForcedPushResult(fp *ForcedPushCheck) {
	fmt.Println("1. Force Push Detection")
	if fp.Detected {
		fmt.Printf("   [PROBLEM] %s\n", fp.Message)
	} else {
		fmt.Printf("   [OK] %s\n", fp.Message)
	}
	fmt.Println()
}

// checkPrefixMismatch detects issues in JSONL that don't match the configured prefix.
func checkPrefixMismatch(ctx context.Context, jsonlPath string) (*PrefixMismatch, error) {
	result := &PrefixMismatch{
		MismatchedIDs: []string{},
	}

	// Get configured prefix
	if err := ensureStoreActive(); err != nil {
		return nil, err
	}

	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix == "" {
		prefix = "bd" // Default
	}
	result.ConfiguredPrefix = prefix

	// Read JSONL and check each issue's prefix
	f, err := os.Open(jsonlPath) // #nosec G304 - controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // No JSONL, no mismatches
		}
		return nil, fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue // Skip malformed lines
		}

		// Check if ID starts with configured prefix
		if !strings.HasPrefix(issue.ID, prefix+"-") {
			result.MismatchedIDs = append(result.MismatchedIDs, issue.ID)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read JSONL: %w", err)
	}

	result.Count = len(result.MismatchedIDs)
	return result, nil
}

func printPrefixMismatchResult(pm *PrefixMismatch) {
	fmt.Println("2. Prefix Mismatch Check")
	if pm == nil {
		fmt.Println("   [SKIP] Could not check prefix")
		fmt.Println()
		return
	}

	fmt.Printf("   Configured prefix: %s\n", pm.ConfiguredPrefix)
	if pm.Count > 0 {
		fmt.Printf("   [PROBLEM] Found %d issue(s) with wrong prefix:\n", pm.Count)
		// Show first 10
		limit := pm.Count
		if limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("      - %s\n", pm.MismatchedIDs[i])
		}
		if pm.Count > 10 {
			fmt.Printf("      ... and %d more\n", pm.Count-10)
		}
	} else {
		fmt.Println("   [OK] All issues have correct prefix")
	}
	fmt.Println()
}

// checkOrphanedChildrenInJSONL detects issues with parent references to non-existent issues.
func checkOrphanedChildrenInJSONL(jsonlPath string) (*OrphanedChildren, error) {
	result := &OrphanedChildren{
		OrphanedIDs: []string{},
	}

	// Read JSONL and build maps of IDs and parent references
	f, err := os.Open(jsonlPath) // #nosec G304 - controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer f.Close()

	existingIDs := make(map[string]bool)
	parentRefs := make(map[string]string) // child ID -> parent ID

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var issue struct {
			ID     string `json:"id"`
			Parent string `json:"parent,omitempty"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue
		}

		// Skip tombstones
		if issue.Status == string(types.StatusTombstone) {
			continue
		}

		existingIDs[issue.ID] = true
		if issue.Parent != "" {
			parentRefs[issue.ID] = issue.Parent
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read JSONL: %w", err)
	}

	// Find orphaned children (parent doesn't exist)
	for childID, parentID := range parentRefs {
		if !existingIDs[parentID] {
			result.OrphanedIDs = append(result.OrphanedIDs, fmt.Sprintf("%s (parent: %s)", childID, parentID))
		}
	}

	result.Count = len(result.OrphanedIDs)
	return result, nil
}

// runGitCmdWithTimeoutMsg runs a git command and prints a helpful message if it takes too long.
// This helps when git operations hang waiting for credential/browser auth.
func runGitCmdWithTimeoutMsg(ctx context.Context, cmd *exec.Cmd, cmdName string, timeoutDelay time.Duration) ([]byte, error) {
	// Use done channel to cleanly exit goroutine when command completes
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(timeoutDelay):
			fmt.Fprintf(os.Stderr, "⏳ %s is taking longer than expected (possibly waiting for authentication). If this hangs, check for a browser auth prompt or run 'git status' in another terminal.\n", cmdName)
		case <-done:
			// Command completed, exit cleanly
		case <-ctx.Done():
			// Context canceled, don't print message
		}
	}()

	output, err := cmd.CombinedOutput()
	close(done)
	return output, err
}

func printOrphanedChildrenResult(oc *OrphanedChildren) {
	fmt.Println("3. Orphaned Children Check")
	if oc == nil {
		fmt.Println("   [SKIP] Could not check orphaned children")
		fmt.Println()
		return
	}

	if oc.Count > 0 {
		fmt.Printf("   [PROBLEM] Found %d issue(s) with missing parent:\n", oc.Count)
		limit := oc.Count
		if limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("      - %s\n", oc.OrphanedIDs[i])
		}
		if oc.Count > 10 {
			fmt.Printf("      ... and %d more\n", oc.Count-10)
		}
	} else {
		fmt.Println("   [OK] No orphaned children found")
	}
	fmt.Println()
}
