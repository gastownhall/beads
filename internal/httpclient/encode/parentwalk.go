// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/parentwalk.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"net/url"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// D4's `bd list --parent` walk, inverted.
//
// THE PROBLEM. The walk hands the store `bd list`'s whole built filter with only
// ParentID and Limit re-pointed per level (cmd/bd/list_show_filter_modes.go
// findAllDescendants), and workapi.BuildListFilter populates ExcludeStatus,
// Pinned, IsTemplate, ExcludeTypes, Ephemeral and SkipWisps on EVERY default
// listing. So the "ParentID-only shape" D4 originally named can never occur, and
// encoding those members verbatim is impossible anyway: listIssues publishes no
// exclude_status, no exclude_type and no is_template.
//
// THE RESOLUTION (escalation ga-2ieek, spec revision 6b428bf63). The wire
// publishes INTENTS — `all`, `include_templates`, `include_gates`,
// `include_infra` — not materialized exclusions, and the server's own role runs
// the SAME builder against the server's authoritative vocabulary. So the bridge
// inverts: it reads the intents back off the derived members, sends those, and
// lets the server re-derive. That is parity-positive as well as expressible —
// the re-derivation heals L7's degraded client-side vocabulary for the walk.
//
// WHY IT VERIFIES RATHER THAN PATTERN-MATCHES. Recognizing each member with its
// own hand-written rule would be a second encoder of BuildListFilter's
// semantics, and a second copy is a thing that can drift — which is exactly the
// objection recorded against inventing a reverse mapping. So the inversion reads
// the intents off the filter and then RE-RUNS THE REAL BUILDER to prove them:
// the intents are accepted only when the filter they produce carries the same
// derived members as the one in hand. The recognition is therefore not a copy of
// the derivation, it IS the derivation.
//
// ANY MEMBER THE RE-DERIVATION DOES NOT ACCOUNT FOR REFUSES, L12-style, citing
// its own ledger row — a user's `--exclude-type`, a `--pinned`, an
// `--include-templates` the intents cannot reach.

// ListConfig is the client-side status and type vocabulary the inversion
// recognizes with. It is workapi's own, because the filter being inverted was
// built from exactly this value: passing anything else would make the
// recognition an approximation of the derivation rather than the derivation.
type ListConfig = workapi.ListConfig

// ZeroVocabulary is the recognition set of an unconfigured workspace: the
// built-in statuses and the default infra types, which is the reading
// BuildListFilter itself gives one. Gates driving the table use it, and so does
// any caller with no store to ask.
func ZeroVocabulary() ListConfig { return ListConfig{} }

// NewListConfig assembles the recognition vocabulary from the three reads that
// are its only wire-visible source (D4's degraded-vocabulary row, L7).
//
// It exists so the STORE does not have to import workapi to build one. The
// bridge subpackage is the allowlisted home for that dependency and the store is
// not, which is the boundary the design's lint row draws — so the store reads
// the three values through its own settings role and hands them here.
func NewListConfig(customStatuses []types.CustomStatus, customTypes []string, infra map[string]bool) ListConfig {
	return ListConfig{CustomStatuses: customStatuses, CustomTypes: customTypes, InfraSet: infra}
}

// The two stand-ins the re-derivation validates with.
//
// BuildListFilter validates `--status` and `--type` against the workspace
// vocabulary, and that check must NOT run here. The filter in hand already
// passed it on the way in, the value is being forwarded to a server that will
// apply its own AUTHORITATIVE vocabulary, and the client's copy is the degraded
// one (L7) — so re-validating client-side could refuse a table-backed custom
// status the server knows perfectly well.
//
// Substituting is exact rather than approximate because the derived members read
// these two fields only through predicates the stand-ins preserve: status
// matters as ""/"all"/"pinned"/"hooked"/other, and type matters as ""/"gate"/an
// infra name/other. Every "other" derives identically, so one representative
// stands for all of them.
const (
	otherStatus = string(types.StatusOpen)
	otherType   = "task"
)

