package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// rbe-fork (bazel.yml modes fork-ro and fork-rw): fork and Dependabot PR
// lanes get a short-lived client certificate from rbe-west's rbe-fork-mint
// (infra nativelink-cas/west README "rbe-fork"). setup-bazel makes a key and
// a CSR (fork-credential.sh key), uploads the CSR as an artifact of the run,
// asks the mint for a certificate naming that artifact (fork-credential.sh
// cert) and writes an rc for rbe-fork (write-bazelrc.sh). These tests run
// the real scripts against a stand-in mint (bazelTestMintCertStub signs the
// CSR with a throwaway CA); nothing reaches the network.

const (
	forkCredentialScript = setupBazelActionDir + "/fork-credential.sh"
	// The only endpoint fork-credential.sh accepts from the mint, and the
	// instances per tier (the mint's INSTANCE map).
	rbeForkEndpoint = "grpcs://rbe-fork.ops.gascity.com:8444"
	rbeForkMintURL  = "https://rbe-mint.ops.gascity.com:8444"
)

var rbeForkInstance = map[string]string{"ro": "oss-fork", "rw": "oss"}

// bazelTestMintCertStub stands in for curl in fork-credential.sh cert: it
// parses the arguments that script passes, logs the URL and body (and the
// timeouts on a "timeouts" line), and answers as BAZEL_TEST_MINT_CERT says: ro / rw (sign the CSR at
// BAZEL_TEST_CSR, as the mint would after downloading the artifact),
// ro-as-rw (tier rw with instance oss-fork), evil-endpoint, an HTTP status
// with an error body, 000 (connection refused), or "<first>-then-<rest>"
// (the first call answers <first>).
const bazelTestMintCertStub = `#!/usr/bin/env bash
set -euo pipefail
d=$BAZEL_TEST_MINT_DIR
out= data= url= connect= max=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift 2 ;;
	--data) data=$2; shift 2 ;;
	--connect-timeout) connect=$2; shift 2 ;;
	--max-time) max=$2; shift 2 ;;
	-w | -H) shift 2 ;;
	-*) shift ;;
	*) url=$1; shift ;;
	esac
done
n=$(($(cat "$d/calls" 2>/dev/null || echo 0) + 1))
echo "$n" >"$d/calls"
printf 'curl %s %s\n' "$url" "$data" >>"$d/log"
printf 'timeouts connect=%s max=%s\n' "$connect" "$max" >>"$d/log"
answer=$BAZEL_TEST_MINT_CERT
case "$answer" in
*-then-*) if [ "$n" -eq 1 ]; then answer=${answer%%-then-*}; else answer=${answer#*-then-}; fi ;;
esac
reply() { printf '%s' "$2" >"$out"; printf '%s' "$1"; }
sign() {
	openssl x509 -req -in "$BAZEL_TEST_CSR" -CA "$d/ca.pem" -CAkey "$d/ca.key" -days 1 -set_serial 7 -out "$d/cert.pem" 2>/dev/null
	reply 200 "$(jq -cn --arg tier "$1" --arg instance "$2" --arg endpoint "${3:-grpcs://rbe-fork.ops.gascity.com:8444}" \
		--rawfile pem "$d/cert.pem" '{tier: $tier, instance: $instance, endpoint: $endpoint, cert_pem: $pem, serial: "7", not_after: "2026-10-03T12:00:00+00:00"}')"
}
case "$answer" in
ro) sign ro oss-fork ;;
rw) sign rw oss ;;
ro-as-rw) sign rw oss-fork ;;
evil-endpoint) sign ro oss-fork grpcs://elsewhere.example:443 ;;
000) echo "curl: (7) Failed to connect to rbe-mint.ops.gascity.com port 8444" >&2; exit 7 ;;
*) reply "$answer" "{\"error\": \"stub answer $answer\"}" ;;
esac
`

// forkMint: a throwaway CA and the stub's state directory.
type forkMint struct {
	dir, bin string
}

func newForkMint(t *testing.T) forkMint {
	t.Helper()
	requireHostTool(t, "openssl")
	requireHostTool(t, "jq")
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"curl": bazelTestMintCertStub,
		// fork-credential.sh backs off between retries; the stub logs it.
		"sleep": "#!/bin/sh\necho \"sleep $*\" >>\"$BAZEL_TEST_MINT_DIR/log\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
		"-keyout", filepath.Join(dir, "ca.key"), "-out", filepath.Join(dir, "ca.pem"), "-days", "1", "-subj", "/CN=test fork CA")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("test CA: %v\n%s", err, b)
	}
	return forkMint{dir: dir, bin: bin}
}

