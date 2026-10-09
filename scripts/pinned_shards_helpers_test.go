package scripts_test

// Helpers for the pinned-shard tests, shared by scripts_test
// (pinned_shards_test.go) and go_test_sources_test
// (pinned_shards_sources_test.go).

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// pinnedShardWrapper is the sh_test src that pins slow tests to shards.
const pinnedShardWrapper = "tools/bazel/go_test_pinned_shard.sh"

// pinnedShardTargets are the sh_tests that run a go_test binary through
// tools/bazel/go_test_pinned_shard.sh: the BUILD file, the rule, its
// manifest, the directory holding the go_test's sources, and the rule's one
// lane tag.
//
// httpclient_served_test is the embedded lane's (a retired tier's) served
// corpus, which may not be narrowed: the wrapper runs every top-level test in
// exactly one shard (TestPinnedShardWrapperSplit), the manifest may name only
// real tests (TestPinnedShardManifestsNameRealTests), and the lane's
// check_testcases.py fails a shard that ran none.
var pinnedShardTargets = []struct {
	build, rule, manifest, pkg, tag string
}{
	{"cmd/bd/BUILD.bazel", "bd_dolt_server_test", "cmd/bd/dolt_server_pinned_shards.txt", "cmd/bd", "dolt-server-cmd"},
	{"tests/regression/BUILD.bazel", "regression_test", "tests/regression/pinned_shards.txt", "tests/regression", "dolt-server-cmd"},
	{"internal/httpclient/BUILD.bazel", "httpclient_served_test", "internal/httpclient/served_pinned_shards.txt", "internal/httpclient", "embedded"},
}

var pinnedLineRe = regexp.MustCompile(`^([1-9][0-9]*) (Test[A-Za-z0-9_]*)$`)

// readPinnedShardManifest parses a pinned-shard manifest the way
// go_test_pinned_shard.sh does, reporting malformed lines, duplicate names
// and empty shards, and returns the pinned names and the pinned shard count.
func readPinnedShardManifest(t *testing.T, root, manifest string) (map[string]int, int) {
	t.Helper()
	pinned := map[string]int{}
	shards := map[int]int{}
	maxShard := 0
	for i, line := range strings.Split(readPolicyFile(t, root, manifest), "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := pinnedLineRe.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s:%d: %q is not \"<shard> <TestName>\"", manifest, i+1, line)
			continue
		}
		shard, _ := strconv.Atoi(m[1])
		if _, dup := pinned[m[2]]; dup {
			t.Errorf("%s: %s pinned twice", manifest, m[2])
		}
		pinned[m[2]] = shard
		shards[shard]++
		maxShard = max(maxShard, shard)
	}
	for s := 1; s <= maxShard; s++ {
		if shards[s] == 0 {
			t.Errorf("%s: pinned shard %d is empty", manifest, s)
		}
	}
	if maxShard == 0 {
		t.Errorf("%s pins nothing", manifest)
	}
	return pinned, maxShard
}
