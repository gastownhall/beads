// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/lifecycle.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// httpLifecycle serves issueops.Lifecycle WHOLE (design D8 row 16): Create over
// POST createIssue, Update over PATCH updateIssue, and Close and Reopen over
// their custom methods.
//
// It was PARTIAL until the create landed: POST /v0/beads/issues was deliberately
// left free on the wire when issues:batchCreate chose a custom method, and
// upstream #5483 filled it with the single create this role now dials.
type httpLifecycle struct {
	store *Store
	wire  WriteWire
}

var _ issueops.Lifecycle = (*httpLifecycle)(nil)

// Create dials POST /v0/beads/issues.
//
// The whole request maps but for two members, and both refuse rather than
// dropping: IDPrefix where it would act on an explicit, unforced id, which the
// server publishes no member for on purpose (W-CreateRequest.IDPrefix), and
// every member of the issue outside the wire's twenty (W-CreateRequest.Issue)
// — save a CreatedBy that names the actor, which the server's create role
// defaults to on its own (issueops.PreparePublicCreateRequest). A create that reported success having silently dropped the
// storage class, the molecule type or the creation time is a row the caller
// believes they wrote and did not.
//
// The RESPONSE is the row as stored — the minted id, the defaulted status, the
// persisted timestamps — so nothing here echoes the request back.
func (l *httpLifecycle) Create(ctx context.Context, req issueops.CreateRequest) (result issueops.CreateResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError —
	// however deeply createBody/refuseUnwirableCreateIssue/createEdge nested it
	// inside an "Issue.%s:"/"dependencies[%d]:" prefix — into
	// *InexpressibleError so errors.As(err, &unsupported) reaches
	// *storage.ErrUnsupported, same as inexpressible does for a read role. The
	// original composite message (including that prefix) is preserved in the
	// decorated error's own text.
	defer func() { err = l.store.inexpressible("Lifecycle.Create", err) }()
	if err := requireActor(req.Actor); err != nil {
		return issueops.CreateResult{}, err
	}
	body, err := createBody(req)
	if err != nil {
		return issueops.CreateResult{}, err
	}

	issue, err := l.wire.CreateIssue(ctx, body)
	if err != nil {
		// The wire's already_exists carries `param: "id"` and no id, because the
		// request already said it — the same argument the problem mapper's
		// target makes for the conflicts it rebuilds. Putting the caller's own
		// id back is what lets `bd create --id X` say WHICH id is taken, the way
		// a local backend's refusal does. Nothing else about the refusal is
		// recomposed: the sentinel and the server's detail are its own.
		if req.Issue != nil && req.Issue.ID != "" && errors.Is(err, issueops.ErrAlreadyExists) {
			return issueops.CreateResult{}, fmt.Errorf("%s: %w", req.Issue.ID, err)
		}
		return issueops.CreateResult{}, err
	}
	return issueops.CreateResult{Issue: issue}, nil
}

