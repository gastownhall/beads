package historyoffequiv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Roles is the role-accessor surface all three legs offer. The shared
// scenario reaches storage only through it (and Leg.Wake), so each leg runs
// the scenario through its own implementations: the embedded store's and the
// server store's issueops-backed roles, and the unit-of-work provider's
// domain-backed ones.
type Roles interface {
	IssueLifecycle() (publicops.Lifecycle, error)
	BatchCreator() (publicops.BatchCreator, error)
	BatchApplier() (publicops.BatchApplier, error)
	BatchCloser() (publicops.BatchCloser, error)
	DependencyEditor() (publicops.DependencyEditor, error)
	Commenter() (publicops.Commenter, error)
	IssueClaimer() (publicops.Claimer, error)
	Heartbeater() (publicops.Heartbeater, error)
	Releaser() (publicops.Releaser, error)
	LeaseReclaimer() (publicops.LeaseReclaimer, error)
	MetadataCAS() (publicops.MetadataCAS, error)
	Deleter() (publicops.Deleter, error)
}

// fixedAt is every timestamp the scenario supplies. It is decades outside
// any run's window, so a column that keeps it reads it verbatim in the dump
// and a column that loses it to the wall clock reads "<now>".
var fixedAt = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

func id(suffix string) string { return Prefix + "-" + suffix }

func newIssue(suffix, title string, issueType types.IssueType) *types.Issue {
	return &types.Issue{
		ID: id(suffix), Title: title, Description: "about " + suffix, Status: types.StatusOpen,
		Priority: 2, IssueType: issueType, CreatedAt: fixedAt, UpdatedAt: fixedAt,
	}
}

func set[T any](v T) publicops.Field[T] { return publicops.Field[T]{Set: true, Value: v} }

func raw(s string) *json.RawMessage {
	m := json.RawMessage(s)
	return &m
}