func (m forkMint) log(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(m.dir, "log"))
	return string(b)
}

func (m forkMint) reset(t *testing.T) {
	t.Helper()
	for _, f := range []string{"log", "calls"} {
		if err := os.RemoveAll(filepath.Join(m.dir, f)); err != nil {
			t.Fatal(err)
		}
	}
}

// runForkCredential runs fork-credential.sh with env (plus PATH with the
// stub first) and returns its $GITHUB_OUTPUT and combined log.
func runForkCredential(t *testing.T, m forkMint, arg string, env map[string]string) (map[string]string, string, error) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	cmd := exec.Command("bash", filepath.Join(sourceRepoRoot(t), forkCredentialScript), arg)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + m.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out, "BAZEL_TEST_MINT_DIR=" + m.dir}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(out)
	return parseGitHubOutput(string(data)), string(b), err
}

// forkMintSleeps: the stub sleep's arguments in the mint log, in order.
func forkMintSleeps(log string) string {
	var got []string
	for _, line := range strings.Split(log, "\n") {
		if n, ok := strings.CutPrefix(line, "sleep "); ok {
			got = append(got, n)
		}
	}
	return strings.Join(got, " ")
}

func parseGitHubOutput(data string) map[string]string {
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

// The CSR artifact's name is the mint's source for the CN's job and attempt
// (infra rbe-fork-mint.py ARTIFACT_RE); the job id must fit its JOB_RE.
var (
	rbeForkArtifactRE = regexp.MustCompile(`^rbe-csr-(?P<job>[a-z0-9][a-z0-9_-]{0,39})-(?P<attempt>[0-9]{1,3})-[0-9a-f]{16,32}$`)
	rbeForkJobRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
)

// fork-credential.sh, both halves, against the mint's client contract: the
// key (EC P-256, PKCS#8, 0600, outside the workspace), the CSR artifact's
// name, the request body (exactly the mint's six keys), the endpoint pin, the
// tier/instance pairing, the tier check against the rbe job's, the retries
// (429, 502 and connection failures only; backoff 10, 20, 30 s, none after
// the last attempt), the timeouts (a closed gate drops the connection) and
// the refusals.
func TestSetupBazelForkCredential(t *testing.T) {
	requireHostTool(t, "bash")
	m := newForkMint(t)
	base := func(secret string) map[string]string {
		return map[string]string{
			"BAZEL_CI_SECRET_DIR": secret, "RUNNER_TEMP": filepath.Dir(secret), "BAZEL_FORK_REMOTE": "true",
			"GITHUB_JOB": "bazel-test", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_RUN_ID": "4242",
			"GITHUB_REPOSITORY": "gastownhall/beads", "RBE_FORK_PR": "7123",
		}
	}

	// key: nothing at all unless BAZEL_FORK_REMOTE=true.
	for _, v := range []string{"", "false", "TRUE"} {
		secret := filepath.Join(t.TempDir(), "secret")
		env := base(secret)
		env["BAZEL_FORK_REMOTE"] = v
		out, logs, err := runForkCredential(t, m, "key", env)
		if err != nil || len(out) != 0 {
			t.Errorf("key with BAZEL_FORK_REMOTE=%q: outputs %v, err %v\n%s", v, out, err, logs)
		}
		if _, err := os.Stat(secret); !os.IsNotExist(err) {
			t.Errorf("key with BAZEL_FORK_REMOTE=%q created %s", v, secret)
		}
	}

	key := func(t *testing.T, env map[string]string) map[string]string {
		t.Helper()
		out, logs, err := runForkCredential(t, m, "key", env)
		if err != nil {
			t.Fatalf("key: %v\n%s", err, logs)
		}
		return out
	}
	secret := filepath.Join(t.TempDir(), "secret")
	out := key(t, base(secret))
	if want := []string{"artifact-name", "csr"}; !sameKeys(out, want) {
		t.Fatalf("key outputs %v, want exactly %v", out, want)
	}
	name := rbeForkArtifactRE.FindStringSubmatch(out["artifact-name"])
	if name == nil || name[1] != "bazel-test" || name[2] != "2" {
		t.Errorf("artifact name %q: want rbe-csr-<job bazel-test>-<attempt 2>-<hex> (the mint's ARTIFACT_RE)", out["artifact-name"])
	}
	if filepath.Base(out["csr"]) != "csr.pem" || strings.HasPrefix(out["csr"], secret) {
		t.Errorf("csr %q: want csr.pem (the one file the mint accepts in the archive) outside the secret dir", out["csr"])
	}
	keyFile := filepath.Join(secret, "fork.key")
	if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("key file %s: %v, mode %v; want 0600", keyFile, err, st)
	}
	if b, _ := os.ReadFile(keyFile); !strings.HasPrefix(string(b), "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("key file is not PKCS#8 (Bazel's Netty TLS refuses SEC1 keys):\n%.40s", b)
	}
	if st, err := os.Stat(secret); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("secret dir %s: %v, mode %v; want 0700", secret, err, st)
	}
	csrText, err := exec.Command("openssl", "req", "-in", out["csr"], "-noout", "-text", "-verify").CombinedOutput()
	if err != nil || !strings.Contains(string(csrText), "prime256v1") && !strings.Contains(string(csrText), "P-256") {
		t.Errorf("CSR is not a self-signed EC P-256 request: %v\n%s", err, csrText)
	}
	// Two runs never share an artifact name (an attempt re-runs failed
	// jobs with fresh CSRs).
	if again := key(t, base(filepath.Join(t.TempDir(), "secret"))); again["artifact-name"] == out["artifact-name"] {
		t.Errorf("two keys share the artifact name %q", out["artifact-name"])
	}
	// A job id the mint's pattern refuses fails here, before any upload.
	for _, job := range []string{"Bazel_Test", "-x", strings.Repeat("a", 41), ""} {
		env := base(filepath.Join(t.TempDir(), "secret"))
		env["GITHUB_JOB"] = job
		if rbeForkJobRE.MatchString(job) {
			t.Fatalf("test job %q fits the mint's pattern", job)
		}
		if out, logs, err := runForkCredential(t, m, "key", env); err == nil {
			t.Errorf("key with GITHUB_JOB %q succeeded: %v\n%s", job, out, logs)
		}
	}
	// Every bazel.yml job that runs setup-bazel has an id the mint accepts.
	for jobName := range readCIWorkflow(t, bazelWorkflowName).Jobs {
		if !rbeForkJobRE.MatchString(jobName) {
			t.Errorf("%s job id %q does not fit the mint's job pattern", bazelWorkflowName, jobName)
		}
	}

	cert := func(t *testing.T, answer, tier string) (map[string]string, string, error) {
		t.Helper()
		m.reset(t)
		env := base(secret)
		env["ARTIFACT_ID"] = "99"
		env["RBE_FORK_TIER"] = tier
		env["BAZEL_TEST_MINT_CERT"] = answer
		env["BAZEL_TEST_CSR"] = out["csr"]
		return runForkCredential(t, m, "cert", env)
	}
	wantBody := map[string]any{"repo": "beads", "run_id": 4242.0, "run_attempt": 2.0, "pr": 7123.0, "job": "bazel-test", "artifact_id": 99.0}
	for _, tier := range []string{"ro", "rw"} {
		for _, answer := range []string{tier, "429-then-" + tier, "502-then-" + tier, "000-then-" + tier} {
			got, logs, err := cert(t, answer, tier)
			if err != nil {
				t.Errorf("cert %s (mint %s): %v\n%s", tier, answer, err, logs)
				continue
			}
			want := map[string]string{"cert": filepath.Join(secret, "fork.crt"), "key": keyFile, "endpoint": rbeForkEndpoint,
				"instance": rbeForkInstance[tier], "tier": tier}
			if !equalMaps(got, want) {
				t.Errorf("cert %s (mint %s): outputs %v, want %v", tier, answer, got, want)
			}
			if st, err := os.Stat(want["cert"]); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("cert file: %v %v; want 0600", err, st)
			}
			calls := strings.Count(m.log(t), "curl ")
			if wantCalls := map[bool]int{false: 1, true: 2}[strings.Contains(answer, "-then-")]; calls != wantCalls {
				t.Errorf("mint %s: %d requests, want %d:\n%s", answer, calls, wantCalls, m.log(t))
			}
			if got, want := forkMintSleeps(m.log(t)), map[bool]string{false: "", true: "10"}[strings.Contains(answer, "-then-")]; got != want {
				t.Errorf("mint %s: slept %q, want %q:\n%s", answer, got, want, m.log(t))
			}
			if got := strings.Count(m.log(t), "timeouts connect=5 max=60\n"); got != calls {
				t.Errorf("mint %s: %d of %d requests with --connect-timeout 5 --max-time 60:\n%s", answer, got, calls, m.log(t))
			}
			for _, line := range strings.Split(strings.TrimSpace(m.log(t)), "\n") {
				if !strings.HasPrefix(line, "curl ") {
					continue
				}
				url, body, _ := strings.Cut(strings.TrimPrefix(line, "curl "), " ")
				var gotBody map[string]any
				if url != rbeForkMintURL+"/v1/cert" || json.Unmarshal([]byte(body), &gotBody) != nil || !equalAny(gotBody, wantBody) {
					t.Errorf("request %s %s; want POST %s/v1/cert with %v", url, body, rbeForkMintURL, wantBody)
				}
			}
		}
	}
	// Answers that are not a certificate for this run's tier: the step fails
	// (a fork lane in a fork mode is required to run remotely), after
	// retrying only what may pass on its own.
	for _, c := range []struct {
		answer, tier string
		calls        int
		why          string
	}{
		{"403", "ro", 1, "rbe-fork mint refused (HTTP 403)"},
		{"409", "ro", 1, "rbe-fork mint refused (HTTP 409)"},
		{"429", "ro", 4, "rbe-fork mint refused (HTTP 429)"},
		{"503", "rw", 1, "rbe-fork mint refused (HTTP 503)"},
		{"502", "ro", 4, "rbe-fork mint refused (HTTP 502)"},
		{"000", "ro", 4, "rbe-fork mint refused (HTTP 000)"},
		{"rw", "ro", 1, "rbe-fork tier changed"},
		{"ro", "rw", 1, "rbe-fork tier changed"},
		{"ro-as-rw", "rw", 1, "returned tier 'rw' instance 'oss-fork'"},
		{"evil-endpoint", "ro", 1, "returned endpoint 'grpcs://elsewhere.example:443'"},
	} {
		got, logs, err := cert(t, c.answer, c.tier)
		if err == nil || !strings.Contains(logs, c.why) {
			t.Errorf("mint %s for tier %s: outputs %v, err %v; want a failure saying %q\n%s", c.answer, c.tier, got, err, c.why, logs)
		}
		if len(got) != 0 {
			t.Errorf("mint %s for tier %s: outputs %v, want none (write-bazelrc.sh must not see a partial set)", c.answer, c.tier, got)
		}
		if calls := strings.Count(m.log(t), "curl "); calls != c.calls {
			t.Errorf("mint %s: %d requests, want %d", c.answer, calls, c.calls)
		}
		// Backoff between attempts only: nothing after the last one.
		if got, want := forkMintSleeps(m.log(t)), map[int]string{1: "", 4: "10 20 30"}[c.calls]; got != want {
			t.Errorf("mint %s: slept %q, want %q", c.answer, got, want)
		}
	}
	// cert needs every fact it sends.
	for _, k := range []string{"ARTIFACT_ID", "RBE_FORK_PR", "RBE_FORK_TIER", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_JOB", "GITHUB_REPOSITORY"} {
		m.reset(t)
		env := base(secret)
		env["ARTIFACT_ID"], env["RBE_FORK_TIER"], env["BAZEL_TEST_MINT_CERT"], env["BAZEL_TEST_CSR"] = "99", "ro", "ro", out["csr"]
		delete(env, k)
		if got, logs, err := runForkCredential(t, m, "cert", env); err == nil {
			t.Errorf("cert without %s succeeded: %v\n%s", k, got, logs)
		}
	}
}

