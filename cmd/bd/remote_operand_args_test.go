// Tests for the CLI half of the leading-dash remote-operand rejection
// (GH#7876 item 4): a remote or peer name that begins with '-' must be
// refused by argument validation, which Cobra runs before the root
// PersistentPreRunE opens a store — so the refusal cannot depend on any
// workspace or backend existing.
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// wantInvalidRemoteName is the exact refusal every command-level validation
// must produce for an option-shaped remote operand. The CLI validators print
// it through HandleError and return an opaque exit error, so in-process
// assertions read it from captured stderr.
const wantInvalidRemoteName = `invalid remote name "--force": must not start with '-'`

// refuseInvalidRemoteOperand runs fn, which must invoke a CLI remote-operand
// validator, and asserts it both refused (non-nil error) and spelled the
// refusal exactly on stderr.
func refuseInvalidRemoteOperand(t *testing.T, fn func() error) {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() {
		err = fn()
	})
	if err == nil {
		t.Fatal("validation accepted an option-shaped remote name; want the leading-dash refusal")
	}
	if want := "Error: " + wantInvalidRemoteName + "\n"; stderr != want {
		t.Fatalf("stderr is not exactly the leading-dash refusal\ngot:  %q\nwant: %q", stderr, want)
	}
}

// TestRemoteFromArgsRejectsLeadingDash covers the shared resolver behind
// `bd dolt push` and `bd dolt pull`: both the --remote flag spelling and a
// positional operand (as after `--`) are refused, while the existing
// empty/duplicate diagnostics and valid names keep their behavior.
func TestRemoteFromArgsRejectsLeadingDash(t *testing.T) {
	// Cannot be parallel: mutates the global command's flag values.
	t.Cleanup(func() {
		_ = doltPushCmd.Flags().Set("remote", "")
	})

	flagCases := []struct {
		name string
		args []string
	}{
		{name: "flag_only", args: nil},
		{name: "flag_with_positional_after_dashdash", args: []string{"--force"}},
	}
	for _, tc := range flagCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := doltPushCmd.Flags().Set("remote", "--force"); err != nil {
				t.Fatalf("setting --remote flag: %v", err)
			}
			refuseInvalidRemoteOperand(t, func() error {
				_, err := remoteFromArgs(doltPushCmd, tc.args)
				return err
			})
		})
	}

	t.Run("positional_only_after_dashdash", func(t *testing.T) {
		if err := doltPushCmd.Flags().Set("remote", ""); err != nil {
			t.Fatalf("clearing --remote flag: %v", err)
		}
		refuseInvalidRemoteOperand(t, func() error {
			_, err := remoteFromArgs(doltPushCmd, []string{"--force"})
			return err
		})
	})

	t.Run("valid_names_keep_existing_behavior", func(t *testing.T) {
		if err := doltPushCmd.Flags().Set("remote", ""); err != nil {
			t.Fatalf("clearing --remote flag: %v", err)
		}
		for _, args := range [][]string{{}, {"origin"}, {"1st"}} {
			remote, err := remoteFromArgs(doltPushCmd, args)
			if err != nil {
				t.Fatalf("remoteFromArgs(%q) = %v, want nil", args, err)
			}
			want := ""
			if len(args) > 0 {
				want = args[0]
			}
			if remote != want {
				t.Fatalf("remoteFromArgs(%q) = %q, want %q", args, remote, want)
			}
		}
	})

	t.Run("empty_name_diagnostic_unchanged", func(t *testing.T) {
		stderr := captureStderr(t, func() {
			_, err := remoteFromArgs(doltPushCmd, []string{"  "})
			if err == nil {
				t.Fatal("remoteFromArgs(whitespace) accepted an empty name")
			}
		})
		if !strings.Contains(stderr, "remote name is empty") {
			t.Fatalf("stderr = %q, want the existing empty-name diagnostic", stderr)
		}
	})
}

