package scripts_test

import (
	"cmp"
	"maps"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The dolt-int union lane (item 5 A2): in mode remote, bazel.yml's
// bazel-dolt-int job runs --config=dolt-int, the union of
// --config=doltserver-integration and --config=doltserver-cmd, in one
// `bazel test`, in place of the bazel-server-storage and bazel-cmd-dolt jobs;
// in every other mode those two run as before. Unlike dolt-race, the members
// have different gating categories: bazel-server-storage is plain-gated
// (BAZEL_SERVER_STORAGE), bazel-cmd-dolt is flag-gated and advisory
// (BAZEL_CMD_DOLT, continue-on-error) until pr.yml's BAZEL_CMD_DOLT_REQUIRED
// is "true". The union itself carries no job-level continue-on-error (see
// TestBazelDoltIntIsGated): only the `bazel test` step does, and an always()
// classify step (tools/bazel/classify_tier_results.py) computes each tier's
// own result from the BEP and fails the job iff server-storage's tier
// failed, so a cmd-dolt-only failure still leaves the job green, exactly as
// bazel-cmd-dolt's own continue-on-error already does outside mode remote.
const (
	bazelDoltIntJobName = "bazel-dolt-int"
	bazelDoltIntConfig  = "dolt-int"
	// Mode remote and integration not "off": exactly where the members do
	// not run (see #7491's members, which skip on integration == "off" too).
	// Its actual if adds the caller's dolt-tiers switch (withDoltTiers).
	bazelDoltIntJobIf = "${{ needs.rbe.outputs.mode == 'remote' && inputs.integration != 'off' }}"
)

func init() {
	// bazelUnionLanes (declared in bazel_dolt_race_test.go): a union job -> the
	// gated member jobs it runs in place of in mode remote. Each member keeps
	// its gate id/flag and its workflow_call output, which reads the union's
	// result first and the member's own otherwise (bazelLaneCallOutput).
	bazelUnionLanes[bazelDoltIntJobName] = []string{bazelServerJobName, bazelCmdDoltJobName}
}

// bazelDoltIntJobTimeoutMargin: as bazelDoltRaceJobTimeoutMargin.
const bazelDoltIntJobTimeoutMargin = 5

// bazelDoltIntTestStepTimeout: the union's `bazel test` step timeout. Both
// members share the same 1200 s Bazel test_timeout cap; the step allows about
// 3 minutes past that cap so Bazel writes TIMEOUT logs before GitHub kills the
// step, plus the margin server-storage's own step budgeted for a cold
// compile (its own step was 25).
const bazelDoltIntTestStepTimeout = 25

// bazelDoltIntClassifyRun: the classify step's run body, exactly (must-fix 3,
// PR #7535 review): pinned by equality, not substring, so an appended
// --gate-tier or a second --tier/--check cannot sneak past a Contains check
// (argparse and dict() would otherwise just let the last value silently
// win, un-gating or re-gating a tier).
const bazelDoltIntClassifyRun = `python3 tools/bazel/classify_tier_results.py --bep "$RUNNER_TEMP/bazel-bep.json" --rc "$TEST_RC" --tier server-storage=//internal/storage/dolt:dolt_server_full_test,//internal/storage/dolt:dolt_server_conformance_test --tier cmd-dolt=//cmd/bd:bd_dolt_server_test,//tests/regression:regression_test --gate-tier server-storage --check "server-storage=$CHECK_SS_OUTCOME" --check "cmd-dolt=$CHECK_CD_OUTCOME" --github-output "$GITHUB_OUTPUT"`

// bazelDoltIntTestRun: the union's `bazel test` step body, exactly. Unlike
// the retired tiers' (bazelTierTestRun), it captures rc as a step output (no
// job-level continue-on-error to rely on job.status instead) for the
// classify step to read, alongside its own exit.
const bazelDoltIntTestRun = `set -o pipefail
start=$(date +%s)
rc=0
bazel test //... --config=dolt-int ${BAZEL_FRESH:+"$BAZEL_FRESH"} \
  --build_event_json_file="$RUNNER_TEMP/bazel-bep.json" \
  --execution_log_compact_file="$RUNNER_TEMP/bazel-exec.log.zst" \
  --profile="$RUNNER_TEMP/bazel-profile.json" \
  --experimental_build_event_upload_strategy=local \
  2>&1 | tee "$RUNNER_TEMP/bazel-test.log" || rc=$?
echo "bazel test --config=dolt-int: exit $rc, $(( $(date +%s) - start ))s wall" | tee -a "$GITHUB_STEP_SUMMARY"
echo "rc=$rc" >> "$GITHUB_OUTPUT"
exit "$rc"`

// The union's members, and the tag filter each owns.
var bazelDoltIntMembers = []struct{ config, tag string }{
	{"doltserver-integration", "dolt-server-integration"},
	{"doltserver-cmd", "dolt-server-cmd"},
}

// TestBazelDoltIntConfigIsMemberUnion: --config=dolt-int is exactly the set
// union of its members' lines, except that each member's --test_tag_filters
// is replaced by one filter naming both members' tags. Every flag stays a
// member's own, so each member target's test action is unchanged in the
// sense that matters for caching (item 5 A2, aquery-verified: byte-identical
// remote action digests, the key RBE's cache lookups use; each action's
// local actionKey differs, since dolt-int reaches these flags through the
// target's own env/args rather than the member's build configuration). Both
// arms run, the mode deciding which, so drift between them would split
// results by mode: a flag added to a member must be added to the union too.
func TestBazelDoltIntConfigIsMemberUnion(t *testing.T) {
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	const tagPrefix = "test --test_tag_filters="

	var want []string
	var tags []string
	for _, m := range bazelDoltIntMembers {
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

	got := bazelRCConfigLines(rc, bazelDoltIntConfig)
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
		t.Errorf("--config=%s tag filters %v, want exactly [%s]", bazelDoltIntConfig, gotTags, wantTag)
	}
	if !sameStringSet(gotRest, want) {
		t.Errorf("--config=%s is not the union of its members' lines (tag filter aside):\ngot:\n%s\nwant (any order):\n%s",
			bazelDoltIntConfig, strings.Join(gotRest, "\n"), strings.Join(want, "\n"))
	}
	if len(gotRest) != len(want) {
		t.Errorf("--config=%s repeats a line: %d lines for %d distinct member lines", bazelDoltIntConfig, len(gotRest), len(want))
	}
	// No nested --config: the union is spelled out, so the policy above sees
	// every flag.
	for _, l := range got {
		if strings.Contains(l, "--config=") {
			t.Errorf("--config=%s line %q: spell the members' flags out, no nested --config", bazelDoltIntConfig, l)
		}
	}
}

// TestBazelDoltIntSoleRunIsInTheUnion: the members never add --config=sole-run
// or read $BAZEL_SOLE_RUN (unlike bazel-test/bazel-pure/bazel-doltserver);
// each bakes --config=sole-run's one line (no retry on a remote cache
// eviction) directly and unconditionally into its own .bazelrc config
// instead. The union must carry that same line itself (not nested
// --config=sole-run: TestBazelDoltIntConfigIsMemberUnion forbids nesting),
// so both tiers' actions are the same whether the member or the union runs
// them.
func TestBazelDoltIntSoleRunIsInTheUnion(t *testing.T) {
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	sole := bazelRCConfigLines(rc, "sole-run")
	if len(sole) == 0 {
		t.Fatal(".bazelrc has no --config=sole-run lines")
	}
	union := bazelRCConfigLines(rc, bazelDoltIntConfig)
	for _, l := range sole {
		if !slices.Contains(union, l) {
			t.Errorf("--config=sole-run line %q is not in --config=%s: the members' remote runs and the union would differ", l, bazelDoltIntConfig)
		}
	}
	// And the union job takes no sole-run of its own: nothing to add.
	test := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltIntJobName).step(t, "bazel test //... --config="+bazelDoltIntConfig)
	if strings.Contains(test.Run, "BAZEL_SOLE_RUN") || strings.Contains(test.Run, "sole-run") {
		t.Errorf("%s test step adds sole-run; --config=%s already has its lines", bazelDoltIntJobName, bazelDoltIntConfig)
	}
}

