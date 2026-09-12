package graphops

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// VerifyLedgerChain refuses a sequence that wraps around. NewLedgerEvent
// never mints a seq of 0 or MaxUint64, so the events that would exercise
// the check are built by hand here, in-package: the law must hold for any
// value a store could hand back, not only for the ones this package minted.
func TestVerifyLedgerChainRefusesWraparoundAndOutOfRangeSeqs(t *testing.T) {
	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	event := func(seq uint64, prev, hash string) LedgerEvent {
		return LedgerEvent{spec: LedgerEventSpec{Seq: seq, Kind: LedgerPromote, PrevHash: prev, Hash: hash}}
	}
	for name, events := range map[string][]LedgerEvent{
		"MaxUint64 then 0 (the wraparound)": {event(math.MaxUint64, GenesisHash, hashA), event(0, hashA, hashB)},
		"MaxLedgerSeq then 0":               {event(MaxLedgerSeq, GenesisHash, hashA), event(0, hashA, hashB)},
		"seq 0 alone":                       {event(0, GenesisHash, hashA)},
		"seq MaxUint64 alone":               {event(math.MaxUint64, GenesisHash, hashA)},
		"MaxLedgerSeq then MaxUint64":       {event(MaxLedgerSeq, GenesisHash, hashA), event(math.MaxUint64, hashA, hashB)},
	} {
		err := VerifyLedgerChain(GenesisHash, events)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", name, err)
		}
	}
	// The top of the range links normally.
	if err := VerifyLedgerChain(GenesisHash, []LedgerEvent{event(MaxLedgerSeq-1, GenesisHash, hashA), event(MaxLedgerSeq, hashA, hashB)}); err != nil {
		t.Fatalf("a chain ending at MaxLedgerSeq must verify: %v", err)
	}
}

// The URL helpers behind ParseRef are defensive about inputs their one caller
// has already screened (isAbsoluteURI guarantees complete escapes; the
// splitter guarantees a nonempty scheme). Those branches are still part of
// the law's statement, so they are pinned here, in-package, rather than left
// as dead code or removed.

func TestNormalizeEscapesKeepsMalformedInput(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a%41b", "/aAb"},   // unreserved: decoded
		{"/a%2fb", "/a%2Fb"}, // reserved: kept, uppercased
		{"/a%", "/a%"},       // incomplete: kept
		{"/a%4", "/a%4"},     // incomplete: kept
		{"/a%zzb", "/a%zzb"}, // malformed: kept
		{"/plain", "/plain"},
		{"/%7e%7E", "/~~"},
	} {
		if got := normalizeEscapes(tc.in); got != tc.want {
			t.Errorf("normalizeEscapes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRemoveDotSegments(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a/../b", "/b"},
		{"/a/./b/", "/a/b/"},
		{"/../a", "/a"},
		{"/a/..", "/"},
		{"/a/.", "/a/"},
		{"/.", "/"},
		{"/..", "/"},
		{"/a/b/../../c", "/c"},
		{"/", "/"},
	} {
		if got := removeDotSegments(tc.in); got != tc.want {
			t.Errorf("removeDotSegments(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsSchemeToken(t *testing.T) {
	for in, want := range map[string]bool{
		"": false, "http": true, "HTTP": true, "h1+.-": true, "1http": false, "ht tp": false, "ht_tp": false, "-x": false,
	} {
		if got := isSchemeToken(in); got != want {
			t.Errorf("isSchemeToken(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCodeUnitKeyOrdersLikeUTF16(t *testing.T) {
	// U+FFFF (one unit) versus U+10000 (D800 DC00): the lead surrogate sorts
	// first, and two supplementary characters order by their trail units.
	if !(codeUnitKey(0x10000) < codeUnitKey(0xFFFF)) || !(codeUnitKey(0x10000) < codeUnitKey(0x10001)) ||
		!(codeUnitKey('a') < codeUnitKey('b')) || !(codeUnitKey(0xD7FF) < codeUnitKey(0x10000)) {
		t.Fatal("codeUnitKey does not order like UTF-16 code units")
	}
}

// binary64Peer is the admission law's other side: what a binary64 reader
// serializes a literal as, in the package's own canonical form; ok is false
// only outside the range. admissibleInt is the same law over a Go int.
func TestBinary64Peer(t *testing.T) {
	for _, tc := range []struct {
		in, peer string
		ok       bool
	}{
		{"-0.0", "0", true}, {"0", "0", true}, {"1.5e-7", "1.5e-7", true}, {"1e21", "1e+21", true},
		{"0.1000000000000000055511151231257827021181583404541015625", "0.1", true},
		{"9007199254740993", "9007199254740992", true}, {"-9007199254740993", "-9007199254740992", true},
		{"1e-400", "0", true}, {"1e400", "", false}, {"-1e400", "", false},
	} {
		peer, ok := binary64Peer([]byte(tc.in))
		if ok != tc.ok || string(peer) != tc.peer {
			t.Errorf("binary64Peer(%q) = %q, %v; want %q, %v", tc.in, peer, ok, tc.peer, tc.ok)
		}
	}
	for n, want := range map[int]bool{1: true, 9007199254740992: true, 9007199254740994: true, 9007199254740993: false, 1 << 60: false, math.MaxInt64: false} {
		if got := admissibleInt(n); got != want {
			t.Errorf("admissibleInt(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestAbbreviate(t *testing.T) {
	short := strings.Repeat("1", 51)
	if abbreviate([]byte(short)) != short {
		t.Error("a 51-byte literal is shown whole")
	}
	long := strings.Repeat("1", 25) + "X" + strings.Repeat("2", 26) // 52 bytes
	if got := abbreviate([]byte(long)); got != strings.Repeat("1", 24)+"..."+strings.Repeat("2", 24)+" (52 bytes)" {
		t.Errorf("abbreviate = %q", got)
	}
}

func TestJSONPointerEscaping(t *testing.T) {
	s := &jsonScanner{path: []jsonPathToken{{key: "a/b", isKey: true}, {index: 3}, {key: "m~n", isKey: true}, {key: "", isKey: true}}}
	if got := s.pointer(); got != "/a~1b/3/m~0n/" {
		t.Errorf("pointer = %q", got)
	}
	if got := (&jsonScanner{}).pointer(); got != "" {
		t.Errorf("root pointer = %q", got)
	}
}
