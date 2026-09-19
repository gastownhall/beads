package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

const (
	// claimActorMember is the only member ClaimRequest carries. The schema is
	// additionalProperties: false, so anything else is refused by name.
	claimActorMember = "actor"
	// maxActorBytes is the document's cap on `actor`. The schema's maxLength
	// counts characters; this byte limit is the binding one.
	maxActorBytes = 256
	// maxJSONBodyBytes bounds every request body on this surface. The largest
	// documented one is a handful of short members, so this is pure refusal of
	// the absurd.
	maxJSONBodyBytes = 1 << 20
	// claimContentType is the one media type this surface accepts on a body —
	// requireJSONContent enforces it for every body-carrying operation, not
	// only the claim — and refusing anything else is a CSRF control, not
	// pedantry: a JSON content type is not CORS-"simple", so a cross-origin
	// write always triggers a preflight this server never approves. Accepting
	// text/plain or a form encoding would let an attacker's page skip the
	// preflight and drive a write from any browser on the host.
	claimContentType = "application/json"
)

// handleClaim is v0's compare-and-set claim of a single issue for a
// caller-named actor, and the write whose posture every later one adopted.
//
// ACTOR SEMANTICS, stated because adopting this endpoint depends on them. The
// actor is caller-ASSERTED provenance for the audit trail and is NOT the
// authenticated principal, even where a bearer is required: the token a
// deployment configures is shared and surface-wide, so it names nobody and
// cannot confirm or contradict the actor a request sends. Any client that
// reaches this endpoint can claim as any name, exactly as any local process
// can pass any --actor to the CLI. The CAS is therefore a correctness fence
// against CONCURRENT claims, not an authorization boundary — it guarantees
// that two racing claimants cannot both win, and guarantees nothing about who
// either of them really is. What bounds the blast radius of that posture is
// the bind: loopback by default, and beyond loopback only with a token file
// (or the explicit --insecure-no-auth).
//
// Two things a CLI claim does that this deliberately does not: hooks do not
// fire (a user-controlled subprocess per mutation is an unbounded latency
// multiplier and an orphaned child at shutdown), and the per-command
// auto-commit machinery never runs. The only durable effect is the single
// storage commit the claim role makes inside its own transaction — which is
// exactly what a proxied CLI write does today.
//
// Everything above the role here is argument validation: the path split, the
// media type, the body shape and the actor rules. The claim itself — the CAS,
// the eligibility rules, the claim pools, the transaction retry and the
// refusal vocabulary — belongs to issueops.Claimer, reached through the
// provider's own accessor.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	// The custom-method dispatcher split the id off the segment and bounded it
	// before this handler was chosen at all; see customMethodTarget.
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	actor, ok := s.claimActor(w, r)
	if !ok {
		return
	}

	claimer, err := s.claimer(r)
	if err != nil {
		s.failClaim(w, r, err)
		return
	}
	result, err := claimer.Claim(r.Context(), issueops.ClaimRequest{IssueID: id, Actor: actor})
	if err != nil {
		s.failClaim(w, r, err)
		return
	}
	// `already_claimed` is the wire's name for the idempotent re-claim, which
	// is exactly the case the role reports as an unchanged result.
	writeJSON(w, apigen.ClaimResponse{
		Issue:          *result.Issue,
		AlreadyClaimed: !result.Changed,
	})
}

// requireJSONContent enforces the request media type. It reports whether the
// request may proceed.
//
// The refusal is 400 invalid_argument naming the header rather than 415:
// 415 is not in the v0 code vocabulary, and adding a status to that vocabulary
// for this would be permanent wire surface. `param` is documented as carrying a
// header name for exactly this kind of refusal — it is what the Host middleware
// already does.
//
// The document states this rule once, at the document level, beside the
// Host-header and unknown-query-parameter rules, because it holds for every
// body-carrying operation rather than for this one.
func (s *Server) requireJSONContent(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("Content-Type")
	if media, _, err := mime.ParseMediaType(got); err == nil && media == claimContentType {
		return true
	}
	// Attacker-controlled, and quoted by logValue: a cross-origin POST that
	// tried to skip the preflight should leave a trace naming what it sent.
	requestInfo(r.Context()).refuse(got)
	s.fail(w, r, InvalidArgument("Content-Type", ReasonInvalidValue,
		"this operation accepts "+claimContentType+" only"))
	return false
}

