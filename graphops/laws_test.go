package graphops_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/graphops"
)

// Every law in laws.go is table-tested here. A table names the sentence it
// pins where the sentence is not obvious from the vector.

func wantValidation(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted, want ErrValidation", what)
	}
	if !errors.Is(err, graphops.ErrValidation) {
		t.Fatalf("%s: error %v does not wrap ErrValidation", what, err)
	}
}

// --- canonical-ID grammar -------------------------------------------------

func TestValidateCanonicalSegment(t *testing.T) {
	accept := []string{
		"task-42", "Task-42", "a.b_c~d", "!$&'()*+,;=:@", "0", "-",
		"caf%C3%A9",      // non-ASCII is percent-encoded, uppercase
		"a%20b",          // space
		"a%25b",          // a literal percent sign
		"%E2%9C%93",      // U+2713
		"x%3Fy", "x%23y", // "?" and "#" are legal once encoded
		"%7B%7D", "a%5Bb%5D", "a%7Cb", // {} [] | are outside the literal set
		"%C2%80", // U+0080 is not an ASCII control
	}
	for _, seg := range accept {
		if err := graphops.ValidateCanonicalSegment(seg); err != nil {
			t.Errorf("segment %q: refused: %v", seg, err)
		}
	}
	reject := []struct{ seg, why string }{
		{"", "empty"},
		{".", "dot segment"},
		{"..", "dot-dot segment"},
		{"%2E", "encoded dot segment"},
		{"%2E%2E", "encoded dot-dot segment"},
		{"a%2Fb", "encoded separator /"},
		{"a%5Cb", "encoded separator \\"},
		{"a/b", "literal separator"},
		{"a\\b", "literal backslash"},
		{"a%00b", "NUL"},
		{"a%1Fb", "C0 control"},
		{"a%7Fb", "DEL"},
		{"a b", "literal space"},
		{"a?b", "literal ? must be encoded"},
		{"a#b", "literal # must be encoded"},
		{"café", "literal non-ASCII must be encoded"},
		{"caf%c3%a9", "lowercase hex"},
		{"%41", "encoded unreserved character"},
		{"a%2Bb", "encoded literal-set character"},
		{"a%2", "incomplete escape"},
		{"a%zz", "malformed escape"},
		{"%", "bare percent"},
		{"%%41", "bare percent before an escape"},
		{"%C3", "truncated UTF-8"},
		{"%ED%A0%80", "UTF-8-encoded surrogate"},
		{"%C0%80", "overlong UTF-8"},
		{"%FF", "invalid UTF-8"},
		{"\x00", "raw control"},
	}
	for _, tc := range reject {
		wantValidation(t, graphops.ValidateCanonicalSegment(tc.seg), "segment "+tc.why+" "+tc.seg)
	}
}

func TestValidatePathGrammar(t *testing.T) {
	beads := []string{
		"beads/task-42", "beads/projects/alpha/tasks/task-42", "beads/Task-42",
		"beads/caf%C3%A9", "beads/beads", "beads/links/x", "beads/alias/x",
	}
	for _, p := range beads {
		if err := graphops.ValidateBeadPath(p); err != nil {
			t.Errorf("bead path %q refused: %v", p, err)
		}
		if err := graphops.ValidatePath(p, graphops.KindBead); err != nil {
			t.Errorf("ValidatePath(%q, bead) refused: %v", p, err)
		}
		wantValidation(t, graphops.ValidateLinkPath(p), "bead path as link "+p)
	}
	rejectBead := []string{
		"", "beads", "beads/", "/beads/x", "beads//x", "beads/x/", "Beads/x", "BEADS/x",
		"links/x", "alias/x", "beads/.", "beads/..", "beads/x?y", "beads/x#y",
		"beads/x/../y", "bead/x", "beadsx/y", " beads/x", "beads/x ", "beads\\x",
		"https://beads.example/acme/beads/x",
	}
	for _, p := range rejectBead {
		wantValidation(t, graphops.ValidateBeadPath(p), "bead path "+p)
		wantValidation(t, graphops.ValidatePath(p, graphops.KindBead), "ValidatePath bead "+p)
	}
	if err := graphops.ValidateLinkPath("links/assigned-to/81"); err != nil {
		t.Errorf("link path refused: %v", err)
	}
	if err := graphops.ValidatePath("links/assigned-to/81", graphops.KindLink); err != nil {
		t.Errorf("ValidatePath link refused: %v", err)
	}
	wantValidation(t, graphops.ValidateLinkPath("beads/x"), "link path under beads/")
	if err := graphops.ValidateAliasPath("alias/latest"); err != nil {
		t.Errorf("alias path refused: %v", err)
	}
	wantValidation(t, graphops.ValidateAliasPath("beads/x"), "alias path under beads/")
	wantValidation(t, graphops.ValidatePath("beads/x", graphops.ResourceKind("type")), "ValidatePath unknown kind")
}

// Case-differing paths are DISTINCT identities: both are canonical, neither
// is the other, and they order by code unit (uppercase first).
func TestCaseDifferingPathsAreDistinct(t *testing.T) {
	upper, lower := "beads/Task-42", "beads/task-42"
	for _, p := range []string{upper, lower} {
		if err := graphops.ValidateBeadPath(p); err != nil {
			t.Fatalf("%q refused: %v", p, err)
		}
	}
	if upper == lower || graphops.CompareCodeUnits(upper, lower) != -1 {
		t.Fatalf("case-differing paths must be distinct and code-unit ordered")
	}
}

func TestCanonicalSegmentEncoder(t *testing.T) {
	for _, tc := range []struct{ decoded, want string }{
		{"task-42", "task-42"},
		{"café", "caf%C3%A9"},
		{"a b", "a%20b"},
		{"a/b", "a%2Fb"},
		{"a+b", "a+b"},
		{"100%", "100%25"},
		{"[x]", "%5Bx%5D"},
		{"✓", "%E2%9C%93"},
		{"", ""},
	} {
		if got := graphops.CanonicalSegment(tc.decoded); got != tc.want {
			t.Errorf("CanonicalSegment(%q) = %q, want %q", tc.decoded, got, tc.want)
		}
	}
}

