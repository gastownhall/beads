package externaldeps

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

type fakeUOWProvider struct {
	uow.UnitOfWorkProvider
	uw uow.UnitOfWork
}

func (p *fakeUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return p.uw, nil }

type fakeUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
	deps   domain.DependencyUseCase
}

func (u *fakeUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u *fakeUOW) DependencyUseCase() domain.DependencyUseCase { return u.deps }

// Close lets a role run over the fake: RunTxRead closes each unit of work it
// opens.
func (u *fakeUOW) Close(context.Context) {}

type fakeIssueUseCase struct {
	domain.IssueUseCase
	ready   []*types.Issue
	wisps   []*types.Issue
	blocked []*types.BlockedIssue
	closed  []string
}

func (u *fakeIssueUseCase) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range u.ready {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, nil
}

func (u *fakeIssueUseCase) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range u.wisps {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, nil
}

// GetMany answers both planes, as ExecuteGetMany does, each row with the
// labels it was seeded with (GetMany's one hydration).
func (u *fakeIssueUseCase) GetMany(ctx context.Context, request publicops.GetManyRequest) (publicops.GetManyResult, error) {
	result := publicops.GetManyResult{Issues: []*publicops.Issue{}, Missing: []string{}}
	for _, id := range request.IDs {
		issue, err := u.GetIssue(ctx, id)
		if err == nil && issue == nil {
			issue, err = u.GetWisp(ctx, id)
		}
		if err != nil {
			return publicops.GetManyResult{}, err
		}
		if issue == nil {
			result.Missing = append(result.Missing, id)
			continue
		}
		result.Issues = append(result.Issues, issue)
	}
	return result, nil
}

func (u *fakeIssueUseCase) CloseIssueChecked(_ context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	u.closed = append(u.closed, id)
	return domain.CloseIssueResult{}, nil
}

func (u *fakeIssueUseCase) CloseWispChecked(_ context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	u.closed = append(u.closed, id)
	return domain.CloseIssueResult{}, nil
}

func (u *fakeIssueUseCase) ApplyUpdate(_ context.Context, id string, spec domain.UpdateSpec, _ string) (*types.Issue, error) {
	u.closed = append(u.closed, id)
	status, _ := spec.Fields["status"].(string)
	return &types.Issue{ID: id, Status: types.Status(status)}, nil
}

func (u *fakeIssueUseCase) GetReadyWork(_ context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	items := make([]*types.Issue, 0, len(u.ready))
	for _, issue := range u.ready {
		if !slices.Contains(filter.ExcludeIDs, issue.ID) {
			items = append(items, issue)
		}
	}
	return domain.SearchPage{Items: items}, nil
}

func (u *fakeIssueUseCase) GetBlockedIssues(_ context.Context, _ types.WorkFilter) ([]*types.BlockedIssue, error) {
	return slices.Clone(u.blocked), nil
}

type fakeDependencyUseCase struct {
	domain.DependencyUseCase
	external map[string][]*types.Dependency
	records  map[string][]*types.Dependency
	wispDeps map[string][]*types.Dependency
	// isBlocked is the stored is_blocked column, per row; an id it does not
	// name has no row.
	isBlocked map[string]bool
}

func (u *fakeDependencyUseCase) GetBlockingInfo(context.Context, []string) (domain.BlockingInfo, error) {
	return domain.BlockingInfo{}, nil
}

func (u *fakeDependencyUseCase) IsBlockedBatch(_ context.Context, ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		if blocked, ok := u.isBlocked[id]; ok {
			result[id] = blocked
		}
	}
	return result, nil
}

func (u *fakeDependencyUseCase) GetExternalBlockingDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	return u.external, nil
}

func (u *fakeDependencyUseCase) GetIssueDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		result[id] = u.records[id]
	}
	return result, nil
}

func (u *fakeDependencyUseCase) GetWispDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		result[id] = u.wispDeps[id]
	}
	return result, nil
}

func TestWrapUOWProviderFiltersProxiedReadyWork(t *testing.T) {
	blocked, ready := issue("be-blocked"), issue("be-ready")
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, ready}},
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	got, err := uw.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := issueIDs(got.Items); !slices.Equal(ids, []string{ready.ID}) {
		t.Fatalf("proxied ready IDs = %v, want [%s]", ids, ready.ID)
	}
}

