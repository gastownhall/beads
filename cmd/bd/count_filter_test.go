//go:build cgo

package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/issueops"
)

// The filter semantics of `bd count` live in the Counter role now, built once
// in workapi.BuildCountFilter, with the cardinality-parity assertions in
// internal/workapi/count_test.go. What is left here is turning flags into the
// role's request and refusing a combination the request cannot express.

// TestCountIncludeInfraFlagShape pins the flag's existence and default so
// scripted callers keep byte-identical behavior (GH#4387). The no-flag path
// carries IncludeInfra=false into the role, where the durable-only default is
// now decided.
func TestCountIncludeInfraFlagShape(t *testing.T) {
	flag := countCmd.Flags().Lookup("include-infra")
	if flag == nil {
		t.Fatal("bd count must expose an --include-infra flag (GH#4387)")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--include-infra must default to false, got %q", flag.DefValue)
	}

	request, _, err := parseCountRequest(newCountFlagSet(t))
	if err != nil {
		t.Fatalf("parseCountRequest with no flags set: %v", err)
	}
	if request.IncludeInfra {
		t.Error("IncludeInfra = true with no flags set, want the durable-only default")
	}
}

// TestCountIncludeEphemeralFlagShape is the same pin for --include-ephemeral,
// plus the case TestParseCountRequestCarriesEveryFilterFlag cannot see because
// it sets both plane flags at once: --include-ephemeral ALONE must not reach
// the role as IncludeInfra, which would also drop templates and gates from
// the count.
func TestCountIncludeEphemeralFlagShape(t *testing.T) {
	flag := countCmd.Flags().Lookup("include-ephemeral")
	if flag == nil {
		t.Fatal("bd count must expose an --include-ephemeral flag")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--include-ephemeral must default to false, got %q", flag.DefValue)
	}

	request, _, err := parseCountRequest(newCountFlagSet(t))
	if err != nil {
		t.Fatalf("parseCountRequest with no flags set: %v", err)
	}
	if request.IncludeEphemeral {
		t.Error("IncludeEphemeral = true with no flags set, want the durable-only default")
	}

	flags := newCountFlagSet(t)
	if err := flags.Flags().Set("include-ephemeral", "true"); err != nil {
		t.Fatalf("set --include-ephemeral: %v", err)
	}
	request, _, err = parseCountRequest(flags)
	if err != nil {
		t.Fatalf("parseCountRequest --include-ephemeral: %v", err)
	}
	if !request.IncludeEphemeral {
		t.Error("IncludeEphemeral = false with --include-ephemeral set; the flag was dropped on the way into the request")
	}
	if request.IncludeInfra {
		t.Error("IncludeInfra = true with only --include-ephemeral set; it is the plane bit and nothing else")
	}
}

// TestCountScopeFlagsShape pins --parent, --no-parent, --exclude-type and
// --exclude-status: their existence, their defaults, and that setting one
// does not leak into another. These are the Counter-scope fields (S8): the
// behavior token issues.count.scope on the HTTP front door exists because of
// exactly these four flags.
func TestCountScopeFlagsShape(t *testing.T) {
	for flag, defValue := range map[string]string{
		"parent":         "",
		"no-parent":      "false",
		"exclude-type":   "[]",
		"exclude-status": "[]",
	} {
		got := countCmd.Flags().Lookup(flag)
		if got == nil {
			t.Fatalf("bd count must expose a --%s flag", flag)
		}
		if got.DefValue != defValue {
			t.Fatalf("--%s must default to %q, got %q", flag, defValue, got.DefValue)
		}
	}

	request, _, err := parseCountRequest(newCountFlagSet(t))
	if err != nil {
		t.Fatalf("parseCountRequest with no flags set: %v", err)
	}
	if request.ParentID != "" || request.NoParent || len(request.ExcludeTypes) != 0 || len(request.ExcludeStatus) != 0 {
		t.Errorf("parseCountRequest with no flags set = %#v, want all four scope fields at their zero value", request)
	}

	flags := newCountFlagSet(t)
	if err := flags.Flags().Set("parent", "bd-1"); err != nil {
		t.Fatalf("set --parent: %v", err)
	}
	if err := flags.Flags().Set("exclude-type", "wisp"); err != nil {
		t.Fatalf("set --exclude-type: %v", err)
	}
	request, _, err = parseCountRequest(flags)
	if err != nil {
		t.Fatalf("parseCountRequest --parent --exclude-type: %v", err)
	}
	if request.ParentID != "bd-1" {
		t.Errorf("ParentID = %q, want %q", request.ParentID, "bd-1")
	}
	if request.NoParent {
		t.Error("NoParent = true with only --parent set; the two flags must not leak into each other")
	}
	if !reflect.DeepEqual(request.ExcludeTypes, []string{"wisp"}) {
		t.Errorf("ExcludeTypes = %v, want [wisp]", request.ExcludeTypes)
	}
	if len(request.ExcludeStatus) != 0 {
		t.Errorf("ExcludeStatus = %v, want empty; only --exclude-type was set", request.ExcludeStatus)
	}
}

