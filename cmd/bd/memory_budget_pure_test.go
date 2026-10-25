// Pure-Go tests for the `bd remember` corpus budget (memories.budget-chars).
// No cgo / Dolt dependency, so this file carries no build tag and compiles
// under CGO_ENABLED=0 with gms_pure_go, exactly like memory_pure_test.go.
//
// The whole budget decision lives in one pure function (rememberBudgetVerdict),
// so every branch the knob has — off, boundary, overwrite delta, --force, the
// 80% warn — is pinned here without a database. The end-to-end plumbing (the
// stderr routing and the nonzero exit) is pinned against a real store by
// TestEmbeddedMemoryCorpusBudget in memory_embedded_test.go.

package main

import (
	"fmt"
	"strings"
	"testing"
)

// budgetCorpus builds a corpus whose measured size is exactly want bytes,
// under a single key. len(key)+len(content) is the unit the budget counts in,
// so the content is padded to want-len(key).
func budgetCorpus(t *testing.T, key string, want int) map[string]string {
	t.Helper()
	if want < len(key) {
		t.Fatalf("corpus of %d bytes cannot hold key %q (%d bytes)", want, key, len(key))
	}
	corpus := map[string]string{key: strings.Repeat("x", want-len(key))}
	if got := memoryCorpusChars(corpus); got != want {
		t.Fatalf("corpus fixture measured %d bytes, want %d", got, want)
	}
	return corpus
}

// TestMemoryCorpusCharsCountsKeyAndContentBytes pins the UNIT: bytes of
// len(key)+len(content), the same unit prime's --max-memory-chars counts in.
// A rune-counting implementation would disagree on any non-ASCII memory, which
// is why the multibyte case is here.
func TestMemoryCorpusCharsCountsKeyAndContentBytes(t *testing.T) {
	if got := memoryCorpusChars(nil); got != 0 {
		t.Fatalf("empty corpus = %d, want 0", got)
	}
	corpus := map[string]string{"ab": "cde", "f": "gh"}
	if got, want := memoryCorpusChars(corpus), 2+3+1+2; got != want {
		t.Fatalf("corpus = %d, want %d", got, want)
	}
	// "é" is two bytes; the budget must see two.
	if got, want := memoryCorpusChars(map[string]string{"k": "é"}), 3; got != want {
		t.Fatalf("multibyte corpus = %d, want %d (bytes, not runes)", got, want)
	}
}

// TestProjectedMemoryCorpusCharsOverwriteCountsDelta is spec point (c): an
// overwrite REPLACES the key's old content in the sum instead of adding to it.
// Without this, editing one memory down to nothing would still read as growth
// and a corpus at its ceiling could never be repaired.
func TestProjectedMemoryCorpusCharsOverwriteCountsDelta(t *testing.T) {
	existing := map[string]string{"k": "aaaa", "other": "bb"} // 1+4 + 5+2 = 12

	// New key: pure addition.
	if got, want := projectedMemoryCorpusChars(existing, "new", "xyz"), 12+3+3; got != want {
		t.Errorf("new key projection = %d, want %d", got, want)
	}
	// Overwrite with longer content: only the delta lands.
	if got, want := projectedMemoryCorpusChars(existing, "k", "aaaaaa"), 12+2; got != want {
		t.Errorf("growing overwrite projection = %d, want %d", got, want)
	}
	// Overwrite with shorter content: the corpus SHRINKS.
	if got, want := projectedMemoryCorpusChars(existing, "k", "a"), 12-3; got != want {
		t.Errorf("shrinking overwrite projection = %d, want %d", got, want)
	}
	// Identical rewrite: no change at all.
	if got, want := projectedMemoryCorpusChars(existing, "k", "aaaa"), 12; got != want {
		t.Errorf("identical overwrite projection = %d, want %d", got, want)
	}
	// A key whose stored value is empty still has its key counted once, not twice.
	empty := map[string]string{"k": ""}
	if got, want := projectedMemoryCorpusChars(empty, "k", "zz"), 3; got != want {
		t.Errorf("empty-valued overwrite projection = %d, want %d", got, want)
	}
}

