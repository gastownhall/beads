package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	publicops "github.com/steveyegge/beads/issueops"
)

func validThreadGuard() *publicops.ThreadGuard {
	return &publicops.ThreadGuard{
		Cutoff:      time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		MemberLabel: "mail",
		Edge:        "replies-to",
		Threads: []publicops.GuardedThread{{
			Root:    "bd-root",
			Members: []publicops.GuardedMember{{ID: "bd-root", Revision: "r1"}},
		}},
	}
}

// TestDeleteInTxRefusesAThreadGuardWithCascade pins that the refusal comes
// before any read: the mock expects no statement after BEGIN, so a query the
// body issued first fails the test as unexpected.
func TestDeleteInTxRefusesAThreadGuardWithCascade(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = DeleteInTx(context.Background(), tx, publicops.DeleteRequest{
		IDs:         []string{"bd-root"},
		Cascade:     true,
		ThreadGuard: validThreadGuard(),
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("DeleteInTx with a thread guard and cascade: err = %v, want ErrValidation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestValidateThreadGuardRefusesAnUncheckableGuard(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*publicops.ThreadGuard)
	}{
		{"no member label", func(g *publicops.ThreadGuard) { g.MemberLabel = " " }},
		{"no edge", func(g *publicops.ThreadGuard) { g.Edge = "" }},
		{"no threads", func(g *publicops.ThreadGuard) { g.Threads = nil }},
		{"no root", func(g *publicops.ThreadGuard) { g.Threads[0].Root = "" }},
		{"no members", func(g *publicops.ThreadGuard) { g.Threads[0].Members = nil }},
		{"member with no id", func(g *publicops.ThreadGuard) { g.Threads[0].Members[0].ID = "" }},
		{"member with no revision", func(g *publicops.ThreadGuard) { g.Threads[0].Members[0].Revision = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard := validThreadGuard()
			test.mutate(guard)
			err := ValidateThreadGuard(publicops.DeleteRequest{IDs: []string{"bd-root"}, ThreadGuard: guard})
			if !errors.Is(err, publicops.ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
		})
	}
	if err := ValidateThreadGuard(publicops.DeleteRequest{IDs: []string{"bd-root"}, ThreadGuard: validThreadGuard()}); err != nil {
		t.Errorf("a valid guard was refused: %v", err)
	}
	if err := ValidateThreadGuard(publicops.DeleteRequest{IDs: []string{"bd-root"}, Cascade: true}); err != nil {
		t.Errorf("cascade with no guard was refused: %v", err)
	}
}

func TestThreadGuardMismatchErrorNamesEveryThreadAndCheck(t *testing.T) {
	err := error(&publicops.ThreadGuardMismatchError{Failed: []publicops.ThreadGuardFailure{
		{Root: "bd-a", Check: publicops.ThreadGuardMembership, IDs: []string{"bd-x"}},
		{Root: "bd-b", Check: publicops.ThreadGuardRevision, IDs: []string{"bd-b", "bd-c"}},
		{Check: publicops.ThreadGuardCoverage, IDs: []string{"bd-y"}},
	}})
	if !errors.Is(err, publicops.ErrThreadGuardMismatch) {
		t.Errorf("errors.Is(ErrThreadGuardMismatch) = false for %v", err)
	}
	for _, want := range []string{"thread bd-a: membership (bd-x)", "thread bd-b: revision (bd-b, bd-c)", "request: coverage (bd-y)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// TestThreadGuardWalksEveryRootAtOnce pins the walk's statements —
// one exclude read over every root, then per level and edge table one UNION
// ALL of single-column arms over the whole frontier, then one exclude read
// over the level's new ids — and its attribution: bd-b1 replying to bd-a1
// joins thread bd-a's component to thread bd-b's, so each sees the other's
// members, while bd-d stays alone and the excluded root bd-c reaches nothing.
// A missing wisp_dependencies table means no edges there.
func TestThreadGuardWalksEveryRootAtOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	const edge, exclude = "replies-to", "keep"
	walk := func(table string, ids ...string) *sqlmock.ExpectedQuery {
		in := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		var arms []string
		var args []driver.Value
		for _, col := range []string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
			arms = append(arms, "SELECT issue_id, "+DepTargetExpr+" AS depends_on_id FROM "+table+" WHERE type = ? AND "+col+" IN ("+in+")")
			args = append(args, edge)
			for _, id := range ids {
				args = append(args, id)
			}
		}
		return mock.ExpectQuery("^" + regexp.QuoteMeta(strings.Join(arms, " UNION ALL ")) + "$").WithArgs(args...)
	}
	labelled := func(ids []string, labels, wispLabels []string) {
		args := []driver.Value{exclude}
		for _, id := range ids {
			args = append(args, id)
		}
		rows := sqlmock.NewRows([]string{"issue_id"})
		for _, id := range labels {
			rows.AddRow(id)
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id FROM labels WHERE label = ?")).WithArgs(args...).WillReturnRows(rows)
		rows = sqlmock.NewRows([]string{"issue_id"})
		for _, id := range wispLabels {
			rows.AddRow(id)
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id FROM wisp_labels WHERE label = ?")).WithArgs(args...).WillReturnRows(rows)
	}
	edges := func(pairs ...string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"issue_id", "depends_on_id"})
		for i := 0; i < len(pairs); i += 2 {
			rows.AddRow(pairs[i], pairs[i+1])
		}
		return rows
	}

	labelled([]string{"bd-a", "bd-b", "bd-c", "bd-d"}, []string{"bd-c"}, nil)
	walk("dependencies", "bd-a", "bd-b", "bd-d").
		WillReturnRows(edges("bd-a1", "bd-a", "bd-b1", "bd-b"))
	walk("wisp_dependencies", "bd-a", "bd-b", "bd-d").
		WillReturnRows(edges("bd-k", "bd-a", "bd-c", "bd-d"))
	labelled([]string{"bd-a1", "bd-b1", "bd-k"}, nil, []string{"bd-k"})
	walk("dependencies", "bd-a1", "bd-b1").
		WillReturnRows(edges("bd-a1", "bd-a", "bd-b1", "bd-b", "bd-b1", "bd-a1"))
	walk("wisp_dependencies", "bd-a1", "bd-b1").
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: wisp_dependencies"})

	components, err := threadComponentsInTx(context.Background(), db, []string{"bd-b", "bd-d", "bd-c", "bd-a"}, edge, exclude)
	if err != nil {
		t.Fatalf("threadComponentsInTx: %v", err)
	}
	joined := map[string]bool{"bd-a": true, "bd-a1": true, "bd-b": true, "bd-b1": true}
	want := map[string]map[string]bool{
		"bd-a": joined,
		"bd-b": joined,
		"bd-c": {},
		"bd-d": {"bd-d": true},
	}
	if !reflect.DeepEqual(components, want) {
		t.Errorf("components = %v\nwant         %v", components, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
