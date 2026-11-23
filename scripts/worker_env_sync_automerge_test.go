package scripts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// worker-env-sync-automerge.yml lands worker-env-autosync.yml's sync PR
// (bot/worker-env-sync) with no review: tools/rbe/worker-env-sync-verify
// checks the PR from the API, as data, and the act job queues it only on
// its "arm" verdict for the exact head act reads itself, once main's
// required checks have passed on that head; anything else takes the PR out.
// Every property pinned here is part of that trust boundary; relaxing one
// is a security decision, not a refactor.
const (
	workerEnvAutomergeWorkflow = "worker-env-sync-automerge.yml"
	workerEnvSyncVerify        = "tools/rbe/worker-env-sync-verify"
	workerEnvSyncPRBot         = "gascity-autofix[bot]"
	workerEnvSyncPushBot       = "gastownhall-autofix[bot]"
	workerEnvSyncReviewLabel   = "status/needs-review-auto"

	// verify's if: the head branch and the same-repository filters first,
	// for pull_request_target and for workflow_run (only its pull_request
	// runs), so no other PR's or branch's run gets past it, then the fork
	// guard and the variable gate.
	workerEnvPRTFilter = "(github.event_name != 'pull_request_target' || " +
		"(github.event.pull_request.head.ref == 'bot/worker-env-sync' && " +
		"github.event.pull_request.head.repo.full_name == github.repository))"
	workerEnvRunFilter = "(github.event_name != 'workflow_run' || " +
		"(github.event.workflow_run.event == 'pull_request' && " +
		"github.event.workflow_run.head_branch == 'bot/worker-env-sync' && " +
		"github.event.workflow_run.head_repository.full_name == github.repository))"
	workerEnvAutomergeVerifyIf = workerEnvPRTFilter + " && " + workerEnvRunFilter + " && " +
		"github.repository == 'gastownhall/beads' && vars.WORKER_ENV_SYNC_AUTOMERGE == 'true'"
	// act and rerun repeat the workflow_run event filter
	// (TestBazelFarmArtifactsHaveNoPrivilegedConsumer requires it of every
	// job of a workflow_run consumer).
	workerEnvRunEventFilter = "(github.event_name != 'workflow_run' || github.event.workflow_run.event == 'pull_request')"
	// act fails closed: after every verification of the sync PR, failed
	// or with its outputs lost included.
	workerEnvActIf   = "!cancelled() && needs.verify.result != 'skipped' && needs.verify.outputs.verdict != 'none' && " + workerEnvRunEventFilter
	workerEnvRerunIf = "needs.verify.outputs.rerun != '' && " + workerEnvRunEventFilter

	workerEnvAutomergeGroupPrefix = "worker-env-sync-automerge-"
	workerEnvAutomergeGroup       = workerEnvAutomergeGroupPrefix + "${{ " +
		"(github.event_name == 'pull_request_target' && " +
		"(github.event.pull_request.head.ref != 'bot/worker-env-sync' || " +
		"github.event.pull_request.head.repo.full_name != github.repository) && " +
		"github.event.pull_request.number) || " +
		"(github.event_name == 'workflow_run' && " +
		"(github.event.workflow_run.event != 'pull_request' || " +
		"github.event.workflow_run.head_branch != 'bot/worker-env-sync' || " +
		"github.event.workflow_run.head_repository.full_name != github.repository) && " +
		"github.event.workflow_run.id) || 'sync-pr' }}"

	// act's one GraphQL read of the PR, in its job env.
	workerEnvPRStateQuery = "query($owner: String!, $name: String!, $number: Int!) { repository(owner: $owner, name: $name) " +
		"{ pullRequest(number: $number) { id headRefOid isInMergeQueue autoMergeRequest { enabledAt } } } }"

	workerEnvStateStep      = "Read the sync PR and decide"
	workerEnvMergeTokenStep = "Mint merge token (gascity-autofix)"
	workerEnvTakeoutStep    = "Take the sync PR out of auto-merge and the merge queue"
	workerEnvArmStep        = "Arm auto-merge"
	workerEnvPRTokenStep    = "Mint PR token (gascity-autofix)"
	workerEnvCommentStep    = "Comment the refusal on the sync PR"
	workerEnvLabelStep      = "Label the sync PR for review instead"

	workerEnvMergeToken = "${{ steps.merge-token.outputs.token }}"
	workerEnvPRToken    = "${{ steps.pr-token.outputs.token }}"
	workerEnvFallbackIf = "steps.state.outputs.arm == 'true' && needs.verify.outputs.fallback-label == 'true' && " +
		"(steps.merge-token.outcome == 'failure' || steps.arm.outputs.denied == '1')"
)

// The act job's steps and their if:.
var workerEnvActSteps = []struct{ name, id, ifExpr string }{
	{workerEnvStateStep, "state", ""},
	{workerEnvMergeTokenStep, "merge-token", "steps.state.outputs.arm == 'true' || steps.state.outputs.takeout != ''"},
	{workerEnvTakeoutStep, "", "steps.state.outputs.takeout != ''"},
	{workerEnvArmStep, "arm", "steps.state.outputs.arm == 'true' && steps.merge-token.outcome == 'success'"},
	{workerEnvPRTokenStep, "pr-token", "steps.state.outputs.pr != '' && (needs.verify.outputs.comment == 'true' || (" + workerEnvFallbackIf + "))"},
	{workerEnvCommentStep, "", "steps.state.outputs.pr != '' && needs.verify.outputs.comment == 'true'"},
	{workerEnvLabelStep, "", workerEnvFallbackIf},
}

// TestWorkerEnvSyncAutomergeWorkflowTriggers: workflow_run of the sync PR's
// PR and PR Risk runs, to queue it the moment its required checks pass;
// pull_request_target (opened, reopened, synchronize, ready_for_review
// into main), to act on a push at once; a 15-minute schedule plus
// workflow_dispatch to re-check an open sync PR after a missed event or a
// merge-queue ejection. Nothing else: no pull_request (a fork's own copy
// of this file would run), no comment trigger.
func TestWorkerEnvSyncAutomergeWorkflowTriggers(t *testing.T) {
	root := readYAMLNode(t, filepath.Join(".github", "workflows", workerEnvAutomergeWorkflow))
	if got := yamlScalar(root, "name"); got != "rbe worker-env sync automerge" {
		t.Errorf("name = %q, want rbe worker-env sync automerge", got)
	}
	triggers := yamlMapKeys(root, "on")
	sort.Strings(triggers)
	if want := []string{"pull_request_target", "schedule", "workflow_dispatch", "workflow_run"}; !slices.Equal(triggers, want) {
		t.Fatalf("triggers = %v, want exactly %v", triggers, want)
	}
	var doc struct {
		On struct {
			PullRequestTarget struct {
				Types    []string `yaml:"types"`
				Branches []string `yaml:"branches"`
			} `yaml:"pull_request_target"`
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
			} `yaml:"workflow_run"`
			Schedule []map[string]string `yaml:"schedule"`
		} `yaml:"on"`
	}
	if err := root.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if want := []string{"opened", "reopened", "synchronize", "ready_for_review"}; !slices.Equal(doc.On.PullRequestTarget.Types, want) {
		t.Errorf("pull_request_target types = %v, want %v", doc.On.PullRequestTarget.Types, want)
	}
	if !slices.Equal(doc.On.PullRequestTarget.Branches, []string{"main"}) {
		t.Errorf("pull_request_target branches = %v, want [main]", doc.On.PullRequestTarget.Branches)
	}
	// The two workflows whose gates main requires: the run that finishes
	// last is the one that turns the head green.
	if !slices.Equal(doc.On.WorkflowRun.Workflows, []string{"PR", "PR Risk"}) || !slices.Equal(doc.On.WorkflowRun.Types, []string{"completed"}) {
		t.Errorf("workflow_run = %v %v, want workflows [PR PR Risk], types [completed]", doc.On.WorkflowRun.Workflows, doc.On.WorkflowRun.Types)
	}
	for file, name := range map[string]string{"pr.yml": "PR", "pr-risk.yml": "PR Risk"} {
		if got := yamlScalar(readYAMLNode(t, filepath.Join(".github", "workflows", file)), "name"); got != name {
			t.Errorf("%s name = %q, want %q (the workflow_run trigger names it)", file, got, name)
		}
	}
	if len(doc.On.Schedule) != 1 || doc.On.Schedule[0]["cron"] != "*/15 * * * *" {
		t.Errorf("schedule = %v, want one */15 * * * * cron", doc.On.Schedule)
	}
}

// TestWorkerEnvSyncAutomergeConcurrency: every run about the sync PR (its
// pull_request_target events, its own PR and PR Risk runs' completions,
// the schedule, a dispatch) shares one group, never cancelled while
// running, so a verdict is acted on before the next verification starts.
// A newer run may replace one still waiting (GitHub's default queue), which
// loses nothing: each run reads the PR afresh. Any other PR's run, or any
// other workflow_run (another branch, a merge group, a fork's branch of the
// same name), gets a group of its own and can never displace a waiting
// sync-PR run.
func TestWorkerEnvSyncAutomergeConcurrency(t *testing.T) {
	root := readYAMLNode(t, filepath.Join(".github", "workflows", workerEnvAutomergeWorkflow))
	var doc struct {
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress *bool  `yaml:"cancel-in-progress"`
			Queue            string `yaml:"queue"`
		} `yaml:"concurrency"`
	}
	if err := root.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Concurrency.Group != workerEnvAutomergeGroup {
		t.Fatalf("concurrency group = %q, want %q", doc.Concurrency.Group, workerEnvAutomergeGroup)
	}
	if doc.Concurrency.CancelInProgress == nil || *doc.Concurrency.CancelInProgress || doc.Concurrency.Queue != "" {
		t.Errorf("concurrency must set cancel-in-progress: false and keep GitHub's default queue")
	}
	expr := strings.TrimPrefix(doc.Concurrency.Group, workerEnvAutomergeGroupPrefix)
	for _, c := range []struct {
		name string
		ctx  map[string]string
		want string
	}{
		{"schedule", map[string]string{"github.event_name": "schedule"}, "sync-pr"},
		{"dispatch", map[string]string{"github.event_name": "workflow_dispatch"}, "sync-pr"},
		{"the sync PR", workerEnvPRTContext("bot/worker-env-sync", "gastownhall/beads", "4321"), "sync-pr"},
		{"a fork PR from bot/worker-env-sync", workerEnvPRTContext("bot/worker-env-sync", "mallory/beads", "4322"), "4322"},
		{"another same-repo PR", workerEnvPRTContext("fix/thing", "gastownhall/beads", "77"), "77"},
		{"the sync PR's own CI", workerEnvRunContext("pull_request", "bot/worker-env-sync", "gastownhall/beads", "9001"), "sync-pr"},
		{"another branch's CI", workerEnvRunContext("pull_request", "fix/thing", "gastownhall/beads", "9002"), "9002"},
		{"a merge group's CI", workerEnvRunContext("merge_group", "gh-readonly-queue/main/pr-4321-abc", "gastownhall/beads", "9003"), "9003"},
		{"a fork's branch of the same name", workerEnvRunContext("pull_request", "bot/worker-env-sync", "mallory/beads", "9004"), "9004"},
	} {
		if got := mustEvalGHRunsOn(t, expr, c.ctx); got != c.want {
			t.Errorf("%s: concurrency group suffix = %q, want %q", c.name, got, c.want)
		}
	}
}

func workerEnvPRTContext(headRef, headRepo, number string) map[string]string {
	return map[string]string{
		"github.event_name":                             "pull_request_target",
		"github.repository":                             "gastownhall/beads",
		"github.event.pull_request.head.ref":            headRef,
		"github.event.pull_request.head.repo.full_name": headRepo,
		"github.event.pull_request.number":              number,
	}
}

func workerEnvRunContext(event, headBranch, headRepo, id string) map[string]string {
	return map[string]string{
		"github.event_name":                                   "workflow_run",
		"github.repository":                                   "gastownhall/beads",
		"github.event.workflow_run.event":                     event,
		"github.event.workflow_run.head_branch":               headBranch,
		"github.event.workflow_run.head_repository.full_name": headRepo,
		"github.event.workflow_run.id":                        id,
	}
}