// claimActor decodes the request body and validates its one member.
//
// Validation happens HERE, at the wire edge, before any database work is done.
// The domain layer refuses only actor == "" (internal/storage/domain/issue.go),
// so without this a whitespace-only or megabyte actor would be persisted to the
// assignee column AND interpolated into the storage commit message — where an
// unvalidated newline forges audit-trail lines that look like separate commits.
func (s *Server) claimActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}

	var unknown []string
	for name := range members {
		if name != claimActorMember {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// Same rule as an unknown query parameter, for the same reason, and
		// named the same way: one offender, chosen deterministically so a
		// client dispatching on `param` never sees it depend on map order.
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries `"+claimActorMember+"` and nothing else"))
		return "", false
	}

	raw, ok := members[claimActorMember]
	if !ok {
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue,
			"`"+claimActorMember+"` is required"))
		return "", false
	}
	// Through a POINTER, so that `null` reaches the type-mismatch branch:
	// unmarshaling JSON null into a string is a no-op, which would have let a
	// null slide down to the actor rules and be reported as "empty after
	// trimming" — the right status, code, param and reason attached to prose
	// that misdescribes what the client sent.
	var actor *string
	if err := json.Unmarshal(raw, &actor); err != nil || actor == nil {
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue,
			"`"+claimActorMember+"` must be a string"))
		return "", false
	}
	trimmed, res := validateActor(*actor)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	return trimmed, true
}

// decodeJSONObjectBody reads the body as a JSON object of raw members. Decoding
// the members rather than the generated struct is what makes the schema's
// additionalProperties: false enforceable by NAME: encoding/json's
// DisallowUnknownFields reports the offender only inside an error string, and
// these endpoints exist to let clients stop parsing prose.
//
// Shared with the sweep, which has the same posture and the same reason for
// it; the member vocabulary each operation accepts is its own.
func decodeJSONObjectBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, *Result) {
	// A body with no nameable part: `param` is documented absent on exactly
	// this case and present on every other 400.
	unparseable := func(detail string) *Result {
		res := InvalidArgument("", ReasonInvalidValue, detail)
		return &res
	}

	var members map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	if err := dec.Decode(&members); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, unparseable(fmt.Sprintf("request body is larger than %d bytes", maxJSONBodyBytes))
		}
		return nil, unparseable("request body must be a JSON object")
	}
	if members == nil {
		// Valid JSON, but `null`: no members to read an actor out of.
		return nil, unparseable("request body must be a JSON object")
	}
	if dec.More() {
		return nil, unparseable("request body must be a single JSON object")
	}
	return members, nil
}

// validateActor applies the document's actor rules in the document's order:
// trim, then refuse an empty result, an over-long value, and any control
// character.
func validateActor(actor string) (string, *Result) {
	return validateNameMember(claimActorMember, actor)
}

// validateNameMember applies the actor rules to any member that NAMES SOMEONE
// and lands in a 255-character column: `actor` on the mutations, `author` on a
// comment. The rules are one statement parameterized by the member's spelling,
// rather than one statement per member — two copies would be two chances for a
// bound to drift, and the difference between them is a name.
//
// It reports the TRIMMED value, which is what reaches the role.
func validateNameMember(member, value string) (string, *Result) {
	refuse := func(detail string) *Result {
		res := InvalidArgument(member, ReasonInvalidValue, detail)
		return &res
	}
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed == "":
		return "", refuse("`" + member + "` is empty after trimming")

	case len(trimmed) > maxActorBytes:
		return "", refuse(fmt.Sprintf("`%s` is %d bytes; the limit is %d",
			member, len(trimmed), maxActorBytes))

	// The storage column holds types.MaxFieldLen (255) CHARACTERS, one fewer
	// than the document's 256-byte cap allows. Refusing that one-character
	// window here rather than letting it through keeps a documented-looking
	// value from becoming a 500 from the assignee column, and keeps the check
	// keyed on storage's own constant instead of a second copy of the number.
	case types.CheckFieldLen(member, trimmed) != nil:
		return "", refuse(fmt.Sprintf("`%s` is %d characters; storage holds at most %d",
			member, utf8.RuneCountInString(trimmed), types.MaxFieldLen))

	// Newline above all: the actor is interpolated into the storage commit
	// message, so a multiline value would forge audit-trail lines. A comment's
	// author is not, and is refused anyway — it lands in a column every renderer
	// of the thread prints, where an unfiltered C1 introducer is an
	// escape-sequence payload.
	case strings.ContainsFunc(trimmed, isControlChar):
		return "", refuse("`" + member + "` must not contain control characters")
	}
	return trimmed, nil
}

