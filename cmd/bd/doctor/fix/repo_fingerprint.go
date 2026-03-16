package fix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
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
		// Detect backend to determine what to remove
		cfg, cfgErr := configfile.Load(beadsDir)
		if cfgErr != nil || cfg == nil {
			cfg = configfile.DefaultConfig()
		}
		dbPath := cfg.DatabasePath(beadsDir)
		isDolt := cfg.GetBackend() == configfile.BackendDolt

		// Confirm before destructive action
		fmt.Printf("  ⚠️  This will DELETE %s. Continue? [y/N]: ", dbPath)
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
		fmt.Printf("  → Removing %s...\n", dbPath)
		if isDolt {
			if err := os.RemoveAll(dbPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove Dolt database: %w", err)
			}
		} else {
			if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove database: %w", err)
			}
			_ = os.Remove(dbPath + "-wal")
			_ = os.Remove(dbPath + "-shm")
		}

		// Reinitialize by creating a new store (auto-bootstraps from JSONL)
		fmt.Println("  → Reinitializing database from JSONL...")
		ctx := context.Background()
		store, err := createDoltStoreForRepoPath(ctx, path)
		if err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		defer func() { _ = store.Close() }()

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