// derivedMember is one member BuildListFilter populates from the intent flags
// rather than from a user's filter, with the ledger row its mismatch cites.
//
// The order is the order the inversion compares in, and it is the order the
// encoder table declares them in, so a filter that diverges in two members
// always refuses on the same one.
var derivedMembers = []struct {
	Field  string
	Ledger string
	Equal  func(got, want types.IssueFilter) bool
}{
	{"ExcludeStatus", "P-IssueFilter.ExcludeStatus", func(g, w types.IssueFilter) bool { return sameSet(g.ExcludeStatus, w.ExcludeStatus) }},
	{"Pinned", "P-IssueFilter.Pinned", func(g, w types.IssueFilter) bool { return boolPtrEqual(g.Pinned, w.Pinned) }},
	{"IsTemplate", "P-IssueFilter.IsTemplate", func(g, w types.IssueFilter) bool { return boolPtrEqual(g.IsTemplate, w.IsTemplate) }},
	{"ExcludeTypes", "P-IssueFilter.ExcludeTypes", func(g, w types.IssueFilter) bool { return sameSet(g.ExcludeTypes, w.ExcludeTypes) }},
	{"Ephemeral", "P-IssueFilter.Ephemeral", func(g, w types.IssueFilter) bool { return boolPtrEqual(g.Ephemeral, w.Ephemeral) }},
	{"SkipWisps", "P-IssueFilter.SkipWisps", func(g, w types.IssueFilter) bool { return g.SkipWisps == w.SkipWisps }},
}

// walkIntents is what one walk filter is recognized as.
type walkIntents struct {
	// Status is the user's `--status` value, forwarded verbatim. Empty means
	// they named none.
	Status string
	// AllFlag, IncludeTemplates, IncludeGates, IncludeInfra and
	// IncludeEphemeral are the five intent booleans listIssues publishes.
	AllFlag          bool
	IncludeTemplates bool
	IncludeGates     bool
	IncludeInfra     bool
	// IncludeEphemeral is the PLANE intent, and it arrived last: the listing
	// published no way to merge the wisp plane when this inversion was written
	// (D9 L1), so a wisp-inclusive walk had nowhere to go and refused. L1 is
	// retired and the member is published, so the intent is read back off
	// SkipWisps here like every other derived default.
	IncludeEphemeral bool
}

// planParentWalk inverts a descendant-walk filter into listIssues parameters.
func planParentWalk(f types.IssueFilter, cfg ListConfig) (url.Values, error) {
	intents, err := recognizeIntents(f, cfg)
	if err != nil {
		return nil, err
	}

	b := newBuilder(OpListIssues, string(SearchParentWalk))
	// The residual sweep runs AFTER the inversion, which is the table's own
	// order: the derived members are declared ahead of the plain refusals, so a
	// filter that diverges in both refuses on the derived one.
	b.refuseInexpressible(f)

	b.str("parent", derefString(f.ParentID))

	// The intents, in place of the exclusions they produced.
	b.str("status", intents.Status)
	b.boolean("all", intents.AllFlag)
	b.boolean("include_templates", intents.IncludeTemplates)
	b.boolean("include_gates", intents.IncludeGates)
	b.boolean("include_infra", intents.IncludeInfra)
	b.boolean("include_ephemeral", intents.IncludeEphemeral)

	// The explicit user filters, under their documented parameters.
	b.strPtr("type", (*string)(f.IssueType))
	b.strPtr("assignee", f.Assignee)
	b.list("label", f.Labels)
	b.list("label_any", f.LabelsAny)
	b.list("exclude_label", f.ExcludeLabels)
	b.timestamp("created_before", f.CreatedBefore)
	b.timestamp("created_after", f.CreatedAfter)
	b.metadata(f.MetadataFields)
	b.str("has_metadata_key", f.HasMetadataKey)

	return b.done()
}

