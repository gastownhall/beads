package scripts_test

import (
	"cmp"
	"maps"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The dolt-race union lane (item 5): in mode remote, bazel.yml's
// bazel-dolt-race job runs --config=dolt-race, the union of
// --config=doltserver and --config=doltserver-proxied, in one `bazel test`,
// in place of the bazel-doltserver and bazel-proxied jobs; in every other
// mode those two run as before. Its job.status is both members' workflow_call
// output, so pr.yml's gate ids (BAZEL_DOLTSERVER, BAZEL_PROXIED), ci-gate.sh
// and bazel-gate.sh are unchanged. Outside mode remote one client would do
// both tiers' work with no in-flight deduplication, so the union would cost
// the sum of the two tiers rather than the max: the members stay there.

const (
	bazelDoltRaceJobName = "bazel-dolt-race"
	bazelDoltRaceConfig  = "dolt-race"
	// Mode remote, on every event that reaches it (same-repo pull requests,
	// merge groups, push, nightly, dispatch, bazel-farm.yml's authorized
	// call): exactly where the members do not run. Its actual if adds the
	// caller's dolt-tiers switch (withDoltTiers), which pr.yml turns off on
	// a merge group.
	bazelDoltRaceJobIf = "${{ needs.rbe.outputs.mode == 'remote' }}"
	// The members: their pre-union rules (bazel-test's skip rule; remote
	// execution only), minus mode remote (and, in their actual ifs, plus the
	// dolt-tiers switch).
	bazelDoltMemberIf    = "${{ needs.rbe.outputs.mode != 'skip' && needs.rbe.outputs.mode != 'remote' }}"
	bazelProxiedMemberIf = "${{ needs.rbe.outputs.enabled == 'true' && needs.rbe.outputs.mode != 'remote' }}"
)

// bazelUnionLanes: a union job -> the gated member jobs it runs in place of
// in mode remote (bazelDoltRaceJobIf). Each member keeps its gate id
// (bazelLaneGateIDs) and its workflow_call output, which reads the union's
// result first and the member's own otherwise (bazelLaneCallOutput).
var bazelUnionLanes = map[string][]string{
	bazelDoltRaceJobName: {bazelDoltJobName, bazelProxiedJobName},
}

// bazelUnionHasFlagGatedMember reports whether a union job runs in place of
// at least one flag-gated, advisory member (bazel-dolt-int's bazel-cmd-dolt,
// item 5 A2): such a union must carry that member's own continue-on-error, so
// a failure confined to that member's tier stays out of the call's aggregate
// result (BAZEL) in the union's mode exactly as it already is through the
// member outside it. The gated member's id still reads the union's result
// directly (bazelLaneReporter), unaffected either way.
func bazelUnionHasFlagGatedMember(job string) bool {
	for _, m := range bazelUnionLanes[job] {
		if _, ok := bazelFlagGatedLanes[m]; ok {
			return true
		}
	}
	return false
}

// bazelUnionOf: the union job a gated member belongs to, or "".
func bazelUnionOf(member string) string {
	for union, members := range bazelUnionLanes {
		if slices.Contains(members, member) {
			return union
		}
	}
	return ""
}

// bazelUnionMemberOutputKey: the union job's own output key that carries a
// member's tier result, for unions that do not report "result" uniformly
// (item 5 A2's bazel-dolt-int: must-fix 2's per-tier outputs, since a
// job-level continue-on-error would hide a server-storage failure from
// anything that is not itself gated on that one tier). Members not listed
// here read their union's plain "result" output, as dolt-race's do.
var bazelUnionMemberOutputKey = map[string]string{
	bazelServerJobName:  "server_storage_result",
	bazelCmdDoltJobName: "cmd_dolt_result",
}

// bazelLaneCallOutput: the workflow_call output value bazel.yml must declare
// for a lane that reports its job.status.
func bazelLaneCallOutput(lane string) string {
	if union := bazelUnionOf(lane); union != "" {
		key := cmp.Or(bazelUnionMemberOutputKey[lane], "result")
		return "${{ jobs." + union + ".outputs." + key + " || jobs." + lane + ".outputs.result }}"
	}
	return "${{ jobs." + lane + ".outputs.result }}"
}

// bazelLaneReporter: the job whose result a gated lane's output carries in
// this mode (lanes: job -> modes it runs in), or "" when neither the lane
// nor its union runs (an expected skip).
func bazelLaneReporter(lanes map[string]map[string]bool, lane, mode string) string {
	if lanes[lane][mode] {
		return lane
	}
	if union := bazelUnionOf(lane); union != "" && lanes[union][mode] {
		return union
	}
	return ""
}

var (
	bazelCallOutputsMu    sync.Mutex
	bazelCallOutputsCache map[string]string
	// bazel.yml's workflow_call input defaults, read with the outputs.
	bazelCallInputDefaults map[string]string
)

// bazelCallOutputValue evaluates bazel.yml's workflow_call output name with
// jobs (job -> its result output; missing or "" = not reported), the way
// GitHub does: what pr.yml's needs.bazel.outputs.<name> reads, for a call
// that leaves every input at its default.
func bazelCallOutputValue(t *testing.T, name string, jobs map[string]string) string {
	t.Helper()
	return bazelCallOutputValueIn(t, name, jobs, nil)
}

// bazelCallOutputValueIn is bazelCallOutputValue for a call with these
// inputs (the caller's with; an input it does not pass takes its default):
// an output may read the inputs too (dolt-tiers, what the call ran).
func bazelCallOutputValueIn(t *testing.T, name string, jobs, with map[string]string) string {
	t.Helper()
	bazelCallOutputsMu.Lock()
	if bazelCallOutputsCache == nil {
		call := readBazelWorkflowCall(t)
		bazelCallOutputsCache, bazelCallInputDefaults = map[string]string{}, map[string]string{}
		for n, out := range call.Outputs {
			bazelCallOutputsCache[n] = out.Value
		}
		for n, in := range call.Inputs {
			bazelCallInputDefaults[n] = in.Default
		}
	}
	expr, ok := bazelCallOutputsCache[name]
	ctx := map[string]string{}
	for input, v := range bazelCallInputDefaults {
		ctx["inputs."+input] = v
	}
	bazelCallOutputsMu.Unlock()
	if !ok {
		t.Fatalf("%s has no workflow_call output %s", bazelWorkflowName, name)
	}
	for input, v := range with {
		ctx["inputs."+input] = v
	}
	for _, m := range regexp.MustCompile(`jobs\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)`).FindAllStringSubmatch(expr, -1) {
		// Every job.outputs reference in a lane's own workflow_call output
		// expression carries that job's conclusion for gating purposes,
		// whatever its output key is named (plain "result", or one of a
		// union's per-tier keys: must-fix 2). jobs is keyed by job name only,
		// so every reference to a given job resolves to the same simulated
		// value regardless of which output key reads it.
		ctx[m[0]] = jobs[m[1]]
	}
	v, err := evalGHExpr(expr, ctx)
	if err != nil {
		t.Fatalf("workflow_call output %s = %q: %v", name, expr, err)
	}
	str, _ := v.(string)
	return str
}

// bazelDoltRaceJobTimeoutMargin: the minutes the union job's timeout-minutes
// must exceed the sum of all its step timeouts (runner setup, post-job
// cleanup), so a hang fails a named step, not the job.
const bazelDoltRaceJobTimeoutMargin = 5

// bazelDoltRaceMergeQueueTimeout: the merge queue's check timeout (ruleset
// merge_queue.check_response_timeout_minutes). The union job is required in
// every merge group, so its own timeout stays below it.
const bazelDoltRaceMergeQueueTimeout = 60

// bazelDoltRaceTestStepTimeout: the union's `bazel test` step timeout. Its
// shards run about 55-95 s on rbe-west, each capped at 1200 s; the step allows
// about 3 minutes past that cap so Bazel writes TIMEOUT logs before GitHub
// kills the step.
const bazelDoltRaceTestStepTimeout = 23

// The union's members, and the tag filter each owns.
var bazelDoltRaceMembers = []struct{ config, tag string }{
	{"doltserver", "dolt-server"},
	{"doltserver-proxied", "dolt-server-proxied"},
}

// bazelRCConfigLines returns .bazelrc's option lines for --config=name
// (any command: test:name, build:name, ...), as "<command> <option>"
// with the ":name" suffix stripped, in file order.
func bazelRCConfigLines(rc, name string) []string {
	var out []string
	for _, line := range strings.Split(rc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, opt, ok := strings.Cut(line, " ")
		cmd, cfg, hasCfg := strings.Cut(head, ":")
		if !ok || !hasCfg || cfg != name {
			continue
		}
		out = append(out, cmd+" "+strings.TrimSpace(opt))
	}
	return out
}

// bazelUnionEffectiveJobs: the --jobs value in effect when a union job's
// `bazel test //... --config=<unionConfig>` step runs in mode remote
// (closed PR #7542). Mode remote's generated rc applies
// `build --config=remote-exec` from an rc file, so --config=remote-exec's
// --jobs line is the baseline every union inherits; but a --jobs line on
// the union's own config outranks it, because that --config is given on
// the command line (the step's own `--config=<unionConfig>`) and
// command-line config outranks rc-file config regardless of which file's
// line comes first (PR #7542's BEP showed `optionsParsed.cmdLine` carrying
// "--jobs=64, --jobs=128" in that rank order, after a union config added
// its own --jobs=128 override; the ordering there is a consequence of the
// rank, not the rule itself, so reordering .bazelrc's lines cannot change
// it). Reading remote-exec's line alone would miss that override silently
// passing a budget check the real run does not meet.
func bazelUnionEffectiveJobs(t *testing.T, rc, unionConfig string) int {
	t.Helper()
	base := regexp.MustCompile(`(?m)^build:remote-exec --jobs=(\d+)$`).FindStringSubmatch(rc)
	if base == nil {
		t.Fatal(".bazelrc has no build:remote-exec --jobs=N line")
	}
	budget, _ := strconv.Atoi(base[1])
	jobsLine := regexp.MustCompile(`^--jobs=(\d+)$`)
	for _, l := range bazelRCConfigLines(rc, unionConfig) {
		_, opt, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		if m := jobsLine.FindStringSubmatch(opt); m != nil {
			budget, _ = strconv.Atoi(m[1])
		}
	}
	return budget
}

// TestBazelDoltRaceConfigIsMemberUnion: --config=dolt-race is exactly the
// set union of its members' lines, except that each member's
// --test_tag_filters is replaced by one filter naming both members' tags.
// Every flag stays a member's own, so each member target's test action is
// unchanged: `bazel aquery 'mnemonic("TestRunner", ...)'` over each member's
// targets gives byte-identical actions (key, argv, env, outputs) under the
// member config and under the union (item 5 A1b, 62 actions then; today's
// count is bazelDoltRaceTestActions). Both arms run, the mode deciding
// which, so drift between them would split results by mode: a flag added to
// a member must be added to the union too.
func TestBazelDoltRaceConfigIsMemberUnion(t *testing.T) {
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	const tagPrefix = "test --test_tag_filters="

	var want []string
	var tags []string
	for _, m := range bazelDoltRaceMembers {
		lines := bazelRCConfigLines(rc, m.config)
		if len(lines) == 0 {
			t.Fatalf(".bazelrc has no --config=%s lines", m.config)
		}
		var memberTags []string
		for _, l := range lines {
			if v, ok := strings.CutPrefix(l, tagPrefix); ok {
				memberTags = append(memberTags, v)
				continue
			}
			if !slices.Contains(want, l) {
				want = append(want, l)
			}
		}
		if len(memberTags) != 1 || memberTags[0] != m.tag {
			t.Errorf("--config=%s tag filters %v, want exactly [%s]", m.config, memberTags, m.tag)
		}
		tags = append(tags, m.tag)
	}

	got := bazelRCConfigLines(rc, bazelDoltRaceConfig)
	var gotTags []string
	var gotRest []string
	for _, l := range got {
		if v, ok := strings.CutPrefix(l, tagPrefix); ok {
			gotTags = append(gotTags, v)
			continue
		}
		gotRest = append(gotRest, l)
	}
	if wantTag := strings.Join(tags, ","); len(gotTags) != 1 || gotTags[0] != wantTag {
		t.Errorf("--config=%s tag filters %v, want exactly [%s]", bazelDoltRaceConfig, gotTags, wantTag)
	}
	if !sameStringSet(gotRest, want) {
		t.Errorf("--config=%s is not the union of its members' lines (tag filter aside):\ngot:\n%s\nwant (any order):\n%s",
			bazelDoltRaceConfig, strings.Join(gotRest, "\n"), strings.Join(want, "\n"))
	}
	if len(gotRest) != len(want) {
		t.Errorf("--config=%s repeats a line: %d lines for %d distinct member lines", bazelDoltRaceConfig, len(gotRest), len(want))
	}
	// No nested --config: the union is spelled out, so the policy above
	// sees every flag.
	for _, l := range got {
		if strings.Contains(l, "--config=") {
			t.Errorf("--config=%s line %q: spell the members' flags out, no nested --config", bazelDoltRaceConfig, l)
		}
	}
}

func sameStringSet(a, b []string) bool {
	as := map[string]bool{}
	for _, s := range a {
		as[s] = true
	}
	bs := map[string]bool{}
	for _, s := range b {
		bs[s] = true
	}
	if len(as) != len(bs) {
		return false
	}
	for s := range as {
		if !bs[s] {
			return false
		}
	}
	return true
}

// TestBazelDoltRaceSoleRunIsInTheUnion: where bazel-doltserver executes
// remotely (the fork modes) it adds --config=sole-run; the union carries that
// config's lines itself, so the dolt-server tier's actions are the same in
// either arm. Every sole-run line is a dolt-race line.
func TestBazelDoltRaceSoleRunIsInTheUnion(t *testing.T) {
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	sole := bazelRCConfigLines(rc, "sole-run")
	if len(sole) == 0 {
		t.Fatal(".bazelrc has no --config=sole-run lines")
	}
	union := bazelRCConfigLines(rc, bazelDoltRaceConfig)
	for _, l := range sole {
		if !slices.Contains(union, l) {
			t.Errorf("--config=sole-run line %q is not in --config=%s: bazel-doltserver's remote runs and the union would differ", l, bazelDoltRaceConfig)
		}
	}
	// And the union job takes no sole-run of its own: nothing to add.
	test := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltRaceJobName).step(t, "bazel test //... --config="+bazelDoltRaceConfig)
	if strings.Contains(test.Run, "BAZEL_SOLE_RUN") || strings.Contains(test.Run, "sole-run") {
		t.Errorf("%s test step adds sole-run; --config=%s already has its lines", bazelDoltRaceJobName, bazelDoltRaceConfig)
	}
}

