// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/handshake.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// APIVersion is the path major this client speaks. ContextResponse.api_version
// must equal it exactly: the wire is versioned by path, so a server answering
// anything else is a server this client cannot address at all.
const APIVersion = "v0"

// baselineOps are the five operations that dispatch WITHOUT a pre-flight.
//
// They exist in every v0 release — they are the first slice — so consulting
// capabilities before them buys nothing and costs a round trip on the hot
// work-distribution path. Their errors classify by problem code like everything
// else.
//
// claimIssue is first-slice too, but it is deliberately NOT here (ga-b8ddd.11).
// A claim is a WRITE, and the handshake carries the project-identity gate;
// exempting the claim let it land against a server whose identity no longer
// matched the workspace — a silent cross-project write. Forcing its handshake
// runs the gate before the claim, at the price of one round trip every other
// write already pays. issues.claim is first-slice on every v0 server, so the
// forced handshake never turns a working claim into a capability refusal.
var baselineOps = map[string]bool{
	OpHealth:        true,
	OpGetContext:    true,
	OpListReadyWork: true,
	OpListIssues:    true,
	OpGetIssue:      true,
}

// opCapability is the token each operation contributes to
// ContextResponse.capabilities. The two identity operations contribute none:
// liveness and the handshake itself are not gated by the list they publish.
//
// TestCapabilityTableMatchesTheServerRouteTable proves this table set-equal
// with the server's own, so an operation added upstream cannot arrive here
// unclassified.
var opCapability = map[string]string{
	OpHealth:            "",
	OpGetContext:        "",
	OpListReadyWork:     "ready.list",
	OpCountReadyWork:    "ready.count",
	OpGetStats:          "stats.get",
	OpListIssues:        "issues.list",
	OpQueryIssues:       "issues.query",
	OpGetIssue:          "issues.get",
	OpClaimIssue:        "issues.claim",
	OpCloseIssue:        "issues.close",
	OpReopenIssue:       "issues.reopen",
	OpUpdateIssue:       "issues.update",
	OpSweepIssues:       "issues.sweep",
	OpDeleteIssues:      "issues.delete",
	OpBatchCreateIssues: "issues.batchCreate",
	OpBatchCloseIssues:  "issues.batchClose",
	// The three operations the wire wave published while this client was being
	// built. They are CLASSIFIED, not wired: the client has no accessor behind
	// any of them yet, so its refusals stand — but an operation the server
	// advertises and this table does not know is one the pre-flight would dial
	// blind, which is the failure the set-equality gate exists to stop. The
	// tokens land here in the sync that absorbs the wave; the accessors land in
	// the client waves that follow.
	OpClaimNextIssue:          "issues.claimNext",
	OpReleaseIssue:            "issues.release",
	OpCountIssues:             "issues.count",
	OpCreateIssue:             "issues.create",
	OpApplyBatch:              "issues.batchApply",
	OpCompareAndSetMetadata:   "issues.casMetadata",
	OpListEvents:              "events.list",
	OpWatchEvents:             "events.watch",
	OpListSettings:            "config.list",
	OpGetSetting:              "config.get",
	OpSetSetting:              "config.set",
	OpUnsetSetting:            "config.unset",
	OpListDependencies:        "dependencies.list",
	OpListBlockingAnnotations: "dependencies.blocking",
	OpGetDependencyTree:       "dependencies.tree",
	OpCountDependencyEdges:    "dependencies.count",
	OpListRelatedIssues:       "issues.related",
	OpAddComment:              "issues.addComment",
	OpListDependencyCycles:    "dependencies.cycles",
	OpAddDependencies:         "dependencies.add",
	OpRemoveDependency:        "dependencies.remove",
	OpListMemories:            "memories.list",
	OpRememberMemory:          "memories.remember",
	OpGetMemory:               "memories.get",
	OpForgetMemory:            "memories.forget",
}

// CapProjectEnforce is the behavior capability the server advertises to announce
// per-request Bd-Project-Id enforcement, spelled exactly as httpapi's constant of
// the same name (held to it by TestProjectEnforceCapabilityMatchesTheServer).
// Unlike the per-operation tokens in opCapability it gates no route: it names a
// server-wide behavior, not an operation. The client does not consult it before
// stamping — the stamp is always sent and an older server simply ignores an
// unknown header — but it rides in the same ContextResponse.capabilities list, so
// the capability-union parity gate must account for it.
const CapProjectEnforce = "project.enforce"