// TestBazelDoltIntIsGated pins the cutover: the union job carries no
// job-level continue-on-error (per-tier results come from an always()
// classify step instead: see the package comment above), it runs the union
// config with the members' checks once over its BEP (the new Shard balance
// step is bespoke, --label-scoped to server-storage's own target), and both
// members' workflow_call outputs read its per-tier result first.
func TestBazelDoltIntIsGated(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelDoltIntJobName)

	if want := withDoltTiers(bazelDoltIntJobIf); job.If != want {
		t.Errorf("%s if: %q, want %q", bazelDoltIntJobName, job.If, want)
	}
	// Unlike dolt-race (both members plain-gated): bazel-cmd-dolt is
	// flag-gated/advisory with its own continue-on-error everywhere else,
	// but a job-level continue-on-error here would make job.status read
	// success regardless of the `bazel test` step's own result, so a
	// server-storage failure would end green on push/nightly/dispatch
	// (nothing there reads BAZEL_SERVER_STORAGE off a workflow_call output
	// to catch it), where it must stay red exactly as on main today. The
	// job must therefore carry no continue-on-error of its own; the classify
	// step's own exit code decides job.status per-tier instead.
	if job.ContinueOnError {
		t.Errorf("%s: want no job-level continue-on-error (per-tier results come from the classify step's exit code instead)", bazelDoltIntJobName)
	}
	if job.Name != "dolt-int" {
		// ci-analytics' collector maps the check name "Bazel / dolt-int" to
		// lane dolt-int.
		t.Errorf("%s name = %q, want dolt-int", bazelDoltIntJobName, job.Name)
	}
	if !slices.Equal(job.Needs, []string{bazelRBEJobName}) {
		t.Errorf("%s needs %v, want [%s]", bazelDoltIntJobName, job.Needs, bazelRBEJobName)
	}
	wantOutputs := map[string]string{
		"server_storage_result": "${{ steps.classify.outputs.server_storage_result }}",
		"cmd_dolt_result":       "${{ steps.classify.outputs.cmd_dolt_result }}",
	}
	if !reflect.DeepEqual(job.Outputs, wantOutputs) {
		t.Errorf("%s outputs = %v, want %v (per-tier, from the classify step)", bazelDoltIntJobName, job.Outputs, wantOutputs)
	}

	// The test step: the union config, as the member lanes run theirs, plus
	// its own continue-on-error and rc capture (must-fix 2: per-tier results,
	// not job-level continue-on-error; see the job's own comment).
	test := job.step(t, "bazel test //... --config="+bazelDoltIntConfig)
	if want := bazelDoltIntTestRun; strings.TrimSpace(test.Run) != want {
		t.Errorf("%s test step run changed; want exactly:\n%s\ngot:\n%s", bazelDoltIntJobName, want, test.Run)
	}
	if v, ok := test.ContinueOnError.(bool); !ok || !v {
		t.Errorf("%s test step: want continue-on-error: true (the classify step below decides the job's own result per-tier), got %v", bazelDoltIntJobName, test.ContinueOnError)
	}
	if test.ID != "test" {
		t.Errorf("%s test step id = %q, want %q (the classify step reads steps.test.outcome/outputs.rc)", bazelDoltIntJobName, test.ID, "test")
	}
	classify := job.step(t, "Classify tier results")
	if classify.ContinueOnError != nil && classify.ContinueOnError != false {
		t.Errorf("%s classify step: want no continue-on-error (its own exit code must decide job.status), got %v", bazelDoltIntJobName, classify.ContinueOnError)
	}
	if classify.ID != "classify" {
		t.Errorf("%s classify step id = %q, want %q (the job's outputs read steps.classify.outputs.*)", bazelDoltIntJobName, classify.ID, "classify")
	}
	if classify.If != "${{ always() && steps.test.outcome != 'skipped' }}" {
		t.Errorf("%s classify step if = %q, want always() guarded by steps.test.outcome (a dead job before this step must leave both outputs empty)", bazelDoltIntJobName, classify.If)
	}
	// steps.test.outputs.rc and each per-tier check step's own outcome reach
	// the script through env, not interpolated directly into the run script
	// (TestBazelWorkflowForkFarmInputs forbids "${{" in any run: body; see
	// "Record job result"'s JOB_STATUS for the existing pattern this
	// follows).
	for env, want := range map[string]string{
		"TEST_RC":          "${{ steps.test.outputs.rc }}",
		"CHECK_SS_OUTCOME": "${{ steps.check-ss.outcome }}",
		"CHECK_CD_OUTCOME": "${{ steps.check-cd.outcome }}",
	} {
		if got := classify.Env[env]; got != want {
			t.Errorf("%s classify step env.%s = %q, want %q", bazelDoltIntJobName, env, got, want)
		}
	}
	// Pinned by exact equality, not substring (must-fix 3): see
	// bazelDoltIntClassifyRun's own comment.
	if got, want := strings.TrimSpace(classify.Run), bazelDoltIntClassifyRun; got != want {
		t.Errorf("%s classify step run changed; want exactly:\n%s\ngot:\n%s", bazelDoltIntJobName, want, got)
	}
	if strings.Contains(classify.Run, "${{") {
		t.Errorf("%s classify step run interpolates an expression directly: %q", bazelDoltIntJobName, classify.Run)
	}
	// server-storage's own "No test target is marked flaky" and "Every
	// listed test ran in its shard" steps (cmd-dolt has neither): textually
	// identical, once over the union's BEP. "Every target and shard ran
	// tests" is bespoke per tier below (should-fix 2: split, continue-on-
	// error, --label-scoped, and the result folded into that tier alone).
	for member, steps := range map[string][]string{
		bazelServerJobName: {"No test target is marked flaky", "Every listed test ran in its shard"},
	} {
		m := workflow.job(t, member)
		for _, name := range steps {
			want, got := m.step(t, name), job.step(t, name)
			if strings.TrimSpace(got.Run) != strings.TrimSpace(want.Run) || got.If != want.If {
				t.Errorf("%s step %q = (if %q) %q, want %s's (if %q) %q", bazelDoltIntJobName, name, got.If, got.Run, member, want.If, want.Run)
			}
		}
	}
	// The split check_testcases steps (should-fix 2, PR #7535 review): each
	// --label-scoped to its own tier, continue-on-error so a zero-testcase
	// shard in either tier cannot fail the job by itself -- the classify
	// step's --check folds each step's own outcome into that tier's result
	// instead (bazelDoltIntClassifyRun pins the --check args).
	for id, want := range map[string]struct {
		name   string
		labels []string
	}{
		"check-ss": {"Every target and shard ran tests (server-storage)", []string{
			"//internal/storage/dolt:dolt_server_full_test",
			"//internal/storage/dolt:dolt_server_conformance_test",
		}},
		"check-cd": {"Every target and shard ran tests (cmd-dolt)", []string{
			"//cmd/bd:bd_dolt_server_test",
			"//tests/regression:regression_test",
		}},
	} {
		step := job.step(t, want.name)
		if step.ID != id {
			t.Errorf("%s step %q id = %q, want %q (the classify step reads steps.%s.outcome)", bazelDoltIntJobName, want.name, step.ID, id, id)
		}
		if step.If != "${{ always() && steps.test.outcome != 'skipped' }}" {
			t.Errorf("%s step %q if = %q, want always() guarded by steps.test.outcome", bazelDoltIntJobName, want.name, step.If)
		}
		if v, ok := step.ContinueOnError.(bool); !ok || !v {
			t.Errorf("%s step %q: want continue-on-error: true (its outcome feeds the classify step's --check, not job.status directly), got %v", bazelDoltIntJobName, want.name, step.ContinueOnError)
		}
		if !strings.Contains(step.Run, "check_testcases.py") {
			t.Errorf("%s step %q run = %q, want it to run check_testcases.py", bazelDoltIntJobName, want.name, step.Run)
		}
		for _, label := range want.labels {
			if !strings.Contains(step.Run, "--label "+label) {
				t.Errorf("%s step %q run missing --label %s:\n%s", bazelDoltIntJobName, want.name, label, step.Run)
			}
		}
	}
	// Shard balance is bespoke: server-storage's own step has no --label (it
	// has only ever budgeted its own target), the union's scopes to that same
	// target with --label so cmd-dolt's untested shards do not skew it (see
	// the job's own comment and shard_budget.py's docstring).
	storageShardBalance := workflow.job(t, bazelServerJobName).step(t, "Shard balance")
	unionShardBalance := job.step(t, "Shard balance")
	if strings.TrimSpace(unionShardBalance.Run) == strings.TrimSpace(storageShardBalance.Run) {
		t.Errorf("%s Shard balance step is textually identical to %s's; want it scoped with --label", bazelDoltIntJobName, bazelServerJobName)
	}
	if !strings.Contains(unionShardBalance.Run, "--label //internal/storage/dolt:dolt_server_full_test") {
		t.Errorf("%s Shard balance step = %q, want --label //internal/storage/dolt:dolt_server_full_test", bazelDoltIntJobName, unionShardBalance.Run)
	}

	// Upload test logs (must-fix 4, PR #7535 review): the `bazel test` step
	// carries its own continue-on-error, so failure() alone stays false on an
	// ordinary test failure until the classify step (which runs after this
	// one) exits non-zero -- by which point this step has already been
	// evaluated. steps.test.outcome == 'failure' catches that case directly;
	// no other test in this suite pins this job's own "Upload test logs" if
	// (TestBazelWorkflowSecretsAndFailureSurface only inventories
	// continue-on-error paths, and bazel_dolt_race_test.go/ci_workflow_test.go
	// pin only the other lanes' upload-logs steps), so a future accidental
	// revert of this condition to plain failure() would otherwise go
	// unnoticed (mutation G, PR #7535 review round 3).
	logs := job.step(t, "Upload test logs")
	const wantLogsIf = "${{ (failure() || steps.test.outcome == 'failure') && steps.test.outcome != 'skipped' }}"
	if logs.If != wantLogsIf {
		t.Errorf("%s Upload test logs if = %q, want %q", bazelDoltIntJobName, logs.If, wantLogsIf)
	}
	if logs.With["name"] != "bazel-dolt-int-testlogs" {
		t.Errorf("%s Upload test logs name = %q, want bazel-dolt-int-testlogs", bazelDoltIntJobName, logs.With["name"])
	}

	for _, member := range bazelUnionLanes[bazelDoltIntJobName] {
		for _, step := range workflow.job(t, member).Steps {
			if strings.Contains(step.Run, "shard_budget.py") && member != bazelServerJobName {
				t.Errorf("%s step %q: a per-lane check the union does not run", member, step.Name)
			}
			if strings.Contains(step.Run, "check_shard_coverage.py") && member != bazelServerJobName {
				t.Errorf("%s step %q: a per-lane check the union does not run", member, step.Name)
			}
		}
	}

	// No job depends on it, and every member's output reads it first.
	for name, other := range workflow.Jobs {
		if slices.Contains(other.Needs, bazelDoltIntJobName) {
			t.Errorf("job %s needs %s", name, bazelDoltIntJobName)
		}
	}
	call := readBazelWorkflowCall(t)
	for _, member := range bazelUnionLanes[bazelDoltIntJobName] {
		if got, want := call.Outputs[member].Value, bazelLaneCallOutput(member); got != want {
			t.Errorf("workflow_call output %s = %q, want %q", member, got, want)
		}
	}
	for name := range call.Outputs {
		if strings.Contains(name, bazelDoltIntConfig) {
			t.Errorf("workflow_call output %s: the union reports through its members' outputs, not one of its own (the gate ids stay)", name)
		}
	}
}

