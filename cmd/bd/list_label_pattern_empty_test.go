package main

import "testing"

// TestListRejectsSuppliedButEmptyLabelPattern pins bd-fl3of2 (GH#6636): a
// supplied-but-empty --label-pattern or --label-regex must be refused, not
// read as "no label filter" (which silently widens the listing to every issue).
//
// The emptiness line is drawn per flag, per the approved design: a glob is
// matched against normalized (trimmed) labels, so whitespace-only globs can
// never match one and are refused; a regex is an unanchored substring
// predicate against raw labels, which may contain spaces
// (label_whitespace_warn.go), so a whitespace-only --label-regex is a real
// filter and is pinned BELOW as accepted, not refused.
func TestListRejectsSuppliedButEmptyLabelPattern(t *testing.T) {
	for _, flag := range []string{"--label-pattern", "--label-regex"} {
		for _, value := range []string{"", "  "} {
			if flag == "--label-regex" && value != "" {
				continue // preserved: a substring regex of spaces still matches
			}
			t.Run(flag+"="+value, func(t *testing.T) {
				in, err := gatherListInput(newListFlagsCommand(t, flag, value))
				if err == nil {
					t.Fatalf("gatherListInput(%s %q) = nil error (LabelPattern=%q LabelRegex=%q), want a refusal: an empty value must not mean \"no label filter\"",
						flag, value, in.LabelPattern, in.LabelRegex)
				}
			})
		}
	}

	// The preservation half: `--label-regex "  "` is usable input (it can
	// match a label with an internal space) and must pass through with no
	// error rather than be refused.
	t.Run("--label-regex=  (preserved)", func(t *testing.T) {
		in, err := gatherListInput(newListFlagsCommand(t, "--label-regex", "  "))
		if err != nil {
			t.Fatalf("gatherListInput(--label-regex \"  \") = %v, want no error: a whitespace regex is a real substring predicate", err)
		}
		if in.LabelRegex != "  " {
			t.Fatalf("LabelRegex = %q, want it passed through byte-for-byte", in.LabelRegex)
		}
	})
}
