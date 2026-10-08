package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// Test actions exec host tools and load the shared libraries of every cgo
// binary they link, so the remote worker host is an input to every result
// rbe-west caches. //platforms:rbe_worker puts it into the action key: its
// worker-env exec property is the sha256 of tools/rbe/worker-env.txt, and
// rbe-west's schedulers run an action only on a worker that advertises that
// exact value. The OSS pool and its manifest are gastownhall/gascity's
// (tools/rbe/worker-env, blacksmith-worker.sh); beads commits a byte-for-byte
// copy of the manifest so the pin is reviewable and moves only with it, and
// tools/rbe/worker-env-sync (nightly) checks the copy against gascity's main.
//
// The manifest is the worker's toolchain, not its image: arch, OS release,
// Go, dolt, and the upstream releases (at most major.minor) of the libraries
// and tools actions reach. The Blacksmith image's Ubuntu security revisions
// are not in it, so they neither re-key every action nor strand the pool.

const (
	rbeWorkerPlatformBuild = "platforms/BUILD.bazel"
	rbeWorkerEnvManifest   = "tools/rbe/worker-env.txt"
	rbeWorkerPlatformFlag  = "--extra_execution_platforms=//platforms:rbe_worker"
	rbeWorkerEnvSync       = "tools/rbe/worker-env-sync"
	rbeWorkerEnvSyncIssue  = "tools/rbe/worker-env-sync-issue"
	// bazel.yml's rbe job checks out only what worker-env-sync compares.
	bazelRBESparseCheckout = "tools/rbe\nplatforms\n"
	rbeWorkerEnvEnabledIf  = "${{ steps.decide.outputs.enabled == 'true' }}"
	nightlyWorkerEnvJob    = "rbe-worker-env-sync"
	nightlyWorkerEnvReport = "${{ runner.temp }}/worker-env-sync.md"
)

