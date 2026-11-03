package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

const createForceHint = " (use --force to override)"

// TestCreateRefusalTextNamesForceForPrefixMismatch pins the --force hint on a
// create role's prefix refusal (GH#7965), and that the direct route's reporter
// is what prints it in both output modes. Other refusals keep their text.
func TestCreateRefusalTextNamesForceForPrefixMismatch(t *testing.T) {
	prefixErr := fmt.Errorf("validation failed: prefix validation failed for export-work: %w: issue ID export-work does not match configured prefix walkthrough", storage.ErrPrefixMismatch)

	t.Run("text", func(t *testing.T) {
		if got, want := createRefusalText(prefixErr), prefixErr.Error()+createForceHint; got != want {
			t.Errorf("createRefusalText(prefix mismatch) = %q, want %q", got, want)
		}
		for _, err := range []error{
			fmt.Errorf("create: %w", storage.ErrAlreadyExists),
			errors.New("dependency target does not exist"),
		} {
			if got := createRefusalText(err); got != err.Error() {
				t.Errorf("createRefusalText(%v) = %q, want the error's own text", err, got)
			}
		}
	})

	t.Run("direct reporter", func(t *testing.T) {
		saved := jsonOutput
		t.Cleanup(func() { jsonOutput = saved })

		jsonOutput = false
		var reportErr error
		stderr := captureStderr(t, func() { reportErr = reportCreateRoleError(prefixErr, "") })
		if reportErr == nil {
			t.Fatal("reportCreateRoleError returned nil for a prefix refusal")
		}
		if !strings.Contains(stderr, "does not match configured prefix walkthrough"+createForceHint) {
			t.Errorf("text-mode stderr = %q, want the refusal with the --force hint", stderr)
		}

		jsonOutput = true
		stdout, reportErr := captureStdoutWithError(t, func() error { return reportCreateRoleError(prefixErr, "") })
		if reportErr == nil {
			t.Fatal("reportCreateRoleError returned nil for a prefix refusal in JSON mode")
		}
		if !strings.Contains(stdout, `"error"`) || !strings.Contains(stdout, "walkthrough"+createForceHint) {
			t.Errorf("JSON-mode stdout = %q, want an error object carrying the --force hint", stdout)
		}

		other := errors.New("dependency target does not exist")
		stdout, reportErr = captureStdoutWithError(t, func() error { return reportCreateRoleError(other, "") })
		if reportErr == nil {
			t.Fatal("reportCreateRoleError returned nil for an unrelated refusal")
		}
		if strings.Contains(stdout, "--force") || !strings.Contains(stdout, other.Error()) {
			t.Errorf("JSON-mode stdout for an unrelated refusal = %q, want its own text and no hint", stdout)
		}
	})
}
