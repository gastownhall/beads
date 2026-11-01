// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/batchcreator.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"reflect"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/issueops"
)

// maxBatchCreateItems mirrors the wire's own bound (internal/httpapi's
// batch_create.go, and the document's maxItems). It is checked here for
// maxDeleteIDs' reason: the request IS the transaction, so splitting a longer
// plan into two requests would turn one atomic create into two — and half a
// file is the outcome the role's all-or-nothing promise exists to make
// impossible. A caller that really has more than a hundred issues splits the
// plan deliberately, which is the document's own advice.
const maxBatchCreateItems = 100

// batchCreateCarriedIssueMembers is the wire's item vocabulary: the eight
// members of apigen.BatchCreateItem that describe the issue itself, keyed by the
// types.Issue field each one carries.
//
// It is the ALLOWLIST half of refuse-not-drop for this operation. What it does
// not name is refused, and there is no third arm — see refuseUnwirableIssue.
//
// Status and CreatedBy are deliberately absent, each for its own reason, and
// neither belongs in roleIgnoredCreateIssueMembers below: both are genuinely
// ACCEPTED by the role's canonical create rules (CreateRequest.Issue's doc,
// which BatchCreateItem.Issue inherits without exception), so silently
// dropping either would be exactly the data loss refuse-not-drop exists to
// prevent.
//   - Status: apigen.BatchCreateItem publishes no status member at all, unlike
//     single create's apigen.CreateIssueRequest (which does, see
//     createCarriedIssueMembers). A populated value is correctly refused. The
//     common case is not lost: PreparePublicCreateRequest defaults an empty
//     Status to StatusOpen on every backend, so a caller that only ever wants
//     the default (cmd/bd/markdown.go's `bd create --file`) leaves it unset
//     rather than spelling out "open" and tripping this refusal. That removes
//     ONE refusal from `bd create --file`, not all of them: its request still
//     always names a Provenance (W-CreateBatchRequest.Provenance) and usually
//     an Owner from git config (W-BatchCreateItem.Issue), so it still refuses
//     over this wire.
//   - CreatedBy: apigen.BatchCreateItem has no member for it either, but the
//     server's create role defaults every item's created_by to the request's
//     actor (issueops.PreparePublicCreateRequest), as single create does. A CreatedBy
//     naming the actor is therefore carried BY the actor
//     (actorStampedCreateMember below), and any other value is refused rather
//     than silently replaced by the stamp.
var batchCreateCarriedIssueMembers = map[string]string{
	"Title":              "title",
	"Description":        "description",
	"Design":             "design",
	"AcceptanceCriteria": "acceptance_criteria",
	"Priority":           "priority",
	"IssueType":          "issue_type",
	"Assignee":           "assignee",
	"Labels":             "labels",
}

// roleIgnoredCreateIssueMembers are the members the ROLE ITSELF ignores on a
// create, each with the reason, taken from issueops.CreateRequest.Issue's own
// list: "It ignores ContentHash, RowVersion, lease state, compaction state,
// routing overrides, hydration flags, and derived fields."
//
// They are not divergences and must not be refused. A local create drops them
// too, so refusing here would make an http workspace REJECT a request every
// other backend accepts — the opposite failure from the one refuse-not-drop
// guards, and just as wrong.
var roleIgnoredCreateIssueMembers = map[string]string{
	"ContentHash":       "derived: recomputed from the stored content",
	"RowVersion":        "derived: the engine stamps its own row lock",
	"LeaseExpiresAt":    "lease state: a create grants no lease",
	"HeartbeatAt":       "lease state: a create grants no lease",
	"LeaseGrantedNode":  "lease state: a create grants no lease",
	"CompactionLevel":   "compaction state: nothing is compacted at create",
	"CompactedAt":       "compaction state: nothing is compacted at create",
	"CompactedAtCommit": "compaction state: nothing is compacted at create",
	"OriginalSize":      "compaction state: nothing is compacted at create",
	"SourceRepo":        "routing override: which local database owns the row",
	"IDPrefix":          "routing override: id generation is the server's",
	"PrefixOverride":    "routing override: id generation is the server's",
	"WispPlaneOverride": "routing override: import's explicit plane marker",
	"IsLitePartial":     "hydration flag: describes a READ, not a create",
}

// actorStampedCreateMember reports whether a populated issue member is one the
// server writes from the request's actor rather than reads from the body, and
// already holds the value that stamp will write.
//
// CreatedBy is the one such member: every create shape's role defaults an empty
// created_by to the actor (issueops.PreparePublicCreateRequest), on the server
// as locally. It is neither carried nor role-ignored — a local create keeps
// whatever explicit value the issue names — so it is carried BY THE ACTOR: a CreatedBy that names the actor,
// which is what `bd create`, `bd create --file` and the graph apply send,
// arrives as written, and any other value would be silently replaced by the
// stamp and is refused.
func actorStampedCreateMember(member string, issue *issueops.Issue, actor string) bool {
	return member == "CreatedBy" && issue.CreatedBy == actor
}

