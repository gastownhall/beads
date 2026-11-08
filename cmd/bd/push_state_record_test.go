package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// staleFailedPushState is push-state.json as a failed auto-push leaves it: an
// old success and a recorded failure, both long out of the debounce window.
func staleFailedPushState() pushState {
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	return pushState{LastPush: old, LastCommit: "c0", LastAttempt: old, LastError: "timed out after 30s"}
}

// pushStateWorkspace builds a temp workspace whose .beads/config.yaml holds
// configYAML, points BEADS_DIR and the user-level config roots at temp
// locations, seeds push-state.json with seed, and (re)initializes config.
func pushStateWorkspace(t *testing.T, configYAML string, seed pushState) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"metadata.json": `{}`,
		"config.yaml":   configYAML,
	} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DIR", beadsDir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, key := range []string{"BD_NO_PUSH", "BD_DOLT_LOCAL_ONLY", "BD_DOLT_AUTO_PUSH", "BD_NO_REMOTE_ADOPT"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if err := savePushState(&seed); err != nil {
		t.Fatalf("savePushState: %v", err)
	}
	return beadsDir
}

// assertPushStateRecordedSuccess checks push-state.json records a success at
// a time in [before, after] that published wantCommit and cleared last_error.
func assertPushStateRecordedSuccess(t *testing.T, beadsDir string, before, after time.Time, wantCommit string) {
	t.Helper()
	got := readPushStateMap(t, beadsDir)
	for _, key := range []string{"last_push", "last_attempt"} {
		v, _ := got[key].(string)
		at, err := time.Parse(time.RFC3339, v)
		if err != nil || at.Before(before.Truncate(time.Second)) || at.After(after) {
			t.Errorf("%s = %q, want this successful push's time; state %v", key, v, got)
		}
	}
	if got["last_commit"] != wantCommit {
		t.Errorf("last_commit = %v, want %q; state %v", got["last_commit"], wantCommit, got)
	}
	if v, ok := got["last_error"]; ok {
		t.Errorf("last_error = %v, want it cleared by the successful push", v)
	}
}

// assertPushStateUnchanged checks push-state.json still holds want exactly.
func assertPushStateUnchanged(t *testing.T, beadsDir string, want pushState) {
	t.Helper()
	got := readPushStateMap(t, beadsDir)
	wantMap := map[string]any{
		"last_push":    want.LastPush,
		"last_commit":  want.LastCommit,
		"last_attempt": want.LastAttempt,
		"last_error":   want.LastError,
	}
	if len(got) != len(wantMap) {
		t.Errorf("push-state.json = %v, want unchanged %v", got, wantMap)
		return
	}
	for k, v := range wantMap {
		if got[k] != v {
			t.Errorf("push-state.json = %v, want unchanged %v", got, wantMap)
			return
		}
	}
}

// pushStateFakeStore is a storage.DoltStorage stand-in for bd dolt push and
// bd sync: a successful push moves HEAD from head to afterPush (as the real
// Push's pre-push commit does), and every push entry point is recorded.
type pushStateFakeStore struct {
	storage.DoltStorage
	head, afterPush string
	pushErr         error
	remotes         []storage.RemoteInfo
	pushed          bool
	calls           []string
}

func (f *pushStateFakeStore) GetCurrentCommit(context.Context) (string, error) {
	if f.pushed {
		return f.afterPush, nil
	}
	return f.head, nil
}

func (f *pushStateFakeStore) push(name string) error {
	f.calls = append(f.calls, name)
	if f.pushErr != nil {
		return f.pushErr
	}
	f.pushed = true
	return nil
}

func (f *pushStateFakeStore) Push(context.Context) error      { return f.push("Push") }
func (f *pushStateFakeStore) ForcePush(context.Context) error { return f.push("ForcePush") }
func (f *pushStateFakeStore) PushRemote(_ context.Context, remote string, force bool) error {
	if force {
		return f.push("PushRemote(" + remote + ",force)")
	}
	return f.push("PushRemote(" + remote + ")")
}
func (f *pushStateFakeStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return f.remotes, nil
}

func pushStateTestRemotes() []storage.RemoteInfo {
	return []storage.RemoteInfo{
		{Name: "origin", URL: "file:///tmp/push-state-origin"},
		{Name: "backup", URL: "file:///tmp/push-state-backup"},
	}
}