// workerEnvEvalIf evaluates a job's if: with the status functions the
// evaluator lacks replaced by the answer for a run nobody cancelled.
func workerEnvEvalIf(t *testing.T, expr string, ctx map[string]string) bool {
	t.Helper()
	v, err := evalGHExpr(strings.ReplaceAll(expr, "!cancelled()", "true"), ctx)
	if err != nil {
		t.Fatalf("evalGHExpr(%q): %v", expr, err)
	}
	return ghTruthy(v)
}

// TestWorkerEnvSyncAutomergeVariableGate: nothing runs unless
// WORKER_ENV_SYNC_AUTOMERGE is 'true' (unset is today's review path, and
// the rollback), and no pull_request_target or workflow_run run but the
// sync PR's own gets past verify's if:. act and rerun need verify, so they
// are gated with it; act runs after every verification that ran (failed,
// or with its outputs lost, included: it fails closed) unless verify found
// no open sync PR. The variable is read nowhere else in the file.
func TestWorkerEnvSyncAutomergeVariableGate(t *testing.T) {
	workflow := readCIWorkflow(t, workerEnvAutomergeWorkflow)
	verify := workflow.job(t, "verify")
	if verify.If != workerEnvAutomergeVerifyIf {
		t.Fatalf("verify if = %q, want %q", verify.If, workerEnvAutomergeVerifyIf)
	}
	on := map[string]string{"github.repository": "gastownhall/beads", "vars.WORKER_ENV_SYNC_AUTOMERGE": "true"}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range on {
			m[k] = v
		}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	syncPR := workerEnvPRTContext("bot/worker-env-sync", "gastownhall/beads", "4321")
	for _, c := range []struct {
		name string
		ctx  map[string]string
		want bool
	}{
		{"schedule", with(map[string]string{"github.event_name": "schedule"}), true},
		{"dispatch", with(map[string]string{"github.event_name": "workflow_dispatch"}), true},
		{"the sync PR", with(syncPR), true},
		{"the sync PR's CI", with(workerEnvRunContext("pull_request", "bot/worker-env-sync", "gastownhall/beads", "9001")), true},
		{"variable unset", with(syncPR, "vars.WORKER_ENV_SYNC_AUTOMERGE", ""), false},
		{"variable false", with(map[string]string{"github.event_name": "schedule"}, "vars.WORKER_ENV_SYNC_AUTOMERGE", "false"), false},
		{"a fork PR from bot/worker-env-sync", with(workerEnvPRTContext("bot/worker-env-sync", "mallory/beads", "4322")), false},
		{"a same-repo PR from another branch", with(workerEnvPRTContext("bot/worker-env-sync-2", "gastownhall/beads", "4323")), false},
		{"another branch's CI", with(workerEnvRunContext("pull_request", "fix/thing", "gastownhall/beads", "9002")), false},
		{"a merge group's CI", with(workerEnvRunContext("merge_group", "bot/worker-env-sync", "gastownhall/beads", "9003")), false},
		{"a fork's CI on a branch of the same name", with(workerEnvRunContext("pull_request", "bot/worker-env-sync", "mallory/beads", "9004")), false},
		{"a fork of this repository", with(map[string]string{"github.event_name": "schedule"}, "github.repository", "mallory/beads"), false},
	} {
		if got := workerEnvEvalIf(t, verify.If, c.ctx); got != c.want {
			t.Errorf("%s: verify runs = %v, want %v", c.name, got, c.want)
		}
	}
	act, rerun := workflow.job(t, "act"), workflow.job(t, "rerun")
	if act.If != workerEnvActIf || rerun.If != workerEnvRerunIf {
		t.Fatalf("act if = %q, rerun if = %q; want %q and %q", act.If, rerun.If, workerEnvActIf, workerEnvRerunIf)
	}
	for _, job := range []ciWorkflowJob{act, rerun} {
		if !slices.Equal(job.Needs, []string{"verify"}) {
			t.Errorf("job %s needs %v, want [verify] (gated by verify's if:)", job.Name, job.Needs)
		}
	}
	for _, c := range []struct {
		name, result, verdict string
		want                  bool
	}{
		{"verify skipped (gate or filter)", "skipped", "", false},
		{"no open sync PR", "success", "none", false},
		{"verified", "success", "arm", true},
		{"refused", "success", "refuse", true},
		{"unknown", "success", "unknown", true},
		{"verify failed", "failure", "", true},
		{"outputs lost", "success", "", true},
	} {
		ctx := map[string]string{"github.event_name": "schedule", "needs.verify.result": c.result, "needs.verify.outputs.verdict": c.verdict}
		if got := workerEnvEvalIf(t, act.If, ctx); got != c.want {
			t.Errorf("act if, %s: runs = %v, want %v", c.name, got, c.want)
		}
	}
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", workerEnvAutomergeWorkflow)), "", func(path string, key bool, value string) {
		if !key && strings.Contains(value, "WORKER_ENV_SYNC_AUTOMERGE") && path != ".jobs.verify.if" {
			t.Errorf("%s reads WORKER_ENV_SYNC_AUTOMERGE; only verify's if: may", path)
		}
	})
}

// TestWorkerEnvSyncAutomergePermissions: least privilege per job. The
// workflow grants nothing; verify reads (pull requests, contents for the
// files and the activity API, checks for the required checks, actions for
// the CI runs it may re-run); act reads the PR with its own token and
// writes only through the App tokens it mints; rerun alone may re-run jobs.
// Only act holds the autofix environment (the App's key); its one job env
// value is the GraphQL read it shares across its steps.
func TestWorkerEnvSyncAutomergePermissions(t *testing.T) {
	root := readYAMLNode(t, filepath.Join(".github", "workflows", workerEnvAutomergeWorkflow))
	var top struct {
		Permissions map[string]string `yaml:"permissions"`
		Env         map[string]string `yaml:"env"`
	}
	if err := root.Decode(&top); err != nil {
		t.Fatal(err)
	}
	if top.Permissions == nil || len(top.Permissions) != 0 {
		t.Errorf("workflow permissions = %v, want {} (every job states its own)", top.Permissions)
	}
	if len(top.Env) != 0 {
		t.Errorf("workflow env = %v, want none", top.Env)
	}
	workflow := readCIWorkflow(t, workerEnvAutomergeWorkflow)
	var jobs []string
	for name := range workflow.Jobs {
		jobs = append(jobs, name)
	}
	sort.Strings(jobs)
	if want := []string{"act", "rerun", "verify"}; !slices.Equal(jobs, want) {
		t.Fatalf("jobs = %v, want %v", jobs, want)
	}
	for name, want := range map[string]struct {
		perms map[string]any
		env   string
		vars  map[string]string
	}{
		"verify": {map[string]any{"actions": "read", "checks": "read", "contents": "read", "pull-requests": "read"}, "", nil},
		"act":    {map[string]any{"pull-requests": "read"}, "autofix", map[string]string{"PR_STATE": workerEnvPRStateQuery}},
		"rerun":  {map[string]any{"actions": "write"}, "", nil},
	} {
		job := workflow.job(t, name)
		if !reflect.DeepEqual(job.Permissions, want.perms) {
			t.Errorf("job %s permissions = %v, want %v", name, job.Permissions, want.perms)
		}
		if job.Environment.Name != want.env {
			t.Errorf("job %s environment = %q, want %q", name, job.Environment.Name, want.env)
		}
		if job.RunsOn != "ubuntu-latest" {
			t.Errorf("job %s runs-on = %q, want ubuntu-latest (it holds a token; never a Blacksmith label)", name, job.RunsOn)
		}
		if job.TimeoutMinutes <= 0 || job.TimeoutMinutes > 10 {
			t.Errorf("job %s timeout-minutes = %d, want 1..10 (runs share one non-cancelling group)", name, job.TimeoutMinutes)
		}
		if len(job.Env) != len(want.vars) || (len(want.vars) > 0 && !reflect.DeepEqual(job.Env, want.vars)) {
			t.Errorf("job %s env = %v, want %v", name, job.Env, want.vars)
		}
	}
	walkYAML(root, "", func(path string, key bool, value string) {
		if !key && strings.Contains(path, ".permissions.") && value != "read" && path != ".jobs.rerun.permissions.actions" {
			t.Errorf("%s = %q; only read, and rerun's actions: write", path, value)
		}
	})
}

// TestWorkerEnvSyncAutomergeNeverRunsPRCode pins the rules for the two
// privileged triggers (pull_request_target, workflow_run). The only checkout
// is verify's, of the run's own base commit (github.sha: main), sparse,
// with no token left on disk; the head's two files reach the verifier
// through the contents API as data. No run: interpolates ${{ }} (event data
// reaches scripts through env), nothing fetches, checks out or downloads
// anything (no artifact, no other run's output), no action but the
// checkout and the two App-token mints is used, and the secrets are those
// mints' app-id and private-key, the gascity-autofix App's alone. The
// verifier itself fetches and runs nothing and writes nothing to GitHub.
func TestWorkerEnvSyncAutomergeNeverRunsPRCode(t *testing.T) {
	rel := filepath.Join(".github", "workflows", workerEnvAutomergeWorkflow)
	root := readYAMLNode(t, rel)
	workflow := readCIWorkflow(t, workerEnvAutomergeWorkflow)
	fetches := regexp.MustCompile(`\bgit\s+(fetch|clone|checkout|switch|worktree|pull|submodule)\b|\bgh\s+pr\s+checkout\b|\bgh\s+run\s+download\b|\bgh\s+release\s+download\b|download-artifact|zipball|tarball`)
	checkouts := 0
	for name, job := range workflow.Jobs {
		for i, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Errorf("job %s step %q interpolates an expression into run:\n%s", name, step.Name, step.Run)
			}
			if fetches.MatchString(step.Run) {
				t.Errorf("job %s step %q fetches, checks out or downloads code:\n%s", name, step.Name, step.Run)
			}
			if _, ok := step.With["run-id"]; ok {
				t.Errorf("job %s step %q downloads another run's artifacts", name, step.Name)
			}
			switch family := actionFamily(step.Uses); {
			case step.Uses == "":
			case family == "actions/checkout":
				checkouts++
				if name != "verify" || i != 0 || step.Uses != "actions/checkout@"+checkoutSHA {
					t.Errorf("job %s step %d checks out (%q); only verify's first step may, with actions/checkout@%s", name, i, step.Uses, checkoutSHA)
				}
				want := map[string]string{"ref": "${{ github.sha }}", "persist-credentials": "false", "sparse-checkout": "tools/rbe\n"}
				if !reflect.DeepEqual(step.With, want) {
					t.Errorf("checkout with = %q, want %q (the run's base commit, never a PR ref; no token on disk)", step.With, want)
				}
			case family == "actions/create-github-app-token":
				if name != "act" || step.Uses != "actions/create-github-app-token@"+appTokenActionSHA {
					t.Errorf("job %s step %q mints an App token with %q; only act may, with actions/create-github-app-token@%s", name, step.Name, step.Uses, appTokenActionSHA)
				}
			default:
				t.Errorf("job %s step %q uses %q; only the checkout and the App-token mint may be used", name, step.Name, step.Uses)
			}
		}
	}
	if checkouts != 1 {
		t.Errorf("%d checkouts, want exactly verify's", checkouts)
	}
	act := workflow.job(t, "act")
	allowed := map[string]bool{}
	for _, step := range []string{workerEnvMergeTokenStep, workerEnvPRTokenStep} {
		i := act.stepIndex(t, step)
		allowed[fmt.Sprintf(".jobs.act.steps[%d].with.app-id", i)] = true
		allowed[fmt.Sprintf(".jobs.act.steps[%d].with.private-key", i)] = true
	}
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	walkYAML(root, "", func(path string, key bool, value string) {
		if key && value == "continue-on-error" && path != fmt.Sprintf(".jobs.act.steps[%d].continue-on-error", act.stepIndex(t, workerEnvMergeTokenStep)) {
			t.Errorf("%s hides a failure; only the merge-token mint may (its failure is the fallback signal)", path)
		}
		if !key && value == "inherit" {
			t.Errorf("%s: secrets: inherit", path)
		}
		if key || !secretRef.MatchString(value) {
			return
		}
		if !allowed[path] {
			t.Errorf("%s reads secrets (%q); only the two mints' app-id and private-key may", path, value)
		}
		for _, ref := range regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(value, -1) {
			if ref[1] != "GASCITY_AUTOFIX_APP_ID" && ref[1] != "GASCITY_AUTOFIX_APP_PRIVATE_KEY" {
				t.Errorf("%s reads secret %s; only the gascity-autofix App's", path, ref[1])
			}
		}
	})
	src := readPolicyFile(t, bazelPolicyRoot(t), workerEnvSyncVerify)
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if fetches.MatchString(line) || regexp.MustCompile(`^\s*(source|eval|\.)\s`).MatchString(line) {
			t.Errorf("%s: %q fetches or runs code", workerEnvSyncVerify, line)
		}
		if regexp.MustCompile(`\bgh\s+(pr|issue|run|workflow|label|release)\b|\s-X\s|--method|mutation`).MatchString(line) {
			t.Errorf("%s: %q writes to GitHub; the verifier only reads", workerEnvSyncVerify, line)
		}
	}
}

