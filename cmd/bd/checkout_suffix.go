package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"golang.org/x/term"
)

const base36Chars = "abcdefghijklmnopqrstuvwxyz0123456789"

// generateCheckoutSuffix returns a random 3-character base36 string
// suitable for use as a checkout_suffix (e.g., "k9x", "a2m").
func generateCheckoutSuffix() (string, error) {
	var b strings.Builder
	for i := 0; i < 3; i++ {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(base36Chars))))
		if err != nil {
			return "", fmt.Errorf("generate checkout suffix: %w", err)
		}
		b.WriteByte(base36Chars[idx.Int64()])
	}
	return b.String(), nil
}

// validateCheckoutSuffix checks that a user-supplied suffix is valid.
// Rules: 1-8 characters, lowercase alphanumeric only.
func validateCheckoutSuffix(suffix string) error {
	suffix = strings.TrimSuffix(suffix, "-")
	if suffix == "" {
		return fmt.Errorf("checkout suffix cannot be empty")
	}
	if len(suffix) > 8 {
		return fmt.Errorf("checkout suffix must be 1-8 characters, got %d", len(suffix))
	}
	matched, _ := regexp.MatchString(`^[a-z0-9]+$`, suffix)
	if !matched {
		return fmt.Errorf("checkout suffix must contain only lowercase letters and numbers: %s", suffix)
	}
	return nil
}

// resolveCheckoutSuffix determines the checkout suffix to apply based on
// the --checkout-suffix flag value and interactivity. Returns "" if no suffix.
//
// Flag semantics:
//   - "" (empty/unset): prompt interactively, or skip in non-interactive mode
//   - "auto": generate a random suffix without prompting
//   - "none": explicitly no suffix
//   - any other value: use as the suffix (validated)
func resolveCheckoutSuffix(flagValue string, nonInteractive bool) (string, error) {
	switch flagValue {
	case "none":
		return "", nil
	case "auto":
		return generateCheckoutSuffix()
	case "":
		// No flag — prompt if interactive, skip otherwise
		if nonInteractive || !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", nil
		}
		return promptCheckoutSuffix()
	default:
		// User-supplied value
		suffix := strings.TrimSuffix(flagValue, "-")
		if err := validateCheckoutSuffix(suffix); err != nil {
			return "", err
		}
		return suffix, nil
	}
}

// promptCheckoutSuffix asks the user whether to use a checkout suffix.
// Uses readLineWithContext for proper context cancellation and clean
// stdin handling, matching the pattern used by the init wizards.
func promptCheckoutSuffix() (string, error) {
	fmt.Fprintf(os.Stderr, "\nIsolate this checkout's issues with a unique suffix?\n")
	fmt.Fprintf(os.Stderr, "  y     = generate random suffix (e.g., \"k9x\")\n")
	fmt.Fprintf(os.Stderr, "  n     = shared namespace (default)\n")
	fmt.Fprintf(os.Stderr, "  <str> = use custom suffix\n")
	fmt.Fprintf(os.Stderr, "Checkout suffix [n]: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := readLineWithContext(getRootContext(), reader, os.Stdin)
	if err != nil {
		if isCanceled(err) {
			exitCanceled()
		}
		return "", fmt.Errorf("read checkout suffix: %w", err)
	}
	line = strings.TrimSpace(line)

	if line == "" || strings.EqualFold(line, "n") || strings.EqualFold(line, "no") {
		return "", nil
	}
	if strings.EqualFold(line, "y") || strings.EqualFold(line, "yes") {
		return generateCheckoutSuffix()
	}

	// Treat as custom suffix
	suffix := strings.TrimSuffix(strings.ToLower(line), "-")
	if err := validateCheckoutSuffix(suffix); err != nil {
		return "", err
	}
	return suffix, nil
}

// computeCheckoutID derives a deterministic 8-hex-char identifier from the
// beads directory path. Delegates to storage.ComputeCheckoutID.
func computeCheckoutID(beadsDir string) string {
	return storage.ComputeCheckoutID(beadsDir)
}

// checkoutSuffixKey returns the config key for a checkout suffix.
func checkoutSuffixKey(checkoutID string) string {
	if checkoutID != "" {
		return "checkout_suffix." + checkoutID
	}
	return "checkout_suffix"
}

// applyCheckoutSuffix sets the checkout_suffix config via a storage.Storage.
// No-op when suffix is empty.
func applyCheckoutSuffix(ctx context.Context, s storage.Storage, suffix, checkoutID string) error {
	if suffix == "" {
		return nil
	}
	if err := s.SetConfig(ctx, checkoutSuffixKey(checkoutID), suffix); err != nil {
		return fmt.Errorf("set checkout suffix: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Checkout suffix set to %q (checkout %s)\n", suffix, checkoutID)
	return nil
}

// applyCheckoutSuffixSQL sets the checkout suffix and commits using a raw
// *sql.DB already scoped to the target database. Used by the embedded-mode
// sync path where opening a second embedded engine would deadlock on the
// exclusive flock.
func applyCheckoutSuffixSQL(ctx context.Context, db *sql.DB, suffix, checkoutID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for checkout suffix: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := issueops.SetConfigInTx(ctx, tx, checkoutSuffixKey(checkoutID), suffix); err != nil {
		return fmt.Errorf("set checkout suffix: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("dolt add: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", "bd bootstrap: set checkout suffix"); err != nil {
		return fmt.Errorf("dolt commit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Checkout suffix set to %q (checkout %s)\n", suffix, checkoutID)
	return nil
}
