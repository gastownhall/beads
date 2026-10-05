// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/accessors.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// The TWENTY-FIVE wire-backed role accessors of design D8's allowlist v1, as
// amended by the `bd close` decision that moved row 17 across, and by the
// five the client waves added after it — Counter and GraphCounter (wave 2a),
// Releaser, Commenter and IssueRelations (wave 2b). A count in prose beside a
// list in code is a count that goes stale, so read the list. They are
// off the generated refusing shell — that is what keeps them off the unsupported
// allowlist and out of RunUnsupportedContract — and hand-written here so a role
// bead flips one accessor in place, in the same PR as the method that serves it,
// without touching the generator's skip list.
//
// An accessor that has not been wired yet refuses through (*Store).unsupported,
// so it already carries the server context D7 renders, and the comment on each
// names the v0 operation that will answer it — a flip has nothing left to
// decide. A wired one returns its role bound to the store's transport; the
// implementations live one file per role, so concurrent role beads do not edit
// the same body.
//
// THREE accessors are still on the generated shell, and after client wave 2b
// they are all there for ONE reason: VersionReconciler, Bootstrapper and
// InitVerifier are PERMANENTLY UNSERVABLE — no wire operation and none coming.
//
// THE SECOND LIST IS EMPTY, and it is worth saying that it existed rather than
// deleting it silently. It held the names the wire had published ahead of this
// client — an operation complete on the server with no accessor in front of it —
// and every one of them has now left it by being WIRED: Counter (issues.count,
// #5508) and GraphCounter (dependencies.count, #5536) with client wave 2a
// (ga-icks1), then Releaser (issues.release, #5507), IssueRelations
// (issues.related, #5540) and Commenter (issues.addComment, #5594) with client
// wave 2b (ga-f352s). The next operation upstream publishes ahead of a client
// wave starts the list again, which is what it is for.
//
// "UNCONSUMED" AND "UNWIRED" ARE NOT THE SAME THING, and this comment used to
// spell them as one. A name leaves the second list when its ACCESSOR lands here;
// its entry in cmd/bd's httpUnconsumedCapabilities leaves only when a `bd`
// command reaches the role over http. Counter's went with wave 2a because
// `bd count` became served; GraphCounter's stayed, because no `bd` command
// reaches that role on ANY backend. Wave 2b splits the same way and TWO of its
// three went: Commenter's, because `bd comment` is served, and IssueRelations',
// because `bd dep list` reaches that role on every shape but the multi-anchor
// default — the command was already classified served and was failing LATE, so
// wiring the accessor turned a refusal into an answer rather than moving a row.
// Only Releaser's stayed, RESCOPED from "no accessor" to the front door it
// actually waits on.
//
// WorkspaceConfig used to be on neither list because it was PARTLY wired — its
// reads served and its two write verbs refusing — and it is now simply wired:
// #5596 published PUT and DELETE /v0/beads/config/{key} and client wave 2c
// (ga-jpywb) dials both. THE PARTIAL COLUMN IS EMPTY, for the first time since
// this file was written.
//
// READYLISTER IS NOT AN ACCESSOR HERE, and it is not a removal: this lift
// carried bd-enterprise's issueops.ReadyLister/ReadyListRequest/ReadyListing
// over from a fork where upstream had published them, and S3 reconciliation
// (gc native-program, 2026-10) found OSS has published none of the three —
// there is no role type to implement and no accessor return type to name.
// `bd ready`'s listing is unaffected; it already runs on IssueReader. S13
// schedules the role once upstream publishes it.

// IssueReader serves listReadyWork/listIssues/getIssue (D8 row 1).
func (s *Store) IssueReader() (issueops.Reader, error) {
	return httpReader{store: s}, nil
}

// ReadyCounter serves countReadyWork (D8 row 2).
func (s *Store) ReadyCounter() (issueops.ReadyCounter, error) {
	return httpReadyCounter{store: s}, nil
}

// Counter serves countIssues (GET /v0/beads/issues:count) — the TWENTY-FIRST
// wire-backed accessor, and the one `bd count` routes through.
//
// Its request maps member for member: every one of CountRequest's twenty-three
// filters is a published parameter, so nothing here refuses and no ledger row
// names a member. What the operation does NOT publish is anything the ROLE does
// not have — a count's plane vocabulary is one `include_infra` where a
// listing's is five parameters, and that narrowing is upstream's design rather
// than a wire gap. See counter.go.
func (s *Store) Counter() (issueops.Counter, error) {
	return httpCounter{store: s}, nil
}

