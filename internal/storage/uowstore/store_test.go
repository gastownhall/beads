package uowstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/journalops"
)

// fakeUOW is a unit of work whose use cases are the fakes a test installs.
// Any use case a test did not install is a nil interface, so an unexpected
// call panics loudly.
type fakeUOW struct {
	uow.UnitOfWork

	mu         sync.Mutex
	commits    []string
	commitErr  error
	commitErrs []error // consumed one per Commit before commitErr applies
	closes     int
	closeCtxOK []bool

	issues domain.IssueUseCase
	labels domain.LabelUseCase
	deps   domain.DependencyUseCase
	config domain.ConfigUseCase
}

func (u *fakeUOW) Commit(_ context.Context, msg string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.commits = append(u.commits, msg)
	if len(u.commitErrs) > 0 {
		err := u.commitErrs[0]
		u.commitErrs = u.commitErrs[1:]
		return err
	}
	return u.commitErr
}

func (u *fakeUOW) Close(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closes++
	u.closeCtxOK = append(u.closeCtxOK, ctx.Err() == nil)
}

func (u *fakeUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u *fakeUOW) LabelUseCase() domain.LabelUseCase           { return u.labels }
func (u *fakeUOW) DependencyUseCase() domain.DependencyUseCase { return u.deps }
func (u *fakeUOW) CommentUseCase() domain.CommentUseCase       { return nil }

// ConfigUseCase defaults to a "bd"-prefixed workspace with no custom or infra
// types, so a create test need not install one.
func (u *fakeUOW) ConfigUseCase() domain.ConfigUseCase {
	if u.config == nil {
		return &fakeConfig{cc: domain.CreateContext{IssuePrefix: "bd"}}
	}
	return u.config
}

type fakeConfig struct {
	domain.ConfigUseCase
	cc domain.CreateContext
}

func (f *fakeConfig) LoadCreateContext(context.Context) (domain.CreateContext, error) {
	return f.cc, nil
}

// fakeProvider hands out one fakeUOW, failing the first setupFailures
// NewUOW calls with setupErr.
type fakeProvider struct {
	mu            sync.Mutex
	uw            *fakeUOW
	newUOWCalls   int
	setupFailures int
	setupErr      error
	closeCalls    int
	db            *sql.DB
}

func (p *fakeProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.newUOWCalls++
	if p.newUOWCalls <= p.setupFailures {
		return nil, p.setupErr
	}
	return p.uw, nil
}

func (p *fakeProvider) Close(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCalls++
	return nil
}

func (p *fakeProvider) UnderlyingDB() *sql.DB { return p.db }

func newFakeStore(t *testing.T) (*Store, *fakeProvider) {
	t.Helper()
	p := &fakeProvider{uw: &fakeUOW{}}
	s, err := New(p)
	require.NoError(t, err)
	return s, p
}

// fakeIssues records which plane each write reached.
type fakeIssues struct {
	domain.IssueUseCase

	issues map[string]*types.Issue
	wisps  map[string]*types.Issue
	calls  []string
	params []domain.CreateIssueParams
	// copyOnCreate makes CreateIssue return a copy instead of minting into
	// the caller's struct, to prove the adapter writes the id back itself.
	copyOnCreate bool
}

func newFakeIssues() *fakeIssues {
	return &fakeIssues{issues: map[string]*types.Issue{}, wisps: map[string]*types.Issue{}}
}

func (f *fakeIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if is, ok := f.issues[id]; ok {
		return is, nil
	}
	return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, id)
}

func (f *fakeIssues) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	if is, ok := f.wisps[id]; ok {
		return is, nil
	}
	return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, id)
}

// create mints like the use case (a caller-named id is kept) and, like it,
// replaces issue.Comments with inserted copies carrying minted ids.
func (f *fakeIssues) create(params domain.CreateIssueParams, plane map[string]*types.Issue, prefix string) domain.CreateIssueResult {
	f.params = append(f.params, params)
	id := fmt.Sprintf("%s-%d", prefix, len(plane)+1)
	if params.Issue.ID != "" {
		id = params.Issue.ID
	}
	if len(params.Comments) > 0 {
		params.Issue.Comments = nil
		for i, c := range params.Comments {
			cp := *c
			cp.ID = fmt.Sprintf("c-%s-%d", id, i+1)
			params.Issue.Comments = append(params.Issue.Comments, &cp)
		}
	}
	if f.copyOnCreate {
		cp := *params.Issue
		cp.ID = id
		plane[id] = &cp
		return domain.CreateIssueResult{Issue: &cp}
	}
	params.Issue.ID = id
	plane[id] = params.Issue
	return domain.CreateIssueResult{Issue: params.Issue}
}

