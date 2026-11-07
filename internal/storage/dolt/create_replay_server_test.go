package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/types"
)

// commitGateProxy forwards MySQL traffic to a server and, while armed, holds
// the first COMMIT a client sends until the test releases it. Holding a
// transaction's COMMIT while a competitor commits is a deterministic way to
// make it lose the commit-time merge, so a retried body replays exactly once —
// with no test hook in production code.
type commitGateProxy struct {
	ln      net.Listener
	target  string
	armed   atomic.Bool
	held    chan struct{}
	release chan struct{}
	// commits counts every COMMIT forwarded, so a test can tell the held
	// attempt was really replayed.
	commits atomic.Int32
}

func startCommitGateProxy(t *testing.T, target string) *commitGateProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &commitGateProxy{ln: ln, target: target, held: make(chan struct{}), release: make(chan struct{})}
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

func (p *commitGateProxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *commitGateProxy) handle(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = server.Close() }) }
	go func() {
		_, _ = io.Copy(client, server)
		closeBoth()
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
		isCommit := hdr[3] == 0 && n > 1 && payload[0] == 0x03 &&
			strings.EqualFold(strings.TrimSpace(string(payload[1:])), "COMMIT")
		if isCommit {
			p.commits.Add(1)
		}
		if isCommit && p.armed.CompareAndSwap(true, false) {
			close(p.held)
			<-p.release
		}
		if _, err := server.Write(append(append([]byte{}, hdr...), payload...)); err != nil {
			closeBoth()
			return
		}
	}
}

// singleCreateReplay creates one issue carrying an inline comment through a
// store whose first COMMIT is held until a competitor on another connection
// has created an issue too, so the create loses the commit-time merge and
// withRetryTx replays it.
func singleCreateReplay(t *testing.T, counterMode bool) (base *DoltStore, loser, competitor *types.Issue) {
	t.Helper()
	base, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := concurrentTestContext(t)
	t.Cleanup(cancel)
	if counterMode {
		// Both writers mint the next counter value: the same issue id.
		if err := base.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("set counter mode: %v", err)
		}
	} else {
		// Hash mode mints distinct ids; the journal counter makes any two
		// overlapping writers collide.
		base.SetEventsJournalEnabled(true)
	}
	proxy := startCommitGateProxy(t, fmt.Sprintf("127.0.0.1:%d", testServerPort))
	via, err := New(ctx, &Config{
		Path: t.TempDir(), CommitterName: "test", CommitterEmail: "test@example.com",
		ServerHost: "127.0.0.1", ServerPort: proxy.port(), Database: base.database, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("open store through proxy: %v", err)
	}
	t.Cleanup(func() { _ = via.Close() })
	via.SetEventsJournalEnabled(!counterMode)

	loser = &types.Issue{
		Title: "loser", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Comments: []*types.Comment{{Author: "loser", Text: "filed with the issue"}},
	}
	proxy.commits.Store(0) // count only the loser's own COMMITs
	proxy.armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- via.CreateIssue(ctx, loser, "loser") }()
	select {
	case <-proxy.held:
	case err := <-done:
		t.Fatalf("loser finished before its COMMIT was held: %v", err)
	case <-ctx.Done():
		t.Fatalf("loser never reached COMMIT: %v", ctx.Err())
	}
	competitor = &types.Issue{Title: "competitor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := base.CreateIssue(ctx, competitor, "competitor"); err != nil {
		close(proxy.release)
		t.Fatalf("competitor create: %v", err)
	}
	close(proxy.release)
	if err := <-done; err != nil {
		t.Fatalf("loser create was not replayed to success: %v", err)
	}
	if n := proxy.commits.Load(); n < 2 {
		t.Fatalf("loser sent %d COMMIT(s), want at least 2: the held attempt did not lose, so nothing was replayed", n)
	}
	return base, loser, competitor
}

// assertCommentDerivedFromOwnIssue checks the loser's comment row carries the
// id derived from its OWN issue — not one left over from the rolled-back
// attempt, which derived it from that attempt's id.
func assertCommentDerivedFromOwnIssue(t *testing.T, db *sql.DB, issueID string) {
	t.Helper()
	var id, author, text, createdAt string
	if err := db.QueryRow(
		"SELECT id, author, text, CAST(created_at AS CHAR) FROM comments WHERE issue_id = ?", issueID,
	).Scan(&id, &author, &text, &createdAt); err != nil {
		t.Fatalf("read the loser's comment: %v", err)
	}
	want := rowid.New("comments", 0, rowid.Digest([]sql.NullString{
		{String: issueID, Valid: true}, {String: author, Valid: true},
		{String: text, Valid: true}, {String: createdAt, Valid: true},
	}))
	if id != want {
		t.Fatalf("comment on %s has id %s, want %s (derived from its own issue): the replay re-used the rolled-back attempt's comment id", issueID, id, want)
	}
}

func TestReplayedCounterModeCreateRestartsFromTheRequest(t *testing.T) {
	base, loser, competitor := singleCreateReplay(t, true)
	if loser.ID == competitor.ID {
		t.Fatalf("loser and competitor both hold %s", loser.ID)
	}
	ctx := context.Background()
	got, err := base.GetIssue(ctx, competitor.ID)
	if err != nil || got.Title != "competitor" {
		t.Fatalf("competitor %s = %+v, %v; the replay overwrote it", competitor.ID, got, err)
	}
	assertCommentDerivedFromOwnIssue(t, base.db, loser.ID)
}

func TestReplayedHashModeCreateRestartsFromTheRequest(t *testing.T) {
	base, loser, _ := singleCreateReplay(t, false)
	assertCommentDerivedFromOwnIssue(t, base.db, loser.ID)
}
