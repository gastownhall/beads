package db

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// journalRow is one decoded bd_events_journal row.
type journalRow struct {
	Seq        int64
	Op         string
	IssueID    string
	Actor      string
	Issue      *types.Issue
	Dep        *issueops.EventDep
	Comment    *issueops.EventComment
	HasIssue   bool
	HasComment bool
}

// enableJournalForTest turns the journal on, clears any rows left by earlier
// tests (the table is dolt-ignored, so DOLT_RESET does not clear it), and turns
// it back off on cleanup.
func (s *testSuite) enableJournalForTest() {
	s.journalEnabled = true
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM bd_events_journal")
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.journalEnabled = false })
}

// clearJournal drops the rows written so far, so an assertion reads only what
// the mutation under test produced.
func (s *testSuite) clearJournal() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM bd_events_journal")
	s.Require().NoError(err)
}

func (s *testSuite) readJournal() []journalRow {
	rows, err := s.Runner().QueryContext(s.Ctx(),
		`SELECT seq, op, issue_id, actor, issue_json, dep_json, comment_json FROM bd_events_journal ORDER BY seq ASC`)
	s.Require().NoError(err)
	defer rows.Close()

	var out []journalRow
	for rows.Next() {
		var (
			jr        journalRow
			issueJS   []byte
			depJS     []byte
			commentJS []byte
		)
		s.Require().NoError(rows.Scan(&jr.Seq, &jr.Op, &jr.IssueID, &jr.Actor, &issueJS, &depJS, &commentJS))
		if len(issueJS) > 0 {
			jr.HasIssue = true
			var iss types.Issue
			s.Require().NoError(json.Unmarshal(issueJS, &iss))
			jr.Issue = &iss
		}
		if len(depJS) > 0 {
			var d issueops.EventDep
			s.Require().NoError(json.Unmarshal(depJS, &d))
			jr.Dep = &d
		}
		if len(commentJS) > 0 {
			jr.HasComment = true
			var c issueops.EventComment
			s.Require().NoError(json.Unmarshal(commentJS, &c))
			jr.Comment = &c
		}
		out = append(out, jr)
	}
	s.Require().NoError(rows.Err())
	return out
}

// TestEventsJournal_UOWPlumbing drives every op kind through the unit-of-work
// repository write path (which reimplements create/update/claim/delete/dep/label
// and delegates close/reopen to issueops) against real Dolt, and asserts the
// journal records each op with an engine-assigned monotonic seq.
func (s *testSuite) TestEventsJournal_UOWPlumbing() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()
	lr := s.labelRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-mj-1", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-mj-2", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Update(ctx, "bd-mj-1", map[string]any{"title": "renamed"}, "actor", domain.IssueTableOpts{}))
	s.Require().NoError(lr.Insert(ctx, "bd-mj-1", "urgent", "actor", domain.LabelOpts{}))
	_, err := ir.Claim(ctx, "bd-mj-1", "worker", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{IssueID: "bd-mj-1", DependsOnID: "bd-mj-2", Type: types.DepBlocks}, "actor", domain.DepInsertOpts{}))
	_, err = dr.Delete(ctx, "bd-mj-1", "bd-mj-2", "actor", domain.DepInsertOpts{})
	s.Require().NoError(err)
	_, err = ir.Close(ctx, "bd-mj-1", domain.CloseRowParams{Reason: "done"}, "actor", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(ir.Delete(ctx, "bd-mj-2", domain.IssueTableOpts{}, "actor"))

	got := s.readJournal()
	wantOps := []string{
		"create", "create", "update", "update", "update", "dep_add",
		"update", // derived is_blocked flip after dependency removal
		"dep_remove", "close", "delete",
	}
	s.Require().Len(got, len(wantOps), "journal rows: %+v", got)

	var prev int64
	for i, want := range wantOps {
		s.Equalf(want, got[i].Op, "row %d op", i)
		s.Greaterf(got[i].Seq, prev, "row %d seq must be strictly increasing", i)
		prev = got[i].Seq
	}
	// Update snapshot reflects the post-mutation title.
	s.Require().True(got[2].HasIssue)
	s.Equal("renamed", got[2].Issue.Title)
	// dep_add carries the edge details.
	s.Require().NotNil(got[5].Dep)
	s.Equal(string(types.DepBlocks), got[5].Dep.Kind)
	s.Equal("bd-mj-2", got[5].Dep.Target)
	// Dependency removal changes the persisted readiness projection before its
	// edge delta, so cursor consumers receive the derived source update too.
	s.Require().True(got[6].HasIssue)
	s.False(got[6].Issue.IsBlocked)
	// dep_remove carries the edge details.
	s.Require().NotNil(got[7].Dep)
	s.Equal("bd-mj-2", got[7].Dep.Target)
	// delete carries a null issue payload.
	s.Equal("delete", got[9].Op)
	s.False(got[9].HasIssue, "delete row must have null issue")
}

