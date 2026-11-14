package wire

import (
	"net/http"
	"sync"
	"time"
)

// ReadMemoTTL bounds how long a memoized read answers for, counted from when
// the server answered it: a hit never renews it. The memo exists to collapse
// the identical reads ONE bd command makes back to back (an id resolution,
// then the command's own fetch of the same row, then its output render), all
// within a second or two; it is not a cache of the workspace. An entry older
// than this is re-read, so even a command that stays alive (an editor session
// between two reads) never answers from a view older than this.
const ReadMemoTTL = 5 * time.Second

// memoableOps are the reads the memo serves: single-row and settings reads,
// which a command repeats verbatim. Listings, counts and the ready queue are
// not memoized — a command reads each once, and they are where a concurrent
// writer's change matters most.
var memoableOps = map[string]bool{
	OpGetIssue:     true,
	OpGetSetting:   true,
	OpListSettings: true,
}

// readMemo holds whole 2xx responses to memoable GETs, keyed by their full URL
// (path AND query: getIssue with include_comments is a different answer from a
// bare getIssue). Responses are stored as the raw bytes the server sent, and
// every hit is decoded afresh by Do, so no two callers share a decoded value
// and the decode path (revision tolerance included) is the same as a miss's.
type readMemo struct {
	mu  sync.Mutex
	now func() time.Time
	// gen counts the clears. A read takes it when it misses and stores its
	// answer only if no clear has happened since: a read whose request was in
	// flight while a write went through may carry the pre-write row even when
	// it returns after the write's own clears, and storing that row would hand
	// it to every later read for a whole TTL.
	gen     uint64
	entries map[string]memoEntry
}

type memoEntry struct {
	res *response
	at  time.Time
}

// EnableReadMemo turns on per-client memoization of identical single-row and
// settings reads (bts-xpw.3.14.6). It is opt-in and meant for a client whose
// lifetime is one CLI command: cmd/bd enables it through httpclient.WithReadMemo,
// for one-shot commands only. Any non-GET request through the client drops
// every memoized answer, before it is sent and again after it returns, and an
// answer to a read that overlapped such a request is not stored, so a command
// always re-reads what its own write could have changed.
//
// Call it before the client is shared; it is idempotent.
func (c *Client) EnableReadMemo() {
	if c.memo != nil {
		return
	}
	c.memo = &readMemo{now: time.Now, entries: map[string]memoEntry{}}
}

// ReadMemoEnabled reports whether EnableReadMemo has been called. A caller
// with a choice of reads (one listSettings, or one getSetting per key) uses it
// to prefer the read a memo can answer again.
func (c *Client) ReadMemoEnabled() bool { return c.memo != nil }

// lookup returns the live answer memoized under key, or nil, together with the
// memo's generation as of the lookup, which a miss hands back to put.
func (m *readMemo) lookup(key string) (*response, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, m.gen
	}
	if m.now().Sub(e.at) > ReadMemoTTL {
		delete(m.entries, key)
		return nil, m.gen
	}
	return e.res, m.gen
}

// put stores the server's answer to a read that missed at generation gen. Only
// a miss's answer is ever stored, never a hit's: an entry's age is its
// answer's, and re-storing a hit would let a row read at least once per TTL
// answer from its first read forever. A non-2xx answer is not stored (a
// missing row may be created next), nor is one whose read overlapped a clear.
// Expired entries are swept on the way in, so however long the client lives
// the memo holds at most one TTL's worth of answers.
func (m *readMemo) put(key string, res *response, gen uint64) {
	if res == nil || res.status < 200 || res.status >= 300 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.gen {
		return
	}
	now := m.now()
	for k, e := range m.entries {
		if now.Sub(e.at) > ReadMemoTTL {
			delete(m.entries, k)
		}
	}
	m.entries[key] = memoEntry{res: res, at: now}
}

// clear drops every memoized answer and starts a new generation, so a read
// already in flight cannot store its answer either.
func (m *readMemo) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	clear(m.entries)
}

// memoable reports whether r is a read the memo may answer.
func memoable(r Request) bool {
	return r.Method == http.MethodGet && memoableOps[r.Op]
}