// TestRememberBudgetVerdictOffIsSilent is spec point (a): with the budget unset
// (or 0, or a nonsense negative) nothing is ever refused and nothing is ever
// printed, no matter how large the corpus is.
func TestRememberBudgetVerdictOffIsSilent(t *testing.T) {
	huge := budgetCorpus(t, "k", 1_000_000)
	for _, budget := range []int{0, -1, -1000} {
		for _, force := range []bool{false, true} {
			line, refuse := rememberBudgetVerdict(huge, "new", strings.Repeat("y", 5000), budget, force)
			if refuse || line != "" {
				t.Fatalf("budget=%d force=%v: budget-off must be silent, got refuse=%v line=%q", budget, force, refuse, line)
			}
		}
	}
}

// TestMemoryCorpusBudgetReadsConfig pins the config read, including the clamp
// that keeps a negative value meaning OFF rather than "refuse everything".
func TestMemoryCorpusBudgetReadsConfig(t *testing.T) {
	orig := memoryConfigInt
	t.Cleanup(func() { memoryConfigInt = orig })

	values := map[string]int{}
	memoryConfigInt = func(key string) int { return values[key] }

	if got := memoryCorpusBudget(); got != 0 {
		t.Fatalf("unset budget = %d, want 0 (off)", got)
	}
	values["memories.budget-chars"] = 4096
	if got := memoryCorpusBudget(); got != 4096 {
		t.Fatalf("configured budget = %d, want 4096", got)
	}
	values["memories.budget-chars"] = -5
	if got := memoryCorpusBudget(); got != 0 {
		t.Fatalf("negative budget = %d, want 0 (off)", got)
	}
}

// TestRememberBudgetVerdictBoundary is spec point (b): a projection exactly AT
// the budget writes; exactly one byte over refuses. The two cases differ by a
// single byte of content, so nothing but the comparison can explain a failure.
func TestRememberBudgetVerdictBoundary(t *testing.T) {
	const budget = 200
	// Corpus at 100 bytes; the new memory's key costs 3.
	existing := budgetCorpus(t, "old", 100)

	// Exactly at the budget: 100 + 3 + 97 = 200. Writes (and warns: 100%).
	atBudget, refuse := rememberBudgetVerdict(existing, "new", strings.Repeat("z", 97), budget, false)
	if refuse {
		t.Fatalf("a projection exactly at the budget must write, got refusal %q", atBudget)
	}
	if !strings.Contains(atBudget, "at 200 of 200 chars (100%)") {
		t.Errorf("at-budget line = %q, want the 100%% warn line", atBudget)
	}

	// One byte over: 201. Refused.
	over, refuse := rememberBudgetVerdict(existing, "new", strings.Repeat("z", 98), budget, false)
	if !refuse {
		t.Fatalf("a projection one byte over the budget must refuse, got line %q", over)
	}
	// 201 of 200 is 100.5%, and it must NOT round down into "100%": that is the
	// at-budget reading the case just above deliberately ALLOWS, so a floored
	// refusal would wear the number of the case it is not. Over the line the
	// percentage ceilings.
	want := "bd remember: memory corpus would be 201 chars, budget is 200 (101%) — refused; use --force to override"
	if over != want {
		t.Errorf("refusal line =\n  %q\nwant\n  %q", over, want)
	}
}