func (f *fakeIssues) CreateIssue(_ context.Context, params domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	f.calls = append(f.calls, "CreateIssue")
	return f.create(params, f.issues, "bd"), nil
}

func (f *fakeIssues) CreateWisp(_ context.Context, params domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	f.calls = append(f.calls, "CreateWisp")
	return f.create(params, f.wisps, "wisp"), nil
}

func (f *fakeIssues) UpdateIssue(_ context.Context, id string, _ map[string]any, _ string) error {
	f.calls = append(f.calls, "UpdateIssue "+id)
	return nil
}

func (f *fakeIssues) UpdateWisp(_ context.Context, id string, _ map[string]any, _ string) error {
	f.calls = append(f.calls, "UpdateWisp "+id)
	return nil
}

func (f *fakeIssues) CloseIssue(_ context.Context, id string, _ domain.CloseIssueParams, _ string) (domain.CloseIssueResult, error) {
	f.calls = append(f.calls, "CloseIssue "+id)
	return domain.CloseIssueResult{}, nil
}

func (f *fakeIssues) CloseWisp(_ context.Context, id string, _ domain.CloseIssueParams, _ string) (domain.CloseIssueResult, error) {
	f.calls = append(f.calls, "CloseWisp "+id)
	return domain.CloseIssueResult{}, nil
}

// GetMany answers from both planes in the request's order and records each
// request. Like the shared body it refuses a request over
// publicops.MaxGetManyIDs or holding an empty id.
func (f *fakeIssues) GetMany(_ context.Context, request publicops.GetManyRequest) (publicops.GetManyResult, error) {
	f.calls = append(f.calls, fmt.Sprintf("GetMany %d", len(request.IDs)))
	if len(request.IDs) > publicops.MaxGetManyIDs {
		return publicops.GetManyResult{}, &publicops.TooManyIDsError{Requested: len(request.IDs), Cap: publicops.MaxGetManyIDs}
	}
	result := publicops.GetManyResult{Issues: []*types.Issue{}, Missing: []string{}}
	for _, id := range request.IDs {
		if id == "" {
			return publicops.GetManyResult{}, fmt.Errorf("%w: empty id", storage.ErrValidation)
		}
		if is, ok := f.issues[id]; ok {
			result.Issues = append(result.Issues, is)
		} else if is, ok := f.wisps[id]; ok {
			result.Issues = append(result.Issues, is)
		} else {
			result.Missing = append(result.Missing, id)
		}
	}
	return result, nil
}

type fakeLabels struct {
	domain.LabelUseCase
	calls []string
	// issueLabels and wispLabels answer GetLabels and GetWispLabels.
	issueLabels map[string][]string
	wispLabels  map[string][]string
}

func (f *fakeLabels) GetLabels(_ context.Context, id string) ([]string, error) {
	f.calls = append(f.calls, "GetLabels "+id)
	return f.issueLabels[id], nil
}

func (f *fakeLabels) GetWispLabels(_ context.Context, id string) ([]string, error) {
	f.calls = append(f.calls, "GetWispLabels "+id)
	return f.wispLabels[id], nil
}

func (f *fakeLabels) AddLabel(_ context.Context, id, label, _ string) error {
	f.calls = append(f.calls, "AddLabel "+id+" "+label)
	return nil
}

func (f *fakeLabels) AddWispLabel(_ context.Context, id, label, _ string) error {
	f.calls = append(f.calls, "AddWispLabel "+id+" "+label)
	return nil
}