// TestWorkerEnvSyncAutomergeVerifyJob: verify checks out main's tools/rbe
// and runs the verifier with the read-only token and the event's facts in
// env. A verdict (exit 1 refused, 2 unknown) never fails the step; the
// outputs carry it to act. Anything above 2 is the script failing, which
// must not pass as a verdict (act then fails closed).
func TestWorkerEnvSyncAutomergeVerifyJob(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutomergeWorkflow).job(t, "verify")
	if len(job.Steps) != 2 {
		t.Fatalf("verify has %d steps, want the checkout and the verifier", len(job.Steps))
	}
	step := job.Steps[1]
	wantEnv := map[string]string{
		"GH_TOKEN":     "${{ github.token }}",
		"REPO":         "${{ github.repository }}",
		"EVENT_NAME":   "${{ github.event_name }}",
		"EVENT_ACTION": "${{ github.event.action }}",
		"EVENT_SENDER": "${{ github.event.sender.login }}",
		"PR":           "${{ github.event.pull_request.number }}",
	}
	if step.Name != "Verify the sync PR" || step.ID != "verify" || step.If != "" || !reflect.DeepEqual(step.Env, wantEnv) ||
		!strings.Contains(step.Run, workerEnvSyncVerify+` "$PR"`) {
		t.Errorf("verify step name %q id %q if %q env %v run %q; want id verify running %s \"$PR\" with env %v", step.Name, step.ID, step.If, step.Env, step.Run, workerEnvSyncVerify, wantEnv)
	}
	wantOutputs := map[string]string{}
	for _, k := range []string{"pr", "head-sha", "verdict", "arm", "hold", "comment", "fallback-label", "rerun", "reason"} {
		wantOutputs[k] = "${{ steps.verify.outputs." + k + " }}"
	}
	if !reflect.DeepEqual(job.Outputs, wantOutputs) {
		t.Errorf("verify outputs = %v, want %v", job.Outputs, wantOutputs)
	}

	requireHostTool(t, "bash")
	dir := t.TempDir()
	for rc, want := range map[int]int{0: 0, 1: 0, 2: 0, 3: 3, 127: 127} {
		rbeTestWrite(t, filepath.Join(dir, workerEnvSyncVerify), fmt.Sprintf("#!/usr/bin/env bash\nexit %d\n", rc), 0o755)
		cmd := exec.Command("bash", "-e", "-c", step.Run)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PR=4321")
		out, err := cmd.CombinedOutput()
		if got := exitCode(err); got != want {
			t.Errorf("verify step with the verifier exiting %d: exit %d, want %d\n%s", rc, got, want, out)
		}
	}
}

// TestWorkerEnvSyncAutomergeActJob pins act: its state step (the job's
// read-only token), the gascity-autofix App's two tokens (the merge token,
// contents + pull-requests + merge-queues write, to arm and take out; the
// PR token, pull-requests write alone, to comment and to label), each used
// only by its steps, and after the mints only gh runs. The decision table
// runs every step's real if: through the expression evaluator: nothing
// arms or labels unless the state step decided to arm, the fallback label
// follows only a failed mint or a permission-denied arm, and a take-out
// comes before the comment.
func TestWorkerEnvSyncAutomergeActJob(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutomergeWorkflow).job(t, "act")
	if len(job.Steps) != len(workerEnvActSteps) {
		t.Fatalf("act has %d steps, want %d", len(job.Steps), len(workerEnvActSteps))
	}
	for i, want := range workerEnvActSteps {
		step := job.Steps[i]
		if step.Name != want.name || step.ID != want.id || step.If != want.ifExpr {
			t.Errorf("act step %d = %q id %q if %q; want %q id %q if %q", i, step.Name, step.ID, step.If, want.name, want.id, want.ifExpr)
		}
	}
	app := map[string]string{
		"app-id":       "${{ secrets.GASCITY_AUTOFIX_APP_ID }}",
		"private-key":  "${{ secrets.GASCITY_AUTOFIX_APP_PRIVATE_KEY }}",
		"owner":        "gastownhall",
		"repositories": "beads",
	}
	withPerms := func(perms ...string) map[string]string {
		m := map[string]string{}
		for k, v := range app {
			m[k] = v
		}
		for _, p := range perms {
			m["permission-"+p] = "write"
		}
		return m
	}
	merge := job.step(t, workerEnvMergeTokenStep)
	if !reflect.DeepEqual(merge.With, withPerms("contents", "pull-requests", "merge-queues")) || merge.ContinueOnError != true {
		t.Errorf("merge-token mint with %v continue-on-error %v; want %v and continue-on-error: true (a failed mint is the fallback signal)", merge.With, merge.ContinueOnError, withPerms("contents", "pull-requests", "merge-queues"))
	}
	prToken := job.step(t, workerEnvPRTokenStep)
	if !reflect.DeepEqual(prToken.With, withPerms("pull-requests")) || prToken.ContinueOnError != nil {
		t.Errorf("pr-token mint with %v continue-on-error %v; want %v", prToken.With, prToken.ContinueOnError, withPerms("pull-requests"))
	}
	const (
		repo  = "${{ github.repository }}"
		pr    = "${{ steps.state.outputs.pr }}"
		node  = "${{ steps.state.outputs.node-id }}"
		state = "${{ github.token }}"
	)
	wantEnv := map[string]map[string]string{
		workerEnvStateStep: {"GH_TOKEN": state, "REPO": repo, "VERDICT": "${{ needs.verify.outputs.verdict }}",
			"VERIFIED_HEAD": "${{ needs.verify.outputs.head-sha }}", "ARM": "${{ needs.verify.outputs.arm }}", "HOLD": "${{ needs.verify.outputs.hold }}"},
		workerEnvTakeoutStep: {"GH_TOKEN": workerEnvMergeToken, "REPO": repo, "PR": pr, "NODE_ID": node,
			"TAKEOUT": "${{ steps.state.outputs.takeout }}", "HEAD": "${{ steps.state.outputs.head }}"},
		workerEnvArmStep: {"GH_TOKEN": workerEnvMergeToken, "REPO": repo, "PR": pr, "NODE_ID": node, "HEAD": "${{ steps.state.outputs.head }}"},
		workerEnvCommentStep: {"GH_TOKEN": workerEnvPRToken, "REPO": repo, "PR": pr, "HEAD_SHA": "${{ needs.verify.outputs.head-sha }}",
			"REASON": "${{ needs.verify.outputs.reason }}", "RUN_URL": "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}"},
		workerEnvLabelStep: {"GH_TOKEN": workerEnvPRToken, "REPO": repo, "PR": pr, "LABEL": workerEnvSyncReviewLabel},
	}
	for name, env := range wantEnv {
		if got := job.step(t, name).Env; !reflect.DeepEqual(got, env) {
			t.Errorf("act step %q env = %v, want %v", name, got, env)
		}
	}
	if arm := job.step(t, workerEnvArmStep); !strings.Contains(arm.Run, `gh pr merge "$PR" --repo "$REPO" --auto --squash --match-head-commit "$HEAD"`) {
		t.Errorf("arm step run %q, want gh pr merge --auto --squash bound to the head with --match-head-commit", arm.Run)
	}
	// No step runs a script, a repository file or a network tool other
	// than gh.
	for _, step := range job.Steps {
		for _, cmd := range workerEnvShellCommands(step.Run) {
			if slices.Contains([]string{"curl", "wget", "git", "bash", "sh", "python", "python3", "node", "make", "go", "source", "eval", "."}, cmd) ||
				strings.Contains(cmd, "/") {
				t.Errorf("act step %q runs %q; act runs only gh", step.Name, cmd)
			}
		}
	}

	type outcome struct {
		arm, takeout, pr, comment, fallback, mint, denied string
	}
	run := func(o outcome) []string {
		ctx := map[string]string{
			"steps.state.outputs.arm":             o.arm,
			"steps.state.outputs.takeout":         o.takeout,
			"steps.state.outputs.pr":              o.pr,
			"needs.verify.outputs.comment":        o.comment,
			"needs.verify.outputs.fallback-label": o.fallback,
			"steps.arm.outputs.denied":            o.denied,
		}
		var ran []string
		for _, s := range workerEnvActSteps {
			ctx["steps.merge-token.outcome"] = "skipped"
			if slices.Contains(ran, workerEnvMergeTokenStep) {
				ctx["steps.merge-token.outcome"] = o.mint
			}
			if s.ifExpr == "" {
				ran = append(ran, s.name)
				continue
			}
			v, err := evalGHExpr(s.ifExpr, ctx)
			if err != nil {
				t.Fatalf("evalGHExpr(%q): %v", s.ifExpr, err)
			}
			if ghTruthy(v) {
				ran = append(ran, s.name)
			}
		}
		return ran
	}
	st := workerEnvStateStep
	for _, c := range []struct {
		name string
		o    outcome
		want []string
	}{
		{"arm due: arm", outcome{arm: "true", pr: "4321", fallback: "true", mint: "success"},
			[]string{st, workerEnvMergeTokenStep, workerEnvArmStep}},
		{"arm due, the App lacks the permissions: label for review", outcome{arm: "true", pr: "4321", fallback: "true", mint: "failure"},
			[]string{st, workerEnvMergeTokenStep, workerEnvPRTokenStep, workerEnvLabelStep}},
		{"arm due, arming denied: label for review", outcome{arm: "true", pr: "4321", fallback: "true", mint: "success", denied: "1"},
			[]string{st, workerEnvMergeTokenStep, workerEnvArmStep, workerEnvPRTokenStep, workerEnvLabelStep}},
		{"arm due, arming denied, already in review: nothing more", outcome{arm: "true", pr: "4321", fallback: "false", mint: "success", denied: "1"},
			[]string{st, workerEnvMergeTokenStep, workerEnvArmStep}},
		{"take everything out", outcome{arm: "false", takeout: "all", pr: "4321", mint: "success"},
			[]string{st, workerEnvMergeTokenStep, workerEnvTakeoutStep}},
		{"take a pending auto-merge out", outcome{arm: "false", takeout: "pending", pr: "4321", mint: "success"},
			[]string{st, workerEnvMergeTokenStep, workerEnvTakeoutStep}},
		{"take a queued PR's auto-merge out", outcome{arm: "false", takeout: "auto-merge", pr: "4321", mint: "success"},
			[]string{st, workerEnvMergeTokenStep, workerEnvTakeoutStep}},
		{"take out, no token: the take-out step fails loudly", outcome{arm: "false", takeout: "all", pr: "4321", mint: "failure"},
			[]string{st, workerEnvMergeTokenStep, workerEnvTakeoutStep}},
		{"refused, first time: comment", outcome{arm: "false", pr: "4321", comment: "true"},
			[]string{st, workerEnvPRTokenStep, workerEnvCommentStep}},
		{"refused and queued: take out, then comment", outcome{arm: "false", takeout: "all", pr: "4321", comment: "true", mint: "success"},
			[]string{st, workerEnvMergeTokenStep, workerEnvTakeoutStep, workerEnvPRTokenStep, workerEnvCommentStep}},
		{"no open sync PR: nothing", outcome{arm: "false", comment: "true"}, []string{st}},
		{"nothing to do", outcome{arm: "false", pr: "4321"}, []string{st}},
		{"the state step failed (no outputs)", outcome{}, []string{st}},
	} {
		if got := run(c.o); !slices.Equal(got, c.want) {
			t.Errorf("%s: act runs %q, want %q", c.name, got, c.want)
		}
	}
	for _, arm := range []string{"false", ""} {
		for _, mint := range []string{"success", "failure"} {
			got := run(outcome{arm: arm, takeout: "all", pr: "4321", comment: "true", fallback: "true", mint: mint, denied: "1"})
			if slices.Contains(got, workerEnvArmStep) || slices.Contains(got, workerEnvLabelStep) {
				t.Errorf("state arm %q, mint %s: act runs %q; nothing arms or labels unless the state step decided to arm", arm, mint, got)
			}
		}
	}
}

