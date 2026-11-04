package uow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The unit-of-work seam is the one whose comment source has to be TOLD which
// table to read — the store-backed sibling's source routes an id itself — so it
// is the one where a wrong answer is a silent wrong-table read. These tests
// drive the real Reader.List with --include-comments and assert the comment
// BODIES arrive, because the failure they cover produced an empty comments array
// beside a nonzero comment_count rather than an error or a visibly wrong row.
//
// The rows are the two shapes where a row's flags and its table disagree, and
// both are states the tree maintains on purpose: `bd import` pins a no_history
// record to the durable table while preserving the flag (clearing it would
// change the content hash and break export→import→export byte-identity), and
// pins a wisp_plane record to the wisps table without setting either flag. See
// workapi.CommentPlanes.

// planeIssues serves one page and answers the residence question from the
// wisps-table membership the test declares, which is what
// IssueUseCase.GetWispsByIDs answers from a real database.
type planeIssues struct {
	domain.IssueUseCase
	rows    []*types.IssueWithCounts
	inWisps map[string]*types.Issue
	wispErr error
	asked   [][]string
}

func (f *planeIssues) SearchIssuesWithCounts(context.Context, string, types.IssueFilter) (domain.SearchCountsPage, error) {
	return domain.SearchCountsPage{Items: f.rows}, nil
}

func (f *planeIssues) GetWispsByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	f.asked = append(f.asked, ids)
	if f.wispErr != nil {
		return nil, f.wispErr
	}
	out := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if wisp, ok := f.inWisps[id]; ok {
			out = append(out, wisp)
		}
	}
	return out, nil
}

// planeComments keeps each plane's comments in its own map, the way the two
// comment tables do, so a read of the wrong one comes back empty here exactly as
// it does against a database.
type planeComments struct {
	domain.CommentUseCase
	issueComments map[string][]*types.Comment
	wispComments  map[string][]*types.Comment
}

func (f planeComments) IterCommentsForIssue(_ context.Context, id string) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter(f.issueComments[id]), nil
}

func (f planeComments) IterCommentsForWisp(_ context.Context, id string) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter(f.wispComments[id]), nil
}

func planeReader(t *testing.T, issues *planeIssues, comments domain.CommentUseCase) publicops.Reader {
	t.Helper()
	uw := &mockUnitOfWork{issueUseCase: issues, configUseCase: readerConfig{}, commentUseCase: comments}
	reader, err := NewIssueReader(&mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}})
	if err != nil {
		t.Fatalf("NewIssueReader: %v", err)
	}
	return reader
}

func commentTexts(item *types.IssueWithCounts) []string {
	out := make([]string, 0, len(item.Comments))
	for _, c := range item.Comments {
		out = append(out, c.Text)
	}
	return out
}

