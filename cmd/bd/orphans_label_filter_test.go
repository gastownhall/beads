package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

// newOrphansFlagSet returns a command carrying `bd orphans`'s flags at their
// defaults. It REGISTERS them rather than copying orphansCmd's set: cobra's
// AddFlagSet shares the underlying *Flag values, so a case that set a flag on
// the copy would leak it into the real command and into every later case.
// Mirrors newCountFlagSet in count_filter_test.go.
func newOrphansFlagSet(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "orphans"}
	registerOrphansFlags(cmd)
	return cmd
}

// TestParseOrphansLabelFilterRejectsEmptyLabelFilters pins the same fix `bd
// ready`, `bd list` and `bd count` got: --label/--label-any, each supplied
// but normalizing to nothing, must fail loud rather than silently behave as
// if the flag were never passed (which would match everything, the opposite
// of what an explicit empty filter asked for). `bd orphans` has no
// --exclude-label flag, so only these two are in scope here.
//
// Like count's equivalent test, this only checks err != nil: the refusal
// goes through HandleErrorRespectJSON, whose *exitError never carries the
// message text in Error() (only ever written to stderr/stdout as a side
// effect), so the wording itself is pinned by ready's and list's tests
// instead.
func TestParseOrphansLabelFilterRejectsEmptyLabelFilters(t *testing.T) {
	cases := []struct {
		name  string
		flag  string
		value string
	}{
		{"label_empty", "label", ""},
		{"label_any_empty", "label-any", ""},
		{"label_whitespace_only", "label", "   "},
		{"label_multiple_all_blank", "label", ",,"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flags := newOrphansFlagSet(t)
			if err := flags.Flags().Set(c.flag, c.value); err != nil {
				t.Fatalf("set --%s=%q: %v", c.flag, c.value, err)
			}
			if _, _, err := parseOrphansLabelFilter(flags); err == nil {
				t.Fatalf("parseOrphansLabelFilter(--%s=%q) = nil error, want a refusal", c.flag, c.value)
			}
		})
	}
}

// TestParseOrphansLabelFilterToleratesEmptyElementsAmongUsableLabels is the
// regression half of the empty-label-filter fix: a label list that mixes
// empty elements with usable ones (e.g. "a,,b") must still filter on the
// usable labels, exactly as it did before this fix. Only a filter that
// normalizes to NOTHING is an error. Unlike count, orphans normalizes the
// labels it gathers before using them (see orphans.go), so the expectation
// here is the normalized ["a" "b"], not the raw slice.
func TestParseOrphansLabelFilterToleratesEmptyElementsAmongUsableLabels(t *testing.T) {
	want := []string{"a", "b"}

	for _, flag := range []string{"label", "label-any"} {
		t.Run(flag, func(t *testing.T) {
			flags := newOrphansFlagSet(t)
			if err := flags.Flags().Set(flag, "a,,b"); err != nil {
				t.Fatalf("set --%s: %v", flag, err)
			}
			labels, labelsAny, err := parseOrphansLabelFilter(flags)
			if err != nil {
				t.Fatalf("parseOrphansLabelFilter(--%s=a,,b): %v", flag, err)
			}
			got := labels
			if flag == "label-any" {
				got = labelsAny
			}
			if !slices.Equal(got, want) {
				t.Errorf("--%s=a,,b produced %q, want %q", flag, got, want)
			}
		})
	}
}

// TestParseOrphansLabelFilterNoLabelFlagsReturnsEverything is the regression
// half of the empty-label-filter fix on the other side: omitting the label
// flags entirely must keep meaning "no label filter", not trip the new
// supplied-but-empty check (which only fires when the flag was actually
// supplied).
func TestParseOrphansLabelFilterNoLabelFlagsReturnsEverything(t *testing.T) {
	labels, labelsAny, err := parseOrphansLabelFilter(newOrphansFlagSet(t))
	if err != nil {
		t.Fatalf("parseOrphansLabelFilter with no flags set: %v", err)
	}
	if len(labels) != 0 || len(labelsAny) != 0 {
		t.Errorf("labels/labelsAny = %q/%q, want both empty", labels, labelsAny)
	}
}