// write-bazelrc.sh in a fork mode: exactly the rbe-fork lines (the mint's
// endpoint, instance and short-lived certificate, nothing uploaded from the
// runner), and the three remote modes are mutually exclusive.
func TestSetupBazelRCWriterForkMode(t *testing.T) {
	bash := requireHostTool(t, "bash")
	script := filepath.Join(sourceRepoRoot(t), setupBazelActionDir, "write-bazelrc.sh")
	run := func(t *testing.T, env map[string]string) (map[string]string, string, string, error) {
		t.Helper()
		dir := t.TempDir()
		ws := filepath.Join(dir, "ws")
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(dir, "secret")
		cmd := exec.Command(bash, script)
		cmd.Dir = ws
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_WORKSPACE=" + ws, "GITHUB_OUTPUT=" + filepath.Join(dir, "out"),
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(dir, "cache"), "BAZEL_CI_SECRET_DIR=" + secret,
			"RBE_CACHE_PROBE_URL=" + refusedProbeURL}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		b, err := cmd.CombinedOutput()
		o, _ := os.ReadFile(filepath.Join(dir, "out"))
		rc, _ := os.ReadFile(filepath.Join(secret, "ci.bazelrc"))
		return parseGitHubOutput(string(o)), string(rc), string(b), err
	}
	fork := func(instance string) map[string]string {
		return map[string]string{"RBE_FORK_CERT_FILE": "/tmp/s/fork.crt", "RBE_FORK_KEY_FILE": "/tmp/s/fork.key",
			"RBE_FORK_ENDPOINT": rbeForkEndpoint, "RBE_FORK_INSTANCE": instance}
	}
	for _, instance := range []string{"oss-fork", "oss"} {
		out, rc, logs, err := run(t, fork(instance))
		if err != nil {
			t.Fatalf("%s: %v\n%s", instance, err, logs)
		}
		var remoteLines []string
		for _, line := range strings.Split(rc, "\n") {
			if strings.HasPrefix(line, "build") {
				remoteLines = append(remoteLines, line)
			}
		}
		want := []string{
			"build:remote-exec --remote_executor=" + rbeForkEndpoint,
			"build:remote-exec --tls_client_certificate=/tmp/s/fork.crt",
			"build:remote-exec --tls_client_key=/tmp/s/fork.key",
			"build:remote-exec --remote_instance_name=" + instance,
			"build:remote-exec --noremote_upload_local_results",
			"build:remote-exec --remote_max_connections=8",
			"build --config=remote-exec",
		}
		if strings.Join(remoteLines, "\n") != strings.Join(want, "\n") || out["remote"] != "true" || out["cache"] != "false" {
			t.Errorf("%s: outputs %v, build lines:\n%s\nwant:\n%s", instance, out, strings.Join(remoteLines, "\n"), strings.Join(want, "\n"))
		}
		if strings.Contains(logs, "::add-mask::") || !strings.Contains(logs, "setup-bazel: remote execution on rbe-fork (instance "+instance) {
			t.Errorf("%s: log %q", instance, logs)
		}
	}
	// Exclusive with the secrets and the fork cache; all four or none; only
	// the two instances the mint hands out.
	secrets := map[string]string{"BAZEL_REMOTE_EXECUTOR": "grpcs://farm.invalid:443", "RBE_TLS_CERT": "x", "RBE_TLS_KEY": "y"}
	bad := map[string]map[string]string{
		"fork + secrets":     mergeMaps(fork("oss-fork"), secrets),
		"fork + one secret":  mergeMaps(fork("oss-fork"), map[string]string{"BAZEL_REMOTE_EXECUTOR": "grpcs://farm.invalid:443"}),
		"fork + fork cache":  mergeMaps(fork("oss"), map[string]string{"BAZEL_FORK_CACHE": "true"}),
		"instance other":     fork("oss-private"),
		"instance empty":     mergeMaps(fork("oss"), map[string]string{"RBE_FORK_INSTANCE": ""}),
		"no endpoint":        mergeMaps(fork("oss"), map[string]string{"RBE_FORK_ENDPOINT": ""}),
		"no key":             mergeMaps(fork("oss"), map[string]string{"RBE_FORK_KEY_FILE": ""}),
		"instance alone":     {"RBE_FORK_INSTANCE": "oss-fork"},
		"cert alone":         {"RBE_FORK_CERT_FILE": "/tmp/s/fork.crt"},
		"instance + cache":   {"RBE_FORK_INSTANCE": "oss-fork", "BAZEL_FORK_CACHE": "true"},
		"endpoint + secrets": mergeMaps(secrets, map[string]string{"RBE_FORK_ENDPOINT": rbeForkEndpoint}),
	}
	var names []string
	for n := range bad {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if out, rc, logs, err := run(t, bad[n]); err == nil {
			t.Errorf("%s: write-bazelrc.sh succeeded: %v\n%s\n%s", n, out, rc, logs)
		}
	}
}

