package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
)

// operandRecordingConn records every statement a remote-operation SQL API
// issues and can fail after the first N execs, so tests can prove an invalid
// operand was refused BEFORE any SQL ran and a valid one reached its first
// statement. Queries are unsupported and always error: nothing in these tests
// reads rows.
type operandRecordingConn struct {
	mu        sync.Mutex
	execCalls []string
	// succeedFirstExecs makes the first N ExecContext calls succeed; every
	// later one errors. 0 means every exec errors.
	succeedFirstExecs int
}

var errOperandConnExec = errors.New("operandRecordingConn: no further execs allowed")

func (c *operandRecordingConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execCalls = append(c.execCalls, query)
	if len(c.execCalls) <= c.succeedFirstExecs {
		return nil, nil
	}
	return nil, errOperandConnExec
}

func (c *operandRecordingConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errOperandConnExec
}

func (c *operandRecordingConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func (c *operandRecordingConn) execCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.execCalls)
}

func (c *operandRecordingConn) firstExec() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.execCalls) == 0 {
		return ""
	}
	return c.execCalls[0]
}

// TestRemoteSQLOperationsRejectLeadingDash is the shared-SQL boundary of the
// option-injection fix (GH#7876): the remote SQL API both drivers call must
// refuse an option-shaped name before issuing any remote-operation SQL, so
// direct programmatic callers cannot bypass the CLI's argument validation.
func TestRemoteSQLOperationsRejectLeadingDash(t *testing.T) {
	ctx := context.Background()
	const wantErr = `invalid remote name "--force": must not start with '-'`

	cases := []struct {
		name string
		run  func(DBConn) error
	}{
		{name: "add_remote", run: func(db DBConn) error { return AddRemote(ctx, db, "--force", "file:///tmp/r", "") }},
		{name: "add_remote_with_ref", run: func(db DBConn) error { return AddRemote(ctx, db, "--force", "file:///tmp/r", "refs/dolt/data") }},
		{name: "remove_remote", run: func(db DBConn) error { return RemoveRemote(ctx, db, "--force") }},
		{name: "fetch", run: func(db DBConn) error { return Fetch(ctx, db, "--force", "") }},
		{name: "push", run: func(db DBConn) error { return Push(ctx, db, "--force", "main", "") }},
		{name: "force_push", run: func(db DBConn) error { return ForcePush(ctx, db, "--force", "main", "") }},
		{name: "pull_with_strategy", run: func(db DBConn) error { return PullWithStrategy(ctx, db, "--force", "main", "", "theirs") }},
		// Pull delegates straight to PullWithStrategy; one row covers both.
		{name: "pull", run: func(db DBConn) error { return Pull(ctx, db, "--force", "main", "") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &operandRecordingConn{}
			err := tc.run(conn)
			if err == nil {
				t.Fatal("remote SQL API accepted an option-shaped operand; want the leading-dash refusal")
			}
			if err.Error() != wantErr {
				t.Fatalf("refusal = %q, want exactly %q", err.Error(), wantErr)
			}
			if got := conn.execCount(); got != 0 {
				t.Fatalf("refused operation still issued %d SQL statement(s), first = %q; the refusal must precede any SQL", got, conn.firstExec())
			}
		})
	}
}

// TestRemoteSQLOperationsValidOperandControl is the control: a legal
// numeric-leading name must reach the transport SQL, not be refused as
// option-shaped.
func TestRemoteSQLOperationsValidOperandControl(t *testing.T) {
	ctx := context.Background()

	t.Run("push", func(t *testing.T) {
		conn := &operandRecordingConn{succeedFirstExecs: 1}
		if err := Push(ctx, conn, "1st", "main", ""); err != nil {
			t.Fatalf("Push to remote 1st = %v, want nil", err)
		}
		if exec := conn.firstExec(); !strings.Contains(exec, "DOLT_PUSH") {
			t.Fatalf("first exec = %q, want the DOLT_PUSH statement", exec)
		}
	})

	t.Run("pull_with_strategy", func(t *testing.T) {
		// The fetch is the first exec; later statements error on the
		// recording conn, which proves the operand passed validation and the
		// call reached the merge machinery.
		conn := &operandRecordingConn{succeedFirstExecs: 1}
		err := PullWithStrategy(ctx, conn, "1st", "main", "", "")
		if err == nil {
			t.Fatal("PullWithStrategy unexpectedly completed against the recording conn")
		}
		if strings.Contains(err.Error(), "invalid remote name") {
			t.Fatalf("valid operand 1st was refused as option-shaped: %v", err)
		}
		if exec := conn.firstExec(); !strings.Contains(exec, "DOLT_FETCH") {
			t.Fatalf("first exec = %q, want the DOLT_FETCH statement", exec)
		}
	})

	t.Run("add_remote", func(t *testing.T) {
		conn := &operandRecordingConn{succeedFirstExecs: 1}
		if err := AddRemote(ctx, conn, "1st", "file:///tmp/r", ""); err != nil {
			t.Fatalf("AddRemote(1st) = %v, want nil", err)
		}
		if exec := conn.firstExec(); !strings.Contains(exec, "DOLT_REMOTE") {
			t.Fatalf("first exec = %q, want the DOLT_REMOTE statement", exec)
		}
	})
}
