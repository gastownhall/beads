// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/writewire.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// WriteWire is the transport surface the WRITE roles dispatch through: the six
// mutating v0 operations plus the two memory reads that belong to the same
// role, plus the ready listing the composed ClaimNext is built from.
//
// It is a separate interface from WireClient's own method rather than a
// widening of it because a role bead should be able to state exactly what
// transport it needs. WireClient embeds it, so a store still holds one
// transport and the compiler still proves a build linked one.
//
// Every method here takes and returns apigen types. That is not laziness about
// a domain type: apigen.Issue IS internal/types.Issue (a Go type alias pinned
// by the document), so there is no second wire struct to convert through, and
// minting one would be the field-by-field copy the pinning exists to prevent.
type WriteWire interface {
	ClaimIssue(ctx context.Context, id string, body apigen.ClaimRequest) (*apigen.ClaimResponse, error)
	// ClaimNextIssue takes the next ready issue as one act. It is the only
	// method here that names no resource AND carries a query: the filter that
	// chooses the row is listReadyWork's vocabulary spelled as a query string,
	// and the actor is the whole body.
	ClaimNextIssue(ctx context.Context, params url.Values, body apigen.ClaimNextRequest) (*apigen.ClaimNextResponse, error)
	// CreateIssue creates ONE issue and answers the row as stored. It names no
	// id: the request may name one and may not, and the server mints it when
	// the request does not.
	CreateIssue(ctx context.Context, body apigen.CreateIssueRequest) (*apigen.Issue, error)
	CloseIssue(ctx context.Context, id string, body apigen.CloseIssueRequest) (*apigen.CloseIssueResponse, error)
	ReopenIssue(ctx context.Context, id string, body apigen.ReopenIssueRequest) (*apigen.ReopenIssueResponse, error)
	// ReleaseIssue gives back the claim on one issue. Its response carries the
	// post-release row version BESIDE the row rather than on it, because
	// types.Issue.RowVersion is `json:"-"`; the role stitches the two together.
	ReleaseIssue(ctx context.Context, id string, body apigen.ReleaseIssueRequest) (*apigen.ReleaseIssueResponse, error)
	// AddComment appends one comment to the thread an issue owns, and is the one
	// write here whose path names a SUB-RESOURCE collection rather than the
	// issue. What comes back is the member the POST created — the stored comment,
	// with the id the insert minted and created_at at the column's precision.
	AddComment(ctx context.Context, id string, body apigen.AddCommentRequest) (*apigen.Comment, error)
	// UpdateIssue takes the patch as a DOCUMENT rather than a struct: on this
	// operation a member's presence is the signal to write it and an explicit
	// null on one of the four nullable members is the clear, and no struct of
	// pointers with `omitempty` can express both. The builder is
	// encodeIssuePatch; this layer marshals what it decided.
	//
	// The guard trio travels beside it as wire.UpdateGuards rather than inside
	// the document — the server reads them off the body's top level — and is a
	// typed struct because it is the one part of this request whose ABSENT and
	// ZERO states are different requests.
	UpdateIssue(ctx context.Context, id, actor string, patch map[string]any, guards wire.UpdateGuards, forceNotesOverwrite bool) (*apigen.UpdateIssueResponse, error)

	// CompareAndSetMetadata is the one write on this seam whose REFUSAL is a
	// 200: a lost race answers `swapped: false` with the value that refused it,
	// so the verdict is a member of a success body rather than an error, and
	// the role reads it there.
	CompareAndSetMetadata(ctx context.Context, id string, body apigen.CompareAndSetMetadataRequest) (*apigen.CompareAndSetMetadataResponse, error)

	AddDependencies(ctx context.Context, body apigen.AddDependenciesRequest) (*apigen.AddDependenciesResponse, error)
	RemoveDependency(ctx context.Context, body apigen.RemoveDependencyRequest) (*apigen.RemoveDependencyResponse, error)

	// The three collection-level custom methods. They name no resource in the
	// path — a sweep describes a set, a delete and a batch create carry their
	// ids and their items in the body — so unlike the four above they take no
	// id argument at all.
	SweepIssues(ctx context.Context, body apigen.SweepRequest) (*apigen.SweepResult, error)
	DeleteIssues(ctx context.Context, body apigen.DeleteIssuesRequest) (*apigen.DeleteIssuesResult, error)
	BatchCreateIssues(ctx context.Context, body apigen.BatchCreateRequest) (*apigen.BatchCreateResponse, error)

	// ApplyBatch applies an ordered, heterogeneous plan as one transaction or
	// not at all. Its body is wire.ApplyBatchRequest rather than apigen's for
	// UpdateIssue's reason and in one place only: an update item's patch is a
	// DOCUMENT, because presence is the signal there and an explicit null on
	// one of the four nullable members is the clear. Everything else in the
	// body is the generated type.
	//
	// It has no per-item outcome array on a refusal, unlike batchClose: the
	// request is all or nothing, so the offender travels in the problem
	// document's item_* members and the role rebuilds *issueops.ItemError from
	// them.
	ApplyBatch(ctx context.Context, body wire.ApplyBatchRequest) (*apigen.ApplyBatchResponse, error)

	// BatchCloseIssues closes a set of issues as one act. Its response carries
	// per-item outcomes inside a 200, which the BatchCloser role walks; the top
	// level classifies through the shared problem mapper like every other write.
	BatchCloseIssues(ctx context.Context, body apigen.BatchCloseRequest) (*apigen.BatchCloseResponse, error)

	RememberMemory(ctx context.Context, body apigen.RememberRequest) (*apigen.RememberedMemory, error)
	RecallMemory(ctx context.Context, key string) (*apigen.Memory, error)
	ForgetMemory(ctx context.Context, key string) (*apigen.Memory, error)
	ListMemories(ctx context.Context, search string) (*apigen.MemoriesPage, error)

	ListReadyWork(ctx context.Context, params url.Values) (*apigen.ReadyPage, error)
}