// TestEventsJournal_CommentPayload asserts a structured comment write lands as
// the engine-only comment op with a replayable payload, so a consumer can
// reproduce the comment without re-reading the database.
func (s *testSuite) TestEventsJournal_CommentPayload() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-cmt-1", "t"), "actor", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	stored, err := s.commentRepo().Insert(ctx, "bd-cmt-1", "alice", "look at this", domain.CommentOpts{})
	s.Require().NoError(err)

	got := s.readJournal()
	s.Require().Len(got, 1, "one comment row: %+v", got)
	s.Equal("comment", got[0].Op)
	s.Equal("bd-cmt-1", got[0].IssueID)
	s.Require().True(got[0].HasComment, "comment op must carry a payload")
	s.Equal(stored.ID, got[0].Comment.ID)
	s.Equal("alice", got[0].Comment.Author)
	s.Equal("look at this", got[0].Comment.Text)
	s.Equal("structured", got[0].Comment.Source)
	s.True(got[0].HasIssue, "comment op still snapshots the bead")
}

// TestEventsJournal_CascadeDelete asserts a cascade delete journals every
// affected bead — the finding the decorator design could not cover.
func (s *testSuite) TestEventsJournal_CascadeDelete() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-cd-parent", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-cd-child", "t"), "actor", domain.InsertIssueOpts{}))
	// child depends on parent via parent-child, so deleting the parent cascades.
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{IssueID: "bd-cd-child", DependsOnID: "bd-cd-parent", Type: types.DepParentChild}, "actor", domain.DepInsertOpts{}))

	// clear the setup rows so we assert only on the cascade delete.
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	uc := s.issueUseCase()
	res, err := uc.DeleteIssues(ctx, domain.DeleteIssuesParams{IDs: []string{"bd-cd-parent"}, Cascade: true}, "actor")
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(res.DeletedCount, 2)

	deleted := map[string]bool{}
	for _, r := range s.readJournal() {
		if r.Op == "delete" {
			deleted[r.IssueID] = true
		}
	}
	s.True(deleted["bd-cd-parent"], "parent delete must be journaled")
	s.True(deleted["bd-cd-child"], "cascade-deleted child must be journaled, got %v", deleted)
}

