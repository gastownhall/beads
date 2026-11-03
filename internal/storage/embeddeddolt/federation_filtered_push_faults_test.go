//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// faultConn fails the first statement containing match with fail or, when fail
// is nil, runs it and then calls after. Other statements pass through, so the
// fault is an actual SQL error return rather than a stage hook. Ported from the
// GLM candidate's connection-level fault injection.
type faultConn struct {
	versioncontrolops.BlockedRecomputeConn
	match string
	fail  error
	after func()
	hit   bool
}

func (c *faultConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.hit || !strings.Contains(query, c.match) {
		return c.BlockedRecomputeConn.ExecContext(ctx, query, args...)
	}
	c.hit = true
	if c.fail != nil {
		return nil, c.fail
	}
	res, err := c.BlockedRecomputeConn.ExecContext(ctx, query, args...)
	if c.after != nil {
		c.after()
	}
	return res, err
}

// runFilteredPush runs one filtered publication of main against peerTip on a
// pinned connection, wrapped by fc when it is non-nil, under the caller
// contract of filteredPushToPeer: cleanup is armed first and runs on a fresh
// bounded context. push stands in for the transport.
func (f *filteredSyncFixture) runFilteredPush(t *testing.T, ctx context.Context, peerTip string, fc *faultConn, push func(versioncontrolops.DBConn, string) error) (res versioncontrolops.FilteredPushResult, err error) {
	t.Helper()
	staging, keep := federationStagingBranchPrefix+"faults", federationKeepBranchPrefix+"faults"
	err = f.store.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) (retErr error) {
		conn := db.(versioncontrolops.BlockedRecomputeConn)
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if err := versioncontrolops.CleanupFilteredPush(cleanupCtx, db, "main", staging, keep); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("cleanup: %w", err))
			}
		}()
		if fc != nil {
			fc.BlockedRecomputeConn = conn
			conn = fc
		}
		res, retErr = versioncontrolops.RunFilteredPush(ctx, conn, versioncontrolops.FilteredPushRequest{
			Source:       "main",
			Staging:      staging,
			Keep:         keep,
			PeerTip:      peerTip,
			ExcludeTypes: []string{"chore", "wisp"},
			Author:       commitAuthor,
			Push:         func(_ context.Context, refspec string) error { return push(db, refspec) },
		})
		return retErr
	})
	return res, err
}

func (f *filteredSyncFixture) head(t *testing.T, branch string) string {
	t.Helper()
	var hash string
	if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT hash FROM dolt_branches WHERE name = ?", branch).Scan(&hash)
	}); err != nil {
		t.Fatalf("read %s head: %v", branch, err)
	}
	return hash
}

func (f *filteredSyncFixture) assertNoMerge(t *testing.T, label string) {
	t.Helper()
	var merging bool
	if err := f.store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT is_merging FROM dolt_merge_status").Scan(&merging)
	}); err != nil {
		t.Fatalf("%s: read merge status: %v", label, err)
	}
	if merging {
		t.Fatalf("%s: a merge is left in progress", label)
	}
}

func countPushes(pushes *int) func(versioncontrolops.DBConn, string) error {
	return func(versioncontrolops.DBConn, string) error {
		*pushes++
		return nil
	}
}

// A DELETE or keep DOLT_MERGE that returns a SQL error publishes nothing,
// reports the error, leaves the source where it was and lets the caller's
// cleanup restore the checkout and remove the temporary branches.
func TestFilteredPushConnectionFaultsKeepLocalData(t *testing.T) {
	for _, tc := range []struct{ name, match string }{
		{"delete", "DELETE FROM issues"},
		{"keep merge", "CALL DOLT_MERGE('--author'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFilteredSyncFixture(t)
			before := f.head(t, "main")
			errInjected := errors.New("injected " + tc.name + " failure")
			var pushes int
			fc := &faultConn{match: tc.match, fail: errInjected}
			res, err := f.runFilteredPush(t, t.Context(), f.tipHash(t), fc, countPushes(&pushes))
			if !errors.Is(err, errInjected) {
				t.Fatalf("RunFilteredPush error = %v, want the injected failure", err)
			}
			if !fc.hit || pushes != 0 || res.Pushed {
				t.Fatalf("fault hit=%v pushes=%d pushed=%v, want the fault and no push", fc.hit, pushes, res.Pushed)
			}
			if got := f.head(t, "main"); got != before {
				t.Fatalf("main moved from %s to %s", before, got)
			}
			f.assertNoMerge(t, tc.name)
			f.assertLocalIntact(t, tc.name)
		})
	}
}

