package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	publicops "github.com/steveyegge/beads/issueops"
)

// TestResolveRetainedTypesInTxReadsTheConfigRow pins the shared body's read of
// types.retained: one config-row read, parsed through workapi's rule, an unset
// key retaining nothing — and a read error returned rather than read as
// "nothing retained", which would let the sweep delete what it should hold.
func TestResolveRetainedTypesInTxReadsTheConfigRow(t *testing.T) {
	const query = "SELECT value FROM config WHERE `key` = ?"
	for _, test := range []struct {
		name    string
		script  func(mock sqlmock.Sqlmock)
		want    []string
		wantErr bool
	}{
		{"set", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(query).WithArgs(publicops.SettingKeyTypesRetained).
				WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(`["op","fact"]`))
		}, []string{"op", "fact"}, false},
		{"unset", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(query).WithArgs(publicops.SettingKeyTypesRetained).
				WillReturnRows(sqlmock.NewRows([]string{"value"}))
		}, nil, false},
		{"read error", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(query).WithArgs(publicops.SettingKeyTypesRetained).
				WillReturnError(errors.New("connection reset"))
		}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			test.script(mock)
			got, err := ResolveRetainedTypesInTx(context.Background(), db)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ResolveRetainedTypesInTx = %v, nil; want the read error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRetainedTypesInTx: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("ResolveRetainedTypesInTx = %v, want %v", got, test.want)
			}
			for _, name := range test.want {
				if !got[name] {
					t.Errorf("ResolveRetainedTypesInTx = %v, missing %q", got, name)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}
