//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// remoteDanglingRefusal is Dolt's remote-side push refusal as GH#3358
// reported it.
const remoteDanglingRefusal = "unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}"

// testHeadCommit and testHeadRoot are the HEAD hashes the fake stores report
// in place of DOLT_HASHOF('HEAD') and DOLT_HASHOF_DB('HEAD').
const (
	testHeadCommit = "0123456789abcdefghijklmnopqrstuv"
	testHeadRoot   = "vutsrqponmlkjihgfedcba9876543210"
)

// headHashReader serves storage.HeadHasher for a fake store, as both Dolt
// stores do: the test HEAD hashes, or headErr when it is set.
type headHashReader struct{ headErr error }

func (r headHashReader) GetHeadHashes(context.Context) (string, string, error) {
	if r.headErr != nil {
		return "", "", r.headErr
	}
	return testHeadCommit, testHeadRoot, nil
}

// remoteDanglingGuidancePhrases are the phrases the guidance pins for every
// remote: what failed, this clone's HEAD values (which bd reads through the
// store, so embedded mode, where bd sql does not run, gets them too), the one
// rule for when to reseed (the playbook's), and where to go next.
var remoteDanglingGuidancePhrases = []string{
	"Dolt refused to update the remote",
	"did not move the remote branch",
	"Compare the hash in the error with this clone's HEAD:\n       HEAD commit: " + testHeadCommit + "\n       HEAD root:   " + testHeadRoot + "\n",
	"A hash that equals either value is a transient race",
	"Re-run the push in every case.",
	"Seed a fresh remote only when the push fails again with the same",
	"hash and that hash equals neither value",
	"'bd version' (and of 'dolt version' in",
	"https://github.com/dolthub/dolt/issues",
	"docs/recovery/sync-failures.md#push-fails-with-found-dangling-references",
}

// seedRecipe is the guidance's seeding recipe for a refused push to remote
// ("" for the default remote): origin's, the remote bd sends Hosted Dolt
// credentials to in server mode, or else the named remote's own, which
// leaves origin and sync.remote alone and says how far it reaches.
func seedRecipe(remote string) []string {
	if remote == "" || remote == "origin" {
		return []string{"point origin at it, and push:\n       bd dolt remote remove origin\n       bd dolt remote add origin <url>\n       bd dolt push\n"}
	}
	return []string{
		"point this remote at it, and push:\n       bd dolt remote remove " + remote + "\n       bd dolt remote add " + remote + " <url>\n       bd dolt push --remote " + remote + "\n",
		"In server mode bd sends Hosted Dolt credentials only to origin;",
	}
}

