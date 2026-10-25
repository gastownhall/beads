package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// offlineWrapper is the sh_test src that runs a go_test binary with no
// network but loopback; its --shard-manifest also selects and shards the
// binary's tests.
const offlineWrapper = "tools/bazel/go_test_offline.sh"

// offlineWrapperTargets are the BUILD rules that run through
// go_test_offline.sh (BUILD file:rule -> its one lane tag). Its
// --shard-manifest selects tests, which the retired tiers' lanes forbid
// (TestBazelRetiredLanesCannotBeNarrowed) except for reviewed targets, whose
// args that test pins.
var offlineWrapperTargets = map[string]string{
	"cmd/bd/BUILD.bazel:bd_managed_local_test": "dolt-server-proxied",
}

// fakeOfflineTools writes stand-ins for the tools go_test_offline.sh runs
// to isolate the binary, so its own logic runs here unchanged without a
// real namespace (which needs unprivileged user namespaces, absent on
// macOS and on some executors): unshare runs the command after "--" with
// FAKE_NETNS=1 set (or fails, with FAKE_UNSHARE=fail), ip does nothing,
// and timeout, which only wraps the wrapper's outbound probes, reports the
// network unreachable (or reachable, with FAKE_NETWORK=up). It returns the
// directory to put first on PATH.
func fakeOfflineTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, script := range map[string]string{
		"unshare": `#!/usr/bin/env bash
[ "${FAKE_UNSHARE:-}" = fail ] && exit 1
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
FAKE_NETNS=1 exec "$@"
`,
		"ip": "#!/usr/bin/env bash\nexit 0\n",
		"timeout": `#!/usr/bin/env bash
[ "${FAKE_NETWORK:-}" = up ] && exit 0
exit 124
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runOffline runs go_test_offline.sh with args in dir, which holds the fake
// binary "bin" and manifest.txt, under the fake isolation tools and env.
func runOffline(t *testing.T, dir, manifest string, args []string, env ...string) (string, error) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "manifest.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(sourceRepoRoot(t), filepath.FromSlash(offlineWrapper))
	cmd := exec.Command(requireHostTool(t, "bash"), append([]string{wrapper}, args...)...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + fakeOfflineTools(t) + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// With --shard-manifest the manifest is the target's whole selection: a
// Bazel shard runs exactly its manifest shard's tests with the binary's own
// sharding off, unsharded runs every listed test, and the binary still runs
// isolated (inside the namespace, or after the no-network probe where there
// is none). Without the flag the arguments pass through unchanged.
func TestOfflineWrapperShardManifest(t *testing.T) {
	const manifest = `# comment
1 TestSlow   # trailing comment
2 TestB
2 TestA

3 TestC
`
	status := filepath.Join(t.TempDir(), "status")
	sharded := func(index string) []string {
		return []string{"TEST_TOTAL_SHARDS=3", "TEST_SHARD_INDEX=" + index, "TEST_SHARD_STATUS_FILE=" + status}
	}
	withManifest := []string{"--shard-manifest=manifest.txt", "bin", "-test.timeout=15m"}
	cases := []struct {
		name string
		args []string
		env  []string
		want []string
	}{
		{"no manifest", []string{"bin", "-test.timeout=15m"}, nil, []string{
			"args=-test.timeout=15m\n",
			"total=unset index=unset status=unset fromBazel=1\n",
			"netns=1\n",
		}},
		{"unsharded", withManifest, nil, []string{
			"args=-test.run=^(TestSlow|TestB|TestA|TestC)$ -test.timeout=15m\n",
			"total=unset index=unset status=unset fromBazel=1\n",
			"netns=1\n",
		}},
		{"one shard is unsharded", withManifest, []string{"TEST_TOTAL_SHARDS=1", "TEST_SHARD_INDEX=0"}, []string{
			"args=-test.run=^(TestSlow|TestB|TestA|TestC)$ -test.timeout=15m\n",
		}},
		{"shard 1 of 3", withManifest, sharded("0"), []string{
			"args=-test.run=^(TestSlow)$ -test.timeout=15m\n",
			"total=unset index=unset status=unset fromBazel=1\n",
			"netns=1\n",
		}},
		{"shard 2 of 3", withManifest, sharded("1"), []string{
			"args=-test.run=^(TestB|TestA)$ -test.timeout=15m\n",
			"total=unset index=unset status=unset fromBazel=1\n",
		}},
		{"shard 3 of 3", withManifest, sharded("2"), []string{
			"args=-test.run=^(TestC)$ -test.timeout=15m\n",
		}},
		{"shard 2 of 3 without namespaces or network", withManifest, append(sharded("1"), "FAKE_UNSHARE=fail"), []string{
			"args=-test.run=^(TestB|TestA)$ -test.timeout=15m\n",
			"total=unset index=unset status=unset fromBazel=1\n",
			"netns=unset\n",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeTestBinary(t, dir)
			_ = os.Remove(status)
			out, err := runOffline(t, dir, manifest, tc.args, tc.env...)
			if err != nil {
				t.Fatalf("wrapper failed: %v\n%s", err, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			_, statErr := os.Stat(status)
			if touched := statErr == nil; touched != strings.Contains(strings.Join(tc.env, " "), "TEST_SHARD_STATUS_FILE") {
				t.Errorf("TEST_SHARD_STATUS_FILE touched = %v, want it touched exactly when Bazel shards the target", touched)
			}
		})
	}
}

// The selection fails closed: a -test.run among the binary's args would
// replace the manifest's, so every shard would run the same tests (exit 2,
// a usage error), and a malformed manifest or one whose shard count is not
// the target's fails before the binary runs. Isolation still fails closed
// too: network inside the namespace, or network with no namespace.
func TestOfflineWrapperRejectsBadShardManifests(t *testing.T) {
	const good = "1 TestA\n2 TestB\n"
	sharded := []string{"TEST_TOTAL_SHARDS=2", "TEST_SHARD_INDEX=0"}
	cases := []struct {
		name, manifest string
		args, env      []string
		code           int // 0: any failure
	}{
		{"-test.run among the args", good, []string{"--shard-manifest=manifest.txt", "bin", "-test.run=^TestA$"}, nil, 2},
		{"--test.run among the args", good, []string{"--shard-manifest=manifest.txt", "bin", "-test.v", "--test.run=^TestA$"}, sharded, 2},
		{"-test.run as its own argument", good, []string{"--shard-manifest=manifest.txt", "bin", "-test.run", "^TestA$"}, sharded, 2},
		{"empty -test.run", good, []string{"--shard-manifest=manifest.txt", "bin", "-test.run="}, nil, 2},
		{"no binary", good, []string{"--shard-manifest=manifest.txt"}, nil, 2},
		{"no manifest path", good, []string{"--shard-manifest=", "bin"}, nil, 2},
		{"missing manifest", good, []string{"--shard-manifest=absent.txt", "bin"}, nil, 0},
		{"shard count is not the target's", "1 TestA\n2 TestB\n3 TestC\n", nil, sharded, 0},
		{"shard index out of range", good, nil, []string{"TEST_TOTAL_SHARDS=2", "TEST_SHARD_INDEX=2"}, 0},
		{"no shard index", good, nil, []string{"TEST_TOTAL_SHARDS=2"}, 0},
		{"gap in shard numbers", "1 TestA\n3 TestB\n", nil, nil, 0},
		{"test listed twice", "1 TestA\n2 TestA\n", nil, nil, 0},
		{"not a test name", "1 BenchmarkA\n", nil, nil, 0},
		{"regex in name", "1 TestA.*\n", nil, nil, 0},
		{"extra field", "1 TestA TestB\n", nil, nil, 0},
		{"shard zero", "0 TestA\n", nil, nil, 0},
		{"rr line", "1 TestA\nrr TestB\n", nil, nil, 0},
		{"no tests", "# empty\n", nil, nil, 0},
		{"network inside the namespace", good, nil, []string{"FAKE_NETWORK=up"}, 0},
		{"network and no namespace", good, nil, []string{"FAKE_NETWORK=up", "FAKE_UNSHARE=fail"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeTestBinary(t, dir)
			args := tc.args
			if args == nil {
				args = []string{"--shard-manifest=manifest.txt", "bin"}
			}
			out, err := runOffline(t, dir, tc.manifest, args, tc.env...)
			if err == nil {
				t.Fatalf("wrapper accepted %v with manifest %q:\n%s", args, tc.manifest, out)
			}
			if code := exitCode(err); tc.code != 0 && code != tc.code {
				t.Errorf("exit status %d, want %d:\n%s", code, tc.code, out)
			}
			if strings.Contains(out, "args=") {
				t.Errorf("wrapper ran the binary:\n%s", out)
			}
		})
	}
}

func TestOfflineWrapperUsers(t *testing.T) {
	for _, e := range shWrapperUsers(t, sourceRepoRoot(t), offlineWrapper, offlineWrapperTargets, "offlineWrapperTargets") {
		t.Error(e)
	}
}