// TestBazelDoltIntJobTimeouts: the union's test step has a 25-minute timeout,
// every step has one, and the job's exceeds their sum by
// bazelDoltIntJobTimeoutMargin minutes (a hang fails a named step) while
// staying under the merge queue's check timeout.
func TestBazelDoltIntJobTimeouts(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltIntJobName)
	test := job.step(t, "bazel test //... --config="+bazelDoltIntConfig)
	if test.TimeoutMinutes != bazelDoltIntTestStepTimeout {
		t.Errorf("%s test step timeout-minutes = %d, want %d", bazelDoltIntJobName, test.TimeoutMinutes, bazelDoltIntTestStepTimeout)
	}
	sum := 0
	for i, step := range job.Steps {
		if step.TimeoutMinutes <= 0 {
			t.Errorf("%s step %d (%q) has no timeout-minutes; a hang would run into the job timeout",
				bazelDoltIntJobName, i, cmp.Or(step.Name, step.Uses))
		}
		sum += step.TimeoutMinutes
	}
	if job.TimeoutMinutes < sum+bazelDoltIntJobTimeoutMargin {
		t.Errorf("%s timeout-minutes %d, want at least the sum of its step timeouts (%d) + %d",
			bazelDoltIntJobName, job.TimeoutMinutes, sum, bazelDoltIntJobTimeoutMargin)
	}
	if job.TimeoutMinutes >= bazelDoltRaceMergeQueueTimeout {
		t.Errorf("%s timeout-minutes %d, want below the merge queue's %d-minute check timeout",
			bazelDoltIntJobName, job.TimeoutMinutes, bazelDoltRaceMergeQueueTimeout)
	}
}

