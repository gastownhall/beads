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
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

const (
	// claimPathValue names the ServeMux wildcard the claim route registers. The
	// route table builds its pattern from this constant and claimSuffix below
	// splits the custom method back off it, so the two halves of the one path
	// exception cannot drift apart.
	claimPathValue = "idop"
	// claimSuffix is the custom method the document spells on the id segment:
	// POST /v0/beads/issues/{id}:claim.
	claimSuffix = ":claim"
	// claimActorMember is the only member ClaimRequest carries. The schema is
	// additionalProperties: false, so anything else is refused by name.
	claimActorMember = "actor"
	// maxActorBytes is the document's cap on `actor`. The schema's maxLength
	// counts characters; this byte limit is the binding one.
	maxActorBytes = 256
	// maxClaimBodyBytes bounds the request body. The only member is an actor of
	// at most a few hundred bytes, so this is pure refusal of the absurd.
	maxClaimBodyBytes = 1 << 20
	// claimContentType is the one media type this operation accepts, and
	// refusing anything else is a CSRF control, not pedantry: a JSON content
	// type is not CORS-"simple", so a cross-origin claim always triggers a
	// preflight this server never approves. Accepting text/plain or a form
	// encoding would let an attacker's page skip the preflight and drive the
	// one write on this surface from any browser on the host.
	claimContentType = "application/json"
)

// handleClaim is the one write in v0: a compare-and-set claim of a single issue
// for a caller-named actor.
//
// ACTOR SEMANTICS, stated because adopting this endpoint depends on them. The
// actor is caller-ASSERTED provenance for the audit trail, not authenticated
// identity: this API has no authentication, so any client can claim as any
// name, exactly as any local process can pass any --actor to the CLI. The CAS
// is therefore a correctness fence against CONCURRENT claims, not an
// authorization boundary — it guarantees that two racing claimants cannot both
// win, and guarantees nothing about who either of them really is. The
// loopback-only bind is what bounds the blast radius of that posture.
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
	id, ok := s.claimTarget(w, r)
	if !ok {
		return
	}
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

// claimTarget splits the custom method off the segment the router matched, and
// reports whether the request may proceed.
//
// ServeMux wildcards match a whole path segment, so `{id}:claim` is not
// expressible as a pattern: the route registers `POST /v0/beads/issues/{idop}`
// and the parse lands here. A segment that does not end in the custom method is
// NOT an id — the only documented POST on this surface is this operation — so
// it gets the same 404 the catch-all gives any other unrouted path. That is
// what keeps POST on the issue-detail path, which the document declares
// GET-only, from being answered as a claim of the issue named there.
//
// The id itself is bounded HERE, for the same reason the actor is: this is the
// last point before a request buys a concurrency slot and two database round
// trips. `issues.id` is VARCHAR(255) and the document calls the parameter an
// exact canonical id, so a longer one — or one carrying a control character,
// which a percent-escape in the path decodes to — names no row that can exist.
// Answering it from the edge costs the server nothing and tells the caller
// exactly what a read would have: 404.
func (s *Server) claimTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := strings.CutSuffix(r.PathValue(claimPathValue), claimSuffix)
	if !ok || id == "" {
		s.fail(w, r, newResult(CodeNotFound, "no such route on this server"))
		return "", false
	}
	if types.CheckFieldLen("id", id) != nil || strings.ContainsFunc(id, isControlChar) {
		// The SAME 404 a real miss gets. A distinct refusal here would let a
		// caller map the server's notion of a well-formed id, and there is
		// nothing to learn from it: no such row exists either way.
		s.fail(w, r, NotFound())
		return "", false
	}
	return id, true
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
// SPEC GAP, deliberate and to be closed at the next revision window: the frozen
// document does not mention Content-Type anywhere, so this refusal is the one
// 400 on this route a client generated from the schema cannot predict. It is
// unreachable for a conformant client — requestBody already declares
// application/json — and the status/code/param/reason are all in the documented
// vocabulary, so the fix is prose describing the CSRF control, not a behavior
// change.
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
	members, res := decodeClaimBody(w, r)
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