func TestCanonicalURLRoundTrip(t *testing.T) {
	scope := "https://beads.example/acme/"
	if got := graphops.CanonicalURL(scope, "beads/task-42"); got != "https://beads.example/acme/beads/task-42" {
		t.Fatalf("CanonicalURL = %q", got)
	}
	for _, tc := range []struct {
		url  string
		path string
		kind graphops.ResourceKind
		ok   bool
	}{
		{"https://beads.example/acme/beads/task-42", "beads/task-42", graphops.KindBead, true},
		{"https://beads.example/acme/links/l/1", "links/l/1", graphops.KindLink, true},
		{"https://beads.example/acme/alias/x", "", "", false},
		{"https://beads.example/acme/beads/", "", "", false},
		{"https://beads.example/acme/", "", "", false},
		{"https://beads.example/other/beads/x", "", "", false},
		{"https://beads.example/acme/beads/x?y", "", "", false},
		{"https://beads.example/acme/beads/caf%c3%a9", "", "", false},
		{"", "", "", false},
	} {
		path, kind, ok := graphops.SplitCanonicalURL(scope, tc.url)
		if path != tc.path || kind != tc.kind || ok != tc.ok {
			t.Errorf("SplitCanonicalURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.url, path, kind, ok, tc.path, tc.kind, tc.ok)
		}
	}
}

// --- code-unit ordering ---------------------------------------------------

func TestCompareCodeUnits(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"a", "b", -1}, {"b", "a", 1}, {"a", "a", 0}, {"a", "ab", -1}, {"ab", "a", 1},
		{"", "", 0}, {"", "a", -1}, {"a", "", 1},
		{"beads/Task", "beads/task", -1},
		{"beads/task-10", "beads/task-9", -1}, // lexicographic, not numeric
		{"z", "\u00e9", -1}, {"\u00e9", "\u20ac", -1},
		{"\U0001F600", "\U0001F601", -1}, {"\U0001F600", "\U0001F600", 0},
		// The cases where code units and bytes disagree: a supplementary
		// character's lead surrogate (D800–DBFF) sorts BELOW U+E000–U+FFFF.
		{"\uFFFF", "\U0001F600", 1},
		{"\uE000", "\U00010000", 1},
		{"\uD7FF", "\U00010000", -1},
		{"x\uFFFF", "x\U0001F600", 1},
	} {
		if got := graphops.CompareCodeUnits(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareCodeUnits(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	// And the divergence from byte order, stated explicitly.
	if bytes.Compare([]byte("\uFFFF"), []byte("\U0001F600")) != -1 {
		t.Fatal("test premise: UTF-8 bytes order U+FFFF before U+1F600")
	}
	if graphops.CompareCodeUnits("\uFFFF", "\U0001F600") != 1 {
		t.Fatal("code units order U+1F600 before U+FFFF")
	}
}

// --- JSON canonicalization ------------------------------------------------

// jcsNumber is RFC 8785 §3.2.2.3 — ECMAScript Number::toString — over a
// binary64, written here independently of the package: Go's shortest
// round-trip digits placed by the ECMAScript rule. The package never formats
// a double into its output, so agreement between the two is the claim the
// bdp#21 ruling makes: on every admitted value the exact-decimal model and
// the binary64 model serialize identically.
func jcsNumber(f float64) string {
	if f == 0 {
		return "0" // both zeros
	}
	es := strconv.FormatFloat(f, 'e', -1, 64) // [-]d[.ddd]e±dd
	neg := strings.HasPrefix(es, "-")
	if neg {
		es = es[1:]
	}
	e := strings.IndexByte(es, 'e')
	x, _ := strconv.Atoi(es[e+1:])
	digits := strings.Replace(es[:e], ".", "", 1)
	k, n := len(digits), x+1 // value = 0.digits × 10^n
	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	case n-1 >= 0:
		out = digits[:1] + fractionOf(digits) + "e+" + strconv.Itoa(n-1)
	default:
		out = digits[:1] + fractionOf(digits) + "e-" + strconv.Itoa(1-n)
	}
	if neg {
		return "-" + out
	}
	return out
}

func fractionOf(digits string) string {
	if len(digits) == 1 {
		return ""
	}
	return "." + digits[1:]
}

// checkAdmittedNumber pins what every admitted literal must satisfy: the
// canonical form is a fixed point, and it is byte-identical to the RFC 8785
// serialization of the literal's nearest binary64.
func checkAdmittedNumber(t *testing.T, in string, canonical []byte) {
	t.Helper()
	again, err := graphops.CanonicalizeJSON(canonical)
	if err != nil || !bytes.Equal(again, canonical) {
		t.Errorf("number %q: canonical form %q is not a fixed point: %q %v", in, canonical, again, err)
	}
	f, err := strconv.ParseFloat(in, 64)
	if err != nil {
		t.Fatalf("number %q: admitted, but strconv cannot read it: %v", in, err)
	}
	if jcs := jcsNumber(f); jcs != string(canonical) {
		t.Errorf("number %q: canonical %q, but RFC 8785 serializes its nearest binary64 as %q", in, canonical, jcs)
	}
}

// The number rule in two halves. The canonical form is the literal's EXACT
// decimal value in RFC 8785 digit placement; and a literal is admitted only
// when that value round-trips through IEEE-754 binary64 (bdp#21, ruled
// 2026-09-08): read to the nearest double, serialized shortest, equal to the
// literal's own value. The refused half names, for each vector, what a
// binary64 reader would have made of it.
func TestCanonicalizeJSONNumbers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1.0", "1"}, {"1", "1"}, {"1e0", "1"}, {"-0.0", "0"}, {"-0", "0"}, {"0.0", "0"}, {"0e10", "0"}, {"0", "0"},
		{"1e300", "1e+300"}, {"1E300", "1e+300"},
		{"9007199254740992", "9007199254740992"}, // 2^53
		{"-9007199254740992", "-9007199254740992"},
		{"9007199254740994", "9007199254740994"},         // 2^53+2: a double whose shortest spelling is itself
		{"18446744073709552000", "18446744073709552000"}, // what a binary64 reader makes of 2^64
		{"1e21", "1e+21"}, {"1e20", "100000000000000000000"}, {"1000000000000000000000", "1e+21"},
		{"123456789012345680000", "123456789012345680000"}, // 17 significant digits, then zeros
		{"1e15", "1000000000000000"}, {"1000000000000000", "1000000000000000"},
		{"0.000001", "0.000001"}, {"1e-6", "0.000001"},
		{"0.0000001", "1e-7"}, {"1e-7", "1e-7"}, {"-1e-7", "-1e-7"}, {"1.5e-7", "1.5e-7"},
		{"123.456e2", "12345.6"}, {"-1.5", "-1.5"}, {"1.50", "1.5"},
		{"100", "100"}, {"1E+2", "100"}, {"1e-1", "0.1"}, {"0.1", "0.1"}, {"0.10", "0.1"},
		{"10.0e-1", "1"}, {"25e-1", "2.5"}, {"123e-2", "1.23"},
		{"1.5e1", "15"}, {"9.99e2", "999"}, {"1.23e+3", "1230"}, {"0.5", "0.5"}, {"-0.5", "-0.5"},
		{"1000000", "1000000"}, {"5e-324", "5e-324"}, {"-5e-324", "-5e-324"}, // the smallest subnormal
		{"1e-323", "1e-323"}, {"1e308", "1e+308"},
		{"1.7976931348623157e308", "1.7976931348623157e+308"}, // the largest double
		{"0.30000000000000004", "0.30000000000000004"},        // 0.1+0.2, as a binary64 reader spells it
		{"3.0000000000000004", "3.0000000000000004"},
		{"333333333.3333333", "333333333.3333333"},   // RFC 8785 Appendix B's rounded value
		{"1424953923781206.2", "1424953923781206.2"}, // RFC 8785 Appendix B, "round to even"
		{"1.0000000000000001e+23", "1.0000000000000001e+23"}, {"9.999999999999997e+22", "9.999999999999997e+22"},
		{"295147905179352830000", "295147905179352830000"}, // RFC 8785 Appendix B's spelling of 2^68
	} {
		got, err := graphops.CanonicalizeJSON([]byte(tc.in))
		if err != nil {
			t.Errorf("number %q: %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("number %q canonicalized to %q, want %q", tc.in, got, tc.want)
		}
		checkAdmittedNumber(t, tc.in, got)
	}
	// Refused: peer is what a binary64 reader serializes the literal as; ""
	// marks a literal outside the binary64 range altogether.
	for _, tc := range []struct{ in, peer string }{
		{"9007199254740993", "9007199254740992"}, // 2^53+1: halfway between two doubles, rounds to even
		{"-9007199254740993", "-9007199254740992"},
		{"9007199254740995", "9007199254740996"},
		{"18446744073709551616", "18446744073709552000"}, // 2^64: exactly a double, but not its shortest spelling
		{"18446744073709551615", "18446744073709552000"}, // MaxUint64
		{"12345678901234567891", "12345678901234567000"}, // 20 significant digits
		{"12345678901234567890", "12345678901234567000"},
		{"123456789012345678901", "123456789012345680000"},
		{"1234567890123456789012", "1.2345678901234568e+21"},
		{"295147905179352825856", "295147905179352830000"}, // 2^68 exactly; a binary64 reader spells it rounded
		{"1.00000000000000001", "1"},
		{"0.1000000000000000055511151231257827021181583404541015625", "0.1"}, // 0.1's exact binary expansion
		{"333333333.33333329", "333333333.3333333"},                          // the RFC 8785 worked example's input
		{"1424953923781206.25", "1424953923781206.2"},
		{"1e-400", "0"}, {"1e-324", "0"}, {"1e-1000000000000", "0"}, // below the range: read as zero
		{"2.4703282292062327e-324", "0"},                      // just under half the smallest subnormal
		{"4.9e-324", "5e-324"},                                // rounds up to the smallest subnormal, which spells differently
		{"1.7976931348623158e308", "1.7976931348623157e+308"}, // rounds down to the largest double
		{"1e400", ""}, {"-1e400", ""}, {"1e309", ""}, {"1.7976931348623159e308", ""}, {"1e1000000000000", ""}, // beyond the range
	} {
		_, err := graphops.CanonicalizeJSON([]byte(tc.in))
		wantValidation(t, err, "number "+tc.in)
		want := `at JSON pointer "" is outside the IEEE-754 binary64 range (bdp#21`
		if tc.peer != "" {
			want = `at JSON pointer "" does not round-trip through IEEE-754 binary64: a binary64 reader serializes it as ` + tc.peer + ` (bdp#21)`
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("number %q: %v\n  want a diagnostic containing %q", tc.in, err, want)
		}
	}
	// Malformed literals are syntax errors, refused before the law is reached.
	for _, in := range []string{
		"01", "1.", ".5", "+1", "1e", "1e+", "-", "0x10", "NaN", "Infinity", "1_000", "--1", "1.e5", "-a",
		"1e1000000000000000", // exponent out of range
	} {
		_, err := graphops.CanonicalizeJSON([]byte(in))
		wantValidation(t, err, "number "+in)
		if strings.Contains(err.Error(), "binary64") {
			t.Errorf("number %q: refused by the admission law instead of its syntax: %v", in, err)
		}
	}
}

