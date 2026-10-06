package scripts_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	// simulation cannot drift from the workflow; in particular no repository
	// variable (fork pull_request runs see none).
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
			// Mode cache's zstd probe: a refused port, never rbe-cache.
			"RBE_CACHE_PROBE_URL=" + refusedProbeURL,
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
				// rbe-prewarm never runs in any fork mode (B1, security
				// review of bdef342d5: gated on mode remote only). A fork or
				// Dependabot pull_request run never carries a
				// workflow_call secret regardless of mode, and
				// bazel-farm.yml (the other path to a privileged fork tier)
				// no longer forwards the app secrets either, so there is no
				// audience left for pre-warming in fork-ro or fork-rw (see
				// bazel.yml's comment on the job).
				case strings.HasPrefix(mode, "fork-") && !runs && !bazelPackageJobs[name] && !(name == bazelRBEPrewarmJobName && strings.HasPrefix(mode, "fork-")):
					t.Errorf("%s does not run in mode %s (every lane runs remotely)", name, mode)
				case mode == "cache" && (name == bazelIntegJobName || name == bazelCmdDoltJobName) && !runs:
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
			// Flag-gated lanes' skips are accepted whatever the flag says.
			for lane, g := range bazelFlagGatedLanes {
				if !ran[lane] {
					wantSkips = append(wantSkips, g.id)
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
	// Every mode: the fetch hardening (key neutral, repository fetching only)
	// and the client heap (R4: a startup option, key neutral).
	for _, want := range []string{
		"\ncommon --repo_env=GOPROXY=https://proxy.golang.org|https://proxy.golang.org|direct\n",
		"\ncommon --http_timeout_scaling=2.0\n",
		"\nstartup --host_jvm_args=-Xmx4g\n",
	} {
		if !has(want) {
			t.Errorf("%s (mode %s): rc lacks %q:\n%s", lane, mode, strings.TrimSpace(want), rc)
		}
	}
	// zstd: no mode here asks for compression. Mode cache's probe is
	// refused here (TestSetupBazelRCWriter runs it passing); the trusted
	// schedulers and rbe-fork advertise none, and Bazel refuses such a
	// remote.
	if has("remote_cache_compression") {
		t.Errorf("%s (mode %s): rc asks for compression:\n%s", lane, mode, rc)
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

// cacheZstdProbe: setup-bazel's zstd probe for the fork cache (a byte copy
// of gascity's tools/rbe/cache-zstd-probe.sh).
const cacheZstdProbe = setupBazelActionDir + "/cache-zstd-probe.sh"

// refusedProbeURL: a loopback port nothing listens on, the probe's URL in
// tests that do not serve one (a refused connection: no flag).
const refusedProbeURL = "https://127.0.0.1:1"

// GetCapabilities bodies for a stand-in rbe-cache. capsLive is what rbe-cache
// answered on 2026-10-04, before it advertised zstd (cache_capabilities:
// SHA256 and BLAKE3, action cache read-only, 64 MiB batches, symlinks
// allowed; API 2.0 to 2.3); capsZstd is the same with supported_compressors
// [ZSTD].
var (
	capsLiveCache       = []byte{0x0a, 0x02, 0x01, 0x09, 0x12, 0x02, 0x08, 0x01, 0x20, 0x80, 0x80, 0x04, 0x28, 0x01}
	capsLiveAPIVersions = []byte{0x22, 0x02, 0x08, 0x02, 0x2a, 0x04, 0x08, 0x02, 0x10, 0x03}
	capsLive            = capsWithCache(capsLiveCache)
	capsZstd            = capsWithCache(pbBytes(6, []byte{1}), capsLiveCache)
	// GetCapabilitiesRequest{instance_name: "oss"}, fork-cache's instance.
	capsRequest = grpcMessage(pbBytes(1, []byte("oss")))
)

// capsWithCache: a ServerCapabilities with the live API versions and a
// cache_capabilities of the given fields.
func capsWithCache(cache ...[]byte) []byte {
	return append(pbBytes(1, bytes.Join(cache, nil)), capsLiveAPIVersions...)
}

func pbBytes(field int, b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field)<<3|2)
	return append(binary.AppendUvarint(out, uint64(len(b))), b...)
}

func pbVarint(field int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field)<<3), v)
}

