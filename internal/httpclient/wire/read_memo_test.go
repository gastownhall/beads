package wire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// memoServer answers getIssue for bd-1 (a fresh title per request, so a hit is
// told apart from a re-read), a 404 for anything else, and 200 to any write.
func memoServer(t *testing.T) (*Client, *recorder) {
	t.Helper()
	served := 0
	return newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path != "/v0/beads/issues/bd-1" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Not Found","status":404,"code":"not_found","detail":"no such issue"}`))
			return
		}
		served++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "bd-1", "title": "read " + string(rune('0'+served))})
	})
}

func memoGetIssue(t *testing.T, c *Client, id string, q url.Values) (string, error) {
	t.Helper()
	path, err := IssuePath(id)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Title string `json:"title"`
	}
	err = c.Do(context.Background(), Request{Op: OpGetIssue, Method: http.MethodGet, Path: path, Query: q, IssueID: id}, &out)
	return out.Title, err
}

// TestReadMemoCollapsesRepeatedRowReads is bts-xpw.3.14.6's per-command memo:
// once enabled, an identical getIssue is answered from the response already
// received, a different query is a different read, and every answer is decoded
// afresh (the second caller gets its own value, equal to the first).
func TestReadMemoCollapsesRepeatedRowReads(t *testing.T) {
	c, rec := memoServer(t)
	c.EnableReadMemo()

	first, err := memoGetIssue(t, c, "bd-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := memoGetIssue(t, c, "bd-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 || first != second {
		t.Fatalf("two identical reads made %d requests and answered %q then %q; want one request, one answer", rec.count(), first, second)
	}
	withComments := url.Values{"include_comments": {"true"}}
	if _, err := memoGetIssue(t, c, "bd-1", withComments); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 2 {
		t.Fatalf("a read with a different query was answered from the memo (%d requests)", rec.count())
	}
}

// TestReadMemoIsDroppedByAnyWrite: a write through the client may change what a
// memoized read said, so the next read goes back to the server — whatever the
// write's method, since the wire spells writes as all four.
func TestReadMemoIsDroppedByAnyWrite(t *testing.T) {
	issuePath, _ := IssuePath("bd-1")
	settingPath, _ := SettingPath("k")
	for _, write := range []Request{
		{Op: OpUpdateIssue, Method: http.MethodPatch, Path: issuePath, Body: map[string]any{}},
		{Op: OpCreateIssue, Method: http.MethodPost, Path: PathIssues, Body: map[string]any{}},
		{Op: OpSetSetting, Method: http.MethodPut, Path: settingPath, Body: map[string]any{}},
		{Op: OpUnsetSetting, Method: http.MethodDelete, Path: settingPath},
	} {
		t.Run(write.Method, func(t *testing.T) {
			c, rec := memoServer(t)
			c.EnableReadMemo()

			before, _ := memoGetIssue(t, c, "bd-1", nil)
			if err := c.Do(context.Background(), write, nil); err != nil {
				t.Fatal(err)
			}
			after, _ := memoGetIssue(t, c, "bd-1", nil)
			if rec.count() != 3 || before == after {
				t.Fatalf("read after a %s: %d requests, %q then %q; want the row re-read", write.Method, rec.count(), before, after)
			}
		})
	}
}

// TestReadMemoKeepsOnlySuccessesAndExpires: a miss is re-asked (it may have been
// created since), and an entry older than ReadMemoTTL is re-read.
func TestReadMemoKeepsOnlySuccessesAndExpires(t *testing.T) {
	c, rec := memoServer(t)
	c.EnableReadMemo()
	now := time.Unix(1_000_000, 0)
	c.memo.now = func() time.Time { return now }

	for range 2 {
		if _, err := memoGetIssue(t, c, "bd-404", nil); err == nil {
			t.Fatal("a missing row read without an error")
		}
	}
	if rec.count() != 2 {
		t.Fatalf("a 404 was memoized: %d requests for two reads", rec.count())
	}
	_, _ = memoGetIssue(t, c, "bd-1", nil)
	now = now.Add(ReadMemoTTL + time.Second)
	_, _ = memoGetIssue(t, c, "bd-1", nil)
	if rec.count() != 4 {
		t.Fatalf("an expired entry answered: %d requests, want 4", rec.count())
	}
}

// TestReadMemoIsOffByDefault: a client nobody opted in re-reads every time —
// the memo is for one command's transport, never a long-lived one's.
func TestReadMemoIsOffByDefault(t *testing.T) {
	c, rec := memoServer(t)
	_, _ = memoGetIssue(t, c, "bd-1", nil)
	_, _ = memoGetIssue(t, c, "bd-1", nil)
	if rec.count() != 2 || c.ReadMemoEnabled() {
		t.Fatalf("an un-enabled client memoized (%d requests)", rec.count())
	}
}

// TestReadMemoAgesFromTheServersAnswerNotTheLastHit: an entry's age is the age
// of the answer the server gave, and a hit does not renew it. A row read more
// often than ReadMemoTTL (bd show --watch polls every 2 s) is still re-read
// once its answer is a TTL old; were each hit to re-stamp the entry, it would
// answer from the first read for as long as the polling kept up.
func TestReadMemoAgesFromTheServersAnswerNotTheLastHit(t *testing.T) {
	c, rec := memoServer(t)
	c.EnableReadMemo()
	now := time.Unix(1_000_000, 0)
	c.memo.now = func() time.Time { return now }

	var titles []string
	for range 4 { // t = 0 s (miss), 2 s (hit), 4 s (hit), 6 s (a TTL old: re-read)
		title, err := memoGetIssue(t, c, "bd-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
		now = now.Add(2 * time.Second)
	}
	if rec.count() != 2 || titles[3] == titles[0] {
		t.Fatalf("a row polled every 2 s made %d requests and answered %q; want it re-read at 6 s (2 requests)", rec.count(), titles)
	}
}

// TestReadMemoDropsAnAnswerThatOverlappedAWrite: a read whose request was in
// flight while a write went through the client may carry the pre-write row,
// even though it returned after the write did. Storing it would answer every
// later read with that row for a whole TTL — the command's own write unseen.
func TestReadMemoDropsAnAnswerThatOverlappedAWrite(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	served := 0
	c, rec := newTestClient(t, Options{ResponseHeaderTimeout: -1}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		served++
		n := served
		mu.Unlock()
		if n == 1 {
			close(arrived)
			<-release
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "bd-1", "title": fmt.Sprintf("read %d", n)})
	})
	c.EnableReadMemo()
	path, err := IssuePath("bd-1")
	if err != nil {
		t.Fatal(err)
	}

	type answer struct {
		title string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		var out struct {
			Title string `json:"title"`
		}
		err := c.Do(context.Background(), Request{Op: OpGetIssue, Method: http.MethodGet, Path: path, IssueID: "bd-1"}, &out)
		done <- answer{out.Title, err}
	}()
	<-arrived // the read is on the wire; the write starts and finishes while it waits
	if err := c.Do(context.Background(), Request{Op: OpUpdateIssue, Method: http.MethodPatch, Path: path, Body: map[string]any{}}, nil); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	overlapped := <-done
	if overlapped.err != nil {
		t.Fatal(overlapped.err)
	}

	after, err := memoGetIssue(t, c, "bd-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 3 || after == overlapped.title {
		t.Fatalf("the read after the write answered %q (the overlapped read's %q) in %d requests; want it re-read (3 requests)", after, overlapped.title, rec.count())
	}
}