// TestWrapUOWProviderBlockedFlagAgreesWithReadyWork: the stored is_blocked
// column has no external term, so a row whose only blocker is an unsatisfied
// external ref reads false there. bd serve answers the served flag on this
// arm, so the flag must call that row blocked, as GetReadyWork does, without
// naming a row the column did not answer: neither an orphan edge's source nor
// an unasked row.
func TestWrapUOWProviderBlockedFlagAgreesWithReadyWork(t *testing.T) {
	blocked, free, elsewhere := issue("be-blocked"), issue("be-free"), issue("be-elsewhere")
	const ghost = "be-ghost"
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, free, elsewhere}},
		deps: &fakeDependencyUseCase{
			external: map[string][]*types.Dependency{
				blocked.ID:   {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
				elsewhere.ID: {externalDep(elsewhere.ID, "external:remote:payments", types.DepBlocks)},
				ghost:        {externalDep(ghost, "external:remote:payments", types.DepBlocks)},
			},
			isBlocked: map[string]bool{blocked.ID: false, free.ID: false, elsewhere.ID: false},
		},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	ready, err := uw.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := issueIDs(ready.Items); !slices.Equal(ids, []string{free.ID}) {
		t.Fatalf("proxied ready IDs = %v, want [%s]", ids, free.ID)
	}

	asked := []string{blocked.ID, free.ID, ghost}
	flags, err := uw.DependencyUseCase().IsBlockedBatch(t.Context(), asked)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	if want := map[string]bool{blocked.ID: true, free.ID: false}; !maps.Equal(flags, want) {
		t.Errorf("IsBlockedBatch = %v, want %v", flags, want)
	}

	annotator, err := provider.(uow.BlockingAnnotatorSource).BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}
	result, err := annotator.AnnotateBlocking(t.Context(), publicops.BlockingRequest{IDs: asked, IncludeIsBlocked: true})
	if err != nil {
		t.Fatalf("AnnotateBlocking: %v", err)
	}
	if got, want := blockedFlags(result), map[string]string{blocked.ID: "true", free.ID: "false", ghost: "absent"}; !maps.Equal(got, want) {
		t.Errorf("AnnotateBlocking is_blocked = %v, want %v", got, want)
	}
}

// TestWrapUOWProviderBlockedFlagResolvesOnlyTheAskedRefs: a client asks for
// the served flag in chunks, so unlike Ready, which must resolve every
// external ref in the workspace, IsBlockedBatch opens only the foreign
// projects its asked rows name, neither an unasked row's nor an orphan
// edge's.
func TestWrapUOWProviderBlockedFlagResolvesOnlyTheAskedRefs(t *testing.T) {
	blocked, free, elsewhere := issue("be-blocked"), issue("be-free"), issue("be-elsewhere")
	// shipped's external blocker is SATISFIED: the foreign project's labelled
	// index carries a closed provider of the capability. Its record is answered
	// and resolved, and it must still read unblocked — the half of the
	// predicate a "raise every answered external record" body would break.
	shipped := issue("be-shipped")
	const ghost = "be-ghost"
	inner := &fakeUOW{deps: &fakeDependencyUseCase{
		external: map[string][]*types.Dependency{
			blocked.ID:   {externalDep(blocked.ID, "external:remote:invoices", types.DepBlocks)},
			shipped.ID:   {externalDep(shipped.ID, "external:remote:payments", types.DepBlocks)},
			elsewhere.ID: {externalDep(elsewhere.ID, "external:other:billing", types.DepBlocks)},
			ghost:        {externalDep(ghost, "external:orphan:audit", types.DepBlocks)},
		},
		isBlocked: map[string]bool{blocked.ID: false, free.ID: false, elsewhere.ID: false, shipped.ID: false},
	}}
	opens := make(map[string]int)
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner},
		func(project ProjectName) (string, bool) { return "/projects/" + string(project), true },
		func(_ context.Context, path string) (storage.DoltStorage, error) {
			opens[path]++
			return &fakeStore{labels: map[string][]*types.Issue{
				"provides:payments": {{ID: "remote-done", Status: types.StatusClosed}},
			}}, nil
		},
	)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}

	for _, tc := range []struct {
		asked     []string
		want      map[string]bool
		wantOpens map[string]int
	}{
		{asked: []string{free.ID, ghost}, want: map[string]bool{free.ID: false}, wantOpens: map[string]int{}},
		{
			asked:     []string{blocked.ID, free.ID},
			want:      map[string]bool{blocked.ID: true, free.ID: false},
			wantOpens: map[string]int{"/projects/remote": 1},
		},
		{
			asked:     []string{shipped.ID, free.ID},
			want:      map[string]bool{shipped.ID: false, free.ID: false},
			wantOpens: map[string]int{"/projects/remote": 1},
		},
		{
			asked:     []string{blocked.ID, shipped.ID},
			want:      map[string]bool{blocked.ID: true, shipped.ID: false},
			wantOpens: map[string]int{"/projects/remote": 1},
		},
	} {
		clear(opens)
		flags, err := uw.DependencyUseCase().IsBlockedBatch(t.Context(), tc.asked)
		if err != nil {
			t.Fatalf("IsBlockedBatch(%v): %v", tc.asked, err)
		}
		if !maps.Equal(flags, tc.want) {
			t.Errorf("IsBlockedBatch(%v) = %v, want %v", tc.asked, flags, tc.want)
		}
		if !maps.Equal(opens, tc.wantOpens) {
			t.Errorf("IsBlockedBatch(%v) opened %v, want %v", tc.asked, opens, tc.wantOpens)
		}
	}
}

