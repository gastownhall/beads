package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// claimCommitBoundaryDriver models a permanent claim through its SQL mutation,
// DOLT_ADD, and DOLT_COMMIT phases. It deliberately keeps the issue open after
// rollback so an unsafe retry is visible as a second claim mutation.
type claimCommitBoundaryDriver struct {
	mu sync.Mutex

	stageErr        error
	commitErr       error
	sqlCommitErr    error
	nothingToCommit bool
	checkedUpdate   bool
	verifyAssignee  string
	verifyStatus    types.Status
	activeWisp      bool

	claimMutations  int
	claimedIDs      []string
	updateMutations int
	eventInserts    int
	claimStateReads int
	stageCalls      int
	doltCommits     int
	txCommits       int
	txRollbacks     int
	txAttempts      int
	activeID        string
	readyIDs        []string
}

func (d *claimCommitBoundaryDriver) Open(string) (driver.Conn, error) {
	return &claimCommitBoundaryConn{driver: d}, nil
}

func (d *claimCommitBoundaryDriver) Connect(context.Context) (driver.Conn, error) {
	return &claimCommitBoundaryConn{driver: d}, nil
}

func (d *claimCommitBoundaryDriver) Driver() driver.Driver { return d }

type claimCommitBoundaryConn struct {
	driver *claimCommitBoundaryDriver
}

func (c *claimCommitBoundaryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("claim commit boundary driver does not prepare statements")
}

func (c *claimCommitBoundaryConn) Close() error { return nil }

func (c *claimCommitBoundaryConn) Begin() (driver.Tx, error) {
	c.driver.mu.Lock()
	c.driver.txAttempts++
	c.driver.activeID = "claim-boundary"
	if len(c.driver.readyIDs) > 0 {
		index := c.driver.txAttempts - 1
		if index >= len(c.driver.readyIDs) {
			index = len(c.driver.readyIDs) - 1
		}
		c.driver.activeID = c.driver.readyIDs[index]
	}
	c.driver.mu.Unlock()
	return &claimCommitBoundaryTx{driver: c.driver}, nil
}

