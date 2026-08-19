package scripts_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// aptStepBudget describes one bounded `apt-get` step: how long its own inner
// `timeout` calls may run, and the step-level `timeout-minutes` that backstops
// them.
//
// The components are declared here rather than only pinning the final minute
// count so that the two halves are checked against each other. A cap that
// lands below the step's own worst case reproduces exactly the undiagnosable
// mid-flight kill this hardening exists to remove, just at a smaller number --
// and that is invisible if the test only asserts the cap's literal value.
type aptStepBudget struct {
	workflow string
	job      string
	step     string

	updateAttempts int // iterations of the `for attempt in ...` loop
	updateTimeout  int // seconds allowed per `apt-get update` attempt
	backoff        int // seconds slept between attempts (never after the last)
	installTimeout int // seconds allowed for `apt-get install`
	installPkgs    string

	capMinutes int // the step's `timeout-minutes`
}

// worstCaseSeconds is the longest the step's own bounds permit it to run.
func (b aptStepBudget) worstCaseSeconds() int {
	return b.updateAttempts*b.updateTimeout +
		(b.updateAttempts-1)*b.backoff +
		b.installTimeout
}

// loopHeader is the `for` header the declared attempt count implies.
func (b aptStepBudget) loopHeader() string {
	attempts := make([]string, 0, b.updateAttempts)
	for i := 1; i <= b.updateAttempts; i++ {
		attempts = append(attempts, strconv.Itoa(i))
	}
	return "for attempt in " + strings.Join(attempts, " ")
}

func aptStepBudgets() []aptStepBudget {
	return []aptStepBudget{
		{
			workflow:       "migration-test.yml",
			job:            "historical-upgrades",
			step:           "Install test dependencies",
			updateAttempts: 3,
			updateTimeout:  120,
			backoff:        10,
			installTimeout: 180,
			installPkgs:    "jq libicu74",
			capMinutes:     11,
		},
		{
			workflow:       "release.yml",
			job:            "goreleaser",
			step:           "Install cross-compilation toolchains and signing tools",
			updateAttempts: 3,
			updateTimeout:  120,
			backoff:        10,
			installTimeout: 300,
			installPkgs:    "gcc-mingw-w64-x86-64 gcc-aarch64-linux-gnu osslsigncode",
			capMinutes:     13,
		},
	}
}

// TestAptGetStepsAreBoundedAndRetried pins the bounded/retried invocations, so
// a revert to a bare `apt-get update` -- the unbounded shape that burned whole
// job budgets on a stalled mirror -- goes red.
func TestAptGetStepsAreBoundedAndRetried(t *testing.T) {
	for _, b := range aptStepBudgets() {
		t.Run(b.workflow+"/"+b.step, func(t *testing.T) {
			step := readCIWorkflow(t, b.workflow).job(t, b.job).step(t, b.step)

			for _, required := range []string{
				fmt.Sprintf("sudo timeout %d apt-get", b.updateTimeout),
				"-o Acquire::Retries=3",
				"-o Acquire::http::Timeout=30",
				"-o Acquire::https::Timeout=30",
				b.loopHeader(),
				// Backoff between attempts, but not after the final one.
				fmt.Sprintf("if [ \"$attempt\" -lt %d ]; then", b.updateAttempts),
				fmt.Sprintf("sleep %d", b.backoff),
				// Exhausting the retries proceeds best-effort against the
				// cached index; the warning is what keeps that visible in the
				// job log instead of a silent fall-through.
				fmt.Sprintf("echo \"::warning::apt-get update failed after %d attempts; proceeding with cached index\"", b.updateAttempts),
				fmt.Sprintf("sudo timeout %d apt-get install -y %s", b.installTimeout, b.installPkgs),
			} {
				if !strings.Contains(step.Run, required) {
					t.Errorf("%s command does not contain %q:\n%s", b.step, required, step.Run)
				}
			}
		})
	}
}

// TestAptGetStepTimeoutsExceedTheirOwnBudget is the relation the first round of
// this hardening got backwards: the step-level cap was set *tighter* than the
// inner `timeout` calls it was meant to backstop.
func TestAptGetStepTimeoutsExceedTheirOwnBudget(t *testing.T) {
	for _, b := range aptStepBudgets() {
		t.Run(b.workflow+"/"+b.step, func(t *testing.T) {
			workflow := readCIWorkflow(t, b.workflow)
			job := workflow.job(t, b.job)
			step := job.step(t, b.step)

			if step.TimeoutMinutes != b.capMinutes {
				t.Errorf("%s timeout-minutes = %d, want %d", b.step, step.TimeoutMinutes, b.capMinutes)
			}

			// Read the relations off the workflow itself, never off capMinutes:
			// checking a declared constant against the budget it was derived
			// from is self-satisfying, and would stay green while the YAML
			// drifted underneath it.
			capSeconds := step.TimeoutMinutes * 60
			if capSeconds <= b.worstCaseSeconds() {
				t.Errorf("%s timeout-minutes = %d (%ds) does not exceed its own worst case of %ds "+
					"(%d update attempts x %ds + %d backoffs x %ds + %ds install); "+
					"raise the cap or shrink the inner budgets",
					b.step, step.TimeoutMinutes, capSeconds, b.worstCaseSeconds(),
					b.updateAttempts, b.updateTimeout, b.updateAttempts-1, b.backoff, b.installTimeout)
			}

			// Where the job carries its own cap, the step must fail first --
			// otherwise the whole job is cancelled and the log gives no hint
			// that apt was the thing that stalled.
			if job.TimeoutMinutes > 0 && step.TimeoutMinutes >= job.TimeoutMinutes {
				t.Errorf("%s timeout-minutes = %d is not below job %q timeout-minutes = %d",
					b.step, step.TimeoutMinutes, b.job, job.TimeoutMinutes)
			}
		})
	}
}
