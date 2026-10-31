package scripts_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// worker-env-autosync.yml closes the #7419 gap: nightly.yml's rbe-worker-
// env-sync job only opens a ci/infra issue for a human to fix a gascity
// re-pin (#7387's ~100-minute stall); this instead runs tools/rbe/worker-
// env-sync as soon as gascity reports a re-pin (repository_dispatch) and
// every 15 minutes, and, on a real mismatch (exit 1), copies gascity's
// manifest and pin (tools/rbe/worker-env-sync-copy) and opens or updates a
// fixed-branch PR. With WORKER_ENV_SYNC_AUTOMERGE == 'true' the PR carries
// no review label and worker-env-sync-automerge.yml verifies it and arms
// auto-merge; otherwise it is labeled status/needs-review-auto and lands
// through the automated review and the merge queue.
const (
	workerEnvAutosyncWorkflow = "worker-env-autosync.yml"
	workerEnvAutosyncJob      = "autosync"
	workerEnvAutosyncBranch   = "bot/worker-env-sync"
	workerEnvSyncCopy         = "tools/rbe/worker-env-sync-copy"
	// gascity's repository_dispatch event type on a push to its main that
	// moves the worker-env pin; client_payload {"sha", "pin"}.
	workerEnvRepinnedEvent = "gascity-worker-env-repinned"
	// The repository variable that turns the unreviewed path on. Unset (or
	// anything but 'true'): today's review path, the rollback.
	workerEnvAutomergeVar = "${{ vars.WORKER_ENV_SYNC_AUTOMERGE }}"
	workerEnvPickStep     = "Pick the gascity commit to compare with"
)

// TestWorkerEnvAutosyncWorkflowTriggers: the App private keys this job mints
// are secrets of the autofix environment (deployment-branch policy: main
// only), and the only events that can ever run on main without a fork in
// the loop are schedule, workflow_dispatch and repository_dispatch (which
// only a token with write access to this repository can send: gascity's
// gascity-worker-env-repinned, within a minute of its re-pin, instead of up
// to 15 minutes later on the cron) -- pull_request (or
// pull_request_target) here would hand a fork a path to them.
func TestWorkerEnvAutosyncWorkflowTriggers(t *testing.T) {
	var parsed struct {
		On map[string]any `yaml:"on"`
	}
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+workerEnvAutosyncWorkflow)
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if got := len(parsed.On); got != 3 {
		t.Fatalf("%s on: has %d triggers (%v), want exactly schedule, workflow_dispatch and repository_dispatch", workerEnvAutosyncWorkflow, got, parsed.On)
	}
	if _, ok := parsed.On["workflow_dispatch"]; !ok {
		t.Errorf("%s has no workflow_dispatch trigger", workerEnvAutosyncWorkflow)
	}
	dispatch, _ := parsed.On["repository_dispatch"].(map[string]any)
	if !reflect.DeepEqual(dispatch, map[string]any{"types": []any{workerEnvRepinnedEvent}}) {
		t.Errorf("%s repository_dispatch = %v, want types: [%s] alone (any other event type must not start a sync)", workerEnvAutosyncWorkflow, parsed.On["repository_dispatch"], workerEnvRepinnedEvent)
	}
	schedule, ok := parsed.On["schedule"].([]any)
	if !ok || len(schedule) != 1 {
		t.Fatalf("%s schedule = %+v, want exactly one cron entry", workerEnvAutosyncWorkflow, parsed.On["schedule"])
	}
	cron, _ := schedule[0].(map[string]any)
	if cron["cron"] != "*/15 * * * *" {
		t.Errorf("%s schedule cron = %v, want */15 * * * * (every 15 minutes)", workerEnvAutosyncWorkflow, cron["cron"])
	}
	for _, forbidden := range []string{"pull_request", "pull_request_target", "push", "merge_group", "workflow_run"} {
		if _, ok := parsed.On[forbidden]; ok {
			t.Errorf("%s must never run on %s (forks or an unprivileged push could reach the autofix App token)", workerEnvAutosyncWorkflow, forbidden)
		}
	}
}

