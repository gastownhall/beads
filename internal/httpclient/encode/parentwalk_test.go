// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/parentwalk_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"errors"
	"net/url"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The gate on D4's parent-walk inversion.
//
// It drives the REAL builder rather than hand-written filters, because that is
// the whole claim being made: `bd list --parent` hands the store whatever
// workapi.BuildListFilter produced for the user's flags, and the inversion says
// it can read the intents back out of it. A test that wrote the filter by hand
// would be checking the inversion against a second guess at the derivation, and
// the two could agree while both were wrong.
//
// The four fixtures the spec's pin list names — default rendering, `--all`, the
// `--include-*` variants, and a residual refusal — are the table below.

// walkFilter builds what `bd list --parent <id> <flags>` hands the walk: the
// listing's own filter, with the two members findAllDescendants re-points per
// level applied exactly as it applies them.
func walkFilter(t *testing.T, in issueops.ListRequest, cfg ListConfig) types.IssueFilter {
	t.Helper()
	in.ParentID = "bd-root"
	filter, err := workapi.BuildListFilter(in, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	parent := "bd-root"
	filter.ParentID = &parent
	filter.Limit = 0
	return filter
}

// textListing is what every text rendering of `bd list` sets on top of the
// user's flags (cmd/bd/list.go), so the default fixture is the filter the walk
// really receives rather than a tidier one.
func textListing(in issueops.ListRequest) issueops.ListRequest {
	in.SkipCounts = true
	return in
}

func TestTheParentWalkInvertsTheDerivedDefaults(t *testing.T) {
	cfg := ListConfig{}

	for _, tc := range []struct {
		name string
		in   issueops.ListRequest
		want url.Values
	}{
		{
			// The fixture D4 exists for: a bare `bd list --parent <id>`, whose
			// filter carries ExcludeStatus, Pinned, IsTemplate, the gate and
			// infra exclusions and SkipWisps — none of which the wire publishes,
			// and all of which the server re-derives from their own absence.
			name: "the default text rendering sends only the parent",
			in:   textListing(issueops.ListRequest{}),
			want: url.Values{"parent": {"bd-root"}},
		},
		{
			name: "--all inverts the dropped status exclusions",
			in:   textListing(issueops.ListRequest{AllFlag: true}),
			want: url.Values{"parent": {"bd-root"}, "all": {"true"}},
		},
		{
			// `--status all` reaches the same three absences as `--all` AND, since
			// upstream #5333 stopped an every-status selector from forcing the
			// pinned default, the same pinned reading too. The two spellings are
			// one filter, so they are one wire question.
			name: "--status all is the same question as --all",
			in:   textListing(issueops.ListRequest{Status: "all"}),
			want: url.Values{"parent": {"bd-root"}, "all": {"true"}},
		},
		{
			name: "the three --include-* flags invert to their intents",
			in: textListing(issueops.ListRequest{
				IncludeTemplates: true, IncludeGates: true, IncludeInfra: true,
			}),
			want: url.Values{
				"parent":            {"bd-root"},
				"include_templates": {"true"},
				"include_gates":     {"true"},
				"include_infra":     {"true"},
			},
		},
		{
			name: "--include-gates alone leaves the infra exclusions in place",
			in:   textListing(issueops.ListRequest{IncludeGates: true}),
			want: url.Values{"parent": {"bd-root"}, "include_gates": {"true"}},
		},
		{
			// The fourth intent, and the one the walk could not state until
			// client wave ga-mijra: a wisp-inclusive descendant listing. Its
			// only trace in the built filter is SkipWisps going false, which is
			// exactly what the inversion reads it back off.
			name: "--include-ephemeral inverts to the plane intent",
			in:   textListing(issueops.ListRequest{IncludeEphemeral: true}),
			want: url.Values{"parent": {"bd-root"}, "include_ephemeral": {"true"}},
		},
		{
			// --include-infra admits the plane on its own, so SkipWisps says
			// nothing about the ephemeral flag here and the reading is "unset" —
			// the same one --type gate gets for the gate intent. The
			// re-derivation rejects it if that is ever wrong.
			name: "--include-infra alone does not invent an ephemeral intent",
			in:   textListing(issueops.ListRequest{IncludeInfra: true}),
			want: url.Values{"parent": {"bd-root"}, "include_infra": {"true"}},
		},
		{
			// And the pair is the SAME QUESTION as --include-infra alone, the
			// way `--all --no-pinned` is the same question as `--status all`:
			// both flags admit the plane, so the filter they build is one
			// filter and the walk says so rather than inventing a distinction
			// the re-derivation cannot see. The pairing gc's TierBoth reads
			// need is real on the LISTING, which sends what the caller set —
			// see ListParams and the served two-plane cases.
			name: "--include-ephemeral --include-infra is the same question as --include-infra",
			in:   textListing(issueops.ListRequest{IncludeEphemeral: true, IncludeInfra: true}),
			want: url.Values{"parent": {"bd-root"}, "include_infra": {"true"}},
		},
		{
			name: "an explicit status travels verbatim under its own parameter",
			in:   textListing(issueops.ListRequest{Status: "in_progress"}),
			want: url.Values{"parent": {"bd-root"}, "status": {"in_progress"}},
		},
		{
			// The multi-status spelling is one comma-separated value on both
			// sides, so it round-trips through the same parameter.
			name: "a multi-status filter re-joins into the one status parameter",
			in:   textListing(issueops.ListRequest{Status: "open,in_progress"}),
			want: url.Values{"parent": {"bd-root"}, "status": {"open,in_progress"}},
		},
		{
			// `--type gate` suppresses the gate exclusion on BOTH sides, so its
			// absence must not be read as `--include-gates`.
			name: "an explicit gate type suppresses the gate exclusion on both sides",
			in:   textListing(issueops.ListRequest{IssueType: "gate"}),
			want: url.Values{"parent": {"bd-root"}, "type": {"gate"}},
		},
		{
			name: "the explicit user filters encode under their documented parameters",
			in: textListing(issueops.ListRequest{
				Assignee:       "ada",
				Labels:         []string{"api"},
				LabelsAny:      []string{"p0", "p1"},
				ExcludeLabels:  []string{"wontfix"},
				HasMetadataKey: "epic",
				MetadataFields: map[string]string{"team": "core"},
			}),
			want: url.Values{
				"parent":           {"bd-root"},
				"assignee":         {"ada"},
				"label":            {"api"},
				"label_any":        {"p0", "p1"},
				"exclude_label":    {"wontfix"},
				"has_metadata_key": {"epic"},
				"metadata_field":   {"team=core"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanSearch(walkFilter(t, tc.in, cfg), vocab(cfg))
			if err != nil {
				t.Fatalf("PlanSearch: %v", err)
			}
			if plan.Shape != SearchParentWalk {
				t.Fatalf("shape = %q, want %q", plan.Shape, SearchParentWalk)
			}
			if got := plan.Params.Encode(); got != tc.want.Encode() {
				t.Errorf("params = %q, want %q", got, tc.want.Encode())
			}
		})
	}
}

// TestTheParentWalkRefusesAResidualFilter is the pin list's refusal case: a flag
// whose effect survives into the filter as something no intent reproduces.
//
// Each of these is a NARROWING the wire cannot state, which is the direction
// that matters — dropping one would answer with rows the user asked to hide.
func TestTheParentWalkRefusesAResidualFilter(t *testing.T) {
	cfg := ListConfig{}

	for _, tc := range []struct {
		name string
		in   issueops.ListRequest
		row  string
	}{
		{
			// The canonical residual: --exclude-type appends past the derived
			// gate and infra members, and listIssues publishes no exclude_type.
			name: "--exclude-type",
			in:   textListing(issueops.ListRequest{ExcludeTypes: []string{"chore"}}),
			row:  "P-IssueFilter.ExcludeTypes",
		},
		{
			// --pinned selects the flagged rows, which is a predicate and not an
			// intent; it also suppresses the status exclusions, so the refusal
			// has to be attributed to Pinned rather than to their absence.
			name: "--pinned",
			in:   textListing(issueops.ListRequest{PinnedFlag: true}),
			row:  "P-IssueFilter.Pinned",
		},
		{
			// A residual with a documented parameter is still a residual when
			// the parameter is not this operation's: listIssues publishes no
			// priority.
			name: "--priority",
			in:   textListing(issueops.ListRequest{Priority: intPtr(1)}),
			row:  "P-IssueFilter.Priority",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refusal *RefusedError
			plan, err := PlanSearch(walkFilter(t, tc.in, cfg), vocab(cfg))
			if !errors.As(err, &refusal) {
				t.Fatalf("encoded as %v without a refusal (err=%v)", plan.Params, err)
			}
			if !errors.Is(err, ErrRefused) {
				t.Error("the refusal does not match ErrRefused")
			}
			if refusal.Row.ID != tc.row {
				t.Errorf("refusal cites %q, want %q", refusal.Row.ID, tc.row)
			}
		})
	}
}

// TestTheParentWalkRefusesADerivedMemberNoIntentReaches covers the members the
// CLI cannot produce but the seam can still be handed.
//
// The walk takes a types.IssueFilter, not a set of flags, so "no flag makes
// this" is not a proof that it cannot arrive — a library caller, or a future
// flag, reaches the same seam. Each case starts from a real built filter and
// changes exactly one member, which is what makes the refusal attributable.
func TestTheParentWalkRefusesADerivedMemberNoIntentReaches(t *testing.T) {
	cfg := ListConfig{}
	base := func() types.IssueFilter { return walkFilter(t, textListing(issueops.ListRequest{}), cfg) }

	for _, tc := range []struct {
		name   string
		mutate func(*types.IssueFilter)
		row    string
	}{
		{
			// Templates ONLY. include_templates admits them alongside everything
			// else and cannot select them, so this narrows in a direction the
			// operation has no way to say.
			name:   "selecting templates only",
			mutate: func(f *types.IssueFilter) { yes := true; f.IsTemplate = &yes },
			row:    "P-IssueFilter.IsTemplate",
		},
		{
			name:   "a status exclusion that is not the derived set",
			mutate: func(f *types.IssueFilter) { f.ExcludeStatus = []types.Status{types.StatusBlocked} },
			row:    "P-IssueFilter.ExcludeStatus",
		},
		{
			// Half the infra vocabulary is a user's own exclusion that happens to
			// name infra types, not the derived default — reading it as the
			// default would drop the rest of their exclusion silently.
			name:   "a partial infra exclusion",
			mutate: func(f *types.IssueFilter) { f.ExcludeTypes = []types.IssueType{"gate", "agent"} },
			row:    "P-IssueFilter.ExcludeTypes",
		},
		{
			name:   "an ephemeral predicate on a non-infra listing",
			mutate: func(f *types.IssueFilter) { yes := true; f.Ephemeral = &yes },
			row:    "P-IssueFilter.Ephemeral",
		},
		{
			// SkipWisps HELD while the type exclusions say the infra vocabulary
			// is admitted: BuildListFilter never produces that pair — admitting
			// infra admits the plane — so no intent reproduces it and dropping
			// the opt-out would merge a plane the caller asked to skip.
			name: "holding the wisp plane out while infra is admitted",
			mutate: func(f *types.IssueFilter) {
				f.ExcludeTypes = nil
				f.SkipWisps = true
			},
			row: "P-IssueFilter.SkipWisps",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := base()
			tc.mutate(&filter)

			var refusal *RefusedError
			plan, err := PlanSearch(filter, vocab(cfg))
			if !errors.As(err, &refusal) {
				t.Fatalf("encoded as %v without a refusal (err=%v)", plan.Params, err)
			}
			if refusal.Row.ID != tc.row {
				t.Errorf("refusal cites %q, want %q", refusal.Row.ID, tc.row)
			}
		})
	}
}

