package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Merge queue readiness (engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Merge
// Queue"). The default-branch ruleset requires "CI Gate / Required" (pr.yml)
// and "PR Risk Gate / Required" (pr-risk.yml); with a merge queue both must
// also report on every queue entry's merge-group commit, from a merge_group
// run that has no github.event.pull_request at all. These tests simulate
// that event with the shared evalGHExpr evaluator (ci_blacksmith_runner_test.go)
// and the workflows' real step scripts.

const (
	// bazel.yml's lanes that otherwise never reuse a test result add
	// --config=reuse-test-results on a merge_group run and on attempt 2+ of
	// a pull_request run (never on a PR's first attempt, push or nightly).
	bazelReuseResultsEnv    = "${{ (github.event_name == 'merge_group' || github.event_name == 'pull_request' && github.run_attempt != '1') && '--config=reuse-test-results' || '' }}"
	bazelReuseResultsArg    = `${BAZEL_REUSE_RESULTS:+"$BAZEL_REUSE_RESULTS"}`
	bazelReuseResultsRCLine = "test:reuse-test-results --cache_test_results=auto"
	mergeQueueActor         = "github-merge-queue[bot]"
	mergeQueueOwnRepo       = "gastownhall/beads"
)

// The retired tiers' configs that set --nocache_test_results themselves (the
// other three reuse lanes get it from sole-run).
var bazelReuseResultsConfigs = map[string]bool{"embedded": true, "doltserver-proxied": true, "doltserver-integration": true}

// Every lane whose `bazel test` would otherwise always execute: sole-run's
// three (test, pure, doltserver) and the retired tiers' three.
var bazelReuseResultsLanes = []string{bazelJobName, bazelPureJobName, bazelDoltJobName, bazelEmbedJobName, bazelProxiedJobName, bazelServerJobName}

// mergeGroupCtx: an evalGHExpr context for a merge_group run of entry n
// (no github.event.pull_request.* key: null on this event).
func mergeGroupCtx(n int, extra map[string]string) map[string]string {
	ctx := map[string]string{
		"github.event_name":                 "merge_group",
		"github.repository":                 mergeQueueOwnRepo,
		"github.actor":                      mergeQueueActor,
		"github.ref":                        fmt.Sprintf("refs/heads/gh-readonly-queue/main/pr-%d-%040d", 7000+n, n),
		"github.run_attempt":                "1",
		"github.event.merge_group.base_sha": fmt.Sprintf("%040d", 100+n),
		"github.event.merge_group.head_sha": fmt.Sprintf("%040d", 200+n),
		"github.event.merge_group.base_ref": "refs/heads/main",
		"github.event.merge_group.head_ref": fmt.Sprintf("refs/heads/gh-readonly-queue/main/pr-%d-%040d", 7000+n, n),
	}
	for k, v := range extra {
		ctx[k] = v
	}
	return ctx
}

// pullRequestCtx: an evalGHExpr context for a same-repo pull_request run.
func pullRequestCtx(n int, extra map[string]string) map[string]string {
	ctx := map[string]string{
		"github.event_name":                             "pull_request",
		"github.repository":                             mergeQueueOwnRepo,
		"github.actor":                                  "alice",
		"github.ref":                                    fmt.Sprintf("refs/pull/%d/merge", 7000+n),
		"github.run_attempt":                            "1",
		"github.event.pull_request.number":              strconv.Itoa(7000 + n),
		"github.event.pull_request.base.sha":            fmt.Sprintf("%040d", 300+n),
		"github.event.pull_request.head.sha":            fmt.Sprintf("%040d", 400+n),
		"github.event.pull_request.head.repo.full_name": mergeQueueOwnRepo,
		"github.event.pull_request.head.repo.fork":      "false",
	}
	for k, v := range extra {
		ctx[k] = v
	}
	return ctx
}

// interpolateGH replaces every ${{ ... }} in s with its evalGHExpr value.
func interpolateGH(t *testing.T, s string, ctx map[string]string) string {
	t.Helper()
	re := regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		v, err := evalGHExpr(m, ctx)
		if err != nil {
			t.Fatalf("evalGHExpr(%q): %v", m, err)
		}
		switch x := v.(type) {
		case nil:
			return ""
		case bool:
			return strconv.FormatBool(x)
		default:
			return fmt.Sprint(x)
		}
	})
}

