package dolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	storagememoryops "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/memoryops"
)

// oneStatementReadSite is a store read that sends exactly one SQL statement,
// with what the mock should expect for that statement and a call that checks
// the answer.
type oneStatementReadSite struct {
	name   string
	expect func(sqlmock.Sqlmock)
	read   func(*DoltStore) error
}

var (
	countIssuesQuery     = `SELECT COUNT\(\*\) FROM issues`
	externalBlockerQuery = `(?s)FROM dependencies.*UNION ALL.*FROM wisp_dependencies.*ORDER BY issue_id, plane\s*$`
	listPageQuery        = `(?s)FROM issues i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC`
	claimStateQuery      = regexp.QuoteMeta("SELECT assignee, status FROM issues WHERE id = ?")
	memoryRecallQuery    = regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")
	memoryListQuery      = regexp.QuoteMeta("SELECT `key`, value FROM config")
)

func oneStatementReadSites() []oneStatementReadSite {
	return []oneStatementReadSite{
		{
			name: "CountIssues on the durable plane",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(countIssuesQuery).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(7))
			},
			read: func(s *DoltStore) error {
				n, err := s.CountIssues(context.Background(), "", types.IssueFilter{SkipWisps: true})
				if err == nil && n != 7 {
					return fmt.Errorf("CountIssues = %d, want 7", n)
				}
				return err
			},
		},
		{
			name: "memory Recall",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(memoryRecallQuery).WithArgs(storagememoryops.StorageKey("k")).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("v"))
			},
			read: func(s *DoltStore) error {
				mem, err := s.Memories()
				if err != nil {
					return err
				}
				got, err := mem.Recall(context.Background(), memoryops.RecallRequest{Key: "k"})
				if err == nil && (!got.Found || got.Value != "v") {
					return fmt.Errorf("Recall = %+v, want v", got)
				}
				return err
			},
		},
		{
			name: "memory List",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(memoryListQuery).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
					AddRow(storagememoryops.StorageKey("k"), "v").AddRow("issue_prefix", "bd"))
			},
			read: func(s *DoltStore) error {
				mem, err := s.Memories()
				if err != nil {
					return err
				}
				got, err := mem.List(context.Background(), memoryops.ListRequest{})
				if err == nil && (len(got.Memories) != 1 || got.Memories["k"] != "v") {
					return fmt.Errorf("List = %+v, want only k", got.Memories)
				}
				return err
			},
		},
		{
			name: "post-claim verification read",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").
					WillReturnRows(sqlmock.NewRows([]string{"assignee", "status"}).AddRow("alice", "in_progress"))
			},
			read: func(s *DoltStore) error {
				assignee, status, err := s.readClaimState(context.Background(), "bd-1")
				if err == nil && (assignee != "alice" || status != types.StatusInProgress) {
					return fmt.Errorf("readClaimState = %q, %q", assignee, status)
				}
				return err
			},
		},
		{
			name: "external blockers",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(externalBlockerQuery).WillReturnRows(sqlmock.NewRows([]string{
					"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id", "plane",
				}).
					AddRow("bd-1", "external:remote:a", "blocks", time.Now(), "t", "{}", "", 0).
					AddRow("bd-w", "external:remote:b", "waits-for", time.Now(), "t", "{}", "", 1))
			},
			read: func(s *DoltStore) error {
				got, err := s.GetExternalBlockingDependencyRecords(context.Background())
				if err == nil && (len(got["bd-1"]) != 1 || len(got["bd-w"]) != 1) {
					return fmt.Errorf("GetExternalBlockingDependencyRecords = %+v", got)
				}
				return err
			},
		},
		{
			name: "bd list page",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(listPageQuery).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			},
			read: func(s *DoltStore) error {
				_, err := s.SearchIssuesWithCounts(context.Background(), "",
					types.IssueFilter{SkipWisps: true, SkipCounts: true})
				return err
			},
		},
	}
}