// workerEnvShellCommands lists the command word of every simple command in
// a run: script, roughly: each line and each part after ; && || | $( or a
// then/else/do keyword, past if/elif/while/!; comment lines and heredoc
// bodies (lines between <<WORD and WORD) are skipped.
func workerEnvShellCommands(script string) []string {
	var cmds []string
	split := regexp.MustCompile(`;|&&|\|\||\||\$\(|\bthen\b|\belse\b|\bdo\b`)
	heredoc := regexp.MustCompile(`<<-?'?([A-Z_]+)'?`)
	assignment := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	end := ""
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if end != "" {
			if trimmed == end {
				end = ""
			}
			continue
		}
		if m := heredoc.FindStringSubmatch(line); m != nil {
			end = m[1]
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, part := range split.Split(trimmed, -1) {
			fields := strings.Fields(part)
			for len(fields) > 0 && slices.Contains([]string{"if", "elif", "while", "!", "{", "(", "fi", "done", "esac", "}", ")"}, fields[0]) {
				fields = fields[1:]
			}
			for len(fields) > 0 && assignment.MatchString(fields[0]) {
				fields = fields[1:] // an assignment, or a prefix to the command
			}
			if len(fields) > 0 && !strings.HasSuffix(fields[0], ")") {
				cmds = append(cmds, fields[0])
			}
		}
	}
	return cmds
}

// workerEnvFakeGH stands in for gh. gh api reads are served from
// $FAKE_GH_DIR/api/<endpoint with / as __>, with --jq applied by jq (a
// missing file is gh's 404); the n-th GraphQL query is served from
// api/graphql.<n> when that file exists, else api/graphql. A GraphQL
// mutation answers {"data":{}}, or fails with the content of
// fail/mutation.<n> (the n-th mutation) or fail/mutation (every one). A
// file fail/<subcommand>_<subsubcommand> makes that invocation fail with
// its content on stderr. Every call's argv is kept, NUL-separated, one file
// per call.
const workerEnvFakeGH = `#!/usr/bin/env bash
set -u
n=$(( $(cat "$FAKE_GH_DIR/count" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$FAKE_GH_DIR/count"
printf '%s\0' "$@" > "$FAKE_GH_DIR/call.$(printf '%03d' "$n")"
if [ "${1-}" = api ]; then
  shift
  endpoint="" jq_expr="" query=""
  while [ $# -gt 0 ]; do
    case "$1" in
    --paginate) ;;
    --jq) jq_expr=$2; shift ;;
    -H|-F) shift ;;
    -f) case "$2" in query=*) query=${2#query=} ;; esac; shift ;;
    -X|--method) echo "fake gh: unexpected method $*" >&2; exit 126 ;;
    -*) echo "fake gh: unknown flag $1" >&2; exit 127 ;;
    *) if [ -z "$endpoint" ]; then endpoint=$1; fi ;;
    esac
    shift
  done
  if [ "$endpoint" = graphql ] && [[ $query == mutation* ]]; then
    m=$(( $(cat "$FAKE_GH_DIR/mutations" 2>/dev/null || echo 0) + 1 ))
    echo "$m" > "$FAKE_GH_DIR/mutations"
    for f in "$FAKE_GH_DIR/fail/mutation.$m" "$FAKE_GH_DIR/fail/mutation"; do
      if [ -f "$f" ]; then cat "$f" >&2; exit 1; fi
    done
    echo '{"data":{}}'
    exit 0
  fi
  f="$FAKE_GH_DIR/api/${endpoint//\//__}"
  if [ "$endpoint" = graphql ]; then
    k=$(( $(cat "$FAKE_GH_DIR/graphql-reads" 2>/dev/null || echo 0) + 1 ))
    echo "$k" > "$FAKE_GH_DIR/graphql-reads"
    if [ -f "$f.$k" ]; then f="$f.$k"; fi
  fi
  if [ ! -f "$f" ]; then echo "gh: Not Found (HTTP 404) $endpoint" >&2; exit 1; fi
  if [ -n "$jq_expr" ]; then jq -r "$jq_expr" "$f"; else cat "$f"; fi
  exit
fi
f="$FAKE_GH_DIR/fail/${1-}_${2-}"
if [ -f "$f" ]; then cat "$f" >&2; exit 1; fi
exit 0
`

type workerEnvFakeGHRun struct {
	code   int
	out    string
	calls  [][]string
	output string // the step's GITHUB_OUTPUT, when the caller reads one
}

// runWithFakeGH runs bash with args, the fake gh first on PATH, the files
// the test lays out in the fake's directory, and env; it returns the exit
// code, the output and every gh call.
func runWithFakeGH(t *testing.T, setup func(fakeDir string), args []string, cwd string, env ...string) workerEnvFakeGHRun {
	t.Helper()
	bash := requireHostTool(t, "bash")
	requireHostTool(t, "jq")
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	fake := filepath.Join(dir, "gh-data")
	rbeTestWrite(t, filepath.Join(bin, "gh"), workerEnvFakeGH, 0o755)
	for _, sub := range []string{"api", "fail"} {
		if err := os.MkdirAll(filepath.Join(fake, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if setup != nil {
		setup(fake)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_GH_DIR=" + fake,
		"GH_TOKEN=fake-token",
	}, env...)...)
	out, err := cmd.CombinedOutput()
	code := exitCode(err)
	if code < 0 {
		t.Fatalf("run %v: %v\n%s", args, err, out)
	}
	files, err := filepath.Glob(filepath.Join(fake, "call.*"))
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
	return workerEnvFakeGHRun{code: code, out: string(out), calls: calls}
}

func workerEnvFakeAPI(t *testing.T, fakeDir, endpoint, body string) {
	t.Helper()
	rbeTestWrite(t, filepath.Join(fakeDir, "api", strings.ReplaceAll(endpoint, "/", "__")), body, 0o644)
}

// workerEnvMutations names the GraphQL mutations among calls, in order.
func workerEnvMutations(calls [][]string) []string {
	var got []string
	for _, c := range calls {
		for _, a := range c {
			if m := regexp.MustCompile(`^query=mutation\(\$id: ID!\) \{ (\w+)\(`).FindStringSubmatch(a); m != nil {
				got = append(got, m[1])
			}
		}
	}
	return got
}

// workerEnvPRState is act's GraphQL read of the PR.
func workerEnvPRState(head string, queued, armed bool) string {
	var auto any
	if armed {
		auto = map[string]any{"enabledAt": "2026-10-24T01:05:00Z"}
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"id": "PR_kwDOQAyAEs6abc", "headRefOid": head, "isInMergeQueue": queued, "autoMergeRequest": auto}}}})
	return string(b)
}