// TestWorkerEnvAutosyncWorkflowConcurrency: one run at a time, never
// cancelled. A dispatch and a cron run can start together, and two runs
// pushing the same branch would race their leases (the loser fails red);
// cancelling a run could stop it between its push and its PR step. With
// cancel-in-progress false a waiting run is replaced only by a newer one,
// which reads gascity afresh anyway.
func TestWorkerEnvAutosyncWorkflowConcurrency(t *testing.T) {
	var parsed struct {
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress *bool  `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	}
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+workerEnvAutosyncWorkflow)
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Concurrency.Group != "worker-env-autosync" {
		t.Errorf("%s concurrency group = %q, want the constant worker-env-autosync (every run, whatever its event, in one queue)", workerEnvAutosyncWorkflow, parsed.Concurrency.Group)
	}
	if parsed.Concurrency.CancelInProgress == nil || *parsed.Concurrency.CancelInProgress {
		t.Errorf("%s concurrency must set cancel-in-progress: false (a run is never cancelled mid-push)", workerEnvAutosyncWorkflow)
	}
}

// TestWorkerEnvAutosyncWorkflowPermissionsAreMinimal: least privilege.
// Top-level and the job's own grant both stay read-only: every write this
// job makes (the push, the PR, the label) goes through one of
// the two minted App tokens, never the job's own GITHUB_TOKEN permissions,
// so there is nothing here for either App token's role to leak into.
func TestWorkerEnvAutosyncWorkflowPermissionsAreMinimal(t *testing.T) {
	var top struct {
		Permissions map[string]string `yaml:"permissions"`
	}
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+workerEnvAutosyncWorkflow)
	if err := yaml.Unmarshal([]byte(text), &top); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("%s top-level permissions = %v, want contents: read", workerEnvAutosyncWorkflow, top.Permissions)
	}

	job := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob)
	wantJobPerms := map[string]any{"contents": "read"}
	if !reflect.DeepEqual(job.Permissions, wantJobPerms) {
		t.Errorf("%s job %s permissions = %v, want %v (contents:read alone; both write roles come from the minted App tokens)", workerEnvAutosyncWorkflow, workerEnvAutosyncJob, job.Permissions, wantJobPerms)
	}
	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("%s job %s runs-on = %q, want ubuntu-latest", workerEnvAutosyncWorkflow, workerEnvAutosyncJob, job.RunsOn)
	}
	if job.If != "github.repository == 'gastownhall/beads'" {
		t.Errorf("%s job %s if = %q, want the fork guard github.repository == 'gastownhall/beads'", workerEnvAutosyncWorkflow, workerEnvAutosyncJob, job.If)
	}
	if job.Environment.Name != "autofix" {
		t.Errorf("%s job %s environment = %q, want autofix (both Apps' id/private-key secrets live there, gated to main)", workerEnvAutosyncWorkflow, workerEnvAutosyncJob, job.Environment.Name)
	}
}

// TestWorkerEnvAutosyncWorkflowShape: the job compares with gascity main,
// gates every write step on a real mismatch (rc == 1, never rc == 2's
// "unknown" or rc == 0's "in step"), mints two App tokens only once a
// mismatch needs a write, pushes the fixed branch with push-token alone,
// and opens/labels the PR with pr-token alone (so it is not a
// GITHUB_TOKEN PR - those never trigger CI).
func TestWorkerEnvAutosyncWorkflowShape(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob)

	var names []string
	for _, step := range job.Steps {
		names = append(names, step.Name)
	}
	wantNames := []string{
		"", // the checkout
		workerEnvPickStep,
		"Compare the worker-env manifest and pin with gascity's main",
		"Copy gascity main's worker-env manifest and pin",
		"Confirm only the worker-env files moved",
		"Mint push token (gastownhall-autofix)",
		"Mint PR token (gascity-autofix)",
		"Commit and push the sync branch",
		"Open or update the sync PR",
	}
	if !slices.Equal(names, wantNames) {
		t.Errorf("%s steps = %q, want %q", workerEnvAutosyncWorkflow, names, wantNames)
	}

	// The comparison and the copy read the one commit the pick step
	// resolved (TestWorkerEnvAutosyncPickStep), never main twice.
	const refEnv = "${{ steps.ref.outputs.ref }}"
	pick := job.step(t, workerEnvPickStep)
	wantPickEnv := map[string]string{"EVENT_NAME": "${{ github.event_name }}", "PAYLOAD_SHA": "${{ github.event.client_payload.sha }}"}
	if pick.ID != "ref" || pick.If != "" || !reflect.DeepEqual(pick.Env, wantPickEnv) || !strings.Contains(pick.Run, rbeWorkerEnvSyncRef+" main") {
		t.Errorf("pick step: id %q, if %q, env %v; want id ref, no if, env %v, resolving main with %s", pick.ID, pick.If, pick.Env, wantPickEnv, rbeWorkerEnvSyncRef)
	}
	sync := job.step(t, "Compare the worker-env manifest and pin with gascity's main")
	if sync.ID != "sync" || sync.Env["REF"] != refEnv || !strings.Contains(sync.Run, `tools/rbe/worker-env-sync "$REF"`) {
		t.Errorf("sync step: id %q, env %v, run %q; want id sync running tools/rbe/worker-env-sync \"$REF\" with REF=%s", sync.ID, sync.Env, sync.Run, refEnv)
	}

	// The PR step stays on the mismatch gate alone, not on the push step's
	// pushed output: a run that pushed nothing must still open a PR for a
	// sync branch that has none (its PR was closed unmerged, or the run that
	// pushed it failed at gh pr create). The step itself leaves an open PR
	// alone unless this run pushed (TestWorkerEnvAutosyncPRStepNeverRelabelsAnOpenPR).
	const mismatchIf = "steps.sync.outputs.rc == '1'"
	for _, name := range []string{
		"Copy gascity main's worker-env manifest and pin",
		"Confirm only the worker-env files moved",
		"Mint push token (gastownhall-autofix)",
		"Mint PR token (gascity-autofix)",
		"Commit and push the sync branch",
		"Open or update the sync PR",
	} {
		step := job.step(t, name)
		if step.If != mismatchIf {
			t.Errorf("step %q if = %q, want %q (a write step must run only on a real mismatch)", name, step.If, mismatchIf)
		}
	}

	copyStep := job.step(t, "Copy gascity main's worker-env manifest and pin")
	if copyStep.Env["REF"] != refEnv || !strings.Contains(copyStep.Run, workerEnvSyncCopy+` "$REF"`) {
		t.Errorf("copy step env %v, run %q, want it to call %s \"$REF\" with REF=%s (the commit the comparison read)", copyStep.Env, copyStep.Run, workerEnvSyncCopy, refEnv)
	}

	pushMint := job.step(t, "Mint push token (gastownhall-autofix)")
	if pushMint.ID != "push-token" || actionFamily(pushMint.Uses) != "actions/create-github-app-token" ||
		pushMint.With["app-id"] != "${{ secrets.AUTOFIX_APP_ID }}" || pushMint.With["private-key"] != "${{ secrets.AUTOFIX_APP_PRIVATE_KEY }}" ||
		pushMint.With["permission-contents"] != "write" || pushMint.With["permission-pull-requests"] != "" {
		t.Errorf("push-token mint = %+v, want the gastownhall-autofix mint (AUTOFIX_APP_ID/AUTOFIX_APP_PRIVATE_KEY, contents:write only)", pushMint)
	}

	prMint := job.step(t, "Mint PR token (gascity-autofix)")
	if prMint.ID != "pr-token" || actionFamily(prMint.Uses) != "actions/create-github-app-token" ||
		prMint.With["app-id"] != "${{ secrets.GASCITY_AUTOFIX_APP_ID }}" || prMint.With["private-key"] != "${{ secrets.GASCITY_AUTOFIX_APP_PRIVATE_KEY }}" ||
		prMint.With["permission-pull-requests"] != "write" || prMint.With["permission-contents"] != "" {
		t.Errorf("pr-token mint = %+v, want the gascity-autofix mint (GASCITY_AUTOFIX_APP_ID/GASCITY_AUTOFIX_APP_PRIVATE_KEY, pull-requests:write only)", prMint)
	}
	if pushMint.Uses != prMint.Uses {
		t.Errorf("push-token and pr-token mint different action pins: %q vs %q, want the same SHA-pinned actions/create-github-app-token", pushMint.Uses, prMint.Uses)
	}

	push := job.step(t, "Commit and push the sync branch")
	if push.Uses != "" || !strings.Contains(push.Run, "git push") {
		t.Errorf("push step = %+v, want a plain git push (no action; push-token and pr-token cannot be split through one action's single token input)", push)
	}
	if push.Env["PUSH_TOKEN"] != "${{ steps.push-token.outputs.token }}" {
		t.Errorf("push step env PUSH_TOKEN = %q, want the minted push-token", push.Env["PUSH_TOKEN"])
	}
	if _, leaked := push.Env["GH_TOKEN"]; leaked {
		t.Errorf("push step sets GH_TOKEN; the push step must never see pr-token")
	}
	if !strings.Contains(push.Run, workerEnvAutosyncBranch) && push.Env["BRANCH"] != workerEnvAutosyncBranch {
		t.Errorf("push step does not reference the fixed branch %q", workerEnvAutosyncBranch)
	}
	// #7533: the lease names the branch tip the step itself read (empty:
	// the branch must not exist yet), never a remote-tracking ref this
	// checkout does not have, so a repeat run cannot fail on "stale info"
	// and a concurrent move of the branch is never overwritten.
	if !strings.Contains(push.Run, `ref="refs/heads/$BRANCH"`) || !strings.Contains(push.Run, "--force-with-lease=$ref:$remote_sha") {
		t.Errorf("push step run %q, want a lease scoped to the fixed branch and its observed SHA", push.Run)
	}
	// pushed=1 is the PR step's only evidence that this run moved the
	// branch. It is written once, after git push: under bash -e a failed
	// push ends the step before it, and a run that pushes nothing never
	// reaches it.
	const pushedLine = `echo "pushed=1" >> "$GITHUB_OUTPUT"`
	if push.ID != "push" {
		t.Errorf("push step id = %q, want push (the PR step reads steps.push.outputs.pushed)", push.ID)
	}
	if strings.Count(push.Run, "pushed=1") != 1 || strings.Index(push.Run, pushedLine) < strings.LastIndex(push.Run, "git push") {
		t.Errorf("push step run %q, want pushed=1 written once, as %s after the git push", push.Run, pushedLine)
	}

	pr := job.step(t, "Open or update the sync PR")
	if pr.Uses != "" {
		t.Fatalf("PR step uses %q, want plain gh (no action; the token split rules out peter-evans/create-pull-request, which takes one token)", pr.Uses)
	}
	if pr.Env["GH_TOKEN"] != "${{ steps.pr-token.outputs.token }}" {
		t.Errorf("PR step env GH_TOKEN = %q, want the minted pr-token", pr.Env["GH_TOKEN"])
	}
	if _, leaked := pr.Env["PUSH_TOKEN"]; leaked {
		t.Errorf("PR step sets PUSH_TOKEN; the PR step must never see push-token")
	}
	if pr.Env["BRANCH"] != workerEnvAutosyncBranch {
		t.Errorf("PR step env BRANCH = %q, want the fixed branch %q (so a later run updates the same PR instead of opening a new one)", pr.Env["BRANCH"], workerEnvAutosyncBranch)
	}
	if !strings.Contains(pr.Run, "gh pr create") || !strings.Contains(pr.Run, "gh pr edit") || !strings.Contains(pr.Run, `--label "$LABEL"`) {
		t.Errorf("PR step run %q, want it to create-or-update the PR via gh, labeling the PR it creates", pr.Run)
	}
	if strings.Contains(pr.Run, "--add-label") {
		t.Errorf("PR step run %q adds a label to an existing PR; it labels only in gh pr create (an open PR's review may be in flight)", pr.Run)
	}
	if pr.Env["LABEL"] != "status/needs-review-auto" {
		t.Errorf("PR step env LABEL = %q, want status/needs-review-auto (routes it through the automated review workflow)", pr.Env["LABEL"])
	}
	if pr.Env["AUTOMERGE"] != workerEnvAutomergeVar || pr.Env["GASCITY_REF"] != refEnv {
		t.Errorf("PR step env AUTOMERGE = %q, GASCITY_REF = %q; want %s (the label gate) and %s", pr.Env["AUTOMERGE"], pr.Env["GASCITY_REF"], workerEnvAutomergeVar, refEnv)
	}
	if pr.Env["PUSHED"] != "${{ steps.push.outputs.pushed }}" {
		t.Errorf("PR step env PUSHED = %q, want ${{ steps.push.outputs.pushed }} (an open PR is edited only by a run that pushed)", pr.Env["PUSHED"])
	}
}

// TestWorkerEnvAutosyncPickStep runs the pick step's real script against
// the shared gascity fixture (rbe_worker_env_test.go): a cron or manual run
// compares with gascity main's head commit; a repository_dispatch compares
// with the commit its payload names only while that commit is still main's
// head, and otherwise (a stale, foreign or malformed sha) with main's head,
// without ever echoing a malformed value into the log, where a newline
// could start a workflow command; and gascity unreachable leaves ref=main
// for worker-env-sync to report "unknown", without failing the step.
func TestWorkerEnvAutosyncPickStep(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the pick step runs only on worker-env-autosync.yml's ubuntu-latest runner; GOOS=%s", runtime.GOOS)
	}
	pick := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob).step(t, workerEnvPickStep)
	f := newRBEWorkerEnvFixture(t)
	beads := f.beads(t, f.manifest, f.pin)
	for _, c := range []struct {
		name, event, payload, gitURL string
		want                         string
		wantOut, wantNot             []string
	}{
		{name: "cron", event: "schedule", want: f.sha},
		{name: "manual run", event: "workflow_dispatch", payload: f.staleSHA, want: f.sha},
		{name: "dispatch naming main's head", event: "repository_dispatch", payload: f.sha, want: f.sha,
			wantOut: []string{"gascity main is at " + f.sha}},
		{name: "dispatch naming a commit main has moved past", event: "repository_dispatch", payload: f.staleSHA, want: f.sha,
			wantOut: []string{"::notice::repository_dispatch named gascity commit " + f.staleSHA + ", which is not gascity main's head (" + f.sha + ")"}},
		{name: "dispatch with a branch name", event: "repository_dispatch", payload: "main", want: f.sha,
			wantOut: []string{"::warning::repository_dispatch's client_payload.sha is not a 40-hex commit"}},
		{name: "dispatch with an injected command", event: "repository_dispatch", payload: f.sha + "\n::error::injected", want: f.sha,
			wantOut: []string{"::warning::repository_dispatch's client_payload.sha is not a 40-hex commit"}, wantNot: []string{"injected", "::error"}},
		{name: "gascity unreachable", event: "repository_dispatch", payload: f.sha, gitURL: "file:///nonexistent-gascity", want: "main"},
	} {
		t.Run(c.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			env := []string{"GITHUB_OUTPUT=" + output, "EVENT_NAME=" + c.event, "PAYLOAD_SHA=" + c.payload}
			if c.gitURL != "" {
				env = append(env, "WORKER_ENV_SYNC_GIT_URL="+c.gitURL)
			}
			code, out := f.run(t, beads, pick.Run, env...)
			got, _ := os.ReadFile(output)
			if code != 0 || string(got) != "ref="+c.want+"\n" {
				t.Errorf("pick step: exit %d, output %q; want exit 0, ref=%s\n%s", code, got, c.want, out)
			}
			for _, want := range c.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("pick step log lacks %q:\n%s", want, out)
				}
			}
			for _, not := range c.wantNot {
				if strings.Contains(out, not) {
					t.Errorf("pick step log contains %q:\n%s", not, out)
				}
			}
		})
	}
}

// TestWorkerEnvAutosyncPRStepNeverRelabelsAnOpenPR runs the "Open or
// update the sync PR" step's real run: script, parsed from the workflow,
// under bash -e (Actions' shell for a run: step with no shell:) against a
// fake gh that applies --jq with jq, as gh does (a null result printed as
// an empty line, as gh prints it). status/needs-review-auto
// requests the automated review, and the review pipeline swaps it for
// status/reviewing when that review starts (#7746: labeled 01:33:56Z,
// reviewing 01:34:34Z, label removed 01:34:36Z), so adding it to a PR that
// is already open requests another review of a PR whose review is running,
// failed, or done. Every run that gets past the push step reaches this
// step, and the push step finishes without pushing once the branch already
// carries the copy (#7533), so that is every run while main's pin is stale,
// the whole time the PR waits on that review.
// The step labels only a PR it opens itself (in the gh pr create call),
// edits an open PR only when this run pushed a new commit to it, and only
// warns about an open PR that carries no review-pipeline label (triage
// labels such as status/needs-triage, which triage-label.yml adds to every
// new PR, and status/needs-bugflow do not count). A fork PR from a branch
// of the same name is never taken for the sync PR. With
// WORKER_ENV_SYNC_AUTOMERGE == 'true' it labels nothing at all (the
// review pipeline must leave the PR to worker-env-sync-automerge.yml) and
// warns about no missing label; any other value is today's path.
func TestWorkerEnvAutosyncPRStepNeverRelabelsAnOpenPR(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the PR step runs only on worker-env-autosync.yml's ubuntu-latest runner; this file's executed fixtures are Linux-only; GOOS=%s", runtime.GOOS)
	}
	bash := requireHostTool(t, "bash")
	requireHostTool(t, "jq")
	pr := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob).step(t, "Open or update the sync PR")
	dir := t.TempDir()
	script := filepath.Join(dir, "pr-step.sh")
	if err := os.WriteFile(script, []byte(pr.Run), 0o600); err != nil {
		t.Fatal(err)
	}
	// The open PRs on the sync branch's head name, as gh pr list's
	// comma-separated JSON objects in $FAKE_GH_DIR/open.json; the fake
	// keeps only the fields the step's --json requests, as gh does. Each
	// call's argv is kept NUL-separated, one file per call, so a multi-line
	// --body stays one argument.
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	const fakeGH = `#!/usr/bin/env bash
set -u
count=$(( $(cat "$FAKE_GH_DIR/count" 2>/dev/null || echo 0) + 1 ))
echo "$count" > "$FAKE_GH_DIR/count"
printf '%s\0' "$@" > "$FAKE_GH_DIR/call.$(printf '%03d' "$count")"
case "${1-} ${2-}" in
"pr list")
  if [ -n "${FAKE_GH_LIST_FAILS:-}" ]; then echo "fake gh: HTTP 502" >&2; exit 1; fi
  jq_expr="." fields=""
  while [ $# -gt 0 ]; do
    case "$1" in
    --jq) jq_expr="$2"; shift ;;
    --json) fields="$2"; shift ;;
    esac
    shift
  done
  if [ -f "$FAKE_GH_DIR/open.json" ]; then json="[$(cat "$FAKE_GH_DIR/open.json")]"; else json='[]'; fi
  # gh returns only the fields named in --json, so a field the --jq reads
  # but --json does not request is absent here too.
  json="$(printf '%s\n' "$json" | jq -c --arg f "$fields" 'map(with_entries(select(.key as $k | $f | split(",") | index($k))))')"
  # gh prints an empty line where jq -r would print null.
  printf '%s\n' "$json" | jq -r "($jq_expr) | if . == null then \"\" else . end"
  exit ;;