// TestBazelDoltIntArmsExactlyOnePerMode evaluates the union's and the
// members' if: for every execution mode and every event bazel.yml runs on:
// the union runs exactly in mode remote, never beside a member, and each
// member's gate id/flag is reported by exactly one job wherever that member
// ran before the union (bazel-server-storage: the remote-executing modes
// plus cache, when enabled and integration is not off; bazel-cmd-dolt: where
// bazel-integration runs) and by none elsewhere, so bazel-gate.sh's per-mode
// skips are unchanged. A caller's dolt-tiers "off" (pr.yml on a merge group)
// stops both arms in every mode. The workflow_call outputs, evaluated as
// GitHub does, carry the running arm's result: a failed or cancelled union
// reds both ids/flags, and a union that never reported reads as skipped,
// which no mode it runs in allows.
func TestBazelDoltIntArmsExactlyOnePerMode(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	union := workflow.job(t, bazelDoltIntJobName)
	members := map[string]struct {
		ifExpr string
		before func(mode string) bool // where the member ran before the union
	}{
		bazelServerJobName:  {bazelServerStorageIf, func(m string) bool { return bazelRemoteModes[m] }},
		bazelCmdDoltJobName: {bazelCmdDoltMemberIf, func(m string) bool { return bazelRemoteModes[m] || m == "cache" }},
	}
	if got := slices.Sorted(maps.Keys(members)); !slices.Equal(got, slices.Sorted(slices.Values(bazelUnionLanes[bazelDoltIntJobName]))) {
		t.Fatalf("members %v, want bazelUnionLanes' %v", got, bazelUnionLanes[bazelDoltIntJobName])
	}
	for name, m := range members {
		if got, want := workflow.job(t, name).If, withDoltTiers(m.ifExpr); got != want {
			t.Errorf("%s if = %q, want %q", name, got, want)
		}
	}
	for _, mode := range bazelRBEModes {
		for _, event := range []string{"pull_request", "merge_group", "pull_request_target", "push", "schedule", "workflow_dispatch"} {
			for _, integration := range []string{"", "off"} {
				for _, dolt := range []string{"", "off"} {
					ctx := map[string]string{
						"needs.rbe.outputs.mode":    mode,
						"needs.rbe.outputs.enabled": bazelModeEnabled(mode),
						"github.event_name":         event,
						"inputs.integration":        integration,
						"inputs.dolt-tiers":         dolt,
					}
					wantUnionRuns := mode == "remote" && integration != "off" && dolt != "off"
					unionRuns := evalRRCIf(t, union.If, ctx)
					if unionRuns != wantUnionRuns {
						t.Errorf("mode %s, %s, integration %q, dolt-tiers %q: %s runs = %v, want %v", mode, event, integration, dolt, bazelDoltIntJobName, unionRuns, wantUnionRuns)
					}
					for name, m := range members {
						runs := evalRRCIf(t, workflow.job(t, name).If, ctx)
						if unionRuns && runs {
							t.Errorf("mode %s, %s, integration %q: %s and %s both run", mode, event, integration, bazelDoltIntJobName, name)
						}
						wantBefore := integration != "off" && dolt != "off" && m.before(mode)
						if got, want := unionRuns || runs, wantBefore; got != want {
							t.Errorf("mode %s, %s, integration %q, dolt-tiers %q: %s's tier reported by a job = %v, want %v (where it ran before the union, unless the caller turned the Dolt tiers off)", mode, event, integration, dolt, name, got, want)
						}
						for _, res := range []string{"success", "failure", "cancelled", ""} {
							jobs := map[string]string{}
							switch {
							case unionRuns:
								jobs[bazelDoltIntJobName] = res
							case runs:
								jobs[name] = res
							}
							want := res
							if !unionRuns && !runs {
								want = ""
							}
							if got := bazelCallOutputValue(t, name, jobs); got != want {
								t.Errorf("mode %s, %s, integration %q, reporting job %q: output %s = %q, want %q", mode, event, integration, res, name, got, want)
							}
						}
					}
				}
			}
		}
	}
	// The union's runner is the lanes' ternary: Blacksmith in mode remote,
	// the only mode it runs in.
	if union.RunsOn != bazelLaneRunsOn {
		t.Errorf("%s runs-on = %q, want %q", bazelDoltIntJobName, union.RunsOn, bazelLaneRunsOn)
	}
}

