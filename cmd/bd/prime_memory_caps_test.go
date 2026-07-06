package main

import (
	"fmt"
	"strings"
	"testing"
)

func testMemories(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("mem-%02d", i)
		m[key] = fmt.Sprintf("insight body for %s", key)
	}
	return m
}

func TestRenderPrimeMemoriesUncappedMatchesLegacyShape(t *testing.T) {
	out := renderPrimeMemories(testMemories(3), false, 0, 0)

	if !strings.Contains(out, "## Persistent Memories (3)\n") {
		t.Fatalf("expected legacy uncapped header, got %q", out)
	}
	if strings.Contains(out, "not shown") || strings.Contains(out, "showing") {
		t.Fatalf("uncapped output must not carry an elision banner, got %q", out)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(out, fmt.Sprintf("### mem-%02d\n", i)) {
			t.Fatalf("missing memory %d in %q", i, out)
		}
	}
}

func TestRenderPrimeMemoriesCountCap(t *testing.T) {
	out := renderPrimeMemories(testMemories(5), false, 2, 0)

	if !strings.Contains(out, "## Persistent Memories (showing 2 of 5, alphabetical)\n") {
		t.Fatalf("expected capped header, got %q", out)
	}
	if !strings.Contains(out, "> 3 more memories are not shown here (capped by max-memories=2).") {
		t.Fatalf("expected elision banner, got %q", out)
	}
	if !strings.Contains(out, "### mem-00\n") || !strings.Contains(out, "### mem-01\n") {
		t.Fatalf("expected first two alphabetical memories, got %q", out)
	}
	if strings.Contains(out, "### mem-02\n") {
		t.Fatalf("memory beyond the cap leaked into output: %q", out)
	}
	if banner, entries := strings.Index(out, "not shown"), strings.Index(out, "### mem-00"); banner > entries {
		t.Fatalf("elision banner must precede entries so host truncation cannot hide it: %q", out)
	}
}

func TestRenderPrimeMemoriesCharCapStopsAtWholeMemoryBoundary(t *testing.T) {
	memories := testMemories(4)
	oneEntry := len(fmt.Sprintf("### mem-00\n%s\n\n", memories["mem-00"]))
	out := renderPrimeMemories(memories, false, 0, oneEntry+5)

	if !strings.Contains(out, "showing 1 of 4") {
		t.Fatalf("expected exactly one memory within budget, got %q", out)
	}
	if !strings.Contains(out, "capped by max-memory-chars=") {
		t.Fatalf("banner must name the character cap, got %q", out)
	}
	if strings.Contains(out, "### mem-01\n") {
		t.Fatalf("second memory should not fit the budget: %q", out)
	}
}

func TestRenderPrimeMemoriesCharCapAlwaysEmitsFirstMemory(t *testing.T) {
	memories := map[string]string{"huge": strings.Repeat("x", 4096)}
	out := renderPrimeMemories(memories, false, 0, 100)

	if !strings.Contains(out, "### huge\n") {
		t.Fatalf("an oversized first memory must still be emitted, got %q", out)
	}
	if strings.Contains(out, "not shown") {
		t.Fatalf("nothing was elided, banner must be absent: %q", out)
	}
}

func TestRenderPrimeMemoriesCompactCaps(t *testing.T) {
	out := renderPrimeMemories(testMemories(4), true, 3, 0)

	if !strings.Contains(out, "## Memories (showing 3 of 4)\n") {
		t.Fatalf("expected compact capped header, got %q", out)
	}
	if !strings.Contains(out, "- 1 more not shown (capped by max-memories=3); browse with `bd memories <keyword>`\n") {
		t.Fatalf("expected compact elision line, got %q", out)
	}
	if strings.Contains(out, "mem-03") {
		t.Fatalf("memory beyond the cap leaked into compact output: %q", out)
	}
}

func TestRenderPrimeMemoriesCompactUncappedUnchanged(t *testing.T) {
	out := renderPrimeMemories(testMemories(2), true, 0, 0)

	if !strings.Contains(out, "\n## Memories\n") || strings.Contains(out, "showing") {
		t.Fatalf("uncapped compact output changed shape: %q", out)
	}
}

func TestRenderPrimeMemoriesBannerNamesOnlyBindingCap(t *testing.T) {
	// 5 memories, count cap = 50 (won't bind), char cap small enough to stop at ~2 entries.
	// One entry is roughly: "### mem-00\ninsight body for mem-00\n\n" ~ 40+ bytes.
	// Set char cap to allow only 1 entry so char cap fires, not count cap.
	memories := testMemories(5)
	firstEntry := fmt.Sprintf("### mem-00\n%s\n\n", memories["mem-00"])
	smallCharBudget := len(firstEntry) + 5 // fits exactly 1 entry

	out := renderPrimeMemories(memories, false, 50, smallCharBudget)

	if !strings.Contains(out, "capped by max-memory-chars=") {
		t.Fatalf("banner must name the char cap that fired, got %q", out)
	}
	if strings.Contains(out, "max-memories") {
		t.Fatalf("banner must NOT name max-memories when it did not fire, got %q", out)
	}
}

func TestPrimeMemoryCapsResolution(t *testing.T) {
	oldMax, oldChars := primeMaxMemories, primeMaxMemoryChars
	oldMaxSet, oldCharsSet := primeMaxMemoriesSet, primeMaxMemoryCharsSet
	oldConfigInt := primeConfigInt
	t.Cleanup(func() {
		primeMaxMemories, primeMaxMemoryChars = oldMax, oldChars
		primeMaxMemoriesSet, primeMaxMemoryCharsSet = oldMaxSet, oldCharsSet
		primeConfigInt = oldConfigInt
	})
	configValues := map[string]int{}
	primeConfigInt = func(key string) int { return configValues[key] }

	// No flags, no config: uncapped.
	primeMaxMemories, primeMaxMemoryChars = 0, 0
	primeMaxMemoriesSet, primeMaxMemoryCharsSet = false, false
	if c, ch := primeMemoryCaps(); c != 0 || ch != 0 {
		t.Fatalf("expected uncapped defaults, got %d/%d", c, ch)
	}

	// Config keys apply when flags are absent.
	configValues["prime.max-memories"] = 20
	configValues["prime.max-memory-chars"] = 25000
	if c, ch := primeMemoryCaps(); c != 20 || ch != 25000 {
		t.Fatalf("expected config caps 20/25000, got %d/%d", c, ch)
	}

	// An explicit flag wins over config.
	primeMaxMemories, primeMaxMemoriesSet = 5, true
	if c, _ := primeMemoryCaps(); c != 5 {
		t.Fatalf("flag must beat config, got %d", c)
	}

	// An explicit 0 flag forces unlimited despite config.
	primeMaxMemories = 0
	if c, _ := primeMemoryCaps(); c != 0 {
		t.Fatalf("explicit --max-memories 0 must force unlimited, got %d", c)
	}

	// Negative values clamp to uncapped.
	primeMaxMemories, primeMaxMemoriesSet = -3, true
	primeMaxMemoryChars, primeMaxMemoryCharsSet = -1, true
	if c, ch := primeMemoryCaps(); c != 0 || ch != 0 {
		t.Fatalf("negative caps must clamp to 0, got %d/%d", c, ch)
	}
}