// TestOneStatementReadSitesOpenNoTransaction pins what each of these reads
// costs a server-mode store with autocommit on, counted at the driver: the store's
// one read of the autocommit mode, then the statement. Each used to sit between
// START TRANSACTION and ROLLBACK, two more round trips for a consistency one
// statement already has: a single statement reads one snapshot.
//
// sqlmock refuses a Begin nothing expected, so a read that went back to a
// transaction fails here as well as in the counts below.
func TestOneStatementReadSitesOpenNoTransaction(t *testing.T) {
	for _, tc := range oneStatementReadSites() {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			expectAutocommit(mock, 1)
			tc.expect(mock)
			if err := tc.read(store); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			got := counts.Snapshot()
			if got.Begin != 0 || got.Rollback != 0 {
				t.Errorf("read opened a transaction: %+v", got)
			}
			if got.Total() != 2 {
				t.Errorf("read sent %d statements, want the mode read and the read: %+v", got.Total(), got)
			}
		})
	}
}

// TestOneStatementReadSitesKeepATransactionWithoutAutocommit: with autocommit
// off (dolt sql-server --no-auto-commit) a bare SELECT holds its session on that
// snapshot, so each of these reads keeps the transaction that refreshes it.
func TestOneStatementReadSitesKeepATransactionWithoutAutocommit(t *testing.T) {
	for _, tc := range oneStatementReadSites() {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			expectAutocommit(mock, 0)
			mock.ExpectBegin()
			tc.expect(mock)
			mock.ExpectRollback()
			if err := tc.read(store); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if got := counts.Snapshot(); got.Begin != 1 || got.Rollback != 1 {
				t.Errorf("read without autocommit = %+v, want one transaction", got)
			}
		})
	}
}

// TestReadsOfSeveralStatementsKeepTheirTransaction: the paths beside the
// one-statement ones send more than one statement that must agree, so they keep
// one read transaction and never ask the autocommit mode.
func TestReadsOfSeveralStatementsKeepTheirTransaction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		read   func(*DoltStore) error
	}{
		{
			name: "CountIssues merging the wisp tier",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(countIssuesQuery).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
				m.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(3))
			},
			read: func(s *DoltStore) error {
				n, err := s.CountIssues(context.Background(), "", types.IssueFilter{})
				if err == nil && n != 5 {
					return fmt.Errorf("CountIssues = %d, want 5", n)
				}
				return err
			},
		},
		{
			name: "search with dependency counts",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				m.ExpectQuery(listPageQuery).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			},
			read: func(s *DoltStore) error {
				_, err := s.SearchIssuesWithCounts(context.Background(), "", types.IssueFilter{SkipWisps: true})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			// With the mode unknown, a read moved onto withSingleRead would send the
			// probe, sqlmock would refuse it, and the read would fall back to the
			// transaction this test expects. With autocommit known to be on, only a
			// read that really keeps its transaction passes.
			store.autocommit.Store(1)
			mock.ExpectBegin()
			tc.expect(mock)
			mock.ExpectRollback()
			if err := tc.read(store); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if got := counts.Snapshot(); got.Begin != 1 || got.Rollback != 1 {
				t.Errorf("read = %+v, want one transaction", got)
			}
		})
	}
}

