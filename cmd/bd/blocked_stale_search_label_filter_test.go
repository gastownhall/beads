package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

// The empty-label-filter fix (a flag supplied but normalizing to nothing must
// fail loud rather than read as "no filter") landed on ready, list, count and
// orphans first. These are the three commands that were still silently
// dropping it: blocked, stale and search.
//
// Each is exercised through the gatherer the real command calls, not through
// the command's RunE, because that gatherer is also the seam the PROXIED
// route goes through -- blockedFilterFromFlags serves blocked's direct and
// proxied paths, parseStaleLabelFilter builds the StaleFilter
// runStaleProxiedServer is handed, and parseSearchLabelFilter is called by
// both search.go and search_proxied_server.go. Testing the gatherer therefore
// covers both frontends at once; testing RunE would cover only one and would
// need a store.
//
// Like the sibling suites, the refusal cases assert only err != nil:
// exitError.Error() never carries the message text (it is only ever written
// to stderr/stdout as a side effect), so the wording is pinned by ready's and
// list's tests instead.

// emptyFilterCases are the shapes that must be refused: the flag was supplied
// (so cmd.Flags().Changed is true) but NormalizeLabels finds nothing usable.
var emptyFilterCases = []struct {
	name  string
	value string
}{
	{"empty", ""},
	{"whitespace_only", "   "},
	{"all_elements_blank", ",,"},
}

func newBlockedFlagSet(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "blocked"}
	registerBlockedFlags(cmd)
	return cmd
}

func newStaleFlagSet(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "stale"}
	registerStaleFlags(cmd)
	return cmd
}

func newSearchFlagSet(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "search"}
	registerSearchLabelFlags(cmd)
	return cmd
}

func TestBlockedFilterFromFlagsRejectsEmptyLabelFilters(t *testing.T) {
	for _, flag := range []string{"label", "label-any", "exclude-label"} {
		for _, c := range emptyFilterCases {
			t.Run(flag+"_"+c.name, func(t *testing.T) {
				cmd := newBlockedFlagSet(t)
				if err := cmd.Flags().Set(flag, c.value); err != nil {
					t.Fatalf("set --%s=%q: %v", flag, c.value, err)
				}
				if _, err := blockedFilterFromFlags(cmd); err == nil {
					t.Fatalf("blockedFilterFromFlags(--%s=%q) = nil error, want a refusal", flag, c.value)
				}
			})
		}
	}
}

