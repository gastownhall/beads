package httpclient

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// UNLIMITED READS OVER HTTP.
//
// Every issue listing on the v0 wire spells "unlimited" as `limit=0`, and a
// server started with --allow-non-loopback refuses that spelling outright
// (400, "unlimited reads are loopback-only; pass an explicit limit"). The
// document's recovery is client-side: ask for an explicit limit, and page. So
// this client NEVER puts `limit=0` on the wire, whatever the server's bind
// mode — one spelling for every server is what keeps a loopback-tested caller
// from meeting the refusal for the first time in production. dispatch holds
// the line (refuseUnlimitedOnWire), so a new read path that forgets is a loud
// client error rather than a silent difference between a laptop and a fleet.
//
// What replaces `limit=0` depends on what paging the operation publishes:
//
//   - listIssues publishes a keyset cursor, so an unlimited `bd list` is the
//     cursor walk in list_walk.go, at walkPageSize rows a request.
//   - listReadyWork and queryIssues publish NO cursor and no offset (their
//     pages are has_more-only by design: a ready set and a predicate query are
//     assembled outside the database's keyset order). There is nothing to walk,
//     so an unlimited read is ONE request bounded at the cap plus one: the
//     overage row is what proves the cap fired.
//
// THE CAP. Every unlimited read is bounded on the client at
// UnlimitedReadCapEnv rows (DefaultUnlimitedReadCap when unset). Past it the
// read is REFUSED with *UnlimitedReadCapError — never truncated. A truncated
// "unlimited" answer is the defect this file exists to remove (the server's
// 100-row ready default used to cap gc's routed-pool queues silently), so the
// only two answers are "every row" and "a typed refusal naming the cap".
//
// SNAPSHOT SEMANTICS, stated per shape because they differ:
//
//   - ready and query: one request is one server read, so the answer is one
//     consistent snapshot of the ready set / the matching set.
//   - list: the cursor walk is one server read PER PAGE, not a snapshot. The
//     keyset is strict, so no row is returned twice, and a row that exists and
//     matches the filter for the whole walk is returned exactly once. A row
//     created during the walk, or one whose status/fields change so that it
//     starts or stops matching, may or may not appear — the answer is "the rows
//     that matched at the moment each page was read". That is the same promise
//     a paging caller of the wire gets, and it is the reason the ready shape
//     (whose consumers act on the whole set at once) is NOT emulated through a
//     list walk.

// UnlimitedReadCapEnv bounds how many rows one unlimited read over HTTP may
// return before it is refused. A positive integer; unset means
// DefaultUnlimitedReadCap. A malformed value is an error on every unlimited
// read rather than a silent fall back to the default, so a typo cannot quietly
// re-impose a bound the operator meant to raise.
const UnlimitedReadCapEnv = "BEADS_HTTP_UNLIMITED_READ_CAP"

// DefaultUnlimitedReadCap is the cap when UnlimitedReadCapEnv is unset.
const DefaultUnlimitedReadCap = 10000

// maxUnlimitedReadCap is the largest cap UnlimitedReadCapEnv accepts. It is a
// sanity bound on the operator's knob, not a tuning value: the request asks
// the server to buffer cap+1 rows in one response.
const maxUnlimitedReadCap = 1_000_000

// ErrUnlimitedReadCap is the sentinel every *UnlimitedReadCapError matches
// under errors.Is.
var ErrUnlimitedReadCap = errors.New("unlimited read exceeds the http client cap")

// UnlimitedReadCapError refuses an unlimited read whose answer is larger than
// the client cap, or whose server would not hand back the whole answer.
type UnlimitedReadCapError struct {
	// Op is the wire operation (listReadyWork, queryIssues, listIssues).
	Op string
	// Cap is the bound in force.
	Cap int
	// Found is how many rows had arrived when the refusal fired: Cap+1 when the
	// cap fired, or the page length when the server truncated (see below).
	Found int
	// ServerTruncated marks the other refusal: the server returned no more than
	// the cap yet reported has_more, so it bounded the page below what was
	// asked. Returning that page would be the silent truncation this refusal
	// exists to prevent.
	ServerTruncated bool
}

func (e *UnlimitedReadCapError) Error() string {
	if e.ServerTruncated {
		return fmt.Sprintf("unlimited %s over http: the server returned %d rows with more remaining, "+
			"below the requested bound of %d; refusing a truncated answer (pass an explicit --limit)",
			e.Op, e.Found, e.Cap+1)
	}
	return fmt.Sprintf("unlimited %s over http: more than %d rows match (%s=%d); "+
		"refine the query, pass an explicit --limit, or raise %s",
		e.Op, e.Cap, UnlimitedReadCapEnv, e.Cap, UnlimitedReadCapEnv)
}

func (e *UnlimitedReadCapError) Unwrap() error { return ErrUnlimitedReadCap }

// unlimitedReadCap reads UnlimitedReadCapEnv.
func unlimitedReadCap() (int, error) {
	raw := strings.TrimSpace(os.Getenv(UnlimitedReadCapEnv))
	if raw == "" {
		return DefaultUnlimitedReadCap, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxUnlimitedReadCap {
		return 0, fmt.Errorf("%s=%q: want an integer between 1 and %d", UnlimitedReadCapEnv, raw, maxUnlimitedReadCap)
	}
	return n, nil
}

// boundUnlimitedPage writes the bounded `limit` that replaces an unlimited
// read on a cursorless listing, returning the cap in force, or 0 when the
// request is not unlimited and goes out unchanged.
//
// params is the encoder's output and is modified in place. The encoder maps
// fields to the wire's own spelling (`limit=0` for an explicit zero, nothing
// for a legacy zero); how an unlimited read is honored is the pager's
// decision, made here.
func boundUnlimitedPage(params url.Values, unlimited bool) (int, error) {
	if !unlimited {
		return 0, nil
	}
	readCap, err := unlimitedReadCap()
	if err != nil {
		return 0, err
	}
	params.Set("limit", strconv.Itoa(readCap+1))
	return readCap, nil
}

// checkUnlimitedPage applies the cap to the one page a cursorless unlimited
// read received. readCap 0 means the read was bounded by the caller and the
// page is returned as the server shaped it.
func checkUnlimitedPage(op string, readCap, rows int, hasMore bool) error {
	switch {
	case readCap == 0:
		return nil
	case rows > readCap:
		return &UnlimitedReadCapError{Op: op, Cap: readCap, Found: rows}
	case hasMore:
		return &UnlimitedReadCapError{Op: op, Cap: readCap, Found: rows, ServerTruncated: true}
	}
	return nil
}

// errUnlimitedOnWire is the dispatch tripwire's answer: a client bug, never a
// server condition, so it names no server and is not retried.
var errUnlimitedOnWire = errors.New("httpclient: refusing to send limit=0 (unlimited) on the wire; an unlimited read must be paged or bounded by the client cap")

// unlimitedSpellingOps are the operations whose `limit=0` means unlimited and
// is refused off loopback.
var unlimitedSpellingOps = map[string]bool{
	wire.OpListReadyWork: true,
	wire.OpListIssues:    true,
	wire.OpQueryIssues:   true,
}

// refuseUnlimitedOnWire is dispatch's guard that `limit=0` never leaves this
// client on an operation that reads it as unlimited.
func refuseUnlimitedOnWire(req wire.Request) error {
	if unlimitedSpellingOps[req.Op] && req.Query != nil && req.Query.Get("limit") == "0" {
		return fmt.Errorf("%w (%s)", errUnlimitedOnWire, req.Op)
	}
	return nil
}