// decodeClaimBody reads the body as a JSON object of raw members. Decoding the
// members rather than the generated struct is what makes the schema's
// additionalProperties: false enforceable by NAME: encoding/json's
// DisallowUnknownFields reports the offender only inside an error string, and
// this endpoint exists to let clients stop parsing prose.
func decodeClaimBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, *Result) {
	// A body with no nameable part: `param` is documented absent on exactly
	// this case and present on every other 400.
	unparseable := func(detail string) *Result {
		res := InvalidArgument("", ReasonInvalidValue, detail)
		return &res
	}

	var members map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClaimBodyBytes))
	if err := dec.Decode(&members); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, unparseable(fmt.Sprintf("request body is larger than %d bytes", maxClaimBodyBytes))
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
	refuse := func(detail string) *Result {
		res := InvalidArgument(claimActorMember, ReasonInvalidValue, detail)
		return &res
	}
	trimmed := strings.TrimSpace(actor)
	switch {
	case trimmed == "":
		return "", refuse("`" + claimActorMember + "` is empty after trimming")

	case len(trimmed) > maxActorBytes:
		return "", refuse(fmt.Sprintf("`%s` is %d bytes; the limit is %d",
			claimActorMember, len(trimmed), maxActorBytes))

	// The storage column holds types.MaxFieldLen (255) CHARACTERS, one fewer
	// than the document's 256-byte cap allows. Refusing that one-character
	// window here rather than letting it through keeps a documented-looking
	// value from becoming a 500 from the assignee column, and keeps the check
	// keyed on storage's own constant instead of a second copy of the number.
	case types.CheckFieldLen(claimActorMember, trimmed) != nil:
		return "", refuse(fmt.Sprintf("`%s` is %d characters; storage holds at most %d",
			claimActorMember, utf8.RuneCountInString(trimmed), types.MaxFieldLen))

	// Newline above all: the actor is interpolated into the storage commit
	// message, so a multiline value would forge audit-trail lines.
	case strings.ContainsFunc(trimmed, isControlChar):
		return "", refuse("`" + claimActorMember + "` must not contain control characters")
	}
	return trimmed, nil
}

// isControlChar reports whether a rune is one no actor may carry: every Unicode
// control character (category Cc — C0, DEL, and the C1 block) plus the
// U+2028/U+2029 line separators.
//
// This is deliberately WIDER than the schema's pattern, which excludes only C0
// and DEL. The document's prose is what governs here — it promises refusal of
// "any control character including newline" — and C1 qualifies: U+0085 is NEL,
// a line break on a VT-conformant terminal, so "alice<U+0085>bd: claim bd-9
// by mallory" forges exactly the audit-trail line the C0 check exists to
// prevent once the actor reaches the storage commit message. U+009B is the
// one-byte CSI introducer, which makes an unfiltered actor an escape-sequence
// payload in anything that prints an assignee. Widening refuses more than the
// pattern advertises and can therefore never persist a value the document
// forbids; the pattern is what should move at the next spec window.
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
	_ uow.IssueReaderSource  = timedProvider{}
	_ uow.IssueClaimerSource = timedProvider{}
)

// IssueReader builds the reader OVER THIS WRAPPER rather than delegating to the
// wrapped provider's own accessor, and this and IssueClaimer below are the two
// places where a constructor is the right call: the whole purpose of the wrapper is
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
// The cost is that a provider whose own accessor decorated its reader would be
// bypassed here. There is one provider (doltSQLProvider) and its accessor is
// this same construction, so nothing is bypassed today — but if a decorating
// provider ever appears, this is the line that has to grow a wrap.
func (p timedProvider) IssueReader() (issueops.Reader, error) {
	return uow.NewIssueReader(p)
}

// IssueClaimer builds the claimer OVER THIS WRAPPER, for the same reason and
// with the same hazard as IssueReader above: the role's units of work must go
// through NewUOW below or the one write on this surface reports uow_ms=0.000.
// TestAClaimTimesTheUnitsOfWorkItsClaimerOpens is the assertion that fails
// instead of the recursion looking correct.
func (p timedProvider) IssueClaimer() (issueops.Claimer, error) {
	return uow.NewIssueClaimer(p)
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