// TestReadMemoHoldsAtMostOneTTLOfAnswers: expired entries are swept when a new
// answer is stored, so a client that lives on (and keeps reading new rows)
// does not keep every answer it ever received.
func TestReadMemoHoldsAtMostOneTTLOfAnswers(t *testing.T) {
	c, _ := memoServer(t)
	c.EnableReadMemo()
	now := time.Unix(1_000_000, 0)
	c.memo.now = func() time.Time { return now }

	if _, err := memoGetIssue(t, c, "bd-1", nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(ReadMemoTTL + time.Second)
	if _, err := memoGetIssue(t, c, "bd-1", url.Values{"include_comments": {"true"}}); err != nil {
		t.Fatal(err)
	}
	c.memo.mu.Lock()
	held := len(c.memo.entries)
	c.memo.mu.Unlock()
	if held != 1 {
		t.Fatalf("the memo holds %d answers after one expired and one was stored; want 1", held)
	}
}

// TestReadMemoNeverKeepsA5xx: a 5xx is not an answer to remember. A 503 above
// all — dispatch comes back to a busy read through Do again (dispatch.go's
// retry loop), and a memoized 503 would answer every retry with the same
// refusal. The 2xx that follows it is the read's answer, and that one is kept.
func TestReadMemoNeverKeepsA5xx(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var mu sync.Mutex
			served := 0
			c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				served++
				n := served
				mu.Unlock()
				if n == 1 {
					problemJSON(w, status, fmt.Sprintf(`{"type":"about:blank","title":"failing","status":%d,"detail":"try again"}`, status))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "bd-1", "title": fmt.Sprintf("read %d", n)})
			})
			c.EnableReadMemo()

			_, err := memoGetIssue(t, c, "bd-1", nil)
			var prob *ProblemError
			if !errors.As(err, &prob) || prob.Status != status {
				t.Fatalf("first read: %v, want the server's %d", err, status)
			}
			again, err := memoGetIssue(t, c, "bd-1", nil)
			if err != nil || rec.count() != 2 {
				t.Fatalf("the read after a %d answered (%q, %v) in %d requests; want the server asked again (2 requests)", status, again, err, rec.count())
			}
			third, err := memoGetIssue(t, c, "bd-1", nil)
			if err != nil || third != again || rec.count() != 2 {
				t.Fatalf("the 2xx after a %d was not memoized: %q then %q in %d requests", status, again, third, rec.count())
			}
		})
	}
}

