package main

import (
	"slices"
	"strings"
	"testing"
)

// newListFlagsCommand and runGatherListInput already exist in
// list_ready_brief_test.go (written for the --brief tests) - reused here
// rather than redeclared. Its runGatherListInput merges stdout+stderr into
// one "shown" string instead of keeping them separate; the JSON test below
// leans on jsonErrorMessage's whole-string json.Unmarshal to catch stderr
// contamination (extra bytes after the JSON object fail the parse) since it
// cannot check stderr in isolation.

// TestGatherListInputRejectsEmptyLabelFilters pins the text-mode half of the
// empty-label-filter contract for `bd list`: --label/--label-any/
// --exclude-label, each supplied but normalizing to nothing, must fail loud
// rather than silently behave as if the flag were never passed (which would
// match everything, the opposite of what an explicit empty filter asked
// for).
func TestGatherListInputRejectsEmptyLabelFilters(t *testing.T) {
	pinJSONOutput(t, false)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"label", []string{"--label", ""}, "--label was supplied but contains no usable label"},
		{"label_any", []string{"--label-any", ""}, "--label-any was supplied but contains no usable label"},
		{"exclude_label", []string{"--exclude-label", ""}, "--exclude-label was supplied but contains no usable label"},
		{"label_whitespace_only", []string{"--label", "   "}, "--label was supplied but contains no usable label"},
		{"label_multiple_all_blank", []string{"--label", ",,"}, "--label was supplied but contains no usable label"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err, shown := runGatherListInput(t, newListFlagsCommand(t, c.args...))
			if err == nil {
				t.Fatalf("gatherListInput(%v) = nil, want an error", c.args)
			}
			if !strings.Contains(shown, c.want) {
				t.Errorf("expected %q, got output:\n%s", c.want, shown)
			}
		})
	}
}

// TestGatherListInputRejectsEmptyLabelFiltersRespectsJSON pins that the new
// check reports through HandleErrorRespectJSON like list's other value-
// validation checks (metadata-field, deps, wisp-type): under --json the
// message must land as a JSON object, with nothing else mixed in - a leak
// onto stderr would leave extra bytes after the object and fail the
// unmarshal below.
func TestGatherListInputRejectsEmptyLabelFiltersRespectsJSON(t *testing.T) {
	pinJSONOutput(t, true)

	_, err, shown := runGatherListInput(t, newListFlagsCommand(t, "--label", ""))
	if err == nil {
		t.Fatal(`gatherListInput(--label "") = nil, want an error`)
	}
	if msg := jsonErrorMessage(t, shown); !strings.Contains(msg, "--label was supplied but contains no usable label") {
		t.Errorf("output = %q, want it to contain the empty-label message", msg)
	}
}

// TestGatherListInputToleratesEmptyElementsAmongUsableLabels is the
// regression half of the empty-label-filter fix: a label list that mixes
// empty elements with usable ones (e.g. "a,,b") must still filter on the
// usable labels, exactly as it did before this fix. Only a filter that
// normalizes to NOTHING is an error.
func TestGatherListInputToleratesEmptyElementsAmongUsableLabels(t *testing.T) {
	want := []string{"a", "b"}

	cases := []struct {
		name string
		args []string
		get  func(listInput) []string
	}{
		{"label", []string{"--label", "a,,b"}, func(in listInput) []string { return in.Labels }},
		{"label_any", []string{"--label-any", "a,,b"}, func(in listInput) []string { return in.LabelsAny }},
		{"exclude_label", []string{"--exclude-label", "a,,b"}, func(in listInput) []string { return in.ExcludeLabels }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, err, _ := runGatherListInput(t, newListFlagsCommand(t, c.args...))
			if err != nil {
				t.Fatalf("gatherListInput(%v): %v", c.args, err)
			}
			if have := c.get(in); !slices.Equal(have, want) {
				t.Errorf("got %q, want %q", have, want)
			}
		})
	}
}

// TestGatherListInputNoLabelFlagsReturnsEverything is the regression half of
// the empty-label-filter fix on the other side: omitting the label flags
// entirely must keep meaning "no label filter", not trip the new supplied-
// but-empty check (which only fires when the flag was actually supplied).
func TestGatherListInputNoLabelFlagsReturnsEverything(t *testing.T) {
	in, err, _ := runGatherListInput(t, newListFlagsCommand(t))
	if err != nil {
		t.Fatalf("gatherListInput(): %v", err)
	}
	if len(in.Labels) != 0 || len(in.LabelsAny) != 0 || len(in.ExcludeLabels) != 0 {
		t.Errorf("label sets = (%q, %q, %q), want all empty", in.Labels, in.LabelsAny, in.ExcludeLabels)
	}
}
