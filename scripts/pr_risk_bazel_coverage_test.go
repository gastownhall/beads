package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ga-96smfk.22: the Linux Go test tiers run only as pr.yml's Bazel lanes
// (bazel.yml). PR Risk's legacy embedded, proxied and server-Dolt jobs and
// pr.yml's PR Core, build-artifacts and pure-Go/js-wasm jobs are gone, with
// the bazel-coverage decision and the BAZEL_RETIRES_LEGACY_* /
// BAZEL_COVERS_FORKS flags that chose between them and the lanes. So on
// every pull_request and merge group pr.yml's gate requires the lanes to
// have run remotely on rbe-west (BAZEL_REMOTE) and passed: no execution mode
// can leave the gate green with a tier unrun.

const (
	prRiskWorkflowName     = "pr-risk.yml"
	prRiskPullRequestValue = "${{ github.event_name == 'pull_request' }}"
	// The decision's Dependabot test: Dependabot runs get no Actions
	// secrets, so bazel.yml's rbe job sends them to rbe-fork like forks.
	prRiskDependabotValue = "${{ github.actor == 'dependabot[bot]' }}"
	// pr.yml's gate id for "the Bazel lanes ran remotely".
	bazelRemoteGateID = "BAZEL_REMOTE"
)

// rbeFacts: what GitHub evaluates bazel.yml's rbe step's env expressions on.
type rbeFacts struct {
	event  string // github.event_name
	rbeVar string // vars.RBE_WEST_WORKERS ("" = unset)
	secret string // secrets.RBE_WEST_EXECUTOR ("" = unavailable: fork, Dependabot, unset)
	fork   bool   // github.event.pull_request.head.repo.fork
	// github.actor is dependabot[bot] (fixed for a PR's runs and re-runs).
	dependabot bool
	// What rbe-fork-mint's /v1/status answers this run (bazelTestMintEnv;
	// "" = unreachable). Only fork and Dependabot pull_request runs ask.
	mint string
}

func (f rbeFacts) String() string {
	return fmt.Sprintf("event=%s var=%q secret=%v fork=%v dependabot=%v mint=%q",
		f.event, f.rbeVar, f.secret != "", f.fork, f.dependabot, f.mint)
}

// forkPR: a run that asks rbe-fork-mint (bazel.yml's rbe job).
func (f rbeFacts) forkPR() bool { return f.event == "pull_request" && (f.fork || f.dependabot) }

// rbeMintAnswers: the mint states every forkPR fact is tried with.
var rbeMintAnswers = []string{"", "closed", "ro", "rw"}

var (
	// The events pr.yml's gate runs on.
	rbeGateEvents = []string{"pull_request", "merge_group"}
	rbeVarValues  = []string{"", "true", "True", "TRUE", "false", "1", "yes"}
	rbeSecrets    = []string{"", "grpcs://rbe.example:443"}
)

// rbeGateFactsMatrix: every combination of the facts bazel.yml's rbe step
// reads, for the events pr.yml's gate runs on; the mint answers only matter
// to forkPR facts.
func rbeGateFactsMatrix() []rbeFacts {
	var out []rbeFacts
	for _, event := range rbeGateEvents {
		for _, v := range rbeVarValues {
			for _, secret := range rbeSecrets {
				for _, fork := range []bool{false, true} {
					for _, dependabot := range []bool{false, true} {
						f := rbeFacts{event, v, secret, fork, dependabot, ""}
						if !f.forkPR() {
							out = append(out, f)
							continue
						}
						for _, m := range rbeMintAnswers {
							f.mint = m
							out = append(out, f)
						}
					}
				}
			}
		}
	}
	return out
}

// evalRBEExpr evaluates the env expressions the decision steps may use, for
// a bazel.yml call with these inputs (with: the caller's `with:`; unset
// inputs take their defaults). GitHub's == on strings is case-insensitive.
// Any other expression fails the test, so the simulation cannot silently
// drift from the workflows.
func evalRBEExpr(t *testing.T, expr string, f rbeFacts, with map[string]string) string {
	t.Helper()
	input := func(name, def string) string {
		if v, ok := with[name]; ok {
			return v
		}
		return def
	}
	switch expr {
	case "${{ github.event.pull_request.number }}":
		if f.event == "pull_request" || f.event == "pull_request_target" {
			return "7123"
		}
		return ""
	case prRiskPullRequestValue:
		return strconv.FormatBool(f.event == "pull_request")
	case prRiskDependabotValue:
		return strconv.FormatBool(f.dependabot)
	case "${{ vars.RBE_WEST_WORKERS == 'true' }}":
		return strconv.FormatBool(strings.EqualFold(f.rbeVar, "true"))
	case "${{ inputs.rbe == 'off' }}":
		return strconv.FormatBool(strings.EqualFold(input("rbe", "on"), "off"))
	case "${{ inputs.rbe == 'cache' }}":
		return strconv.FormatBool(strings.EqualFold(input("rbe", "on"), "cache"))
	case "${{ github.event.pull_request.head.repo.fork == true }}":
		return strconv.FormatBool(f.fork)
	case bazelForkFarmValue:
		return strconv.FormatBool(strings.EqualFold(input("fork-farm", "off"), "authorized") &&
			f.event == "pull_request_target" && input("checkout-sha", "") != "")
	case bazelRBESecretValue:
		return strconv.FormatBool(f.secret != "")
	}
	t.Fatalf("decision env expression %q: teach evalRBEExpr how GitHub evaluates it", expr)
	return ""
}

// runDecisionStep runs a decision step's script under GitHub's bash flags
// with its env evaluated for f, and returns its $GITHUB_OUTPUT.
func runDecisionStep(t *testing.T, step ciWorkflowStep, f rbeFacts, with map[string]string) (map[string]string, error) {
	t.Helper()
	env := map[string]string{bazelTestMintEnv: f.mint}
	for k, v := range step.Env {
		env[k] = evalRBEExpr(t, v, f, with)
	}
	return runBazelRBEDecision(t, step.Run, env)
}

// prGateFor: pr.yml's ci-gate scenario for one run whose Bazel call took
// this mode with every lane that runs in it passing.
func prGateFor(t *testing.T, lanes map[string]map[string]bool, event, mode string) bazelGateScenario {
	t.Helper()
	outputs := map[string]string{}
	for lane, modes := range lanes {
		if modes[mode] {
			outputs[lane] = "success"
		}
	}
	return bazelGateScenario{
		name: fmt.Sprintf("%s mode %s", event, mode), event: event,
		mode: mode, enabled: bazelModeEnabled(mode), call: "success",
		outputs: outputs,
	}
}

// bazelPRCallLanes: the modes each bazel.yml lane runs in under pr.yml's call.
func bazelPRCallLanes(t *testing.T, with map[string]string) map[string]map[string]bool {
	t.Helper()
	lanes := map[string]map[string]bool{}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if name != bazelRBEJobName {
			lanes[name] = bazelLaneRunModes(t, name, job.If, with)
		}
	}
	return lanes
}

