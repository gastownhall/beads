package scripts_test

// The shard manifests and shard scripts against the Go tests they shard:
// //scripts:go_test_sources_test, which reads the _test.go files of the
// sharded packages (and no non-test Go source), so an edit to a workflow file,
// a doc or a non-test Go file leaves it cached.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// S3 (F1, mirroring F2's TestProxiedShardManifestGeneratorNotStale below —
// see that test's doc comment for the full --check rationale, not repeated
// here): the Bazel-only 50-shard cmd block and 15-shard storage block are
// not frozen like their files' legacy 20- and 5-shard blocks.
// gen_embedded_{cmd,storage}_shard_manifest.py --check verifies only that
// the committed block names every discovered test exactly once, failing with
// the exact command to fix it when a name is missing, stale, or duplicated —
// run here so a drifted block fails `go test ./scripts/...`
// (//scripts:go_test_sources_test under Bazel) instead of only surfacing as
// a test silently never running in any shard. The legacy blocks are
// deliberately excluded: both files document that their 20- and 5-shard
// blocks are frozen (see .github/scripts/embedded-{cmd,storage}-
// test-shards.txt and engdocs/TESTING.md), so a --check against them is
// expected to report "missing" entries by design (see those generators'
// module docstrings) and is not what this test runs.
func TestCmdEmbeddedShardManifestGeneratorNotStale(t *testing.T) {
	python := requireHostTool(t, "python3")
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
	python := requireHostTool(t, "python3")
	root := sourceRepoRoot(t)
	shards := strconv.Itoa(bazelEmbeddedStorageShardCount(t))
	cmd := exec.Command(python, "scripts/ci/gen_embedded_storage_shard_manifest.py", shards, "--weights=duration", "--check")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("gen_embedded_storage_shard_manifest.py %s --weights=duration --check: %v\n%s", shards, err, out)
	}
}