// classifyTierResultsTest: tools/bazel/classify_tier_results_test.py's own
// path, run directly (its docstring), not through a Bazel target: locally
// authored, so no SHA256 content pin is needed (unlike the vendored-from-
// gascity Python test files this repo also wraps, e.g. bazel_rrc_test.go's
// rrc_verify_test.py).
const classifyTierResultsTest = "tools/bazel/classify_tier_results_test.py"
const checkTestcasesTest = "tools/bazel/check_testcases_test.py"

// TestClassifyTierResultsUnitTests runs classify_tier_results.py's own
// behavioural test (must-fix 2(f)): the script the "Classify tier results"
// step above calls, covering ss-fail-only, cmd-fail-only, both, build
// failure and rc=0 over a synthetic BEP/test summary.
func TestClassifyTierResultsUnitTests(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	cmd := exec.Command(python, filepath.Join(root, classifyTierResultsTest), "-v")
	cmd.Dir = filepath.Join(root, "tools", "bazel")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", classifyTierResultsTest, err, out)
	}
}

// TestCheckTestcasesUnitTests runs check_testcases.py's own behavioural test
// (should-fix 2 of PR #7535's review): the script the union's two per-tier
// "Every target and shard ran tests" steps (check-ss, check-cd) each call
// with their own --label set, covering that --label both restricts the
// check to its own tier's targets and still fails closed on a label the BEP
// has no result for, or a zero-testcase shard within its own tier.
func TestCheckTestcasesUnitTests(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	cmd := exec.Command(python, filepath.Join(root, checkTestcasesTest), "-v")
	cmd.Dir = filepath.Join(root, "tools", "bazel")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", checkTestcasesTest, err, out)
	}
}

