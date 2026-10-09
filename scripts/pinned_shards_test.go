package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeTestBinary writes a stand-in for a go_test binary that prints its
// arguments and the sharding environment it was started with.
func fakeTestBinary(t *testing.T, dir string) {
	t.Helper()
	script := `#!/usr/bin/env bash
echo "args=$*"
echo "total=${TEST_TOTAL_SHARDS-unset} index=${TEST_SHARD_INDEX-unset} status=${TEST_SHARD_STATUS_FILE-unset} fromBazel=${GO_TEST_RUN_FROM_BAZEL-unset}"
`
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runPinnedShard(t *testing.T, dir, manifest string, env ...string) (string, error) {
	t.Helper()
	wrapper := filepath.Join(sourceRepoRoot(t), "tools", "bazel", "go_test_pinned_shard.sh")
	if err := os.WriteFile(filepath.Join(dir, "manifest.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(requireHostTool(t, "bash"), wrapper, "manifest.txt", "bin", "-test.parallel=4")
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The wrapper's split: pinned shards run exactly their tests with the
// binary's own sharding off; the rest are the binary's round-robin shards
// minus every pinned test; unsharded runs everything. Every test runs in
// exactly one shard because the skip set is the union of the run sets.
func TestPinnedShardWrapperSplit(t *testing.T) {
	const manifest = `# comment
1 TestSlow   # trailing comment
2 TestB
2 TestA

`
	status := filepath.Join(t.TempDir(), "status")
	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{"unsharded", nil, []string{
			"args=-test.parallel=4\n",
			"total=unset index=unset status=unset fromBazel=1\n",
		}},
		{"pinned shard 1", []string{"TEST_TOTAL_SHARDS=5", "TEST_SHARD_INDEX=0", "TEST_SHARD_STATUS_FILE=" + status}, []string{
			"args=-test.run=^(TestSlow)$ -test.parallel=4\n",
			"total=unset index=unset status=unset fromBazel=1\n",
		}},
		{"pinned shard 2", []string{"TEST_TOTAL_SHARDS=5", "TEST_SHARD_INDEX=1", "TEST_SHARD_STATUS_FILE=" + status}, []string{
			"args=-test.run=^(TestB|TestA)$ -test.parallel=4\n",
			"total=unset index=unset",
		}},
		{"first round-robin shard", []string{"TEST_TOTAL_SHARDS=5", "TEST_SHARD_INDEX=2", "TEST_SHARD_STATUS_FILE=" + status}, []string{
			"args=-test.skip=^(TestSlow|TestB|TestA)$ -test.parallel=4\n",
			"total=3 index=0 status=" + status + " fromBazel=1\n",
		}},
		{"last round-robin shard", []string{"TEST_TOTAL_SHARDS=5", "TEST_SHARD_INDEX=4", "TEST_SHARD_STATUS_FILE=" + status}, []string{
			"args=-test.skip=^(TestSlow|TestB|TestA)$ -test.parallel=4\n",
			"total=3 index=2 ",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeTestBinary(t, dir)
			_ = os.Remove(status)
			out, err := runPinnedShard(t, dir, manifest, tc.env...)
			if err != nil {
				t.Fatalf("wrapper failed: %v\n%s", err, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			if len(tc.env) > 0 {
				if _, err := os.Stat(status); err != nil {
					t.Errorf("TEST_SHARD_STATUS_FILE not touched: %v", err)
				}
			}
		})
	}
}

func TestPinnedShardWrapperRejectsBadManifests(t *testing.T) {
	cases := map[string]string{
		"gap in shard numbers":        "1 TestA\n3 TestB\n",
		"test pinned twice":           "1 TestA\n2 TestA\n",
		"not a test name":             "1 BenchmarkA\n",
		"regex in name":               "1 TestA.*\n",
		"extra field":                 "1 TestA TestB\n",
		"shard zero":                  "0 TestA\n",
		"no pinned shards":            "# empty\n",
		"no round-robin shards left":  "1 TestA\n2 TestB\n3 TestC\n4 TestD\n5 TestE\n",
		"more pinned than Bazel runs": "1 TestA\n2 TestB\n3 TestC\n4 TestD\n5 TestE\n6 TestF\n",
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fakeTestBinary(t, dir)
			out, err := runPinnedShard(t, dir, manifest, "TEST_TOTAL_SHARDS=5", "TEST_SHARD_INDEX=0")
			if err == nil {
				t.Fatalf("wrapper accepted manifest %q:\n%s", manifest, out)
			}
			if strings.Contains(out, "args=") {
				t.Errorf("wrapper ran the binary despite a bad manifest:\n%s", out)
			}
		})
	}
}

// The committed manifests are well formed and each target is wired to its
// manifest with round-robin shards left over. (That every pinned name is a
// test of the package: TestPinnedShardManifestNamesAreTests.)
func TestPinnedShardManifests(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, tgt := range pinnedShardTargets {
		t.Run(tgt.rule, func(t *testing.T) {
			_, maxShard := readPinnedShardManifest(t, root, tgt.manifest)
			rule := bazelRuleBlock(readPolicyFile(t, root, tgt.build), tgt.rule)
			if rule == "" {
				t.Fatalf("%s has no rule %s", tgt.build, tgt.rule)
			}
			base := filepath.Base(tgt.manifest)
			for _, want := range []string{
				`srcs = ["//tools/bazel:go_test_pinned_shard.sh"]`,
				`"$(rootpath :` + base + `)",`,
			} {
				if !strings.Contains(rule, want) {
					t.Errorf("%s lacks %s", tgt.rule, want)
				}
			}
			m := shardCountPattern.FindStringSubmatch(rule)
			if m == nil {
				t.Fatalf("%s has no shard_count", tgt.rule)
			}
			if n, _ := strconv.Atoi(m[1]); n <= maxShard {
				t.Errorf("%s shard_count = %d, but %s pins %d shards; round-robin shards must remain for the other tests", tgt.rule, n, tgt.manifest, maxShard)
			}
		})
	}
}

// pin_shards.py bin-packs every test at or above the threshold longest
// first, keeps the round-robin tests out, and takes the slowest of several
// runs.
func TestPinShardsGenerator(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	logs := t.TempDir()
	write := func(run, shard, body string) {
		dir := filepath.Join(logs, run, shard)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		xml := `<testsuites><testsuite name="x">` + body + `</testsuite></testsuites>`
		if err := os.WriteFile(filepath.Join(dir, "test.xml"), []byte(xml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a", "shard_1_of_2", `<testcase name="TestA" time="50"/><testcase name="TestA/sub" time="49"/><testcase name="TestB" time="30"/>`)
	write("a", "shard_2_of_2", `<testcase name="TestC" time="25"/><testcase name="TestD" time="20"/><testcase name="TestFast" time="1"/>`)
	write("b", "shard_1_of_1", `<testcase name="TestD" time="28"/>`)

	out := filepath.Join(t.TempDir(), "pinned.txt")
	cmd := exec.Command(python, filepath.Join(root, "tools", "bazel", "pin_shards.py"),
		"--shards", "2", "--min-seconds", "5", "--write", out, filepath.Join(logs, "a"), filepath.Join(logs, "b"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pin_shards.py: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			got = append(got, line)
		}
	}
	// LPT: A(50)->1, B(30)->2, D(28, the slower run)->2, C(25)->1.
	want := []string{"1 TestA", "1 TestC", "2 TestB", "2 TestD"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("manifest = %q, want %q", got, want)
	}
	if !strings.Contains(string(b), "# 75 58\n") {
		t.Errorf("manifest header lacks per-shard totals 75 58:\n%s", b)
	}

	cmd = exec.Command(python, filepath.Join(root, "tools", "bazel", "pin_shards.py"),
		"--shards", "5", "--min-seconds", "5", filepath.Join(logs, "a"))
	if b, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("pin_shards.py accepted 5 shards for 4 slow tests:\n%s", b)
	}
}

// pinnedShardWrapperUsers returns an error for every BUILD rule that runs
// through go_test_pinned_shard.sh other than pinnedShardTargets, and for any
// of those not tagged exactly its pinnedShardTargets lane: the wrapper
// selects and skips tests, which the retired tiers' lanes forbid
// (TestBazelRetiredLanesCannotBeNarrowed) except for the reviewed targets
// listed there.
func pinnedShardWrapperUsers(t *testing.T, root string) []string {
	t.Helper()
	allowed := map[string]string{}
	for _, tgt := range pinnedShardTargets {
		allowed[tgt.build+":"+tgt.rule] = tgt.tag
	}
	const label = "//tools/bazel:go_test_pinned_shard.sh"
	var errs []string
	seen := 0
	for _, f := range repoFiles(t, root, 300) {
		if filepath.Base(f) != "BUILD.bazel" {
			continue
		}
		build := readPolicyFile(t, root, f)
		if !strings.Contains(build, "go_test_pinned_shard.sh") {
			continue
		}
		for _, rule := range bazelTopRules(stripStarlarkComments(build)) {
			if !strings.Contains(rule, "go_test_pinned_shard.sh") {
				continue
			}
			name := ""
			if m := bazelRuleNameRe.FindStringSubmatch(rule); m != nil {
				name = m[1]
			}
			if !strings.Contains(rule, `srcs = ["`+label+`"]`) {
				if f == "tools/bazel/BUILD.bazel" && strings.HasPrefix(rule, "exports_files(") {
					continue
				}
				errs = append(errs, f+": rule "+name+" references go_test_pinned_shard.sh other than as its sh_test src "+label)
				continue
			}
			seen++
			tag, ok := allowed[f+":"+name]
			if !ok {
				errs = append(errs, f+": "+name+" runs through "+pinnedShardWrapper+"; add it to pinnedShardTargets after reviewing that every test still runs exactly once")
				continue
			}
			if !strings.Contains(rule, `tags = ["`+tag+`"]`) {
				errs = append(errs, f+": "+name+" runs through "+pinnedShardWrapper+" but is not tagged exactly "+tag)
			}
		}
	}
	if seen != len(pinnedShardTargets) {
		errs = append(errs, fmt.Sprintf("found %d rules running through %s, want the %d pinnedShardTargets", seen, pinnedShardWrapper, len(pinnedShardTargets)))
	}
	return errs
}

func TestPinnedShardWrapperUsers(t *testing.T) {
	for _, e := range pinnedShardWrapperUsers(t, sourceRepoRoot(t)) {
		t.Error(e)
	}
}