"pr create")
  echo "https://github.com/gastownhall/beads/pull/4321"
  exit 0 ;;
"pr edit")
  exit 0 ;;
esac
echo "fake gh: unexpected invocation: $*" >&2
exit 127
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	const runURL = "https://github.com/gastownhall/beads/actions/runs/1001"
	const gascityRef = "0123456789abcdef0123456789abcdef01234567"
	run := func(t *testing.T, openJSON, pushed, automerge string, listFails bool) (int, string, [][]string) {
		t.Helper()
		state := t.TempDir()
		if openJSON != "" {
			if err := os.WriteFile(filepath.Join(state, "open.json"), []byte(openJSON), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bash, "-e", script)
		cmd.Env = []string{
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"FAKE_GH_DIR=" + state,
			"GH_TOKEN=fake-pr-token",
			"REPO=gastownhall/beads",
			"BRANCH=" + pr.Env["BRANCH"],
			"TITLE=" + pr.Env["TITLE"],
			"LABEL=" + pr.Env["LABEL"],
			"RUN_URL=" + runURL,
			"PUSHED=" + pushed,
			"AUTOMERGE=" + automerge,
			"GASCITY_REF=" + gascityRef,
		}
		if listFails {
			cmd.Env = append(cmd.Env, "FAKE_GH_LIST_FAILS=1")
		}
		out, err := cmd.CombinedOutput()
		code := exitCode(err)
		if code < 0 {
			t.Fatalf("run PR step: %v\n%s", err, out)
		}
		files, err := filepath.Glob(filepath.Join(state, "call.*"))
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(files)
		var calls [][]string
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, strings.Split(string(bytes.TrimSuffix(raw, []byte{0})), "\x00"))
		}
		return code, string(out), calls
	}

	// Every call but the lookup, with each --body value checked to cite
	// this run and gascity's commit and to say how the PR lands (the guard
	// with automerge on, else the review label, and never both), then
	// replaced by <body>.
	writes := func(t *testing.T, calls [][]string, automerge bool) [][]string {
		t.Helper()
		var w [][]string
		for _, c := range calls {
			if len(c) > 1 && c[0] == "pr" && c[1] == "list" {
				continue
			}
			c = slices.Clone(c)
			if i := slices.Index(c, "--body"); i >= 0 && i+1 < len(c) {
				body := c[i+1]
				if !strings.Contains(body, runURL) || !strings.Contains(body, "gastownhall/gascity@"+gascityRef) {
					t.Errorf("gh %q: --body does not cite this run %s and gascity's commit %s:\n%s", c[:i], runURL, gascityRef, body)
				}
				if guard, label := strings.Contains(body, "worker-env-sync-automerge.yml"), strings.Contains(body, "`status/needs-review-auto`"); guard != automerge || label == automerge {
					t.Errorf("gh %q: --body names the guard %v and the review label %v, want guard=%v (automerge %v):\n%s", c[:i], guard, label, automerge, automerge, body)
				}
				if strings.Contains(strings.ToLower(body), "armed") {
					t.Errorf("gh %q: --body says auto-merge is armed, but this step never arms it:\n%s", c[:i], body)
				}
				c[i+1] = "<body>"
			}
			w = append(w, c)
		}
		return w
	}
	const (
		reviewing = `{"number":7746,"isCrossRepository":false,"labels":[{"name":"status/reviewing"}]}`
		unlabeled = `{"number":7746,"isCrossRepository":false,"labels":[{"name":"kind/chore"}]}`
		triaged   = `{"number":7746,"isCrossRepository":false,"labels":[{"name":"status/needs-triage"}]}`
		bugflow   = `{"number":7746,"isCrossRepository":false,"labels":[{"name":"status/needs-bugflow"},{"name":"status/accepted"}]}`
		// A fork PR from a branch named bot/worker-env-sync: gh pr list
		// --head matches the branch name alone. It also carries
		// status/needs-triage, which triage-label.yml adds to fork PRs too.
		fork = `{"number":7792,"isCrossRepository":true,"labels":[{"name":"status/needs-triage"}]}`
	)
	create := []string{"pr", "create", "--repo", "gastownhall/beads", "--base", "main", "--head", workerEnvAutosyncBranch,
		"--title", "build(rbe): sync worker-env pin with gascity main", "--body", "<body>", "--label", "status/needs-review-auto"}
	// Automerge on: the same PR with no label at all.
	createUnlabeled := create[:len(create)-2]
	edit := []string{"pr", "edit", "7746", "--repo", "gastownhall/beads",
		"--title", "build(rbe): sync worker-env pin with gascity main", "--body", "<body>"}
	for _, tc := range []struct {
		name, open, pushed, automerge string
		want                          [][]string
		wantWarning                   bool
	}{
		{name: "first run opens the PR already labeled", pushed: "1", want: [][]string{create}},
		{name: "run that pushed nothing opens a labeled PR for a sync branch that has none", want: [][]string{create}},
		{name: "run that pushed nothing makes no write to the open PR", open: reviewing},
		{name: "run that pushed a new commit refreshes the open PR's title and body but never labels it", open: reviewing, pushed: "1", want: [][]string{edit}},
		{name: "open PR with no status label is warned about, never labeled", open: unlabeled, wantWarning: true},
		{name: "pushed run on an open PR with no status label edits it and warns, never labels", open: unlabeled, pushed: "1", want: [][]string{edit}, wantWarning: true},
		{name: "open PR carrying only status/needs-triage is warned about, never labeled", open: triaged, wantWarning: true},
		{name: "open PR carrying only non-review status labels is warned about, never labeled", open: bugflow, wantWarning: true},
		{name: "fork PR on the same head name is not the sync PR: opens a labeled PR, never edits the fork's", open: fork, pushed: "1", want: [][]string{create}},
		{name: "fork PR listed ahead of the open sync PR is skipped", open: fork + "," + reviewing, pushed: "1", want: [][]string{edit}},
		{name: "a variable other than true is the review path", pushed: "1", automerge: "false", want: [][]string{create}},
		{name: "automerge: first run opens the PR with no label", pushed: "1", automerge: "true", want: [][]string{createUnlabeled}},
		{name: "automerge: run that pushed nothing opens an unlabeled PR for a sync branch that has none", automerge: "true", want: [][]string{createUnlabeled}},
		{name: "automerge: pushed run refreshes the open PR, never labels it", open: unlabeled, pushed: "1", automerge: "true", want: [][]string{edit}},
		{name: "automerge: an open PR with no review label is expected, not warned about", open: unlabeled, automerge: "true"},
		{name: "automerge: a fork PR on the same head name is not the sync PR", open: fork, pushed: "1", automerge: "true", want: [][]string{createUnlabeled}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, calls := run(t, tc.open, tc.pushed, tc.automerge, false)
			if code != 0 {
				t.Fatalf("PR step exit %d, want 0\n%s", code, out)
			}
			if len(calls) == 0 || calls[0][0] != "pr" || calls[0][1] != "list" {
				t.Errorf("first gh call %q, want the gh pr list lookup", calls)
			}
			for _, c := range calls {
				if slices.Contains(c, "--add-label") {
					t.Errorf("PR step ran %q; it must never add a label to an existing PR (it would request another review)", c)
				}
			}
			if got := writes(t, calls, tc.automerge == "true"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PR step gh calls after the lookup = %q, want %q", got, tc.want)
			}
			if got := strings.Contains(out, "::warning::"); got != tc.wantWarning {
				t.Errorf("PR step emitted a warning = %v, want %v\n%s", got, tc.wantWarning, out)
			}
			if tc.wantWarning && !strings.Contains(out, "::warning::sync PR #7746 carries no review-pipeline status/* label") {
				t.Errorf("PR step warning does not name sync PR #7746's missing review-pipeline label\n%s", out)
			}
		})
	}

	t.Run("a failed PR lookup opens nothing", func(t *testing.T) {
		code, out, calls := run(t, "", "1", "", true)
		if code == 0 {
			t.Errorf("PR step exit 0 after gh pr list failed, want a failure\n%s", out)
		}
		if w := writes(t, calls, false); len(w) != 0 {
			t.Errorf("PR step ran %q after a failed lookup, want nothing (it could open a duplicate PR)", w)
		}
	})
}

