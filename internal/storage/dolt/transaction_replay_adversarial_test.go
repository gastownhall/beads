package dolt

// Adversarial tests for whole-transaction replay, contributed in review: a
// replay must start from the caller's request (inline edges included), a lost
// commit response must never be replayed, and the split regular/ignored
// transactions must roll back and replay as one.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ---------------------------------------------------------------------------
// R1/R2: a replayed store-level batch create whose issue carries an inline
// dependency with no IssueID. Attempt 1 writes the minted ID into the shared
// *types.Dependency (PersistDependencies: dep.IssueID = issue.ID);
// restoring only the Issue struct would let attempt 2 mint a new ID while the
// edge kept attempt 1's.
// ---------------------------------------------------------------------------

func inlineEdgeReplayRace(t *testing.T, counterMode bool) (loser *types.Issue, competitorID string, createErr error, hookCalls int32, base *DoltStore) {
	t.Helper()
	base, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := concurrentTestContext(t)
	t.Cleanup(cancel)
	peer := openPeerStore(t, ctx, base)
	if counterMode {
		if err := base.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("set counter mode: %v", err)
		}
	} else {
		// Hash mode: make any two overlapping writers collide (the events-journal shape).
		base.SetEventsJournalEnabled(true)
		peer.SetEventsJournalEnabled(true)
	}
	anchor := &types.Issue{ID: "test-anchor", Title: "anchor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := base.CreateIssue(ctx, anchor, "seed"); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}

	prepared, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	opts := storage.BatchCreateOptions{
		// Invoked INSIDE the create transaction when the second edge's target
		// is missing: hold attempt 1 open until the competitor has committed.
		OnSkippedDependency: func(_, _, _ string) {
			if calls.Add(1) == 1 {
				close(prepared)
				<-release
			}
		},
	}
	loser = &types.Issue{
		Title: "loser", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{DependsOnID: "test-anchor", Type: types.DepBlocks},
			{DependsOnID: "test-nosuch", Type: types.DepRelated},
		},
	}
	done := make(chan error, 1)
	go func() { done <- base.CreateIssuesWithFullOptions(ctx, []*types.Issue{loser}, "loser", opts) }()
	waitForTransactionPrepared(t, ctx, prepared, "loser")

	competitor := &types.Issue{Title: "competitor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := peer.CreateIssue(ctx, competitor, "competitor"); err != nil {
		close(release)
		t.Fatalf("competitor create: %v", err)
	}
	close(release)
	createErr = <-done
	return loser, competitor.ID, createErr, calls.Load(), base
}