// TestWorkerEnvSyncAutomergeActSteps runs act's real step scripts against
// the fake gh. The state step looks the PR up itself and arms only on
// verify's "arm" for the head it reads (and only if verify found the head
// green, and the PR neither queued nor armed); that verdict, or an
// explicit "unknown", for that head holds a queue entry when verify says
// hold (an "unknown" from a push the activity API does not show does
// not); an auto-merge is taken out whatever the verdict, keeping a queue
// entry the head may hold; any other verdict, a lost one, or a moved head
// takes everything out. The take-out step takes out, through the GraphQL
// mutations (gh pr merge --disable-auto leaves a queued PR as it is),
// whatever it finds, but a queue entry under "auto-merge" for the head
// the state step read: auto-merge first, so it cannot queue the PR again,
// then the queue entry, reading the state again after each round, so an
// entry a pending auto-merge queued meanwhile comes out too, and a failed
// mutation only warns; anything left after two rounds, or a state it
// cannot read, fails it. The arm step binds gh pr merge --auto to the
// head and then disables whatever auto-merge it reads, queued or not (the
// queue entry stays), the same way; a permission refusal is the fallback
// signal. The refusal comment's code block cannot be closed by the
// reasons, and it tells how to start over.
func TestWorkerEnvSyncAutomergeActSteps(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the act job runs only on ubuntu-latest; GOOS=%s", runtime.GOOS)
	}
	job := readCIWorkflow(t, workerEnvAutomergeWorkflow).job(t, "act")
	const head = "8a46b3460d09263096f6ef0061dccfa6b9d10965"
	const other = "2222222222222222222222222222222222222222"
	base := []string{"REPO=gastownhall/beads", "PR_STATE=" + job.Env["PR_STATE"], "QUEUE_WAIT=0"}
	stepRun := func(t *testing.T, name string, setup func(string), env ...string) workerEnvFakeGHRun {
		t.Helper()
		output := filepath.Join(t.TempDir(), "output")
		r := runWithFakeGH(t, setup, []string{"-e", "-c", job.step(t, name).Run}, t.TempDir(), append(append([]string{"GITHUB_OUTPUT=" + output}, base...), env...)...)
		got, _ := os.ReadFile(output)
		r.output = string(got)
		return r
	}
	openList := `[{"number":4322,"head":{"ref":"bot/worker-env-sync","repo":{"full_name":"mallory/beads"}},"base":{"repo":{"full_name":"gastownhall/beads"}}},` +
		`{"number":4321,"head":{"ref":"bot/worker-env-sync","repo":{"full_name":"gastownhall/beads"}},"base":{"repo":{"full_name":"gastownhall/beads"}}}]`
	listEndpoint := "repos/gastownhall/beads/pulls?state=open&base=main&head=gastownhall:bot/worker-env-sync&per_page=100"

	for _, c := range []struct {
		name, verdict, verified, arm, hold string
		queued, armed                      bool
		wantArm, wantTakeout               string
	}{
		{"verified and green: arm", "arm", head, "true", "true", false, false, "true", ""},
		{"verified, not green yet: wait", "arm", head, "false", "true", false, false, "false", ""},
		{"verified, queued: nothing", "arm", head, "true", "true", true, false, "false", ""},
		// GitHub keeps a pending auto-merge across a later push, green or
		// not, and one left on a queued PR could queue it again after a push
		// or an ejection: the auto-merge comes out, the verified head's
		// queue entry stays, and a later run arms afresh.
		{"verified, queued and armed: disable auto-merge, keep the queue entry", "arm", head, "true", "true", true, true, "false", "auto-merge"},
		{"verified, not green yet, pending auto-merge: disable it", "arm", head, "false", "true", false, true, "false", "pending"},
		{"verified and green, pending auto-merge: disable it", "arm", head, "true", "true", false, true, "false", "pending"},
		{"verified an older head, armed: take out", "arm", other, "true", "true", false, true, "false", "all"},
		{"verified an older head, nothing armed: nothing", "arm", other, "true", "true", false, false, "false", ""},
		{"verified, hold lost, queued: take out", "arm", head, "true", "", true, false, "false", "all"},
		{"refused, queued: take out", "refuse", head, "false", "false", true, false, "false", "all"},
		{"refused, armed: take out", "refuse", head, "false", "false", false, true, "false", "all"},
		{"refused, hold set anyway, queued: take out", "refuse", head, "false", "true", true, false, "false", "all"},
		{"unknown, queued: hold", "unknown", head, "false", "true", true, false, "false", ""},
		{"unknown, queued and armed: disable auto-merge, keep the queue entry", "unknown", head, "false", "true", true, true, "false", "auto-merge"},
		{"unknown, pending auto-merge: disable it", "unknown", head, "false", "true", false, true, "false", "pending"},
		// No hold: the activity API does not show who pushed the head,
		// which is no outage.
		{"unknown without hold, queued: take out", "unknown", head, "false", "false", true, false, "false", "all"},
		{"unknown without hold, queued and armed: take out", "unknown", head, "false", "false", true, true, "false", "all"},
		{"unknown for an older head, queued: take out", "unknown", other, "false", "true", true, false, "false", "all"},
		{"verdict lost, queued: take out", "", "", "", "", true, false, "false", "all"},
		{"verdict garbled, armed: take out", "arm::add-mask", "xyz", "true", "true::add-mask", false, true, "false", "all"},
	} {
		t.Run("state: "+c.name, func(t *testing.T) {
			r := stepRun(t, workerEnvStateStep, func(d string) {
				workerEnvFakeAPI(t, d, listEndpoint, openList)
				workerEnvFakeAPI(t, d, "graphql", workerEnvPRState(head, c.queued, c.armed))
			}, "VERDICT="+c.verdict, "VERIFIED_HEAD="+c.verified, "ARM="+c.arm, "HOLD="+c.hold)
			out := parseWorkerEnvOutput(t, r.output)
			want := map[string]string{"pr": "4321", "head": head, "node-id": "PR_kwDOQAyAEs6abc", "arm": c.wantArm, "takeout": c.wantTakeout}
			if r.code != 0 || !reflect.DeepEqual(out, want) {
				t.Errorf("state step: exit %d, outputs %v; want %v\n%s", r.code, out, want, r.out)
			}
			if strings.Contains(r.out, "add-mask") || len(workerEnvMutations(r.calls)) != 0 {
				t.Errorf("state step echoed a garbled verdict or wrote:\n%s", r.out)
			}
		})
	}
	t.Run("state: no open sync PR", func(t *testing.T) {
		r := stepRun(t, workerEnvStateStep, func(d string) { workerEnvFakeAPI(t, d, listEndpoint, "[]") }, "VERDICT=refuse")
		if r.code != 0 || r.output != "" {
			t.Errorf("state step with no open sync PR: exit %d, outputs %q; want exit 0 and none\n%s", r.code, r.output, r.out)
		}
	})
	t.Run("state: a state read that is not true or false fails the step", func(t *testing.T) {
		r := stepRun(t, workerEnvStateStep, func(d string) {
			workerEnvFakeAPI(t, d, listEndpoint, openList)
			workerEnvFakeAPI(t, d, "graphql", strings.Replace(workerEnvPRState(head, true, false), `"isInMergeQueue":true`, `"isInMergeQueue":null`, 1))
		}, "VERDICT=refuse")
		if r.code == 0 || strings.Contains(r.output, "takeout") {
			t.Errorf("state step with isInMergeQueue null: exit %d, outputs %q; want a failure\n%s", r.code, r.output, r.out)
		}
	})
	t.Run("state: an unreadable PR fails the step", func(t *testing.T) {
		r := stepRun(t, workerEnvStateStep, func(d string) { workerEnvFakeAPI(t, d, listEndpoint, openList) }, "VERDICT=refuse")
		if r.code == 0 || strings.Contains(r.output, "takeout") {
			t.Errorf("state step with the PR unreadable: exit %d, outputs %q; want a failure\n%s", r.code, r.output, r.out)
		}
	})

	// The take-out step's successive reads of the PR, the mutations (by
	// number) that fail, and the mutations the reads call for. Under "all"
	// and "pending" everything comes out: the state step's "pending" found
	// a pending auto-merge and no queue entry, so an entry here is one that
	// auto-merge made since. Under "auto-merge" a queue entry for the head
	// the state step read stays. A failed mutation only warns: the next
	// read decides.
	takeout := []string{"PR=4321", "NODE_ID=PR_kwDOQAyAEs6abc", "HEAD=" + head}
	read := func(queued, armed bool) string { return workerEnvPRState(head, queued, armed) }
	moved := func(queued, armed bool) string { return workerEnvPRState(other, queued, armed) }
	garbled := strings.Replace(read(true, false), `"isInMergeQueue":true`, `"isInMergeQueue":null`, 1)
	disable, dequeue := "disablePullRequestAutoMerge", "dequeuePullRequest"
	for _, c := range []struct {
		name, takeout string
		reads         []string
		failing       []int
		want          []string
		fails         bool
	}{
		{"queued", "all", []string{read(true, false), read(false, false)}, nil, []string{dequeue}, false},
		{"armed", "all", []string{read(false, true), read(false, false)}, nil, []string{disable}, false},
		{"queued and armed: auto-merge first, so it cannot queue it again", "all", []string{read(true, true), read(false, false)}, nil,
			[]string{disable, dequeue}, false},
		{"a pending auto-merge queued it meanwhile", "pending", []string{read(false, true), read(true, false), read(false, false)}, nil,
			[]string{disable, dequeue}, false},
		{"nothing left to take out", "all", []string{read(false, false)}, nil, nil, false},
		{"still queued after two rounds: fails", "all", []string{read(true, false), read(true, false), read(true, false)}, nil,
			[]string{dequeue, dequeue}, true},
		{"still armed after two rounds: fails", "pending", []string{read(false, true), read(false, true), read(false, true)}, nil,
			[]string{disable, disable}, true},
		{"state garbled: fails", "all", []string{garbled}, nil, nil, true},
		{"state garbled after a round: fails", "pending", []string{read(false, true), garbled}, nil, []string{disable}, true},
		{"auto-merge: queued and armed, the queue entry kept", "auto-merge", []string{read(true, true), read(true, false)}, nil,
			[]string{disable}, false},
		{"auto-merge: no longer queued", "auto-merge", []string{read(false, true), read(false, false)}, nil, []string{disable}, false},
		{"auto-merge: the head moved, so everything out", "auto-merge", []string{moved(true, true), moved(false, false)}, nil,
			[]string{disable, dequeue}, false},
		{"auto-merge: still armed after two rounds: fails", "auto-merge", []string{read(true, true), read(true, true), read(true, true)}, nil,
			[]string{disable, disable}, true},
		{"a failed disable, then queued meanwhile: round 2 takes it out", "pending",
			[]string{read(false, true), read(true, true), read(false, false)}, []int{1}, []string{disable, disable, dequeue}, false},
		{"a failed dequeue: round 2 takes it out", "all", []string{read(true, true), read(true, false), read(false, false)}, []int{2},
			[]string{disable, dequeue, dequeue}, false},
		{"every mutation failing: fails", "all", []string{read(true, false), read(true, false), read(true, false)}, []int{1, 2},
			[]string{dequeue, dequeue}, true},
	} {
		t.Run("take out: "+c.name, func(t *testing.T) {
			r := stepRun(t, workerEnvTakeoutStep, func(d string) {
				for i, s := range c.reads {
					workerEnvFakeAPI(t, d, fmt.Sprintf("graphql.%d", i+1), s)
				}
				for _, n := range c.failing {
					rbeTestWrite(t, filepath.Join(d, "fail", fmt.Sprintf("mutation.%d", n)), "gh: HTTP 502: Bad Gateway (https://api.github.com/graphql)\n", 0o644)
				}
			}, append(takeout, "TAKEOUT="+c.takeout)...)
			got := workerEnvMutations(r.calls)
			if (r.code != 0) != c.fails || !slices.Equal(got, c.want) || c.fails != strings.Contains(r.out, "::error::") {
				t.Errorf("take-out %s: exit %d, mutations %v; want %v, failing with an error: %v\n%s", c.name, r.code, got, c.want, c.fails, r.out)
			}
			if c.fails && len(c.reads) > 1 && !strings.Contains(r.out, "take it out by hand") {
				t.Errorf("take-out %s: the error does not say to take it out by hand\n%s", c.name, r.out)
			}
			if warned := strings.Contains(r.out, "::warning::"); warned != (len(c.failing) > 0) {
				t.Errorf("take-out %s: warned %v, want %v (one per failed mutation)\n%s", c.name, warned, len(c.failing) > 0, r.out)
			}
			if reads := len(r.calls) - len(got); reads != len(c.reads) {
				t.Errorf("take-out %s: read the state %d times, want %d\n%s", c.name, reads, len(c.reads), r.out)
			}
		})
	}
	t.Run("take out, the state read failing", func(t *testing.T) {
		r := stepRun(t, workerEnvTakeoutStep, nil, append(takeout, "TAKEOUT=all")...)
		if r.code == 0 || len(workerEnvMutations(r.calls)) != 0 || !strings.Contains(r.out, "::error::could not read #4321's queue and auto-merge state") {
			t.Errorf("take-out with the PR unreadable: exit %d, calls %q; want a failure with an error and no mutation\n%s", r.code, r.calls, r.out)
		}
	})
	t.Run("take out with no token", func(t *testing.T) {
		r := stepRun(t, workerEnvTakeoutStep, nil, append(takeout, "TAKEOUT=all", "GH_TOKEN=")...)
		if r.code == 0 || len(r.calls) != 0 || !strings.Contains(r.out, "::error") {
			t.Errorf("take-out with no merge token: exit %d, calls %q; want a failure with an error and no gh call\n%s", r.code, r.calls, r.out)
		}
	})

	// The arm step's reads after gh pr merge --auto (up to three polls for
	// the queue, then one after each disable), the mutations (by number)
	// that fail, and the mutations the reads call for: nothing stays
	// armed, and a queue entry stays.
	armEnv := []string{"PR=4321", "NODE_ID=PR_kwDOQAyAEs6abc", "HEAD=" + head}
	armCall := []string{"pr", "merge", "4321", "--repo", "gastownhall/beads", "--auto", "--squash", "--match-head-commit", head}
	pending := read(false, true)
	for _, c := range []struct {
		name    string
		reads   []string
		failing []int
		want    []string
		fails   bool
		says    string
	}{
		{"GitHub queues it at once", []string{read(true, false)}, nil, nil, false, "#4321 is in the merge queue at " + head},
		{"queued at the second poll", []string{pending, read(true, false)}, nil, nil, false, "#4321 is in the merge queue at " + head},
		{"queued with auto-merge still armed: disarmed, the queue entry kept", []string{read(true, true), read(true, false)}, nil,
			[]string{disable}, false, "#4321 is in the merge queue at " + head},
		{"not queued at once, so disarmed", []string{pending, pending, pending, read(false, false)}, nil,
			[]string{disable}, false, "::warning::GitHub did not queue #4321 at once"},
		{"queued by the pending auto-merge meanwhile: disarmed, the queue entry kept", []string{pending, pending, pending, read(true, false)}, nil,
			[]string{disable}, false, "#4321 is in the merge queue at " + head},
		{"a failed disable: the second disarms it", []string{pending, pending, pending, pending, read(false, false)}, []int{1},
			[]string{disable, disable}, false, "::warning::disabling auto-merge on #4321 failed"},
		{"still armed after two disables: fails", []string{pending, pending, pending, pending, pending}, nil,
			[]string{disable, disable}, true, "::error::after two disables #4321's auto-merge is still armed"},
		{"a garbled poll: fails", []string{garbled}, nil, nil, true, "::error::could not read #4321's queue and auto-merge state"},
		{"a garbled read after a disable: fails", []string{read(true, true), garbled}, nil,
			[]string{disable}, true, "::error::could not read #4321's queue and auto-merge state"},
	} {
		t.Run("arm: "+c.name, func(t *testing.T) {
			r := stepRun(t, workerEnvArmStep, func(d string) {
				for i, s := range c.reads {
					workerEnvFakeAPI(t, d, fmt.Sprintf("graphql.%d", i+1), s)
				}
				for _, n := range c.failing {
					rbeTestWrite(t, filepath.Join(d, "fail", fmt.Sprintf("mutation.%d", n)), "gh: HTTP 502: Bad Gateway (https://api.github.com/graphql)\n", 0o644)
				}
			}, armEnv...)
			got := workerEnvMutations(r.calls)
			if (r.code != 0) != c.fails || len(r.calls) == 0 || !reflect.DeepEqual(r.calls[0], armCall) || !slices.Equal(got, c.want) || r.output != "" ||
				!strings.Contains(r.out, c.says) {
				t.Errorf("arm %s: exit %d, calls %q, mutations %v, output %q; want %q first, mutations %v, failing: %v, saying %q\n%s",
					c.name, r.code, r.calls, got, r.output, armCall, c.want, c.fails, c.says, r.out)
			}
			if reads := len(r.calls) - 1 - len(got); reads != len(c.reads) {
				t.Errorf("arm %s: read the state %d times, want %d\n%s", c.name, reads, len(c.reads), r.out)
			}
		})
	}
	t.Run("arm refused for lack of permission", func(t *testing.T) {
		r := stepRun(t, workerEnvArmStep, func(d string) {
			rbeTestWrite(t, filepath.Join(d, "fail", "pr_merge"), "GraphQL: Resource not accessible by integration (enablePullRequestAutoMerge)\n", 0o644)
		}, armEnv...)
		if r.code != 0 || r.output != "denied=1\n" || !strings.Contains(r.out, "::warning") {
			t.Errorf("denied arm: exit %d, output %q; want exit 0, denied=1 and a warning\n%s", r.code, r.output, r.out)
		}
	})
	t.Run("arm failing otherwise", func(t *testing.T) {
		r := stepRun(t, workerEnvArmStep, func(d string) {
			rbeTestWrite(t, filepath.Join(d, "fail", "pr_merge"), "GraphQL: Head sha didn't match expected head sha (enablePullRequestAutoMerge)\n", 0o644)
		}, armEnv...)
		if r.code == 0 || r.output != "" {
			t.Errorf("failed arm: exit %d, output %q; want a failure and no denied\n%s", r.code, r.output, r.out)
		}
	})

	t.Run("comment", func(t *testing.T) {
		reason := "author: opened by 'mallory', not gascity-autofix[bot]\nfiles: added \"x```y\"; want exactly two"
		r := stepRun(t, workerEnvCommentStep, nil, "PR=4321", "HEAD_SHA="+head, "REASON="+reason, "RUN_URL=https://github.com/gastownhall/beads/actions/runs/7")
		if r.code != 0 || len(r.calls) != 1 || !slices.Equal(r.calls[0][:5], []string{"pr", "comment", "4321", "--repo", "gastownhall/beads"}) {
			t.Fatalf("comment: exit %d, calls %q\n%s", r.code, r.calls, r.out)
		}
		body := r.calls[0][len(r.calls[0])-1]
		for _, want := range []string{"<!-- worker-env-sync-automerge refused " + head + " -->", "````text\n" + reason + "\n````\n",
			"https://github.com/gastownhall/beads/actions/runs/7", "delete the bot/worker-env-sync branch", workerEnvSyncReviewLabel} {
			if !strings.Contains(body, want) {
				t.Errorf("comment body lacks %q:\n%s", want, body)
			}
		}
	})

	t.Run("label", func(t *testing.T) {
		r := stepRun(t, workerEnvLabelStep, nil, "PR=4321", "LABEL="+workerEnvSyncReviewLabel)
		want := [][]string{{"pr", "edit", "4321", "--repo", "gastownhall/beads", "--add-label", workerEnvSyncReviewLabel}}
		if r.code != 0 || !reflect.DeepEqual(r.calls, want) {
			t.Errorf("label: exit %d, calls %q, want %q\n%s", r.code, r.calls, want, r.out)
		}
	})
}