// TestGetStatisticsReadsInOneTransaction: the summary is two SELECTs, the status
// counts and the blocked count, and ReadyIssues is computed from both, so they
// must read one snapshot. They used to run in two transactions of one statement
// each; one transaction is one START TRANSACTION and ROLLBACK pair fewer and
// makes the two counts agree.
func TestGetStatisticsReadsInOneTransaction(t *testing.T) {
	store, mock, counts := vhCountedStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT\s+COUNT\(\*\) AS total`).WillReturnRows(sqlmock.NewRows([]string{
		"total", "open", "in_progress", "closed", "deferred", "pinned", "gate", "template",
	}).AddRow(10, 5, 1, 2, 1, 0, 0, 1))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM issues\s+WHERE is_blocked = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
	mock.ExpectRollback()

	stats, err := store.GetStatistics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if stats.TotalIssues != 10 || stats.BlockedIssues == nil || *stats.BlockedIssues != 2 ||
		stats.ReadyIssues == nil || *stats.ReadyIssues != 3 {
		t.Fatalf("GetStatistics = %+v, want total 10, blocked 2, ready 3", stats)
	}
	got := counts.Snapshot()
	if got.Begin != 1 || got.Rollback != 1 || got.Query != 2 {
		t.Fatalf("GetStatistics sent %+v, want one transaction around two reads", got)
	}
}

// TestExternalBlockingDependencyRecordsReadsBothPlanes runs the one-statement
// external-blocker read against Dolt: explicit external: edges of the three
// blocking types on both planes, and nothing else.
func TestExternalBlockingDependencyRecordsReadsBothPlanes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, iss := range []*types.Issue{
		{ID: "eb-perm", Title: "eb-perm", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "eb-local", Title: "eb-local", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "eb-wisp", Title: "eb-wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", iss.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "eb-perm", DependsOnID: "external:remote:a", Type: types.DepBlocks},
		{IssueID: "eb-perm", DependsOnID: "external:remote:b", Type: types.DepConditionalBlocks},
		{IssueID: "eb-perm", DependsOnID: "external:remote:c", Type: types.DepRelated},
		{IssueID: "eb-perm", DependsOnID: "eb-local", Type: types.DepBlocks},
		{IssueID: "eb-wisp", DependsOnID: "external:remote:d", Type: types.DepWaitsFor},
	} {
		if err := store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("add %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	got, err := store.GetExternalBlockingDependencyRecords(ctx)
	if err != nil {
		t.Fatalf("GetExternalBlockingDependencyRecords: %v", err)
	}
	targets := func(id string) []string {
		var out []string
		for _, d := range got[id] {
			out = append(out, d.DependsOnID+" "+string(d.Type))
		}
		return out
	}
	if p := strings.Join(targets("eb-perm"), ", "); p != "external:remote:a blocks, external:remote:b conditional-blocks" &&
		p != "external:remote:b conditional-blocks, external:remote:a blocks" {
		t.Errorf("durable external blockers = %q", p)
	}
	if w := strings.Join(targets("eb-wisp"), ", "); w != "external:remote:d waits-for" {
		t.Errorf("wisp external blockers = %q", w)
	}
	if len(got) != 2 {
		t.Errorf("external blockers keyed by %d issues, want 2: %+v", len(got), got)
	}

	// A working-set reset (a received dolt push) drops the ignored table. Dolt's
	// error must be recognized as that table's absence, and the read answers the
	// durable plane.
	execOnOneConn(t, ctx, store, "DROP TABLE wisp_dependencies")
	got, err = store.GetExternalBlockingDependencyRecords(ctx)
	if err != nil {
		t.Fatalf("GetExternalBlockingDependencyRecords without wisp_dependencies: %v", err)
	}
	if len(got) != 1 || len(got["eb-perm"]) != 2 {
		t.Errorf("external blockers without wisp_dependencies = %+v, want eb-perm's two", got)
	}
}

// TestExternalBlockersFallbackRereadsBothPlanesInOneTransaction: when the
// one-statement read fails because wisp_dependencies is missing, nothing from it
// is kept. Both planes are read again inside one transaction, so the decision
// that the table is missing and the durable rows come from one snapshot. A
// second bare statement could not promise that: a peer can restore the table, or
// change durable edges, between the two.
func TestExternalBlockersFallbackRereadsBothPlanesInOneTransaction(t *testing.T) {
	wispMissing := errors.New("Error 1146 (42S02): table not found: wisp_dependencies")
	durableOnlyQuery := `(?s)thread_id\s+FROM dependencies\s+WHERE depends_on_external LIKE 'external:%'\s+AND type IN \('blocks', 'conditional-blocks', 'waits-for'\)\s+ORDER BY issue_id\s*$`
	unionRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id", "plane",
		})
	}
	for _, tc := range []struct {
		name       string
		expect     func(sqlmock.Sqlmock)
		want       map[string]int // issue -> edge count
		wantErr    string
		wantBegin  int64
		statements int64
	}{
		{
			name: "the table is still missing",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(externalBlockerQuery).WillReturnError(wispMissing)
				m.ExpectBegin()
				m.ExpectQuery(externalBlockerQuery).WillReturnError(wispMissing)
				m.ExpectQuery(durableOnlyQuery).WillReturnRows(sqlmock.NewRows([]string{
					"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id",
				}).AddRow("bd-1", "external:remote:a", "blocks", time.Now(), "t", "{}", ""))
				m.ExpectRollback()
			},
			want:      map[string]int{"bd-1": 1},
			wantBegin: 1,
			// mode read, failed union, BEGIN, failed union, durable read, ROLLBACK
			statements: 6,
		},
		{
			name: "a peer restored the table between the two reads",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(externalBlockerQuery).WillReturnError(wispMissing)
				m.ExpectBegin()
				m.ExpectQuery(externalBlockerQuery).WillReturnRows(unionRows().
					AddRow("bd-1", "external:remote:a", "blocks", time.Now(), "t", "{}", "", 0).
					AddRow("bd-w", "external:remote:b", "waits-for", time.Now(), "t", "{}", "", 1))
				m.ExpectRollback()
			},
			want:      map[string]int{"bd-1": 1, "bd-w": 1},
			wantBegin: 1,
			// mode read, failed union, BEGIN, union, ROLLBACK
			statements: 5,
		},
		{
			name: "the durable table is the missing one",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(externalBlockerQuery).
					WillReturnError(errors.New("Error 1146 (42S02): table not found: dependencies"))
			},
			wantErr:    "table not found: dependencies",
			statements: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			expectAutocommit(mock, 1)
			tc.expect(mock)

			got, err := store.GetExternalBlockingDependencyRecords(context.Background())
			switch {
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("GetExternalBlockingDependencyRecords = %+v, %v; want %q", got, err, tc.wantErr)
			case tc.wantErr == "" && err != nil:
				t.Fatalf("GetExternalBlockingDependencyRecords: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if tc.wantErr == "" {
				if len(got) != len(tc.want) {
					t.Fatalf("GetExternalBlockingDependencyRecords = %+v, want %v", got, tc.want)
				}
				for id, n := range tc.want {
					if len(got[id]) != n {
						t.Fatalf("GetExternalBlockingDependencyRecords[%s] = %+v, want %d edges", id, got[id], n)
					}
				}
			}
			// Snapshot.Total leaves out BEGIN, COMMIT and ROLLBACK; every one of
			// them is a round trip here.
			snap := counts.Snapshot()
			sent := snap.Total() + snap.Begin + snap.Commit + snap.Rollback
			if snap.Begin != tc.wantBegin || sent != tc.statements {
				t.Fatalf("read sent %+v (%d statements), want %d BEGIN and %d statements", snap, sent, tc.wantBegin, tc.statements)
			}
		})
	}
}

// TestClaimVerificationBareReadFailures: with autocommit on, the verification of
// a claim and of a ready claim is one bare SELECT. Whatever happens to that read,
// the claim write runs exactly once, a claim that cannot be shown to have landed
// is never reported as a success, and an indeterminate commit is returned as such
// without a verification read.
func TestClaimVerificationBareReadFailures(t *testing.T) {
	wispProbe := regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")
	writeSQL := regexp.QuoteMeta("UPDATE issues SET assignee = ?")
	claimRow := func(assignee string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"assignee", "status"}).AddRow(assignee, "in_progress")
	}
	for _, tc := range []struct {
		name string
		// verify sets what the verification read sees, after the mode read.
		verify  func(sqlmock.Sqlmock)
		wantErr string // "" means the claim reports success
	}{
		{
			name: "the row response is lost",
			verify: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").
					WillReturnRows(claimRow("alice").RowError(0, io.ErrUnexpectedEOF))
			},
			wantErr: "could not be verified",
		},
		{
			name: "a lost connection is retried and the row matches",
			verify: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").WillReturnError(errVHReadLost)
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").WillReturnRows(claimRow("alice"))
			},
		},
		{
			name: "a bad connection is replaced and the row matches",
			verify: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").WillReturnError(driver.ErrBadConn)
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").WillReturnRows(claimRow("alice"))
			},
		},
		{
			name: "a bad connection is replaced and the row does not match",
			verify: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").WillReturnError(driver.ErrBadConn)
				m.ExpectQuery(claimStateQuery).WithArgs("bd-1").WillReturnRows(claimRow("bob"))
			},
			wantErr: "did not land",
		},
	} {
		for _, ready := range []bool{false, true} {
			kind := "claim"
			if ready {
				kind = "ready claim"
			}
			t.Run(kind+": "+tc.name, func(t *testing.T) {
				store, mock, counts := vhCountedStore(t)
				store.serverMode = true
				writes := 0
				write := func() error {
					writes++
					_, err := store.db.ExecContext(context.Background(), "UPDATE issues SET assignee = ? WHERE id = ?", "alice", "bd-1")
					return err
				}
				noWisp := func() {
					mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnRows(sqlmock.NewRows([]string{"1"}))
				}
				// A named claim checks the plane before writing; a ready claim
				// learns its ID from the write and checks the plane after it.
				if !ready {
					noWisp()
				}
				mock.ExpectExec(writeSQL).WillReturnResult(sqlmock.NewResult(0, 1))
				if ready {
					noWisp()
				}
				expectAutocommit(mock, 1)
				tc.verify(mock)

				var err error
				if ready {
					var claimed *types.Issue
					claimed, err = store.verifiedReadyClaim(context.Background(), "alice", func() (*types.Issue, error) {
						return &types.Issue{ID: "bd-1"}, write()
					})
					if err != nil && claimed != nil {
						t.Fatalf("a failed ready claim returned %+v", claimed)
					}
				} else {
					err = store.verifiedClaimWrite(context.Background(), "bd-1", claimedBy("alice"), write)
				}
				switch {
				case tc.wantErr == "" && err != nil:
					t.Fatalf("%s: %v", kind, err)
				case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
					t.Fatalf("%s = %v, want %q", kind, err, tc.wantErr)
				}
				if writes != 1 {
					t.Fatalf("the claim write ran %d times, want 1", writes)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
				if got := counts.Snapshot(); got.Begin != 0 || got.Exec != 1 {
					t.Fatalf("claim sent %+v, want one write and no transaction", got)
				}
			})
		}
	}

	t.Run("an indeterminate commit is not verified", func(t *testing.T) {
		for _, ready := range []bool{false, true} {
			store, mock, counts := vhCountedStore(t)
			store.serverMode = true
			if !ready {
				mock.ExpectQuery(wispProbe).WithArgs("bd-1").WillReturnRows(sqlmock.NewRows([]string{"1"}))
			}
			writes := 0
			indeterminate := fmt.Errorf("claim of bd-1: %w", ErrCommitIndeterminate)
			var err error
			if ready {
				var claimed *types.Issue
				claimed, err = store.verifiedReadyClaim(context.Background(), "alice", func() (*types.Issue, error) {
					writes++
					return &types.Issue{ID: "bd-1"}, indeterminate
				})
				if claimed != nil {
					t.Fatalf("an indeterminate ready claim returned %+v", claimed)
				}
			} else {
				err = store.verifiedClaimWrite(context.Background(), "bd-1", claimedBy("alice"), func() error {
					writes++
					return indeterminate
				})
			}
			if !errors.Is(err, ErrCommitIndeterminate) {
				t.Fatalf("ready=%v: err = %v, want ErrCommitIndeterminate", ready, err)
			}
			if writes != 1 {
				t.Fatalf("ready=%v: the claim write ran %d times, want 1", ready, writes)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			wantQueries := int64(0)
			if !ready {
				wantQueries = 1 // the plane check before the write
			}
			if got := counts.Snapshot(); got.Query != wantQueries || got.Begin != 0 {
				t.Fatalf("ready=%v: sent %+v, want no mode read and no verification read", ready, got)
			}
		}
	})
}

// TestPostClaimVerificationReadsLiveAfterTheWrite: the verification is a fresh
// read of the row, sent after the write has committed, never an answer carried
// over from the write. sqlmock matches in order, so the write must come before
// the read; a row that does not match the claim fails the claim.
func TestPostClaimVerificationReadsLiveAfterTheWrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		assignee string
		wantErr  string
	}{
		{name: "the claim landed", assignee: "alice"},
		{name: "the row says otherwise", assignee: "bob", wantErr: "did not land"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			store.serverMode = true
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).WithArgs("bd-1").
				WillReturnRows(sqlmock.NewRows([]string{"1"}))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE issues SET assignee = ?")).
				WillReturnResult(sqlmock.NewResult(0, 1))
			expectAutocommit(mock, 1)
			mock.ExpectQuery(claimStateQuery).WithArgs("bd-1").
				WillReturnRows(sqlmock.NewRows([]string{"assignee", "status"}).AddRow(tc.assignee, "in_progress"))

			err := store.verifiedClaimWrite(context.Background(), "bd-1", claimedBy("alice"), func() error {
				_, err := store.db.ExecContext(context.Background(), "UPDATE issues SET assignee = ? WHERE id = ?", "alice", "bd-1")
				return err
			})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("verifiedClaimWrite: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("verifiedClaimWrite = %v, want %q", err, tc.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if got := counts.Snapshot(); got.Begin != 0 {
				t.Fatalf("verification opened a transaction: %+v", got)
			}
		})
	}
}