// TestParseCountRequestCarriesEveryFilterFlag is the tripwire for a flag that
// is registered, documented and silently dropped on the way into the request.
// Every filter flag is set to a value distinguishable from its zero and read
// back off the request.
//
// --no-parent is NOT in this matrix: review S8 follow-up #1 made --parent and
// --no-parent mutually exclusive, and this test already sets --parent to
// cover that field, so setting --no-parent alongside it would refuse the
// whole request instead of pinning a mapping. --no-parent's carriage into the
// request is pinned on its own in TestParseCountRequestRefusesParentAndNoParentTogether's
// sibling cases below and in TestCountScopeFlagsShape above.
func TestParseCountRequestCarriesEveryFilterFlag(t *testing.T) {
	flags := newCountFlagSet(t)
	for flag, value := range map[string]string{
		"status":            "closed",
		"assignee":          "alice",
		"type":              "bug",
		"label":             "alpha,beta",
		"label-any":         "gamma",
		"title":             "needle",
		"id":                "bd-1,bd-2",
		"title-contains":    "tc",
		"desc-contains":     "dc",
		"notes-contains":    "nc",
		"created-after":     "2026-01-01",
		"created-before":    "2026-01-02",
		"updated-after":     "2026-01-03",
		"updated-before":    "2026-01-04",
		"closed-after":      "2026-01-05",
		"closed-before":     "2026-01-06",
		"empty-description": "true",
		"no-assignee":       "true",
		"no-labels":         "true",
		"metadata-field":    "team=platform",
		"has-metadata-key":  "audit_ref",
		"priority":          "1",
		"priority-min":      "0",
		"priority-max":      "4",
		"include-infra":     "true",
		"include-ephemeral": "true",
		"parent":            "bd-9",
		"exclude-type":      "wisp,gate",
		"exclude-status":    "closed,archived",
	} {
		if err := flags.Flags().Set(flag, value); err != nil {
			t.Fatalf("set --%s=%s: %v", flag, value, err)
		}
	}

	request, group, err := parseCountRequest(flags)
	if err != nil {
		t.Fatalf("parseCountRequest: %v", err)
	}
	if group != "" {
		t.Errorf("group = %q with no --by-* flag, want the scalar count", group)
	}

	// parseTimeFlag resolves a bare date in the LOCAL zone, which is what a
	// user typing --created-after 2026-01-01 means, then normalizes the
	// representation to UTC so the storage layer binds the same instant on
	// every backend. The expectation constructs local midnight and converts,
	// so a change to either half of that contract shows up here instead of
	// shifting every bound by the test machine's offset.
	day := func(d int) *time.Time {
		stamp := time.Date(2026, 1, d, 0, 0, 0, 0, time.Local).UTC()
		return &stamp
	}
	priority, min, max := 1, 0, 4
	want := issueops.CountRequest{
		Status:           "closed",
		IssueType:        "bug",
		Assignee:         "alice",
		Priority:         &priority,
		PriorityMin:      &min,
		PriorityMax:      &max,
		Labels:           []string{"alpha", "beta"},
		LabelsAny:        []string{"gamma"},
		TitleSearch:      "needle",
		IDFilter:         "bd-1,bd-2",
		TitleContains:    "tc",
		DescContains:     "dc",
		NotesContains:    "nc",
		CreatedAfter:     day(1),
		CreatedBefore:    day(2),
		UpdatedAfter:     day(3),
		UpdatedBefore:    day(4),
		ClosedAfter:      day(5),
		ClosedBefore:     day(6),
		EmptyDesc:        true,
		NoAssignee:       true,
		NoLabels:         true,
		MetadataFields:   map[string]string{"team": "platform"},
		HasMetadataKey:   "audit_ref",
		IncludeInfra:     true,
		IncludeEphemeral: true,
		ParentID:         "bd-9",
		ExcludeTypes:     []string{"wisp", "gate"},
		ExcludeStatus:    []string{"closed", "archived"},
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("parseCountRequest built\n %#v\nwant\n %#v", request, want)
	}
}

