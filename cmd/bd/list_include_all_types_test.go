package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// This file pins `bd list --include-all-types` (bd-2lwkfd): the CLI knob for
// ListRequest.IncludeAllTypes, the union that lifts every default type/plane
// suppression at once. The backend semantics are pinned in
// internal/workapi/list_pinned_test.go; everything here is the reachability
// and the help that makes it usable.

// TestListRegistersIncludeAllTypes is the load-bearing registration check.
// ListRequest.IncludeAllTypes is the one field that skips applyTypeSuppressions
// entirely, so frontends cannot drift from future suppressions — but it was
// unreachable from `bd list`, whose four narrow include flags each lift only
// the suppression they name.
func TestListRegistersIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types: ListRequest.IncludeAllTypes is the union that lifts every default type/plane suppression, and the CLI had no way to set it")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--include-all-types must be a bool flag, got type %s", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--include-all-types must default to false (the default listing suppresses hidden types), got default %q", flag.DefValue)
	}
	// Backticks make pflag widen the flag's help column with an invented value
	// placeholder; a bool flag has no argument to name.
	if strings.Contains(flag.Usage, "`") {
		t.Errorf("--include-all-types usage must not use backticks (pflag would widen the help column): %q", flag.Usage)
	}
	// The union says nothing about status; its help must say so, or a caller
	// will read it as "show me everything" and miss closed rows.
	if !strings.Contains(flag.Usage, "status") {
		t.Errorf("--include-all-types usage must state that default status exclusions still apply: %q", flag.Usage)
	}
}

// TestListAllUsageIsStatusOnly pins the corrected --all help. The old wording
// claimed --all "overrides default filter", but --all lifts only the default
// STATUS exclusions, the default pinned filter and the default limit; the
// type/plane suppressions run regardless of it. With --include-all-types now
// registered, help can name the flag that owns that axis.
func TestListAllUsageIsStatusOnly(t *testing.T) {
	flag := listCmd.Flags().Lookup("all")
	if flag == nil {
		t.Fatal("listCmd has no --all flag registered")
	}
	usage := flag.Usage
	if strings.Contains(usage, "overrides default filter") {
		t.Errorf("--all usage must no longer claim to override the default filter; type/plane suppressions still apply to it: %q", usage)
	}
	// The three effects --all actually has, and the flag that owns the fourth
	// axis it does not touch.
	for _, needle := range []string{"status", "pinned", "limit", "--include-all-types"} {
		if !strings.Contains(usage, needle) {
			t.Errorf("--all usage must mention %q (its actual effects, and the separate type/plane flag): %q", needle, usage)
		}
	}
}

// TestListHelpIsByteIdenticalOutsideIncludeAllTypes proves the new flag leaves
// every other help row byte-for-byte as it was: help rendered with the flag's
// single line removed equals help rendered with the flag hidden, which is what
// the help looked like before the flag existed. A usage change elsewhere in the
// table, or a column the new flag widened (backticks, a value placeholder),
// shows up here as a diff.
func TestListHelpIsByteIdenticalOutsideIncludeAllTypes(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types before its help can be compared")
	}

	withFlag := listCmd.UsageString()

	flag.Hidden = true
	withoutFlag := listCmd.UsageString()
	flag.Hidden = false

	var kept []string
	for _, line := range strings.Split(withFlag, "\n") {
		// The one line the flag owns: pflag prints a long flag with no
		// shorthand as six spaces, two dashes and the name.
		if strings.HasPrefix(line, "      --include-all-types") {
			continue
		}
		kept = append(kept, line)
	}
	if got, want := strings.Join(kept, "\n"), withoutFlag; got != want {
		t.Errorf("help outside the --include-all-types row must be byte-identical to help with the flag hidden.\nwith flag, its row removed:\n%s\nwith flag hidden:\n%s", got, want)
	}
}

