package issueops

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	publicops "github.com/steveyegge/beads/issueops"
)

func TestPartitionByWispSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ids       []string
		wispSet   map[string]struct{}
		wantWisps []string
		wantPerms []string
	}{
		{
			name:    "all permanent",
			ids:     []string{"be-1", "be-2", "be-3"},
			wispSet: map[string]struct{}{},
			// wantWisps nil — no entries appended
			wantPerms: []string{"be-1", "be-2", "be-3"},
		},
		{
			name:      "all wisps",
			ids:       []string{"be-wisp-a", "be-wisp-b"},
			wispSet:   map[string]struct{}{"be-wisp-a": {}, "be-wisp-b": {}},
			wantWisps: []string{"be-wisp-a", "be-wisp-b"},
			// wantPerms nil
		},
		{
			name:      "mixed",
			ids:       []string{"be-1", "be-wisp-a", "be-2", "be-wisp-b", "be-3"},
			wispSet:   map[string]struct{}{"be-wisp-a": {}, "be-wisp-b": {}},
			wantWisps: []string{"be-wisp-a", "be-wisp-b"},
			wantPerms: []string{"be-1", "be-2", "be-3"},
		},
		{
			name: "empty input",
			ids:  nil,
			// wantWisps nil, wantPerms nil
			wispSet: map[string]struct{}{"be-wisp-a": {}},
		},
		{
			name:      "nil wisp set treats all as permanent",
			ids:       []string{"be-1", "be-wisp-a"},
			wispSet:   nil,
			wantPerms: []string{"be-1", "be-wisp-a"},
		},
		{
			name:      "explicit-id wisp routes as wisp",
			ids:       []string{"custom-id-42"},
			wispSet:   map[string]struct{}{"custom-id-42": {}},
			wantWisps: []string{"custom-id-42"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotWisps, gotPerms := partitionByWispSet(tc.ids, tc.wispSet)
			if !reflect.DeepEqual(gotWisps, tc.wantWisps) {
				t.Errorf("wispIDs: got %v, want %v", gotWisps, tc.wantWisps)
			}
			if !reflect.DeepEqual(gotPerms, tc.wantPerms) {
				t.Errorf("permIDs: got %v, want %v", gotPerms, tc.wantPerms)
			}
		})
	}
}

func TestWispIDSetInTx_EmptyIDsNoQuery(t *testing.T) {
	t.Parallel()
	set, err := WispIDSetInTx(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("WispIDSetInTx(nil, nil): %v", err)
	}
	if set == nil {
		t.Fatalf("expected non-nil empty map, got nil")
	}
	if len(set) != 0 {
		t.Fatalf("expected empty map, got %v", set)
	}
}

// TestResolveUpdatePlaneInTx pins the update router's answers (GH#5991): one
// wisps probe for a durable or missing ID, an issues probe only on a wisp hit,
// a typed refusal for an ID in both tables, a missing wisps table as durable,
// and every other probe failure as an error rather than an absent row.
func TestResolveUpdatePlaneInTx(t *testing.T) {
	t.Parallel()

	wispProbe := regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")
	issueProbe := regexp.QuoteMeta("SELECT 1 FROM issues WHERE id = ? LIMIT 1")
	hit := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"1"}).AddRow(1) }
	miss := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"1"}) }
	probeFailure := errors.New("connection reset")

	tests := []struct {
		name     string
		expect   func(mock sqlmock.Sqlmock)
		wantWisp bool
		wantDual bool
		wantErr  error
	}{
		{
			name:   "durable only or missing routes durable after one probe",
			expect: func(mock sqlmock.Sqlmock) { mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnRows(miss()) },
		},
		{
			name: "missing wisps table routes durable",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnError(missingTableError("wisps"))
			},
		},
		{
			name: "wisp only routes to wisps",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnRows(hit())
				mock.ExpectQuery(issueProbe).WithArgs("bd-1").WillReturnRows(miss())
			},
			wantWisp: true,
		},
		{
			name: "both tables refuses",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnRows(hit())
				mock.ExpectQuery(issueProbe).WithArgs("bd-1").WillReturnRows(hit())
			},
			wantDual: true,
		},
		{
			name: "wisps probe failure is an error",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnError(probeFailure)
			},
			wantErr: probeFailure,
		},
		{
			name: "a different missing table is an error",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnError(missingTableError("leases"))
			},
			wantErr: missingTableError("leases"),
		},
		{
			name: "issues probe failure is an error",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnRows(hit())
				mock.ExpectQuery(issueProbe).WithArgs("bd-1").WillReturnError(probeFailure)
			},
			wantErr: probeFailure,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			tc.expect(mock)

			gotWisp, err := ResolveUpdatePlaneInTx(context.Background(), tx, "bd-1")
			switch {
			case tc.wantDual:
				var dual *publicops.DualPresenceError
				if !errors.As(err, &dual) || dual.ID != "bd-1" || !errors.Is(err, publicops.ErrDualPresence) {
					t.Fatalf("err = %v, want DualPresenceError for bd-1", err)
				}
			case tc.wantErr != nil:
				if err == nil || !strings.Contains(err.Error(), tc.wantErr.Error()) || errors.Is(err, publicops.ErrDualPresence) {
					t.Fatalf("err = %v, want the probe failure %v", err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("err = %v, want nil", err)
			}
			if gotWisp != tc.wantWisp {
				t.Fatalf("wisp = %v, want %v", gotWisp, tc.wantWisp)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("probes: %v", err)
			}
		})
	}
}