// TestBazelDoltRaceIsGated pins the cutover: the union job is a required
// lane like its members (no continue-on-error, the always() job.status
// recorder), it runs the union config with the members' checks once over its
// BEP, and both members' workflow_call outputs read its result first.
func TestBazelDoltRaceIsGated(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelDoltRaceJobName)

	if want := withDoltTiers(bazelDoltRaceJobIf); job.If != want {
		t.Errorf("%s if: %q, want %q", bazelDoltRaceJobName, job.If, want)
	}
	if job.ContinueOnError {
		t.Errorf("%s: continue-on-error hides the dolt-server and proxied tiers' failures from pr.yml's ci-gate", bazelDoltRaceJobName)
	}
	if job.Name != "dolt-race" {
		// ci-analytics' collector maps the check name "Bazel / dolt-race"
		// to lane dolt-race.
		t.Errorf("%s name = %q, want dolt-race", bazelDoltRaceJobName, job.Name)
	}
	if !slices.Equal(job.Needs, []string{bazelRBEJobName}) {
		t.Errorf("%s needs %v, want [%s]", bazelDoltRaceJobName, job.Needs, bazelRBEJobName)
	}
	if !reflect.DeepEqual(job.Outputs, map[string]string{"result": "${{ steps.result.outputs.result }}"}) {
		t.Errorf("%s outputs = %v, want only result from the result step", bazelDoltRaceJobName, job.Outputs)
	}

	// The test step: the union config, as the member lanes run theirs.
	test := job.step(t, "bazel test //... --config="+bazelDoltRaceConfig)
	if strings.TrimSpace(test.Run) != bazelTierTestRun(bazelDoltRaceConfig) {
		t.Errorf("%s test step run changed; want exactly:\n%s\ngot:\n%s", bazelDoltRaceJobName, bazelTierTestRun(bazelDoltRaceConfig), test.Run)
	}
	// The members' per-lane checks, once over the union's BEP: bazel-proxied's
	// two (bazel-doltserver has none). check_shard_coverage takes the union
	// of the members' --suite/--whole arguments, which is bazel-proxied's.
	for member, steps := range map[string][]string{
		bazelProxiedJobName: {"Every target and shard ran tests", "Every listed test ran in its shard", "No test target is marked flaky"},
	} {
		m := workflow.job(t, member)
		for _, name := range steps {
			want, got := m.step(t, name), job.step(t, name)
			if strings.TrimSpace(got.Run) != strings.TrimSpace(want.Run) || got.If != want.If {
				t.Errorf("%s step %q = (if %q) %q, want %s's (if %q) %q", bazelDoltRaceJobName, name, got.If, got.Run, member, want.If, want.Run)
			}
		}
	}
	for _, member := range bazelUnionLanes[bazelDoltRaceJobName] {
		for _, step := range workflow.job(t, member).Steps {
			if strings.Contains(step.Run, "shard_budget.py") || strings.Contains(step.Run, "check_shard_coverage.py") && member != bazelProxiedJobName {
				t.Errorf("%s step %q: a per-lane check the union does not run", member, step.Name)
			}
		}
	}

	// No job depends on it, and every member's output reads it first.
	for name, other := range workflow.Jobs {
		if slices.Contains(other.Needs, bazelDoltRaceJobName) {
			t.Errorf("job %s needs %s", name, bazelDoltRaceJobName)
		}
	}
	call := readBazelWorkflowCall(t)
	for _, member := range bazelUnionLanes[bazelDoltRaceJobName] {
		if got, want := call.Outputs[member].Value, bazelLaneCallOutput(member); got != want {
			t.Errorf("workflow_call output %s = %q, want %q", member, got, want)
		}
	}
	for name := range call.Outputs {
		if strings.Contains(name, bazelDoltRaceConfig) {
			t.Errorf("workflow_call output %s: the union reports through its members' outputs, not one of its own (the gate ids stay)", name)
		}
	}
}

