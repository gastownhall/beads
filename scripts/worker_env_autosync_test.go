package scripts_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// worker-env-autosync.yml closes the #7419 gap: nightly.yml's rbe-worker-
// env-sync job only opens a ci/infra issue for a human to fix a gascity
// re-pin (#7387's ~100-minute stall); this instead runs tools/rbe/worker-
// env-sync every 15 minutes and, on a real mismatch (exit 1), copies
// gascity main's manifest and pin (tools/rbe/worker-env-sync-copy) and
// opens or updates a fixed-branch PR with auto-merge armed.
const (
	workerEnvAutosyncWorkflow = "worker-env-autosync.yml"
	workerEnvAutosyncJob      = "autosync"
	workerEnvAutosyncBranch   = "bot/worker-env-sync"
	workerEnvSyncCopy         = "tools/rbe/worker-env-sync-copy"
)

// TestWorkerEnvAutosyncWorkflowTriggersOnScheduleAndDispatchOnly: the App
// private key this job mints is a secret of the autofix environment
// (deployment-branch policy: main only), and the only events that can ever
// run on main without a fork in the loop are schedule and workflow_dispatch
// -- pull_request (or pull_request_target) here would hand a fork a path to
// it.
func TestWorkerEnvAutosyncWorkflowTriggersOnScheduleAndDispatchOnly(t *testing.T) {
	var parsed struct {
		On map[string]any `yaml:"on"`
	}
	text := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+workerEnvAutosyncWorkflow)
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatal(err)
	}
	if got := len(parsed.On); got != 2 {
		t.Fatalf("%s on: has %d triggers (%v), want exactly schedule and workflow_dispatch", workerEnvAutosyncWorkflow, got, parsed.On)
	}
	if _, ok := parsed.On["workflow_dispatch"]; !ok {
		t.Errorf("%s has no workflow_dispatch trigger", workerEnvAutosyncWorkflow)
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

// TestWorkerEnvAutosyncWorkflowPermissionsAreMinimal: least privilege.
// Top-level and the job's own grant both stay read-only: every write this
// job makes (the push, the PR, the label, auto-merge) goes through one of
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
// opens/labels the PR and arms auto-merge with pr-token alone (so it is
// not a GITHUB_TOKEN PR - those never trigger CI), and arms auto-merge
// rather than merging directly.
func TestWorkerEnvAutosyncWorkflowShape(t *testing.T) {
	job := readCIWorkflow(t, workerEnvAutosyncWorkflow).job(t, workerEnvAutosyncJob)

	sync := job.step(t, "Compare the worker-env manifest and pin with gascity's main")
	if sync.ID != "sync" || !strings.Contains(sync.Run, "tools/rbe/worker-env-sync main") {
		t.Errorf("sync step: id %q, run %q; want id sync running tools/rbe/worker-env-sync main", sync.ID, sync.Run)
	}

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
	if !strings.Contains(copyStep.Run, workerEnvSyncCopy+" main") {
		t.Errorf("copy step run %q, want it to call %s main", copyStep.Run, workerEnvSyncCopy)
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
	if !strings.Contains(push.Run, "force-with-lease=refs/heads/$BRANCH") {
		t.Errorf("push step run %q, want a --force-with-lease scoped to the fixed branch's ref alone", push.Run)
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
	if !strings.Contains(pr.Run, "gh pr create") || !strings.Contains(pr.Run, "gh pr edit") || !strings.Contains(pr.Run, `--add-label "$LABEL"`) {
		t.Errorf("PR step run %q, want it to create-or-update the PR and add the label via gh", pr.Run)
	}
	if pr.Env["LABEL"] != "status/needs-review-auto" {
		t.Errorf("PR step env LABEL = %q, want status/needs-review-auto (routes it through the automated review workflow)", pr.Env["LABEL"])
	}

	merge := job.step(t, "Enable auto-merge")
	if merge.If != mismatchIf+" && steps.pr.outputs.number" {
		t.Errorf("merge step if = %q, want gated on a real mismatch and a PR having been opened", merge.If)
	}
	if merge.Uses != "" || !strings.Contains(merge.Run, "gh pr merge --auto") {
		t.Errorf("merge step = %+v, want a gh pr merge --auto call (arms auto-merge; never merges outright)", merge)
	}
	if merge.Env["GH_TOKEN"] != "${{ steps.pr-token.outputs.token }}" {
		t.Errorf("merge step env GH_TOKEN = %q, want pr-token (enabling auto-merge is PR-shaped, not a push)", merge.Env["GH_TOKEN"])
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

// TestWorkerEnvSyncCopyAgainstFixture runs the real tools/rbe/worker-env-
// sync-copy against a scratch checkout and a local gascity (reusing
// rbe_worker_env_test.go's fixture): it overwrites the manifest and the pin
// byte for byte, leaves everything else untouched, and fails loudly rather
// than writing anything when gascity's platforms/BUILD.bazel carries no
// pin to copy.
func TestWorkerEnvSyncCopyAgainstFixture(t *testing.T) {
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
	if !strings.Contains(out, "copied gascity main's worker-env ("+f.pin+")") {
		t.Errorf("worker-env-sync-copy output %q, want it to report the copied pin %s", out, f.pin)
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

	// gascity's platforms/BUILD.bazel with no pin at all: nothing copied.
	noPinGascity := t.TempDir()
	rbeTestWrite(t, filepath.Join(noPinGascity, "main/tools/rbe/worker-env.txt"), "arch x86_64\n", 0o644)
	rbeTestWrite(t, filepath.Join(noPinGascity, "main/platforms/BUILD.bazel"), "platform(\n    name = \"rbe_worker\",\n)\n", 0o644)
	beadsNoPin := f.beads(t, rbeTestStaleManifest, stalePin)
	rbeTestWrite(t, filepath.Join(beadsNoPin, workerEnvSyncCopy), readPolicyFile(t, bazelPolicyRoot(t), workerEnvSyncCopy), 0o755)
	cmd := "WORKER_ENV_SYNC_URL=file://" + noPinGascity + " " + workerEnvSyncCopy + " main\n"
	code, out = f.run(t, beadsNoPin, cmd)
	if code == 0 || strings.Contains(out, "copied gascity") {
		t.Errorf("worker-env-sync-copy with no gascity pin: exit %d\n%s, want a failure and nothing copied", code, out)
	}
	stillStale, err := os.ReadFile(filepath.Join(beadsNoPin, rbeWorkerEnvManifest))
	if err != nil || string(stillStale) != rbeTestStaleManifest {
		t.Errorf("worker-env-sync-copy wrote despite no gascity pin: %q, %v", stillStale, err)
	}
}
