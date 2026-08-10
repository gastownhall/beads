package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.Reader must satisfy. There are three wirings — the direct store,
// the embedded store, and the unit-of-work backend — but only TWO bodies: the
// two store accessors both return workapi/storereader.New(store), so the
// store-backed body gets two votes that are one vote. The genuinely separate
// implementation is internal/storage/uow/issue_reader.go. The embedded wiring
// still earns its place: it runs the same body against a different engine, and
// engine-level disagreement (SQL dialect, staging, connection wrapper) is the
// class it has caught before.
//
// WHAT THIS CONTRACT PINS: the QUERY-SEAM RESIDUE. Whether each backend's query
// seam honors the request the shared builder produced, end to end against a
// real database, and whether the implementations agree on the resulting page
// shape and boundary behavior. The existing fake-based comparison test
// (internal/storage/uow/issue_reader_test.go) drives both bodies through stub
// seams and therefore cannot see any of that; it stays where it is, as the unit
// pin for the epilogue, and this contract does not restate it.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, and why each one is already
// owned somewhere else — re-pinning them here is the duplicate-scenario
// anti-pattern engdocs/TESTING.md names:
//
//   - PER-FIELD REQUEST-TO-FILTER MAPPING. Both requests are turned into
//     storage filters by ONE builder pair, workapi.BuildReadyFilter and
//     workapi.BuildListFilter, which every implementation calls and which
//     internal/workapi's golden files pin field by field
//     (ready_golden_test.go, list_golden_test.go,
//     testdata/list_filter_golden.json). A case here that asserted "Assignee
//     reaches the query" would be asserting the golden's job through a Dolt
//     server. The residue this file DOES pin is the part the goldens cannot
//     see: what the SEAM does with the filter once it has it — the type
//     exclusions the ready WHERE clause applies when Type is empty, the
//     fetch-all-then-trim for a sort SQL cannot express, the keyset predicate,
//     the has-more verdict at the page boundary.
//
//   - THE PAGE EPILOGUE ITSELF. Sort, trim and the truncation verdict are
//     workapi.FinishPage, one function with four callers, unit-tested in
//     workapi/page_test.go. The boundary cases below assert the ANSWER a
//     request gets, which is where the two seams feed FinishPage differently
//     (the store body over-fetches one row; the uow body reports has-more
//     natively) — not the epilogue's internals.
//
//   - THE GET RESOLVE. workapi.GetIssueOrWisp is shared with Commenter and
//     Relations and has its own tests. What is pinned here is that a Reader
//     request reaches it and that its verdicts survive the trip: exact id only,
//     the wisp fallback, ErrNotFound, and no decay of a backend failure into
//     not-found.
//
//   - LABEL PATTERN / REGEX / METADATA-KEY MATCHING SEMANTICS. Those are
//     storage-surface behaviors pinned by the audit leaves; the role adds no
//     promise on top of them.
//
//   - THE CONFIGURED INFRA-TYPE VOCABULARY. Which types a workspace calls
//     infra is read through a per-store cache
//     (dolt/config.go:194-214) whose invalidation is a storage concern, not a
//     role promise. The infra assertion below therefore uses the plane the
//     default listing suppresses, not a reconfigured vocabulary.
//
//   - THE NEVER-MUTATE-CALLER-REQUEST RULE, beyond the single tripwire
//     RunReaderDoesNotMutateTheCallerRequest. reader.go:392 makes it a promise,
//     so it gets one case for the whole role; it is a property of the shared
//     implementation, not of each method, and asserting it per method would
//     triple the cost for no new information.
//
// THE DOC COMMENT IS THE SPEC. Every case below asserts what
// issueops/reader.go promises, not what any implementation currently does. A
// backend that disagrees is parked at its own wiring site with
// skipKnownDivergence and a bead, never by weakening the case.

// ReaderFixture supplies adapter-specific storage access for the reader
// assertions. It carries only the hooks these cases use: the role is read-only,
// so every assertion is on the returned page or detail view and none of them
// needs a scalar query.
//
// AddComment is the one hook the shared per-backend role kit does not expose
// (bd-kue5t's kit has CreateIssue/CreateWisp/AddDependency/SetConfig/
// QueryScalar/CountHistory). The Get detail view carries a comment count and an
// opt-in comment list, so this contract cannot assert either half without a way
// to put a comment on an issue. Each wiring supplies it locally; see the S0
// follow-up noted in this slice's report.
type ReaderFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so all of them can
	// share one database. Every case below scopes its query to its own seeded
	// rows — by id set, by label, or by exact id — because a list request with
	// no scope answers with the whole workspace, and on the unit-of-work
	// backend that workspace is shared by every case in the suite.
	IssuePrefix string
	Reader      publicops.Reader
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds one edge and recomputes the source's blocked state,
	// which is what makes the ready and --ready queries answer differently.
	AddDependency func(context.Context, *types.Dependency, string) error
	// AddComment puts one comment on a durable issue.
	AddComment func(ctx context.Context, issueID, author, text string) error
}

// RunReaderReadyDefaultTypeExclusionsYieldToAnExplicitType pins
// ReadyRequest.IssueType's three-part promise (reader.go:30-34): the default
// type exclusions hide a gate; naming the type drops those exclusions AND the
// caller's own ExcludeTypes along with them; and a type nobody has heard of
// matches nothing rather than failing.
//
// The last third is the asymmetry with ListRequest.IssueType, which validates
// against the workspace vocabulary and errors — see
// RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary, whose comment names
// the other side of it.
//
// THE DEFAULT EXCLUSION IS A SET, not a type. It seeded one member — the gate —
// so dropping any OTHER member from that set left every assertion here true,
// and the molecule was the member with no observer anywhere: a molecule is a
// container for its steps rather than a piece of work, so a ready listing that
// started offering them offers work that cannot be done. Both members are
// seeded now, and each is separately shown to come back when its own type is
// named, so a set that lost one is caught by a row rather than by nothing.
func RunReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdytype")
	task := readerID(fixture, "rdytype", "task")
	gate := readerID(fixture, "rdytype", "gate")
	molecule := readerID(fixture, "rdytype", "molecule")
	seedReaderIssue(t, ctx, fixture, readerIssue(task, types.TypeTask, scope))
	seedReaderIssue(t, ctx, fixture, readerIssue(gate, types.TypeGate, scope))
	seedReaderIssue(t, ctx, fixture, readerIssue(molecule, types.TypeMolecule, scope))

	page, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{Labels: []string{scope}})
	if err != nil {
		t.Fatalf("Ready with no type: %v", err)
	}
	assertReaderPageIDSet(t, "Ready with no type", page, []string{task})

	// Naming the type drops the default exclusions. ExcludeTypes goes with
	// them: the field's doc says it is ignored when IssueType is set, so
	// asking for gates while excluding gates still answers with the gate.
	for _, excluded := range []struct {
		issueType types.IssueType
		want      string
	}{
		{types.TypeGate, gate},
		{types.TypeMolecule, molecule},
	} {
		page, err = fixture.Reader.Ready(ctx, publicops.ReadyRequest{
			Labels:       []string{scope},
			IssueType:    string(excluded.issueType),
			ExcludeTypes: []string{string(excluded.issueType)},
		})
		if err != nil {
			t.Fatalf("Ready with IssueType=%s: %v", excluded.issueType, err)
		}
		assertReaderPageIDSet(t,
			"Ready with IssueType="+string(excluded.issueType)+" and ExcludeTypes=["+string(excluded.issueType)+"]",
			page, []string{excluded.want})
	}

	page, err = fixture.Reader.Ready(ctx, publicops.ReadyRequest{
		Labels:    []string{scope},
		IssueType: "no-such-type-anywhere",
	})
	if err != nil {
		t.Fatalf("Ready with an unrecognized type must match nothing rather than fail: %v", err)
	}
	assertReaderPageIDSet(t, "Ready with an unrecognized type", page, nil)
}

// RunReaderReadyDeferredAndEphemeralGates pins the two admission flags
// (reader.go:62-65). Both default to off and each admits exactly its own class:
// IncludeDeferred does not let the wisp in and IncludeEphemeral does not let the
// deferred row in.
func RunReaderReadyDeferredAndEphemeralGates(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdygate")
	plain := readerID(fixture, "rdygate", "plain")
	deferred := readerID(fixture, "rdygate", "deferred")
	wisp := readerID(fixture, "rdygate", "wisp")

	later := time.Now().UTC().Add(24 * time.Hour)
	deferredIssue := readerIssue(deferred, types.TypeTask, scope)
	deferredIssue.DeferUntil = &later
	wispIssue := readerIssue(wisp, types.TypeTask, scope)
	wispIssue.Ephemeral = true

	seedReaderIssue(t, ctx, fixture, readerIssue(plain, types.TypeTask, scope))
	seedReaderIssue(t, ctx, fixture, deferredIssue)
	seedReaderWisp(t, ctx, fixture, wispIssue)

	for _, test := range []struct {
		name string
		req  publicops.ReadyRequest
		want []string
	}{
		{"neither gate open", publicops.ReadyRequest{Labels: []string{scope}}, []string{plain}},
		{"IncludeDeferred", publicops.ReadyRequest{Labels: []string{scope}, IncludeDeferred: true}, []string{plain, deferred}},
		{"IncludeEphemeral", publicops.ReadyRequest{Labels: []string{scope}, IncludeEphemeral: true}, []string{plain, wisp}},
	} {
		page, err := fixture.Reader.Ready(ctx, test.req)
		if err != nil {
			t.Fatalf("Ready (%s): %v", test.name, err)
		}
		assertReaderPageIDSet(t, "Ready ("+test.name+")", page, test.want)
	}
}

// RunReaderReadyLimitBoundary walks the whole Limit vocabulary
// (reader.go:85-100) against the page shape it produces (reader.go:376-381).
//
// This is the one case that exercises a genuine two-implementation seam rather
// than one body twice. The store-backed body has no has-more of its own, so it
// asks the query for one row past the page and lets the extra row's presence be
// the answer (workapi/storereader/reader.go:62-67). The unit-of-work body's
// seam reports has-more natively and feeds that verdict into the same epilogue
// (uow/issue_reader.go:58-68). The two must agree exactly AT the boundary, which
// is where an off-by-one in either mechanism shows: three rows, asked for two,
// three, all, and by default.
//
// Offset is the neighboring knob and is deliberately NOT asserted here, so
// that a body which conflated the two knobs fails one case rather than
// muddying both. RunReaderOffsetSkipsTheRowsBeforeThePage owns it.
func RunReaderReadyLimitBoundary(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdylimit")
	var ids []string
	for _, tag := range []string{"a", "b", "c"} {
		id := readerID(fixture, "rdylimit", tag)
		ids = append(ids, id)
		seedReaderIssue(t, ctx, fixture, readerIssue(id, types.TypeTask, scope))
	}

	for _, test := range []struct {
		name    string
		limit   *int
		wantN   int
		hasMore bool
	}{
		{"unset takes the shared default, which does not truncate three rows", nil, 3, false},
		{"explicit 0 is unlimited", readerLimit(0), 3, false},
		{"a limit under the result count truncates and says so", readerLimit(2), 2, true},
		{"a limit exactly at the result count hides nothing", readerLimit(3), 3, false},
	} {
		page, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{Labels: []string{scope}, Limit: test.limit})
		if err != nil {
			t.Fatalf("Ready (%s): %v", test.name, err)
		}
		assertReaderPageNotNil(t, "Ready ("+test.name+")", page)
		if len(page.Items) != test.wantN {
			t.Errorf("Ready (%s) returned %d items %v, want %d", test.name, len(page.Items), readerPageIDs(page), test.wantN)
		}
		if page.HasMore != test.hasMore {
			t.Errorf("Ready (%s) HasMore = %v, want %v", test.name, page.HasMore, test.hasMore)
		}
		for _, got := range readerPageIDs(page) {
			if !slices.Contains(ids, got) {
				t.Errorf("Ready (%s) returned %s, which this case did not seed", test.name, got)
			}
		}
	}
}

// RunReaderOffsetSkipsTheRowsBeforeThePage pins ReadyRequest.Offset and
// ListRequest.Offset: the page is the TAIL of the unpaged answer, in the
// unpaged order, on every implementation.
//
// It used to be weaker — "honored or refused" — because the two bodies did
// disagree: the unit-of-work one rendered LIMIT/OFFSET and the store-backed one
// rendered LIMIT only and refused rather than answering the first page again.
// A capability that is either served or refused is not a semantics the caller
// can write against, so the split was closed rather than described: both bodies
// now reach past the skipped rows and drop them in the shared page epilogue.
// The spec gap this grew out of (bd-yby99.7) was the weakest form of the same
// thing — three matching rows and Offset 0/1/2 coming back 3/3/3 with no error
// for a pager to notice.
//
// THE WALK IS THE ASSERTION. Every offset from 0 to one past the end is driven,
// so a body that skipped a fixed number, skipped before the page bound, or
// stopped skipping at the end fails on one of them. Both requests name a TOTAL
// order over rows seeded minutes apart, so the expected tail is not storage
// order wobbling between two calls.
func RunReaderOffsetSkipsTheRowsBeforeThePage(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "offset")
	var ids []string
	base := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	for i, tag := range []string{"a", "b", "c"} {
		id := readerID(fixture, "offset", tag)
		ids = append(ids, id)
		issue := readerIssue(id, types.TypeTask, scope)
		at := base.Add(time.Duration(i) * time.Minute)
		issue.CreatedAt = at
		issue.UpdatedAt = at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	idScope := readerIDFilter(ids...)

	for _, test := range []struct {
		what string
		call func(offset int) (publicops.IssuePage, error)
	}{
		{"Ready", func(offset int) (publicops.IssuePage, error) {
			return fixture.Reader.Ready(ctx, publicops.ReadyRequest{
				Labels: []string{scope}, Sort: "oldest", Offset: offset,
			})
		}},
		{"List", func(offset int) (publicops.IssuePage, error) {
			return fixture.Reader.List(ctx, publicops.ListRequest{
				IDFilter: idScope, SortBy: "created", Offset: offset,
			})
		}},
	} {
		// Offset 0 is not a page request, so it is the baseline every paged
		// call is a suffix of. It is READ rather than assumed: the two sorts
		// name a total order, but which end each starts from is the query's
		// business and not this case's.
		unpaged, err := test.call(0)
		if err != nil {
			t.Fatalf("%s at Offset 0: %v", test.what, err)
		}
		whole := readerPageIDs(unpaged)
		if len(whole) != len(ids) {
			t.Fatalf("%s at Offset 0 returned %v, want the three seeded rows", test.what, whole)
		}

		for offset := 1; offset <= len(ids); offset++ {
			paged, err := test.call(offset)
			if err != nil {
				t.Errorf("%s at Offset %d: %v", test.what, offset, err)
				continue
			}
			if paged.Items == nil {
				t.Errorf("%s at Offset %d returned a nil Items; an offset past the end is an empty page, not a null one",
					test.what, offset)
			}
			if got, want := readerPageIDs(paged), whole[offset:]; !slices.Equal(got, want) {
				t.Errorf("%s at Offset %d = %v, want %v — the tail of the unpaged answer %v",
					test.what, offset, got, want, whole)
			}
		}
	}
}

// RunReaderReadySortPoliciesOrderTheSameRows pins that the two concrete ready
// policies order identically across implementations (reader.go:79-83). The rows
// are chosen so the policies disagree with each other: the newer row is the
// higher priority, so "priority" leads with it and "oldest" leads with the other.
// A seam that ignored the policy would answer both requests the same way and
// fail one of them.
//
// The empty-sort fallback is deliberately NOT pinned. The doc says it resolves
// to hybrid at the storage layer and that no front door should rely on it, and
// hybrid's order depends on a wall-clock recency cutoff
// (sqlbuild/ready.go:57-65) — pinning it would both contradict the doc and be
// time-dependent.
func RunReaderReadySortPoliciesOrderTheSameRows(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdysort")
	urgent := readerID(fixture, "rdysort", "urgent")
	ancient := readerID(fixture, "rdysort", "ancient")

	now := time.Now().UTC()
	urgentIssue := readerIssue(urgent, types.TypeTask, scope)
	urgentIssue.Priority = 0
	urgentIssue.CreatedAt = now.Add(-1 * time.Hour)
	ancientIssue := readerIssue(ancient, types.TypeTask, scope)
	ancientIssue.Priority = 3
	ancientIssue.CreatedAt = now.Add(-90 * 24 * time.Hour)

	seedReaderIssue(t, ctx, fixture, urgentIssue)
	seedReaderIssue(t, ctx, fixture, ancientIssue)

	for _, test := range []struct {
		policy string
		want   []string
	}{
		{"priority", []string{urgent, ancient}},
		{"oldest", []string{ancient, urgent}},
	} {
		page, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{Labels: []string{scope}, Sort: test.policy})
		if err != nil {
			t.Fatalf("Ready --sort %s: %v", test.policy, err)
		}
		assertReaderPageIDs(t, "Ready --sort "+test.policy, page, test.want)
	}
}