// Review G1: the checker's input is the real shard scripts' list-only
// output (every retired lane's: the embedded, proxied and server suites). Every name they list, for every shard, must be a test go test
// runs (declared `func Name(t *testing.T)` in the package's _test.go files),
// or one check_shard_coverage.py drops as NOT_TESTS; and each NOT_TESTS name
// must really not be a test (TestMain takes *testing.M). Otherwise the
// checker reports a listed test that "did not run" on every real run.
func TestShardScriptsListOnlyRealTests(t *testing.T) {
	// The CI shard scripts use bash 4 associative arrays (Linux runners only).
	requireAutofixBash(t)
	root := sourceRepoRoot(t)
	m := regexp.MustCompile(`(?m)^NOT_TESTS = frozenset\(\{([^}]*)\}\)`).FindStringSubmatch(readPolicyFile(t, root, "tools/bazel/check_shard_coverage.py"))
	if m == nil {
		t.Fatal("tools/bazel/check_shard_coverage.py has no NOT_TESTS = frozenset({...})")
	}
	notTests := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		notTests[q[1]] = true
	}
	for _, c := range []struct{ job, script, pkg string }{
		{"test-embedded-cmd", ".github/scripts/embedded-test-shard.sh", "cmd/bd"},
		{"test-embedded-storage", ".github/scripts/embedded-storage-test-shard.sh", "internal/storage/embeddeddolt"},
		// D2 step 2: the proxied and server lanes' suites.
		{"test-proxied-cmd", ".github/scripts/proxied-test-shard.sh", "cmd/bd"},
		{"test-server-storage-full", ".github/scripts/server-storage-test-shard.sh", "internal/storage/dolt"},
	} {
		var src strings.Builder
		files, err := filepath.Glob(filepath.Join(root, c.pkg, "*_test.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no _test.go files (%v)", c.pkg, err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src.Write(data)
			src.WriteString("\n")
		}
		declared := map[string]bool{}
		for _, d := range regexp.MustCompile(`(?m)^func (Test\w*)\(\w+ \*testing\.T\) \{`).FindAllStringSubmatch(src.String(), -1) {
			declared[d[1]] = true
		}

		// B1: validate every total this script's committed manifest holds a
		// block for, plus this job's own PR Risk matrix size and (for
		// test-proxied-cmd) the Bazel lane's own shard_count — not just
		// whichever total happens to equal this PR Risk job's matrix. Before
		// F2 those always coincided; now the Bazel-only bazel-proxied lane
		// reads a manifest block (30) that no PR-Risk-matrix-only check ever
		// exercises, so a fork PR (which never runs bazel-proxied) could
		// corrupt that block and still merge green. Looping over every
		// distinct total in the manifest closes that gap for this script and
		// any other script that later grows a second block the same way.
		mm := shardManifestDefault.FindStringSubmatch(readPolicyFile(t, root, c.script))
		if mm == nil {
			t.Fatalf("%s has no ${BEADS_TEST_SHARD_MANIFEST:-...} default manifest", c.script)
		}
		totalsSet := map[int]bool{}
		for _, line := range strings.Split(readPolicyFile(t, root, mm[1]), "\n") {
			line, _, _ = strings.Cut(line, "#")
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if n, err := strconv.Atoi(fields[0]); err == nil {
				totalsSet[n] = true
			}
		}
		switch c.script {
		case ".github/scripts/proxied-test-shard.sh":
			totalsSet[bazelProxiedShardCount(t)] = true
		case ".github/scripts/embedded-test-shard.sh":
			// F1: the Bazel-only bazel-embedded lane reads a 50-shard cmd
			// block that no PR-Risk-matrix-only (20-shard) check exercises;
			// without this, "BUILD.bazel's shard_count and bazel.yml's
			// check_shard_coverage.py arg both drift to a new total with no
			// manifest block" passes every policy test (see review S3) --
			// the lane then silently goes 100% hash fallback and loses its
			// duration balancing.
			totalsSet[bazelEmbeddedCmdShardCount(t)] = true
		case ".github/scripts/embedded-storage-test-shard.sh":
			// F1: mirrors the cmd case above for the 15-shard storage block.
			totalsSet[bazelEmbeddedStorageShardCount(t)] = true
		case ".github/scripts/server-storage-test-shard.sh":
			totalsSet[bazelServerFullShardCount(t)] = true
		}
		totals := make([]int, 0, len(totalsSet))
		for n := range totalsSet {
			totals = append(totals, n)
		}
		sort.Ints(totals)

		for _, shards := range totals {
			// The scripts' hash fallback forks per test (seconds per shard
			// for the server suite): list the shards concurrently.
			outs, errs := make([][]byte, shards+1), make([]error, shards+1)
			var wg sync.WaitGroup
			for k := 1; k <= shards; k++ {
				wg.Add(1)
				go func(k int) {
					defer wg.Done()
					cmd := exec.Command("bash", c.script, strconv.Itoa(k), strconv.Itoa(shards))
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "BEADS_TEST_SHARD_LIST_ONLY=1")
					outs[k], errs[k] = cmd.Output()
				}(k)
			}
			wg.Wait()
			listed, manifestSum := 0, 0
			for k := 1; k <= shards; k++ {
				if errs[k] != nil {
					// The script itself exits 1 on a duplicate manifest entry
					// or a manifest entry that names no discovered test
					// (rename/typo/stale-after-delete), for the requested
					// total only: this is what catches a corrupted block
					// that a PR-Risk-matrix-only check at a different total
					// would never see.
					t.Fatalf("%s %d %d: %v\n%s", c.script, k, shards, errs[k], outs[k])
				}
				for _, line := range strings.Split(string(outs[k]), "\n") {
					if mc := regexp.MustCompile(`^  manifest: (\d+), fallback: \d+$`).FindStringSubmatch(line); mc != nil {
						n, _ := strconv.Atoi(mc[1])
						manifestSum += n
						continue
					}
					name, ok := strings.CutPrefix(line, "  ")
					if !ok || !strings.HasPrefix(name, "Test") || strings.ContainsAny(name, " :") {
						continue
					}
					listed++
					isTest := declared[name]
					switch {
					case notTests[name] && isTest:
						t.Errorf("%s shard %d/%d lists %s, which check_shard_coverage.py drops, but it is a real test", c.script, k, shards, name)
					case !notTests[name] && !isTest:
						t.Errorf("%s shard %d/%d lists %s, which is not a `func %s(t *testing.T)` test in %s: check_shard_coverage.py would report it missing on every run (add it to NOT_TESTS only if go test never runs it)",
							c.script, k, shards, name, name, c.pkg)
					}
				}
			}
			if listed < 50 {
				t.Errorf("%s at %d shards listed only %d tests; did the list-only output format change?", c.script, shards, listed)
			}
			// S1: a total that is supposed to have a committed manifest
			// block (every total this loop considers does: it is either a
			// live job's matrix size or a total this script's own manifest
			// already names) must not have silently gone 100% hash fallback,
			// which is what "the whole block was deleted" looks like from
			// here: check_shard_coverage.py would still pass (it rebuilds
			// its expectation from this same script), so nothing else
			// catches it.
			if manifestSum == 0 {
				t.Errorf("%s at %d shards: manifest entries for this total sum to 0 across all shards (100%% hash fallback); its committed block in %s may have been deleted", c.script, shards, mm[1])
			}
		}
	}
	for name := range notTests {
		if name != "TestMain" {
			t.Errorf("NOT_TESTS has %s; only TestMain is never a test", name)
		}
	}
}

// S3: the Bazel-only 30-shard block is not frozen like the legacy 15-shard
// block (TestShardScriptsListOnlyRealTests's B1 fix catches outright
// corruption, but not a committed block that has drifted from the currently
// discovered TestProxiedServer*/TestServerMode* test set, e.g. a test added,
// renamed, or removed without anyone running --write). gen_proxied_shard_
// manifest.py --check verifies only that the committed block names every
// discovered test exactly once -- not that its shard *assignments* match a
// fresh LPT pack -- and fails with the exact command to fix it when a name
// is missing, stale, or duplicated. It deliberately does NOT fail merely
// because proxied_test_durations.json's weights changed and the existing
// packing is now suboptimal: two PRs each adding one proxied test would
// otherwise force a full repack and conflict on unrelated shard lines (see
// --repack below for the explicit opt-in to that). Run --check here so a
// block with missing/stale/duplicate names fails go test ./scripts/...
// (//scripts:go_test_sources_test under Bazel) instead of only
// surfacing as a test silently never running in any shard. The legacy
// 15-shard block is deliberately excluded: its header documents that it is
// frozen and must not be regenerated (see
// .github/scripts/proxied-cmd-test-shards.txt and engdocs/TESTING.md), so a
// --check against it would always fail by design.
func TestProxiedShardManifestGeneratorNotStale(t *testing.T) {
	python := requireHostTool(t, "python3")
	root := sourceRepoRoot(t)
	cmd := exec.Command(python, "scripts/ci/gen_proxied_shard_manifest.py", "30", "--weights=duration", "--check")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("gen_proxied_shard_manifest.py 30 --weights=duration --check: %v\n%s", err, out)
	}
}
