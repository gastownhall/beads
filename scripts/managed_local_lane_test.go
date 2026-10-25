package scripts_test

import (
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// managedLocalLaneTests are the managed-local proxied lifecycle tests the
// retired proxied-local-smoke.yml asserted were compiled into its test binary
// before it ran `-test.run ^TestManagedLocalProxied`. They now run in
// //cmd/bd:bd_managed_local_test, which runs exactly the tests its shard
// manifest lists (TestManagedLocalShardManifest), so none may drop out of a
// linux cgo build unnoticed.
var managedLocalLaneTests = []string{
	"TestManagedLocalProxiedLifecycleSmoke",
	"TestManagedLocalProxiedForeignListenerNotAdopted",
	"TestManagedLocalProxiedReusedPidNeverKilled",
	"TestManagedLocalProxiedLegacyRecordFailClosed",
	"TestManagedLocalProxiedOrphanBackendReaped",
	"TestManagedLocalProxiedNProcessColdStartConvergence",
	"TestManagedLocalProxiedStopStartInterleave",
	"TestManagedLocalProxiedDoltStatusReportsLiveTopology",
	"TestManagedLocalProxiedDoltStartRefusesOverLiveProxy",
	"TestManagedLocalProxiedDoltStartRefusesWithProxyDown",
	"TestManagedLocalProxiedDoltLifecycleLeavesOtherTopologiesAlone",
	"TestManagedLocalProxiedTrackerUOWConformance",
	"TestManagedLocalProxiedBackupRoundTrip",
	"TestManagedLocalProxiedAutoBackup",
	"TestManagedLocalProxiedBackupRestoreRefusedWhileAttached",
	"TestManagedLocalProxiedPurgeWispsPlaneRetention",
	"TestManagedLocalProxiedBackendCleanExitRetiresProxy",
	"TestManagedLocalProxiedCapabilityMatrix",
}

const (
	managedLocalBuild = "cmd/bd/BUILD.bazel"
	managedLocalRule  = "bd_managed_local_test"
	// managedLocalManifest is bd_managed_local_test's whole selection, read
	// by tools/bazel/go_test_offline.sh --shard-manifest.
	managedLocalManifest = "cmd/bd/managed_local_shards.txt"
)

// managedLocalCompiledTests returns the TestManagedLocalProxied* tests
// defined in the cmd/bd _test.go files that a linux/amd64 cgo build with
// bd_test's tags (gms_pure_go, no integration) compiles, each with its file.
func managedLocalCompiledTests(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(sourceRepoRoot(t), "cmd", "bd")
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "linux", "amd64", true
	ctx.BuildTags = []string{"gms_pure_go"}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]string{}
	funcRe := regexp.MustCompile(`(?m)^func (TestManagedLocalProxied\w*)\(`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		match, err := ctx.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !match {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range funcRe.FindAllStringSubmatch(string(data), -1) {
			defined[m[1]] = name
		}
	}
	return defined
}

// TestManagedLocalLaneTestsCompileIntoBdTest is the job's "Assert the
// managed-local lifecycle tests are compiled in" step: each pinned test is
// defined in a cmd/bd _test.go file that a linux/amd64 cgo build with
// bd_test's tags (gms_pure_go, no integration) compiles.
func TestManagedLocalLaneTestsCompileIntoBdTest(t *testing.T) {
	defined := managedLocalCompiledTests(t)
	for _, name := range managedLocalLaneTests {
		if _, ok := defined[name]; !ok {
			t.Errorf("%s is not defined in any cmd/bd test file a linux cgo build of bd_test compiles", name)
		}
	}
	if len(defined) < len(managedLocalLaneTests) {
		t.Errorf("found %d TestManagedLocalProxied* tests, want at least %d: %v", len(defined), len(managedLocalLaneTests), defined)
	}
}

var managedLocalSecondsRe = regexp.MustCompile(`(?m)^# Seconds per shard:((?: [0-9]+)+)$`)

// TestManagedLocalShardManifest: bd_managed_local_test runs exactly the
// tests its manifest lists (go_test_offline.sh --shard-manifest), each in its
// own shard, so the manifest must list every TestManagedLocalProxied* test
// the lane's bd_test compiles, each once, on shards 1..N (the format
// parsePinnedShardManifest checks, without rr lines), with N the target's
// shard_count. A new test fails here, naming the lightest shard by the
// seconds the header records.
func TestManagedLocalShardManifest(t *testing.T) {
	root := sourceRepoRoot(t)
	text := readPolicyFile(t, root, managedLocalManifest)
	m, problems := parsePinnedShardManifest(managedLocalManifest, text)
	for _, p := range problems {
		t.Error(p)
	}
	if len(m.rr) != 0 {
		t.Errorf("%s lists rr tests; go_test_offline.sh reads only \"<shard> <TestName>\" lines", managedLocalManifest)
	}

	sm := managedLocalSecondsRe.FindStringSubmatch(text)
	if sm == nil {
		t.Fatalf("%s has no %q header line (the measured seconds of each shard, in shard order)", managedLocalManifest, "# Seconds per shard: N N ...")
	}
	var seconds []int
	for _, f := range strings.Fields(sm[1]) {
		n, _ := strconv.Atoi(f)
		seconds = append(seconds, n)
	}
	if len(seconds) != m.shards {
		t.Errorf("%s records seconds for %d shards, but has %d", managedLocalManifest, len(seconds), m.shards)
	}
	lightest := 1
	for i, s := range seconds {
		if s < seconds[lightest-1] {
			lightest = i + 1
		}
	}

	compiled := managedLocalCompiledTests(t)
	var missing, stale []string
	for name := range compiled {
		if _, ok := m.pinned[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range m.pinned {
		if _, ok := compiled[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, name := range missing {
		t.Errorf("%s (%s) is not in %s, so //cmd/bd:%s never runs it: add the line `%d %s` (shard %d has the fewest recorded seconds) and add the test's measured seconds to that shard's figure in the header", name, compiled[name], managedLocalManifest, managedLocalRule, lightest, name, lightest)
	}
	for _, name := range stale {
		t.Errorf("%s lists %s, which no cmd/bd test file a linux cgo build of bd_test compiles defines (renamed, removed or build-constrained away), so its shard runs nothing for it", managedLocalManifest, name)
	}

	rule := bazelRuleBlock(readPolicyFile(t, root, managedLocalBuild), managedLocalRule)
	if rule == "" {
		t.Fatalf("%s has no rule %s", managedLocalBuild, managedLocalRule)
	}
	for _, want := range []string{
		`srcs = ["//tools/bazel:go_test_offline.sh"]`,
		`"--shard-manifest=$(rootpath :managed_local_shards.txt)",`,
		`":managed_local_shards.txt",`,
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("%s lacks %s", managedLocalRule, want)
		}
	}
	if strings.Contains(rule, `"-test.run`) {
		t.Errorf("%s passes a -test.run; its manifest is the selection, and go_test_offline.sh refuses one", managedLocalRule)
	}
	sc := shardCountPattern.FindStringSubmatch(rule)
	if sc == nil {
		t.Fatalf("%s has no shard_count", managedLocalRule)
	}
	if n, _ := strconv.Atoi(sc[1]); n != m.shards {
		t.Errorf("%s shard_count = %d, but %s has %d shards; go_test_offline.sh fails every shard of a target whose shard_count is not its manifest's", managedLocalRule, n, managedLocalManifest, m.shards)
	}
}