// TestParseCountRequestRefusesParentAndNoParentTogether pins review S8
// follow-up #1: `bd count --parent X --no-parent` is refused at the CLI flag
// layer, before a request is even built — the same early-refusal shape every
// other mutually-exclusive pair on this CLI gets (e.g. --pinned/--no-pinned).
//
// HandleErrorRespectJSON prints "--parent and --no-parent are mutually
// exclusive" (`bd list`'s own wording, cmd/bd/list_input.go) to stderr/JSON
// and returns an opaque exit error, so — as every other parseCountRequest
// refusal test in this file does — only the refusal itself is asserted here,
// not the string inside the returned error.
func TestParseCountRequestRefusesParentAndNoParentTogether(t *testing.T) {
	flags := newCountFlagSet(t)
	if err := flags.Flags().Set("parent", "bd-1"); err != nil {
		t.Fatalf("set --parent: %v", err)
	}
	if err := flags.Flags().Set("no-parent", "true"); err != nil {
		t.Fatalf("set --no-parent: %v", err)
	}
	if _, _, err := parseCountRequest(flags); err == nil {
		t.Fatal("parseCountRequest accepted --parent with --no-parent, want a refusal")
	}
}

// TestParseCountRequestCarriesNoParentAlone pins --no-parent's own mapping,
// split out of TestParseCountRequestCarriesEveryFilterFlag because that test's
// --parent case cannot also set --no-parent now that the two refuse each
// other.
func TestParseCountRequestCarriesNoParentAlone(t *testing.T) {
	flags := newCountFlagSet(t)
	if err := flags.Flags().Set("no-parent", "true"); err != nil {
		t.Fatalf("set --no-parent: %v", err)
	}
	request, _, err := parseCountRequest(flags)
	if err != nil {
		t.Fatalf("parseCountRequest --no-parent: %v", err)
	}
	if !request.NoParent {
		t.Error("NoParent = false, want true")
	}
	if request.ParentID != "" {
		t.Errorf("ParentID = %q, want empty: --parent was not set", request.ParentID)
	}
}

func TestParseCountRequestRejectsInvalidMetadataField(t *testing.T) {
	for _, value := range []string{"team", "bad$key=value"} {
		t.Run(value, func(t *testing.T) {
			flags := newCountFlagSet(t)
			if err := flags.Flags().Set("metadata-field", value); err != nil {
				t.Fatalf("set --metadata-field: %v", err)
			}
			if _, _, err := parseCountRequest(flags); err == nil {
				t.Fatalf("parseCountRequest accepted --metadata-field %q", value)
			}
		})
	}
}

func TestParseCountRequestRejectsInvalidHasMetadataKey(t *testing.T) {
	flags := newCountFlagSet(t)
	if err := flags.Flags().Set("has-metadata-key", "1bad"); err != nil {
		t.Fatalf("set --has-metadata-key: %v", err)
	}
	if _, _, err := parseCountRequest(flags); err == nil {
		t.Fatal("parseCountRequest accepted --has-metadata-key 1bad")
	}
}

// TestParseCountRequestResolvesTheGroupingFlags pins each --by-* flag to its
// dimension and the refusal for two at once. The exclusivity check cannot live
// behind the role — by then only one dimension is left — so it is checked here.
func TestParseCountRequestResolvesTheGroupingFlags(t *testing.T) {
	for flag, want := range map[string]issueops.CountGroup{
		"by-status":   issueops.CountGroupStatus,
		"by-priority": issueops.CountGroupPriority,
		"by-type":     issueops.CountGroupType,
		"by-assignee": issueops.CountGroupAssignee,
		"by-label":    issueops.CountGroupLabel,
	} {
		flags := newCountFlagSet(t)
		if err := flags.Flags().Set(flag, "true"); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
		_, group, err := parseCountRequest(flags)
		if err != nil {
			t.Fatalf("parseCountRequest(--%s): %v", flag, err)
		}
		if group != want {
			t.Errorf("--%s resolved to %q, want %q", flag, group, want)
		}
	}

	flags := newCountFlagSet(t)
	for _, flag := range []string{"by-status", "by-label"} {
		if err := flags.Flags().Set(flag, "true"); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	if _, _, err := parseCountRequest(flags); err == nil {
		t.Fatal("two --by-* flags were accepted, want a refusal")
	}
}

// TestParseCountRequestRejectsAnUnparseableDate pins that a bad date bound is
// refused at the flag seam rather than reaching the role as a zero time, which
// would silently widen the count to everything.
func TestParseCountRequestRejectsAnUnparseableDate(t *testing.T) {
	flags := newCountFlagSet(t)
	if err := flags.Flags().Set("created-after", "not-a-date"); err != nil {
		t.Fatalf("set --created-after: %v", err)
	}
	if _, _, err := parseCountRequest(flags); err == nil {
		t.Fatal("an unparseable --created-after was accepted, want a refusal")
	}
}

// newCountFlagSet returns a command carrying `bd count`'s flags at their
// defaults. It REGISTERS them rather than copying countCmd's set: cobra's
// AddFlagSet shares the underlying *Flag values, so a case that set a flag on
// the copy would leak it into the real command and into every later case.
func newCountFlagSet(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "count"}
	registerCountFlags(cmd)
	return cmd
}
