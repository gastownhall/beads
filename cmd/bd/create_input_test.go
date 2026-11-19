package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestCreateCommandRejectsExtraPositionalArguments(t *testing.T) {
	err := createCmd.Args(createCmd, []string{"bug", "real title"})
	if err == nil {
		t.Fatal("expected create to reject a second positional argument")
	}
	if !strings.Contains(err.Error(), "at most 1 arg") {
		t.Fatalf("expected actionable maximum-argument error, got: %v", err)
	}
}

func TestCreateCommandAllowsOnePositionalArgument(t *testing.T) {
	if err := createCmd.Args(createCmd, []string{"title"}); err != nil {
		t.Fatalf("expected create to accept a single title argument, got: %v", err)
	}
	// Zero args with no --file/--graph/--title fails title resolution at
	// Args time (upstream moved title validation into Args via
	// validateCreateArgs), not with an arg-count error.
	if err := createCmd.Args(createCmd, nil); err == nil {
		t.Fatal("expected title-resolution error for zero args")
	}
}

func TestCreateCommandRegistersEmptyDescriptionOptIn(t *testing.T) {
	if createCmd.Flags().Lookup("allow-empty-description") == nil {
		t.Fatal("expected create to register --allow-empty-description")
	}
}

func TestCreateFileFlagHelpDoesNotClaimSingleIssueGH4643(t *testing.T) {
	f := createCmd.Flags().Lookup("file")
	if f == nil {
		t.Fatal("expected create to register --file")
	}
	if !strings.Contains(f.Usage, "one issue per") {
		t.Fatalf("expected --file help text to name its per-heading batch behavior, got: %q", f.Usage)
	}
	if !strings.Contains(f.Usage, "--body-file") {
		t.Fatalf("expected --file help text to point single-issue callers at --body-file, got: %q", f.Usage)
	}
}

func TestGatherCreateInputRejectsTitleWithFile(t *testing.T) {
	cmd := newCreateFlagsCommand(t, "--file", "issues.md")
	var err error
	stderr := captureStderr(t, func() {
		_, err = gatherCreateInput(cmd, []string{"Some title"})
	})
	if err == nil {
		t.Fatal("expected gatherCreateInput to reject a positional title with --file")
	}
	if !strings.Contains(stderr, "cannot specify both title and --file flag") {
		t.Fatalf("expected title/--file conflict on stderr, got stderr=%q err=%v", stderr, err)
	}
	if !strings.Contains(stderr, "--body-file") {
		t.Fatalf("expected title/--file conflict to hint at --body-file, got stderr=%q err=%v", stderr, err)
	}
}

func TestGatherCreateInputRejectsEmptyBodyFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(filePath, nil, 0644); err != nil {
		t.Fatalf("write empty body file: %v", err)
	}

	cmd := &cobra.Command{Use: "create [title]"}
	registerCommonIssueFlags(cmd)
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description input from stdin or file")
	if err := cmd.ParseFlags([]string{"--body-file", filePath}); err != nil {
		t.Fatalf("parse create flags: %v", err)
	}

	_, err := gatherCreateInput(cmd, []string{"title"})
	if err == nil {
		t.Fatal("expected create input gathering to reject an empty body file")
	}
}

