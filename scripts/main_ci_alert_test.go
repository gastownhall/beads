package scripts_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// How a red main surfaces (engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Merge
// Queue"). A merge group runs the fast set only, so the Windows, macOS and
// Nix jobs and the Dolt-tier Bazel lanes first run on main's push, after the
// merge. main-ci-alert.yml watches those push runs and main.yml's: a red one
// opens the ci/main-red issue (one at a time; later reds comment on it)
// naming every PR of the pushed range as a candidate (a batch lands as one
// push), and comments on them when it turns the workflow red; a later green
// run of the same workflow says so, and the issue closes once no tracked
// workflow's latest push run on main is red (.github/scripts/main-ci-alert.sh).

const (
	mainCIAlertWorkflowName = "main-ci-alert.yml"
	mainCIAlertScript       = ".github/scripts/main-ci-alert.sh"
	mainCIAlertLabel        = "ci/main-red"
	mainCIAlertIf           = "github.event.workflow_run.event == 'push' && github.event.workflow_run.head_branch == 'main' && github.event.workflow_run.head_repository.full_name == github.repository"
)

// mainCIAlertTracked: the workflows whose push runs on main the alert
// watches, by name (workflow_run matches names): the post-merge complement
// (PR, PR Risk, Bazel) and main.yml's push-only jobs.
var mainCIAlertTracked = []string{"PR", "PR Risk", "Bazel", "Main"}

// mainCIAlertFiles: the tracked workflows' files, which the script queries.
var mainCIAlertFiles = map[string]string{"PR": "pr.yml", "PR Risk": "pr-risk.yml", "Bazel": "bazel.yml", "Main": "main.yml"}