// TestDoltPushRecordsPushState pins AC-4 of GH#6289: a successful bd dolt
// push to the default remote or to origin, with or without --force, records
// success in push-state.json in the command body (so server and embedded
// mode agree), while a push to another remote, a failed push, and a skipped
// or remote-less push leave the file exactly as it was.
func TestDoltPushRecordsPushState(t *testing.T) {
	// Cannot be parallel: mutates env vars, config, the store global and
	// doltPushCmd's flags.
	tests := []struct {
		name      string
		config    string
		args      []string
		flags     map[string]string
		remotes   []storage.RemoteInfo
		pushErr   error
		wantCalls string
		wantErr   bool
		recorded  bool
	}{
		{name: "default remote", wantCalls: "Push", recorded: true},
		{name: "default remote --force", flags: map[string]string{"force": "true"}, wantCalls: "ForcePush", recorded: true},
		{name: "positional origin", args: []string{"origin"}, wantCalls: "PushRemote(origin)", recorded: true},
		{name: "--remote origin --force", flags: map[string]string{"remote": "origin", "force": "true"}, wantCalls: "PushRemote(origin,force)", recorded: true},
		{name: "backup remote", args: []string{"backup"}, wantCalls: "PushRemote(backup)"},
		{name: "default push fails", pushErr: errors.New("rejected: non-fast-forward"), wantCalls: "Push", wantErr: true},
		{name: "origin push fails", args: []string{"origin"}, pushErr: errors.New("rejected: non-fast-forward"), wantCalls: "PushRemote(origin)", wantErr: true},
		{name: "no-push", config: "no-push: true\n"},
		{name: "dolt.local-only", config: "dolt:\n  local-only: true\n"},
		{
			name:      "no remote configured",
			flags:     map[string]string{"no-adopt": "true"},
			remotes:   []storage.RemoteInfo{},
			pushErr:   errors.New("remote 'origin' not found"),
			wantCalls: "Push",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveAndRestoreGlobals(t)
			resetCommandContext()
			resetCommandFlags(doltPushCmd)
			t.Cleanup(func() { resetCommandFlags(doltPushCmd) })

			seed := staleFailedPushState()
			beadsDir := pushStateWorkspace(t, tt.config, seed)
			remotes := tt.remotes
			if remotes == nil {
				remotes = pushStateTestRemotes()
			}
			fake := &pushStateFakeStore{head: "c1", afterPush: "c2", pushErr: tt.pushErr, remotes: remotes}
			store = fake
			for name, value := range tt.flags {
				if err := doltPushCmd.Flags().Set(name, value); err != nil {
					t.Fatalf("set --%s=%s: %v", name, value, err)
				}
			}

			before := time.Now().UTC()
			var runErr error
			_, stderr := captureStdio(t, func() { runErr = doltPushCmd.RunE(doltPushCmd, tt.args) })
			after := time.Now().UTC()

			if got := strings.Join(fake.calls, ","); got != tt.wantCalls {
				t.Fatalf("store push calls = %q, want %q (stderr %q)", got, tt.wantCalls, stderr)
			}
			if (runErr != nil) != tt.wantErr {
				t.Fatalf("RunE error = %v, want error=%v (stderr %q)", runErr, tt.wantErr, stderr)
			}
			if tt.recorded {
				assertPushStateRecordedSuccess(t, beadsDir, before, after, "c2")
			} else {
				assertPushStateUnchanged(t, beadsDir, seed)
			}
		})
	}

	// The push already succeeded, so failing to write push-state.json must
	// warn without turning the command into a failure.
	t.Run("save failure keeps the push successful", func(t *testing.T) {
		saveAndRestoreGlobals(t)
		resetCommandContext()
		resetCommandFlags(doltPushCmd)
		t.Cleanup(func() { resetCommandFlags(doltPushCmd) })

		beadsDir := pushStateWorkspace(t, "", staleFailedPushState())
		statePath := filepath.Join(beadsDir, "push-state.json")
		if err := os.Remove(statePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(statePath, 0o700); err != nil {
			t.Fatal(err)
		}
		fake := &pushStateFakeStore{head: "c1", afterPush: "c2", remotes: pushStateTestRemotes()}
		store = fake

		var runErr error
		stdout, stderr := captureStdio(t, func() { runErr = doltPushCmd.RunE(doltPushCmd, nil) })
		if runErr != nil {
			t.Fatalf("RunE error = %v, want nil: the push succeeded (stderr %q)", runErr, stderr)
		}
		if !strings.Contains(stdout, "Push complete.") {
			t.Errorf("stdout = %q, want it to report the completed push", stdout)
		}
		if !strings.Contains(stderr, "push-state.json was not updated") {
			t.Errorf("stderr = %q, want a warning that push-state.json was not updated", stderr)
		}
	})
}