// TestListHydratesCommentsFromTheRowsTableNotItsFlags is the seam-level
// regression for the row class this reader used to answer with content-free
// rows: a durable bead that still carries no_history, listed with
// --include-comments, whose comments `bd comment` wrote to the ISSUES table and
// `bd show` reads from there. Routing by the flag sent the list read to
// wisp_comments, where the row has nothing, and the page came back asserting one
// comment, carrying none, and flagging nothing.
func TestListHydratesCommentsFromTheRowsTableNotItsFlags(t *testing.T) {
	issues := &planeIssues{
		rows: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "bd-1", Title: "import-pinned", NoHistory: true}, CommentCount: 1},
		},
		inWisps: map[string]*types.Issue{},
	}
	comments := planeComments{
		issueComments: map[string][]*types.Comment{"bd-1": {{ID: "c1", Text: "zzzuniquephrase"}}},
		// Nothing on the wisp plane: the table the flag points at is empty for
		// this id, which is why the defect was silent rather than wrong.
		wispComments: map[string][]*types.Comment{},
	}
	reader := planeReader(t, issues, comments)

	page, err := reader.List(context.Background(), publicops.ListRequest{IncludeComments: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(page.Items) != 1 {
		t.Fatalf("page has %d rows, want 1", len(page.Items))
	}
	if got := commentTexts(page.Items[0]); !slices.Equal(got, []string{"zzzuniquephrase"}) {
		t.Errorf("comments = %v, want [zzzuniquephrase]: the row lives in the issues table, whatever its no_history flag says", got)
	}
	if page.Items[0].CommentsOmitted != nil {
		t.Errorf("CommentsOmitted = %v on a hydrated row, want unset", *page.Items[0].CommentsOmitted)
	}
	if len(issues.asked) != 1 || !slices.Equal(issues.asked[0], []string{"bd-1"}) {
		t.Errorf("resolved residence with %v, want one lookup for [bd-1]: the plane is a query, not a guess", issues.asked)
	}
}

// TestListHydratesCommentsForAWispTableRowWithNoFlags is the mirror, and the one
// no flag-based rule can get right either: a row the import pinned to the WISPS
// table with neither plane flag set. Its comments are in wisp_comments and the
// flags point at the durable table.
func TestListHydratesCommentsForAWispTableRowWithNoFlags(t *testing.T) {
	issues := &planeIssues{
		rows: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "bd-2", Title: "plane-pinned"}, CommentCount: 1},
		},
		inWisps: map[string]*types.Issue{"bd-2": {ID: "bd-2"}},
	}
	comments := planeComments{
		issueComments: map[string][]*types.Comment{},
		wispComments:  map[string][]*types.Comment{"bd-2": {{ID: "c1", Text: "zzzuniquephrase"}}},
	}
	reader := planeReader(t, issues, comments)

	page, err := reader.List(context.Background(), publicops.ListRequest{IncludeComments: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(page.Items) != 1 {
		t.Fatalf("page has %d rows, want 1", len(page.Items))
	}
	if got := commentTexts(page.Items[0]); !slices.Equal(got, []string{"zzzuniquephrase"}) {
		t.Errorf("comments = %v, want [zzzuniquephrase]: the row is in the wisps table, whatever its flags say", got)
	}
}

// TestListResolvesNoPlanesWithoutIncludeComments pins that the default listing —
// which is every `bd list` that does not pass the flag — pays nothing for the
// routing query. The marker path routes no read, so it asks no table where
// anything lives.
func TestListResolvesNoPlanesWithoutIncludeComments(t *testing.T) {
	issues := &planeIssues{
		rows: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "bd-1", NoHistory: true}, CommentCount: 1},
		},
		inWisps: map[string]*types.Issue{},
	}
	reader := planeReader(t, issues, planeComments{})

	page, err := reader.List(context.Background(), publicops.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(issues.asked) != 0 {
		t.Errorf("resolved residence %d times on a listing that asked for no comments, want 0", len(issues.asked))
	}
	if page.Items[0].CommentsOmitted == nil || !*page.Items[0].CommentsOmitted {
		t.Errorf("CommentsOmitted = %v, want true: the default listing still says the text is missing", page.Items[0].CommentsOmitted)
	}
}

// TestListToleratesAMissingWispsTable keeps `bd list --include-comments` working
// on a database that has no wisps table at all. That table is in the optional set
// every wisp query treats as "no wisps" (sqlbuild.OptionalWispTable) — the counts
// query that produced the page merged wisps under the same tolerance, and the
// store-backed seam's router answers "durable" for it — so a listing must not be
// the one read that cannot run there.
//
// The fixture error is the one this seam receives: a missing table arrives from
// the server as MySQL 1146, and the table it names is what
// dberrors.MissingTableName reads. Classifying it through those two shared
// helpers rather than a local string check is what keeps this tolerance the SAME
// tolerance the counts query already applies.
func TestListToleratesAMissingWispsTable(t *testing.T) {
	issues := &planeIssues{
		rows: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "bd-1", NoHistory: true}, CommentCount: 1},
		},
		wispErr: fmt.Errorf("getByIDs: %w", &mysql.MySQLError{Number: 1146, Message: "table not found: wisps"}),
	}
	comments := planeComments{
		issueComments: map[string][]*types.Comment{"bd-1": {{ID: "c1", Text: "zzzuniquephrase"}}},
	}
	reader := planeReader(t, issues, comments)

	page, err := reader.List(context.Background(), publicops.ListRequest{IncludeComments: true})
	if err != nil {
		t.Fatalf("List over a database with no wisps table: %v", err)
	}
	if got := commentTexts(page.Items[0]); !slices.Equal(got, []string{"zzzuniquephrase"}) {
		t.Errorf("comments = %v, want [zzzuniquephrase]: with no wisps table, every row is durable", got)
	}
}

// TestListFailsWhenResidenceCannotBeRead is the other half of that tolerance:
// any OTHER failure of the routing query fails the call. Hydration promises the
// bodies or an error, and a page that cannot find out where its rows live can
// only keep that promise by saying so — falling back to a plane would answer with
// the wrong table's contents, which is invisible in the response.
func TestListFailsWhenResidenceCannotBeRead(t *testing.T) {
	sentinel := errors.New("connection reset")
	issues := &planeIssues{
		rows: []*types.IssueWithCounts{
			{Issue: &types.Issue{ID: "bd-1"}, CommentCount: 1},
		},
		wispErr: sentinel,
	}
	comments := planeComments{
		issueComments: map[string][]*types.Comment{"bd-1": {{ID: "c1", Text: "zzzuniquephrase"}}},
	}
	reader := planeReader(t, issues, comments)

	_, err := reader.List(context.Background(), publicops.ListRequest{IncludeComments: true})
	if err == nil {
		t.Fatal("List returned nil when residence could not be read, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap %v", err, sentinel)
	}
}