// createBody projects the role's request onto the wire's flat create body.
//
// FLAT is the shape that makes this operation different from every other write
// here: the issue's own members are top-level members of the request rather than
// a nested document, so this function is the only place the two vocabularies
// meet and the allowlist below is the whole of it.
func createBody(req issueops.CreateRequest) (apigen.CreateIssueRequest, error) {
	if req.Issue == nil {
		return apigen.CreateIssueRequest{}, invalid("a create names no issue")
	}
	if len(req.Issue.Comments) > 0 || len(req.Issue.Dependencies) > 0 {
		// The ROLE's own rule, not a wire divergence: edges are supplied through
		// the request's own Dependencies, where their direction can be stated,
		// and a create has no way to supply comments at all. A local backend
		// refuses this too.
		return apigen.CreateIssueRequest{}, invalid(
			"Issue carries comments or dependencies; supply edges through the request's own Dependencies")
	}
	if req.IDPrefix != "" && req.Issue.ID != "" && !req.ForceIDPrefix {
		// Refused only where the override would ACT. The role reads it for one
		// thing — checking an explicit id it was not told to force — so a create
		// that mints its id, or forces one, asks the server for nothing it cannot
		// do. `bd create` sends the workspace's prefix on every create, and an
		// unconditional refusal would refuse every create in a workspace whose
		// config.yaml names one.
		return apigen.CreateIssueRequest{}, refuse(encode.OpCreateIssue, "W-CreateRequest.IDPrefix")
	}
	if err := refuseUnwirableIssueMembers(req.Issue, req.Actor, encode.OpCreateIssue, "W-CreateRequest.Issue"); err != nil {
		return apigen.CreateIssueRequest{}, err
	}

	issue := req.Issue
	// Priority is sent ALWAYS, for batchCreateIssues' reason: 0 is P0 and a real
	// request, so an absent member — which the server reads as the workspace
	// default — would silently reprioritize every critical issue a plan creates.
	priority := issue.Priority
	body := apigen.CreateIssueRequest{Actor: req.Actor, Title: issue.Title, Priority: &priority}

	setItemString(&body.Id, issue.ID)
	setItemString(&body.Description, issue.Description)
	setItemString(&body.Design, issue.Design)
	setItemString(&body.AcceptanceCriteria, issue.AcceptanceCriteria)
	setItemString(&body.Notes, issue.Notes)
	setItemString(&body.Status, string(issue.Status))
	setItemString(&body.IssueType, string(issue.IssueType))
	setItemString(&body.Assignee, issue.Assignee)
	setItemString(&body.Owner, issue.Owner)
	setItemString(&body.Sender, issue.Sender)
	setItemString(&body.ParentId, req.ParentID)
	if issue.ExternalRef != nil {
		// Sent THROUGH the pointer rather than through setItemString: this is
		// the one text member the role models as nullable, so a set-but-empty
		// ref stores the empty string where an absent one stores NULL, and
		// collapsing the two would drop a state the caller can observe.
		ref := *issue.ExternalRef
		body.ExternalRef = &ref
	}
	if issue.EstimatedMinutes != nil {
		minutes := *issue.EstimatedMinutes
		body.EstimatedMinutes = &minutes
	}
	body.DueAt = copyTime(issue.DueAt)
	body.DeferUntil = copyTime(issue.DeferUntil)
	if len(issue.Labels) > 0 {
		labels := append([]string(nil), issue.Labels...)
		body.Labels = &labels
	}
	if len(issue.Metadata) > 0 {
		// The blob travels as the bytes the caller sent, applyDepAddItem's rule:
		// the role is the single definition of what the metadata plane accepts,
		// and a second parse here would be a second definition.
		//
		// WELL-FORMEDNESS is a different question from acceptance, and it is
		// this layer's: bytes that are not JSON at all cannot go on a JSON wire,
		// and letting them through would surface as a marshal fault at the
		// transport where the role promises ErrValidation.
		if err := requireJSON("Issue.Metadata", issue.Metadata); err != nil {
			return apigen.CreateIssueRequest{}, err
		}
		body.Metadata = append(apigen.MetadataValue(nil), issue.Metadata...)
	}
	// The four booleans are sent only when TRUE. Each selects something — a
	// plane, an inheritance, a bypass — so an explicit false is the default said
	// twice, and `ephemeral` and `no_history` are mutually exclusive on the
	// wire, which a pair of explicit falses would not trip but a pair of
	// explicit trues would.
	setItemBool(&body.Ephemeral, issue.Ephemeral)
	setItemBool(&body.NoHistory, issue.NoHistory)
	setItemBool(&body.InheritLabelsFromParent, req.InheritLabelsFromParent)
	setItemBool(&body.ForceIdPrefix, req.ForceIDPrefix)

	if len(req.Dependencies) > 0 {
		edges := make([]apigen.CreateIssueDependency, 0, len(req.Dependencies))
		for i, dep := range req.Dependencies {
			edge, err := createEdge(i, dep)
			if err != nil {
				return apigen.CreateIssueRequest{}, err
			}
			edges = append(edges, edge)
		}
		body.Dependencies = &edges
	}
	if req.WaitsFor != nil {
		gate := apigen.CreateIssueWaitsFor{SpawnerId: req.WaitsFor.SpawnerID}
		setItemString(&gate.Gate, req.WaitsFor.Gate)
		body.WaitsFor = &gate
	}
	return body, nil
}

// createEdge projects one requested edge onto createIssue's own edge, which
// carries `reverse` and `metadata` where batchCreateIssues' does not — a create
// has an id for a target to point back at, and a batch item has none.
func createEdge(index int, dep issueops.CreateDependency) (apigen.CreateIssueDependency, error) {
	where := func(err error) error { return fmt.Errorf("dependencies[%d]: %w", index, err) }
	switch {
	case dep.TargetID == "":
		return apigen.CreateIssueDependency{}, where(invalid("target_id is required"))
	case dep.Type == "":
		return apigen.CreateIssueDependency{}, where(invalid("type is required"))
	case dep.ThreadID != "":
		return apigen.CreateIssueDependency{}, where(refuse(encode.OpCreateIssue, "W-CreateDependency.ThreadID"))
	}
	edge := apigen.CreateIssueDependency{TargetId: dep.TargetID, Type: string(dep.Type)}
	setItemBool(&edge.Reverse, dep.Reverse)
	if dep.Metadata != "" {
		if err := requireJSON("metadata", []byte(dep.Metadata)); err != nil {
			return apigen.CreateIssueDependency{}, where(err)
		}
		edge.Metadata = apigen.MetadataValue(dep.Metadata)
	}
	return edge, nil
}

// requireJSON refuses a raw blob that is not JSON, before it can become a
// request body.
//
// It is NOT a judgment about the metadata plane's vocabulary — that belongs to
// the role, which is why the blob travels verbatim — only about whether these
// bytes can be sent at all. Without it a malformed blob fails inside
// json.Marshal and reaches the caller as a transport fault, where every role
// contract promises a deterministic validation failure.
func requireJSON(member string, raw []byte) error {
	if !json.Valid(raw) {
		return invalid("%s is not valid JSON", member)
	}
	return nil
}