// The two required gates: their workflows run on merge_group (exactly the
// checks_requested type), the gate jobs keep their ruleset names and always
// run, and no job's or step's condition in either workflow keys on the
// event, so the merge group runs (or skips) exactly what the decision jobs
// say, like a PR.
func TestMergeQueueRequiredGatesRunOnMergeGroup(t *testing.T) {
	gates := map[string]string{"pr.yml": "CI Gate / Required", prRiskWorkflowName: "PR Risk Gate / Required"}
	for name, gateName := range gates {
		var doc struct {
			On map[string]yaml.Node `yaml:"on"`
		}
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+name)), &doc); err != nil {
			t.Fatal(err)
		}
		mg, ok := doc.On["merge_group"]
		if !ok {
			t.Fatalf("%s has no merge_group trigger; a queued PR would wait forever for %q", name, gateName)
		}
		var trig struct {
			Types []string `yaml:"types"`
		}
		if err := mg.Decode(&trig); err != nil || strings.Join(trig.Types, ",") != "checks_requested" {
			t.Errorf("%s merge_group trigger types = %v (%v), want exactly [checks_requested]", name, trig.Types, err)
		}
		w := readCIWorkflow(t, name)
		gate := w.job(t, "ci-gate")
		if gate.Name != gateName || gate.If != "${{ always() }}" {
			t.Errorf("%s ci-gate: name %q, if %q; want %q (the ruleset's exact context), if ${{ always() }}", name, gate.Name, gate.If, gateName)
		}
		// The gate runs on Blacksmith for a merge group (same-repo trust).
		if got := mustEvalGHRunsOn(t, gate.RunsOn, mergeGroupCtx(1, nil)); !strings.HasPrefix(got, "blacksmith-") {
			t.Errorf("%s ci-gate runs-on for merge_group = %q, want a Blacksmith runner", name, got)
		}
		event := regexp.MustCompile(`github\.event_name|github\.event\.|github\.base_ref|github\.head_ref`)
		for jobName, job := range w.Jobs {
			if event.MatchString(job.If) {
				t.Errorf("%s job %s if %q keys on the event; decide in a job (detect-ci-tier, bazel-coverage) and read its outputs", name, jobName, job.If)
			}
			for _, step := range job.Steps {
				if event.MatchString(step.If) {
					t.Errorf("%s job %s step %q if %q keys on the event; a merge group must run it like a PR", name, jobName, step.Name, step.If)
				}
				if strings.Contains(step.Run, "github.base_ref") || strings.Contains(step.Run, "github.head_ref") {
					t.Errorf("%s job %s step %q interpolates base_ref/head_ref, which a merge group lacks", name, jobName, step.Name)
				}
			}
		}
	}
}

// Concurrency: a merge group's runs key on its own gh-readonly-queue/* ref,
// so two queue entries never share a group (cancel-in-progress would
// otherwise cancel the earlier entry's required checks), and a PR's own
// runs never cancel its queue entry's or the other way round. bazel.yml's
// called-workflow group never cancels a merge group either.
func TestMergeQueueConcurrencyNeverCancelsQueueRuns(t *testing.T) {
	for _, name := range []string{"pr.yml", prRiskWorkflowName, bazelWorkflowName} {
		var doc struct {
			Concurrency struct {
				Group            string `yaml:"group"`
				CancelInProgress any    `yaml:"cancel-in-progress"`
			} `yaml:"concurrency"`
		}
		if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+name)), &doc); err != nil {
			t.Fatal(err)
		}
		group := func(ctx map[string]string) string {
			c := map[string]string{"github.workflow": strings.TrimSuffix(name, ".yml")}
			for k, v := range ctx {
				c[k] = v
			}
			return interpolateGH(t, doc.Concurrency.Group, c)
		}
		mg1, mg2 := group(mergeGroupCtx(1, nil)), group(mergeGroupCtx(2, nil))
		pr1 := group(pullRequestCtx(1, nil))
		if mg1 == mg2 || mg1 == pr1 || !strings.Contains(mg1, "gh-readonly-queue/main/pr-7001") {
			t.Errorf("%s concurrency group: merge group 1 %q, merge group 2 %q, PR %q; want one group per queue entry, apart from the PR's", name, mg1, mg2, pr1)
		}
		if name == bazelWorkflowName {
			if got := interpolateGH(t, fmt.Sprint(doc.Concurrency.CancelInProgress), mergeGroupCtx(1, nil)); got != "false" {
				t.Errorf("%s cancel-in-progress on merge_group = %q, want false", name, got)
			}
		}
	}
}