func TestStoreImplementsStorage(t *testing.T) {
	var s any = &Store{}
	_, ok := s.(storage.Storage)
	assert.True(t, ok, "Storage")
	_, ok = s.(storage.DoltStorage)
	assert.False(t, ok, "*Store must not claim the full DoltStorage surface")
	_, ok = s.(storage.EventsJournalConfigurer)
	assert.True(t, ok, "SetEventsJournalEnabled")
	_, ok = s.(journalops.Journal)
	assert.True(t, ok, "ReadEventsJournalPage")
	_, ok = s.(interface {
		IsBlocked(context.Context, string) (bool, []string, error)
		IsBlockedBatch(context.Context, []string) (map[string]bool, error)
	})
	assert.True(t, ok, "BlockedQuerier")
	_, ok = s.(interface {
		ClaimIssue(context.Context, string, string) error
		ClaimReadyIssue(context.Context, types.WorkFilter, string) (*types.Issue, error)
	})
	assert.True(t, ok, "IssueClaimer")
	_, ok = s.(interface{ IsClosed() bool })
	assert.True(t, ok, "IsClosed")
	_, ok = s.(interface{ UnderlyingDB() *sql.DB })
	assert.True(t, ok, "UnderlyingDB")
	_, ok = s.(interface{ DB() *sql.DB })
	assert.True(t, ok, "DB")
}

// TestEveryRoleAccessorIsServed pins that none of the 33 role accessors is a
// stub: each returns a role and no error over a bare provider.
func TestEveryRoleAccessorIsServed(t *testing.T) {
	s, _ := newFakeStore(t)
	iface := reflect.TypeOf((*storage.Storage)(nil)).Elem()
	errType := reflect.TypeOf((*error)(nil)).Elem()
	roles := 0
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		if m.Type.NumIn() != 0 || m.Type.NumOut() != 2 || m.Type.Out(1) != errType {
			continue
		}
		roles++
		out := reflect.ValueOf(s).MethodByName(m.Name).Call(nil)
		require.True(t, out[1].IsNil(), "%s: %v", m.Name, out[1].Interface())
		require.False(t, out[0].IsNil(), "%s returned a nil role", m.Name)
	}
	assert.Equal(t, 33, roles)
}

func TestRunInTransactionInvokesCallbackOnce(t *testing.T) {
	s, p := newFakeStore(t)
	p.uw.commitErr = &mysql.MySQLError{Number: 1213, Message: "serialization failure"}
	calls := 0
	err := s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error {
		calls++
		return nil
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "a commit-time serialization failure must not replay the callback")
	assert.NotErrorIs(t, err, storage.ErrCommitIndeterminate, "a decoded server error is a definite outcome")
	var mysqlErr *mysql.MySQLError
	assert.ErrorAs(t, err, &mysqlErr)
	assert.Equal(t, 1, p.uw.closes)
}

func TestRunInTransactionRollsBackOnError(t *testing.T) {
	s, p := newFakeStore(t)
	sentinel := errors.New("callback failed")
	err := s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error { return sentinel })
	assert.Same(t, sentinel, err, "fn's error is returned unchanged")
	assert.Empty(t, p.uw.commits)
	assert.Equal(t, 1, p.uw.closes)
}

func TestRunInTransactionRollsBackOnPanic(t *testing.T) {
	s, p := newFakeStore(t)
	assert.PanicsWithValue(t, "boom", func() {
		_ = s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error { panic("boom") })
	})
	assert.Empty(t, p.uw.commits)
	assert.Equal(t, 1, p.uw.closes)
}

func TestRunInTransactionRollsBackOnCanceledContext(t *testing.T) {
	s, p := newFakeStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := s.RunInTransaction(ctx, "msg", func(storage.Transaction) error {
		cancel()
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, p.uw.closes)
	assert.True(t, p.uw.closeCtxOK[0], "rollback must run on a live context after the caller canceled")
}

func TestRunInTransactionEmptyMessageCommitsWithoutDoltCommit(t *testing.T) {
	s, p := newFakeStore(t)
	require.NoError(t, s.RunInTransaction(context.Background(), "", func(storage.Transaction) error { return nil }))
	assert.Equal(t, []string{""}, p.uw.commits, "an empty message is still committed (plain SQL COMMIT), not skipped")
}

func TestRunInTransactionPassesMessage(t *testing.T) {
	s, p := newFakeStore(t)
	require.NoError(t, s.RunInTransaction(context.Background(), "gc: sling", func(storage.Transaction) error { return nil }))
	assert.Equal(t, []string{"gc: sling"}, p.uw.commits)
	assert.Equal(t, 1, p.uw.closes)
}

func TestRunInTransactionRetriesOnlySetup(t *testing.T) {
	s, p := newFakeStore(t)
	p.setupFailures = 2
	p.setupErr = driver.ErrBadConn
	calls := 0
	require.NoError(t, s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error {
		calls++
		return nil
	}))
	assert.Equal(t, 3, p.newUOWCalls)
	assert.Equal(t, 1, calls)
}