// TestEventsJournal_DeleteRelationshipDeltas proves a cursor consumer sees
// every edge removal before the nodes disappear, including both incoming and
// outgoing relationships in a multi-node UOW delete.
func (s *testSuite) TestEventsJournal_DeleteRelationshipDeltas() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()

	for _, id := range []string{"bd-del-center", "bd-del-in", "bd-del-out"} {
		s.Require().NoError(ir.Insert(ctx, newTestIssue(id, "t"), "actor", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-del-center", DependsOnID: "bd-del-out", Type: types.DepBlocks,
	}, "actor", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-del-in", DependsOnID: "bd-del-center", Type: types.DepRelated,
	}, "actor", domain.DepInsertOpts{}))

	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	_, err = s.issueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
		IDs: []string{"bd-del-center", "bd-del-in", "bd-del-out"},
	}, "actor")
	s.Require().NoError(err)

	got := s.readJournal()
	var depRemoves, deletes int
	for i, r := range got {
		switch r.Op {
		case "dep_remove":
			depRemoves++
			s.Equal(0, deletes, "row %d: every edge delta must precede the node deletes", i)
		case "delete":
			deletes++
		default:
			s.Failf("unexpected op", "row %d op %q: %+v", i, r.Op, r)
		}
	}
	s.Equal(2, depRemoves, "both edges must be journaled: %+v", got)
	s.Equal(3, deletes, "all three nodes must be journaled: %+v", got)
	s.Require().NotNil(got[0].Dep)
	s.Require().NotNil(got[1].Dep)
}

// TestEventsJournal_NoPhantomDeletes asserts DeleteByIDs journals a delete
// only for ids that actually removed a row — never a phantom delete for an id
// that matched nothing (the batched DELETE reports only a per-batch total).
func (s *testSuite) TestEventsJournal_NoPhantomDeletes() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-pd-1", "t"), "actor", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-pd-2", "t"), "actor", domain.InsertIssueOpts{}))
	// clear setup rows so we assert only on the delete.
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	// Delete a mix: two real ids and two that do not exist.
	n, err := ir.DeleteByIDs(ctx, []string{"bd-pd-1", "bd-pd-missing-a", "bd-pd-2", "bd-pd-missing-b"}, domain.IssueTableOpts{}, "actor")
	s.Require().NoError(err)
	s.Equal(2, n, "only the two present ids are deleted")

	deleted := map[string]bool{}
	for _, r := range s.readJournal() {
		s.Equal("delete", r.Op, "only delete rows expected: %+v", r)
		deleted[r.IssueID] = true
	}
	s.Equal(map[string]bool{"bd-pd-1": true, "bd-pd-2": true}, deleted,
		"journal must record a delete only for ids that removed a row, no phantoms")
}

// TestEventsJournal_DeleteActorAttribution pins the repository seam the UOW
// door bottoms out in: IssueSQLRepository.Delete / DeleteByIDs and
// DependencySQLRepository.DeleteAllForIDs each journal under the actor they are
// handed, not the empty string (#5985).
//
// It asserts at the repository rather than through the use case because that is
// where the actor was being dropped — deleteMany had it in hand the whole time.
// The empty-actor half of the contract stays pinned by the derived-maintenance
// rows in TestEventsJournal_UOWPlumbing.
func (s *testSuite) TestEventsJournal_DeleteActorAttribution() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()

	for _, id := range []string{"bd-da-single", "bd-da-bulk-1", "bd-da-bulk-2", "bd-da-edge-src", "bd-da-edge-tgt"} {
		s.Require().NoError(ir.Insert(ctx, newTestIssue(id, "t"), "creator", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-da-edge-src", DependsOnID: "bd-da-edge-tgt", Type: types.DepBlocks,
	}, "linker", domain.DepInsertOpts{}))

	actorOf := func(op, id string) string {
		s.T().Helper()
		for _, r := range s.readJournal() {
			if r.Op == op && r.IssueID == id {
				return r.Actor
			}
		}
		s.Failf("no journal row", "op %q for %q", op, id)
		return ""
	}

	// The bulk edge delete: its dep_remove row belongs to the delete that caused
	// it, journaled under the edge's source.
	s.clearJournal()
	_, err := dr.DeleteAllForIDs(ctx, []string{"bd-da-edge-tgt"}, domain.DepInsertOpts{}, "edge-remover")
	s.Require().NoError(err)
	s.Equal("edge-remover", actorOf("dep_remove", "bd-da-edge-src"))

	s.clearJournal()
	s.Require().NoError(ir.Delete(ctx, "bd-da-single", domain.IssueTableOpts{}, "single-deleter"))
	s.Equal("single-deleter", actorOf("delete", "bd-da-single"))

	s.clearJournal()
	n, err := ir.DeleteByIDs(ctx, []string{"bd-da-bulk-1", "bd-da-bulk-2"}, domain.IssueTableOpts{}, "bulk-deleter")
	s.Require().NoError(err)
	s.Equal(2, n)
	s.Equal("bulk-deleter", actorOf("delete", "bd-da-bulk-1"))
	s.Equal("bulk-deleter", actorOf("delete", "bd-da-bulk-2"))
}

