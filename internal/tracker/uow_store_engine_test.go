package tracker

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type engineUOWState struct {
	issues  map[string]*types.Issue
	wisps   map[string]*types.Issue
	configs map[string]string
	commits int
	// uows counts units of work handed out by the provider, so a test can pin
	// how many read transactions one logical lookup is allowed to span.
	uows int
}

type engineIssueUC struct {
	domain.IssueUseCase
	s *engineUOWState
}

// searchPlane returns the rows in one plane matching filter, lowest ID first.
// ephemeralOnly models the `ephemeral = 1` predicate that IssueFilter.Ephemeral
// contributes on top of routing the search to the wisps plane.
func searchPlane(plane map[string]*types.Issue, filter types.IssueFilter, ephemeralOnly bool) []*types.Issue {
	items := make([]*types.Issue, 0, len(plane))
	for _, issue := range plane {
		if filter.ExternalRef != nil && (issue.ExternalRef == nil || *issue.ExternalRef != *filter.ExternalRef) {
			continue
		}
		if ephemeralOnly && !issue.Ephemeral {
			continue
		}
		items = append(items, issue)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// SearchIssues models the repository's plane routing, which is the mechanism
// GetIssueByExternalRef parity depends on: SkipWisps searches the issues plane
// alone, Ephemeral routes to the wisps plane alone (and narrows it to
// ephemeral=1 rows), and neither flag merges both with NO plane preference.
//
// The merged branch deliberately yields the wisp BEFORE the issue. The real
// merged path is a UNION ALL ordered by content and terminating in `id ASC`, so
// a wisp whose id sorts first is returned first — the adversarial ordering is
// the whole reason the plane preference has to be explicit rather than implied.
func (u *engineIssueUC) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var items []*types.Issue
	switch {
	case filter.SkipWisps:
		items = searchPlane(u.s.issues, filter, false)
	case filter.Ephemeral != nil && *filter.Ephemeral:
		items = searchPlane(u.s.wisps, filter, true)
	default:
		items = append(searchPlane(u.s.wisps, filter, false), searchPlane(u.s.issues, filter, false)...)
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return domain.SearchPage{Items: items}, nil
}
func (u *engineIssueUC) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return u.s.issues[id], nil
}
func (u *engineIssueUC) CreateIssue(_ context.Context, p domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	u.s.issues[p.Issue.ID] = p.Issue
	return domain.CreateIssueResult{Issue: p.Issue}, nil
}
func (u *engineIssueUC) UpdateIssue(_ context.Context, id string, fields map[string]any, _ string) error {
	if issue := u.s.issues[id]; issue != nil {
		if title, ok := fields["title"].(string); ok {
			issue.Title = title
		}
	}
	return nil
}
func (u *engineIssueUC) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if err := u.UpdateIssue(ctx, id, spec.Fields, actor); err != nil {
		return nil, err
	}
	return u.s.issues[id], nil
}

type engineConfigUC struct {
	domain.ConfigUseCase
	s *engineUOWState
}

func (u *engineConfigUC) GetConfig(_ context.Context, key string) (string, error) {
	return u.s.configs[key], nil
}
func (u *engineConfigUC) GetAllConfig(context.Context) (map[string]string, error) {
	return u.s.configs, nil
}
func (u *engineConfigUC) GetLocalMetadata(context.Context, string) (string, error) { return "", nil }
func (u *engineConfigUC) SetLocalMetadata(context.Context, string, string) error   { return nil }

type engineDepUC struct{ domain.DependencyUseCase }