// Every github.event.pull_request.* read in the required topology either
// falls back to the merge_group field (evaluated below: the queue's SHAs on
// merge_group, the PR's on pull_request) or is listed here with why null is
// right on a merge group. A new read fails until it is classified.
func TestMergeQueuePullRequestFieldsHandleMergeGroup(t *testing.T) {
	// path suffix (workflow:YAML path) -> why null on merge_group is right.
	nullOK := map[string]string{
		"pr.yml:.concurrency.group":               "falls back to github.ref (TestMergeQueueConcurrencyNeverCancelsQueueRuns)",
		"pr-risk.yml:.concurrency.group":          "falls back to github.ref",
		"bazel.yml:.concurrency.group":            "falls back to github.ref",
		"bazel-coverage.steps[0].env.FORK":        "null == true is false: a merge group is never a fork (bazel.yml's rbe job's expression, verbatim)",
		"rbe.steps[0].env.FORK":                   "null == true is false: a merge group is never a fork",
		"rbe.steps[0].env.PR_NUMBER":              "read only for fork/Dependabot pull_request runs (the rbe-fork mint)",
		".env.RBE_FORK_PR":                        "setup-bazel reads it only in the fork modes, which a merge group never takes",
		"detect-ci-tier.steps[1].env.PR_BASE_SHA": "ci-embedded-tier.sh returns full coverage for merge_group before reading it",
		"detect-ci-tier.steps[1].env.PR_HEAD_SHA": "ci-embedded-tier.sh returns full coverage for merge_group before reading it",
		"detect-ci-tier.steps[2].env.PR_BASE_SHA": "advisory shadow selector; merge_group selects everything",
		"detect-ci-tier.steps[2].env.PR_HEAD_SHA": "advisory shadow selector; merge_group selects everything",
		"bazel-test.steps[*].env.PR_NUMBER":       "bazel-sync patch metadata; bazel-autofix.yml ignores non-pull_request runs",
		"bazel-test.steps[*].env.PR_HEAD_SHA":     "bazel-sync patch metadata; bazel-autofix.yml ignores non-pull_request runs",
	}
	used := map[string]bool{}
	var fallbacks []string
	for _, name := range []string{"pr.yml", prRiskWorkflowName, bazelWorkflowName} {
		walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", name)), "", func(path string, key bool, value string) {
			if key || !strings.Contains(value, "github.event.pull_request.") {
				return
			}
			// The Blacksmith venue ternaries name merge_group explicitly
			// (TestSameRepoBlacksmithExpressionSemantics).
			if strings.HasSuffix(path, ".runs-on") && strings.Contains(value, "github.event_name == 'merge_group' ||") {
				return
			}
			if strings.Contains(value, "github.event.merge_group.") {
				fallbacks = append(fallbacks, value)
				return
			}
			for suffix := range nullOK {
				// steps[*] matches any step index (the sync step moves when
				// steps are added before it).
				re := regexp.MustCompile(regexp.QuoteMeta(suffix) + "$")
				if strings.Contains(suffix, "[*]") {
					re = regexp.MustCompile(strings.ReplaceAll(regexp.QuoteMeta(suffix), `\[\*\]`, `\[[0-9]+\]`) + "$")
				}
				if re.MatchString(name + ":" + path) {
					used[suffix] = true
					return
				}
			}
			t.Errorf("%s %s reads %q, which is null on merge_group: add a github.event.merge_group.* fallback or classify it here", name, path, value)
		})
	}
	for suffix, why := range nullOK {
		if !used[suffix] {
			t.Errorf("stale merge_group classification %q (%s): nothing matches it", suffix, why)
		}
	}
	if len(fallbacks) < 3 {
		t.Fatalf("found %d merge_group fallbacks, want at least the migration-hygiene BASE_SHA and both package gates' bounds", len(fallbacks))
	}
	for _, expr := range fallbacks {
		mg, pr := interpolateGH(t, expr, mergeGroupCtx(1, nil)), interpolateGH(t, expr, pullRequestCtx(1, nil))
		if !strings.HasPrefix(mg, "0000") || mg == pr || !strings.HasPrefix(pr, "0000") {
			t.Errorf("%q: merge_group %q, pull_request %q; want the queue's SHA and the PR's", expr, mg, pr)
		}
	}
}