// requireRemoteDanglingGuidance checks that stderr carries the guidance, with
// the seeding recipe for remote, and no other push guidance. The commands run
// in embedded mode, bd's default (pinStorageMode), so it also pins that the
// guidance prints the HEAD values there and no bd sql line.
func requireRemoteDanglingGuidance(t *testing.T, stderr, remote string) {
	t.Helper()
	for _, want := range append(seedRecipe(remote), remoteDanglingGuidancePhrases...) {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	unwanted := []string{"histories have diverged", "different primary keys", "bd sql", "could not read"}
	if remote == "" || remote == "origin" {
		unwanted = append(unwanted, "bd dolt push --remote")
	} else {
		unwanted = append(unwanted, "remove origin", "add origin")
	}
	for _, s := range unwanted {
		if strings.Contains(stderr, s) {
			t.Errorf("stderr has %q:\n%s", s, stderr)
		}
	}
}

// pinStorageMode makes usesSQLServer report server mode when server is true,
// and embedded mode, bd's default, otherwise.
func pinStorageMode(t *testing.T, server bool) {
	t.Helper()
	oldServer, oldProxied := serverMode, proxiedServerMode
	t.Cleanup(func() { serverMode, proxiedServerMode = oldServer, oldProxied })
	serverMode, proxiedServerMode = server, false
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
}

// failingPushStore is a store with an origin remote whose pushes fail with
// pushErr. It reads HEAD as both Dolt stores do.
type failingPushStore struct {
	storage.DoltStorage
	headHashReader
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

// GetCurrentCommit serves auto-push, which reads HEAD before it pushes.
func (s *failingPushStore) GetCurrentCommit(context.Context) (string, error) {
	return "c1", nil
}

// runDoltPush runs `bd dolt push` (with --remote when remote is set) against
// st, in server mode when server is true and embedded mode otherwise,
// returning stderr and RunE's error.
func runDoltPush(t *testing.T, st storage.DoltStorage, remote string, server bool) (string, error) {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	pinStorageMode(t, server)
	store = st
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

// runFailingDoltPush runs `bd dolt push` in embedded mode against a store
// whose push fails with pushErr.
func runFailingDoltPush(t *testing.T, pushErr error, remote string) (string, error) {
	t.Helper()
	return runDoltPush(t, &failingPushStore{pushErr: pushErr}, remote, false)
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
// The seeding commands re-point the remote that failed.
func TestDoltPushPrintsRemoteDanglingReferenceGuidance(t *testing.T) {
	t.Run("default remote, sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: remoteDanglingRefusal}
		stderr, err := runFailingDoltPush(t, fmt.Errorf("failed to push to origin/main: %w", sqlErr), "")
		requireSilentExit(t, err, stderr)
		errLine := "Error: failed to push to origin/main: Error 1105 (HY000): unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}\n"
		if !strings.HasPrefix(stderr, errLine) {
			t.Errorf("stderr does not start with the unchanged Error: line %q:\n%s", errLine, stderr)
		}
		requireRemoteDanglingGuidance(t, stderr, "")
	})

	t.Run("named remote, cli route", func(t *testing.T) {
		pushErr := fmt.Errorf("dolt push failed: %s: %w", "Uploading...\n"+remoteDanglingRefusal, errors.New("exit status 1"))
		stderr, err := runFailingDoltPush(t, pushErr, "backup")
		requireSilentExit(t, err, stderr)
		if errLine := "Error: " + pushErr.Error() + "\n"; !strings.HasPrefix(stderr, errLine) {
			t.Errorf("stderr does not start with the unchanged Error: line %q:\n%s", errLine, stderr)
		}
		requireRemoteDanglingGuidance(t, stderr, "backup")
		if unwanted := `Remote "backup" is not configured.`; strings.Contains(stderr, unwanted) {
			t.Errorf("stderr has %q:\n%s", unwanted, stderr)
		}
	})

	t.Run("origin named with --remote", func(t *testing.T) {
		stderr, err := runFailingDoltPush(t, errors.New(remoteDanglingRefusal), "origin")
		requireSilentExit(t, err, stderr)
		requireRemoteDanglingGuidance(t, stderr, "origin")
	})
}

// TestRemoteDanglingReferenceGuidanceWhenHeadIsUnreadable: when bd cannot
// read HEAD, step 1 says why and prints the bd sql query that reads it only
// in server mode, since bd sql does not run in embedded mode. The rest of the
// guidance is unchanged.
func TestRemoteDanglingReferenceGuidanceWhenHeadIsUnreadable(t *testing.T) {
	const bdSQL = "\n       bd sql \"SELECT dolt_hashof('HEAD'), dolt_hashof_db('HEAD')\"\n"
	for _, tc := range []struct {
		name   string
		server bool
	}{
		{"embedded", false},
		{"server", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &failingPushStore{pushErr: errors.New(remoteDanglingRefusal), headHashReader: headHashReader{headErr: errors.New("engine closed")}}
			stderr, err := runDoltPush(t, st, "", tc.server)
			requireSilentExit(t, err, stderr)
			if want := "  1. Compare the hash in the error with this clone's HEAD commit and\n     root hashes. bd could not read them: engine closed\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
			if got := strings.Contains(stderr, bdSQL); got != tc.server {
				t.Errorf("stderr has the bd sql query = %v, want %v:\n%s", got, tc.server, stderr)
			}
			if strings.Contains(stderr, "HEAD commit:") {
				t.Errorf("stderr names HEAD values bd did not read:\n%s", stderr)
			}
			for _, want := range append(seedRecipe(""), "Re-run the push in every case.") {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// TestRemoteDanglingSeedStepQuotesTheRemote: the named remote goes into each
// seeding command as one shell word, or as a placeholder named escaped after
// the command, never bare.
func TestRemoteDanglingSeedStepQuotesTheRemote(t *testing.T) {
	for _, remote := range []string{"back up", "x;touch pwned", "x\x1b[2Ky"} {
		var out strings.Builder
		printDanglingSeedStep(&out, remote)
		requireNoTerminalControl(t, fmt.Sprintf("printDanglingSeedStep(%q)", remote), out.String())
		arg, note := remedy.CommandArg(runtime.GOOS, remote, "<remote>")
		for _, want := range []string{
			"       bd dolt remote remove " + arg + "\n",
			"       bd dolt remote add " + arg + " <url>\n",
			"       bd dolt push --remote " + arg + note + "\n",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("printDanglingSeedStep(%q) lacks %q:\n%s", remote, want, out.String())
			}
		}
		if strings.Contains(out.String(), "--remote "+remote) {
			t.Errorf("printDanglingSeedStep(%q) pastes the remote bare:\n%s", remote, out.String())
		}
	}
}

// TestDoltPushKeepsOtherFailureOutput: failures that are not the remote's
// dangling-reference refusal keep their output: bd's own pre-push fsck (the
// push never reached the remote, even when fsck's output quotes Dolt's
// phrase), diverged history and the different-primary-keys refusal.
func TestDoltPushKeepsOtherFailureOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		guidance  string
		forbidden []string
	}{
		{
			name: "pre-push fsck",
			err: fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks: %s",
				dolt.ErrDanglingReference, "Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}"),
			forbidden: []string{"histories have diverged", "different primary keys"},
		},
		{
			name:      "diverged history",
			err:       errors.New("failed to push to origin/main: no common ancestor"),
			guidance:  "Local and remote Dolt histories have diverged.",
			forbidden: []string{"different primary keys"},
		},
		{
			name:      "pk mismatch",
			err:       errors.New("error: cannot merge because table dependencies has different primary keys in its common ancestor"),
			guidance:  `Dolt refused to merge: table "dependencies" has different primary keys`,
			forbidden: []string{"histories have diverged"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := runFailingDoltPush(t, tc.err, "")
			requireSilentExit(t, err, stderr)
			errLine := "Error: " + tc.err.Error() + "\n"
			if !strings.HasPrefix(stderr, errLine) {
				t.Errorf("stderr does not start with the unchanged Error: line %q:\n%s", errLine, stderr)
			}
			if tc.guidance == "" && stderr != errLine {
				t.Errorf("stderr = %q, want only the Error: line", stderr)
			}
			if tc.guidance != "" && !strings.Contains(stderr, tc.guidance) {
				t.Errorf("stderr lacks %q:\n%s", tc.guidance, stderr)
			}
			for _, unwanted := range append([]string{"Dolt refused to update the remote"}, tc.forbidden...) {
				if strings.Contains(stderr, unwanted) {
					t.Errorf("stderr has %q:\n%s", unwanted, stderr)
				}
			}
		})
	}
}

// prePushFSCKAbort is bd's own pre-push fsck abort quoting Dolt's phrase: the
// push never reached the remote, so it must not get the remote guidance.
var prePushFSCKAbort = fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks: %s",
	dolt.ErrDanglingReference, "Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}")

// headHashSyncStore is a fakeSyncStore that reads HEAD as both Dolt stores
// do.
type headHashSyncStore struct {
	*fakeSyncStore
	headHashReader
}

// runFailingSync runs `bd sync` (with --remote when remote is set) in
// embedded mode against a store whose pushes fail with pushErr, returning
// stderr and the command's error.
func runFailingSync(t *testing.T, pushErr error, remote string) (string, error) {
	t.Helper()
	fake := &fakeSyncStore{pushErr: pushErr, pushRemoteErr: pushErr}
	setupSyncCommandTest(t, fake)
	pinStorageMode(t, false)
	store = &headHashSyncStore{fakeSyncStore: fake}
	if err := syncCmd.Flags().Set("remote", remote); err != nil {
		t.Fatalf("set --remote: %v", err)
	}
	var runErr error
	_, stderr := captureStdio(t, func() { runErr = runSyncCommand(syncCmd, nil) })
	return stderr, runErr
}

// TestSyncPrintsRemoteDanglingReferenceGuidance: bd sync pushes too, so the
// refusal it reports after its Error: line gets the same guidance as
// bd dolt push, seeding the remote sync pushed to, and bd's pre-push fsck
// abort keeps its bare Error: line.
func TestSyncPrintsRemoteDanglingReferenceGuidance(t *testing.T) {
	t.Run("default remote, sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: remoteDanglingRefusal}
		pushErr := fmt.Errorf("failed to push to origin/main: %w", sqlErr)
		stderr, err := runFailingSync(t, pushErr, "")
		requireSilentExit(t, err, stderr)
		if errLine := "Error: sync failed: push: " + pushErr.Error() + "\n"; !strings.HasPrefix(stderr, errLine) {
			t.Errorf("stderr does not start with the unchanged Error: line %q:\n%s", errLine, stderr)
		}
		requireRemoteDanglingGuidance(t, stderr, "")
	})

	t.Run("named remote, cli route", func(t *testing.T) {
		pushErr := fmt.Errorf("dolt push failed: %s: %w", "Uploading...\n"+remoteDanglingRefusal, errors.New("exit status 1"))
		stderr, err := runFailingSync(t, pushErr, "backup")
		requireSilentExit(t, err, stderr)
		requireRemoteDanglingGuidance(t, stderr, "backup")
		if unwanted := `Remote "backup" is not configured.`; strings.Contains(stderr, unwanted) {
			t.Errorf("stderr has %q:\n%s", unwanted, stderr)
		}
	})

	t.Run("pre-push fsck", func(t *testing.T) {
		stderr, err := runFailingSync(t, prePushFSCKAbort, "")
		requireSilentExit(t, err, stderr)
		if errLine := "Error: sync failed: push: " + prePushFSCKAbort.Error() + "\n"; stderr != errLine {
			t.Errorf("stderr = %q, want only the Error: line %q", stderr, errLine)
		}
	})
}

// runFailingAutoPush runs dolt auto-push, enabled, in embedded mode against a
// store whose push fails with pushErr, and returns stderr.
func runFailingAutoPush(t *testing.T, pushErr error) string {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	pinStorageMode(t, false)
	oldJSON, oldQuiet := jsonOutput, quietFlag
	t.Cleanup(func() { jsonOutput, quietFlag = oldJSON, oldQuiet })
	jsonOutput, quietFlag = false, false
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

// TestAutoPushPrintsRemoteDanglingReferenceGuidance: dolt auto-push, which
// pushes to the default remote, follows its warning with the same guidance as
// bd dolt push, and keeps bd's pre-push fsck abort to the bare warning.
func TestAutoPushPrintsRemoteDanglingReferenceGuidance(t *testing.T) {
	t.Run("sql route", func(t *testing.T) {
		sqlErr := &mysql.MySQLError{Number: 1105, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: remoteDanglingRefusal}
		pushErr := fmt.Errorf("failed to push to origin/main: %w", sqlErr)
		stderr := runFailingAutoPush(t, pushErr)
		if warnLine := "Warning: dolt auto-push failed: " + pushErr.Error() + "\n"; !strings.HasPrefix(stderr, warnLine) {
			t.Errorf("stderr does not start with the unchanged warning %q:\n%s", warnLine, stderr)
		}
		requireRemoteDanglingGuidance(t, stderr, "")
	})

	t.Run("pre-push fsck", func(t *testing.T) {
		stderr := runFailingAutoPush(t, prePushFSCKAbort)
		if warnLine := "Warning: dolt auto-push failed: " + prePushFSCKAbort.Error() + "\n"; stderr != warnLine {
			t.Errorf("stderr = %q, want only the warning %q", stderr, warnLine)
		}
	})
}