// TestMainCIAlertWorkflow pins the workflow: workflow_run on the tracked
// workflows' completed runs on main, acting only on this repository's push
// runs; GITHUB_TOKEN with exactly what it writes and reads; one run at a
// time (two reds finishing together must not open two issues); a sparse,
// credential-free checkout of the script from the default branch; no
// artifact and no code from the run it reports on.
func TestMainCIAlertWorkflow(t *testing.T) {
	root := sourceRepoRoot(t)
	raw := readPolicyFile(t, root, ".github/workflows/"+mainCIAlertWorkflowName)
	var doc struct {
		Name string `yaml:"name"`
		On   struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
				Branches  []string `yaml:"branches"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Concurrency any               `yaml:"concurrency"`
		Jobs        map[string]struct {
			Concurrency map[string]any `yaml:"concurrency"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	node := readYAMLNode(t, filepath.Join(".github", "workflows", mainCIAlertWorkflowName))
	if got := yamlMapKeys(node, "on"); !slices.Equal(got, []string{"workflow_run"}) {
		t.Errorf("%s triggers = %v, want exactly [workflow_run]", mainCIAlertWorkflowName, got)
	}
	wr := doc.On.WorkflowRun
	if !slices.Equal(wr.Workflows, mainCIAlertTracked) || !slices.Equal(wr.Types, []string{"completed"}) || !slices.Equal(wr.Branches, []string{"main"}) {
		t.Errorf("%s workflow_run = %+v, want workflows %v, types [completed], branches [main]", mainCIAlertWorkflowName, wr, mainCIAlertTracked)
	}
	// Each tracked name is a workflow here, and it runs on push to main.
	for _, name := range mainCIAlertTracked {
		file := mainCIAlertFiles[name]
		wf := readYAMLNode(t, filepath.Join(".github", "workflows", file))
		if got := yamlScalar(wf, "name"); got != name {
			t.Errorf("%s is named %q, want %q (the workflow_run trigger names it)", file, got, name)
		}
		if !slices.Contains(yamlMapKeys(wf, "on"), "push") {
			t.Errorf("%s has no push trigger; its push runs on main are what %s watches", file, mainCIAlertWorkflowName)
		}
	}
	if !reflect.DeepEqual(doc.Permissions, map[string]string{"contents": "read"}) || doc.Concurrency != nil {
		t.Errorf("%s workflow permissions %v, concurrency %v; want contents: read and no workflow-level concurrency", mainCIAlertWorkflowName, doc.Permissions, doc.Concurrency)
	}
	w := readCIWorkflow(t, mainCIAlertWorkflowName)
	if got := sortedKeys(w.Jobs); !slices.Equal(got, []string{"alert"}) {
		t.Fatalf("%s jobs = %v, want [alert]", mainCIAlertWorkflowName, got)
	}
	job := w.job(t, "alert")
	if job.If != mainCIAlertIf {
		t.Errorf("alert if = %q, want %q", job.If, mainCIAlertIf)
	}
	wantPerms := map[string]any{"contents": "read", "issues": "write", "pull-requests": "write", "actions": "read"}
	if !reflect.DeepEqual(job.Permissions, wantPerms) {
		t.Errorf("alert permissions = %v, want %v", job.Permissions, wantPerms)
	}
	if job.RunsOn != "ubuntu-latest" || job.TimeoutMinutes == 0 || job.TimeoutMinutes > 10 || job.ContinueOnError || len(job.Needs) != 0 {
		t.Errorf("alert runs-on %q, timeout %d, continue-on-error %v, needs %v; want ubuntu-latest, a timeout of at most 10 minutes", job.RunsOn, job.TimeoutMinutes, job.ContinueOnError, job.Needs)
	}
	// Serialized, and nothing dropped: GitHub keeps one pending run per
	// group by default and cancels the rest; queue: max keeps them all
	// (it may not be combined with cancel-in-progress).
	if got := doc.Jobs["alert"].Concurrency; !reflect.DeepEqual(got, map[string]any{"group": "main-ci-alert", "queue": "max"}) {
		t.Errorf("alert concurrency = %v, want group main-ci-alert, queue max", got)
	}
	if len(job.Steps) != 2 {
		t.Fatalf("alert has %d steps, want the script's checkout and the script", len(job.Steps))
	}
	checkout, run := job.Steps[0], job.Steps[1]
	wantCheckout := map[string]string{"ref": "${{ github.sha }}", "persist-credentials": "false", "sparse-checkout": mainCIAlertScript, "sparse-checkout-cone-mode": "false"}
	if checkout.Uses != "actions/checkout@"+checkoutSHA || !reflect.DeepEqual(checkout.With, wantCheckout) {
		t.Errorf("alert checkout = %+v, want %s with %v (the default branch's script only)", checkout, checkoutSHA, wantCheckout)
	}
	wantEnv := map[string]string{
		"GH_TOKEN":            "${{ github.token }}",
		"WORKFLOW_NAME":       "${{ github.event.workflow_run.name }}",
		"RUN_ID":              "${{ github.event.workflow_run.id }}",
		"RUN_URL":             "${{ github.event.workflow_run.html_url }}",
		"RUN_CREATED_AT":      "${{ github.event.workflow_run.created_at }}",
		"RUN_ATTEMPT":         "${{ github.event.workflow_run.run_attempt }}",
		"CONCLUSION":          "${{ github.event.workflow_run.conclusion }}",
		"HEAD_SHA":            "${{ github.event.workflow_run.head_sha }}",
		"HEAD_COMMIT_MESSAGE": "${{ github.event.workflow_run.head_commit.message }}",
	}
	if run.Run != "bash "+mainCIAlertScript || !reflect.DeepEqual(run.Env, wantEnv) || run.If != "" || run.Uses != "" {
		t.Errorf("alert step = %+v, want %q with env %v", run, "bash "+mainCIAlertScript, wantEnv)
	}
	// No artifact, no other run's code, no expression in a script.
	script := readPolicyFile(t, root, mainCIAlertScript)
	for _, forbidden := range []string{"download-artifact", "gh run download", "/artifacts", "git clone", "git fetch", "gh run rerun", "gh workflow run"} {
		if strings.Contains(raw, forbidden) || strings.Contains(script, forbidden) {
			t.Errorf("%s or %s mentions %q; the alert reads run metadata and writes the issue and a PR comment, nothing else", mainCIAlertWorkflowName, mainCIAlertScript, forbidden)
		}
	}
	if strings.Contains(script, "${{") || strings.Contains(run.Run, "${{") {
		t.Errorf("an expression reaches the alert's script; pass it through env")
	}
	// The script queries exactly the tracked workflows' files.
	for name, file := range mainCIAlertFiles {
		if !strings.Contains(script, `"`+name+`"`) || !strings.Contains(script, file) {
			t.Errorf("%s does not track %s (%s)", mainCIAlertScript, name, file)
		}
	}
	// The guard: only this repository's push runs on main.
	for _, c := range []struct {
		name string
		ctx  map[string]string
		want bool
	}{
		{"push to main", map[string]string{"github.event.workflow_run.event": "push", "github.event.workflow_run.head_branch": "main", "github.event.workflow_run.head_repository.full_name": mergeQueueOwnRepo, "github.repository": mergeQueueOwnRepo}, true},
		{"fork PR from the fork's main", map[string]string{"github.event.workflow_run.event": "pull_request", "github.event.workflow_run.head_branch": "main", "github.event.workflow_run.head_repository.full_name": "someone/beads", "github.repository": mergeQueueOwnRepo}, false},
		{"merge group", map[string]string{"github.event.workflow_run.event": "merge_group", "github.event.workflow_run.head_branch": "gh-readonly-queue/main/pr-1-abc", "github.event.workflow_run.head_repository.full_name": mergeQueueOwnRepo, "github.repository": mergeQueueOwnRepo}, false},
		{"dispatch on main", map[string]string{"github.event.workflow_run.event": "workflow_dispatch", "github.event.workflow_run.head_branch": "main", "github.event.workflow_run.head_repository.full_name": mergeQueueOwnRepo, "github.repository": mergeQueueOwnRepo}, false},
		{"nightly", map[string]string{"github.event.workflow_run.event": "schedule", "github.event.workflow_run.head_branch": "main", "github.event.workflow_run.head_repository.full_name": mergeQueueOwnRepo, "github.repository": mergeQueueOwnRepo}, false},
		{"push to another branch", map[string]string{"github.event.workflow_run.event": "push", "github.event.workflow_run.head_branch": "release/1.4", "github.event.workflow_run.head_repository.full_name": mergeQueueOwnRepo, "github.repository": mergeQueueOwnRepo}, false},
	} {
		if got := evalRRCIf(t, "${{ "+job.If+" }}", c.ctx); got != c.want {
			t.Errorf("alert guard on %s = %v, want %v", c.name, got, c.want)
		}
	}
}

// mainCIAlertGHStub stands in for gh: it logs every call (one line each) and
// every --body-file, lists the open issues it is given (GH_ISSUES, through
// the call's --jq), answers a run's jobs (GH_JOBS; GH_JOBS_FAIL fails it), a
// workflow's runs ($GH_RUNS_DIR/<file>.json, a page at a time as the API
// serves them: page, per_page (default 30), the newest 1000 only;
// GH_RUNS_FAIL fails every page, GH_RUNS_FAIL_PAGE=N page N) and a compare
// (GH_COMPARE; none set, or GH_COMPARE_FAIL, fails it), and prints a new
// issue's URL.
const mainCIAlertGHStub = `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_LOG"
prev=
for a in "$@"; do
	case "$prev" in --body-file) { printf '=== %s\n' "$*"; cat "$a"; } >>"$GH_LOG.bodies" ;; --jq) jqexpr=$a ;; esac
	prev=$a
done
case "$1 $2" in
	"issue list") jq -r "$jqexpr" <<<"$GH_ISSUES" ;;
	"issue create") echo "https://github.com/gastownhall/beads/issues/77" ;;
	"pr comment") [ -z "${GH_PR_COMMENT_FAIL:-}" ] || exit 1 ;;
	"api "*)
		for a in "$@"; do case "$a" in repos/*) path=$a ;; esac; done
		case "$path" in
			*/jobs*) [ -z "${GH_JOBS_FAIL:-}" ] || { echo "gh: jobs failed" >&2; exit 1; }; printf '%s\n' "$GH_JOBS" ;;
			*/compare/*) [ -n "${GH_COMPARE:-}" ] && [ -z "${GH_COMPARE_FAIL:-}" ] || { echo "gh: compare failed" >&2; exit 1; }; printf '%s\n' "$GH_COMPARE" ;;
			*/workflows/*/runs*)
				[ -z "${GH_RUNS_FAIL:-}" ] || { echo "gh: HTTP 502" >&2; exit 1; }
				file=${path#*/workflows/}; file=${file%%/runs*}
				page=1 per_page=30
				IFS='&' read -ra params <<<"${path#*\?}"
				for kv in "${params[@]}"; do case "$kv" in page=*) page=${kv#page=} ;; per_page=*) per_page=${kv#per_page=} ;; esac; done
				[ "$page" != "${GH_RUNS_FAIL_PAGE:-}" ] || { echo "gh: HTTP 502" >&2; exit 1; }
				runs='{"workflow_runs":[]}'
				if [ -f "$GH_RUNS_DIR/$file.json" ]; then runs=$(cat "$GH_RUNS_DIR/$file.json"); fi
				jq --argjson p "$page" --argjson n "$per_page" '{workflow_runs: .workflow_runs[:1000][($p - 1) * $n:$p * $n]}' <<<"$runs" ;;
			*) echo "unexpected api path $path" >&2; exit 1 ;;
		esac ;;
esac
`

// mainCIAlertRun: one workflow run as the runs API lists it.
type mainCIAlertRun struct {
	CreatedAt  string `json:"created_at"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	HeadSHA    string `json:"head_sha"`
}

// mainCIAlertCompare: a compare API answer (status, total_commits and the
// commits' messages, oldest first).
func mainCIAlertCompare(t *testing.T, status string, total int, messages ...string) string {
	t.Helper()
	var commits []map[string]any
	for _, m := range messages {
		commits = append(commits, map[string]any{"commit": map[string]any{"message": m}})
	}
	data, err := json.Marshal(map[string]any{"status": status, "total_commits": total, "commits": commits})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runMainCIAlert runs the script for one completed run with the stand-in gh
// and returns its exit code, its output, the gh calls and the bodies.
func runMainCIAlert(t *testing.T, env map[string]string, runs map[string][]mainCIAlertRun) (int, string, string, string) {
	t.Helper()
	requireHostTool(t, "bash")
	requireHostTool(t, "jq")
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	runsDir := filepath.Join(dir, "runs")
	for _, d := range []string{bin, runsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(mainCIAlertGHStub), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, list := range runs {
		if list == nil {
			list = []mainCIAlertRun{} // the API lists no runs as [], not null
		}
		data, err := json.Marshal(map[string]any{"workflow_runs": list})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runsDir, file+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "gh.log")
	cmd := exec.Command("bash", filepath.Join(sourceRepoRoot(t), mainCIAlertScript))
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "GH_LOG=" + log, "GH_RUNS_DIR=" + runsDir,
		"GITHUB_REPOSITORY=gastownhall/beads", "GH_TOKEN=token", "GH_ISSUES=[]", `GH_JOBS={"jobs":[]}`,
		"WORKFLOW_NAME=Bazel", "RUN_ID=555", "RUN_URL=https://github.com/gastownhall/beads/actions/runs/555",
		"RUN_CREATED_AT=2026-10-24T10:00:00Z", "RUN_ATTEMPT=1", "HEAD_SHA=" + strings.Repeat("d", 40),
		"HEAD_COMMIT_MESSAGE=fix(dolt): retry the dial (#123)\n\nbody line (#999)",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	bodies, _ := os.ReadFile(log + ".bodies")
	return exitCode(err), string(out), string(calls), string(bodies)
}

// TestMainCIAlertScript: the script against the stand-in gh. A red run
// (failure, timed_out, startup_failure) opens the ci/main-red issue, or
// comments on the open one, with the workflow, run, head commit, candidate
// PRs (every PR between the workflow's previous decided push run's head and
// this one's, or the head commit's alone when that range cannot be read) and
// failed jobs, and comments on the candidates only when it turns the workflow
// red and no newer push run of it is green (which makes it stale: the open
// issue alone hears of it); a green run with the issue open says so when the
// workflow's previous push run was red, and closes the issue once no tracked
// workflow's latest push run on main is red or unknown; anything else, or a
// green run with no issue open, changes nothing. Decided runs are found page
// after page past cancelled ones, within the runs API's newest 1000.
func TestMainCIAlertScript(t *testing.T) {
	const (
		jobs = `{"jobs":[{"name":"dolt-race","conclusion":"failure","html_url":"https://x/job/1"},` +
			`{"name":"test","conclusion":"success","html_url":"https://x/job/2"},` +
			`{"name":"embedded Dolt tier","conclusion":"timed_out","html_url":"https://x/job/3"}]}`
		open42 = `[{"number":42}]`
	)
	// A run's head: a fake SHA per run (its hour), the previous run's being
	// what the compare starts from.
	sha := func(created string) string { return strings.Repeat(created[11:13], 20) }
	red := func(created string) mainCIAlertRun {
		return mainCIAlertRun{created, "failure", "https://x/runs/red-" + created, sha(created)}
	}
	green := func(created string) mainCIAlertRun {
		return mainCIAlertRun{created, "success", "https://x/runs/green-" + created, sha(created)}
	}
	cancelled := func(created string) mainCIAlertRun {
		return mainCIAlertRun{created, "cancelled", "https://x/runs/cancelled-" + created, sha(created)}
	}
	// cancelledRuns: n cancelled runs, newest first, from 09:59:59 down (a
	// landing burst under main.yml's cancel-in-progress group); at most 3600.
	cancelledRuns := func(n int) []mainCIAlertRun {
		var out []mainCIAlertRun
		for i := range n {
			out = append(out, cancelled(fmt.Sprintf("2026-10-24T09:%02d:%02dZ", 59-i/60, 59-i%60)))
		}
		return out
	}
	// runsPage: the gh call that reads a page of a workflow's push runs.
	runsPage := func(file string, page int) string {
		return fmt.Sprintf("api repos/gastownhall/beads/actions/workflows/%s/runs?branch=main&event=push&status=completed&per_page=100&page=%d&", file, page)
	}
	head := strings.Repeat("d", 40)
	compareCall := "api repos/gastownhall/beads/compare/" + sha("2026-10-24T09:00:00Z") + "..." + head + "?per_page=100"
	batch := mainCIAlertCompare(t, "ahead", 4, "feat: one (#120)\n\nbody (#7)", "chore: a direct push", "fix: two (#121)", "fix(dolt): retry the dial (#123)")
	var dozen []string
	for n := 1; n <= 12; n++ {
		dozen = append(dozen, fmt.Sprintf("fix: %d (#%d)", n, n))
	}
	allGreen := map[string][]mainCIAlertRun{
		"pr.yml": {green("2026-10-24T10:00:00Z")}, "pr-risk.yml": {green("2026-10-24T10:00:00Z")},
		"bazel.yml": {green("2026-10-24T10:00:00Z"), red("2026-10-24T09:00:00Z")}, "main.yml": {green("2026-10-24T09:30:00Z")},
	}
	withRuns := func(base map[string][]mainCIAlertRun, file string, list ...mainCIAlertRun) map[string][]mainCIAlertRun {
		out := map[string][]mainCIAlertRun{}
		for k, v := range base {
			out[k] = v
		}
		out[file] = list
		return out
	}
	for _, c := range []struct {
		name    string
		env     map[string]string
		runs    map[string][]mainCIAlertRun
		want    []string // gh calls (prefixes of log lines)
		notWant []string // gh calls that must not happen
		bodyHas []string
		bodyNot []string
		// outputHas: the script's own output must contain this (a warning).
		outputHas string
		wantCode  int
	}{
		{
			name: "red, no issue open",
			env:  map[string]string{"CONCLUSION": "failure", "GH_JOBS": jobs},
			want: []string{
				"issue list --repo gastownhall/beads --label " + mainCIAlertLabel + " --state open",
				"api --paginate repos/gastownhall/beads/actions/runs/555/jobs?per_page=100",
				"label create " + mainCIAlertLabel + " --repo gastownhall/beads",
				"issue create --repo gastownhall/beads --title CI is red on main --label " + mainCIAlertLabel + " --body-file ",
				"pr comment 123 --repo gastownhall/beads --body-file ",
			},
			notWant: []string{"issue comment", "issue close"},
			bodyHas: []string{"**Bazel**", "failure", "https://github.com/gastownhall/beads/actions/runs/555", strings.Repeat("d", 40),
				"`fix(dolt): retry the dial (#123)`", "#123", "- [dolt-race](https://x/job/1): failure", "- [embedded Dolt tier](https://x/job/3): timed_out", "#77"},
			bodyNot: []string{"[test]", "#999"},
		},
		{
			name:    "red, issue open: comment, no second issue",
			env:     map[string]string{"CONCLUSION": "timed_out", "GH_JOBS": jobs, "GH_ISSUES": open42},
			want:    []string{"issue comment 42 --repo gastownhall/beads --body-file ", "pr comment 123 --repo gastownhall/beads --body-file "},
			notWant: []string{"issue create", "label create", "issue close"},
			bodyHas: []string{"timed_out", "#42", "- [dolt-race](https://x/job/1): failure"},
		},
		{
			name:    "startup failure: no job ran",
			env:     map[string]string{"CONCLUSION": "startup_failure"},
			want:    []string{"issue create --repo gastownhall/beads"},
			bodyHas: []string{"startup_failure", "no job ran"},
		},
		{
			name:    "red direct push: no PR to comment on",
			env:     map[string]string{"CONCLUSION": "failure", "HEAD_COMMIT_MESSAGE": "release: v1.4.0"},
			want:    []string{"issue create --repo gastownhall/beads"},
			notWant: []string{"pr comment"},
			bodyHas: []string{"`release: v1.4.0`", "no PR number"},
		},
		{
			name:    "red, the PR comment fails: warned, the issue stands",
			env:     map[string]string{"CONCLUSION": "failure", "GH_PR_COMMENT_FAIL": "1"},
			want:    []string{"issue create --repo gastownhall/beads", "pr comment 123"},
			bodyHas: []string{"#123"},
		},
		{
			name:    "subject with backticks and a mention stays inert",
			env:     map[string]string{"CONCLUSION": "failure", "HEAD_COMMIT_MESSAGE": "fix `x` for @someone (#5)"},
			want:    []string{"pr comment 5 "},
			bodyHas: []string{"`fix 'x' for @someone (#5)`"},
		},
		{
			name: "red after green, a batch of three: every PR in the pushed range",
			env:  map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch},
			runs: map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T09:00:00Z"), red("2026-10-24T08:00:00Z")}},
			want: []string{
				compareCall,
				"issue create --repo gastownhall/beads",
				"pr comment 120 --repo gastownhall/beads --body-file ",
				"pr comment 121 --repo gastownhall/beads --body-file ",
				"pr comment 123 --repo gastownhall/beads --body-file ",
			},
			notWant: []string{"pr comment 7 "},
			bodyHas: []string{"Candidates: #120, #121, #123 (every PR in https://github.com/gastownhall/beads/compare/", "https://x/runs/green-2026-10-24T09:00:00Z"},
			bodyNot: []string{"#7,", "not notified"},
		},
		{
			name:    "red after red: the issue alone, no PR comment",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "GH_ISSUES": open42},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {red("2026-10-24T09:00:00Z")}},
			want:    []string{compareCall, "issue comment 42 --repo gastownhall/beads --body-file "},
			notWant: []string{"pr comment", "issue create"},
			bodyHas: []string{"Candidates: #120, #121, #123", "already red on main before this push (https://x/runs/red-2026-10-24T09:00:00Z)"},
		},
		{
			name:    "red after red behind a full page of cancelled runs: the range from it, no PR comment",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "GH_ISSUES": open42},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": append(cancelledRuns(100), red("2026-10-24T09:00:00Z"))},
			want:    []string{runsPage("bazel.yml", 2), compareCall, "issue comment 42 --repo gastownhall/beads --body-file "},
			notWant: []string{runsPage("bazel.yml", 3), "pr comment", "issue create"},
			bodyHas: []string{"Candidates: #120, #121, #123", "already red on main before this push (https://x/runs/red-2026-10-24T09:00:00Z)"},
		},
		{
			name:      "red, no decided run before it in the runs API's window: the head commit's PR, notified, warned",
			env:       map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch},
			runs:      map[string][]mainCIAlertRun{"bazel.yml": append(cancelledRuns(1000), green("2026-10-24T08:00:00Z"))},
			want:      []string{runsPage("bazel.yml", 10), "issue create --repo gastownhall/beads", "pr comment 123 "},
			notWant:   []string{runsPage("bazel.yml", 11), "api repos/gastownhall/beads/compare/", "pr comment 120"},
			bodyHas:   []string{"Candidates: #123 (the head commit's alone"},
			outputHas: "::warning::main-ci-alert.sh: none of the runs API's newest 1000 push runs of Bazel",
		},
		{
			name:      "red, a newer push run is green: stale, nothing to report",
			env:       map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "GH_JOBS": jobs},
			runs:      map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T11:00:00Z"), green("2026-10-24T09:00:00Z")}},
			want:      []string{runsPage("bazel.yml", 1)},
			notWant:   []string{"issue create", "label create", "issue comment", "pr comment", "api repos/gastownhall/beads/compare/", "api --paginate"},
			outputHas: "a newer push run of it is green (https://x/runs/green-2026-10-24T11:00:00Z): nothing to report",
		},
		{
			name:    "red, a newer push run is green, issue open: the issue alone, no PR comment",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "GH_ISSUES": open42},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T11:00:00Z"), green("2026-10-24T09:00:00Z")}},
			want:    []string{compareCall, "issue comment 42 --repo gastownhall/beads --body-file "},
			notWant: []string{"pr comment", "issue create", "issue close"},
			bodyHas: []string{"Candidates: #120, #121, #123", "A newer push run of Bazel is green already (https://x/runs/green-2026-10-24T11:00:00Z)"},
		},
		{
			name:      "red, a newer green run behind a newest red one: still stale",
			env:       map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch},
			runs:      map[string][]mainCIAlertRun{"bazel.yml": {red("2026-10-24T11:00:00Z"), green("2026-10-24T10:30:00Z"), green("2026-10-24T09:00:00Z")}},
			notWant:   []string{"issue create", "pr comment"},
			outputHas: "green (https://x/runs/green-2026-10-24T10:30:00Z): nothing to report",
		},
		{
			name:    "red after green, the range cannot be listed: the head commit's PR",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "GH_COMPARE_FAIL": "1"},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T09:00:00Z")}},
			want:    []string{compareCall, "pr comment 123 "},
			notWant: []string{"pr comment 120", "pr comment 121"},
			bodyHas: []string{"Candidates: #123 (the head commit's alone"},
		},
		{
			name:    "red after green, not a fast-forward: the head commit's PR",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": mainCIAlertCompare(t, "diverged", 4, "feat: one (#120)", "fix(dolt): retry the dial (#123)")},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T09:00:00Z")}},
			want:    []string{"pr comment 123 "},
			notWant: []string{"pr comment 120"},
		},
		{
			name:    "red after green, a range past one page: the head commit's PR",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": mainCIAlertCompare(t, "ahead", 150, "feat: one (#120)", "fix(dolt): retry the dial (#123)")},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T09:00:00Z")}},
			want:    []string{"pr comment 123 "},
			notWant: []string{"pr comment 120"},
		},
		{
			name:    "red after green, twelve candidates: the issue lists all, the newest ten are notified",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": mainCIAlertCompare(t, "ahead", 12, dozen...)},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T09:00:00Z")}},
			want:    []string{"pr comment 3 ", "pr comment 12 "},
			notWant: []string{"pr comment 1 ", "pr comment 2 "},
			bodyHas: []string{"Candidates: #1, #2, #3, #4, #5, #6, #7, #8, #9, #10, #11, #12 (", "Notified the newest 10 of them."},
		},
		{
			name:    "red, a newer push run is red too: the range from the run before this one, notified",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {red("2026-10-24T11:00:00Z"), green("2026-10-24T09:00:00Z")}},
			want:    []string{compareCall, "pr comment 120 "},
			bodyNot: []string{"red-2026-10-24T11"},
		},
		{
			name:      "red, the runs API fails: the issue opens with the head commit's PR, notified",
			env:       map[string]string{"CONCLUSION": "failure", "GH_RUNS_FAIL": "1", "GH_COMPARE": batch},
			runs:      map[string][]mainCIAlertRun{"bazel.yml": {red("2026-10-24T09:00:00Z")}},
			want:      []string{"issue create --repo gastownhall/beads", "pr comment 123 "},
			notWant:   []string{"api repos/gastownhall/beads/compare/", "pr comment 120"},
			bodyHas:   []string{"Candidates: #123 (the head commit's alone"},
			outputHas: "::warning",
		},
		{
			name:      "red, the runs API fails on page 2: the issue opens with the head commit's PR, notified",
			env:       map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "GH_RUNS_FAIL_PAGE": "2"},
			runs:      map[string][]mainCIAlertRun{"bazel.yml": append(cancelledRuns(100), red("2026-10-24T09:00:00Z"))},
			want:      []string{runsPage("bazel.yml", 2), "issue create --repo gastownhall/beads", "pr comment 123 "},
			notWant:   []string{"api repos/gastownhall/beads/compare/", "pr comment 120"},
			bodyHas:   []string{"Candidates: #123 (the head commit's alone"},
			outputHas: "::warning::main-ci-alert.sh: could not list Bazel's push runs on main",
		},
		{
			name:      "red, the jobs API fails: the issue still opens",
			env:       map[string]string{"CONCLUSION": "failure", "GH_JOBS_FAIL": "1"},
			want:      []string{"issue create --repo gastownhall/beads", "pr comment 123 "},
			bodyHas:   []string{"Failed jobs: the run's jobs could not be listed"},
			outputHas: "::warning",
		},
		{
			name:    "red again on a re-run attempt: the issue alone, no PR comment",
			env:     map[string]string{"CONCLUSION": "failure", "GH_COMPARE": batch, "RUN_ATTEMPT": "2"},
			runs:    map[string][]mainCIAlertRun{"bazel.yml": {green("2026-10-24T09:00:00Z")}},
			want:    []string{compareCall, "issue create --repo gastownhall/beads"},
			notWant: []string{"pr comment"},
			bodyHas: []string{"Candidates: #120, #121, #123", "attempt 2 of this run"},
		},
		{name: "cancelled: nothing", env: map[string]string{"CONCLUSION": "cancelled", "GH_ISSUES": open42}, notWant: []string{"issue", "pr comment", "api", "label"}},
		{name: "skipped: nothing", env: map[string]string{"CONCLUSION": "skipped", "GH_ISSUES": open42}, notWant: []string{"issue", "pr comment", "api", "label"}},
		{
			name:    "green, no issue open: nothing to say",
			env:     map[string]string{"CONCLUSION": "success"},
			runs:    allGreen,
			want:    []string{"issue list --repo gastownhall/beads --label " + mainCIAlertLabel + " --state open"},
			notWant: []string{"issue comment", "issue close", "issue create", "pr comment", "api"},
		},
		{
			name:    "green after red, everything green now: comment and close",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs:    allGreen,
			want:    []string{"issue comment 42 --repo gastownhall/beads --body-file ", "issue close 42 --repo gastownhall/beads --comment "},
			notWant: []string{"issue create", "pr comment"},
			bodyHas: []string{"**Bazel** is green again", strings.Repeat("d", 40)},
		},
		{
			name:    "green after red, another workflow still red: comment, keep open",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs:    withRuns(allGreen, "pr-risk.yml", red("2026-10-24T09:50:00Z"), green("2026-10-24T08:00:00Z")),
			want:    []string{"issue comment 42 --repo gastownhall/beads --body-file "},
			notWant: []string{"issue close"},
			bodyHas: []string{"**Bazel** is green again", "PR Risk"},
		},
		{
			name:    "green, was green before: no comment, close once all green",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42, "WORKFLOW_NAME": "Main"},
			runs:    allGreen,
			want:    []string{"issue close 42 --repo gastownhall/beads --comment "},
			notWant: []string{"issue comment"},
		},
		{
			name:    "green, the API has not listed this run yet: it counts as the latest",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs:    withRuns(allGreen, "bazel.yml", red("2026-10-24T09:00:00Z")),
			want:    []string{"issue comment 42", "issue close 42"},
			bodyHas: []string{"**Bazel** is green again"},
		},
		{
			name:    "green, a newer push run of the same workflow is red: keep open",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs:    withRuns(allGreen, "bazel.yml", red("2026-10-24T11:00:00Z"), green("2026-10-24T10:00:00Z"), red("2026-10-24T09:00:00Z")),
			want:    []string{"issue comment 42"},
			notWant: []string{"issue close"},
		},
		{
			name:    "cancelled runs say nothing about the tree",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42, "WORKFLOW_NAME": "PR"},
			runs:    withRuns(allGreen, "main.yml", cancelled("2026-10-24T09:59:00Z"), red("2026-10-24T09:58:00Z")),
			notWant: []string{"issue close", "issue comment"},
		},
		{
			name:    "green, Main's latest decided run is red behind a full page of cancelled runs: keep open",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs:    withRuns(allGreen, "main.yml", append(cancelledRuns(100), red("2026-10-24T08:00:00Z"))...),
			want:    []string{runsPage("main.yml", 2), "issue comment 42 --repo gastownhall/beads --body-file "},
			notWant: []string{runsPage("main.yml", 3), "issue close"},
			bodyHas: []string{"**Bazel** is green again", "Still red: Main (https://x/runs/red-2026-10-24T08:00:00Z)"},
		},
		{
			name:    "green, no decided Main run in the runs API's window: not known, keep open",
			env:     map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs:    withRuns(allGreen, "main.yml", append(cancelledRuns(1000), green("2026-10-24T08:00:00Z"))...),
			want:    []string{runsPage("main.yml", 10), "issue comment 42 --repo gastownhall/beads --body-file "},
			notWant: []string{runsPage("main.yml", 11), "issue close"},
			bodyHas: []string{"**Bazel** is green again", "Not known: Main (no decided push run on main among the newest 1000"},
		},
		{
			name: "green, a tracked workflow has no push run on main yet: close",
			env:  map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42},
			runs: withRuns(allGreen, "pr.yml"),
			want: []string{"issue comment 42", "issue close 42 --repo gastownhall/beads --comment "},
		},
		{
			name:     "green, the runs API fails on page 2: the issue stays open, the job fails",
			env:      map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42, "GH_RUNS_FAIL_PAGE": "2"},
			runs:     withRuns(allGreen, "main.yml", append(cancelledRuns(100), green("2026-10-24T08:00:00Z"))...),
			want:     []string{runsPage("main.yml", 2)},
			notWant:  []string{"issue close", "issue comment"},
			wantCode: 1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, calls, bodies := runMainCIAlert(t, c.env, c.runs)
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d\n%s\ncalls:\n%s", code, c.wantCode, out, calls)
			}
			lines := strings.Split(strings.TrimSpace(calls), "\n")
			for _, want := range c.want {
				if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, want) }) {
					t.Errorf("no gh call %q:\n%s", want, calls)
				}
			}
			for _, not := range c.notWant {
				if slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, not) }) {
					t.Errorf("unexpected gh call %q:\n%s", not, calls)
				}
			}
			for _, want := range c.bodyHas {
				if !strings.Contains(bodies, want) && !strings.Contains(calls, want) {
					t.Errorf("no %q in the bodies:\n%s\ncalls:\n%s", want, bodies, calls)
				}
			}
			for _, not := range c.bodyNot {
				if strings.Contains(bodies, not) {
					t.Errorf("%q in the bodies:\n%s", not, bodies)
				}
			}
			if strings.Contains(c.name, "comment fails") && !strings.Contains(out, "::warning") {
				t.Errorf("a failed PR comment printed no warning:\n%s", out)
			}
			if c.outputHas != "" && !strings.Contains(out, c.outputHas) {
				t.Errorf("output lacks %q:\n%s", c.outputHas, out)
			}
		})
	}
	// Env it cannot do without fails the job (a broken alert must show).
	if code, out, _, _ := runMainCIAlert(t, map[string]string{"CONCLUSION": "failure", "RUN_ID": ""}, nil); code == 0 {
		t.Errorf("RUN_ID unset: exit 0, want a failure\n%s", out)
	}
	if code, out, _, _ := runMainCIAlert(t, map[string]string{"CONCLUSION": "success", "GH_ISSUES": open42, "RUN_CREATED_AT": "yesterday"}, allGreen); code == 0 {
		t.Errorf("RUN_CREATED_AT not a timestamp: exit 0, want a failure\n%s", out)
	}
	if code, out, _, _ := runMainCIAlert(t, map[string]string{"CONCLUSION": "failure", "WORKFLOW_NAME": "Nightly"}, nil); code == 0 {
		t.Errorf("an untracked workflow: exit 0, want a failure\n%s", out)
	}
	for _, attempt := range []string{"", "0", "x", "2a", "-1"} {
		if code, out, calls, _ := runMainCIAlert(t, map[string]string{"CONCLUSION": "failure", "RUN_ATTEMPT": attempt}, nil); code == 0 || calls != "" {
			t.Errorf("RUN_ATTEMPT %q: exit %d, gh calls %q; want a failure before any call\n%s", attempt, code, calls, out)
		}
	}
	// The label it files under is the one the workflow and docs name.
	if script := readPolicyFile(t, sourceRepoRoot(t), mainCIAlertScript); !regexp.MustCompile(`(?m)^label="` + regexp.QuoteMeta(mainCIAlertLabel) + `"$`).MatchString(script) {
		t.Errorf("%s does not file under %s", mainCIAlertScript, mainCIAlertLabel)
	}
}