func TestGatherCreateInputMapsSingleIssueFlags(t *testing.T) {
	cmd := newCreateFlagsCommand(t,
		"--title", "Title from flag",
		"--description", "Description",
		"--design", "Design",
		"--acceptance", "Acceptance",
		"--notes", "Notes",
		"--append-notes", "Append notes",
		"--spec-id", "spec-1",
		"--priority", "P1",
		"--type", "event",
		"--status", "blocked",
		"--assignee", "alice",
		"--external-ref", "gh-9",
		"--labels", "team-a,shared",
		"--label", "alias",
		"--deps", "blocks:bd-target,related:bd-other",
		"--waits-for", "bd-spawner",
		"--waits-for-gate", "any-children",
		"--silent",
		"--dry-run",
		"--force",
		"--validate",
		"--ephemeral",
		"--mol-type", "work",
		"--wisp-type", "heartbeat",
		"--event-category", "patrol.muted",
		"--event-actor", "agent:alice",
		"--event-target", "bd-target",
		"--event-payload", `{"reason":"test"}`,
		"--due", "+48h",
		"--defer", "+24h",
		"--metadata", `{"key":"value"}`,
		"--estimate", "90",
		"--repo", "https://example.test/issues",
	)

	beforeGather := time.Now()
	in, err := gatherCreateInput(cmd, nil)
	afterGather := time.Now()
	if err != nil {
		t.Fatalf("gatherCreateInput: %v", err)
	}

	if in.title != "Title from flag" || in.description != "Description" || in.design != "Design" || in.acceptanceCriteria != "Acceptance" {
		t.Errorf("content fields = %#v", in)
	}
	if in.notes != "Notes" || in.appendNotes != "Append notes" || in.specID != "spec-1" {
		t.Errorf("planning fields = %#v", in)
	}
	if in.priority != 1 || in.issueType != "event" || in.status != "blocked" || in.assignee != "alice" || in.externalRef != "gh-9" {
		t.Errorf("issue fields = %#v", in)
	}
	if got, want := strings.Join(in.labels, ","), "team-a,shared,alias"; got != want {
		t.Errorf("labels = %q, want %q", got, want)
	}
	if got, want := strings.Join(in.deps, ","), "blocks:bd-target,related:bd-other"; got != want {
		t.Errorf("deps = %q, want %q", got, want)
	}
	if in.waitsFor != "bd-spawner" || in.waitsForGate != "any-children" || !in.waitsForGateSet {
		t.Errorf("waits-for fields = %#v", in)
	}
	if !in.silent || !in.dryRun || !in.force || !in.validate || !in.ephemeral || in.noHistory {
		t.Errorf("boolean fields = %#v", in)
	}
	if in.molType != "work" || in.wispType != "heartbeat" {
		t.Errorf("wisp fields = %#v", in)
	}
	if in.eventCategory != "patrol.muted" || in.eventActor != "agent:alice" || in.eventTarget != "bd-target" || in.eventPayload != `{"reason":"test"}` {
		t.Errorf("event fields = %#v", in)
	}
	if in.dueAt == nil || in.deferUntil == nil {
		t.Errorf("due/defer fields = %#v", in)
	} else {
		if in.dueAt.Before(beforeGather.Add(48*time.Hour)) || in.dueAt.After(afterGather.Add(48*time.Hour)) {
			t.Errorf("DueAt = %v, want between %v and %v", in.dueAt, beforeGather.Add(48*time.Hour), afterGather.Add(48*time.Hour))
		}
		if in.deferUntil.Before(beforeGather.Add(24*time.Hour)) || in.deferUntil.After(afterGather.Add(24*time.Hour)) {
			t.Errorf("DeferUntil = %v, want between %v and %v", in.deferUntil, beforeGather.Add(24*time.Hour), afterGather.Add(24*time.Hour))
		}
	}
	if string(in.metadata) != `{"key":"value"}` || !in.metadataSet {
		t.Errorf("metadata = %q, set = %t", in.metadata, in.metadataSet)
	}
	if in.estimatedMinutes == nil || *in.estimatedMinutes != 90 {
		t.Errorf("estimate = %v", in.estimatedMinutes)
	}
	if in.repoOverride != "https://example.test/issues" || !in.repoOverrideSet {
		t.Errorf("repo override = %q, set = %t", in.repoOverride, in.repoOverrideSet)
	}

	t.Run("explicit ID", func(t *testing.T) {
		in, err := gatherCreateInput(newCreateFlagsCommand(t, "--id", "bd-explicit"), []string{"Explicit ID"})
		if err != nil {
			t.Fatalf("gatherCreateInput: %v", err)
		}
		if in.explicitID != "bd-explicit" || in.parentID != "" {
			t.Errorf("ID fields = %#v", in)
		}
	})

	t.Run("parent and label inheritance controls", func(t *testing.T) {
		in, err := gatherCreateInput(newCreateFlagsCommand(t,
			"--parent", "bd-parent",
			"--no-inherit-labels",
			"--labels", "own-label",
		), []string{"Child"})
		if err != nil {
			t.Fatalf("gatherCreateInput: %v", err)
		}
		if in.parentID != "bd-parent" || !in.noInheritLabels || strings.Join(in.labels, ",") != "own-label" {
			t.Errorf("parent fields = %#v", in)
		}
	})
}

