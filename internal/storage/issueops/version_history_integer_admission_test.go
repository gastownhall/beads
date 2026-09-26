package issueops

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func issueWithMetadata(meta string) types.Issue {
	return types.Issue{ID: "be-1", Title: "t", Metadata: json.RawMessage(meta)}
}

// A metadata integer past 2^53 used to be ROUNDED by jcs.Transform, which
// collapsed two distinct issue states onto one durable_state and therefore
// one content token. It must be refused instead, so the loss is loud.
func TestCanonicalDurableStateRefusesIntegerPastBinary64(t *testing.T) {
	_, err := canonicalDurableState(issueWithMetadata(`{"n":9007199254740993}`))
	if err == nil {
		t.Fatal("canonicalDurableState(metadata 9007199254740993) = nil error, want a refusal")
	}
	if !errors.Is(err, ErrIntegerNotRepresentable) {
		t.Fatalf("error = %v, want ErrIntegerNotRepresentable", err)
	}
}

// The defect this admission gate exists to prevent, stated directly: two
// DISTINCT states must never render to the same bytes.
func TestCanonicalDurableStateNeverCollapsesTwoDistinctStates(t *testing.T) {
	// Before the admission gate, BOTH of these canonicalized, and both to
	// the same bytes: 2^53+1 rounded down onto 2^53. One content token for
	// two live states. Now the unrepresentable side is refused, so the pair
	// can never be confused for one another.
	a, errA := canonicalDurableState(issueWithMetadata(`{"n":9007199254740993}`))
	b, errB := canonicalDurableState(issueWithMetadata(`{"n":9007199254740991}`))
	if errA == nil && errB == nil && string(a) == string(b) {
		t.Fatalf("two distinct states produced identical durable_state %s", a)
	}
	if errA == nil {
		t.Fatalf("the unrepresentable state canonicalized instead of being refused: %s", a)
	}
	if errB != nil {
		t.Fatalf("the representable state was refused: %v", errB)
	}
}