// createCarriedIssueMembers is the wire's create vocabulary: the twenty members
// of apigen.CreateIssueRequest that describe the issue itself, keyed by the
// types.Issue field each one carries.
//
// It is the ALLOWLIST half of refuse-not-drop for this operation. What it does
// not name is refused, and there is no third arm — see refuseUnwirableIssueMembers.
var createCarriedIssueMembers = map[string]string{
	"ID":                 "id",
	"Title":              "title",
	"Description":        "description",
	"Design":             "design",
	"AcceptanceCriteria": "acceptance_criteria",
	"Notes":              "notes",
	"Status":             "status",
	"Priority":           "priority",
	"IssueType":          "issue_type",
	"Assignee":           "assignee",
	"Owner":              "owner",
	"EstimatedMinutes":   "estimated_minutes",
	"ExternalRef":        "external_ref",
	"DueAt":              "due_at",
	"DeferUntil":         "defer_until",
	"Sender":             "sender",
	"Metadata":           "metadata",
	"Labels":             "labels",
	"Ephemeral":          "ephemeral",
	"NoHistory":          "no_history",
}

// setItemBool writes an optional wire boolean, leaving it absent when the caller
// asked for nothing. The pointer addresses a local copy, never the caller's field.
func setItemBool(dest **bool, value bool) {
	if !value {
		return
	}
	v := value
	*dest = &v
}

// copyTime detaches an optional timestamp from the caller's request, so nothing
// downstream can be surprised by a value the caller still holds a pointer to.
func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

// Update dials PATCH updateIssue.
//
// Everything hard about it is the PATCH shape, and it is hard for one reason:
// on this operation PRESENCE is the signal and an explicit null is the CLEAR, so
// "omitted" and "set to nothing" are different requests. issueops.Field carries
// that distinction; apigen.IssuePatchBody cannot — its members are pointers with
// `omitempty`, which collapses a nil clear into an absent member — so the body
// is built as an ordered document here and the wire marshals it verbatim.
//
// Refuse-not-drop governs every member the wire's issuePatchMembers list
// excludes, and every UpdateRequest member with no wire counterpart. A dropped
// patch member is an edit the caller believes landed; a dropped precondition is
// a conditional write turned unconditional. Both are the failure class the
// divergence ledger exists to make impossible.
//
// A CLAIM rides the same request (upstream #6890): `claim` is a top-level
// member beside the patch, so a claim alone, a claim with a patch and a claim
// with a version guard are each ONE updateIssue call, claimed and patched in
// one transaction by the same role the direct route runs, and answered with the
// post-write `revision` like every other update. The one claim that goes
// anywhere else is a claim ALONE to a server that predates the member — see
// serverPredatesUpdateClaim.
func (l *httpLifecycle) Update(ctx context.Context, req issueops.UpdateRequest) (result issueops.UpdateResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError —
	// however deeply refuseUnwirableUpdateMembers/encodeIssuePatch/
	// refuseExcludedPatchMembers nested it, and transitively covering
	// claimOnlyUpdate's own refusals since every one of its returns flows back
	// through this method's single call site — into *InexpressibleError so
	// errors.As(err, &unsupported) reaches *storage.ErrUnsupported, same as
	// inexpressible does for a read role.
	defer func() { err = l.store.inexpressible("Lifecycle.Update", err) }()
	if err := requireActor(req.Actor); err != nil {
		return issueops.UpdateResult{}, err
	}
	if err := requireID("issue id", req.IssueID); err != nil {
		return issueops.UpdateResult{}, err
	}
	if err := refuseUnwirableUpdateMembers(req); err != nil {
		return issueops.UpdateResult{}, err
	}

	patch, err := encodeIssuePatch(req.Patch)
	if err != nil {
		return issueops.UpdateResult{}, err
	}
	if len(patch) == 0 && !req.Claim {
		// The server answers a 400 for an empty patch — except beside `claim`,
		// where the claim is the write — and so does the local role's own
		// validation. Saying it here keeps a write that writes nothing off the
		// wire entirely.
		return issueops.UpdateResult{}, invalid("update names no field to write")
	}

	res, err := l.wire.UpdateIssue(ctx, req.IssueID, req.Actor, patch, updateGuards(req), updateFlags(req))
	if err != nil {
		if req.Claim && isClaimOnlyUpdate(req) && serverPredatesUpdateClaim(err) {
			return l.claimOnlyUpdate(ctx, req)
		}
		return issueops.UpdateResult{}, err
	}
	// The stitch releaser.go documents: types.Issue.RowVersion is `json:"-"`, so
	// the decoded row carries no token and updateIssue publishes the post-write
	// revision as a sibling member. Riding it back onto Issue.RowVersion is what
	// lets a guarded read-modify-write compose its next ExpectedVersion from the
	// result rather than paying a Get to re-read a token the write already answered.
	issue := res.Issue
	if issue.RowVersion, err = parseRevision("updateIssue", res.Revision); err != nil {
		return issueops.UpdateResult{}, err
	}
	return issueops.UpdateResult{Issue: &issue, Changed: res.Changed}, nil
}