// TestBazelDoltRaceJobTimeouts: the union's test step has a 23-minute
// timeout, every step has one, and the job's exceeds their sum by
// bazelDoltRaceJobTimeoutMargin minutes (a hang fails a named step) while
// staying under the merge queue's check timeout (a hung required job would
// otherwise drop the group).
func TestBazelDoltRaceJobTimeouts(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltRaceJobName)
	test := job.step(t, "bazel test //... --config="+bazelDoltRaceConfig)
	if test.TimeoutMinutes != bazelDoltRaceTestStepTimeout {
		t.Errorf("%s test step timeout-minutes = %d, want %d", bazelDoltRaceJobName, test.TimeoutMinutes, bazelDoltRaceTestStepTimeout)
	}
	sum := 0
	for i, step := range job.Steps {
		if step.TimeoutMinutes <= 0 {
			t.Errorf("%s step %d (%q) has no timeout-minutes; a hang would run into the job timeout",
				bazelDoltRaceJobName, i, cmp.Or(step.Name, step.Uses))
		}
		sum += step.TimeoutMinutes
	}
	if job.TimeoutMinutes < sum+bazelDoltRaceJobTimeoutMargin {
		t.Errorf("%s timeout-minutes %d, want at least the sum of its step timeouts (%d) + %d",
			bazelDoltRaceJobName, job.TimeoutMinutes, sum, bazelDoltRaceJobTimeoutMargin)
	}
	if job.TimeoutMinutes >= bazelDoltRaceMergeQueueTimeout {
		t.Errorf("%s timeout-minutes %d, want below the merge queue's %d-minute check timeout",
			bazelDoltRaceJobName, job.TimeoutMinutes, bazelDoltRaceMergeQueueTimeout)
	}
}