// roleWire narrows the store's transport for a role that is about to dispatch.
//
// A store built with no transport is a build-wiring fault, not a user's, and it
// must not surface as a nil-pointer panic three frames into a role. Every role
// accessor below calls this first, so the fault is named once.
func (s *Store) roleWire(accessor string) (WriteWire, error) {
	if s == nil || s.wire == nil {
		return nil, fmt.Errorf("%w: %s cannot dial %s", ErrNoTransport, accessor, s.target)
	}
	return s.wire, nil
}

// invalid builds a deterministic request-validation failure.
//
// Every role's doc promises these match issueops.ErrValidation, and every one of
// them is raised BEFORE the dial. That ordering is the point: a request the role
// contract calls invalid must not reach a shared server as a 400 whose problem
// body a future server release might spell differently, and it must not consume
// a write slot to be told what the client already knew.
func invalid(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), issueops.ErrValidation)
}

// refuse raises the typed refuse-not-drop failure for a request member the v0
// wire has no place for, citing the divergence-ledger row that says why.
//
// Raising BY LEDGER ID rather than by string is what makes a refusal without a
// row impossible: RowByID panics on an id the ledger does not carry, so a
// forgotten entry is a loud programming error rather than a refusal with no
// vocabulary for the taxonomy to render.
func refuse(op encode.Op, ledgerID string) error {
	return &encode.RefusedError{Op: op, Row: encode.RowByID(ledgerID)}
}

// requireActor is the actor rule every write on this surface shares. The server
// applies its own (trim, 256 bytes, no control characters) and answers a 400;
// this is only the emptiness half, which every role's own contract states.
func requireActor(actor string) error {
	if strings.TrimSpace(actor) == "" {
		return invalid("actor is required")
	}
	return nil
}

// requireID refuses an empty id before it can become a path.
//
// Without it an empty id would join to the COLLECTION path and turn an
// operation on one resource into an operation on all of them. escapeSegment
// refuses it too, but it refuses with a transport error where the role
// contracts promise ErrValidation.
func requireID(kind, id string) error {
	if strings.TrimSpace(id) == "" {
		return invalid("%s is required", kind)
	}
	return nil
}
