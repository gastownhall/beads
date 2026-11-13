package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newUpdateDescriptionGuardCmd is shaped like updateCmd's text-field
// surface: the common issue flags, the stdin/file opt-in, and the GH#8149
// clear verbs, whose registration (not their value) marks the update
// contract in validateDescriptionUpdate. The clear verbs are registered
// inline so this file still compiles on revisions where updateCmd itself has
// none yet — the registration on the real commands is pinned separately by
// TestClearTextFlagsRegisteredOnUpdateOnly.
func newUpdateDescriptionGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "test",
		Run: func(cmd *cobra.Command, args []string) {},
	}
	registerCommonIssueFlags(cmd)
	cmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description replacement when reading from stdin or file")
	cmd.Flags().Bool("force", false, "Override refusals")
	cmd.Flags().Bool("clear-description", false, "Clear the description")
	cmd.Flags().Bool("clear-design", false, "Clear the design")
	cmd.Flags().Bool("clear-acceptance", false, "Clear the acceptance criteria")
	return cmd
}

// newCreateDescriptionGuardCmd is the create-shaped counterpart: create
// shares the description validator but none of the update clear verbs, so an
// inline empty stays its sanctioned explicit clear (a field may be born
// empty) and only stdin/file input is gated.
func newCreateDescriptionGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "test-create",
		Run: func(cmd *cobra.Command, args []string) {},
	}
	registerCommonIssueFlags(cmd)
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description input from stdin or file")
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

// GH#8149: neither --force nor --allow-empty-description unblocks an inline
// empty on update — the opt-in covers stdin/file input plumbing only, and
// what needs authorizing is the clear intent, which is --clear-description.
func TestValidateDescriptionUpdateRejectsInlineEmptyWithForceAndOptIn(t *testing.T) {
	for _, args := range [][]string{
		{"--description=", "--force"},
		{"--description=", "--allow-empty-description"},
		{"--description=", "--force", "--allow-empty-description"},
		{"--body=", "--force"},
		{"--message=", "--force"},
	} {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatalf("failed to parse flags %v: %v", args, err)
		}
		description, changed, flagErr := getDescriptionFlag(cmd)
		if flagErr != nil {
			t.Fatalf("unexpected error for %v: %v", args, flagErr)
		}
		err := validateDescriptionUpdate(cmd, description, changed)
		if err == nil {
			t.Fatalf("expected %v to be rejected on update", args)
		}
		if !strings.Contains(err.Error(), "--clear-description") {
			t.Fatalf("expected refusal to name --clear-description for %v, got: %v", args, err)
		}
	}
}

// The create contract is unchanged by GH#8149: create registers none of the
// clear verbs, so an inline empty description stays its sanctioned explicit
// clear (a field may be born empty).
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
		t.Fatalf("expected explicit inline empty description to succeed on create, got: %v", err)
	}
	if description != "" {
		t.Fatalf("expected empty inline description, got %q", description)
	}
}

// TestClearTextFlagsRegisteredOnUpdateOnly pins the real command surface:
// updateCmd must register all three GH#8149 clear verbs (their registration
// is the update-contract marker validateDescriptionUpdate reads — a missing
// flag would silently restore the wipe), and createCmd must expose none of
// them (creation keeps omitted and explicitly empty values working).
func TestClearTextFlagsRegisteredOnUpdateOnly(t *testing.T) {
	for _, name := range []string{"clear-description", "clear-design", "clear-acceptance"} {
		if updateCmd.Flags().Lookup(name) == nil {
			t.Errorf("updateCmd is missing --%s; the empty-replacement guard keys on its registration (GH#8149)", name)
		}
		if createCmd.Flags().Lookup(name) != nil {
			t.Errorf("createCmd must not expose --%s: the clear verbs are update-only (GH#8149 AC-5)", name)
		}
	}
}

// newGH8149UpdateInputCmd builds a command shaped like updateCmd's
// text-field surface for gatherUpdateInput, with the clear verbs registered
// inline so the test compiles (and its cases run red) on revisions before
// the flags land on updateCmd itself.
func newGH8149UpdateInputCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "update"}
	registerCommonIssueFlags(cmd)
	cmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description replacement when reading from stdin or file")
	cmd.Flags().Bool("force", false, "Override refusals")
	cmd.Flags().Bool("clear-description", false, "Clear the description")
	cmd.Flags().Bool("clear-design", false, "Clear the design")
	cmd.Flags().Bool("clear-acceptance", false, "Clear the acceptance criteria")
	cmd.Flags().String("if-assignee", "", "Apply the update only if the current assignee equals this value")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse update flags %v: %v", args, err)
	}
	return cmd
}