// isControlChar reports whether a rune is one no actor may carry: every Unicode
// control character (category Cc — C0, DEL, and the C1 block) plus the
// U+2028/U+2029 line separators.
//
// C1 is refused for the reason C0 is, not for tidiness: U+0085 is NEL, a line
// break on a VT-conformant terminal, so "alice<U+0085>bd: claim bd-9 by
// mallory" forges exactly the audit-trail line the C0 check exists to prevent
// once the actor reaches the storage commit message. U+009B is the one-byte CSI
// introducer, which makes an unfiltered actor an escape-sequence payload in
// anything that prints an assignee.
//
// The schema's `actor` pattern spells this same set, so what the document
// advertises and what the server refuses are one statement.
func isControlChar(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// failClaim answers a failed claim, adding the extension members a typed 409
// carries.
//
// The extensions come from issueops.ClaimConflictError, which the role fills
// from a query inside the transaction that lost the CAS — never from parsing
// fragments out of the sentinel's message ("already assigned to", "claimed
// by"). That substring classification is what a client adopting this endpoint
// gets to delete, and it can only delete it if the server never does it either.
func (s *Server) failClaim(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *issueops.ClaimConflictError
	if !errors.As(err, &conflict) {
		s.failErr(w, r, err)
		return
	}
	res := ClassifyError(err)
	// `assignee` is documented with already_claimed only: an issue refused for
	// its STATUS may well carry a stale assignee, and publishing it there would
	// tell a client someone holds work they do not.
	if res.Problem.Code == string(CodeAlreadyClaimed) && conflict.Assignee != "" {
		res = res.WithAssignee(conflict.Assignee)
	}
	if conflict.Status != "" {
		res = res.WithIssueStatus(string(conflict.Status))
	}
	s.fail(w, r, res)
}

// timedProvider records how long a request spent obtaining units of work, so
// the claim's request line carries the same uow_ms every other route does.
// RunTxResult owns the unit-of-work lifecycle — that is the point of having one
// retry/commit implementation — so the measurement wraps the provider rather
// than the acquisition. Retry attempts ACCUMULATE: two attempts really did
// spend two acquisitions, and a claim that retried is exactly the case where
// the number matters.
type timedProvider struct {
	inner uow.UnitOfWorkProvider
	rec   *reqInfo
}

// timedProvider carries the capability accessors, so a handler asks the
// provider it holds for the role — the same two-step a CLI command performs on
// a store — instead of reaching past it to a constructor.
var (
	_ uow.IssueReaderSource         = timedProvider{}
	_ uow.IssueClaimerSource        = timedProvider{}
	_ uow.BatchCloserSource         = timedProvider{}
	_ uow.ReadyClaimerSource        = timedProvider{}
	_ uow.ReleaserSource            = timedProvider{}
	_ uow.IssueLifecycleSource      = timedProvider{}
	_ uow.WorkspaceConfigSource     = timedProvider{}
	_ uow.StatsReporterSource       = timedProvider{}
	_ uow.CycleDetectorSource       = timedProvider{}
	_ uow.EdgeReaderSource          = timedProvider{}
	_ uow.GraphCounterSource        = timedProvider{}
	_ uow.RelationsSource           = timedProvider{}
	_ uow.CommenterSource           = timedProvider{}
	_ uow.BlockingAnnotatorSource   = timedProvider{}
	_ uow.TreeWalkerSource          = timedProvider{}
	_ uow.ReadyCounterSource        = timedProvider{}
	_ uow.CounterSource             = timedProvider{}
	_ uow.QuerierSource             = timedProvider{}
	_ uow.SweeperSource             = timedProvider{}
	_ uow.DeleterSource             = timedProvider{}
	_ uow.BatchCreatorSource        = timedProvider{}
	_ uow.DependencyEditorSource    = timedProvider{}
	_ uow.MetadataCASSource         = timedProvider{}
	_ uow.BatchApplierSource        = timedProvider{}
	_ uow.MemoriesSource            = timedProvider{}
	_ uow.EventsJournalCursorSource = timedProvider{}
)

// IssueReader builds the reader OVER THIS WRAPPER rather than delegating to the
// wrapped provider's own accessor. Every accessor on timedProvider does this,
// and this type is the ONE place in the codebase where a constructor is the
// right call rather than an accessor: the whole purpose of the wrapper is
// that every unit of work the reader opens goes through NewUOW below and lands
// in this request's uow_ms. `p.inner.IssueReader()` would return a reader
// bound to the untimed provider and the measurement would silently read zero.
//
// THAT IS NOT A HYPOTHETICAL, which is why it now has a test. This decorator's
// layer is on NewUOW, so only a reader holding this wrapper can reach it —
// unlike telemetry's storage accessor, whose layer is on the RESULT and which
// therefore recurses and wraps. A reviewer reading the two side by side
// proposed making this one recurse for symmetry; the whole internal/httpapi
// suite passes with that change and every read route logs uow_ms=0.000
// forever. TestAReadRouteTimesTheUnitsOfWorkItsReaderOpens is the assertion
// that fails instead.
//
// That pin is per-route and there are thirteen accessors here, so
// TestEveryTimedProviderAccessorBindsToTheWrapper covers the rest structurally.
//
// The cost is that a provider whose own accessor decorated its reader would be
// bypassed here. There is one provider (doltSQLProvider) and its accessor is
// this same construction, so nothing is bypassed today — but if a decorating
// provider ever appears, this is the line that has to grow a wrap.
func (p timedProvider) IssueReader() (issueops.Reader, error) {
	return uow.NewIssueReader(p)
}

// IssueClaimer builds the claimer OVER THIS WRAPPER, for the same reason and
// with the same hazard as IssueReader above: the role's units of work must go
// through NewUOW below or every claim reports uow_ms=0.000.
// TestAClaimTimesTheUnitsOfWorkItsClaimerOpens is the assertion that fails
// instead of the recursion looking correct.
func (p timedProvider) IssueClaimer() (issueops.Claimer, error) {
	return uow.NewIssueClaimer(p)
}

// BatchCloser builds the many-issue close role OVER THIS WRAPPER, for the same
// reason as the roles above. Like BatchApplier it opens one of the longest
// write units of work on this surface — up to a hundred closes plus their
// blocked-state maintenance in one transaction — so a recursion here would
// report uow_ms=0.000 for exactly the requests whose timing matters most.
func (p timedProvider) BatchCloser() (issueops.BatchCloser, error) {
	return uow.NewBatchCloser(p)
}

// ReadyClaimer builds the take-ready-work role OVER THIS WRAPPER, for the same
// reason and with the same hazard as IssueClaimer: it opens a write unit of
// work per call, and its scan is the longest read on this surface — it walks
// the whole ready order past rows other agents took — so a recursion here would
// report uow_ms=0.000 for exactly the requests whose timing matters most.
func (p timedProvider) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return uow.NewReadyClaimer(p)
}

