package graphops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The laws: pure functions over strings and bytes, stated once here and
// enforced by every constructor in types.go. Nothing in this file touches a
// store, a clock, or a random source. Each law cites where its text comes
// from — the BDP draft at the pin (docs/specs/bdp.md, commit 0b7d86e7), its
// schema bundle, or the reference implementation that the pinned conformance
// matrix was proven against (packages/protocol/src/read-values.ts) — so a
// reader can check the code against the sentence rather than trust it.

// reason strips the ErrValidation prefix from a validation error's message so
// a law that wraps another law's refusal does not stutter the sentinel.
func reason(err error) string {
	return strings.TrimPrefix(err.Error(), ErrValidation.Error()+": ")
}

// isLowerHex reports whether s is exactly n lowercase hexadecimal digits: the
// shape of every id, revision, fingerprint and hash column.
func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// hashHex is SHA-256 as 64 lowercase hex digits: the ledger hash and the
// descriptor fingerprint.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Canonical-ID grammar
//
// BDP, "Scopes and identity" and "Scope discovery": a local Bead ID is
// beads/{id-path}, a local Link ID is links/{id-path}, an alias is
// alias/{alias-path}; {id-path} is one or more nonempty segments that are
// opaque identity. Empty, "." and ".." segments, controls, backslashes,
// queries, fragments, scheme-relative references and encoded "/" or "\"
// separators are invalid. Percent escapes are decoded exactly once and must
// decode to valid UTF-8; unreserved characters are emitted literally and every
// required escape uses uppercase hex; decoded segments compare exactly, with
// no Unicode normalization. A supplied spelling that is not already canonical
// is REJECTED, never normalized: trimming would mint an identity the creator
// did not write.
//
// The literal set is the reference implementation's LITERAL_PATH_CHARACTER
// (read-values.ts): RFC 3986 pchar minus pct-encoded — unreserved, sub-delims,
// ":" and "@". Everything else, non-ASCII included, is percent-encoded, so a
// canonical path is always pure ASCII.
// ---------------------------------------------------------------------------

const (
	beadRoot  = "beads"
	linkRoot  = "links"
	aliasRoot = "alias"
)

const upperHexDigits = "0123456789ABCDEF"
const lowerHexDigits = "0123456789abcdef"

func isLiteralSegmentByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '.', '_', '~', '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', ':', '@', '-':
		return true
	}
	return false
}

// isUnreservedByte is RFC 3986 unreserved: a character that is never
// percent-encoded in a canonical URL.
func isUnreservedByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return c == '-' || c == '.' || c == '_' || c == '~'
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// CanonicalSegment encodes one DECODED segment the way the authority emits
// it: the literal set as is, every other byte of its UTF-8 form as an
// uppercase percent escape. It is the encoder half of the grammar; an
// authority allocating an id, or a creator spelling one, produces exactly
// this, and ValidateCanonicalSegment accepts exactly this.
func CanonicalSegment(decoded string) string {
	var b strings.Builder
	b.Grow(len(decoded))
	for i := 0; i < len(decoded); i++ {
		c := decoded[i]
		if isLiteralSegmentByte(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHexDigits[c>>4])
		b.WriteByte(upperHexDigits[c&0x0F])
	}
	return b.String()
}

// decodeSegment decodes percent escapes exactly once. It accepts either hex
// case here — the canonical re-encoding comparison is what refuses lowercase.
func decodeSegment(segment string) ([]byte, error) {
	out := make([]byte, 0, len(segment))
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if c != '%' {
			out = append(out, c)
			continue
		}
		if i+2 >= len(segment) {
			return nil, errors.New("incomplete percent escape")
		}
		hi, ok1 := hexValue(segment[i+1])
		lo, ok2 := hexValue(segment[i+2])
		if !ok1 || !ok2 {
			return nil, errors.New("malformed percent escape")
		}
		out = append(out, hi<<4|lo)
		i += 2
	}
	return out, nil
}

// ValidateCanonicalSegment applies the segment grammar to one segment of a
// local ID, an alias, or a Scope URL path: nonempty; percent escapes complete
// and decoding, once, to valid UTF-8; not "." or ".."; no "/" or "\" and no
// ASCII control (U+0000–U+001F, U+007F) after decoding; and spelled exactly as
// CanonicalSegment would spell the decoded value — which is what refuses a
// lowercase escape, an escaped unreserved character, and a literal non-ASCII
// character.
func ValidateCanonicalSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("%w: path segment must be nonempty", ErrValidation)
	}
	decoded, err := decodeSegment(segment)
	if err != nil {
		return fmt.Errorf("%w: path segment %q: %v", ErrValidation, segment, err)
	}
	if !utf8.Valid(decoded) {
		return fmt.Errorf("%w: path segment %q does not decode to valid UTF-8", ErrValidation, segment)
	}
	s := string(decoded)
	if s == "." || s == ".." {
		return fmt.Errorf("%w: path segment %q is a dot segment", ErrValidation, segment)
	}
	for _, r := range s {
		switch {
		case r <= 0x1F || r == 0x7F:
			return fmt.Errorf("%w: path segment %q decodes to a control character", ErrValidation, segment)
		case r == '/' || r == '\\':
			return fmt.Errorf("%w: path segment %q decodes to a separator", ErrValidation, segment)
		}
	}
	if CanonicalSegment(s) != segment {
		return fmt.Errorf("%w: path segment %q is not canonically encoded (canonical spelling is %q)", ErrValidation, segment, CanonicalSegment(s))
	}
	return nil
}

func validateRootedPath(path, root, what string) error {
	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf("%w: %s %q must not contain a query or fragment", ErrValidation, what, path)
	}
	segments := strings.Split(path, "/")
	if segments[0] != root || len(segments) < 2 {
		return fmt.Errorf("%w: %s %q must begin with %s/ and contain an ID path", ErrValidation, what, path, root)
	}
	for _, segment := range segments[1:] {
		if err := ValidateCanonicalSegment(segment); err != nil {
			return fmt.Errorf("%w: %s %q: %s", ErrValidation, what, path, reason(err))
		}
	}
	return nil
}

// ValidateBeadPath accepts exactly the canonical local Bead IDs: "beads/"
// followed by one or more canonical segments. Case matters — beads/Task and
// beads/task are two Beads.
func ValidateBeadPath(path string) error { return validateRootedPath(path, beadRoot, "bead path") }

// ValidateLinkPath accepts exactly the canonical local Link IDs under "links/".
func ValidateLinkPath(path string) error { return validateRootedPath(path, linkRoot, "link path") }

// ValidateAliasPath accepts exactly the alias locators under "alias/": the
// same grammar, so whether a spelling names identity or an alias is decidable
// from the spelling alone. Aliases are not served in v0; the law is here so
// the reserved root is refused as identity rather than mistaken for it.
func ValidateAliasPath(path string) error { return validateRootedPath(path, aliasRoot, "alias path") }

// ValidatePath validates a path under the root its kind fixes.
func ValidatePath(path string, kind ResourceKind) error {
	switch kind {
	case KindBead:
		return ValidateBeadPath(path)
	case KindLink:
		return ValidateLinkPath(path)
	}
	return fmt.Errorf("%w: resource kind %q is not bead or link", ErrValidation, kind)
}

// CanonicalURL is the absolute canonical Resource URL: the Scope URL (which
// ends in "/") followed by the Scope-relative path. It is concatenation, by
// design — rows store the path, the URL is computed at the boundary, and a
// Scope URL rotation rewrites nothing. Both inputs are assumed valid.
func CanonicalURL(scopeURL, path string) string { return scopeURL + path }

// SplitCanonicalURL is the inverse: for a URL that is exactly scopeURL
// followed by a canonical Bead or Link path, it returns the path and its kind.
// Anything else — a different Scope, a non-canonical spelling, a path under
// another root — is not a canonical Resource URL of this Scope. scopeURL is
// assumed valid.
func SplitCanonicalURL(scopeURL, url string) (path string, kind ResourceKind, ok bool) {
	if !strings.HasPrefix(url, scopeURL) {
		return "", "", false
	}
	path = url[len(scopeURL):]
	switch {
	case ValidateBeadPath(path) == nil:
		return path, KindBead, true
	case ValidateLinkPath(path) == nil:
		return path, KindLink, true
	}
	return "", "", false
}

