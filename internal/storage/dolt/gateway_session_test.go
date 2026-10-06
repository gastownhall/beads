package dolt

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// gatewaySessionMock stands in for a gateway that answers SELECT DATABASE()
// with the database it actually put the session on — which need not be the one
// named in the handshake.
func gatewaySessionMock(t *testing.T, answer any) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New(): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(answer))
	return db
}

func TestAssertGatewaySessionDatabase(t *testing.T) {
	ctx := context.Background()

	t.Run("session on the requested database passes", func(t *testing.T) {
		if err := assertGatewaySessionDatabase(ctx, gatewaySessionMock(t, "team_b"), "team_b"); err != nil {
			t.Fatalf("assertGatewaySessionDatabase() = %v, want nil", err)
		}
	})

	t.Run("case-folded match passes", func(t *testing.T) {
		if err := assertGatewaySessionDatabase(ctx, gatewaySessionMock(t, "Team_B"), "team_b"); err != nil {
			t.Fatalf("assertGatewaySessionDatabase() = %v, want nil", err)
		}
	})

	// Before this assertion the direct gateway open accepted the mismatch in
	// silence: bd went on to read team_a's identity and adopt it as the
	// workspace's own, on a `bd init --database=team_b` that never reached
	// team_b at all.
	t.Run("credential-scoped gateway is caught and both databases are named", func(t *testing.T) {
		err := assertGatewaySessionDatabase(ctx, gatewaySessionMock(t, "team_a"), "team_b")
		if err == nil {
			t.Fatal("assertGatewaySessionDatabase() = nil, want a mismatch error")
		}
		for _, want := range []string{
			`connected this session to database "team_a"`,
			`not the requested "team_b"`,
			"ask the server administrator",
			"re-run init without --database",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("mismatch message %q missing %q", err.Error(), want)
			}
		}
	})

	t.Run("no session database is reported as not provisioned or not granted", func(t *testing.T) {
		for _, answer := range []any{nil, ""} {
			err := assertGatewaySessionDatabase(ctx, gatewaySessionMock(t, answer), "team_b")
			if err == nil {
				t.Fatalf("assertGatewaySessionDatabase(%v) = nil, want an error", answer)
			}
			for _, want := range []string{"no database", "not provisioned", "has not been granted access"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("message %q missing %q", err.Error(), want)
				}
			}
		}
	})

	t.Run("query failure surfaces the driver error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New(): %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cause := errors.New("connection reset by peer")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).WillReturnError(cause)

		if got := assertGatewaySessionDatabase(ctx, db, "team_b"); !errors.Is(got, cause) {
			t.Fatalf("assertGatewaySessionDatabase() = %v, want it to wrap %v", got, cause)
		}
	})
}
