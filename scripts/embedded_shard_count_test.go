package scripts_test

// The Bazel shard counts the CI policy tests (//scripts:scripts_test) and the
// shard-manifest tests (//scripts:go_test_sources_test) check against, read
// from each sharded target's own BUILD.bazel rule. Helpers only: both targets
// compile this file, so a test here would run twice.

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"
)

// shardCountPattern matches a rule block's `    shard_count = N,` line, the
// same pattern bazelProxiedShardCount (below) inlines
// for its own single use; shared here since both embedded accessors below
// need it.
var shardCountPattern = regexp.MustCompile(`(?m)^    shard_count = (\d+),$`)

// embeddedCmdTargets are the cmd/bd rules that split the bazel-embedded
// lane's cmd/bd manifest block between them (more shards than Bazel's
// 50-per-rule cap), in shard order.
var embeddedCmdTargets = []string{"bd_embedded_test", "bd_embedded_part2_test"}

// embeddedCmdPart is one of embeddedCmdTargets: its Bazel shard_count and the
// range of the manifest block it runs (go_test_manifest_shard.sh's
// --shard-offset/--shard-total args).
type embeddedCmdPart struct {
	label                 string
	shards, offset, total int
}

// spec is the part's check_shard_coverage.py --suite SHARDS argument.
func (p embeddedCmdPart) spec() string {
	return fmt.Sprintf("%d@%d/%d", p.shards, p.offset, p.total)
}

var (
	shardOffsetArg = regexp.MustCompile(`(?m)^        "--shard-offset=(\d+)",$`)
	shardTotalArg  = regexp.MustCompile(`(?m)^        "--shard-total=(\d+)",$`)
)

// bazelEmbeddedCmdParts reads embeddedCmdTargets' shard_count and shard
// range from cmd/bd/BUILD.bazel and fails unless the ranges tile one block
// exactly once, in order.
func bazelEmbeddedCmdParts(t *testing.T) []embeddedCmdPart {
	t.Helper()
	build := readPolicyFile(t, sourceRepoRoot(t), "cmd/bd/BUILD.bazel")
	var parts []embeddedCmdPart
	next := 0
	for _, name := range embeddedCmdTargets {
		rule := bazelRuleBlock(build, name)
		p := embeddedCmdPart{label: "//cmd/bd:" + name}
		for _, f := range []struct {
			re   *regexp.Regexp
			dst  *int
			what string
		}{{shardCountPattern, &p.shards, "shard_count = N,"}, {shardOffsetArg, &p.offset, `"--shard-offset=N",`}, {shardTotalArg, &p.total, `"--shard-total=N",`}} {
			m := f.re.FindStringSubmatch(rule)
			if m == nil {
				t.Fatalf("cmd/bd:%s has no %s in cmd/bd/BUILD.bazel:\n%s", name, f.what, rule)
			}
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("cmd/bd:%s %s: %v", name, f.what, err)
			}
			*f.dst = n
		}
		if p.offset != next || (len(parts) > 0 && p.total != parts[0].total) {
			t.Fatalf("cmd/bd:%s runs shards %d..%d of %d; want it to start at shard %d of the same block as %v", name, p.offset+1, p.offset+p.shards, p.total, next+1, parts)
		}
		next += p.shards
		parts = append(parts, p)
	}
	if next != parts[0].total {
		t.Fatalf("%v run %d shards of a %d-shard block; their shard_counts must sum to --shard-total", parts, next, parts[0].total)
	}
	return parts
}

// bazelEmbeddedCmdShardCount returns the shard total of the bazel-embedded
// lane's cmd/bd manifest block, which embeddedCmdTargets split between them
// (bazelEmbeddedCmdParts): the single source of truth for the Bazel-only
// lane's cmd/bd shard split (slice F1). It no longer has to equal PR
// Risk's/main.yml's legacy "Test (Embedded Dolt Cmd N/20)" fork/push jobs'
// matrix size — those jobs, and the frozen 20-shard block they read, have
// since been deleted (see engdocs/TESTING.md) — mirrors bazelProxiedShardCount
// (F2; see that function's doc comment below for the shared rationale, not
// repeated here).
func bazelEmbeddedCmdShardCount(t *testing.T) int {
	t.Helper()
	return bazelEmbeddedCmdParts(t)[0].total
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