// recognizeIntents reads the intents off f's derived members and proves them by
// re-derivation, or refuses naming the member that does not fit.
//
// EVERY INTENT IS READ, NONE IS SEARCHED FOR. A search over candidate intents
// would find the right answer just as often, but it could not say WHICH member
// was wrong when it found none: every candidate mismatches somewhere, so
// reporting the first one tried would name an arbitrary member and the refusal
// would cite an arbitrary ledger row. Each intent is therefore derived from the
// member that uniquely determines it, ONE candidate is built, and the comparison
// attributes exactly.
//
// `all` is the intent no single member determines: it is read off the absence
// of the status exclusions together with the pinned default.
//
// UPSTREAM #5333 COLLAPSED ONE OF THE READINGS THIS USED TO MAKE. `--status all`
// used to leave the pinned default in place and was told apart from `--all` by
// exactly that; the fix stopped a selector that promises EVERY status from
// silently forcing Pinned=false, so the two spellings now build one identical
// filter and are one question here. What survives is the other direction: a
// pinned default with no status and no exclusions is now reachable only from an
// explicit `--all --no-pinned`, and the wire has no parameter for it, so it
// refuses on P-IssueFilter.Pinned instead of encoding a `status=all` that would
// now ask the server a wider question than the caller did.
func recognizeIntents(f types.IssueFilter, cfg ListConfig) (walkIntents, error) {
	refuse := func(row string) (walkIntents, error) {
		return walkIntents{}, &RefusedError{Op: OpListIssues, Shape: string(SearchParentWalk), Row: RowByID(row)}
	}

	issueType := derefString((*string)(f.IssueType))
	intents := walkIntents{
		Status: explicitStatus(f),

		IncludeTemplates: f.IsTemplate == nil,
		IncludeGates:     !slices.Contains(f.ExcludeTypes, types.IssueType("gate")),
		IncludeInfra:     !carriesEveryInfraType(f.ExcludeTypes, cfg),
		// SkipWisps is the only trace the plane intent leaves in a built
		// filter, so a merged plane is read as `--include-ephemeral` and a
		// skipped one as its absence.
		IncludeEphemeral: !f.SkipWisps,
	}
	// An infra `--type`, or the type `gate` itself, suppresses its exclusions on
	// its own, so their absence says nothing about the flag. Read it as unset:
	// that omits a parameter the server does not need, and the verification
	// below rejects the reading if it is ever wrong.
	if issueType == "gate" {
		intents.IncludeGates = false
	}
	if cfg.IsInfra(issueType) {
		intents.IncludeInfra = false
	}
	// The same rule for the plane, and it has TWO suppressors rather than one:
	// `--include-infra` admits the wisp plane as well as the infra types, and an
	// infra `--type` routes to the plane on its own. Either one leaves SkipWisps
	// false whatever the ephemeral flag was, so under either of them the pair is
	// the same question as the infra intent alone — `--all --no-pinned` and
	// `--status all` again — and inventing the second parameter would be a
	// distinction the re-derivation cannot see.
	if intents.IncludeInfra || cfg.IsInfra(issueType) {
		intents.IncludeEphemeral = false
	}

	// The derivation's own reading of the status, which decides both the
	// exclusion block and the pinned default.
	derived := derivationStatus(intents.Status)
	switch {
	case len(f.ExcludeStatus) > 0:
		// Exclusions survive only with no status and no `--all`.
		intents.AllFlag = false
	case derived != "":
		// A named status suppresses the exclusions itself, so `all` shows only
		// in the pinned default it also suppresses.
		intents.AllFlag = f.Pinned == nil && derived != "pinned" && derived != "hooked"
	case f.Pinned == nil:
		intents.AllFlag = true
	default:
		// No status, no exclusions, and the pinned default still in place. Since
		// upstream #5333 the only spelling that produces all three is
		// `--all --no-pinned`, and the wire can carry neither half of it: `all`
		// alone would drop the narrowing, and `status=all` no longer implies it
		// server-side either. Refused here rather than read as `--status all`
		// and left to the re-derivation below, which would refuse on the same
		// member for a reason a reader would have to reconstruct.
		return refuse("P-IssueFilter.Pinned")
	}

	want, err := workapi.BuildListFilter(issueops.ListRequest{
		ParentID:         derefString(f.ParentID),
		Status:           derived,
		IssueType:        derivationType(issueType, cfg),
		AllFlag:          intents.AllFlag,
		IncludeTemplates: intents.IncludeTemplates,
		IncludeGates:     intents.IncludeGates,
		IncludeInfra:     intents.IncludeInfra,
		IncludeEphemeral: intents.IncludeEphemeral,
	}, cfg)
	if err != nil {
		// The stand-ins are drawn from the built-in vocabulary, so the builder
		// cannot refuse them for being unknown. Reaching here means it refused
		// the SHAPE, which is a client bug rather than a wire limitation — but
		// it is still a refusal rather than a panic, because the alternative is
		// answering a question this bridge has not understood.
		return refuse("P-IssueFilter.unrecognized")
	}
	if row, mismatched := firstDerivedMismatch(f, want); mismatched {
		return refuse(row)
	}
	return intents, nil
}

