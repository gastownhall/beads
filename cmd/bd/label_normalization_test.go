package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
)

// Write paths must normalize labels the same way read paths already do.
//
// bd applies utils.NormalizeLabels (trim, drop empty, dedupe) to every label
// FILTER path (list, search, ready, orphans, count, workapi) but to no label
// WRITE path. The asymmetry means `--labels 'a, b'` stores " b" with a leading
// space, while `--label ' b'` on a filter is trimmed to "b" — so the stored
// label can never match its own filter and a filtered list is silently short.
func TestGatherCreateInputNormalizesLabels(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			// The comma-space form most people type. pflag's CSV split keeps
			// the leading space, and the split looks like it worked.
			name: "comma space separated",
			args: []string{"--labels", "theme:a, theme:b"},
			want: []string{"theme:a", "theme:b"},
		},
		{
			name: "surrounding whitespace trimmed",
			args: []string{"--labels", "  theme:a  "},
			want: []string{"theme:a"},
		},
		{
			name: "empty elements dropped",
			args: []string{"--labels", "theme:a,,theme:b"},
			want: []string{"theme:a", "theme:b"},
		},
		{
			name: "duplicates collapsed",
			args: []string{"--labels", "theme:a,theme:a"},
			want: []string{"theme:a"},
		},
		{
			// --label is documented as an alias for --labels, so it must
			// normalize identically rather than bypass the cleanup.
			name: "label alias normalized",
			args: []string{"--label", " theme:a , theme:b"},
			want: []string{"theme:a", "theme:b"},
		},
		{
			// The alias appends to --labels; dedupe must span both flags.
			name: "alias and labels deduped together",
			args: []string{"--labels", "theme:a", "--label", " theme:a"},
			want: []string{"theme:a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--title", "Title"}, tt.args...)
			cmd := newCreateFlagsCommand(t, args...)

			in, err := gatherCreateInput(cmd, nil)
			if err != nil {
				t.Fatalf("gatherCreateInput: %v", err)
			}

			assertLabels(t, in.labels, tt.want)
		})
	}
}

// The update write paths carry the same gap on --add-label, --remove-label and
// --set-labels.
func TestGatherUpdateInputNormalizesLabels(t *testing.T) {
	// gatherUpdateInput reads flags through Flags().Changed, which reports
	// false for anything unregistered, so the three label flags are all this
	// case needs. ctx is consulted only for --status validation.
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	cmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	cmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")

	args := []string{
		"--add-label", "theme:a, theme:b",
		"--remove-label", " theme:c ",
		"--set-labels", "theme:d,,theme:d",
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse update flags: %v", err)
	}

	in, err := gatherUpdateInput(context.Background(), cmd)
	if err != nil {
		t.Fatalf("gatherUpdateInput: %v", err)
	}

	assertLabels(t, in.addLabels, []string{"theme:a", "theme:b"})
	assertLabels(t, in.removeLabels, []string{"theme:c"})
	if in.setLabels == nil {
		t.Fatal("expected --set-labels to be captured")
	}
	assertLabels(t, *in.setLabels, []string{"theme:d"})
}

// `bd tag` calls itself "Shorthand for 'bd update <id> --add-label <label>'",
// so it must normalize identically. It took the positional verbatim, which made
// it the one CLI label write that could still store an unfilterable label.
//
// normalizeLabelForTag is called before the direct/proxied route split, so one
// test covers both routes.
func TestNormalizeLabelForTag(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{"leading space trimmed", " theme:a", "theme:a"},
		{"surrounding whitespace trimmed", "  theme:a\t", "theme:a"},
		{"already clean is untouched", "theme:a", "theme:a"},
		// A label containing a space is legitimate and is stored as asked;
		// warnLabelsContainingWhitespace is what flags it.
		{"internal space preserved", "good first issue", "good first issue"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeLabelForTag(tt.raw)
			if err != nil {
				t.Fatalf("normalizeLabelForTag(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeLabelForTag(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}

	// The plural flags can drop an empty element and still honor the rest of
	// the request. `bd tag` has one label to add, so dropping it would report
	// success having written nothing.
	for _, raw := range []string{"", "   ", "\t"} {
		t.Run("rejects empty "+fmt.Sprintf("%q", raw), func(t *testing.T) {
			if _, err := normalizeLabelForTag(raw); err == nil {
				t.Fatalf("normalizeLabelForTag(%q) = nil error, want a refusal", raw)
			}
		})
	}
}

func assertLabels(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels = %q, want %q", got, want)
		}
	}
}
