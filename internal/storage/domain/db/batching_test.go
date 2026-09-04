package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// recordingRunner delegates to a real Runner and records every statement it
// is asked to run, so a test can assert on how many round trips a bulk
// reader made and how many placeholders each one carried.
type recordingRunner struct {
	Runner
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	query string
	nargs int
}

func (r *recordingRunner) record(query string, args []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedCall{query: query, nargs: len(args)})
}

func (r *recordingRunner) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.record(query, args)
	return r.Runner.QueryContext(ctx, query, args...)
}

func (r *recordingRunner) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.record(query, args)
	return r.Runner.QueryRowContext(ctx, query, args...)
}

func (r *recordingRunner) reset() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

// TestBulkReadersBatch pins the fence wy-237yfi built: the bulk relation
// readers (comment, label, dependency) and GetByIDs send at most
// queryBatchSize ids per IN list, in ceil(N/queryBatchSize) statements per
// query leg, and merge the batches into one result that is indistinguishable
// from a single statement over all N ids. Dolt is super-linear in IN-list size (a 19k-id list took 24-60s
// per statement on the constellation's rig; 200-id batches take ~5ms each),
// so the placeholder cap is the property, not the statement count.
func (s *testSuite) TestBulkReadersBatch() {
	const n = queryBatchSize*2 + 50 // three batches: 200, 200, 50
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-batch-%03d", i)
	}
	// Relations sit on the ids at each batch boundary so a merge that
	// dropped or duplicated a batch would show up in the result.
	seeded := []int{0, queryBatchSize - 1, queryBatchSize, n - 1}
	for _, i := range seeded {
		s.seedIssueRow(ids[i])
	}
	wantBatches := (n + queryBatchSize - 1) / queryBatchSize

	rec := &recordingRunner{Runner: s.Runner()}
	comments := NewCommentSQLRepository(rec)
	labels := NewLabelSQLRepository(rec)
	deps := NewDependencySQLRepository(rec)

	base := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC)
	for k, i := range seeded {
		for j := 0; j <= k; j++ {
			s.seedComment(ids[i], "tester", fmt.Sprintf("c%d", j), base.Add(time.Duration(j)*time.Second))
		}
		s.Require().NoError(labels.Insert(s.Ctx(), ids[i], fmt.Sprintf("l%d", k), "tester", domain.LabelOpts{}))
	}
	// ids[0] blocks ids[n-1] (crosses batches 1 and 3); ids[200] blocks
	// ids[199] (crosses batches 2 and 1).
	s.Require().NoError(deps.Insert(s.Ctx(), newDep(ids[n-1], ids[0], types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(deps.Insert(s.Ctx(), newDep(ids[queryBatchSize-1], ids[queryBatchSize], types.DepBlocks), "tester", domain.DepInsertOpts{}))
	rec.reset()

	// extra is the number of non-id placeholders a leg carries (a type
	// filter); the id list itself is what the cap is about.
	assertBatchedExtra := func(name string, legs, extra int) {
		calls := rec.reset()
		s.Require().Len(calls, wantBatches*legs, "%s: statements issued", name)
		for _, c := range calls {
			s.LessOrEqual(c.nargs, queryBatchSize+extra, "%s: bound args in one statement", name)
			s.LessOrEqual(strings.Count(c.query, "?"), queryBatchSize+extra, "%s: placeholders in one statement", name)
			s.Contains(c.query, " IN (", "%s: every leg is an IN-list read", name)
		}
	}
	assertBatched := func(name string, legs int) { assertBatchedExtra(name, legs, 0) }

	s.Run("CommentCounts", func() {
		out, err := comments.CountsByIssueIDs(s.Ctx(), ids, domain.CommentOpts{})
		s.Require().NoError(err)
		assertBatched("CountsByIssueIDs", 1)
		s.Len(out, len(seeded))
		for k, i := range seeded {
			s.Equal(k+1, out[ids[i]], "count for %s", ids[i])
		}
	})

	s.Run("CommentList", func() {
		out, err := comments.ListByIssueIDs(s.Ctx(), ids, domain.CommentOpts{})
		s.Require().NoError(err)
		assertBatched("ListByIssueIDs", 1)
		s.Len(out, len(seeded))
		for k, i := range seeded {
			got := out[ids[i]]
			s.Require().Len(got, k+1, "comments for %s", ids[i])
			for j, c := range got {
				s.Equal(fmt.Sprintf("c%d", j), c.Text, "order preserved within %s", ids[i])
			}
		}
	})

	s.Run("Labels", func() {
		out, err := labels.ListByIssueIDs(s.Ctx(), ids, domain.LabelOpts{})
		s.Require().NoError(err)
		assertBatched("Labels.ListByIssueIDs", 1)
		s.Len(out, len(seeded))
		for k, i := range seeded {
			s.Equal([]string{fmt.Sprintf("l%d", k)}, out[ids[i]])
		}
	})

	s.Run("DependencyList", func() {
		out, err := deps.ListByIssueIDs(s.Ctx(), ids, domain.DepListOpts{Direction: domain.DepDirectionBoth})
		s.Require().NoError(err)
		assertBatched("Deps.ListByIssueIDs", 2)
		s.Len(out.Outgoing, 2)
		s.Len(out.Incoming, 2)
		s.Require().Len(out.Outgoing[ids[n-1]], 1)
		s.Equal(ids[0], out.Outgoing[ids[n-1]][0].DependsOnID)
		s.Require().Len(out.Incoming[ids[0]], 1)
		s.Equal(ids[n-1], out.Incoming[ids[0]][0].IssueID)
		s.Require().Len(out.Outgoing[ids[queryBatchSize-1]], 1)
		s.Require().Len(out.Incoming[ids[queryBatchSize]], 1)
	})

	s.Run("DependencyCounts", func() {
		out, err := deps.CountsByIssueIDs(s.Ctx(), ids, domain.DepCountsOpts{})
		s.Require().NoError(err)
		assertBatched("Deps.CountsByIssueIDs", 2)
		s.Len(out, n, "every requested id is present, zero or not")
		s.Equal(&types.DependencyCounts{DependencyCount: 1}, out[ids[n-1]])
		s.Equal(&types.DependencyCounts{DependentCount: 1}, out[ids[0]])
		s.Equal(&types.DependencyCounts{DependencyCount: 1}, out[ids[queryBatchSize-1]])
		s.Equal(&types.DependencyCounts{DependentCount: 1}, out[ids[queryBatchSize]])
		s.Equal(&types.DependencyCounts{}, out[ids[1]])
	})

	s.Run("DependencyListTypeFilter", func() {
		// typeArgs ride along after the id args in EVERY batch, so the
		// filter must hold across all three and the placeholder cap grows
		// by exactly the filter's width.
		s.Require().NoError(deps.Insert(s.Ctx(), newDep(ids[0], ids[queryBatchSize], types.DepRelated), "tester", domain.DepInsertOpts{}))
		rec.reset()
		out, err := deps.ListByIssueIDs(s.Ctx(), ids, domain.DepListOpts{
			Direction: domain.DepDirectionBoth,
			Types:     []types.DependencyType{types.DepBlocks},
		})
		s.Require().NoError(err)
		assertBatchedExtra("Deps.ListByIssueIDs(Types)", 2, 1)
		s.Require().Len(out.Outgoing[ids[0]], 0, "the related edge is filtered out in batch 1")
		s.Require().Len(out.Incoming[ids[queryBatchSize]], 1, "only the blocks edge survives in batch 2")
		s.Equal(types.DepBlocks, out.Incoming[ids[queryBatchSize]][0].Type)
		s.Require().Len(out.Outgoing[ids[n-1]], 1)
	})

	s.Run("WispPlane", func() {
		// One wisp in the middle batch; every reader routed to the wisp
		// tables must batch the same way and find it.
		wisp := ids[queryBatchSize+50]
		s.seedWispRow(wisp)
		s.seedWispComment(wisp, "tester", "w0", base)
		s.seedWispComment(wisp, "tester", "w1", base.Add(time.Second))
		s.Require().NoError(labels.Insert(s.Ctx(), wisp, "wl", "tester", domain.LabelOpts{UseWispsTable: true}))
		// wisp -> durable edge: wisp_dependencies(issue_id -> wisps, target -> issues).
		s.Require().NoError(deps.Insert(s.Ctx(), newDep(wisp, ids[0], types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))
		rec.reset()

		counts, err := comments.CountsByIssueIDs(s.Ctx(), ids, domain.CommentOpts{UseWispsTable: true})
		s.Require().NoError(err)
		assertBatched("wisp CountsByIssueIDs", 1)
		s.Equal(map[string]int{wisp: 2}, counts)

		list, err := comments.ListByIssueIDs(s.Ctx(), ids, domain.CommentOpts{UseWispsTable: true})
		s.Require().NoError(err)
		assertBatched("wisp ListByIssueIDs", 1)
		s.Require().Len(list, 1)
		s.Require().Len(list[wisp], 2)
		s.Equal("w0", list[wisp][0].Text)

		wl, err := labels.ListByIssueIDs(s.Ctx(), ids, domain.LabelOpts{UseWispsTable: true})
		s.Require().NoError(err)
		assertBatched("wisp Labels.ListByIssueIDs", 1)
		s.Equal(map[string][]string{wisp: {"wl"}}, wl)

		dl, err := deps.ListByIssueIDs(s.Ctx(), ids, domain.DepListOpts{Direction: domain.DepDirectionBoth, UseWispsTable: true})
		s.Require().NoError(err)
		assertBatched("wisp Deps.ListByIssueIDs", 2)
		s.Require().Len(dl.Outgoing[wisp], 1)
		s.Equal(ids[0], dl.Outgoing[wisp][0].DependsOnID)
		s.Require().Len(dl.Incoming[ids[0]], 1, "the durable target's inbound wisp edge lives in wisp_dependencies")

		dc, err := deps.CountsByIssueIDs(s.Ctx(), ids, domain.DepCountsOpts{UseWispsTable: true})
		s.Require().NoError(err)
		assertBatched("wisp Deps.CountsByIssueIDs", 2)
		s.Equal(&types.DependencyCounts{DependencyCount: 1}, dc[wisp])
		s.Equal(&types.DependencyCounts{DependentCount: 1}, dc[ids[0]])

		issues := NewIssueSQLRepository(rec)
		rec.reset()
		got, err := issues.GetByIDs(s.Ctx(), ids, domain.IssueTableOpts{UseWispsTable: true})
		s.Require().NoError(err)
		assertBatched("wisp GetByIDs", 1)
		s.Require().Len(got, 1)
		s.Equal(wisp, got[0].ID)
	})

	s.Run("GetByIDs", func() {
		issues := NewIssueSQLRepository(rec)
		rec.reset()
		got, err := issues.GetByIDs(s.Ctx(), ids, domain.IssueTableOpts{})
		s.Require().NoError(err)
		assertBatched("GetByIDs", 1)
		s.Require().Len(got, len(seeded), "one row per seeded durable id, across all three batches")
		gotIDs := make(map[string]bool, len(got))
		for _, iss := range got {
			gotIDs[iss.ID] = true
		}
		for _, i := range seeded {
			s.True(gotIDs[ids[i]], "%s present", ids[i])
		}
	})

	s.Run("EmptyInputIssuesNoStatement", func() {
		_, err := comments.CountsByIssueIDs(s.Ctx(), nil, domain.CommentOpts{})
		s.Require().NoError(err)
		_, err = labels.ListByIssueIDs(s.Ctx(), nil, domain.LabelOpts{})
		s.Require().NoError(err)
		_, err = deps.CountsByIssueIDs(s.Ctx(), nil, domain.DepCountsOpts{})
		s.Require().NoError(err)
		_, err = NewIssueSQLRepository(rec).GetByIDs(s.Ctx(), nil, domain.IssueTableOpts{})
		s.Require().NoError(err)
		s.Empty(rec.reset())
	})
}
