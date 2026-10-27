package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// remoteDanglingRefusal is Dolt's remote-side push refusal as GH#3358
// reported it.
const remoteDanglingRefusal = "unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}"

// remoteDanglingGuidancePhrases are the phrases the guidance pins (P1–P7).
var remoteDanglingGuidancePhrases = []string{
	"Dolt refused to update the remote",
	"did not move the remote branch",
	"same hash",
	"Re-run the same push once",
	"bd dolt remote add <name> <url>",
	"bd dolt push --remote <name>",
	"https://github.com/dolthub/dolt/issues",
	"docs/recovery/sync-failures.md#push-fails-with-found-dangling-references",
}

// failingPushStore is a store with an origin remote whose pushes fail with
// pushErr.
type failingPushStore struct {
	storage.DoltStorage
	pushErr error
}

func (s *failingPushStore) Push(context.Context) error      { return s.pushErr }
func (s *failingPushStore) ForcePush(context.Context) error { return s.pushErr }
func (s *failingPushStore) PushRemote(context.Context, string, bool) error {
	return s.pushErr
}
func (s *failingPushStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return []storage.RemoteInfo{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/o/r"}}, nil
}

// runFailingDoltPush runs `bd dolt push` (with --remote when remote is set)
// against a store whose push fails with pushErr, returning stderr and RunE's
// error.
func runFailingDoltPush(t *testing.T, pushErr error, remote string) (string, error) {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	store = &failingPushStore{pushErr: pushErr}
	for name, value := range map[string]string{"force": "false", "yes": "false", "no-adopt": "false", "remote": remote} {
		if err := doltPushCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	t.Cleanup(func() { _ = doltPushCmd.Flags().Set("remote", "") })
	var runErr error
	_, stderr := captureStdio(t, func() { runErr = doltPushCmd.RunE(doltPushCmd, nil) })
	return stderr, runErr
}

func requireSilentExit(t *testing.T, err error, stderr string) {
	t.Helper()
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("RunE = %v, want SilentExit (exit 1)\n%s", err, stderr)
	}
}

// TestDoltPushPrintsRemoteDanglingReferenceGuidance: Dolt's remote-side
// "Found dangling references" refusal (GH#3358) keeps its Error: line and is
// followed by guidance on what failed and how to recover, not a bare exit 1.
func TestDoltPushPrintsRemoteDanglingReferenceGuidance(t *testing.T) {
	t.Run("default remote, sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: remoteDanglingRefusal}
		stderr, err := runFailingDoltPush(t, fmt.Errorf("failed to push to origin/main: %w", sqlErr), "")
		requireSilentExit(t, err, stderr)
		errLine := "Error: failed to push to origin/main: Error 1105 (HY000): unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}\n"
		if !strings.HasPrefix(stderr, errLine) {
			t.Errorf("stderr does not start with the unchanged Error: line %q:\n%s", errLine, stderr)
		}
		for _, want := range remoteDanglingGuidancePhrases {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		for _, unwanted := range []string{"histories have diverged", "different primary keys"} {
			if strings.Contains(stderr, unwanted) {
				t.Errorf("stderr has %q:\n%s", unwanted, stderr)
			}
		}
	})

	t.Run("named remote, cli route", func(t *testing.T) {
		pushErr := fmt.Errorf("dolt push failed: %s: %w", "Uploading...\n"+remoteDanglingRefusal, errors.New("exit status 1"))
		stderr, err := runFailingDoltPush(t, pushErr, "backup")
		requireSilentExit(t, err, stderr)
		if errLine := "Error: " + pushErr.Error() + "\n"; !strings.HasPrefix(stderr, errLine) {
			t.Errorf("stderr does not start with the unchanged Error: line %q:\n%s", errLine, stderr)
		}
		for _, want := range remoteDanglingGuidancePhrases {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		if unwanted := `Remote "backup" is not configured.`; strings.Contains(stderr, unwanted) {
			t.Errorf("stderr has %q:\n%s", unwanted, stderr)
		}
	})
}
