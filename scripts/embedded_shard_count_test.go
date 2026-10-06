package scripts_test

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// shardCountPattern matches a rule block's `    shard_count = N,` line, the
// same pattern bazelProxiedShardCount (scripts/ci_workflow_test.go) inlines
// for its own single use; shared here since both embedded accessors below
// need it.
var shardCountPattern = regexp.MustCompile(`(?m)^    shard_count = (\d+),$`)

// bazelEmbeddedCmdShardCount returns cmd/bd:bd_embedded_test's own
// shard_count from cmd/bd/BUILD.bazel: the single source of truth for the
// Bazel-only bazel-embedded lane's cmd/bd shard split (slice F1), which no
// longer has to equal PR Risk's/main.yml's legacy "Test (Embedded Dolt Cmd
// N/20)" fork/push jobs' matrix size — mirrors bazelProxiedShardCount (F2;
// see that function's doc comment in scripts/ci_workflow_test.go for the
// shared rationale, not repeated here). Under `bazel test`, scripts_test's
// runfiles hold no other package's BUILD file, so this falls back to the
// literal TestBazelRetiredLanesCheckListedTestsRan pins under plain `go
// test` for //cmd/bd:bd_embedded_test; that test fails if cmd/bd/BUILD.bazel's
// shard_count ever drifts from this fallback.
func bazelEmbeddedCmdShardCount(t *testing.T) int {
	t.Helper()
	const bazelTestFallback = 50
	if os.Getenv("TEST_SRCDIR") != "" {
		return bazelTestFallback
	}
	root := sourceRepoRoot(t)
	rule := bazelRuleBlock(readPolicyFile(t, root, "cmd/bd/BUILD.bazel"), "bd_embedded_test")
	m := shardCountPattern.FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("cmd/bd:bd_embedded_test has no `shard_count = N,` in cmd/bd/BUILD.bazel:\n%s", rule)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("cmd/bd:bd_embedded_test shard_count: %v", err)
	}
	return n
}

// bazelEmbeddedStorageShardCount returns
// embeddeddolt:embeddeddolt_embedded_test's own shard_count from
// internal/storage/embeddeddolt/BUILD.bazel: the single source of truth for
// the Bazel-only bazel-embedded lane's storage shard split (slice F1), which
// no longer has to equal PR Risk's/main.yml's legacy "Test (Embedded Dolt
// Storage N/5)" fork/push jobs' matrix size. Mirrors
// bazelEmbeddedCmdShardCount above; see its doc comment.
func bazelEmbeddedStorageShardCount(t *testing.T) int {
	t.Helper()
	const bazelTestFallback = 15
	if os.Getenv("TEST_SRCDIR") != "" {
		return bazelTestFallback
	}
	root := sourceRepoRoot(t)
	rule := bazelRuleBlock(readPolicyFile(t, root, "internal/storage/embeddeddolt/BUILD.bazel"), "embeddeddolt_embedded_test")
	m := shardCountPattern.FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("embeddeddolt:embeddeddolt_embedded_test has no `shard_count = N,` in internal/storage/embeddeddolt/BUILD.bazel:\n%s", rule)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("embeddeddolt:embeddeddolt_embedded_test shard_count: %v", err)
	}
	return n
}

// S3 (F1, mirroring F2's TestProxiedShardManifestGeneratorNotStale in
// scripts/pr_risk_bazel_coverage_test.go — see that test's doc comment for
// the full --check rationale, not repeated here): the Bazel-only 50-shard
// cmd block and 15-shard storage block are not frozen like their files'
// legacy 20- and 5-shard blocks. gen_embedded_{cmd,storage}_shard_manifest.py
// --check verifies only that the committed block names every discovered
// test exactly once, failing with the exact command to fix it when a name
// is missing, stale, or duplicated — run here so a drifted block fails `go
// test ./scripts/...` (and scripts-go-checks on fork PRs) instead of only
// surfacing as a test silently never running in any shard. The legacy
// blocks are deliberately excluded: both files document that their 20- and
// 5-shard blocks are frozen (see .github/scripts/embedded-{cmd,storage}-
// test-shards.txt and engdocs/TESTING.md), so a --check against them is
// expected to report "missing" entries by design (see those generators'
// module docstrings) and is not what this test runs.
func TestCmdEmbeddedShardManifestGeneratorNotStale(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold neither the generator's sources nor cmd/bd")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	root := sourceRepoRoot(t)
	shards := strconv.Itoa(bazelEmbeddedCmdShardCount(t))
	cmd := exec.Command(python, "scripts/ci/gen_embedded_cmd_shard_manifest.py", shards, "--weights=duration", "--check")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("gen_embedded_cmd_shard_manifest.py %s --weights=duration --check: %v\n%s", shards, err, out)
	}
}

// TestStorageEmbeddedShardManifestGeneratorNotStale mirrors
// TestCmdEmbeddedShardManifestGeneratorNotStale above for the storage tier's
// Bazel-only 15-shard block; see that test's doc comment.
func TestStorageEmbeddedShardManifestGeneratorNotStale(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("scripts_test's runfiles hold neither the generator's sources nor cmd/bd")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	root := sourceRepoRoot(t)
	shards := strconv.Itoa(bazelEmbeddedStorageShardCount(t))
	cmd := exec.Command(python, "scripts/ci/gen_embedded_storage_shard_manifest.py", shards, "--weights=duration", "--check")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("gen_embedded_storage_shard_manifest.py %s --weights=duration --check: %v\n%s", shards, err, out)
	}
}
