package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// failingRemoteConn answers every statement with err, standing in for a
// transfer procedure that cannot reach its remote.
type failingRemoteConn struct{ err error }

func (c *failingRemoteConn) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, c.err
}

func (c *failingRemoteConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *failingRemoteConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// TestRemoteTransferErrorsRedactCredentials pins the embedded-mode half of the
// credential leak: embedded mode reaches every push, pull and fetch through
// these CALL DOLT_PUSH/DOLT_FETCH statements, and Dolt's error names the
// remote's stored URL, token and all (the text below is CALL DOLT_PUSH's for a
// git+https remote it cannot reach). The wrapped error must drop the userinfo,
// keep the host, path and Dolt's detail, and still unwrap to Dolt's error.
func TestRemoteTransferErrorsRedactCredentials(t *testing.T) {
	ctx := context.Background()
	const detail = "failed to get remote db; the remote: origin '%s' could not be accessed; fatal: unable to access 'https://127.0.0.1:1/org/repo/': Couldn't connect to server"
	doltErr := fmt.Errorf(detail, "git+https://x-access-token:ghp_TESTTOKEN@127.0.0.1:1/org/repo")
	redacted := fmt.Sprintf(detail, "git+https://127.0.0.1:1/org/repo")

	tests := []struct {
		name string
		call func(DBConn) error
		want string
	}{
		{"Push", func(db DBConn) error { return Push(ctx, db, "origin", "main", "") }, "push to origin/main: " + redacted},
		{"Push with user", func(db DBConn) error { return Push(ctx, db, "origin", "main", "u") }, "push to origin/main: " + redacted},
		{"ForcePush", func(db DBConn) error { return ForcePush(ctx, db, "origin", "main", "") }, "force push to origin/main: " + redacted},
		{"Fetch", func(db DBConn) error { return Fetch(ctx, db, "origin", "") }, "fetch from origin: " + redacted},
		{"Pull", func(db DBConn) error { return Pull(ctx, db, "origin", "main", "") }, "fetch from origin/main: " + redacted},
		{"PullWithStrategy", func(db DBConn) error { return PullWithStrategy(ctx, db, "origin", "main", "u", "theirs") }, "fetch from origin/main: " + redacted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(&failingRemoteConn{err: doltErr})
			if err == nil {
				t.Fatal("transfer = nil error, want the statement's failure")
			}
			if got := err.Error(); got != tt.want {
				t.Errorf("error =\n%q\nwant\n%q", got, tt.want)
			}
			if !errors.Is(err, doltErr) {
				t.Errorf("errors.Is(err, doltErr) = false: %v", err)
			}
		})
	}
}

// TestRemoteTransferErrorsKeepCredentialFreeText pins that an error naming no
// credential-bearing URL is wrapped exactly as before.
func TestRemoteTransferErrorsKeepCredentialFreeText(t *testing.T) {
	doltErr := errors.New("fatal: unable to access 'https://127.0.0.1:1/org/repo/': Couldn't connect to server")
	err := Push(context.Background(), &failingRemoteConn{err: doltErr}, "origin", "main", "")
	if want := "push to origin/main: " + doltErr.Error(); err == nil || err.Error() != want {
		t.Fatalf("Push error = %v, want %q", err, want)
	}
	if !errors.Is(err, doltErr) {
		t.Errorf("errors.Is(err, doltErr) = false: %v", err)
	}
}