// Releaser builds the claim-release role OVER THIS WRAPPER, for the same reason
// and with the same hazard as IssueClaimer: a release opens a write transaction
// per call, so a recursion here would report uow_ms=0.000 for every one of
// them.
func (p timedProvider) Releaser() (issueops.Releaser, error) {
	return uow.NewReleaser(p)
}

// IssueLifecycle builds the guarded-mutation role OVER THIS WRAPPER, for the
// same reason and with the same hazard as IssueClaimer: this role opens the
// longest write transactions on the surface, so a claimer-style recursion here
// would report uow_ms=0.000 for exactly the requests whose timing matters most.
func (p timedProvider) IssueLifecycle() (issueops.Lifecycle, error) {
	return uow.NewIssueOperations(p)
}

// WorkspaceConfig builds the settings role OVER THIS WRAPPER, for the same
// reason and with the same hazard as the two above.
func (p timedProvider) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return uow.NewWorkspaceConfig(p)
}

// StatsReporter builds the summary role OVER THIS WRAPPER, for the same reason
// and with the same hazard as IssueReader.
func (p timedProvider) StatsReporter() (issueops.StatsReporter, error) {
	return uow.NewStatsReporter(p)
}

// CycleDetector builds the detector OVER THIS WRAPPER, for the same reason and
// with the same hazard as IssueReader.
func (p timedProvider) CycleDetector() (issueops.CycleDetector, error) {
	return uow.NewCycleDetector(p)
}

// EdgeReader builds the stored-edge reader OVER THIS WRAPPER, for the same
// reason and with the same hazard as IssueReader.
func (p timedProvider) EdgeReader() (issueops.EdgeReader, error) {
	return uow.NewEdgeReader(p)
}

// GraphCounter builds the edge-count role OVER THIS WRAPPER, for the same
// reason and with the same hazard as IssueReader.
func (p timedProvider) GraphCounter() (issueops.GraphCounter, error) {
	return uow.NewGraphCounter(p)
}

