package scripts_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Mode cache, end to end, for every way a bazel.yml run can start: the rbe
// job's real decision step, each lane's `if:` and setup-bazel env (GitHub's
// expressions, evaluated) and the rc write-bazelrc.sh then writes. A cache
// run (fork and Dependabot PRs, rbe=cache, a missing executor secret) must
// reach the rc with --config=fork-cache and nothing else remote: no
// executor, no TLS material, no instance, no upload. That holds even where
// the run could read the secrets (an rbe=cache dispatch on the base repo,
// and, adversarially, a fork PR whose secrets GitHub would never pass). Its
// lanes are the local ones plus bazel-integration; the remote-only tiers
// skip, and pr.yml's gate (bazel-gate.sh) accepts exactly those skips.
func TestBazelCacheModeReachesTheRC(t *testing.T) {
	bash := requireHostTool(t, "bash")
	root := sourceRepoRoot(t)
	workflow := readCIWorkflow(t, bazelWorkflowName)
	decide := workflow.job(t, bazelRBEJobName).Steps[0]
	script := filepath.Join(root, setupBazelActionDir, "write-bazelrc.sh")

	pem := func(kind string) string {
		return base64.StdEncoding.EncodeToString([]byte("-----BEGIN " + kind + "-----\nMIIBfake\n-----END " + kind + "-----\n"))
	}
	secrets := map[string]string{
		"RBE_WEST_EXECUTOR": "grpcs://" + "farm.invalid:443",
		"RBE_TLS_CERT":      pem("CERTIFICATE"),
		"RBE_TLS_KEY":       pem("PRIVATE KEY"),
		"RBE_TLS_CA":        pem("CERTIFICATE"),
	}

	// setup-bazel's env values: `needs.rbe.outputs.X == 'v' && RHS || ''`,
	// RHS a secret or a literal. Anything else fails the test, so the
	// simulation cannot drift from the workflow.
	setupExpr := regexp.MustCompile(`^\$\{\{ needs\.rbe\.outputs\.(enabled|mode) == '([a-z]+)' && (?:secrets\.([A-Z_]+)|'([^']*)') \|\| '' \}\}$`)
	evalSetup := func(t *testing.T, expr, mode, enabled string, haveSecrets bool) string {
		t.Helper()
		m := setupExpr.FindStringSubmatch(expr)
		if m == nil {
			t.Fatalf("setup-bazel env %q: teach TestBazelCacheModeReachesTheRC how GitHub evaluates it", expr)
		}
		got := map[string]string{"enabled": enabled, "mode": mode}[m[1]]
		if !strings.EqualFold(got, m[2]) {
			return ""
		}
		if m[3] != "" {
			if !haveSecrets {
				return ""
			}
			v, ok := secrets[m[3]]
			if !ok {
				t.Fatalf("setup-bazel env reads secrets.%s, which no caller passes", m[3])
			}
			return v
		}
		return m[4]
	}

	writeRC := func(t *testing.T, env map[string]string) (outputs, rc string, files []string, logs string, err error) {
		t.Helper()
		dir := t.TempDir()
		ws := filepath.Join(dir, "ws")
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		secretDir := filepath.Join(dir, "secret")
		cmd := exec.Command(bash, script)
		cmd.Dir = ws
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"GITHUB_WORKSPACE=" + ws,
			"GITHUB_OUTPUT=" + filepath.Join(dir, "out"),
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(dir, "cache"),
			"BAZEL_CI_SECRET_DIR=" + secretDir,
		}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		b, err := cmd.CombinedOutput()
		o, _ := os.ReadFile(filepath.Join(dir, "out"))
		r, _ := os.ReadFile(filepath.Join(secretDir, "ci.bazelrc"))
		entries, _ := os.ReadDir(secretDir)
		for _, e := range entries {
			files = append(files, e.Name())
		}
		return string(o), string(r), files, string(b), err
	}

	type start struct {
		name   string
		f      rbeFacts
		with   map[string]string
		secret bool // the run can read the RBE secrets
	}
	var starts []start
	farm := map[string]string{"fork-farm": "authorized", "checkout-sha": "0123456789abcdef0123456789abcdef01234567"}
	for _, rbeVar := range []string{"true", ""} {
		v := "var=" + map[bool]string{true: "on", false: "unset"}[rbeVar != ""]
		pr := func(fork, dependabot bool, secret bool) rbeFacts {
			s := ""
			if secret {
				s = "x"
			}
			return rbeFacts{event: "pull_request", rbeVar: rbeVar, secret: s, fork: fork, dependabot: dependabot}
		}
		starts = append(starts,
			start{"same-repo PR, " + v, pr(false, false, true), nil, true},
			start{"same-repo PR without the secret, " + v, pr(false, false, false), nil, false},
			start{"Dependabot PR, " + v, pr(false, true, false), nil, false},
			start{"fork PR, " + v, pr(true, false, false), nil, false},
			start{"fork PR that somehow has the secrets, " + v, pr(true, false, true), nil, true},
			start{"fork PR passing fork-farm from pull_request, " + v, pr(true, false, true), farm, true},
			start{"push to main, " + v, rbeFacts{event: "push", rbeVar: rbeVar, secret: "x"}, nil, true},
			start{"dispatch rbe=cache, " + v, rbeFacts{event: "workflow_dispatch", rbeVar: rbeVar, secret: "x"}, map[string]string{"rbe": "cache"}, true},
			start{"dispatch rbe=off, " + v, rbeFacts{event: "workflow_dispatch", rbeVar: rbeVar, secret: "x"}, map[string]string{"rbe": "off"}, true},
			start{"bazel-farm authorized fork, " + v, rbeFacts{event: "pull_request_target", rbeVar: rbeVar, secret: "x", fork: true}, farm, true},
			start{"bazel-farm authorized fork without the secret, " + v, rbeFacts{event: "pull_request_target", rbeVar: rbeVar, fork: true}, farm, false},
		)
	}

	seen := map[string]bool{}
	for _, s := range starts {
		t.Run(s.name, func(t *testing.T) {
			out, err := runDecisionStep(t, decide, s.f, s.with)
			if err != nil {
				t.Fatal(err)
			}
			mode, enabled := out["mode"], out["enabled"]
			seen[mode] = true
			// Forks outside bazel-farm.yml's authorized call and rbe=cache
			// are mode cache whatever else holds; Dependabot (no secrets)
			// too unless the farm switch is off (mode skip).
			authorized := s.f.event == "pull_request_target" && s.with["fork-farm"] == "authorized"
			dependabot := s.f.dependabot && s.f.rbeVar == "true"
			if s.with["rbe"] != "off" && !authorized && (s.f.fork || dependabot || s.with["rbe"] == "cache") && mode != "cache" {
				t.Errorf("mode = %s, want cache", mode)
			}
			if mode == "remote" && (!s.secret || s.f.fork && !authorized) {
				t.Errorf("mode remote without the secret or for an unauthorized fork")
			}

			var lanes []string
			for name, job := range workflow.Jobs {
				if name == bazelRBEJobName {
					continue
				}
				runs := bazelLaneRunModes(t, name, job.If, s.with)[mode]
				switch {
				case mode == "cache" && name == bazelIntegJobName && !runs:
					t.Errorf("%s does not run in mode cache", name)
				case mode == "cache" && bazelRemoteOnlyJobs[name] && runs:
					t.Errorf("remote-only %s runs in mode cache", name)
				case mode == "local" && name == bazelIntegJobName && runs:
					t.Errorf("%s runs in mode local (no cache: a cold tagged race build)", name)
				}
				if !runs {
					continue
				}
				lanes = append(lanes, name)
				for _, step := range job.Steps {
					if step.Uses != "./"+setupBazelActionDir {
						continue
					}
					env := map[string]string{}
					for k, expr := range step.Env {
						env[k] = evalSetup(t, expr, mode, enabled, s.secret)
					}
					outputs, rc, files, logs, err := writeRC(t, env)
					if err != nil {
						t.Fatalf("%s: write-bazelrc.sh failed in mode %s: %v\n%s", name, mode, err, logs)
					}
					checkModeRC(t, name, mode, outputs, rc, files, logs)
				}
			}
			if mode == "skip" && len(lanes) != 0 {
				t.Errorf("mode skip runs %v", lanes)
			}
			// pr.yml's gate accepts as skipped exactly the gated lanes that
			// did not run (TestBazelGateSimulation runs the gate itself).
			ran := map[string]bool{}
			for _, l := range lanes {
				ran[l] = true
			}
			var wantSkips []string
			for lane, id := range bazelLaneGateIDs {
				// F3: package-mcp/package-npm skip only when the caller's
				// package-gates input is off, never because of rbe mode;
				// bazel-gate.sh knows nothing about them (its skip list is
				// mode-derived only), so this scenario's with (which never
				// sets package-gates here) must not expect them either.
				if bazelPackageJobs[lane] {
					continue
				}
				if !ran[lane] {
					wantSkips = append(wantSkips, id)
				}
			}
			if len(lanes) == 0 {
				wantSkips = append(wantSkips, bazelAggregateGateID)
			}
			cmd := exec.Command(bash, bazelGateScript, "skips")
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "BAZEL_RBE_MODE=" + mode, "BAZEL_RBE_ENABLED=" + enabled}
			b, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Fields(string(b))
			sort.Strings(got)
			sort.Strings(wantSkips)
			if strings.Join(got, " ") != strings.Join(wantSkips, " ") {
				t.Errorf("mode %s: gate skips %v, want %v (lanes run: %v)", mode, got, wantSkips, lanes)
			}
		})
	}
	for _, mode := range bazelRBEModes {
		if !seen[mode] {
			t.Errorf("no start reaches mode %s", mode)
		}
	}
}

