package scripts_test

// Helpers for the pinned-shard tests, shared by scripts_test
// (pinned_shards_test.go) and go_test_sources_test
// (pinned_shards_sources_test.go).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// pinnedShardWrapper is the sh_test src that pins slow tests to shards.
const pinnedShardWrapper = "tools/bazel/go_test_pinned_shard.sh"

// minRoundRobinShards is the fewest round-robin shards a pinned-shard target
// may leave (TestPinnedShardManifests). Every test added since its manifest
// was measured lands in them, so one catch-all shard grows without bound:
// http_served_test's single one ran 314-633s by 2026-10-23, 10 days after its
// manifest was written. corpus_served_test is the exception: one shard per
// corpus part and one for its engine-free tests
// (TestCorpusPartsArePinnedOnePerShard).
const minRoundRobinShards = 3

// pinnedShardTargets are the sh_tests that run a go_test binary through
// tools/bazel/go_test_pinned_shard.sh: the BUILD file, the rule, its
// manifest, the directory holding the go_test's sources, and the rule's one
// lane tag.
//
// httpclient_served_test, http_served_test and corpus_served_test are the
// embedded lane's (a retired tier's) served tiers, which may not be narrowed:
// the wrapper runs every top-level test in exactly one shard
// (TestPinnedShardWrapperSplit), the manifest may name only real tests
// (TestPinnedShardManifestNamesAreTests), and the lane's check_testcases.py
// fails a shard that ran none. corpus_served_test pins its corpus parts, one
// per shard (TestCorpusPartsArePinnedOnePerShard).
var pinnedShardTargets = []struct {
	build, rule, manifest, pkg, tag string
}{
	{"cmd/bd/BUILD.bazel", "bd_dolt_server_test", "cmd/bd/dolt_server_pinned_shards.txt", "cmd/bd", "dolt-server-cmd"},
	{"tests/regression/BUILD.bazel", "regression_test", "tests/regression/pinned_shards.txt", "tests/regression", "dolt-server-cmd"},
	{"internal/httpclient/BUILD.bazel", "httpclient_served_test", "internal/httpclient/served_pinned_shards.txt", "internal/httpclient", "embedded"},
	{"backend/http/BUILD.bazel", "http_served_test", "backend/http/served_pinned_shards.txt", "backend/http", "embedded"},
	{"backend/http/corpus/BUILD.bazel", "corpus_served_test", "backend/http/corpus/served_pinned_shards.txt", "backend/http/corpus", "embedded"},
}

// pinnedShardManifest is a parsed pinned-shard manifest.
type pinnedShardManifest struct {
	// pinned maps each pinned test to its shard, 1..shards.
	pinned map[string]int
	// shards is the number of pinned shards.
	shards int
	// rr holds the "rr <TestName>" lines (tools/bazel/pin_shards.py
	// --record-measured): tests measured under the pinning threshold when
	// the manifest was generated. They run in the round-robin shards like
	// any test the manifest does not list.
	rr map[string]bool
}

var (
	pinnedLineRe = regexp.MustCompile(`^([1-9][0-9]*) (Test[A-Za-z0-9_]*)$`)
	rrLineRe     = regexp.MustCompile(`^rr (Test[A-Za-z0-9_]*)$`)
)

// parsePinnedShardManifest parses a pinned-shard manifest the way
// go_test_pinned_shard.sh does and returns it with every problem it finds:
// a malformed line, a name listed twice (pinned or rr), an empty shard, no
// pinned shard at all.
func parsePinnedShardManifest(manifest, text string) (pinnedShardManifest, []string) {
	m := pinnedShardManifest{pinned: map[string]int{}, rr: map[string]bool{}}
	var problems []string
	perShard := map[int]int{}
	for i, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, shard := "", 0
		if p := pinnedLineRe.FindStringSubmatch(line); p != nil {
			name = p[2]
			shard, _ = strconv.Atoi(p[1])
		} else if r := rrLineRe.FindStringSubmatch(line); r != nil {
			name = r[1]
		} else {
			problems = append(problems, fmt.Sprintf("%s:%d: %q is not \"<shard> <TestName>\" or \"rr <TestName>\"", manifest, i+1, line))
			continue
		}
		if _, dup := m.pinned[name]; dup || m.rr[name] {
			problems = append(problems, fmt.Sprintf("%s:%d: %s listed twice", manifest, i+1, name))
			continue
		}
		if shard == 0 {
			m.rr[name] = true
			continue
		}
		m.pinned[name] = shard
		perShard[shard]++
		m.shards = max(m.shards, shard)
	}
	for s := 1; s <= m.shards; s++ {
		if perShard[s] == 0 {
			problems = append(problems, fmt.Sprintf("%s: pinned shard %d is empty", manifest, s))
		}
	}
	if m.shards == 0 {
		problems = append(problems, manifest+" pins nothing")
	}
	return m, problems
}

// readPinnedShardManifest reads and parses a committed pinned-shard
// manifest, failing the test on every problem parsePinnedShardManifest finds.
func readPinnedShardManifest(t *testing.T, root, manifest string) pinnedShardManifest {
	t.Helper()
	m, problems := parsePinnedShardManifest(manifest, readPolicyFile(t, root, manifest))
	for _, p := range problems {
		t.Error(p)
	}
	return m
}