func edgesInto(t *testing.T, ctx context.Context, s *DoltStore, target string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, "SELECT issue_id FROM dependencies WHERE depends_on_issue_id = ?", target)
	if err != nil {
		t.Fatalf("query edges: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestReplayedCounterModeBatchCreateKeepsInlineEdgeOnItsOwnIssue(t *testing.T) {
	loser, competitorID, err, calls, base := inlineEdgeReplayRace(t, true)
	if err != nil {
		t.Fatalf("loser create failed: %v", err)
	}
	if calls < 2 {
		t.Fatalf("no replay happened (hook calls=%d): the competitor's commit did not conflict", calls)
	}
	ctx := context.Background()
	got := edgesInto(t, ctx, base, "test-anchor")
	t.Logf("loser id=%s competitor id=%s loser.Dependencies[0].IssueID=%s edges into anchor from=%v",
		loser.ID, competitorID, loser.Dependencies[0].IssueID, got)
	if len(got) != 1 || got[0] != loser.ID {
		t.Fatalf("edge into test-anchor is from %v, want exactly [%s] (the loser's own issue); competitor is %s", got, loser.ID, competitorID)
	}
}

func TestReplayedHashModeBatchCreateKeepsInlineEdgeOnItsOwnIssue(t *testing.T) {
	loser, competitorID, err, calls, base := inlineEdgeReplayRace(t, false)
	if calls < 2 && err == nil {
		t.Fatalf("no replay happened (hook calls=%d): the competitor's commit did not conflict", calls)
	}
	if err != nil {
		t.Fatalf("loser create failed after replay (hook calls=%d, dep IssueID=%s, issue ID=%s): %v",
			calls, loser.Dependencies[0].IssueID, loser.ID, err)
	}
	got := edgesInto(t, context.Background(), base, "test-anchor")
	if len(got) != 1 || got[0] != loser.ID {
		t.Fatalf("edge into test-anchor is from %v, want exactly [%s]; competitor %s", got, loser.ID, competitorID)
	}
}

// ---------------------------------------------------------------------------
// R3: a COMMIT whose response is lost (connection dropped after the server
// processed it) must surface as indeterminate and never be replayed.
// ---------------------------------------------------------------------------

type commitDropProxy struct {
	ln      net.Listener
	target  string
	armed   atomic.Bool
	tripped atomic.Int32
	// match selects the statement whose response is dropped; nil = COMMIT.
	match func(sql string) bool
}

func startCommitDropProxy(t *testing.T, target string) *commitDropProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &commitDropProxy{ln: ln, target: target}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *commitDropProxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *commitDropProxy) handle(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = server.Close() }) }
	var swallow atomic.Bool
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if swallow.Load() {
					// The server answered the COMMIT: drop the answer and cut
					// the client off, exactly a lost commit response.
					closeBoth()
					return
				}
				if _, werr := client.Write(buf[:n]); werr != nil {
					closeBoth()
					return
				}
			}
			if err != nil {
				closeBoth()
				return
			}
		}
	}()
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(client, hdr); err != nil {
			closeBoth()
			return
		}
		n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
		payload := make([]byte, n)
		if _, err := io.ReadFull(client, payload); err != nil {
			closeBoth()
			return
		}
		isTarget := false
		if hdr[3] == 0 && n > 1 && payload[0] == 0x03 {
			q := strings.TrimSpace(string(payload[1:]))
			if p.match != nil {
				isTarget = p.match(q)
			} else {
				isTarget = strings.EqualFold(q, "COMMIT")
			}
		}
		if isTarget && p.armed.CompareAndSwap(true, false) {
			swallow.Store(true)
			p.tripped.Add(1)
		}
		if _, err := server.Write(append(append([]byte{}, hdr...), payload...)); err != nil {
			closeBoth()
			return
		}
	}
}

func countByTitle(t *testing.T, ctx context.Context, s *DoltStore, table, title string) int {
	t.Helper()
	var n int
	//nolint:gosec // test-chosen table literal
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE title = ?", title).Scan(&n); err != nil {
		t.Fatalf("count %s %q: %v", table, title, err)
	}
	return n
}