var (
	rbeWorkerPlatformRE = regexp.MustCompile(`(?s)\nplatform\(\n    name = "rbe_worker",\n(.*?)\n\)\n`)
	rbeExecPropsRE      = regexp.MustCompile(`(?s)exec_properties = \{\n(.*?)\n    \},`)
	rbeExecPropRE       = regexp.MustCompile(`^\s*"([^"]+)": "([^"]*)",$`)
	workerEnvPinRE      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// checkRBEWorkerPlatform: the rbe_worker platform's only exec property is
// worker-env, pinned to the sha256 of manifest. Any other property would be
// one no OSS worker advertises, and no action would ever schedule.
func checkRBEWorkerPlatform(build, manifest string) []error {
	m := rbeWorkerPlatformRE.FindStringSubmatch(build)
	if m == nil {
		return []error{errors.New(rbeWorkerPlatformBuild + ": no platform rbe_worker")}
	}
	props := rbeExecPropsRE.FindStringSubmatch(m[1])
	if props == nil {
		return []error{errors.New(rbeWorkerPlatformBuild + ": platform rbe_worker has no exec_properties")}
	}
	got := map[string]string{}
	for _, line := range strings.Split(props[1], "\n") {
		e := rbeExecPropRE.FindStringSubmatch(line)
		if e == nil {
			return []error{errors.New(rbeWorkerPlatformBuild + ": unexpected exec_properties line " + line)}
		}
		got[e[1]] = e[2]
	}
	pin := got["worker-env"]
	if len(got) != 1 || !workerEnvPinRE.MatchString(pin) {
		return []error{errors.New(rbeWorkerPlatformBuild + ": rbe_worker exec_properties must be worker-env=sha256:<hex> alone")}
	}
	sum := sha256.Sum256([]byte(manifest))
	if want := "sha256:" + hex.EncodeToString(sum[:]); pin != want {
		return []error{errors.New(rbeWorkerPlatformBuild + " pins worker-env=" + pin + ", but " +
			rbeWorkerEnvManifest + " hashes to " + want + ": commit the manifest and its sha256 together")}
	}
	return nil
}

// checkRBEWorkerSelected: every command executes on rbe_worker (the flag is
// key-affecting, so it may not depend on a config or command).
func checkRBEWorkerSelected(rc string) error {
	for _, line := range strings.Split(rc, "\n") {
		if strings.TrimSpace(line) == "build "+rbeWorkerPlatformFlag {
			return nil
		}
	}
	return errors.New(".bazelrc must set `build " + rbeWorkerPlatformFlag + "` unconditionally")
}

func TestRBEWorkerPlatformPinsWorkerEnv(t *testing.T) {
	root := bazelPolicyRoot(t)
	for _, err := range checkRBEWorkerPlatform(readPolicyFile(t, root, rbeWorkerPlatformBuild), readPolicyFile(t, root, rbeWorkerEnvManifest)) {
		t.Error(err)
	}
	if err := checkRBEWorkerSelected(readPolicyFile(t, root, ".bazelrc")); err != nil {
		t.Error(err)
	}
}

func TestRBEWorkerPlatformGuards(t *testing.T) {
	manifest := "arch x86_64\nos ubuntu 24.04\n"
	sum := sha256.Sum256([]byte(manifest))
	pin := "sha256:" + hex.EncodeToString(sum[:])
	build := "# header\nplatform(\n    name = \"rbe_worker\",\n    exec_properties = {\n        \"worker-env\": \"" + pin + "\",\n    },\n    parents = [\"@bazel_tools//tools:host_platform\"],\n)\n"
	if errs := checkRBEWorkerPlatform(build, manifest); len(errs) != 0 {
		t.Fatalf("good platform fixture: %v", errs)
	}
	for name, bad := range map[string][2]string{
		"manifest moved": {build, manifest + "pkg git 1\n"},
		"no platform":    {strings.Replace(build, `name = "rbe_worker"`, `name = "other"`, 1), manifest},
		"extra property": {strings.Replace(build, "    },", "        \"pool\": \"x\",\n    },", 1), manifest},
		"not a sha":      {strings.Replace(build, pin, "latest", 1), manifest},
	} {
		if len(checkRBEWorkerPlatform(bad[0], bad[1])) == 0 {
			t.Errorf("%s: expected an error", name)
		}
	}

	rc := "common --enable_bzlmod\nbuild " + rbeWorkerPlatformFlag + "\n"
	if err := checkRBEWorkerSelected(rc); err != nil {
		t.Fatalf("good .bazelrc fixture: %v", err)
	}
	for name, bad := range map[string]string{
		"missing":          "common --enable_bzlmod\n",
		"only in a config": strings.Replace(rc, "build "+rbeWorkerPlatformFlag, "build:remote-exec "+rbeWorkerPlatformFlag, 1),
		"commented out":    strings.Replace(rc, "build "+rbeWorkerPlatformFlag, "# build "+rbeWorkerPlatformFlag, 1),
	} {
		if checkRBEWorkerSelected(bad) == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// rbeWorkerEnvLineRE is every line gascity's tools/rbe/worker-env prints on
// a worker that has what it measures: one arch, dolt, go, os and yq line,
// and each package at its upstream release, at most major.minor.
var rbeWorkerEnvLineRE = map[string]*regexp.Regexp{
	"arch": regexp.MustCompile(`^arch [a-z0-9_]+$`),
	"dolt": regexp.MustCompile(`^dolt dolt version \S+$`),
	"go":   regexp.MustCompile(`^go go version go\S+ linux/\S+$`),
	"os":   regexp.MustCompile(`^os ubuntu \d+\.\d+$`),
	"tool": regexp.MustCompile(`^tool yq \d+$`),
	"pkg":  regexp.MustCompile(`^pkg [a-z0-9][a-z0-9+.-]* \d+(\.\d+)?$`),
}

// checkRBEWorkerEnvManifest: manifest is a rendering of gascity's
// tools/rbe/worker-env in its toolchain-only form. A copy of an older
// gascity manifest (dpkg revisions such as 9.4-3ubuntu6.2), a hand edit, or
// a truncated copy fails here rather than queueing every remote action.
func checkRBEWorkerEnvManifest(manifest string) []error {
	if !strings.HasSuffix(manifest, "\n") {
		return []error{errors.New(rbeWorkerEnvManifest + " must end with a newline, as gascity's tools/rbe/worker-env prints it")}
	}
	lines := strings.Split(strings.TrimSuffix(manifest, "\n"), "\n")
	var errs []error
	if !slices.IsSorted(lines) {
		errs = append(errs, errors.New(rbeWorkerEnvManifest+" is not sorted (LC_ALL=C), as gascity's tools/rbe/worker-env prints it"))
	}
	seen := map[string]int{}
	for _, line := range lines {
		kind, _, _ := strings.Cut(line, " ")
		seen[kind]++
		re := rbeWorkerEnvLineRE[kind]
		if re == nil || !re.MatchString(line) {
			errs = append(errs, errors.New(rbeWorkerEnvManifest+": "+strings.TrimSpace(line)+
				" is not a gascity tools/rbe/worker-env line (packages are measured at their upstream release, at most major.minor, and installed)"))
		}
	}
	for _, kind := range []string{"arch", "dolt", "go", "os", "tool"} {
		if seen[kind] != 1 {
			errs = append(errs, errors.New(rbeWorkerEnvManifest+" needs exactly one "+kind+" line"))
		}
	}
	if seen["pkg"] == 0 {
		errs = append(errs, errors.New(rbeWorkerEnvManifest+" has no packages"))
	}
	return errs
}

func TestRBEWorkerEnvManifestIsGascitysRendering(t *testing.T) {
	root := bazelPolicyRoot(t)
	for _, err := range checkRBEWorkerEnvManifest(readPolicyFile(t, root, rbeWorkerEnvManifest)) {
		t.Error(err)
	}
}

func TestRBEWorkerEnvManifestGuards(t *testing.T) {
	good := "arch x86_64\ndolt dolt version 2.1.8\ngo go version go1.26.6 linux/amd64\nos ubuntu 24.04\n" +
		"pkg coreutils 9.4\npkg git 2\npkg libc6 2.39\ntool yq 4\n"
	if errs := checkRBEWorkerEnvManifest(good); len(errs) != 0 {
		t.Fatalf("good manifest fixture: %v", errs)
	}
	for name, bad := range map[string]string{
		"dpkg revision":   strings.Replace(good, "pkg coreutils 9.4\n", "pkg coreutils 9.4-3ubuntu6.2\n", 1),
		"epoch":           strings.Replace(good, "pkg git 2\n", "pkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n", 1),
		"patch level":     strings.Replace(good, "pkg libc6 2.39\n", "pkg libc6 2.39.0\n", 1),
		"missing package": strings.Replace(good, "pkg git 2\n", "pkg git missing\n", 1),
		"no yq line":      strings.Replace(good, "tool yq 4\n", "", 1),
		"unsorted":        strings.Replace(good, "arch x86_64\n", "", 1) + "arch x86_64\n",
		"no newline":      strings.TrimSuffix(good, "\n"),
		"unknown line":    good + "zzz extra\n",
		"two os lines":    strings.Replace(good, "os ubuntu 24.04\n", "os ubuntu 24.04\nos ubuntu 26.04\n", 1),
	} {
		if len(checkRBEWorkerEnvManifest(bad)) == 0 {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// rbeWorkerEnvFixture is a local gascity (served to worker-env-sync through
// file:// URLs, WORKER_ENV_SYNC_URL) whose main pins manifest at pin, and a
// way to lay out a beads checkout with the given manifest and pin plus the
// real tools/rbe scripts, as the workflows run them.
type rbeWorkerEnvFixture struct {
	gascity, manifest, pin string
}

func rbeTestPin(manifest string) string {
	sum := sha256.Sum256([]byte(manifest))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func rbeTestPlatformBuild(pin string) string {
	return "platform(\n    name = \"rbe_worker\",\n    exec_properties = {\n        \"worker-env\": \"" + pin + "\",\n    },\n)\n"
}

func rbeTestWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func newRBEWorkerEnvFixture(t *testing.T) rbeWorkerEnvFixture {
	t.Helper()
	requireHostTool(t, "curl")
	requireHostTool(t, "bash")
	f := rbeWorkerEnvFixture{gascity: t.TempDir(), manifest: "arch x86_64\npkg git 2\n"}
	f.pin = rbeTestPin(f.manifest)
	rbeTestWrite(t, filepath.Join(f.gascity, "main", rbeWorkerEnvManifest), f.manifest, 0o644)
	rbeTestWrite(t, filepath.Join(f.gascity, "main", rbeWorkerPlatformBuild), rbeTestPlatformBuild(f.pin), 0o644)
	return f
}

// beads lays out a beads checkout pinning manifest at pin, with the real
// worker-env-sync scripts, and returns its root.
func (f rbeWorkerEnvFixture) beads(t *testing.T, manifest, pin string) string {
	t.Helper()
	root := bazelPolicyRoot(t)
	dir := t.TempDir()
	rbeTestWrite(t, filepath.Join(dir, rbeWorkerEnvManifest), manifest, 0o644)
	rbeTestWrite(t, filepath.Join(dir, rbeWorkerPlatformBuild), rbeTestPlatformBuild(pin), 0o644)
	for _, script := range []string{rbeWorkerEnvSync, rbeWorkerEnvSyncIssue} {
		rbeTestWrite(t, filepath.Join(dir, script), readPolicyFile(t, root, script), 0o755)
	}
	return dir
}

// run runs a workflow step's script (bash -e, as GitHub does) in dir with
// gascity served from the fixture plus env, and returns its exit code and
// output.
func (f rbeWorkerEnvFixture) run(t *testing.T, dir, script string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"WORKER_ENV_SYNC_URL=file://" + f.gascity}, env...)...)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), string(out)
	default:
		t.Fatalf("run step: %v\n%s", err, out)
		return -1, ""
	}
}

const rbeTestStaleManifest = "arch x86_64\npkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n"

// TestRBEWorkerEnvSync runs tools/rbe/worker-env-sync against a local
// gascity: quiet success when the manifest and pin match gascity's at REF;
// exit 1 with the diff and the pin to commit in the output, step summary and
// WORKER_ENV_SYNC_REPORT for a manifest or a pin that differs; and exit 2
// ("unknown", a warning) when gascity cannot be read, never "in step".
func TestRBEWorkerEnvSync(t *testing.T) {
	f := newRBEWorkerEnvFixture(t)
	run := func(ourManifest, ourPin string) (int, string, string, string) {
		t.Helper()
		beads := f.beads(t, ourManifest, ourPin)
		summary := filepath.Join(beads, "summary.md")
		report := filepath.Join(beads, "report.md")
		code, out := f.run(t, beads, "tools/rbe/worker-env-sync\n", "GITHUB_STEP_SUMMARY="+summary, "WORKER_ENV_SYNC_REPORT="+report)
		s, _ := os.ReadFile(summary)
		r, _ := os.ReadFile(report)
		return code, out, string(s), string(r)
	}

	code, out, summary, report := run(f.manifest, f.pin)
	if code != 0 || !strings.Contains(out, "worker-env: in step with gascity main ("+f.pin+")") || summary != "" || report != "" {
		t.Fatalf("in step: exit %d\n%s\nsummary:\n%s\nreport:\n%s", code, out, summary, report)
	}

	stalePin := rbeTestPin(rbeTestStaleManifest)
	for name, c := range map[string][2]string{
		"manifest and pin": {rbeTestStaleManifest, stalePin},
		"pin only":         {f.manifest, stalePin},
		"manifest only":    {rbeTestStaleManifest, f.pin},
	} {
		code, out, summary, report := run(c[0], c[1])
		if code != 1 {
			t.Errorf("%s out of step: exit %d, want 1\n%s", name, code, out)
			continue
		}
		for _, want := range []string{
			"::error title=rbe worker-env out of step::beads pins worker-env=" + c[1] + ", gascity main pins " + f.pin,
			"beads' worker-env pin is out of step with gascity main",
			"        \"worker-env\": \"" + f.pin + "\",\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", name, want, out)
			}
		}
		for what, md := range map[string]string{"step summary": summary, "report": report} {
			if !strings.Contains(md, "### rbe worker-env out of step with gascity") || !strings.Contains(md, "\"worker-env\": \""+f.pin+"\"") {
				t.Errorf("%s: no %s:\n%s", name, what, md)
			}
		}
		if c[0] != f.manifest && !strings.Contains(out, "-pkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n+pkg git 2\n") {
			t.Errorf("%s: no manifest diff:\n%s", name, out)
		}
	}

	// gascity unreachable (a REF that does not exist, or no gascity at all):
	// exit 2 with a warning, never "in step" and never "out of step".
	beads := f.beads(t, f.manifest, f.pin)
	for name, env := range map[string][]string{
		"missing ref": {"REF=no-such-ref"},
		"unreachable": {"REF=main", "WORKER_ENV_SYNC_URL=file:///nonexistent-gascity"},
	} {
		code, out := f.run(t, beads, `tools/rbe/worker-env-sync "$REF"`+"\n", env...)
		if code != 2 || !strings.Contains(out, "::warning title=rbe worker-env unknown::") || strings.Contains(out, "in step") || strings.Contains(out, "::error") {
			t.Errorf("worker-env-sync, %s: exit %d, want 2 with an unknown warning:\n%s", name, code, out)
		}
	}
}