func (c *claimCommitBoundaryConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	switch {
	case strings.Contains(query, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1"):
		if c.driver.activeWisp {
			return &claimCommitBoundaryRows{columns: []string{"exists"}, values: [][]driver.Value{{int64(1)}}}, nil
		}
		return &claimCommitBoundaryRows{columns: []string{"exists"}}, nil
	case strings.Contains(query, "SELECT assignee, status FROM issues WHERE id = ?"):
		c.driver.claimStateReads++
		assignee := ""
		status := types.StatusOpen
		if c.driver.checkedUpdate && c.driver.claimStateReads > 1 {
			assignee = c.driver.verifyAssignee
			status = c.driver.verifyStatus
		}
		return &claimCommitBoundaryRows{
			columns: []string{"assignee", "status"},
			values:  [][]driver.Value{{assignee, string(status)}},
		}, nil
	case strings.Contains(query, "SELECT id FROM issues"):
		return &claimCommitBoundaryRows{
			columns: []string{"id"},
			values:  [][]driver.Value{{c.driver.activeID}},
		}, nil
	case strings.Contains(query, "FROM issues") && strings.Contains(query, "LEFT JOIN leases") && strings.Contains(query, "WHERE id IN ("):
		return &claimCommitBoundaryRows{
			columns: claimBoundaryIssueColumns(),
			values:  [][]driver.Value{claimBoundaryIssueValues(c.driver.activeID)},
		}, nil
	case strings.Contains(query, "FROM issues") && strings.Contains(query, "LEFT JOIN leases") && strings.Contains(query, "WHERE id = ?"):
		return &claimCommitBoundaryRows{
			columns: claimBoundaryIssueColumns(),
			values:  [][]driver.Value{claimBoundaryIssueValues(c.driver.activeID)},
		}, nil
	case strings.Contains(query, "SELECT label FROM labels"):
		return &claimCommitBoundaryRows{columns: []string{"label"}}, nil
	case strings.Contains(query, "SELECT issue_id, label FROM labels"):
		return &claimCommitBoundaryRows{columns: []string{"issue_id", "label"}}, nil
	case strings.Contains(query, "SELECT name, category FROM custom_statuses"):
		return &claimCommitBoundaryRows{columns: []string{"name", "category"}}, nil
	case strings.Contains(query, "SELECT value FROM config"):
		return &claimCommitBoundaryRows{columns: []string{"value"}}, nil
	case strings.Contains(query, "CALL DOLT_ADD"):
		c.driver.stageCalls++
		if c.driver.stageErr != nil {
			return nil, c.driver.stageErr
		}
		return &claimCommitBoundaryRows{columns: []string{"status"}}, nil
	case strings.Contains(query, "CALL DOLT_COMMIT"):
		c.driver.doltCommits++
		if c.driver.commitErr != nil && c.driver.doltCommits == 1 {
			return nil, c.driver.commitErr
		}
		if c.driver.nothingToCommit {
			return nil, errors.New("nothing to commit")
		}
		return &claimCommitBoundaryRows{columns: []string{"hash"}}, nil
	default:
		return &claimCommitBoundaryRows{columns: []string{"value"}}, nil
	}
}

func (c *claimCommitBoundaryConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	if strings.Contains(query, "UPDATE issues") && strings.Contains(query, "SET assignee") {
		c.driver.claimMutations++
		c.driver.claimedIDs = append(c.driver.claimedIDs, c.driver.activeID)
	}
	if strings.Contains(query, "UPDATE issues") && strings.Contains(query, "`title`") {
		c.driver.updateMutations++
	}
	if strings.Contains(query, "INSERT INTO events") {
		c.driver.eventInserts++
	}
	return driver.RowsAffected(1), nil
}

type claimCommitBoundaryTx struct {
	driver *claimCommitBoundaryDriver
}

func (t *claimCommitBoundaryTx) Commit() error {
	t.driver.mu.Lock()
	defer t.driver.mu.Unlock()
	t.driver.txCommits++
	return t.driver.sqlCommitErr
}

func (t *claimCommitBoundaryTx) Rollback() error {
	t.driver.mu.Lock()
	defer t.driver.mu.Unlock()
	t.driver.txRollbacks++
	return nil
}

type claimCommitBoundaryRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *claimCommitBoundaryRows) Columns() []string { return r.columns }
func (r *claimCommitBoundaryRows) Close() error      { return nil }
func (r *claimCommitBoundaryRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func claimBoundaryIssueColumns() []string {
	parts := strings.Split(strings.ReplaceAll(issueops.IssueSelectColumns, "\n", " "), ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func claimBoundaryIssueValues(id string) []driver.Value {
	values := make([]driver.Value, 0, len(claimBoundaryIssueColumns()))
	for _, column := range claimBoundaryIssueColumns() {
		switch column {
		case "id":
			values = append(values, id)
		case "title":
			values = append(values, "claim boundary")
		case "description", "design", "acceptance_criteria", "notes":
			values = append(values, "")
		case "status":
			values = append(values, string(types.StatusOpen))
		case "priority":
			values = append(values, int64(2))
		case "issue_type":
			values = append(values, string(types.TypeTask))
		case "compaction_level":
			values = append(values, int64(0))
		default:
			values = append(values, nil)
		}
	}
	return values
}

func newClaimCommitBoundaryStore(d *claimCommitBoundaryDriver) *DoltStore {
	return &DoltStore{db: sql.OpenDB(d)}
}

func TestClaimIssueDoltCommitResponseLossIsIndeterminateAndNotReplayed(t *testing.T) {
	driver := &claimCommitBoundaryDriver{commitErr: testConnectionLoss}
	store := newClaimCommitBoundaryStore(driver)
	t.Cleanup(func() { _ = store.db.Close() })

	err := store.ClaimIssue(context.Background(), "claim-boundary", "alice")
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("ClaimIssue() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("ClaimIssue() error = %v, want cause %v", err, testConnectionLoss)
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.claimMutations != 1 {
		t.Fatalf("claim mutations = %d, want 1", driver.claimMutations)
	}
	if driver.doltCommits != 1 {
		t.Fatalf("DOLT_COMMIT calls = %d, want 1", driver.doltCommits)
	}
	if driver.txCommits != 0 || driver.txRollbacks != 1 {
		t.Fatalf("SQL transaction outcomes = commits:%d rollbacks:%d, want commits:0 rollbacks:1", driver.txCommits, driver.txRollbacks)
	}
}

func TestClaimIssueDoltAddFailureCannotReportSuccess(t *testing.T) {
	stageErr := errors.New("stage failed")
	driver := &claimCommitBoundaryDriver{stageErr: stageErr, nothingToCommit: true}
	store := newClaimCommitBoundaryStore(driver)
	t.Cleanup(func() { _ = store.db.Close() })

	err := store.ClaimIssue(context.Background(), "claim-boundary", "alice")
	if !errors.Is(err, stageErr) {
		t.Fatalf("ClaimIssue() error = %v, want stage failure %v", err, stageErr)
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.stageCalls != 1 {
		t.Fatalf("DOLT_ADD calls = %d, want 1", driver.stageCalls)
	}
	if driver.doltCommits != 0 {
		t.Fatalf("DOLT_COMMIT calls = %d, want 0 after staging failure", driver.doltCommits)
	}
	if driver.txCommits != 0 || driver.txRollbacks != 1 {
		t.Fatalf("SQL transaction outcomes = commits:%d rollbacks:%d, want commits:0 rollbacks:1", driver.txCommits, driver.txRollbacks)
	}
}

func TestClaimReadyIssueDoltCommitResponseLossDoesNotDoubleClaim(t *testing.T) {
	driver := &claimCommitBoundaryDriver{
		commitErr: testConnectionLoss,
		readyIDs:  []string{"ready-first", "ready-second"},
	}
	store := newClaimCommitBoundaryStore(driver)
	t.Cleanup(func() { _ = store.db.Close() })

	claimed, err := store.ClaimReadyIssue(context.Background(), types.WorkFilter{}, "alice")
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("ClaimReadyIssue() error = %v, want ErrCommitIndeterminate", err)
	}
	if claimed != nil {
		t.Fatalf("ClaimReadyIssue() claimed = %+v, want nil while commit outcome is indeterminate", claimed)
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()
	if got, want := strings.Join(driver.claimedIDs, ","), "ready-first"; got != want {
		t.Fatalf("claimed IDs = %q, want %q (no replay onto another ready issue)", got, want)
	}
	if driver.txAttempts != 1 {
		t.Fatalf("transaction attempts = %d, want 1", driver.txAttempts)
	}
	if driver.doltCommits != 1 {
		t.Fatalf("DOLT_COMMIT calls = %d, want 1", driver.doltCommits)
	}
}

// TestClaimReadyIssueVerifyFailureDoesNotRecordCircuitSuccess pins the fix for
// the verify-gated circuit-accounting major: when the SQL write commits but the
// post-write verify-by-re-read contradicts the reported success, the breaker
// must NOT be reset. withCircuitWrite records terminal success only after
// verifiedReadyClaim returns nil, and the nested withRetryTx / verify reads
// defer their own success reset to that boundary. Before the fix, withRetryTx
// reset the breaker the instant the SQL commit returned — laundering a phantom
// claim (reported success, failed verification) into breaker-health optimism.
func TestClaimReadyIssueVerifyFailureDoesNotRecordCircuitSuccess(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// The commit succeeds (no commitErr/sqlCommitErr), but the verify re-read
	// returns ("", open) — checkedUpdate stays false — which cannot match
	// claimedBy("alice"), so the claim reports success then fails verification.
	driver := &claimCommitBoundaryDriver{readyIDs: []string{"ready-first"}}
	store := newClaimCommitBoundaryStore(driver)
	store.serverMode = true
	breaker := newTestCircuitBreaker(t)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	// Prime the breaker to one failure short of tripping. If the operation
	// preserves the counter, one further failure trips it; if the write reset it
	// (the bug), the counter is back to zero and one failure leaves it closed.
	for i := 0; i < circuitFailureThreshold-1; i++ {
		breaker.RecordFailure()
	}
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state after priming = %q, want %q", state, circuitClosed)
	}

	claimed, err := store.ClaimReadyIssue(context.Background(), types.WorkFilter{}, "alice")
	if err == nil {
		t.Fatal("ClaimReadyIssue() error = nil, want a verification failure")
	}
	if claimed != nil {
		t.Fatalf("ClaimReadyIssue() claimed = %+v, want nil when verify contradicts the reported success", claimed)
	}

	// The write must have reported success (the SQL tx committed) so the buggy
	// path's premature RecordSuccess would have fired here.
	driver.mu.Lock()
	if driver.claimMutations != 1 || driver.doltCommits != 1 || driver.txCommits != 1 {
		t.Fatalf("write outcome = mutations:%d Dolt commits:%d SQL commits:%d, want 1,1,1 (write must report success)",
			driver.claimMutations, driver.doltCommits, driver.txCommits)
	}
	driver.mu.Unlock()

	// The verify failure must have left the breaker's failure counter intact:
	// one more failure trips it. Had the write recorded success before verify,
	// the counter would be zero and this single failure would leave it closed.
	breaker.RecordFailure()
	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after verify-failed claim + one failure = %q, want %q (RecordSuccess must not fire before verify)", state, circuitOpen)
	}
}

// TestIssueOperationsUpdateClaimVerifyFailureDoesNotRecordCircuitSuccess pins the
// fix for the verify-gated circuit-accounting major on the IssueLifecycle.Update
// facade (the twin of the ClaimReadyIssue regression above). A facade claim whose
// SQL/Dolt write commits but whose post-write verify-by-re-read contradicts the
// reported success must NOT reset the breaker. verifiedUpdate now runs the write
// and its verify under withCircuitWrite, so the nested runIssueOperationTx
// inherits the managed context and defers its success reset to the boundary,
// which records success only after verifiedClaimWrite returns nil. Before the fix,
// withRetryTx reset the breaker the instant the SQL commit returned — laundering a
// phantom facade claim (reported success, failed verification) into breaker-health
// optimism.
func TestIssueOperationsUpdateClaimVerifyFailureDoesNotRecordCircuitSuccess(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// The commit succeeds (no commitErr/sqlCommitErr), but every verify re-read
	// returns ("", open) — checkedUpdate stays false — which cannot match
	// claimedAs("alice", in_progress), so the facade claim reports success then
	// fails verification.
	driver := &claimCommitBoundaryDriver{}
	store := newClaimCommitBoundaryStore(driver)
	store.serverMode = true
	breaker := newTestCircuitBreaker(t)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	// Prime the breaker to one failure short of tripping. If the operation
	// preserves the counter, one further failure trips it; if the write reset it
	// (the bug), the counter is back to zero and one failure leaves it closed.
	for i := 0; i < circuitFailureThreshold-1; i++ {
		breaker.RecordFailure()
	}
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state after priming = %q, want %q", state, circuitClosed)
	}

	operations := &issueOperations{store: store}
	_, err := operations.Update(context.Background(), publicops.UpdateRequest{
		Actor:   "alice",
		IssueID: "claim-boundary",
		Claim:   true,
	})
	if err == nil {
		t.Fatal("facade claim Update error = nil, want a verification failure")
	}
	if !strings.Contains(err.Error(), "did not land") {
		t.Fatalf("facade claim Update error = %v, want a verify 'did not land' failure", err)
	}

	// The write must have reported success (the SQL tx committed and DOLT_COMMIT
	// landed) so the buggy path's premature RecordSuccess would have fired here.
	driver.mu.Lock()
	if driver.claimMutations != 1 || driver.doltCommits != 1 || driver.txCommits != 1 {
		t.Fatalf("write outcome = mutations:%d Dolt commits:%d SQL commits:%d, want 1,1,1 (write must report success)",
			driver.claimMutations, driver.doltCommits, driver.txCommits)
	}
	driver.mu.Unlock()

	// The verify failure must have left the breaker's failure counter intact: one
	// more failure trips it. Had the write recorded success before verify, the
	// counter would be zero and this single failure would leave it closed.
	breaker.RecordFailure()
	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after verify-failed facade claim + one failure = %q, want %q (RecordSuccess must not fire before verify)", state, circuitOpen)
	}
}

// TestIssueOperationsUpdateGuardedVerifyFailureDoesNotRecordCircuitSuccess is the
// coordination-only guarded twin of the facade-claim regression above. A guarded
// status write whose compare-and-set precondition passes and whose write commits,
// but whose post-write verify shows the coordination state did not land, must not
// reset the breaker either. The guarded branch is verifiable for the same reason a
// claim is — its postcondition is provable from an assignee/status re-read — so it
// travels the same withCircuitWrite boundary and must defer success accounting the
// same way.
func TestIssueOperationsUpdateGuardedVerifyFailureDoesNotRecordCircuitSuccess(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// checkedUpdate stays false, so every SELECT assignee,status returns
	// ("", open). The CAS read sees open and matches ExpectedStatus open, so the
	// guard passes and the write runs; the post-write verify sees open again,
	// which cannot match the wanted status in_progress, so the update reports
	// success then fails verification.
	driver := &claimCommitBoundaryDriver{}
	store := newClaimCommitBoundaryStore(driver)
	store.serverMode = true
	breaker := newTestCircuitBreaker(t)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	for i := 0; i < circuitFailureThreshold-1; i++ {
		breaker.RecordFailure()
	}
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state after priming = %q, want %q", state, circuitClosed)
	}

	operations := &issueOperations{store: store}
	expectedStatus := types.StatusOpen
	_, err := operations.Update(context.Background(), publicops.UpdateRequest{
		Actor:          "alice",
		IssueID:        "claim-boundary",
		ExpectedStatus: &expectedStatus,
		Patch: publicops.IssuePatch{
			Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		},
	})
	if err == nil {
		t.Fatal("guarded coordination Update error = nil, want a verification failure")
	}
	if !strings.Contains(err.Error(), "did not land") {
		t.Fatalf("guarded coordination Update error = %v, want a verify 'did not land' failure", err)
	}

	// A coordination-only status write trips neither driver mutation counter (its
	// UPDATE leads with updated_at and back-tick-quotes the status column), so the
	// write-reported-success signal is the committed SQL tx plus the landed
	// DOLT_COMMIT — exactly what would have fired the buggy premature RecordSuccess.
	driver.mu.Lock()
	if driver.doltCommits != 1 || driver.txCommits != 1 {
		t.Fatalf("write outcome = Dolt commits:%d SQL commits:%d, want 1,1 (write must report success)",
			driver.doltCommits, driver.txCommits)
	}
	driver.mu.Unlock()

	breaker.RecordFailure()
	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after verify-failed guarded update + one failure = %q, want %q (RecordSuccess must not fire before verify)", state, circuitOpen)
	}
}

