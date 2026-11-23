package uowstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeDeps records the dependency writes and the event options they carried.
type fakeDeps struct {
	domain.DependencyUseCase

	added   []*types.Dependency
	addOpts []domain.BulkAddDepsOpts
	removed []string
	remOpts []domain.RemoveDepBySourceOpts
}

func (f *fakeDeps) AddDependencies(_ context.Context, deps []*types.Dependency, _ string, opts domain.BulkAddDepsOpts) (domain.BulkAddDepsResult, error) {
	f.added = append(f.added, deps...)
	f.addOpts = append(f.addOpts, opts)
	return domain.BulkAddDepsResult{Added: deps}, nil
}

func (f *fakeDeps) RemoveDependencyBySource(_ context.Context, src, dst, _ string) (bool, error) {
	f.removed = append(f.removed, "plain "+src+" "+dst)
	return true, nil
}

func (f *fakeDeps) RemoveDependencyBySourceWithOptions(_ context.Context, src, dst, _ string, opts domain.RemoveDepBySourceOpts) (bool, error) {
	f.removed = append(f.removed, src+" "+dst)
	f.remOpts = append(f.remOpts, opts)
	return true, nil
}

func task(title string) *types.Issue {
	return &types.Issue{Title: title, IssueType: types.TypeTask}
}

// inTx runs fn in RunInTransaction, so each create test covers both entry
// points: the flat method and the transaction's.
func inTx(s *Store, fn func(ctx context.Context, tx storage.Transaction) error) error {
	return s.RunInTransaction(context.Background(), "msg", func(tx storage.Transaction) error {
		return fn(context.Background(), tx)
	})
}

func TestCreatePersistsLabelsAndComments(t *testing.T) {
	for _, viaTx := range []bool{false, true} {
		s, p := newFakeStore(t)
		issues := newFakeIssues()
		p.uw.issues = issues
		comment := &types.Comment{Author: "a", Text: "hello"}
		issue := task("t")
		issue.Labels = []string{"l1", "l2"}
		issue.Comments = []*types.Comment{comment}
		if viaTx {
			require.NoError(t, inTx(s, func(ctx context.Context, tx storage.Transaction) error {
				return tx.CreateIssue(ctx, issue, "a")
			}))
		} else {
			require.NoError(t, s.CreateIssue(context.Background(), issue, "a"))
		}
		require.Len(t, issues.params, 1, "viaTx=%v", viaTx)
		assert.Equal(t, []string{"l1", "l2"}, issues.params[0].Labels, "viaTx=%v: labels must reach the use case", viaTx)
		require.Len(t, issues.params[0].Comments, 1, "viaTx=%v: comments must reach the use case", viaTx)
		assert.Same(t, comment, issue.Comments[0], "viaTx=%v: the caller keeps its own comment structs", viaTx)
		assert.Equal(t, "c-bd-1-1", comment.ID, "viaTx=%v: the minted comment id is written back", viaTx)
		assert.NotEmpty(t, issue.ContentHash, "viaTx=%v: PrepareIssueForInsert computes the content hash", viaTx)
	}
}

func TestCreateRoutesPlaneByIsWisp(t *testing.T) {
	no := false
	cases := []struct {
		name   string
		issue  func() *types.Issue
		viaTx  bool
		want   string
		ephOut bool
	}{
		{"flat no-history", func() *types.Issue { i := task("t"); i.NoHistory = true; return i }, false, "CreateWisp", false},
		{"tx no-history", func() *types.Issue { i := task("t"); i.NoHistory = true; return i }, true, "CreateWisp", false},
		{"tx plane override", func() *types.Issue { i := task("t"); i.Ephemeral = true; i.WispPlaneOverride = &no; return i }, true, "CreateIssue", true},
		{"flat wisp type", func() *types.Issue { i := task("t"); i.WispType = types.WispTypeHeartbeat; return i }, false, "CreateWisp", true},
		{"flat infra type", func() *types.Issue { i := task("t"); i.IssueType = "agent"; return i }, false, "CreateWisp", true},
		{"tx wisp type stays durable", func() *types.Issue { i := task("t"); i.WispType = types.WispTypeHeartbeat; return i }, true, "CreateIssue", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newFakeStore(t)
			issues := newFakeIssues()
			p.uw.issues = issues
			p.uw.config = &fakeConfig{cc: domain.CreateContext{
				IssuePrefix: "bd", CustomTypes: []string{"agent"}, InfraTypes: map[string]bool{"agent": true},
			}}
			issue := tc.issue()
			if tc.viaTx {
				require.NoError(t, inTx(s, func(ctx context.Context, tx storage.Transaction) error {
					return tx.CreateIssue(ctx, issue, "a")
				}))
			} else {
				require.NoError(t, s.CreateIssue(context.Background(), issue, "a"))
			}
			assert.Equal(t, []string{tc.want}, issues.calls)
			assert.Equal(t, tc.ephOut, issue.Ephemeral)
		})
	}
}

