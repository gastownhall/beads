package scripts_test

import (
	"maps"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	checkSkipCensusTest       = "tools/bazel/check_skip_census_test.py"
	bazelSkipCensusArg        = "--skip-census tools/bazel/skip_census.txt"
	bazelSkipCensusReportArg  = "${SKIP_CENSUS_REPORT_ONLY:+--skip-census-report-only}"
	bazelSkipCensusReportEnv  = "SKIP_CENSUS_REPORT_ONLY"
	bazelSkipCensusReportExpr = "${{ needs.rbe.outputs.enabled != 'true' && 'true' || '' }}"
)

func TestCheckSkipCensusUnitTests(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	cmd := exec.Command(python, filepath.Join(root, checkSkipCensusTest), "-v")
	cmd.Dir = filepath.Join(root, "tools", "bazel")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", checkSkipCensusTest, err, out)
	}
}

func bazelJobRunsLocally(t *testing.T, job ciWorkflowJob) bool {
	t.Helper()
	for _, mode := range bazelRBEModes {
		if bazelRemoteModes[mode] {
			continue
		}
		for _, event := range []string{"pull_request", "merge_group", "pull_request_target", "push", "schedule", "workflow_dispatch"} {
			ctx := map[string]string{
				"needs.rbe.outputs.mode":    mode,
				"needs.rbe.outputs.enabled": bazelModeEnabled(mode),
				"github.event_name":         event,
			}
			if evalRRCIf(t, job.If, ctx) {
				return true
			}
		}
	}
	return false
}

func TestSkipCensusRunsInEveryDoltTierLane(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	for _, name := range slices.Sorted(maps.Keys(workflow.Jobs)) {
		job := workflow.Jobs[name]
		tier := slices.Contains(bazelDoltTierLanes, name)
		var checks []ciWorkflowStep
		for _, s := range job.Steps {
			if strings.Contains(s.Run, "--skip-census") && !tier {
				t.Errorf("%s step %q runs the skip census; only bazelDoltTierLanes do", name, s.Name)
			}
			if strings.Contains(s.Run, "check_testcases.py") {
				checks = append(checks, s)
			}
		}
		if !tier {
			continue
		}
		if len(checks) == 0 {
			t.Errorf("%s runs no check_testcases.py step, so nothing counts its skips", name)
			continue
		}
		local := bazelJobRunsLocally(t, job)
		for _, s := range checks {
			if !strings.Contains(s.Run, " "+bazelSkipCensusArg) {
				t.Errorf("%s step %q does not pass %s:\n%s", name, s.Name, bazelSkipCensusArg, s.Run)
			}
			if s.If != "${{ always() && steps.test.outcome != 'skipped' }}" {
				t.Errorf("%s step %q if = %q", name, s.Name, s.If)
			}
			// Report-only may only ever come from the env-gated expansion: a
			// literal flag would quietly turn a gating lane into a reporter.
			if lit, gated := strings.Count(s.Run, "--skip-census-report-only"), strings.Count(s.Run, bazelSkipCensusReportArg); lit != gated {
				t.Errorf("%s step %q passes --skip-census-report-only %d times but only %d through %s:\n%s", name, s.Name, lit, gated, bazelSkipCensusReportArg, s.Run)
			}
			if got := strings.Contains(s.Run, bazelSkipCensusReportArg); got != local {
				t.Errorf("%s step %q: report-only switch = %v, want %v (the job executes on the runner in some mode: %v)", name, s.Name, got, local, local)
			}
			expr, has := s.Env[bazelSkipCensusReportEnv]
			if has != local || (local && expr != bazelSkipCensusReportExpr) {
				t.Errorf("%s step %q env %s = %q (set %v), want %q only where the job can execute on the runner", name, s.Name, bazelSkipCensusReportEnv, expr, has, bazelSkipCensusReportExpr)
			}
			if local {
				for _, mode := range bazelRBEModes {
					v, err := evalGHExpr(expr, map[string]string{"needs.rbe.outputs.enabled": bazelModeEnabled(mode)})
					want := ""
					if !bazelRemoteModes[mode] {
						want = "true"
					}
					if err != nil || v != want {
						t.Errorf("%s: %s in mode %s = %#v (%v), want %q", name, bazelSkipCensusReportEnv, mode, v, err, want)
					}
				}
			}
		}
	}
}

func TestSkipCensusLaneTagsAreTheDoltTierTags(t *testing.T) {
	root := bazelPolicyRoot(t)
	rc := readPolicyFile(t, root, ".bazelrc")
	workflow := readCIWorkflow(t, bazelWorkflowName)
	configRe := regexp.MustCompile(`--config=([a-z-]+)`)
	want := map[string]bool{}
	for _, name := range bazelDoltTierLanes {
		for _, s := range workflow.job(t, name).Steps {
			if s.ID != "test" {
				continue
			}
			m := configRe.FindStringSubmatch(s.Run)
			if m == nil {
				t.Fatalf("%s test step names no --config", name)
			}
			for _, l := range bazelRCConfigLines(rc, m[1]) {
				if v, ok := strings.CutPrefix(l, "test --test_tag_filters="); ok {
					for _, tag := range strings.Split(v, ",") {
						want[tag] = true
					}
				}
			}
		}
	}
	src := readPolicyFile(t, sourceRepoRoot(t), "tools/bazel/check_skip_census.py")
	m := regexp.MustCompile(`(?m)^CENSUS_LANE_TAGS = frozenset\(\{([^}]*)\}\)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("tools/bazel/check_skip_census.py has no CENSUS_LANE_TAGS = frozenset({...})")
	}
	got := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		got[q[1]] = true
	}
	if !maps.Equal(got, want) {
		t.Errorf("CENSUS_LANE_TAGS = %v, want the Dolt-tier lanes' tag filters %v", got, want)
	}
}

// bazelCensusCheckRun: a Dolt-tier lane's "Every target and shard ran tests"
// step over its one BEP: the zero-testcase check plus the skip census,
// report-only where the job can execute on the runner.
func bazelCensusCheckRun(t *testing.T, job ciWorkflowJob) string {
	t.Helper()
	run := `python3 tools/bazel/check_testcases.py --bep "$RUNNER_TEMP/bazel-bep.json" ` + bazelSkipCensusArg
	if bazelJobRunsLocally(t, job) {
		run += " " + bazelSkipCensusReportArg
	}
	return run
}