// TestRememberBudgetVerdictForceWrites is spec point (d): --force turns the
// refusal into a write, and STILL prints a line — a forced crossing that said
// nothing would make the budget invisible exactly when it matters most.
func TestRememberBudgetVerdictForceWrites(t *testing.T) {
	existing := budgetCorpus(t, "old", 100)

	line, refuse := rememberBudgetVerdict(existing, "new", strings.Repeat("z", 98), 200, true)
	if refuse {
		t.Fatalf("--force must not refuse, got refusal %q", line)
	}
	if line == "" {
		t.Fatal("--force over budget must still print a line")
	}
	// (101%), not (100%): the ceiling over the budget is a property of the
	// VALUE, so the same projection cannot report two different percentages
	// depending on whether --force was passed.
	for _, want := range []string{"201", "200", "(101%)", "--force"} {
		if !strings.Contains(line, want) {
			t.Errorf("forced line %q is missing %q", line, want)
		}
	}
	if strings.Contains(line, "refused") {
		t.Errorf("forced line must not claim a refusal: %q", line)
	}
}

// TestRememberBudgetVerdictWarnBand is spec point (e) plus its two edges: below
// 80% of the budget the command is silent (today's behavior), and from 80% up
// to the budget it writes AND warns.
func TestRememberBudgetVerdictWarnBand(t *testing.T) {
	const budget = 1000
	cases := []struct {
		name      string
		projected int
		wantLine  bool
		wantPct   int
	}{
		{"well below", 100, false, 0},
		{"one byte under the warn band", 799, false, 0},
		{"exactly 80 percent", 800, true, 80},
		{"inside the band", 950, true, 95},
		// One byte under: 99.9%, which must FLOOR to 99. Within the budget the
		// rounding goes down, so "100%" belongs to the exactly-at-budget case
		// alone and never to a projection that merely rounds up to it.
		{"one byte under the budget", 999, true, 99},
		{"exactly at budget", 1000, true, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Project by writing a fresh key of known cost into an empty corpus.
			key := "k"
			content := strings.Repeat("q", tc.projected-len(key))
			line, refuse := rememberBudgetVerdict(map[string]string{}, key, content, budget, false)
			if refuse {
				t.Fatalf("projection %d of %d must not refuse", tc.projected, budget)
			}
			if !tc.wantLine {
				if line != "" {
					t.Fatalf("projection %d of %d must be silent, got %q", tc.projected, budget, line)
				}
				return
			}
			want := fmt.Sprintf("bd remember: memory corpus at %d of %d chars (%d%%)", tc.projected, budget, tc.wantPct)
			if line != want {
				t.Errorf("warn line =\n  %q\nwant\n  %q", line, want)
			}
		})
	}
}

// TestMemoryBudgetLinePercentIsUnclamped pins the arithmetic: over the budget
// the percentage is ceil(projected*100/budget) and is NOT clamped at 100, so a
// corpus forced well past its ceiling reports how far past it actually is. A
// clamp here would make 101% and 400% read identically — exactly the reading an
// operator needs. The ceiling is what keeps an over-budget line off the "100%"
// that means at-budget-and-allowed.
func TestMemoryBudgetLinePercentIsUnclamped(t *testing.T) {
	cases := []struct {
		projected, budget int
		want              string
	}{
		{101, 100, "(101%)"},
		{201, 200, "(101%)"}, // ceil(100.5) — never floors back onto at-budget
		{400, 100, "(400%)"}, // exact: ceil and floor agree
		{1001, 1000, "(101%)"},
	}
	for _, tc := range cases {
		line, _ := memoryBudgetLine(tc.projected, tc.budget, false)
		if !strings.Contains(line, tc.want) {
			t.Errorf("memoryBudgetLine(%d, %d) = %q, want it to contain %q", tc.projected, tc.budget, line, tc.want)
		}
	}
}

// TestMemoryBudgetLineHasNoTrailingNewline guards the "ONE line" contract: the
// callers add the newline, so a line that carried its own would print a blank
// line into stderr between the warning and whatever follows.
func TestMemoryBudgetLineHasNoTrailingNewline(t *testing.T) {
	for _, force := range []bool{false, true} {
		line, _ := memoryBudgetLine(500, 100, force)
		if line == "" {
			t.Fatalf("force=%v: expected an over-budget line", force)
		}
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("force=%v: budget line must be a single line, got %q", force, line)
		}
	}
}