// TestWorkerEnvSyncAutomergeRerunJob: rerun re-runs the failed jobs of the
// runs verify named, with the job's own token (actions: write, nothing
// else), and refuses a list that is not run ids before re-running any.
func TestWorkerEnvSyncAutomergeRerunJob(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutomergeWorkflow).job(t, "rerun")
	if len(job.Steps) != 1 {
		t.Fatalf("rerun has %d steps; want one", len(job.Steps))
	}
	step := job.Steps[0]
	wantEnv := map[string]string{"GH_TOKEN": "${{ github.token }}", "REPO": "${{ github.repository }}", "RUNS": "${{ needs.verify.outputs.rerun }}"}
	if !reflect.DeepEqual(step.Env, wantEnv) || step.Uses != "" {
		t.Errorf("rerun step env %v uses %q; want env %v and plain gh", step.Env, step.Uses, wantEnv)
	}
	if runtime.GOOS != "linux" {
		t.Skipf("the rerun job runs only on ubuntu-latest; GOOS=%s", runtime.GOOS)
	}
	r := runWithFakeGH(t, nil, []string{"-e", "-c", step.Run}, t.TempDir(), "REPO=gastownhall/beads", "RUNS=501 601")
	want := [][]string{
		{"run", "rerun", "501", "--failed", "--repo", "gastownhall/beads"},
		{"run", "rerun", "601", "--failed", "--repo", "gastownhall/beads"},
	}
	if r.code != 0 || !reflect.DeepEqual(r.calls, want) {
		t.Errorf("rerun: exit %d, calls %q, want %q\n%s", r.code, r.calls, want, r.out)
	}
	r = runWithFakeGH(t, nil, []string{"-e", "-c", step.Run}, t.TempDir(), "REPO=gastownhall/beads", "RUNS=501 $(id)")
	if r.code == 0 || len(r.calls) != 0 {
		t.Errorf("rerun of a non-numeric id: exit %d, calls %q; want a failure before any re-run\n%s", r.code, r.calls, r.out)
	}
}

// TestWorkerEnvSyncVerifyReviewLabelsMatchAutosync: the verifier's
// review-pipeline labels (a PR carrying one is in the review already, so
// the fallback never labels it) are worker-env-autosync.yml's, which
// decide whether it warns about an unlabeled sync PR: one list, two copies.
// Its required checks are the gate job names main's ruleset requires.
func TestWorkerEnvSyncVerifyReviewLabelsMatchAutosync(t *testing.T) {
	pr := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob).step(t, "Open or update the sync PR")
	m := regexp.MustCompile(`select\(IN\(([^)]*)\)\)`).FindStringSubmatch(pr.Run)
	if m == nil {
		t.Fatalf("autosync PR step: no select(IN(...)) label list in %q", pr.Run)
	}
	var autosync []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		autosync = append(autosync, q[1])
	}
	src := readPolicyFile(t, bazelPolicyRoot(t), workerEnvSyncVerify)
	v := regexp.MustCompile(`(?m)^readonly review_labels='(\[[^']*\])'$`).FindStringSubmatch(src)
	if v == nil {
		t.Fatalf("%s: no readonly review_labels='[...]' line", workerEnvSyncVerify)
	}
	var verify []string
	if err := json.Unmarshal([]byte(v[1]), &verify); err != nil {
		t.Fatal(err)
	}
	sort.Strings(autosync)
	sort.Strings(verify)
	if !slices.Equal(autosync, verify) || len(verify) == 0 || !slices.Contains(verify, workerEnvSyncReviewLabel) {
		t.Errorf("review-pipeline labels: verifier %v, autosync %v; want the same non-empty list, holding %s", verify, autosync, workerEnvSyncReviewLabel)
	}
	checks := regexp.MustCompile(`(?m)^readonly -a required_checks=\("([^"]+)" "([^"]+)"\)$`).FindStringSubmatch(src)
	if checks == nil || checks[1] != readCIWorkflow(t, "pr.yml").job(t, "ci-gate").Name || checks[2] != readCIWorkflow(t, "pr-risk.yml").job(t, "ci-gate").Name {
		t.Errorf("%s required_checks = %q; want pr.yml's and pr-risk.yml's ci-gate job names, the contexts main's ruleset requires", workerEnvSyncVerify, checks)
	}
}

// workerEnvVerifyCase is one PR as GitHub's API shows it to the verifier.
type workerEnvVerifyCase struct {
	number                             int
	author, headRepo, baseRepo         string
	headRef, baseRef, state, headSHA   string
	draft, merged, autoMerge, queued   bool
	labels                             []string
	commits                            [][3]string // sha, author login, committer login ("" = no GitHub user)
	activity                           [][3]string // activity_type, after, actor
	files                              [][2]string // status, filename
	headManifest, headBuild, mainBuild string
	comments                           [][2]string            // user, body
	labeled                            []string               // labels the PR's events add, in order
	runs                               map[string][][4]string // workflow file: id, status, conclusion, run_attempt (newest first)
	checks                             map[string]string      // required check: its latest run's "status conclusion"; absent: no run
	queuedAt                           []string               // when the PR was added to the merge queue
	openList                           []map[string]any
	hidePR                             bool
}

const (
	workerEnvTestHead     = "1111111111111111111111111111111111111111"
	workerEnvTestPushedAt = "2026-10-24T01:00:00Z"
)

// newWorkerEnvVerifyCase: what worker-env-autosync.yml pushes when gascity
// pins manifest at pin, over a main that pins rbeTestStaleManifest, with
// both required checks passed on the head.
func newWorkerEnvVerifyCase(manifest, pin string) workerEnvVerifyCase {
	stalePin := rbeTestPin(rbeTestStaleManifest)
	mainBuild := "# rbe_worker: the execution platform of every build.\n" + rbeTestPlatformBuild(stalePin) + "# --- end ---\n"
	return workerEnvVerifyCase{
		number: 4321, author: workerEnvSyncPRBot, headRepo: "gastownhall/beads", baseRepo: "gastownhall/beads",
		headRef: "bot/worker-env-sync", baseRef: "main", state: "open", headSHA: workerEnvTestHead,
		labels:       []string{"status/needs-triage"},
		commits:      [][3]string{{workerEnvTestHead, workerEnvSyncPushBot, workerEnvSyncPushBot}},
		activity:     [][3]string{{"force_push", workerEnvTestHead, workerEnvSyncPushBot}, {"branch_creation", "2222222222222222222222222222222222222222", workerEnvSyncPushBot}},
		files:        [][2]string{{"modified", "platforms/BUILD.bazel"}, {"modified", "tools/rbe/worker-env.txt"}},
		headManifest: manifest, headBuild: strings.Replace(mainBuild, stalePin, pin, 1), mainBuild: mainBuild,
		labeled: []string{"status/needs-triage"},
		runs: map[string][][4]string{
			"pr.yml":      {{"501", "completed", "success", "1"}},
			"pr-risk.yml": {{"601", "completed", "success", "1"}},
		},
		checks: map[string]string{"CI Gate / Required": "completed success", "PR Risk Gate / Required": "completed success"},
	}
}

