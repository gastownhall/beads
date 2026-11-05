package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// newIncludeAllTypesInputCmd carries only the flags these checks read, in the
// shape of newWispTypeInputCmd: gatherListInput reads unregistered flags as
// zero values.
func newIncludeAllTypesInputCmd() *cobra.Command {
	cmd := newWispTypeInputCmd()
	cmd.Flags().Bool("include-all-types", false, "")
	cmd.Flags().Bool("all", false, "")
	return cmd
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
	if flag.DefValue != "false" {
		t.Fatalf("--include-all-types must default to false, got %q", flag.DefValue)
	}
}

// TestGatherListInput_IncludeAllTypesReachesTheFilter checks the flag is read
// onto the request, and that the filter built from it hides no bead for what
// it is while the status defaults still apply.
func TestGatherListInput_IncludeAllTypesReachesTheFilter(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	cmd := newIncludeAllTypesInputCmd()
	if err := cmd.Flags().Set("include-all-types", "true"); err != nil {
		t.Fatalf("set include-all-types: %v", err)
	}
	in, err := gatherListInput(cmd)
	if err != nil {
		t.Fatalf("gatherListInput: %v", err)
	}
	if !in.IncludeAllTypes {
		t.Fatal("--include-all-types must set ListRequest.IncludeAllTypes, got false")
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
	excludesClosed := false
	for _, s := range filter.ExcludeStatus {
		if s == types.StatusClosed {
			excludesClosed = true
		}
	}
	if !excludesClosed {
		t.Errorf("--include-all-types says nothing about status: closed rows stay hidden without --all, got ExcludeStatus=%v", filter.ExcludeStatus)
	}

	// The default request keeps every suppression, so the plain listing is
	// unchanged.
	bare, err := gatherListInput(newIncludeAllTypesInputCmd())
	if err != nil {
		t.Fatalf("gatherListInput (bare): %v", err)
	}
	if bare.IncludeAllTypes {
		t.Fatal("IncludeAllTypes must default to false")
	}
	bareFilter, err := workapi.BuildListFilter(bare.ListRequest, workapi.ListConfig{})
	if err != nil {
		t.Fatalf("BuildListFilter (bare): %v", err)
	}
	if !bareFilter.SkipWisps || len(bareFilter.ExcludeTypes) == 0 {
		t.Errorf("the default listing must keep the type suppressions, got SkipWisps=%v ExcludeTypes=%v", bareFilter.SkipWisps, bareFilter.ExcludeTypes)
	}
}

// TestGatherListInput_WispTypeWithIncludeAllTypesIsAccepted: the flag admits
// the wisp plane, so --wisp-type can match rows and the reachability guard
// must let it through.
func TestGatherListInput_WispTypeWithIncludeAllTypesIsAccepted(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	cmd := newIncludeAllTypesInputCmd()
	if err := cmd.Flags().Set("wisp-type", "heartbeat"); err != nil {
		t.Fatalf("set wisp-type: %v", err)
	}
	if err := cmd.Flags().Set("include-all-types", "true"); err != nil {
		t.Fatalf("set include-all-types: %v", err)
	}
	var in listInput
	var err error
	_ = captureStderr(t, func() { in, err = gatherListInput(cmd) })
	if err != nil {
		t.Fatalf("--wisp-type with --include-all-types must be accepted, got: %v", err)
	}
	if in.WispType == nil || string(*in.WispType) != "heartbeat" {
		t.Errorf("WispType must survive onto the request, got %v", in.WispType)
	}
}

// TestListHelpIsByteIdenticalOutsideIncludeAllTypes: the new flag adds its
// own line to `bd list --help` and changes no byte of any other. pflag pads
// every line to the widest flag column, so a flag rendered wider than the
// widest one (a backticked word in its usage becomes a placeholder) would
// reflow the whole listing.
func TestListHelpIsByteIdenticalOutsideIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types")
	}
	shown := listCmd.Flags().FlagUsages()
	defer func(prev bool) { flag.Hidden = prev }(flag.Hidden)
	flag.Hidden = true
	hidden := listCmd.Flags().FlagUsages()

	var others []string
	removed := 0
	for _, line := range strings.Split(shown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--include-all-types") {
			removed++
			continue
		}
		others = append(others, line)
	}
	if removed != 1 {
		t.Fatalf("want one --include-all-types help line, got %d", removed)
	}
	if got := strings.Join(others, "\n"); got != hidden {
		t.Errorf("--include-all-types changed other bd list help lines:\n--- with it (its line removed)\n%s\n--- without it\n%s", got, hidden)
	}
}

// TestGatherListInput_ExistingFlagsKeepTheirRequestIdentity: an invocation
// that does not name --include-all-types builds the same ListRequest and the
// same filter whether or not the flag is registered, and leaves
// IncludeAllTypes false. --all in particular stays a status switch.
func TestGatherListInput_ExistingFlagsKeepTheirRequestIdentity(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	withoutFlag := func() *cobra.Command {
		cmd := newWispTypeInputCmd()
		cmd.Flags().Bool("all", false, "")
		cmd.Flags().Bool("include-gates", false, "")
		cmd.Flags().Bool("include-templates", false, "")
		return cmd
	}
	withFlag := func() *cobra.Command {
		cmd := withoutFlag()
		cmd.Flags().Bool("include-all-types", false, "")
		return cmd
	}
	invocations := [][]string{
		{},
		{"all", "true"},
		{"include-gates", "true"},
		{"include-templates", "true"},
		{"include-infra", "true"},
		{"include-ephemeral", "true"},
		{"all", "true", "include-infra", "true"},
		{"include-ephemeral", "true", "wisp-type", "heartbeat"},
		{"type", "gate"},
	}
	gather := func(cmd *cobra.Command, args []string) listInput {
		t.Helper()
		for i := 0; i < len(args); i += 2 {
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
	for _, args := range invocations {
		before := gather(withoutFlag(), args)
		after := gather(withFlag(), args)
		if after.IncludeAllTypes {
			t.Errorf("%v: IncludeAllTypes must stay false unless --include-all-types is given", args)
		}
		if !reflect.DeepEqual(before.ListRequest, after.ListRequest) {
			t.Errorf("%v: ListRequest changed:\nwithout the flag %+v\nwith it          %+v", args, before.ListRequest, after.ListRequest)
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
}