// SharedSteps is the scenario every leg runs: each write role on the issues
// plane, then each again on the wisps plane (an ephemeral wisp, and the
// no-history wisp nh along the way), including the paths the
// versioned-history stack rewired (single and batch creates, the persistence
// moves between planes, the defer wake), refusals that must write nothing (a
// batch close item close policy refuses), and no-op updates that must leave
// every row alone. On the wisps plane the lease roles refuse (Claim,
// Heartbeat) or find nothing (Reclaim), as their contracts say, and those
// steps check the refusal. Neither reclaim sweep finds an expired lease: a
// role cannot age one, so they cover the sweep's reads and commit, not a
// revert.
func SharedSteps(r Roles, wake func(context.Context) error) []Step {
	create := func(req publicops.CreateRequest) func(context.Context) error {
		req.Actor = "alice"
		return func(ctx context.Context) error {
			lifecycle, err := r.IssueLifecycle()
			if err != nil {
				return err
			}
			_, err = lifecycle.Create(ctx, req)
			return err
		}
	}
	update := func(issueID string, patch publicops.IssuePatch) func(context.Context) error {
		return func(ctx context.Context) error {
			lifecycle, err := r.IssueLifecycle()
			if err != nil {
				return err
			}
			_, err = lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "bob", IssueID: issueID, Patch: patch})
			return err
		}
	}

	a := newIssue("a", "alpha", types.TypeTask)
	a.Labels = []string{"one", "two"}
	a.Metadata = json.RawMessage(`{"k":1,"s":"x"}`)
	a.Notes = "first note"
	b := newIssue("b", "bravo", types.TypeBug)
	b.Priority = 1
	epic := newIssue("epic", "the epic", types.TypeEpic)
	child := newIssue("epic.1", "epic child", types.TypeTask)
	w1 := newIssue("w1", "a wisp", types.TypeTask)
	w1.Ephemeral = true
	w1.Labels = []string{"wl"}
	nh := newIssue("nh", "no history", types.TypeTask)
	nh.NoHistory = true
	c := newIssue("c", "charlie", types.TypeFeature)
	c.Labels = []string{"moving"}
	d := newIssue("d", "delta", types.TypeTask)
	e := newIssue("e", "echo", types.TypeTask)
	// The wisps-plane block's wisps: x carries the field, lease, comment,
	// metadata and close traffic, y the refused claim and the dependency,
	// apply and delete traffic, z the defer wake.
	wisp := func(suffix, title string) *types.Issue {
		w := newIssue(suffix, title, types.TypeTask)
		w.Ephemeral = true
		return w
	}
	x := wisp("x", "x-ray")
	x.Labels = []string{"xl", "xm"}
	x.Metadata = json.RawMessage(`{"k":1}`)
	y, z := wisp("y", "yankee"), wisp("z", "zulu")

	return []Step{
		{"create a", create(publicops.CreateRequest{Issue: a})},
		{"create b blocked by a", create(publicops.CreateRequest{Issue: b,
			Dependencies: []publicops.CreateDependency{{TargetID: id("a"), Type: types.DepBlocks}}})},
		{"create epic", create(publicops.CreateRequest{Issue: epic})},
		{"create epic.1 under epic", create(publicops.CreateRequest{Issue: child, ParentID: id("epic")})},
		{"create wisp w1", create(publicops.CreateRequest{Issue: w1})},
		{"create no-history nh", create(publicops.CreateRequest{Issue: nh})},
		{"create c", create(publicops.CreateRequest{Issue: c})},
		{"create d", create(publicops.CreateRequest{Issue: d})},
		{"create e", create(publicops.CreateRequest{Issue: e})},

		{"update a fields labels metadata", update(id("a"), publicops.IssuePatch{
			Title:       set("alpha two"),
			Priority:    set(3),
			Assignee:    set("bob"),
			Status:      set(types.StatusInProgress),
			AppendNotes: set("second note"),
			Labels:      publicops.LabelPatch{Add: []string{"three"}, Remove: []string{"two"}},
			Metadata:    publicops.MetadataPatch{Set: map[string]json.RawMessage{"k": json.RawMessage(`2`)}},
		})},
		{"update a no-op", update(id("a"), publicops.IssuePatch{Title: set("alpha two")})},
		{"update w1 to persistent", update(id("w1"), publicops.IssuePatch{Persistence: set(types.PersistenceModePersistent)})},
		{"update c to ephemeral", update(id("c"), publicops.IssuePatch{Persistence: set(types.PersistenceModeEphemeral)})},
		{"update d parent epic", update(id("d"), publicops.IssuePatch{ParentID: set(id("epic"))})},

		{"add dependencies", func(ctx context.Context) error {
			editor, err := r.DependencyEditor()
			if err != nil {
				return err
			}
			_, err = editor.AddDependencies(ctx, publicops.AddDependenciesRequest{Actor: "carol", Edges: []publicops.DependencyEdge{
				{IssueID: id("d"), DependsOnID: id("b"), Type: types.DepBlocks},
				{IssueID: id("e"), DependsOnID: id("a"), Type: types.DepRelated},
				{IssueID: id("nh"), DependsOnID: id("d"), Type: types.DepBlocks},
			}})
			return err
		}},
		{"remove dependency b->a", func(ctx context.Context) error {
			editor, err := r.DependencyEditor()
			if err != nil {
				return err
			}
			_, err = editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{Actor: "carol", IssueID: id("b"), DependsOnID: id("a")})
			return err
		}},
		{"comment on a and nh", func(ctx context.Context) error {
			commenter, err := r.Commenter()
			if err != nil {
				return err
			}
			if _, err := commenter.AddComment(ctx, publicops.AddCommentRequest{Author: "carol", IssueID: id("a"), Text: "hello a"}); err != nil {
				return err
			}
			_, err = commenter.AddComment(ctx, publicops.AddCommentRequest{Author: "carol", IssueID: id("nh"), Text: "hello nh"})
			return err
		}},
		{"claim heartbeat release b", func(ctx context.Context) error {
			claimer, err := r.IssueClaimer()
			if err != nil {
				return err
			}
			if _, err := claimer.Claim(ctx, publicops.ClaimRequest{Actor: "dave", IssueID: id("b")}); err != nil {
				return fmt.Errorf("claim: %w", err)
			}
			heartbeater, err := r.Heartbeater()
			if err != nil {
				return err
			}
			if _, err := heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: "dave", IssueID: id("b")}); err != nil {
				return fmt.Errorf("heartbeat: %w", err)
			}
			releaser, err := r.Releaser()
			if err != nil {
				return err
			}
			_, err = releaser.Release(ctx, publicops.ReleaseRequest{Actor: "dave", IssueID: id("b")})
			return err
		}},
		{"claim e and reclaim no lease", func(ctx context.Context) error {
			claimer, err := r.IssueClaimer()
			if err != nil {
				return err
			}
			if _, err := claimer.Claim(ctx, publicops.ClaimRequest{Actor: "erin", IssueID: id("e")}); err != nil {
				return fmt.Errorf("claim: %w", err)
			}
			reclaimer, err := r.LeaseReclaimer()
			if err != nil {
				return err
			}
			// e's lease has not expired, so the sweep reverts nothing.
			_, err = reclaimer.Reclaim(ctx, publicops.ReclaimRequest{Actor: "reaper",
				Filter: publicops.ReclaimFilter{IDs: []string{id("e")}}})
			return err
		}},
		{"metadata compare-and-set on a", func(ctx context.Context) error {
			cas, err := r.MetadataCAS()
			if err != nil {
				return err
			}
			_, err = cas.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{Actor: "frank", IssueID: id("a"), Key: "cas", Value: raw(`"v1"`)})
			return err
		}},
		{"close reopen close epic.1", func(ctx context.Context) error {
			lifecycle, err := r.IssueLifecycle()
			if err != nil {
				return err
			}
			if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "bob", IssueID: id("epic.1"), Reason: "done"}); err != nil {
				return fmt.Errorf("close: %w", err)
			}
			if _, err := lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "bob", IssueID: id("epic.1"), Reason: "not yet"}); err != nil {
				return fmt.Errorf("reopen: %w", err)
			}
			_, err = lifecycle.Close(ctx, publicops.CloseRequest{Actor: "bob", IssueID: id("epic.1"), Reason: "done again"})
			return err
		}},
		{"close batch b and refused epic", func(ctx context.Context) error {
			closer, err := r.BatchCloser()
			if err != nil {
				return err
			}
			// b closes; epic keeps its open child d, so close policy refuses
			// that item and the batch keeps its survivor.
			_, err = closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "bob", Items: []publicops.BatchCloseItem{
				{IssueID: id("b"), Reason: "b done"}, {IssueID: id("epic"), Reason: "epic done"},
			}})
			return err
		}},
		{"force close blocked no-history nh", func(ctx context.Context) error {
			lifecycle, err := r.IssueLifecycle()
			if err != nil {
				return err
			}
			_, err = lifecycle.Close(ctx, publicops.CloseRequest{Actor: "bob", IssueID: id("nh"), Reason: "nh done", Force: true})
			return err
		}},
		{"create batch", func(ctx context.Context) error {
			creator, err := r.BatchCreator()
			if err != nil {
				return err
			}
			f1 := newIssue("f1", "foxtrot one", types.TypeTask)
			f1.Labels = []string{"batch"}
			f2 := newIssue("f2", "foxtrot two", types.TypeTask)
			fw := newIssue("fw", "foxtrot wisp", types.TypeTask)
			fw.Ephemeral = true
			_, err = creator.CreateBatch(ctx, publicops.CreateBatchRequest{Actor: "gina", Items: []publicops.BatchCreateItem{
				{Issue: f1},
				{Issue: f2, Dependencies: []publicops.CreateDependency{{TargetID: id("f1"), Type: types.DepBlocks}}},
				{Issue: fw},
			}})
			return err
		}},
		{"apply batch", func(ctx context.Context) error {
			applier, err := r.BatchApplier()
			if err != nil {
				return err
			}
			g1 := newIssue("g1", "golf one", types.TypeTask)
			g2 := newIssue("g2", "golf two", types.TypeBug)
			g2.Labels = []string{"applied"}
			_, err = applier.ApplyBatch(ctx, publicops.ApplyBatchRequest{Actor: "hank", Items: []publicops.ApplyItem{
				{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: "g1", Issue: g1}},
				{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: "g2", Issue: g2}},
				{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{Source: publicops.Ref{Key: "g2"}, Target: publicops.Ref{Key: "g1"}, Type: types.DepBlocks}},
				{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{Target: publicops.Ref{ID: id("d")}, Patch: publicops.IssuePatch{Assignee: set("hank")}}},
				{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{Key: "g1"}, Reason: "applied"}},
			}})
			return err
		}},
		{"defer f1 into the past and wake it", func(ctx context.Context) error {
			past := fixedAt
			if err := update(id("f1"), publicops.IssuePatch{Status: set(types.StatusDeferred), DeferUntil: set(&past)})(ctx); err != nil {
				return fmt.Errorf("defer: %w", err)
			}
			return wake(ctx)
		}},
		{"delete f2", func(ctx context.Context) error {
			deleter, err := r.Deleter()
			if err != nil {
				return err
			}
			_, err = deleter.Delete(ctx, publicops.DeleteRequest{Actor: "ivan", IDs: []string{id("f2")}, Force: true})
			return err
		}},

		// The wisps plane, role by role.
		{"create wisps x y z", func(ctx context.Context) error {
			for _, w := range []*types.Issue{x, y, z} {
				if err := create(publicops.CreateRequest{Issue: w})(ctx); err != nil {
					return fmt.Errorf("create %s: %w", w.ID, err)
				}
			}
			return nil
		}},
		{"update wisp x fields labels metadata", update(id("x"), publicops.IssuePatch{
			Title:       set("x-ray two"),
			Priority:    set(1),
			Assignee:    set("bob"),
			Status:      set(types.StatusInProgress),
			AppendNotes: set("wisp note"),
			Labels:      publicops.LabelPatch{Add: []string{"xn"}, Remove: []string{"xm"}},
			Metadata:    publicops.MetadataPatch{Set: map[string]json.RawMessage{"k": json.RawMessage(`2`)}},
		})},
		{"update wisp x no-op", update(id("x"), publicops.IssuePatch{Title: set("x-ray two")})},
		{"claim and heartbeat refuse wisps", func(ctx context.Context) error {
			// The lease roles do not lease a wisp: Claim answers ErrNotFound,
			// Heartbeat ErrNotClaimable, and neither may write.
			claimer, err := r.IssueClaimer()
			if err != nil {
				return err
			}
			if _, err := claimer.Claim(ctx, publicops.ClaimRequest{Actor: "dave", IssueID: id("y")}); !errors.Is(err, publicops.ErrNotFound) {
				return fmt.Errorf("claim wisp y = %v, want ErrNotFound", err)
			}
			heartbeater, err := r.Heartbeater()
			if err != nil {
				return err
			}
			if _, err := heartbeater.Heartbeat(ctx, publicops.HeartbeatRequest{Actor: "bob", IssueID: id("x")}); !errors.Is(err, publicops.ErrNotClaimable) {
				return fmt.Errorf("heartbeat wisp x = %v, want ErrNotClaimable", err)
			}
			return nil
		}},
		{"reclaim sweep scoped to in-progress wisp x", func(ctx context.Context) error {
			reclaimer, err := r.LeaseReclaimer()
			if err != nil {
				return err
			}
			// The sweep only reads the issues table, so it reverts nothing.
			_, err = reclaimer.Reclaim(ctx, publicops.ReclaimRequest{Actor: "reaper",
				Filter: publicops.ReclaimFilter{IDs: []string{id("x")}}})
			return err
		}},
		{"release wisp x", func(ctx context.Context) error {
			releaser, err := r.Releaser()
			if err != nil {
				return err
			}
			_, err = releaser.Release(ctx, publicops.ReleaseRequest{Actor: "bob", IssueID: id("x")})
			return err
		}},
		{"add and remove wisp dependencies", func(ctx context.Context) error {
			editor, err := r.DependencyEditor()
			if err != nil {
				return err
			}
			if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{Actor: "carol", Edges: []publicops.DependencyEdge{
				{IssueID: id("y"), DependsOnID: id("x"), Type: types.DepBlocks},
				{IssueID: id("x"), DependsOnID: id("a"), Type: types.DepRelated},
			}}); err != nil {
				return fmt.Errorf("add: %w", err)
			}
			_, err = editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{Actor: "carol", IssueID: id("y"), DependsOnID: id("x")})
			return err
		}},
		{"comment on wisp x", func(ctx context.Context) error {
			commenter, err := r.Commenter()
			if err != nil {
				return err
			}
			_, err = commenter.AddComment(ctx, publicops.AddCommentRequest{Author: "carol", IssueID: id("x"), Text: "hello x"})
			return err
		}},
		{"metadata compare-and-set on wisp x", func(ctx context.Context) error {
			cas, err := r.MetadataCAS()
			if err != nil {
				return err
			}
			_, err = cas.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{Actor: "frank", IssueID: id("x"), Key: "cas", Value: raw(`"w1"`)})
			return err
		}},
		{"close reopen wisp x", func(ctx context.Context) error {
			lifecycle, err := r.IssueLifecycle()
			if err != nil {
				return err
			}
			if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "bob", IssueID: id("x"), Reason: "x done"}); err != nil {
				return fmt.Errorf("close: %w", err)
			}
			_, err = lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "bob", IssueID: id("x"), Reason: "x not yet"})
			return err
		}},
		{"apply batch on the wisps plane", func(ctx context.Context) error {
			applier, err := r.BatchApplier()
			if err != nil {
				return err
			}
			h1 := wisp("h1", "hotel wisp")
			h1.Labels = []string{"applied"}
			_, err = applier.ApplyBatch(ctx, publicops.ApplyBatchRequest{Actor: "hank", Items: []publicops.ApplyItem{
				{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Key: "h1", Issue: h1}},
				{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{Source: publicops.Ref{ID: id("y")}, Target: publicops.Ref{Key: "h1"}, Type: types.DepBlocks}},
				{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{Target: publicops.Ref{ID: id("y")}, Patch: publicops.IssuePatch{Assignee: set("hank")}}},
				{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{Key: "h1"}, Reason: "applied"}},
			}})
			return err
		}},
		{"close batch wisps x and nh", func(ctx context.Context) error {
			closer, err := r.BatchCloser()
			if err != nil {
				return err
			}
			// x closes; nh is already closed, so the batch closes x alone.
			_, err = closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "bob", Items: []publicops.BatchCloseItem{
				{IssueID: id("x"), Reason: "x done again"}, {IssueID: id("nh"), Reason: "nh again"},
			}})
			return err
		}},
		{"defer wisp z into the past and wake it", func(ctx context.Context) error {
			past := fixedAt
			if err := update(id("z"), publicops.IssuePatch{Status: set(types.StatusDeferred), DeferUntil: set(&past)})(ctx); err != nil {
				return fmt.Errorf("defer: %w", err)
			}
			return wake(ctx)
		}},
		{"delete wisp y", func(ctx context.Context) error {
			deleter, err := r.Deleter()
			if err != nil {
				return err
			}
			_, err = deleter.Delete(ctx, publicops.DeleteRequest{Actor: "ivan", IDs: []string{id("y")}, Force: true})
			return err
		}},
	}
}

