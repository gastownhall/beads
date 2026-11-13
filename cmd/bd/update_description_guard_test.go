package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newUpdateDescriptionGuardCmd is update-shaped: it registers
// --clear-description, the capability validateDescriptionUpdate keys the
// update contract on (GH#8149).
func newUpdateDescriptionGuardCmd() *cobra.Command {
	cmd := newCreateDescriptionGuardCmd()
	cmd.Flags().Bool("clear-description", false, "Clear the description")
	return cmd
}

// newCreateDescriptionGuardCmd is create-shaped: the shared description flags
// and the stdin/file opt-in, but no --clear-description.
func newCreateDescriptionGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "test",
		Run: func(cmd *cobra.Command, args []string) {},
	}
	registerCommonIssueFlags(cmd)
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description replacement when reading from stdin or file")
	return cmd
}

func withTestStdin(t *testing.T, content string, fn func()) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}

	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()

	fn()
}

func TestValidateDescriptionUpdateRejectsEmptyStdinWithoutOptIn(t *testing.T) {
	withTestStdin(t, "", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed, flagErr := getDescriptionFlag(cmd)
		if flagErr != nil {
			t.Fatalf("unexpected error: %v", flagErr)
		}
		err := validateDescriptionUpdate(cmd, description, changed)
		if err == nil {
			t.Fatal("expected empty stdin description to be rejected")
		}
		if !strings.Contains(err.Error(), "--allow-empty-description") {
			t.Fatalf("expected opt-in guidance in error, got: %v", err)
		}
	})
}

func TestValidateDescriptionUpdateRejectsEmptyDashShorthandWithoutOptIn(t *testing.T) {
	withTestStdin(t, "", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--description", "-"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed, flagErr := getDescriptionFlag(cmd)
		if flagErr != nil {
			t.Fatalf("unexpected error: %v", flagErr)
		}
		err := validateDescriptionUpdate(cmd, description, changed)
		if err == nil {
			t.Fatal("expected empty dash shorthand description to be rejected")
		}
		if !strings.Contains(err.Error(), "--allow-empty-description") {
			t.Fatalf("expected opt-in guidance in error, got: %v", err)
		}
	})
}

func TestValidateDescriptionUpdateRejectsEmptyBodyFileWithoutOptIn(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.md")
	if err := os.WriteFile(filePath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}

	cmd := newUpdateDescriptionGuardCmd()
	if err := cmd.ParseFlags([]string{"--body-file", filePath}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	description, changed, flagErr := getDescriptionFlag(cmd)
	if flagErr != nil {
		t.Fatalf("unexpected error: %v", flagErr)
	}
	err := validateDescriptionUpdate(cmd, description, changed)
	if err == nil {
		t.Fatal("expected empty body file description to be rejected")
	}
	if !strings.Contains(err.Error(), "--allow-empty-description") {
		t.Fatalf("expected opt-in guidance in error, got: %v", err)
	}
}

func TestValidateDescriptionUpdateAllowsEmptyStdinWithOptIn(t *testing.T) {
	withTestStdin(t, "", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--stdin", "--allow-empty-description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed, flagErr := getDescriptionFlag(cmd)
		if flagErr != nil {
			t.Fatalf("unexpected error: %v", flagErr)
		}
		if err := validateDescriptionUpdate(cmd, description, changed); err != nil {
			t.Fatalf("expected opt-in empty stdin to succeed, got: %v", err)
		}
		if description != "" {
			t.Fatalf("expected empty description, got %q", description)
		}
	})
}

func TestValidateDescriptionUpdateAllowsNonEmptyStdinWithoutOptIn(t *testing.T) {
	withTestStdin(t, "Updated from stdin\n", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed, flagErr := getDescriptionFlag(cmd)
		if flagErr != nil {
			t.Fatalf("unexpected error: %v", flagErr)
		}
		if err := validateDescriptionUpdate(cmd, description, changed); err != nil {
			t.Fatalf("expected non-empty stdin to succeed, got: %v", err)
		}
		if description != "Updated from stdin\n" {
			t.Fatalf("expected stdin content to round-trip, got %q", description)
		}
	})
}