func (engineDepUC) ListWithIssueMetadata(context.Context, string, domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (engineDepUC) AddDependencies(context.Context, []*types.Dependency, string, domain.BulkAddDepsOpts) (domain.BulkAddDepsResult, error) {
	return domain.BulkAddDepsResult{}, nil
}

type engineUOW struct{ state *engineUOWState }

func (u *engineUOW) Close(context.Context)                             {}
func (u *engineUOW) Commit(context.Context, string) error              { u.state.commits++; return nil }
func (u *engineUOW) SwitchDatabase(context.Context, string) error      { return nil }
func (u *engineUOW) ConfigUseCase() domain.ConfigUseCase               { return &engineConfigUC{s: u.state} }
func (u *engineUOW) DoltRemoteUseCase() domain.DoltRemoteUseCase       { return nil }
func (u *engineUOW) IssueUseCase() domain.IssueUseCase                 { return &engineIssueUC{s: u.state} }
func (u *engineUOW) DependencyUseCase() domain.DependencyUseCase       { return engineDepUC{} }
func (u *engineUOW) LabelUseCase() domain.LabelUseCase                 { return nil }
func (u *engineUOW) CommentUseCase() domain.CommentUseCase             { return nil }
func (u *engineUOW) RawSQLUseCase() domain.RawSQLUseCase               { return nil }
func (u *engineUOW) EventsJournalUseCase() domain.EventsJournalUseCase { return nil }

type engineUOWProvider struct{ state *engineUOWState }

func (p *engineUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.state.uows++
	return &engineUOW{state: p.state}, nil
}
func (*engineUOWProvider) Close(context.Context) error { return nil }

func TestUOWStoreEngineMutationsCommitThroughProvider(t *testing.T) {
	state := &engineUOWState{issues: map[string]*types.Issue{}, configs: map[string]string{"issue_prefix": "bd"}}
	st := NewUOWStore(&engineUOWProvider{state: state})
	issue := &types.Issue{ID: "bd-new", Title: "new"}
	if err := st.CreateIssue(context.Background(), issue, "test"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.UpdateIssue(context.Background(), issue.ID, map[string]interface{}{"title": "updated"}, "test"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := st.GetIssueByExternalRef(context.Background(), "missing")
	if got != nil || err == nil {
		t.Fatalf("missing external ref: got=%v err=%v", got, err)
	}
	if state.issues[issue.ID].Title != "updated" || state.commits != 2 {
		t.Fatalf("state=%+v commits=%d", state.issues[issue.ID], state.commits)
	}
}

// TestUOWStoreGetIssueByExternalRefPrefersIssuePlane pins the proxied adapter to
// the direct backend's resolution order. issueops.GetIssueByExternalRefInTx
// queries `issues` and falls through to `wisps` only on no rows, so one
// external_ref present on both planes must resolve to the ISSUE on both routes.
// A merged search has no plane preference, and the consumer is the pull dedup,
// which updates whatever row comes back — so a divergence here is a silent
// write to the wrong local bead in proxied mode only.
func TestUOWStoreGetIssueByExternalRefPrefersIssuePlane(t *testing.T) {
	ctx := context.Background()
	ref := "https://tracker.test/EXT-1"

	// The wisp's ID sorts before the issue's, so the merged search returns it
	// first: resolving by "first merged hit" fails this test.
	issue := &types.Issue{ID: "bd-durable", Title: "durable", ExternalRef: &ref}
	wisp := &types.Issue{ID: "bd-aaa-wisp", Title: "pushed wisp", Ephemeral: true, ExternalRef: &ref}
	state := &engineUOWState{
		issues:  map[string]*types.Issue{issue.ID: issue},
		wisps:   map[string]*types.Issue{wisp.ID: wisp},
		configs: map[string]string{},
	}
	st := NewUOWStore(&engineUOWProvider{state: state})

	got, err := st.GetIssueByExternalRef(ctx, ref)
	if err != nil {
		t.Fatalf("resolve collided external_ref: %v", err)
	}
	if got == nil || got.ID != issue.ID {
		t.Fatalf("external_ref on both planes resolved to %v, want issues-plane %q", got, issue.ID)
	}
}

// TestUOWStoreGetIssueByExternalRefFallsBackToWispPlane is the other half of the
// contract: preferring the issues plane must not stop resolving wisp-only refs,
// which is the case the direct backend's fall-through exists to serve.
//
// The ephemeral=0 subtest is why the fallback is the merged search rather than
// Ephemeral=true: that flag contributes an `ephemeral = 1` predicate the direct
// backend's `SELECT id FROM wisps WHERE external_ref = ?` does not have, and
// wisp-plane rows legitimately carry ephemeral=0 (NoHistory beads, and typed
// wisps minted without the flag — see types.IssueFilter.EphemeralTier).
func TestUOWStoreGetIssueByExternalRefFallsBackToWispPlane(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		ephemeral bool
	}{
		{name: "ephemeral_flag_set", ephemeral: true},
		{name: "ephemeral_flag_clear", ephemeral: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := "https://tracker.test/EXT-WISP"
			wisp := &types.Issue{ID: "bd-wisp", Title: "wisp only", Ephemeral: tc.ephemeral, ExternalRef: &ref}
			state := &engineUOWState{
				issues:  map[string]*types.Issue{},
				wisps:   map[string]*types.Issue{wisp.ID: wisp},
				configs: map[string]string{},
			}
			st := NewUOWStore(&engineUOWProvider{state: state})

			got, err := st.GetIssueByExternalRef(ctx, ref)
			if err != nil {
				t.Fatalf("resolve wisp-only external_ref: %v", err)
			}
			if got == nil || got.ID != wisp.ID {
				t.Fatalf("wisp-only external_ref resolved to %v, want %q", got, wisp.ID)
			}
		})
	}
}

// TestUOWStoreGetIssueByExternalRefUsesOneReadTransaction pins both plane reads
// to a SINGLE unit of work. uow.RunTxRead opens a fresh UOW per call, so
// resolving through two s.SearchIssues calls would split one logical lookup
// across two read transactions, and the wisp fallback's premise ("the issues
// plane is already known empty") would stop being snapshot-guaranteed: a
// concurrent same-ref insert into the issues plane landing between the reads
// would let the merged fallback return the wisp, re-creating the wrong-row
// write TestUOWStoreGetIssueByExternalRefPrefersIssuePlane exists to prevent.
// The direct backend resolves both planes inside one withReadTx, so this is
// also the parity-preserving shape.
//
// Only the paths that actually perform both reads can pin this, so the cases
// are the wisp fallback and the miss; an issues-plane hit short-circuits after
// one read and would pass either way.
func TestUOWStoreGetIssueByExternalRefUsesOneReadTransaction(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		wispRef bool
	}{
		{name: "wisp_fallback", wispRef: true},
		{name: "no_match_on_either_plane", wispRef: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := "https://tracker.test/EXT-SNAPSHOT"
			wisps := map[string]*types.Issue{}
			if tc.wispRef {
				wisps["bd-wisp"] = &types.Issue{ID: "bd-wisp", Title: "wisp only", Ephemeral: true, ExternalRef: &ref}
			}
			state := &engineUOWState{
				issues:  map[string]*types.Issue{},
				wisps:   wisps,
				configs: map[string]string{},
			}
			st := NewUOWStore(&engineUOWProvider{state: state})

			got, err := st.GetIssueByExternalRef(ctx, ref)
			if tc.wispRef {
				if err != nil {
					t.Fatalf("resolve wisp-only external_ref: %v", err)
				}
				if got == nil || got.ID != "bd-wisp" {
					t.Fatalf("resolved to %v, want %q", got, "bd-wisp")
				}
			} else if got != nil || !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("unmatched external_ref: got=%v err=%v, want ErrNotFound", got, err)
			}

			if state.uows != 1 {
				t.Fatalf("both plane reads must share one unit of work: opened %d, want 1", state.uows)
			}
		})
	}
}