func TestCreateValidatesBeforeInsert(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	p.uw.issues = issues
	err := s.CreateIssue(context.Background(), &types.Issue{Title: "t", IssueType: "no-such-type"}, "a")
	require.ErrorContains(t, err, "invalid issue type")
	assert.Empty(t, issues.calls)
	assert.Empty(t, p.uw.commits)
}

func TestCreateRefusesCrossPlaneID(t *testing.T) {
	for _, viaTx := range []bool{false, true} {
		s, p := newFakeStore(t)
		issues := newFakeIssues()
		p.uw.issues = issues
		issues.issues["bd-7"] = &types.Issue{ID: "bd-7"}
		issue := task("t")
		issue.ID = "bd-7"
		issue.Ephemeral = true
		var err error
		if viaTx {
			err = inTx(s, func(ctx context.Context, tx storage.Transaction) error { return tx.CreateIssue(ctx, issue, "a") })
		} else {
			err = s.CreateIssue(context.Background(), issue, "a")
		}
		require.ErrorContains(t, err, "already exists in the issues table", "viaTx=%v", viaTx)
		assert.Empty(t, issues.calls, "viaTx=%v", viaTx)
	}
}

func TestFlatCreateIssuesValidatesPrefix(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	p.uw.issues = issues
	issue := task("t")
	issue.ID = "zz-1"
	err := s.CreateIssues(context.Background(), []*types.Issue{issue}, "a")
	require.ErrorIs(t, err, storage.ErrPrefixMismatch)
	assert.Empty(t, issues.calls)
}

func TestCreateIssuesPersistsDependenciesQuietly(t *testing.T) {
	for _, viaTx := range []bool{false, true} {
		s, p := newFakeStore(t)
		issues := newFakeIssues()
		deps := &fakeDeps{}
		p.uw.issues, p.uw.deps = issues, deps
		issues.issues["bd-9"] = &types.Issue{ID: "bd-9"}
		dep := &types.Dependency{DependsOnID: "bd-9", Type: types.DepBlocks}
		a, b := task("a"), task("b")
		a.Dependencies = []*types.Dependency{dep}
		batch := []*types.Issue{a, b}
		if viaTx {
			require.NoError(t, inTx(s, func(ctx context.Context, tx storage.Transaction) error {
				return tx.CreateIssues(ctx, batch, "a")
			}))
		} else {
			require.NoError(t, s.CreateIssues(context.Background(), batch, "a"))
			assert.Equal(t, []string{"bd: create 2 issue(s)"}, p.uw.commits)
		}
		require.Equal(t, []*types.Dependency{dep}, deps.added, "viaTx=%v", viaTx)
		assert.Equal(t, a.ID, dep.IssueID, "viaTx=%v: an empty dep source is the issue's own id", viaTx)
		assert.True(t, deps.addOpts[0].SuppressEvents, "viaTx=%v: create-with-deps writes no dependency_added row", viaTx)
	}
}

func TestCreateIssuesRefusesMixedBucketDependency(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	p.uw.issues, p.uw.deps = issues, &fakeDeps{}
	durable, wisp := task("d"), task("w")
	durable.ID, wisp.ID = "bd-1", "bd-2"
	wisp.Ephemeral = true
	durable.Dependencies = []*types.Dependency{{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks}}
	err := s.CreateIssues(context.Background(), []*types.Issue{durable, wisp}, "a")
	require.Error(t, err)
	assert.Empty(t, issues.calls)
}

// A create that did not commit hands back the struct as passed in; one whose
// commit outcome is unknown keeps its IDs and is not replayed.
func TestFlatCreateResetsIDsUnlessIndeterminate(t *testing.T) {
	t.Run("definite failure", func(t *testing.T) {
		s, p := newFakeStore(t)
		p.uw.issues = newFakeIssues()
		p.uw.commitErr = &mysql.MySQLError{Number: 1105, Message: "rejected"}
		comment := &types.Comment{Author: "a", Text: "c"}
		issue := task("t")
		issue.Comments = []*types.Comment{comment}
		require.Error(t, s.CreateIssue(context.Background(), issue, "a"))
		assert.Empty(t, issue.ID)
		assert.Empty(t, comment.ID)
	})
	t.Run("batch definite failure keeps preset ids", func(t *testing.T) {
		s, p := newFakeStore(t)
		p.uw.issues = newFakeIssues()
		p.uw.deps = &fakeDeps{}
		p.uw.commitErr = &mysql.MySQLError{Number: 1105, Message: "rejected"}
		named, minted := task("n"), task("m")
		named.ID = "bd-42"
		dep := &types.Dependency{DependsOnID: "bd-42", Type: types.DepBlocks}
		minted.Dependencies = []*types.Dependency{dep}
		require.Error(t, s.CreateIssues(context.Background(), []*types.Issue{named, minted}, "a"))
		assert.Equal(t, "bd-42", named.ID)
		assert.Empty(t, minted.ID)
		assert.Empty(t, dep.IssueID, "the dep source backfill is undone too")
	})
	t.Run("indeterminate", func(t *testing.T) {
		s, p := newFakeStore(t)
		p.uw.issues = newFakeIssues()
		p.uw.commitErr = driver.ErrBadConn
		issue := task("t")
		err := s.CreateIssue(context.Background(), issue, "a")
		require.ErrorIs(t, err, storage.ErrCommitIndeterminate)
		assert.Equal(t, "bd-1", issue.ID)
		assert.Len(t, p.uw.commits, 1, "an indeterminate commit is never replayed")
	})
	t.Run("serialization failure is replayed", func(t *testing.T) {
		s, p := newFakeStore(t)
		issues := newFakeIssues()
		p.uw.issues = issues
		p.uw.commitErrs = []error{&mysql.MySQLError{Number: 1213, Message: "deadlock"}}
		issue := task("t")
		require.NoError(t, s.CreateIssue(context.Background(), issue, "a"))
		assert.Len(t, p.uw.commits, 2)
		require.Len(t, issues.params, 2)
		assert.Equal(t, "bd-2", issue.ID, "the replay minted afresh instead of reusing the rolled-back id")
	})
}