// httpBatchCreator serves issueops.BatchCreator from the batchCreateIssues
// custom method (design D8 row 14) — the write side of `bd create --file`.
//
// THE REQUEST IS THE TRANSACTION on both sides, and that is the whole reason
// this composes at all: the wire's operation is itself all-or-nothing, so one
// role call is one request is one server-side transaction with at most one
// history entry. Nothing here loops, and nothing here chunks.
//
// WHAT THE WIRE'S ITEM CANNOT SAY is where the work is. apigen.BatchCreateItem
// publishes eight content members and a dependency list; issueops reads a whole
// types.Issue, and the role accepts far more of it than that — an explicit id,
// the wisp flags, metadata, the gate and molecule fields, every timestamp.
// Every one of those is refused rather than dropped (ledger row
// W-BatchCreateItem.Issue): a create that reported success having silently
// planted an issue in the durable tier that the caller asked to be ephemeral is
// data loss the caller has no way to learn about.
//
// The refusal is decided by REFLECTION over the two tables above rather than by
// a hand-written switch, so a member added to types.Issue tomorrow is refused
// the day it lands instead of dropped until someone notices.
type httpBatchCreator struct {
	store *Store
	wire  WriteWire
}

var _ issueops.BatchCreator = (*httpBatchCreator)(nil)

// CreateBatch dials POST /v0/beads/issues:batchCreate.
func (b *httpBatchCreator) CreateBatch(ctx context.Context, req issueops.CreateBatchRequest) (result issueops.CreateBatchResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError —
	// however deeply batchCreateItem/batchCreateEdge/refuseUnwirableIssue
	// nested it inside an "items[%d]..." prefix — into *InexpressibleError so
	// errors.As(err, &unsupported) reaches *storage.ErrUnsupported, same as
	// inexpressible does for a read role. The original composite message
	// (including that prefix) is preserved in the decorated error's own text.
	defer func() { err = b.store.inexpressible("BatchCreator.CreateBatch", err) }()
	if err := requireActor(req.Actor); err != nil {
		return issueops.CreateBatchResult{}, err
	}
	if len(req.Items) == 0 {
		return issueops.CreateBatchResult{}, invalid("a batch create names no items")
	}
	if len(req.Items) > maxBatchCreateItems {
		return issueops.CreateBatchResult{}, refuse(encode.OpBatchCreateIssues, "L-batchcreate-bound")
	}
	if req.Provenance != "" {
		return issueops.CreateBatchResult{}, refuse(encode.OpBatchCreateIssues, "W-CreateBatchRequest.Provenance")
	}
	if req.ForceIDPrefix {
		return issueops.CreateBatchResult{}, refuse(encode.OpBatchCreateIssues, "W-CreateBatchRequest.ForceIDPrefix")
	}

	items := make([]apigen.BatchCreateItem, 0, len(req.Items))
	for i, item := range req.Items {
		wireItem, err := batchCreateItem(i, item, req.Actor)
		if err != nil {
			return issueops.CreateBatchResult{}, err
		}
		items = append(items, wireItem)
	}
	priorities := make([]**int, len(items))
	for i := range items {
		priorities[i] = &items[i].Priority
	}
	if err := b.store.pinCreateDefaultPriority(ctx, priorities...); err != nil {
		return issueops.CreateBatchResult{}, err
	}

	res, err := b.wire.BatchCreateIssues(ctx, apigen.BatchCreateRequest{Actor: req.Actor, Items: items})
	if err != nil {
		return issueops.CreateBatchResult{}, err
	}

	// One entry per requested item, in request order, never nil. The response is
	// read back rather than the request echoed because the GENERATED IDS are the
	// one fact the request cannot carry — and a server that answered a different
	// length would be caught here rather than by an index panic upstream.
	if len(res.Items) != len(req.Items) {
		return issueops.CreateBatchResult{}, fmt.Errorf(
			"batchCreateIssues answered %d issues for %d items; the operation is all-or-nothing and has no partial outcome",
			len(res.Items), len(req.Items))
	}
	issues := make([]*issueops.Issue, len(res.Items))
	for i := range res.Items {
		issue := res.Items[i]
		issues[i] = &issue
	}
	return issueops.CreateBatchResult{Issues: issues}, nil
}

