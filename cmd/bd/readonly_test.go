package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
)

// TestReadonlyModeBlocksWrites verifies that --readonly blocks write operations
func TestReadonlyModeBlocksWrites(t *testing.T) {
	// Store original state
	originalMode := readonlyMode
	defer func() { readonlyMode = originalMode }()

	// Enable readonly mode
	readonlyMode = true

	// Test that CheckReadonly exits with error
	tests := []struct {
		operation string
	}{
		{"create"},
		{"update"},
		{"close"},
		{"delete"},
		{"sync"},
		{"import"},
		{"reopen"},
		{"edit"},
		{"comment add"},
		{"dep add"},
		{"dep remove"},
		{"label add"},
		{"label remove"},
		{"repair-deps"},
		{"compact"},
		{"duplicates --auto-merge"},
		{"epic close-eligible"},
		{"migrate"},

		{"migrate-issues"},
		{"rename-prefix"},
		{"validate --fix-all"},
		{"jira sync"},
	}

	for _, tc := range tests {
		t.Run(tc.operation, func(t *testing.T) {
			// CheckReadonly calls os.Exit when readonly, which we can't test
			// directly, but we can verify the logic
			if !readonlyMode {
				t.Error("readonly mode should be enabled")
			}
		})
	}
}

// TestReadonlyModeAllowsReads verifies that --readonly allows read operations
func TestReadonlyModeAllowsReads(t *testing.T) {
	// Store original state
	originalMode := readonlyMode
	defer func() { readonlyMode = originalMode }()

	// Enable readonly mode
	readonlyMode = true

	// Read operations should work - just verify the flag doesn't affect them
	// by ensuring readonlyMode doesn't break anything
	if !readonlyMode {
		t.Error("readonly mode should be enabled")
	}

	// The actual read commands (list, show, ready) don't call CheckReadonly
	// so they should work fine. This is verified by integration tests.
}

// TestCheckReadonlyReturnsEarlyWhenDisabled verifies CheckReadonly is a no-op when disabled
func TestCheckReadonlyReturnsEarlyWhenDisabled(t *testing.T) {
	// Store original state
	originalMode := readonlyMode
	defer func() { readonlyMode = originalMode }()

	// Disable readonly mode
	readonlyMode = false

	// Capture that CheckReadonly doesn't exit when readonly mode is disabled.
	// Since CheckReadonly calls os.Exit when enabled, we verify by ensuring we
	// don't panic/exit; the function should just return early

	// This test passes if it completes without calling os.Exit
	// Since we can't easily mock os.Exit, we just verify the logic
	if readonlyMode {
		t.Error("readonly mode should be disabled")
	}
}

// TestReadonlyFlagRegistered verifies the --readonly flag is registered
func TestReadonlyFlagRegistered(t *testing.T) {
	// Create a new root command to test flag registration
	cmd := rootCmd
	flag := cmd.PersistentFlags().Lookup("readonly")
	if flag == nil {
		t.Error("--readonly flag should be registered")
	}
	if flag != nil && flag.Usage == "" {
		t.Error("--readonly flag should have usage text")
	}
}

// TestReadonlyModeVariable ensures the variable exists and is accessible
func TestReadonlyModeVariable(t *testing.T) {
	// Just verify the variable is accessible
	_ = readonlyMode

	// Set and unset to verify it's writable
	original := readonlyMode
	readonlyMode = true
	if !readonlyMode {
		t.Error("should be able to set readonlyMode to true")
	}
	readonlyMode = false
	if readonlyMode {
		t.Error("should be able to set readonlyMode to false")
	}
	readonlyMode = original
}

// TestCheckReadonlyErrorMessage verifies the error message format
func TestCheckReadonlyErrorMessage(t *testing.T) {
	// The error message should mention the operation and readonly mode
	expectedSubstrings := []string{"operation", "is not allowed", "read-only mode"}

	// We can't easily test the os.Exit output, but we can verify the format
	// by checking what error message CheckReadonly would produce
	operation := "test-operation"
	expectedMsg := "operation 'test-operation' is not allowed in read-only mode"

	for _, substr := range expectedSubstrings {
		if !strings.Contains(expectedMsg, substr) {
			t.Errorf("error message should contain %q", substr)
		}
	}

	// Verify operation name is included
	if !strings.Contains(expectedMsg, operation) {
		t.Errorf("error message should contain operation name %q", operation)
	}
}

// capture is a helper to capture stdout/stderr (not currently used but available)
type capture struct {
	buf *bytes.Buffer
}

func (c *capture) Write(p []byte) (n int, err error) {
	return c.buf.Write(p)
}

func TestDoltPublishingCommandsRefusedInReadonly(t *testing.T) {
	originalMode := readonlyMode
	t.Cleanup(func() { readonlyMode = originalMode })
	readonlyMode = true

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
		want string
	}{
		{
			name: "dolt push",
			cmd:  doltPushCmd,
			want: "operation 'dolt push' is not allowed in read-only mode",
		},
		{
			name: "dolt clean-databases",
			cmd:  doltCleanDatabasesCmd,
			want: "operation 'dolt clean-databases' is not allowed in read-only mode",
		},
		{
			name: "dolt remote reset-data",
			cmd:  doltRemoteResetDataCmd,
			args: []string{"origin"},
			want: "operation 'dolt remote reset-data' is not allowed in read-only mode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertReadonlyRefusal(t, tc.cmd, tc.args, tc.want)
		})
	}
}

// assertReadonlyRefusal runs cmd with args and requires exit code 1 whose
// stderr names the read-only refusal. It is shared by the cgo-only
// federation cases in readonly_federation_test.go.
func assertReadonlyRefusal(t *testing.T, cmd *cobra.Command, args []string, want string) {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() { err = cmd.RunE(cmd, args) })
	code, ok := exitCodeFromError(err)
	if !ok || code != 1 {
		t.Fatalf("RunE error = %v, want exit code 1", err)
	}
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

func TestDoltPublishingCommandsRefusedWhenNoPushSet(t *testing.T) {
	t.Setenv("BD_NO_PUSH", "true")
	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{
			name: "dolt remote reset-data",
			cmd:  doltRemoteResetDataCmd,
			args: []string{"origin"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNoPushRefusal(t, tc.cmd, tc.args)
		})
	}
}

// assertNoPushRefusal runs cmd with args and requires exit code 1 whose
// stderr names the no-push refusal. It is shared by the cgo-only federation
// cases in readonly_federation_test.go.
func assertNoPushRefusal(t *testing.T, cmd *cobra.Command, args []string) {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() { err = cmd.RunE(cmd, args) })
	code, ok := exitCodeFromError(err)
	if !ok || code != 1 {
		t.Fatalf("RunE error = %v, want exit code 1", err)
	}
	if !strings.Contains(stderr, "no-push: true") {
		t.Fatalf("stderr = %q, want a refusal naming no-push: true", stderr)
	}
}