// D2 on merge_group: both workflows' bazel-coverage decision, with its env
// evaluated by evalGHExpr for a merge group, retires every flagged legacy
// tier (the Bazel lanes cover them), and bazel.yml's rbe step, evaluated the
// same way, takes mode remote with the CI secrets, so pr.yml's gate requires
// those lanes to have run remotely and passed. Without the secret or with
// the farm switch off the mode is cache or skip, and the gate is red
// (TestPRRiskDecisionMatchesBazelMode's named merge_group cases).
func TestMergeQueueBazelCoversRetiredTiers(t *testing.T) {
	requireHostTool(t, "bash")
	for _, name := range []string{"pr.yml", prRiskWorkflowName} {
		step := coverageStep(t, name)
		ctx := mergeGroupCtx(1, nil)
		for k, v := range workflowEnv(t, name) {
			ctx["env."+k] = v
		}
		env := map[string]string{}
		for k, v := range step.Env {
			env[k] = interpolateGH(t, v, ctx)
		}
		if env["MERGE_GROUP"] != "true" || env["PULL_REQUEST"] != "false" || env["FORK"] != "false" || env["DEPENDABOT"] != "false" {
			t.Errorf("%s bazel-coverage env on merge_group = %v", name, env)
		}
		out, err := runBazelRBEDecision(t, step.Run, env)
		if err != nil {
			t.Fatalf("%s bazel-coverage on merge_group: %v", name, err)
		}
		for _, r := range retiredTiers {
			want := strconv.FormatBool(ctx["env."+r.flag] == "true")
			if out[r.output] != want {
				t.Errorf("%s bazel-coverage %s on merge_group = %q, want %s (flag %s %q)", name, r.output, out[r.output], want, r.flag, ctx["env."+r.flag])
			}
		}
	}
	rbe := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName)
	for _, c := range []struct {
		name, varOn, secret, mode string
	}{
		{"farm on, secrets", "true", "grpcs://rbe.example:443", "remote"},
		{"secret missing", "true", "", "cache"},
		{"farm switch off", "", "grpcs://rbe.example:443", "skip"},
	} {
		ctx := mergeGroupCtx(1, map[string]string{"vars.RBE_WEST_WORKERS": c.varOn, "secrets.RBE_WEST_EXECUTOR": c.secret, "inputs.rbe": "on", "inputs.fork-farm": "off"})
		env := map[string]string{}
		for k, v := range rbe.Steps[0].Env {
			env[k] = interpolateGH(t, v, ctx)
		}
		out, log, err := runBazelRBEDecisionLog(t, rbe.Steps[0].Run, env)
		if err != nil {
			t.Fatalf("rbe on merge_group (%s): %v", c.name, err)
		}
		if out["mode"] != c.mode || log != "" {
			t.Errorf("rbe on merge_group (%s): mode %q (mint asked: %q), want %s without asking rbe-fork", c.name, out["mode"], log, c.mode)
		}
		if got := mustEvalGHRunsOn(t, rbe.RunsOn, ctx); !strings.HasPrefix(got, "blacksmith-") {
			t.Errorf("rbe runs-on for merge_group = %q, want Blacksmith", got)
		}
	}
}

