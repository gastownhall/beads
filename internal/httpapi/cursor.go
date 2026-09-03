package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// listOrder names one of the total orders the issue listing serves. It is the
// `sort` vocabulary and the cursor's order tag in one type, so the value a
// request asks for and the value a position was minted in are the same kind of
// thing and can be compared directly.
//
// THE SET IS CLOSED, and every member of it costs a position shape, a
// strictly-after predicate, an index and a conformance case. An order with no
// proven total key cannot be served with a cursor at all — it can only be
// walked to exhaustion and sorted client-side, which is what this type exists
// to stop doing for the two orders below.
type listOrder string

const (
	// orderCreated is (created_at DESC, id ASC): the order this operation has
	// always served, and what an absent `sort` means. Permanently.
	orderCreated listOrder = "created"
	// orderPriority is (priority ASC, created_at DESC, id ASC): the order
	// `bd list` renders with no flags, which is also what `bd list --sort
	// priority` produces — a pure priority comparator applied STABLY over the
	// created order is the same sequence. One served order therefore answers
	// both, in one request instead of a walk.
	orderPriority listOrder = "priority"
)

// listOrders is the documented vocabulary, in the document's order.
var listOrders = []string{string(orderCreated), string(orderPriority)}

// listOrderDefault is the order an absent `sort` selects.
//
// IT CANNOT CHANGE. Every v0 client — including the walking clients this
// parameter exists to retire, which resume with their own created-ordered
// bookkeeping — was written when this was the only order there was. Moving it
// would alter which rows a truncated page contains for all of them, with no
// error and no version to notice it by.
const listOrderDefault = orderCreated

// cursorVersion prefixes every token this server mints. It is the ONLY thing
// that invalidates a cursor: the token carries a position and nothing else, so
// it does not expire, does not die with a restart, and is not tied to the
// connection that issued it. Bumping this is how an encoding change is made
// safely — every older token then decodes to invalid_cursor, whose documented
// recovery is to restart paging.
const cursorVersion = "v2"

// cursorVersionV1 is the retired encoding: base64 of `{t,i}`, minted when
// (created_at DESC, id ASC) was the only order this operation served.
//
// It stays DECODABLE, as exactly that order, so bumping to v2 costs no client a
// restarted traversal — a client mid-walk when a server upgrades is holding one
// of these. It is never minted again, and it is refused under `sort=priority`
// for the same reason a v2 created-order token is: it is a position in the
// created order and means nothing else.
const cursorVersionV1 = "v1"

// cursorPosition is the keyset position a token carries: one row's place in a
// named total order.
//
// The token deliberately does NOT carry the filters it was minted under. That
// is a documented property with a documented consequence — a cursor reused
// under different filters is not refused, it just resumes from the old
// position — and it is what keeps the token from becoming a second, opaque
// copy of the request that can disagree with the request itself.
//
// THE ORDER IS NOT A FILTER, which is why it is here and they are not. A
// filter selects the SET; the order decides what the position MEANS. The bytes
// of `{"t":…,"i":…}` are a position in the created order, a position in the
// priority order, or nothing at all, and there is no way to tell which from the
// bytes. Carrying the order is therefore not a copy of the request — it is the
// position's own type tag, and without it a created-order token replayed under
// `sort=priority` decodes cleanly and is silently misread as a priority
// position, answering 200 with a page that both skips and duplicates rows.
type cursorPosition struct {
	Order     listOrder `json:"o"`
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
	// Priority is present if and only if Order is orderPriority, because that
	// order's position is the triple (priority, created_at, id). A pointer so
	// "the token carried none" is distinguishable from priority 0, which is a
	// real and reachable value.
	Priority *int `json:"p,omitempty"`
}

// encodeCursor mints the opaque token for a page's last row.
//
// The encoding is server-private and clients are told never to parse it, which
// is the whole point: base64 of a JSON object is legible enough that someone
// WILL read it, so the contract is enforced by the version prefix rather than
// by obscurity. A client that constructs its own token gets invalid_cursor the
// moment the encoding moves.
func encodeCursor(pos cursorPosition) string {
	blob, err := json.Marshal(pos)
	if err != nil {
		// Unreachable: the struct is a string, a time, a string and an int.
		return ""
	}
	return cursorVersion + "." + base64.RawURLEncoding.EncodeToString(blob)
}

// decodeCursor reads a token this server minted, IN THE ORDER THE REQUEST IS
// ASKING FOR. Every failure mode — wrong version, undecodable base64, malformed
// JSON, an empty position, a position in another order, a priority order
// missing its priority — is the same answer, because they are the same client
// situation: the position cannot be salvaged and re-sending the value cannot
// succeed.
//
// THE ORDER IS A PARAMETER RATHER THAN A FIELD THE CALLER CHECKS AFTERWARDS.
// A decoder that returned the position and left the comparison to its caller
// would be correct at every call site that remembered, and the one that forgot
// would not fail loudly — it would serve a wrong page with a 200. Passing
// `want` in makes the mismatch unrepresentable at the seam instead of merely
// detectable somewhere past it.
func decodeCursor(token string, want listOrder) (cursorPosition, bool) {
	if rest, ok := strings.CutPrefix(token, cursorVersionV1+"."); ok {
		// The retired encoding named no order because it had none to name.
		// Every v1 token in existence is a created-order position.
		if want != orderCreated {
			return cursorPosition{}, false
		}
		pos, ok := decodeCursorBlob(rest)
		if !ok {
			return cursorPosition{}, false
		}
		pos.Order = orderCreated
		pos.Priority = nil
		return pos, true
	}
	rest, ok := strings.CutPrefix(token, cursorVersion+".")
	if !ok {
		return cursorPosition{}, false
	}
	pos, ok := decodeCursorBlob(rest)
	if !ok {
		return cursorPosition{}, false
	}
	if pos.Order != want {
		return cursorPosition{}, false
	}
	// A priority-order token with no priority is two thirds of a position, and
	// a created-order one with a priority is a position in an order it does not
	// name. Neither is mintable here, which is precisely why both are refused:
	// the token is base64 of legible JSON, so the shapes a client hand-rolls
	// are the shapes that have to be turned down.
	if (pos.Order == orderPriority) != (pos.Priority != nil) {
		return cursorPosition{}, false
	}
	return pos, true
}

// decodeCursorBlob reads the base64 JSON body shared by both encodings.
func decodeCursorBlob(blob string) (cursorPosition, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		return cursorPosition{}, false
	}
	var pos cursorPosition
	if err := json.Unmarshal(raw, &pos); err != nil {
		return cursorPosition{}, false
	}
	if pos.CreatedAt.IsZero() {
		return cursorPosition{}, false
	}
	return pos, true
}

// cursorFor returns the token that resumes after the last row of a page in the
// order that page was served in, or "" when there is nothing to resume from.
func cursorFor(items []*types.IssueWithCounts, order listOrder) string {
	if len(items) == 0 {
		return ""
	}
	last := items[len(items)-1]
	if last == nil || last.Issue == nil {
		return ""
	}
	pos := cursorPosition{Order: order, CreatedAt: last.CreatedAt, ID: last.ID}
	if order == orderPriority {
		priority := last.Priority
		pos.Priority = &priority
	}
	return encodeCursor(pos)
}