// setupBazelForkSim runs setup-bazel's fork steps (action.yml's fork-key,
// fork-csr and fork-cert, by their ids, ifs and env) for one lane, the way
// the composite action runs them with the caller's step env, and returns
// the env its rc step then gets (caller env plus the evaluated RBE_FORK_*).
// The CSR upload is simulated (artifact id 99); the mint is the stub,
// answering mintAnswer. Every mode runs it: fork-credential.sh key must do
// nothing outside the fork modes.
func setupBazelForkSim(t *testing.T, m forkMint, runnerTemp, lane string, caller map[string]string, mintAnswer string) map[string]string {
	t.Helper()
	root := sourceRepoRoot(t)
	steps := map[string]ciWorkflowStep{}
	for _, s := range readSetupBazelAction(t).Runs.Steps {
		if s.ID != "" {
			steps[s.ID] = s
		}
	}
	outputs := map[string]map[string]string{}
	ref := regexp.MustCompile(`^\$\{\{ steps\.([a-z-]+)\.outputs\.([a-z-]+) \}\}$`)
	eval := func(expr string) string {
		if expr == "${{ runner.temp }}/bazel-ci-secret" {
			return filepath.Join(runnerTemp, "bazel-ci-secret")
		}
		if expr == "${{ runner.temp }}/bazel-ci-cache" {
			return filepath.Join(runnerTemp, "bazel-ci-cache")
		}
		if m := ref.FindStringSubmatch(expr); m != nil {
			return outputs[m[1]][m[2]]
		}
		if !strings.Contains(expr, "${{") {
			return expr
		}
		t.Fatalf("setup-bazel expression %q: teach setupBazelForkSim how GitHub evaluates it", expr)
		return ""
	}
	cond := regexp.MustCompile(`^steps\.([a-z-]+)\.outputs\.([a-z-]+) != ''$`)
	runs := func(id string) bool {
		s := steps[id]
		if s.If == "" {
			return true
		}
		c := cond.FindStringSubmatch(s.If)
		if c == nil {
			t.Fatalf("setup-bazel %s if %q: teach setupBazelForkSim how GitHub evaluates it", id, s.If)
		}
		return outputs[c[1]][c[2]] != ""
	}
	run := func(id string, extra map[string]string) {
		t.Helper()
		s, ok := steps[id]
		if !ok {
			t.Fatalf("setup-bazel has no step %s", id)
		}
		dir := t.TempDir()
		out := filepath.Join(dir, "out")
		cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", s.Run)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + m.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out,
			"GITHUB_ACTION_PATH=" + filepath.Join(root, setupBazelActionDir), "RUNNER_TEMP=" + runnerTemp,
			"GITHUB_JOB=" + lane, "GITHUB_RUN_ID=4242", "GITHUB_RUN_ATTEMPT=1", "GITHUB_REPOSITORY=gastownhall/beads",
			"BAZEL_TEST_MINT_DIR=" + m.dir}
		for k, v := range caller {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+eval(v))
		}
		for k, v := range extra {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: setup-bazel step %s failed: %v\n%s", lane, id, err, b)
		}
		data, _ := os.ReadFile(out)
		outputs[id] = parseGitHubOutput(string(data))
	}

	run("fork-key", nil)
	fork := caller["BAZEL_FORK_REMOTE"] == "true"
	if got := runs("fork-csr"); got != fork || runs("fork-cert") != fork {
		t.Fatalf("%s: BAZEL_FORK_REMOTE %q, but the CSR upload / certificate steps run = %v / %v", lane, caller["BAZEL_FORK_REMOTE"], got, runs("fork-cert"))
	}
	if fork {
		csr := steps["fork-csr"]
		if !strings.HasPrefix(csr.Uses, "actions/upload-artifact@") || eval(csr.With["path"]) != outputs["fork-key"]["csr"] ||
			eval(csr.With["name"]) != outputs["fork-key"]["artifact-name"] || csr.With["compression-level"] != "0" {
			t.Fatalf("%s: the CSR upload %+v does not upload fork-key's csr under its artifact name, stored", lane, csr)
		}
		outputs["fork-csr"] = map[string]string{"artifact-id": "99"}
		run("fork-cert", map[string]string{"BAZEL_TEST_MINT_CERT": mintAnswer, "BAZEL_TEST_CSR": outputs["fork-key"]["csr"]})
	}
	env := map[string]string{}
	for k, v := range caller {
		env[k] = v
	}
	for k, v := range steps["rc"].Env {
		if strings.HasPrefix(k, "RBE_FORK_") {
			env[k] = eval(v)
		}
	}
	return env
}

func sameKeys(m map[string]string, want []string) bool {
	var got []string
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	return strings.Join(got, ",") == strings.Join(want, ",")
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func equalAny(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func mergeMaps(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
