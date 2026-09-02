package tracker

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type engineUOWState struct {
	issues  map[string]*types.Issue
	configs map[string]string
	commits int
}

type engineIssueUC struct {
	domain.IssueUseCase
	s *engineUOWState
}

func (u *engineIssueUC) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	items := make([]*types.Issue, 0, len(u.s.issues))
	for _, issue := range u.s.issues {
		if filter.ExternalRef != nil && (issue.ExternalRef == nil || *issue.ExternalRef != *filter.ExternalRef) {
			continue
		}
		items = append(items, issue)
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
