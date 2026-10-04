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
// run (fork and Dependabot PRs while rbe-fork is closed or unreachable,
// rbe=cache, a missing executor secret) must reach the rc with
// --config=fork-cache and nothing else remote: no executor, no TLS
// material, no instance, no upload. That holds even where the run could
// read the secrets (an rbe=cache dispatch on the base repo, and,
// adversarially, a fork PR whose secrets GitHub would never pass). Its
// lanes are the local ones plus bazel-integration; the remote-only tiers
// skip, and pr.yml's gate (bazel-gate.sh) accepts exactly those skips.
//
// The fork modes too (rbe-fork open, the mint answering ro or rw): every
// lane runs, setup-bazel's fork steps (setupBazelForkSim: key, CSR
// artifact, certificate from a stand-in mint) hand write-bazelrc.sh the
// minted certificate, and the rc executes on rbe-fork with instance
// oss-fork (ro) or oss (rw) and that certificate, never the CI secrets.
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
	evalSetup := func(t *testing.T, expr, mode, enabled, tier string, haveSecrets bool) string {
		t.Helper()
		switch expr {
		case bazelForkRemoteValue:
			if strings.HasPrefix(mode, "fork-") {
				return "true"
			}
			return ""
		case "${{ needs.rbe.outputs.tier }}":
			return tier
		case "${{ github.event.pull_request.number }}":
			return "7123" // every start that reaches a fork mode is a pull_request
		}
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

	// writeRC: write-bazelrc.sh as the rc step runs it, in runner.temp (the
	// fork steps put the key and certificate in the same secret dir).
	writeRC := func(t *testing.T, dir string, env map[string]string) (outputs, rc string, files []string, logs string, err error) {
		t.Helper()
		ws := filepath.Join(dir, "ws")
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		secretDir := filepath.Join(dir, "bazel-ci-secret")
		cmd := exec.Command(bash, script)
		cmd.Dir = ws
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"GITHUB_WORKSPACE=" + ws,
			"GITHUB_OUTPUT=" + filepath.Join(dir, "out"),
			"BAZEL_CI_CACHE_DIR=" + filepath.Join(dir, "bazel-ci-cache"),
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
	mint := newForkMint(t)
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
		// prMint: the same, with rbe-fork-mint's /v1/status answer.
		prMint := func(fork, dependabot, secret bool, answer string) rbeFacts {
			f := pr(fork, dependabot, secret)
			f.mint = answer
			return f
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
			// rbe-fork: the mint's answer decides fork and Dependabot runs.
			start{"fork PR, rbe-fork ro, " + v, prMint(true, false, false, "ro"), nil, false},
			start{"fork PR, rbe-fork rw, " + v, prMint(true, false, false, "rw"), nil, false},
			start{"fork PR, rbe-fork closed, " + v, prMint(true, false, false, "closed"), nil, false},
			start{"fork PR, rbe-fork rw tier closed, " + v, prMint(true, false, false, "rw-closed"), nil, false},
			start{"fork PR, rbe-fork canary refuses, " + v, prMint(true, false, false, "canary"), nil, false},
			start{"Dependabot PR, rbe-fork ro, " + v, prMint(false, true, false, "ro"), nil, false},
			start{"fork PR that somehow has the secrets, rbe-fork ro, " + v, prMint(true, false, true, "ro"), nil, true},
			start{"fork PR, rbe-fork ro, dispatch-style rbe=cache, " + v, prMint(true, false, false, "ro"), map[string]string{"rbe": "cache"}, false},
		)
	}

	seen := map[string]bool{}
	for _, s := range starts {
		t.Run(s.name, func(t *testing.T) {
			out, err := runDecisionStep(t, decide, s.f, s.with)
			if err != nil {
				t.Fatal(err)
			}
			mode, enabled, tier := out["mode"], out["enabled"], out["tier"]
			seen[mode] = true
			// Fork and Dependabot pull_request runs: fork-<tier> when the
			// mint says open with tier ro or rw, else cache, whatever else
			// holds (rbe=off: local). Other forks outside bazel-farm.yml's
			// authorized call, and rbe=cache, are mode cache.
			authorized := s.f.event == "pull_request_target" && s.with["fork-farm"] == "authorized"
			forkPR := s.f.event == "pull_request" && (s.f.fork || s.f.dependabot)
			want := ""
			switch {
			case s.with["rbe"] == "off":
				want = "local"
			case s.with["rbe"] == "cache":
				want = "cache"
			case forkPR && (s.f.mint == "ro" || s.f.mint == "rw"):
				want = "fork-" + s.f.mint
			case forkPR, s.f.fork && !authorized:
				want = "cache"
			}
			if want != "" && mode != want {
				t.Errorf("mode = %s, want %s", mode, want)
			}
			if wantTier := strings.TrimPrefix(want, "fork-"); strings.HasPrefix(want, "fork-") && tier != wantTier || !strings.HasPrefix(mode, "fork-") && tier != "" {
				t.Errorf("mode %s with tier %q", mode, tier)
			}
			if mode == "remote" && (!s.secret || s.f.fork && !authorized || forkPR) {
				t.Errorf("mode remote without the secret, for an unauthorized fork, or for a fork or Dependabot pull_request")
			}

			var lanes []string
			for name, job := range workflow.Jobs {
				if name == bazelRBEJobName {
					continue
				}
				runs := bazelLaneRunModes(t, name, job.If, s.with)[mode]
				switch {
				case strings.HasPrefix(mode, "fork-") && !runs && !bazelPackageJobs[name]:
					t.Errorf("%s does not run in mode %s (every lane runs remotely)", name, mode)
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
						env[k] = evalSetup(t, expr, mode, enabled, tier, s.secret)
					}
					runnerTemp := t.TempDir()
					env = setupBazelForkSim(t, mint, runnerTemp, name, env, tier)
					outputs, rc, files, logs, err := writeRC(t, runnerTemp, env)
					if err != nil {
						t.Fatalf("%s: write-bazelrc.sh failed in mode %s: %v\n%s", name, mode, err, logs)
					}
					checkModeRC(t, name, mode, outputs, rc, files, logs)
					if strings.HasPrefix(mode, "fork-") {
						secretDir := filepath.Join(runnerTemp, "bazel-ci-secret")
						for _, want := range []string{
							"\nbuild:remote-exec --tls_client_certificate=" + filepath.Join(secretDir, "fork.crt") + "\n",
							"\nbuild:remote-exec --tls_client_key=" + filepath.Join(secretDir, "fork.key") + "\n",
						} {
							if !strings.Contains(rc, want) {
								t.Errorf("%s (mode %s): rc lacks the minted certificate %q:\n%s", name, mode, strings.TrimSpace(want), rc)
							}
						}
					}
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
	case "fork-ro", "fork-rw":
		// rbe-fork with the minted certificate: the mint's endpoint and
		// instance, no upload from the runner, never the CI secrets (their
		// endpoint is farm.invalid here) or the fork cache.
		instance := rbeForkInstance[strings.TrimPrefix(mode, "fork-")]
		for _, want := range []string{
			"\nbuild:remote-exec --remote_executor=" + rbeForkEndpoint + "\n",
			"\nbuild:remote-exec --remote_instance_name=" + instance + "\n",
			"\nbuild:remote-exec --noremote_upload_local_results\n",
			"\nbuild --config=remote-exec\n",
		} {
			if !has(want) {
				t.Errorf("%s (mode %s): rc lacks %q:\n%s", lane, mode, strings.TrimSpace(want), rc)
			}
		}
		for _, bad := range []string{"fork-cache", "farm.invalid", "client.crt", "client.key", "--tls_certificate", "--remote_upload_local_results", "--remote_cache", "--remote_header", "--bes_"} {
			if has(bad) {
				t.Errorf("%s (mode %s): rc carries %q:\n%s", lane, mode, bad, rc)
			}
		}
		sort.Strings(files)
		if strings.Join(files, " ") != "ci.bazelrc fork.crt fork.key mint.json" {
			t.Errorf("%s (mode %s): secret dir holds %v, want the rc, the minted certificate, its key and the mint's reply", lane, mode, files)
		}
		if !strings.Contains(outputs, "remote=true") || !strings.Contains(outputs, "cache=false") || strings.Contains(logs, "::add-mask::") {
			t.Errorf("%s (mode %s): outputs %q logs %q; want remote=true, cache=false and nothing masked (no secret seen)", lane, mode, outputs, logs)
		}
	default:
		t.Errorf("%s runs in mode %q", lane, mode)
	}
}