// TestTheParentWalkReadsTheAllSpellingsTogetherAndTheNarrowingApart is the
// fourth derived default doing the only job it has left.
//
// It used to separate `--all` from `--status all`. Upstream #5333 removed that
// distinction at the source — an every-status selector no longer forces
// Pinned=false, because doing so hid pinned beads from the one selector that
// promises to hide nothing — so the two spellings now build ONE filter and this
// walk encodes them identically. The case survives inverted: what the pinned
// default still marks is a NARROWING, `--all --no-pinned`, and the wire has no
// parameter for it. Encoding it as `status=all` would have been right before
// #5333 and is now a wider question than the caller asked, so it refuses.
func TestTheParentWalkReadsTheAllSpellingsTogetherAndTheNarrowingApart(t *testing.T) {
	cfg := ListConfig{}
	for _, tc := range []struct {
		name string
		in   issueops.ListRequest
		want string
	}{
		{"--all leaves no pinned default", issueops.ListRequest{AllFlag: true}, "all=true&parent=bd-root"},
		{"--status all is now the same filter, so the same params", issueops.ListRequest{Status: "all"}, "all=true&parent=bd-root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanSearch(walkFilter(t, textListing(tc.in), cfg), vocab(cfg))
			if err != nil {
				t.Fatalf("PlanSearch: %v", err)
			}
			if got := plan.Params.Encode(); got != tc.want {
				t.Errorf("params = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("--all --no-pinned is a narrowing the wire cannot state", func(t *testing.T) {
		var refusal *RefusedError
		plan, err := PlanSearch(walkFilter(t, textListing(issueops.ListRequest{AllFlag: true, NoPinnedFlag: true}), cfg), vocab(cfg))
		if !errors.As(err, &refusal) {
			t.Fatalf("encoded as %v without a refusal (err=%v)", plan.Params, err)
		}
		if refusal.Row.ID != "P-IssueFilter.Pinned" {
			t.Errorf("refusal cites %q, want P-IssueFilter.Pinned", refusal.Row.ID)
		}
	})
}

// TestTheParentWalkRecognizesAConfiguredVocabulary drives the inversion against
// a workspace that configured its own statuses and infra types.
//
// It is the case the "recognition set IS the derivation" claim is really about:
// the derived exclusions are longer, their order is a map's, and the inversion
// still has to read the same intents back out. A hand-written recognizer would
// have to restate the category rule and the fallback rule to get here.
func TestTheParentWalkRecognizesAConfiguredVocabulary(t *testing.T) {
	cfg := ListConfig{
		CustomStatuses: []types.CustomStatus{
			{Name: "shipped", Category: types.CategoryDone},
			{Name: "icebox", Category: types.CategoryFrozen},
			{Name: "reviewing", Category: types.CategoryActive},
		},
		InfraSet: map[string]bool{"agent": true, "role": true, "beacon": true},
	}

	filter := walkFilter(t, textListing(issueops.ListRequest{}), cfg)
	// The precondition this case exists for: the derived exclusions really do
	// carry the configured vocabulary, so recognizing them is not vacuous.
	if len(filter.ExcludeStatus) != 4 || len(filter.ExcludeTypes) != 4 {
		t.Fatalf("the configured vocabulary did not reach the filter: exclude_status=%v exclude_types=%v",
			filter.ExcludeStatus, filter.ExcludeTypes)
	}

	plan, err := PlanSearch(filter, vocab(cfg))
	if err != nil {
		t.Fatalf("PlanSearch: %v", err)
	}
	if got, want := plan.Params.Encode(), (url.Values{"parent": {"bd-root"}}).Encode(); got != want {
		t.Errorf("params = %q, want %q", got, want)
	}

	// A status this workspace configured is a status the wire carries verbatim,
	// and the client must not re-validate it: the server's vocabulary is the
	// authoritative one (L7).
	plan, err = PlanSearch(walkFilter(t, textListing(issueops.ListRequest{Status: "reviewing"}), cfg), vocab(cfg))
	if err != nil {
		t.Fatalf("PlanSearch with a configured status: %v", err)
	}
	if got := plan.Params.Get("status"); got != "reviewing" {
		t.Errorf("status = %q, want the configured value verbatim", got)
	}
}

// TestTheParentWalkSendsNoUnlimitedPage pins the one thing the encoder must
// never emit for this shape: the walk asks for every descendant at each level,
// and `limit=0` is refused outright by a server bound past loopback. Paging to
// exhaustion is the pager's answer, and it owns the parameter.
func TestTheParentWalkSendsNoUnlimitedPage(t *testing.T) {
	plan, err := PlanSearch(walkFilter(t, textListing(issueops.ListRequest{}), ListConfig{}), ZeroVocabulary)
	if err != nil {
		t.Fatalf("PlanSearch: %v", err)
	}
	if _, ok := plan.Params["limit"]; ok {
		t.Errorf("the walk encoded a limit (%v); the pager owns it", plan.Params)
	}
}

// TestTheParentWalkRoundTripsThroughTheServerDecoder closes the loop the
// inversion's whole argument rests on.
//
// The claim is not "these parameters look right". It is that the SERVER, given
// the intents, re-derives the same filter from the same builder — so the walk
// asks the remote store the question the local store was asked. That is what is
// checked here: the encoded parameters go through the production route table and
// query decoder, the ListRequest the server built is read back off a capturing
// role, and THAT is fed through BuildListFilter again. The filter that comes out
// must carry the derived members of the one that went in.
//
// A parameter name this client has wrong is a 400 and fails at the dial. A value
// that decodes to a different intent survives the dial and fails at the
// comparison. Neither can be papered over from this side, because nothing in the
// expectation is written by hand.
func TestTheParentWalkRoundTripsThroughTheServerDecoder(t *testing.T) {
	oracle := startOracle(t)
	cfg := ListConfig{
		CustomStatuses: []types.CustomStatus{{Name: "shipped", Category: types.CategoryDone}},
		CustomTypes:    []string{"agent", "beacon"},
		InfraSet:       map[string]bool{"agent": true, "beacon": true},
	}

	assignee := "ada"
	for _, tc := range []struct {
		name string
		in   issueops.ListRequest
	}{
		{"the default text rendering", textListing(issueops.ListRequest{})},
		{"--all", textListing(issueops.ListRequest{AllFlag: true})},
		{"--status all", textListing(issueops.ListRequest{Status: "all"})},
		{"every --include-* flag", textListing(issueops.ListRequest{IncludeTemplates: true, IncludeGates: true, IncludeInfra: true})},
		{"--include-gates alone", textListing(issueops.ListRequest{IncludeGates: true})},
		{"--include-infra alone", textListing(issueops.ListRequest{IncludeInfra: true})},
		{"--include-ephemeral alone", textListing(issueops.ListRequest{IncludeEphemeral: true})},
		{"--include-ephemeral --include-infra", textListing(issueops.ListRequest{IncludeEphemeral: true, IncludeInfra: true})},
		{"--include-ephemeral with an infra --type", textListing(issueops.ListRequest{IncludeEphemeral: true, IssueType: "agent"})},
		{"--include-templates alone", textListing(issueops.ListRequest{IncludeTemplates: true})},
		{"an explicit status", textListing(issueops.ListRequest{Status: "in_progress"})},
		{"a multi-status filter", textListing(issueops.ListRequest{Status: "open,in_progress"})},
		{"a configured custom status", textListing(issueops.ListRequest{Status: "shipped"})},
		{"--type gate", textListing(issueops.ListRequest{IssueType: "gate"})},
		{"an infra --type", textListing(issueops.ListRequest{IssueType: "agent"})},
		{"a plain --type", textListing(issueops.ListRequest{IssueType: "bug"})},
		{"the explicit user filters", textListing(issueops.ListRequest{
			Assignee:       assignee,
			Labels:         []string{"api", "core"},
			LabelsAny:      []string{"p0"},
			ExcludeLabels:  []string{"wontfix"},
			MetadataFields: map[string]string{"team": "core"},
			HasMetadataKey: "team",
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := walkFilter(t, tc.in, cfg)

			plan, err := PlanSearch(sent, vocab(cfg))
			if err != nil {
				t.Fatalf("PlanSearch: %v", err)
			}

			oracle.reset()
			if status := oracle.dial(t, OpListIssues, Encoded{Params: plan.Params}); status != 200 {
				t.Fatalf("the server answered %d for %q", status, plan.Params.Encode())
			}
			decoded, ok := oracle.captured(t, OpListIssues, "").(issueops.ListRequest)
			if !ok {
				t.Fatalf("captured a %T, want an issueops.ListRequest", decoded)
			}

			// The server's role runs the same builder against the server's own
			// vocabulary; here that is cfg, which is what makes the comparison
			// exact rather than approximate.
			rebuilt, err := workapi.BuildListFilter(decoded, cfg)
			if err != nil {
				t.Fatalf("the server's request does not rebuild: %v", err)
			}

			if row, mismatched := firstDerivedMismatch(sent, rebuilt); mismatched {
				t.Errorf("the server re-derived a different %s:\n  sent    exclude_status=%v exclude_types=%v pinned=%v is_template=%v ephemeral=%v skip_wisps=%v\n  rebuilt exclude_status=%v exclude_types=%v pinned=%v is_template=%v ephemeral=%v skip_wisps=%v",
					row,
					sent.ExcludeStatus, sent.ExcludeTypes, sent.Pinned, sent.IsTemplate, sent.Ephemeral, sent.SkipWisps,
					rebuilt.ExcludeStatus, rebuilt.ExcludeTypes, rebuilt.Pinned, rebuilt.IsTemplate, rebuilt.Ephemeral, rebuilt.SkipWisps)
			}

			// The explicit filters are not derived, so the sweep above says
			// nothing about them: they have to arrive as themselves.
			for _, member := range []struct {
				name       string
				sent, back any
			}{
				{"ParentID", canonicalOf(sent.ParentID), canonicalOf(rebuilt.ParentID)},
				{"Status", canonicalOf(sent.Status), canonicalOf(rebuilt.Status)},
				{"Statuses", canonicalOf(sent.Statuses), canonicalOf(rebuilt.Statuses)},
				{"IssueType", canonicalOf(sent.IssueType), canonicalOf(rebuilt.IssueType)},
				{"Assignee", canonicalOf(sent.Assignee), canonicalOf(rebuilt.Assignee)},
				{"Labels", canonicalOf(sent.Labels), canonicalOf(rebuilt.Labels)},
				{"LabelsAny", canonicalOf(sent.LabelsAny), canonicalOf(rebuilt.LabelsAny)},
				{"ExcludeLabels", canonicalOf(sent.ExcludeLabels), canonicalOf(rebuilt.ExcludeLabels)},
				{"MetadataFields", canonicalOf(sent.MetadataFields), canonicalOf(rebuilt.MetadataFields)},
				{"HasMetadataKey", canonicalOf(sent.HasMetadataKey), canonicalOf(rebuilt.HasMetadataKey)},
			} {
				if !reflect.DeepEqual(member.sent, member.back) {
					t.Errorf("%s: the server rebuilt %#v, the caller sent %#v", member.name, member.back, member.sent)
				}
			}
		})
	}
}

// canonicalOf reduces a filter member to the comparable shape the round-trip
// sweep uses, so the two gates read a nil slice and an empty one the same way.
func canonicalOf(v any) any { return canonical(reflect.ValueOf(v)) }

func intPtr(v int) *int { return &v }

// vocab adapts a recognition set to the lazy accessor PlanSearch takes. The
// laziness is for the resolver's hot path, which reads no vocabulary at all; a
// test that already holds one just hands it back.
func vocab(cfg ListConfig) func() ListConfig { return func() ListConfig { return cfg } }