// bazelDoltIntTestActions: the union's test actions, 17 server-storage + 46
// cmd-dolt (aquery, item 5 A2).
const bazelDoltIntTestActions = 63

// TestBazelDoltIntFitsTheJobsBudget pins the one-wave rule (closed PR #7542,
// carried from dolt-race: see TestBazelDoltRaceFitsTheJobsBudget's own
// comment for the mechanism and the measured +575s regression): a union's
// total test actions must fit in the effective --jobs
// (bazelUnionEffectiveJobs) so every action starts in Bazel's first wave,
// since Bazel does not schedule the longest action first and a second-wave
// tail test adds its own full duration to the critical path instead of
// overlapping it. The union runs both tiers' shards in one invocation under
// that budget, so all of them must fit at once or the slower tier's shards
// queue behind the other's. Every rule tagged with one of the union's tags,
// read from the BUILD files: its shard_count (1 when unsharded), summed.
func TestBazelDoltIntFitsTheJobsBudget(t *testing.T) {
	root := bazelPolicyRoot(t)
	rc := readPolicyFile(t, root, ".bazelrc")
	budget := bazelUnionEffectiveJobs(t, rc, bazelDoltIntConfig)

	var tags []string
	for _, mem := range bazelDoltIntMembers {
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
	if total != bazelDoltIntTestActions {
		t.Errorf("--config=%s: %d test actions over %d targets, want %d (aquery-verified at the cutover): re-check against --jobs=%d and update", bazelDoltIntConfig, total, targets, bazelDoltIntTestActions, budget)
	}
	if total > budget {
		t.Errorf("--config=%s: %d test actions, more than the effective --jobs=%d: a second wave would add its tail test's full duration to the critical path instead of overlapping it (closed PR #7542 measured up to +575s from exactly this)", bazelDoltIntConfig, total, budget)
	}
}

// bazelDoltIntTierNames maps each member's tag to the --tier/--check name
// bazel.yml's classify step uses for it.
var bazelDoltIntTierNames = map[string]string{
	"dolt-server-integration": "server-storage",
	"dolt-server-cmd":         "cmd-dolt",
}

// bazelDoltIntTaggedLabels returns, for each of the union's tags, the exact
// sorted set of labels the BUILD files tag with it: the same rule-block walk
// TestBazelDoltIntFitsTheJobsBudget uses to sum shard counts, but keeping
// each rule's own label (//pkg:name) instead.
func bazelDoltIntTaggedLabels(t *testing.T) map[string][]string {
	t.Helper()
	root := bazelPolicyRoot(t)
	ruleStart := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*\($`)
	nameLine := regexp.MustCompile(`(?m)^    name = "([^"]+)",$`)
	out := map[string][]string{}
	for _, f := range repoFiles(t, root, 300) {
		if path.Base(f) != "BUILD.bazel" {
			continue
		}
		build := readPolicyFile(t, root, f)
		pkg := path.Dir(f)
		if pkg == "." {
			pkg = ""
		}
		for _, loc := range ruleStart.FindAllStringIndex(build, -1) {
			end := strings.Index(build[loc[0]:], "\n)\n")
			if end < 0 {
				continue
			}
			rule := build[loc[0] : loc[0]+end]
			nm := nameLine.FindStringSubmatch(rule)
			if nm == nil {
				continue
			}
			for _, mem := range bazelDoltIntMembers {
				tagLine := `"` + mem.tag + `"`
				for _, line := range strings.Split(rule, "\n") {
					if strings.HasPrefix(line, "    tags = [") && strings.Contains(line, tagLine) {
						out[mem.tag] = append(out[mem.tag], "//"+pkg+":"+nm[1])
						break
					}
				}
			}
		}
	}
	for tag := range out {
		slices.Sort(out[tag])
	}
	return out
}