// A refusal names where it bit: the RFC 6901 pointer of the member or
// element ("" is the whole document), with "~" and "/" in member names
// escaped, the byte offset of the literal, and the literal itself —
// abbreviated when it is long. Every door user-supplied JSON enters through
// reports the same way.
func TestCanonicalizeJSONBinary64RefusalNamesThePointer(t *testing.T) {
	for _, tc := range []struct{ in, pointer string }{
		{`9007199254740993`, ``},
		{`{"n":9007199254740993}`, `/n`},
		{`{"":9007199254740993}`, `/`},
		{`{"a":[1,{"b":9007199254740993}]}`, `/a/1/b`},
		{`[[[1e400]]]`, `/0/0/0`},
		{`[1,[2,[3,4,1e400]]]`, `/1/1/2`},
		{`{"a/b":{"m~n":[0,1,1e400]}}`, `/a~1b/m~0n/2`},
		{`{"x":[{"y":[1,2,3,4,5,6,7,8,9,10,11,1e-400]}]}`, `/x/0/y/11`},
		{`{"ok":[1.0,-0.0,1e300],"bad":{"deep":[{"n":18446744073709551616}]}}`, `/bad/deep/0/n`},
		{`{"first":9007199254740993,"second":9007199254740993}`, `/first`}, // the first offender, in source order
	} {
		_, err := graphops.CanonicalizeJSON([]byte(tc.in))
		wantValidation(t, err, tc.in)
		if want := fmt.Sprintf("at JSON pointer %q", tc.pointer); !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v\n  want %s", tc.in, err, want)
		}
	}
	// The byte offset is the literal's start.
	_, err := graphops.CanonicalizeJSON([]byte(`{"n":  9007199254740993}`))
	wantValidation(t, err, "offset")
	if !strings.Contains(err.Error(), `JSON at byte 7: number 9007199254740993 at JSON pointer "/n"`) {
		t.Errorf("byte offset: %v", err)
	}
	// A long literal is elided in the diagnostic; the pointer locates it.
	long := strings.Repeat("9", 60)
	_, err = graphops.CanonicalizeJSON([]byte(`{"n":` + long + `}`))
	wantValidation(t, err, "long literal")
	if !strings.Contains(err.Error(), `number `+long[:24]+`...`+long[:24]+` (60 bytes) at JSON pointer "/n"`) || strings.Contains(err.Error(), long) {
		t.Errorf("long literal is not elided: %v", err)
	}
	// NewProperties and JSONEqual pass the diagnostic through.
	_, err = graphops.NewProperties([]byte(`{"n":9007199254740993}`))
	wantValidation(t, err, "properties")
	if !strings.Contains(err.Error(), `properties: JSON at byte 5: number 9007199254740993 at JSON pointer "/n" does not round-trip`) {
		t.Errorf("NewProperties: %v", err)
	}
	_, err = graphops.JSONEqual([]byte(`[1]`), []byte(`[9007199254740993]`))
	wantValidation(t, err, "JSONEqual")
	if !strings.Contains(err.Error(), `at JSON pointer "/0"`) {
		t.Errorf("JSONEqual: %v", err)
	}
	// One refusal never poisons the next document: the scanner's location is
	// per call.
	if got, err := graphops.CanonicalizeJSON([]byte(`{"n":9007199254740992}`)); err != nil || string(got) != `{"n":9007199254740992}` {
		t.Errorf("after a refusal: %q %v", got, err)
	}
}

// The exponent bound (maxExponentMagnitude) is stated over the NORMALIZED
// value and is checked before the binary64 law: a literal past it is refused
// as "exponent out of range", one within it but past binary64 as outside the
// range. Zero, which has no magnitude, passes both whatever its exponent —
// short of the spelled-exponent guard — and canonicalization stays a fixed
// point on every admitted literal. In exact mode (the ledger framing) the
// bound is the only bound, which the frozen-layout test pins from its side.
func TestCanonicalizeJSONNumberExponentBoundAndTheBinary64Range(t *testing.T) {
	const bound = "1000000000000" // maxExponentMagnitude
	for _, tc := range []struct{ in, want string }{
		{"0e" + bound, "0"}, {"0e1000000000005", "0"}, {"0.000e-1000000000005", "0"}, {"-0.0e999999999999", "0"},
		{"0e-" + bound, "0"}, {"0.0e0", "0"},
	} {
		got, err := graphops.CanonicalizeJSON([]byte(tc.in))
		if err != nil || string(got) != tc.want {
			t.Errorf("number %q: got %q, %v; want %q", tc.in, got, err, tc.want)
			continue
		}
		checkAdmittedNumber(t, tc.in, got)
	}
	// Within the bound, beyond binary64: refused by the admission law.
	for _, tc := range []struct{ in, reason string }{
		{"1e" + bound, "is outside the IEEE-754 binary64 range"},
		{"10e999999999999", "is outside the IEEE-754 binary64 range"},   // trailing zero moves the exponent up to the bound
		{"0.1e1000000000001", "is outside the IEEE-754 binary64 range"}, // the spelled exponent exceeds the bound; the value does not
		{"1.50e" + bound, "is outside the IEEE-754 binary64 range"},
		{"-1e" + bound, "is outside the IEEE-754 binary64 range"},
		{"123456789e999999999992", "is outside the IEEE-754 binary64 range"},
		{"1e-" + bound, "a binary64 reader serializes it as 0"}, // below the range
		{"0.01e-999999999998", "a binary64 reader serializes it as 0"},
		{"100e-1000000000002", "a binary64 reader serializes it as 0"},
		{"1.5e-" + bound, "a binary64 reader serializes it as 0"},
		{"1e309", "is outside the IEEE-754 binary64 range"}, // the first power of ten past the largest double
	} {
		_, err := graphops.CanonicalizeJSON([]byte(tc.in))
		wantValidation(t, err, "number "+tc.in)
		if !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("number %q: %v\n  want %q", tc.in, err, tc.reason)
		}
	}
	// Past the bound: refused as out of range, before the law is consulted.
	for _, in := range []string{
		"10e" + bound,               // canonically 1e+1000000000001
		"1e1000000000001",           // one past the bound
		"100e" + bound,              // two past
		"1.5e1000000000001",         // digits do not rescue it
		"0.1e-" + bound,             // canonically 1e-1000000000001
		"1e-1000000000001",          // one past the negative bound
		"0.001e-999999999999",       // canonically 1e-1000000000002
		"-10e" + bound,              // sign is orthogonal
		"1e99999999999999999999999", // absurd spelled exponent: refused before it can overflow
		"1e-99999999999999999999999",
		"0e99999999999999999999999", // zero too: the guard is on the spelling
	} {
		_, err := graphops.CanonicalizeJSON([]byte(in))
		wantValidation(t, err, "number "+in)
		if !strings.Contains(err.Error(), "exponent out of range") || strings.Contains(err.Error(), "binary64") {
			t.Errorf("number %q: want the exponent bound's refusal, got %v", in, err)
		}
	}
	// The same laws hold inside a document, where a properties write lands.
	doc := `{"zero":0e1000000000005,"big":1e308,"small":5e-324}`
	once, err := graphops.CanonicalizeJSON([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != `{"big":1e+308,"small":5e-324,"zero":0}` {
		t.Fatalf("document canonicalized to %s", once)
	}
	twice, err := graphops.CanonicalizeJSON(once)
	if err != nil || !bytes.Equal(once, twice) {
		t.Fatalf("document canonical form is not a fixed point: %s %v", twice, err)
	}
	for _, in := range []string{`{"x":10e` + bound + `}`, `{"x":1e` + bound + `}`, `{"x":1e400}`} {
		if _, err := graphops.NewProperties([]byte(in)); !errors.Is(err, graphops.ErrValidation) {
			t.Fatalf("a properties document %s must be refused at admission: %v", in, err)
		}
	}
}

// RFC 8785's own vectors. Appendix B's table lists binary64 bit patterns
// with the JSON each serializes to; under the admission law every such
// serialization is admitted, is its own canonical form, and reads back to
// the listed double. The RFC's worked example is refused as authored — its
// input carries 333333333.33333329, which JCS rounds — while the RFC's
// expected output is admitted as a fixed point, and the input with that one
// value already rounded canonicalizes to the RFC's output byte for byte. Then
// a seeded sweep of random doubles: each one's RFC 8785 serialization is
// admitted and canonical, and its EXACT decimal expansion is admitted only
// when it is that serialization's value (math/big is the oracle), in which
// case the two canonicalize identically.
func TestCanonicalizeJSONAgreesWithRFC8785(t *testing.T) {
	for _, tc := range []struct {
		bits uint64
		json string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"}, // minus zero
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"}, // round to even
	} {
		f, err := strconv.ParseFloat(tc.json, 64)
		if err != nil || f != math.Float64frombits(tc.bits) {
			t.Errorf("premise: %q does not read back as %016x: %v", tc.json, tc.bits, err)
		}
		got, err := graphops.CanonicalizeJSON([]byte(tc.json))
		if err != nil || string(got) != tc.json {
			t.Errorf("RFC 8785 vector %q: got %q, %v", tc.json, got, err)
		}
	}

	const rfcInput = `{"numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001], "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/", "literals": [null, true, false]}`
	const rfcOutput = "{\"literals\":[null,true,false],\"numbers\":[333333333.3333333,1e+30,4.5,0.002,1e-27],\"string\":\"\u20ac$\\u000f\\nA'B\\\"\\\\\\\\\\\"/\"}"
	_, err := graphops.CanonicalizeJSON([]byte(rfcInput))
	wantValidation(t, err, "the RFC's worked example as authored")
	if !strings.Contains(err.Error(), `number 333333333.33333329 at JSON pointer "/numbers/0" does not round-trip through IEEE-754 binary64: a binary64 reader serializes it as 333333333.3333333`) {
		t.Errorf("worked example: %v", err)
	}
	got, err := graphops.CanonicalizeJSON([]byte(rfcOutput))
	if err != nil || string(got) != rfcOutput {
		t.Errorf("the RFC's expected output must be a fixed point: %q %v", got, err)
	}
	got, err = graphops.CanonicalizeJSON([]byte(strings.Replace(rfcInput, "333333333.33333329", "333333333.3333333", 1)))
	if err != nil || string(got) != rfcOutput {
		t.Errorf("the worked example with its rounded value: %q %v\nwant %q", got, err, rfcOutput)
	}

	rng := rand.New(rand.NewPCG(0x8785, 0x21))
	for i := 0; i < 2000; i++ {
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		jcs := jcsNumber(f)
		got, err := graphops.CanonicalizeJSON([]byte(jcs))
		if err != nil || string(got) != jcs {
			t.Fatalf("%016x: RFC 8785 form %q: got %q, %v", math.Float64bits(f), jcs, got, err)
		}
		exact := strconv.FormatFloat(f, 'e', 1100, 64) // every double's decimal expansion ends within 1100 digits
		jcsValue, ok := new(big.Rat).SetString(jcs)
		if !ok {
			t.Fatalf("%016x: oracle cannot read %q", math.Float64bits(f), jcs)
		}
		got, err = graphops.CanonicalizeJSON([]byte(exact))
		if new(big.Rat).SetFloat64(f).Cmp(jcsValue) == 0 {
			if err != nil || string(got) != jcs {
				t.Fatalf("%016x: exact expansion %q is the RFC 8785 value and must canonicalize to %q: got %q, %v", math.Float64bits(f), exact, jcs, got, err)
			}
		} else if !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), "a binary64 reader serializes it as "+jcs+" (bdp#21)") {
			t.Fatalf("%016x: exact expansion must be refused naming %q: got %q, %v", math.Float64bits(f), jcs, got, err)
		}
	}
}

