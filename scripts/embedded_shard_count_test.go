package scripts_test

// The Bazel shard counts the CI policy tests (//scripts:scripts_test) and the
// shard-manifest tests (//scripts:go_test_sources_test) check against, read
// from each sharded target's own BUILD.bazel rule. Helpers only: both targets
// compile this file, so a test here would run twice.

import (
	"regexp"
	"strconv"
	"testing"
)

// shardCountPattern matches a rule block's `    shard_count = N,` line, the
// same pattern bazelProxiedShardCount (below) inlines
// for its own single use; shared here since both embedded accessors below
// need it.
var shardCountPattern = regexp.MustCompile(`(?m)^    shard_count = (\d+),$`)

// bazelEmbeddedCmdShardCount returns cmd/bd:bd_embedded_test's own
// shard_count from cmd/bd/BUILD.bazel: the single source of truth for the
// Bazel-only bazel-embedded lane's cmd/bd shard split (slice F1), which no
// longer has to equal PR Risk's/main.yml's legacy "Test (Embedded Dolt Cmd
// N/20)" fork/push jobs' matrix size — mirrors bazelProxiedShardCount (F2;
// see that function's doc comment below for the
// shared rationale, not repeated here).
func bazelEmbeddedCmdShardCount(t *testing.T) int {
	t.Helper()
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

// bazelServerFullShardCount returns dolt:dolt_server_full_test's own
// shard_count from internal/storage/dolt/BUILD.bazel: the single source of
// truth for the bazel-server-storage lane's full-suite shard split, now that
// PR Risk's legacy "Test (Server Dolt Full Suite N/16)" job, whose matrix it
// used to mirror, is retired (ga-96smfk.22). Mirrors
// bazelEmbeddedCmdShardCount above.
func bazelServerFullShardCount(t *testing.T) int {
	t.Helper()
	root := sourceRepoRoot(t)
	rule := bazelRuleBlock(readPolicyFile(t, root, "internal/storage/dolt/BUILD.bazel"), "dolt_server_full_test")
	m := shardCountPattern.FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("dolt:dolt_server_full_test has no `shard_count = N,` in internal/storage/dolt/BUILD.bazel:\n%s", rule)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("dolt:dolt_server_full_test shard_count: %v", err)
	}
	return n
}

// The default shard manifest of a PR Risk shard script.
var shardManifestDefault = regexp.MustCompile(`\$\{BEADS_TEST_SHARD_MANIFEST:-([^}]+)\}`)

// bazelProxiedShardCount returns cmd/bd:bd_proxied_test's own shard_count
// from cmd/bd/BUILD.bazel: the single source of truth for the Bazel-only
// bazel-proxied lane's shard split, which no longer has to equal PR
// Risk's/main.yml's legacy test-proxied-cmd jobs' matrix size (F2).
func bazelProxiedShardCount(t *testing.T) int {
	t.Helper()
	root := sourceRepoRoot(t)
	rule := bazelRuleBlock(readPolicyFile(t, root, "cmd/bd/BUILD.bazel"), "bd_proxied_test")
	m := regexp.MustCompile(`(?m)^    shard_count = (\d+),$`).FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("cmd/bd:bd_proxied_test has no `shard_count = N,` in cmd/bd/BUILD.bazel:\n%s", rule)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("cmd/bd:bd_proxied_test shard_count: %v", err)
	}
	return n
}