// updateGuards carries the request's three compare-and-set preconditions onto
// the wire.
//
// EVERY ONE IS A POINTER ON BOTH SIDES AND THE COPY IS A NIL CHECK, which is the
// whole implementation and is the point: a zero value is a REQUEST on all three
// — `expected_version` 0 is a token the migration-0054 backfill really wrote,
// and an empty `expected_assignee` is the guard that says "only if nobody holds
// it" — so absent has to stay absent. No sentinel is encoded here, in either
// direction.
//
// The pointers address LOCAL copies, so nothing downstream can be surprised by
// a value the caller still holds.
func updateGuards(req issueops.UpdateRequest) wire.UpdateGuards {
	var guards wire.UpdateGuards
	guards.ExpectedVersion = revisionGuard(req.ExpectedVersion)
	if req.ExpectedStatus != nil {
		status := string(*req.ExpectedStatus)
		guards.ExpectedStatus = &status
	}
	if req.ExpectedAssignee != nil {
		assignee := *req.ExpectedAssignee
		guards.ExpectedAssignee = &assignee
	}
	return guards
}

// updateFlags carries the claim and the three force overrides onto the wire.
// The wire sends each only when true, so a request that sets none of them is
// byte-identical to one built before any of them was carried.
func updateFlags(req issueops.UpdateRequest) wire.UpdateFlags {
	return wire.UpdateFlags{
		Claim:                 req.Claim,
		ForceAssigneeTransfer: req.ForceAssigneeTransfer,
		ForceClosePolicy:      req.ForceClosePolicy,
		ForceNotesOverwrite:   req.ForceNotesOverwrite,
	}
}

// serverPredatesUpdateClaim reports whether err is a server that predates
// `claim` on updateIssue (upstream #6890) refusing the member: the version-skew
// 400 — `unknown_parameter`, naming `claim` itself. Such a server checks body
// members before it reads anything else, so the refusal arrives before any
// database work and nothing the request asked for was written.
//
// It is the SKEW signal, not the capability list, because no capability token
// announces the member — it is additive, so it bumped no wire revision either —
// and the reason is what dispatches, never the code alone: `invalid_value`
// naming `claim` is a current server refusing a claim beside a member it may
// not ride with, which is the caller's error to see, not a server to route
// around.
func serverPredatesUpdateClaim(err error) bool {
	var problem *wire.ProblemError
	return errors.As(err, &problem) &&
		problem.Reason == encode.UnknownParameterReason &&
		problem.Param == "claim"
}

// Close dials POST issues/{id}:close.
func (l *httpLifecycle) Close(ctx context.Context, req issueops.CloseRequest) (issueops.CloseResult, error) {
	if err := requireActor(req.Actor); err != nil {
		return issueops.CloseResult{}, err
	}
	if err := requireID("issue id", req.IssueID); err != nil {
		return issueops.CloseResult{}, err
	}

	body := closeBody(req.Actor, req.Reason, req.Session, req.Force)
	body.ExpectedVersion = revisionGuard(req.ExpectedVersion)
	// AutoCloseMolecule rides the member only where the server advertises it;
	// elsewhere the close goes out without it and the library's role-composed
	// auto-close runs after it (molecule.go). Explicitly degraded, never
	// dropped.
	sendAutoClose, degradeAutoClose, err := l.store.autoCloseMoleculeMember(ctx, req.AutoCloseMolecule)
	if err != nil {
		return issueops.CloseResult{}, err
	}
	if sendAutoClose {
		body.AutoCloseMolecule = &sendAutoClose
	}
	res, err := l.wire.CloseIssue(ctx, req.IssueID, body)
	if err != nil {
		return issueops.CloseResult{}, err
	}
	// The stitch releaser.go documents; an idempotent re-close carries the current
	// token too, so a close-then-reopen chain composes its next ExpectedVersion here.
	issue := res.Issue
	if issue.RowVersion, err = parseRevision("closeIssue", res.Revision); err != nil {
		return issueops.CloseResult{}, err
	}
	result := issueops.CloseResult{
		Issue:              &issue,
		Changed:            !res.AlreadyClosed,
		OpenChildren:       res.OpenChildren,
		AutoClosedMolecule: res.AutoClosedMolecule,
	}
	if res.MoleculeAutoCloseRefusal != nil {
		result.MoleculeAutoCloseRefusal = *res.MoleculeAutoCloseRefusal
	}
	if degradeAutoClose {
		result.AutoClosedMolecule, result.MoleculeAutoCloseRefusal = l.store.autoCloseMoleculeOverRoles(ctx, req.IssueID, req.Actor, req.Session)
	}
	return result, nil
}

// Reopen dials POST issues/{id}:reopen.
func (l *httpLifecycle) Reopen(ctx context.Context, req issueops.ReopenRequest) (result issueops.ReopenResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError into
	// *InexpressibleError so errors.As(err, &unsupported) reaches
	// *storage.ErrUnsupported, same as inexpressible does for a read role.
	defer func() { err = l.store.inexpressible("Lifecycle.Reopen", err) }()
	if err := requireActor(req.Actor); err != nil {
		return issueops.ReopenResult{}, err
	}
	if err := requireID("issue id", req.IssueID); err != nil {
		return issueops.ReopenResult{}, err
	}
	if req.Provenance != "" {
		return issueops.ReopenResult{}, refuse(encode.OpReopenIssue, "W-ReopenRequest.Provenance")
	}

	body := apigen.ReopenIssueRequest{Actor: req.Actor, ExpectedVersion: revisionGuard(req.ExpectedVersion)}
	if req.Reason != "" {
		body.Reason = &req.Reason
	}
	res, err := l.wire.ReopenIssue(ctx, req.IssueID, body)
	if err != nil {
		return issueops.ReopenResult{}, err
	}
	// The stitch releaser.go documents; a recovery flow that reopens and then
	// re-closes composes its next ExpectedVersion from this token.
	issue := res.Issue
	if issue.RowVersion, err = parseRevision("reopenIssue", res.Revision); err != nil {
		return issueops.ReopenResult{}, err
	}
	return issueops.ReopenResult{Issue: &issue, Changed: !res.AlreadyOpen}, nil
}