// RunReaderListDefaultExclusionsAndTheirOverrides pins what the default listing
// hides and which knob takes each exclusion back off (reader.go:117-123,
// reader.go:182-188, reader.go:204).
//
// The load-bearing half is that Status and AllFlag REPLACE the status
// exclusions rather than fighting them, and that they replace ONLY those: a
// caller who asks for everything still does not get gates, templates or the
// ephemeral plane, each of which has its own knob.
//
// PINNED IS TWO EXCLUSIONS, not one, and the leaf says so (reader.go:109-115).
// A row can be pinned by STATUS or by the separate Pinned FLAG, each is hidden
// by its own predicate, and each has a different knob: `Status: pinned` for the
// first, PinnedFlag for the second. The table seeds one of each — plus the two
// rows carrying BOTH, which are the only rows that can tell the predicates
// apart when a request lifts one of them and not the other.
//
// (This was a spec gap, bd-yby99.16: the enumeration named status, template,
// gate and infra but not pinned, so no answer could be asserted without
// inventing a promise. The owner ruled the doc was lagging the code.)
func RunReaderListDefaultExclusionsAndTheirOverrides(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	open := readerID(fixture, "lsdef", "open")
	closed := readerID(fixture, "lsdef", "closed")
	gate := readerID(fixture, "lsdef", "gate")
	template := readerID(fixture, "lsdef", "template")
	wisp := readerID(fixture, "lsdef", "wisp")
	infraWisp := readerID(fixture, "lsdef", "infrawisp")
	flagOnly := readerID(fixture, "lsdef", "flagonly")
	statusOnly := readerID(fixture, "lsdef", "statusonly")
	flagAndStatus := readerID(fixture, "lsdef", "flagandstatus")
	flagAndClosed := readerID(fixture, "lsdef", "flagandclosed")

	closedIssue := readerIssue(closed, types.TypeTask, "")
	closedIssue.Status = types.StatusClosed
	templateIssue := readerIssue(template, types.TypeTask, "")
	templateIssue.IsTemplate = true
	wispIssue := readerIssue(wisp, types.TypeTask, "")
	wispIssue.Ephemeral = true
	// The SAME plane, an EXCLUDED TYPE. `message` is one of the built-in infra
	// types, so this row is hidden twice over — once by the plane and once by
	// the type — and it is the only row that can tell the two knobs apart. The
	// wisp above is a task: it comes back under either one, so a body that
	// wired the plane knob to the type branch answers every other row here
	// correctly.
	infraWispIssue := readerIssue(infraWisp, types.TypeMessage, "")
	infraWispIssue.Ephemeral = true
	// Open, pinned only by the FLAG: no status exclusion can hide it, so it
	// isolates the flag predicate.
	flagOnlyIssue := readerIssue(flagOnly, types.TypeTask, "")
	flagOnlyIssue.Pinned = true
	// Pinned only by STATUS: the flag predicate cannot hide it, so it isolates
	// the status exclusion.
	statusOnlyIssue := readerIssue(statusOnly, types.TypeTask, "")
	statusOnlyIssue.Status = types.StatusPinned
	// Both, so a request that lifts only the status half still has a row the
	// flag half would hide — which is what makes "Status pinned drops the flag
	// predicate too" an observation rather than a restatement.
	flagAndStatusIssue := readerIssue(flagAndStatus, types.TypeTask, "")
	flagAndStatusIssue.Status = types.StatusPinned
	flagAndStatusIssue.Pinned = true
	// Hidden twice over the other way round, so PinnedFlag dropping the status
	// exclusions on its way is observable too.
	flagAndClosedIssue := readerIssue(flagAndClosed, types.TypeTask, "")
	flagAndClosedIssue.Status = types.StatusClosed
	flagAndClosedIssue.Pinned = true

	seedReaderIssue(t, ctx, fixture, readerIssue(open, types.TypeTask, ""))
	seedReaderIssue(t, ctx, fixture, closedIssue)
	seedReaderIssue(t, ctx, fixture, readerIssue(gate, types.TypeGate, ""))
	seedReaderIssue(t, ctx, fixture, templateIssue)
	seedReaderWisp(t, ctx, fixture, wispIssue)
	seedReaderWisp(t, ctx, fixture, infraWispIssue)
	seedReaderIssue(t, ctx, fixture, flagOnlyIssue)
	seedReaderIssue(t, ctx, fixture, statusOnlyIssue)
	seedReaderIssue(t, ctx, fixture, flagAndStatusIssue)
	seedReaderIssue(t, ctx, fixture, flagAndClosedIssue)

	scope := readerIDFilter(open, closed, gate, template, wisp, infraWisp, flagOnly, statusOnly, flagAndStatus, flagAndClosed)
	for _, test := range []struct {
		name string
		req  publicops.ListRequest
		want []string
	}{
		{"default", publicops.ListRequest{IDFilter: scope}, []string{open}},
		{"AllFlag replaces the status exclusions only", publicops.ListRequest{IDFilter: scope, AllFlag: true}, []string{open, closed, flagOnly, statusOnly, flagAndStatus, flagAndClosed}},
		{"Status replaces the status exclusions", publicops.ListRequest{IDFilter: scope, Status: string(types.StatusClosed)}, []string{closed}},
		{"Status pinned drops the flag predicate too", publicops.ListRequest{IDFilter: scope, Status: string(types.StatusPinned)}, []string{statusOnly, flagAndStatus}},
		{"PinnedFlag answers the flagged rows at any status", publicops.ListRequest{IDFilter: scope, PinnedFlag: true}, []string{flagOnly, flagAndStatus, flagAndClosed}},
		{"NoPinnedFlag changes nothing on a default listing", publicops.ListRequest{IDFilter: scope, NoPinnedFlag: true}, []string{open}},
		{"NoPinnedFlag holds the flag predicate under AllFlag", publicops.ListRequest{IDFilter: scope, AllFlag: true, NoPinnedFlag: true}, []string{open, closed, statusOnly}},
		{"IncludeGates", publicops.ListRequest{IDFilter: scope, IncludeGates: true}, []string{open, gate}},
		{"IncludeTemplates", publicops.ListRequest{IDFilter: scope, IncludeTemplates: true}, []string{open, template}},
		// IncludeInfra is BOTH knobs at once: it admits the plane and takes the
		// infra-type exclusion off, so it is the only request here that reaches
		// the infra-typed wisp.
		{"IncludeInfra reaches the ephemeral plane and the infra types", publicops.ListRequest{IDFilter: scope, IncludeInfra: true}, []string{open, wisp, infraWisp}},
		// The plane knob alone. It admits the plane and takes NO exclusion off
		// with it: the gate and the template stay hidden, and so does the
		// infra-typed wisp in the very plane it just admitted. That last row is
		// the one that makes this an observation — a field wired to
		// IncludeInfra's branch answers with it.
		{"IncludeEphemeral reaches the plane and takes no type exclusion off", publicops.ListRequest{IDFilter: scope, IncludeEphemeral: true}, []string{open, wisp}},
		// And they compose rather than fight: the union of the two answers.
		{"IncludeEphemeral with IncludeInfra", publicops.ListRequest{IDFilter: scope, IncludeEphemeral: true, IncludeInfra: true}, []string{open, wisp, infraWisp}},
	} {
		page, err := fixture.Reader.List(ctx, test.req)
		if err != nil {
			t.Fatalf("List (%s): %v", test.name, err)
		}
		assertReaderPageIDSet(t, "List ("+test.name+")", page, test.want)
	}
}

// RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary pins the asymmetry
// ListRequest.IssueType's doc calls out by name (reader.go:124-126): this method
// validates the type against the workspace vocabulary and ERRORS, where
// ReadyRequest.IssueType matches nothing and succeeds. Both halves are asserted
// here, in one place, because the promise is the difference between them.
func RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	const unknown = "no-such-type-anywhere"

	page, err := fixture.Reader.List(ctx, publicops.ListRequest{IssueType: unknown})
	if err == nil {
		t.Fatalf("List with an unknown issue type returned %d items and no error, want a refusal", len(page.Items))
	}

	// A built-in type is accepted by the same validation, so the refusal above
	// is the vocabulary check and not a blanket rejection of IssueType.
	if _, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter:  readerIDFilter(readerID(fixture, "lstype", "absent")),
		IssueType: string(types.TypeTask),
	}); err != nil {
		t.Fatalf("List with a built-in issue type: %v", err)
	}

	if _, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{IssueType: unknown}); err != nil {
		t.Fatalf("the same unknown type must be tolerated by Ready, which matches nothing rather than failing: %v", err)
	}
}

// RunReaderListNaturalNumericIDSortTrimsAfterTheFetch pins the sort the database
// cannot express (reader.go:239-250). `--sort id` needs natural-numeric
// comparison — bd-9 before bd-10, which no lexical ORDER BY produces — so the
// query runs unlimited and the page limit is applied afterwards, in Go, over the
// full result set. This is the historical divergence (one implementation
// returned the --ready arm untrimmed), asserted here against real rows rather
// than a stub seam.
func RunReaderListNaturalNumericIDSortTrimsAfterTheFetch(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	one := readerID(fixture, "lsnat", "1")
	two := readerID(fixture, "lsnat", "2")
	ten := readerID(fixture, "lsnat", "10")
	// Seeded in an order that is neither the natural order nor the lexical one,
	// so an implementation that returns storage order fails visibly.
	for _, id := range []string{ten, one, two} {
		seedReaderIssue(t, ctx, fixture, readerIssue(id, types.TypeTask, ""))
	}
	scope := readerIDFilter(one, two, ten)

	page, err := fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: scope, SortBy: "id"})
	if err != nil {
		t.Fatalf("List --sort id: %v", err)
	}
	assertReaderPageIDs(t, "List --sort id", page, []string{one, two, ten})
	if page.HasMore {
		t.Error("List --sort id reported HasMore on an untruncated page")
	}

	// The trim is the ONLY thing bounding this page, and it runs after the
	// natural sort: a limit of two must keep the two natural-lowest ids, not
	// the two the query happened to return first.
	page, err = fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: scope, SortBy: "id", Limit: readerLimit(2)})
	if err != nil {
		t.Fatalf("List --sort id --limit 2: %v", err)
	}
	assertReaderPageIDs(t, "List --sort id --limit 2", page, []string{one, two})
	if !page.HasMore {
		t.Error("List --sort id --limit 2 hid a row without reporting HasMore")
	}

	page, err = fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: scope, SortBy: "id", Reverse: true, Limit: readerLimit(2)})
	if err != nil {
		t.Fatalf("List --sort id --reverse --limit 2: %v", err)
	}
	assertReaderPageIDs(t, "List --sort id --reverse --limit 2", page, []string{ten, two})
}

// RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder pins the decoded
// cursor (reader.go:277-286). The position is a PAIR, and both halves matter:
// rows older than the cursor's timestamp are returned, and rows sharing that
// timestamp are returned only when their id sorts after the cursor's. A seam
// that compared the timestamp alone would drop the same-second row, which is
// exactly how a keyset page loses records.
func RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	newest := readerID(fixture, "lskeys", "c")
	cursor := readerID(fixture, "lskeys", "b1")
	sameSecond := readerID(fixture, "lskeys", "b2")
	oldest := readerID(fixture, "lskeys", "a")

	// Whole seconds, minutes apart: the created_at columns are DATETIME, so a
	// sub-second difference is not something a cursor may depend on.
	base := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)
	cursorAt := base.Add(30 * time.Minute)
	for _, seed := range []struct {
		id string
		at time.Time
	}{
		{newest, base.Add(45 * time.Minute)},
		{cursor, cursorAt},
		{sameSecond, cursorAt},
		{oldest, base},
	} {
		issue := readerIssue(seed.id, types.TypeTask, "")
		issue.CreatedAt = seed.at
		issue.UpdatedAt = seed.at
		seedReaderIssue(t, ctx, fixture, issue)
	}

	page, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter:       readerIDFilter(newest, cursor, sameSecond, oldest),
		SortBy:         "created",
		AfterCreatedAt: &cursorAt,
		AfterID:        cursor,
	})
	if err != nil {
		t.Fatalf("List from a keyset position: %v", err)
	}
	// created_at DESC, id ASC: the same-second row with the larger id comes
	// first, then everything strictly older. The cursor row itself and
	// everything newer are already delivered and must not repeat.
	assertReaderPageIDs(t, "List from a keyset position", page, []string{sameSecond, oldest})
}

// RunReaderListReadyFlagAnswersTheBlockerAwareSet pins the CARRIED half of the
// --ready arm (reader.go:204-237): it switches the query to blocker-aware ready
// work, the labels that scope it survive the trip, and the page epilogue still
// runs over the result. The last part is the regression: the arm once returned
// storage order, unsorted and untrimmed, while the other arm of the same method
// sorted and trimmed.
//
// Scoped by LABEL rather than by the id set every other list case here uses,
// and that is now the doc's promise rather than a workaround: the ready query
// is reached through a narrower filter vocabulary, Labels is on the carried
// side of it and IDFilter is not. The DROPPED half — and the refusal that
// replaced the silent widening — is
// RunReaderListReadyFlagRefusesAFilterItCannotCarry.
//
// THREE READY IDS, not two, and the reason is the regression's other half. With
// only bd-1 and bd-10 in the set, natural-numeric order, lexical order and the
// order the query returns them in all read the same, so an arm that skipped the
// display order entirely still answered correctly and the case could only see
// the missing TRIM. bd-1 / bd-2 / bd-10 separates them: natural order is
// 1, 2, 10, lexical is 1, 10, 2, and the seed order below is neither. `--sort
// id` is the sort SQL cannot express, so on this arm as on the other one the
// epilogue is the only thing that can produce it (ListRequest.SortBy, "the
// display order is applied to the page after the query rather than inside it").
func RunReaderListReadyFlagAnswersTheBlockerAwareSet(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "lsready")
	blocker := readerID(fixture, "lsready", "1")
	alsoFree := readerID(fixture, "lsready", "2")
	blocked := readerID(fixture, "lsready", "3")
	free := readerID(fixture, "lsready", "10")
	// Seeded in an order that is neither the natural order nor the lexical one.
	for _, id := range []string{free, blocked, alsoFree, blocker} {
		seedReaderIssue(t, ctx, fixture, readerIssue(id, types.TypeTask, scope))
	}
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: blocked, DependsOnID: blocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the blocking edge: %v", err)
	}

	page, err := fixture.Reader.List(ctx, publicops.ListRequest{
		Labels: []string{scope}, ReadyFlag: true, SortBy: "id",
	})
	if err != nil {
		t.Fatalf("List --ready: %v", err)
	}
	assertReaderPageIDs(t, "List --ready --sort id", page, []string{blocker, alsoFree, free})

	// The same arm, under a sort the database cannot express and a limit: the
	// epilogue has to sort AND trim here, and report the truncation.
	page, err = fixture.Reader.List(ctx, publicops.ListRequest{
		Labels: []string{scope}, ReadyFlag: true, SortBy: "id", Limit: readerLimit(1),
	})
	if err != nil {
		t.Fatalf("List --ready --sort id --limit 1: %v", err)
	}
	assertReaderPageIDs(t, "List --ready --sort id --limit 1", page, []string{blocker})
	if !page.HasMore {
		t.Error("List --ready --sort id --limit 1 hid a row without reporting HasMore")
	}
}

// RunReaderListReadyFlagRefusesAFilterItCannotCarry pins the DROPPED half of
// the --ready arm (reader.go:219-226): a request that asks the blocker-aware
// query to honor a filter it cannot carry is REFUSED with ErrValidation
// naming the field, not answered with the wider set.
//
// This is the case that would have caught the original defect. An
// IDFilter-scoped --ready request used to answer with every open row in the
// workspace — measured at 18 rows for a three-id scope on the unit-of-work leg,
// where the suite shares one database. The store-backed leg ran the same
// unscoped query and passed anyway, because each of its cases owns a private
// branch and the workspace happened to BE the seeded rows. A silently dropped
// filter looks exactly like a passing test, so the assertion cannot be on the
// rows: it has to be on the refusal.
//
// The message is asserted, not just the sentinel. A generic "unsupported
// combination" would leave a caller to bisect their own request to find which
// half of it was the problem.
func RunReaderListReadyFlagRefusesAFilterItCannotCarry(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "lsdrop")
	one := readerID(fixture, "lsdrop", "1")
	two := readerID(fixture, "lsdrop", "2")
	for _, id := range []string{one, two} {
		seedReaderIssue(t, ctx, fixture, readerIssue(id, types.TypeTask, scope))
	}
	cutoff := time.Now().UTC().Add(-time.Hour)
	maxPriority := 1

	// One per family the doc names, not one per field: the id set, the text
	// searches, the date bounds, the pinned/absent-relation flags, the numeric
	// range and the keyset position.
	for _, tc := range []struct {
		name  string
		req   publicops.ListRequest
		field string
	}{
		{"IDFilter", publicops.ListRequest{IDFilter: readerIDFilter(one), ReadyFlag: true}, "IDFilter"},
		{"TitleContains", publicops.ListRequest{Labels: []string{scope}, TitleContains: one, ReadyFlag: true}, "TitleContains"},
		{"CreatedAfter", publicops.ListRequest{Labels: []string{scope}, CreatedAfter: &cutoff, ReadyFlag: true}, "CreatedAfter"},
		{"PinnedFlag", publicops.ListRequest{Labels: []string{scope}, PinnedFlag: true, ReadyFlag: true}, "PinnedFlag"},
		{"PriorityMax", publicops.ListRequest{Labels: []string{scope}, PriorityMax: &maxPriority, ReadyFlag: true}, "PriorityMax"},
		{"keyset position", publicops.ListRequest{Labels: []string{scope}, AfterCreatedAt: &cutoff, AfterID: one, ReadyFlag: true}, "AfterCreatedAt"},
	} {
		page, err := fixture.Reader.List(ctx, tc.req)
		if !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("List --ready with %s: got (%d items, %v); want ErrValidation", tc.name, len(page.Items), err)
			continue
		}
		if !strings.Contains(err.Error(), tc.field) {
			t.Errorf("List --ready with %s refused without naming the field: %v", tc.name, err)
		}
	}

	// Naming every field it could not honor, not just the first: a caller who
	// fixes one and retries should not have to discover the next one the same
	// way.
	_, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: readerIDFilter(one), OverdueFlag: true, ReadyFlag: true,
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("List --ready with two dropped fields: %v; want ErrValidation", err)
	}
	for _, field := range []string{"IDFilter", "OverdueFlag"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("List --ready with two dropped fields did not name %s: %v", field, err)
		}
	}

	// The refusal is scoped to what the query cannot carry. Labels is carried,
	// and NoPinnedFlag is already true of the ready set, so neither is refused
	// — the doc promises both, and a validator that refused every flag it did
	// not recognize would break `bd list --ready` outright.
	page, err := fixture.Reader.List(ctx, publicops.ListRequest{
		Labels: []string{scope}, NoPinnedFlag: true, ReadyFlag: true, SortBy: "id",
	})
	if err != nil {
		t.Fatalf("List --ready --no-pinned on a carried scope: %v", err)
	}
	assertReaderPageIDs(t, "List --ready --no-pinned on a carried scope", page, []string{one, two})

	// Without ReadyFlag the same dropped fields are ordinary list filters and
	// must still work: the refusal belongs to the arm, not to the request type.
	page, err = fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: readerIDFilter(one), SortBy: "id"})
	if err != nil {
		t.Fatalf("List --id without --ready: %v", err)
	}
	assertReaderPageIDs(t, "List --id without --ready", page, []string{one})
}