// TestBlockedFilterFromFlagsToleratesEmptyElementsAmongUsableLabels is the
// regression half: "a,,b" carries a blank element but still names two real
// labels, so it must filter rather than refuse.
func TestBlockedFilterFromFlagsToleratesEmptyElementsAmongUsableLabels(t *testing.T) {
	cmd := newBlockedFlagSet(t)
	for _, flag := range []string{"label", "label-any", "exclude-label"} {
		if err := cmd.Flags().Set(flag, "a,,b"); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	filter, err := blockedFilterFromFlags(cmd)
	if err != nil {
		t.Fatalf("blockedFilterFromFlags with a,,b: %v", err)
	}
	want := []string{"a", "b"}
	for name, got := range map[string][]string{
		"Labels":        filter.Labels,
		"LabelsAny":     filter.LabelsAny,
		"ExcludeLabels": filter.ExcludeLabels,
	} {
		if !slices.Equal(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestBlockedFilterFromFlagsNoLabelFlagsReturnsEverything is the other
// regression half: omitting the flags entirely still means "no label filter".
// The check only fires on a flag that was actually supplied.
func TestBlockedFilterFromFlagsNoLabelFlagsReturnsEverything(t *testing.T) {
	filter, err := blockedFilterFromFlags(newBlockedFlagSet(t))
	if err != nil {
		t.Fatalf("blockedFilterFromFlags with no flags set: %v", err)
	}
	if len(filter.Labels) != 0 || len(filter.LabelsAny) != 0 || len(filter.ExcludeLabels) != 0 {
		t.Errorf("Labels/LabelsAny/ExcludeLabels = %q/%q/%q, want all empty",
			filter.Labels, filter.LabelsAny, filter.ExcludeLabels)
	}
}

func TestParseStaleLabelFilterRejectsEmptyLabelFilters(t *testing.T) {
	for _, flag := range []string{"label", "label-any", "exclude-label"} {
		for _, c := range emptyFilterCases {
			t.Run(flag+"_"+c.name, func(t *testing.T) {
				cmd := newStaleFlagSet(t)
				if err := cmd.Flags().Set(flag, c.value); err != nil {
					t.Fatalf("set --%s=%q: %v", flag, c.value, err)
				}
				if _, _, _, err := parseStaleLabelFilter(cmd); err == nil {
					t.Fatalf("parseStaleLabelFilter(--%s=%q) = nil error, want a refusal", flag, c.value)
				}
			})
		}
	}
}

func TestParseStaleLabelFilterToleratesEmptyElementsAmongUsableLabels(t *testing.T) {
	cmd := newStaleFlagSet(t)
	for _, flag := range []string{"label", "label-any", "exclude-label"} {
		if err := cmd.Flags().Set(flag, "a,,b"); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	labels, labelsAny, excludeLabels, err := parseStaleLabelFilter(cmd)
	if err != nil {
		t.Fatalf("parseStaleLabelFilter with a,,b: %v", err)
	}
	want := []string{"a", "b"}
	for name, got := range map[string][]string{
		"labels":        labels,
		"labelsAny":     labelsAny,
		"excludeLabels": excludeLabels,
	} {
		if !slices.Equal(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestParseStaleLabelFilterNoLabelFlagsReturnsEverything(t *testing.T) {
	labels, labelsAny, excludeLabels, err := parseStaleLabelFilter(newStaleFlagSet(t))
	if err != nil {
		t.Fatalf("parseStaleLabelFilter with no flags set: %v", err)
	}
	if len(labels) != 0 || len(labelsAny) != 0 || len(excludeLabels) != 0 {
		t.Errorf("labels/labelsAny/excludeLabels = %q/%q/%q, want all empty", labels, labelsAny, excludeLabels)
	}
}

func TestParseSearchLabelFilterRejectsEmptyLabelFilters(t *testing.T) {
	// bd search registers no --exclude-label, so only these two are in scope.
	for _, flag := range []string{"label", "label-any"} {
		for _, c := range emptyFilterCases {
			t.Run(flag+"_"+c.name, func(t *testing.T) {
				cmd := newSearchFlagSet(t)
				if err := cmd.Flags().Set(flag, c.value); err != nil {
					t.Fatalf("set --%s=%q: %v", flag, c.value, err)
				}
				if _, _, err := parseSearchLabelFilter(cmd); err == nil {
					t.Fatalf("parseSearchLabelFilter(--%s=%q) = nil error, want a refusal", flag, c.value)
				}
			})
		}
	}
}

func TestParseSearchLabelFilterToleratesEmptyElementsAmongUsableLabels(t *testing.T) {
	cmd := newSearchFlagSet(t)
	for _, flag := range []string{"label", "label-any"} {
		if err := cmd.Flags().Set(flag, "a,,b"); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	labels, labelsAny, err := parseSearchLabelFilter(cmd)
	if err != nil {
		t.Fatalf("parseSearchLabelFilter with a,,b: %v", err)
	}
	want := []string{"a", "b"}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %q, want %q", labels, want)
	}
	if !slices.Equal(labelsAny, want) {
		t.Errorf("labelsAny = %q, want %q", labelsAny, want)
	}
}

func TestParseSearchLabelFilterNoLabelFlagsReturnsEverything(t *testing.T) {
	labels, labelsAny, err := parseSearchLabelFilter(newSearchFlagSet(t))
	if err != nil {
		t.Fatalf("parseSearchLabelFilter with no flags set: %v", err)
	}
	if len(labels) != 0 || len(labelsAny) != 0 {
		t.Errorf("labels/labelsAny = %q/%q, want both empty", labels, labelsAny)
	}
}

// TestLabelFilterFlagsAreRegisteredOnTheRealCommands guards the extraction
// itself: registerBlockedFlags, registerStaleFlags and
// registerSearchLabelFlags replaced inline init blocks, so a flag dropped in
// that move would silently un-register a documented filter and turn every
// refusal test above into a test of a command nobody can invoke that way.
func TestLabelFilterFlagsAreRegisteredOnTheRealCommands(t *testing.T) {
	for _, tc := range []struct {
		cmd   *cobra.Command
		flags []string
	}{
		{blockedCmd, []string{"parent", "label", "label-any", "exclude-label"}},
		{staleCmd, []string{"days", "status", "limit", "label", "label-any", "exclude-label"}},
		{searchCmd, []string{"label", "label-any"}},
	} {
		for _, name := range tc.flags {
			if tc.cmd.Flags().Lookup(name) == nil {
				t.Errorf("bd %s must expose --%s", tc.cmd.Use, name)
			}
		}
	}
}
