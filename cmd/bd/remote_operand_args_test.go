package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
)

// TestRemoteOperandArgs pins AC-5 (GH#7876): every CLI surface that hands a
// remote or peer name to Dolt refuses an option-shaped name (one starting
// with '-') in its cobra Args validation, which runs before PersistentPreRunE
// opens the store, so `--remote=--force` can never reach DOLT_PUSH, DOLT_PULL
// or a `dolt push` argv. The control rows keep numeric-leading names,
// ordinary names and omitted defaults valid.
func TestRemoteOperandArgs(t *testing.T) {
	// Cannot be parallel: mutates the shared command flags and config.
	tests := []struct {
		name  string
		cmd   *cobra.Command
		args  []string
		flags map[string]string
		// wantRefused is the remote name the refusal must quote; "" means
		// validation must pass.
		wantRefused string
	}{
		{name: "push/flag", cmd: doltPushCmd, flags: map[string]string{"remote": "--force"}, wantRefused: "--force"},
		{name: "push/positional_after_dashdash", cmd: doltPushCmd, args: []string{"--force"}, wantRefused: "--force"},
		{name: "push/positional_single_dash", cmd: doltPushCmd, args: []string{"-"}, wantRefused: "-"},
		{name: "push/positional_short", cmd: doltPushCmd, args: []string{"-f"}, wantRefused: "-f"},
		{name: "push/both_equal", cmd: doltPushCmd, args: []string{"-f"}, flags: map[string]string{"remote": "-f"}, wantRefused: "-f"},
		{name: "pull/flag", cmd: doltPullCmd, flags: map[string]string{"remote": "--force"}, wantRefused: "--force"},
		{name: "pull/positional", cmd: doltPullCmd, args: []string{"--no-ff"}, wantRefused: "--no-ff"},
		{name: "sync/remote_flag", cmd: syncCmd, flags: map[string]string{"remote": "--force"}, wantRefused: "--force"},
		{name: "federation_sync/peer_flag", cmd: federationSyncCmd, flags: map[string]string{"peer": "--force"}, wantRefused: "--force"},
		{name: "remote_add/name", cmd: doltRemoteAddCmd, args: []string{"--force", "file:///tmp/remote"}, wantRefused: "--force"},
		{name: "remote_remove/name", cmd: doltRemoteRemoveCmd, args: []string{"-f"}, wantRefused: "-f"},

		{name: "control/push_default", cmd: doltPushCmd},
		{name: "control/push_numeric_leading", cmd: doltPushCmd, args: []string{"1st"}},
		{name: "control/push_flag_ordinary", cmd: doltPushCmd, flags: map[string]string{"remote": "origin"}},
		{name: "control/pull_numeric_leading", cmd: doltPullCmd, args: []string{"1st"}},
		{name: "control/sync_default", cmd: syncCmd},
		{name: "control/sync_named", cmd: syncCmd, flags: map[string]string{"remote": "1st"}},
		{name: "control/federation_sync_all", cmd: federationSyncCmd},
		{name: "control/federation_sync_peer", cmd: federationSyncCmd, flags: map[string]string{"peer": "town-beta"}},
		{name: "control/remote_add", cmd: doltRemoteAddCmd, args: []string{"1st", "file:///tmp/remote"}},
		{name: "control/remote_remove", cmd: doltRemoteRemoveCmd, args: []string{"origin"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveAndRestoreGlobals(t)
			resetCommandContext()
			resetCommandFlags(tt.cmd)
			t.Cleanup(func() { resetCommandFlags(tt.cmd) })
			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			for name, value := range tt.flags {
				if err := tt.cmd.Flags().Set(name, value); err != nil {
					t.Fatalf("set --%s=%s: %v", name, value, err)
				}
			}

			var validateErr error
			stderr := captureStderr(t, func() { validateErr = tt.cmd.ValidateArgs(tt.args) })
			if tt.wantRefused == "" {
				if validateErr != nil {
					t.Fatalf("ValidateArgs(%q) = %v (stderr %q), want nil", tt.args, validateErr, stderr)
				}
				return
			}
			if validateErr == nil {
				t.Fatalf("ValidateArgs(%q) = nil, want the leading-dash refusal", tt.args)
			}
			want := `Error: invalid remote name "` + tt.wantRefused + `": must not start with '-'`
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		})
	}
}

// TestRemoteOperandRefusedBeforeStoreOpen runs the bd binary from a directory
// with no workspace. A name that passed Args validation would reach
// PersistentPreRunE and fail on the missing database instead, so the exact
// refusal on stderr proves the store was never opened.
func TestRemoteOperandRefusedBeforeStoreOpen(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"dolt", "push", "--remote=--force"},
		{"dolt", "push", "--", "--force"},
		{"dolt", "pull", "--", "-f"},
		{"sync", "--remote=--force"},
		{"federation", "sync", "--peer=--force"},
		{"dolt", "remote", "add", "--", "--force", "file:///tmp/remote"},
		{"dolt", "remote", "remove", "--", "-f"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, `invalid remote name "`) || !strings.Contains(stderr, `must not start with '-'`) {
				t.Errorf("stderr is not the leading-dash refusal (the command got past Args validation):\n%s", stderr)
			}
		})
	}
}