// Never a green gate with the Linux Go test tiers unrun: for every fact
// bazel.yml's rbe step reads on a pull_request or merge group (the farm
// variable, the executor secret, fork, Dependabot, what rbe-fork's mint
// answers), pr.yml's actual gate step, with every lane that runs in the
// decided mode passing, is green exactly when that mode executed the lanes
// on rbe-west, and a red one names BAZEL_REMOTE. A lane that failed, was
// cancelled or did not report reds the gate in a remote mode too.
func TestPRGateRequiresRemoteBazelLanes(t *testing.T) {
	requireHostTool(t, "bash")
	pr := readCIWorkflow(t, "pr.yml")
	call := pr.job(t, "bazel")
	if call.Uses != "./.github/workflows/"+bazelWorkflowName {
		t.Fatalf("pr.yml bazel job uses %q, want the local %s", call.Uses, bazelWorkflowName)
	}
	rbeStep := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName).Steps[0]
	gateStep := pr.job(t, "ci-gate").step(t, "Evaluate CI gate")
	lanes := bazelPRCallLanes(t, call.With)

	required := strings.Fields(gateStep.Env["CI_GATE_REQUIRED"])
	if !contains(required, bazelRemoteGateID) {
		t.Errorf("pr.yml's ci-gate does not require %s", bazelRemoteGateID)
	}
	if _, ok := gateStep.Env[bazelRemoteGateID]; ok {
		t.Errorf("pr.yml's ci-gate env sets %s; only its run script may", bazelRemoteGateID)
	}
	for lane, id := range bazelLaneGateIDs {
		if !lanes[lane]["remote"] {
			t.Errorf("gated lane %s does not run in mode remote", lane)
		}
		if !contains(required, id) && !bazelPackageJobs[lane] {
			t.Errorf("pr.yml's ci-gate does not require %s", id)
		}
	}

	gateMemo := map[string]bool{}
	saw := map[string]bool{}
	for _, f := range rbeGateFactsMatrix() {
		out, err := runDecisionStep(t, rbeStep, f, call.With)
		if err != nil {
			t.Fatalf("%v: bazel.yml rbe step: %v", f, err)
		}
		mode := out["mode"]
		saw[mode] = true
		key := f.event + "/" + mode
		pass, ok := gateMemo[key]
		if !ok {
			var log string
			pass, log = runPRGateStep(t, gateStep, prGateFor(t, lanes, f.event, mode))
			gateMemo[key] = pass
			if !pass && !regexp.MustCompile(`::error::`+bazelRemoteGateID+`\b`).MatchString(log) {
				t.Errorf("%v (mode %s): red gate does not name %s:\n%s", f, mode, bazelRemoteGateID, log)
			}
		}
		if pass != bazelRemoteModes[mode] {
			t.Errorf("%v: mode %s, every lane that runs in it passed: gate pass = %v, want %v", f, mode, pass, bazelRemoteModes[mode])
		}
	}
	for _, mode := range []string{"remote", "fork-ro", "fork-rw", "cache", "skip"} {
		if !saw[mode] {
			t.Errorf("the fact matrix never reached mode %s: %v", mode, saw)
		}
	}

	// Named cases, for the record.
	for _, c := range []struct {
		name string
		f    rbeFacts
		mode string
		pass bool
	}{
		{"same-repo PR, farm on", rbeFacts{"pull_request", "true", "x", false, false, ""}, "remote", true},
		{"same-repo PR, kill switch (var unset)", rbeFacts{"pull_request", "", "x", false, false, ""}, "skip", false},
		{"same-repo PR, executor secret missing", rbeFacts{"pull_request", "true", "", false, false, ""}, "cache", false},
		{"fork PR, rbe-fork ro", rbeFacts{"pull_request", "true", "", true, false, "ro"}, "fork-ro", true},
		{"fork PR, rbe-fork rw", rbeFacts{"pull_request", "", "", true, false, "rw"}, "fork-rw", true},
		{"fork PR, rbe-fork closed", rbeFacts{"pull_request", "true", "", true, false, "closed"}, "cache", false},
		{"fork PR, mint unreachable", rbeFacts{"pull_request", "true", "", true, false, ""}, "cache", false},
		{"Dependabot PR, rbe-fork ro", rbeFacts{"pull_request", "true", "", false, true, "ro"}, "fork-ro", true},
		{"Dependabot PR, rbe-fork closed", rbeFacts{"pull_request", "", "", false, true, "closed"}, "cache", false},
		{"merge_group", rbeFacts{"merge_group", "true", "x", false, false, ""}, "remote", true},
		{"merge_group, kill switch (var unset)", rbeFacts{"merge_group", "", "x", false, false, ""}, "skip", false},
		{"merge_group, executor secret missing", rbeFacts{"merge_group", "true", "", false, false, ""}, "cache", false},
	} {
		out, err := runDecisionStep(t, rbeStep, c.f, call.With)
		if err != nil {
			t.Fatalf("%s: bazel.yml rbe step: %v", c.name, err)
		}
		if out["mode"] != c.mode {
			t.Errorf("%s: mode = %q, want %s", c.name, out["mode"], c.mode)
		}
		if pass, log := runPRGateStep(t, gateStep, prGateFor(t, lanes, c.f.event, out["mode"])); pass != c.pass {
			t.Errorf("%s: pr.yml gate pass = %v, want %v\n%s", c.name, pass, c.pass, log)
		}
	}

	// Remote, but one gated lane failed, was cancelled or reported nothing:
	// red, naming that lane.
	for lane, id := range bazelLaneGateIDs {
		for _, mode := range []string{"remote", "fork-ro"} {
			for _, res := range []string{"failure", "cancelled", ""} {
				sc := prGateFor(t, lanes, "pull_request", mode)
				sc.outputs[lane] = res
				if pass, log := runPRGateStep(t, gateStep, sc); pass || !regexp.MustCompile(`::error::`+id+`\b`).MatchString(log) {
					t.Errorf("mode %s, lane %s %q: gate pass = %v, want red naming %s\n%s", mode, lane, res, pass, id, log)
				}
			}
		}
	}
	// A mode that executed nothing remotely is red even if every lane
	// somehow reported success.
	for _, mode := range []string{"skip", "local", "cache"} {
		sc := prGateFor(t, lanes, "pull_request", mode)
		for lane := range bazelLaneGateIDs {
			sc.outputs[lane] = "success"
		}
		if pass, log := runPRGateStep(t, gateStep, sc); pass || !strings.Contains(log, "::error::"+bazelRemoteGateID) {
			t.Errorf("mode %s, every lane reported success: gate pass = %v, want red naming %s\n%s", mode, pass, bazelRemoteGateID, log)
		}
	}
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Review F5: each manifest-sharded Bazel lane (embedded, proxied, server
// storage) checks, after the run, that every Bazel shard of its
// manifest-sharded targets ran exactly the tests its shard script lists, for
// the targets' own BUILD.bazel shard counts, and that its unsharded targets
// did not only skip. These lanes are the tiers' only run since PR Risk's
// legacy jobs were retired (ga-96smfk.22), so the check is what stands
// between a manifest drift and a test silently never running.
func TestBazelRetiredLanesCheckListedTestsRan(t *testing.T) {
	type suite struct {
		job, step, label, script string
		// shardCount: -1 reads the label's own shard_count live from its
		// BUILD.bazel rule, via the liveShardCount dispatch table below;
		// only a label present in that table may use it (enforced below).
		shardCount int
	}
	// liveShardCount dispatches a suite's -1 shardCount to the accessor that
	// reads its target's own shard_count from its BUILD.bazel rule — the
	// single source of truth for a lane's shard split. See
	// bazelProxiedShardCount's doc comment (scripts/embedded_shard_count_test.go) for
	// the shared rationale.
	liveShardCount := map[string]func(*testing.T) int{
		"//cmd/bd:bd_proxied_test":                                   bazelProxiedShardCount,
		"//internal/storage/embeddeddolt:embeddeddolt_embedded_test": bazelEmbeddedStorageShardCount,
		"//internal/storage/dolt:dolt_server_full_test":              bazelServerFullShardCount,
	}
	for _, c := range []struct {
		lane, config string
		suites       []suite
		whole        []string
	}{
		{bazelEmbedJobName, "embedded", []suite{
			// bazel-embedded runs its own duration-balanced manifest blocks
			// (scripts/ci/embedded_{cmd,storage}_test_durations.json), not
			// PR Risk's frozen 20- and 5-shard blocks (slice F1): neither is
			// a drop-in retirement of its legacy job's shard count, just its
			// tests. bazelEmbeddedCmdShardCount/bazelEmbeddedStorageShardCount
			// read the real counts from BUILD.bazel, making these suites'
			// `want` (below) genuine cross-file pins against bazel.yml's own
			// check_shard_coverage.py arguments, not independently
			// hard-coded literals that could drift from BUILD.bazel
			// unnoticed (S1).
			// The cmd/bd block is split over embeddedCmdTargets: one
			// --suite each, SHARDS@OFFSET/TOTAL (added below).
			{"test-embedded-storage", "Test", "//internal/storage/embeddeddolt:embeddeddolt_embedded_test", ".github/scripts/embedded-storage-test-shard.sh", -1},
		}, []string{"//internal/storage/embeddeddolt:embeddeddolt_conformance_core_test", "//internal/storage/embeddeddolt:embeddeddolt_conformance_audit_test"}},
		{bazelProxiedJobName, "doltserver-proxied", []suite{
			// bazel-proxied runs its own duration-balanced manifest block
			// (scripts/ci/proxied_test_durations.json), not PR Risk's frozen
			// 15-shard bd-init-cost-proxy block: it is not a drop-in
			// retirement of test-proxied-cmd's shard count, just its tests.
			// bazelProxiedShardCount reads the real count from
			// cmd/bd/BUILD.bazel, making this suite's `want` (below) a
			// genuine cross-file pin against bazel.yml's own
			// check_shard_coverage.py argument, not an independently
			// hard-coded literal that could drift from BUILD.bazel unnoticed
			// (S1).
			{"test-proxied-cmd", "Test proxied-server cmd shard", "//cmd/bd:bd_proxied_test", ".github/scripts/proxied-test-shard.sh", -1},
		}, nil},
		{bazelServerJobName, "doltserver-integration", []suite{
			{"test-server-storage-full", "Test", "//internal/storage/dolt:dolt_server_full_test", ".github/scripts/server-storage-test-shard.sh", -1},
		}, []string{"//internal/storage/dolt:dolt_server_conformance_test"}},
	} {
		job := readCIWorkflow(t, bazelWorkflowName).job(t, c.lane)
		step := job.step(t, "Every listed test ran in its shard")
		want := []string{"python3 tools/bazel/check_shard_coverage.py", `--bep "$RUNNER_TEMP/bazel-bep.json"`}
		if c.lane == bazelEmbedJobName {
			for _, p := range bazelEmbeddedCmdParts(t) {
				want = append(want, fmt.Sprintf("--suite %s %s %s", p.label, ".github/scripts/embedded-test-shard.sh", p.spec()))
			}
		}
		for _, s := range c.suites {
			shards := s.shardCount
			if shards < 0 {
				fn, ok := liveShardCount[s.label]
				if !ok {
					t.Fatalf("%s: shardCount<0 (read live from BUILD.bazel) is not configured for %s", s.job, s.label)
				}
				shards = fn(t)
			}
			want = append(want, fmt.Sprintf("--suite %s %s %d", s.label, s.script, shards))
		}
		for _, w := range c.whole {
			want = append(want, "--whole "+w)
		}
		if got := strings.Join(strings.Fields(step.Run), " "); got != strings.Join(want, " ") {
			t.Errorf("%s coverage step runs %q, want %q", c.lane, got, strings.Join(want, " "))
		}
		if step.If != "${{ always() && steps.test.outcome != 'skipped' }}" || step.ContinueOnError != nil {
			t.Errorf("%s coverage step: if %q, continue-on-error %v; want always() after the test step and no continue-on-error", c.lane, step.If, step.ContinueOnError)
		}
		if !(job.stepIndex(t, "bazel test //... --config="+c.config) < job.stepIndex(t, step.Name) &&
			job.stepIndex(t, step.Name) < job.stepIndex(t, "Record job result")) {
			t.Errorf("%s coverage step must run after the test step and before the result recorder", c.lane)
		}
	}
}

// check_shard_coverage.py itself, on a synthetic BEP, test.xml files and
// shard script.
func TestCheckShardCoverageScript(t *testing.T) {
	requireHostTool(t, "bash")
	python := requireHostTool(t, "python3")
	script := filepath.Join(sourceRepoRoot(t), "tools", "bazel", "check_shard_coverage.py")
	dir := t.TempDir()
	shard := filepath.Join(dir, "shard.sh")
	// Shard 1 lists TestA and TestB, shard 2 TestC; list-only mode only.
	if err := os.WriteFile(shard, []byte(`#!/usr/bin/env bash
set -euo pipefail
[ "${BEADS_TEST_SHARD_LIST_ONLY:-}" = 1 ] || { echo "not list-only" >&2; exit 3; }
[ "$2" = 2 ] || { echo "bad total $2" >&2; exit 1; }
echo "Shard $1/$2: running"
echo "  manifest: 1, fallback: 0"
case "$1" in
  1) printf '  %s\n' TestA TestB TestMain ;; # TestMain: never a testcase
  2) printf '  %s\n' TestC ;;
  *) exit 1 ;;
esac
[ -z "${FAKE_SHARD_FAIL:-}" ] || exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	const label = "//pkg:t"
	writeBEP := func(shards int) string {
		lines := []string{`{"id":{"targetConfigured":{"label":"` + label + `"}},"configured":{"targetKind":"sh_test rule"}}`}
		for k := 1; k <= shards; k++ {
			lines = append(lines, fmt.Sprintf(`{"id":{"testResult":{"label":"%s","shard":%d,"run":1,"attempt":1}}}`, label, k))
		}
		path := filepath.Join(dir, fmt.Sprintf("bep%d.json", shards))
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// A name ending in "~" is written as a skipped testcase.
	writeXML := func(logs string, k int, names ...string) {
		d := filepath.Join(logs, "pkg", "t", fmt.Sprintf("shard_%d_of_2", k))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString(`<testsuites><testsuite name="pkg">`)
		for _, n := range names {
			if skipped, ok := strings.CutSuffix(n, "~"); ok {
				fmt.Fprintf(&b, `<testcase classname="pkg" name="%s"><skipped message="skip"></skipped></testcase>`, skipped)
				continue
			}
			fmt.Fprintf(&b, `<testcase classname="pkg" name="%s"></testcase>`, n)
		}
		b.WriteString(`</testsuite></testsuites>`)
		if err := os.WriteFile(filepath.Join(d, "test.xml"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(bep, logs string) (bool, string) {
		cmd := exec.Command(python, script, "--bep", bep, "--testlogs", logs, "--suite", label, shard, "2")
		out, err := cmd.CombinedOutput()
		return err == nil, string(out)
	}
	cases := []struct {
		name     string
		bepShard int
		s1, s2   []string
		pass     bool
		mention  string
	}{
		{"exact", 2, []string{"TestA", "TestB", "TestA/sub"}, []string{"TestC"}, true, ""},
		{"listed test missing from the binary", 2, []string{"TestA"}, []string{"TestC"}, false, "TestB is listed"},
		{"test in the wrong shard", 2, []string{"TestA", "TestB", "TestC"}, []string{}, false, "TestC"},
		{"unlisted test ran", 2, []string{"TestA", "TestB", "TestZ"}, []string{"TestC"}, false, "TestZ ran"},
		{"shard count differs", 1, []string{"TestA", "TestB"}, []string{"TestC"}, false, "want 2"},
		{"missing test.xml", 2, []string{"TestA", "TestB"}, nil, false, "missing"},
		// Review G5: a shard whose test.xml has no top-level test at all
		// still reports every listed test missing (check_testcases.py also
		// rejects it, but this check must not depend on that).
		{"shard ran zero tests", 2, []string{}, []string{"TestC"}, false, "TestA is listed"},
		{"zero tests, only subtests", 2, []string{"TestA/sub"}, []string{"TestC"}, false, "TestB is listed"},
		// Review G3: some skips are fine; a shard of only skips is not.
		{"some skipped", 2, []string{"TestA~", "TestB"}, []string{"TestC"}, true, ""},
		{"shard all skipped", 2, []string{"TestA~", "TestB~"}, []string{"TestC"}, false, "shard 1/2: every top-level test (2) was skipped"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := filepath.Join(dir, fmt.Sprintf("logs%d", i))
			writeXML(logs, 1, c.s1...)
			if c.s2 != nil {
				writeXML(logs, 2, c.s2...)
			}
			pass, out := run(writeBEP(c.bepShard), logs)
			if pass != c.pass || (c.mention != "" && !strings.Contains(out, c.mention)) {
				t.Errorf("pass = %v, want %v (mention %q):\n%s", pass, c.pass, c.mention, out)
			}
		})
	}
	// --whole: an unsharded target must list a test and not only skips.
	for i, c := range []struct {
		names   []string
		pass    bool
		mention string
	}{
		{[]string{"TestConformance"}, true, ""},
		{[]string{"TestConformance~"}, false, "was skipped"},
		{[]string{}, false, "lists no tests"},
	} {
		logs := filepath.Join(dir, fmt.Sprintf("whole%d", i))
		d := filepath.Join(logs, "pkg", "w")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		xml := `<testsuites><testsuite name="pkg">`
		for _, n := range c.names {
			if skipped, ok := strings.CutSuffix(n, "~"); ok {
				xml += `<testcase name="` + skipped + `"><skipped></skipped></testcase>`
			} else {
				xml += `<testcase name="` + n + `"></testcase>`
			}
		}
		xml += `</testsuite></testsuites>`
		if err := os.WriteFile(filepath.Join(d, "test.xml"), []byte(xml), 0o644); err != nil {
			t.Fatal(err)
		}
		bep := filepath.Join(dir, fmt.Sprintf("whole%d.json", i))
		if err := os.WriteFile(bep, []byte(`{"id":{"targetConfigured":{"label":"//pkg:w"}},"configured":{"targetKind":"sh_test rule"}}
{"id":{"testResult":{"label":"//pkg:w","run":1,"attempt":1}}}
{"id":{"targetConfigured":{"label":"`+label+`"}},"configured":{"targetKind":"sh_test rule"}}
{"id":{"testResult":{"label":"`+label+`","shard":1}}}
{"id":{"testResult":{"label":"`+label+`","shard":2}}}
`), 0o644); err != nil {
			t.Fatal(err)
		}
		writeXML(logs, 1, "TestA", "TestB")
		writeXML(logs, 2, "TestC")
		out, err := exec.Command(python, script, "--bep", bep, "--testlogs", logs, "--suite", label, shard, "2", "--whole", "//pkg:w").CombinedOutput()
		if (err == nil) != c.pass || (c.mention != "" && !strings.Contains(string(out), c.mention)) {
			t.Errorf("--whole %v: pass = %v, want %v (mention %q):\n%s", c.names, err == nil, c.pass, c.mention, out)
		}
	}
	// A 2-shard block split over two 1-shard targets (SHARDS@OFFSET/TOTAL):
	// //pkg:p's shard 1 is the script's shard 1, //pkg:q's shard 1 its shard
	// 2. The targets must tile the block exactly once.
	splitLogs := filepath.Join(dir, "split")
	for target, names := range map[string][]string{"p": {"TestA", "TestB"}, "q": {"TestC"}} {
		d := filepath.Join(splitLogs, "pkg", target)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		xml := `<testsuites><testsuite name="pkg">`
		for _, n := range names {
			xml += `<testcase name="` + n + `"></testcase>`
		}
		if err := os.WriteFile(filepath.Join(d, "test.xml"), []byte(xml+`</testsuite></testsuites>`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	splitBEP := filepath.Join(dir, "split.json")
	if err := os.WriteFile(splitBEP, []byte(`{"id":{"targetConfigured":{"label":"//pkg:p"}},"configured":{"targetKind":"sh_test rule"}}
{"id":{"testResult":{"label":"//pkg:p","run":1,"attempt":1}}}
{"id":{"targetConfigured":{"label":"//pkg:q"}},"configured":{"targetKind":"sh_test rule"}}
{"id":{"testResult":{"label":"//pkg:q","run":1,"attempt":1}}}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		suites  []string
		pass    bool
		mention string
	}{
		{"split exact", []string{"//pkg:p", "1@0/2", "//pkg:q", "1@1/2"}, true, ""},
		{"split shard run twice", []string{"//pkg:p", "1@0/2", "//pkg:q", "1@0/2"}, false, "run by more than one target"},
		{"split shard run by no target", []string{"//pkg:p", "1@0/2"}, false, "no target runs shard(s) [2]"},
		{"split range past the total", []string{"//pkg:p", "1@0/2", "//pkg:q", "1@2/2"}, false, "exceed"},
	} {
		args := []string{script, "--bep", splitBEP, "--testlogs", splitLogs}
		for i := 0; i < len(c.suites); i += 2 {
			args = append(args, "--suite", c.suites[i], shard, c.suites[i+1])
		}
		out, err := exec.Command(python, args...).CombinedOutput()
		if (err == nil) != c.pass || (c.mention != "" && !strings.Contains(string(out), c.mention)) {
			t.Errorf("%s: pass = %v, want %v (mention %q):\n%s", c.name, err == nil, c.pass, c.mention, out)
		}
	}
	if out, err := exec.Command(python, script, "--bep", writeBEP(2), "--testlogs", filepath.Join(dir, "logs0"), "--suite", label, shard, "2", "--whole", "//pkg:absent").CombinedOutput(); err == nil {
		t.Errorf("--whole for a target the BEP lacks passed:\n%s", out)
	}

	// A failing shard script fails the check, even if what it listed
	// matches; so does a missing one.
	logs := filepath.Join(dir, "logs-bad")
	writeXML(logs, 1, "TestA", "TestB")
	writeXML(logs, 2, "TestC")
	for _, c := range []struct {
		script string
		env    []string
	}{{shard, []string{"FAKE_SHARD_FAIL=1"}}, {"/nonexistent/shard.sh", nil}} {
		cmd := exec.Command(python, script, "--bep", writeBEP(2), "--testlogs", logs, "--suite", label, c.script, "2")
		cmd.Env = append(os.Environ(), c.env...)
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "failed") {
			t.Errorf("shard script %s %v: check passed or did not say it failed:\n%s", c.script, c.env, out)
		}
	}
}

// walkBazelFiles visits every BUILD.bazel and BUILD file under root (and,
// with bzl, every .bzl file), skipping .git, node_modules and .beads. A
// symlink to a regular file is visited: under `bazel test` on a local
// executor the runfiles tree is a symlink forest, and a walk that skipped
// symlinks read no BUILD file there and reported every lane's targets gone
// (#7350). A symlink to a directory is never followed.
func walkBazelFiles(root string, bzl bool, visit func(path string, d os.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".beads":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name != "BUILD.bazel" && name != "BUILD" && !(bzl && strings.HasSuffix(name, ".bzl")) {
			return nil
		}
		if !isFileOrFileLink(path, d) {
			return nil
		}
		return visit(path, d)
	})
}

