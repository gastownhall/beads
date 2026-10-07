package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// bd honors the shell's word boundaries: a quoted or backslash-escaped space
// makes ONE label, exactly as it makes one filename. That is correct, and also
// indistinguishable from a comma someone meant to type — so it warns.
func TestWarnLabelsContainingWhitespace(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		wantWarn bool
	}{
		{"plain slugs stay silent", []string{"theme:a", "theme:b"}, false},
		{"no labels stay silent", nil, false},
		{"space-containing label warns", []string{"theme:a theme:b"}, true},
		{"tab-containing label warns", []string{"theme:a\ttheme:b"}, true},
		{"a deliberate multi-word label also warns", []string{"good first issue"}, true},
		{"one bad among good still warns", []string{"theme:a", "b c"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				warnLabelsContainingWhitespace(tt.labels)
			})
			if got := out != ""; got != tt.wantWarn {
				t.Fatalf("warned=%v, want %v (stderr: %q)", got, tt.wantWarn, out)
			}
			if tt.wantWarn {
				if !strings.Contains(out, "ONE label") {
					t.Errorf("warning should say the label was stored whole, got: %q", out)
				}
				if !strings.Contains(out, "commas") {
					t.Errorf("warning should point at the comma form, got: %q", out)
				}
			}
		})
	}
}

// The warning names every offending label, so a bulk write does not hide one
// behind another, but prints the remedy once.
func TestWarnLabelsContainingWhitespaceNamesEachLabelOnce(t *testing.T) {
	out := captureStderr(t, func() {
		warnLabelsContainingWhitespace([]string{"a b", "c d", "fine"})
	})
	if !strings.Contains(out, `"a b"`) || !strings.Contains(out, `"c d"`) {
		t.Fatalf("expected both offending labels named, got: %q", out)
	}
	if strings.Contains(out, `"fine"`) {
		t.Fatalf("a clean label must not be named, got: %q", out)
	}
	if n := strings.Count(out, "separate them with commas"); n != 1 {
		t.Fatalf("expected the remedy line once, got %d: %q", n, out)
	}
}

// The warning fires through the real create path, not just in isolation.
func TestGatherCreateInputWarnsOnSpaceContainingLabel(t *testing.T) {
	out := captureStderr(t, func() {
		cmd := newCreateFlagsCommand(t, "--title", "Title", "--labels", "theme:a theme:b")
		if _, err := gatherCreateInput(cmd, nil); err != nil {
			t.Errorf("gatherCreateInput: %v", err)
		}
	})
	if !strings.Contains(out, "ONE label") {
		t.Fatalf("expected create to warn on a space-containing label, got: %q", out)
	}
}

// Removing a space-containing label is how the damage gets repaired, so the
// repair must not be warned at.
func TestGatherUpdateInputDoesNotWarnOnRemoveLabel(t *testing.T) {
	out := captureStderr(t, func() {
		cmd := &cobra.Command{Use: "update"}
		cmd.Flags().StringSlice("add-label", nil, "")
		cmd.Flags().StringSlice("remove-label", nil, "")
		cmd.Flags().StringSlice("set-labels", nil, "")
		if err := cmd.ParseFlags([]string{"--remove-label", "theme:a theme:b"}); err != nil {
			t.Errorf("parse update flags: %v", err)
			return
		}
		if _, err := gatherUpdateInput(t.Context(), cmd); err != nil {
			t.Errorf("gatherUpdateInput: %v", err)
		}
	})
	if out != "" {
		t.Fatalf("removing a space-containing label must not warn, got: %q", out)
	}
}