// revisionGuard spells a row-version precondition for the wire.
//
// The Go contract holds the token as an int64 (types.Issue.RowVersion, the
// request's ExpectedVersion) and the wire carries it as a decimal STRING
// (upstream #6053: the token spans the full 64-bit range and a JSON number
// would be rounded past 2^53 by any IEEE-754-double consumer, so the server
// refuses a bare number on `expected_version` with a 400). types.RevisionToken
// is the one spelling of that encoding and this is the one place the client
// applies it on the way out.
//
// NIL STAYS NIL and 0 becomes "0": the token is opaque and compared for
// equality alone, and 0 is one a row really holds — the migration-0054 backfill
// wrote it — so an absent guard and a guard on zero are different requests.
// Every verb that publishes the member goes through here, so there is one place
// the rule can be read and one place it could be broken.
func revisionGuard(version *int64) *string {
	if version == nil {
		return nil
	}
	token := types.RevisionToken(*version)
	return &token
}

// parseRevision reads the `revision` a write answered with back to the int64
// the Go contract carries, revisionGuard's inverse.
//
// The member is required on every response that publishes it, and the server
// spells it with types.RevisionToken, so ParseRevisionToken accepts exactly what
// a conforming server emits. A token it cannot read is a server this client
// cannot follow, reported the way the package reports every other malformed
// answer (a batch-apply result of the wrong arity, a count with no `groups`):
// as a plain error naming bd serve, the operation and the member, rather than
// stitching 0 onto the row — 0 is a real token and the corruption would only
// surface as a precondition_failed on the NEXT request.
func parseRevision(op, token string) (int64, error) {
	version, err := types.ParseRevisionToken(token)
	if err != nil {
		return 0, fmt.Errorf("%s: bd serve answered a `revision` this client cannot read as a token (%q): %w", op, token, err)
	}
	return version, nil
}

// closeBody builds the close request. The three optional members are pointers on
// the wire, and an omitted one is not the same as an empty one: the server
// distinguishes "no reason supplied" from an explicit null, which it refuses.
func closeBody(actor, reason, session string, force bool) apigen.CloseIssueRequest {
	body := apigen.CloseIssueRequest{Actor: actor}
	if reason != "" {
		body.Reason = &reason
	}
	if session != "" {
		body.Session = &session
	}
	if force {
		body.Force = &force
	}
	return body
}

