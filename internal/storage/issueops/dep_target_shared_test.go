package issueops

import (
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

func TestDepTargetKindFromPresence(t *testing.T) {
	for _, tc := range []struct {
		name, source, target    string
		issueExists, wispExists bool
		want                    DepTargetKind
	}{
		{"local foreign-prefix issue", "a-queue", "b-blocker", true, false, DepTargetIssue},
		{"local foreign-prefix wisp", "a-queue", "b-wisp", false, true, DepTargetWisp},
		{"absent foreign-prefix", "a-queue", "t-other", false, false, DepTargetExternal},
		{"explicit external wins", "a-queue", "external:GH-42", true, false, DepTargetExternal},
		{"same-prefix local", "a-queue", "a-blocker", true, false, DepTargetIssue},
		{"same-prefix missing uses FK", "a-queue", "a-missing", false, false, DepTargetIssue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DepTargetKindFromPresence(tc.source, tc.target, tc.issueExists, tc.wispExists)
			if got != tc.want {
				t.Fatalf("DepTargetKindFromPresence(%q, %q) = %v; want %v", tc.source, tc.target, got, tc.want)
			}
		})
	}
}

// TestClassifyDepTargetWispProbeErrorFailsClosed prevents a failed wisp read
// from silently being classified as a non-wisp/external dependency.
func TestClassifyDepTargetWispProbeErrorFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	probeErr := errors.New("injected wisp read failure")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs("b-target").WillReturnError(probeErr)
	_, err = ClassifyDepTarget(t.Context(), db, db, &types.Dependency{
		IssueID: "a-source", DependsOnID: "b-target", Type: types.DepBlocks,
	})
	if !errors.Is(err, probeErr) {
		t.Fatalf("ClassifyDepTarget(wisp probe error) = %v, want wrapped probe error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
