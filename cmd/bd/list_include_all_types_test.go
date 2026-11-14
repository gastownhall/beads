package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
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