// Result reuse: exactly the six always-execute lanes take
// BAZEL_REUSE_RESULTS, it is --config=reuse-test-results only on a merge
// group or a PR re-run, every `bazel test` in those lanes passes it after
// the lane's own config and sole-run (so its --cache_test_results=auto
// wins), and the config is that one line: no retries, no eviction-retry
// change. Push to main, nightly, dispatch and a PR's first attempt never
// reuse a result.
func TestMergeQueueReusesUnchangedTestResults(t *testing.T) {
	w := readCIWorkflow(t, bazelWorkflowName)
	lanes := map[string]bool{}
	for _, lane := range bazelReuseResultsLanes {
		lanes[lane] = true
	}
	bazelTest := regexp.MustCompile(`^\s*bazel\s+test\b`)
	for name, job := range w.Jobs {
		if !lanes[name] {
			if _, ok := job.Env["BAZEL_REUSE_RESULTS"]; ok {
				t.Errorf("%s sets BAZEL_REUSE_RESULTS; only %v reuse results this way", name, bazelReuseResultsLanes)
			}
			for _, step := range job.Steps {
				if strings.Contains(step.Run, "reuse-test-results") || strings.Contains(step.Run, "BAZEL_REUSE_RESULTS") || strings.Contains(step.Run, "cache_test_results") {
					t.Errorf("%s step %q names result reuse", name, step.Name)
				}
			}
			continue
		}
		if job.Env["BAZEL_REUSE_RESULTS"] != bazelReuseResultsEnv {
			t.Errorf("%s env BAZEL_REUSE_RESULTS = %q, want %q", name, job.Env["BAZEL_REUSE_RESULTS"], bazelReuseResultsEnv)
		}
		tests := 0
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "reuse-test-results") || strings.Contains(step.Run, "cache_test_results") {
				t.Errorf("%s step %q names the config directly; only the env may", name, step.Name)
			}
			for _, line := range strings.Split(step.Run, "\n") {
				if !bazelTest.MatchString(line) {
					continue
				}
				tests++
				i := strings.Index(line, bazelReuseResultsArg)
				if i < 0 || strings.Contains(line[i:], "--config") || strings.Contains(line[i:], "SOLE_RUN") {
					t.Errorf("%s step %q: %q must pass %s after every --config", name, step.Name, line, bazelReuseResultsArg)
				}
			}
		}
		if tests == 0 {
			t.Errorf("%s runs no bazel test", name)
		}
	}
	for _, c := range []struct {
		name string
		ctx  map[string]string
		want string
	}{
		{"merge_group", mergeGroupCtx(1, nil), "--config=reuse-test-results"},
		{"merge_group re-run", mergeGroupCtx(1, map[string]string{"github.run_attempt": "2"}), "--config=reuse-test-results"},
		{"pull_request, first attempt", pullRequestCtx(1, nil), ""},
		{"pull_request, re-run", pullRequestCtx(1, map[string]string{"github.run_attempt": "2"}), "--config=reuse-test-results"},
		{"pull_request, third attempt", pullRequestCtx(1, map[string]string{"github.run_attempt": "3"}), "--config=reuse-test-results"},
		{"push to main", map[string]string{"github.event_name": "push", "github.run_attempt": "1"}, ""},
		{"push to main, re-run", map[string]string{"github.event_name": "push", "github.run_attempt": "2"}, ""},
		{"nightly", map[string]string{"github.event_name": "schedule", "github.run_attempt": "1"}, ""},
		{"dispatch", map[string]string{"github.event_name": "workflow_dispatch", "github.run_attempt": "2"}, ""},
		{"bazel-farm", map[string]string{"github.event_name": "pull_request_target", "github.run_attempt": "2"}, ""},
	} {
		if got := interpolateGH(t, bazelReuseResultsEnv, c.ctx); got != c.want {
			t.Errorf("%s: BAZEL_REUSE_RESULTS = %q, want %q", c.name, got, c.want)
		}
	}
	rc := readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")
	var lines []string
	for _, line := range strings.Split(rc, "\n") {
		line = strings.TrimSpace(line)
		head, _, _ := strings.Cut(line, " ")
		if strings.HasSuffix(head, ":reuse-test-results") {
			lines = append(lines, line)
		}
	}
	if strings.Join(lines, "\n") != bazelReuseResultsRCLine {
		t.Errorf(".bazelrc --config=reuse-test-results = %q, want exactly %q", lines, bazelReuseResultsRCLine)
	}
	// The docker lane (host state outside the action key) never reuses:
	// dolt-lane docker is a dispatch-only input, where the env is empty.
	if !strings.Contains(rc, "\ntest:docker --nocache_test_results\n") {
		t.Error(".bazelrc lost test:docker --nocache_test_results")
	}
}