// CapListSort is the behavior capability announcing that listIssues honors
// `sort` and `reverse`, spelled exactly as httpapi's constant of the same name
// (held to it by TestTheProjectIdentityVocabularyMatchesTheServer). Like
// CapProjectEnforce it gates no route — listIssues has its own operation token
// — it names a property of that operation, so it rides here rather than on
// opCapability.
//
// Nothing in this package reads it yet: the server surface ships first and the
// client that pushes a sort down follows. It is declared now because the
// capability-union parity gate compares this client's whole vocabulary with the
// server's, so the mirror is what makes the two land together.
const CapListSort = "issues.list.sort"

// CapCountScope is the behavior capability announcing that countIssues honors
// the four scope parameters `parent`, `no_parent`, `exclude_type` and
// `exclude_status` (upstream #7199), spelled exactly as httpapi's
// CapIssuesCountScope (held to it by TestTheProjectIdentityVocabularyMatchesTheServer).
// Like CapListSort it names parameters on an existing operation —
// issues.count is already countIssues' per-operation token — so it rides here
// rather than on opCapability.
//
// Nothing in this package reads it yet: the encoder table maps the four
// members (encode.countTable), and the count role client that dials
// countIssues is what must consult it — refusing LOCALLY with a typed
// capability error naming this token, before any network call, when a request
// populates ParentID, NoParent, ExcludeTypes or ExcludeStatus and the
// handshake snapshot does not advertise it (httpapi.CapIssuesCountScope's doc
// states that obligation and the downstream fallback it protects). It is
// declared now for the same reason CapListSort was: the capability-union
// parity gate compares this client's whole vocabulary with the server's.
const CapCountScope = "issues.count.scope"

// CapBatchApplyLarge is the behavior capability announcing that issues.batchApply
// accepts a batch larger than the compiled-in floor (100 items), up to the
// raised ceiling (1000), spelled exactly as httpapi's constant of the same name
// (held to it by TestTheBatchCapVocabularyMatchesTheServer). Like CapListSort it
// names a property of an existing operation — issues.batchApply already has its
// own per-operation token in opCapability — rather than a route of its own, so
// it rides here.
//
// Preflight reads this one: a batch over 100 items refuses locally with a typed
// capability error BEFORE any network call unless the handshake snapshot
// advertises it, in which case the ceiling is 1000. The cap is never a bare
// compiled constant for that reason — it is always read off the snapshot this
// token gates.
const CapBatchApplyLarge = "issues.batchApplyLarge"

// CapExternalDependencies is the CONDITIONAL behavior capability announcing
// that the ready, claim and close operations of this server apply bd's
// external-dependency policy themselves, spelled exactly as httpapi's constant
// of the same name (held to it by TestExternalDependencyCapabilityMatchesTheServer).
//
// It is not in behaviorCapabilities because it is not a property of the build:
// httpapi advertises it only when the serving process composed its roles through
// the policy layer, so httpapi.Capabilities() — the build-level list the parity
// gate compares against — never contains it. The store reads it to answer
// storage.ServerEnforcedPolicy, which is what decides whether this client layers
// the policy itself.
const CapExternalDependencies = "policy.external_dependencies"

// ClientWireRevision is the wire shape this client was built to speak and
// decode, mirroring internal/httpapi/wire_revision.go's CurrentWireRevision.
// It is sent as the Bd-Wire-Revision request header on every request
// (Client.stampRequest), so a server whose own min_client_wire_revision has
// moved past it refuses with the typed wire_revision_unsupported problem
// (problem.go's WireRevisionUnsupportedError) instead of answering with a
// shape this build was never compiled to read. It doubles as the upper bound
// of the handshake gate below: nothing compiled against revision 2 can
// promise to decode revision 3.
const ClientWireRevision = 2