// newCreateFlagsCommand clones createCmd's flag definitions onto a fresh
// command. This keeps gatherCreateInput's flag-level tests on the real CLI
// surface without allowing state from one case to leak into another.
func newCreateFlagsCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "create [title]"}
	createCmd.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Value.Type() {
		case "bool":
			cmd.Flags().Bool(f.Name, f.DefValue == "true", f.Usage)
		case "int":
			n, err := strconv.Atoi(f.DefValue)
			if err != nil {
				t.Fatalf("--%s has non-integer default %q: %v", f.Name, f.DefValue, err)
			}
			cmd.Flags().Int(f.Name, n, f.Usage)
		case "string":
			cmd.Flags().String(f.Name, f.DefValue, f.Usage)
		case "stringSlice":
			if f.DefValue != "[]" {
				t.Fatalf("--%s has a non-empty slice default %q, which this clone does not reproduce", f.Name, f.DefValue)
			}
			cmd.Flags().StringSlice(f.Name, nil, f.Usage)
		default:
			t.Fatalf("--%s has unhandled flag type %q", f.Name, f.Value.Type())
		}
	})
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// TestIsAmbiguousRepoTarget covers bd-8d3f: an explicit relative/bare --repo
// value with no existing workspace must be refused rather than silently
// auto-vivified. Table-tested at the pure-function level since the embedded
// dolt round trip is ~15s per case.
func TestIsAmbiguousRepoTarget(t *testing.T) {
	tests := []struct {
		name          string
		flagChanged   bool
		repoOverride  string
		wantAmbiguous bool
	}{
		{"relative flag value is ambiguous", true, "some-other-rig", true},
		{"nested relative flag value is ambiguous", true, "../sibling", true},
		{"absolute flag value is unambiguous", true, "/abs/path", false},
		{"tilde-prefixed flag value is unambiguous", true, "~/planning", false},
		// Regression: an explicit but empty --repo value is the most
		// ambiguous possible input (it silently resolves to the cwd, see
		// routing.ExpandPath) and must still be gated, not exempted.
		{"explicit empty flag value is ambiguous", true, "", true},
		// Auto-routed paths (routing.mode: auto / routing.default) never
		// set the --repo flag, so they're always exempt from the gate
		// regardless of their shape.
		{"unset flag is never ambiguous, relative-looking value", false, "some-other-rig", false},
		{"unset flag is never ambiguous, empty value", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAmbiguousRepoTarget(tt.flagChanged, tt.repoOverride)
			if got != tt.wantAmbiguous {
				t.Errorf("isAmbiguousRepoTarget(%v, %q) = %v, want %v", tt.flagChanged, tt.repoOverride, got, tt.wantAmbiguous)
			}
		})
	}
}

// newCreateTypeHintCommand clones the flags validateCreateArgs reads, keeping
// --type's -t shorthand: the mistake the hint names is typed as -t.
func newCreateTypeHintCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "create [title]"}
	for _, name := range []string{"title", "file", "graph", "type"} {
		f := createCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("create does not register --%s", name)
		}
		cmd.Flags().StringP(f.Name, f.Shorthand, f.DefValue, f.Usage)
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// TestCreateTypeHint pins GH#5787 issue 1: -t is --type's shorthand, so
// `--type=bug -t "<title>"` overrides the type with the title text and leaves
// no title. The Args-time refusal keeps its missing-title line and names the
// parsed type value, the shorthand and where a title goes — before any store
// opens, so it cannot say whether a non-built-in value is a custom type.
func TestCreateTypeHint(t *testing.T) {
	if f := createCmd.Flags().ShorthandLookup("t"); f == nil || f.Name != "type" {
		t.Fatalf("-t no longer binds --type (got %v); revisit the hint", f)
	}
	const mistaken = "a long human title with spaces"

	validate := func(t *testing.T, positional []string, flags ...string) (string, error) {
		t.Helper()
		cmd := newCreateTypeHintCommand(t, flags...)
		var err error
		stderr := captureStderr(t, func() { err = validateCreateArgs(cmd, positional) })
		return stderr, err
	}

	hinted := []struct {
		name  string
		flags []string
	}{
		{"repeated type", []string{"--type=bug", "-t", mistaken}},
		{"single short type", []string{"-t", mistaken}},
		{"custom-looking name", []string{"--type", "incident"}},
	}
	for _, tc := range hinted {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := validate(t, nil, tc.flags...)
			if err == nil {
				t.Fatalf("validateCreateArgs accepted %v with no title", tc.flags)
			}
			value := tc.flags[len(tc.flags)-1]
			for _, want := range []string{
				"title required",
				fmt.Sprintf("--type is %q", value),
				"-t is short for --type, not --title",
				"--title",
			} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
			if strings.Contains(stderr, "invalid issue type") {
				t.Errorf("Args-time hint must not declare the type invalid (custom types are unknowable before the store opens):\n%s", stderr)
			}
		})
	}

	unhinted := []struct {
		name       string
		positional []string
		flags      []string
		wantErr    string
	}{
		{"default type", nil, nil, "title required"},
		{"built-in type", nil, []string{"--type", "bug"}, "title required"},
		{"alias type", nil, []string{"-t", "feat"}, "title required"},
		{"whitespace title", nil, []string{"--title", "   ", "-t", mistaken}, "title cannot be empty"},
		{"flag title", nil, []string{"--title", "real", "-t", mistaken}, ""},
		{"positional title", []string{"real"}, []string{"-t", mistaken}, ""},
		{"file", nil, []string{"--file", "x.md", "-t", mistaken}, ""},
		{"graph", nil, []string{"--graph", "x.json", "-t", mistaken}, ""},
	}
	for _, tc := range unhinted {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := validate(t, tc.positional, tc.flags...)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCreateArgs refused %v %v: %v\n%s", tc.positional, tc.flags, err, stderr)
				}
			} else if err == nil || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("want %q refusal, got err=%v stderr=%q", tc.wantErr, err, stderr)
			}
			if strings.Contains(stderr, "short for --type") {
				t.Errorf("unexpected -t hint:\n%s", stderr)
			}
		})
	}
}