// DoltStorageSteps are the store legs' extra steps: the storage.DoltStorage
// methods most of bd's commands still write through, including the import
// upsert (CreateIssuesWithFullOptions) whose create-versus-update choice the
// stack rewired, promotion out of the wisps table, and RunInTransaction.
func DoltStorageSteps(s storage.DoltStorage) []Step {
	return []Step{
		{"store create s1", func(ctx context.Context) error {
			s1 := newIssue("s1", "sierra one", types.TypeTask)
			s1.Labels = []string{"store"}
			return s.CreateIssue(ctx, s1, "sam")
		}},
		{"store create issues s2 s3", func(ctx context.Context) error {
			return s.CreateIssues(ctx, []*types.Issue{newIssue("s2", "sierra two", types.TypeTask), newIssue("s3", "sierra three", types.TypeBug)}, "sam")
		}},
		{"store import upsert s1 and new s4", func(ctx context.Context) error {
			s1 := newIssue("s1", "sierra one imported", types.TypeTask)
			s1.Labels = []string{"store", "imported"}
			s1.UpdatedAt = fixedAt.Add(time.Hour)
			s4 := newIssue("s4", "sierra four", types.TypeTask)
			s4.Dependencies = []*types.Dependency{{IssueID: id("s4"), DependsOnID: id("s1"), Type: types.DepBlocks, CreatedAt: fixedAt}}
			s4.Comments = []*types.Comment{{ID: "ho-imported-comment", IssueID: id("s4"), Author: "importer", Text: "imported", CreatedAt: fixedAt}}
			sw := newIssue("sw", "sierra wisp", types.TypeTask)
			sw.Ephemeral = true
			return s.CreateIssuesWithFullOptions(ctx, []*types.Issue{s1, s4, sw}, "importer",
				storage.BatchCreateOptions{SkipPrefixValidation: true})
		}},
		{"store update s2", func(ctx context.Context) error {
			return s.UpdateIssue(ctx, id("s2"), map[string]interface{}{"title": "sierra two updated", "priority": 1, "assignee": "sam"}, "sam")
		}},
		{"store update s2 no-op", func(ctx context.Context) error {
			return s.UpdateIssue(ctx, id("s2"), map[string]interface{}{"title": "sierra two updated"}, "sam")
		}},
		{"store labels", func(ctx context.Context) error {
			if err := s.AddLabel(ctx, id("s2"), "added", "sam"); err != nil {
				return err
			}
			return s.RemoveLabel(ctx, id("s1"), "store", "sam")
		}},
		{"store dependencies", func(ctx context.Context) error {
			if err := s.AddDependency(ctx, &types.Dependency{IssueID: id("s3"), DependsOnID: id("s2"), Type: types.DepBlocks}, "sam"); err != nil {
				return err
			}
			return s.RemoveDependency(ctx, id("s4"), id("s1"), "sam")
		}},
		{"store comment", func(ctx context.Context) error {
			_, err := s.AddIssueComment(ctx, id("s3"), "sam", "store comment")
			return err
		}},
		{"store claim s3", func(ctx context.Context) error {
			bulk, ok := s.(storage.BulkIssueStore)
			if !ok {
				return fmt.Errorf("%T is not a storage.BulkIssueStore", s)
			}
			return bulk.ClaimIssue(ctx, id("s3"), "tess")
		}},
		{"store close and reopen s2", func(ctx context.Context) error {
			if err := s.CloseIssue(ctx, id("s2"), "closed by store", "sam", "session-1"); err != nil {
				return err
			}
			return s.ReopenIssue(ctx, id("s2"), "reopened by store", "sam")
		}},
		{"store promote wisp sw", func(ctx context.Context) error {
			bulk, ok := s.(storage.BulkIssueStore)
			if !ok {
				return fmt.Errorf("%T is not a storage.BulkIssueStore", s)
			}
			return bulk.PromoteFromEphemeral(ctx, id("sw"), "sam")
		}},
		{"store transaction", func(ctx context.Context) error {
			return s.RunInTransaction(ctx, "bd: historyoffequiv transaction", func(tx storage.Transaction) error {
				t1 := newIssue("t1", "tango one", types.TypeTask)
				if err := tx.CreateIssue(ctx, t1, "tx"); err != nil {
					return err
				}
				if err := tx.UpdateIssue(ctx, id("t1"), map[string]interface{}{"description": "changed in tx"}, "tx"); err != nil {
					return err
				}
				if err := tx.AddLabel(ctx, id("t1"), "tx", "tx"); err != nil {
					return err
				}
				if err := tx.AddDependency(ctx, &types.Dependency{IssueID: id("t1"), DependsOnID: id("s1"), Type: types.DepRelated}, "tx"); err != nil {
					return err
				}
				return tx.CloseIssue(ctx, id("s4"), "closed in tx", "tx", "")
			})
		}},
		{"store delete s3", func(ctx context.Context) error {
			return s.DeleteIssue(ctx, id("s3"))
		}},
	}
}