// TestWorkerEnvAutosyncPushAgainstFixture executes the actual workflow Bash
// against one local bare Git remote. The fixture redirects only the synthetic
// authenticated URL and injects a concurrent ref move before the real push.
// From #7533 (vishnujayvel): a repeat run whose copy is already on the sync
// branch makes no commit and no push (and writes no pushed=1); a changed copy
// is pushed under a lease on the tip it read; a branch that moved in between,
// an unreadable remote and an ambiguous ls-remote answer each stop the step
// without a push.
func TestWorkerEnvAutosyncPushAgainstFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("worker-env autosync push fixture follows the Linux-only worker-env-autosync.yml test policy; GOOS=%s", runtime.GOOS)
	}
	python := requireHostTool(t, "python3")
	requireHostTool(t, "bash")
	requireHostTool(t, "git")
	push := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob).step(t, "Commit and push the sync branch")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", filepath.Join(bazelPolicyRoot(t), "scripts/worker_env_autosync_push_fixture.py"), "--fixture", t.TempDir())
	cmd.Stdin = strings.NewReader(push.Run)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worker-env autosync push fixture: %v\n%s", err, out)
	}
}

// TestWorkerEnvAutosyncWorkflowNeverInterpolatesExpressionsInRun: every
// `${{ }}` GitHub would otherwise substitute directly into a shell script
// is instead assigned to a step's `env:` first, so a value that happens to
// contain shell metacharacters (a PR title, a run URL) is data the shell
// sees through a variable, never text GitHub splices into the script
// before bash ever runs it.
func TestWorkerEnvAutosyncWorkflowNeverInterpolatesExpressionsInRun(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob)
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Errorf("step %q run contains a ${{ }} expression; move it to env: and reference the variable instead:\n%s", step.Name, step.Run)
		}
	}
}