func TestCanonicalizeJSONStrings(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`"\u0041"`, `"A"`},
		{`"\u00e9"`, "\"\u00e9\""},
		{`"\u00C9"`, "\"\u00c9\""},
		{`"\ud83d\ude00"`, "\"\U0001F600\""},
		{`"\uD83D\uDE00"`, "\"\U0001F600\""},
		{`"\u2028"`, "\"\u2028\""}, // literal, as JSON.stringify emits it
		{`"\/"`, `"/"`},
		{`"\u001f"`, `"\u001f"`},
		{`"\u000B"`, `"\u000b"`}, // lowercase hex
		{`"\u0000"`, `"\u0000"`},
		{`"\u007f"`, "\"\x7f\""}, // DEL is literal
		{`"\b\f\n\r\t"`, `"\b\f\n\r\t"`},
		{`"\u0008\u000c\u000a\u000d\u0009"`, `"\b\f\n\r\t"`},
		{`"<>&"`, `"<>&"`},
		{`"\""`, `"\""`},
		{`"\\"`, `"\\"`},
		{"\"\u00e9\"", "\"\u00e9\""},
		{`""`, `""`},
	} {
		got, err := graphops.CanonicalizeJSON([]byte(tc.in))
		if err != nil {
			t.Errorf("string %s: %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("string %s canonicalized to %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{
		`"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\u0041"`, `"\ud800\ud800"`, `"\ud83d\ude0"`,
		"\"a\x01b\"", `"\x"`, `"abc`, "\"\xff\"", "\"\xed\xa0\x80\"", `"\u12"`, `"\u12G4"`, `"\`, `"\u`,
		"\"\xc3\\n\"", // invalid UTF-8 before an escape
	} {
		_, err := graphops.CanonicalizeJSON([]byte(in))
		wantValidation(t, err, "string "+in)
	}
}

func TestCanonicalizeJSONStructure(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{" \t\n\r{ \"a\" : [ 1 , 2 ] , \"b\" : { } } \n", `{"a":[1,2],"b":{}}`},
		{`[]`, `[]`}, {`[ ]`, `[]`}, {`{}`, `{}`}, {`{ }`, `{}`},
		{`{"a":{"c":1,"b":2}}`, `{"a":{"b":2,"c":1}}`},
		{`[1,[2,[3]]]`, `[1,[2,[3]]]`},
		{`{"":1}`, `{"":1}`},
		{`true`, `true`}, {` false `, `false`}, {`null`, `null`}, {`"x"`, `"x"`}, {`1`, `1`},
		{`{"10":1,"9":2,"1":3}`, `{"1":3,"10":1,"9":2}`}, // lexicographic keys
		// Keys sort by UTF-16 code unit: the emoji's lead surrogate sorts
		// before U+FFFF, where byte order would put it after.
		{`{"\uffff":1,"\ud83d\ude00":2}`, "{\"\U0001F600\":2,\"\uFFFF\":1}"},
		// RFC 8785 §3.2.3's sorting example.
		{
			`{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One","\ud83d\ude02":"Emoji: Face with Tears of Joy","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`,
			"{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\",\"\u20ac\":\"Euro Sign\",\"\U0001F602\":\"Emoji: Face with Tears of Joy\",\"\uFB33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
		// RFC 8785's worked example, with the one value JCS rounds already
		// rounded: TestCanonicalizeJSONAgreesWithRFC8785 pins that the
		// example as authored is refused at /numbers/0.
		{
			`{"numbers": [333333333.3333333, 1E30, 4.50, 2e-3, 0.000000000000000000000000001], "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/", "literals": [null, true, false]}`,
			"{\"literals\":[null,true,false],\"numbers\":[333333333.3333333,1e+30,4.5,0.002,1e-27],\"string\":\"\u20ac$\\u000f\\nA'B\\\"\\\\\\\\\\\"/\"}",
		},
	} {
		got, err := graphops.CanonicalizeJSON([]byte(tc.in))
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s canonicalized to\n  %q\nwant\n  %q", tc.in, got, tc.want)
		}
	}
	// Canonical form is a fixed point.
	in := `{"z":[1.0,{"b":"\u0041","a":null}],"a":-0.0}`
	once, err := graphops.CanonicalizeJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := graphops.CanonicalizeJSON(once)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Fatalf("canonical form is not a fixed point: %q then %q", once, twice)
	}
}

func TestCanonicalizeJSONRejects(t *testing.T) {
	for _, in := range []string{
		"", " ", "{}x", "[1,]", `{"a":1,}`, `{a:1}`, "[1 2]", "tru", "nul", "True", "[", "{", `{"a"}`, `{"a":}`,
		"[,1]", "{,}", "1 2", `"a" "b"`, `{"a":1 "b":2}`, "[1;2]", "]", "}", `{"a":1]`, "[1}", "\x00", "{\"a\":1", "{\"a\":1,", "[1", "[1,", "{\"a\":1,\"b\"", "{\"ab", "{\"\x01\":1}",
		`{"a":1,"a":2}`, `{"a":1,"b":2,"a":3}`, `{"x":{"a":1,"a":1}}`,
		`{"a":1,"\u0061":2}`, // duplicate after decoding
		"{\"a\":1}\x00",
	} {
		_, err := graphops.CanonicalizeJSON([]byte(in))
		wantValidation(t, err, "input "+in)
	}
}

func TestCanonicalizeJSONDepth(t *testing.T) {
	ok := strings.Repeat("[", 10000) + strings.Repeat("]", 10000)
	if _, err := graphops.CanonicalizeJSON([]byte(ok)); err != nil {
		t.Fatalf("depth 10000 refused: %v", err)
	}
	tooDeep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	_, err := graphops.CanonicalizeJSON([]byte(tooDeep))
	wantValidation(t, err, "depth 10001 arrays")
	tooDeepObjects := strings.Repeat(`{"a":`, 10001) + "1" + strings.Repeat("}", 10001)
	_, err = graphops.CanonicalizeJSON([]byte(tooDeepObjects))
	wantValidation(t, err, "depth 10001 objects")
}