// blockedFlags renders each annotated row's is_blocked flag, "absent" where
// the row carries none.
func blockedFlags(result publicops.BlockingResult) map[string]string {
	flags := make(map[string]string, len(result.Items))
	for _, item := range result.Items {
		flags[item.ID] = "absent"
		if item.IsBlocked != nil {
			flags[item.ID] = strconv.FormatBool(*item.IsBlocked)
		}
	}
	return flags
}

func TestWrapUOWProviderKeepsUnitOfWorkUnwrappable(t *testing.T) {
	inner := &fakeUOW{}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, nil, nil)
	wrapped, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}

	unwrapper, ok := wrapped.(interface{ Unwrap() uow.UnitOfWork })
	if !ok {
		t.Fatalf("wrapped UOW %T does not expose Unwrap", wrapped)
	}
	if got := unwrapper.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %T, want the wrapped UOW", got)
	}
}

func TestWrapUOWProviderPreservesIssueReader(t *testing.T) {
	inner := &fakeUOWProvider{uw: &fakeUOW{}}
	provider := WrapUOWProvider(inner, nil, nil)
	source, ok := provider.(uow.IssueReaderSource)
	if !ok {
		t.Fatalf("wrapped provider %T does not preserve IssueReaderSource", provider)
	}
	if reader, err := source.IssueReader(); err != nil || reader == nil {
		t.Fatalf("IssueReader() = %T, %v", reader, err)
	}
	if got := uow.UnwrapProvider(provider); got != inner {
		t.Fatalf("UnwrapProvider() = %T, want the wrapped provider", got)
	}
}

func TestWrapUOWProviderRefusesProxiedCheckedClose(t *testing.T) {
	blocked := issue("be-blocked")
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked}}
	inner := &fakeUOW{
		issues: issues,
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	_, err = uw.IssueUseCase().CloseIssueChecked(t.Context(), blocked.ID, domain.CloseIssueParams{}, "tester", false)
	if !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("CloseIssueChecked error = %v, want ErrCloseBlocked", err)
	}
	assertExternalCloseBlockers(t, err, blocked.ID, "external:remote:payments")
	if len(issues.closed) != 0 {
		t.Fatalf("inner close calls = %v, want none", issues.closed)
	}
}

