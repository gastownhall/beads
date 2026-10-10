package scripts_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The dolt-race shadow (item 5, Phase 1): bazel.yml's bazel-dolt-race job
// runs --config=dolt-race, the union of --config=doltserver and
// --config=doltserver-proxied, in one `bazel test` beside the two member
// lanes, so the union-job model can be measured against them before any
// cutover. It must be advisory: no workflow_call output, nothing in pr.yml's
// gates or bazel-gate.sh/ci-gate.sh, and a failure that cannot fail the
// call's aggregate result (BAZEL) that pr.yml's ci-gate requires.

const (
	bazelDoltRaceJobName = "bazel-dolt-race"
	bazelDoltRaceConfig  = "dolt-race"
	// Mode remote only, and only on same-repo pull requests and merge
	// groups (mode remote on a pull_request event is never a fork: forks
	// get fork-ro, fork-rw or cache). Not on push, nightly, dispatch or
	// bazel-farm.yml's pull_request_target call.
	bazelDoltRaceJobIf = "${{ needs.rbe.outputs.mode == 'remote' && (github.event_name == 'pull_request' || github.event_name == 'merge_group') }}"
)

// bazelShadowLanes: bazel.yml jobs that run beside gated lanes only to
// measure a change to them, and report into nothing (no job or
// workflow_call output, no gate id, job-level continue-on-error), with the
// reason. TestBazelLaneIsGated and the continue-on-error checks carve them
// out by this map.
var bazelShadowLanes = map[string]string{
	bazelDoltRaceJobName: "item 5 Phase 1: the dolt-server + proxied union beside its member lanes, advisory until a cutover PR",
}

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

// TestBazelDoltRaceConfigIsMemberUnion: --config=dolt-race is exactly the
// set union of its members' lines, except that each member's
// --test_tag_filters is replaced by one filter naming both members' tags.
// Every flag stays a member's own, so each member target's test action is
// unchanged (verified with aquery in the PR that added it), and a flag added
// to a member must be added to the union too.
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

// TestBazelDoltRaceShadowIsAdvisory pins what keeps the shadow out of every
// gate: remote-only, job-level continue-on-error (a job's failure otherwise
// fails the reusable-workflow call, whose result pr.yml's ci-gate requires
// as BAZEL), no workflow_call output, no job needs it, and no gate script
// or caller names it.
func TestBazelDoltRaceShadowIsAdvisory(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	job := workflow.job(t, bazelDoltRaceJobName)

	if job.If != bazelDoltRaceJobIf {
		t.Errorf("%s if: %q, want %q", bazelDoltRaceJobName, job.If, bazelDoltRaceJobIf)
	}
	if !job.ContinueOnError {
		t.Errorf("%s: continue-on-error false; the shadow must never fail the bazel call pr.yml's ci-gate requires", bazelDoltRaceJobName)
	}
	if !slices.Equal(job.Needs, []string{bazelRBEJobName}) {
		t.Errorf("%s needs %v, want [%s]", bazelDoltRaceJobName, job.Needs, bazelRBEJobName)
	}
	for name, other := range workflow.Jobs {
		if slices.Contains(other.Needs, bazelDoltRaceJobName) {
			t.Errorf("job %s needs %s; nothing may wait on the shadow", name, bazelDoltRaceJobName)
		}
	}

	// The test step: the union config, as the member lanes run theirs.
	test := job.step(t, "bazel test //... --config="+bazelDoltRaceConfig)
	if strings.TrimSpace(test.Run) != bazelTierTestRun(bazelDoltRaceConfig) {
		t.Errorf("%s test step run changed; want exactly:\n%s\ngot:\n%s", bazelDoltRaceJobName, bazelTierTestRun(bazelDoltRaceConfig), test.Run)
	}

	// No workflow_call output reads the job.
	var doc struct {
		On struct {
			WorkflowCall struct {
				Outputs map[string]struct {
					Value string `yaml:"value"`
				} `yaml:"outputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.On.WorkflowCall.Outputs) == 0 {
		t.Fatalf("%s: no workflow_call outputs parsed; the check is broken", bazelWorkflowName)
	}
	for name, out := range doc.On.WorkflowCall.Outputs {
		if strings.Contains(out.Value, "jobs."+bazelDoltRaceJobName+".") || strings.Contains(name, bazelDoltRaceConfig) {
			t.Errorf("%s workflow_call output %s (%q) reads the shadow; it reports into no gate", bazelWorkflowName, name, out.Value)
		}
	}

	// No gate or caller names the job, its config or a gate id for it.
	root := sourceRepoRoot(t)
	for _, f := range []string{
		".github/workflows/pr.yml",
		".github/workflows/pr-risk.yml",
		".github/workflows/nightly.yml",
		".github/workflows/bazel-farm.yml",
		".github/scripts/bazel-gate.sh",
		".github/scripts/ci-gate.sh",
	} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		s := strings.ToLower(string(b))
		for _, needle := range []string{"dolt-race", "dolt_race", "doltrace"} {
			if strings.Contains(s, needle) {
				t.Errorf("%s mentions %q; the dolt-race shadow must stay out of every gate", f, needle)
			}
		}
	}
}

// TestBazelDoltRaceShadowRunsOnlyInRemotePRCalls evaluates the job's if:
// for every execution mode and every event bazel.yml runs on. It runs only
// in mode remote on a pull_request or merge_group event: never in a fork
// mode (fork-ro, fork-rw), cache or local, never on bazel-farm.yml's
// pull_request_target call (fork code with trusted credentials), and not on
// push, nightly (schedule) or dispatch, so it never doubles nightly's fresh
// execution of the two tiers.
func TestBazelDoltRaceShadowRunsOnlyInRemotePRCalls(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelDoltRaceJobName)
	for _, mode := range []string{"remote", "fork-ro", "fork-rw", "cache", "local", "skip"} {
		for _, event := range []string{"pull_request", "merge_group", "pull_request_target", "push", "schedule", "workflow_dispatch"} {
			want := mode == "remote" && (event == "pull_request" || event == "merge_group")
			got := evalRRCIf(t, job.If, map[string]string{
				"needs.rbe.outputs.mode": mode,
				"github.event_name":      event,
			})
			if got != want {
				t.Errorf("%s runs in mode %s on %s: %v, want %v", bazelDoltRaceJobName, mode, event, got, want)
			}
		}
	}
	// The runner is the lanes' ternary, which picks Blacksmith only in mode
	// remote: the only mode the job runs in.
	if job.RunsOn != bazelLaneRunsOn {
		t.Errorf("%s runs-on = %q, want %q", bazelDoltRaceJobName, job.RunsOn, bazelLaneRunsOn)
	}
}
