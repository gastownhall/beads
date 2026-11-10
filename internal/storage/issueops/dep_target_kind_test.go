package issueops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestDepTargetKindFor pins the one rule every write path classifies a
// dependency target by (GH#8299): what this database holds decides, and the
// prefix only decides for a target held nowhere.
func TestDepTargetKindFor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		source      string
		target      string
		heldAsWisp  bool
		heldAsIssue bool
		want        DepTargetKind
	}{
		{"external reference held nowhere", "b-1", "external:jira:X-1", false, false, DepTargetExternal},
		{"external reference never probes as local", "b-1", "external:b-1", true, true, DepTargetExternal},
		{"same-prefix issue", "b-1", "b-2", false, true, DepTargetIssue},
		{"cross-prefix issue held here", "a-x1", "b-2", false, true, DepTargetIssue},
		{"cross-prefix wisp held here", "a-x1", "b-wisp-1", true, false, DepTargetWisp},
		{"wisp wins over issue", "b-1", "b-2", true, true, DepTargetWisp},
		{"cross-prefix held nowhere routes later", "b-1", "other-9", false, false, DepTargetExternal},
		{"same-prefix held nowhere stays an issue", "b-1", "b-missing", false, false, DepTargetIssue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DepTargetKindFor(tc.source, tc.target, tc.heldAsWisp, tc.heldAsIssue); got != tc.want {
				t.Errorf("DepTargetKindFor(%q, %q, wisp=%v, issue=%v) = %v, want %v",
					tc.source, tc.target, tc.heldAsWisp, tc.heldAsIssue, got, tc.want)
			}
		})
	}
}

// TestClassifyDepTargetProbeErrors pins ClassifyDepTarget's reads: a failed
// probe is returned rather than read as "not held" (which would file a local
// target as external), a missing optional wisps table is absence, and a
// same-prefix target never pays for the issues probe.
func TestClassifyDepTargetProbeErrors(t *testing.T) {
	t.Parallel()

	wispProbe := regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")
	issueProbe := regexp.QuoteMeta("SELECT 1 FROM issues WHERE id = ? LIMIT 1")
	boom := errors.New("connection reset")
	held := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"1"}).AddRow(1) }

	for _, tc := range []struct {
		name    string
		dep     types.Dependency
		expect  func(mock sqlmock.Sqlmock)
		want    DepTargetKind
		wantErr string
	}{
		{
			name: "wisp probe failure is returned",
			dep:  types.Dependency{IssueID: "a-x1", DependsOnID: "b-2"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("b-2").WillReturnError(boom)
			},
			wantErr: "probe wisps",
		},
		{
			name: "issue probe failure is returned",
			dep:  types.Dependency{IssueID: "a-x1", DependsOnID: "b-2"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("b-2").WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery(issueProbe).WithArgs("b-2").WillReturnError(boom)
			},
			wantErr: "probe issues",
		},
		{
			name: "missing wisps table is absence",
			dep:  types.Dependency{IssueID: "a-x1", DependsOnID: "b-2"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("b-2").
					WillReturnError(errors.New("Error 1146: table not found: wisps"))
				mock.ExpectQuery(issueProbe).WithArgs("b-2").WillReturnRows(held())
			},
			want: DepTargetIssue,
		},
		{
			name: "cross-prefix issue held here",
			dep:  types.Dependency{IssueID: "a-x1", DependsOnID: "b-2"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("b-2").WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery(issueProbe).WithArgs("b-2").WillReturnRows(held())
			},
			want: DepTargetIssue,
		},
		{
			name: "cross-prefix wisp held here skips the issues probe",
			dep:  types.Dependency{IssueID: "a-x1", DependsOnID: "b-wisp-1"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("b-wisp-1").WillReturnRows(held())
			},
			want: DepTargetWisp,
		},
		{
			name: "cross-prefix target held nowhere routes later",
			dep:  types.Dependency{IssueID: "a-x1", DependsOnID: "other-9"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("other-9").WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery(issueProbe).WithArgs("other-9").WillReturnError(sql.ErrNoRows)
			},
			want: DepTargetExternal,
		},
		{
			name: "same-prefix target skips the issues probe",
			dep:  types.Dependency{IssueID: "b-1", DependsOnID: "b-2"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("b-2").WillReturnError(sql.ErrNoRows)
			},
			want: DepTargetIssue,
		},
		{
			name:   "external reference reads nothing",
			dep:    types.Dependency{IssueID: "b-1", DependsOnID: "external:jira:X-1"},
			expect: func(sqlmock.Sqlmock) {},
			want:   DepTargetExternal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			tc.expect(mock)

			dep := tc.dep
			got, err := ClassifyDepTarget(context.Background(), tx, &dep)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, boom) {
					t.Fatalf("ClassifyDepTarget err = %v, want a wrapped %q failure", err, tc.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("ClassifyDepTarget: %v", err)
				}
				if got != tc.want {
					t.Errorf("ClassifyDepTarget = %v, want %v", got, tc.want)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("probe reads: %v", err)
			}
		})
	}
}

// TestAddDependencyInTxRefusesMissingSamePrefixTargetWithTypedError pins the
// missing-target half of the preserved behavior (GH#8299): a same-prefix
// target no local row answers falls back to DepTargetIssue in classification,
// and that fallback is NOT evidence of presence — the existence read still
// runs and its miss is the typed MissingDependencyTarget refusal, not an FK
// violation surfacing from the INSERT. The container-backed twin is
// TestAddDependency_SamePrefix_RequiresTargetExistence (internal/storage/dolt).
func TestAddDependencyInTxRefusesMissingSamePrefixTargetWithTypedError(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	// Classification probes the wisp plane and finds no row; a same-prefix
	// target never pays for the issues probe.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs("bd-b").
		WillReturnError(sql.ErrNoRows)
	// Source read.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	// The fallback classification is not validated, so the target's
	// existence read runs — and misses.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("bd-b").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}))

	dep := &types.Dependency{IssueID: "bd-a", DependsOnID: "bd-b", Type: types.DepBlocks}
	_, err := AddDependencyInTx(context.Background(), tx, dep, "writer", AddDependencyOpts{
		SourceTable: "issues",
		WriteTable:  "dependencies",
	})
	if err == nil {
		t.Fatal("AddDependencyInTx(bd-a -> missing bd-b) = nil, want the missing-target refusal")
	}
	var notFound *publicops.DependencyEndpointNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want DependencyEndpointNotFoundError (not a raw FK or SQL error)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