func TestJSONEqual(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1", "1.0", true}, {"1", `"1"`, false}, {"1e2", "100", true}, {"0.1", "0.10", true}, {"-0", "0", true},
		{"9007199254740994", "9007199254740992", false}, {"1", "1.0000000000000002", false},
		{"9007199254740992", "9.007199254740992e15", true}, {"0.1", "1e-1", true},
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`, true}, {`{"a":1}`, `{"a":1,"b":2}`, false},
		{"[1,2]", "[2,1]", false}, {"[1,2]", "[1,2]", true}, {"[]", "{}", false},
		{"null", "null", true}, {`{"a":null}`, `{}`, false}, {"true", "false", false},
		{`"\u0041"`, `"A"`, true}, {`"a"`, `"A"`, false},
		{`[1,{"a":[2,3]}]`, ` [ 1 , { "a" : [ 2 , 3 ] } ] `, true},
	} {
		got, err := graphops.JSONEqual([]byte(tc.a), []byte(tc.b))
		if err != nil {
			t.Errorf("JSONEqual(%s, %s): %v", tc.a, tc.b, err)
			continue
		}
		if got != tc.want {
			t.Errorf("JSONEqual(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if _, err := graphops.JSONEqual([]byte("{"), []byte("1")); err == nil {
		t.Error("JSONEqual accepted a malformed left operand")
	}
	if _, err := graphops.JSONEqual([]byte("1"), []byte("}")); err == nil {
		t.Error("JSONEqual accepted a malformed right operand")
	}
	// A number the admission law refuses is an error on either side, never a
	// verdict: 9007199254740993 is not "unequal to" 9007199254740992, it is
	// not a value the domain holds.
	for _, tc := range [][2]string{{"9007199254740993", "9007199254740992"}, {"1", "1.00000000000000001"}, {`{"a":1e400}`, `{"a":1e400}`}} {
		if _, err := graphops.JSONEqual([]byte(tc[0]), []byte(tc[1])); !errors.Is(err, graphops.ErrValidation) {
			t.Errorf("JSONEqual(%s, %s): want ErrValidation, got %v", tc[0], tc[1], err)
		}
	}
}

// --- Scope URL and Type URL -----------------------------------------------

func TestValidateScopeURL(t *testing.T) {
	accept := []string{
		"https://beads.example/acme/", "https://beads.example/", "http://localhost:3000/",
		"https://beads.example:8443/acme/", "https://beads.example/caf%C3%A9/", "https://[::1]/",
		"https://[2001:db8::1]:8443/", "https://127.0.0.1/", "https://beads.example/a/b/c/",
		"https://beads.example/local-testing/", "https://beads.example/x/local-test/",
		"https://beads.example/Acme/", "https://my_host/", "http://beads.example:8080/x/",
		"https://beads.example:0/",
		// A client may name a development server's reserved Scope; only a
		// PERSISTED identity refuses it (TestValidatePersistedScopeURL).
		"https://beads.example/local-test/",
		// WHATWG leaves a trailing-dot registered name alone: it is canonical,
		// and a different host from the undotted spelling.
		"https://beads.example./", "https://beads.example./acme/",
		// The WHATWG IPv6 serializer's spellings, IPv4-mapped included.
		"https://[::ffff:102:304]/", "https://[::ffff:c0a8:1]/", "https://[::]/", "https://[1::]/",
		"https://[2001:db8:0:1:1:1:1:1]/", "https://[1:0:0:2::]/",
		// Every port that is not the default and has no leading zero.
		"https://beads.example:80/", "http://beads.example:443/", "https://beads.example:65535/",
		"https://0.0.0.0/", "https://255.255.255.255/",
	}
	for _, u := range accept {
		if err := graphops.ValidateScopeURL(u); err != nil {
			t.Errorf("Scope URL %q refused: %v", u, err)
		}
	}
	reject := []string{
		"", "https://beads.example/acme", "HTTPS://beads.example/acme/", "Https://beads.example/",
		"https://Beads.Example/acme/", "https://beads.example:443/acme/", "http://beads.example:80/",
		"https://beads.example/acme/?x=1", "https://beads.example/acme/?", "https://beads.example/acme/#f",
		"https://beads.example/acme/#", "https://user:pw@beads.example/acme/", "https://@beads.example/",
		"https://beads.example/a%2Fb/", "https://beads.example/a b/", "https://beads.example/caf%c3%a9/",
		"https://beads.example/%41/", "https://beads.example/./", "https://beads.example/../",
		"https://beads.example/a/./b/", "https://beads.example//x/", "ftp://x/", "https:///acme/",
		"https://[0:0:0:0:0:0:0:1]/", "https://[::ffff:1.2.3.4]/", "https://[::1", "https://[::1]x/",
		"https://[fe80::1%25eth0]/", "https://beads.example/a\\b/",
		"https://beads.example:/", "https://beads.example:0080/", "https://beads.example:99999/",
		"https://beads.example:abc/", "https://.beads.example/",
		"https://127.1/", "https://01.2.3.4/", "https://1.2.3.4.5/", "https://beads.example/café/",
		"beads.example/acme/", "//beads.example/acme/", "https://beads.example/a?b/",
		"https://beads.example/%/", "https://beads.example/a%2/", "https://beads.example/a[b]/",
		"https://beads.example/\t/", "https://beads example/", "https://beads.example/a|b/",
		"https://beads.example/a{b}/", "https://beads.example/\x7f/", "https:beads.example/",
		"https:/beads.example/", "https://BEADS.example/", "https://beads.example/%E2%9C%93/x%2e/",
		"https://beads.example/a%5Cb/", "https://beads.example/a%00b/",
		// Numeric ports: leading zeros and zero-padded defaults are the
		// parser's :443 and :0, spelled otherwise.
		"https://beads.example:0443/", "https://beads.example:00/", "http://beads.example:00080/",
		"https://beads.example:65536/", "https://beads.example:000000/",
		// Percent-encoded host characters: the parser decodes them.
		"https://beads%2Eexample/", "https://%62eads.example/", "https://beads%2fexample/",
		"https://beads%25example/", "https://beads%40example/", "https://caf%C3%A9.example/",
		"https://beads%2/", "https://beads%zz/", "https://beads%00example/", "https://beads%7Fexample/",
		"https://beads%09example/", "https://beads%20example/",
		// IPv4 in every spelling the parser rewrites: hex, decimal, octal,
		// short forms, a trailing dot, and a last label that is a number.
		"https://0x7f000001/", "https://0X7F000001/", "https://2130706433/", "https://0177.0.0.1/",
		"https://127.0.1/", "https://0x7f.1/", "https://1.2.3.4./", "https://beads.123/",
		"https://beads.0x1f/", "https://beads.0x/", "https://1.2.3.256/", "https://256.1.1.1/",
		"https://1.2.65536/", "https://4294967296/", "https://0x/", "https://1.2.3.4../",
		"https://99999999999999999999/", "https://0xffffffffffff.1/", "https://1.99999999999999999999/",
		// Registered names the parser refuses or rewrites.
		"https://a..b/", "https://beads.example../", "https://./", "https://a!b/", "https://a^b/",
		"https://a%5Bb/",
		// IPv6 that is not the serializer's spelling.
		"https://[::FFFF:102:304]/", "https://[0::1]/", "https://[::0001]/", "https://[1.2.3.4]/",
		"https://[::1.2.3.4]/", "https://[1:0:0:2:0:0:0:0]/",
	}
	for _, u := range reject {
		wantValidation(t, graphops.ValidateScopeURL(u), "Scope URL "+u)
	}
	// Diagnostics never echo the value (a pasted token would land in a log).
	for _, u := range []string{
		"https://secret-token-value@beads.example/",
		"https://secret-token-value.example:0443/",
		"https://secret-token-value..example/",
		"https://secret-token-value.example:99999/",
	} {
		err := graphops.ValidateScopeURL(u)
		if err == nil || strings.Contains(err.Error(), "secret-token-value") {
			t.Fatalf("Scope URL diagnostic echoed the value: %v", err)
		}
	}
}

// ValidatePersistedScopeURL is ValidateScopeURL plus the reservation of the
// first path segment "local-test": a client may reference such a Scope, an
// authority may not persist one.
func TestValidatePersistedScopeURL(t *testing.T) {
	for _, u := range []string{
		"https://beads.example/acme/", "https://beads.example/", "https://beads.example/x/local-test/",
		"https://beads.example/local-testing/", "https://beads.example/local-test-2/", "https://beads.example./",
	} {
		if err := graphops.ValidatePersistedScopeURL(u); err != nil {
			t.Errorf("persisted Scope URL %q refused: %v", u, err)
		}
	}
	for _, u := range []string{
		"https://beads.example/local-test/", "https://beads.example/local-test/acme/", "http://localhost:3000/local-test/",
		"https://beads.example/acme", "https://Beads.example/acme/", "", "https://beads.example:0443/",
	} {
		wantValidation(t, graphops.ValidatePersistedScopeURL(u), "persisted Scope URL "+u)
	}
	// The general law admits the reserved segment: a client can point at a
	// bdptest development server.
	if err := graphops.ValidateScopeURL("https://beads.example/local-test/"); err != nil {
		t.Fatalf("a client-side Scope URL under local-test must be admissible: %v", err)
	}
	ref, err := graphops.ParseRef("https://beads.example/local-test/", "https://beads.example/local-test/beads/x", "")
	if err != nil || !ref.InScope() || ref.Path() != "beads/x" {
		t.Fatalf("a reference into a local-test Scope must classify in-Scope: %v %+v", err, ref)
	}
}

func TestNormalizeScopeURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://EXAMPLE.com:443/scope/", "https://example.com/scope/"},
		{"http://example.com:80/scope/", "http://example.com/scope/"},
		{"https://example.com/scope", "https://example.com/scope/"},
		{"https://example.com", "https://example.com/"},
		{"HTTP://Example.COM", "http://example.com/"},
		{"https://[0:0:0:0:0:0:0:1]:8443/x", "https://[::1]:8443/x/"},
		{"https://example.com:", "https://example.com/"},
		{"https://example.com:8080", "https://example.com:8080/"},
		{"https://example.com/caf%C3%A9", "https://example.com/caf%C3%A9/"},
		// The whole origin normalizes: ports, IPv4 and IPv6 spellings,
		// percent-encoded host characters, a trailing dot on an IPv4 host.
		{"https://example.com:0443/x", "https://example.com/x/"},
		{"http://example.com:00080/", "http://example.com/"},
		{"https://example.com:08080/", "https://example.com:8080/"},
		{"https://example.com:00/", "https://example.com:0/"},
		{"https://0x7f000001/x", "https://127.0.0.1/x/"},
		{"https://127.1/", "https://127.0.0.1/"},
		{"https://0177.0.0.1/", "https://127.0.0.1/"},
		{"https://2130706433/", "https://127.0.0.1/"},
		{"https://1.2.3.4./", "https://1.2.3.4/"},
		{"https://1.2.65535/", "https://1.2.255.255/"},
		{"https://0x/", "https://0.0.0.0/"},
		{"https://BEADS%2eEXAMPLE/", "https://beads.example/"},
		{"https://%62eads.example/", "https://beads.example/"},
		{"https://[::ffff:1.2.3.4]/", "https://[::ffff:102:304]/"},
		{"https://[::FFFF:0:0]/", "https://[::ffff:0:0]/"},
		{"https://[1:0:0:2:0:0:0:0]/", "https://[1:0:0:2::]/"},
		{"https://[0:0:0:0:0:0:0:0]/", "https://[::]/"},
		{"https://[1:0:1:1:1:1:1:1]/", "https://[1:0:1:1:1:1:1:1]/"},
		{"https://[::1.2.3.4]/", "https://[::102:304]/"},
		{"https://beads.example./x", "https://beads.example./x/"},
	} {
		got, err := graphops.NormalizeScopeURL(tc.in)
		if err != nil {
			t.Errorf("NormalizeScopeURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeScopeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if err := graphops.ValidatePersistedScopeURL(got); err != nil {
			t.Errorf("normalized %q does not validate: %v", got, err)
		}
		// Normalization is a fixed point.
		if again, err := graphops.NormalizeScopeURL(got); err != nil || again != got {
			t.Errorf("NormalizeScopeURL(%q) = %q, %v; not a fixed point", got, again, err)
		}
	}
	for _, in := range []string{
		"https://u:p@example.com/", "https://example.com/?q", "https://example.com/#f",
		"https://example.com/a/../b/", "ftp://example.com/", "example.com/", "", "://x/",
		"https://example.com/local-test/", "https://example.com/a b/", "https://example.com/a%2fb/",
		"https://example.com/a[b]", "https:example.com", "https://EXAMPLE.com/%41/",
		"https://[::1", "https://[::1]:x/", "ht tp://example.com/",
		// The parser fails on these hosts and ports; nothing to normalize to.
		"https://beads%2fexample/", "https://beads%25example/", "https://caf%C3%A9.example/",
		"https://beads.123/", "https://1.2.3.4.5/", "https://999.1.1.1/", "https://1.2.3.999/",
		"https://example.com:65536/", "https://%25/", "https://beads%2/", "https://a..b/",
		"https://./", "https://[fe80::1%25eth0]/", "https://[1.2.3.4]/", "https:///x/",
		"https://[::1]:99999/", "https://a!b/",
	} {
		_, err := graphops.NormalizeScopeURL(in)
		wantValidation(t, err, "NormalizeScopeURL "+in)
	}
}

func TestValidateTypeURL(t *testing.T) {
	accept := []string{
		"https://work.example/types/task", "https://work.example/types/task?v=1", "http://localhost:8080/t",
		"https://work.example/", "https://work.example/a[b]", "https://work.example/a|b",
		"https://work.example/x//y", "https://work.example/schemas/task-properties-v1",
		"https://work.example/t?a=b&c=d", "https://work.example/t?a=%20", "https://work.example/caf%C3%A9",
		"https://[::1]:8443/t", "https://work.example/t?", "https://work.example/t?x=[1]",
		"https://work.example/~user/T.Y_P-E", "https://work.example./types/task", "https://127.0.0.1/t",
		"https://[::ffff:102:304]/t", "https://work.example/local-test/t",
	}
	for _, u := range accept {
		if err := graphops.ValidateTypeURL(u); err != nil {
			t.Errorf("Type URL %q refused: %v", u, err)
		}
	}
	reject := []string{
		"", "https://work.example", "HTTPS://work.example/", "https://Work.example/", "https://work.example:443/",
		"https://work.example/#x", "https://work.example/#", "https://u@work.example/", "https://work.example/a b",
		"https://work.example/a%2fb", "https://work.example/%41", "https://work.example/./x",
		"https://work.example/x/../y", "https://work.example/a\\b", "https://work.example/é",
		"https://work.example/a%", "https://work.example/a%4", "https://work.example/?a b",
		"https://work.example/x?a='b'", "https://work.example/x?a=\"b\"", "https://work.example/x?a=<b>",
		"https://work.example/{x}", "https://work.example/x`", "https://work.example/x?q=%zz",
		"work.example/types/task", "beads/x", "https://work.example/\x7f", "https://work.example/a\"b",
		"https://work.example/<x>", "mailto:a@b", "https://work.example:x/", "https://work.example:/",
		"https://:8080/", "https://work.example/x\n", "1http://x/",
		"https://work.example:0443/t", "https://0x7f000001/t", "https://work%2Eexample/t", "https://[::ffff:1.2.3.4]/t",
		graphops.WildcardOwnedLinkKey, // the ownsOutgoing wildcard key is never a Type
	}
	for _, u := range reject {
		wantValidation(t, graphops.ValidateTypeURL(u), "Type URL "+u)
	}
	// Type URL diagnostics DO quote the value: it is public catalog data and
	// the operator needs to see which descriptor is wrong.
	err := graphops.ValidateTypeURL("https://Work.example/x")
	if err == nil || !strings.Contains(err.Error(), "https://Work.example/x") {
		t.Fatalf("Type URL diagnostic should quote the value: %v", err)
	}
}