// Releaser serves releaseIssue (POST /v0/beads/issues/{id}:release) — the
// TWENTY-THIRD wire-backed accessor, and the claim's inverse.
//
// Its request maps member for member and nothing refuses on shape. What the port
// had to carry instead is a fact about the ANSWER: a release leaves an ANONYMOUS
// post-state — assignee cleared, status open, started_at gone — which is the same
// row whoever emptied it, so the role cannot report an idempotent no-op the way
// Claimer does and every shape that would not write is a REFUSAL. Those refusals
// are the whole value of this accessor to a caller, so they travel whole rather
// than being softened into a quiet false.
//
// ONE OF THEM IS FLATTENED BY THE WIRE and is ledgered: `not_releasable` covers
// both "holds no claim" and "status will not accept a release", with no member
// telling them apart, so this client answers the wider of the two
// (L-release-notclaimed). See releaser.go.
func (s *Store) Releaser() (issueops.Releaser, error) {
	w, err := s.roleWire("Releaser")
	if err != nil {
		return nil, err
	}
	return &httpReleaser{store: s, wire: w}, nil
}

// Commenter serves addComment (POST /v0/beads/issues/{id}/comments) — the
// TWENTY-FOURTH wire-backed accessor, and the only write here whose path names a
// SUB-RESOURCE collection rather than the issue.
//
// Three request members, one of them the path, and one result member: the
// smallest total mapping on this surface. What is worth knowing is what the role
// declines rather than what it carries — there is no comment READ here, because
// a comment page is a paging question with a cursor of its own and the wire
// publishes no such operation. The thread is read through getIssue's
// `include_comments`. See commenter.go.
func (s *Store) Commenter() (issueops.Commenter, error) {
	w, err := s.roleWire("Commenter")
	if err != nil {
		return nil, err
	}
	return &httpCommenter{store: s, wire: w}, nil
}

// IssueRelations serves listRelatedIssues (GET /v0/beads/issues/{id}/related) —
// the TWENTY-FIFTH wire-backed accessor, and the first read here anchored on one
// issue in the PATH.
//
// It is the SINGLE-ANCHOR member of the graph family, which is what makes its
// miss a 404 rather than the per-anchor Missing sentinel EdgeReader and
// GraphCounter carry: one request names one id, so there is nowhere to put a
// per-anchor flag and no need for one. Both halves of the walk are two-plane and
// a wisp id is a legal anchor, all of it decided server-side.
//
// `bd dep list` IS ITS FRONT DOOR, which is easy to miss because that command
// reads as an EDGE command: the multi-anchor default shape takes the EdgeReader
// batch, and every other shape — the single anchor, and any --direction up —
// takes this role, one call per anchor. So this accessor was the difference
// between a command that was classified served and failed at dispatch, and one
// that answers. See relations.go.
func (s *Store) IssueRelations() (issueops.Relations, error) {
	return httpRelations{store: s}, nil
}

// IssueClaimer serves claimIssue (D8 row 3). See claimer.go.
func (s *Store) IssueClaimer() (issueops.Claimer, error) {
	w, err := s.roleWire("IssueClaimer")
	if err != nil {
		return nil, err
	}
	return &httpClaimer{store: s, wire: w}, nil
}