// TestBazelRBEWorkerEnvPreflight: when gascity re-pins and beads' copy falls
// behind, a remote-execution run fails in bazel.yml's rbe job (every lane
// needs it, so none starts and pr.yml's gate is red at once) with the
// actionable message, instead of every lane queueing on rbe-west until it
// times out. Only runs whose lanes execute remotely (enabled: remote,
// fork-ro, fork-rw; merge groups are remote) check; gascity unreachable
// warns and proceeds. The steps run after decide and set no output, so the
// mode the lanes read is decide's alone.
func TestBazelRBEWorkerEnvPreflight(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName)
	if len(job.Steps) != 3 || job.Steps[0].ID != "decide" {
		t.Fatalf("%s steps = %d (first %q), want decide, the checkout and the worker-env check", bazelRBEJobName, len(job.Steps), job.Steps[0].ID)
	}
	if !strings.Contains(job.Steps[0].Run, `case "$mode" in remote|fork-ro|fork-rw) enabled=true ;; esac`) {
		t.Errorf("%s decide no longer sets enabled exactly for the remote modes; the preflight's if depends on it", bazelRBEJobName)
	}
	checkout, check := job.Steps[1], job.Steps[2]
	if actionFamily(checkout.Uses) != "actions/checkout" || checkout.If != rbeWorkerEnvEnabledIf || checkout.With["sparse-checkout"] != bazelRBESparseCheckout {
		t.Errorf("%s step 2: uses %q, if %q, sparse-checkout %q; want a checkout of %q if %s",
			bazelRBEJobName, checkout.Uses, checkout.If, checkout.With["sparse-checkout"], bazelRBESparseCheckout, rbeWorkerEnvEnabledIf)
	}
	wantEnv := map[string]string{"MODE": "${{ steps.decide.outputs.mode }}"}
	if check.If != rbeWorkerEnvEnabledIf || check.ID != "" || check.Uses != "" || check.ContinueOnError != nil || !reflect.DeepEqual(check.Env, wantEnv) ||
		!strings.Contains(check.Run, "tools/rbe/worker-env-sync main") {
		t.Errorf("%s step 3: if %q, id %q, uses %q, continue-on-error %v, env %v; want if %s, no id, env %v, running tools/rbe/worker-env-sync main",
			bazelRBEJobName, check.If, check.ID, check.Uses, check.ContinueOnError, check.Env, rbeWorkerEnvEnabledIf, wantEnv)
	}

	f := newRBEWorkerEnvFixture(t)
	stalePin := rbeTestPin(rbeTestStaleManifest)
	for _, c := range []struct {
		name, manifest, pin, url string
		code                     int
		want                     string
	}{
		{"in step", f.manifest, f.pin, "", 0, "worker-env: in step with gascity main"},
		{"gascity re-pinned", rbeTestStaleManifest, stalePin, "", 1,
			"::error title=Bazel lanes stopped before queueing::beads' worker-env pin is out of step with gascity main, so rbe-west has no worker for this run's remote actions (mode remote): copy tools/rbe/worker-env.txt and the worker-env pin in platforms/BUILD.bazel from gastownhall/gascity main"},
		{"gascity unreachable", rbeTestStaleManifest, stalePin, "file:///nonexistent-gascity", 0,
			"::warning title=rbe worker-env not checked::could not compare beads' worker-env pin with gascity main (exit 2); proceeding in mode remote."},
	} {
		env := []string{"MODE=remote", "GITHUB_STEP_SUMMARY=" + filepath.Join(t.TempDir(), "summary")}
		if c.url != "" {
			env = append(env, "WORKER_ENV_SYNC_URL="+c.url)
		}
		code, out := f.run(t, f.beads(t, c.manifest, c.pin), check.Run, env...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("rbe preflight, %s: exit %d, want %d with %q:\n%s", c.name, code, c.code, c.want, out)
		}
	}
}

