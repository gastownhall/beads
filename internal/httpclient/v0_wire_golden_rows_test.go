package httpclient

import (
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

const (
	v0WireActor = "golden-actor"
	v0WireTS    = "2026-01-02T03:04:05Z"
	v0WireIssue = `{"id":"bd-1","title":"golden","status":"open","priority":2,"issue_type":"task","created_at":"` + v0WireTS + `","updated_at":"` + v0WireTS + `"}`
	// v0WireOneRow is a one-row page for the reads whose follow-up request
	// depends on what came back.
	v0WireOneRow = `{"items":[` + v0WireIssue + `],"has_more":false}`
)

var v0WireTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func v0WirePtr[T any](v T) *T { return &v }

func v0WireJSON(s string) *json.RawMessage {
	m := json.RawMessage(s)
	return &m
}

// v0WireAnswers is the minimal 2xx body the recorder sends for each operation
// (getContext is v0WireContext's; watchEvents is an empty event stream).
var v0WireAnswers = map[string]string{
	"health":                  `{"status":"ok"}`,
	"getIssue":                `{"id":"bd-1","title":"golden","status":"open","priority":2,"issue_type":"task","created_at":"` + v0WireTS + `","updated_at":"` + v0WireTS + `","revision":"7"}`,
	"listReadyWork":           `{"items":[],"has_more":false}`,
	"countReadyWork":          `{"total":3}`,
	"listIssues":              `{"items":[],"has_more":false}`,
	"queryIssues":             `{"items":[],"has_more":false}`,
	"countIssues":             `{"total":1,"groups":{"open":1}}`,
	"getStats":                `{"summary":{}}`,
	"listBlockedIssues":       `{"items":[],"has_more":false}`,
	"listDependencyCycles":    `{"items":[]}`,
	"getDependencyTree":       `{"items":[]}`,
	"listDependencies":        `{"items":[],"missing":[]}`,
	"countDependencyEdges":    `{"anchors":[{"id":"bd-1","count":0,"missing":false}]}`,
	"listBlockingAnnotations": `{"items":[]}`,
	"listRelatedIssues":       `{"items":[]}`,
	"listSettings":            `{"items":[]}`,
	"getSetting":              `{"key":"golden.key","value":"v","redacted":false}`,
	"setSetting":              `{"key":"golden.key","value":"v","redacted":false}`,
	"unsetSetting":            `{"key":"golden.key"}`,
	"listEvents":              `{"records":[],"head":0}`,
	"batchGetIssues":          `{"issues":[],"missing":["bd-1"]}`,
	"reclaimIssues":           `{"reclaimed":[]}`,
	"heartbeatIssue":          `{"id":"bd-1","holder":"golden-actor","heartbeat_at":"` + v0WireTS + `","lease_expires_at":"` + v0WireTS + `","rearmed":false}`,
	"advanceMolecule":         `{"closed_step":` + v0WireIssue + `,"complete":true}`,
	"claimIssue":              `{"issue":` + v0WireIssue + `,"already_claimed":false}`,
	"claimNextIssue":          `{"claimed":null}`,
	"closeIssue":              `{"issue":` + v0WireIssue + `,"revision":"8","already_closed":false}`,
	"reopenIssue":             `{"issue":` + v0WireIssue + `,"revision":"8","already_open":false}`,
	"updateIssue":             `{"issue":` + v0WireIssue + `,"revision":"8","changed":true}`,
	"releaseIssue":            `{"issue":` + v0WireIssue + `,"revision":"8","changed":true}`,
	"createIssue":             v0WireIssue,
	"addComment":              `{"id":1,"issue_id":"bd-1","author":"golden-actor","text":"hello","created_at":"` + v0WireTS + `"}`,
	"compareAndSetMetadata":   `{"swapped":true,"current":{"n":1}}`,
	"addDependencies":         `{"added":[]}`,
	"removeDependency":        `{"removed":true}`,
	"sweepIssues":             `{"dry_run":true,"swept":0,"dependencies":0,"labels":0,"events":0,"skipped":{"pinned":0,"referenced":0,"not_closed":0,"unknown_closed_at":0,"closed_at_or_after_cutoff":0,"unreadable":0}}`,
	"deleteIssues":            `{"dry_run":true,"deleted":0,"dependencies":0,"labels":0,"events":0,"references_updated":0}`,
	"batchCreateIssues":       `{"items":[` + v0WireIssue + `]}`,
	"batchCloseIssues":        `{"outcomes":[{"issue_id":"bd-1","issue":` + v0WireIssue + `}]}`,
	"applyBatch":              `{"items":[{"kind":"create","issue_id":"bd-1","changed":true,"revision":"1"}],"keys":{"k1":"bd-1"}}`,
	"rememberMemory":          `{"key":"golden-key","value":"remember me","replaced":false}`,
	"getMemory":               `{"key":"golden-key","value":"remember me"}`,
	"forgetMemory":            `{"key":"golden-key","value":"remember me"}`,
	"listMemories":            `{"items":[],"has_more":false}`,
}

func v0WireRows() []v0WireRow {
	reader := func(c v0WireCall) issueops.Reader { r := v0WireMust(c.store.IssueReader()); return r }
	lifecycle := func(c v0WireCall) issueops.Lifecycle { l := v0WireMust(c.store.IssueLifecycle()); return l }
	memories := func(c v0WireCall) memoryops.Memories { m := v0WireMust(c.store.Memories()); return m }
	return []v0WireRow{
		// No Store role reaches these two; the wire client is the door.
		{name: "health", call: func(c v0WireCall) error { return c.conn.Health(c.ctx) }},
		{name: "watchEvents", call: func(c v0WireCall) error {
			s, err := c.conn.WatchEvents(c.ctx, 42)
			if err == nil {
				s.Close()
			}
			return err
		}},
		// The first Ping is the handshake; the second re-reads the context.
		{name: "getContext.ping", call: func(c v0WireCall) error {
			if err := c.store.Ping(c.ctx); err != nil {
				return err
			}
			return c.store.Ping(c.ctx)
		}},

		// Reads.
		{name: "getIssue", call: func(c v0WireCall) error {
			_, err := reader(c).Get(c.ctx, issueops.GetRequest{ID: "bd-1", IncludeComments: true, IncludeDependents: true})
			return err
		}},
		{name: "getIssue.escaped-id", call: func(c v0WireCall) error {
			_, err := reader(c).Get(c.ctx, issueops.GetRequest{ID: "bd:1/x y"})
			return err
		}},
		{name: "getIssue.store-get-issue", call: func(c v0WireCall) error {
			_, err := c.store.GetIssue(c.ctx, "bd-1")
			return err
		}},
		{name: "getIssue.store-comments", call: func(c v0WireCall) error {
			_, err := c.store.GetIssueComments(c.ctx, "bd-1")
			return err
		}},
		{name: "getIssue.store-exact-ids", call: func(c v0WireCall) error {
			_, err := c.store.SearchIssues(c.ctx, "", types.IssueFilter{IDs: []string{"bd-1"}})
			return err
		}},
		{name: "listReadyWork", call: func(c v0WireCall) error {
			_, err := reader(c).Ready(c.ctx, issueops.ReadyRequest{Assignee: "alice", Labels: []string{"b", "a"}, Priority: v0WirePtr(1), IssueType: "bug", Limit: v0WirePtr(5)})
			return err
		}},
		{name: "listReadyWork.store-ready-work", call: func(c v0WireCall) error {
			_, err := c.store.GetReadyWork(c.ctx, types.WorkFilter{Status: types.StatusOpen, Limit: 5})
			return err
		}},
		{name: "countReadyWork.store-ready-total", answers: map[string]string{"listReadyWork": `{"items":[` + v0WireIssue + `],"has_more":true}`},
			call: func(c v0WireCall) error {
				_, _, err := c.store.GetReadyWorkWithCountsAndTotal(c.ctx, types.WorkFilter{Status: types.StatusOpen, Limit: 1})
				return err
			}},
		{name: "countReadyWork", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.ReadyCounter())
			_, err := r.CountReady(c.ctx, issueops.ReadyRequest{Assignee: "alice", Labels: []string{"a"}})
			return err
		}},
		{name: "listIssues", call: func(c v0WireCall) error {
			_, err := reader(c).List(c.ctx, issueops.ListRequest{Status: "open", Labels: []string{"x"}, Assignee: "alice", Limit: v0WirePtr(10)})
			return err
		}},
		// A negative limit is the server's to refuse: the pager sends the
		// encoder's params as they are, with no sort.
		{name: "listIssues.negative-limit", call: func(c v0WireCall) error {
			_, err := reader(c).List(c.ctx, issueops.ListRequest{Status: "open", Limit: v0WirePtr(-1)})
			return err
		}},
		{name: "queryIssues", call: func(c v0WireCall) error {
			q := v0WireMust(c.store.Querier())
			_, err := q.Query(c.ctx, issueops.QueryRequest{Expression: "type=bug AND priority<2", IncludeClosed: true, Limit: v0WirePtr(20)})
			return err
		}},
		{name: "countIssues", call: func(c v0WireCall) error {
			n := v0WireMust(c.store.Counter())
			_, err := n.Count(c.ctx, issueops.CountRequest{Status: "open", IssueType: "bug", Labels: []string{"a"}})
			return err
		}},
		{name: "countIssues.group-by", call: func(c v0WireCall) error {
			n := v0WireMust(c.store.Counter())
			_, err := n.CountByGroup(c.ctx, issueops.CountByGroupRequest{Filter: issueops.CountRequest{Status: "open"}, GroupBy: issueops.CountGroupStatus})
			return err
		}},
		{name: "getStats", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.StatsReporter())
			_, err := r.Stats(c.ctx, issueops.StatsRequest{SkipBlocked: true})
			return err
		}},
		{name: "listBlockedIssues", call: func(c v0WireCall) error {
			l := v0WireMust(c.store.BlockedLister())
			_, err := l.ListBlocked(c.ctx, issueops.BlockedRequest{ParentID: "bd-1", Labels: []string{"a"}})
			return err
		}},
		{name: "listDependencyCycles", call: func(c v0WireCall) error {
			d := v0WireMust(c.store.CycleDetector())
			_, err := d.DetectCycles(c.ctx, issueops.DetectCyclesRequest{})
			return err
		}},
		{name: "getDependencyTree", call: func(c v0WireCall) error {
			w := v0WireMust(c.store.TreeWalker())
			_, err := w.WalkTree(c.ctx, issueops.WalkTreeRequest{RootID: "bd-1", MaxDepth: 3, Direction: "down", Status: "open"})
			return err
		}},
		{name: "listDependencies", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.EdgeReader())
			_, err := r.ReadEdges(c.ctx, issueops.EdgeReadRequest{IDs: []string{"bd-1", "bd-2"}, Types: []issueops.DependencyType{"blocks"}})
			return err
		}},
		{name: "listDependencies.store-records", call: func(c v0WireCall) error {
			_, err := c.store.GetDependencyRecords(c.ctx, "bd-1")
			return err
		}},
		{name: "listDependencies.store-all-records", answers: map[string]string{"listIssues": v0WireOneRow},
			call: func(c v0WireCall) error {
				_, err := c.store.GetAllDependencyRecords(c.ctx)
				return err
			}},
		{name: "countDependencyEdges", call: func(c v0WireCall) error {
			g := v0WireMust(c.store.GraphCounter())
			_, err := g.CountEdges(c.ctx, issueops.EdgeCountRequest{IDs: []string{"bd-1"}, Direction: "in", Types: []issueops.DependencyType{"blocks"}, Status: "open"})
			return err
		}},
		{name: "listBlockingAnnotations", call: func(c v0WireCall) error {
			a := v0WireMust(c.store.BlockingAnnotator())
			_, err := a.AnnotateBlocking(c.ctx, issueops.BlockingRequest{IDs: []string{"bd-1"}, IncludeIsBlocked: true})
			return err
		}},
		{name: "listRelatedIssues", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.IssueRelations())
			_, err := r.Related(c.ctx, issueops.RelatedRequest{ID: "bd-1", Direction: issueops.RelationOut, Types: []issueops.DependencyType{"related"}})
			return err
		}},
		{name: "batchGetIssues", call: func(c v0WireCall) error {
			g := v0WireMust(c.store.BatchGetter())
			_, err := g.GetMany(c.ctx, issueops.GetManyRequest{IDs: []string{"bd-1"}})
			return err
		}},
		{name: "listEvents", call: func(c v0WireCall) error {
			_, err := c.store.ReadEventsJournalPage(c.ctx, 5, 100)
			return err
		}},

		// Settings.
		{name: "getSetting", call: func(c v0WireCall) error {
			_, err := c.store.GetConfig(c.ctx, "golden.key")
			return err
		}},
		{name: "listSettings", call: func(c v0WireCall) error {
			w := v0WireMust(c.store.WorkspaceConfig())
			_, err := w.ListSettings(c.ctx, issueops.ListSettingsRequest{})
			return err
		}},
		{name: "setSetting", call: func(c v0WireCall) error {
			w := v0WireMust(c.store.WorkspaceConfig())
			_, err := w.SetSetting(c.ctx, issueops.SetSettingRequest{Key: "golden.key", Value: "v"})
			return err
		}},
		{name: "unsetSetting", call: func(c v0WireCall) error {
			w := v0WireMust(c.store.WorkspaceConfig())
			_, err := w.UnsetSetting(c.ctx, issueops.UnsetSettingRequest{Key: "golden.key"})
			return err
		}},

		// Leases.
		{name: "claimIssue", call: func(c v0WireCall) error {
			l := v0WireMust(c.store.IssueClaimer())
			_, err := l.Claim(c.ctx, issueops.ClaimRequest{Actor: v0WireActor, IssueID: "bd-1"})
			return err
		}},
		{name: "claimIssue.escaped-id", call: func(c v0WireCall) error {
			l := v0WireMust(c.store.IssueClaimer())
			_, err := l.Claim(c.ctx, issueops.ClaimRequest{Actor: v0WireActor, IssueID: ".."})
			return err
		}},
		{name: "claimNextIssue", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.ReadyClaimer())
			_, err := r.ClaimNext(c.ctx, issueops.ClaimNextRequest{Actor: v0WireActor, Filter: issueops.ReadyRequest{Labels: []string{"a"}}})
			return err
		}},
		// A server without issues.claimNext: the client composes the claim.
		{name: "claimIssue.composed-claim-next", withoutCaps: []string{"issues.claimNext"},
			answers: map[string]string{"listReadyWork": v0WireOneRow},
			call: func(c v0WireCall) error {
				r := v0WireMust(c.store.ReadyClaimer())
				_, err := r.ClaimNext(c.ctx, issueops.ClaimNextRequest{Actor: v0WireActor, Filter: issueops.ReadyRequest{Labels: []string{"a"}}})
				return err
			}},
		{name: "releaseIssue", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.Releaser())
			_, err := r.Release(c.ctx, issueops.ReleaseRequest{Actor: v0WireActor, IssueID: "bd-1", ExpectedAssignee: v0WirePtr("alice")})
			return err
		}},
		{name: "heartbeatIssue", call: func(c v0WireCall) error {
			h := v0WireMust(c.store.Heartbeater())
			_, err := h.Heartbeat(c.ctx, issueops.HeartbeatRequest{Actor: v0WireActor, IssueID: "bd-1"})
			return err
		}},
		{name: "reclaimIssues", call: func(c v0WireCall) error {
			r := v0WireMust(c.store.LeaseReclaimer())
			_, err := r.Reclaim(c.ctx, issueops.ReclaimRequest{Actor: v0WireActor, OlderThan: 90 * time.Second, Filter: issueops.ReclaimFilter{IDs: []string{"bd-1"}}})
			return err
		}},

		// Lifecycle.
		{name: "createIssue", call: func(c v0WireCall) error {
			_, err := lifecycle(c).Create(c.ctx, issueops.CreateRequest{Actor: v0WireActor,
				Issue: &issueops.Issue{Title: "golden", Description: "d", Priority: 1, IssueType: "bug", Labels: []string{"a"}}})
			return err
		}},
		{name: "createIssue.default-priority", call: func(c v0WireCall) error {
			_, err := lifecycle(c).Create(c.ctx, issueops.CreateRequest{Actor: v0WireActor, Issue: &issueops.Issue{Title: "golden"}, DefaultPriority: true})
			return err
		}},
		{name: "updateIssue", call: func(c v0WireCall) error {
			_, err := lifecycle(c).Update(c.ctx, issueops.UpdateRequest{Actor: v0WireActor, IssueID: "bd-1",
				Patch: issueops.IssuePatch{
					Title:    issueops.Field[string]{Set: true, Value: "new"},
					Priority: issueops.Field[int]{Set: true, Value: 1},
				},
				ExpectedVersion: v0WirePtr(int64(7))})
			return err
		}},
		{name: "updateIssue.claim", call: func(c v0WireCall) error {
			_, err := lifecycle(c).Update(c.ctx, issueops.UpdateRequest{Actor: v0WireActor, IssueID: "bd-1", Claim: true})
			return err
		}},
		// A server without issues.update.claim: a claim-only update becomes
		// the template pre-read, claimIssue and the hydrating re-read.
		{name: "claimIssue.update-claim-legacy", withoutCaps: []string{"issues.update.claim"}, call: func(c v0WireCall) error {
			_, err := lifecycle(c).Update(c.ctx, issueops.UpdateRequest{Actor: v0WireActor, IssueID: "bd-1", Claim: true})
			return err
		}},
		{name: "closeIssue", call: func(c v0WireCall) error {
			_, err := lifecycle(c).Close(c.ctx, issueops.CloseRequest{Actor: v0WireActor, IssueID: "bd-1", Reason: "done", ExpectedVersion: v0WirePtr(int64(7))})
			return err
		}},
		{name: "reopenIssue", call: func(c v0WireCall) error {
			_, err := lifecycle(c).Reopen(c.ctx, issueops.ReopenRequest{Actor: v0WireActor, IssueID: "bd-1", Reason: "again"})
			return err
		}},
		{name: "advanceMolecule", call: func(c v0WireCall) error {
			m := v0WireMust(c.store.MoleculeStepper())
			_, err := m.Advance(c.ctx, issueops.AdvanceRequest{Actor: v0WireActor, ClosedStepID: "bd-1", AutoClaim: true})
			return err
		}},
		{name: "addComment", call: func(c v0WireCall) error {
			m := v0WireMust(c.store.Commenter())
			_, err := m.AddComment(c.ctx, issueops.AddCommentRequest{Author: v0WireActor, IssueID: "bd-1", Text: "hello"})
			return err
		}},
		{name: "compareAndSetMetadata", call: func(c v0WireCall) error {
			m := v0WireMust(c.store.MetadataCAS())
			_, err := m.CompareAndSetKey(c.ctx, issueops.CompareAndSetKeyRequest{Actor: v0WireActor, IssueID: "bd-1", Key: "k",
				Expected: v0WireJSON(`null`), Value: v0WireJSON(`{"n":1}`)})
			return err
		}},
		{name: "addDependencies", call: func(c v0WireCall) error {
			d := v0WireMust(c.store.DependencyEditor())
			_, err := d.AddDependencies(c.ctx, issueops.AddDependenciesRequest{Actor: v0WireActor,
				Edges: []issueops.DependencyEdge{{IssueID: "bd-1", DependsOnID: "bd-2", Type: "blocks"}}})
			return err
		}},
		{name: "removeDependency", call: func(c v0WireCall) error {
			d := v0WireMust(c.store.DependencyEditor())
			_, err := d.RemoveDependency(c.ctx, issueops.RemoveDependencyRequest{Actor: v0WireActor, IssueID: "bd-1", DependsOnID: "bd-2"})
			return err
		}},

		// Bulk.
		{name: "sweepIssues", call: func(c v0WireCall) error {
			s := v0WireMust(c.store.Sweeper())
			_, err := s.Sweep(c.ctx, issueops.SweepRequest{Actor: v0WireActor, Tier: "ephemeral", ClosedBefore: &v0WireTime, DryRun: true})
			return err
		}},
		{name: "deleteIssues", call: func(c v0WireCall) error {
			d := v0WireMust(c.store.Deleter())
			_, err := d.Delete(c.ctx, issueops.DeleteRequest{Actor: v0WireActor, IDs: []string{"bd-1"}, DryRun: true})
			return err
		}},
		{name: "batchCreateIssues", call: func(c v0WireCall) error {
			b := v0WireMust(c.store.BatchCreator())
			_, err := b.CreateBatch(c.ctx, issueops.CreateBatchRequest{Actor: v0WireActor,
				Items: []issueops.BatchCreateItem{{Issue: &issueops.Issue{Title: "golden", Priority: 2}}}})
			return err
		}},
		{name: "batchCloseIssues", call: func(c v0WireCall) error {
			b := v0WireMust(c.store.BatchCloser())
			_, err := b.CloseBatch(c.ctx, issueops.CloseBatchRequest{Actor: v0WireActor, Items: []issueops.BatchCloseItem{{IssueID: "bd-1", Reason: "done"}}})
			return err
		}},
		{name: "applyBatch", call: func(c v0WireCall) error {
			b := v0WireMust(c.store.BatchApplier())
			_, err := b.ApplyBatch(c.ctx, issueops.ApplyBatchRequest{Actor: v0WireActor, Items: []issueops.ApplyItem{{
				Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Key: "k1", Issue: &issueops.Issue{Title: "golden", Priority: 2}},
			}}})
			return err
		}},

		// Memories.
		{name: "rememberMemory", call: func(c v0WireCall) error {
			_, err := memories(c).Remember(c.ctx, memoryops.RememberRequest{Key: "golden-key", Content: "remember me"})
			return err
		}},
		{name: "getMemory", call: func(c v0WireCall) error {
			_, err := memories(c).Recall(c.ctx, memoryops.RecallRequest{Key: "golden key/with:colon"})
			return err
		}},
		{name: "forgetMemory", call: func(c v0WireCall) error {
			_, err := memories(c).Forget(c.ctx, memoryops.ForgetRequest{Key: "golden-key"})
			return err
		}},
		{name: "listMemories", call: func(c v0WireCall) error {
			_, err := memories(c).List(c.ctx, memoryops.ListRequest{Search: "gold"})
			return err
		}},
	}
}