// RunReaderListEmptyPageIsWellFormed pins the page shape when nothing matches
// (reader.go:376-381). Items is never nil for a successful call, so no caller
// has to tell null from empty to learn that nothing matched, and an empty page
// hid nothing.
func RunReaderListEmptyPageIsWellFormed(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	absent := readerIDFilter(readerID(fixture, "lsempty", "never-seeded"))

	listed, err := fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: absent})
	if err != nil {
		t.Fatalf("List matching nothing: %v", err)
	}
	assertReaderPageIDs(t, "List matching nothing", listed, nil)
	if listed.HasMore {
		t.Error("List matching nothing reported HasMore")
	}

	// Ready shares the page type deliberately, so it owes the same shape.
	ready, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{Labels: []string{readerLabel(fixture, "lsempty-never-applied")}})
	if err != nil {
		t.Fatalf("Ready matching nothing: %v", err)
	}
	assertReaderPageIDs(t, "Ready matching nothing", ready, nil)
	if ready.HasMore {
		t.Error("Ready matching nothing reported HasMore")
	}
}

// RunReaderListMaxRowsIsHonored pins ListRequest.MaxRows: a cap the result set
// exceeds refuses the whole answer with *ErrTooManyRows carrying the count, the
// cap and the request's attribution — on every implementation.
//
// It used to say "honored OR refused with *ErrUnsupported", because one body
// threaded the cap and the other did not. That disjunction could not tell a
// working circuit breaker from a backend that had none, which is the one thing
// a caller setting a cap needs to know. Both query paths now size the same
// window through one function (internal/storage/issueops.SearchProbeLimit) and
// enforce it with the same one (EnforceMaxRowsCap).
//
// A cap is a CIRCUIT BREAKER: a caller sets it because it would rather fail
// than wait, so answering the unbounded query hands that caller exactly the
// runaway result it was guarding against.
//
// THE COMPLEMENT IS ASSERTED TOO — the same request under a cap the result set
// fits inside comes back as an ordinary page. Without it a body that refused
// every non-zero MaxRows out of hand would pass. And the cap is driven UNDER AN
// OFFSET as well, because a row the query skipped is still a row it matched: a
// body that counted only what survived the skip would let an offset talk a
// caller out of the breaker.
//
// THE LIMIT BOUNDARY IS THE LAST PART, and it is where the two seams are most
// able to disagree. A cap only fires when the PAGE could have exceeded it: at
// Limit <= MaxRows the caller can never receive more rows than the cap allows,
// so the answer is an ordinary truncated page, and at Limit > MaxRows the same
// result set fires. Both sides of the boundary are driven, one row apart. The
// store seam reaches it by composing workapi.WithFetchOneExtra with
// EffectiveSearchLimit — including the cap bump that keeps the probe row from
// tripping a cap the page cannot break — and the unit-of-work seam by calling
// the one function that IS that composition. A seam that added its probe row
// without the bump fires at Limit == MaxRows; one that added no probe row
// reports has-more wrongly. Neither is visible from the unlimited requests
// above.
func RunReaderListMaxRowsIsHonored(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	var ids []string
	for _, tag := range []string{"a", "b", "c"} {
		id := readerID(fixture, "maxrows", tag)
		ids = append(ids, id)
		seedReaderIssue(t, ctx, fixture, readerIssue(id, types.TypeTask, ""))
	}
	idScope := readerIDFilter(ids...)

	// A cap the three seeded rows fit inside: an ordinary page everywhere.
	const roomyWhat = "List under a cap the result set fits inside"
	roomy, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: idScope, SortBy: "created", MaxRows: len(ids) + 1, MaxRowsSource: "--max-rows",
	})
	if err != nil {
		t.Fatalf("%s: %v", roomyWhat, err)
	}
	assertReaderPageIDSet(t, roomyWhat, roomy, ids)

	// A cap the result set exceeds, with and without an offset in front of it.
	// A PAGE from either means the field was ignored.
	for _, test := range []struct {
		what   string
		offset int
	}{
		{"List under a cap the result set exceeds", 0},
		{"List under a cap the result set exceeds, behind an offset", 1},
	} {
		tight, err := fixture.Reader.List(ctx, publicops.ListRequest{
			IDFilter: idScope, SortBy: "created", Offset: test.offset,
			MaxRows: len(ids) - 1, MaxRowsSource: "--max-rows",
		})
		if err == nil {
			t.Fatalf("%s (MaxRows=%d over %d matching rows) returned the page %v and no error: the cap was silently ignored",
				test.what, len(ids)-1, len(ids), readerPageIDs(tight))
		}
		// The leaf names the answer — *ErrTooManyRows — so a caller can tell
		// "the cap fired" from any other failure without reading error text.
		var tooMany *storageops.ErrTooManyRows
		if !errors.As(err, &tooMany) {
			t.Fatalf("%s failed with %v; a cap that fired has to answer with *ErrTooManyRows a caller can classify", test.what, err)
		}
		if tooMany.Cap != len(ids)-1 {
			t.Errorf("%s: the cap error reports Cap = %d, want the %d the request asked for", test.what, tooMany.Cap, len(ids)-1)
		}
		if tooMany.Found <= tooMany.Cap {
			t.Errorf("%s: the cap error reports Found = %d against Cap = %d; a cap that fired saw more rows than it allows",
				test.what, tooMany.Found, tooMany.Cap)
		}
		// The whole job of MaxRowsSource (reader.go): the attribution the
		// request supplied comes back on the refusal, so the caller that set
		// the cap can say which of its own knobs did.
		if tooMany.Source != "--max-rows" {
			t.Errorf("%s: the cap error reports Source = %q, want the %q the request supplied: MaxRowsSource decides nothing else",
				test.what, tooMany.Source, "--max-rows")
		}
	}

	// The boundary, one row apart, over the same three rows and the same cap.
	// Limit 2 under a cap of 2 delivers a page of 2 and says there is more;
	// Limit 3 under the same cap fires.
	const cap2 = 2
	page, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: idScope, SortBy: "created", Limit: readerLimit(cap2),
		MaxRows: cap2, MaxRowsSource: "--max-rows",
	})
	if err != nil {
		t.Fatalf("List at Limit=%d under MaxRows=%d: %v; a page the caller receives cannot exceed a cap it fits inside, so this must not fire",
			cap2, cap2, err)
	}
	if len(page.Items) != cap2 {
		t.Errorf("List at Limit=%d under MaxRows=%d returned %v, want %d rows", cap2, cap2, readerPageIDs(page), cap2)
	}
	if !page.HasMore {
		t.Errorf("List at Limit=%d under MaxRows=%d over %d rows reported has_more=false; the probe row that answers that question is what the cap must not fire on",
			cap2, cap2, len(ids))
	}
	over, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: idScope, SortBy: "created", Limit: readerLimit(cap2 + 1),
		MaxRows: cap2, MaxRowsSource: "--max-rows",
	})
	if err == nil {
		t.Fatalf("List at Limit=%d under MaxRows=%d returned the page %v; a page that could exceed the cap has to fire it",
			cap2+1, cap2, readerPageIDs(over))
	}
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) {
		t.Fatalf("List at Limit=%d under MaxRows=%d failed with %v, want *ErrTooManyRows", cap2+1, cap2, err)
	}
	if tooMany.Cap != cap2 {
		t.Errorf("the cap error reports Cap = %d, want the %d the request asked for; the probe row's bump must not reach the caller", tooMany.Cap, cap2)
	}
}

// RunReaderListMaxRowsBoundaryIsLimitPlusOffset pins WHICH WINDOW the cap is
// sized against: the rows the query TOUCHES, Limit+Offset, not the rows the
// caller receives (reader.go, ListRequest.MaxRows).
//
// A row Offset skips is a row the query matched, so an offset walks a caller
// TOWARD the breaker and never past it. The boundary that follows is exact:
// Limit+Offset <= MaxRows is a page whatever the result set does, and
// Limit+Offset > MaxRows fires as soon as that many rows match. The case above
// drives that boundary along the LIMIT axis at Offset 0; this one drives it
// along the OFFSET axis, with the limit and the cap held still, so the only
// thing that moves between the last page and the first refusal is one row of
// skip.
//
// WHY IT IS A CASE OF ITS OWN rather than another arm of the one above: the two
// seams compose the window differently — the store-backed body widens the
// filter and then sizes its probe row (workapi.WithRowsBeforeThePage, then
// WithFetchOneExtra), the unit-of-work body hands its seam the widened limit
// and lets internal/storage/domain/db size both. Either composition can be one
// row wrong in a way no request without an offset can see, and the two can be
// wrong in DIFFERENT directions: the cap bump at the equal boundary keys off
// the widened window on one side and off the page on the other unless both are
// written to key off the same one.
//
// THE FIXTURE HAS TO REACH THE CAP FOR ANY OF THAT TO BE VISIBLE. Five rows
// against a cap of three: every window below is bounded at four rows or fewer,
// so each query has more matching rows behind it than its bound, and a body
// that fetched one row too many or counted one row too few has somewhere to
// show it. Three rows would make the two non-firing cases pass on a body with
// no cap at all.
func RunReaderListMaxRowsBoundaryIsLimitPlusOffset(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	const rowCap = 3
	scope := readerLabel(fixture, "maxrowswindow")
	var ids []string
	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Hour)
	for i, tag := range []string{"a", "b", "c", "d", "e"} {
		id := readerID(fixture, "maxrowswindow", tag)
		ids = append(ids, id)
		issue := readerIssue(id, types.TypeTask, scope)
		at := base.Add(time.Duration(i) * time.Minute)
		issue.CreatedAt, issue.UpdatedAt = at, at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	idScope := readerIDFilter(ids...)

	// The order every expectation below is a window of, READ rather than
	// assumed: which end "created" starts from is the query's business, and
	// this case is about which rows the cap counts, not about that.
	unpaged, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: idScope, SortBy: "created", Limit: readerLimit(0),
	})
	if err != nil {
		t.Fatalf("List unpaged: %v", err)
	}
	order := readerPageIDs(unpaged)
	if len(order) != len(ids) {
		t.Fatalf("List unpaged returned %v, want the %d seeded rows: a result set smaller than the bound cannot observe the cap this case drives",
			order, len(ids))
	}

	// The window, one row of offset at a time, against a cap of three.
	for _, test := range []struct {
		what   string
		limit  int
		offset int
		fires  bool
	}{
		// Limit+Offset == MaxRows-1: strictly inside the cap.
		{"Limit+Offset one row inside the cap", 1, 1, false},
		// Limit+Offset == MaxRows: the touched window is exactly the cap, and
		// the probe row that answers has-more must not be counted against it.
		{"Limit+Offset exactly at the cap", 2, 1, false},
		// Limit+Offset == MaxRows+1: the same limit and the same cap, one more
		// row of skip, and the query can now touch more rows than the cap
		// allows.
		{"Limit+Offset one row past the cap", 2, 2, true},
	} {
		what := test.what
		page, err := fixture.Reader.List(ctx, publicops.ListRequest{
			IDFilter: idScope, SortBy: "created",
			Limit: readerLimit(test.limit), Offset: test.offset,
			MaxRows: rowCap, MaxRowsSource: "--max-rows",
		})
		if test.fires {
			if err == nil {
				t.Errorf("%s (Limit=%d Offset=%d MaxRows=%d over %d matching rows) returned the page %v; a query that may touch %d rows has to fire a cap of %d",
					what, test.limit, test.offset, rowCap, len(ids), readerPageIDs(page), test.limit+test.offset, rowCap)
				continue
			}
			var tooMany *storageops.ErrTooManyRows
			if !errors.As(err, &tooMany) {
				t.Errorf("%s failed with %v, want *ErrTooManyRows", what, err)
				continue
			}
			if tooMany.Cap != rowCap {
				t.Errorf("%s: the cap error reports Cap = %d, want the %d the request asked for; the probe row's bump must not reach the caller",
					what, tooMany.Cap, rowCap)
			}
			if tooMany.Found <= tooMany.Cap {
				t.Errorf("%s: the cap error reports Found = %d against Cap = %d; a cap that fired saw more rows than it allows",
					what, tooMany.Found, tooMany.Cap)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s (Limit=%d Offset=%d MaxRows=%d): %v; a query bounded to %d rows cannot break a cap of %d, so this must not fire",
				what, test.limit, test.offset, rowCap, err, test.limit+test.offset, rowCap)
			continue
		}
		assertReaderPageIDs(t, what, page, order[test.offset:test.offset+test.limit])
		// Every window here stops short of the fifth row, so the page that
		// came back hid something. A body that failed to reach past the skip
		// answers the same rows with has_more=false.
		if !page.HasMore {
			t.Errorf("%s reported has_more=false over %d matching rows; the row past the page is what the bound has to have reached",
				what, len(ids))
		}
	}

	// THE SAME BOUNDARY ON THE --ready ARM, which is a different query in both
	// seams — a blocker-aware union rather than the search — reached through
	// the same request and the same cap. Both sides are driven: an arm that
	// refused every capped ready request would pass on the firing half alone.
	//
	// WHICH rows come back is deliberately not asserted here. The ready query
	// runs in its sort POLICY's order and the display order is applied to the
	// page afterwards, so a bounded ready query picks its rows in an order this
	// request does not name — that promise is
	// RunReaderReadySortPoliciesOrderTheSameRows's, and restating it here would
	// pin an order the contract does not owe. What this arm owes is the cap.
	readyAll, err := fixture.Reader.List(ctx, publicops.ListRequest{
		Labels: []string{scope}, ReadyFlag: true, SortBy: "created", Limit: readerLimit(0),
	})
	if err != nil {
		t.Fatalf("List --ready unpaged: %v", err)
	}
	if got := readerPageIDs(readyAll); len(got) != len(ids) {
		t.Fatalf("List --ready unpaged returned %v, want the %d seeded rows: they are open, unassigned and unblocked, and a smaller ready set cannot reach the cap",
			got, len(ids))
	}
	readyAt, err := fixture.Reader.List(ctx, publicops.ListRequest{
		Labels: []string{scope}, ReadyFlag: true, SortBy: "created",
		Limit: readerLimit(2), Offset: 1, MaxRows: rowCap, MaxRowsSource: "--max-rows",
	})
	switch {
	case err != nil:
		t.Errorf("List --ready at Limit=2 Offset=1 under MaxRows=%d: %v; the touched window is exactly the cap and must not fire", rowCap, err)
	case len(readyAt.Items) != 2:
		t.Errorf("List --ready at Limit=2 Offset=1 returned %v, want the 2 rows behind the skip", readerPageIDs(readyAt))
	}
	readyOver, err := fixture.Reader.List(ctx, publicops.ListRequest{
		Labels: []string{scope}, ReadyFlag: true, SortBy: "created",
		Limit: readerLimit(2), Offset: 2, MaxRows: rowCap, MaxRowsSource: "--max-rows",
	})
	if err == nil {
		t.Fatalf("List --ready at Limit=2 Offset=2 under MaxRows=%d returned the page %v; the ready query counts a skipped row the same way the search does",
			rowCap, readerPageIDs(readyOver))
	}
	var readyTooMany *storageops.ErrTooManyRows
	if !errors.As(err, &readyTooMany) {
		t.Fatalf("List --ready at Limit=2 Offset=2 under MaxRows=%d failed with %v, want *ErrTooManyRows", rowCap, err)
	}
	if readyTooMany.Cap != rowCap {
		t.Errorf("List --ready: the cap error reports Cap = %d, want the %d the request asked for", readyTooMany.Cap, rowCap)
	}
}

// RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse pins
// ListRequest.SkipCounts (reader.go:160-175). Two halves, and the second is
// the load-bearing one:
//
//   - the three cardinalities come back ZERO on a row that genuinely has each
//     of them, so the knob demonstrably reached the query rather than being
//     accepted and dropped; and
//   - NOTHING ELSE MOVES. Same rows, same order, same Parent, same has-more
//     verdict as the identical request without the knob. The aggregates hang
//     off outer joins, so an implementation that made one of them inner would
//     answer with a strict subset and still look like it had "skipped the
//     counts".
//
// Zero is asserted rather than "unknown" because zero is what the wire and the
// struct can carry; the doc's instruction to READ it as unknown is a promise to
// the caller.
func RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	subject := readerID(fixture, "skipcounts", "subject")
	blocker := readerID(fixture, "skipcounts", "blocker")
	dependent := readerID(fixture, "skipcounts", "dependent")
	parent := readerID(fixture, "skipcounts", "parent")
	for _, id := range []string{subject, blocker, dependent, parent} {
		seedReaderIssue(t, ctx, fixture, readerIssue(id, types.TypeTask, ""))
	}
	// One outgoing blocks edge, one incoming one, and a parent-child edge, so
	// the subject row carries a nonzero DependencyCount, DependentCount and
	// Parent at once. Parent rides the same mega-query as the counts and is NOT
	// a count: it is the tripwire for a knob that suppressed too much.
	for _, edge := range []*types.Dependency{
		{IssueID: subject, DependsOnID: blocker, Type: types.DepBlocks},
		{IssueID: dependent, DependsOnID: subject, Type: types.DepBlocks},
		{IssueID: subject, DependsOnID: parent, Type: types.DepParentChild},
	} {
		if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}
	if err := fixture.AddComment(ctx, subject, "seed", "so the comment count is nonzero"); err != nil {
		t.Fatalf("seed the comment: %v", err)
	}

	idScope := readerIDFilter(subject, blocker, dependent, parent)
	req := publicops.ListRequest{IDFilter: idScope, SortBy: "created"}

	hydrated, err := fixture.Reader.List(ctx, req)
	if err != nil {
		t.Fatalf("List with the counts hydrated: %v", err)
	}
	hydratedRow := readerRowByID(t, "List with the counts hydrated", hydrated, subject)
	if hydratedRow == nil {
		return
	}
	// The premise. If the seeded row has no counts to suppress and no parent to
	// keep, the second half of this case proves nothing either way.
	if hydratedRow.DependencyCount == 0 || hydratedRow.DependentCount == 0 || hydratedRow.CommentCount == 0 {
		t.Fatalf("the seeded subject came back with counts (%d, %d, %d); this case needs all three nonzero before it can assert they are suppressed",
			hydratedRow.DependencyCount, hydratedRow.DependentCount, hydratedRow.CommentCount)
	}
	if hydratedRow.Parent == nil {
		t.Fatalf("the seeded subject came back with no Parent; this case needs one before Parent can be the tripwire for a knob that suppressed too much")
	}

	req.SkipCounts = true
	skipped, err := fixture.Reader.List(ctx, req)
	if err != nil {
		t.Fatalf("List with SkipCounts: %v", err)
	}
	skippedRow := readerRowByID(t, "List with SkipCounts", skipped, subject)
	if skippedRow == nil {
		return
	}
	for _, got := range []struct {
		what  string
		count int
	}{
		{"DependencyCount", skippedRow.DependencyCount},
		{"DependentCount", skippedRow.DependentCount},
		{"CommentCount", skippedRow.CommentCount},
	} {
		if got.count != 0 {
			t.Errorf("List with SkipCounts returned %s = %d, want 0: the knob was accepted and the aggregate computed anyway", got.what, got.count)
		}
	}

	// Nothing else moves.
	if !slices.Equal(readerPageIDs(skipped), readerPageIDs(hydrated)) {
		t.Errorf("List with SkipCounts returned %v, want the same page as without it, %v: this knob chooses what is hydrated, never which rows match",
			readerPageIDs(skipped), readerPageIDs(hydrated))
	}
	if skipped.HasMore != hydrated.HasMore {
		t.Errorf("List with SkipCounts reported HasMore = %v, want %v", skipped.HasMore, hydrated.HasMore)
	}
	if !readerSameParent(skippedRow.Parent, hydratedRow.Parent) {
		t.Errorf("List with SkipCounts returned Parent = %v, want %v: Parent is not a cardinality and rides the same query",
			readerParentText(skippedRow.Parent), readerParentText(hydratedRow.Parent))
	}
}

// RunReaderGetResolvesTheExactIDAcrossBothPlanes pins GetRequest.ID
// (reader.go:364): the id is exact and canonical, the issue-to-wisp fallback
// happens inside, and there is no fuzzy, prefix or substring resolution. The
// prefix probe is the half that matters — an affordance that can answer with a
// different issue than the caller named has no place on a contract an
// unattended client calls.
func RunReaderGetResolvesTheExactIDAcrossBothPlanes(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	issue := readerID(fixture, "getid", "durable")
	wisp := readerID(fixture, "getid", "ephemeral")
	wispIssue := readerIssue(wisp, types.TypeTask, "")
	wispIssue.Ephemeral = true
	seedReaderIssue(t, ctx, fixture, readerIssue(issue, types.TypeTask, ""))
	seedReaderWisp(t, ctx, fixture, wispIssue)

	for _, id := range []string{issue, wisp} {
		details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: id})
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if details == nil || details.ID != id {
			t.Fatalf("Get(%s) answered with %#v, want the issue itself", id, details)
		}
	}

	for _, name := range []struct{ what, id string }{
		{"a prefix of a real id", issue[:len(issue)-2]},
		{"a real id with a suffix", issue + "x"},
	} {
		details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: name.id})
		if !errors.Is(err, publicops.ErrNotFound) {
			t.Errorf("Get(%s) = (%#v, %v), want ErrNotFound: this contract resolves exact ids only", name.what, details, err)
		}
	}
}

// RunReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay pins both halves of
// Get's error promise (reader.go:500-503). A miss on BOTH planes is ErrNotFound;
// a backend failure passes through unchanged and never decays into not-found.
//
// The decay half needs a fixture that can induce a backend error without
// destroying the fixture for the cases that follow, so it uses an
// already-canceled context: every backend has to begin a transaction or open a
// connection before it can look anything up, so the failure lands underneath the
// resolve rather than inside it. What is asserted is only that the error is NOT
// ErrNotFound — the error's spelling is each backend's own.
func RunReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	missing := readerID(fixture, "getmiss", "never-seeded")

	details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: missing})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("Get on a miss = (%#v, %v), want ErrNotFound", details, err)
	}
	if details != nil {
		t.Errorf("Get on a miss returned %#v alongside its error, want nil", details)
	}

	dead, cancel := context.WithCancel(ctx)
	cancel()
	details, err = fixture.Reader.Get(dead, publicops.GetRequest{ID: missing})
	if err == nil {
		t.Fatalf("Get on a dead context returned %#v and no error", details)
	}
	if errors.Is(err, publicops.ErrNotFound) {
		t.Errorf("a backend failure decayed into not-found: %v", err)
	}
}

// RunReaderGetOptionalRowListsAreOffByDefault pins the two DetailOptions
// (reader.go:365-369): the detail view carries counts either way, the expensive
// row lists are absent until asked for, and a positive comment count with no
// rows says so rather than reading as "no comments".
func RunReaderGetOptionalRowListsAreOffByDefault(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	subject := readerID(fixture, "getopt", "subject")
	dependent := readerID(fixture, "getopt", "dependent")
	seedReaderIssue(t, ctx, fixture, readerIssue(subject, types.TypeTask, ""))
	seedReaderIssue(t, ctx, fixture, readerIssue(dependent, types.TypeTask, ""))
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: dependent, DependsOnID: subject, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the incoming edge: %v", err)
	}
	const commentText = "the detail view carries a count for this"
	if err := fixture.AddComment(ctx, subject, "seed", commentText); err != nil {
		t.Fatalf("seed the comment: %v", err)
	}

	details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: subject})
	if err != nil {
		t.Fatalf("Get with both options off: %v", err)
	}
	assertReaderCount(t, "DependentCount", details.DependentCount, 1)
	assertReaderCount(t, "CommentCount", details.CommentCount, 1)
	if len(details.Dependents) != 0 {
		t.Errorf("Get with IncludeDependents off returned %d dependent rows, want none", len(details.Dependents))
	}
	if len(details.Comments) != 0 {
		t.Errorf("Get with IncludeComments off returned %d comment rows, want none", len(details.Comments))
	}
	if details.CommentsOmitted == nil || !*details.CommentsOmitted {
		t.Errorf("CommentsOmitted = %v with a nonzero comment count and no rows, want true", details.CommentsOmitted)
	}

	details, err = fixture.Reader.Get(ctx, publicops.GetRequest{
		ID: subject, IncludeDependents: true, IncludeComments: true,
	})
	if err != nil {
		t.Fatalf("Get with both options on: %v", err)
	}
	assertReaderCount(t, "DependentCount with the rows requested", details.DependentCount, 1)
	assertReaderCount(t, "CommentCount with the rows requested", details.CommentCount, 1)
	if len(details.Dependents) != 1 || details.Dependents[0].ID != dependent {
		t.Errorf("Get with IncludeDependents returned %v, want the one dependent %s", readerDependencyIDs(details.Dependents), dependent)
	}
	if len(details.Comments) != 1 || details.Comments[0].Text != commentText {
		t.Errorf("Get with IncludeComments returned %d rows, want the one seeded comment verbatim", len(details.Comments))
	}
	if details.CommentsOmitted != nil {
		t.Errorf("CommentsOmitted = %v alongside a populated Comments slice, want unset", *details.CommentsOmitted)
	}
}

// RunReaderGetBriefDepsProjectsTheDependencyRows pins BriefDeps on the public
// Get contract (#5546). Without a case here an out-of-tree Reader can ignore
// the field and still pass the suite, which is the failure the sibling row
// options already have a case against.
func RunReaderGetBriefDepsProjectsTheDependencyRows(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	subject := readerID(fixture, "briefdeps", "subject")
	blocker := readerID(fixture, "briefdeps", "blocker")
	const heavy = "the free-form body a brief projection is meant to drop"

	seedReaderIssue(t, ctx, fixture, readerIssue(subject, types.TypeTask, ""))
	blockerIssue := readerIssue(blocker, types.TypeTask, "")
	blockerIssue.Description = heavy
	blockerIssue.Design = heavy
	blockerIssue.AcceptanceCriteria = heavy
	blockerIssue.Notes = heavy
	seedReaderIssue(t, ctx, fixture, blockerIssue)
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: subject, DependsOnID: blocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the outgoing edge: %v", err)
	}

	details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: subject})
	if err != nil {
		t.Fatalf("Get with BriefDeps off: %v", err)
	}
	if len(details.Dependencies) != 1 {
		t.Fatalf("Get returned %d dependency rows, want 1", len(details.Dependencies))
	}
	if got := details.Dependencies[0]; got.Description != heavy || got.Design != heavy ||
		got.AcceptanceCriteria != heavy || got.Notes != heavy {
		t.Errorf("BriefDeps off must return the full body, got description=%d design=%d acceptance=%d notes=%d",
			len(got.Description), len(got.Design), len(got.AcceptanceCriteria), len(got.Notes))
	}

	details, err = fixture.Reader.Get(ctx, publicops.GetRequest{ID: subject, BriefDeps: true})
	if err != nil {
		t.Fatalf("Get with BriefDeps on: %v", err)
	}
	if len(details.Dependencies) != 1 {
		t.Fatalf("BriefDeps returned %d dependency rows, want 1", len(details.Dependencies))
	}
	got := details.Dependencies[0]
	if got.ID != blocker || got.Title != blocker || got.Status != types.StatusOpen ||
		got.IssueType != types.TypeTask || got.Priority != 2 || got.DependencyType != types.DepBlocks {
		t.Errorf("BriefDeps dropped an identity field: %+v", got)
	}
	if got.Description != "" || got.Design != "" || got.Notes != "" || got.AcceptanceCriteria != "" {
		t.Errorf("BriefDeps left free-form text on the row: %+v", got)
	}
	assertReaderCount(t, "DependencyCount under BriefDeps", details.DependencyCount, 1)
}

// RunReaderGetDetailShapeMatchesTheSeededIssue pins the shape of the detail view
// against what was actually stored (reader.go:15, reader.go:364-369): the
// issue's own fields, its labels, its OUTGOING edges with their types, and the
// three cardinalities. The direction split is the part worth pinning — an
// implementation that answered Dependencies with the incoming edges would still
// return a plausible-looking detail view.
func RunReaderGetDetailShapeMatchesTheSeededIssue(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	subject := readerID(fixture, "getshape", "subject")
	blocker := readerID(fixture, "getshape", "blocker")
	label := readerLabel(fixture, "getshape")
	seeded := readerIssue(subject, types.TypeBug, label)
	seeded.Title = "a title the detail view must echo"
	seeded.Priority = 1
	seedReaderIssue(t, ctx, fixture, seeded)
	seedReaderIssue(t, ctx, fixture, readerIssue(blocker, types.TypeTask, ""))
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: subject, DependsOnID: blocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the outgoing edge: %v", err)
	}

	details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: subject})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if details.Title != seeded.Title {
		t.Errorf("Title = %q, want %q", details.Title, seeded.Title)
	}
	if details.IssueType != types.TypeBug {
		t.Errorf("IssueType = %q, want %q", details.IssueType, types.TypeBug)
	}
	if details.Priority != 1 {
		t.Errorf("Priority = %d, want 1", details.Priority)
	}
	if !slices.Contains(details.Labels, label) {
		t.Errorf("Labels = %v, want it to carry the seeded label %q", details.Labels, label)
	}
	if got := readerDependencyIDs(details.Dependencies); !slices.Equal(got, []string{blocker}) {
		t.Errorf("Dependencies = %v, want the one outgoing edge to %s", got, blocker)
	}
	if len(details.Dependencies) == 1 && details.Dependencies[0].DependencyType != types.DepBlocks {
		t.Errorf("the outgoing edge came back with type %q, want %q", details.Dependencies[0].DependencyType, types.DepBlocks)
	}
	assertReaderCount(t, "DependencyCount", details.DependencyCount, 1)
	assertReaderCount(t, "DependentCount", details.DependentCount, 0)
	assertReaderCount(t, "CommentCount", details.CommentCount, 0)
}

// RunReaderDoesNotMutateTheCallerRequest is the role's single request-snapshot
// tripwire (reader.go:392-393). One case for the whole role, not one per
// method: the promise is a property of the shared implementation, and every
// method is driven here through the same request values so a normalization
// written in place is caught wherever it lives.
//
// The slices are the point. A builder that normalized labels or exclude-types
// into the caller's backing array instead of a new one would leave the request
// struct's header untouched and its CONTENTS changed, which is what
// reflect.DeepEqual over a deep copy sees and a shallow compare does not.
func RunReaderDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	limit := 5
	ready := publicops.ReadyRequest{
		Labels:         []string{"Beta", "alpha"},
		LabelsAny:      []string{"gamma", " delta "},
		ExcludeLabels:  []string{"omega"},
		ExcludeTypes:   []string{"chore,epic", " feat "},
		MetadataFields: map[string]string{"kind": "probe"},
		Sort:           "priority",
		Limit:          &limit,
	}
	list := publicops.ListRequest{
		IDFilter:       readerIDFilter(readerID(fixture, "nomut", "a"), readerID(fixture, "nomut", "b")),
		Labels:         []string{"Beta", "alpha"},
		LabelsAny:      []string{"gamma", " delta "},
		ExcludeLabels:  []string{"omega"},
		ExcludeTypes:   []string{"chore,epic", " feat "},
		MetadataFields: map[string]string{"kind": "probe"},
		SortBy:         "id",
		Limit:          &limit,
	}
	get := publicops.GetRequest{ID: readerID(fixture, "nomut", "absent")}

	readySnapshot := readerCopyReadyRequest(ready)
	listSnapshot := readerCopyListRequest(list)
	getSnapshot := get

	if _, err := fixture.Reader.Ready(ctx, ready); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if _, err := fixture.Reader.List(ctx, list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := fixture.Reader.Get(ctx, get); !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("Get on an absent id: %v", err)
	}

	if !reflect.DeepEqual(ready, readySnapshot) {
		t.Errorf("Ready mutated the caller's request\n after: %#v\nbefore: %#v", ready, readySnapshot)
	}
	if !reflect.DeepEqual(list, listSnapshot) {
		t.Errorf("List mutated the caller's request\n after: %#v\nbefore: %#v", list, listSnapshot)
	}
	if !reflect.DeepEqual(get, getSnapshot) {
		t.Errorf("Get mutated the caller's request\n after: %#v\nbefore: %#v", get, getSnapshot)
	}
}

// RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress walks
// ListRequest.Limit's vocabulary at the page boundary (reader.go:264-272) under
// a display order SQL CAN express, which is the only way to reach the seam this
// case exists for.
//
// Both existing List cases that set a limit sort by id, and workapi.SQLLimit
// zeroes the QUERY's limit for that sort (workapi/list.go:36-41) because
// natural-numeric order has to be applied in Go over the whole result set. So
// both of them run the fetch-everything-then-trim path and neither ever reaches
// the over-fetch. Under `--sort created` the limit is pushed into the query
// instead, and the two bodies detect truncation by genuinely different
// mechanisms: the store body asks the query for one row past the page
// (workapi.WithFetchOneExtra, storereader/reader.go:124,126) and lets the extra
// row's presence be the answer, while the unit-of-work body renders LIMIT n+1
// itself and reports the verdict natively (domain/db/issue_search.go:511-529).
// An off-by-one in either one shows here and nowhere else in this file:
// RunReaderReadyLimitBoundary pins the analogous mechanism for the READY query,
// and the store body derives its extra row differently per method — an inline
// limit+1 in Ready (storereader/reader.go:83-88) against WithFetchOneExtra in
// List.
//
// The page is asserted as a PREFIX of the unlimited answer rather than by id
// set. "Limit bounds the page the caller receives" is a bound on a page, not a
// license to answer with a different one, and a limit pushed into a query whose
// ORDER BY disagrees with the epilogue's would return the wrong END of the
// order while still returning the right COUNT.
func RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	var ids []string
	base := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Hour)
	for i, tag := range []string{"a", "b", "c"} {
		id := readerID(fixture, "lslimit", tag)
		ids = append(ids, id)
		issue := readerIssue(id, types.TypeTask, "")
		// Minutes apart and whole seconds, so `--sort created` is a total order
		// over these rows and "the first two" is a well-defined answer.
		at := base.Add(time.Duration(i) * time.Minute)
		issue.CreatedAt = at
		issue.UpdatedAt = at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	scope := readerIDFilter(ids...)

	full, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: scope, SortBy: "created", Limit: readerLimit(0),
	})
	if err != nil {
		t.Fatalf("List --sort created --limit 0: %v", err)
	}
	if len(full.Items) != len(ids) {
		t.Fatalf("List --sort created --limit 0 returned %v, want the three seeded rows", readerPageIDs(full))
	}
	if full.HasMore {
		t.Error("List --sort created --limit 0 is unlimited and can hide nothing, but reported HasMore")
	}
	order := readerPageIDs(full)

	for _, test := range []struct {
		name    string
		limit   *int
		wantN   int
		hasMore bool
	}{
		{"unset takes the shared list default, which does not truncate three rows", nil, 3, false},
		{"a limit under the result count truncates and says so", readerLimit(2), 2, true},
		{"a limit exactly at the result count hides nothing", readerLimit(3), 3, false},
		{"a limit of one is the tightest page the over-fetch has to get right", readerLimit(1), 1, true},
	} {
		page, err := fixture.Reader.List(ctx, publicops.ListRequest{
			IDFilter: scope, SortBy: "created", Limit: test.limit,
		})
		if err != nil {
			t.Fatalf("List --sort created (%s): %v", test.name, err)
		}
		assertReaderPageNotNil(t, "List --sort created ("+test.name+")", page)
		if len(page.Items) != test.wantN {
			t.Errorf("List --sort created (%s) returned %d items %v, want %d", test.name, len(page.Items), readerPageIDs(page), test.wantN)
		}
		if page.HasMore != test.hasMore {
			t.Errorf("List --sort created (%s) HasMore = %v, want %v", test.name, page.HasMore, test.hasMore)
		}
		assertReaderPageIsPrefixOf(t, "List --sort created ("+test.name+")", page, order)
	}
}

// RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions pins the three things
// the ready set decides for itself, which no request field overrides
// (reader.go:216-217, 228-235, 415-416).
//
// STATUS: ready work is open work — "Open only, not in_progress"
// (workapi/ready.go:44) — so neither an in_progress nor a closed row is in it.
// PINNED: it never returns pinned issues. TEMPLATES: it applies no template
// predicate AT ALL, which is the counter-intuitive half — the default listing's
// template exclusion does not reach this query, so an open task-type template IS
// ready work and IncludeTemplates changes nothing. And SkipLabels is not carried
// either: labels are hydrated on this arm either way.
//
// Asserted through BOTH doors, because they are two entry points to one query
// and the doc states the promise as a property of the SET rather than of either
// request. RunReaderListReadyFlagRefusesAFilterItCannotCarry pins only that the
// flags are accepted or refused, never what the rows underneath them are; the
// audit leaf that pins the pinned and type exclusions at the storage surface
// (audit_dependencies_readiness.go) runs through the Factory, which hands back a
// bare storage.DoltStorage, so it never runs on the unit-of-work backend at all
// — and that backend composes the shared ready WHERE into a UNION query of its
// own (domain/db/ready_work_union.go), where a dropped conjunct is exactly this
// class.
func RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdyset")
	open := readerID(fixture, "rdyset", "open")
	inProgress := readerID(fixture, "rdyset", "inprogress")
	closed := readerID(fixture, "rdyset", "closed")
	pinned := readerID(fixture, "rdyset", "pinned")
	template := readerID(fixture, "rdyset", "template")

	inProgressIssue := readerIssue(inProgress, types.TypeTask, scope)
	inProgressIssue.Status = types.StatusInProgress
	closedIssue := readerIssue(closed, types.TypeTask, scope)
	closedIssue.Status = types.StatusClosed
	pinnedIssue := readerIssue(pinned, types.TypeTask, scope)
	pinnedIssue.Pinned = true
	templateIssue := readerIssue(template, types.TypeTask, scope)
	templateIssue.IsTemplate = true

	seedReaderIssue(t, ctx, fixture, readerIssue(open, types.TypeTask, scope))
	seedReaderIssue(t, ctx, fixture, inProgressIssue)
	seedReaderIssue(t, ctx, fixture, closedIssue)
	seedReaderIssue(t, ctx, fixture, pinnedIssue)
	seedReaderIssue(t, ctx, fixture, templateIssue)

	// The template is IN the answer: the ready query has no template predicate,
	// and this type is not one it already excludes.
	want := []string{open, template}

	page, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{Labels: []string{scope}})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	assertReaderPageIDSet(t, "Ready over a scope holding an in_progress, a closed, a pinned and a template row", page, want)

	for _, test := range []struct {
		name string
		req  publicops.ListRequest
	}{
		{"List --ready", publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true}},
		{"List --ready --templates", publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true, IncludeTemplates: true}},
		{"List --ready --skip-labels", publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true, SkipLabels: true}},
	} {
		page, err := fixture.Reader.List(ctx, test.req)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		assertReaderPageIDSet(t, test.name, page, want)
		// SkipLabels is not carried onto the ready query, so the rows come back
		// hydrated on every one of these requests, not just the first two.
		assertReaderItemsCarryLabel(t, test.name, page, scope)
	}
}

// RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters pins three more
// entries from the --ready arm's CARRIED list (reader.go:226-232): Assignee,
// NoAssignee and the exact Priority.
//
// RunReaderListReadyFlagAnswersTheBlockerAwareSet pins that list for Labels
// alone. The rest of it reaches the ready query through
// workapi.ReadyFilterFromIssueFilter (sort.go:49-84), the projection that stands
// between "these filters reach the ready query" and a silently WIDER answer, and
// nothing pins that projection field by field at any layer: the builder goldens
// stop at BuildListFilter's IssueFilter, one step before it. A field dropped
// there reproduces the defect class this file's own header narrates — a silently
// dropped filter looks exactly like a passing test — so the assertion is that
// the answer NARROWS, with a row present that the filter has to leave out.
//
// The three chosen are the three projection lines with distinct shapes:
// Assignee copies a pointer, NoAssignee changes NAME on the way across (it
// becomes WorkFilter.Unassigned), and Priority is the pointer that exists
// because P0 has been lost to a value-plus-flag pair once already.
func RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "lsrdyf")
	mine := readerID(fixture, "lsrdyf", "mine")
	theirs := readerID(fixture, "lsrdyf", "theirs")
	nobodys := readerID(fixture, "lsrdyf", "nobodys")
	me := fixture.IssuePrefix + "-lsrdyf-me"
	them := fixture.IssuePrefix + "-lsrdyf-them"

	mineIssue := readerIssue(mine, types.TypeTask, scope)
	mineIssue.Assignee = me
	mineIssue.Priority = 1
	theirsIssue := readerIssue(theirs, types.TypeTask, scope)
	theirsIssue.Assignee = them
	theirsIssue.Priority = 3
	nobodysIssue := readerIssue(nobodys, types.TypeTask, scope)
	nobodysIssue.Priority = 3

	seedReaderIssue(t, ctx, fixture, mineIssue)
	seedReaderIssue(t, ctx, fixture, theirsIssue)
	seedReaderIssue(t, ctx, fixture, nobodysIssue)

	// The unfiltered arm first: all three rows are ready, so every narrowing
	// below is the filter's doing and not the scope's.
	page, err := fixture.Reader.List(ctx, publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true})
	if err != nil {
		t.Fatalf("List --ready: %v", err)
	}
	assertReaderPageIDSet(t, "List --ready with nothing to narrow it", page, []string{mine, theirs, nobodys})

	priority := 3
	for _, test := range []struct {
		name string
		req  publicops.ListRequest
		want []string
	}{
		{"List --ready --assignee", publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true, Assignee: me}, []string{mine}},
		{"List --ready --unassigned", publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true, NoAssignee: true}, []string{nobodys}},
		{"List --ready --priority", publicops.ListRequest{Labels: []string{scope}, ReadyFlag: true, Priority: &priority}, []string{theirs, nobodys}},
	} {
		page, err := fixture.Reader.List(ctx, test.req)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		assertReaderPageIDSet(t, test.name, page, test.want)
	}
}

// RunReaderListStatusAcceptsACommaSeparatedORSet pins the plural half of
// ListRequest.Status (reader.go:117-123): "one name, OR a comma-separated OR
// set", and either way it REPLACES the default exclusions rather than fighting
// them.
//
// The singular branch is pinned by
// RunReaderListDefaultExclusionsAndTheirOverrides and the request-side parse by
// the builder's golden (list_filter_golden.json, status_multi). The residue is
// the seam: the two branches render DIFFERENT SQL — `status = ?` against a
// `status IN (...)` placeholder set (sqlbuild/filter.go:86-92, and the same
// twin structure at sqlbuild/ready.go:96-105) — and no case drives the plural
// one end to end against a database. A set of THREE is deliberate: two entries
// is the smallest IN clause and would pass against a renderer that emitted only
// the first and the last.
func RunReaderListStatusAcceptsACommaSeparatedORSet(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	open := readerID(fixture, "lsstat", "open")
	inProgress := readerID(fixture, "lsstat", "inprogress")
	closed := readerID(fixture, "lsstat", "closed")

	inProgressIssue := readerIssue(inProgress, types.TypeTask, "")
	inProgressIssue.Status = types.StatusInProgress
	closedIssue := readerIssue(closed, types.TypeTask, "")
	closedIssue.Status = types.StatusClosed

	seedReaderIssue(t, ctx, fixture, readerIssue(open, types.TypeTask, ""))
	seedReaderIssue(t, ctx, fixture, inProgressIssue)
	seedReaderIssue(t, ctx, fixture, closedIssue)

	scope := readerIDFilter(open, inProgress, closed)
	for _, test := range []struct {
		name   string
		status string
		want   []string
	}{
		{"a two-status set reaches past the default closed exclusion", "closed,in_progress", []string{closed, inProgress}},
		{"a three-status set is the whole seeded scope", "open,in_progress,closed", []string{open, inProgress, closed}},
		{"whitespace around a member is the caller's, not the query's", " closed , open ", []string{closed, open}},
	} {
		page, err := fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: scope, Status: test.status})
		if err != nil {
			t.Fatalf("List --status %q (%s): %v", test.status, test.name, err)
		}
		assertReaderPageIDSet(t, "List --status "+test.status, page, test.want)
	}
}

// RunReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded pins what a
// bounded ready page IS (reader.go:85-100): the front of the answer the same
// request returns unlimited — row for row AND COUNT FOR COUNT.
//
// The counts are the half nothing pinned. Every row of a ready page is an
// IssueWithCounts (reader.go:11-12), and each cardinality is a function of the
// whole dependency graph rather than of the page, so bounding the page must not
// be able to move one. That is not free on either seam. The store-backed body
// answers a BOUNDED request with a different query from an unbounded one: it
// resolves the page of ids with the cheap indexed query and then hydrates the
// cardinalities constrained to exactly those ids, where an unbounded request
// runs the predicate-form mega-query (issueops.runReadyCountsInTx). The
// unit-of-work body resolves ids first either way — its own UNION across the
// two planes — and hydrates them by id (domain/db.fetchCountsByIDs). Three
// wirings, three routes to one promise, and `bd ready`'s "showing N of M" rides
// on all of them.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE that RunReaderReadyLimitBoundary's does
// not. That case seeds three interchangeable rows and reads only how many came
// back, so a seam that hydrated a row's counts from the page rather than the
// graph, or that paired a row with its neighbor's cardinalities, answers it
// correctly. Here every row carries its OWN count signature and the comparison
// is per row, so a mis-keyed hydration join has nowhere to land.
//
// THE EDGE TYPES ARE THE OTHER HALF. A ready page's cardinalities are
// BLOCKS-ONLY, where the detail view's count every edge — the split
// RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge owns — and every
// count fixture in this file seeded blocks edges alone, so an implementation
// that also counted parent-child and relates-to edges passed all of them. The
// `family` row below has two outgoing edges of neither type and must still
// report zero dependencies while carrying its Parent.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: the label that scopes it, and nothing
// else. The sort policy is NAMED — `oldest` is created_at ASC, id ASC — and the
// rows are seeded whole seconds apart, because created_at is a DATETIME with no
// fractional part and same-second rows would leave the word "prefix" resting on
// the id tiebreak alone.
func RunReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdycnt")
	id := func(name string) string { return readerID(fixture, "rdycnt", name) }
	plain, onedep, twodeps := id("plain"), id("onedep"), id("twodeps")
	depended, commented, family := id("depended"), id("commented"), id("family")
	closedA, closedB, closedC := id("closed-a"), id("closed-b"), id("closed-c")
	dependentA, dependentB := id("dependent-a"), id("dependent-b")
	parent, related := id("parent"), id("related")

	// The ready rows, in the order `oldest` will answer with. Whole seconds
	// apart; the helpers below are unlabeled, so they are outside the scope
	// this case queries no matter what state they are in.
	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	order := []string{plain, onedep, twodeps, depended, commented, family}
	for i, member := range order {
		issue := readerIssue(member, types.TypeTask, scope)
		at := base.Add(time.Duration(i) * time.Second)
		issue.CreatedAt = at
		issue.UpdatedAt = at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	// A CLOSED blocker is a blocks dependency that does not block, which is the
	// only way a row can be ready and carry a nonzero DependencyCount at once.
	for _, blocker := range []string{closedA, closedB, closedC} {
		closedIssue := readerIssue(blocker, types.TypeTask, "")
		closedIssue.Status = types.StatusClosed
		seedReaderIssue(t, ctx, fixture, closedIssue)
	}
	for _, helper := range []string{dependentA, dependentB, parent, related} {
		seedReaderIssue(t, ctx, fixture, readerIssue(helper, types.TypeTask, ""))
	}
	for _, edge := range []*types.Dependency{
		{IssueID: onedep, DependsOnID: closedA, Type: types.DepBlocks},
		{IssueID: twodeps, DependsOnID: closedB, Type: types.DepBlocks},
		{IssueID: twodeps, DependsOnID: closedC, Type: types.DepBlocks},
		{IssueID: dependentA, DependsOnID: depended, Type: types.DepBlocks},
		{IssueID: dependentB, DependsOnID: depended, Type: types.DepBlocks},
		{IssueID: family, DependsOnID: parent, Type: types.DepParentChild},
		{IssueID: family, DependsOnID: related, Type: types.DepRelatesTo},
	} {
		if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}
	for _, text := range []string{"first", "second"} {
		if err := fixture.AddComment(ctx, commented, "seed", text); err != nil {
			t.Fatalf("seed comment %q: %v", text, err)
		}
	}

	request := func(limit int) publicops.ReadyRequest {
		return publicops.ReadyRequest{Labels: []string{scope}, Sort: "oldest", Limit: readerLimit(limit)}
	}
	unbounded, err := fixture.Reader.Ready(ctx, request(0))
	if err != nil {
		t.Fatalf("Ready unlimited: %v", err)
	}
	assertReaderPageIDs(t, "Ready unlimited", unbounded, order)
	if unbounded.HasMore {
		t.Error("Ready unlimited reported HasMore")
	}

	// The exact cardinalities, on the unbounded answer, before any page is
	// compared against it. A prefix identity over two answers that were both
	// wrong the same way is the shape of assertion this program keeps
	// finding, so the reference is pinned to the seeds first.
	for _, want := range []readerCounts{
		{id: plain},
		{id: onedep, dependencies: 1},
		{id: twodeps, dependencies: 2},
		{id: depended, dependents: 2},
		{id: commented, comments: 2},
		{id: family, parent: &parent},
	} {
		assertReaderRowCounts(t, "Ready unlimited", unbounded, want)
	}

	for _, limit := range []int{1, 3, len(order), len(order) + 3} {
		page, pageErr := fixture.Reader.Ready(ctx, request(limit))
		if pageErr != nil {
			t.Errorf("Ready --limit %d: %v", limit, pageErr)
			continue
		}
		assertReaderPageIsPrefixWithItsCounts(t, fmt.Sprintf("Ready --limit %d", limit), page, unbounded, min(limit, len(order)))
		if want := limit < len(order); page.HasMore != want {
			t.Errorf("Ready --limit %d reported HasMore = %v, want %v", limit, page.HasMore, want)
		}
	}
}

// RunReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary is the same
// promise across the plane union (reader.go:62-65 with reader.go:85-100), and
// the union is where the two seams stop resembling each other. The store-backed
// body runs the ready-counts query ONCE PER TABLE FAMILY and merges the two
// results wisp-wins in Go (issueops.GetReadyWorkWithCountsInTx); the
// unit-of-work body renders one UNION ALL over both planes, orders it in SQL,
// and then hydrates each plane's ids separately
// (domain/db.getReadyWorkWithCountsUnion). A page bound that falls INSIDE the
// interleaved order is what makes those two arrangements answer differently:
// one of them can trim a plane before the merge, the other after it.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE that RunReaderReadyDeferredAndEphemeralGates
// does not. That case asks only WHICH rows the gate admits, with one wisp and
// no counts, and reads the answer as a set. Here the two planes ALTERNATE in
// the answer's order and both a durable row and a WISP row carry a nonzero
// cardinality, so a merge that ordered one plane after the other, or hydrated
// the wisp ids against the durable tables, changes the page rather than passing
// quietly. The wisp's own count is the load-bearing one: the reverse-blocker
// subquery reads both edge tables, and a wisp hydrated against the wrong family
// comes back with the zeros a caller cannot tell from a wisp that has none.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: the scoping label and the named sort,
// as above. The two planes' rows are seeded on alternating seconds so their
// interleaving is a property of the fixture rather than of whichever plane the
// implementation happens to read first.
func RunReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdyeph")
	id := func(name string) string { return readerID(fixture, "rdyeph", name) }
	durableOne, durableTwo, durableThree := id("i1"), id("i2"), id("i3")
	wispOne, wispTwo, wispThree := id("w1"), id("w2"), id("w3")
	closedBlocker, wispDependent := id("closed"), id("w-dependent")

	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	seeds := []struct {
		id        string
		ephemeral bool
	}{
		{durableOne, false}, {wispOne, true}, {durableTwo, false},
		{wispTwo, true}, {durableThree, false}, {wispThree, true},
	}
	order := make([]string, 0, len(seeds))
	for i, seed := range seeds {
		order = append(order, seed.id)
		issue := readerIssue(seed.id, types.TypeTask, scope)
		at := base.Add(time.Duration(i) * time.Second)
		issue.CreatedAt = at
		issue.UpdatedAt = at
		if seed.ephemeral {
			issue.Ephemeral = true
			seedReaderWisp(t, ctx, fixture, issue)
			continue
		}
		seedReaderIssue(t, ctx, fixture, issue)
	}
	closedIssue := readerIssue(closedBlocker, types.TypeTask, "")
	closedIssue.Status = types.StatusClosed
	seedReaderIssue(t, ctx, fixture, closedIssue)
	seedReaderIssue(t, ctx, fixture, readerIssue(wispDependent, types.TypeTask, ""))
	for _, edge := range []*types.Dependency{
		{IssueID: durableTwo, DependsOnID: closedBlocker, Type: types.DepBlocks},
		{IssueID: wispDependent, DependsOnID: wispTwo, Type: types.DepBlocks},
	} {
		if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}

	request := func(limit int) publicops.ReadyRequest {
		return publicops.ReadyRequest{
			Labels: []string{scope}, Sort: "oldest", IncludeEphemeral: true, Limit: readerLimit(limit),
		}
	}
	unbounded, err := fixture.Reader.Ready(ctx, request(0))
	if err != nil {
		t.Fatalf("Ready --include-ephemeral unlimited: %v", err)
	}
	assertReaderPageIDs(t, "Ready --include-ephemeral unlimited", unbounded, order)
	assertReaderRowCounts(t, "Ready --include-ephemeral unlimited", unbounded, readerCounts{id: durableTwo, dependencies: 1})
	assertReaderRowCounts(t, "Ready --include-ephemeral unlimited", unbounded, readerCounts{id: wispTwo, dependents: 1})

	// Every bound from inside the first plane's run to past the end. The
	// interesting ones are 3 and 4, where the cut lands between a wisp and the
	// durable row that follows it.
	for limit := 1; limit <= len(order)+1; limit++ {
		page, pageErr := fixture.Reader.Ready(ctx, request(limit))
		if pageErr != nil {
			t.Errorf("Ready --include-ephemeral --limit %d: %v", limit, pageErr)
			continue
		}
		assertReaderPageIsPrefixWithItsCounts(t, fmt.Sprintf("Ready --include-ephemeral --limit %d", limit), page, unbounded, min(limit, len(order)))
		if want := limit < len(order); page.HasMore != want {
			t.Errorf("Ready --include-ephemeral --limit %d reported HasMore = %v, want %v", limit, page.HasMore, want)
		}
	}
}

// RunReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix drives the same
// promise past the point where both seams stop asking one question.
//
// Neither implementation binds an unbounded id list into one statement: each
// hydrates a page's cardinalities in batches of two hundred ids and merges the
// batches in Go — issueops.runReadyCountsInTx against sqlbuild.QueryBatchSize,
// domain/db.fetchCountsByIDs against its own constant of the same value, two
// loops written separately. A page that fits in one batch cannot tell a working
// merge from a body that returns only the batch it happened to keep, and no
// fixture in this contract or in any other seeds past the boundary.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE: rows on BOTH SIDES of the boundary carry
// distinct nonzero counts, so a second batch that was dropped, overwritten by
// the first, or merged back in the wrong order fails on a row rather than on a
// length. The page is also driven at a bound that lands one row into the second
// batch, which is the off-by-one a loop's terminating condition gets wrong.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: the scoping label, the named sort and
// the batch constant itself, which is imported rather than transcribed — a
// backend that raised its own batching would otherwise leave this case seeding
// under the boundary it means to cross.
func RunReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	scope := readerLabel(fixture, "rdybatch")
	total := sqlbuild.QueryBatchSize + 5
	// The two rows that straddle the boundary: the last of the first batch and
	// the first of the second.
	lastOfFirst, firstOfSecond := sqlbuild.QueryBatchSize-1, sqlbuild.QueryBatchSize
	closedBlocker := readerID(fixture, "rdybatch", "closed")
	commentAnchor := readerID(fixture, "rdybatch", "anchor")

	base := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
	order := make([]string, 0, total)
	for i := range total {
		// Zero-padded so the id tiebreak agrees with the seeded order rather
		// than sorting 10 before 2 if two rows ever share a second.
		member := readerID(fixture, "rdybatch", fmt.Sprintf("%04d", i))
		order = append(order, member)
		issue := readerIssue(member, types.TypeTask, scope)
		at := base.Add(time.Duration(i) * time.Second)
		issue.CreatedAt = at
		issue.UpdatedAt = at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	closedIssue := readerIssue(closedBlocker, types.TypeTask, "")
	closedIssue.Status = types.StatusClosed
	seedReaderIssue(t, ctx, fixture, closedIssue)
	seedReaderIssue(t, ctx, fixture, readerIssue(commentAnchor, types.TypeTask, ""))
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: order[firstOfSecond], DependsOnID: closedBlocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the edge on the first row past the batch boundary: %v", err)
	}
	if err := fixture.AddComment(ctx, order[lastOfFirst], "seed", "the last row of the first batch"); err != nil {
		t.Fatalf("seed the comment on the last row of the first batch: %v", err)
	}

	request := func(limit int) publicops.ReadyRequest {
		return publicops.ReadyRequest{Labels: []string{scope}, Sort: "oldest", Limit: readerLimit(limit)}
	}
	unbounded, err := fixture.Reader.Ready(ctx, request(0))
	if err != nil {
		t.Fatalf("Ready unlimited over %d rows: %v", total, err)
	}
	assertReaderPageIDs(t, "Ready unlimited", unbounded, order)
	assertReaderRowCounts(t, "Ready unlimited", unbounded, readerCounts{id: order[lastOfFirst], comments: 1})
	assertReaderRowCounts(t, "Ready unlimited", unbounded, readerCounts{id: order[firstOfSecond], dependencies: 1})

	for _, limit := range []int{firstOfSecond + 1, total} {
		page, pageErr := fixture.Reader.Ready(ctx, request(limit))
		if pageErr != nil {
			t.Errorf("Ready --limit %d: %v", limit, pageErr)
			continue
		}
		assertReaderPageIsPrefixWithItsCounts(t, fmt.Sprintf("Ready --limit %d", limit), page, unbounded, min(limit, total))
	}
}

// RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge pins the exact
// cardinalities on BOTH read planes at once, and with them the fact that the
// two planes count different things.
//
// A page row's DependencyCount and DependentCount are BLOCKS-ONLY — outgoing
// blocks edges and reverse blockers — while a detail view's count EVERY
// outgoing and incoming edge whatever its type. Two vocabularies on one role,
// and a user sees both: `bd list` prints the first, `bd show` the second.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE. Every existing count fixture in this file
// seeds blocks edges alone — RunReaderGetDetailShapeMatchesTheSeededIssue,
// RunReaderGetOptionalRowListsAreOffByDefault and
// RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse all do — so under
// them the two vocabularies produce identical numbers and an implementation
// that swapped them, or that answered the page with the detail view's counts,
// passes every one. The subject below carries three outgoing edges of three
// types and three incoming ones, so the two answers are REQUIRED TO DIFFER and
// the case fails if they agree.
//
// The list plane's numbers are also pinned exactly rather than as nonzero.
// RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse needs its
// premise-check to see nonzero counts and asks for no more than that, so a
// mega-query that answered all three cardinalities with the same total edge
// count satisfied it. Here CommentCount is 2, DependencyCount is 1 and
// DependentCount is 2 — three different numbers on one row.
//
// PARENT IS THE FOURTH COLUMN and rides the same query as the counts without
// being one. Its value is pinned against the seeded edge, and a second row with
// no parent-child edge pins the nil, which is the arm a body returning the
// row's own id or an empty string would fail.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: the id set it scopes on. Both reads
// name one subject explicitly, so no other case's rows can reach either answer.
func RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	id := func(name string) string { return readerID(fixture, "cntsplit", name) }
	subject, orphan := id("subject"), id("orphan")
	blocker, parent, related := id("blocker"), id("parent"), id("related")
	blockedBy, alsoBlockedBy, child := id("blocked-by"), id("also-blocked-by"), id("child")

	for _, member := range []string{subject, orphan, blocker, parent, related, blockedBy, alsoBlockedBy, child} {
		seedReaderIssue(t, ctx, fixture, readerIssue(member, types.TypeTask, ""))
	}
	// Three out, three in, one type each way that the page counts and two it
	// does not.
	for _, edge := range []*types.Dependency{
		{IssueID: subject, DependsOnID: blocker, Type: types.DepBlocks},
		{IssueID: subject, DependsOnID: parent, Type: types.DepParentChild},
		{IssueID: subject, DependsOnID: related, Type: types.DepRelatesTo},
		{IssueID: blockedBy, DependsOnID: subject, Type: types.DepBlocks},
		{IssueID: alsoBlockedBy, DependsOnID: subject, Type: types.DepBlocks},
		{IssueID: child, DependsOnID: subject, Type: types.DepParentChild},
	} {
		if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}
	for _, text := range []string{"one", "two"} {
		if err := fixture.AddComment(ctx, subject, "seed", text); err != nil {
			t.Fatalf("seed comment %q: %v", text, err)
		}
	}

	page, err := fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: readerIDFilter(subject, orphan)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertReaderRowCounts(t, "List", page, readerCounts{
		id: subject, dependencies: 1, dependents: 2, comments: 2, parent: &parent,
	})
	assertReaderRowCounts(t, "List", page, readerCounts{id: orphan})

	details, err := fixture.Reader.Get(ctx, publicops.GetRequest{ID: subject})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertReaderCount(t, "Get DependencyCount", details.DependencyCount, 3)
	assertReaderCount(t, "Get DependentCount", details.DependentCount, 3)
	assertReaderCount(t, "Get CommentCount", details.CommentCount, 2)

	// Said once more as the thing itself, so a future change that quietly
	// unified the two vocabularies fails on the promise rather than on an
	// arithmetic detail of this fixture.
	row := readerRowByID(t, "List", page, subject)
	if row == nil {
		return
	}
	if details.DependencyCount != nil && int64(row.DependencyCount) == *details.DependencyCount {
		t.Errorf("the page and the detail view both answered DependencyCount = %d; the page counts blocks edges and the detail view counts every edge, and this subject has both kinds",
			row.DependencyCount)
	}
	if details.DependentCount != nil && int64(row.DependentCount) == *details.DependentCount {
		t.Errorf("the page and the detail view both answered DependentCount = %d; the page counts reverse blockers and the detail view counts every incoming edge, and this subject has both kinds",
			row.DependentCount)
	}
}

// RunReaderReadyParentScopesToItsTransitiveDescendants pins
// ReadyRequest.ParentID (reader.go:57-58) — a documented public field with no
// coverage at any leg until now.
//
// The promise is RECURSIVE DESCENDANTS, and it is served two ways at once. Both
// seams resolve the descendant id set by walking parent-child edges — one
// through issueops.GetDescendantIDsInTx, the other through the unit-of-work
// repository's own getDescendantIDs — and then OR that set with an id-prefix
// clause that admits dotted rows carrying no parent-child edge at all
// (sqlbuild.BuildReadyWorkWhere). A one-hop subquery in place of the walk is
// the defect this field already shipped once, GH#3396.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE. The audit leaf this came from gave every
// descendant BOTH a dotted id and a parent-child edge, so either mechanism
// alone answered it and neither could be shown to run: dropping the walk left
// the id prefix covering for it, and dropping the prefix left the walk covering
// for it. Here the two are separated. The ADOPTED row is a descendant by edge
// with an id under no prefix, so only the walk reaches it; the DOTTED row is a
// descendant by id with no edge, so only the prefix reaches it; and the
// GRANDCHILD is two hops down AND carries an edge, which puts it outside the
// prefix arm — the clause excludes rows that have a parent-child edge — so it
// is the one row that can only arrive through a walk that recurses.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: nothing. ParentID is the scope, and
// the decoy is a sibling of the parent rather than an unrelated row, so a
// prefix rendered without its separator (`LIKE 'p%'` rather than `LIKE 'p.%'`)
// takes the decoy and fails.
func RunReaderReadyParentScopesToItsTransitiveDescendants(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	parent := readerID(fixture, "rdypar", "p")
	child := parent + ".1"
	grandchild := parent + ".1.1"
	dotted := parent + ".9"
	adopted := readerID(fixture, "rdypar", "adopted")
	decoy := parent + "x"

	for _, member := range []string{parent, child, grandchild, dotted, adopted, decoy} {
		seedReaderIssue(t, ctx, fixture, readerIssue(member, types.TypeTask, ""))
	}
	for _, edge := range []*types.Dependency{
		{IssueID: child, DependsOnID: parent, Type: types.DepParentChild},
		{IssueID: grandchild, DependsOnID: child, Type: types.DepParentChild},
		{IssueID: adopted, DependsOnID: parent, Type: types.DepParentChild},
	} {
		if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}

	page, err := fixture.Reader.Ready(ctx, publicops.ReadyRequest{ParentID: parent, Sort: "oldest"})
	if err != nil {
		t.Fatalf("Ready --parent: %v", err)
	}
	// The parent itself is not its own descendant, and neither the sibling
	// decoy nor anything else in the workspace belongs to this answer.
	assertReaderPageIDSet(t, "Ready --parent", page, []string{child, grandchild, dotted, adopted})
}

// RunReaderListParentReachesEveryDescendantAndOnlyItsOwn is the same field on
// the listing plane (reader.go:213), where it is rendered differently: one
// clause with a parent-child subquery ORed against the id prefix
// (sqlbuild/filter.go), no precomputed id set and therefore no walk. That is
// why it gets a case of its own rather than an arm — the ready arm's descendant
// set is computed in Go and this one is not, so a break in either is invisible
// from the other.
//
// THE SIBLING PARENTS DIFFER ONLY IN CASE, which is the half no id-prefix case
// can borrow from elsewhere. The prefix is a LIKE against a binary-collated
// column, and a backend that folded case there would answer one parent's
// listing with the other parent's child — the bd-oyvc2.10 class. Both
// directions are driven, because a fold is symmetric and asserting one of them
// would leave the other reading as a pass.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE that the reader contract could not: no
// case in this file or in the querier's drives ParentID at all, so the whole
// field — descendant reach, case sensitivity, and the exclusion of the parent
// itself — was unpinned on all three legs.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: nothing beyond ParentID, which is the
// scope. The rows carry no parent-child edges at all, so what is measured is
// the prefix arm rather than the subquery arm; the ready case above owns the
// edge-derived half.
func RunReaderListParentReachesEveryDescendantAndOnlyItsOwn(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	lower := readerID(fixture, "lspar", "pc")
	upper := readerID(fixture, "lspar", "PC")
	lowerChild, upperChild := lower+".1", upper+".1"
	lowerGrandchild := lower + ".1.1"

	for _, member := range []string{lower, upper, lowerChild, upperChild, lowerGrandchild} {
		seedReaderIssue(t, ctx, fixture, readerIssue(member, types.TypeTask, ""))
	}

	for _, test := range []struct {
		parent string
		want   []string
	}{
		{lower, []string{lowerChild, lowerGrandchild}},
		{upper, []string{upperChild}},
	} {
		page, err := fixture.Reader.List(ctx, publicops.ListRequest{ParentID: test.parent})
		if err != nil {
			t.Fatalf("List --parent %s: %v", test.parent, err)
		}
		assertReaderPageIDSet(t, "List --parent "+test.parent, page, test.want)
	}
}

// RunReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing
// drives the keyset position the way its doc says a caller should
// (reader.go:292-296, "it does not skip or repeat rows when the result set
// changes underneath a walk") — page after page, to the end.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE that
// RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder's does not. That
// case reads ONE page from one hand-written position, over a group of two rows
// sharing a second. Both of its properties survive a body that walks correctly
// once and then stalls, and neither can see the shape that actually loses
// records: a same-second group LARGER THAN THE PAGE, where the position has to
// resume in the middle of a tie the timestamp alone cannot break. Five rows
// share one second here against a page of two, so the walk crosses that group
// twice.
//
// The walk is also where the two seams stop agreeing by construction. The
// keyset PREDICATE is one shared builder, but the page around it is not: one
// body over-fetches a probe row and the other reports has-more natively, and
// the trim runs in the shared epilogue after that. A body that fed the next
// position from the probe row rather than from the last DELIVERED row skips one
// record per page, which no single-page read can see.
//
// THE ONE-SHOT ORDER IS READ FIRST, so a backend that agrees with itself but
// orders differently from the reference fails on the sequence rather than on
// the walk — two different defects that would otherwise report the same way.
//
// THE EMPTY AfterID is the documented group-start form (types.go: an empty id
// starts the same-second group from its first id) and is driven on the same
// fixture, because it is the position a decoded cursor carries when it points
// at a second rather than at a row.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: the id set it scopes on, and nothing
// about the workspace. Every timestamp is a whole second, deliberately: the
// column has no fractional part, so a fixture written in milliseconds would
// have the engine, not the case, decide which rows tie.
func RunReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	newer := readerID(fixture, "kswalk", "newer")
	older := readerID(fixture, "kswalk", "older")
	group := []string{
		readerID(fixture, "kswalk", "a1"), readerID(fixture, "kswalk", "a2"),
		readerID(fixture, "kswalk", "a3"), readerID(fixture, "kswalk", "a4"),
		readerID(fixture, "kswalk", "a5"),
	}
	groupSecond := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)

	seed := func(memberID string, at time.Time) {
		issue := readerIssue(memberID, types.TypeTask, "")
		issue.CreatedAt = at
		issue.UpdatedAt = at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	seed(newer, groupSecond.Add(time.Second))
	for _, member := range group {
		seed(member, groupSecond)
	}
	seed(older, groupSecond.Add(-time.Second))

	want := append([]string{newer}, group...)
	want = append(want, older)
	idScope := readerIDFilter(want...)

	oneShot, err := fixture.Reader.List(ctx, publicops.ListRequest{IDFilter: idScope, SortBy: "created"})
	if err != nil {
		t.Fatalf("List unpaged: %v", err)
	}
	assertReaderPageIDs(t, "List unpaged", oneShot, want)

	const pageSize = 2
	var walked []string
	seen := make(map[string]bool, len(want))
	var afterCreatedAt *time.Time
	afterID := ""
	for page := 0; page <= len(want); page++ {
		got, pageErr := fixture.Reader.List(ctx, publicops.ListRequest{
			IDFilter: idScope, SortBy: "created", Limit: readerLimit(pageSize),
			AfterCreatedAt: afterCreatedAt, AfterID: afterID,
		})
		if pageErr != nil {
			t.Fatalf("List page %d: %v", page, pageErr)
		}
		if len(got.Items) == 0 {
			if got.HasMore {
				t.Errorf("List page %d came back empty with HasMore set", page)
			}
			break
		}
		if len(got.Items) > pageSize {
			t.Fatalf("List page %d answered %d rows over a Limit of %d", page, len(got.Items), pageSize)
		}
		for _, item := range got.Items {
			if item == nil || item.Issue == nil {
				t.Fatalf("List page %d returned a nil row", page)
			}
			if seen[item.ID] {
				t.Fatalf("List page %d repeated %s: the same-second group is larger than the page, and the position re-delivered a row it had already handed out",
					page, item.ID)
			}
			seen[item.ID] = true
			walked = append(walked, item.ID)
		}
		last := got.Items[len(got.Items)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !slices.Equal(walked, want) {
		t.Errorf("the keyset walk delivered %v, want the one-shot sequence %v with nothing dropped and nothing repeated", walked, want)
	}

	// The group-start form: a position naming the second with no id starts that
	// second from its first row rather than skipping the group.
	fromGroupStart, err := fixture.Reader.List(ctx, publicops.ListRequest{
		IDFilter: idScope, SortBy: "created", AfterCreatedAt: &groupSecond, AfterID: "",
	})
	if err != nil {
		t.Fatalf(`List from (the group's second, ""): %v`, err)
	}
	assertReaderPageIDs(t, `List from (the group's second, "")`, fromGroupStart, append(slices.Clone(group), older))
}

// RunReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates pins the
// keyset position as a CONJUNCT. It narrows what the rest of the request
// matched; it does not become the request.
//
// The three legs are cumulative on one fixture, so each predicate is shown to
// be load-bearing by the row only it excludes: the unpositioned read fixes what
// the id set and the status matched, adding the position drops the cursor row
// and everything newer, and adding CreatedBefore drops the same-second survivor
// the position had admitted. That last leg is the one worth having. The keyset
// renders its own upper bound on created_at, and that bound is INCLUSIVE —
// same-second rows are exactly what it exists to admit — where CreatedBefore's
// is strict. A body that let one displace the other answers this request with a
// row that is not before the time the caller named.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE that the existing keyset case's does not:
// that case sends a position and NOTHING ELSE, so composition is unpinned in
// both directions — a body that dropped the other predicates when a position
// arrived, and a body that dropped the position when other predicates did,
// both pass it.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: nothing. The shadow rows share every
// timestamp with the scoped ones and are excluded by the id set alone, so a
// position that widened its own scope collects them.
func RunReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	id := func(name string) string { return readerID(fixture, "kscompose", name) }
	newest, cursor := id("a1"), id("b1")
	closedSibling, sameSecond := id("b2"), id("b3")
	older, closedOlder, oldest := id("c1"), id("c2"), id("d1")

	second := func(offset int) time.Time {
		return time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour).Add(time.Duration(offset) * time.Minute)
	}
	oldestAt, olderAt, cursorAt, newestAt := second(0), second(1), second(2), second(3)
	seed := func(memberID string, at time.Time, status types.Status) {
		issue := readerIssue(memberID, types.TypeTask, "")
		issue.Status = status
		issue.CreatedAt = at
		issue.UpdatedAt = at
		seedReaderIssue(t, ctx, fixture, issue)
	}
	seed(newest, newestAt, types.StatusOpen)
	seed(cursor, cursorAt, types.StatusOpen)
	seed(closedSibling, cursorAt, types.StatusClosed)
	seed(sameSecond, cursorAt, types.StatusOpen)
	seed(older, olderAt, types.StatusOpen)
	seed(closedOlder, olderAt, types.StatusClosed)
	seed(oldest, oldestAt, types.StatusOpen)
	// A second population sharing every timestamp, kept out by the id set alone.
	for i, at := range []time.Time{newestAt, cursorAt, olderAt, oldestAt} {
		seed(id(fmt.Sprintf("shadow%d", i)), at, types.StatusOpen)
	}

	base := publicops.ListRequest{
		IDFilter: readerIDFilter(newest, cursor, closedSibling, sameSecond, older, closedOlder, oldest),
		Status:   string(types.StatusOpen),
		SortBy:   "created",
	}
	matched, err := fixture.Reader.List(ctx, base)
	if err != nil {
		t.Fatalf("List over the id set: %v", err)
	}
	assertReaderPageIDs(t, "List over the id set", matched, []string{newest, cursor, sameSecond, older, oldest})

	positioned := base
	positioned.AfterCreatedAt = &cursorAt
	positioned.AfterID = cursor
	resumed, err := fixture.Reader.List(ctx, positioned)
	if err != nil {
		t.Fatalf("List resumed from the cursor: %v", err)
	}
	assertReaderPageIDs(t, "List resumed from the cursor", resumed, []string{sameSecond, older, oldest})

	bounded := positioned
	bounded.CreatedBefore = &cursorAt
	narrowed, err := fixture.Reader.List(ctx, bounded)
	if err != nil {
		t.Fatalf("List resumed from the cursor under CreatedBefore: %v", err)
	}
	assertReaderPageIDs(t, "List resumed from the cursor under CreatedBefore", narrowed, []string{older, oldest})
}

// RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder pins what
// ListRequest.IncludeEphemeral ANSWERS WITH, which is the half of that promise
// an admission assertion cannot see: not the durable page with the ephemeral
// rows appended to it, but ONE page ordered across both planes as if they were
// a single table. That is what makes Limit and the keyset position mean the
// same thing under the flag that they mean without it.
//
// WHAT THIS FIXTURE MAKES OBSERVABLE that the admission row in
// RunReaderListDefaultExclusionsAndTheirOverrides does not. That row asks only
// WHICH rows the flag admits and reads the answer as a SET, so a body that
// concatenated one plane after the other passes it. Here the planes ALTERNATE
// in the created order, so a concatenation is a different SEQUENCE; a Limit
// that lands inside the interleaving keeps a different SET; and the keyset walk
// resumes from positions that fall on both sides of the merge.
//
// It is a genuine two-body case. The store-backed seam runs the search once per
// table family and merge-sorts the two independently ordered legs in Go before
// trimming (issueops.searchInTx, sortMergedResults); the unit-of-work seam
// renders one UNION ALL and lets SQL order it (domain/db.searchUnion). A trim
// applied before the merge, or an order applied after it, is a different page
// on exactly this fixture.
//
// WHAT IT DEPENDS ON FROM OUTSIDE ITSELF: the id set that scopes it and the
// named sort. The rows are seeded on alternating seconds, so the interleaving
// is a property of the fixture rather than of whichever plane an implementation
// happens to read first.
func RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	id := func(name string) string { return readerID(fixture, "lseph", name) }
	seeds := []struct {
		id        string
		ephemeral bool
	}{
		{id("i1"), false}, {id("w1"), true}, {id("i2"), false},
		{id("w2"), true}, {id("i3"), false}, {id("w3"), true},
	}

	firstSecond := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	var merged, durableOnly []string
	for i, seed := range seeds {
		issue := readerIssue(seed.id, types.TypeTask, "")
		at := firstSecond.Add(time.Duration(i) * time.Second)
		issue.CreatedAt = at
		issue.UpdatedAt = at
		if seed.ephemeral {
			issue.Ephemeral = true
			seedReaderWisp(t, ctx, fixture, issue)
		} else {
			seedReaderIssue(t, ctx, fixture, issue)
			durableOnly = append(durableOnly, seed.id)
		}
		merged = append(merged, seed.id)
	}
	// The answer is (created_at DESC, id ASC), so both expectations run
	// youngest first — the reverse of the seeding order.
	slices.Reverse(merged)
	slices.Reverse(durableOnly)

	scoped := publicops.ListRequest{IDFilter: readerIDFilter(merged...), SortBy: "created"}
	durable, err := fixture.Reader.List(ctx, scoped)
	if err != nil {
		t.Fatalf("List over both planes' ids without the flag: %v", err)
	}
	assertReaderPageIDs(t, "List without IncludeEphemeral", durable, durableOnly)

	admitted := scoped
	admitted.IncludeEphemeral = true
	all, err := fixture.Reader.List(ctx, admitted)
	if err != nil {
		t.Fatalf("List --include-ephemeral: %v", err)
	}
	assertReaderPageIDs(t, "List --include-ephemeral", all, merged)

	// Every bound from inside the first plane's run to past the end. The
	// interesting ones are the cuts that land between a wisp and the durable
	// row next to it, which is where a body that trimmed one plane before
	// merging keeps the wrong row.
	for limit := 1; limit <= len(merged)+1; limit++ {
		bounded := admitted
		bounded.Limit = readerLimit(limit)
		page, pageErr := fixture.Reader.List(ctx, bounded)
		if pageErr != nil {
			t.Errorf("List --include-ephemeral --limit %d: %v", limit, pageErr)
			continue
		}
		assertReaderPageIDs(t, fmt.Sprintf("List --include-ephemeral --limit %d", limit), page, merged[:min(limit, len(merged))])
		if want := limit < len(merged); page.HasMore != want {
			t.Errorf("List --include-ephemeral --limit %d reported HasMore = %v, want %v", limit, page.HasMore, want)
		}
	}

	// The keyset walk across the merge. The position is a created-order pair
	// and neither half of it names a plane, so a walk that resumes correctly on
	// one plane and skips the other is the failure this looks for: every page
	// but the first resumes from a row of the plane the previous page ended on.
	const pageSize = 2
	var walked []string
	seen := make(map[string]bool, len(merged))
	var afterCreatedAt *time.Time
	afterID := ""
	for page := 0; page <= len(merged); page++ {
		req := admitted
		req.Limit = readerLimit(pageSize)
		req.AfterCreatedAt = afterCreatedAt
		req.AfterID = afterID
		got, pageErr := fixture.Reader.List(ctx, req)
		if pageErr != nil {
			t.Fatalf("List --include-ephemeral page %d: %v", page, pageErr)
		}
		if len(got.Items) == 0 {
			if got.HasMore {
				t.Errorf("List --include-ephemeral page %d came back empty with HasMore set", page)
			}
			break
		}
		if len(got.Items) > pageSize {
			t.Fatalf("List --include-ephemeral page %d answered %d rows over a Limit of %d", page, len(got.Items), pageSize)
		}
		for _, item := range got.Items {
			if item == nil || item.Issue == nil {
				t.Fatalf("List --include-ephemeral page %d returned a nil row", page)
			}
			if seen[item.ID] {
				t.Fatalf("List --include-ephemeral page %d repeated %s: a position that crossed the plane boundary re-delivered a row it had already handed out",
					page, item.ID)
			}
			seen[item.ID] = true
			walked = append(walked, item.ID)
		}
		last := got.Items[len(got.Items)-1]
		at := last.CreatedAt.UTC()
		afterCreatedAt = &at
		afterID = last.ID
	}
	if !slices.Equal(walked, merged) {
		t.Errorf("the keyset walk over the merged planes delivered %v, want the one-shot sequence %v with nothing dropped and nothing repeated", walked, merged)
	}
}

// RunReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt pins
// ListRequest.WispType's interaction with the plane knob beside it.
//
// wisp_type is a COLUMN BOTH TABLES CARRY, so the field is a predicate and not
// a plane selector: it narrows whatever the rest of the request admitted. On a
// default listing that is the durable rows, which carry no classification, so
// the answer is EMPTY rather than "the wisps of that type" — the reading a
// caller is most likely to assume, and the one that would make the field a
// second, undocumented way to reach the ephemeral plane.
//
// The combination is LAWFUL rather than refused, and this asserts it composes
// as an ordinary AND: admit the plane, then narrow it to one classification.
// The second wisp carries a different type so the narrowing is visible as an
// exclusion rather than as a limit, and the durable row is inside the scope
// throughout so a body that dropped the predicate on the durable leg — the leg
// where matching it is pointless — fails on a row it let through.
func RunReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	id := func(name string) string { return readerID(fixture, "lswtyp", name) }
	durable, heartbeat, patrol := id("i1"), id("w-heartbeat"), id("w-patrol")

	seedReaderIssue(t, ctx, fixture, readerIssue(durable, types.TypeTask, ""))
	for _, seed := range []struct {
		id       string
		wispType types.WispType
	}{
		{heartbeat, types.WispTypeHeartbeat},
		{patrol, types.WispTypePatrol},
	} {
		wisp := readerIssue(seed.id, types.TypeTask, "")
		wisp.Ephemeral = true
		wisp.WispType = seed.wispType
		seedReaderWisp(t, ctx, fixture, wisp)
	}

	scope := readerIDFilter(durable, heartbeat, patrol)
	wispType := func(w types.WispType) *publicops.WispType { return &w }
	for _, test := range []struct {
		name string
		req  publicops.ListRequest
		want []string
	}{
		{"WispType alone admits no plane", publicops.ListRequest{IDFilter: scope, WispType: wispType(types.WispTypeHeartbeat)}, nil},
		{"IncludeEphemeral alone admits every classification", publicops.ListRequest{IDFilter: scope, IncludeEphemeral: true}, []string{durable, heartbeat, patrol}},
		{"the two compose as an AND", publicops.ListRequest{IDFilter: scope, IncludeEphemeral: true, WispType: wispType(types.WispTypeHeartbeat)}, []string{heartbeat}},
		{"and the other classification is reachable the same way", publicops.ListRequest{IDFilter: scope, IncludeEphemeral: true, WispType: wispType(types.WispTypePatrol)}, []string{patrol}},
	} {
		page, err := fixture.Reader.List(ctx, test.req)
		if err != nil {
			t.Fatalf("List (%s): %v", test.name, err)
		}
		assertReaderPageIDSet(t, "List ("+test.name+")", page, test.want)
	}
}

// RunReaderListBriefDropsTheFreeFormTextAndNothingElse pins
// ListRequest.Brief. It is the SkipCounts case's shape, on the other kind of
// payload, and both halves are load-bearing for the same reason:
//
//   - all six free-form fields come back zero-valued on a row that genuinely
//     carries each of them, so the knob demonstrably reached the SELECT rather
//     than being accepted and dropped; and
//   - NOTHING ELSE MOVES. Same rows, same order, same identity fields, same
//     counts, same labels, same Parent, same has-more verdict as the identical
//     request without the knob.
//
// EVERY ASSERTED FIELD IS SEEDED HEAVY, which is the discipline the sibling
// projection on the detail view had to be sent back for: a case that asserts
// Design and AcceptanceCriteria empty without seeding them passes on a body
// that strips two of the six and leaks four.
//
// IDENTITY IS ASSERTED, not assumed. Brief keeps Title, and the lite SELECT
// deliberately retains the small routing columns beside it; a body that
// stripped the row down to its id would satisfy every emptiness clause above.
//
// IsLitePartial IS THE HALF NO WIRE CONSUMER CAN SEE. All six fields are
// omitempty, so a projected row marshals identically to a genuinely textless
// one — the ambiguity ga-clgh and CommentsOmitted already record. The flag is
// how an in-process caller tells them apart, so a body that blanked the fields
// without setting it would answer correctly and lie about why.
func RunReaderListBriefDropsTheFreeFormTextAndNothingElse(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	subject := readerID(fixture, "lsbrief", "subject")
	blocker := readerID(fixture, "lsbrief", "blocker")
	dependent := readerID(fixture, "lsbrief", "dependent")
	parent := readerID(fixture, "lsbrief", "parent")
	label := readerLabel(fixture, "lsbrief")

	for _, id := range []string{subject, blocker, dependent, parent} {
		seedReaderIssue(t, ctx, fixture, readerHeavyIssue(id, label))
	}
	// The same three edges and one comment the SkipCounts case seeds, so the
	// counts and Parent this one holds STILL are nonzero and can be tripwires
	// for a projection that suppressed more than the text.
	for _, edge := range []*types.Dependency{
		{IssueID: subject, DependsOnID: blocker, Type: types.DepBlocks},
		{IssueID: dependent, DependsOnID: subject, Type: types.DepBlocks},
		{IssueID: subject, DependsOnID: parent, Type: types.DepParentChild},
	} {
		if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}
	if err := fixture.AddComment(ctx, subject, "seed", "so the comment count is nonzero"); err != nil {
		t.Fatalf("seed the comment: %v", err)
	}

	req := publicops.ListRequest{IDFilter: readerIDFilter(subject, blocker, dependent, parent), SortBy: "created"}
	hydrated, err := fixture.Reader.List(ctx, req)
	if err != nil {
		t.Fatalf("List with the text hydrated: %v", err)
	}
	hydratedRow := readerRowByID(t, "List with the text hydrated", hydrated, subject)
	if hydratedRow == nil {
		return
	}
	assertReaderHeavyPremise(t, "List", hydratedRow.Issue)
	if hydratedRow.DependencyCount == 0 || hydratedRow.DependentCount == 0 || hydratedRow.CommentCount == 0 || hydratedRow.Parent == nil {
		t.Fatalf("the seeded subject came back with counts (%d, %d, %d) and Parent %v; this case needs all of them populated before it can assert Brief leaves them alone",
			hydratedRow.DependencyCount, hydratedRow.DependentCount, hydratedRow.CommentCount, readerParentText(hydratedRow.Parent))
	}

	req.Brief = true
	brief, err := fixture.Reader.List(ctx, req)
	if err != nil {
		t.Fatalf("List with Brief: %v", err)
	}
	briefRow := readerRowByID(t, "List with Brief", brief, subject)
	if briefRow == nil {
		return
	}
	assertReaderBriefRow(t, "List with Brief", briefRow.Issue, hydratedRow.Issue)

	if !slices.Equal(readerPageIDs(brief), readerPageIDs(hydrated)) {
		t.Errorf("List with Brief returned %v, want the same page as without it, %v: this knob chooses what is hydrated, never which rows match",
			readerPageIDs(brief), readerPageIDs(hydrated))
	}
	if brief.HasMore != hydrated.HasMore {
		t.Errorf("List with Brief reported HasMore = %v, want %v", brief.HasMore, hydrated.HasMore)
	}
	for _, got := range []struct {
		what      string
		got, want int
	}{
		{"DependencyCount", briefRow.DependencyCount, hydratedRow.DependencyCount},
		{"DependentCount", briefRow.DependentCount, hydratedRow.DependentCount},
		{"CommentCount", briefRow.CommentCount, hydratedRow.CommentCount},
	} {
		if got.got != got.want {
			t.Errorf("List with Brief returned %s = %d, want %d: Brief bounds a row's TEXT and no aggregate beside it", got.what, got.got, got.want)
		}
	}
	if !readerSameParent(briefRow.Parent, hydratedRow.Parent) {
		t.Errorf("List with Brief returned Parent = %v, want %v: Parent is not free-form text and rides the same query",
			readerParentText(briefRow.Parent), readerParentText(hydratedRow.Parent))
	}
	if !slices.Equal(briefRow.Labels, hydratedRow.Labels) {
		t.Errorf("List with Brief returned Labels = %v, want %v: labels are their own opt-out (SkipLabels) and this is not it", briefRow.Labels, hydratedRow.Labels)
	}
}

