package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// embeddedDoltEnv is the variable only //cmd/bd:bd_embedded_test and
// //cmd/bd:bd_embedded_part2_test set for cmd/bd: a cmd/bd test that skips
// without it runs in that lane or in none.
const embeddedDoltEnv = "BEADS_TEST_EMBEDDED_DOLT"

// TestEmbeddedGatedTestsRunInTheEmbeddedLane is
// TestProxiedGatedTestsRunInTheProxiedLane for the embedded-Dolt lane: every
// top-level cmd/bd test that skips unless BEADS_TEST_EMBEDDED_DOLT=1 —
// directly or through any chain of helpers — must be selected by
// .github/scripts/embedded-test-shard.sh, the lane's only test selection. The
// script selects by the TestEmbedded* name prefix alone, so a gated test named
// otherwise skips in every CI lane and its pass means nothing: 92 did,
// including #7203's --if-revision CLI coverage, until they were renamed into
// the prefix.
func TestEmbeddedGatedTestsRunInTheEmbeddedLane(t *testing.T) {
	requireAutofixBash(t)
	root := sourceRepoRoot(t)

	gated, err := gatedTests(filepath.Join(root, "cmd/bd"), embeddedDoltEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(gated) < 300 {
		t.Fatalf("found only %d cmd/bd tests gated on %s; did the gate helpers change shape?", len(gated), embeddedDoltEnv)
	}

	listed, err := laneListedTests(root, ".github/scripts/embedded-test-shard.sh", bazelEmbeddedCmdShardCount(t))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, name := range gated {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d cmd/bd test(s) skip unless %s=1 but .github/scripts/embedded-test-shard.sh never selects them, so no CI lane runs them; rename them into the TestEmbedded* prefix (for a test gated in only some of its subtests, move those subtests into a TestEmbedded* test of their own instead, so the rest keep running in the other lanes) and place them with scripts/ci/gen_embedded_cmd_shard_manifest.py --write:\n  %s",
			len(missing), embeddedDoltEnv, strings.Join(missing, "\n  "))
	}
}

// TestEmbeddedGatedTestsDetector pins gatedTests for the embedded variable
// against a fixture, so a regression in the walk cannot silently turn the
// guard above into a no-op, and a test gated on another lane's variable is not
// demanded here.
func TestEmbeddedGatedTestsDetector(t *testing.T) {
	dir := t.TempDir()
	// Every fixture line is indented one tab, so repo scanners that find
	// tests by a line-anchored `^func TestX(t *testing.T)` (equivalence.py)
	// do not mistake the fixture's functions for this package's tests.
	const src = `package main

	import (
		"os"
		"testing"
	)

	func requireEmbedded(t *testing.T) {
		if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
			t.Skip("gated")
		}
	}

	func requireProxied(t *testing.T) {
		if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
			t.Skip("another lane")
		}
	}

	func buildBD(t *testing.T) string { requireEmbedded(t); return "bd" }

	type harness struct{}

	func (harness) start(t *testing.T) { buildBD(t) }

	func TestDirect(t *testing.T)     { requireEmbedded(t) }
	func TestTransitive(t *testing.T) { _ = buildBD(t) }
	func TestMethod(t *testing.T)     { harness{}.start(t) }
	func TestInSubtest(t *testing.T) {
		t.Run("x", func(t *testing.T) { _ = buildBD(t) })
	}
	func TestInline(t *testing.T) {
		if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
			t.Skip("gated")
		}
	}
	func TestOtherLane(t *testing.T) { requireProxied(t) }
	func TestUngated(t *testing.T)   {}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := gatedTests(dir, embeddedDoltEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TestDirect", "TestInSubtest", "TestInline", "TestMethod", "TestTransitive"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gatedTests(%s) = %v, want %v", embeddedDoltEnv, got, want)
	}
}