// TestValidateDescriptionUpdateAllowsExplicitInlineEmptyOnCreate pins the
// create half of the shared guard: a command without --clear-description keeps
// accepting an inline empty description (GH#8149 changed update only).
func TestValidateDescriptionUpdateAllowsExplicitInlineEmptyOnCreate(t *testing.T) {
	cmd := newCreateDescriptionGuardCmd()
	if err := cmd.ParseFlags([]string{"--description", ""}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	description, changed, flagErr := getDescriptionFlag(cmd)
	if flagErr != nil {
		t.Fatalf("unexpected error: %v", flagErr)
	}
	if err := validateDescriptionUpdate(cmd, description, changed); err != nil {
		t.Fatalf("expected explicit inline empty description to succeed, got: %v", err)
	}
	if description != "" {
		t.Fatalf("expected empty inline description, got %q", description)
	}
}

// Issue #8149: an inline empty value (e.g. a failed $(...) substitution) must not
// silently wipe the description without an explicit opt-in.
func TestValidateDescriptionUpdateRejectsInlineEmptyWithoutOptIn(t *testing.T) {
	cmd := newUpdateDescriptionGuardCmd()
	if err := cmd.ParseFlags([]string{"--description="}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	description, changed, flagErr := getDescriptionFlag(cmd)
	if flagErr != nil {
		t.Fatalf("unexpected error: %v", flagErr)
	}
	if !changed || description != "" {
		t.Fatalf("expected changed empty description, got changed=%v %q", changed, description)
	}
	if err := validateDescriptionUpdate(cmd, description, changed); err == nil {
		t.Fatal("expected inline empty --description= to be rejected without an explicit opt-in; it would silently wipe existing content (#8149)")
	}
}

// newUpdateFlagsCommand clones the live updateCmd's local flags (shorthands
// and cobra annotations included, so mutual-exclusion groups survive) onto a
// fresh command and parses args, so gatherUpdateInput sees the real update
// flag surface without mutating the shared updateCmd.
func newUpdateFlagsCommand(t *testing.T, args ...string) (*cobra.Command, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "update", Run: func(*cobra.Command, []string) {}}
	updateCmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		fs := cmd.Flags()
		switch f.Value.Type() {
		case "bool":
			fs.BoolP(f.Name, f.Shorthand, f.DefValue == "true", f.Usage)
		case "int":
			n, err := strconv.Atoi(f.DefValue)
			if err != nil {
				t.Fatalf("--%s has non-integer default %q: %v", f.Name, f.DefValue, err)
			}
			fs.IntP(f.Name, f.Shorthand, n, f.Usage)
		case "string":
			fs.StringP(f.Name, f.Shorthand, f.DefValue, f.Usage)
		case "stringSlice":
			fs.StringSliceP(f.Name, f.Shorthand, nil, f.Usage)
		case "stringArray":
			fs.StringArrayP(f.Name, f.Shorthand, nil, f.Usage)
		default:
			t.Fatalf("--%s has unhandled flag type %q", f.Name, f.Value.Type())
		}
		clone := fs.Lookup(f.Name)
		for k, v := range f.Annotations {
			if clone.Annotations == nil {
				clone.Annotations = map[string][]string{}
			}
			clone.Annotations[k] = append([]string(nil), v...)
		}
	})
	return cmd, cmd.ParseFlags(args)
}

// gatherUpdateInputFor parses args against the cloned update flags and runs
// the proxied route's input gathering, returning the error text the user
// would see (HandleError prints it and returns a bare exit error).
func gatherUpdateInputFor(t *testing.T, args ...string) (*updateInput, error, string) {
	t.Helper()
	cmd, err := newUpdateFlagsCommand(t, args...)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	var in *updateInput
	stderr := captureStderr(t, func() {
		in, err = gatherUpdateInput(t.Context(), cmd)
	})
	return in, err, stderr
}

func writeEmptyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	return path
}