// TestRemoteOperandArgs covers every command whose argument validation now
// rejects option-shaped remote or peer names: push/pull positionals, sync's
// --remote, federation sync's --peer, and remote add/remove names.
func TestRemoteOperandArgs(t *testing.T) {
	// Cannot be parallel: mutates the global commands' flag values.
	t.Cleanup(func() {
		_ = syncCmd.Flags().Set("remote", "")
		_ = federationSyncCmd.Flags().Set("peer", "")
	})

	setFlag := func(t *testing.T, cmd *cobra.Command, name, value string) {
		t.Helper()
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("setting --%s flag: %v", name, err)
		}
	}

	cases := []struct {
		name     string
		validate func(*testing.T) error
	}{
		{name: "dolt_push_args", validate: func(t *testing.T) error {
			return doltPushCmd.Args(doltPushCmd, []string{"--force"})
		}},
		{name: "dolt_pull_args", validate: func(t *testing.T) error {
			return doltPullCmd.Args(doltPullCmd, []string{"--force"})
		}},
		{name: "sync_remote_flag", validate: func(t *testing.T) error {
			setFlag(t, syncCmd, "remote", "--force")
			return syncCmd.Args(syncCmd, nil)
		}},
		{name: "federation_peer_flag", validate: func(t *testing.T) error {
			setFlag(t, federationSyncCmd, "peer", "--force")
			return federationSyncCmd.Args(federationSyncCmd, nil)
		}},
		{name: "remote_add_name", validate: func(t *testing.T) error {
			return doltRemoteAddCmd.Args(doltRemoteAddCmd, []string{"--force", "file:///tmp/remote"})
		}},
		{name: "remote_remove_name", validate: func(t *testing.T) error {
			return doltRemoteRemoveCmd.Args(doltRemoteRemoveCmd, []string{"--force"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refuseInvalidRemoteOperand(t, func() error {
				return tc.validate(t)
			})
		})
	}

	t.Run("remote_add_valid_name_still_passes", func(t *testing.T) {
		if err := doltRemoteAddCmd.Args(doltRemoteAddCmd, []string{"1st", "file:///tmp/remote"}); err != nil {
			t.Fatalf("remote add with a numeric-leading name was refused: %v", err)
		}
	})
}

// TestRemoteOperandCLIRefusedBeforeStoreOpen proves the ordering claim: the
// refusal comes from Cobra's Args validation, which runs before the root
// PersistentPreRunE selects a workspace or opens a store. Run from a directory
// with no workspace at all, the invalid-name refusal must win over the
// workspace-not-found error the pre-run hook would produce.
func TestRemoteOperandCLIRefusedBeforeStoreOpen(t *testing.T) {
	bd := buildBDForInitTests(t)
	dir := t.TempDir()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "dolt_push_remote_flag", args: []string{"dolt", "push", "--remote=--force"}},
		{name: "dolt_push_positional", args: []string{"dolt", "push", "--", "--force"}},
		{name: "dolt_pull_remote_flag", args: []string{"dolt", "pull", "--remote=--force"}},
		{name: "dolt_pull_positional", args: []string{"dolt", "pull", "--", "--force"}},
		{name: "sync_remote_flag", args: []string{"sync", "--remote=--force"}},
		{name: "federation_peer_flag", args: []string{"federation", "sync", "--peer=--force"}},
		// Positional names only reach Args validation after `--`: a raw
		// option-shaped argument is an unknown flag, not a positional name
		// (see the test plan note for GH#7876).
		{name: "remote_add_name", args: []string{"dolt", "remote", "add", "--", "--force", "file:///tmp/remote"}},
		{name: "remote_remove_name", args: []string{"dolt", "remote", "remove", "--", "--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, tc.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			want := "Error: " + wantInvalidRemoteName + "\n"
			if stderr != want {
				t.Errorf("stderr is not exactly the leading-dash refusal\ngot:  %q\nwant: %q", stderr, want)
			}
		})
	}
}