// --- ledger hashing -------------------------------------------------------

const (
	opID        = "0123456789abcdef0123456789abcdef"
	authorityID = "fedcba9876543210fedcba9876543210"
	scopeURL    = "https://beads.example/acme/"
)

var at = time.Date(2026, 9, 7, 12, 34, 56, 123456789, time.UTC)

func mustEvent(t *testing.T, spec graphops.LedgerEventSpec) graphops.LedgerEvent {
	t.Helper()
	e, err := graphops.NewLedgerEvent(spec)
	if err != nil {
		t.Fatalf("NewLedgerEvent(seq %d %s): %v", spec.Seq, spec.Kind, err)
	}
	return e
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The byte layout is FROZEN: the P1 migration stores these hashes. A change
// to this test is a change to every stored ledger.
func TestLedgerHashLayoutIsFrozen(t *testing.T) {
	mint := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 1, Kind: graphops.LedgerMint, OpID: opID, ScopeURL: scopeURL,
		AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: graphops.GenesisHash,
	})
	wantMint := `{"at":"2026-09-07T12:34:56.123456Z","authority_id":"fedcba9876543210fedcba9876543210","epoch":1,"kind":"mint","op_id":"0123456789abcdef0123456789abcdef","prev_hash":"0000000000000000000000000000000000000000000000000000000000000000","scope_url":"https://beads.example/acme/","seq":1}`
	if got := string(mint.CanonicalBytes()); got != wantMint {
		t.Fatalf("mint canonical bytes\n got %s\nwant %s", got, wantMint)
	}
	if mint.Hash() != sha256Hex([]byte(wantMint)) {
		t.Fatalf("mint hash %s is not sha256 of the canonical bytes", mint.Hash())
	}
	if mint.At() != at.Truncate(time.Microsecond) {
		t.Fatalf("At() = %v, want microsecond-truncated %v", mint.At(), at.Truncate(time.Microsecond))
	}

	rev, _ := graphops.NewRevision("abc")
	tomb := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 4, Kind: graphops.LedgerTombstone, OpID: opID, Path: "beads/x", ResourceKind: graphops.KindBead,
		Revision: rev, State: graphops.AllocationPruned, AuthorityID: authorityID, Epoch: math.MaxUint64,
		At: time.Date(2026, 9, 7, 14, 34, 56, 0, time.FixedZone("plus2", 2*3600)), PrevHash: mint.Hash(),
	})
	wantTomb := `{"at":"2026-09-07T12:34:56.000000Z","authority_id":"fedcba9876543210fedcba9876543210","epoch":18446744073709551615,"kind":"tombstone","op_id":"0123456789abcdef0123456789abcdef","path":"beads/x","prev_hash":"` + mint.Hash() + `","resource_kind":"bead","revision":"abc","seq":4,"state":"pruned"}`
	if got := string(tomb.CanonicalBytes()); got != wantTomb {
		t.Fatalf("tombstone canonical bytes\n got %s\nwant %s", got, wantTomb)
	}
	if tomb.Hash() != sha256Hex([]byte(wantTomb)) {
		t.Fatal("tombstone hash is not sha256 of the canonical bytes")
	}
	// The framing is exact and EXEMPT from the binary64 admission law
	// (bdp#21 DECISION in laws.go): epoch 2^64-1 has no binary64 spelling,
	// so the public canonicalizer refuses these very bytes, and the ledger
	// hashes them unchanged. The same holds for a seq past 2^53.
	if _, err := graphops.CanonicalizeJSON([]byte(wantTomb)); !errors.Is(err, graphops.ErrValidation) || !strings.Contains(err.Error(), `number 18446744073709551615 at JSON pointer "/epoch"`) {
		t.Fatalf("the ledger framing must be exempt from the admission law CanonicalizeJSON enforces: %v", err)
	}
	far := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 9007199254740993, Kind: graphops.LedgerPromote, OpID: opID,
		AuthorityID: authorityID, Epoch: 9007199254740993, At: at, PrevHash: mint.Hash(),
	})
	if got := string(far.CanonicalBytes()); !strings.Contains(got, `"epoch":9007199254740993,`) || !strings.HasSuffix(got, `"seq":9007199254740993}`) {
		t.Fatalf("seq and epoch past 2^53 must be hashed exactly: %s", got)
	}

	fp := strings.Repeat("ab", 32)
	install := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 2, Kind: graphops.LedgerInstall, OpID: opID, Fingerprint: fp,
		AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: mint.Hash(),
	})
	wantInstall := `{"at":"2026-09-07T12:34:56.123456Z","authority_id":"fedcba9876543210fedcba9876543210","epoch":1,"fingerprint":"` + fp + `","kind":"install","op_id":"0123456789abcdef0123456789abcdef","prev_hash":"` + mint.Hash() + `","seq":2}`
	if got := string(install.CanonicalBytes()); got != wantInstall {
		t.Fatalf("install canonical bytes\n got %s\nwant %s", got, wantInstall)
	}
	// Accessors carry the members through.
	spec := install.Spec()
	if spec.Fingerprint != fp || spec.Hash != install.Hash() || install.Fingerprint() != fp || install.OpID() != opID ||
		install.AuthorityID() != authorityID || install.Epoch() != 1 || install.PrevHash() != mint.Hash() ||
		install.Kind() != graphops.LedgerInstall || install.Seq() != 2 || install.Path() != "" || install.ScopeURL() != "" ||
		install.ResourceKind() != "" || !install.Revision().IsZero() || install.State() != "" || install.IsZero() {
		t.Fatalf("install accessors disagree with the spec: %+v", spec)
	}
	if tomb.Path() != "beads/x" || tomb.ResourceKind() != graphops.KindBead || tomb.State() != graphops.AllocationPruned ||
		!tomb.Revision().Equal(rev) || mint.ScopeURL() != scopeURL {
		t.Fatal("tombstone or mint accessors disagree with the spec")
	}
	if (graphops.LedgerEvent{}).IsZero() == false {
		t.Fatal("zero LedgerEvent must report IsZero")
	}
}