// ---------------------------------------------------------------------------
// Code-unit ordering
//
// BDP, "Collection retrieval and selection": the baseline order every
// authority must serve is canonical-uri — ascending lexicographic comparison,
// by Unicode code unit, of each item's absolute canonical id — and the
// reference implementation compares with JavaScript's string operators, which
// order by UTF-16 code unit. RFC 8785 §3.2.3 sorts object keys the same way.
// This is that comparison, without converting either string.
//
// For the ASCII alphabet every canonical path and URL is made of, code-unit
// order IS byte order, which is what the storage leg's binary-collated
// ORDER BY path produces. The two orders differ only for strings mixing
// characters above U+FFFF with characters in U+E000–U+FFFF (a supplementary
// character's lead surrogate sorts below them), which is why the law is stated
// over code units rather than bytes and why JSON keys sort here and not with
// bytes.Compare.
// ---------------------------------------------------------------------------

// codeUnitKey maps a scalar value to an integer whose order is the order of
// its UTF-16 code-unit sequence: a BMP character sorts by its single unit, a
// supplementary character by its lead surrogate and then its trail. Because a
// scalar value is never a surrogate, a BMP unit never ties with a lead
// surrogate, so the packed comparison decides every pair.
func codeUnitKey(r rune) uint32 {
	if r < 0x10000 {
		return uint32(r) << 16
	}
	r -= 0x10000
	return (0xD800+uint32(r>>10))<<16 | (0xDC00 + uint32(r&0x3FF))
}

// CompareCodeUnits orders two strings by their UTF-16 code units, returning
// -1, 0 or +1. Invalid UTF-8 decodes as U+FFFD, as it would in every consumer.
func CompareCodeUnits(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			if codeUnitKey(ra) < codeUnitKey(rb) {
				return -1
			}
			return 1
		}
		a, b = a[na:], b[nb:]
	}
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return -1
	}
	return 1
}

// ---------------------------------------------------------------------------
// JSON canonicalization, the number admission law, and equality
//
// What is canonicalized: any JSON text (RFC 8259) into ONE byte string, such
// that two texts canonicalize to the same bytes exactly when they are equal
// under RFC 6902 §4.6 — the comparison that decides whether a properties write
// is a no-op. The form is RFC 8785 (JCS), bit-exact over every admitted
// document, with an admission law on numbers where JCS would silently round:
//
//   - Whitespace outside strings is removed.
//   - Object members are sorted by the UTF-16 code units of their keys
//     (CompareCodeUnits); a duplicate key is refused, as I-JSON requires.
//   - Strings are serialized as ECMAScript JSON.stringify does: '"' and '\'
//     escaped, U+0008/0009/000A/000C/000D as \b \t \n \f \r, every other
//     control below U+0020 as \u00xx with lowercase hex, and everything else
//     literally in UTF-8 — U+007F, U+2028, non-ASCII, '/' included. An input
//     that is not valid UTF-8, or that escapes a lone surrogate, is refused
//     rather than laundered into U+FFFD.
//   - Literals are true, false, null.
//   - Numbers are serialized with ECMAScript's Number::toString digit
//     placement (RFC 8785 §3.2.2.3): no exponent for 10^-6 ≤ |x| < 10^21,
//     shortest digit string, "e+"/"e-" otherwise — applied to the EXACT
//     DECIMAL VALUE of the literal, never to a float64 reading of it — and a
//     literal is ADMITTED only if that value round-trips through IEEE-754
//     binary64 (bdp#21, ruled 2026-09-08; RFC 7493 I-JSON's interoperability
//     rule made mandatory). Round-trip, precisely: parse the literal's exact
//     decimal value to the nearest binary64 (round-to-nearest-even, which is
//     what strconv.ParseFloat computes however the literal is spelled and
//     however long it is); serialize that double as the shortest decimal
//     that reads back to it (strconv.FormatFloat with precision -1, the
//     digits Number::toString prints); the literal is admissible exactly
//     when that decimal's value is the literal's own value. So 1, 1.0 and
//     1e0 admit and canonicalize to "1"; -0.0 to "0"; 1e300 and 1E300 to
//     "1e+300"; 9007199254740992 (2^53) and 9007199254740994 (a double whose
//     shortest spelling is itself) admit as themselves. 9007199254740993
//     (2^53+1, halfway between two doubles), a 20-significant-digit decimal,
//     18446744073709551616 (2^64: a double, but one a binary64 reader
//     spells 18446744073709552000), 1e400 (past the range) and 1e-400
//     (below it, read as 0) are refused with ErrValidation naming the RFC
//     6901 JSON pointer of the offending member or element and what a
//     binary64 reader would have made of the value.
//
//     The canonical bytes are the literal's own digits — no float64
//     laundering — and on every admitted value they coincide, by
//     construction, with what RFC 8785 emits for the nearest double: the
//     exact-decimal model and the binary64 model agree wherever a value is
//     admitted, and disagree only where the value is refused. RFC 6902
//     equality is numeric equality over that admitted domain.
//
// DECISION 3 (P0, 2026-09-07; amended by the bdp#21 ruling, 2026-09-08). The
// P0 form was exact-value JCS with one stated departure: a literal a double
// could not carry (an integer past 2^53, 18 or more significant digits, an
// exponent past the range) was preserved exactly where JCS rounds it. It was
// chosen over bit-exact JCS (which rounds numbers the storage design existed
// to keep) and source-literal preservation (the metadata plane's rule, under
// which 1 and 1.0 are different values, contradicting the no-op law), and it
// left B4's interop caveat: a JCS peer modeling numbers as binary64 could
// not reproduce such a value. The ruling closes the caveat from the other
// side — equality stays exact-decimal, and a value no binary64 peer could
// reproduce is refused at admission instead of stored. Two consequences are
// recorded where they bite: the ledger framing is NOT under the law (see
// "Ledger hashing": its integers are Go-typed, hashed exactly, and the frozen
// layout is unchanged), and a descriptor's ownsOutgoing max obeys the law at
// both of its doors (ParseTypeDescriptor and NewOwnedLinkDecl), so every
// descriptor that exists has a canonical form the law admits.
//
// DECISION: the order of the number refusals. Syntax first; then the spelled
// exponent guard and the normalized exponent bound (maxExponentMagnitude,
// "exponent out of range"); then the binary64 law. 1e1000000000001 is
// therefore refused as out of range and 1e400 as outside binary64 — both
// ErrValidation, each message naming the first law the literal breaks. The
// exponent bound stays (see its comment) although, for every nonzero literal
// in admission mode, the binary64 range is now the tighter one.
//
// Nesting is bounded at maxJSONDepth (the depth encoding/json accepts) as a
// stack-safety measure, not a protocol limit; advertised limits are the
// serving surface's.
// ---------------------------------------------------------------------------

const maxJSONDepth = 10000

// CanonicalizeJSON returns the canonical bytes of one JSON text, or
// ErrValidation for anything that is not exactly one well-formed JSON value —
// a number the binary64 admission law refuses included, reported with the
// RFC 6901 JSON pointer of the offending member or element ("" is the whole
// document). This is the one door through which user-supplied JSON enters
// the domain: NewProperties, ParseTypeDescriptor and JSONEqual all pass
// through it.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	return canonicalizeJSON(raw, false)
}

// canonicalizeJSONExact is CanonicalizeJSON without the binary64 admission
// law: every number is kept at its exact decimal value whatever a binary64
// reader would make of it. It exists for the ledger framing alone (see
// "Ledger hashing"), whose integers are Go-typed and hashed exactly under a
// frozen layout, and it is deliberately unexported: nothing user-supplied
// reaches the domain through it.
func canonicalizeJSONExact(raw []byte) ([]byte, error) {
	return canonicalizeJSON(raw, true)
}

func canonicalizeJSON(raw []byte, exact bool) ([]byte, error) {
	s := &jsonScanner{in: raw, exact: exact}
	s.skipSpace()
	out, err := s.value(make([]byte, 0, len(raw)))
	if err != nil {
		return nil, err
	}
	s.skipSpace()
	if s.pos != len(s.in) {
		return nil, s.errorf("unexpected content after the JSON value")
	}
	return out, nil
}

// JSONEqual is RFC 6902 §4.6 equality of two JSON texts: numbers by numeric
// value, strings by code points, arrays element-wise, objects as member sets,
// literals as themselves. Either text failing to parse — or carrying a number
// the admission law refuses — is an error.
func JSONEqual(a, b []byte) (bool, error) {
	ca, err := CanonicalizeJSON(a)
	if err != nil {
		return false, err
	}
	cb, err := CanonicalizeJSON(b)
	if err != nil {
		return false, err
	}
	return string(ca) == string(cb), nil
}

type jsonScanner struct {
	in    []byte
	pos   int
	depth int
	// path is the RFC 6901 location of the value being scanned: one token per
	// enclosing object member or array element, pushed before the value and
	// popped after it, so a refusal can say where it bit.
	path []jsonPathToken
	// exact disables the binary64 admission law. Only canonicalizeJSONExact
	// sets it.
	exact bool
}