func TestDependencyWritesAreQuietByDefault(t *testing.T) {
	type op struct {
		name string
		run  func(s *Store) error
	}
	ctx := context.Background()
	dep := func() *types.Dependency {
		return &types.Dependency{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks}
	}
	for _, emit := range []bool{false, true} {
		addOpts := storage.DependencyAddOptions{EmitEvent: emit}
		remOpts := storage.DependencyRemoveOptions{EmitEvent: emit}
		ops := []op{
			{"flat add", func(s *Store) error { return s.AddDependencyWithOptions(ctx, dep(), "a", addOpts) }},
			{"flat remove", func(s *Store) error { return s.RemoveDependencyWithOptions(ctx, "bd-1", "bd-2", "a", remOpts) }},
			{"tx add", func(s *Store) error {
				return inTx(s, func(ctx context.Context, tx storage.Transaction) error {
					return tx.AddDependencyWithOptions(ctx, dep(), "a", addOpts)
				})
			}},
			{"tx remove", func(s *Store) error {
				return inTx(s, func(ctx context.Context, tx storage.Transaction) error {
					return tx.RemoveDependencyWithOptions(ctx, "bd-1", "bd-2", "a", remOpts)
				})
			}},
		}
		if !emit {
			ops = append(ops,
				op{"flat add default", func(s *Store) error { return s.AddDependency(ctx, dep(), "a") }},
				op{"flat remove default", func(s *Store) error { return s.RemoveDependency(ctx, "bd-1", "bd-2", "a") }},
				op{"tx add default", func(s *Store) error {
					return inTx(s, func(ctx context.Context, tx storage.Transaction) error { return tx.AddDependency(ctx, dep(), "a") })
				}},
				op{"tx remove default", func(s *Store) error {
					return inTx(s, func(ctx context.Context, tx storage.Transaction) error {
						return tx.RemoveDependency(ctx, "bd-1", "bd-2", "a")
					})
				}},
			)
		}
		for _, o := range ops {
			s, p := newFakeStore(t)
			deps := &fakeDeps{}
			p.uw.deps = deps
			require.NoError(t, o.run(s), o.name)
			switch {
			case len(deps.addOpts) == 1:
				assert.Equal(t, !emit, deps.addOpts[0].SuppressEvents, "%s emit=%v", o.name, emit)
			case len(deps.remOpts) == 1:
				assert.Equal(t, !emit, deps.remOpts[0].SuppressEvent, "%s emit=%v", o.name, emit)
			default:
				t.Errorf("%s emit=%v: removal did not honor its options: %v", o.name, emit, deps.removed)
			}
		}
	}
}

// A role handed out before Close must refuse afterwards rather than reach the
// provider whose pool Close released.
func TestRoleHeldPastCloseRefuses(t *testing.T) {
	s, p := newFakeStore(t)
	p.uw.issues = newFakeIssues()
	reader, err := s.IssueReader()
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = reader.Get(context.Background(), issueops.GetRequest{ID: "bd-1"})
	require.ErrorIs(t, err, ErrStoreClosed)
	assert.Equal(t, 0, p.newUOWCalls)
}

func TestCloseRacesReadersSafely(t *testing.T) {
	s, p := newFakeStore(t)
	p.uw.issues = newFakeIssues()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.UnderlyingDB()
				_, _ = s.GetIssue(context.Background(), "bd-1")
			}
		}()
	}
	require.NoError(t, s.Close())
	wg.Wait()
	_, err := s.GetIssue(context.Background(), "bd-1")
	assert.True(t, errors.Is(err, ErrStoreClosed))
}