// TestGatherUpdateInputRefusesEmptyTextFieldsGH8149: an exactly empty
// replacement for description, design or acceptance is what a failed command
// substitution collapses to, so update refuses it and names the field's
// deliberate clear flag; --force and --allow-empty-description do not open it.
func TestGatherUpdateInputRefusesEmptyTextFieldsGH8149(t *testing.T) {
	emptyFile := writeEmptyFile(t)
	cases := []struct {
		name     string
		args     []string
		stdin    *string
		wantFlag string
	}{
		{"description", []string{"--description="}, nil, "--clear-description"},
		{"description_short", []string{"-d", ""}, nil, "--clear-description"},
		{"body", []string{"--body="}, nil, "--clear-description"},
		{"message", []string{"--message="}, nil, "--clear-description"},
		{"message_short", []string{"-m", ""}, nil, "--clear-description"},
		{"description_with_force", []string{"--description=", "--force"}, nil, "--clear-description"},
		{"description_with_allow_empty", []string{"--description=", "--allow-empty-description"}, nil, "--clear-description"},
		// --description wins alias precedence, so the non-empty --body is
		// ignored and the resolved value is empty.
		{"description_empty_body_nonempty", []string{"--description=", "--body=kept"}, nil, "--clear-description"},
		{"design", []string{"--design="}, nil, "--clear-design"},
		{"design_with_force", []string{"--design=", "--force"}, nil, "--clear-design"},
		{"design_file_empty", []string{"--design-file", emptyFile}, nil, "--clear-design"},
		{"design_file_stdin_empty", []string{"--design-file", "-"}, new(string), "--clear-design"},
		{"acceptance", []string{"--acceptance="}, nil, "--clear-acceptance"},
		{"acceptance_criteria", []string{"--acceptance-criteria="}, nil, "--clear-acceptance"},
		{"acceptance_with_force", []string{"--acceptance=", "--force"}, nil, "--clear-acceptance"},
		{"empty_with_other_field", []string{"--title", "Renamed", "--design="}, nil, "--clear-design"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func() {
				in, err, stderr := gatherUpdateInputFor(t, tc.args...)
				if err == nil {
					t.Fatalf("%v: expected refusal, got fields %#v", tc.args, in.fields)
				}
				if !strings.Contains(stderr, tc.wantFlag) {
					t.Fatalf("%v: expected refusal naming %s, got: %q", tc.args, tc.wantFlag, stderr)
				}
			}
			if tc.stdin != nil {
				withTestStdin(t, *tc.stdin, run)
				return
			}
			run()
		})
	}

	// Non-empty and whitespace values stay content; the empty-value guard
	// does not reinterpret alias precedence.
	accepted := []struct {
		name  string
		args  []string
		field string
		want  string
	}{
		{"description_whitespace", []string{"--description= "}, "description", " "},
		{"design_whitespace", []string{"--design", "\n"}, "design", "\n"},
		{"acceptance_nonempty", []string{"--acceptance", "done when green"}, "acceptance_criteria", "done when green"},
		{"acceptance_wins_over_empty_criteria", []string{"--acceptance", "kept", "--acceptance-criteria="}, "acceptance_criteria", "kept"},
		{"equal_nonempty_aliases", []string{"--description", "same", "--body", "same"}, "description", "same"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			in, err, stderr := gatherUpdateInputFor(t, tc.args...)
			if err != nil {
				t.Fatalf("%v: unexpected refusal: %v %s", tc.args, err, stderr)
			}
			if got, ok := in.fields[tc.field]; !ok || got != tc.want {
				t.Fatalf("%v: fields[%q] = %#v (present=%v), want %q", tc.args, tc.field, got, ok, tc.want)
			}
		})
	}
}

// TestUpdateRegistersTextClearFlagsGH8149 guards the wiring the refusals
// advertise: update registers each clear flag, and validateDescriptionUpdate
// keys the inline-empty refusal on --clear-description being registered, so
// dropping that registration would silently reopen the wipe.
func TestUpdateRegistersTextClearFlagsGH8149(t *testing.T) {
	for _, name := range []string{"clear-description", "clear-design", "clear-acceptance"} {
		if updateCmd.Flags().Lookup(name) == nil {
			t.Errorf("expected update to register --%s", name)
		}
	}
}