func TestWrapUOWProviderFiltersExternalBlockedWorkByParent(t *testing.T) {
	parent, child := issue("be-parent"), issue("be-child")
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{child}},
		deps: &fakeDependencyUseCase{
			external: map[string][]*types.Dependency{
				child.ID: {externalDep(child.ID, "external:remote:payments", types.DepBlocks)},
			},
			records: map[string][]*types.Dependency{
				child.ID: {{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}},
			},
		},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	got, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{ParentID: &parent.ID})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if len(got) != 1 || got[0].ID != child.ID {
		t.Fatalf("blocked issues = %v, want [%s]", got, child.ID)
	}
}

func TestWrapUOWProviderGuardsDoneUpdatesAndWispCloses(t *testing.T) {
	durable, wisp := issue("be-durable"), issue("be-wisp")
	issues := &fakeIssueUseCase{ready: []*types.Issue{durable}, wisps: []*types.Issue{wisp}}
	inner := &fakeUOW{
		issues: issues,
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			durable.ID: {externalDep(durable.ID, "external:remote:payments", types.DepBlocks)},
			wisp.ID:    {externalDep(wisp.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	issueUC := uw.IssueUseCase()
	for _, id := range []string{durable.ID, wisp.ID} {
		if _, err := issueUC.ApplyUpdate(t.Context(), id, domain.UpdateSpec{Claim: true, Fields: map[string]any{"notes": "must not be written"}}, "tester"); !errors.Is(err, storage.ErrCloseBlocked) {
			t.Fatalf("ApplyUpdate claim %s: %v, want ErrCloseBlocked", id, err)
		}
	}
	if _, err := issueUC.ClaimIssue(t.Context(), durable.ID, "tester"); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("ClaimIssue error = %v, want ErrCloseBlocked", err)
	}
	if _, err := issueUC.ClaimWisp(t.Context(), wisp.ID, "tester"); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("ClaimWisp error = %v, want ErrCloseBlocked", err)
	}
	if _, err := issueUC.ApplyUpdate(t.Context(), durable.ID, domain.UpdateSpec{Fields: map[string]any{"status": string(types.StatusClosed)}}, "tester"); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("ApplyUpdate error = %v, want ErrCloseBlocked", err)
	}
	if _, err := issueUC.CloseWispChecked(t.Context(), wisp.ID, domain.CloseIssueParams{}, "tester", false); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("CloseWispChecked error = %v, want ErrCloseBlocked", err)
	}
	if len(issues.closed) != 0 {
		t.Fatalf("inner mutation calls = %v, want none", issues.closed)
	}
	blocked, err := issueUC.GetBlockedIssues(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if ids := blockedIssueIDs(blocked); !slices.Equal(ids, []string{durable.ID, wisp.ID}) {
		t.Fatalf("blocked IDs = %v, want [%s %s]", ids, durable.ID, wisp.ID)
	}
	durable.Status, wisp.Status = types.StatusClosed, types.StatusClosed
	if _, err := issueUC.CloseIssueChecked(t.Context(), durable.ID, domain.CloseIssueParams{}, "tester", false); err != nil {
		t.Fatalf("durable re-close: %v", err)
	}
	if _, err := issueUC.CloseWispChecked(t.Context(), wisp.ID, domain.CloseIssueParams{}, "tester", false); err != nil {
		t.Fatalf("wisp re-close: %v", err)
	}
	if !slices.Equal(issues.closed, []string{durable.ID, wisp.ID}) {
		t.Fatalf("re-closes did not reach the backend: %v", issues.closed)
	}
}

func TestWrapUOWProviderPaginatesCombinedExternalBlockedWork(t *testing.T) {
	local, external := issue("be-local"), issue("be-external")
	createdAt := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC)
	local.Priority, external.Priority = 0, 0
	local.CreatedAt, external.CreatedAt = createdAt, createdAt
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{
			ready:   []*types.Issue{external},
			blocked: []*types.BlockedIssue{{Issue: *local, BlockedBy: []string{"be-local-blocker"}, BlockedByCount: 1}},
		},
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			external.ID: {externalDep(external.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	first, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{Limit: 1})
	if err != nil {
		t.Fatalf("GetBlockedIssues first page: %v", err)
	}
	if ids := blockedIssueIDs(first); !slices.Equal(ids, []string{external.ID}) {
		t.Fatalf("first page IDs = %v, want [%s]", ids, external.ID)
	}
	second, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("GetBlockedIssues second page: %v", err)
	}
	if ids := blockedIssueIDs(second); !slices.Equal(ids, []string{local.ID}) {
		t.Fatalf("second page IDs = %v, want [%s]", ids, local.ID)
	}
	_, err = uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{MaxRows: 1, MaxRowsSource: "--max-rows"})
	var capErr *issueops.ErrTooManyRows
	if !errors.As(err, &capErr) {
		t.Fatalf("GetBlockedIssues MaxRows error = %v, want ErrTooManyRows", err)
	}
}