// claimOnlyUpdate is the claim a server that predates `claim` on updateIssue
// can still serve: a claim and nothing else, dialed as claimIssue through the
// Claimer role. Update reaches it only after such a server refused the member
// as unknown (serverPredatesUpdateClaim) and only for a claim-only request
// (isClaimOnlyUpdate) — claimIssue's own request is `{actor}` alone (see
// apigen.ClaimRequest), so a claim-only UpdateRequest IS that request, and
// anything beside the claim would be dropped by it. A claim combined with a
// patch, a guard or a force override is therefore NOT retried here: the skew
// refusal is returned as it came, rather than synthesized as two calls
// (claimIssue then updateIssue), which would let a caller observe an issue
// claimed but not yet patched.
//
// That keeps `bd update <id> --claim --json` — gc's exclusive claim path —
// working against an older bd serve. The two roles' result shapes line up
// member for member (Issue, Changed): same-actor re-claim is the idempotent
// Changed=false Claimer already promises, and a foreign holder or an
// ineligible status is the same *issueops.ClaimConflictError updateIssue's
// claim raises.
//
// What this route cannot match, it says. claimIssue answers the bare row
// (issueops/claimer.go's ClaimResult doc, pinned against the Claimer role
// directly by TestServedClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow),
// where updateIssue answers the hydrated one, so the LABELS and CreatedBy are
// read back below for CommandUpdateMutation's shared `bd update --json`
// renderer — the hydration is composed HERE rather than by widening Claimer's
// promise, which backend/conformance's ClaimerFixture suite holds every backend
// to. claimIssue also carries no `revision`, so the result's RowVersion stays
// unset rather than borrowing the follow-up read's token: that read is a second
// snapshot, and a token from it could postdate a write this claim never saw,
// arming a guard that should have missed. And claimIssue excludes the wisp
// plane, so a wisp id refuses by name (W-ClaimRequest.Wisp).
func (l *httpLifecycle) claimOnlyUpdate(ctx context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	claimer, err := l.store.IssueClaimer()
	if err != nil {
		return issueops.UpdateResult{}, err
	}
	res, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: req.Actor, IssueID: req.IssueID})
	if err != nil {
		// claimIssue excludes wisps (issueops.Claimer's own contract), where the
		// direct route's `bd update <id> --claim` claims one through Lifecycle's
		// general issue-or-wisp routing. A wisp id therefore reaches THIS branch
		// as the wire's generic not-found — "no issue or wisp with that id" —
		// indistinguishable from an id that names nothing at all. The probe
		// below runs ONLY here, on a not-found answer, rather than ahead of
		// every claim attempt, so an ordinary claim of a real, non-wisp issue
		// costs no probe. A refusal for any other reason (already claimed, not
		// eligible from its current status, actor/id validation) needs no probe
		// and returns unchanged.
		if errors.Is(err, issueops.ErrNotFound) {
			if issue, probeErr := l.store.GetIssue(ctx, req.IssueID); probeErr == nil && issue != nil && isWispIssue(issue) {
				return issueops.UpdateResult{}, refuse(encode.OpUpdateIssue, "W-ClaimRequest.Wisp")
			}
			// Not a wisp — either the probe also found nothing (the id truly
			// names no row), or the probe itself failed. Either way, the
			// original not-found error from claimIssue is the right thing to
			// return, unmodified: it is what lets a not-found-sensitive caller
			// (gc's Claim among them) recognize a missing id over http exactly
			// as it recognizes one against a local workspace.
		}
		return issueops.UpdateResult{}, err
	}
	issue := res.Issue
	// Best-effort hydration for output parity with the direct route (see the
	// doc above): the claim itself already committed, so a failed follow-up
	// read is not this call's failure to report — fall back to the bare row
	// issueops.Claimer promised rather than failing an otherwise-successful
	// claim. Labels and CreatedBy ONLY: the read's RowVersion is another
	// snapshot's, and the doc above is why it must not ride onto this row. A
	// guard composed from the unset token fails closed (ErrVersionMismatch: the
	// caller re-reads), where a borrowed one could pass over a write this claim
	// never saw. Against a current server the claim rides updateIssue instead,
	// whose `revision` Update stitches as it does for any patch, so the real
	// token is missing only on this pre-#6890 fallback.
	if issue != nil {
		if hydrated, hydrateErr := l.store.GetIssue(ctx, req.IssueID); hydrateErr == nil && hydrated != nil {
			withLabels := *issue
			withLabels.Labels = hydrated.Labels
			withLabels.CreatedBy = hydrated.CreatedBy
			issue = &withLabels
		}
	}
	return issueops.UpdateResult{Issue: issue, Changed: res.Changed}, nil
}

// isClaimOnlyUpdate reports whether req carries nothing but the claim itself
// (and the Actor/IssueID pair every UpdateRequest needs) — whether claimIssue,
// whose wire request is the actor alone, can carry ALL of it. Any other
// UpdateRequest member holding a non-zero value — a Patch field, a guard, a
// force override, a provenance label — is one claimIssue has no place for, so
// claimOnlyUpdate's fallback would drop it.
//
// reflect.DeepEqual against the zero IssuePatch, rather than a hand-enumerated
// field list, is deliberate: a future IssuePatch member defaults to unset, so
// it is caught by this check without this function needing to learn its name.
func isClaimOnlyUpdate(req issueops.UpdateRequest) bool {
	return reflect.DeepEqual(req.Patch, issueops.IssuePatch{}) &&
		!req.ForceAssigneeTransfer &&
		!req.ForceClosePolicy &&
		!req.ForceNotesOverwrite &&
		!req.IssuePlaneOnly &&
		req.Provenance == "" &&
		req.ExpectedVersion == nil &&
		req.ExpectedAssignee == nil &&
		req.ExpectedStatus == nil
}

// isWispIssue reports whether issue lives on the wisp plane, mirroring
// internal/storage/issueops.IsWisp's flags-not-id-pattern rule (unexported
// there, and this package does not import it): a row is a wisp because it is
// Ephemeral or NoHistory, never because of what its id looks like. It does
// not read WispPlaneOverride, which only matters mid-import — a row this
// client just fetched off a live server is never an in-flight import record.
func isWispIssue(issue *types.Issue) bool {
	return issue.Ephemeral || issue.NoHistory
}

// refuseUnwirableUpdateMembers walks the UpdateRequest members the wire's
// updateIssue body has no place for.
//
// The claim and the three force overrides are NOT on this switch: updateIssue
// publishes all four (internal/httpapi/apigen's UpdateIssueRequest), the
// server reads them (internal/httpapi/update.go), and Update sends them beside
// the patch as wire.UpdateFlags. Their W-UpdateRequest rows are RETIRED.
//
// The order is the ledger's, and each refusal cites its row rather than a
// sentence, so the taxonomy renders the same reason the design recorded.
func refuseUnwirableUpdateMembers(req issueops.UpdateRequest) error {
	switch {
	case req.IssuePlaneOnly:
		return refuse(encode.OpUpdateIssue, "W-UpdateRequest.IssuePlaneOnly")
	case req.Provenance != "":
		return refuse(encode.OpUpdateIssue, "W-UpdateRequest.Provenance")
	}
	return nil
}