func TestRunInTransactionDoesNotRetryPermanentSetupFailure(t *testing.T) {
	s, p := newFakeStore(t)
	p.setupFailures = 1
	p.setupErr = errors.New("permission denied")
	err := s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error {
		t.Fatal("callback must not run")
		return nil
	})
	require.ErrorIs(t, err, p.setupErr)
	assert.Equal(t, 1, p.newUOWCalls)
}

func TestRunInTransactionMarksLostCommitIndeterminate(t *testing.T) {
	s, p := newFakeStore(t)
	p.uw.commitErr = io.ErrUnexpectedEOF
	err := s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error { return nil })
	require.ErrorIs(t, err, storage.ErrCommitIndeterminate)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestTxCreateIssueWritesBackID(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	issues.copyOnCreate = true
	p.uw.issues = issues
	issue := &types.Issue{Title: "t", IssueType: types.TypeTask}
	require.NoError(t, s.RunInTransaction(context.Background(), "msg", func(tx storage.Transaction) error {
		return tx.CreateIssue(context.Background(), issue, "actor")
	}))
	assert.Equal(t, "bd-1", issue.ID)
}

func TestTxRoutesWispAndIssuePlanes(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	labels := &fakeLabels{}
	p.uw.issues, p.uw.labels = issues, labels
	issues.issues["bd-9"] = &types.Issue{ID: "bd-9"}
	issues.wisps["wisp-9"] = &types.Issue{ID: "wisp-9", Ephemeral: true}

	require.NoError(t, s.RunInTransaction(context.Background(), "msg", func(tx storage.Transaction) error {
		ctx := context.Background()
		created := &types.Issue{Title: "w", IssueType: types.TypeTask, Ephemeral: true}
		if err := tx.CreateIssue(ctx, created, "a"); err != nil {
			return err
		}
		for _, id := range []string{"bd-9", "wisp-9", created.ID} {
			if err := tx.UpdateIssue(ctx, id, map[string]interface{}{"title": "x"}, "a"); err != nil {
				return err
			}
			if err := tx.CloseIssue(ctx, id, "done", "a", ""); err != nil {
				return err
			}
			if err := tx.AddLabel(ctx, id, "l", "a"); err != nil {
				return err
			}
		}
		return nil
	}))
	assert.Equal(t, []string{
		"CreateWisp",
		"UpdateIssue bd-9", "CloseIssue bd-9",
		"UpdateWisp wisp-9", "CloseWisp wisp-9",
		"UpdateWisp wisp-2", "CloseWisp wisp-2",
	}, issues.calls)
	assert.Equal(t, []string{"AddLabel bd-9 l", "AddWispLabel wisp-9 l", "AddWispLabel wisp-2 l"}, labels.calls)
}

func TestTxMissingIssueIsNotFound(t *testing.T) {
	s, p := newFakeStore(t)
	p.uw.issues = newFakeIssues()
	err := s.RunInTransaction(context.Background(), "msg", func(tx storage.Transaction) error {
		return tx.UpdateIssue(context.Background(), "bd-404", map[string]interface{}{}, "a")
	})
	require.ErrorIs(t, err, storage.ErrNotFound)
	assert.Empty(t, p.uw.commits)
}

// TestUnsupportedIsTyped walks the generated shell's whole method set, so a
// stub added by a later regeneration is covered without editing this test.
func TestUnsupportedIsTyped(t *testing.T) {
	shell := reflect.ValueOf(unsupportedStorage{})
	require.Positive(t, shell.NumMethod())
	for i := 0; i < shell.NumMethod(); i++ {
		m := shell.Type().Method(i)
		fn := shell.Method(i)
		args := make([]reflect.Value, fn.Type().NumIn())
		for a := range args {
			args[a] = reflect.Zero(fn.Type().In(a))
		}
		out := fn.Call(args)
		err, _ := out[len(out)-1].Interface().(error)
		var unsup *storage.ErrUnsupported
		require.ErrorAs(t, err, &unsup, m.Name)
		assert.Equal(t, Backend, unsup.Backend, m.Name)
		assert.Equal(t, m.Name, unsup.Op)
	}
}