// The bound itself, both sides of it, bare-integer spelling.
func TestCanonicalDurableStateIntegerBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		meta    string
		refused bool
	}{
		{"2^53-1 accepted", `{"n":9007199254740991}`, false},
		{"2^53 refused", `{"n":9007199254740992}`, true},
		{"2^53+1 refused", `{"n":9007199254740993}`, true},
		{"negative past bound refused", `{"n":-9007199254740993}`, true},
		{"beyond int64 refused", `{"n":18446744073709551616}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := canonicalDurableState(issueWithMetadata(tc.meta))
			if tc.refused && err == nil {
				t.Fatalf("%s: nil error, want refusal", tc.meta)
			}
			if !tc.refused && err != nil {
				t.Fatalf("%s: %v, want acceptance", tc.meta, err)
			}
		})
	}
}

// Non-integer literals must keep working: they are not exactly representable
// either, but ES6 shortest-round-trip returns them to the same binary64
// value, so they neither round to a different value nor collide.
//
// 1e300 is deliberately NOT in this list: it is a mathematical integer
// (10^300) whose magnitude vastly exceeds 2^53-1, so it is refused like any
// other unrepresentable integer, regardless of spelling. See
// TestCanonicalDurableStateRefusesHugeExponentInteger below.
func TestCanonicalDurableStateStillAcceptsOrdinaryDecimals(t *testing.T) {
	for _, meta := range []string{`{"n":0.1}`, `{"n":1.0}`, `{"n":-2.5}`, `{"n":0}`} {
		if _, err := canonicalDurableState(issueWithMetadata(meta)); err != nil {
			t.Fatalf("canonicalDurableState(%s) = %v, want acceptance", meta, err)
		}
	}
}

// 1e300 is IsInt()==true (it denotes the integer 10^300) at a magnitude
// vastly past 2^53-1, and is subject to the same collision defect this gate
// exists to close -- binary64 spacing near 10^300 is roughly 2^248, so a wide
// range of distinct large integers near 10^300 collapse onto that one
// double, same failure mode as the 2^53 boundary, larger window. The mayor's
// spec for this predicate is "value not form, any spelling, no exceptions"
// (see version_history.go's refuseUnrepresentableIntegers doc comment),
// applied literally that refuses 1e300 -- but two pre-gate tests assumed it
// stayed accepted, one of which lived right here until this admission gate
// (see version_history_durable_state_test.go's "huge" fixture, moved off
// 1E300 for the same reason).
//
// RESOLVED (mayor, gm-wisp-3f2w8, be-wdlod): REFUSE 1e300. The rule applies
// literally, with no notation carve-out -- admitting a spelling this large
// while refusing 9007199254740993 would be indefensible, since the collision
// window near 10^300 (binary64 spacing ~2^248) is vastly larger than the one
// at the 2^53 boundary this gate was built to close. This is final, not
// provisional.
func TestCanonicalDurableStateRefusesHugeExponentInteger(t *testing.T) {
	_, err := canonicalDurableState(issueWithMetadata(`{"n":1e300}`))
	if err == nil {
		t.Fatal("canonicalDurableState(metadata 1e300) = nil error, want a refusal")
	}
	if !errors.Is(err, ErrIntegerNotRepresentable) {
		t.Fatalf("error = %v, want ErrIntegerNotRepresentable", err)
	}
}

// THE GAP QA FOUND IN THE FIRST CUT OF THIS GATE. The first patch's
// refuseUnrepresentableIntegers skipped any literal containing '.', 'e', or
// 'E' outright, reasoning that non-integer literals round-trip losslessly
// through ES6 shortest-round-trip. That reasoning covers genuinely
// fractional literals (0.1, 1.5) but not integer-VALUED literals written in
// decimal or exponential FORM: 9007199254740993.0, 9.007199254740993e15, and
// 90071992547409930e-1 all denote the same integer VALUE as the bare literal
// 9007199254740993, and all three walked straight past the first gate and
// canonicalized to the identical colliding target 9007199254740992 -- the
// exact defect class this gate exists to close, just reachable through
// spelling instead of magnitude.
//
// The predicate under test must inspect the VALUE (e.g. via math/big,
// exactly, with no float64 in the decision path -- float64 is the very
// thing being tested for) rather than the literal's spelling, so it must
// refuse and accept the same values regardless of which of the three JSON
// number forms (integer, decimal, exponent) carries them. This is pinned
// here for the bound itself; ordinary small decimals are covered separately
// by TestCanonicalDurableStateStillAcceptsOrdinaryDecimals.
func TestCanonicalDurableStateIntegerBoundaryAllSpellings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lit     string
		refused bool
	}{
		// at the bound (2^53-1 = 9007199254740991): every spelling accepted.
		{"at bound, bare", `9007199254740991`, false},
		{"at bound, decimal", `9007199254740991.0`, false},
		{"at bound, exponent", `9.007199254740991e15`, false},

		// one past the bound (2^53+1 = 9007199254740993): every spelling
		// refused. The decimal and exponent rows are the QA-found gap.
		{"past bound, bare", `9007199254740993`, true},
		{"past bound, decimal (THE GAP)", `9007199254740993.0`, true},
		{"past bound, exponent (THE GAP)", `9.007199254740993e15`, true},
		{"past bound, exponent negative-exp form (THE GAP)", `90071992547409930e-1`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := `{"n":` + tc.lit + `}`
			_, err := canonicalDurableState(issueWithMetadata(meta))
			if tc.refused && err == nil {
				t.Fatalf("metadata %s: nil error, want refusal", meta)
			}
			if !tc.refused && err != nil {
				t.Fatalf("metadata %s: %v, want acceptance", meta, err)
			}
			if tc.refused && !errors.Is(err, ErrIntegerNotRepresentable) {
				t.Fatalf("metadata %s: error = %v, want ErrIntegerNotRepresentable", meta, err)
			}
		})
	}
}

// Same property as TestCanonicalDurableStateNeverCollapsesTwoDistinctStates,
// spelled in decimal form -- the exact QA-found gap stated as a distinctness
// property rather than a table row: two states differing only in a
// decimal-spelled metadata integer past 2^53 must never render to the same
// bytes.
func TestCanonicalDurableStateNeverCollapsesDecimalSpellingOfDistinctStates(t *testing.T) {
	a, errA := canonicalDurableState(issueWithMetadata(`{"n":9007199254740993.0}`))
	b, errB := canonicalDurableState(issueWithMetadata(`{"n":9007199254740991.0}`))
	if errA == nil && errB == nil && string(a) == string(b) {
		t.Fatalf("two distinct states produced identical durable_state %s", a)
	}
	if errA == nil {
		t.Fatalf("the unrepresentable state (9007199254740993.0) canonicalized instead of being refused: %s", a)
	}
	if errB != nil {
		t.Fatalf("the representable state (9007199254740991.0) was refused: %v", errB)
	}
}