func (c workerEnvVerifyCase) serve(t *testing.T, dir string) {
	t.Helper()
	repo := "gastownhall/beads"
	js := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	pr := fmt.Sprint(c.number)
	var head map[string]any
	if c.headRepo != "" {
		head = map[string]any{"full_name": c.headRepo}
	}
	var labels []map[string]any
	for _, l := range c.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	if !c.hidePR {
		workerEnvFakeAPI(t, dir, "repos/"+repo+"/pulls/"+pr, js(map[string]any{
			"number": c.number, "node_id": "PR_kwDOQAyAEs6abc", "state": c.state, "merged": c.merged, "draft": c.draft,
			"user": map[string]any{"login": c.author}, "commits": len(c.commits), "labels": labels,
			"head": map[string]any{"ref": c.headRef, "sha": c.headSHA, "repo": head},
			"base": map[string]any{"ref": c.baseRef, "repo": map[string]any{"full_name": c.baseRepo}},
		}))
	}
	user := func(login string) any {
		if login == "" {
			return nil
		}
		return map[string]any{"login": login}
	}
	commits := []map[string]any{}
	for _, cm := range c.commits {
		commits = append(commits, map[string]any{"sha": cm[0], "author": user(cm[1]), "committer": user(cm[2])})
	}
	files := []map[string]any{}
	for _, f := range c.files {
		files = append(files, map[string]any{"status": f[0], "filename": f[1]})
	}
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/compare/main..."+c.headSHA+"?per_page=100",
		js(map[string]any{"status": "ahead", "total_commits": len(commits), "commits": commits, "files": files}))
	var activity []map[string]any
	for _, a := range c.activity {
		activity = append(activity, map[string]any{"activity_type": a[0], "after": a[1], "ref": "refs/heads/bot/worker-env-sync",
			"actor": map[string]any{"login": a[2]}, "timestamp": workerEnvTestPushedAt})
	}
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/activity?ref=refs/heads/bot/worker-env-sync&per_page=100", js(activity))
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/contents/tools/rbe/worker-env.txt?ref="+c.headSHA, c.headManifest)
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/contents/platforms/BUILD.bazel?ref="+c.headSHA, c.headBuild)
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/contents/platforms/BUILD.bazel?ref=main", c.mainBuild)
	var auto any
	if c.autoMerge {
		auto = map[string]any{"enabledAt": "2026-10-24T01:05:00Z"}
	}
	nodes := []map[string]any{}
	for _, at := range c.queuedAt {
		nodes = append(nodes, map[string]any{"createdAt": at})
	}
	workerEnvFakeAPI(t, dir, "graphql", js(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"isInMergeQueue": c.queued, "autoMergeRequest": auto, "timelineItems": map[string]any{"nodes": nodes}}}}}))
	for _, name := range []string{"CI Gate / Required", "PR Risk Gate / Required"} {
		runs := []map[string]any{}
		if got, ok := c.checks[name]; ok {
			status, conclusion, _ := strings.Cut(got, " ")
			var concl any = conclusion
			if conclusion == "" {
				concl = nil
			}
			runs = append(runs, map[string]any{"id": 900, "name": name, "status": status, "conclusion": concl},
				map[string]any{"id": 800, "name": name, "status": "completed", "conclusion": "failure"})
		}
		enc := strings.NewReplacer(" ", "%20", "/", "%2F").Replace(name)
		workerEnvFakeAPI(t, dir, "repos/"+repo+"/commits/"+c.headSHA+"/check-runs?check_name="+enc+"&filter=latest&app_id=15368&per_page=100",
			js(map[string]any{"total_count": len(runs), "check_runs": runs}))
	}
	var comments []map[string]any
	for _, cm := range c.comments {
		comments = append(comments, map[string]any{"user": map[string]any{"login": cm[0]}, "body": cm[1]})
	}
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/issues/"+pr+"/comments?per_page=100", js(comments))
	var events []map[string]any
	for _, l := range c.labeled {
		events = append(events, map[string]any{"event": "labeled", "label": map[string]any{"name": l}})
	}
	workerEnvFakeAPI(t, dir, "repos/"+repo+"/issues/"+pr+"/events?per_page=100", js(events))
	for wf, runs := range c.runs {
		var list []map[string]any
		for _, r := range runs {
			var conclusion any = r[2]
			if r[2] == "" {
				conclusion = nil
			}
			var id, attempt int
			fmt.Sscan(r[0], &id)
			fmt.Sscan(r[3], &attempt)
			list = append(list, map[string]any{"id": id, "status": r[1], "conclusion": conclusion, "run_attempt": attempt, "head_sha": c.headSHA, "event": "pull_request"})
		}
		workerEnvFakeAPI(t, dir, "repos/"+repo+"/actions/workflows/"+wf+"/runs?event=pull_request&head_sha="+c.headSHA+"&per_page=100",
			js(map[string]any{"total_count": len(list), "workflow_runs": list}))
	}
	if c.openList != nil {
		workerEnvFakeAPI(t, dir, "repos/"+repo+"/pulls?state=open&base=main&head=gastownhall:bot/worker-env-sync&per_page=100", js(c.openList))
	}
}

// parseWorkerEnvOutput reads a $GITHUB_OUTPUT file: name=value lines and
// name<<DELIMITER heredocs.
func parseWorkerEnvOutput(t *testing.T, text string) map[string]string {
	t.Helper()
	out := map[string]string{}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if name, delim, ok := strings.Cut(line, "<<"); ok && !strings.Contains(name, "=") {
			var body []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				body = append(body, lines[i])
			}
			if i == len(lines) {
				t.Fatalf("GITHUB_OUTPUT heredoc %s has no closing %s:\n%s", name, delim, text)
			}
			out[name] = strings.Join(body, "\n")
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("GITHUB_OUTPUT line %q is neither name=value nor a heredoc", line)
		}
		out[name] = value
	}
	return out
}

// workerEnvCommandLines: the lines of a step's output the runner would
// take for workflow commands (::name:: at a line's start, past blanks; the
// legacy ##[name] anywhere in a line).
func workerEnvCommandLines(out string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "::") || strings.Contains(line, "##[") {
			got = append(got, line)
		}
	}
	return got
}

// workerEnvRunnerMessage: a workflow command's message as the runner reads
// it, decoding %0D, %0A and %25, in that order (actions/runner's
// ActionCommand.UnescapeData).
func workerEnvRunnerMessage(data string) string {
	for _, m := range [][2]string{{"%0D", "\r"}, {"%0A", "\n"}, {"%25", "%"}} {
		data = strings.ReplaceAll(data, m[0], m[1])
	}
	return data
}