func TestUpdateIssueCheckedMixedCoordinationCommitLossIsNotMasked(t *testing.T) {
	driver := &claimCommitBoundaryDriver{
		commitErr:      testConnectionLoss,
		checkedUpdate:  true,
		verifyAssignee: "alice",
		verifyStatus:   types.StatusInProgress,
	}
	store := newClaimCommitBoundaryStore(driver)
	store.serverMode = true
	t.Cleanup(func() { _ = store.db.Close() })

	expectedAssignee := ""
	expectedStatus := string(types.StatusOpen)
	err := store.UpdateIssueChecked(context.Background(), "claim-boundary", map[string]interface{}{
		"assignee": "alice",
		"status":   string(types.StatusInProgress),
		"title":    "ordinary field must not be masked",
	}, "alice", storage.UpdateIssueOptions{
		ExpectedAssignee: &expectedAssignee,
		ExpectedStatus:   &expectedStatus,
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("UpdateIssueChecked() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("UpdateIssueChecked() error = %v, want cause %v", err, testConnectionLoss)
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.updateMutations != 1 || driver.eventInserts != 1 {
		t.Fatalf("mixed update attempts = updates:%d events:%d, want updates:1 events:1", driver.updateMutations, driver.eventInserts)
	}
	if driver.txAttempts != 1 || driver.doltCommits != 1 || driver.txRollbacks != 1 {
		t.Fatalf("transaction outcomes = attempts:%d Dolt commits:%d rollbacks:%d, want 1, 1, 1", driver.txAttempts, driver.doltCommits, driver.txRollbacks)
	}
}

var _ driver.Connector = (*claimCommitBoundaryDriver)(nil)
var _ driver.ExecerContext = (*claimCommitBoundaryConn)(nil)
var _ driver.QueryerContext = (*claimCommitBoundaryConn)(nil)