// TestEventsJournal_CascadeDeleteAttribution is the same promise one level up,
// where a user's `bd delete --cascade` actually enters: every row a cascade
// produces — the named bead, the beads the cascade reached, and the edges that
// went with them — carries the REQUESTING actor. A consumer resolving a delete
// conflict reads this column, and an empty one there would read as a system
// write it must not overrule.
func (s *testSuite) TestEventsJournal_CascadeDeleteAttribution() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()

	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-cda-parent", "t"), "creator", domain.InsertIssueOpts{}))
	s.Require().NoError(ir.Insert(ctx, newTestIssue("bd-cda-child", "t"), "creator", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-cda-child", DependsOnID: "bd-cda-parent", Type: types.DepParentChild,
	}, "linker", domain.DepInsertOpts{}))
	s.clearJournal()

	_, err := s.issueUseCase().DeleteIssues(ctx,
		domain.DeleteIssuesParams{IDs: []string{"bd-cda-parent"}, Cascade: true}, "cascade-deleter")
	s.Require().NoError(err)

	var deletes, depRemoves int
	for _, r := range s.readJournal() {
		switch r.Op {
		case "delete":
			deletes++
			s.Equalf("cascade-deleter", r.Actor, "delete row for %s", r.IssueID)
		case "dep_remove":
			depRemoves++
			s.Equalf("cascade-deleter", r.Actor, "dep_remove row for %s", r.IssueID)
		}
	}
	s.Equal(2, deletes, "the named bead and the one the cascade reached")
	s.GreaterOrEqual(depRemoves, 1, "the parent-child edge must be journaled")
}

// TestEventsJournal_DisabledWritesNothing asserts the default-off knob writes
// no rows.
func (s *testSuite) TestEventsJournal_DisabledWritesNothing() {
	s.journalEnabled = false
	ctx := s.Ctx()
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	s.Require().NoError(s.issueRepo().Insert(ctx, newTestIssue("bd-off-1", "t"), "actor", domain.InsertIssueOpts{}))

	var n int
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&n))
	s.Equal(0, n, "disabled journal must write nothing")
}

// TestEventsJournal_UOWDependencySnapshotsFollowBlockedState mirrors the
// direct-store guard at the UOW repository seam. The event snapshot must be
// taken after is_blocked maintenance, including same-type metadata refreshes.
func (s *testSuite) TestEventsJournal_UOWDependencySnapshotsFollowBlockedState() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-uj-source", "bd-uj-target"} {
		s.Require().NoError(ir.Insert(ctx, newTestIssue(id, id), "actor", domain.InsertIssueOpts{}))
	}
	_, err := s.Runner().ExecContext(ctx, "DELETE FROM bd_events_journal")
	s.Require().NoError(err)

	add := func(metadata string) {
		s.T().Helper()
		s.Require().NoError(dr.Insert(ctx, &types.Dependency{
			IssueID: "bd-uj-source", DependsOnID: "bd-uj-target", Type: types.DepBlocks, Metadata: metadata,
		}, "actor", domain.DepInsertOpts{}))
	}
	last := func() journalRow {
		s.T().Helper()
		rows := s.readJournal()
		s.Require().NotEmpty(rows)
		return rows[len(rows)-1]
	}

	add(`{"revision":"A"}`)
	row := last()
	s.Require().True(row.Issue.IsBlocked)
	s.Require().NotNil(row.Dep)
	s.Equal(`{"revision":"A"}`, row.Dep.Metadata)

	add(`{"revision":"B"}`)
	row = last()
	s.Require().True(row.Issue.IsBlocked)
	s.Require().NotNil(row.Dep)
	s.Equal(`{"revision":"B"}`, row.Dep.Metadata)

	_, err = dr.Delete(ctx, "bd-uj-source", "bd-uj-target", "actor", domain.DepInsertOpts{})
	s.Require().NoError(err)
	row = last()
	s.Require().False(row.Issue.IsBlocked)
	s.Require().NotNil(row.Dep)
	s.Equal(`{"revision":"B"}`, row.Dep.Metadata)
}