// A local Bazel executor hands the test a runfiles tree in which every file
// is a symlink into the sandbox's inputs (#7350: scripts_test ran there on an
// unauthorized fork's PR, read no BUILD file and failed every lane's pin with
// `got map[]`). The walk must see the same Bazel files through such a forest
// as in the checkout, and must not follow a symlinked directory or trip on a
// dangling link.
func TestBazelFileWalkFollowsFileSymlinks(t *testing.T) {
	root := sourceRepoRoot(t)
	collect := func(root string) []string {
		var rels []string
		err := walkBazelFiles(root, true, func(path string, _ os.DirEntry) error {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rels = append(rels, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		sort.Strings(rels)
		return rels
	}
	checkout := collect(root)
	if len(checkout) < 10 {
		t.Fatalf("found only %d Bazel files under %s; the checkout walk is broken", len(checkout), root)
	}

	forest := filepath.Join(t.TempDir(), "_main")
	for _, rel := range checkout {
		dst := filepath.Join(forest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, filepath.FromSlash(rel)), dst); err != nil {
			t.Fatal(err)
		}
	}
	// Hazards a runfiles tree or a checkout can hold: Bazel's bazel-* links to
	// directories, and a link whose target is gone.
	if err := os.Symlink(filepath.Join(root, "scripts"), filepath.Join(forest, "bazel-bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(forest, "missing", "BUILD.bazel"), filepath.Join(forest, "BUILD")); err != nil {
		t.Fatal(err)
	}

	if got := collect(forest); !reflect.DeepEqual(got, checkout) {
		t.Errorf("the symlink forest yields different Bazel files than the checkout:\nforest   %d: %v\ncheckout %d: %v", len(got), got, len(checkout), checkout)
	}
}

// Review G3: since D2 the retired tiers' Bazel lanes (embedded, proxied,
// server-storage) are those tiers' only pre-merge run on same-repo PRs, so
// nothing that reaches them may narrow them (select fewer tests, or turn
// them into skips) without a reviewed edit of this test.
// TestBazelEmbeddedJobRunsEmbeddedTier and TestBazelRetiredLanesArePinned
// pin the command lines and each lane config's lines; this covers
// everything else that applies to the lanes: every other .bazelrc line of a
// config a lane uses (the unconfigured ones, remote-exec and fork-cache,
// which setup-bazel's rc enables, and any config those pull in), rc files
// that would be try-imported, the tools/bazel scripts every test runs under
// or through, the whole setup-bazel action, and the lanes' targets' args and
// env. At run time, check_shard_coverage.py also fails a shard of only
// skips.
func TestBazelRetiredLanesCannotBeNarrowed(t *testing.T) {
	root := sourceRepoRoot(t)
	rcNarrow := regexp.MustCompile(`test_filter|test_arg|-test\.|_filters\b|test_env=(BEADS_TEST|GO_TEST|TESTBRIDGE)|--config=|cache_test_results|eviction_retries|run_under|flaky|runs_per_test|test_sharding_strategy|build_tests_only`)
	const runUnder = "test --run_under=//tools/bazel:test_env"
	wantImports := []string{"try-import %workspace%/.bazelrc.local", "try-import %workspace%/user.bazelrc"}

	type rcLine struct{ cmd, config, text string }
	var lines []rcLine
	var imports []string
	sawRunUnder := false
	for _, raw := range strings.Split(readPolicyFile(t, root, ".bazelrc"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, _, _ := strings.Cut(line, " ")
		if head == "import" || head == "try-import" {
			imports = append(imports, line)
			continue
		}
		cmd, config, _ := strings.Cut(head, ":")
		lines = append(lines, rcLine{cmd, config, line})
	}
	if !reflect.DeepEqual(imports, wantImports) {
		t.Errorf(".bazelrc imports %v, want exactly %v (an import can carry any flag into the lanes)", imports, wantImports)
	}
	// The configs the lanes use: their own (pinned), the configs
	// setup-bazel's generated rc enables for every command (remote-exec,
	// and fork-cache in mode cache, where the remote-only lanes are skipped
	// but the rc still applies), the unconfigured lines, and anything they
	// reference (which the check below then forbids anyway).
	laneConfigs := map[string][]string{"embedded": bazelEmbeddedRCLines}
	for config, want := range bazelDoltServerRCLines {
		laneConfigs[config] = want
	}
	// D2 step 3's lanes (PR-core, dolt-server, pure-Go/js-wasm, sole-run).
	for config, want := range bazelPRLaneRCLines {
		laneConfigs[config] = want
	}
	rcEnabled := map[string]bool{"remote-exec": true, "fork-cache": true}
	inUse := map[string]bool{"": true}
	for c := range laneConfigs {
		inUse[c] = true
	}
	for c := range rcEnabled {
		inUse[c] = true
	}
	for changed := true; changed; {
		changed = false
		for _, l := range lines {
			if !inUse[l.config] {
				continue
			}
			for _, m := range regexp.MustCompile(`--config=([A-Za-z0-9_-]+)`).FindAllStringSubmatch(l.text, -1) {
				if !inUse[m[1]] {
					inUse[m[1]], changed = true, true
				}
			}
		}
	}
	pinned := map[string]bool{}
	for _, want := range laneConfigs {
		for _, l := range want {
			pinned[l] = true
		}
	}
	for _, l := range lines {
		if !inUse[l.config] || pinned[l.text] {
			continue
		}
		if _, lane := laneConfigs[l.config]; lane {
			t.Errorf(".bazelrc %q: not one of the pinned --config=%s lines", l.text, l.config)
			continue
		}
		if l.text == runUnder {
			sawRunUnder = true
			continue
		}
		if rcNarrow.MatchString(l.text) {
			t.Errorf(".bazelrc %q applies to the retired tiers' lanes (config %q) and selects, narrows or re-runs tests", l.text, l.config)
		}
	}
	if !sawRunUnder {
		t.Errorf(".bazelrc lacks %q (the wrapper the narrowing checks below cover)", runUnder)
	}

	// Committed rc files: only .bazelrc. .bazelrc.local and user.bazelrc are
	// developer-local (gitignored) and would be try-imported into CI runs.
	// Under Bazel the listing is //:repo_other_files, every tracked file but
	// Go and Markdown source (no rc file is either): several hundred files.
	for _, f := range repoFiles(t, root, 300) {
		base := filepath.Base(f)
		if strings.Contains(base, "bazelrc") && f != ".bazelrc" && f != setupBazelActionDir+"/write-bazelrc.sh" {
			t.Errorf("committed rc file %s: .bazelrc's try-import would load it into every CI run", f)
		}
	}

	// The scripts every lane's test runs under or through, and the whole
	// setup-bazel action (its generated rc applies to every command).
	scriptNarrow := regexp.MustCompile(`-test\.(short|run|skip|list|bench)|BEADS_TEST_SKIP|BEADS_TEST_EMBEDDED_DOLT|BEADS_TEST_PROXIED_SERVER|BEADS_TEST_ENV_RUN_DOLT|BEADS_TEST_REQUIRE_DOLT_CONTAINER|BEADS_TEST_DOLT_SERVER\b|TESTBRIDGE_TEST_ONLY|test_filter|test_arg|_filters\b|cache_test_results|eviction_retries|flaky|runs_per_test|test_sharding_strategy`)
	files, _ := filepath.Glob(filepath.Join(root, "tools", "bazel", "*.sh"))
	action, _ := filepath.Glob(filepath.Join(root, setupBazelActionDir, "*"))
	files = append(files, action...)
	if len(files) < 5 {
		t.Fatalf("found only %v", files)
	}
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// go_test_pinned_shard.sh selects and skips by design (each test in
		// exactly one shard: TestPinnedShardWrapperSplit), and is reviewed
		// here for pinnedShardTargets only (the dolt-server-cmd targets and
		// the embedded lane's httpclient_served_test, whose args are pinned
		// above); pinnedShardWrapperUsers fails if anything else runs
		// through it.
		exempt := filepath.ToSlash(rel) == pinnedShardWrapper
		if exempt {
			for _, e := range pinnedShardWrapperUsers(t, root) {
				t.Error(e)
			}
		}
		for i, line := range strings.Split(string(data), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") {
				continue
			}
			if scriptNarrow.MatchString(code) && !exempt {
				t.Errorf("%s:%d %q can select, skip or re-run the retired tiers' lanes' tests", rel, i+1, code)
			}
			for _, m := range regexp.MustCompile(`--config=([A-Za-z0-9_-]+)`).FindAllStringSubmatch(code, -1) {
				if !rcEnabled[m[1]] {
					t.Errorf("%s:%d enables --config=%s for every command", rel, i+1, m[1])
				}
			}
		}
	}

	// Review F2 (step 2): the shard scripts the sharded targets run (the
	// legacy jobs run the same scripts, so they are not changed here): the
	// command that runs the selected tests is pinned exactly, and nothing
	// else in them may select, skip, export a tier switch or run tests.
	for _, c := range bazelShardScripts {
		for _, e := range shardScriptNarrowing(c, readPolicyFile(t, root, c.script)) {
			t.Error(e)
		}
	}

	// The lanes' targets (tagged embedded, dolt-server-proxied or
	// dolt-server-integration): exactly these, with exactly these args and
	// env (the legacy jobs' flags; the shard scripts add the rest).
	type target struct {
		args []string
		env  map[string]string
	}
	embeddedCmdEnv := map[string]string{
		"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd)", "BEADS_TEST_EMBEDDED_DOLT": "1", "BEADS_TEST_GOFMT": "$(rlocationpath @go_sdk//:bin/gofmt)",
		// bdInit's schema template and the race runtime's exit sleep:
		// neither selects or skips a test.
		"BEADS_TEST_EMBEDDED_SCHEMA_TOOL": "$(rlocationpath //internal/storage/embeddeddolt/cmd:cmd_norace)", "GORACE": "atexit_sleep_ms=0",
	}
	want := map[string]target{
		"//cmd/bd:bd_embedded_test": {
			[]string{"--shard-offset=0", "--shard-total=100", "$(rootpath //:.github/scripts/embedded-test-shard.sh)", "BEADS_TEST_CMD_BINARY", "$(rootpath :bd_test)", "-test.timeout=19m"},
			embeddedCmdEnv,
		},
		"//cmd/bd:bd_embedded_part2_test": {
			[]string{"--shard-offset=50", "--shard-total=100", "$(rootpath //:.github/scripts/embedded-test-shard.sh)", "BEADS_TEST_CMD_BINARY", "$(rootpath :bd_test)", "-test.timeout=19m"},
			embeddedCmdEnv,
		},
		"//internal/storage/embeddeddolt:embeddeddolt_embedded_test": {
			[]string{"$(rootpath //:.github/scripts/embedded-storage-test-shard.sh)", "BEADS_TEST_EMBEDDED_TEST_BINARY", "$(rootpath :embeddeddolt_test)", "-test.timeout=19m"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		"//internal/storage/embeddeddolt:embeddeddolt_conformance_core_test": {
			[]string{"$(rootpath :embeddeddolt_test)", "-test.v", "-test.count=1", "-test.timeout=19m", "-test.run=^TestConformance$$", "-test.skip=^TestConformance$$/^Audit$$"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		"//internal/storage/embeddeddolt:embeddeddolt_conformance_audit_test": {
			[]string{"$(rootpath :embeddeddolt_test)", "-test.v", "-test.count=1", "-test.timeout=19m", "-test.run=^TestConformance$$/^Audit$$"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		// nightly.yml's retired non-race embedded batch-apply step: the large
		// shapes every race lane shrinks or skips (raceEnabled).
		"//internal/storage/embeddeddolt:embeddeddolt_batch_apply_nonrace_test": {
			[]string{"$(rootpath :embeddeddolt_race_off)", "-test.v", "-test.count=1", "-test.timeout=19m", "-test.run=^(TestBatchApplyContract|TestLargeBatchApplyWallClock_Embedded|TestLargeBatchApplyStatementCounts712_Embedded|TestCreateBatchFastPathsMatchPerRowLarge_Embedded)$$"},
			map[string]string{"BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		// scripts/conformance.sh's Tier 3, the served HTTP corpus, with the
		// script's switches: required, so a missing engine fails each served
		// case instead of skipping it.
		"//internal/httpclient:httpclient_served_test": {
			[]string{"$(rootpath :served_pinned_shards.txt)", "$(rootpath :httpclient_test)", "-test.v", "-test.count=1", "-test.timeout=19m"},
			map[string]string{"BEADS_HTTP_TEST_REQUIRED": "1", "BEADS_TEST_EMBEDDED_DOLT": "1"},
		},
		"//cmd/bd:bd_proxied_test": {
			[]string{"$(rootpath //:.github/scripts/proxied-test-shard.sh)", "BEADS_TEST_CMD_BINARY", "$(rootpath :bd_test)"},
			map[string]string{
				"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd_for_tests)", "BEADS_TEST_DOLT_SERVER": "local", "BEADS_TEST_GIT_IDENTITY": "1",
				"BEADS_TEST_GOFMT": "$(rlocationpath @go_sdk//:bin/gofmt)", "BEADS_TEST_PROXIED_SERVER": "1",
				"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "BEADS_TEST_REQUIRE_SOCAT": "1", "GOMAXPROCS": "4",
			},
		},
		"//cmd/bd:bd_managed_local_test": {
			[]string{"$(rootpath :bd_test)", "-test.run=^TestManagedLocalProxied", "-test.timeout=15m"},
			map[string]string{
				"BEADS_TEST_BD_BINARY": "$(rlocationpath :bd_for_tests)", "BEADS_TEST_DOLT_SERVER": "local",
				"BEADS_TEST_GOFMT": "$(rlocationpath @go_sdk//:bin/gofmt)", "BEADS_TEST_PREFLIGHT_GO": "$(rlocationpath :preflight_go_fixture)",
				"BEADS_TEST_PROXIED_LOCAL": "1", "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "BEADS_TEST_SKIP": "dolt",
			},
		},
		"//internal/storage/dolt:dolt_server_conformance_test": {
			[]string{"$(rootpath :dolt_race_off)", "-test.v", "-test.count=1", "-test.timeout=15m", "-test.run=^TestConformance$$"},
			map[string]string{"BEADS_TEST_DOLT_SERVER": "local", "BEADS_TEST_GIT_IDENTITY": "1", "BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "GOMAXPROCS": "4"},
		},
		"//internal/storage/dolt:dolt_server_full_test": {
			[]string{"$(rootpath //:.github/scripts/server-storage-test-shard.sh)", "BEADS_TEST_SERVER_TEST_BINARY", "$(rootpath :dolt_race_off)"},
			map[string]string{
				"BEADS_TEST_DOLT_SERVER": "local", "BEADS_TEST_ENV_RUN_DOLT": "1", "BEADS_TEST_GIT_IDENTITY": "1",
				"BEADS_TEST_REQUIRE_DOLT_CONTAINER": "1", "BEADS_TEST_SUBPROCESS_BINARY": "$(rlocationpath :dolt_race_off)", "GOMAXPROCS": "4",
			},
		},
	}
	laneTags := regexp.MustCompile(`"(embedded|dolt-server-proxied|dolt-server-integration)"`)
	quoted := regexp.MustCompile(`"([^"]*)"`)
	envPair := regexp.MustCompile(`"([^"]*)":\s*"([^"]*)"`)
	nameRe := regexp.MustCompile(`(?m)^    name = "([^"]+)",$`)
	tagsRe := regexp.MustCompile(`(?ms)^    tags = \[(.*?)\],$`)
	envRe := regexp.MustCompile(`(?ms)^    env = \{(.*?)\},$`)
	got := map[string]target{}
	err := walkBazelFiles(root, false, func(path string, d os.DirEntry) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pkg, _ := filepath.Rel(root, filepath.Dir(path))
		for _, rule := range bazelTopRules(string(data)) {
			tags := tagsRe.FindStringSubmatch(rule)
			if tags == nil || !laneTags.MatchString(tags[1]) {
				continue
			}
			name := nameRe.FindStringSubmatch(rule)
			if name == nil {
				t.Errorf("%s: a retired lane's rule without a literal name:\n%s", pkg, rule)
				continue
			}
			var tg target
			for _, q := range quoted.FindAllStringSubmatch(bazelAttrBlock(rule, "args"), -1) {
				tg.args = append(tg.args, q[1])
			}
			tg.env = map[string]string{}
			if e := envRe.FindStringSubmatch(rule); e != nil {
				for _, p := range envPair.FindAllStringSubmatch(e[1], -1) {
					tg.env[p[1]] = p[2]
				}
			}
			if strings.Contains(rule, "args = select") || strings.Contains(rule, "env = select") || strings.Contains(rule, "env_inherit") {
				t.Errorf("//%s:%s sets args/env indirectly; keep them literal so this check sees them", pkg, name[1])
			}
			got["//"+filepath.ToSlash(pkg)+":"+name[1]] = tg
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the retired lanes' targets' args/env changed:\ngot  %v\nwant %v", got, want)
	}

	// D2 step 3: the PR-core, dolt-server and pure lanes run (nearly) every
	// other test target, too many to pin one by one: no rule outside the
	// pinned ones above, and no .bzl macro, may select, skip or switch off
	// tests through its args, env or anything else.
	ruleNarrow := regexp.MustCompile(`-test\.(short|run|skip|list|bench)|BEADS_TEST_SKIP|BEADS_TEST_EMBEDDED_DOLT|TESTBRIDGE_TEST_ONLY|test_filter|flaky\s*=\s*(True|1|[A-Za-z_])`)
	// Second runs over tests another target already runs in full: the
	// selection is the point (a required-suite contract that checks it), and
	// nothing leaves the lanes. Pinned to exactly these args.
	extraRunVariants := map[string][]string{
		// The doc-freshness suite (also run by //scripts:shell_scripts_test) under
		// -required-suite, as pr.yml's former Linux doc-freshness leg ran it.
		"//scripts:doc_freshness_required_test": {
			"-test.count=1",
			"-test.run=^(TestDocFreshness.*|TestRequiredSuiteContract)$$",
			"-required-suite=doc-freshness",
		},
	}
	err = walkBazelFiles(root, true, func(path string, d os.DirEntry) error {
		isBzl := strings.HasSuffix(d.Name(), ".bzl")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))
		var units []string
		if isBzl {
			units = []string{string(data)}
		} else {
			units = bazelTopRules(string(data))
		}
		for _, unit := range units {
			if !isBzl {
				if name := nameRe.FindStringSubmatch(unit); name != nil {
					label := "//" + pkg + ":" + name[1]
					if _, pinned := want[label]; pinned {
						continue
					}
					if wantArgs, ok := extraRunVariants[label]; ok {
						var args []string
						for _, q := range quoted.FindAllStringSubmatch(bazelAttrBlock(unit, "args"), -1) {
							args = append(args, q[1])
						}
						if !reflect.DeepEqual(args, wantArgs) {
							t.Errorf("%s args = %q, want exactly %q", label, args, wantArgs)
						}
						continue
					}
				}
			}
			for _, line := range strings.Split(unit, "\n") {
				code, _, _ := strings.Cut(line, "#")
				if ruleNarrow.MatchString(code) && !strings.Contains(code, "flaky = False") {
					t.Errorf("%s: %q can select, skip or retry tests of the gated lanes", rel, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Review G4: each retired tier's lane asks Bazel which test targets are
// flaky (a grep of BUILD files misses `flaky = _VAR` or a macro default)
// before it runs the tier, and fails on any.
func TestBazelRetiredLanesQueryFlakyTargets(t *testing.T) {
	const want = `flaky="$(bazel query 'attr(flaky, 1, tests(//...))')"
if [ -n "$flaky" ]; then
  echo "::error::flaky = True on ${flaky//$'\n'/ }: the gated lanes run each test once"
  exit 1
fi`
	for lane, config := range bazelRetiredLaneConfigs {
		job := readCIWorkflow(t, bazelWorkflowName).job(t, lane)
		step := job.step(t, "No test target is marked flaky")
		if strings.TrimSpace(step.Run) != want || step.If != "" || step.ContinueOnError != nil {
			t.Errorf("%s flaky query step: if %q, continue-on-error %v, run:\n%s\nwant run:\n%s", lane, step.If, step.ContinueOnError, step.Run, want)
		}
		if !(job.stepIndex(t, "Set up Bazel") < job.stepIndex(t, step.Name) && job.stepIndex(t, step.Name) < job.stepIndex(t, "bazel test //... --config="+config)) {
			t.Errorf("%s flaky query step must run after setup-bazel and before the tier", lane)
		}
	}
}

// The retired tiers' Bazel lanes and the config each runs.
var bazelRetiredLaneConfigs = map[string]string{
	bazelEmbedJobName:   "embedded",
	bazelProxiedJobName: "doltserver-proxied",
	bazelServerJobName:  "doltserver-integration",
}

// bazelTierTestRun: a retired lane's test step, exactly (review F4).
func bazelTierTestRun(config string) string {
	return strings.ReplaceAll(bazelEmbeddedTestRun, "--config=embedded", "--config="+config)
}

// .bazelrc's --config=doltserver-proxied and --config=doltserver-integration,
// exactly and in order (D2 step 2, as review F4 for embedded): no
// --test_filter, no -test.short/-test.run/-test.skip, no retries (results
// are cached like every lane's; nightly's --config=fresh re-executes). Their targets' own args (the conformance target's -test.run, the
// legacy job's) are pinned by TestBazelRetiredLanesCannotBeNarrowed.
var bazelDoltServerRCLines = map[string][]string{
	"doltserver-proxied": {
		"test:doltserver-proxied --@rules_go//go/config:race",
		"test:doltserver-proxied --test_tag_filters=dolt-server-proxied",
		"test:doltserver-proxied --build_tests_only",
		"test:doltserver-proxied --keep_going",
		"test:doltserver-proxied --test_summary=terse",
		"test:doltserver-proxied --test_timeout=-1,-1,-1,1200",
		"test:doltserver-proxied --test_env=GO_TEST_WRAP_TESTV=1",
		"test:doltserver-proxied --test_arg=-test.parallel=4",
		"test:doltserver-proxied --local_test_jobs=4",
		"test:doltserver-proxied --remote_download_regex=.*/test\\.(log|xml)$",
		"test:doltserver-proxied --experimental_remote_cache_eviction_retries=0",
	},
	"doltserver-integration": {
		"build:doltserver-integration --@rules_go//go/config:tags=gms_pure_go,integration",
		"test:doltserver-integration --@rules_go//go/config:race",
		"test:doltserver-integration --test_tag_filters=dolt-server-integration",
		"test:doltserver-integration --build_tests_only",
		"test:doltserver-integration --keep_going",
		"test:doltserver-integration --test_summary=terse",
		"test:doltserver-integration --test_timeout=-1,-1,-1,1200",
		"test:doltserver-integration --test_env=GO_TEST_WRAP_TESTV=1",
		"test:doltserver-integration --test_arg=-test.parallel=4",
		"test:doltserver-integration --local_test_jobs=4",
		"test:doltserver-integration --remote_download_regex=.*/test\\.(log|xml)$",
		"test:doltserver-integration --experimental_remote_cache_eviction_retries=0",
	},
}

// D2 step 2, as review F2/F4 for embedded: the proxied and server lanes run
// exactly `bazel test //... --config=<config>` (plus nightly's BAZEL_FRESH)
// with a BEP and nothing else, their configs are exactly the pinned lines,
// and only test:fresh sets result caching.
func TestBazelRetiredLanesArePinned(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	for lane, config := range bazelRetiredLaneConfigs {
		test := workflow.job(t, lane).step(t, "bazel test //... --config="+config)
		if strings.TrimSpace(test.Run) != bazelTierTestRun(config) {
			t.Errorf("%s test step run changed; want exactly:\n%s\ngot:\n%s", lane, bazelTierTestRun(config), test.Run)
		}
	}
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	for config, want := range bazelDoltServerRCLines {
		var got []string
		for _, line := range strings.Split(rc, "\n") {
			line = strings.TrimSpace(line)
			head, _, _ := strings.Cut(line, " ")
			if strings.HasSuffix(head, ":"+config) {
				got = append(got, line)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf(".bazelrc --config=%s lines changed; want exactly:\n%s\ngot:\n%s", config, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
		// Results are cached like every lane's; only nightly's
		// --config=fresh re-executes them (ci_merge_queue_test.go).
	}
	// Review F1 (step 2): no whole-invocation retry after a remote cache
	// eviction in any retired lane (it would re-run, and could turn green,
	// a test that failed in the first attempt), and nothing else in
	// .bazelrc sets the retry count (a later value would win).
	evictionAllowed := map[string]bool{bazelSoleRunEvictionLine: true}
	for _, config := range bazelRetiredLaneConfigs {
		want := "test:" + config + " --experimental_remote_cache_eviction_retries=0"
		lines := bazelEmbeddedRCLines
		if config != "embedded" {
			lines = bazelDoltServerRCLines[config]
		}
		if !contains(lines, want) {
			t.Errorf("pinned --config=%s lacks %q", config, want)
		}
		evictionAllowed[want] = true
	}
	for _, line := range strings.Split(rc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "remote_cache_eviction_retries") && !evictionAllowed[line] {
			t.Errorf(".bazelrc %q: only the retired tiers' configs set the eviction retry count (to 0)", line)
		}
	}
	// A later --cache_test_results (any config the lanes use) would win.
	// Only nightly's --config=fresh (appended only when the caller asks,
	// ci_merge_queue_test.go) turns result caching off.
	allowed := map[string]bool{bazelFreshRCLine: true}
	for _, line := range strings.Split(rc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "cache_test_results") && !allowed[line] {
			t.Errorf(".bazelrc %q: only test:fresh sets test result caching", line)
		}
	}
}

// bazelShardScript: a manifest shard script a retired lane's sharded target
// runs, and how it runs the selected tests.
type bazelShardScript struct {
	script, binVar, timeout, goTags string
	race                            bool
	pkg, cd                         string // cd: the directory the prebuilt binary runs in ("" = none)
}

var bazelShardScripts = []bazelShardScript{
	{".github/scripts/embedded-test-shard.sh", "CMD_BINARY", "20m", "gms_pure_go", true, "./cmd/bd/", ""},
	{".github/scripts/embedded-storage-test-shard.sh", "STORAGE_BINARY", "20m", "gms_pure_go", true, "./internal/storage/embeddeddolt/", ""},
	{".github/scripts/proxied-test-shard.sh", "CMD_BINARY", "15m", "gms_pure_go", false, "./cmd/bd/", ""},
	{".github/scripts/server-storage-test-shard.sh", "STORAGE_BINARY", "15m", "integration,gms_pure_go", false, "./internal/storage/dolt/", "internal/storage/dolt"},
}

// tail: the script's code lines (comments and blank lines dropped) from the
// prebuilt-binary branch to the end of the file, exactly.
func (c bazelShardScript) tail() []string {
	race := ""
	if c.race {
		race = "-race "
	}
	out := []string{`if [ -x "$` + c.binVar + `" ]; then`}
	if c.cd != "" {
		out = append(out, "cd "+c.cd)
	}
	return append(out,
		`exec "$`+c.binVar+`" -test.v -test.count=1 -test.timeout=`+c.timeout+` \`,
		`-test.run "$RUN_REGEX" \`,
		`"$@"`,
		"else",
		`echo "Warning: pre-built test binary not found at $`+c.binVar+`, falling back to go test"`,
		`exec go test -tags=`+c.goTags+` -v `+race+`-count=1 -timeout `+c.timeout+` \`,
		`-run "$RUN_REGEX" \`,
		`"$@" \`,
		c.pkg,
		"fi",
	)
}

const bazelShardRunRegex = `RUN_REGEX="^($(IFS='|'; echo "${SHARD_TESTS[*]}"))$"`

// shardScriptNarrowing: why src does not run exactly c.tail() as its last code
// lines, builds RUN_REGEX from the selection exactly once, and outside the
// tail neither runs tests nor selects, skips or switches them.
func shardScriptNarrowing(c bazelShardScript, src string) []string {
	var errs []string
	var code []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			code = append(code, line)
		}
	}
	want := c.tail()
	if len(code) < len(want) || !reflect.DeepEqual(code[len(code)-len(want):], want) {
		got := code
		if len(code) > len(want) {
			got = code[len(code)-len(want):]
		}
		return append(errs, fmt.Sprintf("%s: the command that runs the shard changed; want exactly:\n%s\ngot:\n%s", c.script, strings.Join(want, "\n"), strings.Join(got, "\n")))
	}
	narrow := regexp.MustCompile(`-test\.|(^|\s)-(short|run|skip|count|list|bench)\b|\bexec\b|\bgo (test|run)\b|"\$@"|\$\*|\beval\b|BEADS_TEST_(SKIP|EMBEDDED_DOLT|PROXIED_SERVER|ENV_RUN_DOLT|REQUIRE_DOLT_CONTAINER|DOLT_SERVER\b)|GOFLAGS|TESTBRIDGE|RUN_REGEX`)
	runRegex := 0
	for _, line := range code[:len(code)-len(want)] {
		if line == bazelShardRunRegex {
			runRegex++
			continue
		}
		if narrow.MatchString(line) {
			errs = append(errs, fmt.Sprintf("%s: %q can run, select, skip or switch the shard's tests outside its pinned command", c.script, line))
		}
	}
	if runRegex != 1 {
		errs = append(errs, fmt.Sprintf("%s: want exactly one %s, got %d", c.script, bazelShardRunRegex, runRegex))
	}
	return errs
}

// The shard script check itself, on the real scripts and on narrowing edits
// of one (review F2 mutations m2 and m3).
func TestShardScriptNarrowingCheck(t *testing.T) {
	root := sourceRepoRoot(t)
	scripts := map[string]bool{}
	for _, c := range bazelShardScripts {
		scripts[c.script] = true
	}
	// Every retired lane's sharded target runs one of these scripts.
	for _, c := range []string{"embedded-test-shard.sh", "embedded-storage-test-shard.sh", "proxied-test-shard.sh", "server-storage-test-shard.sh"} {
		if !scripts[".github/scripts/"+c] {
			t.Errorf("bazelShardScripts lacks %s", c)
		}
	}
	c := bazelShardScripts[3]
	src := readPolicyFile(t, root, c.script)
	for name, edit := range map[string][2]string{
		"exec -test.short":        {`-test.timeout=15m \`, `-test.timeout=15m -test.short \`},
		"export BEADS_TEST_SKIP":  {"set -euo pipefail\n", "set -euo pipefail\nexport BEADS_TEST_SKIP=dolt\n"},
		"GOFLAGS":                 {"set -euo pipefail\n", "set -euo pipefail\nexport GOFLAGS=-short\n"},
		"second exec":             {"set -euo pipefail\n", "set -euo pipefail\n[ -n \"${X:-}\" ] && exec true\n"},
		"narrowed regex":          {bazelShardRunRegex, `RUN_REGEX="^(${SHARD_TESTS[0]})$"`},
		"go test fallback -short": {`-v -count=1 -timeout 15m \`, `-v -short -count=1 -timeout 15m \`},
	} {
		if strings.Count(src, edit[0]) == 0 {
			t.Fatalf("%s: mutation %q no longer applies", c.script, name)
		}
		if len(shardScriptNarrowing(c, strings.Replace(src, edit[0], edit[1], 1))) == 0 {
			t.Errorf("%s: mutation %q passes the shard script check", c.script, name)
		}
	}
}