// RunReaderReadyBriefDropsTheFreeFormTextAndNothingElse is ListRequest.Brief's
// twin on the ready plane. It gets a case of its own rather than an arm because
// the two operations reach the projection through DIFFERENT filters: the
// listing carries types.IssueFilter.Lite and ready carries
// types.WorkFilter.Lite, built by a different builder and read by a different
// hydration helper on each backend. A break in either is invisible from the
// other, and the ready side is the one no route reaches through this role from
// the CLI, so this case is the only thing holding it.
func RunReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t *testing.T, ctx context.Context, fixture ReaderFixture) {
	t.Helper()
	label := readerLabel(fixture, "rdybrief")
	subject := readerID(fixture, "rdybrief", "subject")
	dependent := readerID(fixture, "rdybrief", "dependent")

	for _, id := range []string{subject, dependent} {
		seedReaderIssue(t, ctx, fixture, readerHeavyIssue(id, label))
	}
	// An INCOMING blocks edge only. It leaves the subject itself unblocked, so
	// it still qualifies for ready work, while giving its row a nonzero
	// DependentCount for the same tripwire the listing case uses.
	if err := fixture.AddDependency(ctx, &types.Dependency{IssueID: dependent, DependsOnID: subject, Type: types.DepBlocks}, "seed"); err != nil {
		t.Fatalf("seed edge %s -> %s: %v", dependent, subject, err)
	}
	if err := fixture.AddComment(ctx, subject, "seed", "so the comment count is nonzero"); err != nil {
		t.Fatalf("seed the comment: %v", err)
	}

	req := publicops.ReadyRequest{Labels: []string{label}, Sort: "oldest"}
	hydrated, err := fixture.Reader.Ready(ctx, req)
	if err != nil {
		t.Fatalf("Ready with the text hydrated: %v", err)
	}
	hydratedRow := readerRowByID(t, "Ready with the text hydrated", hydrated, subject)
	if hydratedRow == nil {
		return
	}
	assertReaderHeavyPremise(t, "Ready", hydratedRow.Issue)
	if hydratedRow.DependentCount == 0 || hydratedRow.CommentCount == 0 {
		t.Fatalf("the seeded subject came back with DependentCount %d and CommentCount %d; this case needs both nonzero before it can assert Brief leaves them alone",
			hydratedRow.DependentCount, hydratedRow.CommentCount)
	}

	req.Brief = true
	brief, err := fixture.Reader.Ready(ctx, req)
	if err != nil {
		t.Fatalf("Ready with Brief: %v", err)
	}
	briefRow := readerRowByID(t, "Ready with Brief", brief, subject)
	if briefRow == nil {
		return
	}
	assertReaderBriefRow(t, "Ready with Brief", briefRow.Issue, hydratedRow.Issue)

	if !slices.Equal(readerPageIDs(brief), readerPageIDs(hydrated)) {
		t.Errorf("Ready with Brief returned %v, want the same page as without it, %v", readerPageIDs(brief), readerPageIDs(hydrated))
	}
	if brief.HasMore != hydrated.HasMore {
		t.Errorf("Ready with Brief reported HasMore = %v, want %v", brief.HasMore, hydrated.HasMore)
	}
	// Ready carries no SkipLabels and no SkipCounts (issueops.readyHydrationFor),
	// so an implementation that reached for the listing's hydration helper here
	// would drop these along with the text.
	if briefRow.DependentCount != hydratedRow.DependentCount || briefRow.CommentCount != hydratedRow.CommentCount {
		t.Errorf("Ready with Brief returned DependentCount %d / CommentCount %d, want %d / %d: a ready filter carries no counts opt-out",
			briefRow.DependentCount, briefRow.CommentCount, hydratedRow.DependentCount, hydratedRow.CommentCount)
	}
	if !slices.Equal(briefRow.Labels, hydratedRow.Labels) {
		t.Errorf("Ready with Brief returned Labels = %v, want %v: a ready filter carries no label opt-out either", briefRow.Labels, hydratedRow.Labels)
	}
}

// readerHeavyFields is what the two Brief cases seed and assert on: every
// free-form column the lite SELECT drops (issueops.HeavyDropList). It is one
// list so the seed and the assertions cannot fall out of step, which is the
// exact way the sibling projection's first draft went vacuous.
func readerHeavyFields(issue *types.Issue) []struct {
	what string
	text string
} {
	return []struct {
		what string
		text string
	}{
		{"Description", issue.Description},
		{"Design", issue.Design},
		{"AcceptanceCriteria", issue.AcceptanceCriteria},
		{"Notes", issue.Notes},
		{"Payload", issue.Payload},
		{"Waiters", strings.Join(issue.Waiters, ",")},
	}
}

// readerHeavyIssue is readerIssue with every droppable field carrying text.
func readerHeavyIssue(id, label string) *types.Issue {
	issue := readerIssue(id, types.TypeTask, label)
	issue.Description = id + " description body"
	issue.Design = id + " design body"
	issue.AcceptanceCriteria = id + " acceptance criteria body"
	issue.Notes = id + " notes body"
	issue.Payload = id + " payload body"
	issue.Waiters = []string{id + "-waiter"}
	return issue
}

// assertReaderHeavyPremise fails the case when a field it is about to assert
// EMPTY did not arrive populated. Without it, a backend that never round-trips
// one of the six turns this case's clause for that field into a clause that
// cannot fail.
func assertReaderHeavyPremise(t *testing.T, what string, issue *types.Issue) {
	t.Helper()
	for _, field := range readerHeavyFields(issue) {
		if field.text == "" {
			t.Fatalf("%s returned the seeded subject with an empty %s; this case needs every field it asserts stripped to arrive populated, or that assertion cannot fail",
				what, field.what)
		}
	}
	if issue.IsLitePartial {
		t.Fatalf("%s returned the seeded subject with IsLitePartial already set on a full read", what)
	}
}

// assertReaderBriefRow holds a projected row to both halves of the promise: the
// text is gone, the identity is not, and the row says which it is.
func assertReaderBriefRow(t *testing.T, what string, brief, full *types.Issue) {
	t.Helper()
	for _, field := range readerHeavyFields(brief) {
		if field.text != "" {
			t.Errorf("%s returned %s = %q, want empty: Brief was accepted and the column selected anyway", what, field.what, field.text)
		}
	}
	if !brief.IsLitePartial {
		t.Errorf("%s returned IsLitePartial = false on a projected row: a blank body and a genuinely textless one are indistinguishable without it", what)
	}
	for _, id := range []struct {
		what      string
		got, want string
	}{
		{"ID", brief.ID, full.ID},
		{"Title", brief.Title, full.Title},
		{"Status", string(brief.Status), string(full.Status)},
		{"IssueType", string(brief.IssueType), string(full.IssueType)},
	} {
		if id.got != id.want {
			t.Errorf("%s returned %s = %q, want %q: Brief drops the free-form text and keeps everything a caller picks a row by", what, id.what, id.got, id.want)
		}
	}
	if brief.Priority != full.Priority {
		t.Errorf("%s returned Priority = %d, want %d", what, brief.Priority, full.Priority)
	}
}

// readerIssue builds the seed every case starts from: an open, unassigned,
// unblocked task that qualifies for ready work. A case that needs it to fail one
// of those tests changes the field it means to test and nothing else.
func readerIssue(id string, issueType types.IssueType, label string) *types.Issue {
	issue := &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: issueType,
	}
	if label != "" {
		issue.Labels = []string{label}
	}
	return issue
}

func seedReaderIssue(t *testing.T, ctx context.Context, fixture ReaderFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func seedReaderWisp(t *testing.T, ctx context.Context, fixture ReaderFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", issue.ID, err)
	}
}

// readerID namespaces one seeded id by the fixture's prefix and the case's own
// tag, so every case in a suite can share one database.
func readerID(fixture ReaderFixture, caseTag, name string) string {
	return fixture.IssuePrefix + "-" + caseTag + "-" + name
}

// readerLabel is how a ready case scopes its query: WorkFilter carries no id
// set, so the rows a ready assertion owns are the rows carrying its label.
func readerLabel(fixture ReaderFixture, caseTag string) string {
	return fixture.IssuePrefix + "-scope-" + caseTag
}

// readerIDFilter is how a list case scopes its query. The default listing
// answers with the whole workspace, and on the unit-of-work backend that
// workspace is shared by every case in the suite.
func readerIDFilter(ids ...string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += id
	}
	return out
}

func readerLimit(n int) *int { return &n }

// readerRowByID picks one row out of a page by id, failing the case rather than
// returning a zero row: an assertion about a row that is not there would
// otherwise read as an assertion that passed.
func readerRowByID(t *testing.T, what string, page publicops.IssuePage, id string) *types.IssueWithCounts {
	t.Helper()
	for _, item := range page.Items {
		if item != nil && item.Issue != nil && item.ID == id {
			return item
		}
	}
	t.Errorf("%s returned %v, which does not contain %s", what, readerPageIDs(page), id)
	return nil
}

// assertReaderUnsupported is GONE. It existed for the two "honored or refused"
// cases above, which accepted a typed *ErrUnsupported in place of the
// behavior; nothing on this role is unsupported by any implementation now, so
// keeping a helper that accepts a refusal would keep the disjunction available
// to the next case that finds one convenient. *ErrUnsupported is still a real
// contract elsewhere — conformance.go:100 pins it for the capabilities a
// backend genuinely does not have.

func readerSameParent(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func readerParentText(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func readerPageIDs(page publicops.IssuePage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item == nil || item.Issue == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, item.ID)
	}
	return out
}

func readerDependencyIDs(rows []*types.IssueWithDependencyMetadata) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, row.ID)
	}
	return out
}

// assertReaderPageNotNil is the half of the page shape every assertion owes:
// Items is never nil for a successful call (reader.go:376-381).
func assertReaderPageNotNil(t *testing.T, what string, page publicops.IssuePage) {
	t.Helper()
	if page.Items == nil {
		t.Errorf("%s returned a nil Items; an empty page is an empty slice", what)
	}
}

// assertReaderPageIDs compares the page in ORDER. Use it only where the request
// named a display order or a sort policy; storage order is not a promise.
func assertReaderPageIDs(t *testing.T, what string, page publicops.IssuePage, want []string) {
	t.Helper()
	assertReaderPageNotNil(t, what, page)
	got := readerPageIDs(page)
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s returned %v, want %v in that order", what, got, want)
	}
}

// assertReaderPageIDSet compares the page as a SET, for requests whose order the
// contract does not promise.
func assertReaderPageIDSet(t *testing.T, what string, page publicops.IssuePage, want []string) {
	t.Helper()
	assertReaderPageNotNil(t, what, page)
	got := slices.Clone(readerPageIDs(page))
	wantSorted := slices.Clone(want)
	slices.Sort(got)
	slices.Sort(wantSorted)
	if len(got) == 0 && len(wantSorted) == 0 {
		return
	}
	if !slices.Equal(got, wantSorted) {
		t.Errorf("%s returned %v, want exactly %v", what, got, wantSorted)
	}
}

// assertReaderPageIsPrefixOf compares a limited page against the order the same
// request answers with UNLIMITED. A limit bounds the page a caller receives, so
// the page it does receive is the front of that order — not the same number of
// rows taken from somewhere else in it.
func assertReaderPageIsPrefixOf(t *testing.T, what string, page publicops.IssuePage, order []string) {
	t.Helper()
	got := readerPageIDs(page)
	if len(got) > len(order) {
		t.Errorf("%s returned %v, which is longer than the unlimited answer %v", what, got, order)
		return
	}
	if !slices.Equal(got, order[:len(got)]) {
		t.Errorf("%s returned %v, want the first %d of the unlimited order %v", what, got, len(got), order)
	}
}

// assertReaderItemsCarryLabel is the hydration half of a page assertion: every
// row came back with the label the request scoped on.
func assertReaderItemsCarryLabel(t *testing.T, what string, page publicops.IssuePage, label string) {
	t.Helper()
	for _, item := range page.Items {
		if item == nil || item.Issue == nil {
			t.Errorf("%s returned a nil row", what)
			continue
		}
		if !slices.Contains(item.Labels, label) {
			t.Errorf("%s returned %s with labels %v, want it to carry %q: labels are hydrated on this arm either way", what, item.ID, item.Labels, label)
		}
	}
}

// readerCounts is one row's expected cardinalities on a PAGE — the blocks-only
// vocabulary, which is not the detail view's. The zero value is the row that
// has none of them, so a case names only what it seeded.
type readerCounts struct {
	id           string
	dependencies int
	dependents   int
	comments     int
	parent       *string
}

func readerCountsOf(row *types.IssueWithCounts) readerCounts {
	return readerCounts{
		id:           row.ID,
		dependencies: row.DependencyCount,
		dependents:   row.DependentCount,
		comments:     row.CommentCount,
		parent:       row.Parent,
	}
}

func (c readerCounts) String() string {
	return fmt.Sprintf("%s{dependencies:%d dependents:%d comments:%d parent:%s}",
		c.id, c.dependencies, c.dependents, c.comments, readerParentText(c.parent))
}

func (c readerCounts) equal(other readerCounts) bool {
	return c.id == other.id &&
		c.dependencies == other.dependencies &&
		c.dependents == other.dependents &&
		c.comments == other.comments &&
		readerSameParent(c.parent, other.parent)
}

// assertReaderRowCounts pins one row's cardinalities EXACTLY. Nonzero is not
// enough: the three aggregates hang off separate joins over the same edge
// table, and a body that answered every one of them with the row's total edge
// count would satisfy "nonzero" everywhere it matters.
func assertReaderRowCounts(t *testing.T, what string, page publicops.IssuePage, want readerCounts) {
	t.Helper()
	row := readerRowByID(t, what, page, want.id)
	if row == nil {
		return
	}
	if got := readerCountsOf(row); !got.equal(want) {
		t.Errorf("%s returned %s, want %s", what, got, want)
	}
}

// assertReaderPageIsPrefixWithItsCounts compares a bounded page against the
// answer the same request gives unbounded, CARDINALITIES INCLUDED. It is the
// stronger sibling of assertReaderPageIsPrefixOf, which compares ids alone:
// each count is a function of the whole graph, so the page a bound produces
// carries the same numbers the unbounded answer did for those rows.
//
// wantN is the length the bound calls for, and it is a SEPARATE argument
// because a row-for-row comparison over whatever came back agrees with itself:
// a body that delivered only the first of the batches it hydrates answers a
// short page every one of whose rows is correct.
func assertReaderPageIsPrefixWithItsCounts(t *testing.T, what string, page, unbounded publicops.IssuePage, wantN int) {
	t.Helper()
	assertReaderPageNotNil(t, what, page)
	if len(page.Items) != wantN {
		t.Errorf("%s returned %d rows, want %d: %v against the unbounded answer %v",
			what, len(page.Items), wantN, readerPageIDs(page), readerPageIDs(unbounded))
		return
	}
	if len(page.Items) > len(unbounded.Items) {
		t.Errorf("%s returned %v, which is longer than the unbounded answer %v",
			what, readerPageIDs(page), readerPageIDs(unbounded))
		return
	}
	for i, item := range page.Items {
		reference := unbounded.Items[i]
		if item == nil || item.Issue == nil || reference == nil || reference.Issue == nil {
			t.Errorf("%s returned a nil row at position %d", what, i)
			continue
		}
		got, want := readerCountsOf(item), readerCountsOf(reference)
		if !got.equal(want) {
			t.Errorf("%s at position %d returned %s, want %s — a bound chooses how many rows a caller receives, never what any of them says",
				what, i, got, want)
		}
	}
}

func assertReaderCount(t *testing.T, what string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil; the detail view carries counts whether or not the rows were requested", what)
		return
	}
	if *got != want {
		t.Errorf("%s = %d, want %d", what, *got, want)
	}
}

// readerCopyReadyRequest and readerCopyListRequest deep-copy the reference
// fields the tripwire watches. A shallow copy would share the same backing
// arrays as the original and compare equal no matter what the implementation
// did to them.
func readerCopyReadyRequest(in publicops.ReadyRequest) publicops.ReadyRequest {
	out := in
	out.Labels = slices.Clone(in.Labels)
	out.LabelsAny = slices.Clone(in.LabelsAny)
	out.ExcludeLabels = slices.Clone(in.ExcludeLabels)
	out.ExcludeTypes = slices.Clone(in.ExcludeTypes)
	out.MetadataFields = readerCopyStringMap(in.MetadataFields)
	if in.Limit != nil {
		limit := *in.Limit
		out.Limit = &limit
	}
	return out
}

func readerCopyListRequest(in publicops.ListRequest) publicops.ListRequest {
	out := in
	out.Labels = slices.Clone(in.Labels)
	out.LabelsAny = slices.Clone(in.LabelsAny)
	out.ExcludeLabels = slices.Clone(in.ExcludeLabels)
	out.ExcludeTypes = slices.Clone(in.ExcludeTypes)
	out.MetadataFields = readerCopyStringMap(in.MetadataFields)
	if in.Limit != nil {
		limit := *in.Limit
		out.Limit = &limit
	}
	return out
}

func readerCopyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