// ClientMinWireRevision is the oldest SERVER-reported wire_revision this
// client tolerates — the other half of DESIGN.txt sec 4's "Client rule":
// handshake once per Store and gate on api_version == "v0" AND wire_revision
// within [client_min, client_max]. It is 0, not 2, because a decoded 0 means
// only that the server omitted ContextResponse.wire_revision entirely: 0 and
// 1 are permanently retired values no server implementing the member will
// ever legitimately send (see that field's doc comment), and the one thing a
// server old enough to omit it can still do is answer revision/expected_version
// tokens as bare JSON integers rather than the decimal strings upstream #6053
// standardized — a shape this package tolerates on both halves of the wire:
// problem.go's legacyRevisionFields on the error path, and
// revision_tolerance.go's serverPredatesRevisionStrings on every success-path
// response type that carries a `revision` member. There is therefore nothing
// on the low end for this client to refuse.
const ClientMinWireRevision = 0

// checkWireRevision applies the handshake half of the Client rule to a decoded
// ContextResponse, returning the typed skew refusal when the server's wire
// shape falls outside what this client build can speak, or nil when it is
// safe to proceed.
//
// Two independent conditions trigger it: the server's own
// min_client_wire_revision already exceeds what this client declares (so
// every request would earn the same wire_revision_unsupported 400 the
// Bd-Wire-Revision header invites — refusing here spends no round trip
// learning what the handshake already answered), or the server's own
// wire_revision is past ClientWireRevision (a future, non-additive wire
// change this build predates). A decoded wire_revision of 0 never trips the
// second check on its own: per ClientMinWireRevision's doc, that is the
// "omitted" signal, not a value above range.
func (c *Client) checkWireRevision(body apigen.ContextResponse) error {
	switch {
	case body.MinClientWireRevision > ClientWireRevision:
		return &WireRevisionSkewError{
			ServerURL:             c.base.Redacted(),
			BdVersion:             stripControlRunes(body.BdVersion),
			ClientWireRevision:    ClientWireRevision,
			ServerWireRevision:    body.WireRevision,
			MinClientWireRevision: body.MinClientWireRevision,
		}
	case body.WireRevision > ClientWireRevision:
		return &WireRevisionSkewError{
			ServerURL:             c.base.Redacted(),
			BdVersion:             stripControlRunes(body.BdVersion),
			ClientWireRevision:    ClientWireRevision,
			ServerWireRevision:    body.WireRevision,
			MinClientWireRevision: body.MinClientWireRevision,
		}
	default:
		return nil
	}
}

// ErrWireRevisionSkew reports a handshake-time wire-shape mismatch this client
// cannot safely proceed past. See WireRevisionSkewError.
var ErrWireRevisionSkew = errors.New("bd serve speaks a wire revision this client does not")

// WireRevisionSkewError is DESIGN.txt sec 4's Client rule, violated: the
// server's wire shape is outside [ClientMinWireRevision, ClientWireRevision].
// It is raised by Handshake itself, from a 200 ContextResponse, which is what
// separates it from WireRevisionUnsupportedError (problem.go) — that one is
// the SERVER refusing a request with a 400 after reading this client's own
// declared Bd-Wire-Revision header; this one is the CLIENT declining to
// proceed after reading the server's.
type WireRevisionSkewError struct {
	ServerURL string
	BdVersion string
	// ClientWireRevision is this build's own declared revision.
	ClientWireRevision int
	// ServerWireRevision is the server's ContextResponse.wire_revision, decoded
	// as-is (0 means the server omitted the member; see ClientMinWireRevision).
	ServerWireRevision int
	// MinClientWireRevision is the server's ContextResponse.min_client_wire_revision.
	MinClientWireRevision int
}

func (e *WireRevisionSkewError) Error() string {
	if e.MinClientWireRevision > e.ClientWireRevision {
		return fmt.Sprintf("bd serve at %s (bd_version %s) requires a client wire revision of at least %d; this client speaks %d",
			e.ServerURL, e.BdVersion, e.MinClientWireRevision, e.ClientWireRevision)
	}
	return fmt.Sprintf("bd serve at %s (bd_version %s) speaks wire revision %d, newer than any shape this client build knows how to decode (max %d)",
		e.ServerURL, e.BdVersion, e.ServerWireRevision, e.ClientWireRevision)
}

func (e *WireRevisionSkewError) Unwrap() error { return ErrWireRevisionSkew }