func chain(t *testing.T) []graphops.LedgerEvent {
	t.Helper()
	mint := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 1, Kind: graphops.LedgerMint, OpID: opID, ScopeURL: scopeURL,
		AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: graphops.GenesisHash,
	})
	install := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 2, Kind: graphops.LedgerInstall, OpID: opID, Fingerprint: strings.Repeat("ab", 32),
		AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: mint.Hash(),
	})
	alloc := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 3, Kind: graphops.LedgerAllocate, OpID: graphops.MintOpaqueToken(), Path: "links/l", ResourceKind: graphops.KindLink,
		Revision: graphops.MintRevision(), AuthorityID: authorityID, Epoch: 1, At: at.Add(time.Second), PrevHash: install.Hash(),
	})
	tomb := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 4, Kind: graphops.LedgerTombstone, OpID: graphops.MintOpaqueToken(), Path: "links/l", ResourceKind: graphops.KindLink,
		State: graphops.AllocationErased, AuthorityID: authorityID, Epoch: 1, At: at.Add(2 * time.Second), PrevHash: alloc.Hash(),
	})
	return []graphops.LedgerEvent{mint, install, alloc, tomb}
}

func TestLedgerHashIsDeterministicAndTamperEvident(t *testing.T) {
	spec := graphops.LedgerEventSpec{
		Seq: 1, Kind: graphops.LedgerMint, OpID: opID, ScopeURL: scopeURL,
		AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: graphops.GenesisHash,
	}
	a, b := mustEvent(t, spec), mustEvent(t, spec)
	if a.Hash() != b.Hash() || !bytes.Equal(a.CanonicalBytes(), b.CanonicalBytes()) {
		t.Fatal("the same spec must hash the same")
	}
	// Restoring with the stored hash verifies it …
	stored := a.Spec()
	if _, err := graphops.NewLedgerEvent(stored); err != nil {
		t.Fatalf("restoring an untouched event: %v", err)
	}
	// … and every tampered member is caught, including ones beyond the
	// microsecond that canonicalization drops.
	for name, mutate := range map[string]func(*graphops.LedgerEventSpec){
		"at":           func(s *graphops.LedgerEventSpec) { s.At = s.At.Add(time.Microsecond) },
		"epoch":        func(s *graphops.LedgerEventSpec) { s.Epoch++ },
		"seq":          func(s *graphops.LedgerEventSpec) { s.Seq++ },
		"scope_url":    func(s *graphops.LedgerEventSpec) { s.ScopeURL = "https://beads.example/other/" },
		"authority_id": func(s *graphops.LedgerEventSpec) { s.AuthorityID = strings.Repeat("0", 32) },
		"op_id":        func(s *graphops.LedgerEventSpec) { s.OpID = strings.Repeat("1", 32) },
		"prev_hash":    func(s *graphops.LedgerEventSpec) { s.PrevHash = strings.Repeat("1", 64) },
	} {
		tampered := stored
		mutate(&tampered)
		_, err := graphops.NewLedgerEvent(tampered)
		wantValidation(t, err, "tampered "+name)
	}
	// A sub-microsecond change is NOT a change: it never reached the bytes.
	sub := stored
	sub.At = sub.At.Add(500 * time.Nanosecond)
	if _, err := graphops.NewLedgerEvent(sub); err != nil {
		t.Fatalf("sub-microsecond instant should verify: %v", err)
	}
}

func TestVerifyLedgerChain(t *testing.T) {
	events := chain(t)
	if err := graphops.VerifyLedgerChain(graphops.GenesisHash, events); err != nil {
		t.Fatalf("intact chain refused: %v", err)
	}
	if err := graphops.VerifyLedgerChain(events[0].Hash(), events[1:]); err != nil {
		t.Fatalf("suffix chain refused: %v", err)
	}
	if err := graphops.VerifyLedgerChain(graphops.GenesisHash, nil); err != nil {
		t.Fatalf("empty chain refused: %v", err)
	}
	wantValidation(t, graphops.VerifyLedgerChain(graphops.GenesisHash, events[1:]), "chain not starting at genesis")
	wantValidation(t, graphops.VerifyLedgerChain(graphops.GenesisHash, []graphops.LedgerEvent{events[1], events[0]}), "reordered chain")
	wantValidation(t, graphops.VerifyLedgerChain(graphops.GenesisHash, []graphops.LedgerEvent{events[0], events[2]}), "missing link")
	wantValidation(t, graphops.VerifyLedgerChain("nope", events), "bad starting hash")
	wantValidation(t, graphops.VerifyLedgerChain(graphops.GenesisHash, []graphops.LedgerEvent{{}}), "zero event")
	// A gap with intact linkage: seq 5 linked to seq 2's hash.
	skip := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 5, Kind: graphops.LedgerPromote, OpID: opID, AuthorityID: authorityID, Epoch: 2, At: at, PrevHash: events[1].Hash(),
	})
	err := graphops.VerifyLedgerChain(graphops.GenesisHash, []graphops.LedgerEvent{events[0], events[1], skip})
	wantValidation(t, err, "gap")
	if !strings.Contains(err.Error(), "gap") {
		t.Fatalf("gap error should say so: %v", err)
	}
}