// newIncludeAllTypesInputCmd carries the flags the include-all-types wiring
// reads, with listCmd's own --limit default so the limit policy resolves the
// way it does on the real command. gatherListInput reads many other flags,
// but GetString / GetBool on an unregistered flag return zero values, so this
// stays minimal — the same shape as newWispTypeInputCmd.
func newIncludeAllTypesInputCmd(t *testing.T, registerUnion bool, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "list"}
	f := cmd.Flags()
	if registerUnion {
		f.Bool("include-all-types", false, "")
	}
	f.Bool("include-templates", false, "")
	f.Bool("include-gates", false, "")
	f.Bool("include-infra", false, "")
	f.Bool("include-ephemeral", false, "")
	f.StringSlice("exclude-type", nil, "")
	f.Bool("all", false, "")
	f.IntP("limit", "n", workapi.DefaultListLimit, "")
	f.StringP("type", "t", "", "")
	f.String("wisp-type", "", "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// gatherIncludeAllTypesInput runs gatherListInput on a controlled config so
// the limit policy below is the real one, not whatever another test left in
// the process (the same setup as TestListLimitPolicyIsResolvedBeforeTheRequest).
func gatherIncludeAllTypesInput(t *testing.T, args ...string) (listInput, error) {
	return gatherIncludeAllTypesInputVariant(t, true, args...)
}

// gatherIncludeAllTypesInputVariant is the same with the union flag left
// UNREGISTERED, which is what `bd list` looked like before it existed: a
// GetBool on an unregistered flag returns the zero value, so the request a
// caller that never passes the flag builds must be identical either way.
func gatherIncludeAllTypesInputVariant(t *testing.T, registerUnion bool, args ...string) (listInput, error) {
	t.Helper()
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	t.Chdir(t.TempDir())
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	return gatherListInput(newIncludeAllTypesInputCmd(t, registerUnion, args...))
}

// TestGatherListInput_IncludeAllTypesReachesTheFilter pins the wiring, not
// just the registration: a flag that parses but is never read would leave the
// union exactly as unreachable as before. The flag must set ONLY
// ListRequest.IncludeAllTypes, and the resolved request must do what the
// backend pins for that field (TestIncludeAllTypesLiftsEverySuppression):
// no default type exclusions, templates admitted, the wisp plane read — while
// the status/pinned axis and the page limit stay exactly what the same
// command without the flag resolves to.
func TestGatherListInput_IncludeAllTypesReachesTheFilter(t *testing.T) {
	baseline, err := gatherIncludeAllTypesInput(t)
	if err != nil {
		t.Fatalf("gatherListInput (baseline): %v", err)
	}
	union, err := gatherIncludeAllTypesInput(t, "--include-all-types")
	if err != nil {
		t.Fatalf("gatherListInput (--include-all-types): %v", err)
	}

	if !union.IncludeAllTypes {
		t.Error("--include-all-types must set ListRequest.IncludeAllTypes, got false")
	}
	if union.IncludeTemplates || union.IncludeGates || union.IncludeInfra || union.IncludeEphemeral {
		t.Errorf("the union flag must set only IncludeAllTypes, not the narrow knobs: templates=%v gates=%v infra=%v ephemeral=%v",
			union.IncludeTemplates, union.IncludeGates, union.IncludeInfra, union.IncludeEphemeral)
	}
	if baseline.IncludeAllTypes {
		t.Error("IncludeAllTypes must default to false; the default listing keeps every suppression")
	}

	cfg := workapi.ListConfig{}
	fUnion, err := workapi.BuildListFilter(union.ListRequest, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter (union): %v", err)
	}
	if len(fUnion.ExcludeTypes) != 0 {
		t.Errorf("the union lifts every default type exclusion, got ExcludeTypes %v", fUnion.ExcludeTypes)
	}
	if fUnion.IsTemplate != nil {
		t.Errorf("templates must not be hidden, got IsTemplate %v", *fUnion.IsTemplate)
	}
	if fUnion.SkipWisps {
		t.Error("the union must admit the ephemeral wisp plane, got SkipWisps=true")
	}

	// The status axis and the page limit are a different knob: identical to
	// the same command without the flag, whatever that command resolves to.
	fBaseline, err := workapi.BuildListFilter(baseline.ListRequest, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter (baseline): %v", err)
	}
	if !reflect.DeepEqual(fUnion.ExcludeStatus, fBaseline.ExcludeStatus) {
		t.Errorf("default status exclusions must be untouched: union %v, baseline %v", fUnion.ExcludeStatus, fBaseline.ExcludeStatus)
	}
	if len(fUnion.ExcludeStatus) == 0 {
		t.Error("the union must NOT lift the default status exclusions; that is --all's axis")
	}
	if !reflect.DeepEqual(fUnion.Pinned, fBaseline.Pinned) {
		t.Errorf("the pinned default must be untouched: union %v, baseline %v", fUnion.Pinned, fBaseline.Pinned)
	}
	if union.effectiveLimit != baseline.effectiveLimit {
		t.Errorf("the union must not change the limit policy: effectiveLimit %d, want the baseline %d", union.effectiveLimit, baseline.effectiveLimit)
	}
	if union.Limit == nil || baseline.Limit == nil || *union.Limit != *baseline.Limit {
		t.Errorf("the request limit must match the baseline: union %v, baseline %v", union.Limit, baseline.Limit)
	}

	t.Run("an explicit --exclude-type still applies", func(t *testing.T) {
		in, err := gatherIncludeAllTypesInput(t, "--include-all-types", "--exclude-type=gate")
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		filter, err := workapi.BuildListFilter(in.ListRequest, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		// The caller's own exclusion is appended after the suppression
		// function, so the union cannot swallow it.
		if len(filter.ExcludeTypes) != 1 || filter.ExcludeTypes[0] != types.IssueType("gate") {
			t.Errorf("ExcludeTypes = %v, want [gate]: an explicit exclusion outlives the union", filter.ExcludeTypes)
		}
	})

	t.Run("a narrow include flag adds nothing the union lacks", func(t *testing.T) {
		in, err := gatherIncludeAllTypesInput(t, "--include-all-types", "--include-ephemeral")
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if !in.IncludeEphemeral {
			t.Error("--include-ephemeral must still set its own field beside the union")
		}
		filter, err := workapi.BuildListFilter(in.ListRequest, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if !reflect.DeepEqual(filter, fUnion) {
			t.Errorf("union + narrow include must resolve to the union's filter:\n%#v\nwant:\n%#v", filter, fUnion)
		}
	})

	t.Run("--all clears the status axis the union leaves alone", func(t *testing.T) {
		in, err := gatherIncludeAllTypesInput(t, "--include-all-types", "--all")
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if !in.AllFlag || !in.IncludeAllTypes {
			t.Errorf("both flags must survive onto the request: all=%v includeAllTypes=%v", in.AllFlag, in.IncludeAllTypes)
		}
		filter, err := workapi.BuildListFilter(in.ListRequest, cfg)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(filter.ExcludeStatus) != 0 {
			t.Errorf("--all must lift the default status exclusions, got %v", filter.ExcludeStatus)
		}
		if filter.Pinned != nil {
			t.Errorf("--all must lift the default pinned exclusion, got Pinned %v", *filter.Pinned)
		}
		if len(filter.ExcludeTypes) != 0 || filter.IsTemplate != nil || filter.SkipWisps {
			t.Errorf("the union's type/plane lift must survive --all: ExcludeTypes %v IsTemplate %v SkipWisps %v",
				filter.ExcludeTypes, filter.IsTemplate, filter.SkipWisps)
		}
		if in.effectiveLimit != 0 {
			t.Errorf("--all is unlimited unless --limit is explicit, got effectiveLimit %d", in.effectiveLimit)
		}
	})

	t.Run("a configured done status stays excluded", func(t *testing.T) {
		custom := workapi.ListConfig{CustomStatuses: []types.CustomStatus{
			{Name: "archived", Category: types.CategoryDone},
		}}
		filter, err := workapi.BuildListFilter(union.ListRequest, custom)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		found := false
		for _, s := range filter.ExcludeStatus {
			if s == types.Status("archived") {
				found = true
			}
		}
		if !found {
			t.Errorf("a configured done-category status must stay excluded beside the union, got %v", filter.ExcludeStatus)
		}
	})

	t.Run("an explicit infra type keeps the type and both planes", func(t *testing.T) {
		in, err := gatherIncludeAllTypesInput(t, "--include-all-types", "--type", "agent")
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		// An infra type reaches validation as a workspace custom type; the
		// default InfraSet already classifies it for the plane decision.
		infra := workapi.ListConfig{CustomTypes: []string{"agent"}}
		filter, err := workapi.BuildListFilter(in.ListRequest, infra)
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		// Without the union, naming an infra type narrows to its EPHEMERAL
		// rows alone; the union lifts that narrowing, so both planes are read.
		if filter.Ephemeral != nil {
			t.Errorf("the union must remove the ephemeral-only narrowing, got Ephemeral %v", *filter.Ephemeral)
		}
		if filter.SkipWisps {
			t.Error("the union must keep the wisp plane admitted beside an explicit type")
		}
		if filter.IssueType == nil || string(*filter.IssueType) != "agent" {
			t.Errorf("the explicit type must be preserved, got %v", filter.IssueType)
		}
	})
}

// TestGatherListInput_WispTypeWithIncludeAllTypesIsAccepted covers the last
// seam the union flag had to cross: --wisp-type is a predicate, not a plane
// selector, and the CLI guard admits it only when the request already admits
// the wisp plane. --include-all-types IS a plane selector — it skips the plane
// suppression in applyTypeSuppressions — so the refusal must not fire for it.
// The negative case (--wisp-type alone, still refused with the hint naming
// --include-ephemeral) is TestGatherListInput_WispTypeWithoutAPlaneIsRefused,
// which runs unchanged beside this.
func TestGatherListInput_WispTypeWithIncludeAllTypesIsAccepted(t *testing.T) {
	in, err := gatherIncludeAllTypesInput(t, "--wisp-type", "heartbeat", "--include-all-types")
	if err != nil {
		t.Fatalf("--wisp-type with --include-all-types must be accepted (the union admits the plane), got: %v", err)
	}
	if in.WispType == nil || string(*in.WispType) != "heartbeat" {
		t.Errorf("WispType must survive onto the request, got %v", in.WispType)
	}
	if !in.IncludeAllTypes {
		t.Error("IncludeAllTypes must survive onto the request alongside the predicate")
	}
}

// TestGatherListInput_ExistingFlagsKeepTheirRequestIdentity is the control the
// two changes above ride on: every flag combination that omits
// --include-all-types must build the same request and the same filter as it
// did before the flag existed. Each combination is gathered twice on an
// otherwise identical command, once with the union flag unregistered (the old
// surface) and once registered but false (the new surface); only the callers
// that pass the flag may see a difference.
func TestGatherListInput_ExistingFlagsKeepTheirRequestIdentity(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--all"},
		{"--include-templates"},
		{"--include-gates"},
		{"--include-infra"},
		{"--include-ephemeral"},
		{"--all", "--include-ephemeral"},
		{"--limit", "5"},
		{"--all", "--limit", "5"},
	} {
		name := "bare"
		if len(args) > 0 {
			name = strings.Join(args, " ")
		}
		t.Run(name, func(t *testing.T) {
			base, err := gatherIncludeAllTypesInputVariant(t, false, args...)
			if err != nil {
				t.Fatalf("gatherListInput (no union flag, %v): %v", args, err)
			}
			today, err := gatherIncludeAllTypesInputVariant(t, true, args...)
			if err != nil {
				t.Fatalf("gatherListInput (union registered, %v): %v", args, err)
			}
			if !reflect.DeepEqual(base.ListRequest, today.ListRequest) {
				t.Errorf("registering --include-all-types must not change the request for %v:\nbase:  %#v\ntoday: %#v", args, base.ListRequest, today.ListRequest)
			}
			if base.effectiveLimit != today.effectiveLimit {
				t.Errorf("effectiveLimit changed for %v: base %d, today %d", args, base.effectiveLimit, today.effectiveLimit)
			}
			cfg := workapi.ListConfig{}
			fBase, err := workapi.BuildListFilter(base.ListRequest, cfg)
			if err != nil {
				t.Fatalf("BuildListFilter (base, %v): %v", args, err)
			}
			fToday, err := workapi.BuildListFilter(today.ListRequest, cfg)
			if err != nil {
				t.Fatalf("BuildListFilter (today, %v): %v", args, err)
			}
			if !reflect.DeepEqual(fBase, fToday) {
				t.Errorf("registering --include-all-types must not change the filter for %v", args)
			}
		})
	}

	// --all is the axis the reworded help describes; pin that its actual
	// effects are exactly those: the status/pinned defaults and the default
	// limit lift, the type/plane suppressions do not, and the union stays off.
	t.Run("--all lifts status, pinned and the default limit only", func(t *testing.T) {
		in, err := gatherIncludeAllTypesInput(t, "--all")
		if err != nil {
			t.Fatalf("gatherListInput (--all): %v", err)
		}
		if !in.AllFlag {
			t.Fatal("--all must set AllFlag")
		}
		if in.IncludeAllTypes {
			t.Error("--all must not set IncludeAllTypes: the status and type axes are deliberately independent")
		}
		filter, err := workapi.BuildListFilter(in.ListRequest, workapi.ListConfig{})
		if err != nil {
			t.Fatalf("BuildListFilter: %v", err)
		}
		if len(filter.ExcludeTypes) == 0 {
			t.Error("--all must leave the default type exclusions in place; that is --include-all-types' axis")
		}
		if filter.IsTemplate == nil || *filter.IsTemplate {
			t.Errorf("--all must keep templates hidden, got IsTemplate %v", filter.IsTemplate)
		}
		if !filter.SkipWisps {
			t.Error("--all must keep the wisp plane suppressed")
		}
		if len(filter.ExcludeStatus) != 0 {
			t.Errorf("--all must lift the default status exclusions, got %v", filter.ExcludeStatus)
		}
		if filter.Pinned != nil {
			t.Errorf("--all must lift the default pinned exclusion, got Pinned %v", *filter.Pinned)
		}
		if in.effectiveLimit != 0 {
			t.Errorf("--all must lift the default limit, got effectiveLimit %d", in.effectiveLimit)
		}

		limited, err := gatherIncludeAllTypesInput(t, "--all", "--limit", "5")
		if err != nil {
			t.Fatalf("gatherListInput (--all --limit 5): %v", err)
		}
		if limited.effectiveLimit != 5 {
			t.Errorf("an explicit --limit must outrank --all, got effectiveLimit %d", limited.effectiveLimit)
		}
	})
}
