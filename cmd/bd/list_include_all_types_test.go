package main

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestListIncludeAllTypesReachable pins bd-sqhqcd (GH#5694): bd list must be
// able to set issueops.ListRequest.IncludeAllTypes, the future-proof "show
// every type" switch that today only `bd human list` reaches.
func TestListIncludeAllTypesReachable(t *testing.T) {
	resetListFilterFlagState(t)

	fl := listCmd.Flags().Lookup("include-all-types")
	if fl == nil {
		t.Fatal("bd list registers no --include-all-types flag: ListRequest.IncludeAllTypes is unreachable from bd list")
	}
	t.Cleanup(func() { _ = fl.Value.Set("false"); fl.Changed = false })

	if err := listCmd.ParseFlags([]string{"--include-all-types"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	in, err := gatherListInput(listCmd)
	if err != nil {
		t.Fatalf("gatherListInput: %v", err)
	}
	if !in.IncludeAllTypes {
		t.Fatal("--include-all-types did not set ListRequest.IncludeAllTypes")
	}
}

// TestListRegistersIncludeAllTypes pins the flag itself. ListRequest's
// IncludeAllTypes is the one switch whose contract is "lift every type
// suppression, including ones added later", and before this
// `bd list` had no flag for it: callers had to spell out the four
// --include-* flags and would silently narrow when a fifth suppression lands.
func TestListRegistersIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types (ListRequest.IncludeAllTypes)")
	}
	if got := flag.Value.Type(); got != "bool" {
		t.Fatalf("--include-all-types must be a bool, got %s", got)
	}
	if flag.DefValue != "false" {
		t.Fatalf("--include-all-types must default to false, got %q", flag.DefValue)
	}
}

// newListTypeAxisInputCmd carries only the flags these checks read, in the
// shape of newWispTypeInputCmd: gatherListInput reads unregistered flags as
// zero values. It does not register --include-all-types, so it stands for
// `bd list` as it was before the flag.
func newListTypeAxisInputCmd() *cobra.Command {
	cmd := newWispTypeInputCmd()
	f := cmd.Flags()
	f.Bool("all", false, "")
	f.Bool("include-gates", false, "")
	f.Bool("include-templates", false, "")
	f.StringSlice("exclude-type", nil, "")
	f.IntP("limit", "n", workapi.DefaultListLimit, "")
	return cmd
}

// newIncludeAllTypesInputCmd is newListTypeAxisInputCmd plus the new flag.
func newIncludeAllTypesInputCmd() *cobra.Command {
	cmd := newListTypeAxisInputCmd()
	cmd.Flags().Bool("include-all-types", false, "")
	return cmd
}

// listTypeAxisConfiguredLimit is the list.limit useListTypeAxisConfig pins.
const listTypeAxisConfiguredLimit = 7

// useListTypeAxisConfig gives the test a fresh config in an empty directory,
// so the limit policy is the real one rather than whatever another test left
// in the process (the setup of TestListLimitPolicyIsResolvedBeforeTheRequest).
// It pins a configured list.limit: with the shared default a request that
// keeps the default limit and one that loses it could both resolve to an
// unlimited 0 on a pipe, and the comparison would prove nothing.
func useListTypeAxisConfig(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("list.limit", listTypeAxisConfiguredLimit)
}

// gatherListInputWith sets args (flag, value pairs) on cmd and gathers it.
func gatherListInputWith(t *testing.T, cmd *cobra.Command, args ...string) listInput {
	t.Helper()
	for i := 0; i+1 < len(args); i += 2 {
		if err := cmd.Flags().Set(args[i], args[i+1]); err != nil {
			t.Fatalf("set %s: %v", args[i], err)
		}
	}
	in, err := gatherListInput(cmd)
	if err != nil {
		t.Fatalf("gatherListInput %v: %v", args, err)
	}
	return in
}

// TestGatherListInput_IncludeAllTypesReachesTheFilter checks the flag is read
// onto the request as the union field alone, and that the filter built from
// it hides no bead for what it is, while the default listing keeps every
// suppression.
func TestGatherListInput_IncludeAllTypesReachesTheFilter(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	useListTypeAxisConfig(t)

	in := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true")
	if !in.IncludeAllTypes {
		t.Fatal("--include-all-types must set ListRequest.IncludeAllTypes, got false")
	}
	// The union is its own field: spelling it out as the four narrow knobs
	// would narrow silently when a fifth suppression lands.
	if in.IncludeGates || in.IncludeTemplates || in.IncludeInfra || in.IncludeEphemeral {
		t.Errorf("--include-all-types must set only IncludeAllTypes, got gates=%v templates=%v infra=%v ephemeral=%v",
			in.IncludeGates, in.IncludeTemplates, in.IncludeInfra, in.IncludeEphemeral)
	}

	filter, err := workapi.BuildListFilter(in.ListRequest, workapi.ListConfig{})
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	if len(filter.ExcludeTypes) != 0 {
		t.Errorf("no type may be excluded under --include-all-types, got ExcludeTypes=%v", filter.ExcludeTypes)
	}
	if filter.IsTemplate != nil {
		t.Errorf("templates must not be filtered out under --include-all-types, got IsTemplate=%v", *filter.IsTemplate)
	}
	if filter.SkipWisps {
		t.Error("the wisp plane must be read under --include-all-types, got SkipWisps=true")
	}

	// The default request keeps every suppression, so the plain listing is
	// unchanged.
	bare := gatherListInputWith(t, newIncludeAllTypesInputCmd())
	if bare.IncludeAllTypes {
		t.Fatal("IncludeAllTypes must default to false")
	}
	bareFilter, err := workapi.BuildListFilter(bare.ListRequest, workapi.ListConfig{})
	if err != nil {
		t.Fatalf("BuildListFilter (bare): %v", err)
	}
	if !bareFilter.SkipWisps || len(bareFilter.ExcludeTypes) == 0 || bareFilter.IsTemplate == nil {
		t.Errorf("the default listing must keep the type suppressions, got SkipWisps=%v ExcludeTypes=%v IsTemplate=%v",
			bareFilter.SkipWisps, bareFilter.ExcludeTypes, bareFilter.IsTemplate)
	}
}

// TestGatherListInput_IncludeAllTypesChangesOnlyTheTypeAxis compares an
// invocation with and without --include-all-types: the default type and
// plane suppressions are the only thing that moves. The status and pinned
// defaults, the effective limit and every explicit predicate stay what they
// are without it.
func TestGatherListInput_IncludeAllTypesChangesOnlyTheTypeAxis(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	useListTypeAxisConfig(t)

	in := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true")
	filter, err := workapi.BuildListFilter(in.ListRequest, workapi.ListConfig{})
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	bare := gatherListInputWith(t, newIncludeAllTypesInputCmd())
	bareFilter, err := workapi.BuildListFilter(bare.ListRequest, workapi.ListConfig{})
	if err != nil {
		t.Fatalf("BuildListFilter (bare): %v", err)
	}
	if filter.SkipWisps || len(filter.ExcludeTypes) != 0 || filter.IsTemplate != nil {
		t.Fatalf("precondition: --include-all-types lifts the type and plane defaults, got SkipWisps=%v ExcludeTypes=%v IsTemplate=%v",
			filter.SkipWisps, filter.ExcludeTypes, filter.IsTemplate)
	}

	// Only the type/plane suppression fields may differ between the two
	// filters: clear them on copies and the rest must be identical.
	typeAxisCleared := func(f types.IssueFilter) types.IssueFilter {
		f.SkipWisps = false
		f.ExcludeTypes = nil
		f.IsTemplate = nil
		return f
	}
	if a, b := typeAxisCleared(filter), typeAxisCleared(bareFilter); !reflect.DeepEqual(a, b) {
		t.Errorf("--include-all-types changed more than the type and plane suppressions:\nwith it    %+v\nwithout it %+v", a, b)
	}
	if !containsStatus(filter.ExcludeStatus, types.StatusClosed) {
		t.Errorf("--include-all-types says nothing about status: closed rows stay hidden without --all, got ExcludeStatus=%v", filter.ExcludeStatus)
	}
	if !reflect.DeepEqual(filter.ExcludeStatus, bareFilter.ExcludeStatus) {
		t.Errorf("--include-all-types changed the status exclusions: %v, want %v", filter.ExcludeStatus, bareFilter.ExcludeStatus)
	}
	if !reflect.DeepEqual(filter.Pinned, bareFilter.Pinned) || filter.Pinned == nil || *filter.Pinned {
		t.Errorf("--include-all-types must keep the pinned default (Pinned=false), got %v, bare %v", filter.Pinned, bareFilter.Pinned)
	}
	if bare.effectiveLimit != listTypeAxisConfiguredLimit {
		t.Fatalf("precondition: the bare listing takes the configured list.limit %d, got %d", listTypeAxisConfiguredLimit, bare.effectiveLimit)
	}
	if in.effectiveLimit != bare.effectiveLimit || !reflect.DeepEqual(in.Limit, bare.Limit) {
		t.Errorf("--include-all-types changed the limit: %d (%v), want %d (%v)", in.effectiveLimit, in.Limit, bare.effectiveLimit, bare.Limit)
	}

	t.Run("an explicit limit still wins", func(t *testing.T) {
		got := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true", "limit", "5")
		if got.effectiveLimit != 5 || got.Limit == nil || *got.Limit != 5 {
			t.Errorf("--include-all-types --limit 5 must list 5, got effectiveLimit=%d Limit=%v", got.effectiveLimit, got.Limit)
		}
	})

	t.Run("an explicit --exclude-type still applies", func(t *testing.T) {
		got := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true", "exclude-type", "gate")
		f, err := workapi.BuildListFilter(got.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(f.ExcludeTypes) != 1 || f.ExcludeTypes[0] != types.IssueType("gate") {
			t.Errorf("ExcludeTypes = %v, want [gate]: the caller's own exclusion is not a default suppression", f.ExcludeTypes)
		}
	})

	t.Run("a narrow include flag adds nothing to the union", func(t *testing.T) {
		got := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true", "include-gates", "true")
		f, err := workapi.BuildListFilter(got.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if !reflect.DeepEqual(f, filter) {
			t.Errorf("--include-all-types --include-gates built a different filter:\n%+v\nwant\n%+v", f, filter)
		}
	})

	t.Run("--include-ephemeral keeps its own field beside the union", func(t *testing.T) {
		got := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true", "include-ephemeral", "true")
		if !got.IncludeEphemeral || !got.IncludeAllTypes {
			t.Errorf("both include fields must be set, got IncludeEphemeral=%v IncludeAllTypes=%v", got.IncludeEphemeral, got.IncludeAllTypes)
		}
		f, err := workapi.BuildListFilter(got.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if !reflect.DeepEqual(f, filter) {
			t.Errorf("--include-all-types --include-ephemeral built a different filter:\n%+v\nwant\n%+v", f, filter)
		}
	})

	t.Run("with --all both sets of defaults lift", func(t *testing.T) {
		got := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true", "all", "true")
		f, err := workapi.BuildListFilter(got.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(f.ExcludeStatus) != 0 || f.Pinned != nil {
			t.Errorf("--all must lift the status and pinned defaults, got ExcludeStatus=%v Pinned=%v", f.ExcludeStatus, f.Pinned)
		}
		if len(f.ExcludeTypes) != 0 || f.IsTemplate != nil || f.SkipWisps {
			t.Errorf("--include-all-types must lift the type and plane defaults, got ExcludeTypes=%v IsTemplate=%v SkipWisps=%v", f.ExcludeTypes, f.IsTemplate, f.SkipWisps)
		}
		if got.effectiveLimit != 0 {
			t.Errorf("--all lifts the default limit, got %d", got.effectiveLimit)
		}
	})

	t.Run("an explicit infra type keeps the type but not the ephemeral-only narrowing", func(t *testing.T) {
		infra := workapi.ListConfig{}.InfraTypes()[0]
		cfg := workapi.ListConfig{CustomTypes: []string{infra}}
		got := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true", "type", infra)
		f, err := workapi.BuildListFilter(got.ListRequest, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if f.IssueType == nil || string(*f.IssueType) != infra {
			t.Errorf("the explicit type must survive, got IssueType=%v", f.IssueType)
		}
		if f.Ephemeral != nil {
			t.Errorf("--include-all-types must not narrow %s to its ephemeral rows, got Ephemeral=%v", infra, *f.Ephemeral)
		}
		without := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "type", infra)
		wf, err := workapi.BuildListFilter(without.ListRequest, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter (without): %v", err)
		}
		if wf.Ephemeral == nil || !*wf.Ephemeral {
			t.Errorf("precondition: --type %s alone narrows to ephemeral rows, got Ephemeral=%v", infra, wf.Ephemeral)
		}
	})

	t.Run("custom done and frozen statuses stay excluded", func(t *testing.T) {
		cfg := workapi.ListConfig{CustomStatuses: []types.CustomStatus{
			{Name: "shipped", Category: types.CategoryDone},
			{Name: "iced", Category: types.CategoryFrozen},
		}}
		f, err := workapi.BuildListFilter(in.ListRequest, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		for _, s := range []types.Status{"shipped", "iced"} {
			if !containsStatus(f.ExcludeStatus, s) {
				t.Errorf("--include-all-types must keep custom status %q excluded, got ExcludeStatus=%v", s, f.ExcludeStatus)
			}
		}
	})
}

func containsStatus(statuses []types.Status, want types.Status) bool {
	for _, s := range statuses {
		if s == want {
			return true
		}
	}
	return false
}

// TestGatherListInput_ExistingFlagsKeepTheirRequestIdentity: an invocation
// that does not name --include-all-types builds the same ListRequest and the
// same filter whether or not the flag is registered, and leaves
// IncludeAllTypes false. --all in particular stays off the type axis: it
// lifts the status and pinned defaults and the default limit only.
func TestGatherListInput_ExistingFlagsKeepTheirRequestIdentity(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	useListTypeAxisConfig(t)

	invocations := [][]string{
		{},
		{"all", "true"},
		{"all", "true", "limit", "5"},
		{"limit", "5"},
		{"include-gates", "true"},
		{"include-templates", "true"},
		{"include-infra", "true"},
		{"include-ephemeral", "true"},
		{"all", "true", "include-infra", "true"},
		{"include-ephemeral", "true", "wisp-type", "heartbeat"},
		{"exclude-type", "epic"},
		{"type", "gate"},
	}
	for _, args := range invocations {
		before := gatherListInputWith(t, newListTypeAxisInputCmd(), args...)
		after := gatherListInputWith(t, newIncludeAllTypesInputCmd(), args...)
		if after.IncludeAllTypes {
			t.Errorf("%v: IncludeAllTypes must stay false unless --include-all-types is given", args)
		}
		if !reflect.DeepEqual(before.ListRequest, after.ListRequest) {
			t.Errorf("%v: ListRequest changed:\nwithout the flag %+v\nwith it          %+v", args, before.ListRequest, after.ListRequest)
		}
		if before.effectiveLimit != after.effectiveLimit {
			t.Errorf("%v: effective limit changed: %d, want %d", args, after.effectiveLimit, before.effectiveLimit)
		}
		beforeFilter, err := workapi.BuildListFilter(before.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("%v: BuildListFilter (without the flag): %v", args, err)
		}
		afterFilter, err := workapi.BuildListFilter(after.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("%v: BuildListFilter (with it): %v", args, err)
		}
		if !reflect.DeepEqual(beforeFilter, afterFilter) {
			t.Errorf("%v: filter changed:\nwithout the flag %+v\nwith it          %+v", args, beforeFilter, afterFilter)
		}
	}

	all := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "all", "true")
	f, err := workapi.BuildListFilter(all.ListRequest, workapi.ListConfig{})
	if err != nil {
		t.Fatalf("BuildListFilter (--all): %v", err)
	}
	if len(f.ExcludeStatus) != 0 || f.Pinned != nil {
		t.Errorf("--all must lift the default status and pinned exclusions, got ExcludeStatus=%v Pinned=%v", f.ExcludeStatus, f.Pinned)
	}
	if !f.SkipWisps || len(f.ExcludeTypes) == 0 || f.IsTemplate == nil || *f.IsTemplate {
		t.Errorf("--all must leave the type and plane suppressions on, got SkipWisps=%v ExcludeTypes=%v IsTemplate=%v", f.SkipWisps, f.ExcludeTypes, f.IsTemplate)
	}
	if all.effectiveLimit != 0 {
		t.Errorf("--all lifts the default limit, got %d", all.effectiveLimit)
	}
	if limited := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "all", "true", "limit", "5"); limited.effectiveLimit != 5 {
		t.Errorf("an explicit --limit wins over --all, got %d", limited.effectiveLimit)
	}
}
