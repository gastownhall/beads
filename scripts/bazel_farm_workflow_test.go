package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// bazel-farm.yml: the one pull_request_target caller of bazel.yml. It gives
// the RBE secrets to fork PRs whose author and triggering user are on the
// base branch's allowlist (engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Trusted-
// author fork PRs"). Every property below is load-bearing for that trust
// boundary; relaxing one is a security decision, not a refactor.
const (
	bazelFarmWorkflowName  = "bazel-farm.yml"
	bazelFarmWorkflowTitle = "Bazel Farm (trusted forks)"
	bazelFarmAllowlist     = ".github/bazel-farm-allowlist.txt"
	bazelFarmAuthorize     = ".github/scripts/bazel-farm-authorize.sh"

	// bazel.yml's rbe job: a fork runs remotely only for this caller's
	// authorized pull_request_target call with a pinned checkout.
	bazelForkFarmValue = "${{ inputs.fork-farm == 'authorized' && github.event_name == 'pull_request_target' && inputs.checkout-sha != '' }}"
	// Every bazel.yml checkout: the caller's pinned SHA (empty: the event's
	// default ref) and no token in .git/config.
	bazelCheckoutRef = "${{ inputs.checkout-sha }}"
	// actions/checkout v7 refuses a fork PR head on pull_request_target
	// unless allow-unsafe-pr-checkout is true. Only the authorized farm call
	// may opt in; every other run evaluates this to false.
	bazelAllowUnsafeCheckout = "${{ inputs.fork-farm == 'authorized' && github.event_name == 'pull_request_target' }}"
)

// The users on the allowlist, numeric id -> login (gh api users/<login>
// --jq .id), the same people as gascity's .github/blacksmith-allowlist.txt.
// A change here is a trust decision.
var bazelFarmUsers = map[string]string{
	"8082291":  "julianknutsen",
	"91582":    "quad341",
	"36544495": "sjarmak",
	"2568253":  "csells",
}