// batchCreateItem projects one role item onto the wire's item.
func batchCreateItem(index int, item issueops.BatchCreateItem, actor string) (apigen.BatchCreateItem, error) {
	if item.Issue == nil {
		return apigen.BatchCreateItem{}, invalid("items[%d] carries no issue", index)
	}
	if len(item.Issue.Comments) > 0 || len(item.Issue.Dependencies) > 0 {
		// The ROLE's own rule, not a wire divergence: edges are supplied through
		// the item's own Dependencies, and a create batch has no way to supply
		// comments at all. A local backend refuses this too.
		return apigen.BatchCreateItem{}, invalid(
			"items[%d].Issue carries comments or dependencies; supply edges through the item's own Dependencies", index)
	}
	if err := refuseUnwirableIssue(index, item.Issue, actor); err != nil {
		return apigen.BatchCreateItem{}, err
	}

	// Priority is sent whenever the item names one, unlike the five optional
	// strings (see wirePriority).
	priority, err := wirePriority(item.Issue.Priority, item.DefaultPriority)
	if err != nil {
		return apigen.BatchCreateItem{}, err
	}
	wireItem := apigen.BatchCreateItem{Title: item.Issue.Title, Priority: priority}
	setItemString(&wireItem.Description, item.Issue.Description)
	setItemString(&wireItem.Design, item.Issue.Design)
	setItemString(&wireItem.AcceptanceCriteria, item.Issue.AcceptanceCriteria)
	setItemString(&wireItem.Assignee, item.Issue.Assignee)
	setItemString(&wireItem.IssueType, string(item.Issue.IssueType))
	if len(item.Issue.Labels) > 0 {
		labels := append([]string(nil), item.Issue.Labels...)
		wireItem.Labels = &labels
	}

	if len(item.Dependencies) > 0 {
		edges := make([]apigen.BatchCreateDependency, 0, len(item.Dependencies))
		for j, dep := range item.Dependencies {
			edge, err := batchCreateEdge(index, j, dep)
			if err != nil {
				return apigen.BatchCreateItem{}, err
			}
			edges = append(edges, edge)
		}
		wireItem.Dependencies = &edges
	}
	return wireItem, nil
}

// batchCreateEdge projects one requested edge. The wire's edge carries a target
// and a type and nothing else, so the three members issueops.CreateDependency
// adds refuse.
func batchCreateEdge(index, j int, dep issueops.CreateDependency) (apigen.BatchCreateDependency, error) {
	where := func(err error) error { return fmt.Errorf("items[%d].dependencies[%d]: %w", index, j, err) }
	switch {
	case dep.TargetID == "":
		return apigen.BatchCreateDependency{}, where(invalid("target_id is required"))
	case dep.Type == "":
		return apigen.BatchCreateDependency{}, where(invalid("type is required"))
	case dep.Reverse:
		return apigen.BatchCreateDependency{}, where(refuse(encode.OpBatchCreateIssues, "W-CreateDependency.Reverse"))
	case dep.Metadata != "":
		return apigen.BatchCreateDependency{}, where(refuse(encode.OpBatchCreateIssues, "W-CreateDependency.Metadata"))
	case dep.ThreadID != "":
		return apigen.BatchCreateDependency{}, where(refuse(encode.OpBatchCreateIssues, "W-CreateDependency.ThreadID"))
	}
	return apigen.BatchCreateDependency{TargetId: dep.TargetID, Type: string(dep.Type)}, nil
}

// refuseUnwirableIssue is the other half of the allowlist: every populated
// member of the issue that is neither carried by the wire, nor ignored by the
// role, nor stamped from the actor (actorStampedCreateMember).
//
// It answers the FIRST offender in declaration order, which is deterministic —
// a caller fixing one member at a time must not see the reported member depend
// on map iteration.
func refuseUnwirableIssue(index int, issue *issueops.Issue, actor string) error {
	value := reflect.ValueOf(*issue)
	shape := value.Type()
	for i := range shape.NumField() {
		field := shape.Field(i)
		if !field.IsExported() {
			continue
		}
		if _, carried := batchCreateCarriedIssueMembers[field.Name]; carried {
			continue
		}
		if _, ignored := roleIgnoredCreateIssueMembers[field.Name]; ignored {
			continue
		}
		if value.Field(i).IsZero() || actorStampedCreateMember(field.Name, issue, actor) {
			continue
		}
		// The ledger row is the vocabulary; the member name is the fact the row
		// cannot carry, because one row covers the whole population.
		return fmt.Errorf("items[%d].Issue.%s: %w",
			index, field.Name, refuse(encode.OpBatchCreateIssues, "W-BatchCreateItem.Issue"))
	}
	return nil
}

// setItemString writes an optional wire member, leaving it absent when the role
// carried nothing. An empty string and an omitted member mean the same thing on
// every one of these five, so absent is the honest encoding.
func setItemString(dest **string, value string) {
	if value == "" {
		return
	}
	v := value
	*dest = &v
}
