//go:build cgo

package main

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestApplyCountIncludeInfraMirrorsListFilter pins `bd count --include-infra`
// to the exact cardinality semantics of `bd list --include-infra --all`
// (GH#4387): for any filter set, the count must equal the number of rows the
// equivalent list invocation returns. The trap dimensions are the wisps merge
// (SkipWisps), template exclusion (IsTemplate), the default gate exclusion
// (ExcludeTypes), and infra-type routing to the ephemeral tier (Ephemeral).
func TestApplyCountIncludeInfraMirrorsListFilter(t *testing.T) {
	cfg := listFilterConfig{}
	for _, issueType := range []string{"", "task", "gate", "message"} {
		name := issueType
		if name == "" {
			name = "none"
		}
		t.Run("type_"+name, func(t *testing.T) {
			in := listInput{allFlag: true, includeInfra: true, issueType: issueType}
			want, err := buildListFilter(in, cfg)
			if err != nil {
				t.Fatalf("buildListFilter(%q): %v", issueType, err)
			}

			got := types.IssueFilter{}
			if issueType != "" {
				it := types.IssueType(issueType)
				got.IssueType = &it
			}
			applyCountIncludeInfra(&got, issueType, cfg)

			if got.SkipWisps != want.SkipWisps {
				t.Errorf("SkipWisps = %v, list --include-infra --all uses %v", got.SkipWisps, want.SkipWisps)
			}
			if !reflect.DeepEqual(got.IsTemplate, want.IsTemplate) {
				t.Errorf("IsTemplate = %v, list --include-infra --all uses %v", ptrStr(got.IsTemplate), ptrStr(want.IsTemplate))
			}
			if !reflect.DeepEqual(got.ExcludeTypes, want.ExcludeTypes) {
				t.Errorf("ExcludeTypes = %v, list --include-infra --all uses %v", got.ExcludeTypes, want.ExcludeTypes)
			}
			if !reflect.DeepEqual(got.Ephemeral, want.Ephemeral) {
				t.Errorf("Ephemeral = %v, list --include-infra --all uses %v", ptrStr(got.Ephemeral), ptrStr(want.Ephemeral))
			}
			if !reflect.DeepEqual(got.IssueType, want.IssueType) {
				t.Errorf("IssueType = %v, list --include-infra --all uses %v", got.IssueType, want.IssueType)
			}
			// bd count defaults to all statuses and all pinned states, which is
			// exactly what list's --all flag selects: none of these dimensions
			// may carry a filter on either side.
			if !reflect.DeepEqual(got.Status, want.Status) {
				t.Errorf("Status = %v, list --include-infra --all uses %v", got.Status, want.Status)
			}
			if !reflect.DeepEqual(got.Statuses, want.Statuses) {
				t.Errorf("Statuses = %v, list --include-infra --all uses %v", got.Statuses, want.Statuses)
			}
			if !reflect.DeepEqual(got.ExcludeStatus, want.ExcludeStatus) {
				t.Errorf("ExcludeStatus = %v, list --include-infra --all uses %v", got.ExcludeStatus, want.ExcludeStatus)
			}
			if !reflect.DeepEqual(got.Pinned, want.Pinned) {
				t.Errorf("Pinned = %v, list --include-infra --all uses %v", ptrStr(got.Pinned), ptrStr(want.Pinned))
			}
		})
	}
}

// TestApplyCountIncludeInfraCustomInfraTypes verifies that the infra-type
// routing honors a store-configured infra set, exactly like bd list does.
func TestApplyCountIncludeInfraCustomInfraTypes(t *testing.T) {
	cfg := listFilterConfig{infraSet: map[string]bool{"robot": true}}

	var robot types.IssueFilter
	applyCountIncludeInfra(&robot, "robot", cfg)
	if robot.Ephemeral == nil || !*robot.Ephemeral {
		t.Errorf("custom infra type %q must route to the ephemeral tier (Ephemeral=true), got %v", "robot", ptrStr(robot.Ephemeral))
	}

	// "message" is a default infra type but NOT part of the custom set, so it
	// must not route to the ephemeral tier (mirrors listFilterConfig.isInfra).
	var msg types.IssueFilter
	applyCountIncludeInfra(&msg, "message", cfg)
	if msg.Ephemeral != nil {
		t.Errorf("non-infra type under custom set must keep Ephemeral=nil, got %v", ptrStr(msg.Ephemeral))
	}
}

// TestApplyCountIncludeInfraDefaultUntouched documents that the helper is only
// invoked under --include-infra: the no-flag path must keep today's
// durable-only semantics (SkipWisps=true, no template/gate exclusion).
func TestApplyCountIncludeInfraDefaultUntouched(t *testing.T) {
	// The default path in count.go does not call applyCountIncludeInfra; it
	// sets SkipWisps=true and nothing else. Pin the flag's existence and
	// default value so scripted callers keep byte-identical behavior.
	flag := countCmd.Flags().Lookup("include-infra")
	if flag == nil {
		t.Fatal("bd count must expose an --include-infra flag (GH#4387)")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--include-infra must default to false, got %q", flag.DefValue)
	}
}

func ptrStr[T any](p *T) string {
	if p == nil {
		return "<nil>"
	}
	return "&" + reflect.ValueOf(*p).String()
}