// encodeIssuePatch turns the role's patch into the wire's patch document.
//
// A member that is Set becomes a member of the document; a member that is not
// stays out of it. The four NULLABLE members are the reason the document is a
// map of any rather than a struct: their Field values wrap a POINTER, and a set
// nil pointer has to reach the server as a literal null — the clear — where an
// unset one has to be absent entirely.
//
// Every member the wire excludes refuses here rather than being skipped. Reading
// this function as the wire's allowlist is the point: what it does not write, it
// refuses, and there is no third arm.
func encodeIssuePatch(patch issueops.IssuePatch) (map[string]any, error) {
	// REFUSE FIRST, then encode. The excluded members are decided before any
	// member is written, so a patch that both names one and carries a malformed
	// metadata blob reports the refusal — the fact about the WIRE — rather than
	// whichever error the encoder happened to reach first.
	if err := refuseExcludedPatchMembers(patch); err != nil {
		return nil, err
	}
	out := map[string]any{}

	setString(out, "title", patch.Title)
	setString(out, "description", patch.Description)
	setString(out, "design", patch.Design)
	setString(out, "acceptance_criteria", patch.AcceptanceCriteria)
	setString(out, "notes", patch.Notes)
	setString(out, "append_notes", patch.AppendNotes)
	if patch.Priority.Set {
		out["priority"] = patch.Priority.Value
	}
	if patch.IssueType.Set {
		out["issue_type"] = string(patch.IssueType.Value)
	}

	// The four members upstream #5484 added.
	//
	// PRESENCE ALONE DECIDES all three of the strings, and on two of them the
	// EMPTY STRING is a request rather than an absence: an empty `assignee`
	// unassigns, and an empty `parent_id` removes every parent-child edge. A
	// builder that skipped a member because its value was empty would turn both
	// into no edit at all — silently, since the server answers `changed: false`
	// and the caller reads a 200.
	//
	// `status` is not a second spelling of close and reopen. Those two carry
	// semantics a status write has nowhere to put — the reason and session under
	// first-close-wins, the done-status normalization, the already-closed
	// idempotence flag — and stay the operations to reach for. This member is
	// the status moved ALONGSIDE other fields in one transaction, which is the
	// thing two calls cannot do. It answers to close policy either way: a
	// crossing into the done category earns the server's own not_closable.
	if patch.Status.Set {
		out["status"] = string(patch.Status.Value)
	}
	setString(out, "assignee", patch.Assignee)
	setString(out, "parent_id", patch.ParentID)
	if metadataPatched(patch.Metadata) {
		document, err := encodeMetadataPatch(patch.Metadata)
		if err != nil {
			return nil, err
		}
		out["metadata"] = document
	}

	// The nullable four. A set member always lands, and lands as null when its
	// pointer is nil, because that is the clear.
	setNullable(out, "estimated_minutes", patch.EstimatedMinutes)
	setNullable(out, "external_ref", patch.ExternalRef)
	setNullableTime(out, "due_at", patch.DueAt)
	setNullableTime(out, "defer_until", patch.DeferUntil)

	// THE ORDERED LABEL EDIT, whole. The wire spells it as three FLAT members
	// where applyBatch nests them under one object, and the difference is
	// historical rather than meaningful: `labels` shipped as a bare array, and
	// nesting it now would re-type a published member. What both spell is the
	// role's own algebra — replace, then add, then remove, so REMOVAL WINS where
	// a label appears in more than one — and the server assembles all three into
	// ONE issueops.LabelPatch, so the order is never this client's to arrange.
	//
	// AN INCREMENTAL EDIT USED TO REFUSE HERE (W-IssuePatch.Labels), and it is
	// worth saying what the refusal was protecting: a client that degraded an add
	// to the replace-only member would have to read the set, add to it and write
	// it back, which silently drops any label another writer added in between —
	// and `bd label add` and every agent that tags work concurrently are exactly
	// that caller. The members retire the refusal rather than the argument.
	if patch.Labels.Replace.Set {
		labels := patch.Labels.Replace.Value
		if labels == nil {
			// An empty array clears the set; a JSON null on this member is not
			// a clear and the server refuses it.
			labels = []string{}
		}
		out["labels"] = labels
	}
	// EMITTED ONLY WHEN NON-EMPTY, which is not a drop: Add and Remove are bare
	// slices on the role, so nil and empty are one value there — an Add carrying
	// nothing changes nothing and reports Changed false — and sending an empty
	// array would put a member on the wire that says nothing while turning a
	// patch that edits nothing into one the server has to refuse for a different
	// reason. It is encodeApplyLabelPatch's rule, on the flat spelling.
	if len(patch.Labels.Add) > 0 {
		out["add_labels"] = append([]string(nil), patch.Labels.Add...)
	}
	if len(patch.Labels.Remove) > 0 {
		out["remove_labels"] = append([]string(nil), patch.Labels.Remove...)
	}
	return out, nil
}