// ReadyClaimer serves claimNext (POST /v0/beads/issues:claimNext).
//
// IT ADDS NO ORDINAL, and the absence is the point: every accessor before it in
// this file counts itself as the Nth wire-backed one because it CROSSED the
// matrix, moving off the refusing shell in the wave that wired it. This one did
// not cross — it has been in wireBackedAccessors since the first commit, served
// by a composition — so the matrix stays at twenty-five and calling this the
// twenty-sixth would be counting a row that never moved. What changed is what
// stands behind the accessor, which is what the rest of this leaf is about.
//
// "The wire has no claimNext" was true when this leaf was written and stopped
// being true with upstream #5510. What the operation buys is not a round trip:
// it is the one transaction the composition could never have — selection, the
// compare-and-set and the hydration together — which is what makes an ephemeral
// row claimable, a lease actually written, and the returned cardinalities a
// description of the state the claim produced rather than of a listing taken
// before it.
//
// IT NAMES NO ID BECAUSE IT NAMES NO ROW: the caller asks a question and the
// server picks the answer. The filter travels as the QUERY STRING GET
// /v0/beads/ready's own decode reads — one predicate, one spelling — and the
// actor is the whole body.
//
// THE COMPOSITION SURVIVES as the down-level leg, capability-gated the way
// BatchCloser's is, so `bd ready --claim` keeps working against a server older
// than the operation. L14 is now that leg's row and not the role's. See
// readyclaimer.go.
func (s *Store) ReadyClaimer() (issueops.ReadyClaimer, error) {
	w, err := s.roleWire("ReadyClaimer")
	if err != nil {
		return nil, err
	}
	return &httpReadyClaimer{store: s, wire: w}, nil
}

// Querier serves queryIssues (D8 row 5).
func (s *Store) Querier() (issueops.Querier, error) {
	return httpQuerier{store: s}, nil
}

// StatsReporter serves getStats (D8 row 6). See roles_aux.go.
func (s *Store) StatsReporter() (issueops.StatsReporter, error) {
	return httpStatsReporter{store: s}, nil
}

// CycleDetector serves listDependencyCycles (D8 row 7). See roles_graph.go.
func (s *Store) CycleDetector() (issueops.CycleDetector, error) {
	return httpCycleDetector{store: s}, nil
}

// TreeWalker serves getDependencyTree (D8 row 8). See roles_graph.go.
func (s *Store) TreeWalker() (issueops.TreeWalker, error) {
	return httpTreeWalker{store: s}, nil
}

// GraphCounter serves countDependencyEdges (GET
// /v0/beads/dependencies:count) — the TWENTY-SECOND wire-backed accessor.
//
// It is the counting sibling of EdgeReader below, on the same collection and
// under the same anchor bound, plus the direction that read does not take. Its
// mapping is total: anchors, direction, edge types and the inbound-only status
// filter are all published, and the two facts it reads back — a 64-bit count and
// the Missing sentinel — are both required members, which is what keeps a typo
// from being indistinguishable from a real zero. See roles_graph.go.
func (s *Store) GraphCounter() (issueops.GraphCounter, error) {
	return httpGraphCounter{store: s}, nil
}

// EdgeReader serves listDependencies (D8 row 9). See roles_graph.go.
func (s *Store) EdgeReader() (issueops.EdgeReader, error) {
	return httpEdgeReader{store: s}, nil
}

// BlockingAnnotator serves listBlockingAnnotations (D8 row 10), which is what
// keeps `bd list`'s blocked and blocks marks working. See roles_graph.go.
func (s *Store) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return httpBlockingAnnotator{store: s}, nil
}

// WorkspaceConfig serves listSettings/getSetting/setSetting/unsetSetting (D8
// row 11, no longer partial). See roles_aux.go.
//
// Its two writes are the smallest mapping on this surface — a key in the path, a
// value in the body — and they carry no actor and no guard, because this plane
// records no history entry to attribute a write on and holds no row version to
// compare. Neither is a wire narrowing: no backend attributes a config write.
//
// WHAT THE PORT COST was a decision about the ANSWER, like every write in wave
// 2b before it. The operation PERMITS writing a credential-bearing key and then
// WITHHOLDS the value from its own answer — one projection serves the write and
// the read, so a PUT's body is the body of the GET after it — which leaves a
// successful write whose `value` member is absent and a result type with no
// spelling for "withheld". The client answers with the value it SENT, which is
// the operation's own promise rather than a guess about what redaction hid: the
// stored value equals the value sent for every key this plane accepts, because
// the one key with a normalization step is the one key the role refuses.
//
// The two EDGE BOUNDS the operation adds — a key past 255 characters on the
// write, a value past types.MaxTextBytes bytes — are the server's and are not
// restated here (L-config-bounds).
func (s *Store) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return httpWorkspaceConfig{store: s}, nil
}

// Sweeper serves sweepIssues (D8 row 12). See sweeper.go.
func (s *Store) Sweeper() (issueops.Sweeper, error) {
	w, err := s.roleWire("Sweeper")
	if err != nil {
		return nil, err
	}
	return &httpSweeper{store: s, wire: w}, nil
}

