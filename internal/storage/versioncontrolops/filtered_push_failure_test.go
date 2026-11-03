package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// These tests pin PushFilteredBranch's failure-before-publication contract
// (AC: GH#7890): a filtering or keep-merge failure must reach the caller as an
// error without the push callback ever running, and an ordinary recoverable
// failure — a live connection, an uncanceled context — must restore the
// source checkout so no caller is left on a branch whose life ends with its
// cleanup.

// filteredPushHookConn is the connection PushFilteredBranch runs on. It
// forwards every statement after failing the first one whose query text
// matches failQuery, without running it. Both statement paths hook: the
// DELETE runs through ExecContext while the DOLT_* procedures run through
// QueryContext (schema.DrainCall).
type filteredPushHookConn struct {
	*sql.Conn

	failQuery string // first matching statement fails instead of running
	failWith  error  // the error to fail it with
}

// maybeFail returns the injected failure when query matches, consuming it.
func (c *filteredPushHookConn) maybeFail(query string) error {
	if c.failQuery != "" && strings.Contains(query, c.failQuery) {
		err := c.failWith
		c.failQuery = "" // fail only the first matching statement
		return err
	}
	return nil
}

func (c *filteredPushHookConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := c.maybeFail(query); err != nil {
		return nil, err
	}
	return c.Conn.ExecContext(ctx, query, args...)
}

func (c *filteredPushHookConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if err := c.maybeFail(query); err != nil {
		return nil, err
	}
	return c.Conn.QueryContext(ctx, query, args...)
}

func (c *filteredPushHookConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.Conn.QueryRowContext(ctx, query, args...)
}

func (c *filteredPushHookConn) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return c.Conn.BeginTx(ctx, opts)
}

// newFilteredPushFixture starts one database on a test server with the real
// issue schema and a versioned wisp plane (the staged is_blocked recompute
// reads wisp tables, which a fresh staging branch would otherwise lack), then
// commits one excluded-typed issue on the source branch.
func newFilteredPushFixture(t *testing.T) (ctx context.Context, db *sql.DB, conn *filteredPushHookConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	port := testutil.StartIsolatedDoltContainerHandle(t).Port
	name := fmt.Sprintf("filtered_push_%d", time.Now().UnixNano())
	root, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/?multiStatements=true", port))
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer root.Close()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	db, err = sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?multiStatements=true", port, name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { _ = pinned.Close() })
	conn = &filteredPushHookConn{Conn: pinned}

	for _, q := range []string{
		"CALL DOLT_CHECKOUT('main')",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("materialize schema: %v", err)
	}
	for _, q := range []string{
		`DELETE FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')`,
		"CALL DOLT_ADD('-A')",
		"CALL DOLT_COMMIT('-m', 'test: version empty wisp plane for filtered push')",
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
		 VALUES ('ff-new-secret', 'filtered push seed', '', '', '', '', 'open', 2, 'secret')`,
		"CALL DOLT_COMMIT('-Am', 'seed source branch')",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return ctx, db, conn
}

// filteredPushFailureCase runs PushFilteredBranch with the first statement
// matching failQuery hooked to fail, and asserts the failure-before-
// publication contract: the push callback never runs, the injected failure
// reaches the caller, the source checkout is restored (the session is not
// left on the staging branch), the source keeps its excluded issue, and the
// caller's cleanup contract — both temporary branches delete cleanly on a
// live connection — still holds (AC: GH#7890).
func filteredPushFailureCase(t *testing.T, failQuery string) {
	ctx, db, conn := newFilteredPushFixture(t)

	sourceBranch := "main"
	stagingBranch := "__federation_push_staging_" + strings.ToLower(t.Name())
	keepBranch := strings.Replace(stagingBranch, "_staging_", "_keep_", 1)
	conn.failQuery, conn.failWith = failQuery, fmt.Errorf("hooked: injected %s failure", failQuery)

	pushCalls := 0
	_, err := PushFilteredBranch(ctx, conn, FilteredPushOptions{
		SourceBranch:  sourceBranch,
		StagingBranch: stagingBranch,
		KeepBranch:    keepBranch,
		ExcludeTypes:  []string{"secret"},
		Push: func(context.Context, string) error {
			pushCalls++
			return nil
		},
	})
	if err == nil {
		t.Fatal("failure before publication did not fail the publication")
	}
	if !strings.Contains(err.Error(), "hooked: injected") {
		t.Fatalf("publication error = %v, want the injected failure", err)
	}
	if pushCalls != 0 {
		t.Fatalf("push callback ran %d times after a pre-publication failure, want 0", pushCalls)
	}
	if branch, branchErr := CurrentBranch(ctx, conn); branchErr != nil {
		t.Fatalf("read current branch after failure: %v", branchErr)
	} else if branch != sourceBranch {
		t.Fatalf("session left on branch %s after a pre-publication failure, want %s", branch, sourceBranch)
	}
	var n int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = 'ff-new-secret'").Scan(&n); err != nil {
		t.Fatalf("read source issues: %v", err)
	}
	if n != 1 {
		t.Fatalf("source excluded issue count = %d, want 1 (this operation must not delete it)", n)
	}
	// The caller's cleanup contract, as both drivers implement it: the
	// staging branch deletes cleanly, and a temporary branch that never came
	// to exist is tolerated.
	for _, branch := range []string{stagingBranch, keepBranch} {
		if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", branch); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "branch not found") {
			t.Fatalf("delete temporary branch %s: %v", branch, err)
		}
	}
}

// A filtering failure before publication (the DELETE of excluded issues
// fails) fails the whole publication safely.
func TestPushFilteredBranchFilterFailureRestoresSourceWithoutPush(t *testing.T) {
	filteredPushFailureCase(t, "DELETE FROM issues")
}

// A keep-merge failure before publication (the keep merge into the source
// branch fails) behaves the same way.
func TestPushFilteredBranchKeepMergeFailureRestoresSourceWithoutPush(t *testing.T) {
	filteredPushFailureCase(t, "DOLT_MERGE")
}