// encodeMetadataPatch turns the role's metadata edit into the wire's own
// metadata document.
//
// It is a PROJECTION rather than a translation: apigen.ApplyMetadataPatch
// publishes replace, merge, set and unset, the role's MetadataPatch carries
// exactly those four, and the algebra over them — merge, then set in key order,
// then unset, with replace replacing the whole document and refusing beside the
// other three — is the ROLE's and is applied server-side by the same body a
// local workspace runs. Nothing here re-decides any of it, which is why the
// replace-plus-incremental contradiction is sent rather than pre-empted: both
// sides refuse it as a validation failure and writing nothing.
//
// It is a map for its parent's reason. apigen.ApplyMetadataPatch spells every
// member `omitempty`, and on THIS document that collapses the one state the
// role can express and the struct cannot: a set Replace holding no bytes is the
// CLEAR, and an omitted `replace` is no replacement at all.
//
// The two things it does decide, both because the alternative is not
// expressible on a JSON wire:
//
//   - a clear travels as `{}`. An empty json.RawMessage is not a JSON value —
//     json.Marshal fails on one — and `{}` is the document the role's own
//     ApplyMetadataPatch substitutes for it before it writes, so this is the
//     role's answer rather than a choice made here.
//   - a blob that is not JSON refuses as ErrValidation, the way the create's
//     does, rather than failing inside the marshaler as a transport fault.
func encodeMetadataPatch(patch issueops.MetadataPatch) (map[string]any, error) {
	out := map[string]any{}
	if patch.Replace.Set {
		replacement := append(json.RawMessage(nil), patch.Replace.Value...)
		if len(replacement) == 0 {
			replacement = json.RawMessage(`{}`)
		}
		if err := requireJSON("Metadata.Replace", replacement); err != nil {
			return nil, err
		}
		out["replace"] = replacement
	}
	if patch.Merge.Set {
		merge := append(json.RawMessage(nil), patch.Merge.Value...)
		if err := requireJSON("Metadata.Merge", merge); err != nil {
			return nil, err
		}
		out["merge"] = merge
	}
	if len(patch.Set) > 0 {
		values := make(map[string]json.RawMessage, len(patch.Set))
		// Sorted, so a patch breaking two keys always names the same offender.
		// The role applies them in key order for its own reasons; this is only
		// about which refusal a caller fixing one key at a time sees first.
		for _, key := range slices.Sorted(maps.Keys(patch.Set)) {
			value := append(json.RawMessage(nil), patch.Set[key]...)
			if err := requireJSON(fmt.Sprintf("Metadata.Set[%q]", key), value); err != nil {
				return nil, err
			}
			values[key] = value
		}
		out["set"] = values
	}
	if len(patch.Unset) > 0 {
		out["unset"] = append([]string(nil), patch.Unset...)
	}
	return out, nil
}

// refuseExcludedPatchMembers is the other half of the allowlist above: the
// IssuePatch members internal/httpapi/update.go's issuePatchMembers list leaves
// out.
func refuseExcludedPatchMembers(patch issueops.IssuePatch) error {
	switch {
	case patch.Owner.Set:
		return refuse(encode.OpUpdateIssue, "W-IssuePatch.Owner")
	case patch.ClosedBySession.Set:
		return refuse(encode.OpUpdateIssue, "W-IssuePatch.ClosedBySession")
	case patch.SpecID.Set:
		return refuse(encode.OpUpdateIssue, "W-IssuePatch.SpecID")
	case patch.AwaitID.Set:
		return refuse(encode.OpUpdateIssue, "W-IssuePatch.AwaitID")
	case patch.Persistence.Set:
		return refuse(encode.OpUpdateIssue, "W-IssuePatch.Persistence")
	}
	return nil
}

func metadataPatched(m issueops.MetadataPatch) bool {
	return m.Replace.Set || m.Merge.Set || len(m.Set) > 0 || len(m.Unset) > 0
}

func setString(out map[string]any, member string, field issueops.Field[string]) {
	if field.Set {
		out[member] = field.Value
	}
}

// setNullable writes a member whose absence and whose null mean different
// things. The generic parameter is the POINTEE, so a nil Value marshals to null.
func setNullable[T any](out map[string]any, member string, field issueops.Field[*T]) {
	if !field.Set {
		return
	}
	if field.Value == nil {
		out[member] = nil
		return
	}
	out[member] = *field.Value
}

// setNullableTime is setNullable for the two timestamp members. It is separate
// only because a *time.Time dereferenced into an `any` would marshal through
// time.Time's own encoder either way, and spelling it out keeps the RFC 3339
// contract visible at the call site.
func setNullableTime(out map[string]any, member string, field issueops.Field[*time.Time]) {
	if !field.Set {
		return
	}
	if field.Value == nil {
		out[member] = nil
		return
	}
	out[member] = field.Value.UTC().Format(time.RFC3339Nano)
}

// CloseIssue is the OFF-ROLE raw close, mapped onto the same closeIssue
// operation the Lifecycle role uses (design D8's off-role list, extended by the
// `bd close` decision).
//
// Its one caller at tip is the molecule auto-close, which closes a parent whose
// children have all finished and discards nothing about the outcome. Routing it
// onto the role's operation rather than refusing it is what keeps that path
// working over http; it carries no reason-per-item and no force, so the mapping
// is total.
func (s *Store) CloseIssue(ctx context.Context, id, reason, actor, session string) error {
	w, err := s.roleWire("CloseIssue")
	if err != nil {
		return err
	}
	if err := requireActor(actor); err != nil {
		return err
	}
	if err := requireID("issue id", id); err != nil {
		return err
	}
	_, err = w.CloseIssue(ctx, id, closeBody(actor, reason, session, false))
	return err
}
