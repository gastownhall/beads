package scripts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var goTopLevelTestRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(t \*testing\.T\) \{`)

// packageTopLevelTests returns the top-level Test functions defined in a
// package directory's *_test.go files.
func packageTopLevelTests(t *testing.T, root, pkg string) map[string]bool {
	t.Helper()
	tests := map[string]bool{}
	srcs, err := filepath.Glob(filepath.Join(root, pkg, "*_test.go"))
	if err != nil || len(srcs) == 0 {
		t.Fatalf("no *_test.go in %s: %v", pkg, err)
	}
	for _, src := range srcs {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range goTopLevelTestRe.FindAllStringSubmatch(string(b), -1) {
			tests[m[1]] = true
		}
	}
	return tests
}

// Every name a pinned-shard manifest pins is a top-level test of the
// target's package: a stale pinned name runs nowhere, and a renamed test
// silently falls back to the round-robin shards (regenerate with
// tools/bazel/pin_shards.py). A stale rr name is only logged: rr lines change
// no sharding, and a test renamed or added since the manifest was measured is
// unmeasured whatever the rr lines say (TestPinnedShardManifestsAreMeasured
// counts the package's tests, not the manifest's names), so renaming or
// removing an unpinned test needs no regeneration.
func TestPinnedShardManifestNamesAreTests(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, tgt := range pinnedShardTargets {
		t.Run(tgt.rule, func(t *testing.T) {
			tests := packageTopLevelTests(t, root, tgt.pkg)
			m := readPinnedShardManifest(t, root, tgt.manifest)
			var stale, staleRR []string
			for name := range m.pinned {
				if !tests[name] {
					stale = append(stale, name)
				}
			}
			for name := range m.rr {
				if !tests[name] {
					staleRR = append(staleRR, name)
				}
			}
			sort.Strings(stale)
			sort.Strings(staleRR)
			for _, name := range stale {
				t.Errorf("%s pins %s, which is not a top-level test in %s/*_test.go; regenerate it (tools/bazel/pin_shards.py)", tgt.manifest, name, tgt.pkg)
			}
			if len(staleRR) > 0 {
				t.Logf("%s lists rr %s, no longer tests in %s/*_test.go; harmless (rr lines change no sharding), dropped when it is next regenerated", tgt.manifest, strings.Join(staleRR, ", "), tgt.pkg)
			}
		})
	}
}

// maxUnmeasuredPerRoundRobinShard: how many tests added since a measured
// manifest was generated each of its target's round-robin shards may take
// before the manifest is stale.
const maxUnmeasuredPerRoundRobinShard = 2

// measuredManifestTestlogs: for each pinnedShardTargets rule whose manifest
// records every test it measured (rr lines, pin_shards.py
// --record-measured), the TESTLOGS argument that regenerates it: test.xml
// directories of its lane's latest runs. Such a manifest must keep its rr
// lines, or the staleness check below would stop seeing new tests.
var measuredManifestTestlogs = map[string]string{
	"http_served_test": `$(tools/bazel/fetch_served_testxml.sh 30 "$(mktemp -d)")`,
}

var pinThresholdRe = regexp.MustCompile(`(?m)^# Every top-level test measured at >= ([0-9]+(?:\.[0-9]+)?)s,`)

// pinShardsRecipe is the one command that regenerates a measured manifest:
// pin_shards.py with the pinned shard count and threshold the manifest was
// generated with, over testlogs, keeping only the tests pkg defines (the
// runs measured other trees: older commits, other branches). It returns ""
// when the manifest's header does not state its threshold.
func pinShardsRecipe(manifest, pkg, text string, m pinnedShardManifest, testlogs string) string {
	th := pinThresholdRe.FindStringSubmatch(text)
	if th == nil {
		return ""
	}
	return fmt.Sprintf("python3 tools/bazel/pin_shards.py --shards %d --min-seconds %s \\\n"+
		"    --record-measured --tests-in %s \\\n"+
		"    --write %s \\\n    %s", m.shards, th[1], pkg, manifest, testlogs)
}

// unmeasuredTests returns the tests, sorted, that a manifest neither pins
// nor lists rr: added since it was generated.
func unmeasuredTests(m pinnedShardManifest, tests map[string]bool) []string {
	var out []string
	for name := range tests {
		if _, pinned := m.pinned[name]; !pinned && !m.rr[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// staleMeasuredManifest returns why a measured manifest is stale, or "":
// the tests added since it was generated all run in its target's rrShards
// round-robin shards, where nothing measured bounds them, and past
// maxUnmeasuredPerRoundRobinShard per shard it must be regenerated.
func staleMeasuredManifest(manifest, rule string, m pinnedShardManifest, tests map[string]bool, rrShards int, recipe string) string {
	unmeasured := unmeasuredTests(m, tests)
	limit := maxUnmeasuredPerRoundRobinShard * rrShards
	if len(unmeasured) <= limit {
		return ""
	}
	return fmt.Sprintf("%s is stale: %d tests are neither pinned nor listed rr (added since it was measured), more than %d for each of %s's %d round-robin shards: %s.\n"+
		"Regenerate it from the lane's latest runs (needs gh and the network):\n%s",
		manifest, len(unmeasured), maxUnmeasuredPerRoundRobinShard, rule, rrShards, strings.Join(unmeasured, ", "), recipe)
}

func TestStaleMeasuredManifestCheck(t *testing.T) {
	m := pinnedShardManifest{pinned: map[string]int{"TestSlow": 1}, shards: 1, rr: map[string]bool{"TestFast": true}}
	tests := map[string]bool{"TestSlow": true, "TestFast": true, "TestNew1": true, "TestNew2": true}
	if got := unmeasuredTests(m, tests); !reflect.DeepEqual(got, []string{"TestNew1", "TestNew2"}) {
		t.Errorf("unmeasuredTests = %q, want TestNew1, TestNew2", got)
	}
	if msg := staleMeasuredManifest("m.txt", "x_test", m, tests, 1, "RECIPE"); msg != "" {
		t.Errorf("2 unmeasured tests over 1 round-robin shard reported stale: %s", msg)
	}
	tests["TestNew3"] = true
	msg := staleMeasuredManifest("m.txt", "x_test", m, tests, 1, "RECIPE")
	for _, want := range []string{"m.txt is stale: 3 tests", "x_test's 1 round-robin shards: TestNew1, TestNew2, TestNew3.", "\nRECIPE"} {
		if !strings.Contains(msg, want) {
			t.Errorf("3 unmeasured tests over 1 round-robin shard: message lacks %q:\n%s", want, msg)
		}
	}
	if msg := staleMeasuredManifest("m.txt", "x_test", m, tests, 2, "RECIPE"); msg != "" {
		t.Errorf("3 unmeasured tests over 2 round-robin shards reported stale: %s", msg)
	}

	const header = "# Every top-level test measured at >= 3s, bin-packed\n"
	want := "python3 tools/bazel/pin_shards.py --shards 1 --min-seconds 3 \\\n" +
		"    --record-measured --tests-in pkg \\\n" +
		"    --write m.txt \\\n    LOGS"
	if got := pinShardsRecipe("m.txt", "pkg", header, m, "LOGS"); got != want {
		t.Errorf("pinShardsRecipe = %q, want %q", got, want)
	}
	if got := pinShardsRecipe("m.txt", "pkg", "# no threshold\n", m, "LOGS"); got != "" {
		t.Errorf("pinShardsRecipe without a threshold = %q, want \"\"", got)
	}
}

// A manifest generated with pin_shards.py --record-measured lists every test
// it measured, pinned or rr. A test listed neither way was added since and
// runs in the target's round-robin shards, which nothing measured bounds:
// once there are more than maxUnmeasuredPerRoundRobinShard per round-robin
// shard, regenerate the manifest (the failure prints the command).
func TestPinnedShardManifestsAreMeasured(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, tgt := range pinnedShardTargets {
		t.Run(tgt.rule, func(t *testing.T) {
			text := readPolicyFile(t, root, tgt.manifest)
			m := readPinnedShardManifest(t, root, tgt.manifest)
			testlogs, measured := measuredManifestTestlogs[tgt.rule]
			switch {
			case !measured && len(m.rr) == 0:
				return
			case !measured:
				t.Fatalf("%s lists rr tests, but measuredManifestTestlogs has no entry for %s to regenerate it from", tgt.manifest, tgt.rule)
			}
			recipe := pinShardsRecipe(tgt.manifest, tgt.pkg, text, m, testlogs)
			if recipe == "" {
				t.Fatalf("%s's header does not state its threshold (%s); regenerate it with tools/bazel/pin_shards.py", tgt.manifest, pinThresholdRe)
			}
			if len(m.rr) == 0 {
				t.Fatalf("%s lists no rr tests, so nothing can tell a new test from a measured one; regenerate it with --record-measured:\n%s", tgt.manifest, recipe)
			}
			rule := bazelRuleBlock(readPolicyFile(t, root, tgt.build), tgt.rule)
			sm := shardCountPattern.FindStringSubmatch(rule)
			if sm == nil {
				t.Fatalf("%s:%s has no shard_count", tgt.build, tgt.rule)
			}
			n, _ := strconv.Atoi(sm[1])
			if msg := staleMeasuredManifest(tgt.manifest, tgt.rule, m, packageTopLevelTests(t, root, tgt.pkg), n-m.shards, recipe); msg != "" {
				t.Error(msg)
			}
		})
	}
}

var corpusPartsConst = regexp.MustCompile(`(?m)^const corpusParts = (\d+)$`)

// corpus_served_test's manifest is not pin_shards.py's: it pins
// TestServedCorpusPart1..N, the corpus parts (backend/http/corpus
// corpusParts), to shards 1..N, one each, and pins nothing else, so every
// part runs alone in its own Bazel shard on its own seed. One shard is left
// for the binary's round-robin over the package's other (engine-free) tests.
func TestCorpusPartsArePinnedOnePerShard(t *testing.T) {
	root := sourceRepoRoot(t)
	const (
		build    = "backend/http/corpus/BUILD.bazel"
		manifest = "backend/http/corpus/served_pinned_shards.txt"
		src      = "backend/http/corpus/corpus_test.go"
	)
	m := corpusPartsConst.FindStringSubmatch(readPolicyFile(t, root, src))
	if m == nil {
		t.Fatalf("%s has no `const corpusParts = N`", src)
	}
	parts, _ := strconv.Atoi(m[1])
	if parts < 2 {
		t.Fatalf("corpusParts = %d; a single part needs no pinned shards", parts)
	}
	got := readPinnedShardManifest(t, root, manifest)
	want := map[string]int{}
	for k := 1; k <= parts; k++ {
		want[fmt.Sprintf("TestServedCorpusPart%d", k)] = k
	}
	if !reflect.DeepEqual(got.pinned, want) || len(got.rr) != 0 {
		t.Errorf("%s pins %v and lists rr %v, want each of the %d corpus parts on its own shard and nothing else: %v", manifest, got.pinned, got.rr, parts, want)
	}
	rule := bazelRuleBlock(readPolicyFile(t, root, build), "corpus_served_test")
	sm := shardCountPattern.FindStringSubmatch(rule)
	if sm == nil {
		t.Fatalf("%s:corpus_served_test has no shard_count", build)
	}
	if n, _ := strconv.Atoi(sm[1]); n != got.shards+1 {
		t.Errorf("%s:corpus_served_test shard_count = %d, want %d (%d parts and one round-robin shard)", build, n, got.shards+1, got.shards)
	}
}