func TestBazelFarmWorkflowSecurity(t *testing.T) {
	rel := filepath.Join(".github", "workflows", bazelFarmWorkflowName)
	root := readYAMLNode(t, rel)
	raw := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelFarmWorkflowName)

	var doc struct {
		Name string `yaml:"name"`
		On   map[string]struct {
			Types    []string `yaml:"types"`
			Branches []string `yaml:"branches"`
		} `yaml:"on"`
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress any    `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
		Permissions any               `yaml:"permissions"`
		Env         map[string]string `yaml:"env"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != bazelFarmWorkflowTitle {
		t.Errorf("name = %q, want %q (bazel-autofix.yml and docs-autofix.yml must never list it)", doc.Name, bazelFarmWorkflowTitle)
	}

	// Trigger: pull_request_target only (the base branch's copy of this
	// file and of bazel.yml runs), the four PR-code-changing actions, main.
	if got := yamlMapKeys(root, "on"); !reflect.DeepEqual(got, []string{"pull_request_target"}) {
		t.Errorf("triggers = %v, want exactly [pull_request_target]", got)
	}
	prt := doc.On["pull_request_target"]
	if want := []string{"opened", "synchronize"}; !reflect.DeepEqual(prt.Types, want) {
		t.Errorf("pull_request_target types = %v, want %v (no reopened/ready_for_review: they re-test a head no listed user necessarily pushed; no labeled/edited/comment-driven runs)", prt.Types, want)
	}
	if !reflect.DeepEqual(prt.Branches, []string{"main"}) {
		t.Errorf("pull_request_target branches = %v, want [main]", prt.Branches)
	}

	// One run per PR, superseded by the next event.
	if doc.Concurrency.Group != "bazel-farm-${{ github.event.pull_request.number }}" || doc.Concurrency.CancelInProgress != true {
		t.Errorf("concurrency = %+v; want group bazel-farm-<pr number>, cancel-in-progress true", doc.Concurrency)
	}
	if len(doc.Env) != 0 {
		t.Errorf("workflow env = %v; want none (nothing workflow-wide reaches PR code)", doc.Env)
	}

	// Permissions: contents: read at the workflow and on every job, and
	// nothing else anywhere in the file.
	readOnly := map[string]any{"contents": "read"}
	if !reflect.DeepEqual(doc.Permissions, readOnly) {
		t.Errorf("workflow permissions = %v, want %v", doc.Permissions, readOnly)
	}
	workflow := readCIWorkflow(t, bazelFarmWorkflowName)
	var jobs []string
	for name, job := range workflow.Jobs {
		jobs = append(jobs, name)
		if !reflect.DeepEqual(job.Permissions, readOnly) {
			t.Errorf("job %s permissions = %v, want %v", name, job.Permissions, readOnly)
		}
		if len(job.Env) != 0 {
			t.Errorf("job %s env = %v; want none", name, job.Env)
		}
	}
	sort.Strings(jobs)
	if !reflect.DeepEqual(jobs, []string{"authorize", "farm"}) {
		t.Fatalf("jobs = %v, want [authorize farm]", jobs)
	}
	secretRef := regexp.MustCompile(`\bsecrets\s*(\.|\[)`)
	walkYAML(root, "", func(path string, key bool, value string) {
		if strings.Contains(path, "permissions") && !key && value != "read" {
			t.Errorf("%s = %q; only read permissions are allowed", path, value)
		}
		if key && value == "continue-on-error" {
			t.Errorf("%s hides a failure", path)
		}
		if !key && value == "inherit" {
			t.Errorf("%s: secrets: inherit; pass the four RBE secrets explicitly", path)
		}
		// Secrets only in the farm call's secrets block.
		if !key && secretRef.MatchString(value) && !strings.HasPrefix(path, ".jobs.farm.secrets.") {
			t.Errorf("%s reads secrets (%q); only .jobs.farm.secrets may", path, value)
		}
		// No expression in any script: event data (branch names, titles,
		// logins) reaches scripts through env only.
		if !key && strings.HasSuffix(path, ".run") && strings.Contains(value, "${{") {
			t.Errorf("%s interpolates an expression into a script: %q", path, value)
		}
	})

	// authorize: no PR code, no secrets, the base commit's allowlist.
	auth := workflow.job(t, "authorize")
	const wantAuthIf = "github.event.pull_request.base.repo.full_name == github.repository && " +
		"github.event.pull_request.head.repo.full_name != github.repository"
	if auth.If != wantAuthIf || auth.RunsOn != "ubuntu-latest" || len(auth.Needs) != 0 {
		t.Errorf("authorize if=%q runs-on=%q needs=%v; want the fork prefilter %q on ubuntu-latest", auth.If, auth.RunsOn, auth.Needs, wantAuthIf)
	}
	if auth.TimeoutMinutes <= 0 || auth.TimeoutMinutes > 10 {
		t.Errorf("authorize timeout-minutes = %d, want 1..10", auth.TimeoutMinutes)
	}
	if want := map[string]string{"allowed": "${{ steps.decide.outputs.allowed }}"}; !reflect.DeepEqual(auth.Outputs, want) {
		t.Errorf("authorize outputs = %v, want %v", auth.Outputs, want)
	}
	if len(auth.Steps) != 2 {
		t.Fatalf("authorize has %d steps, want the base checkout and the decision", len(auth.Steps))
	}
	checkout := auth.Steps[0]
	if checkout.Uses != "actions/checkout@"+checkoutSHA {
		t.Errorf("authorize checkout uses %q, want actions/checkout@%s", checkout.Uses, checkoutSHA)
	}
	wantCheckout := map[string]string{
		"ref":                       "${{ github.sha }}",
		"persist-credentials":       "false",
		"sparse-checkout":           bazelFarmAllowlist + "\n" + bazelFarmAuthorize + "\n",
		"sparse-checkout-cone-mode": "false",
	}
	if !reflect.DeepEqual(checkout.With, wantCheckout) {
		t.Errorf("authorize checkout with = %q; want %q (the run's own base commit, never a PR ref)", checkout.With, wantCheckout)
	}
	decide := auth.Steps[1]
	wantEnv := map[string]string{
		"ALLOWLIST":      bazelFarmAllowlist,
		"EVENT_NAME":     "${{ github.event_name }}",
		"ACTION":         "${{ github.event.action }}",
		"REPOSITORY":     "${{ github.repository }}",
		"BASE_REPO":      "${{ github.event.pull_request.base.repo.full_name }}",
		"HEAD_REPO":      "${{ github.event.pull_request.head.repo.full_name }}",
		"HEAD_OWNER_ID":  "${{ github.event.pull_request.head.repo.owner.id }}",
		"BASE_REF":       "${{ github.event.pull_request.base.ref }}",
		"DEFAULT_BRANCH": "${{ github.event.repository.default_branch }}",
		"HEAD_SHA":       "${{ github.event.pull_request.head.sha }}",
		"PR_AUTHOR":      "${{ github.event.pull_request.user.login }}",
		"PR_AUTHOR_ID":   "${{ github.event.pull_request.user.id }}",
		"SENDER":         "${{ github.event.sender.login }}",
		"SENDER_ID":      "${{ github.event.sender.id }}",
	}
	if decide.ID != "decide" || decide.Uses != "" || decide.Run != "bash "+bazelFarmAuthorize || !reflect.DeepEqual(decide.Env, wantEnv) {
		t.Errorf("authorize decision step id=%q uses=%q run=%q env=%v; want id decide running %s with env %v",
			decide.ID, decide.Uses, decide.Run, decide.Env, bazelFarmAuthorize, wantEnv)
	}

	// farm: bazel.yml from the same base commit, only when authorized, with
	// the four RBE secrets and the event's pinned head SHA.
	farm := workflow.job(t, "farm")
	if farm.Uses != "./.github/workflows/"+bazelWorkflowName || !reflect.DeepEqual([]string(farm.Needs), []string{"authorize"}) ||
		farm.If != "${{ needs.authorize.outputs.allowed == 'true' }}" || farm.RunsOn != "" || len(farm.Steps) != 0 {
		t.Errorf("farm uses=%q needs=%v if=%q; want a call of ./.github/workflows/%s needing authorize, if allowed == 'true'",
			farm.Uses, farm.Needs, farm.If, bazelWorkflowName)
	}
	// Check runs are "<job name> / <lane>": the farm's PR-controlled
	// results must never share a name with pr.yml's Bazel call.
	if farm.Name != "Bazel Farm" {
		t.Errorf("farm job name = %q, want %q", farm.Name, "Bazel Farm")
	}
	if prName := readCIWorkflow(t, "pr.yml").job(t, "bazel").Name; strings.EqualFold(farm.Name, prName) || farm.Name == "" {
		t.Errorf("farm job name %q collides with pr.yml's bazel job %q", farm.Name, prName)
	}
	gotSecrets := map[string]string{}
	if m, ok := farm.Secrets.(map[string]any); ok {
		for k, v := range m {
			gotSecrets[k] = fmt.Sprint(v)
		}
	}
	if !reflect.DeepEqual(gotSecrets, bazelCallSecrets) {
		t.Errorf("farm secrets = %v, want exactly %v", farm.Secrets, bazelCallSecrets)
	}
	wantWith := map[string]string{
		"checkout-sha":        "${{ github.event.pull_request.head.sha }}",
		"fork-farm":           "authorized",
		"integration":         "on",
		"build-artifact-name": "bazel-farm-build-artifacts",
	}
	if !reflect.DeepEqual(farm.With, wantWith) {
		t.Errorf("farm with = %v, want exactly %v", farm.With, wantWith)
	}
}