// TestBazelDoltIntTierLabelsMatchTheTags ties the classify step's --tier
// label lists, and the per-tier check_testcases.py steps' --label lists, to
// the BUILD files' own tag sets -- the same sets TestBazelDoltIntFitsTheJobsBudget
// walks (must-fix 1, PR #7535 review): classify_tier_results.py fails every
// tier closed on a non-PASSED result for a label outside every --tier, but
// only if these lists actually name every tagged target. A target tagged
// dolt-server-integration or dolt-server-cmd without a matching update here
// (and to bazelDoltIntTestActions/bazelDoltIntClassifyRun, which this test
// also indirectly guards) would otherwise run untracked.
func TestBazelDoltIntTierLabelsMatchTheTags(t *testing.T) {
	tagged := bazelDoltIntTaggedLabels(t)
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelDoltIntJobName)

	classify := job.step(t, "Classify tier results")
	tierArg := regexp.MustCompile(`--tier ([a-z-]+)=(\S+)`)
	gotTiers := map[string][]string{}
	for _, m := range tierArg.FindAllStringSubmatch(classify.Run, -1) {
		gotTiers[m[1]] = strings.Split(m[2], ",")
	}

	checkSteps := map[string]string{
		"dolt-server-integration": "Every target and shard ran tests (server-storage)",
		"dolt-server-cmd":         "Every target and shard ran tests (cmd-dolt)",
	}

	for _, mem := range bazelDoltIntMembers {
		want := tagged[mem.tag]
		if len(want) == 0 {
			t.Fatalf("no BUILD rule is tagged %q", mem.tag)
		}
		tierName := bazelDoltIntTierNames[mem.tag]

		got := slices.Clone(gotTiers[tierName])
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("classify step --tier %s=... labels %v, want the %q-tagged labels from the BUILD files %v", tierName, got, mem.tag, want)
		}

		step := job.step(t, checkSteps[mem.tag])
		for _, label := range want {
			if !strings.Contains(step.Run, "--label "+label) {
				t.Errorf("%s step %q missing --label %s (one of %q's tagged labels)", bazelDoltIntJobName, checkSteps[mem.tag], label, mem.tag)
			}
		}
		gotLabelCount := strings.Count(step.Run, "--label ")
		if gotLabelCount != len(want) {
			t.Errorf("%s step %q has %d --label args, want exactly %d (%v)", bazelDoltIntJobName, checkSteps[mem.tag], gotLabelCount, len(want), want)
		}
	}
}