// TestBazelDoltRaceArmsExactlyOnePerMode evaluates the union's and the
// members' if: for every execution mode and every event bazel.yml runs on:
// the union runs exactly in mode remote, never beside a member, and each
// member's gate id is reported by exactly one job wherever that member ran
// before the union (bazel-doltserver: every mode but skip; bazel-proxied:
// the remote-executing modes) and by none elsewhere, so bazel-gate.sh's
// per-mode skips are unchanged. A caller's dolt-tiers "off" (pr.yml on a
// merge group) stops both arms in every mode. The workflow_call outputs,
// evaluated as GitHub does, carry the running arm's result: a failed or
// cancelled union reds both ids, and a union that never reported reads as
// skipped, which no mode it runs in allows.
func TestBazelDoltRaceArmsExactlyOnePerMode(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	union := workflow.job(t, bazelDoltRaceJobName)
	members := map[string]struct {
		ifExpr string
		before func(mode string) bool // where the member ran before the union
	}{
		bazelDoltJobName:    {bazelDoltMemberIf, func(m string) bool { return m != "skip" }},
		bazelProxiedJobName: {bazelProxiedMemberIf, func(m string) bool { return bazelRemoteModes[m] }},
	}
	if got := slices.Sorted(maps.Keys(members)); !slices.Equal(got, slices.Sorted(slices.Values(bazelUnionLanes[bazelDoltRaceJobName]))) {
		t.Fatalf("members %v, want bazelUnionLanes' %v", got, bazelUnionLanes[bazelDoltRaceJobName])
	}
	for name, m := range members {
		if got, want := workflow.job(t, name).If, withDoltTiers(m.ifExpr); got != want {
			t.Errorf("%s if = %q, want %q", name, got, want)
		}
	}
	for _, mode := range bazelRBEModes {
		for _, event := range []string{"pull_request", "merge_group", "pull_request_target", "push", "schedule", "workflow_dispatch"} {
			for _, dolt := range []string{"", "on", "off"} {
				ctx := map[string]string{
					"needs.rbe.outputs.mode":    mode,
					"needs.rbe.outputs.enabled": bazelModeEnabled(mode),
					"github.event_name":         event,
				}
				if dolt != "" {
					ctx["inputs.dolt-tiers"] = dolt // "": push and dispatch, no input
				}
				unionRuns := evalRRCIf(t, union.If, ctx)
				if want := mode == "remote" && dolt != "off"; unionRuns != want {
					t.Errorf("mode %s, %s, dolt-tiers %q: %s runs = %v, want %v", mode, event, dolt, bazelDoltRaceJobName, unionRuns, want)
				}
				for name, m := range members {
					runs := evalRRCIf(t, workflow.job(t, name).If, ctx)
					if unionRuns && runs {
						t.Errorf("mode %s, %s: %s and %s both run", mode, event, bazelDoltRaceJobName, name)
					}
					if got, want := unionRuns || runs, m.before(mode) && dolt != "off"; got != want {
						t.Errorf("mode %s, %s, dolt-tiers %q: %s's tier reported by a job = %v, want %v (where it ran before the union, unless the caller turned the Dolt tiers off)", mode, event, dolt, name, got, want)
					}
					for _, res := range []string{"success", "failure", "cancelled", ""} {
						jobs := map[string]string{}
						switch {
						case unionRuns:
							jobs[bazelDoltRaceJobName] = res
						case runs:
							jobs[name] = res
						}
						want := res
						if !unionRuns && !runs {
							want = ""
						}
						if got := bazelCallOutputValue(t, name, jobs); got != want {
							t.Errorf("mode %s, %s, reporting job %q: output %s = %q, want %q", mode, event, res, name, got, want)
						}
					}
				}
			}
		}
	}
	// The union's runner is the lanes' ternary: Blacksmith in mode remote,
	// the only mode it runs in.
	if union.RunsOn != bazelLaneRunsOn {
		t.Errorf("%s runs-on = %q, want %q", bazelDoltRaceJobName, union.RunsOn, bazelLaneRunsOn)
	}
}