// TestWorkerEnvAutosyncWorkflowLeavesMergingToTheGuard: nothing in this
// workflow merges the sync PR, arms auto-merge or queues it, and nothing
// could: pr-token is pull-requests:write alone, and GitHub's
// enablePullRequestAutoMerge wants contents:write on the same token
// (#7746's run failed with "Resource not accessible by integration"), the
// push-plus-PR token the two-token split here rules out. With
// WORKER_ENV_SYNC_AUTOMERGE == 'true' the queueing is
// worker-env-sync-automerge.yml's, after it has verified the PR and its
// required checks have passed, with a token of its own; otherwise the
// review label lands it
// (TestWorkerEnvAutosyncPRStepNeverRelabelsAnOpenPR checks the body says
// which).
func TestWorkerEnvAutosyncWorkflowLeavesMergingToTheGuard(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob)
	for _, step := range job.Steps {
		for _, merge := range []string{"gh pr merge", "enablePullRequestAutoMerge", "enqueuePullRequest"} {
			if strings.Contains(step.Run, merge) {
				t.Errorf("step %q runs %s; arming is worker-env-sync-automerge.yml's, after verification", step.Name, merge)
			}
		}
	}
	// The variable is read where the label is decided, and nowhere else
	// here.
	for _, step := range job.Steps {
		for k, v := range step.Env {
			if strings.Contains(v, "WORKER_ENV_SYNC_AUTOMERGE") && (step.Name != "Open or update the sync PR" || k != "AUTOMERGE") {
				t.Errorf("step %q env %s reads WORKER_ENV_SYNC_AUTOMERGE; only the PR step's AUTOMERGE may", step.Name, k)
			}
		}
	}
}

