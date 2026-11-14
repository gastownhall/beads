package main

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
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

// TestGatherListInput_WispTypeWithIncludeAllTypesIsAccepted: the flag admits
// the wisp plane, so --wisp-type can match rows and the reachability guard
// must let it through. Without it the guard still refuses, naming
// --include-ephemeral.
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
	if !in.IncludeAllTypes {
		t.Error("IncludeAllTypes must survive onto the request alongside --wisp-type")
	}

	t.Run("without it the guard still refuses", func(t *testing.T) {
		cmd := newIncludeAllTypesInputCmd()
		if err := cmd.Flags().Set("wisp-type", "heartbeat"); err != nil {
			t.Fatalf("set wisp-type: %v", err)
		}
		var err error
		stderr := captureStderr(t, func() { _, err = gatherListInput(cmd) })
		if err == nil {
			t.Fatal("--wisp-type with no plane knob must still be refused")
		}
		if !strings.Contains(stderr, "--include-ephemeral") {
			t.Errorf("the refusal must keep naming --include-ephemeral; got:\n%s", stderr)
		}
	})
}

// TestListIncludeAllTypesUsageKeepsTheStatusAxis: the union's help names
// what it lifts (issue types and the wisp plane) and what it does not (the
// default status and pinned exclusions), and points at --all for those.
func TestListIncludeAllTypesUsageKeepsTheStatusAxis(t *testing.T) {
	flag := listCmd.Flags().Lookup("include-all-types")
	if flag == nil {
		t.Fatal("bd list must register --include-all-types")
	}
	// pflag turns a backticked word into the flag's value placeholder, which
	// would print `--include-all-types word` for a bool.
	if strings.Contains(flag.Usage, "`") {
		t.Errorf("--include-all-types usage must not contain backticks, got %q", flag.Usage)
	}
	for _, want := range []string{"issue types", "wisp plane", "status", "pinned", "still apply", "--all"} {
		if !strings.Contains(flag.Usage, want) {
			t.Errorf("--include-all-types usage must mention %q, got %q", want, flag.Usage)
		}
	}
}

// TestListAllUsageIsStatusOnly: --all lifts the default status and pinned
// exclusions and the default limit, nothing on the type axis. Its old help
// ("overrides default filter") read as lifting every default, which sent
// callers looking for gates and wisps to a flag that never shows them.
func TestListAllUsageIsStatusOnly(t *testing.T) {
	flag := listCmd.Flags().Lookup("all")
	if flag == nil {
		t.Fatal("bd list must register --all")
	}
	usage := flag.Usage
	if strings.Contains(usage, "overrides default filter") {
		t.Errorf("--all must not claim to override the default filter: type and plane suppressions still apply; got %q", usage)
	}
	for _, want := range []string{"status", "pinned", "limit", "--include-all-types"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--all usage must mention %q, got %q", want, usage)
		}
	}
}

// TestListHelpIsByteIdenticalOutsideIncludeAllTypes: the new flag adds its
// own line to `bd list --help` and changes no byte of any other. pflag pads
// every line to the widest flag column, so a flag rendered wider than the
// widest one (a backticked word in its usage becomes a placeholder) would
// reflow the whole listing. It compares shown and hidden help at one
// revision; the command-help golden pins the rest against main.
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

// undialableWire is an httpclient wire that records every attempt to reach
// the server and answers none: a refusal decided by the encoder must never
// get this far. Methods it does not override are the embedded nil
// interface's and would panic, which is the same verdict.
type undialableWire struct {
	httpclient.WireClient
	dialed []string
}

func (w *undialableWire) ServerContext(context.Context) (*apigen.ContextResponse, error) {
	w.dialed = append(w.dialed, "ServerContext")
	return nil, errors.New("undialableWire: ServerContext dialed")
}

func (w *undialableWire) Preflight(_ context.Context, op string) error {
	w.dialed = append(w.dialed, "Preflight "+op)
	return errors.New("undialableWire: Preflight dialed")
}

func (w *undialableWire) Do(_ context.Context, req wire.Request, _ any) error {
	w.dialed = append(w.dialed, "Do "+req.Op)
	return errors.New("undialableWire: Do dialed")
}

// TestIncludeAllTypesOverHTTPRefusesWithTheLedgerRow composes the request
// gathered from `bd list --include-all-types` with the `bd serve` HTTP
// backend's reader. The v0 wire publishes only the four narrow include
// intents, so the reader must answer with the encoder's typed refusal
// (ledger row E-ListRequest.IncludeAllTypes), before any dial, rather than a
// narrower page of rows that silently hides the kinds the flag promised.
func TestIncludeAllTypesOverHTTPRefusesWithTheLedgerRow(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	useListTypeAxisConfig(t)

	in := gatherListInputWith(t, newIncludeAllTypesInputCmd(), "include-all-types", "true")

	base, err := url.Parse("https://beads.example.invalid")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	w := &undialableWire{}
	store := httpclient.New(httpclient.Target{BaseURL: base}, w, &apigen.ContextResponse{})
	reader, err := store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}

	page, err := reader.List(context.Background(), in.ListRequest)
	if err == nil {
		t.Fatalf("an HTTP list with --include-all-types must refuse, got a page of %d rows", len(page.Items))
	}
	if len(page.Items) != 0 {
		t.Errorf("a refused list must return no rows, got %d", len(page.Items))
	}
	var inexpressible *httpclient.InexpressibleError
	if !errors.As(err, &inexpressible) {
		t.Fatalf("got %v, want *httpclient.InexpressibleError (the encoder's typed refusal)", err)
	}
	if got := inexpressible.Refused.Row.ID; got != "E-ListRequest.IncludeAllTypes" {
		t.Errorf("the refusal cites ledger row %q, want E-ListRequest.IncludeAllTypes", got)
	}
	if got := inexpressible.Refused.Row.Field; got != "IncludeAllTypes" {
		t.Errorf("the refusal names field %q, want IncludeAllTypes", got)
	}
	if !errors.Is(err, encode.ErrRefused) {
		t.Errorf("the refusal does not classify as encode.ErrRefused: %v", err)
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) || unsup.Backend != httpclient.Backend || unsup.Op != "Reader.List" {
		t.Errorf("the refusal must classify as *storage.ErrUnsupported{Op: Reader.List, Backend: %s}, got %v", httpclient.Backend, err)
	}
	if len(w.dialed) != 0 {
		t.Errorf("a refused list dialed the server: %v", w.dialed)
	}

	t.Run("the same listing without the flag reaches the wire", func(t *testing.T) {
		bare := gatherListInputWith(t, newIncludeAllTypesInputCmd())
		w := &undialableWire{}
		store := httpclient.New(httpclient.Target{BaseURL: base}, w, &apigen.ContextResponse{})
		reader, err := store.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader(): %v", err)
		}
		_, err = reader.List(context.Background(), bare.ListRequest)
		if errors.Is(err, encode.ErrRefused) {
			t.Fatalf("a plain listing must be expressible on the v0 wire, got the refusal %v", err)
		}
		if len(w.dialed) == 0 {
			t.Errorf("a plain listing must reach the wire (the refusal above is specific to the union), got err %v and no dial", err)
		}
	})
}