// IssueRelations builds the single-anchor neighbor role OVER THIS WRAPPER, for
// the same reason and with the same hazard as IssueReader. It is the one
// accessor here whose name is not the role's: the seam spells it IssueRelations
// on both the store and the provider, and this type implements the seam.
func (p timedProvider) IssueRelations() (issueops.Relations, error) {
	return uow.NewIssueRelations(p)
}

// Commenter builds the add-comment role OVER THIS WRAPPER, for the same reason
// and with the same hazard as IssueReader.
func (p timedProvider) Commenter() (issueops.Commenter, error) {
	return uow.NewCommenter(p)
}

// BlockingAnnotator builds the blocking-decoration role OVER THIS WRAPPER, for
// the same reason and with the same hazard as IssueReader.
func (p timedProvider) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return uow.NewBlockingAnnotator(p)
}

// TreeWalker builds the dependency-tree walker OVER THIS WRAPPER, for the same
// reason and with the same hazard as IssueReader.
func (p timedProvider) TreeWalker() (issueops.TreeWalker, error) {
	return uow.NewTreeWalker(p)
}

// ReadyCounter builds the ready counter OVER THIS WRAPPER, for the same reason
// and with the same hazard as IssueReader.
func (p timedProvider) ReadyCounter() (issueops.ReadyCounter, error) {
	return uow.NewReadyCounter(p)
}

// Counter builds the issue counter OVER THIS WRAPPER, for ReadyCounter's reason.
func (p timedProvider) Counter() (issueops.Counter, error) {
	return uow.NewCounter(p)
}

// Querier builds the boolean-query role OVER THIS WRAPPER, for the same reason
// and with the same hazard as IssueReader.
func (p timedProvider) Querier() (issueops.Querier, error) {
	return uow.NewQuerier(p)
}

// Sweeper builds the sweeper OVER THIS WRAPPER, for the same reason and with
// the same hazard as IssueReader.
func (p timedProvider) Sweeper() (issueops.Sweeper, error) {
	return uow.NewSweeper(p)
}

// Deleter builds the deleter OVER THIS WRAPPER, for the same reason and with
// the same hazard as IssueReader.
func (p timedProvider) Deleter() (issueops.Deleter, error) {
	return uow.NewDeleter(p)
}

// BatchCreator builds the batch creator OVER THIS WRAPPER, for the same reason
// as the roles above. It is the one role here that opens a WRITE unit of work
// per call.
func (p timedProvider) BatchCreator() (issueops.BatchCreator, error) {
	return uow.NewBatchCreator(p)
}

// DependencyEditor builds the graph's write role OVER THIS WRAPPER, for the
// same reason as the roles above. Like BatchCreator and the lifecycle it opens
// a WRITE unit of work per call.
func (p timedProvider) DependencyEditor() (issueops.DependencyEditor, error) {
	return uow.NewDependencyEditor(p)
}

// MetadataCAS builds the conditional metadata write OVER THIS WRAPPER, for the
// same reason and with the same hazard as IssueReader.
func (p timedProvider) MetadataCAS() (issueops.MetadataCAS, error) {
	return uow.NewMetadataCAS(p)
}

// BatchApplier builds the ordered-plan write role OVER THIS WRAPPER, for the
// same reason as the roles above. It opens the LONGEST write unit of work on
// this surface — up to a hundred items in one transaction — so a recursion here
// would report uow_ms=0.000 for exactly the requests whose timing matters most.
func (p timedProvider) BatchApplier() (issueops.BatchApplier, error) {
	return uow.NewBatchApplier(p)
}

// Memories builds the persistent-memory role OVER THIS WRAPPER, for the same
// reason and with the same hazard as IssueReader. It is the one accessor here
// whose role is not an issueops role; the binding rule is the same.
func (p timedProvider) Memories() (memoryops.Memories, error) {
	return uow.NewMemories(p)
}

func (p timedProvider) EventsJournalCursor() (storage.EventsJournalCursor, error) {
	return uow.NewEventsJournalCursor(p)
}

func (p timedProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	start := time.Now()
	uw, err := p.inner.NewUOW(ctx)
	if p.rec != nil {
		p.rec.uowWait += time.Since(start)
	}
	return uw, err
}

// Close satisfies the provider interface and is never called: RunTxResult
// closes units of work, not providers. It deliberately does NOT reach the
// wrapped provider — closing the process-wide pool from inside one request
// would take the server down with it.
func (p timedProvider) Close(context.Context) error { return nil }