// jsonPathToken is one RFC 6901 reference token: a member name or an index.
type jsonPathToken struct {
	key   string
	index int
	isKey bool
}

// pointerEscaper is RFC 6901 §3: "~" becomes "~0" and "/" becomes "~1".
var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// pointer renders the scanner's current location as an RFC 6901 JSON
// pointer: "" for the whole document, "/a/0/b~1c" for member "b/c" of the
// first element of member "a".
func (s *jsonScanner) pointer() string {
	var b strings.Builder
	for _, t := range s.path {
		b.WriteByte('/')
		if t.isKey {
			b.WriteString(pointerEscaper.Replace(t.key))
		} else {
			b.WriteString(strconv.Itoa(t.index))
		}
	}
	return b.String()
}

func (s *jsonScanner) errorf(format string, args ...any) error {
	return s.errorAt(s.pos, format, args...)
}

func (s *jsonScanner) errorAt(pos int, format string, args ...any) error {
	return fmt.Errorf("%w: JSON at byte %d: %s", ErrValidation, pos, fmt.Sprintf(format, args...))
}

func (s *jsonScanner) skipSpace() {
	for s.pos < len(s.in) {
		switch s.in[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

func (s *jsonScanner) value(out []byte) ([]byte, error) {
	if s.pos >= len(s.in) {
		return nil, s.errorf("unexpected end of input")
	}
	switch c := s.in[s.pos]; {
	case c == '{':
		return s.object(out)
	case c == '[':
		return s.array(out)
	case c == '"':
		str, err := s.str()
		if err != nil {
			return nil, err
		}
		return appendCanonicalString(out, str), nil
	case c == 't':
		return s.literal(out, "true")
	case c == 'f':
		return s.literal(out, "false")
	case c == 'n':
		return s.literal(out, "null")
	case c == '-' || (c >= '0' && c <= '9'):
		return s.number(out)
	}
	return nil, s.errorf("unexpected byte %q", s.in[s.pos])
}

func (s *jsonScanner) enter() error {
	s.depth++
	if s.depth > maxJSONDepth {
		return s.errorf("nesting deeper than %d", maxJSONDepth)
	}
	return nil
}

type jsonMember struct {
	key   string
	value []byte
}

func (s *jsonScanner) object(out []byte) ([]byte, error) {
	members, err := s.objectMembers()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(members, func(i, j int) bool {
		return CompareCodeUnits(members[i].key, members[j].key) < 0
	})
	out = append(out, '{')
	for i, m := range members {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendCanonicalString(out, m.key)
		out = append(out, ':')
		out = append(out, m.value...)
	}
	return append(out, '}'), nil
}

// objectMembers parses an object at the opening brace and returns its
// members in source order, each value already canonical; a duplicate key is
// refused. object serializes them sorted; the descriptor parser walks them
// as they are.
func (s *jsonScanner) objectMembers() ([]jsonMember, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	s.pos++ // '{'
	s.skipSpace()
	if s.pos < len(s.in) && s.in[s.pos] == '}' {
		s.pos++
		s.depth--
		return nil, nil
	}
	var members []jsonMember
	seen := map[string]struct{}{}
	for {
		s.skipSpace()
		if s.pos >= len(s.in) || s.in[s.pos] != '"' {
			return nil, s.errorf("expected an object key")
		}
		key, err := s.str()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[key]; dup {
			return nil, s.errorf("duplicate object key %q", key)
		}
		seen[key] = struct{}{}
		s.skipSpace()
		if s.pos >= len(s.in) || s.in[s.pos] != ':' {
			return nil, s.errorf("expected ':' after object key")
		}
		s.pos++
		s.skipSpace()
		s.path = append(s.path, jsonPathToken{key: key, isKey: true})
		value, err := s.value(nil)
		if err != nil {
			return nil, err
		}
		s.path = s.path[:len(s.path)-1]
		members = append(members, jsonMember{key: key, value: value})
		s.skipSpace()
		if s.pos >= len(s.in) {
			return nil, s.errorf("unterminated object")
		}
		if s.in[s.pos] == ',' {
			s.pos++
			continue
		}
		if s.in[s.pos] == '}' {
			s.pos++
			break
		}
		return nil, s.errorf("expected ',' or '}' in object")
	}
	s.depth--
	return members, nil
}

// The three helpers below walk CANONICAL bytes — the output of
// CanonicalizeJSON, which is well-formed by construction — so none of them
// can fail, and each trusts that. They are what ParseTypeDescriptor uses to
// see a document member by member with the member names exact: encoding/json
// matches field names case-insensitively, which is not a closed shape.

// canonicalObjectMembers returns the members of a canonical object.
func canonicalObjectMembers(canonical []byte) []jsonMember {
	s := &jsonScanner{in: canonical}
	members, _ := s.objectMembers() // canonical input: cannot fail
	return members
}

// canonicalArrayValues returns the canonical bytes of a canonical array's
// elements.
func canonicalArrayValues(canonical []byte) [][]byte {
	s := &jsonScanner{in: canonical, pos: 1} // past '['
	var out [][]byte
	for s.in[s.pos] != ']' {
		v, _ := s.value(nil) // canonical input: cannot fail
		out = append(out, v)
		if s.in[s.pos] == ',' {
			s.pos++
		}
	}
	return out
}

// canonicalStringValue decodes a canonical JSON string.
func canonicalStringValue(canonical []byte) string {
	s := &jsonScanner{in: canonical}
	str, _ := s.str() // canonical input: cannot fail
	return str
}

func (s *jsonScanner) array(out []byte) ([]byte, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	s.pos++ // '['
	s.skipSpace()
	out = append(out, '[')
	if s.pos < len(s.in) && s.in[s.pos] == ']' {
		s.pos++
		s.depth--
		return append(out, ']'), nil
	}
	for i := 0; ; i++ {
		s.skipSpace()
		if i > 0 {
			out = append(out, ',')
		}
		s.path = append(s.path, jsonPathToken{index: i})
		var err error
		if out, err = s.value(out); err != nil {
			return nil, err
		}
		s.path = s.path[:len(s.path)-1]
		s.skipSpace()
		if s.pos >= len(s.in) {
			return nil, s.errorf("unterminated array")
		}
		if s.in[s.pos] == ',' {
			s.pos++
			continue
		}
		if s.in[s.pos] == ']' {
			s.pos++
			break
		}
		return nil, s.errorf("expected ',' or ']' in array")
	}
	s.depth--
	return append(out, ']'), nil
}

func (s *jsonScanner) literal(out []byte, word string) ([]byte, error) {
	if !strings.HasPrefix(string(s.in[s.pos:]), word) {
		return nil, s.errorf("invalid literal")
	}
	s.pos += len(word)
	return append(out, word...), nil
}

func (s *jsonScanner) hex4() (rune, error) {
	if s.pos+4 > len(s.in) {
		return 0, s.errorf("truncated \\u escape")
	}
	var r rune
	for i := 0; i < 4; i++ {
		v, ok := hexValue(s.in[s.pos+i])
		if !ok {
			return 0, s.errorf("malformed \\u escape")
		}
		r = r<<4 | rune(v)
	}
	s.pos += 4
	return r, nil
}

// str parses a JSON string at the opening quote and returns its decoded
// value. Raw bytes must be valid UTF-8 and free of unescaped controls;
// escapes must be the RFC 8259 set; a \u escape of a surrogate must be half
// of a well-formed pair.
func (s *jsonScanner) str() (string, error) {
	s.pos++ // opening quote
	var buf []byte
	start := s.pos
	for {
		if s.pos >= len(s.in) {
			return "", s.errorf("unterminated string")
		}
		c := s.in[s.pos]
		switch {
		case c == '"':
			segment := s.in[start:s.pos]
			if !utf8.Valid(segment) {
				return "", s.errorf("string is not valid UTF-8")
			}
			s.pos++
			if buf == nil {
				return string(segment), nil
			}
			return string(append(buf, segment...)), nil
		case c == '\\':
			segment := s.in[start:s.pos]
			if !utf8.Valid(segment) {
				return "", s.errorf("string is not valid UTF-8")
			}
			buf = append(buf, segment...)
			s.pos++
			if s.pos >= len(s.in) {
				return "", s.errorf("unterminated string")
			}
			e := s.in[s.pos]
			s.pos++
			switch e {
			case '"', '\\', '/':
				buf = append(buf, e)
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				r, err := s.hex4()
				if err != nil {
					return "", err
				}
				switch {
				case r >= 0xD800 && r <= 0xDBFF:
					if s.pos+1 >= len(s.in) || s.in[s.pos] != '\\' || s.in[s.pos+1] != 'u' {
						return "", s.errorf("lone lead surrogate escape")
					}
					s.pos += 2
					trail, err := s.hex4()
					if err != nil {
						return "", err
					}
					if trail < 0xDC00 || trail > 0xDFFF {
						return "", s.errorf("lone lead surrogate escape")
					}
					r = 0x10000 + (r-0xD800)<<10 + (trail - 0xDC00)
				case r >= 0xDC00 && r <= 0xDFFF:
					return "", s.errorf("lone trail surrogate escape")
				}
				buf = utf8.AppendRune(buf, r)
			default:
				return "", s.errorf("invalid escape \\%c", e)
			}
			start = s.pos
		case c < 0x20:
			return "", s.errorf("unescaped control character in string")
		default:
			s.pos++
		}
	}
}

// appendCanonicalString serializes a decoded string as JSON.stringify would.
func appendCanonicalString(out []byte, str string) []byte {
	out = append(out, '"')
	for i := 0; i < len(str); i++ {
		c := str[i]
		switch {
		case c == '"':
			out = append(out, '\\', '"')
		case c == '\\':
			out = append(out, '\\', '\\')
		case c == '\b':
			out = append(out, '\\', 'b')
		case c == '\f':
			out = append(out, '\\', 'f')
		case c == '\n':
			out = append(out, '\\', 'n')
		case c == '\r':
			out = append(out, '\\', 'r')
		case c == '\t':
			out = append(out, '\\', 't')
		case c < 0x20:
			out = append(out, '\\', 'u', '0', '0', lowerHexDigits[c>>4], lowerHexDigits[c&0x0F])
		default:
			out = append(out, c)
		}
	}
	return append(out, '"')
}

// maxExponentMagnitude bounds the exponent of a number's CANONICAL form —
// the e that Number::toString would print — not the exponent the literal
// happened to be spelled with. Beyond it the exact value is still
// representable here, but no consumer could use it, and unbounded
// accumulation would be an integer overflow waiting to happen. Bounding the
// normalized value is what makes canonicalization a fixed point: a literal
// passes this bound exactly when its canonical form does, so 10e1000000000000
// (canonically 1e+1000000000001) is refused on the way in rather than stored
// and then unreadable, and 0.1e1000000000001 (canonically 1e+1000000000000)
// is not refused HERE although its spelled exponent exceeds the bound.
//
// Since the bdp#21 ruling the binary64 range (about 5e-324 to 1.8e308) is the
// tighter bound for every nonzero literal in admission mode — 1e+1000000000000
// is refused by the admission law on its own account — so this bound decides
// there only for zero, which has no magnitude (0e1000000000005 is still "0",
// and 0e99999999999999999999999 is still refused by the spelled-exponent
// guard), and in exact mode, where the ledger framing spells no exponent at
// all. It stays because the overflow guard and the fixed-point argument rest
// on it, not on the admission law.
const maxExponentMagnitude = 1_000_000_000_000

func (s *jsonScanner) digits() ([]byte, error) {
	start := s.pos
	for s.pos < len(s.in) && s.in[s.pos] >= '0' && s.in[s.pos] <= '9' {
		s.pos++
	}
	if s.pos == start {
		return nil, s.errorf("expected a digit")
	}
	return s.in[start:s.pos], nil
}

// number parses one RFC 8259 number, appends its canonical form, and — unless
// the scanner is exact — applies the binary64 admission law to it.
func (s *jsonScanner) number(out []byte) ([]byte, error) {
	start := s.pos
	neg := false
	if s.in[s.pos] == '-' {
		neg = true
		s.pos++
	}
	if s.pos >= len(s.in) {
		return nil, s.errorf("expected a digit")
	}
	var intPart []byte
	if s.in[s.pos] == '0' {
		intPart = s.in[s.pos : s.pos+1]
		s.pos++
	} else {
		var err error
		if intPart, err = s.digits(); err != nil {
			return nil, err
		}
	}
	var frac []byte
	if s.pos < len(s.in) && s.in[s.pos] == '.' {
		s.pos++
		var err error
		if frac, err = s.digits(); err != nil {
			return nil, err
		}
	}
	var exp int64
	if s.pos < len(s.in) && (s.in[s.pos] == 'e' || s.in[s.pos] == 'E') {
		s.pos++
		expNeg := false
		if s.pos < len(s.in) && (s.in[s.pos] == '+' || s.in[s.pos] == '-') {
			expNeg = s.in[s.pos] == '-'
			s.pos++
		}
		expDigits, err := s.digits()
		if err != nil {
			return nil, err
		}
		// The canonical exponent differs from the spelled one by fewer
		// places than the literal has digits, so a spelled exponent this far
		// beyond the bound can never normalize back inside it; refusing it
		// here keeps the accumulation from overflowing. The bound itself is
		// applied to the normalized value below.
		limit := maxExponentMagnitude + int64(len(s.in))
		for _, d := range expDigits {
			exp = exp*10 + int64(d-'0')
			if exp > limit {
				return nil, s.errorf("exponent out of range")
			}
		}
		if expNeg {
			exp = -exp
		}
	}
	mark := len(out)
	out, ok := appendCanonicalNumber(out, neg, intPart, frac, exp)
	if !ok {
		return nil, s.errorf("exponent out of range")
	}
	if s.exact {
		return out, nil
	}
	literal := s.in[start:s.pos]
	peer, ok := binary64Peer(literal)
	if !ok {
		return nil, s.errorAt(start, "number %s at JSON pointer %q is outside the IEEE-754 binary64 range (bdp#21: a number must round-trip through binary64)", abbreviate(literal), s.pointer())
	}
	if !bytes.Equal(peer, out[mark:]) {
		return nil, s.errorAt(start, "number %s at JSON pointer %q does not round-trip through IEEE-754 binary64: a binary64 reader serializes it as %s (bdp#21)", abbreviate(literal), s.pointer(), peer)
	}
	return out, nil
}

// appendCanonicalNumber formats the exact decimal value ±(intPart.frac)×10^exp
// with ECMAScript Number::toString digit placement. It reports false when the
// normalized value's exponent exceeds maxExponentMagnitude, which is the one
// way a syntactically valid number is refused.
func appendCanonicalNumber(out []byte, neg bool, intPart, frac []byte, exp int64) ([]byte, bool) {
	digits := make([]byte, 0, len(intPart)+len(frac))
	digits = append(digits, intPart...)
	digits = append(digits, frac...)
	exp10 := exp - int64(len(frac))
	lead := 0
	for lead < len(digits) && digits[lead] == '0' {
		lead++
	}
	digits = digits[lead:]
	if len(digits) == 0 {
		return append(out, '0'), true // every zero, -0 included, is "0"
	}
	trail := len(digits)
	for digits[trail-1] == '0' {
		trail--
		exp10++
	}
	digits = digits[:trail]
	k := int64(len(digits))
	n := exp10 + k // value = 0.digits × 10^n
	if e := n - 1; e > maxExponentMagnitude || e < -maxExponentMagnitude {
		return out, false
	}
	if neg {
		out = append(out, '-')
	}
	switch {
	case k <= n && n <= 21:
		out = append(out, digits...)
		for i := k; i < n; i++ {
			out = append(out, '0')
		}
	case 0 < n && n <= 21:
		out = append(out, digits[:n]...)
		out = append(out, '.')
		out = append(out, digits[n:]...)
	case -6 < n && n <= 0:
		out = append(out, '0', '.')
		for i := n; i < 0; i++ {
			out = append(out, '0')
		}
		out = append(out, digits...)
	default:
		out = append(out, digits[0])
		if k > 1 {
			out = append(out, '.')
			out = append(out, digits[1:]...)
		}
		out = append(out, 'e')
		e := n - 1
		if e >= 0 {
			out = append(out, '+')
		} else {
			out = append(out, '-')
			e = -e
		}
		out = strconv.AppendInt(out, e, 10)
	}
	return out, true
}

// binary64Peer is what RFC 8785's number model makes of a literal: the
// canonical form of the shortest decimal that reads back to the literal's
// nearest IEEE-754 binary64. The admission law (bdp#21) admits a literal
// exactly when this equals the literal's own canonical form. ok is false
// when the nearest binary64 is infinite — the literal is outside the range;
// a literal below the range is not an error here, its peer is "0".
//
// strconv.ParseFloat is correctly rounded (round-to-nearest-even over the
// exact decimal value, however the literal is spelled and however long it
// is), and strconv.FormatFloat with precision -1 prints the shortest digits
// that round-trip — the digits ECMAScript's Number::toString prints. The 'e'
// form is asked for so that the digits and the exponent are read back
// without guessing at Go's own placement rule; ECMAScript's placement is
// then applied by appendCanonicalNumber, the function that placed the
// literal's digits, so the two sides are compared in one normal form.
func binary64Peer(literal []byte) (peer []byte, ok bool) {
	f, err := strconv.ParseFloat(string(literal), 64)
	if err != nil {
		return nil, false // the scanner admitted the syntax, so only the range can fail
	}
	es := strconv.FormatFloat(f, 'e', -1, 64) // [-]d[.ddd]e±dd
	neg := es[0] == '-'
	if neg {
		es = es[1:]
	}
	e := strings.IndexByte(es, 'e')
	exp, _ := strconv.ParseInt(es[e+1:], 10, 64) // a sign and digits: cannot fail
	intPart, frac := []byte(es[:1]), []byte(nil)
	if e > 1 {
		frac = []byte(es[2:e]) // past "d."
	}
	peer, _ = appendCanonicalNumber(nil, neg, intPart, frac, exp) // |exp| ≤ 324: within the bound
	return peer, true
}

// admissibleInt reports whether n, spelled as a JSON integer, passes the
// binary64 admission law. An int is below 10^21, so its spelling is its own
// canonical form and the law holds exactly when its binary64 peer is that
// same spelling.
func admissibleInt(n int) bool {
	literal := strconv.AppendInt(nil, int64(n), 10)
	peer, ok := binary64Peer(literal)
	return ok && bytes.Equal(peer, literal)
}

// abbreviate renders a number literal for a diagnostic. A properties document
// may spell a number at any length, and the pointer and byte offset already
// locate it, so a long one is elided in the middle.
func abbreviate(literal []byte) string {
	const keep = 24
	if len(literal) <= 2*keep+3 {
		return string(literal)
	}
	return fmt.Sprintf("%s...%s (%d bytes)", literal[:keep], literal[len(literal)-keep:], len(literal))
}

// ---------------------------------------------------------------------------
// Scope URL and Type URL
//
// BDP, "Scope discovery": every Scope has one absolute canonical Scope URL
// ending in "/"; Scope, Resource, Type, schema and navigation members are
// HTTP(S) URLs. The reference implementation (read-values.ts
// parseCanonicalHttpUrl / parseCanonicalScope) makes "canonical" precise: an
// absolute http or https URL with no credentials and no fragment whose WHATWG
// serialization is the input itself — lowercase scheme and host, no default
// port, no dot segments, no character the parser would percent-encode —
// with every percent escape complete, and in the path uppercase and never of
// an unreserved character; a Scope URL additionally has no query, ends in
// "/", and every path segment obeys the ID-segment grammar.
//
// ONE MODEL, THREE USES. The WHATWG parser's host and port rules are stated
// once, in normalizeOrigin (P0 council, 2026-09-07): a host is percent-
// decoded and lowercased, a host that "ends in a number" is an IPv4 address
// in any of the parser's spellings (0x7f000001, 127.1, 0177.0.0.1,
// 2130706433) and serializes as dotted decimal, an IPv6 address serializes
// compressed in lowercase hex with an IPv4-mapped tail in hex pieces, and a
// port is a decimal number without leading zeros that is dropped when it is
// the scheme's default (:0443 is :443 is nothing). validateCanonicalHTTPURL
// accepts a URL exactly when its spelled authority already equals that
// serialization; NormalizeScopeURL rewrites a configured URL's authority to
// it; claimsScope classifies a reference by it, so no spelling of the Scope's
// own origin can slip past the in-Scope endpoint law as "external". An
// external reference — a different origin — is never rewritten: it is
// preserved byte-for-byte, however it is spelled.
//
// The startup contract the design mirrors (docs/design/startup-configuration.md
// at the pin, "Canonical Scope identity") NORMALIZES the spellings that mean
// the same Scope — the origin's spelling and a missing trailing slash — and
// REFUSES the rest, and it reserves the first path segment "local-test" for
// a derived development identity that is never persisted. NormalizeScopeURL
// is that admission rule; ValidateScopeURL is what any Scope URL a client may
// name must satisfy, and ValidatePersistedScopeURL is what this store may
// persist as its own identity.
//
// Scope URL diagnostics never echo the value: the startup contract's reason
// is that a token pasted into the variable by mistake would otherwise be
// copied into a log line by the parse error that rejects it.
// ---------------------------------------------------------------------------

type urlParts struct {
	scheme, host, port, path, query, fragment   string
	hasUserinfo, hasPort, hasQuery, hasFragment bool
}

func isSchemeToken(s string) bool {
	if s == "" || !(s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// splitHTTPURL splits scheme://[userinfo@]host[:port]path[?query][#fragment]
// without normalizing anything.
func splitHTTPURL(s string) (urlParts, error) {
	var p urlParts
	i := strings.IndexByte(s, ':')
	if i <= 0 || !isSchemeToken(s[:i]) {
		return p, errors.New("must be an absolute URL")
	}
	p.scheme = s[:i]
	rest := s[i+1:]
	if !strings.HasPrefix(rest, "//") {
		return p, errors.New("must have an authority")
	}
	rest = rest[2:]
	authority := rest
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority, rest = rest[:end], rest[end:]
	} else {
		rest = ""
	}
	if j := strings.IndexByte(rest, '#'); j >= 0 {
		p.hasFragment, p.fragment, rest = true, rest[j+1:], rest[:j]
	}
	if j := strings.IndexByte(rest, '?'); j >= 0 {
		p.hasQuery, p.query, rest = true, rest[j+1:], rest[:j]
	}
	p.path = rest
	if j := strings.LastIndexByte(authority, '@'); j >= 0 {
		p.hasUserinfo, authority = true, authority[j+1:]
	}
	if strings.HasPrefix(authority, "[") {
		j := strings.IndexByte(authority, ']')
		if j < 0 {
			return p, errors.New("has an unterminated IPv6 host")
		}
		p.host = authority[:j+1]
		if tail := authority[j+1:]; tail != "" {
			if tail[0] != ':' {
				return p, errors.New("has a malformed authority")
			}
			p.hasPort, p.port = true, tail[1:]
		}
		return p, nil
	}
	if j := strings.LastIndexByte(authority, ':'); j >= 0 {
		p.host, p.hasPort, p.port = authority[:j], true, authority[j+1:]
		return p, nil
	}
	p.host = authority
	return p, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// origin is the normalized (scheme, host, port) of an http(s) URL: the three
// components the WHATWG parser serializes canonically and the tuple RFC 6454
// calls the origin. Two URLs with equal origins and one path prefix name the
// same Scope whatever their spelling; a canonical URL is one whose spelled
// authority already equals its origin.
type origin struct{ scheme, host, port string }

// normalizeOrigin applies the parser's rules to a split URL: the scheme is
// lowercased and must be http or https, the host is normalizeHost's
// serialization, and the port is normalizePort's.
func normalizeOrigin(p urlParts) (origin, error) {
	scheme := strings.ToLower(p.scheme)
	if scheme != "http" && scheme != "https" {
		return origin{}, errors.New("must use the http or https scheme")
	}
	host, err := normalizeHost(p.host)
	if err != nil {
		return origin{}, err
	}
	port, err := normalizePort(p.port, scheme)
	if err != nil {
		return origin{}, err
	}
	return origin{scheme: scheme, host: host, port: port}, nil
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// normalizePort is the WHATWG port rule: an absent or empty port is no port,
// a port is decimal digits whose leading zeros carry nothing (:0443 is :443),
// it may not exceed 65535, and the scheme's default port is no port at all.
func normalizePort(port, scheme string) (string, error) {
	if port == "" {
		return "", nil
	}
	if !allDigits(port) {
		return "", errors.New("port must be a decimal number")
	}
	trimmed := strings.TrimLeft(port, "0")
	if trimmed == "" {
		trimmed = "0"
	}
	if len(trimmed) > 5 || (len(trimmed) == 5 && trimmed > "65535") {
		return "", errors.New("port must not exceed 65535")
	}
	if trimmed == defaultPort(scheme) {
		return "", nil
	}
	return trimmed, nil
}

// isForbiddenDomainByte is the WHATWG "forbidden domain code point" set:
// the forbidden host code points (NUL, tab, LF, CR, space, "#", "/", ":",
// "<", ">", "?", "@", "[", "\", "]", "^", "|") plus every C0 control, "%"
// and DEL. A host that contains one after percent-decoding is not a host.
func isForbiddenDomainByte(c byte) bool {
	if c <= 0x20 || c == 0x7F {
		return true
	}
	switch c {
	case '#', '%', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|':
		return true
	}
	return false
}

// normalizeHost is the WHATWG host parser, restricted to what a canonical
// Scope or Type host may be, returning the host's serialization:
//
//   - a bracketed IPv6 address serializes as the WHATWG IPv6 serializer
//     writes it — lowercase hex, the first longest run of two or more zero
//     pieces compressed, an IPv4-mapped address in hex pieces
//     ([::ffff:102:304], never [::ffff:1.2.3.4]); a zone is refused;
//   - any other host is percent-decoded (a malformed escape is refused), must
//     decode to ASCII with no forbidden domain code point, and is lowercased;
//   - a host whose last label is all digits or a 0x-prefixed hex number "ends
//     in a number" and is parsed as IPv4 — at most four parts, each decimal,
//     octal (leading 0) or hexadecimal (0x), the last filling the remaining
//     bytes — and serializes as dotted decimal: 0x7f000001, 127.1,
//     0177.0.0.1, 2130706433 and 127.0.0.1. are all 127.0.0.1, and beads.123
//     is not a host at all;
//   - otherwise it is a registered name: labels of lowercase letters, digits,
//     "-" and "_" (DNS LDH plus the underscore the tree already accepts), no
//     label empty — except that ONE trailing dot is kept as written, because
//     the parser leaves beads.example. unchanged and treats it as a host
//     distinct from beads.example, and so does this law.
//
// DECISION: IDNA is not applied. A host that decodes to non-ASCII is refused
// rather than mapped, so an internationalized Scope host is configured in its
// A-label (xn--) form, and a U-label spelling of it is classified as an
// external reference rather than resolved. The startup contract already
// refuses non-ASCII input; this closes the percent-encoded route the same way.
func normalizeHost(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("host must be nonempty")
	}
	if raw[0] == '[' {
		// splitHTTPURL only produces a bracketed host with its closing
		// bracket, so the slice below is well-formed by construction.
		return normalizeIPv6Host(raw[1 : len(raw)-1])
	}
	decoded, err := decodeSegment(raw)
	if err != nil {
		return "", fmt.Errorf("host has an %v", err)
	}
	host := make([]byte, 0, len(decoded))
	for _, c := range decoded {
		if c >= 0x80 {
			return "", errors.New("host must be ASCII (IDNA mapping is not applied)")
		}
		if isForbiddenDomainByte(c) {
			return "", errors.New("host contains a character the URL parser forbids")
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		host = append(host, c)
	}
	s := string(host)
	if hostEndsInANumber(s) {
		return parseIPv4Host(s)
	}
	labels := strings.Split(s, ".")
	if n := len(labels); n > 1 && labels[n-1] == "" {
		labels = labels[:n-1] // the one trailing dot: kept in s, not a label
	}
	for _, label := range labels {
		if label == "" {
			return "", errors.New("host must not contain an empty label")
		}
		for i := 0; i < len(label); i++ {
			if c := label[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", errors.New("host must be a lowercase registered name or a canonical IP address")
			}
		}
	}
	return s, nil
}

// hostEndsInANumber is the WHATWG "ends in a number" checker over a decoded,
// lowercased, nonempty host: a trailing empty label is dropped first, and
// the last label is a number when it is all digits or parses as a
// 0x-prefixed IPv4 number.
func hostEndsInANumber(host string) bool {
	parts := strings.Split(host, ".")
	if n := len(parts); n > 1 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	last := parts[len(parts)-1]
	if allDigits(last) {
		return true
	}
	_, ok := parseIPv4Number(last)
	return ok
}

// parseIPv4Number is the WHATWG IPv4 number parser: decimal, octal with a
// leading 0, or hexadecimal with 0x/0X, where an empty digit string after
// the prefix is 0. The value is capped well above the largest meaningful
// part so a long input cannot overflow.
func parseIPv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	radix := uint64(10)
	switch {
	case len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X'):
		s, radix = s[2:], 16
	case len(s) >= 2 && s[0] == '0':
		s, radix = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		d, ok := hexValue(s[i])
		if !ok || uint64(d) >= radix {
			return 0, false
		}
		n = n*radix + uint64(d)
		if n > 1<<40 {
			return 0, false
		}
	}
	return n, true
}

// ipv4LastPartLimit bounds the last part of an IPv4 host with 1, 2, 3 or 4
// parts: it fills the remaining 4, 3, 2 or 1 bytes.
var ipv4LastPartLimit = [...]uint64{1 << 32, 1 << 24, 1 << 16, 1 << 8}

// parseIPv4Host is the WHATWG IPv4 parser over a host that ends in a number,
// returning the dotted-decimal serialization.
func parseIPv4Host(host string) (string, error) {
	parts := strings.Split(host, ".")
	if n := len(parts); n > 1 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	if len(parts) > 4 {
		return "", errors.New("a numeric host must be an IPv4 address of at most four parts")
	}
	numbers := make([]uint64, len(parts))
	for i, part := range parts {
		n, ok := parseIPv4Number(part)
		if !ok {
			return "", errors.New("a numeric host must be an IPv4 address")
		}
		numbers[i] = n
	}
	for _, n := range numbers[:len(numbers)-1] {
		if n > 255 {
			return "", errors.New("an IPv4 part must not exceed 255")
		}
	}
	last := numbers[len(numbers)-1]
	if last >= ipv4LastPartLimit[len(numbers)-1] {
		return "", errors.New("the last IPv4 part is out of range")
	}
	ipv4 := last
	for i, n := range numbers[:len(numbers)-1] {
		ipv4 += n << (8 * (3 - i))
	}
	return fmt.Sprintf("%d.%d.%d.%d", ipv4>>24&0xFF, ipv4>>16&0xFF, ipv4>>8&0xFF, ipv4&0xFF), nil
}

// normalizeIPv6Host parses the text inside the brackets and returns the
// bracketed WHATWG serialization.
func normalizeIPv6Host(inner string) (string, error) {
	addr, err := netip.ParseAddr(inner)
	if err != nil || !addr.Is6() || addr.Zone() != "" {
		return "", errors.New("IPv6 host must be a bracketed address without a zone")
	}
	return "[" + serializeIPv6(addr) + "]", nil
}

// serializeIPv6 is the WHATWG IPv6 serializer: eight 16-bit pieces in
// lowercase hex without leading zeros, the first longest run of two or more
// zero pieces written as "::", and no dotted-decimal tail for an IPv4-mapped
// address (the parser accepts that spelling; the serializer never emits it).
func serializeIPv6(addr netip.Addr) string {
	b := addr.As16()
	var pieces [8]uint16
	for i := range pieces {
		pieces[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	compress, longest := -1, 1
	for i := 0; i < len(pieces); {
		if pieces[i] != 0 {
			i++
			continue
		}
		j := i
		for j < len(pieces) && pieces[j] == 0 {
			j++
		}
		if j-i > longest {
			compress, longest = i, j-i
		}
		i = j
	}
	var out strings.Builder
	ignoreZeros := false
	for i, piece := range pieces {
		if ignoreZeros && piece == 0 {
			continue
		}
		ignoreZeros = false
		if i == compress {
			if i == 0 {
				out.WriteString("::")
			} else {
				out.WriteByte(':')
			}
			ignoreZeros = true
			continue
		}
		out.WriteString(strconv.FormatUint(uint64(piece), 16))
		if i != len(pieces)-1 {
			out.WriteByte(':')
		}
	}
	return out.String()
}

// completeEscapes reports whether every '%' begins a two-hex-digit escape.
func completeEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) {
			return false
		}
		_, ok1 := hexValue(s[i+1])
		_, ok2 := hexValue(s[i+2])
		if !ok1 || !ok2 {
			return false
		}
		i += 2
	}
	return true
}

// canonicalPathEscapes checks a path segment's escapes (already complete):
// uppercase hex, and never an unreserved character.
func canonicalPathEscapes(segment string) error {
	for i := 0; i < len(segment); i++ {
		if segment[i] != '%' {
			continue
		}
		hi, lo := segment[i+1], segment[i+2]
		if hi >= 'a' && hi <= 'f' || lo >= 'a' && lo <= 'f' {
			return errors.New("path must use uppercase percent escapes")
		}
		h, _ := hexValue(hi)
		l, _ := hexValue(lo)
		if isUnreservedByte(h<<4 | l) {
			return errors.New("path must not percent-encode an unreserved character")
		}
		i += 2
	}
	return nil
}

// validateCanonicalHTTPURL is the shared half of the Scope and Type URL laws:
// the URL's spelled authority must already be its normalized origin, and its
// path must be one the parser would leave alone. echo says whether
// diagnostics may quote the value.
func validateCanonicalHTTPURL(s, what string, echo bool) (urlParts, error) {
	fail := func(msg string) (urlParts, error) {
		if echo {
			return urlParts{}, fmt.Errorf("%w: %s %q %s", ErrValidation, what, s, msg)
		}
		return urlParts{}, fmt.Errorf("%w: %s %s", ErrValidation, what, msg)
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7F || c == '\\' {
			return fail("contains a character that is not a canonical URL character")
		}
	}
	p, err := splitHTTPURL(s)
	if err != nil {
		return fail(err.Error())
	}
	if p.scheme != "http" && p.scheme != "https" {
		return fail("must use the http or https scheme, lowercase")
	}
	if p.hasUserinfo {
		return fail("must not carry credentials")
	}
	if p.hasFragment {
		return fail("must not carry a fragment")
	}
	o, err := normalizeOrigin(p)
	if err != nil {
		return fail(err.Error())
	}
	if o.host != p.host {
		return fail("host must be spelled as the URL parser serializes it (lowercase; IPv4 in dotted decimal; IPv6 compressed in lowercase hex)")
	}
	if p.hasPort {
		switch {
		case p.port == "":
			return fail("an empty port must be omitted")
		case o.port == "":
			return fail("a default port must be omitted")
		case o.port != p.port:
			return fail("port must be a decimal number without leading zeros")
		}
	}
	if p.path == "" {
		return fail("must have a path beginning with /")
	}
	if !completeEscapes(s) {
		return fail("must use complete percent escapes")
	}
	for _, segment := range strings.Split(p.path[1:], "/") {
		if segment == "." || segment == ".." {
			return fail("must not contain dot segments")
		}
		if strings.ContainsAny(segment, "\"<>`{}") {
			return fail("path contains a character the URL parser would encode")
		}
		if err := canonicalPathEscapes(segment); err != nil {
			return fail(err.Error())
		}
	}
	if p.hasQuery && strings.ContainsAny(p.query, "\"<>'") {
		return fail("query contains a character the URL parser would encode")
	}
	return p, nil
}

// ValidateTypeURL accepts exactly the canonical, credential-free HTTP(S) URLs
// a Type ID, a propertiesSchema or a discovery member may be. A query is
// permitted; a fragment is not.
func ValidateTypeURL(s string) error {
	if s == WildcardOwnedLinkKey {
		// DECISION: refused by name, not by accident of the URL grammar.
		// "*" is an ownsOutgoing KEY (bdp#1 item 5), never a Type — not a
		// Link's type, not a descriptor's or an endpoint's conformsTo
		// entry, not an id or a propertiesSchema — and the diagnostic
		// says which of the two the author reached for.
		return fmt.Errorf("%w: %q is the wildcard owned-Link key, not a Type URL", ErrValidation, s)
	}
	_, err := validateCanonicalHTTPURL(s, "URL", true)
	return err
}

// ValidateScopeURL accepts exactly the canonical Scope URLs: a canonical
// HTTP(S) URL with no query, ending in "/", every path segment under the
// ID-segment grammar. It is the rule for any Scope URL a CLIENT may name —
// ParseRef classifies references against it — and it admits a development
// server's reserved "…/local-test/" Scope, because a client must be able to
// reference one. What this store may persist as its own identity is the
// narrower ValidatePersistedScopeURL.
func ValidateScopeURL(s string) error {
	p, err := validateCanonicalHTTPURL(s, "Scope URL", false)
	if err != nil {
		return err
	}
	if p.hasQuery {
		return fmt.Errorf("%w: Scope URL must not carry a query", ErrValidation)
	}
	if !strings.HasSuffix(p.path, "/") {
		return fmt.Errorf("%w: Scope URL must end in /", ErrValidation)
	}
	segments := strings.Split(p.path, "/")
	for _, segment := range segments[1 : len(segments)-1] {
		if err := ValidateCanonicalSegment(segment); err != nil {
			return fmt.Errorf("%w: Scope URL path: %s", ErrValidation, reason(err))
		}
	}
	return nil
}

// localTestSegment is the first path segment the startup contract reserves
// for a derived development identity.
const localTestSegment = "local-test"

// ValidatePersistedScopeURL accepts exactly the Scope URLs this store may
// PERSIST as its own identity — mint under, rotate to, record in the ledger:
// ValidateScopeURL plus the startup contract's reservation of the first path
// segment "local-test" for a derived development identity that is never
// persisted. This tree has no development mode (engdocs/
// BDP_GRAPH_ARCHITECTURE.md §6, "no dev-mode derivation"), so a persisted
// Scope URL never carries the segment; a client referencing another server's
// local-test Scope goes through ValidateScopeURL and is not affected.
//
// DECISION (P0 council): the refusal lives here, at persisted-identity
// admission, and not in the general law, so that a bdptest development
// server's "…/local-test/" Scope stays referenceable.
func ValidatePersistedScopeURL(s string) error {
	if err := ValidateScopeURL(s); err != nil {
		return err
	}
	p, _ := splitHTTPURL(s)
	if strings.HasPrefix(p.path, "/"+localTestSegment+"/") {
		return fmt.Errorf("%w: a persisted Scope URL path must not begin with the reserved local-test segment", ErrValidation)
	}
	return nil
}

// NormalizeScopeURL is the startup contract's admission rule for a configured
// Scope URL: the authority is rewritten to its normalized origin — scheme
// and host case, the host's IPv4/IPv6 spelling and percent-encoding, a
// default port, a port's leading zeros — a missing trailing slash is added,
// and the result must then satisfy ValidatePersistedScopeURL, so
// credentials, a query, a fragment, a path spelling the URL parser would
// rewrite, and the reserved local-test segment are refused rather than
// repaired. The returned string is the persisted identity.
//
// DECISION: the origin is normalized in full rather than only in case. The
// contract names case, the default port and the slash; the parser makes
// :0443, 0x7f000001 and [::ffff:1.2.3.4] the same origin as :443,
// 127.0.0.1 and [::ffff:102:304], and persisting the spelled form would
// leave the persisted identity unequal to the origin every reference is
// classified against.
func NormalizeScopeURL(s string) (string, error) {
	p, err := splitHTTPURL(s)
	if err != nil {
		return "", fmt.Errorf("%w: Scope URL %s", ErrValidation, err)
	}
	if p.hasUserinfo {
		return "", fmt.Errorf("%w: Scope URL must not carry credentials", ErrValidation)
	}
	o, err := normalizeOrigin(p)
	if err != nil {
		return "", fmt.Errorf("%w: Scope URL %s", ErrValidation, err)
	}
	path := p.path
	if path == "" {
		path = "/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	var rebuilt strings.Builder
	rebuilt.WriteString(o.scheme)
	rebuilt.WriteString("://")
	rebuilt.WriteString(o.host)
	if o.port != "" {
		rebuilt.WriteString(":")
		rebuilt.WriteString(o.port)
	}
	rebuilt.WriteString(path)
	if p.hasQuery {
		rebuilt.WriteString("?")
		rebuilt.WriteString(p.query)
	}
	if p.hasFragment {
		rebuilt.WriteString("#")
		rebuilt.WriteString(p.fragment)
	}
	out := rebuilt.String()
	if err := ValidatePersistedScopeURL(out); err != nil {
		return "", err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reference classification helpers (the law itself is ParseRef in types.go).
// ---------------------------------------------------------------------------

func isSubDelimByte(c byte) bool {
	switch c {
	case '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=':
		return true
	}
	return false
}

// isAbsoluteURI is the schema bundle's absoluteUri: a scheme token followed
// by ':' (pattern ^[A-Za-z][A-Za-z0-9+.-]*:), spelled with the RFC 3986
// character discipline — unreserved, reserved, and complete percent escapes,
// nothing else. A full syntactic parse is not attempted: an external
// reference is opaque and never dereferenced.
func isAbsoluteURI(s string) bool {
	i := strings.IndexByte(s, ':')
	if i <= 0 || !isSchemeToken(s[:i]) {
		return false
	}
	for j := 0; j < len(s); j++ {
		c := s[j]
		switch {
		case isUnreservedByte(c), isSubDelimByte(c):
		case c == ':' || c == '/' || c == '?' || c == '#' || c == '[' || c == ']' || c == '@':
		case c == '%':
			if j+2 >= len(s) {
				return false
			}
			_, ok1 := hexValue(s[j+1])
			_, ok2 := hexValue(s[j+2])
			if !ok1 || !ok2 {
				return false
			}
			j += 2
		default:
			return false
		}
	}
	return true
}

// normalizeEscapes decodes escapes of unreserved characters and uppercases
// the rest (RFC 3986 §6.2.2.2); malformed escapes are left as they are.
func normalizeEscapes(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c != '%' || i+2 >= len(path) {
			b.WriteByte(c)
			continue
		}
		hi, ok1 := hexValue(path[i+1])
		lo, ok2 := hexValue(path[i+2])
		if !ok1 || !ok2 {
			b.WriteByte(c)
			continue
		}
		if v := hi<<4 | lo; isUnreservedByte(v) {
			b.WriteByte(v)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHexDigits[hi])
			b.WriteByte(upperHexDigits[lo])
		}
		i += 2
	}
	return b.String()
}

// removeDotSegments is RFC 3986 §5.2.4 over an absolute path.
func removeDotSegments(path string) string {
	segments := strings.Split(path, "/")
	out := make([]string, 0, len(segments))
	for i, segment := range segments {
		last := i == len(segments)-1
		switch segment {
		case ".":
			if last {
				out = append(out, "")
			}
		case "..":
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, segment)
		}
	}
	return strings.Join(out, "/")
}

// claimsScope reports whether reference resolves under the canonical Scope
// URL — the test that decides whether a reference CLAIMS an in-Scope Bead
// (and must therefore be that Bead's canonical spelling) or is external. The
// origins are compared normalized (normalizeOrigin: scheme and host case,
// every IPv4 and IPv6 spelling, percent-encoded host characters, a default
// or zero-padded port), and the path after RFC 3986 §6.2.2 escape
// normalization and dot-segment removal. A reference whose authority the
// parser cannot make sense of claims nothing and is external. scopeURL is
// assumed valid.
func claimsScope(scopeURL, reference string) bool {
	p, err := splitHTTPURL(reference)
	if err != nil {
		return false
	}
	ref, err := normalizeOrigin(p)
	if err != nil {
		return false
	}
	sp, _ := splitHTTPURL(scopeURL)
	scope, _ := normalizeOrigin(sp) // scopeURL is valid: its origin is its spelling
	if ref != scope {
		return false
	}
	path := p.path
	if path == "" {
		path = "/"
	}
	return strings.HasPrefix(removeDotSegments(normalizeEscapes(path)), sp.path)
}

// ---------------------------------------------------------------------------
// Ledger hashing
//
// engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md B2/B4: the ledger is append-only
// and hash-chained; hash = sha256(canonical(event without hash)). This is the
// exact byte layout, FROZEN by the P1 migration that stores it:
//
//	canonical(event) = CanonicalizeJSON of a JSON object with these members
//	and no others, absent optional members OMITTED, keys therefore in this
//	(code-unit) order:
//
//	  "at"            string  RFC 3339 UTC with exactly six fractional digits
//	                          and the "Z" designator: 2006-01-02T15:04:05.000000Z
//	                          (the DATETIME(6) column's precision; the value is
//	                          truncated, not rounded, to the microsecond)
//	  "authority_id"  string  32 lowercase hex digits
//	  "epoch"         number  the authority epoch, as a JSON integer
//	  "fingerprint"   string  64 lowercase hex digits         (optional)
//	  "kind"          string  the event kind
//	  "op_id"         string  32 lowercase hex digits
//	  "path"          string  canonical Scope-relative path    (optional)
//	  "prev_hash"     string  64 lowercase hex digits; GenesisHash for the
//	                          first event of a Scope's history
//	  "resource_kind" string  "bead" | "link"                  (optional)
//	  "revision"      string  the revision token               (optional)
//	  "scope_url"     string  canonical Scope URL              (optional)
//	  "seq"           number  the sequence number, as a JSON integer
//	  "state"         string  "pruned" | "erased"              (optional)
//
//	hash = lowercase hex SHA-256 of those bytes; the next event's prev_hash is
//	this hash. The framing is canonicalized by canonicalizeJSONExact, NOT by
//	CanonicalizeJSON: seq and epoch are Go uint64 values, spelled as exact
//	JSON integers and hashed as such over the full uint64 range, and they
//	are not subject to the binary64 admission law (bdp#21, 2026-09-08),
//	which governs user-supplied JSON entering the domain. Every string
//	member is VALID UTF-8:
//	the hex, path, URL and enum members by their own grammars, and the
//	opaque revision by NewRevision's refusal of invalid bytes — encoding/json
//	would otherwise launder an invalid byte to U+FFFD, and two events that
//	differ only there would share one canonical form and one hash.
//
// DECISION: the layout above — member names as the B4 columns spell them,
// JCS for the framing, microsecond UTC for the instant, all-zero genesis,
// and the UTF-8 requirement on every hashed string (P0 council). B2 gives
// the formula and the member list; the framing and the formats are this
// file's choice and are pinned by a golden test.
//
// DECISION (bdp#21 fold, 2026-09-08): the layout is UNCHANGED by the
// admission law; the exemption above is stated rather than the layout
// re-cut. The framing does spell seq and epoch as JSON number literals, so a
// JCS peer that models numbers as binary64 would read 18446744073709551615
// as 18446744073709552000 and could not reproduce the hash of an event whose
// seq or epoch is past 2^53 — the interop caveat B4 already records. The
// ledger is internal (this package alone computes and verifies its hashes),
// the layout is frozen by the P1 migration that stores it, and the ruling's
// law is about values a peer is asked to hold. The golden test hashes epoch
// 2^64-1 and pins that CanonicalizeJSON refuses those same bytes.
// ---------------------------------------------------------------------------

// GenesisHash is the prev_hash of the first event of a Scope's history: no
// event exists before mint, and the chain starts from sixty-four zeros.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

const ledgerAtLayout = "2006-01-02T15:04:05.000000Z07:00"

// ledgerWire is the hashed shape; fields are declared in key order so the
// encoder emits them sorted, and CanonicalizeJSON afterwards makes the
// framing canonical whatever the encoder did.
type ledgerWire struct {
	At           string `json:"at"`
	AuthorityID  string `json:"authority_id"`
	Epoch        uint64 `json:"epoch"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	Kind         string `json:"kind"`
	OpID         string `json:"op_id"`
	Path         string `json:"path,omitempty"`
	PrevHash     string `json:"prev_hash"`
	ResourceKind string `json:"resource_kind,omitempty"`
	Revision     string `json:"revision,omitempty"`
	ScopeURL     string `json:"scope_url,omitempty"`
	Seq          uint64 `json:"seq"`
	State        string `json:"state,omitempty"`
}

// ledgerEventCanonicalBytes renders the hashed bytes of a validated spec.
func ledgerEventCanonicalBytes(spec LedgerEventSpec) []byte {
	w := ledgerWire{
		At:           spec.At.UTC().Truncate(time.Microsecond).Format(ledgerAtLayout),
		AuthorityID:  spec.AuthorityID,
		Epoch:        spec.Epoch,
		Fingerprint:  spec.Fingerprint,
		Kind:         string(spec.Kind),
		OpID:         spec.OpID,
		Path:         spec.Path,
		PrevHash:     spec.PrevHash,
		ResourceKind: string(spec.ResourceKind),
		Revision:     spec.Revision.String(),
		ScopeURL:     spec.ScopeURL,
		Seq:          spec.Seq,
		State:        spec.State,
	}
	raw, _ := json.Marshal(w)                  // strings and integers: cannot fail
	canonical, _ := canonicalizeJSONExact(raw) // one valid value, integers exact: see the DECISION above
	return canonical
}

// VerifyLedgerChain checks that events is one contiguous, correctly linked run
// of the ledger continuing from prevHash: each event's prev_hash is the
// preceding hash, sequence numbers lie in [1, MaxLedgerSeq] and ascend by
// exactly one, and every hash was computed by NewLedgerEvent (a zero event
// never links). A gap, a fork, a reordering, or a sequence that wraps around
// (MaxUint64 followed by 0) is ErrValidation naming the first offending
// sequence number.
func VerifyLedgerChain(prevHash string, events []LedgerEvent) error {
	if !isLowerHex(prevHash, 64) {
		return fmt.Errorf("%w: ledger chain must continue from a 64-hex-digit hash", ErrValidation)
	}
	prev := prevHash
	for i, e := range events {
		if e.PrevHash() != prev {
			return fmt.Errorf("%w: ledger event seq %d does not link to the preceding hash", ErrValidation, e.Seq())
		}
		if e.Seq() == 0 || e.Seq() > MaxLedgerSeq {
			return fmt.Errorf("%w: ledger event seq %d is outside 1..MaxLedgerSeq", ErrValidation, e.Seq())
		}
		// Compared without addition, so no sequence number can wrap.
		if i > 0 && (e.Seq() <= events[i-1].Seq() || e.Seq()-events[i-1].Seq() != 1) {
			return fmt.Errorf("%w: ledger gap: seq %d follows seq %d", ErrValidation, e.Seq(), events[i-1].Seq())
		}
		prev = e.Hash()
	}
	return nil
}
