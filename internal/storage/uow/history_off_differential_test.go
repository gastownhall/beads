package uow

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/historyoffequiv"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestVersionedHistoryOffMatchesPreStackGolden_UOW is the unit-of-work leg's
// versioned-history flag-off differential (internal/storage/historyoffequiv):
// on a provider whose versioned history was never turned on, the scripted
// mutation set must leave every table as the code before the
// versioned-history stack left it, apart from that stack's new columns and
// table staying NULL and empty.
//
// Beyond the shared role scenario this leg drives the domain create that
// leaves CreateOnly unset (the quick, todo and batch proxied servers' path):
// it is an upsert, and the stack gave it an existence probe that must not run
// while history is off (see TestInsertUpsertWithHistoryOffIssuesThePreStackStatements
// in domain/db). The importer role's upsert of an existing row runs too.
func TestVersionedHistoryOffMatchesPreStackGolden_UOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, historyoffequiv.Prefix)
	roles, ok := provider.(historyoffequiv.Roles)
	if !ok {
		t.Fatalf("provider %T does not offer every role the scenario drives", provider)
	}
	fixedAt := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	issue := func(id, title string) *types.Issue {
		return &types.Issue{ID: id, Title: title, Description: "about " + id, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, CreatedAt: fixedAt, UpdatedAt: fixedAt}
	}
	domainCreate := func(wisp bool, is *types.Issue) func(context.Context) error {
		return func(ctx context.Context) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				params := domain.CreateIssueParams{Issue: is, ExplicitID: is.ID}
				var err error
				if wisp {
					_, err = uw.IssueUseCase().CreateWisp(ctx, params, "uma")
				} else {
					_, err = uw.IssueUseCase().CreateIssue(ctx, params, "uma")
				}
				return "bd: create " + is.ID, err
			})
		}
	}
	historyoffequiv.Run(t, historyoffequiv.Leg{
		Name:  "uow",
		Roles: roles,
		Wake: func(ctx context.Context) error {
			_, err := WakeExpiredDefers(ctx, provider)
			return err
		},
		Extra: []historyoffequiv.Step{
			{Name: "domain upsert new u1", Run: domainCreate(false, issue("ho-u1", "uniform one"))},
			{Name: "domain upsert existing u1", Run: domainCreate(false, issue("ho-u1", "uniform one again"))},
			{Name: "domain upsert new wisp uw", Run: domainCreate(true, issue("ho-uw", "uniform wisp"))},
			{Name: "domain upsert existing wisp uw", Run: domainCreate(true, issue("ho-uw", "uniform wisp again"))},
			{Name: "import upsert u1 and new u2", Run: func(ctx context.Context) error {
				importer, err := provider.(interface {
					Importer() (publicops.Importer, error)
				}).Importer()
				if err != nil {
					return err
				}
				u1 := issue("ho-u1", "uniform one imported")
				u1.UpdatedAt = fixedAt.Add(time.Hour)
				u2 := issue("ho-u2", "uniform two")
				u2.Labels = []string{"imported"}
				u2.Dependencies = []*types.Dependency{{IssueID: "ho-u2", DependsOnID: "ho-u1", Type: types.DepBlocks, CreatedAt: fixedAt}}
				u2.Comments = []*types.Comment{{ID: "ho-u2-comment", IssueID: "ho-u2", Author: "importer", Text: "imported", CreatedAt: fixedAt}}
				_, err = importer.ImportBatch(ctx, publicops.ImportBatchRequest{Actor: "importer",
					Issues: []*types.Issue{u1, u2}, SkipPrefixValidation: true})
				return err
			}},
		},
		Query: func(ctx context.Context, query string) ([][]*string, error) {
			return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([][]*string, error) {
				result, err := uw.RawSQLUseCase().Query(ctx, query)
				if err != nil {
					return nil, err
				}
				return historyoffequiv.AnyRows(result.Rows), nil
			})
		},
	})
}