func TestLedgerEventShapePerKind(t *testing.T) {
	rev := graphops.MintRevision()
	base := func(kind graphops.LedgerEventKind) graphops.LedgerEventSpec {
		return graphops.LedgerEventSpec{Seq: 1, Kind: kind, OpID: opID, AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: graphops.GenesisHash}
	}
	with := func(kind graphops.LedgerEventKind, f func(*graphops.LedgerEventSpec)) graphops.LedgerEventSpec {
		s := base(kind)
		f(&s)
		return s
	}
	fp := strings.Repeat("cd", 32)
	for _, tc := range []struct {
		name string
		spec graphops.LedgerEventSpec
		ok   bool
		msg  string
	}{
		{"mint ok", with(graphops.LedgerMint, func(s *graphops.LedgerEventSpec) { s.ScopeURL = scopeURL }), true, ""},
		{"mint without scope_url", base(graphops.LedgerMint), false, "scope_url is required"},
		{"mint with path", with(graphops.LedgerMint, func(s *graphops.LedgerEventSpec) { s.ScopeURL = scopeURL; s.Path = "beads/x" }), false, "path is not a member"},
		{"mint bad scope_url", with(graphops.LedgerMint, func(s *graphops.LedgerEventSpec) { s.ScopeURL = "https://beads.example/acme" }), false, "scope_url:"},
		{"mint local-test scope_url", with(graphops.LedgerMint, func(s *graphops.LedgerEventSpec) { s.ScopeURL = "https://beads.example/local-test/" }), false, "local-test"},
		{"rotate local-test scope_url", with(graphops.LedgerRotate, func(s *graphops.LedgerEventSpec) { s.ScopeURL = "https://beads.example/local-test/" }), false, "local-test"},
		{"install ok", with(graphops.LedgerInstall, func(s *graphops.LedgerEventSpec) { s.Fingerprint = fp }), true, ""},
		{"install without fingerprint", base(graphops.LedgerInstall), false, "fingerprint is required"},
		{"install bad fingerprint", with(graphops.LedgerInstall, func(s *graphops.LedgerEventSpec) { s.Fingerprint = "xyz" }), false, "fingerprint must be"},
		{"install with revision", with(graphops.LedgerInstall, func(s *graphops.LedgerEventSpec) { s.Fingerprint = fp; s.Revision = rev }), false, "revision is not a member"},
		{"update ok", with(graphops.LedgerUpdate, func(s *graphops.LedgerEventSpec) {
			s.Path = "beads/x"
			s.ResourceKind = graphops.KindBead
			s.Revision = rev
		}), true, ""},
		{"update without revision", with(graphops.LedgerUpdate, func(s *graphops.LedgerEventSpec) { s.Path = "beads/x"; s.ResourceKind = graphops.KindBead }), false, "revision is required"},
		{"update with state", with(graphops.LedgerUpdate, func(s *graphops.LedgerEventSpec) {
			s.Path = "beads/x"
			s.ResourceKind = graphops.KindBead
			s.Revision = rev
			s.State = graphops.AllocationPruned
		}), false, "state is not a member"},
		{"promote ok", base(graphops.LedgerPromote), true, ""},
		{"promote with scope_url", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.ScopeURL = scopeURL }), false, "scope_url is not a member"},
		{"rotate ok", with(graphops.LedgerRotate, func(s *graphops.LedgerEventSpec) { s.ScopeURL = scopeURL }), true, ""},
		{"rotate without scope_url", base(graphops.LedgerRotate), false, "scope_url is required"},
		{"refuse_url ok", with(graphops.LedgerRefuseURL, func(s *graphops.LedgerEventSpec) { s.ScopeURL = scopeURL }), true, ""},
		{"refuse_url with fingerprint", with(graphops.LedgerRefuseURL, func(s *graphops.LedgerEventSpec) { s.ScopeURL = scopeURL; s.Fingerprint = fp }), false, "fingerprint is not a member"},
		{"allocate ok without revision", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) { s.Path = "links/l"; s.ResourceKind = graphops.KindLink }), true, ""},
		{"allocate ok with revision", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) {
			s.Path = "links/l"
			s.ResourceKind = graphops.KindLink
			s.Revision = rev
		}), true, ""},
		{"allocate without resource_kind", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) { s.Path = "links/l" }), false, "resource_kind is required"},
		{"allocate with state", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) {
			s.Path = "links/l"
			s.ResourceKind = graphops.KindLink
			s.State = graphops.AllocationErased
		}), false, "state is not a member"},
		{"allocate bad kind", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) { s.Path = "links/l"; s.ResourceKind = "type" }), false, "resource_kind must be"},
		{"allocate path under wrong root", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) { s.Path = "links/l"; s.ResourceKind = graphops.KindBead }), false, "path:"},
		{"allocate noncanonical path", with(graphops.LedgerAllocate, func(s *graphops.LedgerEventSpec) { s.Path = "beads/caf%c3%a9"; s.ResourceKind = graphops.KindBead }), false, "path:"},
		{"tombstone ok", with(graphops.LedgerTombstone, func(s *graphops.LedgerEventSpec) {
			s.Path = "beads/x"
			s.ResourceKind = graphops.KindBead
			s.State = graphops.AllocationErased
		}), true, ""},
		{"tombstone without state", with(graphops.LedgerTombstone, func(s *graphops.LedgerEventSpec) { s.Path = "beads/x"; s.ResourceKind = graphops.KindBead }), false, "state is required"},
		{"tombstone live", with(graphops.LedgerTombstone, func(s *graphops.LedgerEventSpec) {
			s.Path = "beads/x"
			s.ResourceKind = graphops.KindBead
			s.State = graphops.AllocationLive
		}), false, "state must be pruned or erased"},
		{"tombstone reserved", with(graphops.LedgerTombstone, func(s *graphops.LedgerEventSpec) {
			s.Path = "beads/x"
			s.ResourceKind = graphops.KindBead
			s.State = graphops.AllocationReserved
		}), false, "state must be pruned or erased"},
		{"unknown kind", base(graphops.LedgerEventKind("bogus")), false, "unknown kind"},
		{"bad op_id", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.OpID = "ABC" }), false, "op_id must be"},
		{"uppercase op_id", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.OpID = strings.ToUpper(opID) }), false, "op_id must be"},
		{"bad authority_id", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.AuthorityID = "" }), false, "authority_id must be"},
		{"zero at", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.At = time.Time{} }), false, "at is required"},
		{"bad prev_hash", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.PrevHash = "abc" }), false, "prev_hash must be"},
		{"bad stored hash", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.Hash = strings.Repeat("0", 64) }), false, "does not verify"},
		// Sequence numbers are [1, MaxLedgerSeq]: 0 is the range sentinel,
		// and MaxUint64 is the exhausted counter, never an event.
		{"seq zero", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.Seq = 0 }), false, "seq must be in 1.."},
		{"seq exhausted", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.Seq = math.MaxUint64 }), false, "seq must be in 1.."},
		{"seq at MaxLedgerSeq", with(graphops.LedgerPromote, func(s *graphops.LedgerEventSpec) { s.Seq = graphops.MaxLedgerSeq }), true, ""},
	} {
		_, err := graphops.NewLedgerEvent(tc.spec)
		if tc.ok {
			if err != nil {
				t.Errorf("%s: refused: %v", tc.name, err)
			}
			continue
		}
		wantValidation(t, err, tc.name)
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.msg)
		}
	}
	for _, k := range []graphops.LedgerEventKind{graphops.LedgerMint, graphops.LedgerInstall, graphops.LedgerUpdate, graphops.LedgerPromote,
		graphops.LedgerRotate, graphops.LedgerAllocate, graphops.LedgerTombstone, graphops.LedgerRefuseURL} {
		if !k.Valid() {
			t.Errorf("%s should be a valid kind", k)
		}
	}
	if graphops.LedgerEventKind("").Valid() {
		t.Error("empty kind should not be valid")
	}
}

func TestLedgerManifestCovers(t *testing.T) {
	events := chain(t)
	mint := events[0]
	full, err := graphops.NewLedgerManifest(graphops.LedgerManifestSpec{
		ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 1, LastSeq: 4, PrevHash: graphops.GenesisHash, HeadHash: events[3].Hash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if full.ScopeURL() != scopeURL || full.Lineage() != mint.Hash() || full.FirstSeq() != 1 || full.LastSeq() != 4 ||
		full.PrevHash() != graphops.GenesisHash || full.HeadHash() != events[3].Hash() || full.IsZero() {
		t.Fatal("manifest accessors disagree with the spec")
	}
	if err := full.Covers(events); err != nil {
		t.Fatalf("full range refused: %v", err)
	}
	wantValidation(t, full.Covers(events[:3]), "too few events")
	wantValidation(t, full.Covers(events[1:]), "wrong first seq")
	wantValidation(t, full.Covers([]graphops.LedgerEvent{events[0], events[1], events[3], events[2]}), "broken chain")

	suffix, err := graphops.NewLedgerManifest(graphops.LedgerManifestSpec{
		ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 2, LastSeq: 4, PrevHash: mint.Hash(), HeadHash: events[3].Hash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := suffix.Covers(events[1:]); err != nil {
		t.Fatalf("suffix range refused: %v", err)
	}
	wantValidation(t, suffix.Covers(events[:3]), "suffix with wrong events")

	wrongHead, _ := graphops.NewLedgerManifest(graphops.LedgerManifestSpec{
		ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 1, LastSeq: 4, PrevHash: graphops.GenesisHash, HeadHash: events[2].Hash(),
	})
	wantValidation(t, wrongHead.Covers(events), "wrong head hash")

	foreign, _ := graphops.NewLedgerManifest(graphops.LedgerManifestSpec{
		ScopeURL: scopeURL, Lineage: strings.Repeat("9", 64), FirstSeq: 1, LastSeq: 4, PrevHash: graphops.GenesisHash, HeadHash: events[3].Hash(),
	})
	wantValidation(t, foreign.Covers(events), "foreign lineage")

	// A range claiming to start at genesis must start with the mint event.
	notMint := mustEvent(t, graphops.LedgerEventSpec{
		Seq: 1, Kind: graphops.LedgerPromote, OpID: opID, AuthorityID: authorityID, Epoch: 1, At: at, PrevHash: graphops.GenesisHash,
	})
	single, _ := graphops.NewLedgerManifest(graphops.LedgerManifestSpec{
		ScopeURL: scopeURL, Lineage: notMint.Hash(), FirstSeq: 1, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: notMint.Hash(),
	})
	wantValidation(t, single.Covers([]graphops.LedgerEvent{notMint}), "genesis range without mint")

	for name, spec := range map[string]graphops.LedgerManifestSpec{
		"bad scope url":  {ScopeURL: "https://beads.example/acme", Lineage: mint.Hash(), FirstSeq: 1, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
		"local-test url": {ScopeURL: "https://beads.example/local-test/", Lineage: mint.Hash(), FirstSeq: 1, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
		"bad lineage":    {ScopeURL: scopeURL, Lineage: "x", FirstSeq: 1, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
		"bad prev hash":  {ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 1, LastSeq: 1, PrevHash: "", HeadHash: mint.Hash()},
		"bad head hash":  {ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 1, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: "ABC"},
		"inverted range": {ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 4, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
		// The range is [1, MaxLedgerSeq]: 0..MaxUint64 would make last-first+1
		// wrap to zero, and MaxUint64 is the exhausted counter.
		"zero first seq":  {ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 0, LastSeq: 1, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
		"exhausted range": {ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 1, LastSeq: math.MaxUint64, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
		"full wraparound": {ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: 0, LastSeq: math.MaxUint64, PrevHash: graphops.GenesisHash, HeadHash: mint.Hash()},
	} {
		_, err := graphops.NewLedgerManifest(spec)
		wantValidation(t, err, "manifest "+name)
	}
	if !(graphops.LedgerManifest{}).IsZero() {
		t.Fatal("zero manifest must report IsZero")
	}
	// The top of the range is a valid single-event manifest.
	top, err := graphops.NewLedgerManifest(graphops.LedgerManifestSpec{
		ScopeURL: scopeURL, Lineage: mint.Hash(), FirstSeq: graphops.MaxLedgerSeq, LastSeq: graphops.MaxLedgerSeq, PrevHash: mint.Hash(), HeadHash: mint.Hash(),
	})
	if err != nil || top.FirstSeq() != graphops.MaxLedgerSeq {
		t.Fatalf("manifest at MaxLedgerSeq: %v", err)
	}
	// No events is a refusal, never an index panic — on a constructed
	// manifest and on the zero value alike.
	wantValidation(t, full.Covers(nil), "Covers(nil)")
	wantValidation(t, full.Covers([]graphops.LedgerEvent{}), "Covers(empty)")
	wantValidation(t, (graphops.LedgerManifest{}).Covers(nil), "zero manifest Covers(nil)")
	if uint64(graphops.MaxLedgerSeq) != math.MaxUint64-1 {
		t.Fatalf("MaxLedgerSeq = %d, want MaxUint64-1 so that last_seq + 1 is always representable", uint64(graphops.MaxLedgerSeq))
	}
}
