package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/workapi"
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

// TestRetainedTypesParseAsTypesCustomDoes pins that types.retained takes
// exactly the spellings types.custom does: workapi owns the retained-type
// parse (it sits below this package), and a drift between the two would make
// `bd config set types.retained` accept a value the sweep reads differently.
func TestRetainedTypesParseAsTypesCustomDoes(t *testing.T) {
	for _, value := range []string{
		"", " ", "op", "op,fact", " op , ,fact,op ", `["op","fact"]`, `[" op ",""]`, `[]`, `["op"`, "a b,c",
	} {
		want := map[string]bool{}
		for _, name := range ParseTypesConfigValue(value) {
			want[name] = true
		}
		got := workapi.RetainedTypesFromSetting(value)
		if len(got) != len(want) {
			t.Errorf("value %q: retained %v, types.custom parse %v", value, got, want)
			continue
		}
		for name := range want {
			if !got[name] {
				t.Errorf("value %q: retained %v, types.custom parse %v", value, got, want)
			}
		}
	}
}