// TestGatherUpdateInputClearTextFlagsGH8149 pins the clear verbs' effect at
// input-gathering time: a true clear flag contributes exactly its field's
// empty replacement, clears compose with one another and with content for
// other fields, and a false clear flag contributes nothing (so
// --clear-description=false alone is the same no-update as before). The
// entries are ordinary field updates, so the conditional-update guards can
// ride on them.
func TestGatherUpdateInputClearTextFlagsGH8149(t *testing.T) {
	gather := func(t *testing.T, args ...string) *updateInput {
		t.Helper()
		cmd := newGH8149UpdateInputCmd(t, args...)
		var in *updateInput
		var err error
		captureStderr(t, func() {
			in, err = gatherUpdateInput(t.Context(), cmd)
		})
		if err != nil {
			t.Fatalf("gatherUpdateInput(%v): %v", args, err)
		}
		return in
	}

	t.Run("each_clear_flag_empties_exactly_its_field", func(t *testing.T) {
		for _, tt := range []struct {
			name  string
			args  []string
			field string
		}{
			{"clear_description", []string{"--clear-description"}, "description"},
			{"clear_design", []string{"--clear-design"}, "design"},
			{"clear_acceptance", []string{"--clear-acceptance"}, "acceptance_criteria"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				in := gather(t, tt.args...)
				if got, ok := in.fields[tt.field]; !ok || got != "" {
					t.Fatalf("fields[%s] = %v (present=%t), want the empty string", tt.field, got, ok)
				}
				if len(in.fields) != 1 {
					t.Fatalf("expected exactly the one cleared field, got %v", in.fields)
				}
			})
		}
	})

	t.Run("clear_flags_compose", func(t *testing.T) {
		in := gather(t, "--clear-description", "--clear-design", "--clear-acceptance")
		for _, field := range []string{"description", "design", "acceptance_criteria"} {
			if got, ok := in.fields[field]; !ok || got != "" {
				t.Fatalf("fields[%s] = %v (present=%t), want the empty string", field, got, ok)
			}
		}
		if len(in.fields) != 3 {
			t.Fatalf("expected exactly the three cleared fields, got %v", in.fields)
		}
	})

	t.Run("clear_composes_with_other_fields_content", func(t *testing.T) {
		in := gather(t, "--clear-description", "--design=kept")
		if got, ok := in.fields["description"]; !ok || got != "" {
			t.Fatalf("fields[description] = %v (present=%t), want the empty string", got, ok)
		}
		if got := in.fields["design"]; got != "kept" {
			t.Fatalf("fields[design] = %v, want %q", got, "kept")
		}
		if len(in.fields) != 2 {
			t.Fatalf("expected exactly the two fields, got %v", in.fields)
		}
	})

	t.Run("false_clear_flag_contributes_nothing", func(t *testing.T) {
		in := gather(t, "--clear-description=false")
		if len(in.fields) != 0 {
			t.Fatalf("--clear-description=false must not update anything, got %v", in.fields)
		}
		if !isUpdateInputNoop(in) {
			t.Fatal("a lone false clear flag must be a no-op update")
		}
	})

	t.Run("conditional_guard_rides_on_a_clear", func(t *testing.T) {
		// The --if-assignee guard needs a field update to ride on; the
		// clear's map entry is one.
		in := gather(t, "--if-assignee", "", "--clear-description")
		if got, ok := in.fields["description"]; !ok || got != "" {
			t.Fatalf("fields[description] = %v (present=%t), want the empty string", got, ok)
		}
		if in.ifAssignee == nil || *in.ifAssignee != "" {
			t.Fatalf("ifAssignee = %v, want the explicit empty guard", in.ifAssignee)
		}
	})
}

// TestUpdateClearTextFlagPairsAreMutuallyExclusive exercises each pairwise
// clear/content conflict through cobra's own flag-group validation, on a
// command built by the same registration call the real updateCmd uses. Only
// the clear/content pairs of the same field conflict: different clear flags
// compose, and a clear of one field composes with content for another.
func TestUpdateClearTextFlagPairsAreMutuallyExclusive(t *testing.T) {
	newCmd := func(t *testing.T, args ...string) *cobra.Command {
		t.Helper()
		cmd := &cobra.Command{Use: "update"}
		registerCommonIssueFlags(cmd)
		cmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
		registerUpdateClearTextFlags(cmd)
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatalf("parse update flags %v: %v", args, err)
		}
		return cmd
	}

	for _, tt := range []struct {
		clear   string
		content string
		asStdin bool
	}{
		{"clear-description", "description", false},
		{"clear-description", "body", false},
		{"clear-description", "message", false},
		{"clear-description", "body-file", false},
		{"clear-description", "description-file", false},
		{"clear-description", "stdin", true},
		{"clear-design", "design", false},
		{"clear-design", "design-file", false},
		{"clear-acceptance", "acceptance", false},
		{"clear-acceptance", "acceptance-criteria", false},
	} {
		t.Run(tt.clear+"_with_"+tt.content, func(t *testing.T) {
			var args []string
			if tt.asStdin {
				args = []string{"--" + tt.clear, "--stdin"}
			} else {
				args = []string{"--" + tt.clear, "--" + tt.content, "kept"}
			}
			if err := newCmd(t, args...).ValidateFlagGroups(); err == nil {
				t.Errorf("expected %v to be mutually exclusive", args)
			}
		})
	}

	for _, args := range [][]string{
		{"--clear-description", "--clear-design"},
		{"--clear-design", "--clear-acceptance"},
		{"--clear-description", "--design=kept"},
		{"--clear-design", "--acceptance=kept"},
		{"--clear-acceptance", "--notes", "kept"},
	} {
		t.Run("compose_"+strings.Join(args, "_"), func(t *testing.T) {
			if err := newCmd(t, args...).ValidateFlagGroups(); err != nil {
				t.Errorf("expected %v to compose, got: %v", args, err)
			}
		})
	}
}