func TestCloseIsIdempotentAndDoesNotStopProxy(t *testing.T) {
	s, p := newFakeStore(t)
	p.db = &sql.DB{}
	assert.Same(t, p.db, s.UnderlyingDB())
	assert.Same(t, p.db, s.DB())

	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	assert.Equal(t, 1, p.closeCalls, "Close closes the provider exactly once and signals nothing else")
	assert.True(t, s.IsClosed())

	_, err := s.GetIssue(context.Background(), "bd-1")
	require.ErrorIs(t, err, ErrStoreClosed)
	err = s.RunInTransaction(context.Background(), "msg", func(storage.Transaction) error { return nil })
	require.ErrorIs(t, err, ErrStoreClosed)
	_, err = s.IssueReader()
	require.ErrorIs(t, err, ErrStoreClosed)
	assert.Equal(t, 0, p.newUOWCalls, "no unit of work may begin after Close")
	assert.Nil(t, s.UnderlyingDB())
}

func TestFlatCreateIssueMintsAndCommitsWithItsID(t *testing.T) {
	s, p := newFakeStore(t)
	p.uw.issues = newFakeIssues()
	issue := &types.Issue{Title: "t", IssueType: types.TypeTask}
	require.NoError(t, s.CreateIssue(context.Background(), issue, "a"))
	assert.Equal(t, "bd-1", issue.ID)
	assert.Equal(t, []string{"bd: create bd-1"}, p.uw.commits)
}

// TestFlatGetIssueFillsLabelsFromItsPlane pins that the flat GetIssue
// returns the issue's labels, read from the plane the issue lives in, in the
// one unit of work, as DoltStore's GetIssue does (the transaction's GetIssue
// reads none, as DoltStore's transaction does not).
func TestFlatGetIssueFillsLabelsFromItsPlane(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	labels := &fakeLabels{
		issueLabels: map[string][]string{"bd-1": {"kept"}},
		wispLabels:  map[string][]string{"wisp-1": {"ephemeral"}},
	}
	p.uw.issues, p.uw.labels = issues, labels
	issues.issues["bd-1"] = &types.Issue{ID: "bd-1"}
	issues.wisps["wisp-1"] = &types.Issue{ID: "wisp-1", Ephemeral: true}

	got, err := s.GetIssue(context.Background(), "bd-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"kept"}, got.Labels)
	got, err = s.GetIssue(context.Background(), "wisp-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"ephemeral"}, got.Labels)
	assert.Equal(t, []string{"GetLabels bd-1", "GetWispLabels wisp-1"}, labels.calls)
	assert.Equal(t, 2, p.newUOWCalls, "each GetIssue is one unit of work")

	_, err = s.GetIssue(context.Background(), "bd-404")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

// TestFlatGetIssuesByIDsReadsBothPlanesInChunks pins that the flat
// GetIssuesByIDs goes through the both-plane batch read (GetMany) in one unit
// of work, and accepts what DoltStore's accepts: more ids than one GetMany
// request may carry are read in chunks, and an empty id is skipped rather
// than refused. Duplicates collapse to the first mention, and an id that
// names no row is left out.
func TestFlatGetIssuesByIDsReadsBothPlanesInChunks(t *testing.T) {
	s, p := newFakeStore(t)
	issues := newFakeIssues()
	p.uw.issues = issues
	issues.wisps["wisp-1"] = &types.Issue{ID: "wisp-1", Ephemeral: true}
	ids := []string{"wisp-1", ""}
	for i := 0; i <= publicops.MaxGetManyIDs; i++ {
		id := fmt.Sprintf("bd-%d", i)
		issues.issues[id] = &types.Issue{ID: id}
		ids = append(ids, id)
	}
	ids = append(ids, "bd-missing", "wisp-1", "bd-0")

	got, err := s.GetIssuesByIDs(context.Background(), ids)
	require.NoError(t, err)
	assert.Equal(t, []string{fmt.Sprintf("GetMany %d", publicops.MaxGetManyIDs), "GetMany 3"}, issues.calls)
	assert.Equal(t, 1, p.newUOWCalls, "every chunk is read in the one unit of work")
	require.Len(t, got, publicops.MaxGetManyIDs+2)
	assert.Equal(t, "wisp-1", got[0].ID)
	assert.Equal(t, "bd-0", got[1].ID)
	assert.Equal(t, fmt.Sprintf("bd-%d", publicops.MaxGetManyIDs), got[len(got)-1].ID)

	got, err = s.GetIssuesByIDs(context.Background(), []string{"", ""})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, 1, p.newUOWCalls, "nothing to read opens no unit of work")
}
