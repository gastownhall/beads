package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readLineUnbuffered reads a line from stdin without buffering.
// This ensures subprocess stdin isn't consumed by our buffered reader.
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

// RepoFingerprint fixes repo fingerprint mismatches by prompting the user
// for which action to take. This is interactive because the consequences
// differ significantly between options:
//  1. Update repo ID (if URL changed or bd upgraded)
//  2. Reinitialize database (if wrong database was copied)
//  3. Skip (do nothing)
func RepoFingerprint(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
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
	response, err := readLineUnbuffered()
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))

	switch response {
	case "1":
		// Run bd migrate --update-repo-id
		fmt.Println("  → Running 'bd migrate --update-repo-id'...")
		cmd := newBdCmd(bdBinary, "migrate", "--update-repo-id")
		cmd.Dir = path
		cmd.Stdin = os.Stdin // Allow user to respond to migrate's confirmation prompt
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to update repo ID: %w", err)
		}
		return nil

	case "2":
		// Confirm before destructive action
		fmt.Print("  ⚠️  This will DELETE .beads/beads.db. Continue? [y/N]: ")
		confirm, err := readLineUnbuffered()
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		confirm = strings.TrimSpace(strings.ToLower(confirm))
		if confirm != "y" && confirm != "yes" {
			fmt.Println("  → Skipped (canceled)")
			return nil
		}

		// Remove database and reinitialize
		beadsDir := filepath.Join(path, ".beads")
		dbPath := filepath.Join(beadsDir, "beads.db")

		fmt.Printf("  → Removing %s...\n", dbPath)
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove database: %w", err)
		}

		// Also remove WAL and SHM files if they exist
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")

		fmt.Println("  → Running 'bd init'...")
		cmd := newBdCmd(bdBinary, "init", "--quiet")
		cmd.Dir = path
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
		return nil

	case "s", "":
		fmt.Println("  → Skipped")
		return nil

	default:
		fmt.Printf("  → Unrecognized input '%s', skipping\n", response)
		return nil
	}
}