// A writer that advances the source after the pre-merge check, with a change
// the keep branch conflicts with, makes the keep merge conflict for real. On a
// session allowing commit conflicts the conflicted merge is left in progress;
// the publication aborts only that merge, on a fresh context even when the
// sync was canceled, keeps the other writer's commit and pushes nothing.
func TestFilteredPushAbortsItsConflictedKeepMerge(t *testing.T) {
	for _, cancelAfterMerge := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%v", cancelAfterMerge), func(t *testing.T) {
			f := newFilteredSyncFixture(t)
			tip := f.tipHash(t)
			var otherCommit string
			t.Cleanup(versioncontrolops.SetFilteredPushStageHookForTest(func(ctx context.Context, stage string, conn versioncontrolops.DBConn, req versioncontrolops.FilteredPushRequest) error {
				if stage != "merge" {
					return nil
				}
				for _, stmt := range []string{
					"UPDATE issues SET title = 'source side' WHERE id = '" + f.task + "'",
					"CALL DOLT_COMMIT('-am', 'other writer')",
					"CALL DOLT_CHECKOUT('" + req.Keep + "')",
					"UPDATE issues SET title = 'keep side' WHERE id = '" + f.task + "'",
					"CALL DOLT_COMMIT('-am', 'conflicting keep change')",
					"CALL DOLT_CHECKOUT('" + req.Source + "')",
					"SET @@dolt_allow_commit_conflicts = 1",
				} {
					if _, err := conn.ExecContext(ctx, stmt); err != nil {
						return err
					}
				}
				return conn.QueryRowContext(ctx, "SELECT hash FROM dolt_branches WHERE name = ?", req.Source).Scan(&otherCommit)
			}))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fc := &faultConn{match: "CALL DOLT_MERGE('--author'"}
			if cancelAfterMerge {
				fc.after = cancel
			}
			var pushes int
			res, err := f.runFilteredPush(t, ctx, tip, fc, countPushes(&pushes))
			want := versioncontrolops.ErrFilteredPushSourceChanged
			if cancelAfterMerge {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("RunFilteredPush error = %v, want %v", err, want)
			}
			if pushes != 0 || res.Pushed {
				t.Fatalf("pushes=%d pushed=%v, want no publication", pushes, res.Pushed)
			}
			f.assertNoMerge(t, "conflict")
			if got := f.head(t, "main"); got != otherCommit {
				t.Fatalf("main is at %s, want the other writer's commit %s", got, otherCommit)
			}
			if n := countAt(t, f.store, "main", "SELECT COUNT(*) FROM issues %s WHERE id = ? AND title = 'source side'", f.task); n != 1 {
				t.Fatal("the other writer's change was lost")
			}
			f.assertLocalIntact(t, "conflict")
		})
	}
}

// A filtered payload that matches the fetched peer tip publishes nothing and
// merges no keep branch, so no filter/revert pair is retained in the source.
func TestFilteredPushUnchangedPayloadPublishesNothing(t *testing.T) {
	f := newFilteredSyncFixture(t)
	var published string
	res, err := f.runFilteredPush(t, t.Context(), f.tipHash(t), nil, func(db versioncontrolops.DBConn, refspec string) error {
		staging, _, _ := strings.Cut(refspec, ":")
		return db.QueryRowContext(t.Context(), "SELECT hash FROM dolt_branches WHERE name = ?", staging).Scan(&published)
	})
	if err != nil || !res.Pushed || !res.Filtered {
		t.Fatalf("first publication: pushed=%v filtered=%v err=%v, want a filtered push", res.Pushed, res.Filtered, err)
	}

	// The peer now holds the first publication; nothing has changed since.
	before := f.head(t, "main")
	var pushes int
	res, err = f.runFilteredPush(t, t.Context(), published, nil, countPushes(&pushes))
	if err != nil {
		t.Fatalf("second publication: %v", err)
	}
	if !res.Unchanged || res.Pushed || pushes != 0 {
		t.Fatalf("second publication: unchanged=%v pushed=%v pushes=%d, want an unchanged payload and no push", res.Unchanged, res.Pushed, pushes)
	}
	if got := f.head(t, "main"); got != before {
		t.Fatalf("main moved from %s to %s: an unchanged payload retained commits", before, got)
	}
	f.assertLocalIntact(t, "unchanged")
}