func TestLostCommitResponseIsNeverReplayed(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	proxy := startCommitDropProxy(t, fmt.Sprintf("127.0.0.1:%d", testServerPort))
	via, err := New(ctx, &Config{
		Path: t.TempDir(), CommitterName: "test", CommitterEmail: "test@example.com",
		ServerHost: "127.0.0.1", ServerPort: proxy.port(), Database: base.database, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("open store through proxy: %v", err)
	}
	t.Cleanup(func() { _ = via.Close() })

	for _, journal := range []bool{false, true} {
		via.SetEventsJournalEnabled(journal)

		t.Run(fmt.Sprintf("RunReplayableTransaction/journal=%v", journal), func(t *testing.T) {
			title := fmt.Sprintf("replayable-lost-commit-%v", journal)
			var calls atomic.Int32
			proxy.armed.Store(true)
			err := storage.RunReplayableTransaction(ctx, via, "bd: lost commit", func(tx storage.Transaction) error {
				calls.Add(1)
				return tx.CreateIssue(ctx, &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester")
			})
			if proxy.tripped.Load() == 0 {
				t.Fatalf("proxy never saw the COMMIT")
			}
			proxy.tripped.Store(0)
			if err == nil {
				t.Fatalf("lost commit response reported success")
			}
			if !errors.Is(err, ErrCommitIndeterminate) || errors.Is(err, storage.ErrTransactionConflict) {
				t.Fatalf("err = %v; want indeterminate and not a replayable conflict", err)
			}
			if c := calls.Load(); c != 1 {
				t.Fatalf("callback ran %d times; a lost commit response must never be replayed", c)
			}
			if n := countByTitle(t, ctx, base, "issues", title); n != 1 {
				t.Fatalf("rows = %d, want exactly 1 (the server committed it once)", n)
			}
		})

		t.Run(fmt.Sprintf("withRetryTx-CreateIssue/journal=%v", journal), func(t *testing.T) {
			title := fmt.Sprintf("retrytx-lost-commit-%v", journal)
			proxy.armed.Store(true)
			err := via.CreateIssue(ctx, &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester")
			if proxy.tripped.Load() == 0 {
				t.Fatalf("proxy never saw the COMMIT")
			}
			proxy.tripped.Store(0)
			if err == nil || !errors.Is(err, ErrCommitIndeterminate) {
				t.Fatalf("err = %v; want indeterminate", err)
			}
			if n := countByTitle(t, ctx, base, "issues", title); n != 1 {
				t.Fatalf("rows = %d, want exactly 1", n)
			}
		})

		t.Run(fmt.Sprintf("withRetryTx-wisp-update/journal=%v", journal), func(t *testing.T) {
			id := fmt.Sprintf("test-wisp-lost-%v", journal)
			if err := base.CreateIssue(ctx, &types.Issue{ID: id, Title: "w", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "seed"); err != nil {
				t.Fatalf("seed wisp: %v", err)
			}
			proxy.armed.Store(true)
			err := via.ClaimIssue(ctx, id, "claimer")
			if proxy.tripped.Load() == 0 {
				t.Fatalf("proxy never saw the COMMIT")
			}
			proxy.tripped.Store(0)
			if err == nil || !errors.Is(err, ErrCommitIndeterminate) {
				t.Fatalf("claim err = %v; want indeterminate (no replay)", err)
			}
			got, gerr := base.GetIssue(ctx, id)
			if gerr != nil {
				t.Fatalf("read wisp: %v", gerr)
			}
			if got.Assignee != "claimer" || got.Status != types.StatusInProgress {
				t.Fatalf("wisp = %s/%s; the server committed the claim once", got.Assignee, got.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R4/R5: journal OFF, the split regular + ignored (wisp) transactions of #207.
// ---------------------------------------------------------------------------

// R4: the REGULAR commit loses. Both planes must roll back, and the replay must
// write each plane exactly once (no wisp from the lost attempt survives).
func TestSplitTxRegularConflictRollsBackBothPlanesAndReplaysOnce(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	peer := openPeerStore(t, ctx, base)
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-shared", Title: "orig", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	prepared := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	done := [2]chan error{make(chan error, 1), make(chan error, 1)}
	var loserCalls atomic.Int32
	go func() {
		done[0] <- peer.RunInTransaction(ctx, "winner", func(tx storage.Transaction) error {
			if err := tx.UpdateIssue(ctx, "test-shared", map[string]interface{}{"title": "winner"}, "w"); err != nil {
				return err
			}
			close(prepared[0])
			return waitForTransactionRelease(ctx, release[0])
		})
	}()
	go func() {
		done[1] <- storage.RunReplayableTransaction(ctx, base, "loser", func(tx storage.Transaction) error {
			attempt := loserCalls.Add(1)
			if err := tx.UpdateIssue(ctx, "test-shared", map[string]interface{}{"title": "loser"}, "l"); err != nil {
				return err
			}
			if err := tx.CreateIssue(ctx, &types.Issue{Title: "split-wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "l"); err != nil {
				return err
			}
			if attempt == 1 {
				close(prepared[1])
				return waitForTransactionRelease(ctx, release[1])
			}
			return nil
		})
	}()
	waitForTransactionPrepared(t, ctx, prepared[0], "winner")
	waitForTransactionPrepared(t, ctx, prepared[1], "loser")
	close(release[0])
	if err := <-done[0]; err != nil {
		t.Fatalf("winner: %v", err)
	}
	close(release[1])
	if err := <-done[1]; err != nil {
		t.Fatalf("loser: %v", err)
	}
	if c := loserCalls.Load(); c != 2 {
		t.Fatalf("loser attempts = %d, want 2", c)
	}
	if n := countByTitle(t, ctx, base, "wisps", "split-wisp"); n != 1 {
		t.Fatalf("wisp rows = %d, want 1: the lost attempt's ignored-plane write leaked", n)
	}
	got, err := base.GetIssue(ctx, "test-shared")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Title != "loser" {
		t.Fatalf("title = %q, want the replayed loser's", got.Title)
	}
}

// R5: the IGNORED commit loses after the regular commit landed. That outcome
// is partial and must be indeterminate, never a replayable conflict.
func TestSplitTxIgnoredConflictAfterRegularCommitIsNotReplayed(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	peer := openPeerStore(t, ctx, base)
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-wisp-shared", Title: "orig", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	prepared := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	done := [2]chan error{make(chan error, 1), make(chan error, 1)}
	var loserCalls atomic.Int32
	go func() {
		done[0] <- peer.RunInTransaction(ctx, "winner", func(tx storage.Transaction) error {
			if err := tx.UpdateIssue(ctx, "test-wisp-shared", map[string]interface{}{"title": "winner"}, "w"); err != nil {
				return err
			}
			close(prepared[0])
			return waitForTransactionRelease(ctx, release[0])
		})
	}()
	go func() {
		done[1] <- storage.RunReplayableTransaction(ctx, base, "loser", func(tx storage.Transaction) error {
			attempt := loserCalls.Add(1)
			if err := tx.CreateIssue(ctx, &types.Issue{Title: "split-durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "l"); err != nil {
				return err
			}
			if err := tx.UpdateIssue(ctx, "test-wisp-shared", map[string]interface{}{"title": "loser"}, "l"); err != nil {
				return err
			}
			if attempt == 1 {
				close(prepared[1])
				return waitForTransactionRelease(ctx, release[1])
			}
			return nil
		})
	}()
	waitForTransactionPrepared(t, ctx, prepared[0], "winner")
	waitForTransactionPrepared(t, ctx, prepared[1], "loser")
	close(release[0])
	if err := <-done[0]; err != nil {
		t.Fatalf("winner: %v", err)
	}
	close(release[1])
	lerr := <-done[1]
	t.Logf("loser err: %v", lerr)
	if lerr == nil {
		t.Fatalf("loser succeeded; the ignored-plane overlap did not conflict")
	}
	if errors.Is(lerr, storage.ErrTransactionConflict) || !errors.Is(lerr, ErrCommitIndeterminate) {
		t.Fatalf("err = %v; want indeterminate, not a replayable conflict", lerr)
	}
	if c := loserCalls.Load(); c != 1 {
		t.Fatalf("loser attempts = %d, want 1 (no replay after the regular commit landed)", c)
	}
	if n := countByTitle(t, ctx, base, "issues", "split-durable"); n != 1 {
		t.Fatalf("durable rows = %d, want 1", n)
	}
}

// R6: the Deleter's commit point is the CALL DOLT_COMMIT inside its
// transaction (Dolt commits the SQL transaction there). A lost response to it
// must be indeterminate and never replayed; withRetryTx treats a connection
// error raised inside the body as "pre-commit" and replays it.
func TestDeleterLostDoltCommitResponseIsNotReplayed(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	proxy := startCommitDropProxy(t, fmt.Sprintf("127.0.0.1:%d", testServerPort))
	proxy.match = func(q string) bool { return strings.HasPrefix(strings.ToUpper(q), "CALL DOLT_COMMIT") }
	via, err := New(ctx, &Config{
		Path: t.TempDir(), CommitterName: "test", CommitterEmail: "test@example.com",
		ServerHost: "127.0.0.1", ServerPort: proxy.port(), Database: base.database, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("open store through proxy: %v", err)
	}
	t.Cleanup(func() { _ = via.Close() })
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-doomed", Title: "doomed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deleter, err := via.Deleter()
	if err != nil {
		t.Fatal(err)
	}
	proxy.armed.Store(true)
	_, derr := deleter.Delete(ctx, issueopsDeleteRequest("test-doomed"))
	t.Logf("delete err: %v (tripped=%d)", derr, proxy.tripped.Load())
	if proxy.tripped.Load() == 0 {
		t.Fatalf("proxy never saw CALL DOLT_COMMIT")
	}
	if _, gerr := base.GetIssue(ctx, "test-doomed"); gerr == nil {
		t.Fatalf("the delete did not land; the test did not exercise a lost response")
	}
	if derr == nil {
		t.Fatalf("a lost DOLT_COMMIT response reported success")
	}
	if !errors.Is(derr, ErrCommitIndeterminate) {
		t.Fatalf("delete err = %v; want ErrCommitIndeterminate (the body was replayed or misreported after its commit point landed)", derr)
	}
}

// R6b: the sweeper twin of R6. The sweep's CALL DOLT_COMMIT commits its SQL
// transaction, so a lost response there may mean the sweep landed: it must be
// indeterminate, never replayed by withRetryTx as a pre-commit failure.
func TestSweeperLostDoltCommitResponseIsNotReplayed(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()
	proxy := startCommitDropProxy(t, fmt.Sprintf("127.0.0.1:%d", testServerPort))
	proxy.match = func(q string) bool { return strings.HasPrefix(strings.ToUpper(q), "CALL DOLT_COMMIT") }
	via, err := New(ctx, &Config{
		Path: t.TempDir(), CommitterName: "test", CommitterEmail: "test@example.com",
		ServerHost: "127.0.0.1", ServerPort: proxy.port(), Database: base.database, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("open store through proxy: %v", err)
	}
	t.Cleanup(func() { _ = via.Close() })
	if err := base.CreateIssue(ctx, &types.Issue{ID: "test-swept", Title: "swept", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := base.CloseIssue(ctx, "test-swept", "done", "seed", ""); err != nil {
		t.Fatalf("close seed: %v", err)
	}
	sweeper, err := via.Sweeper()
	if err != nil {
		t.Fatal(err)
	}
	proxy.armed.Store(true)
	_, serr := sweeper.Sweep(ctx, publicops.SweepRequest{Actor: "sweeper", Tier: publicops.SweepDurable, IDPattern: "test-swept"})
	t.Logf("sweep err: %v (tripped=%d)", serr, proxy.tripped.Load())
	if proxy.tripped.Load() == 0 {
		t.Fatalf("proxy never saw CALL DOLT_COMMIT")
	}
	if _, gerr := base.GetIssue(ctx, "test-swept"); gerr == nil {
		t.Fatalf("the sweep did not land; the test did not exercise a lost response")
	}
	if serr == nil {
		t.Fatalf("a lost DOLT_COMMIT response reported success")
	}
	if !errors.Is(serr, ErrCommitIndeterminate) {
		t.Fatalf("sweep err = %v; want ErrCommitIndeterminate (the body was replayed or misreported after its commit point landed)", serr)
	}
}

func issueopsDeleteRequest(id string) publicops.DeleteRequest {
	return publicops.DeleteRequest{Actor: "deleter", IDs: []string{id}, Force: true}
}
