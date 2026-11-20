//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
)

// TestSyncGitUsageGuidance: bd sync pushes too, so the Git usage refusal its
// loop reports after the Error: line gets the same guidance as bd dolt push —
// and JSON mode keeps suppressing stderr guidance, as it does for every
// other failure.
func TestSyncGitUsageGuidance(t *testing.T) {
	t.Run("default remote, sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: sqlGitUsageHints}
		pushErr := fmt.Errorf("failed to push to origin/main: %w", sqlErr)
		stderr, err := runFailingSync(t, pushErr, "")
		requireSilentExit(t, err, stderr)
		requireGitUsageGuidance(t, stderr, "Error: sync failed: push: "+pushErr.Error()+"\n", "bd sync")
	})

	t.Run("named remote, cli route", func(t *testing.T) {
		pushErr := fmt.Errorf("dolt push failed: %s: %w", cliGitUsageLease, errors.New("exit status 129"))
		stderr, err := runFailingSync(t, pushErr, "backup")
		requireSilentExit(t, err, stderr)
		requireGitUsageGuidance(t, stderr, "Error: sync failed: push: "+pushErr.Error()+"\n", "bd sync --remote backup")
	})

	t.Run("json output keeps stderr guidance suppressed", func(t *testing.T) {
		pushErr := errors.New(sqlGitUsageHints)
		fake := &fakeSyncStore{pushErr: pushErr, pushRemoteErr: pushErr}
		setupSyncCommandTest(t, fake)
		jsonOutput = true
		var runErr error
		_, stderr := captureStdio(t, func() { runErr = runSyncCommand(syncCmd, nil) })
		if runErr == nil {
			t.Fatalf("runSyncCommand = nil, want failure\n%s", stderr)
		}
		if strings.Contains(stderr, "Git rejected the arguments") {
			t.Errorf("json run printed stderr guidance:\n%s", stderr)
		}
	})
}

// TestAutoPushGitUsageGuidance: the visible dolt auto-push warning path adds
// the same guidance after its warning, and keeps it inside the
// quiet/JSON suppression the warning already has.
func TestAutoPushGitUsageGuidance(t *testing.T) {
	t.Run("sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: sqlGitUsageHints}
		pushErr := fmt.Errorf("failed to push to origin/main: %w", sqlErr)
		stderr := runFailingAutoPush(t, pushErr)
		requireGitUsageGuidance(t, stderr, "Warning: dolt auto-push failed: "+pushErr.Error()+"\n", "bd dolt push")
	})

	t.Run("quiet suppresses the warning and guidance", func(t *testing.T) {
		pushErr := errors.New(sqlGitUsageHints)
		stderr := runFailingAutoPushQuiet(t, pushErr)
		if strings.Contains(stderr, "Git rejected the arguments") {
			t.Errorf("quiet auto-push printed guidance:\n%s", stderr)
		}
		if strings.Contains(stderr, "Warning: dolt auto-push failed") {
			t.Errorf("quiet auto-push printed its warning:\n%s", stderr)
		}
	})
}

// runFailingAutoPushQuiet runs dolt auto-push, enabled and quiet, in embedded
// mode against a store whose push fails with pushErr, and returns stderr —
// the same environment runFailingAutoPush builds, with the quiet flag set.
func runFailingAutoPushQuiet(t *testing.T, pushErr error) string {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	pinStorageMode(t, false)
	oldJSON, oldQuiet := jsonOutput, quietFlag
	t.Cleanup(func() { jsonOutput, quietFlag = oldJSON, oldQuiet })
	jsonOutput, quietFlag = false, true
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_DOLT_AUTO_PUSH", "true")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	store = &failingPushStore{pushErr: pushErr}
	_, stderr := captureStdio(t, func() { maybeAutoPush(context.Background()) })
	return stderr
}