// bazel.yml's side of the farm: the pinned checkout, the one decision input,
// and nothing a pull_request_target run could hand a privileged consumer.
func TestBazelWorkflowForkFarmInputs(t *testing.T) {
	call := readBazelWorkflowCall(t)
	for name, want := range map[string]string{"checkout-sha": "", "fork-farm": "off"} {
		if in, ok := call.Inputs[name]; !ok || in.Type != "string" || in.Default != want {
			t.Errorf("workflow_call input %s = %+v, want type string, default %q", name, in, want)
		}
	}
	workflow := readCIWorkflow(t, bazelWorkflowName)
	checkouts := 0
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Errorf("%s job %s step %q interpolates an expression into its script", bazelWorkflowName, name, step.Name)
			}
			if actionFamily(step.Uses) != "actions/checkout" {
				continue
			}
			checkouts++
			want := map[string]string{"ref": bazelCheckoutRef, "persist-credentials": "false", "allow-unsafe-pr-checkout": bazelAllowUnsafeCheckout}
			if !reflect.DeepEqual(step.With, want) {
				t.Errorf("%s job %s checkout with = %v, want %v", bazelWorkflowName, name, step.With, want)
			}
		}
	}
	if checkouts != len(workflow.Jobs)-1 {
		t.Errorf("%d checkouts in %s, want one per lane (%d)", checkouts, bazelWorkflowName, len(workflow.Jobs)-1)
	}
	if got := workflow.job(t, bazelRBEJobName).Steps[0].Env["FORK_FARM"]; got != bazelForkFarmValue {
		t.Errorf("rbe FORK_FARM = %q, want %q", got, bazelForkFarmValue)
	}
	// inputs.checkout-sha: only the checkouts' ref and FORK_FARM.
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "", func(path string, key bool, value string) {
		if key || !strings.Contains(value, "inputs.checkout-sha") {
			return
		}
		if value == bazelCheckoutRef && strings.HasSuffix(path, ".with.ref") {
			return
		}
		if value == bazelForkFarmValue && path == ".jobs."+bazelRBEJobName+".steps[0].env.FORK_FARM" {
			return
		}
		t.Errorf("%s: %s uses inputs.checkout-sha (%q); only checkout refs and FORK_FARM may", bazelWorkflowName, path, value)
	})
	var conc struct {
		Concurrency struct {
			CancelInProgress string `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &conc); err != nil {
		t.Fatal(err)
	}
	if want := "${{ github.event_name == 'pull_request' || github.event_name == 'pull_request_target' }}"; conc.Concurrency.CancelInProgress != want {
		t.Errorf("%s cancel-in-progress = %q, want %q", bazelWorkflowName, conc.Concurrency.CancelInProgress, want)
	}

	// allow-unsafe-pr-checkout: nowhere but bazel.yml's lane checkouts, and
	// there only as the gated expression (never a literal true).
	optIns := 0
	for _, entry := range mustReadWorkflowDir(t) {
		walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", entry)), "", func(path string, key bool, value string) {
			if !key || value != "allow-unsafe-pr-checkout" {
				return
			}
			optIns++
			if entry != bazelWorkflowName || !strings.HasSuffix(path, ".with.allow-unsafe-pr-checkout") {
				t.Errorf("%s: %s opts into checking out fork PR code; only bazel.yml's lane checkouts may", entry, path)
			}
		})
	}
	if optIns != checkouts {
		t.Errorf("%d allow-unsafe-pr-checkout keys, want one per bazel.yml lane checkout (%d)", optIns, checkouts)
	}

	// Only bazel-farm.yml passes the farm inputs (pr.yml's call is pinned by
	// bazelPRCallWith; nightly.yml's here).
	entries, err := os.ReadDir(filepath.Join(sourceRepoRoot(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yml") || entry.Name() == bazelFarmWorkflowName {
			continue
		}
		for jobName, job := range readCIWorkflow(t, entry.Name()).Jobs {
			for _, in := range []string{"checkout-sha", "fork-farm"} {
				if _, ok := job.With[in]; ok {
					t.Errorf("%s job %s passes %s; only %s may", entry.Name(), jobName, in, bazelFarmWorkflowName)
				}
			}
		}
	}
}

// No privileged workflow consumes anything from a farm run: every
// workflow_run consumer watches only "PR" and acts only on pull_request
// runs, and nothing downloads another run's artifacts by run id.
func TestBazelFarmArtifactsHaveNoPrivilegedConsumer(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(sourceRepoRoot(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	consumers := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		rel := filepath.Join(".github", "workflows", entry.Name())
		raw := readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+entry.Name())
		if strings.Contains(raw, bazelFarmWorkflowTitle) && entry.Name() != bazelFarmWorkflowName {
			t.Errorf("%s names %q; nothing may consume a farm run", entry.Name(), bazelFarmWorkflowTitle)
		}
		for jobName, job := range readCIWorkflow(t, entry.Name()).Jobs {
			for _, step := range job.Steps {
				if _, ok := step.With["run-id"]; ok {
					t.Errorf("%s job %s step %q downloads another run's artifacts by run-id", entry.Name(), jobName, step.Name)
				}
			}
		}
		if !contains(yamlMapKeys(readYAMLNode(t, rel), "on"), "workflow_run") {
			continue
		}
		consumers++
		var doc struct {
			On struct {
				WorkflowRun struct {
					Workflows []string `yaml:"workflows"`
				} `yaml:"workflow_run"`
			} `yaml:"on"`
		}
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(doc.On.WorkflowRun.Workflows, []string{"PR"}) {
			t.Errorf("%s workflow_run workflows = %v, want exactly [PR]", entry.Name(), doc.On.WorkflowRun.Workflows)
		}
		for jobName, job := range readCIWorkflow(t, entry.Name()).Jobs {
			if !strings.Contains(job.If, "github.event.workflow_run.event == 'pull_request'") {
				t.Errorf("%s job %s if = %q; want it limited to pull_request runs (a farm run is pull_request_target)", entry.Name(), jobName, job.If)
			}
		}
	}
	if consumers == 0 {
		t.Errorf("found no workflow_run consumers; the scan is broken (bazel-autofix.yml, docs-autofix.yml)")
	}
}

// The allowlist is the trust decision: exactly these numeric user ids, each
// with its login as a comment for humans (ids are what the script matches:
// a renamed account's old login can be registered by anyone).
func TestBazelFarmAllowlist(t *testing.T) {
	got := map[string]string{}
	entry := regexp.MustCompile(`^([1-9][0-9]{0,19}) # ([a-z0-9][a-z0-9-]{0,38})$`)
	for _, line := range strings.Split(readPolicyFile(t, sourceRepoRoot(t), bazelFarmAllowlist), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := entry.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("allowlist line %q is not `<numeric id> # <login>`", line)
			continue
		}
		got[m[1]] = m[2]
	}
	if !reflect.DeepEqual(got, bazelFarmUsers) {
		t.Errorf("allowlist = %v, want %v (changing it is a trust decision: update bazelFarmUsers deliberately)", got, bazelFarmUsers)
	}
}

