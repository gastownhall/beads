package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

const createForceHint = " (use --force to override)"

// prefixRefusalOps is an issueops.Lifecycle whose Create always answers the
// wrapped prefix-mismatch refusal the direct route renders. Reaching the
// route's role-error branch requires it: the route's own explicit-ID precheck
// answers a foreign unforced --id before the role ever sees it, so only a
// role-side refusal exercises this render.
type prefixRefusalOps struct{ issueops.Lifecycle }

func (prefixRefusalOps) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	return issueops.CreateResult{}, fmt.Errorf("validation failed: prefix validation failed for ref-1: %w", storage.ErrPrefixMismatch)
}

// TestCreateRefusalTextNamesForceForPrefixMismatch pins the --force hint on a
// create role's prefix refusal (GH#7965), and that the direct route's reporter
// is what prints it in both output modes. Other refusals keep their text. The
// command arm drives createCmd itself, with the role stubbed through the
// newIssueOperations seam, so the reporter is proven wired into the route.
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

	t.Run("command", func(t *testing.T) {
		saveAndRestoreGlobals(t)
		ensureCleanGlobalState(t)
		savedRootCtx, savedJSONOutput, savedOps := rootCtx, jsonOutput, newIssueOperations
		t.Cleanup(func() { rootCtx, jsonOutput, newIssueOperations = savedRootCtx, savedJSONOutput, savedOps })

		jsonOutput = false
		newIssueOperations = func(beads.Storage) (issueops.Lifecycle, error) {
			return prefixRefusalOps{}, nil
		}

		var err error
		stderr := captureStderr(t, func() {
			err = createCmd.RunE(createCmd, []string{"Refused child"})
		})
		if err == nil {
			t.Fatal("create over a prefix-refusing role should exit nonzero")
		}
		if !strings.Contains(stderr, createForceHint) {
			t.Errorf("direct route role refusal = %q, want it to name --force", stderr)
		}
	})
}