// firstDerivedMismatch reports the ledger row of the first derived member the
// recognized intents do not reproduce.
func firstDerivedMismatch(got, want types.IssueFilter) (string, bool) {
	for _, member := range derivedMembers {
		if !member.Equal(got, want) {
			return member.Ledger, true
		}
	}
	return "", false
}

// derivationStatus maps a user's status onto the representative the derivation
// behaves identically for. See otherStatus.
func derivationStatus(status string) string {
	switch status {
	case "", "all", "pinned", "hooked":
		return status
	default:
		return otherStatus
	}
}

// derivationType maps a user's type onto the representative the derivation
// behaves identically for. See otherType.
func derivationType(issueType string, cfg ListConfig) string {
	switch {
	case issueType == "":
		return ""
	case issueType == "gate":
		return "gate"
	case cfg.IsInfra(issueType):
		return issueType
	default:
		return otherType
	}
}

// explicitStatus renders the status intent that produced f's status members.
//
// A single status came from a bare `--status` name; several came from one
// comma-separated value, which is also how the wire's `status` parameter carries
// them (the operation reads it with the comma decoder and the role splits it
// again). Joining and re-splitting is therefore a round trip, not a guess.
func explicitStatus(f types.IssueFilter) string {
	if f.Status != nil {
		return string(*f.Status)
	}
	if len(f.Statuses) == 0 {
		return ""
	}
	parts := make([]string, 0, len(f.Statuses))
	for _, s := range f.Statuses {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, ",")
}

// carriesEveryInfraType reports whether the exclusions hold the whole
// client-derivable infra vocabulary, which is what `--include-infra` being off
// produces.
//
// EVERY member, not any: a partial overlap is a user's own `--exclude-type` that
// happens to name an infra type, and reading it as the derived default would
// drop the rest of their exclusion silently.
func carriesEveryInfraType(excluded []types.IssueType, cfg ListConfig) bool {
	infra := cfg.InfraTypes()
	if len(infra) == 0 {
		return false
	}
	for _, t := range infra {
		if !slices.Contains(excluded, types.IssueType(t)) {
			return false
		}
	}
	return true
}

// sameSet compares two exclusion lists as SETS.
//
// Order is not part of the meaning and is not even stable on one side:
// ListConfig.InfraTypes ranges a map when the workspace configures its own, so
// the infra members BuildListFilter appends arrive in a different order run to
// run. A slice comparison would make the inversion flaky on exactly the
// workspaces that configured the vocabulary it exists to respect.
func sameSet[T ~string](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := slices.Clone(a), slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func derefString[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}