// bazelDoltIntCmdDoltPinnedArgsAndEnv: the two dolt-server-cmd-tagged
// targets that carry the moved -test.timeout=19m arg and
// BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION env var (item 5 A2: moved off
// --config=doltserver-cmd's --test_arg/--test_env onto the targets
// themselves, so --config=dolt-int does not also apply them to
// server-storage's targets). Nothing else pins these two lines once they
// live on the target instead of in .bazelrc, so a drive-by edit that drops
// or alters either one would silently fall back to Bazel's own default test
// timeout, or leave BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION unset, with every
// other policy test (TestBazelDoltIntConfigIsMemberUnion,
// TestBazelDoltIntFitsTheJobsBudget, TestBazelDoltIntTierLabelsMatchTheTags)
// none the wiser, since none of them inspect a tagged rule's own args/env
// bodies.
var bazelDoltIntCmdDoltPinnedArgsAndEnv = []struct {
	file, rule string
}{
	{"tests/regression/BUILD.bazel", "regression_test"},
	{"cmd/bd/BUILD.bazel", "bd_dolt_server_test"},
}

func TestBazelDoltIntCmdDoltTimeoutAndEnvPinned(t *testing.T) {
	root := bazelPolicyRoot(t)
	ruleStart := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*\($`)
	nameLine := regexp.MustCompile(`(?m)^    name = "([^"]+)",$`)
	const wantArg = `        "-test.timeout=19m",`
	const wantEnv = `        "BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION": "1",`

	for _, tc := range bazelDoltIntCmdDoltPinnedArgsAndEnv {
		build := readPolicyFile(t, root, tc.file)
		var rule string
		for _, loc := range ruleStart.FindAllStringIndex(build, -1) {
			end := strings.Index(build[loc[0]:], "\n)\n")
			if end < 0 {
				continue
			}
			candidate := build[loc[0] : loc[0]+end]
			if nm := nameLine.FindStringSubmatch(candidate); nm != nil && nm[1] == tc.rule {
				rule = candidate
				break
			}
		}
		if rule == "" {
			t.Fatalf("%s: no rule named %q found", tc.file, tc.rule)
		}
		if !strings.Contains(rule, wantArg) {
			t.Errorf("%s %q: missing exact args line %q (the moved --config=doltserver-cmd -test.timeout=19m, PR #7535 item 5 A2)", tc.file, tc.rule, wantArg)
		}
		if !strings.Contains(rule, wantEnv) {
			t.Errorf("%s %q: missing exact env line %q (the moved --config=doltserver-cmd BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION, PR #7535 item 5 A2)", tc.file, tc.rule, wantEnv)
		}
	}
}