// TestReadMemoKeepsOnlyAnswersThatDecoded: a 2xx is memoized only once it has
// decoded. An empty or undecodable body is the server misbehaving once, and
// keeping it would fail every identical read for a whole TTL rather than ask
// again. A read that decoded nothing (out == nil) vouches for nothing either.
func TestReadMemoKeepsOnlyAnswersThatDecoded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		noOut bool
	}{
		{name: "empty body", body: ""},
		{name: "undecodable body", body: `{"id":"bd-1","title":`},
		{name: "answer nobody decoded", body: `{"id":"bd-1","title":"undecoded"}`, noOut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			served := 0
			c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				served++
				n := served
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n == 1 {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "bd-1", "title": fmt.Sprintf("read %d", n)})
			})
			c.EnableReadMemo()
			path, err := IssuePath("bd-1")
			if err != nil {
				t.Fatal(err)
			}

			if tc.noOut {
				if err := c.Do(context.Background(), Request{Op: OpGetIssue, Method: http.MethodGet, Path: path, IssueID: "bd-1"}, nil); err != nil {
					t.Fatal(err)
				}
			} else if _, err := memoGetIssue(t, c, "bd-1", nil); err == nil {
				t.Fatalf("a 200 with body %q decoded without an error", tc.body)
			}
			title, err := memoGetIssue(t, c, "bd-1", nil)
			if err != nil || title != "read 2" || rec.count() != 2 {
				t.Fatalf("the read after a 200 with body %q answered (%q, %v) in %d requests; want the server asked again (\"read 2\", 2 requests)", tc.body, title, err, rec.count())
			}
		})
	}
}

// TestReadMemoIsPerClient: each client holds its own memo. In either order, a
// read through one client is never answered from what another client read,
// and turning the memo on for one client turns it on for no other.
func TestReadMemoIsPerClient(t *testing.T) {
	for _, first := range []int{0, 1} {
		t.Run(fmt.Sprintf("client %d reads first", first), func(t *testing.T) {
			var mu sync.Mutex
			served := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				served++
				n := served
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "bd-1", "title": fmt.Sprintf("read %d", n)})
			}))
			t.Cleanup(srv.Close)
			base, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			var clients [3]*Client
			for i := range clients {
				if clients[i], err = New(base, nil, Options{HTTPClient: srv.Client()}); err != nil {
					t.Fatal(err)
				}
			}
			clients[0].EnableReadMemo()
			clients[1].EnableReadMemo()
			// clients[2] never opts in.
			requests := func() int {
				mu.Lock()
				defer mu.Unlock()
				return served
			}
			a, b := clients[first], clients[1-first]

			a1, _ := memoGetIssue(t, a, "bd-1", nil)
			b1, _ := memoGetIssue(t, b, "bd-1", nil)
			if requests() != 2 || a1 == b1 {
				t.Fatalf("two clients' first reads made %d requests and answered %q and %q; want each to ask the server", requests(), a1, b1)
			}
			a2, _ := memoGetIssue(t, a, "bd-1", nil)
			b2, _ := memoGetIssue(t, b, "bd-1", nil)
			if requests() != 2 || a2 != a1 || b2 != b1 {
				t.Fatalf("each client's repeat read: %d requests, %q/%q then %q/%q; want each answered from its own memo", requests(), a1, b1, a2, b2)
			}
			for range 2 {
				_, _ = memoGetIssue(t, clients[2], "bd-1", nil)
			}
			if requests() != 4 || clients[2].ReadMemoEnabled() {
				t.Fatalf("a client that never opted in memoized (%d requests, enabled=%v)", requests(), clients[2].ReadMemoEnabled())
			}
		})
	}
}

// TestReadMemoDropsAnAnswerReadWhileAWriteWasInFlight: a read that starts and
// finishes while a write is on the wire (after the write's own pre-send clear)
// may carry the pre-write row. The write's clear on the way back drops it, so
// a read that starts once the write has returned asks the server again.
func TestReadMemoDropsAnAnswerReadWhileAWriteWasInFlight(t *testing.T) {
	writeArrived, releaseWrite := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	served := 0
	c, rec := newTestClient(t, Options{ResponseHeaderTimeout: -1}, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			close(writeArrived)
			<-releaseWrite
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		served++
		n := served
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "bd-1", "title": fmt.Sprintf("read %d", n)})
	})
	c.EnableReadMemo()
	path, err := IssuePath("bd-1")
	if err != nil {
		t.Fatal(err)
	}

	wrote := make(chan error, 1)
	go func() {
		wrote <- c.Do(context.Background(), Request{Op: OpUpdateIssue, Method: http.MethodPatch, Path: path, Body: map[string]any{}}, nil)
	}()
	<-writeArrived // the write's pre-send clear has run, and the server holds it
	during, err := memoGetIssue(t, c, "bd-1", nil)
	close(releaseWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}

	after, err := memoGetIssue(t, c, "bd-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 3 || after == during {
		t.Fatalf("the read after the write answered %q (the in-flight read's %q) in %d requests; want it re-read (3 requests)", after, during, rec.count())
	}
}