// bazelDoltRaceTestActions: the union's test actions, 27 dolt-server + 37
// proxied-server, each a target's shard (62 when item 5 A1b aquery-verified
// them; 64 since bd_managed_local_test runs as 3 shards). That is exactly
// --jobs=64, so the union has no headroom: a 4th managed-local shard, or any
// new test action in either tier, would make 65 and needs a --jobs or
// shard-count change first.
const bazelDoltRaceTestActions = 64

// TestBazelDoltRaceFitsTheJobsBudget pins the one-wave rule (closed PR
// #7542): a union's total test actions must fit in the effective --jobs so
// every action starts in Bazel's first wave. Bazel does not schedule the
// longest action first, so an action that lands in a second wave because
// the first is full adds its own full duration to the critical path rather
// than overlapping it; #7542's paired A/B runs measured this costing up to
// +575 s of wall clock on a union that exceeded its --jobs by one tier's
// worth of actions. The union runs both tiers' shards in one invocation
// under the effective --jobs (bazelUnionEffectiveJobs), so all of them must
// fit at once or the slower tier's shards queue behind the other's and the
// union stops ending with the slower member. Every rule tagged with one of
// the union's tags, read from the BUILD files: its shard_count (1 when
// unsharded), summed. A tag outside a one-line `tags = [...]` list cannot
// be counted and fails the test.
func TestBazelDoltRaceFitsTheJobsBudget(t *testing.T) {
	root := bazelPolicyRoot(t)
	rc := readPolicyFile(t, root, ".bazelrc")
	budget := bazelUnionEffectiveJobs(t, rc, bazelDoltRaceConfig)

	var tags []string
	for _, mem := range bazelDoltRaceMembers {
		tags = append(tags, `"`+mem.tag+`"`)
	}
	ruleStart := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*\($`)
	total, targets := 0, 0
	for _, f := range repoFiles(t, root, 300) {
		if path.Base(f) != "BUILD.bazel" {
			continue
		}
		build := readPolicyFile(t, root, f)
		for _, line := range strings.Split(build, "\n") {
			for _, tag := range tags {
				if strings.Contains(line, tag) && !strings.HasPrefix(line, "    tags = [") {
					t.Errorf("%s: %q outside a one-line tags list: %q", f, tag, line)
				}
			}
		}
		for _, loc := range ruleStart.FindAllStringIndex(build, -1) {
			end := strings.Index(build[loc[0]:], "\n)\n")
			if end < 0 {
				continue
			}
			rule := build[loc[0] : loc[0]+end]
			tagged := false
			for _, line := range strings.Split(rule, "\n") {
				if strings.HasPrefix(line, "    tags = [") && slices.ContainsFunc(tags, func(tag string) bool { return strings.Contains(line, tag) }) {
					tagged = true
				}
			}
			if !tagged {
				continue
			}
			shards := 1
			if sm := shardCountPattern.FindStringSubmatch(rule); sm != nil {
				shards, _ = strconv.Atoi(sm[1])
			}
			total += shards
			targets++
		}
	}
	if total != bazelDoltRaceTestActions {
		t.Errorf("--config=%s: %d test actions over %d targets, want %d (aquery-verified at the cutover): re-check against --jobs=%d and update", bazelDoltRaceConfig, total, targets, bazelDoltRaceTestActions, budget)
	}
	if total > budget {
		t.Errorf("--config=%s: %d test actions, more than the effective --jobs=%d: a second wave would add its tail test's full duration to the critical path instead of overlapping it (closed PR #7542 measured up to +575s from exactly this)", bazelDoltRaceConfig, total, budget)
	}
}