// TestGatherUpdateInputRefusesEmptyTextFieldsGH8149 is the input-gathering
// half of the GH#8149 guard (the proxied route's boundary; the direct route
// validates the same values in RunE): an exactly empty replacement for a
// long-form text field is refused, naming the field's clear verb, instead of
// silently wiping the field behind a success receipt. Non-empty and
// whitespace-only values keep flowing through unchanged.
func TestGatherUpdateInputRefusesEmptyTextFieldsGH8149(t *testing.T) {
	refused := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"description_empty", []string{"--description="}, "--clear-description"},
		{"body_empty", []string{"--body="}, "--clear-description"},
		{"message_empty", []string{"--message="}, "--clear-description"},
		{"description_empty_with_force", []string{"--description=", "--force"}, "--clear-description"},
		{"description_empty_with_allow_empty_description", []string{"--description=", "--allow-empty-description"}, "--clear-description"},
		// Alias precedence: --description wins over --body, so this resolves
		// to the empty value and must fail rather than wipe.
		{"description_empty_wins_over_body", []string{"--description=", "--body=kept"}, "--clear-description"},
		{"design_empty", []string{"--design="}, "--clear-design"},
		{"acceptance_empty", []string{"--acceptance="}, "--clear-acceptance"},
		{"acceptance_criteria_empty", []string{"--acceptance-criteria="}, "--clear-acceptance"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newGH8149UpdateInputCmd(t, tt.args...)
			var in *updateInput
			var gatherErr error
			stderr := captureStderr(t, func() {
				in, gatherErr = gatherUpdateInput(t.Context(), cmd)
			})
			if gatherErr == nil {
				t.Fatalf("gatherUpdateInput accepted %v: an exactly empty replacement must be refused", tt.args)
			}
			if in != nil {
				t.Fatalf("a refused update must not yield a partial input: %+v", in)
			}
			if !strings.Contains(stderr, tt.wantErr) {
				t.Fatalf("expected stderr naming %s for %v, got: %s", tt.wantErr, tt.args, stderr)
			}
		})
	}

	t.Run("design_file_empty", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "design.md")
		if err := os.WriteFile(filePath, []byte{}, 0644); err != nil {
			t.Fatalf("write empty design file: %v", err)
		}
		cmd := newGH8149UpdateInputCmd(t, "--design-file", filePath)
		var in *updateInput
		var gatherErr error
		stderr := captureStderr(t, func() {
			in, gatherErr = gatherUpdateInput(t.Context(), cmd)
		})
		if gatherErr == nil || in != nil {
			t.Fatalf("empty --design-file must be refused, got in=%v err=%v", in, gatherErr)
		}
		if !strings.Contains(stderr, "--clear-design") {
			t.Fatalf("expected stderr naming --clear-design, got: %s", stderr)
		}
	})

	// Non-empty and whitespace values are content and keep flowing through,
	// including the alias-precedence case that resolves non-empty.
	kept := []struct {
		name  string
		args  []string
		field string
		want  any
	}{
		{"description_non_empty", []string{"--description=kept"}, "description", "kept"},
		{"description_whitespace_is_content", []string{"--description=  "}, "description", "  "},
		{"design_non_empty", []string{"--design=kept"}, "design", "kept"},
		{"design_whitespace_is_content", []string{"--design=\t"}, "design", "\t"},
		{"acceptance_non_empty_wins_over_empty_alias", []string{"--acceptance=kept", "--acceptance-criteria="}, "acceptance_criteria", "kept"},
	}
	for _, tt := range kept {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newGH8149UpdateInputCmd(t, tt.args...)
			var in *updateInput
			var gatherErr error
			captureStderr(t, func() {
				in, gatherErr = gatherUpdateInput(t.Context(), cmd)
			})
			if gatherErr != nil {
				t.Fatalf("gatherUpdateInput refused %v: %v", tt.args, gatherErr)
			}
			if got := in.fields[tt.field]; got != tt.want {
				t.Fatalf("fields[%s] = %v, want %v", tt.field, got, tt.want)
			}
		})
	}
}