// TestNightlyWorkerEnvSyncOpensIssue: nightly's check, out of step, opens a
// ci/infra issue with the diff (or, with one open, updates its body and
// comments with the run), with GITHUB_TOKEN's issues: write scoped to that
// job and the token in that one step; the rest of nightly.yml reads only.
// gascity unreachable fails the job without an issue.
func TestNightlyWorkerEnvSyncOpensIssue(t *testing.T) {
	nightly := readCIWorkflow(t, "nightly.yml")
	var top struct {
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/nightly.yml")), &top); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("nightly.yml permissions = %v, want contents: read", top.Permissions)
	}
	for name, job := range nightly.Jobs {
		want := any(nil)
		if name == nightlyWorkerEnvJob {
			want = map[string]any{"contents": "read", "issues": "write"}
		}
		if !reflect.DeepEqual(job.Permissions, want) {
			t.Errorf("nightly.yml job %s permissions = %v, want %v", name, job.Permissions, want)
		}
	}
	job := nightly.job(t, nightlyWorkerEnvJob)
	if len(job.Steps) != 3 {
		t.Fatalf("nightly.yml %s has %d steps, want checkout, check, issue", nightlyWorkerEnvJob, len(job.Steps))
	}
	check, issue := job.Steps[1], job.Steps[2]
	if check.ID != "sync" || check.If != "" || !reflect.DeepEqual(check.Env, map[string]string{"WORKER_ENV_SYNC_REPORT": nightlyWorkerEnvReport}) {
		t.Errorf("nightly check step: id %q, if %q, env %v; want id sync, no if, WORKER_ENV_SYNC_REPORT=%s", check.ID, check.If, check.Env, nightlyWorkerEnvReport)
	}
	wantIssueEnv := map[string]string{
		"GH_TOKEN": "${{ github.token }}",
		"REPORT":   nightlyWorkerEnvReport,
		"RUN_URL":  "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
	}
	if issue.If != "${{ failure() && steps.sync.outputs.rc == '1' }}" || !reflect.DeepEqual(issue.Env, wantIssueEnv) || !strings.Contains(issue.Run, rbeWorkerEnvSyncIssue) {
		t.Errorf("nightly issue step: if %q, env %v, run %q; want only on rc 1, env %v, running %s", issue.If, issue.Env, issue.Run, wantIssueEnv, rbeWorkerEnvSyncIssue)
	}
	for name, j := range nightly.Jobs {
		for i, step := range j.Steps {
			if (name != nightlyWorkerEnvJob || i != 2) && strings.Contains(fmt.Sprint(step.Env, step.With, step.Run), "github.token") {
				t.Errorf("nightly.yml job %s step %d uses github.token; only the issue step may", name, i)
			}
		}
	}

	// The check step's rc output: 1 out of step (the issue step runs), 2
	// unknown (it does not), 0 in step.
	f := newRBEWorkerEnvFixture(t)
	stalePin := rbeTestPin(rbeTestStaleManifest)
	for _, c := range []struct {
		name, manifest, pin, url, rc string
	}{
		{"in step", f.manifest, f.pin, "", "0"},
		{"out of step", rbeTestStaleManifest, stalePin, "", "1"},
		{"gascity unreachable", f.manifest, f.pin, "file:///nonexistent-gascity", "2"},
	} {
		dir := t.TempDir()
		output := filepath.Join(dir, "output")
		report := filepath.Join(dir, "report.md")
		env := []string{"GITHUB_OUTPUT=" + output, "WORKER_ENV_SYNC_REPORT=" + report}
		if c.url != "" {
			env = append(env, "WORKER_ENV_SYNC_URL="+c.url)
		}
		code, out := f.run(t, f.beads(t, c.manifest, c.pin), check.Run, env...)
		got, _ := os.ReadFile(output)
		if fmt.Sprint(code) != c.rc || string(got) != "rc="+c.rc+"\n" {
			t.Errorf("nightly check, %s: exit %d, output %q; want exit and rc %s\n%s", c.name, code, got, c.rc, out)
		}
		if r, _ := os.ReadFile(report); (c.rc == "1") != strings.Contains(string(r), "### rbe worker-env out of step with gascity") {
			t.Errorf("nightly check, %s: report %q", c.name, r)
		}
	}

	// The issue step, against a stand-in gh that logs its calls and lists
	// the open ci/infra issues it is given.
	const ghStub = `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_LOG"
for a in "$@"; do
	case "$prev" in --body-file) cp "$a" "$GH_LOG.body" ;; esac
	prev=$a
done
case "$1 $2" in
	"issue list") jq -r "$(for a in "$@"; do [ "$p" = --jq ] && echo "$a"; p=$a; done)" <<<"$GH_ISSUES" ;;
esac
`
	requireHostTool(t, "jq")
	title := "rbe worker-env out of step with gascity main"
	for _, c := range []struct {
		name, issues string
		want         []string
	}{
		{"no open issue", `[{"number":7,"title":"something else"}]`, []string{
			"issue create --repo gastownhall/beads --title " + title + " --label ci/infra --body-file ",
		}},
		{"open issue", `[{"number":7,"title":"something else"},{"number":42,"title":"` + title + `"}]`, []string{
			"issue edit 42 --repo gastownhall/beads --body-file ",
			"issue comment 42 --repo gastownhall/beads --body Still out of step with gascity main: https://github.com/gastownhall/beads/actions/runs/9",
		}},
	} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		rbeTestWrite(t, filepath.Join(bin, "gh"), ghStub, 0o755)
		report := filepath.Join(dir, "report.md")
		rbeTestWrite(t, report, "### rbe worker-env out of step with gascity\n\nthe diff\n", 0o644)
		log := filepath.Join(dir, "gh.log")
		code, out := f.run(t, f.beads(t, f.manifest, f.pin), issue.Run,
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GH_LOG="+log, "GH_ISSUES="+c.issues,
			"GITHUB_REPOSITORY=gastownhall/beads", "REPORT="+report, "RUN_URL=https://github.com/gastownhall/beads/actions/runs/9")
		calls, _ := os.ReadFile(log)
		body, _ := os.ReadFile(log + ".body")
		if code != 0 {
			t.Errorf("issue step, %s: exit %d\n%s", c.name, code, out)
		}
		for _, want := range c.want {
			if !strings.Contains(string(calls), want) {
				t.Errorf("issue step, %s: gh calls lack %q:\n%s", c.name, want, calls)
			}
		}
		if strings.Contains(string(calls), "issue create") == (c.name == "open issue") {
			t.Errorf("issue step, %s: gh calls:\n%s", c.name, calls)
		}
		if !strings.Contains(string(body), "the diff") || !strings.Contains(string(body), "https://github.com/gastownhall/beads/actions/runs/9") {
			t.Errorf("issue step, %s: body:\n%s", c.name, body)
		}
	}
}