// behaviorCapabilities mirrors the server's own behavior-token set (httpapi's
// behaviorCapabilities): the advertised tokens that name a server-wide behavior
// rather than an operation. TestCapabilityTableMatchesTheServerRouteTable proves
// union(opCapability tokens, this) set-equal with httpapi.Capabilities(), which is
// what makes the server change and this client change land together: the parity
// test goes red the moment one ships without the other.
var behaviorCapabilities = []string{CapProjectEnforce, CapBatchApplyLarge, CapListSort, CapCountScope}

// CapabilityFor reports the capability token gating op, and whether op is on
// this client's map at all. An operation with no token — liveness, the
// handshake — reports ("", true).
func CapabilityFor(op string) (string, bool) {
	token, ok := opCapability[op]
	return token, ok
}

// IsBaseline reports whether op dispatches without a capability pre-flight.
func IsBaseline(op string) bool { return baselineOps[op] }

// Snapshot is the parsed handshake: one ContextResponse and its capability list
// indexed for lookup.
type Snapshot struct {
	Context apigen.ContextResponse
	caps    map[string]struct{}
}

// Has reports whether the server advertised token.
func (s *Snapshot) Has(token string) bool {
	_, ok := s.caps[token]
	return ok
}

// Capabilities is the advertised token list, sorted, as a copy.
func (s *Snapshot) Capabilities() []string {
	out := make([]string, 0, len(s.caps))
	for token := range s.caps {
		out = append(out, token)
	}
	slices.Sort(out)
	return out
}

type handshakeCache struct {
	mu   sync.Mutex
	snap *Snapshot
}

// Health drives the liveness probe. It is the one operation served with no
// credential, and it stays 200 while the database is wedged — so a green health
// check says the process is up and nothing more.
func (c *Client) Health(ctx context.Context) error {
	var out apigen.Health
	return c.Do(ctx, Request{Op: OpHealth, Method: http.MethodGet, Path: PathHealth}, &out)
}

// GetContext fetches the startup snapshot with no caching and no gates. Callers
// that want the process-wide, gated, identity-checked one want Handshake.
func (c *Client) GetContext(ctx context.Context) (apigen.ContextResponse, error) {
	var out apigen.ContextResponse
	err := c.Do(ctx, Request{Op: OpGetContext, Method: http.MethodGet, Path: PathContext}, &out)
	return out, err
}

// Handshake fetches the context once and caches it for the life of the client,
// applying the two gates that must not be skipped: api_version equality and, if
// the workspace recorded one, project identity.
//
// The fetch is lazy — it happens on the first post-baseline dispatch, not at
// open — so a `bd ready` against a server that serves only the baseline costs
// no extra round trip. A FAILED handshake is not cached: a server that was down
// when the first post-baseline command ran must not poison the rest of the
// process.
func (c *Client) Handshake(ctx context.Context) (*Snapshot, error) {
	c.handshake.mu.Lock()
	defer c.handshake.mu.Unlock()
	if c.handshake.snap != nil {
		return c.handshake.snap, nil
	}

	body, err := c.GetContext(ctx)
	if err != nil {
		return nil, err
	}
	if body.ApiVersion != APIVersion {
		return nil, &APIVersionError{ServerURL: c.base.Redacted(), Got: body.ApiVersion, Want: APIVersion}
	}
	// The Client rule's other half (DESIGN.txt sec 4), run right after the
	// api_version gate and before the identity gate: a server this client
	// cannot decode is not a server worth reporting a wrong-project diagnosis
	// against either, and api_version is the coarser, cheaper check of the two.
	if err := c.checkWireRevision(body); err != nil {
		return nil, err
	}
	if c.expectID != "" && body.ProjectId != c.expectID {
		// These three are server-controlled and end up in an error rendered to a
		// terminal — often through a per-id write sink (bd close/reopen/update)
		// that bypasses the CLI's display-layer strip — so their control runes are
		// removed at construction, the same source-layer strip mapProblem applies.
		// RepoRoot renders via %s (raw); Got and Database render via %q (already
		// escaped), stripped too so a future Error() change cannot reintroduce the
		// leak. Expected is the workspace's own pinned id, not server-controlled.
		//
		// repo_root is OPTIONAL on the wire, so the generated member is a pointer;
		// an absent one is the empty string here, which is exactly what Error()
		// already reads as "that server disclosed no root" (it omits the whole
		// parenthetical when Database and RepoRoot are both empty).
		return nil, &ProjectMismatchError{
			ServerURL: c.base.Redacted(),
			Expected:  c.expectID,
			Got:       stripControlRunes(body.ProjectId),
			Database:  stripControlRunes(body.Database),
			RepoRoot:  stripControlRunes(deref(body.RepoRoot)),
		}
	}

	caps := make(map[string]struct{}, len(body.Capabilities))
	for _, token := range body.Capabilities {
		caps[token] = struct{}{}
	}
	c.handshake.snap = &Snapshot{Context: body, caps: caps}
	return c.handshake.snap, nil
}