// checkModeRC: what one lane's generated rc may hold in its mode.
func checkModeRC(t *testing.T, lane, mode, outputs, rc string, files []string, logs string) {
	t.Helper()
	has := func(s string) bool { return strings.Contains(rc, s) }
	// Every mode: the fetch hardening (key neutral, repository fetching only).
	for _, want := range []string{
		"\ncommon --repo_env=GOPROXY=https://proxy.golang.org|https://proxy.golang.org|direct\n",
		"\ncommon --http_timeout_scaling=2.0\n",
	} {
		if !has(want) {
			t.Errorf("%s (mode %s): rc lacks %q:\n%s", lane, mode, strings.TrimSpace(want), rc)
		}
	}
	credentialFree := func() {
		for _, bad := range []string{"remote_executor", "remote-exec", "tls_", "remote_instance_name", "--remote_upload_local_results", "--remote_cache", "--remote_header", "--bes_", "farm.invalid"} {
			if has(bad) {
				t.Errorf("%s (mode %s): rc carries %q:\n%s", lane, mode, bad, rc)
			}
		}
		if len(files) != 1 || files[0] != "ci.bazelrc" {
			t.Errorf("%s (mode %s): secret dir holds %v, want only ci.bazelrc (no key material)", lane, mode, files)
		}
		if strings.Contains(logs, "::add-mask::") {
			t.Errorf("%s (mode %s): masked something, so it saw a secret:\n%s", lane, mode, logs)
		}
	}
	switch mode {
	case "cache":
		credentialFree()
		if !has("\nbuild --config=fork-cache\n") || !strings.Contains(outputs, "remote=false") || !strings.Contains(outputs, "cache=true") {
			t.Errorf("%s (mode cache): outputs %q rc %q; want --config=fork-cache, remote=false, cache=true", lane, outputs, rc)
		}
	case "local":
		credentialFree()
		if has("fork-cache") || !strings.Contains(outputs, "cache=false") {
			t.Errorf("%s (mode local): outputs %q rc %q; want neither cache nor remote", lane, outputs, rc)
		}
	case "remote":
		if !has("\nbuild --config=remote-exec\n") || has("fork-cache") || !strings.Contains(outputs, "remote=true") {
			t.Errorf("%s (mode remote): outputs %q rc %q; want remote-exec and no fork-cache", lane, outputs, rc)
		}
	default:
		t.Errorf("%s runs in mode %q", lane, mode)
	}
}