// TestWorkerEnvSyncVerify runs tools/rbe/worker-env-sync-verify from a
// scratch copy of the trusted scripts against the fake gh's API (the
// head's files served by the contents API, commits and files by compare at
// the head) and the shared gascity fixture (rbe_worker_env_test.go, main
// resolved with git ls-remote). A good sync PR whose required checks
// passed is armed; one still waiting on them is not; each departure from
// what worker-env-autosync.yml pushes is refused with its own reason; what
// cannot be decided is unknown, which never arms. Its only workflow
// commands are its own annotations, however the PR's files and file names
// are spelled, and each message stays one line once the runner decodes
// it. It checks the decisions the act and rerun jobs carry out, and that
// every gh call it makes is a read.
func TestWorkerEnvSyncVerify(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the verifier runs only on worker-env-sync-automerge.yml's ubuntu-latest runner; GOOS=%s", runtime.GOOS)
	}
	requireHostTool(t, "jq")
	requireHostTool(t, "sha256sum")
	f := newRBEWorkerEnvFixture(t)
	trusted := t.TempDir()
	for _, script := range []string{rbeWorkerEnvSync, rbeWorkerEnvSyncRef, workerEnvSyncVerify} {
		rbeTestWrite(t, filepath.Join(trusted, script), readPolicyFile(t, bazelPolicyRoot(t), script), 0o755)
	}
	// A pin gascity's own BUILD carries without it hashing gascity's
	// manifest: the head copies both, so only the sha256 check can see it.
	bogusPin := rbeTestPin("not the manifest\n")
	inconsistent := newRBEWorkerEnvGascity(t, f.manifest, rbeTestPlatformBuild(bogusPin))
	marker := func(sha string) string { return "<!-- worker-env-sync-automerge refused " + sha + " -->\nrefused" }
	const before, after = "2026-10-24T00:30:00Z", "2026-10-24T02:00:00Z"
	// Workflow commands, both spellings, in the PR's BUILD file, manifest and
	// file names (one of them a line break only once the runner decodes it).
	const inject = "##[add-mask]true\n::add-mask::true\n"

	type want struct {
		code    int
		verdict string
		fails   []string // check names that must FAIL
		unknown []string // check names that must be UNKNOWN
		outputs map[string]string
	}
	for _, c := range []struct {
		name    string
		mutate  func(*workerEnvVerifyCase)
		gascity *rbeWorkerEnvFixture
		env     []string
		noArg   bool
		want    want
		says    string // a line of the dry run's report
	}{
		{name: "good sync PR, CI green", want: want{0, "arm", nil, nil, map[string]string{
			"pr": "4321", "head-sha": workerEnvTestHead, "arm": "true", "hold": "true", "comment": "false", "fallback-label": "true", "rerun": "", "reason": ""}}},
		{name: "good sync PR on a synchronize the bot pushed", env: []string{"EVENT_ACTION=synchronize", "EVENT_SENDER=" + workerEnvSyncPushBot},
			want: want{0, "arm", nil, nil, map[string]string{"arm": "true"}}},
		{name: "CI still running", mutate: func(c *workerEnvVerifyCase) { c.checks["PR Risk Gate / Required"] = "in_progress" },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false", "fallback-label": "false"}}},
		{name: "a required check failed", mutate: func(c *workerEnvVerifyCase) { c.checks["CI Gate / Required"] = "completed failure" },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false"}}},
		{name: "a required check not started", mutate: func(c *workerEnvVerifyCase) { delete(c.checks, "CI Gate / Required") },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false"}}},
		{name: "queued three times at this head", mutate: func(c *workerEnvVerifyCase) { c.queuedAt = []string{after, after, after} },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false"}}},
		{name: "queued three times, twice before this head", mutate: func(c *workerEnvVerifyCase) { c.queuedAt = []string{before, before, after} },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "true"}}},
		{name: "already armed", mutate: func(c *workerEnvVerifyCase) { c.autoMerge = true },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false", "hold": "true", "fallback-label": "false"}},
			says: "the guard would: disable the pending auto-merge on #4321"},
		{name: "already queued", mutate: func(c *workerEnvVerifyCase) { c.queued = true },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false", "hold": "true"}}},
		{name: "already queued and armed", mutate: func(c *workerEnvVerifyCase) { c.queued, c.autoMerge = true, true },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "false", "hold": "true"}},
			says: "the guard would: disable auto-merge on #4321, keeping it in the merge queue"},
		{name: "wrong author", mutate: func(c *workerEnvVerifyCase) { c.author = "mallory" },
			want: want{1, "refuse", []string{"author"}, nil, map[string]string{"arm": "false", "hold": "false", "comment": "true"}}},
		{name: "wrong author, queued", mutate: func(c *workerEnvVerifyCase) { c.author, c.queued = "mallory", true },
			want: want{1, "refuse", []string{"author"}, nil, map[string]string{"arm": "false", "hold": "false"}},
			says: "the guard would: take #4321 out of the merge queue"},
		{name: "wrong branch", mutate: func(c *workerEnvVerifyCase) { c.headRef = "bot/worker-env-sync-2" },
			want: want{1, "refuse", []string{"head-ref"}, nil, map[string]string{"arm": "false"}}},
		{name: "fork", mutate: func(c *workerEnvVerifyCase) { c.headRepo = "mallory/beads" },
			want: want{1, "refuse", []string{"head-repo"}, nil, map[string]string{"arm": "false"}}},
		{name: "deleted head repository", mutate: func(c *workerEnvVerifyCase) { c.headRepo = "" },
			want: want{1, "refuse", []string{"head-repo"}, nil, map[string]string{"arm": "false"}}},
		{name: "base not main", mutate: func(c *workerEnvVerifyCase) { c.baseRef = "release/v1.3" },
			want: want{1, "refuse", []string{"base-ref"}, nil, map[string]string{"arm": "false"}}},
		{name: "draft", mutate: func(c *workerEnvVerifyCase) { c.draft = true },
			want: want{1, "refuse", []string{"draft"}, nil, map[string]string{"arm": "false"}}},
		{name: "closed", mutate: func(c *workerEnvVerifyCase) { c.state, c.merged = "closed", true },
			want: want{1, "refuse", []string{"open"}, nil, map[string]string{"arm": "false", "comment": "false"}}},
		{name: "synchronize sent by someone else", env: []string{"EVENT_ACTION=synchronize", "EVENT_SENDER=mallory"},
			want: want{1, "refuse", []string{"sender"}, nil, map[string]string{"arm": "false"}}},
		{name: "commit by another author", mutate: func(c *workerEnvVerifyCase) { c.commits[0][1] = "mallory" },
			want: want{1, "refuse", []string{"commits"}, nil, map[string]string{"arm": "false"}}},
		{name: "commit by another committer", mutate: func(c *workerEnvVerifyCase) { c.commits[0][2] = "web-flow" },
			want: want{1, "refuse", []string{"commits"}, nil, map[string]string{"arm": "false"}}},
		{name: "commit no GitHub user wrote", mutate: func(c *workerEnvVerifyCase) {
			c.commits = append(c.commits, [3]string{"3333333333333333333333333333333333333333", "", workerEnvSyncPushBot})
		}, want: want{1, "refuse", []string{"commits"}, nil, map[string]string{"arm": "false"}}},
		{name: "push by another actor", mutate: func(c *workerEnvVerifyCase) { c.activity[0][2] = "mallory" },
			want: want{1, "refuse", []string{"pusher"}, nil, map[string]string{"arm": "false"}}},
		{name: "extra file", mutate: func(c *workerEnvVerifyCase) { c.files = append(c.files, [2]string{"added", "README.md"}) },
			want: want{1, "refuse", []string{"files"}, nil, map[string]string{"arm": "false"}}},
		{name: "manifest only", mutate: func(c *workerEnvVerifyCase) { c.files = c.files[1:] },
			want: want{1, "refuse", []string{"files"}, nil, map[string]string{"arm": "false"}}},
		{name: "edited non-pin BUILD line", mutate: func(c *workerEnvVerifyCase) {
			c.headBuild = strings.Replace(c.headBuild, "# --- end ---", "# --- edited ---", 1)
		}, want: want{1, "refuse", []string{"build-file"}, nil, map[string]string{"arm": "false"}}},
		{name: "two pin lines", mutate: func(c *workerEnvVerifyCase) {
			c.headBuild = strings.Replace(c.headBuild, "    },\n", "        \"worker-env\": \""+f.pin+"\",\n    },\n", 1)
		}, want: want{1, "refuse", []string{"build-file", "gascity"}, nil, map[string]string{"arm": "false"}}},
		{name: "manifest differs from gascity's", mutate: func(c *workerEnvVerifyCase) {
			c.headManifest = "arch x86_64\npkg git 3\n"
			c.headBuild = strings.Replace(c.headBuild, f.pin, rbeTestPin(c.headManifest), 1)
		}, want: want{1, "refuse", []string{"gascity"}, nil, map[string]string{"arm": "false"}}},
		{name: "pin is not the manifest's sha256", gascity: &inconsistent, mutate: func(c *workerEnvVerifyCase) {
			c.headBuild = strings.Replace(c.headBuild, f.pin, bogusPin, 1)
		}, want: want{1, "refuse", []string{"pin"}, nil, map[string]string{"arm": "false"}}},
		{name: "workflow commands in the PR's files and file names", mutate: func(c *workerEnvVerifyCase) {
			c.headBuild += "# " + strings.ReplaceAll(inject, "\n", "\n# ")
			c.headManifest += inject
			c.files = append(c.files, [2]string{"added", "x##[add-mask]true"}, [2]string{"added", "::add-mask::true"},
				[2]string{"added", "%0A%3A%3Aadd-mask%3A%3Atrue"})
		}, want: want{1, "refuse", []string{"files", "build-file", "pin", "gascity"}, nil, map[string]string{"arm": "false", "comment": "true"}}},
		{name: "refusal already commented on this head", mutate: func(c *workerEnvVerifyCase) {
			c.author = "mallory"
			c.comments = [][2]string{{workerEnvSyncPRBot, marker(workerEnvTestHead)}}
		}, want: want{1, "refuse", []string{"author"}, nil, map[string]string{"comment": "false"}}},
		{name: "refusal commented on an earlier head", mutate: func(c *workerEnvVerifyCase) {
			c.author = "mallory"
			c.comments = [][2]string{{workerEnvSyncPRBot, marker("2222222222222222222222222222222222222222")}}
		}, want: want{1, "refuse", []string{"author"}, nil, map[string]string{"comment": "true"}}},
		{name: "marker someone else posted", mutate: func(c *workerEnvVerifyCase) {
			c.author = "mallory"
			c.comments = [][2]string{{"mallory", marker(workerEnvTestHead)}}
		}, want: want{1, "refuse", []string{"author"}, nil, map[string]string{"comment": "true"}}},
		// Who pushed the head is undecided, but no outage: no hold, so a
		// queue entry comes out.
		{name: "activity not yet showing the head's push", mutate: func(c *workerEnvVerifyCase) {
			c.activity[0][1] = "2222222222222222222222222222222222222222"
		}, want: want{2, "unknown", nil, []string{"pusher"}, map[string]string{"arm": "false", "hold": "false", "comment": "false"}}},
		{name: "activity not yet showing the head's push, queued and armed", mutate: func(c *workerEnvVerifyCase) {
			c.activity[0][1] = "2222222222222222222222222222222222222222"
			c.queued, c.autoMerge = true, true
		}, want: want{2, "unknown", nil, []string{"pusher"}, map[string]string{"arm": "false", "hold": "false"}},
			says: "the guard would: take #4321 out of the merge queue"},
		{name: "gascity unreachable", env: []string{"WORKER_ENV_SYNC_GIT_URL=file:///nonexistent-gascity"},
			want: want{2, "unknown", nil, []string{"gascity"}, map[string]string{"arm": "false", "hold": "true"}}},
		{name: "gascity unreachable, queued", env: []string{"WORKER_ENV_SYNC_GIT_URL=file:///nonexistent-gascity"},
			mutate: func(c *workerEnvVerifyCase) { c.queued = true },
			want:   want{2, "unknown", nil, []string{"gascity"}, map[string]string{"arm": "false", "hold": "true"}},
			says:   "the guard would: hold #4321 in the merge queue (unknown is transient; a refusal or a new head takes it out)"},
		{name: "PR unreadable", mutate: func(c *workerEnvVerifyCase) { c.hidePR = true },
			want: want{2, "unknown", nil, []string{"pr"}, map[string]string{"arm": "false", "hold": "false"}}},
		{name: "fallback: already in the review pipeline", mutate: func(c *workerEnvVerifyCase) { c.labels = append(c.labels, "status/reviewing") },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "true", "fallback-label": "false"}}},
		{name: "fallback: labeled for review once before", mutate: func(c *workerEnvVerifyCase) { c.labeled = append(c.labeled, workerEnvSyncReviewLabel) },
			want: want{0, "arm", nil, nil, map[string]string{"arm": "true", "fallback-label": "false"}}},
		{name: "rerun: the failed run's own completion", env: []string{"EVENT_NAME=workflow_run", "EVENT_ACTION=completed"}, mutate: func(c *workerEnvVerifyCase) {
			c.runs["pr.yml"] = [][4]string{{"501", "completed", "failure", "1"}}
			c.checks["CI Gate / Required"] = "completed failure"
		}, want: want{0, "arm", nil, nil, map[string]string{"rerun": "501", "arm": "false"}}},
		{name: "rerun: schedule, cancelled and timed out", env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.runs["pr.yml"] = [][4]string{{"501", "completed", "cancelled", "1"}}
			c.runs["pr-risk.yml"] = [][4]string{{"601", "completed", "timed_out", "1"}}
		}, want: want{0, "arm", nil, nil, map[string]string{"rerun": "501 601"}}},
		{name: "rerun: never a second attempt", env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.runs["pr.yml"] = [][4]string{{"501", "completed", "failure", "2"}}
		}, want: want{0, "arm", nil, nil, map[string]string{"rerun": ""}}},
		{name: "rerun: an earlier run of the head was re-run", env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.runs["pr.yml"] = [][4]string{{"502", "completed", "failure", "1"}, {"501", "completed", "failure", "2"}}
		}, want: want{0, "arm", nil, nil, map[string]string{"rerun": ""}}},
		{name: "rerun: still running", env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.runs["pr.yml"] = [][4]string{{"501", "in_progress", "", "1"}}
		}, want: want{0, "arm", nil, nil, map[string]string{"rerun": ""}}},
		{name: "rerun: not on a pull_request_target event", mutate: func(c *workerEnvVerifyCase) {
			c.runs["pr.yml"] = [][4]string{{"501", "completed", "failure", "1"}}
		}, want: want{0, "arm", nil, nil, map[string]string{"rerun": ""}}},
		{name: "rerun: never for a refused PR", env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.author = "mallory"
			c.runs["pr.yml"] = [][4]string{{"501", "completed", "failure", "1"}}
		}, want: want{1, "refuse", []string{"author"}, nil, map[string]string{"rerun": ""}}},
		{name: "schedule finds the open sync PR", noArg: true, env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.openList = []map[string]any{
				{"number": 4322, "head": map[string]any{"ref": "bot/worker-env-sync", "repo": map[string]any{"full_name": "mallory/beads"}}},
				{"number": 4321, "head": map[string]any{"ref": "bot/worker-env-sync", "repo": map[string]any{"full_name": "gastownhall/beads"}}},
			}
		}, want: want{0, "arm", nil, nil, map[string]string{"pr": "4321", "arm": "true"}}},
		{name: "schedule with no open sync PR", noArg: true, env: []string{"EVENT_NAME=schedule"}, mutate: func(c *workerEnvVerifyCase) {
			c.openList = []map[string]any{}
		}, want: want{0, "none", nil, nil, map[string]string{"arm": "false", "comment": "false", "rerun": ""}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := f
			if c.gascity != nil {
				g = *c.gascity
			}
			pr := newWorkerEnvVerifyCase(f.manifest, f.pin)
			if c.mutate != nil {
				pr.mutate(c.mutate)
			}
			outputFile := filepath.Join(t.TempDir(), "output")
			env := append(g.env(), "REPO=gastownhall/beads", "EVENT_NAME=pull_request_target", "EVENT_ACTION=opened", "EVENT_SENDER="+workerEnvSyncPRBot,
				"GITHUB_OUTPUT="+outputFile, "GITHUB_STEP_SUMMARY="+filepath.Join(t.TempDir(), "summary"), "WORKER_ENV_SYNC_VERIFY_RETRY_DELAY=0")
			env = append(env, c.env...)
			args := []string{filepath.Join(trusted, workerEnvSyncVerify)}
			if !c.noArg {
				args = append(args, "4321")
			}
			r := runWithFakeGH(t, func(d string) { pr.serve(t, d) }, args, t.TempDir(), env...)
			raw, _ := os.ReadFile(outputFile)
			out := parseWorkerEnvOutput(t, string(raw))
			if r.code != c.want.code || out["verdict"] != c.want.verdict {
				t.Fatalf("exit %d verdict %q, want exit %d verdict %q\n%s", r.code, out["verdict"], c.want.code, c.want.verdict, r.out)
			}
			for k, v := range c.want.outputs {
				if out[k] != v {
					t.Errorf("output %s = %q, want %q\n%s", k, out[k], v, r.out)
				}
			}
			names := func(re string) []string {
				var n []string
				for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(r.out, -1) {
					n = append(n, m[1])
				}
				return n
			}
			if c.says != "" && !strings.Contains(r.out, c.says+"\n") {
				t.Errorf("the report lacks %q\n%s", c.says, r.out)
			}
			if got := names(`(?m)^FAIL ([a-z-]+):`); !slices.Equal(got, c.want.fails) {
				t.Errorf("failed checks %v, want %v\n%s", got, c.want.fails, r.out)
			}
			if got := names(`(?m)^UNKNOWN ([a-z-]+):`); !slices.Equal(got, c.want.unknown) {
				t.Errorf("undecided checks %v, want %v\n%s", got, c.want.unknown, r.out)
			}
			// Its only workflow commands are its own annotations, each a
			// single ::name ...:: with nothing after it the runner could read
			// as another one; the reasons it hands act are defanged too.
			var annotation string
			switch c.want.verdict {
			case "refuse":
				annotation = "::error title=worker-env sync PR refused::"
				for _, check := range c.want.fails {
					if !strings.Contains(out["reason"], check+": ") {
						t.Errorf("reason %q does not name the failed check %s", out["reason"], check)
					}
				}
			case "unknown":
				annotation = "::warning title=worker-env sync PR not verified::"
			}
			cmds := workerEnvCommandLines(r.out)
			if annotation == "" && len(cmds) != 0 || annotation != "" && (len(cmds) != 1 || !strings.HasPrefix(cmds[0], annotation) || strings.Count(cmds[0], "::") != 2) {
				t.Errorf("workflow commands in the output %q, want only %q once\n%s", cmds, annotation, r.out)
			} else if msg := workerEnvRunnerMessage(strings.TrimPrefix(strings.Join(cmds, ""), annotation)); strings.ContainsAny(msg, "\r\n") {
				t.Errorf("annotation message %q, as the runner decodes it, breaks across lines", msg)
			}
			if strings.Contains(out["reason"], "::") || strings.Contains(out["reason"], "##[") {
				t.Errorf("reason output holds a workflow command opener: %q", out["reason"])
			}
			for _, call := range r.calls {
				joined := strings.Join(call, " ")
				if call[0] != "api" || slices.Contains(call, "-X") || slices.Contains(call, "--method") || strings.Contains(joined, "mutation") {
					t.Errorf("the verifier wrote to GitHub: gh %q", call)
				}
			}
		})
	}
}

func (c *workerEnvVerifyCase) mutate(f func(*workerEnvVerifyCase)) {
	// Deep-copy what a case may edit in place.
	c.commits = slices.Clone(c.commits)
	c.activity = slices.Clone(c.activity)
	c.files = slices.Clone(c.files)
	c.labels = slices.Clone(c.labels)
	c.labeled = slices.Clone(c.labeled)
	runs := map[string][][4]string{}
	for k, v := range c.runs {
		runs[k] = slices.Clone(v)
	}
	c.runs = runs
	checks := map[string]string{}
	for k, v := range c.checks {
		checks[k] = v
	}
	c.checks = checks
	f(c)
}
