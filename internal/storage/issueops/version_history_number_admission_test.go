package issueops

import (
	"errors"
	"math"
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// admitNumber runs the admission gate over one JSON number literal in the
// position the mint meets it: an object member of an issue's metadata.
func admitNumber(lit string) error {
	return refuseUnrepresentableIntegers([]byte(`{"n":` + lit + `}`))
}

// The admission rule, stated once for the tests below: a JSON number literal
// is admitted into a durable_state if and only if the binary64 nearest to it
// has magnitude at most 2^53-1, whatever the literal's spelling (integer,
// decimal or exponent), and a literal that overflows binary64 is refused.
// Ordinary fractions such as 0.1 stay admitted, and so do magnitudes too small
// for binary64, which round to zero.

// A fraction whose nearest double lies at or beyond 2^53 used to be admitted:
// the old rule looked only at literals that denote an integer VALUE, and
// 9007199254740993.5 is not one. jcs.Transform then rounds it to the integer
// 9007199254740994, so two distinct issue states shared one durable_state,
// which is exactly what the gate exists to prevent. Refusing must depend on
// where the nearest double falls, not on whether the literal looks integral.
func TestNumberAdmissionRefusesFractionsWhoseNearestDoubleIsPastTheBound(t *testing.T) {
	for _, tc := range []struct {
		lit     string
		refused bool
		why     string
	}{
		{"9007199254740993.5", true, "nearest double is 2^53+2"},
		{"9007199254740994.5", true, "nearest double is 2^53+2"},
		{"9007199254740991.5", true, "a tie between 2^53-1 and 2^53 that rounds to the even 2^53"},
		{"9007199254740991.75", true, "nearest double is 2^53"},
		{"-9007199254740993.5", true, "magnitude decides, not sign"},
		{"9007199254740990.5", false, "a tie that rounds to the even 2^53-2, inside the range"},
		{"9007199254740991.25", false, "nearest double is 2^53-1, inside the range"},
		{"9007199254740991.0", false, "exactly 2^53-1"},
	} {
		t.Run(tc.lit, func(t *testing.T) {
			err := admitNumber(tc.lit)
			if tc.refused {
				if !errors.Is(err, ErrIntegerNotRepresentable) {
					t.Fatalf("admitNumber(%s) = %v, want ErrIntegerNotRepresentable (%s)", tc.lit, err, tc.why)
				}
				if _, mintErr := canonicalDurableState(issueWithMetadata(`{"n":` + tc.lit + `}`)); !errors.Is(mintErr, ErrIntegerNotRepresentable) {
					t.Fatalf("canonicalDurableState(metadata %s) = %v, want ErrIntegerNotRepresentable", tc.lit, mintErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("admitNumber(%s) = %v, want acceptance (%s)", tc.lit, err, tc.why)
			}
		})
	}
}

// numberAdmissionCorpus is every literal the closure property is checked
// over: the boundary cases the rule was written around, every spelling of the
// values on either side of 2^53, and pseudo-random doubles in three renderings.
// The seed is fixed so a failure names a literal that reproduces.
func numberAdmissionCorpus() []string {
	corpus := []string{
		"0", "-0", "0.0", "-0.0", "1", "1.0", "-2.5", "0.1", "3.14159265358979323846", "0.30000000000000004",
		"5e-324", "4.9e-324", "2.2250738585072014e-308", "1e-7", "1e-400", "1e-100000000",
		"9007199254740991", "9007199254740992", "9007199254740993", "9.007199254740991e15", "90071992547409930e-1",
		"9007199254740993.5", "9007199254740994.5", "9007199254740991.5", "9007199254740990.5",
		"1727000000000000000", "1727000000000000123", "1e21", "1e300", "1.7976931348623157e308",
		"123456789012345678901234567890", "18446744073709551616",
	}
	// Every spelling of the integers and near-integers either side of the bound.
	for delta := int64(-6); delta <= 6; delta++ {
		base := strconv.FormatInt(int64(1)<<53+delta, 10)
		for _, frac := range []string{"", ".0", ".25", ".5", ".75", ".999999"} {
			corpus = append(corpus, base+frac, "-"+base+frac)
		}
	}
	rng := rand.New(rand.NewSource(20260930))
	for _, scale := range []float64{1, 1e3, 1e9, 1e15, 1e-9} {
		for range 60 {
			f := (rng.Float64()*2 - 1) * scale
			corpus = append(corpus,
				strconv.FormatFloat(f, 'g', -1, 64),
				strconv.FormatFloat(f, 'e', 17, 64),
				strconv.FormatFloat(f, 'f', -1, 64))
		}
	}
	for range 200 {
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		corpus = append(corpus, strconv.FormatFloat(f, 'g', -1, 64), strconv.FormatFloat(f, 'e', -1, 64))
	}
	return corpus
}

// The gate must be closed under its own canonicalization: whatever it admits
// canonicalizes to a form it also admits. Otherwise a value can pass at the
// door and become unmintable one step later, which is what 9007199254740991.5
// did (it rounds to 2^53, and the canonical bytes are then refused). Checked as
// a property over a corpus, not by example.
func TestNumberAdmissionIsClosedUnderCanonicalization(t *testing.T) {
	admitted := 0
	for _, lit := range numberAdmissionCorpus() {
		doc := []byte(`{"n":` + lit + `}`)
		if err := refuseUnrepresentableIntegers(doc); err != nil {
			continue // refused literals are outside the property
		}
		admitted++
		canonical, err := jcsCanonicalize(doc)
		if err != nil {
			t.Errorf("literal %s: jcsCanonicalize: %v", lit, err)
			continue
		}
		if err := refuseUnrepresentableIntegers(canonical); err != nil {
			t.Errorf("literal %s is admitted but its canonical form %s is refused: %v", lit, canonical, err)
		}
	}
	if admitted < 100 {
		t.Fatalf("only %d corpus literals were admitted; the property is close to vacuous", admitted)
	}
}

// The classification must not depend on how large an exponent the literal
// spells. The old rule parsed every literal as an exact rational, so a huge
// exponent either cost a million-digit integer or fell outside what the
// parser accepts and came back as an invalid literal, a different class of
// error from the refusal it is. Overflow is a refusal; a magnitude too small
// for binary64 is admitted.
func TestNumberAdmissionClassifiesByMagnitudeWhateverTheExponent(t *testing.T) {
	for _, tc := range []struct {
		lit     string
		refused bool
	}{
		{"1e100000000", true},
		{"1e1000000", true},
		{"1e309", true},
		{"-1e400", true},
		{"1.7976931348623159e308", true},
		{"1e-100000000", false},
		{"1e-400", false},
		{"-1e-400", false},
		{"5e-324", false},
	} {
		t.Run(tc.lit, func(t *testing.T) {
			err := admitNumber(tc.lit)
			switch {
			case tc.refused && !errors.Is(err, ErrIntegerNotRepresentable):
				t.Fatalf("admitNumber(%s) = %v, want ErrIntegerNotRepresentable", tc.lit, err)
			case !tc.refused && err != nil:
				t.Fatalf("admitNumber(%s) = %v, want acceptance", tc.lit, err)
			}
		})
	}
}

// The gate allocates nothing proportional to an exponent. Measured as an
// allocation count and never as wall time, so a loaded machine cannot flake it,
// and it is what distinguishes a classifier that builds a million-digit integer
// from one that does not.
//
// The count is exact only without the race detector. Under it sync.Pool drops
// one Put in four at random (sync/pool.go), and the refusal builds its error with
// fmt.Errorf, whose printer comes from a pool, so a call now and then allocates a
// printer the pool would have supplied: a literal that costs 22 allocations reads
// 23, or 24, on a run where a printer was dropped, and comparing two
// independently averaged samples with a strict > is a coin flip. That noise only
// ever ADDS allocations, so the fewest allocations over many single-call samples
// is the count without it, and it is the same on every run. The comparison stays
// strict, with no margin to tune, and it loses no sensitivity: a classifier that
// builds per-exponent state does so on every call, so it raises the fewest too.
func TestNumberAdmissionAllocationsDoNotGrowWithTheExponent(t *testing.T) {
	// The chance that every one of these samples is noisy is under one in 10^19.
	const samples = 32
	fewest := func(lit string) float64 {
		doc := []byte(`{"n":` + lit + `}`)
		least := math.Inf(1)
		for range samples {
			least = math.Min(least, testing.AllocsPerRun(1, func() { _ = refuseUnrepresentableIntegers(doc) }))
		}
		return least
	}
	baseline := fewest("1e400")
	for _, lit := range []string{"1e1000000", "1e100000000", "-1e1000000"} {
		if got := fewest(lit); got > baseline {
			t.Errorf("%s allocates %v times per call against %v for 1e400: allocation grows with the exponent", lit, got, baseline)
		}
	}
}

// TestNumberAdmissionBytesDoNotGrowWithTheExponent is the count bound's twin, and finding 6
// of the #6661 review. The count bound passes a classifier that builds the number it is
// asked about: one big-integer allocation is one allocation whether it holds 400 bits or a
// hundred million. What grows with the exponent is then the size of the allocation, so this
// bounds bytes: the fewest bytes one call allocates for a literal with a huge exponent is
// no more than for a small one, plus a margin and a few bytes for each byte of literal (a
// refusal names the literal in its message).
//
// The fewest over many single-call samples, as in the count bound and for the same reason:
// whatever else allocates while a sample runs only ever ADDS bytes, so the minimum is the
// cost of the call itself. TotalAlloc is read before and after each call, with GOMAXPROCS
// pinned to one for the measurement as testing.AllocsPerRun does, so a background
// goroutine does not add to a sample.
func TestNumberAdmissionBytesDoNotGrowWithTheExponent(t *testing.T) {
	const (
		samples      = 32
		smallLiteral = "1e400"
		// What the bound allows beyond the small literal's bytes: a margin, and a few
		// bytes for each byte a longer literal adds to the refusal's message.
		slackBytes = 1024
		perLitByte = 16
	)
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	fewest := func(lit string) uint64 {
		doc := []byte(`{"n":` + lit + `}`)
		least := uint64(math.MaxUint64)
		var before, after runtime.MemStats
		for range samples {
			runtime.ReadMemStats(&before)
			_ = refuseUnrepresentableIntegers(doc)
			runtime.ReadMemStats(&after)
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		return least
	}
	baseline := fewest(smallLiteral)
	for _, lit := range []string{"1e1000000", "1e100000000", "-1e1000000"} {
		limit := baseline + slackBytes + perLitByte*uint64(max(len(lit)-len(smallLiteral), 0))
		if got := fewest(lit); got > limit {
			t.Errorf("%s allocates %d bytes per call against %d for %s (limit %d): bytes grow with the exponent", lit, got, baseline, smallLiteral, limit)
		}
	}
}

// The sentinel names the range it enforces. Its old wording said a literal
// was not exactly representable, which no longer describes what is refused:
// 9007199254740993.5 is a fraction, and the rule is about magnitude. The
// identifier keeps its historical name so nothing that matches it with
// errors.Is has to change; the text is what a person reads.
func TestIntegerNotRepresentableTextNamesTheIJSONRange(t *testing.T) {
	const want = "number is outside the I-JSON exact-integer range (magnitude above 2^53-1)"
	got := ErrIntegerNotRepresentable.Error()
	if got != want {
		t.Fatalf("ErrIntegerNotRepresentable.Error() = %q, want %q", got, want)
	}
	if !strings.Contains(got, "I-JSON") {
		t.Errorf("sentinel text %q does not name I-JSON", got)
	}
	if strings.Contains(got, "cannot represent") {
		t.Errorf("sentinel text %q still uses the old wording", got)
	}
	err := admitNumber("9007199254740993")
	if err == nil || err.Error() != want+": 9007199254740993" {
		t.Fatalf("refusal text = %v, want the sentinel followed by the offending literal", err)
	}
}

// Nothing the store already accepted becomes admitted or refused differently.
// Dolt does not hand a literal back the way it was written: an integer up to
// int64 is stored exactly and every other number is stored as a double, so what
// the mint sees is the right-hand column. Each row pins the verdict on that
// column, which is what production meets. The verdicts are the ones the rule
// had before it changed, so this stays green through the change and fails only
// if the new rule moves one.
func TestNumberAdmissionKeepsTheVerdictOnEveryLiteralAsTheStoreReturnsIt(t *testing.T) {
	for _, tc := range []struct {
		written  string
		returned string // what a Dolt JSON column hands back for it
		refused  bool
	}{
		{"9007199254740993", "9007199254740993", true},
		{"1727000000000000123", "1727000000000000123", true},
		{"9007199254740993.5", "9007199254740994", true},
		{"1.0", "1", false},
		{"0.1", "0.1", false},
		{"3.14159265358979323846", "3.141592653589793", false},
		{"1e300", "1" + strings.Repeat("0", 300), true},
		{"123456789012345678901234567890", "123456789012345680000000000000", true},
	} {
		t.Run(tc.written, func(t *testing.T) {
			err := admitNumber(tc.returned)
			if tc.refused && !errors.Is(err, ErrIntegerNotRepresentable) {
				t.Fatalf("admitNumber(%s), the store's rendering of %s = %v, want a refusal", tc.returned, tc.written, err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("admitNumber(%s), the store's rendering of %s = %v, want acceptance", tc.returned, tc.written, err)
			}
		})
	}
}