// fakeConfigurableProvider is a provider that implements BOTH optional
// activation configurers and records what reached it.
type fakeConfigurableProvider struct {
	uow.UnitOfWorkProvider
	journal bool
	version bool
}

func (p *fakeConfigurableProvider) SetEventsJournalEnabled(enabled bool) { p.journal = enabled }

func (p *fakeConfigurableProvider) SetVersionedHistoryEnabled(enabled bool) { p.version = enabled }

// TestWrapUOWProviderForwardsActivationConfigurers pins the whole optional
// activation family through this wrapper, not one member of it.
//
// uowProvider is the OUTERMOST provider on both real chains (cmd/bd/main.go
// and cmd/bd/serve.go each wrap everything else in
// wireExternalDependencyUOWProvider), and activation discovers its target by
// type-asserting that outermost value. A configurer the wrapper does not
// implement therefore fails the assertion on exactly the chains that matter,
// and any forwarder further in is unreachable — with no error and no log,
// because a missing optional-interface method is invisible to the compiler.
// The table is the point: the next configurer added to this family gets one
// row here and is covered, rather than shipping the same silent gap.
func TestWrapUOWProviderForwardsActivationConfigurers(t *testing.T) {
	cases := []struct {
		name   string
		enable func(uow.UnitOfWorkProvider) bool
		got    func(*fakeConfigurableProvider) bool
	}{
		{
			name: "EventsJournalConfigurer",
			enable: func(p uow.UnitOfWorkProvider) bool {
				configurer, ok := p.(storage.EventsJournalConfigurer)
				if ok {
					configurer.SetEventsJournalEnabled(true)
				}
				return ok
			},
			got: func(p *fakeConfigurableProvider) bool { return p.journal },
		},
		{
			name: "VersionedHistoryConfigurer",
			enable: func(p uow.UnitOfWorkProvider) bool {
				configurer, ok := p.(storage.VersionedHistoryConfigurer)
				if ok {
					configurer.SetVersionedHistoryEnabled(true)
				}
				return ok
			},
			got: func(p *fakeConfigurableProvider) bool { return p.version },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &fakeConfigurableProvider{}
			provider := WrapUOWProvider(inner, nil, nil)
			if !tc.enable(provider) {
				t.Fatalf("wrapped provider %T does not implement %s — activation type-asserts the OUTERMOST provider, so it would silently enable nothing", provider, tc.name)
			}
			if !tc.got(inner) {
				t.Fatalf("%s did not reach the inner provider through the wrapper", tc.name)
			}
		})
	}
}

// TestWrapUOWProviderToleratesInnerWithoutConfigurers pins the other half of
// the forwarding contract: an inner provider that implements neither
// configurer must be a silent no-op, not a panic. The wrapper still satisfies
// both interfaces (that is what makes activation reach it at all), so the
// inner type assertion is the only thing standing between a non-configurable
// store and a nil-method call.
func TestWrapUOWProviderToleratesInnerWithoutConfigurers(t *testing.T) {
	provider := WrapUOWProvider(&fakeUOWProvider{uw: &fakeUOW{}}, nil, nil)
	journal, ok := provider.(storage.EventsJournalConfigurer)
	if !ok {
		t.Fatalf("wrapped provider %T does not implement storage.EventsJournalConfigurer", provider)
	}
	version, ok := provider.(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("wrapped provider %T does not implement storage.VersionedHistoryConfigurer", provider)
	}
	journal.SetEventsJournalEnabled(true)
	version.SetVersionedHistoryEnabled(true)
}