// ServerContext is the handshake as the STORE's transport seam spells it: one
// gated, cached, identity-checked snapshot per process.
//
// It exists so a *Client satisfies that seam directly rather than through an
// adapter every build would have to write identically. The pointer is into the
// cache and the caller must treat it as read-only — the same rule Handshake's
// Snapshot carries, for the same reason.
func (c *Client) ServerContext(ctx context.Context) (*apigen.ContextResponse, error) {
	snap, err := c.Handshake(ctx)
	if err != nil {
		return nil, err
	}
	return &snap.Context, nil
}

// Preflight applies the two-speed policy to op, and is the call every dispatch
// site makes before building a request.
//
// A baseline operation returns immediately. Every other operation forces the
// handshake and checks its token, because an unrouted path on an older server
// answers a bare 404 that is INDISTINGUISHABLE from an entity's not_found — the
// version-skew signal would arrive spelled as "no such issue". Consulting the
// advertised list is the only way to tell the two apart, and it has to happen
// before the dial.
func (c *Client) Preflight(ctx context.Context, op string) error {
	token, known := CapabilityFor(op)
	if !known {
		return fmt.Errorf("no such v0 operation %q", op)
	}
	if IsBaseline(op) || token == "" {
		return nil
	}
	snap, err := c.Handshake(ctx)
	if err != nil {
		return err
	}
	if snap.Has(token) {
		return nil
	}
	// BdVersion is server-controlled and reaches the case-2 taxonomy text AND a
	// per-id write sink that bypasses the display-layer strip, so its control runes
	// are removed at construction (Op and Capability are this client's own known
	// strings; Capabilities is a token-membership set, never rendered as free text).
	return &CapabilityError{
		Op:           op,
		Capability:   token,
		ServerURL:    c.base.Redacted(),
		BdVersion:    stripControlRunes(snap.Context.BdVersion),
		Capabilities: snap.Capabilities(),
	}
}

// ErrCapabilityAbsent reports that the server does not advertise the capability
// an operation needs.
var ErrCapabilityAbsent = errors.New("bd serve does not advertise the capability this operation needs")

// CapabilityError is the typed form of that refusal. It carries what the
// refusal taxonomy's case-2 text names — the server, its version, and the
// tokens it DOES advertise — so the rendering layer parses nothing.
type CapabilityError struct {
	Op           string
	Capability   string
	ServerURL    string
	BdVersion    string
	Capabilities []string
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("bd serve at %s (bd_version %s) does not advertise capability %q, which %s requires",
		e.ServerURL, e.BdVersion, e.Capability, e.Op)
}

func (e *CapabilityError) Unwrap() error { return ErrCapabilityAbsent }

// NewCapabilityError builds the capability-absent refusal for a behavior the
// caller checked itself, off a handshake it already holds, rather than for an
// operation Preflight checked. The server-controlled version is stripped of
// control runes the way Preflight strips it, because the refusal reaches a
// terminal through sinks that bypass the display-layer strip.
func NewCapabilityError(op, token, serverURL string, ctx *apigen.ContextResponse) *CapabilityError {
	e := &CapabilityError{Op: op, Capability: token, ServerURL: serverURL}
	if ctx != nil {
		e.BdVersion = stripControlRunes(ctx.BdVersion)
		e.Capabilities = append([]string(nil), ctx.Capabilities...)
		slices.Sort(e.Capabilities)
	}
	return e
}

// ErrAPIVersion reports a server on a path major this client cannot address.
var ErrAPIVersion = errors.New("bd serve speaks a different api_version")