// TestWorkerEnvSyncCopyAgainstFixture runs the real tools/rbe/worker-env-
// sync-copy against a scratch checkout and a local gascity (reusing
// rbe_worker_env_test.go's fixture): it overwrites the manifest and the pin
// byte for byte with those of the commit REF names (main's own, not the
// CDN's stale copy of main the fixture also serves, or a commit given as
// REF), leaves everything else untouched, and fails loudly rather than
// writing anything when gascity's platforms/BUILD.bazel carries no pin to
// copy.
func TestWorkerEnvSyncCopyAgainstFixture(t *testing.T) {
	// worker-env-sync-copy runs only in worker-env-autosync.yml, on
	// ubuntu-latest. It edits the pin with GNU `sed -i -E`; BSD sed (macOS)
	// takes -E as -i's backup suffix, so the {64} interval is read as a
	// basic regex and never matches, and the pin is left stale. Its owner
	// keeps it Linux-only rather than porting it.
	if runtime.GOOS != "linux" {
		t.Skipf("worker-env-sync-copy is Linux-CI-only (worker-env-autosync.yml) and uses GNU sed -i -E; GOOS=%s", runtime.GOOS)
	}
	f := newRBEWorkerEnvFixture(t)
	stalePin := rbeTestPin(rbeTestStaleManifest)
	beads := f.beads(t, rbeTestStaleManifest, stalePin)
	rbeTestWrite(t, filepath.Join(beads, workerEnvSyncCopy), readPolicyFile(t, bazelPolicyRoot(t), workerEnvSyncCopy), 0o755)
	// A file the copy must never touch.
	rbeTestWrite(t, filepath.Join(beads, "tools/rbe/untouched.txt"), "keep\n", 0o644)

	code, out := f.run(t, beads, workerEnvSyncCopy+" main\n")
	if code != 0 {
		t.Fatalf("worker-env-sync-copy: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "copied gascity main's worker-env ("+f.pin+") at gascity commit "+f.sha+"\n") {
		t.Errorf("worker-env-sync-copy output %q, want it to report the copied pin %s and gascity's commit %s", out, f.pin, f.sha)
	}
	gotManifest, err := os.ReadFile(filepath.Join(beads, rbeWorkerEnvManifest))
	if err != nil || string(gotManifest) != f.manifest {
		t.Errorf("worker-env.txt after copy = %q, %v; want gascity's manifest %q", gotManifest, err, f.manifest)
	}
	gotBuild, err := os.ReadFile(filepath.Join(beads, rbeWorkerPlatformBuild))
	if err != nil || !strings.Contains(string(gotBuild), "\"worker-env\": \""+f.pin+"\",") {
		t.Errorf("platforms/BUILD.bazel after copy = %q, %v; want the pin %s", gotBuild, err, f.pin)
	}
	untouched, err := os.ReadFile(filepath.Join(beads, "tools/rbe/untouched.txt"))
	if err != nil || string(untouched) != "keep\n" {
		t.Errorf("worker-env-sync-copy touched an unrelated file: %q, %v", untouched, err)
	}

	// A commit as REF: that commit's manifest and pin.
	beadsAtCommit := f.beads(t, f.manifest, f.pin)
	rbeTestWrite(t, filepath.Join(beadsAtCommit, workerEnvSyncCopy), readPolicyFile(t, bazelPolicyRoot(t), workerEnvSyncCopy), 0o755)
	code, out = f.run(t, beadsAtCommit, workerEnvSyncCopy+" "+f.staleSHA+"\n")
	if code != 0 {
		t.Fatalf("worker-env-sync-copy %s: exit %d\n%s", f.staleSHA, code, out)
	}
	if got, err := os.ReadFile(filepath.Join(beadsAtCommit, rbeWorkerEnvManifest)); err != nil || string(got) != rbeTestStaleManifest {
		t.Errorf("worker-env.txt after copying commit %s = %q, %v; want that commit's manifest %q", f.staleSHA, got, err, rbeTestStaleManifest)
	}
	if got, err := os.ReadFile(filepath.Join(beadsAtCommit, rbeWorkerPlatformBuild)); err != nil || !strings.Contains(string(got), "\"worker-env\": \""+stalePin+"\",") {
		t.Errorf("platforms/BUILD.bazel after copying commit %s = %q, %v; want its pin %s", f.staleSHA, got, err, stalePin)
	}

	// gascity's platforms/BUILD.bazel with no pin at all: nothing copied.
	noPin := newRBEWorkerEnvGascity(t, "arch x86_64\n", "platform(\n    name = \"rbe_worker\",\n)\n")
	beadsNoPin := noPin.beads(t, rbeTestStaleManifest, stalePin)
	rbeTestWrite(t, filepath.Join(beadsNoPin, workerEnvSyncCopy), readPolicyFile(t, bazelPolicyRoot(t), workerEnvSyncCopy), 0o755)
	code, out = noPin.run(t, beadsNoPin, workerEnvSyncCopy+" main\n")
	if code == 0 || strings.Contains(out, "copied gascity") {
		t.Errorf("worker-env-sync-copy with no gascity pin: exit %d\n%s, want a failure and nothing copied", code, out)
	}
	stillStale, err := os.ReadFile(filepath.Join(beadsNoPin, rbeWorkerEnvManifest))
	if err != nil || string(stillStale) != rbeTestStaleManifest {
		t.Errorf("worker-env-sync-copy wrote despite no gascity pin: %q, %v", stillStale, err)
	}
}