func TestBazelFarmAuthorizeScript(t *testing.T) {
	bash := requireHostTool(t, "bash")
	script := filepath.Join(sourceRepoRoot(t), bazelFarmAuthorize)
	dir := t.TempDir()
	list := filepath.Join(dir, "allowlist.txt")
	// 1001: listed; 1002: listed with trailing comment and spaces; 1003
	// appears only inside a comment.
	if err := os.WriteFile(list, []byte("# comment 1003\n\n1001 # alice\n  1002   # bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"ALLOWLIST":      list,
		"EVENT_NAME":     "pull_request_target",
		"ACTION":         "synchronize",
		"REPOSITORY":     "gastownhall/beads",
		"BASE_REPO":      "gastownhall/beads",
		"HEAD_REPO":      "alice/beads",
		"HEAD_OWNER_ID":  "1001",
		"BASE_REF":       "main",
		"DEFAULT_BRANCH": "main",
		"HEAD_SHA":       strings.Repeat("ab", 20),
		"PR_AUTHOR":      "alice",
		"PR_AUTHOR_ID":   "1001",
		"SENDER":         "alice",
		"SENDER_ID":      "1001",
	}
	run := func(t *testing.T, env map[string]string) (string, string, error) {
		t.Helper()
		out := filepath.Join(t.TempDir(), "out")
		cmd := exec.Command(bash, script)
		// en_US.UTF-8 (where installed), whose [0-9] and [A-Za-z] match
		// non-ASCII: the script must pin its own locale.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out, "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		logs, err := cmd.CombinedOutput()
		data, _ := os.ReadFile(out)
		return string(data), string(logs), err
	}
	with := func(change map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range change {
			env[k] = v
		}
		return env
	}
	cases := []struct {
		name    string
		change  map[string]string
		allowed bool
	}{
		{"listed author pushes", nil, true},
		{"opened", map[string]string{"ACTION": "opened"}, true},
		{"listed via trailing-comment line", map[string]string{"HEAD_REPO": "bob/beads", "HEAD_OWNER_ID": "1002", "PR_AUTHOR_ID": "1002", "SENDER_ID": "1002"}, true},
		{"another listed user pushes to a listed author's PR", map[string]string{"SENDER": "bob", "SENDER_ID": "1002"}, true},
		{"login is display only: listed ids, unlisted logins", map[string]string{"PR_AUTHOR": "mallory", "SENDER": "mallory"}, true},
		// F5: a listed login re-registered by someone else has a new id.
		{"renamed login re-registered (author)", map[string]string{"PR_AUTHOR": "alice", "PR_AUTHOR_ID": "666", "HEAD_OWNER_ID": "666"}, false},
		{"renamed login re-registered (sender)", map[string]string{"SENDER": "alice", "SENDER_ID": "666"}, false},
		{"author not listed", map[string]string{"PR_AUTHOR_ID": "666", "HEAD_OWNER_ID": "666"}, false},
		{"collaborator pushes to a listed author's fork", map[string]string{"SENDER": "mallory", "SENDER_ID": "666"}, false},
		{"listed pusher on an unlisted author's PR", map[string]string{"PR_AUTHOR_ID": "666", "HEAD_OWNER_ID": "666", "SENDER_ID": "1001"}, false},
		{"id only in a comment", map[string]string{"SENDER_ID": "1003"}, false},
		{"id prefix", map[string]string{"SENDER_ID": "100"}, false},
		{"id suffix", map[string]string{"SENDER_ID": "10011"}, false},
		{"id with leading zero", map[string]string{"SENDER_ID": "01001"}, false},
		{"id with newline", map[string]string{"SENDER_ID": "1001\n1002"}, false},
		{"empty sender id", map[string]string{"SENDER_ID": ""}, false},
		{"empty author id", map[string]string{"PR_AUTHOR_ID": ""}, false},
		{"bot sender login is not printed", map[string]string{"SENDER": "dependabot[bot]", "SENDER_ID": "49699333"}, false},
		{"login with shell metacharacters", map[string]string{"SENDER": "alice;id", "SENDER_ID": "666"}, false},
		{"same-repo PR", map[string]string{"HEAD_REPO": "gastownhall/beads"}, false},
		{"same-repo PR, other case", map[string]string{"HEAD_REPO": "GastownHall/Beads"}, false},
		{"no head repo (deleted fork)", map[string]string{"HEAD_REPO": ""}, false},
		{"other base repo", map[string]string{"BASE_REPO": "someone/beads"}, false},
		{"base repo prefix", map[string]string{"BASE_REPO": "gastownhall/beads-evil"}, false},
		{"not the default branch", map[string]string{"BASE_REF": "release/1.0"}, false},
		{"pull_request event", map[string]string{"EVENT_NAME": "pull_request"}, false},
		{"reopened", map[string]string{"ACTION": "reopened"}, false},
		{"ready_for_review", map[string]string{"ACTION": "ready_for_review"}, false},
		{"labeled", map[string]string{"ACTION": "labeled"}, false},
		{"edited", map[string]string{"ACTION": "edited"}, false},
		{"unanchored action", map[string]string{"ACTION": "xopenedx"}, false},
		// F4: the head must be the author's own fork.
		{"cross-fork: head in someone else's fork", map[string]string{"HEAD_REPO": "mallory/beads", "HEAD_OWNER_ID": "666"}, false},
		{"cross-fork: head in another listed user's fork", map[string]string{"HEAD_REPO": "bob/beads", "HEAD_OWNER_ID": "1002"}, false},
		{"head owner id missing", map[string]string{"HEAD_OWNER_ID": ""}, false},
		{"owner and author ids missing", map[string]string{"HEAD_OWNER_ID": "", "PR_AUTHOR_ID": ""}, false},
		{"owner id not numeric", map[string]string{"HEAD_OWNER_ID": "1001x", "PR_AUTHOR_ID": "1001x"}, false},
		{"short sha", map[string]string{"HEAD_SHA": "abcdef1"}, false},
		{"uppercase sha", map[string]string{"HEAD_SHA": strings.Repeat("AB", 20)}, false},
		{"ref instead of sha", map[string]string{"HEAD_SHA": "refs/heads/main"}, false},
		{"sha with suffix", map[string]string{"HEAD_SHA": strings.Repeat("ab", 20) + "\nx"}, false},
		// F8: non-ASCII digits and letters never pass the byte-class checks,
		// whatever the runner's locale.
		{"non-ASCII digit in id", map[string]string{"SENDER_ID": "100\u0661"}, false},
		{"non-ASCII digit in owner id", map[string]string{"HEAD_OWNER_ID": "100\u0661", "PR_AUTHOR_ID": "100\u0661"}, false},
		{"non-ASCII hex in sha", map[string]string{"HEAD_SHA": strings.Repeat("ab", 19) + "a\u00e9"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, logs, err := run(t, with(c.change))
			if err != nil {
				t.Fatalf("script failed: %v\n%s", err, logs)
			}
			if want := fmt.Sprintf("allowed=%t\n", c.allowed); out != want {
				t.Errorf("GITHUB_OUTPUT = %q, want %q\n%s", out, want, logs)
			}
			if strings.Contains(logs, ";id") || strings.Contains(logs, "[bot]") {
				t.Errorf("log echoes an unvalidated login:\n%s", logs)
			}
		})
	}
	for name, content := range map[string]string{
		"missing":     "",
		"login entry": "1001 # alice\nmallory\n",
		"signed id":   "+1001\n",
	} {
		t.Run("broken allowlist fails: "+name, func(t *testing.T) {
			env := with(nil)
			env["ALLOWLIST"] = filepath.Join(t.TempDir(), "missing.txt")
			if content != "" {
				if err := os.WriteFile(env["ALLOWLIST"], []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if out, logs, err := run(t, env); err == nil || strings.Contains(out, "allowed=true") {
				t.Errorf("want failure without allowed=true; out=%q\n%s", out, logs)
			}
		})
	}
	t.Run("pins the C locale", func(t *testing.T) {
		if !regexp.MustCompile(`(?m)^export LC_ALL=C$`).MatchString(readPolicyFile(t, sourceRepoRoot(t), bazelFarmAuthorize)) {
			t.Errorf("%s does not export LC_ALL=C", bazelFarmAuthorize)
		}
	})
	t.Run("real allowlist admits listed users", func(t *testing.T) {
		env := with(map[string]string{
			"ALLOWLIST":     filepath.Join(sourceRepoRoot(t), bazelFarmAllowlist),
			"HEAD_REPO":     "sjarmak/beads",
			"HEAD_OWNER_ID": "36544495", "PR_AUTHOR_ID": "36544495", "SENDER_ID": "2568253",
		})
		if out, logs, err := run(t, env); err != nil || out != "allowed=true\n" {
			t.Errorf("out=%q err=%v\n%s", out, err, logs)
		}
	})
}

func mustReadWorkflowDir(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(sourceRepoRoot(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml") {
			names = append(names, entry.Name())
		}
	}
	return names
}

// F3: what a farm run's (pull_request_target) PR code could write to a cache
// must not change what later trusted runs execute.
//
// GitHub gives untrusted triggers a read-only cache token, and a declared
// cache-mode would override that default: bazel-farm.yml, bazel.yml and
// setup-bazel may declare none but read or none.
func TestBazelFarmCacheModeStaysReadOnly(t *testing.T) {
	for _, rel := range []string{
		filepath.Join(".github", "workflows", bazelFarmWorkflowName),
		filepath.Join(".github", "workflows", bazelWorkflowName),
		filepath.Join(setupBazelActionDir, "action.yml"),
	} {
		root := readYAMLNode(t, rel)
		var check func(node *yaml.Node, path string)
		check = func(node *yaml.Node, path string) {
			switch node.Kind {
			case yaml.MappingNode:
				for i := 0; i+1 < len(node.Content); i += 2 {
					k, v := node.Content[i].Value, node.Content[i+1]
					if k == "cache-mode" && (v.Kind != yaml.ScalarNode || (v.Value != "read" && v.Value != "none")) {
						t.Errorf("%s: %s.cache-mode = %q; only read or none (a pull_request_target run must not write caches trusted runs restore)", rel, path, v.Value)
					}
					check(v, path+"."+k)
				}
			case yaml.SequenceNode:
				for i, item := range node.Content {
					check(item, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		}
		check(root, "")
	}
}

// bazel.yml's token: exactly contents: read, at the top and on any job that
// declares permissions (a call cannot exceed its caller's, but pin it).
func TestBazelWorkflowPermissionsReadOnly(t *testing.T) {
	var doc struct {
		Permissions any `yaml:"permissions"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/"+bazelWorkflowName)), &doc); err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]any{"contents": "read"}
	if !reflect.DeepEqual(doc.Permissions, readOnly) {
		t.Errorf("%s permissions = %v, want exactly %v", bazelWorkflowName, doc.Permissions, readOnly)
	}
	for name, job := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if job.Permissions != nil && !reflect.DeepEqual(job.Permissions, readOnly) {
			t.Errorf("%s job %s permissions = %v, want none or exactly %v", bazelWorkflowName, name, job.Permissions, readOnly)
		}
	}
}

// Nothing restored from the runner cache is executed unverified: the Bazel
// binary is downloaded fresh into a Bazelisk home outside the cache and
// checked against a sha256 pinned for .bazelversion, the repo contents cache
// (unverified extracted repos) is off, and restored Go modules are checked
// against go.sum before use.
func TestBazelRestoredCachesAreVerified(t *testing.T) {
	version := strings.TrimSpace(readPolicyFile(t, sourceRepoRoot(t), ".bazelversion"))
	var install, wrapper ciWorkflowStep
	for _, step := range readSetupBazelAction(t).Runs.Steps {
		switch step.Name {
		case "Install Bazelisk":
			install = step
		case "Install bazel wrapper":
			wrapper = step
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		pin := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(version+"/"+arch) + `\) bazel_sha=[0-9a-f]{64} ;;$`)
		if !pin.MatchString(install.Run) {
			t.Errorf("setup-bazel pins no sha256 for Bazel %s (.bazelversion) on %s", version, arch)
		}
	}
	for _, want := range []string{
		`echo "BAZELISK_HOME=$RUNNER_TEMP/bazelisk-home"`,
		`echo "BAZELISK_VERIFY_SHA256=$bazel_sha"`,
		`echo "BAZEL_CI_BAZEL_SHA256=$bazel_sha"`,
		`bazel_version="$(tr -d '[:space:]' < .bazelversion)"`,
	} {
		if !strings.Contains(install.Run, want) {
			t.Errorf("setup-bazel Install Bazelisk lacks %q", want)
		}
	}
	if strings.Contains(install.Run, "bazel-ci-cache") {
		t.Errorf("setup-bazel puts Bazelisk's home in the runner cache; the Bazel binary must never be restored from it")
	}
	for _, want := range []string{
		"/usr/local/bin/bazelisk --version",
		`echo "${BAZEL_CI_BAZEL_SHA256}  $bin" | sha256sum -c -`,
		`find "$BAZELISK_HOME/downloads" -type f -path '*/bin/bazel' -print0`,
		`if [ "$n" -eq 0 ]; then`,
	} {
		if !strings.Contains(wrapper.Run, want) {
			t.Errorf("setup-bazel Install bazel wrapper lacks %q", want)
		}
	}
	if strings.Index(wrapper.Run, "/usr/local/bin/bazelisk --version") > strings.Index(wrapper.Run, "sha256sum -c") {
		t.Errorf("setup-bazel verifies the Bazel binary before downloading it")
	}

	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelJobName)
	restore := job.stepIndex(t, "Restore Go module cache")
	verify := job.stepIndex(t, "Verify restored Go modules")
	if verify != restore+1 || strings.TrimSpace(job.Steps[verify].Run) != "go mod verify" || job.Steps[verify].If != "" {
		t.Errorf("%s: want an unconditional `go mod verify` step right after the Go module cache restore", bazelJobName)
	}
	for name, j := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		for i, step := range j.Steps {
			if actionFamily(step.Uses) == cacheRestoreActionFamily && strings.Contains(step.With["path"], "go/pkg/mod") &&
				(i+1 >= len(j.Steps) || strings.TrimSpace(j.Steps[i+1].Run) != "go mod verify") {
				t.Errorf("%s job %s restores the Go module cache without verifying it next", bazelWorkflowName, name)
			}
		}
	}
}