// TestEventsJournal_ReplayFromZeroReconstructsLiveSet is the consumer contract:
// applying every record in seq order from an empty state must reproduce exactly
// the beads the database actually holds. A missing emit, a phantom delete, or a
// stale snapshot all show up here as a diff.
func (s *testSuite) TestEventsJournal_ReplayFromZeroReconstructsLiveSet() {
	s.enableJournalForTest()
	ctx := s.Ctx()
	ir := s.issueRepo()
	dr := s.depRepo()
	lr := s.labelRepo()

	for _, id := range []string{"bd-rp-1", "bd-rp-2", "bd-rp-3", "bd-rp-4"} {
		s.Require().NoError(ir.Insert(ctx, newTestIssue(id, "title "+id), "actor", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(ir.Update(ctx, "bd-rp-1", map[string]any{"title": "edited"}, "actor", domain.IssueTableOpts{}))
	s.Require().NoError(lr.Insert(ctx, "bd-rp-2", "urgent", "actor", domain.LabelOpts{}))
	s.Require().NoError(dr.Insert(ctx, &types.Dependency{
		IssueID: "bd-rp-3", DependsOnID: "bd-rp-4", Type: types.DepBlocks,
	}, "actor", domain.DepInsertOpts{}))
	_, err := ir.Close(ctx, "bd-rp-2", domain.CloseRowParams{Reason: "done"}, "actor", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(ir.Delete(ctx, "bd-rp-4", domain.IssueTableOpts{}, "actor"))

	// Replay: create/update/close/comment/dep_* set the snapshot, delete drops it.
	replay := map[string]*types.Issue{}
	for _, r := range s.readJournal() {
		if r.Op == "delete" {
			delete(replay, r.IssueID)
			continue
		}
		if r.HasIssue {
			replay[r.IssueID] = r.Issue
		}
	}

	live := map[string]*types.Issue{}
	rows, err := s.Runner().QueryContext(ctx, "SELECT id FROM issues")
	s.Require().NoError(err)
	var ids []string
	for rows.Next() {
		var id string
		s.Require().NoError(rows.Scan(&id))
		ids = append(ids, id)
	}
	s.Require().NoError(rows.Err())
	s.Require().NoError(rows.Close())
	for _, id := range ids {
		iss, err := ir.Get(ctx, id, domain.IssueTableOpts{})
		s.Require().NoError(err)
		// The issue repository does not hydrate labels (that is the label
		// repository's job); the journal snapshot does, so read them to compare
		// like with like.
		labels, err := lr.List(ctx, id, domain.LabelOpts{})
		s.Require().NoError(err)
		iss.Labels = labels
		live[id] = iss
	}

	s.Require().Len(replay, len(live), "replayed set size must match the live set")
	for id, want := range live {
		got, ok := replay[id]
		s.Require().Truef(ok, "replay is missing %s", id)
		s.Equalf(want.Title, got.Title, "%s title", id)
		s.Equalf(want.Status, got.Status, "%s status", id)
		s.Equalf(want.Assignee, got.Assignee, "%s assignee", id)
		s.ElementsMatchf(want.Labels, got.Labels, "%s labels", id)
	}
	s.NotContains(replay, "bd-rp-4", "a deleted bead must not survive replay")
}
