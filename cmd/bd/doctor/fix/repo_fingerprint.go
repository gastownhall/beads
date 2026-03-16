package fix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
)

var repoFingerprintReadLine = readLineUnbuffered

// readLineUnbuffered reads a line from stdin without buffering.
// This avoids consuming input past the newline, keeping stdin available
// for any further prompts in the same session.
func readLineUnbuffered() (string, error) {
	var result []byte
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return string(result), err
		}
		if n == 1 {
			c := buf[0] // #nosec G602 -- n==1 guarantees buf has 1 byte
			if c == '\n' {
				return string(result), nil
			}
			result = append(result, c)
		}
	}
}

// updateRepoIDInProcess updates the repo_id metadata directly in the Dolt store,
// avoiding subprocess lock contention. (GH#1805)
func updateRepoIDInProcess(path string, autoYes bool) error {
	ctx := context.Background()

	// Compute new repo ID
	newRepoID, err := beads.ComputeRepoIDForPath(path)
	if err != nil {
		return fmt.Errorf("failed to compute repository ID: %w", err)
	}

	// Open database
	store, err := openDoltStoreForRepoPath(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Get old repo ID (treat any error as "no existing repo_id")
	oldRepoID, _ := store.GetMetadata(ctx, "repo_id")

	oldDisplay := "none"
	if len(oldRepoID) >= 8 {
		oldDisplay = oldRepoID[:8]
	}
	newDisplay := newRepoID
	if len(newDisplay) >= 8 {
		newDisplay = newDisplay[:8]
	}

	// Prompt for confirmation if repo_id exists and differs
	if oldRepoID != "" && oldRepoID != newRepoID && !autoYes {
		fmt.Printf("  WARNING: Changing repository ID can break sync if other clones exist.\n\n")
		fmt.Printf("  Current repo ID: %s\n", oldDisplay)
		fmt.Printf("  New repo ID:     %s\n\n", newDisplay)
		fmt.Printf("  Continue? [y/N] ")
		response, err := repoFingerprintReadLine()
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("  → Canceled")
			return nil
		}
	}

	// Update repo ID
	if err := store.SetMetadata(ctx, "repo_id", newRepoID); err != nil {
		return fmt.Errorf("failed to update repo_id: %w", err)
	}

	fmt.Printf("  ✓ Repository ID updated (old: %s, new: %s)\n", oldDisplay, newDisplay)
	return nil
}

// RepoFingerprint fixes repo fingerprint mismatches by prompting the user
// for which action to take. This is interactive because the consequences
// differ significantly between options:
//  1. Update repo ID (if URL changed or bd upgraded)
//  2. Reinitialize database (if wrong database was copied)
//  3. Skip (do nothing)
//
// All operations are performed in-process to avoid Dolt lock contention
// that occurs when spawning bd subcommands. (GH#1805)
func RepoFingerprint(path string, autoYes bool) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// In --yes mode, auto-select the recommended safe action [1].
	if autoYes {
		fmt.Println("  → Auto mode (--yes): updating repo ID in-process...")
		return updateRepoIDInProcess(path, true)
	}

	// Prompt user for action
	fmt.Println("\n  Repo fingerprint mismatch detected. Choose an action:")
	fmt.Println()
	fmt.Println("    [1] Update repo ID (if git remote URL changed or bd was upgraded)")
	fmt.Println("    [2] Reinitialize database (if wrong .beads was copied here)")
	fmt.Println("    [s] Skip (do nothing)")
	fmt.Println()
	fmt.Print("  Choice [1/2/s]: ")

	// Read single character without buffering to avoid consuming input meant for subprocesses
	response, err := repoFingerprintReadLine()
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))

	switch response {
	case "1":
		return updateRepoIDInProcess(path, false)

	case "2":
		info, err := resolveRuntimeInfoForRepo(path)
		if err != nil {
			return fmt.Errorf("failed to resolve repo runtime: %w", err)
		}
		cfg := effectiveFixConfig(info.Config)
		database := selectedRuntimeDatabase(info.Runtime, cfg)
		deleteTarget := runtimeDatabaseDir(info.Runtime)
		if deleteTarget == "" {
			deleteTarget = cfg.DatabasePath(beadsDir)
		}

		// Confirm before destructive action
		fmt.Printf("  ⚠️  This will DELETE Dolt database %q", database)
		if deleteTarget != "" {
			fmt.Printf(" in %s", deleteTarget)
		}
		fmt.Print(". Continue? [y/N]: ")
		confirm, err := repoFingerprintReadLine()
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		confirm = strings.TrimSpace(strings.ToLower(confirm))
		if confirm != "y" && confirm != "yes" {
			fmt.Println("  → Skipped (canceled)")
			return nil
		}

		// Remove database and reinitialize in-process
		fmt.Printf("  → Removing Dolt database %q...\n", database)
		ctx := context.Background()
		if err := dropRuntimeDatabase(ctx, info.Runtime, cfg); err != nil {
			return fmt.Errorf("failed to remove Dolt database: %w", err)
		}

		// Reinitialize and import from JSONL when present.
		fmt.Println("  → Reinitializing database from JSONL...")
		store, err := createDoltStoreForRepoPath(ctx, path)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer func() { _ = store.Close() }()

		jsonlPath := filepath.Join(info.Runtime.BeadsDir, "issues.jsonl")
		if _, statErr := os.Stat(jsonlPath); statErr == nil {
			count, importErr := importJSONLIntoStore(ctx, store, jsonlPath)
			if importErr != nil {
				fmt.Printf("  Warning: failed to import from JSONL: %v\n", importErr)
			} else if count > 0 {
				fmt.Printf("  → Imported %d issues from issues.jsonl\n", count)
			}
		}

		fmt.Println("  ✓ Database reinitialized")
		return nil

	case "s", "":
		fmt.Println("  → Skipped")
		return nil

	default:
		fmt.Printf("  → Unrecognized input '%s', skipping\n", response)
		return nil
	}
}
