package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// cursorVersion prefixes every token this server mints. It is the ONLY thing
// that invalidates a cursor: the token carries a position and nothing else, so
// it does not expire, does not die with a restart, and is not tied to the
// connection that issued it. Bumping this is how an encoding change is made
// safely — every older token then decodes to invalid_cursor, whose documented
// recovery is to restart paging.
const cursorVersion = "v1"

// cursorPosition is the keyset position a token carries: one row's place in
// the (created_at DESC, id ASC) total order.
//
// The token deliberately does NOT carry the filters it was minted under. That
// is a documented property with a documented consequence — a cursor reused
// under different filters is not refused, it just resumes from the old
// position — and it is what keeps the token from becoming a second, opaque
// copy of the request that can disagree with the request itself.
type cursorPosition struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
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
		// Unreachable: the struct is a time and a string.
		return ""
	}
	return cursorVersion + "." + base64.RawURLEncoding.EncodeToString(blob)
}

// decodeCursor reads a token this server minted. Every failure mode —
// wrong version, undecodable base64, malformed JSON, an empty position — is
// the same answer, because they are the same client situation: the position
// cannot be salvaged and re-sending the value cannot succeed.
func decodeCursor(token string) (cursorPosition, bool) {
	rest, ok := strings.CutPrefix(token, cursorVersion+".")
	if !ok {
		return cursorPosition{}, false
	}
	blob, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return cursorPosition{}, false
	}
	var pos cursorPosition
	if err := json.Unmarshal(blob, &pos); err != nil {
		return cursorPosition{}, false
	}
	if pos.CreatedAt.IsZero() {
		return cursorPosition{}, false
	}
	return pos, true
}

// cursorFor returns the token that resumes after the last row of a page, or ""
// when there is nothing to resume from.
func cursorFor(items []*types.IssueWithCounts) string {
	if len(items) == 0 {
		return ""
	}
	last := items[len(items)-1]
	if last == nil || last.Issue == nil {
		return ""
	}
	return encodeCursor(cursorPosition{CreatedAt: last.CreatedAt, ID: last.ID})
}