// APIVersionError names both versions, because neither alone tells an operator
// which side to move.
type APIVersionError struct {
	ServerURL string
	Got       string
	Want      string
}

func (e *APIVersionError) Error() string {
	return fmt.Sprintf("bd serve at %s serves api_version %q; this client speaks %q", e.ServerURL, e.Got, e.Want)
}

func (e *APIVersionError) Unwrap() error { return ErrAPIVersion }

// ErrInvalidProjectID reports that the workspace's pinned project id is not a
// value that can be sent as the Bd-Project-Id header on every request.
var ErrInvalidProjectID = errors.New("pinned project id is not a valid header value")

// InvalidProjectIDError is the loud refusal New raises when the pinned project id
// cannot be stamped. The id comes from the workspace's sidecar, recorded by
// `bd connect`, and it is stamped on EVERY request — so a corrupt one is caught
// once, at open, rather than skipped (which would leave the workspace unenforced)
// or deferred to a per-request transport failure. The offending value is rendered
// %q so its own bad bytes — the reason it is invalid — cannot inject into the
// error text on its way to a terminal.
type InvalidProjectIDError struct {
	ServerURL string
	ProjectID string
}

func (e *InvalidProjectIDError) Error() string {
	return fmt.Sprintf("the project id %q pinned for the workspace connected to bd serve at %s is not a valid %s header value; re-run bd connect to re-record the workspace's identity from its sidecar",
		e.ProjectID, e.ServerURL, ProjectIDHeader)
}

func (e *InvalidProjectIDError) Unwrap() error { return ErrInvalidProjectID }

// ErrProjectMismatch reports that the server owns a different workspace.
var ErrProjectMismatch = errors.New("bd serve owns a different workspace")

// ProjectMismatchRecovery is the one action that resolves a wrong-server
// mismatch: point the workspace back at the server that owns it. There is no
// local database to reconcile — bd doctor and bd bootstrap have nothing to fix
// on an http workspace — so reconnecting IS the whole recovery. It is exported
// so cmd/bd's pre-write identity check can render the same guidance the
// post-baseline read path already surfaces (design D1/D6) without restating the
// string in a second place that could drift.
const ProjectMismatchRecovery = "re-run bd connect against the server that owns this workspace"

// ProjectMismatchError is the wrong-server diagnostic. It names both project
// ids AND the server's own database and repo root, because "the ids differ" does
// not tell an operator WHICH wrong server answered — and on a shared host that
// is the whole question.
type ProjectMismatchError struct {
	ServerURL string
	Expected  string
	Got       string
	Database  string
	RepoRoot  string
}

func (e *ProjectMismatchError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "bd serve at %s owns project %q, but this workspace expects %q", e.ServerURL, e.Got, e.Expected)
	if e.Database != "" || e.RepoRoot != "" {
		fmt.Fprintf(&b, " (that server serves database %q from %s)", e.Database, e.RepoRoot)
	}
	b.WriteString("; " + ProjectMismatchRecovery)
	return b.String()
}

func (e *ProjectMismatchError) Unwrap() error { return ErrProjectMismatch }

// BatchTooLargeError is a locally-refused issues:batchApply plan: the item
// count exceeds issueops.MaxApplyBatchItems, the absolute ceiling every v0
// server enforces regardless of capability (internal/httpapi's
// maxApplyBatchItems). ApplyBatch (writes.go) raises it before any network
// call — never dialing a plan this large — the same way Preflight never
// dials an operation the server has not advertised. It lives here rather
// than beside ApplyBatch because every exported method in writes.go is swept
// by TestEveryOperationMethodRoutesThroughTheSharedDispatch as an operation
// dispatch method; this type's two methods are not one.
//
// It unwraps to issueops.ErrValidation, the sentinel a server's own 400 on
// the same oversized plan would classify as, had the request been allowed to
// reach it.
type BatchTooLargeError struct {
	Op        string
	ServerURL string
	Count     int
	Limit     int
}

func (e *BatchTooLargeError) Error() string {
	return fmt.Sprintf("%s: a plan of %d items exceeds the %d-item ceiling bd serve at %s enforces; split it into requests of %d or fewer",
		e.Op, e.Count, e.Limit, e.ServerURL, e.Limit)
}

func (e *BatchTooLargeError) Unwrap() error { return issueops.ErrValidation }
