package issueops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestResolveUpdatePlaneInTx pins the update resolver's decision table
// (GH#5991). Routing matches the reader's precedence — an id absent from
// wisps is durable and needs no second probe — and a wisp hit adds exactly one
// issues probe whose only two outcomes are the wisp plane and the typed
// dual-presence refusal. Absence is answered only by a genuine no-row or the
// optional wisps table's own absence: a failed probe is an error, never a
// silent "empty table". sqlmock refuses every statement not expected here, so
// each case also proves which probes the resolution did and did not need.
func TestResolveUpdatePlaneInTx(t *testing.T) {
	const wispsProbe = `SELECT 1 FROM wisps WHERE id = ? LIMIT 1`
	const issuesProbe = `SELECT 1 FROM issues WHERE id = ? LIMIT 1`

	tests := []struct {
		name            string
		stub            func(mock sqlmock.Sqlmock)
		wantWisp        bool
		wantDual        bool
		wantErrContains string
	}{
		{
			name: "absent from wisps routes durable without a second probe",
			stub: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(wispsProbe)).
					WithArgs("bd-x").
					WillReturnRows(sqlmock.NewRows([]string{"1"}))
			},
		},
		{
			name: "wisp only routes to the wisp plane",
			stub: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(wispsProbe)).
					WithArgs("bd-x").
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta(issuesProbe)).
					WithArgs("bd-x").
					WillReturnRows(sqlmock.NewRows([]string{"1"}))
			},
			wantWisp: true,
		},
		{
			name: "present in both refuses typed",
			stub: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(wispsProbe)).
					WithArgs("bd-x").
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta(issuesProbe)).
					WithArgs("bd-x").
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			},
			wantDual: true,
		},
		{
			name: "missing wisps table routes durable without a second probe",
			stub: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(wispsProbe)).
					WithArgs("bd-x").
					WillReturnError(errors.New("Error 1146 (42S02): Table 'beads.wisps' doesn't exist"))
			},
		},
		{
			name: "a failed wisps probe is an error, not an empty table",
			stub: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(wispsProbe)).
					WithArgs("bd-x").
					WillReturnError(errors.New("Error 1045: Access denied"))
			},
			wantErrContains: "probe wisps",
		},
		{
			name: "a failed issues probe is an error, not a wisp",
			stub: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta(wispsProbe)).
					WithArgs("bd-x").
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta(issuesProbe)).
					WithArgs("bd-x").
					WillReturnError(errors.New("Error 1045: Access denied"))
			},
			wantErrContains: "probe issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, db := newBlockedStateResultMock(t)
			tt.stub(mock)

			isWisp, err := ResolveUpdatePlaneInTx(context.Background(), db, "bd-x")
			switch {
			case tt.wantDual:
				if err == nil {
					t.Fatalf("ResolveUpdatePlaneInTx(%s) = isWisp %v, nil; want the dual-presence refusal", tt.name, isWisp)
				}
				var dual *publicops.DualPresenceError
				if !errors.As(err, &dual) {
					t.Fatalf("refusal is not *publicops.DualPresenceError: %v", err)
				}
				if dual.ID != "bd-x" {
					t.Errorf("DualPresenceError.ID = %q, want bd-x", dual.ID)
				}
				if !errors.Is(err, publicops.ErrDualPresence) {
					t.Errorf("refusal does not match publicops.ErrDualPresence: %v", err)
				}
				if isWisp {
					t.Errorf("ResolveUpdatePlaneInTx(%s) = isWisp true alongside the refusal", tt.name)
				}
			case tt.wantErrContains != "":
				if err == nil {
					t.Fatalf("ResolveUpdatePlaneInTx(%s) = isWisp %v, nil; want an error naming %q", tt.name, isWisp, tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("ResolveUpdatePlaneInTx(%s) error = %q, want it to name %q", tt.name, err, tt.wantErrContains)
				}
				if isWisp {
					t.Errorf("ResolveUpdatePlaneInTx(%s) = isWisp true alongside an error", tt.name)
				}
			default:
				if err != nil {
					t.Fatalf("ResolveUpdatePlaneInTx(%s) error = %v; want none", tt.name, err)
				}
				if isWisp != tt.wantWisp {
					t.Errorf("ResolveUpdatePlaneInTx(%s) = isWisp %v, want %v", tt.name, isWisp, tt.wantWisp)
				}
			}
		})
	}
}