// detect-package-gates.sh on merge_group diffs the queue's own bounds
// (merge_group.base_sha..head_sha), not HEAD^: a rebase-method entry
// stacks every commit of the PR, and only the first touches a package.
func TestMergeQueueDetectPackageGatesUsesQueueBounds(t *testing.T) {
	requireHostTool(t, "git")
	requireHostTool(t, "bash")
	dir, base, head := mergeQueueRepo(t, []string{"npm-package/package.json"}, []string{"README.md"})
	script := filepath.Join(sourceRepoRoot(t), "scripts/ci/detect-package-gates.sh")
	run := func(env ...string) map[string]string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "out")
		cmd := exec.Command("bash", script)
		cmd.Dir = dir
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out, "GITHUB_EVENT_NAME=merge_group"}, env...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("detect-package-gates.sh: %v\n%s", err, b)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			k, v, _ := strings.Cut(l, "=")
			got[k] = v
		}
		return got
	}
	if got := run("PR_BASE_SHA="+base, "PR_HEAD_SHA="+head); got["npm_package"] != "true" || got["mcp_package"] != "false" {
		t.Errorf("merge group whose first commit changes npm-package/: %v, want npm_package only", got)
	}
	if got := run(); got["npm_package"] != "true" || got["mcp_package"] != "true" {
		t.Errorf("merge group without bounds: %v, want every gate", got)
	}
	if got := run("PR_BASE_SHA="+head+"^", "PR_HEAD_SHA="+head); got["npm_package"] != "false" {
		t.Errorf("merge group touching only README.md: %v, want no gate", got)
	}
}

// fast-checks' .beads guard runs on merge_group against the queue's base
// commit and on pull_request against origin/<base branch>.
func TestMergeQueueBeadsGuardRuns(t *testing.T) {
	requireHostTool(t, "git")
	requireHostTool(t, "bash")
	step := readCIWorkflow(t, "pr.yml").job(t, "fast-checks").step(t, "Check for .beads/issues.jsonl changes")
	for _, c := range []struct {
		name    string
		changed string
		pass    bool
	}{
		{"unrelated change", "README.md", true},
		{".beads change", ".beads/issues.jsonl", false},
	} {
		dir, base, _ := mergeQueueRepo(t, []string{c.changed})
		gitIn(t, dir, "update-ref", "refs/remotes/origin/main", base)
		for event, env := range map[string][]string{
			"merge_group":  {"MERGE_GROUP_BASE_SHA=" + interpolateGH(t, step.Env["MERGE_GROUP_BASE_SHA"], mergeGroupCtx(1, map[string]string{"github.event.merge_group.base_sha": base}))},
			"pull_request": {"MERGE_GROUP_BASE_SHA=" + interpolateGH(t, step.Env["MERGE_GROUP_BASE_SHA"], pullRequestCtx(1, nil)), "GITHUB_BASE_REF=main"},
		} {
			cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
			cmd.Dir = dir
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
			out, err := cmd.CombinedOutput()
			if (err == nil) != c.pass {
				t.Errorf("%s, %s: err %v, want pass %v\n%s", event, c.name, err, c.pass, out)
			}
		}
	}
}

// check-doc-flags' CLI-docs drift attribution needs a diff base; a merge
// group has no GITHUB_BASE_REF, so the step passes the queue's base commit
// (and nothing on pull_request, where origin/$GITHUB_BASE_REF applies).
// Without it, drift already on main fails every queue entry.
func TestMergeQueueDocDriftHasDiffBase(t *testing.T) {
	step := readCIWorkflow(t, "pr.yml").job(t, "check-doc-flags").step(t, "Validate docs against CLI")
	expr := step.Env["BD_DOCS_DIFF_BASE"]
	if got := interpolateGH(t, expr, mergeGroupCtx(1, nil)); got != fmt.Sprintf("%040d", 101) {
		t.Errorf("check-doc-flags BD_DOCS_DIFF_BASE on merge_group = %q (%q), want merge_group.base_sha", got, expr)
	}
	if got := interpolateGH(t, expr, pullRequestCtx(1, nil)); got != "" {
		t.Errorf("check-doc-flags BD_DOCS_DIFF_BASE on pull_request = %q, want empty (origin/$GITHUB_BASE_REF)", got)
	}
	drift := readPolicyFile(t, sourceRepoRoot(t), "scripts/check-cli-docs-drift.sh")
	if !strings.Contains(drift, `BASE_REF="${BD_DOCS_DIFF_BASE:-}"`) {
		t.Error("check-cli-docs-drift.sh no longer reads BD_DOCS_DIFF_BASE")
	}
}

// mergeQueueRepo: a git repo with a base commit and one commit per file
// group on top; returns its dir, the base SHA and the head SHA.
func mergeQueueRepo(t *testing.T, commits ...[]string) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base.txt", "base\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	base = strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	for i, files := range commits {
		for _, f := range files {
			write(f, fmt.Sprintf("change %d\n", i))
		}
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", fmt.Sprintf("change %d", i))
	}
	head = strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	return dir, base, head
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
