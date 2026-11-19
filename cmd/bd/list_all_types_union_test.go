package main

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// These checks extend list_include_all_types_test.go and share its helpers
// (newIncludeAllTypesInputCmd, gatherListInputWith, useListTypeAxisConfig).

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

// TestGatherListInput_IncludeAllTypesChangesOnlyTheTypeAxis compares the
// filter built with and without --include-all-types: the default type and
// plane suppressions (SkipWisps, ExcludeTypes, IsTemplate) are the only
// fields that move, and every other field, the status and pinned defaults
// among them, is identical. TestGatherListInput_IncludeAllTypesReachesTheFilter
// pins their values, the effective limit and the explicit predicates.
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