// grpcMessage frames m as one uncompressed gRPC message.
func grpcMessage(m []byte) []byte {
	return append(binary.BigEndian.AppendUint32([]byte{0}, uint32(len(m))), m...)
}

// capsAnswer is how the stand-in rbe-cache answers GetCapabilities: HTTP
// status (0: 200), body, and grpc-status ("": none; with no body, in the
// headers, as gRPC's trailers-only errors are), after delay.
type capsAnswer struct {
	status int
	grpc   string
	body   []byte
	delay  time.Duration
}

// serveCapabilities answers a like rbe-cache's Caddy, over TLS and HTTP/2,
// after checking the request is the probe's GetCapabilities for instance
// oss. It returns the probe's RBE_CACHE_PROBE_URL, a CA file for curl's
// CURL_CA_BUNDLE, and the request count.
func serveCapabilities(t *testing.T, a capsAnswer) (url, caFile string, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		const path = "/build.bazel.remote.execution.v2.Capabilities/GetCapabilities"
		if r.ProtoMajor != 2 || r.Method != http.MethodPost || r.URL.Path != path ||
			r.Header.Get("Content-Type") != "application/grpc" || !bytes.Equal(body, capsRequest) {
			t.Errorf("probe sent %s %s %s, content-type %q, body %x; want HTTP/2 POST %s, application/grpc, %x",
				r.Proto, r.Method, r.URL.Path, r.Header.Get("Content-Type"), body, path, capsRequest)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case <-time.After(a.delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		trailer := a.grpc != "" && a.body != nil
		if trailer {
			w.Header().Set("Trailer", "Grpc-Status")
		} else if a.grpc != "" {
			w.Header().Set("Grpc-Status", a.grpc)
		}
		status := a.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(a.body)
		// Streamed, as gRPC servers answer: Go drops the trailer of a
		// response it can give a content-length.
		w.(http.Flusher).Flush()
		if trailer {
			w.Header().Set("Grpc-Status", a.grpc)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv.URL, caFile, hits
}

// requireCacheZstdProbeTools: the probe needs curl and python3 (without
// them it leaves the flag off, which the refused-probe tests cover).
func requireCacheZstdProbeTools(t *testing.T) {
	t.Helper()
	requireHostTool(t, "curl")
	requireHostTool(t, "python3")
}

// TestCacheZstdProbe runs cache-zstd-probe.sh against a stand-in rbe-cache.
// Only an answer listing ZSTD in cache_capabilities.supported_compressors
// (the field Bazel checks) passes; every other answer, error, refusal or
// timeout fails, and write-bazelrc.sh then writes no flag. The probe never
// writes stdout (write-bazelrc.sh appends its stdout to the rc) and always
// says why on stderr.
func TestCacheZstdProbe(t *testing.T) {
	bash := requireHostTool(t, "bash")
	requireCacheZstdProbeTools(t)
	root := sourceRepoRoot(t)
	script := filepath.Join(root, cacheZstdProbe)

	// The probe asks what fork-cache uses: rbe-cache, instance oss.
	probeText := readPolicyFile(t, root, cacheZstdProbe)
	for _, want := range []string{
		"url=${RBE_CACHE_PROBE_URL:-https://rbe-cache.ops.gascity.com:8443}\n",
		`printf '\000\000\000\000\005\012\003oss'`,
		"--connect-timeout 3 --max-time \"$max_time\"",
		"max_time=${RBE_CACHE_PROBE_MAX_TIME:-5}\n",
	} {
		if !strings.Contains(probeText, want) {
			t.Errorf("%s lacks %q", cacheZstdProbe, want)
		}
	}
	if !bytes.Equal(capsRequest, []byte("\x00\x00\x00\x00\x05\x0a\x03oss")) {
		t.Fatalf("capsRequest = %x", capsRequest)
	}
	bazelrc := readPolicyFile(t, root, ".bazelrc")
	for _, want := range []string{"\nbuild:fork-cache --remote_cache=" + forkCacheEndpoint + "\n", "\nbuild:fork-cache --remote_instance_name=" + forkCacheInstance + "\n"} {
		if !strings.Contains(bazelrc, want) {
			t.Errorf(".bazelrc lacks %q, which the probe asks", strings.TrimSpace(want))
		}
	}
	if forkCacheEndpoint != "grpcs://rbe-cache.ops.gascity.com:8443" || forkCacheInstance != "oss" {
		t.Errorf("fork-cache is %s instance %s; the probe asks rbe-cache.ops.gascity.com:8443 instance oss", forkCacheEndpoint, forkCacheInstance)
	}

	zstd := grpcMessage(capsZstd)
	cases := []struct {
		name   string
		answer capsAnswer
		want   bool
	}{
		{"rbe-cache before zstd", capsAnswer{grpc: "0", body: grpcMessage(capsLive)}, false},
		{"zstd advertised", capsAnswer{grpc: "0", body: zstd}, true},
		{"zstd among others, unpacked", capsAnswer{grpc: "0", body: grpcMessage(capsWithCache(capsLiveCache, pbVarint(6, 2), pbVarint(6, 1)))}, true},
		{"deflate only", capsAnswer{grpc: "0", body: grpcMessage(capsWithCache(capsLiveCache, pbBytes(6, []byte{2})))}, false},
		{"zstd for batch updates only", capsAnswer{grpc: "0", body: grpcMessage(capsWithCache(capsLiveCache, pbBytes(7, []byte{1})))}, false},
		{"zstd outside cache_capabilities", capsAnswer{grpc: "0", body: grpcMessage(append(capsLive, pbBytes(2, pbBytes(6, []byte{1}))...))}, false},
		{"gRPC error after the answer", capsAnswer{grpc: "13", body: zstd}, false},
		{"no grpc-status", capsAnswer{body: zstd}, false},
		{"trailers-only UNIMPLEMENTED", capsAnswer{grpc: "12"}, false},
		{"HTTP 502", capsAnswer{status: http.StatusBadGateway, grpc: "0", body: zstd}, false},
		{"compressed message", capsAnswer{grpc: "0", body: append([]byte{1}, zstd[1:]...)}, false},
		{"truncated message", capsAnswer{grpc: "0", body: zstd[:len(zstd)-1]}, false},
		{"truncated field", capsAnswer{grpc: "0", body: grpcMessage(capsZstd[:4])}, false},
		{"not gRPC", capsAnswer{grpc: "0", body: []byte("<html>bad gateway</html>")}, false},
		{"timeout", capsAnswer{grpc: "0", body: zstd, delay: 10 * time.Second}, false},
	}
	run := func(t *testing.T, env ...string) (bool, string, string) {
		t.Helper()
		cmd := exec.Command(bash, script)
		cmd.Dir = t.TempDir()
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "RBE_CACHE_PROBE_MAX_TIME=1"}, env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatalf("run %s: %v", cacheZstdProbe, err)
		}
		return err == nil, stdout.String(), stderr.String()
	}
	check := func(t *testing.T, ok bool, stdout, stderr string, want bool) {
		t.Helper()
		if ok != want {
			t.Errorf("probe passed: %v, want %v; stderr:\n%s", ok, want, stderr)
		}
		if stdout != "" {
			t.Errorf("probe wrote stdout %q; write-bazelrc.sh would append it to the rc", stdout)
		}
		verdict := "; the fork cache stays identity\n"
		if want {
			verdict = "; the fork cache uses zstd\n"
		}
		if !strings.HasPrefix(stderr, "rbe-cache zstd probe: ") || !strings.HasSuffix(stderr, verdict) {
			t.Errorf("probe stderr %q; want one verdict line ending %q", stderr, verdict)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, ca, hits := serveCapabilities(t, c.answer)
			start := time.Now()
			ok, stdout, stderr := run(t, "RBE_CACHE_PROBE_URL="+url, "CURL_CA_BUNDLE="+ca)
			check(t, ok, stdout, stderr, c.want)
			if hits.Load() != 1 {
				t.Errorf("probe asked %d times, want 1", hits.Load())
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("probe took %v; RBE_CACHE_PROBE_MAX_TIME=1 bounds it", d)
			}
		})
	}
	t.Run("refused", func(t *testing.T) {
		ok, stdout, stderr := run(t, "RBE_CACHE_PROBE_URL="+refusedProbeURL)
		check(t, ok, stdout, stderr, false)
	})
	t.Run("untrusted certificate", func(t *testing.T) {
		url, _, hits := serveCapabilities(t, capsAnswer{grpc: "0", body: zstd})
		ok, stdout, stderr := run(t, "RBE_CACHE_PROBE_URL="+url)
		check(t, ok, stdout, stderr, false)
		if hits.Load() != 0 {
			t.Errorf("probe completed a request through an untrusted certificate")
		}
	})
}