// Deleter serves deleteIssues (D8 row 13). See deleter.go.
func (s *Store) Deleter() (issueops.Deleter, error) {
	w, err := s.roleWire("Deleter")
	if err != nil {
		return nil, err
	}
	return &httpDeleter{store: s, wire: w}, nil
}

// BatchCreator serves batchCreateIssues (D8 row 14); the item members the wire
// cannot carry refuse per-item, per refuse-not-drop. See batchcreator.go.
func (s *Store) BatchCreator() (issueops.BatchCreator, error) {
	w, err := s.roleWire("BatchCreator")
	if err != nil {
		return nil, err
	}
	return &httpBatchCreator{store: s, wire: w}, nil
}

// Memories serves the four memory operations (D8 row 15). See memories.go.
func (s *Store) Memories() (memoryops.Memories, error) {
	w, err := s.roleWire("Memories")
	if err != nil {
		return nil, err
	}
	return &httpMemories{store: s, wire: w}, nil
}

// IssueLifecycle serves createIssue/updateIssue/closeIssue/reopenIssue (D8 row
// 16). It was PARTIAL until the create landed — POST /v0/beads/issues was
// deliberately left free on the wire, so there was nothing to dial — and
// upstream #5483 filled it. See lifecycle.go.
func (s *Store) IssueLifecycle() (issueops.Lifecycle, error) {
	w, err := s.roleWire("IssueLifecycle")
	if err != nil {
		return nil, err
	}
	return &httpLifecycle{store: s, wire: w}, nil
}

// DependencyEditor serves addDependencies/removeDependency (D8 row 18). See
// dependencyeditor.go.
func (s *Store) DependencyEditor() (issueops.DependencyEditor, error) {
	w, err := s.roleWire("DependencyEditor")
	if err != nil {
		return nil, err
	}
	return &httpDependencyEditor{store: s, wire: w}, nil
}

// BatchCloser serves the single-item, no-ClaimNext shape of CloseBatch by
// composing onto closeIssue, and refuses every other shape (the `bd close`
// decision, amending D8 row 17's PENDING-DECISION). See batchcloser.go.
//
// It is the EIGHTEENTH wire-backed accessor: the design's matrix recorded
// seventeen because row 17 was undecided when it was written, and the decision
// moved this one across.
func (s *Store) BatchCloser() (issueops.BatchCloser, error) {
	w, err := s.roleWire("BatchCloser")
	if err != nil {
		return nil, err
	}
	return &httpBatchCloser{store: s, wire: w}, nil
}

// MetadataCAS serves compareAndSetMetadata (POST
// /v0/beads/issues/{id}:casMetadata) — the NINETEENTH wire-backed accessor.
//
// The operation's contract was the part that needed wiring rather than the
// transport, and it is the one place this seam's usual rule does not hold: a
// lost compare-and-set is a 200 carrying `swapped: false` and the current
// value, so the client reads the body of a SUCCESS to learn the swap did not
// happen, where every other write here learns that from a status code. See
// metadatacas.go.
func (s *Store) MetadataCAS() (issueops.MetadataCAS, error) {
	w, err := s.roleWire("MetadataCAS")
	if err != nil {
		return nil, err
	}
	return &httpMetadataCAS{store: s, wire: w}, nil
}

// BatchApplier serves applyBatch (POST /v0/beads/issues:batchApply) — the
// TWENTIETH wire-backed accessor, and the widest write on the surface: one
// request carrying creates, updates, closes and edges that either all land or
// none do.
//
// Its key-binding half — a create item names a key that later items reference,
// and the response maps every key to the id it was bound to — has no analog in
// any other role here, and neither does the shape it forced on the refusal
// path: an all-or-nothing operation publishes no per-item outcomes, so the
// offender travels in the problem document's item_* members and this role
// rebuilds *issueops.ItemError and *issueops.RefError from them. See
// batchapplier.go.
func (s *Store) BatchApplier() (issueops.BatchApplier, error) {
	w, err := s.roleWire("BatchApplier")
	if err != nil {
		return nil, err
	}
	return &httpBatchApplier{store: s, wire: w}, nil
}
